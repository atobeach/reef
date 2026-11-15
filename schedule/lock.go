package schedule

import (
	"context"
	"sync"
	"time"

	"github.com/atobeach/reef/cache"
)

// Locker hands out named, non-blocking, cross-process locks.
type Locker interface {
	// TryLock takes the lock for at most ttl if it is free; ok is false when
	// someone else holds it. release frees it only if this caller still owns it.
	TryLock(ctx context.Context, name string, ttl time.Duration) (release func(), ok bool, err error)
}

// CacheLocker locks with the cache store's owner-tagged locks (cache.NewLock),
// so a crashed holder's lock lapses with its TTL, not with a connection.
type CacheLocker struct {
	Store cache.Store
}

// TryLock implements Locker. Releasing twice is harmless.
func (l CacheLocker) TryLock(ctx context.Context, name string, ttl time.Duration) (release func(), ok bool, err error) {
	lock := cache.NewLock(l.Store, name, ttl)

	ok, err = lock.Get(ctx)
	if err != nil || !ok {
		return nil, false, err
	}

	var once sync.Once

	return func() {
		once.Do(func() {
			// Detached: a run cancelled by SIGTERM must still release the
			// lock rather than leave it to its TTL.
			_ = lock.Release(context.WithoutCancel(ctx))
		})
	}, true, nil
}

// NewMemoryLocker is a CacheLocker over an in-process memory store, for tests
// and single-process use.
func NewMemoryLocker() CacheLocker {
	return CacheLocker{Store: cache.NewMemoryStore()}
}
