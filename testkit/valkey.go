package testkit

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/atobeach/reef/support"
)

// ValkeyAddr is the shared Valkey the suite's real-server tests use.
const ValkeyAddr = "127.0.0.1:6379"

// Valkey connects a fresh client with a random key prefix the test owns,
// whose keys are deleted when it ends (the server is shared: never FLUSH).
// Without Valkey the test is skipped, or fails under CI.
func Valkey(t testing.TB) (client *redis.Client, prefix string) {
	t.Helper()

	client = redis.NewClient(&redis.Options{Addr: ValkeyAddr})
	if err := client.Ping(t.Context()).Err(); err != nil {
		_ = client.Close()

		if os.Getenv("CI") != "" {
			t.Fatalf("CI must provide Valkey on %s: %v", ValkeyAddr, err)
		}

		t.Skipf("no Valkey on %s: %v", ValkeyAddr, err)
	}

	prefix = "testkit-" + support.RandomString(12) + ":"

	// Background, not t.Context(): that one is already cancelled by cleanup time.
	t.Cleanup(func() {
		ctx := context.Background()

		var keys []string
		iter := client.Scan(ctx, 0, prefix+"*", 100).Iterator()
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}

		if len(keys) > 0 {
			client.Del(ctx, keys...)
		}

		_ = client.Close()
	})

	return client, prefix
}

// ValkeyClient is another client on the same server, standing in for a
// second process sharing the store.
func ValkeyClient(t testing.TB) *redis.Client {
	t.Helper()

	client := redis.NewClient(&redis.Options{Addr: ValkeyAddr})
	t.Cleanup(func() { _ = client.Close() })

	return client
}
