package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/atobeach/reef/clock/clocktest"
)

func TestRememberComputesOnceThenServesTheCache(t *testing.T) {
	store := NewMemoryStore()
	calls := 0
	compute := func(context.Context) ([]string, error) {
		calls++

		return []string{"portugal"}, nil
	}

	for range 3 {
		value, err := Remember(t.Context(), store, "k", time.Hour, compute)
		if err != nil || len(value) != 1 || value[0] != "portugal" {
			t.Fatalf("value = %v, err = %v", value, err)
		}
	}

	if calls != 1 {
		t.Fatalf("computed %d times, want 1", calls)
	}
}

func TestRememberNeverCachesErrors(t *testing.T) {
	store := NewMemoryStore()
	missing := errors.New("not found")

	if _, err := Remember(t.Context(), store, "k", time.Hour, func(context.Context) (int, error) { return 0, missing }); !errors.Is(err, missing) {
		t.Fatalf("err = %v", err)
	}

	value, err := Remember(t.Context(), store, "k", time.Hour, func(context.Context) (int, error) { return 7, nil })
	if err != nil || value != 7 {
		t.Fatalf("value = %d, err = %v", value, err)
	}
}

func TestRememberRecomputesAfterTheTTL(t *testing.T) {
	store := NewMemoryStore()
	ctx, now := clocktest.FrozenAt(t.Context(), time.Now())

	_, _ = Remember(ctx, store, "k", time.Minute, func(context.Context) (int, error) { return 1, nil })
	now.Travel(61 * time.Second)

	value, _ := Remember(ctx, store, "k", time.Minute, func(context.Context) (int, error) { return 2, nil })
	if value != 2 {
		t.Fatalf("value = %d, want a recompute", value)
	}
}

func TestFlexibleServesStaleWhileOneCallerRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewMemoryStore()
		var calls atomic.Int32
		release := make(chan struct{})

		compute := func(context.Context) (int32, error) {
			n := calls.Add(1)
			if n == 2 {
				// Hold the refresh until every stale read below has happened.
				<-release
			}

			return n, nil
		}

		ctx, now := clocktest.FrozenAt(t.Context(), time.Now())
		first, _ := Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute)

		// Past fresh but within stale.
		now.Travel(2 * time.Hour)

		// Many readers of a stale entry: all get the stale value, one refreshes.
		for range 5 {
			if value, _ := Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute); value != first {
				t.Fatalf("stale read = %d, want %d", value, first)
			}
		}

		close(release)
		// The refresh has landed once nothing else in the bubble can run.
		synctest.Wait()

		if value, _ := Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute); value != 2 {
			t.Fatalf("after the refresh = %d, want 2", value)
		}

		if calls.Load() != 2 {
			t.Fatalf("computed %d times, want 2 (initial + one refresh)", calls.Load())
		}
	})
}

// readBefore answers key's first Get with bytes read earlier: a caller whose
// read raced a refresh.
type readBefore struct {
	Store
	key  string
	raw  []byte
	used bool
}

func (s *readBefore) Get(ctx context.Context, key string) ([]byte, error) {
	if key == s.key && !s.used {
		s.used = true

		return s.raw, nil
	}

	return s.Store.Get(ctx, key)
}

func TestAReadThatRacedARefreshDoesNotRecompute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewMemoryStore()
		var calls atomic.Int32
		compute := func(context.Context) (int32, error) { return calls.Add(1), nil }

		ctx, now := clocktest.FrozenAt(t.Context(), time.Now())
		_, _ = Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute)
		now.Travel(2 * time.Hour)

		// A late caller reads the stale entry; then a refresh lands and frees its lock.
		stale, err := store.Get(ctx, "k")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute)
		synctest.Wait()

		// The late caller serves what it read and wins the freed lock.
		late := &readBefore{Store: store, key: "k", raw: stale}
		if value, _ := Flexible(ctx, late, "k", time.Hour, 4*time.Hour, compute); value != 1 {
			t.Fatalf("late read = %d, want the stale 1 it read", value)
		}
		synctest.Wait()

		if calls.Load() != 2 {
			t.Fatalf("computed %d times, want 2: a refreshed entry was refreshed again", calls.Load())
		}
		if value, _ := Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute); value != 2 {
			t.Fatalf("served %d, want the one refresh's 2", value)
		}
	})
}

func TestARefreshLeavesAnEntryForgottenUnderItForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewMemoryStore()
		var calls atomic.Int32
		compute := func(context.Context) (int32, error) { return calls.Add(1), nil }

		ctx, now := clocktest.FrozenAt(t.Context(), time.Now())
		_, _ = Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute)
		now.Travel(2 * time.Hour)

		// A late caller reads the stale entry; then the entry is forgotten.
		stale, err := store.Get(ctx, "k")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Forget(ctx, "k"); err != nil {
			t.Fatal(err)
		}

		late := &readBefore{Store: store, key: "k", raw: stale}
		if value, _ := Flexible(ctx, late, "k", time.Hour, 4*time.Hour, compute); value != 1 {
			t.Fatalf("late read = %d, want the stale 1 it read", value)
		}
		synctest.Wait()

		if calls.Load() != 1 {
			t.Fatalf("computed %d times, want 1: a forgotten entry was refreshed", calls.Load())
		}
		if _, err := store.Get(ctx, "k"); !errors.Is(err, ErrMiss) {
			t.Fatalf("err = %v, want a miss: the refresh brought a forgotten entry back", err)
		}
	})
}

// Nothing waits on a background refresh, so one that panics is logged
// there: the stale value is still served, and its lock goes, so the next
// stale reader refreshes again.
func TestABackgroundRefreshThatPanicsIsLoggedAndTheStaleValueStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(previous) })

		store := NewMemoryStore()
		ctx, now := clocktest.FrozenAt(t.Context(), time.Now())
		if _, err := Flexible(ctx, store, "k", time.Hour, 4*time.Hour, func(context.Context) (int, error) { return 1, nil }); err != nil {
			t.Fatal(err)
		}

		// Past fresh but within stale.
		now.Travel(2 * time.Hour)

		value, err := Flexible(ctx, store, "k", time.Hour, 4*time.Hour, func(context.Context) (int, error) { panic("nil map") })
		if value != 1 || err != nil {
			t.Fatalf("stale read = %d, %v; want the stale 1", value, err)
		}
		synctest.Wait()

		refreshed := false
		_, _ = Flexible(ctx, store, "k", time.Hour, 4*time.Hour, func(context.Context) (int, error) {
			refreshed = true

			return 2, nil
		})
		synctest.Wait()
		if !refreshed {
			t.Fatal("the panicked refresh kept its lock: no later reader refreshed")
		}

		var line struct {
			Msg   string `json:"msg"`
			Key   string `json:"key"`
			Error string `json:"error"`
			Stack string `json:"stack"`
		}
		if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
			t.Fatalf("logs = %s (%v)", logs.String(), err)
		}
		if line.Msg != "cache: background refresh panicked" || line.Key != "k" || line.Error != "nil map" || !strings.Contains(line.Stack, "cache.TestABackgroundRefreshThatPanicsIsLoggedAndTheStaleValueStays") {
			t.Fatalf("logged %+v", line)
		}
	})
}
