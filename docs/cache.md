# Cache

- [Introduction](#introduction)
- [Configuration](#configuration)
    - [Driver Prerequisites](#driver-prerequisites)
- [Cache Usage](#cache-usage)
    - [Obtaining a Cache Instance](#obtaining-a-cache-instance)
    - [Retrieving Items From the Cache](#retrieving-items-from-the-cache)
    - [Storing Items in the Cache](#storing-items-in-the-cache)
    - [Removing Items From the Cache](#removing-items-from-the-cache)
- [Atomic Locks](#atomic-locks)
    - [Managing Locks](#managing-locks)
- [Adding Custom Cache Drivers](#adding-custom-cache-drivers)
    - [Writing the Driver](#writing-the-driver)
- [Testing](#testing)

## Introduction

Some of the data retrieval or processing tasks performed by your application could be CPU intensive or take several seconds to complete. When this is the case, it is common to cache the retrieved data for a time so it can be retrieved quickly on subsequent requests for the same data. A flight-booking application might cache airport weather forecasts, exchange rates and its list of airports this way, in Valkey.

reef's `cache` package has two layers:

- A `cache.Store` is a byte-level driver with five methods. reef ships a Valkey (Redis) driver for your application and an in-memory driver for tests.
- The typed helpers, `cache.Get`, `cache.Put`, `cache.Forever`, `cache.Remember` and `cache.Flexible`, encode your values as JSON on the way in and decode them on the way out, so a cached value is always plain data, never a serialized object.

## Configuration

There is no cache configuration file and no default store: you build a `Store` once, when your application boots, and hand it to whatever needs it. A typical application builds its Valkey store when it boots, prefixing every key with its own namespace:

```go
valkey := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), Password: cfg.Redis.Password, DB: cfg.Redis.DB})

store := cache.NewRedisStore(valkey, cfg.Redis.Prefix+"cache:")
```

`NewRedisStore` takes a go-redis client and a prefix, and returns a `*cache.RedisStore`. The prefix is prepended to every key the store reads or writes, so your application's entries never collide with other data in a shared Valkey. Expiry is Valkey's own: a key with a TTL is stored with one, and a zero TTL stores it without.

### Driver Prerequisites

The Redis driver works with any server that speaks the Redis protocol, Valkey included. It relies on `SET` with `NX` and an expiry for [locks](#atomic-locks), and on a small Lua script for the lock owner's atomic compare-and-delete, so the server must allow `EVAL`.

The memory driver, `cache.NewMemoryStore()`, returns an empty `*cache.MemoryStore` and needs nothing at all. It is meant for tests and single-process tools: its entries live in the process and vanish with it. It is safe for concurrent use, and expires entries on the [clock](#testing) of the context each call is given.

## Cache Usage

### Obtaining a Cache Instance

reef has no `Cache` facade. A service that caches declares a `cache.Store` dependency and receives the application's store when it is built, usually in a [provider](lifecycle.md). For example, an airport weather service:

```go
type AirportWeather struct {
	client *httpclient.Client
	cache  cache.Store
}

func NewAirportWeather(client *httpclient.Client, store cache.Store) *AirportWeather {
	return &AirportWeather{client: client, cache: store}
}
```

A controller that caches declares the store as an exported field, `Cache cache.Store`, like any other [controller dependency](controllers.md).

### Retrieving Items From the Cache

The generic `cache.Get` function retrieves an item and decodes it into the type you name. When the item does not exist, or has expired, `ok` is `false` and `err` is `nil`:

```go
rates, ok, err := cache.Get[map[string]float64](ctx, store, "exchange_rates:GBP")
if err != nil {
	return err
}

if ok {
	// ...
}
```

`err` reports a store failure, or a cached value that no longer decodes into the type you asked for.

#### Retrieve and Store

Sometimes you may wish to retrieve an item from the cache, but also store a default value if the requested item doesn't exist. You may do this using `cache.Remember`. If the item isn't cached, the closure is executed and its result is placed in the cache:

```go
func (w *AirportWeather) Forecast(ctx context.Context, airportID int64) ([]DayData, error) {
	return cache.Remember(ctx, w.cache, fmt.Sprintf("airports:weather:%d", airportID), 6*time.Hour, func(ctx context.Context) ([]DayData, error) {
		return w.fetch(ctx, airportID)
	})
}
```

`Remember` treats the cache as an optimization that must never fail the caller:

- When the closure returns an error, `Remember` returns it and caches nothing, so a 404 or a transient upstream failure never sticks.
- A cache read that fails, or a value that no longer decodes, counts as a miss: the closure runs and its result overwrites the entry.
- A cache write that fails is logged as a warning (`cache: put failed`), and the computed value is still returned.

> [!NOTE]
> Values go through `encoding/json`, so only exported fields survive the round trip. When you change the shape of a cached type, put a version in the key (for example, `airports:index:v3`) so old entries are never decoded into the new type.

#### Stale While Revalidate

When using `Remember`, some users may experience slow response times when the cached value has expired, and under load every request that races the expiry rebuilds the value at once. For certain types of data, it can be useful to serve partially stale data while the value is recalculated in the background. This is often referred to as the "stale-while-revalidate" pattern, and `cache.Flexible` implements it.

`Flexible` accepts two durations: how long the cached value is considered "fresh", and how long it may be served at all, as "stale" data, before recalculation is necessary. Both are measured from when the value was computed:

```go
// Fresh for an hour; served stale for up to four.
func (ctl *AirportController) Index(c *web.Context) ([]models.Airport, error) {
	return cache.Flexible(c, ctl.Cache, "airports:index", time.Hour, 4*time.Hour, func(ctx context.Context) ([]models.Airport, error) {
		return models.Airports(ctx).Active().OrderBy(models.AirportTableColumns.Name).Get()
	})
}
```

- Within the fresh period, the cached value is returned immediately.
- Within the stale period, the cached value is still returned immediately, and exactly one caller recomputes it in a background goroutine. The refresher is chosen with a lock on `{key}:refresh-lock`, held for at most 30 seconds; callers that don't get the lock just serve the stale value. Once it holds the lock, the refresher reads the entry again and leaves it alone if another refresh already replaced it or it was forgotten meanwhile, so a caller whose read raced a refresh never computes the value twice, and a forgotten entry stays forgotten. A refresh that fails is logged and the stale value keeps being served; so is one that panics, logged with its stack, since nothing waits on that goroutine. Either way the lock is released, so the next stale reader tries again.
- After the stale period, the entry has expired, and the value is recomputed inline, as in `Remember`.

As with `Remember`, an inline closure error is returned uncached, and a cache failure is a miss rather than the caller's error.

> [!WARNING]
> The background refresh runs after your request may have finished, so it receives a context that keeps your request's values (its database connection, its clock) without its cancellation. Always build queries from the `ctx` the closure is handed, never from one captured from the surrounding request.
>
> Keeping the connection means that inside a transaction, which is every rolled-back test, the refresh's goroutine shares that transaction. Passing `c` itself is safe: a `*web.Context`'s context methods may be called from any goroutine (see [the request is a context](requests.md#the-request-is-a-context)).

`Flexible` stores the value wrapped together with the time it was computed, so read a `Flexible` key only through `Flexible`, never through `Get` or `Remember`.

### Storing Items in the Cache

You may use `cache.Put` to store a value for a given duration:

```go
err := cache.Put(ctx, store, "currency:"+userID, "EUR", 24*time.Hour)
```

A TTL of zero stores the item without an expiry.

#### Storing Items Forever

The `cache.Forever` function stores an item permanently. Since these items never expire, they must be removed manually with `Forget`. For example, an application may cache its list of airlines forever, and forget it when the supplier reports a change:

```go
if err := cache.Forever(ctx, s.cache, "airlines:all:v1", airlines); err != nil {
	return err
}
```

#### Store if Not Present

A store's `Add` method stores an item only if it does not already exist, and reports whether it did. It is atomic, which makes it the building block for [locks](#atomic-locks). `Add` works on raw bytes, so encode the value yourself:

```go
added, err := store.Add(ctx, "welcome-sent:42", []byte("1"), 24*time.Hour)
```

### Removing Items From the Cache

You may remove an item from the cache using the store's `Forget` method:

```go
err := store.Forget(ctx, "airlines:all:v1")
```

`ForgetIf` removes an item only while it still holds the given bytes, atomically, and reports whether it did. Locks release themselves with it:

```go
deleted, err := store.ForgetIf(ctx, "imports:schedules", ownerToken)
```

reef's cache deliberately stays this small: there are no tags, no `flush`, no `increment` or `pull`. Use a versioned key prefix where you would have flushed a tag, and the [rate limiter](rate-limiting.md) where you would have counted.

## Atomic Locks

Atomic locks allow for the manipulation of distributed locks without worrying about race conditions. For example, a lock can make sure that two requests for the same flight search never query the airline API twice at once. A lock works with any store, but only locks every server shares, such as the Valkey store, guard across processes.

### Managing Locks

You may create a lock using `cache.NewLock`, which takes the store, the lock's name, and how long the lock may be held. The TTL frees a lock whose holder crashed:

```go
lock := cache.NewLock(store, "imports:schedules", 10*time.Minute)

acquired, err := lock.Get(ctx)
if err != nil || !acquired {
	return err
}
defer func() { _ = lock.Release(context.WithoutCancel(ctx)) }()

// Lock acquired for at most 10 minutes...
```

`Get` tries once to acquire the lock. `Release` frees it only if this lock still holds it: every `*Lock` carries a random owner token, and the check and the delete are one atomic compare-and-delete, so a holder whose TTL lapsed can never delete the next holder's lock. Two `NewLock` values for the same name are two different owners, and there is no way to force-release someone else's lock.

If the lock is not available at the moment you request it, `Block` waits for it, runs your function while holding it, and releases it afterwards. For example, a flight search might take a lock like this:

```go
const (
	// searchLockTTL frees the lock of a crashed holder; searchLockWait is
	// how long a concurrent run of the same search waits for it.
	searchLockTTL  = 10 * time.Second
	searchLockWait = 5 * time.Second
)

err := cache.NewLock(store, fmt.Sprintf("flight-search:%d", search.ID), searchLockTTL).Block(ctx, searchLockWait, func() error {
	// Lock acquired for 10 seconds after waiting a maximum of 5 seconds...
	return nil
})
```

`Block` retries every 250 milliseconds. It returns:

- your function's error, or `nil`;
- a `*cache.LockTimeoutError` when the lock couldn't be acquired within the wait, carrying the lock's `Name` (its `Error()` reads `cache: timed out waiting for lock "{name}"`);
- the context's error when the context ends while waiting;
- a wrapped store error when the store fails.

The lock is released even when your function fails or panics; a release that fails is logged and leaves the lock to its TTL.

```go
var timeout *cache.LockTimeoutError
if errors.As(err, &timeout) {
	// Unable to acquire the lock...
}
```

A `LockTimeoutError` returned from a controller is an unexpected failure, rendered as a 500 like any other error. If waiting out a busy lock is a normal outcome for you, check for it and answer something more useful.

The scheduler's overlap prevention is built on these locks too; see [scheduling](scheduling.md).

## Adding Custom Cache Drivers

### Writing the Driver

To create a custom cache driver, implement the `cache.Store` interface:

```go
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	Forget(ctx context.Context, key string) error
	ForgetIf(ctx context.Context, key string, value []byte) (bool, error)
}
```

Every driver must keep these contracts, which the typed helpers and locks rely on:

- `Get` returns `cache.ErrMiss` for a key that is absent or expired.
- A TTL of zero means the item never expires.
- `Add` stores only when the key is absent and reports whether it did, atomically.
- `ForgetIf` deletes the key only while it still holds exactly `value`, atomically, and reports whether it did.

Here is a minimal driver that keeps everything in a map and never expires anything:

```go
// MapStore is an in-process cache that never expires anything.
type MapStore struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func (s *MapStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	value, ok := s.entries[key]
	if !ok {
		return nil, cache.ErrMiss
	}

	return value, nil
}

func (s *MapStore) Put(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[key] = value

	return nil
}

func (s *MapStore) Add(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[key]; ok {
		return false, nil
	}

	s.entries[key] = value

	return true, nil
}

func (s *MapStore) Forget(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)

	return nil
}

func (s *MapStore) ForgetIf(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if current, ok := s.entries[key]; !ok || !bytes.Equal(current, value) {
		return false, nil
	}

	delete(s.entries, key)

	return true, nil
}
```

There is nothing to register: pass your store wherever a `cache.Store` is expected. `*cache.RedisStore` and `*cache.MemoryStore` implement the same five methods, so you may also call `Get`, `Put`, `Add`, `Forget` and `ForgetIf` on them directly.

## Testing

In tests, use `cache.NewMemoryStore()`. Give every test a fresh memory store in your [test case](testing.md#a-full-test-case), so cached values never leak from one test into another.

The memory store reads the time from the [clock](helpers.md) on the context it is given, so a test that travels in time steps past TTLs without sleeping. An item expires the moment its TTL has fully elapsed:

```go
func TestTheForecastIsCachedForSixHours(t *testing.T) {
	ctx, now := clocktest.FrozenAt(t.Context(), time.Now())
	store := cache.NewMemoryStore()

	if err := cache.Put(ctx, store, "airports:weather:1", []DayData{{Date: "2026-10-22", Max: 24}}, 6*time.Hour); err != nil {
		t.Fatal(err)
	}

	now.Travel(6 * time.Hour)

	if _, ok, _ := cache.Get[[]DayData](ctx, store, "airports:weather:1"); ok {
		t.Fatal("the forecast outlived its TTL")
	}
}
```

`Flexible` reads the same clock to tell fresh from stale. Its background refresh and `Lock.Block`'s retries run on goroutines and real timers: test them in a `testing/synctest` bubble and wait with `synctest.Wait()` rather than polling. See [mocking](mocking.md) for [bubbles](mocking.md#goroutines-and-timers) and for time travel in feature tests.
