package ratelimit

import (
	"testing"
	"time"

	"github.com/atobeach/reef/clock/clocktest"
)

// The memory store's windows run on the caller's clock: a test travels past
// one instead of waiting it out.
func TestMemoryStoreWindowsRunOnTheCallersClock(t *testing.T) {
	store := NewMemoryStore()
	ctx, now := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))

	for want := int64(1); want <= 3; want++ {
		if hits, err := store.Hit(ctx, "bucket", time.Minute); err != nil || hits != want {
			t.Fatalf("hit %d = %d, %v", want, hits, err)
		}
	}

	now.Travel(20 * time.Second)

	if hits, resetIn, err := store.Attempts(ctx, "bucket"); err != nil || hits != 3 || resetIn != 40*time.Second {
		t.Fatalf("attempts = %d, resets in %s, %v", hits, resetIn, err)
	}

	now.Travel(40 * time.Second)

	if hits, resetIn, _ := store.Attempts(ctx, "bucket"); hits != 0 || resetIn != 0 {
		t.Fatalf("the window ended but holds %d hits, resetting in %s", hits, resetIn)
	}

	if hits, _ := store.Hit(ctx, "bucket", time.Minute); hits != 1 {
		t.Fatalf("a new window starts at 1, got %d", hits)
	}
}
