// Package cache is a byte-level Store (Valkey, or memory in tests) plus
// typed helpers that JSON-encode values, so a cached payload is plain data.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/panics"
)

// ErrMiss is returned by Store.Get for an absent or expired key.
var ErrMiss = errors.New("cache: miss")

// Store is the minimal surface every driver implements.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Add stores only when the key is absent and reports whether it did:
	// the primitive behind locks.
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	Forget(ctx context.Context, key string) error
	// ForgetIf atomically deletes the key while it still holds value: the
	// compare-and-delete a lock's owner releases with.
	ForgetIf(ctx context.Context, key string, value []byte) (bool, error)
}

// Get decodes a cached value; ok is false on a miss.
func Get[T any](ctx context.Context, store Store, key string) (value T, ok bool, err error) {
	raw, err := store.Get(ctx, key)
	if errors.Is(err, ErrMiss) {
		return value, false, nil
	}

	if err != nil {
		return value, false, err
	}

	if err := json.Unmarshal(raw, &value); err != nil {
		return value, false, fmt.Errorf("cache: decoding %q: %w", key, err)
	}

	return value, true, nil
}

// Put encodes and stores a value.
func Put[T any](ctx context.Context, store Store, key string, value T, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: encoding %q: %w", key, err)
	}

	return store.Put(ctx, key, raw, ttl)
}

// Forever stores a value with no expiry.
func Forever[T any](ctx context.Context, store Store, key string, value T) error {
	return Put(ctx, store, key, value, 0)
}

// Remember returns the cached value, or computes, stores and returns it. A
// compute error is never cached, and a cache outage never fails the caller:
// a failed read is a miss and a failed write is logged.
func Remember[T any](ctx context.Context, store Store, key string, ttl time.Duration, compute func(ctx context.Context) (T, error)) (T, error) {
	if value, ok, err := Get[T](ctx, store, key); err == nil && ok {
		return value, nil
	}

	value, err := compute(ctx)
	if err != nil {
		return value, err
	}

	if err := Put(ctx, store, key, value, ttl); err != nil {
		slog.WarnContext(ctx, "cache: put failed", "key", key, "error", err)
	}

	return value, nil
}

// flexibleEntry stores the value with when it was computed, so a reader
// tells fresh from stale without a second key.
type flexibleEntry[T any] struct {
	Value      T     `json:"value"`
	ComputedAt int64 `json:"computed_at"`
}

// Flexible is stale-while-revalidate: a value older than fresh is still
// served while one locked caller recomputes it in the background, and past
// stale it is recomputed inline, so a hot key never stampedes. As in
// Remember, a cache failure is a miss. The background refresh keeps ctx's
// values, its db connection included: inside a transaction (every
// rolled-back test) its goroutine shares it.
func Flexible[T any](ctx context.Context, store Store, key string, fresh time.Duration, stale time.Duration, compute func(ctx context.Context) (T, error)) (T, error) {
	entry, ok, err := Get[flexibleEntry[T]](ctx, store, key)
	if err == nil && ok {
		age := clock.Now(ctx).Sub(time.UnixMilli(entry.ComputedAt))
		if age >= fresh {
			refreshInBackground(ctx, store, key, entry.ComputedAt, stale, compute)
		}

		return entry.Value, nil
	}

	value, err := compute(ctx)
	if err != nil {
		return value, err
	}

	putFlexible(ctx, store, key, value, stale)

	return value, nil
}

// refreshInBackground recomputes the entry computed at seen, once, under the
// refresh lock.
func refreshInBackground[T any](ctx context.Context, store Store, key string, seen int64, stale time.Duration, compute func(ctx context.Context) (T, error)) {
	lock := key + ":refresh-lock"

	// The request may finish first, so the refresh keeps its values (its db
	// connection) without its cancellation. compute must query through the
	// ctx it is handed, never one captured from the request.
	background := context.WithoutCancel(ctx)

	// No lock, no refresh: the next reader past fresh tries again.
	acquired, err := store.Add(background, lock, []byte("1"), 30*time.Second)
	if err != nil || !acquired {
		return
	}

	go func() {
		// A lock that isn't released expires with its 30s TTL.
		defer func() { _ = store.Forget(background, lock) }()
		// Nothing waits on this goroutine, so a panic is logged here and the
		// stale value stays; the lock still goes, so the next reader retries.
		defer func() {
			if recovered := recover(); recovered != nil {
				p := panics.New(recovered)
				slog.ErrorContext(background, "cache: background refresh panicked", "key", key, "error", p, "stack", p.Stack())
			}
		}()

		// Reading the entry and taking the lock are two steps: a caller that
		// read it stale just before another refresh landed can win the freed
		// lock. Only the entry it read is refreshed.
		if current, ok, err := Get[flexibleEntry[T]](background, store, key); err != nil || !ok || current.ComputedAt != seen {
			return
		}

		value, err := compute(background)
		if err != nil {
			slog.WarnContext(background, "cache: background refresh failed", "key", key, "error", err)

			return
		}

		putFlexible(background, store, key, value, stale)
	}()
}

func putFlexible[T any](ctx context.Context, store Store, key string, value T, stale time.Duration) {
	entry := flexibleEntry[T]{Value: value, ComputedAt: clock.Now(ctx).UnixMilli()}
	if err := Put(ctx, store, key, entry, stale); err != nil {
		slog.WarnContext(ctx, "cache: put failed", "key", key, "error", err)
	}
}
