package cache

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/atobeach/reef/clock"
)

// RedisStore is the Valkey driver. Keys are prefixed so they never collide
// with other data in a shared Valkey.
type RedisStore struct {
	client *redis.Client
	prefix string
}

// NewRedisStore wraps a client with a key prefix.
func NewRedisStore(client *redis.Client, prefix string) *RedisStore {
	return &RedisStore{client: client, prefix: prefix}
}

// Get reads key; ErrMiss when absent.
func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := s.client.Get(ctx, s.prefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}

	return value, err
}

// Put stores value for ttl (zero: forever).
func (s *RedisStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, s.prefix+key, value, ttl).Err()
}

// Add stores value only when key is absent.
func (s *RedisStore) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	err := s.client.SetArgs(ctx, s.prefix+key, value, redis.SetArgs{Mode: "NX", TTL: ttl}).Err()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}

	return err == nil, err
}

// Forget deletes key.
func (s *RedisStore) Forget(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.prefix+key).Err()
}

// forgetIfScript runs GET and DEL as one script, so no other client's SET can
// land between the owner check and the delete.
var forgetIfScript = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end`)

// ForgetIf deletes key while it still holds value.
func (s *RedisStore) ForgetIf(ctx context.Context, key string, value []byte) (bool, error) {
	deleted, err := forgetIfScript.Run(ctx, s.client, []string{s.prefix + key}, value).Int()
	if err != nil {
		return false, err
	}

	return deleted == 1, nil
}

// MemoryStore is the in-process driver for tests. Its TTLs run on the
// caller's clock, so a test can travel past them.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
	forever   bool
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: map[string]memoryEntry{}}
}

// Get reads key; ErrMiss when absent or expired.
func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok || entry.expired(ctx) {
		delete(s.entries, key)

		return nil, ErrMiss
	}

	return entry.value, nil
}

// Put stores value for ttl (zero: forever).
func (s *MemoryStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[key] = newMemoryEntry(ctx, value, ttl)

	return nil
}

// Add stores value only when key is absent.
func (s *MemoryStore) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.entries[key]; ok && !entry.expired(ctx) {
		return false, nil
	}

	s.entries[key] = newMemoryEntry(ctx, value, ttl)

	return true, nil
}

// Forget deletes key.
func (s *MemoryStore) Forget(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)

	return nil
}

// ForgetIf deletes key while it still holds value.
func (s *MemoryStore) ForgetIf(ctx context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok || entry.expired(ctx) || !bytes.Equal(entry.value, value) {
		return false, nil
	}

	delete(s.entries, key)

	return true, nil
}

// newMemoryEntry holds value for ttl from now on ctx's clock.
func newMemoryEntry(ctx context.Context, value []byte, ttl time.Duration) memoryEntry {
	return memoryEntry{value: value, expiresAt: clock.Now(ctx).Add(ttl), forever: ttl <= 0}
}

// expired treats a zero TTL as "forever", as Valkey's SET without EX does.
func (e memoryEntry) expired(ctx context.Context) bool {
	if e.forever {
		return false
	}

	return !clock.Now(ctx).Before(e.expiresAt)
}
