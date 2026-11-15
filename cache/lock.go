package cache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/atobeach/reef/support"
)

// lockPollInterval is how often Block retries a held lock.
const lockPollInterval = 250 * time.Millisecond

// LockTimeoutError is Block giving up waiting, a 500.
type LockTimeoutError struct {
	Name string
}

func (e *LockTimeoutError) Error() string {
	return fmt.Sprintf("cache: timed out waiting for lock %q", e.Name)
}

// Lock is a mutex over the cache: an owner-tagged key stored with Add, so
// one holder runs the guarded section; the TTL frees a dead holder's lock.
type Lock struct {
	store Store
	name  string
	ttl   time.Duration
	owner []byte
}

// NewLock builds a lock on name that frees itself after ttl.
func NewLock(store Store, name string, ttl time.Duration) *Lock {
	return &Lock{store: store, name: name, ttl: ttl, owner: []byte(support.RandomString(16))}
}

// Get tries once to acquire the lock.
func (l *Lock) Get(ctx context.Context) (bool, error) {
	return l.store.Add(ctx, l.name, l.owner, l.ttl)
}

// Release frees the lock when this owner still holds it, atomically, so a
// holder whose TTL lapsed never deletes the next holder's lock.
func (l *Lock) Release(ctx context.Context) error {
	_, err := l.store.ForgetIf(ctx, l.name, l.owner)

	return err
}

// Block waits up to wait for the lock, runs fn while holding it and releases
// it afterwards. Giving up is a *LockTimeoutError.
func (l *Lock) Block(ctx context.Context, wait time.Duration, fn func() error) error {
	started := time.Now()

	for {
		acquired, err := l.Get(ctx)
		if err != nil {
			return fmt.Errorf("cache: lock %q: %w", l.name, err)
		}

		if acquired {
			break
		}

		if time.Since(started)+lockPollInterval >= wait {
			return &LockTimeoutError{Name: l.name}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}

	// Released even when fn fails or panics; a failed release only leaves
	// the lock to its TTL.
	defer func() {
		if err := l.Release(context.WithoutCancel(ctx)); err != nil {
			slog.WarnContext(ctx, "cache: releasing lock", "lock", l.name, "error", err)
		}
	}()

	return fn()
}
