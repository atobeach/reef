package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/atobeach/reef/support"
)

func TestRedisStoreCountsHitsInAFixedWindow(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 15})
	if err := client.Ping(t.Context()).Err(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI must provide Valkey: %v", err)
		}
		t.Skipf("no Valkey on 127.0.0.1:6379: %v", err)
	}

	prefix := "ratelimit-test-" + support.RandomString(8) + "-"
	// Background, not t.Context(): that one is already cancelled by cleanup time.
	t.Cleanup(func() {
		keys, _ := client.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			client.Del(context.Background(), keys...)
		}
	})

	store := NewRedisStore(client, prefix)
	ctx := t.Context()

	if hits, resetIn, err := store.Attempts(ctx, "bucket"); err != nil || hits != 0 || resetIn != 0 {
		t.Fatalf("no window yet: %d, %s, %v", hits, resetIn, err)
	}

	for want := int64(1); want <= 3; want++ {
		if hits, err := store.Hit(ctx, "bucket", time.Minute); err != nil || hits != want {
			t.Fatalf("hit %d = %d, %v", want, hits, err)
		}
	}

	hits, resetIn, err := store.Attempts(ctx, "bucket")
	if err != nil || hits != 3 || resetIn < 58*time.Second || resetIn > time.Minute {
		t.Fatalf("attempts = %d, resets in %s, %v", hits, resetIn, err)
	}

	// The window is fixed from the first hit: a short one ends and the
	// counter starts over.
	if _, err := store.Hit(ctx, "short", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	if hits, _, _ := store.Attempts(ctx, "short"); hits != 0 {
		t.Fatalf("expired window still holds %d hits", hits)
	}

	if hits, _ := store.Hit(ctx, "short", time.Minute); hits != 1 {
		t.Fatalf("a new window starts at 1, got %d", hits)
	}
}
