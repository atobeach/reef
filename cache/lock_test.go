package cache_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/atobeach/reef/cache"
	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/testkit"
)

// lockStores are the lock contract's drivers: memory (the test default) and
// a real Valkey (production). Each opens two independent handles on one
// store, standing in for two servers.
var lockStores = []struct {
	name string
	open func(t *testing.T) (a, b cache.Store)
}{
	{"memory", func(t *testing.T) (a, b cache.Store) {
		t.Helper()

		store := cache.NewMemoryStore()

		return store, store
	}},
	{"valkey", func(t *testing.T) (a, b cache.Store) {
		t.Helper()

		client, prefix := testkit.Valkey(t)

		return cache.NewRedisStore(client, prefix), cache.NewRedisStore(testkit.ValkeyClient(t), prefix)
	}},
}

func TestOneOwnerHoldsALockAcrossStoresAndOnlyItReleases(t *testing.T) {
	for _, driver := range lockStores {
		t.Run(driver.name, func(t *testing.T) {
			a, b := driver.open(t)

			ctx := t.Context()

			holder := cache.NewLock(a, "leader", time.Minute)
			rival := cache.NewLock(b, "leader", time.Minute)

			if ok, err := holder.Get(ctx); !ok || err != nil {
				t.Fatalf("first get: %v %v", ok, err)
			}

			if ok, err := rival.Get(ctx); ok || err != nil {
				t.Fatalf("a held lock was granted twice: %v %v", ok, err)
			}

			// The rival doesn't own it: its release is a no-op.
			if err := rival.Release(ctx); err != nil {
				t.Fatal(err)
			}

			if ok, _ := cache.NewLock(b, "leader", time.Minute).Get(ctx); ok {
				t.Fatal("a non-owner's release freed the lock")
			}

			if err := holder.Release(ctx); err != nil {
				t.Fatal(err)
			}

			if ok, _ := rival.Get(ctx); !ok {
				t.Fatal("the owner's release must free the lock")
			}

			// The old owner releasing again must not free the new owner's lock.
			if err := holder.Release(ctx); err != nil {
				t.Fatal(err)
			}

			if ok, _ := cache.NewLock(a, "leader", time.Minute).Get(ctx); ok {
				t.Fatal("a stale owner's second release freed the next holder's lock")
			}
		})
	}
}

func TestForgetIfDeletesOnlyTheMatchingValue(t *testing.T) {
	for _, driver := range lockStores {
		t.Run(driver.name, func(t *testing.T) {
			a, _ := driver.open(t)

			ctx := t.Context()

			if err := a.Put(ctx, "k", []byte("mine"), time.Minute); err != nil {
				t.Fatal(err)
			}

			if deleted, err := a.ForgetIf(ctx, "k", []byte("theirs")); deleted || err != nil {
				t.Fatalf("deleted another value: %v %v", deleted, err)
			}

			if deleted, err := a.ForgetIf(ctx, "k", []byte("mine")); !deleted || err != nil {
				t.Fatalf("kept its own value: %v %v", deleted, err)
			}

			if _, err := a.Get(ctx, "k"); !errors.Is(err, cache.ErrMiss) {
				t.Fatalf("still there: %v", err)
			}

			if deleted, err := a.ForgetIf(ctx, "absent", []byte("x")); deleted || err != nil {
				t.Fatalf("absent key: %v %v", deleted, err)
			}
		})
	}
}

// The TTL frees a lock whose holder died without releasing it.
func TestALockLapsesWithItsTTL(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		store := cache.NewMemoryStore()
		ctx, now := clocktest.FrozenAt(t.Context(), time.Now())
		if ok, _ := cache.NewLock(store, "job", time.Minute).Get(ctx); !ok {
			t.Fatal("get")
		}

		now.Travel(61 * time.Second)

		if ok, _ := cache.NewLock(store, "job", time.Minute).Get(ctx); !ok {
			t.Fatal("an expired lock must be free")
		}
	})

	t.Run("valkey", func(t *testing.T) {
		client, prefix := testkit.Valkey(t)
		store := cache.NewRedisStore(client, prefix)

		if ok, _ := cache.NewLock(store, "job", 24*time.Hour).Get(t.Context()); !ok {
			t.Fatal("get")
		}

		if ttl := client.TTL(t.Context(), prefix+"job").Val(); ttl < 23*time.Hour || ttl > 24*time.Hour {
			t.Fatalf("ttl = %s, want the lock's 24h", ttl)
		}

		if ok, _ := cache.NewLock(store, "short", 200*time.Millisecond).Get(t.Context()); !ok {
			t.Fatal("get short")
		}

		time.Sleep(400 * time.Millisecond)

		if ok, _ := cache.NewLock(store, "short", time.Minute).Get(t.Context()); !ok {
			t.Fatal("an expired lock must be free")
		}
	})
}

func TestALockRunsTheCallbackAndReleases(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryStore()

	ran := false
	if err := cache.NewLock(store, "job", 10*time.Second).Block(ctx, time.Second, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("ran = %v, err = %v", ran, err)
	}

	// Released: the next holder gets it at once.
	if ok, _ := cache.NewLock(store, "job", 10*time.Second).Get(ctx); !ok {
		t.Fatal("the lock must be released after the callback")
	}
}

func TestAHeldLockTimesOutAndIsNotStolen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		store := cache.NewMemoryStore()

		holder := cache.NewLock(store, "job", time.Minute)
		if ok, _ := holder.Get(ctx); !ok {
			t.Fatal("first get")
		}

		started := time.Now()
		err := cache.NewLock(store, "job", time.Minute).Block(ctx, 300*time.Millisecond, func() error {
			t.Fatal("the callback must not run while the lock is held")

			return nil
		})
		waited := time.Since(started)

		var timeout *cache.LockTimeoutError
		if !errors.As(err, &timeout) {
			t.Fatalf("err = %v", err)
		}

		// Block retries the held lock, and gives up within its wait.
		if waited <= 0 || waited > 300*time.Millisecond {
			t.Fatalf("waited %v, want a retry within the 300ms wait", waited)
		}

		// Another owner's release leaves the holder's lock alone.
		_ = cache.NewLock(store, "job", time.Minute).Release(ctx)
		if ok, _ := cache.NewLock(store, "job", time.Minute).Get(ctx); ok {
			t.Fatal("a non-owner released the lock")
		}

		_ = holder.Release(ctx)
		if ok, _ := cache.NewLock(store, "job", time.Minute).Get(ctx); !ok {
			t.Fatal("the owner's release frees it")
		}
	})
}

func TestALockIsReleasedWhenTheCallbackFails(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryStore()
	boom := errors.New("boom")

	if err := cache.NewLock(store, "job", time.Minute).Block(ctx, time.Second, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	if ok, _ := cache.NewLock(store, "job", time.Minute).Get(ctx); !ok {
		t.Fatal("a failing callback must still release")
	}
}
