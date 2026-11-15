// Package ratelimit counts attempts per key in fixed windows: the first hit
// opens a window of the limit's decay, every hit in it counts, and the
// counter vanishes when the window ends.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/atobeach/reef/clock"
)

// Limit allows MaxAttempts hits per Decay window in the bucket named Key.
type Limit struct {
	MaxAttempts int
	Decay       time.Duration
	Key         string
}

// PerMinute allows n hits per minute.
func PerMinute(n int) Limit {
	return Limit{MaxAttempts: n, Decay: time.Minute}
}

// By sets the bucket key (an IP, a session id, a user id).
func (l Limit) By(key string) Limit {
	l.Key = key

	return l
}

// Store keeps the window counters.
type Store interface {
	// Attempts is the hit count of key's open window and how long until it
	// resets; 0 and 0 when no window is open.
	Attempts(ctx context.Context, key string) (hits int64, resetIn time.Duration, err error)
	// Hit counts one attempt, opening a window of decay on the first.
	Hit(ctx context.Context, key string, decay time.Duration) (int64, error)
}

// hitScript increments and, on the first hit, starts the window in one
// round trip, so a crash between the two never leaves a counter that never
// resets.
var hitScript = redis.NewScript(`
local hits = redis.call('INCR', KEYS[1])
if redis.call('PTTL', KEYS[1]) < 0 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return hits
`)

// RedisStore keeps the counters in Valkey, so every server shares them.
type RedisStore struct {
	client *redis.Client
	prefix string
}

// NewRedisStore stores counters under prefix.
func NewRedisStore(client *redis.Client, prefix string) *RedisStore {
	return &RedisStore{client: client, prefix: prefix}
}

// Attempts reads the counter and its remaining lifetime together.
func (s *RedisStore) Attempts(ctx context.Context, key string) (int64, time.Duration, error) {
	var (
		hits *redis.StringCmd
		ttl  *redis.DurationCmd
	)

	_, err := s.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		hits = pipe.Get(ctx, s.prefix+key)
		ttl = pipe.PTTL(ctx, s.prefix+key)

		return nil
	})
	if errors.Is(err, redis.Nil) {
		return 0, 0, nil
	}

	if err != nil {
		return 0, 0, fmt.Errorf("ratelimit: reading %q: %w", key, err)
	}

	n, err := hits.Int64()
	if err != nil {
		return 0, 0, fmt.Errorf("ratelimit: reading %q: %w", key, err)
	}

	// A counter without an expiry must not block forever; the next hit gives
	// it one.
	if ttl.Val() <= 0 {
		return 0, 0, nil
	}

	return n, ttl.Val(), nil
}

// Hit counts one attempt.
func (s *RedisStore) Hit(ctx context.Context, key string, decay time.Duration) (int64, error) {
	hits, err := hitScript.Run(ctx, s.client, []string{s.prefix + key}, decay.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("ratelimit: hitting %q: %w", key, err)
	}

	return hits, nil
}

// MemoryStore keeps windows in process, for tests, on the caller's clock so
// a test can travel past them.
type MemoryStore struct {
	mu      sync.Mutex
	windows map[string]window
}

type window struct {
	hits    int64
	resetAt time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{windows: map[string]window{}}
}

func (s *MemoryStore) live(key string, now time.Time) (window, bool) {
	w, ok := s.windows[key]
	if !ok || !now.Before(w.resetAt) {
		return window{}, false
	}

	return w, true
}

// Attempts reads the open window.
func (s *MemoryStore) Attempts(ctx context.Context, key string) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := clock.Now(ctx)

	w, ok := s.live(key, now)
	if !ok {
		return 0, 0, nil
	}

	return w.hits, w.resetAt.Sub(now), nil
}

// Hit counts one attempt.
func (s *MemoryStore) Hit(ctx context.Context, key string, decay time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := clock.Now(ctx)

	w, ok := s.live(key, now)
	if !ok {
		w = window{resetAt: now.Add(decay)}
	}

	w.hits++
	s.windows[key] = w

	return w.hits, nil
}
