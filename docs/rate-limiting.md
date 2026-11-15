# Rate Limiting

- [Introduction](#introduction)
    - [Store Configuration](#store-configuration)
- [Defining Rate Limiters](#defining-rate-limiters)
    - [Segmenting Rate Limits](#segmenting-rate-limits)
    - [Multiple Rate Limits](#multiple-rate-limits)
    - [Windows Other Than a Minute](#windows-other-than-a-minute)
- [Attaching Rate Limiters to Routes](#attaching-rate-limiters-to-routes)
    - [Per-Minute Limits](#per-minute-limits)
    - [Throttled Responses](#throttled-responses)
- [Basic Usage](#basic-usage)
    - [Manually Incrementing Attempts](#manually-incrementing-attempts)
    - [Determining Limiter Availability](#determining-limiter-availability)
    - [Clearing Attempts](#clearing-attempts)
- [Stores](#stores)
    - [The Redis Store](#the-redis-store)
    - [The Memory Store](#the-memory-store)
    - [Writing a Store](#writing-a-store)
- [Testing](#testing)
- [Available Methods](#available-methods)

## Introduction

reef includes a simple rate limiting abstraction which, together with a counter store such as Valkey, provides an easy way to limit any action during a window of time. You'll mostly use it to throttle routes: a sign-in endpoint, a route that spends money with a third party, or a public read that shouldn't become a free amplifier.

The counting lives in the `ratelimit` package. Its rules are simple: the first hit on a key opens a fixed window of the limit's decay, every hit inside the window counts, and the counter vanishes when the window ends. On top of it, the `RateLimiters` registry in reef's [stock middleware](middleware.md#available-middleware) holds your named limiters and turns them into route middleware. The [routing documentation](routing.md#rate-limiting) introduces the same registry from the routes' side.

### Store Configuration

The counters live in a `ratelimit.Store`. In production you'll want `ratelimit.RedisStore`, which keeps them in Valkey (or Redis) so every server running your application shares them; tests use the in-process `ratelimit.MemoryStore`. A typical application dials Valkey at boot and stores its counters under their own key prefix:

```go
import (
	"github.com/redis/go-redis/v9"

	"github.com/atobeach/reef/ratelimit"
)

valkey := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), Password: cfg.Redis.Password, DB: cfg.Redis.DB})

store := ratelimit.NewRedisStore(valkey, cfg.Redis.Prefix+"ratelimit:")
```

You then build the registry over the store with `middleware.NewRateLimiters`. If you give it the guard's `UserID` method, [per-minute limits](#per-minute-limits) count per signed-in user rather than per IP:

```go
import "github.com/atobeach/reef/web/middleware"

limiters := middleware.NewRateLimiters(middleware.RateLimitersConfig{
	Store:  store,
	UserID: guard.UserID,
})
```

| Option | Description |
| --- | --- |
| `Store` | The `ratelimit.Store` keeping the counters. Required. |
| `UserID` | Optional. Returns the signed-in user's id, or nil for a guest; when set, `PerMinute` limits key on the user rather than the IP. |

## Defining Rate Limiters

A rate limiter is a name and a function that, for each request, returns the limit the request counts against. You define limiters on the registry, once, at boot, before your routes are registered. Keep the definitions together in one function your bootstrap calls, handed the registry and the guard its per-user limiters key on:

```go
import (
	"example.com/flights/app/models"
	"github.com/atobeach/reef/auth"
	"github.com/atobeach/reef/ratelimit"
	"github.com/atobeach/reef/web"
	"github.com/atobeach/reef/web/middleware"
)

// RegisterRateLimiters defines the named limiters the routes throttle by.
func RegisterRateLimiters(limiters *middleware.RateLimiters, guard *auth.Guard[models.User]) {
	// Public reads are cheap but still DB work, so not a free amplifier.
	limiters.For("public-read", func(c *web.Context) ratelimit.Limit {
		return ratelimit.PerMinute(180).By(c.IP())
	})

	// ...
}
```

A `ratelimit.Limit` allows `MaxAttempts` hits per `Decay` window in the bucket named `Key`. `ratelimit.PerMinute(n)` builds a limit of `n` hits a minute, and its `By` method returns a copy with the bucket key set.

### Segmenting Rate Limits

The key you pass to `By` decides who shares a bucket. Key on `c.IP()` to limit each address, on a user's id to limit each user, or on anything else the request carries. Because the function runs on every request, you may also pick a different limit for different callers:

```go
limiters.For("uploads", func(c *web.Context) ratelimit.Limit {
	if id := guard.UserID(c); id != nil {
		return ratelimit.PerMinute(100).By("user:" + strconv.FormatInt(*id, 10))
	}

	return ratelimit.PerMinute(10).By("ip:" + c.IP())
})
```

A login limiter may key on the email address and the IP together, so one attacker can't lock a user out from everywhere, and lowercases and transliterates the address so case or accent variants share one bucket:

```go
limiters.For("login", func(c *web.Context) ratelimit.Limit {
	email := strings.ToLower(c.String("email"))

	return ratelimit.PerMinute(5).By(support.Transliterate(email + "|" + c.IP()))
})
```

Each limiter's buckets are namespaced by its name, and the key you pass to `By` is hashed with SHA-256 before it reaches the store, so a key may hold an email address and be as long as the request makes it. Two limiters never share a bucket, even when their keys are equal.

> [!NOTE]
> Changing a limiter's name or the shape of its key starts every caller in a fresh bucket, which in practice resets the limit for everyone on deploy.

### Multiple Rate Limits

If needed, you may define a limiter with several limits using `ForAll`, whose function returns a slice. Every limit is enforced. A typical pairing is a per-user bucket under a wider per-IP ceiling, so several people behind one office NAT don't throttle each other, while one client cycling through accounts is still bounded:

```go
limiters.ForAll("booking", func(c *web.Context) []ratelimit.Limit {
	return []ratelimit.Limit{
		ratelimit.PerMinute(10).By("booking:" + userKey(c)),
		ratelimit.PerMinute(30).By("booking-ip:" + c.IP()),
	}
})
```

Here `userKey` is the application's own helper: the signed-in user's id, or else the IP. Give each limit in the slice a distinct key: two limits with the same key share one counter. The per-IP ceiling above is prefixed `booking-ip:` so it never collides with a user bucket that fell back to the IP.

The limits are checked in order. Each is checked before it's counted, so a rejected request isn't a hit on the limit that rejected it (or on the ones after it), but it has already counted on the limits before it.

> [!WARNING]
> Build the slice inside the function, as above, rather than returning a slice you keep elsewhere. The registry rewrites each limit's `Key` in the slice you return.

### Windows Other Than a Minute

`PerMinute` is the only constructor. For any other window, build the `Limit` yourself and set its `Decay`:

```go
limiters.For("exports", func(c *web.Context) ratelimit.Limit {
	return ratelimit.Limit{MaxAttempts: 20, Decay: time.Hour}.By(c.IP())
})
```

## Attaching Rate Limiters to Routes

Rate limiters are attached to routes or route groups with the registry's `Throttle` method, which takes the limiter's name and returns a middleware:

```go
r.Middleware(limiters.Throttle("public-read")).Group(func(r *web.Router) {
	r.Get("/airports", c.Airports.Index).Name("airports.index")
	r.Get("/airports/{code}", c.Airports.Show).Name("airports.show")
})

r.Post("/login", accounts.AuthenticatedSession.Store).Middleware(limiters.Throttle("login"), guard.RedirectIfAuthenticated).Name("login.store")
```

Every route a limiter throttles shares its buckets: above, a visitor's reads of `/airports` and `/airports/{code}` count against the same 180 a minute.

`Throttle` panics when no limiter has the name, and it's called while your routes are registered. A typo therefore stops your application at boot, rather than shipping an unthrottled route. Register your limiters before your routes.

> [!NOTE]
> Throttling fails closed. When the store can't be read or written, the request fails with a `500` rather than passing unthrottled: limiters guard paid upstream calls and money routes, so a store outage must not lift them. The throttle reads the store once more after the route has run, to fill in the [rate-limit headers](#throttled-responses); a failure there answers `500` too, though the route's work is already done.

### Per-Minute Limits

For a route that only needs a plain cap, the registry's `PerMinute` method returns a throttle without a named limiter:

```go
r.Post("/email/verification-otp", c.EmailVerification.Send).Middleware(limiters.PerMinute(6)).Name("verification.send")
```

It keys on the signed-in user when the registry has a `UserID` function and someone is signed in, and on the client IP otherwise.

> [!WARNING]
> Every `PerMinute` route shares one bucket per caller, whatever its `n`. A caller who spends ten hits on a `PerMinute(60)` route is already over a `PerMinute(6)` route's limit. Define a named limiter when a route needs a budget of its own.

### Throttled Responses

When a request is over a limit, the throttle answers with a `429 Too Many Requests` and the JSON body `{"message": "Too Many Attempts."}`, with these headers:

| Header | Value |
| --- | --- |
| `Retry-After` | Seconds until the window resets, rounded, and at least `1`. |
| `X-RateLimit-Reset` | The Unix time at which the window resets. |
| `X-RateLimit-Limit` | The rejecting limit's `MaxAttempts`. |
| `X-RateLimit-Remaining` | `0`. |

Every other response from a throttled route carries `X-RateLimit-Limit` and `X-RateLimit-Remaining` for the most constrained of its limits: the one with the fewest attempts left, the first on a tie. A per-user 120 under a per-IP 480 reports the 120. An error the route returns gets them too, so a `422` from a wrong password tells the client how many attempts it has left, just as a success would.

The rejection is a `*web.Error` built by `web.TooManyRequests`, returned up the middleware chain like any other [error](errors.md), so a middleware listed before the throttle may tell a throttled request apart:

```go
// CountRefusals counts the requests the throttle after it refuses.
func CountRefusals(c *web.Context, next web.Handler) (any, error) {
	value, err := next(c)

	if refused, ok := errors.AsType[*web.Error](err); ok && refused.Code == http.StatusTooManyRequests {
		// Count the refusal...
	}

	return value, err
}

r.Get("/airports", c.Airports.Index).Middleware(CountRefusals, limiters.Throttle("public-read")).Name("airports.index")
```

The throttle has no setting for a custom rejection. If you need one, such a middleware may return a response or an error of its own in place of the `429`.

## Basic Usage

You may also use a store directly, to rate limit any action outside a route's middleware: a message a user sends from inside a longer flow, say. Hand the store to the code that needs it as you would any other service, then check and count attempts yourself.

### Manually Incrementing Attempts

The store's `Attempts` method returns the hits in a key's open window and how long until the window resets, both zero when no window is open. Its `Hit` method counts one attempt, opening a window of the given decay on the first, and returns the new count:

```go
import (
	"context"
	"net/http"
	"strconv"

	"github.com/atobeach/reef/ratelimit"
	"github.com/atobeach/reef/web"
)

// SendMessage sends a message, at most five a minute per user.
type SendMessage struct {
	Limits ratelimit.Store
}

func (a SendMessage) Handle(ctx context.Context, userID int64, body string) error {
	limit := ratelimit.PerMinute(5).By("send-message:" + strconv.FormatInt(userID, 10))

	hits, _, err := a.Limits.Attempts(ctx, limit.Key)
	if err != nil {
		return err
	}

	if hits >= int64(limit.MaxAttempts) {
		return web.Abort(http.StatusTooManyRequests, "Too many messages sent!")
	}

	if _, err := a.Limits.Hit(ctx, limit.Key, limit.Decay); err != nil {
		return err
	}

	// Send the message...

	return nil
}
```

A key you use directly reaches the store as you wrote it, unhashed and without a limiter's name. Prefix it with something of your own, as `send-message:` above, so it can't collide with another action's counters.

### Determining Limiter Availability

The second value `Attempts` returns is how long until the window resets. When a key has no attempts left, you may return it to the caller. `web.TooManyRequests` builds the same `429`, with the same headers, that the throttle middleware answers with:

```go
hits, resetIn, err := a.Limits.Attempts(ctx, limit.Key)
if err != nil {
	return err
}

if hits >= int64(limit.MaxAttempts) {
	return web.TooManyRequests(ctx, limit.MaxAttempts, resetIn)
}
```

### Clearing Attempts

reef's stores have no method to clear a key: a window simply ends when its decay runs out, and the counter goes with it. In tests, each test case starts with an empty `MemoryStore`, or [moves its clock](#testing) past the window.

## Stores

A store keeps the window counters. reef ships two, and both satisfy `ratelimit.Store`.

### The Redis Store

`ratelimit.NewRedisStore(client, prefix)` keeps each bucket in one expiring key, `prefix` followed by the bucket's key, so every instance counting against the same Valkey shares the limits. `Hit` increments the counter and, on the first hit, sets its expiry in a single Lua script, so a crash between the two can never leave a counter that never resets. `Attempts` reads the counter and its remaining lifetime in one pipelined round trip; a counter left without an expiry reads as no open window, and the next hit gives it one.

### The Memory Store

`ratelimit.NewMemoryStore()` keeps the windows in process, behind a mutex. It's meant for tests: its windows run on the clock the context carries (`clock.Now(ctx)`), so a test that travels in time steps past them.

### Writing a Store

Any type with the two methods of `ratelimit.Store` will do:

```go
type Store interface {
	// Attempts is the hit count of key's open window and how long until it
	// resets; 0 and 0 when no window is open.
	Attempts(ctx context.Context, key string) (hits int64, resetIn time.Duration, err error)
	// Hit counts one attempt, opening a window of decay on the first.
	Hit(ctx context.Context, key string, decay time.Duration) (int64, error)
}
```

Keep the fixed-window semantics: the first hit opens the window, later hits inside it never extend it, and a key whose window has ended reads as zero.

## Testing

A typical [test case](testing.md#a-full-test-case) builds its application over a fresh `ratelimit.NewMemoryStore()`, so every test starts with empty buckets, and the test's own clock drives the windows. A throttling test may spend the budget, assert the `429`, then travel past the window:

```go
func TestLoginIsThrottled(t *testing.T) {
	tc := tests.Setup(t)
	user := factories.User(tc).Create()
	credentials := map[string]any{"email": user.Email, "password": "wrong-password"}

	for range 5 {
		tc.Browser().PostJSON("/login", credentials).AssertUnprocessable()
	}

	tc.Browser().PostJSON("/login", credentials).AssertTooManyRequests()

	tc.Travel(time.Minute)

	tc.Browser().PostJSON("/login", credentials).AssertUnprocessable()
}
```

Code that uses a store directly may be tested the same way, on a clock of its own from [`clocktest`](mocking.md#interacting-with-time):

```go
func TestSendMessageAllowsFiveAMinute(t *testing.T) {
	ctx, now := clocktest.FrozenAt(t.Context(), time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	action := SendMessage{Limits: ratelimit.NewMemoryStore()}

	for range 5 {
		if err := action.Handle(ctx, 7, "hello"); err != nil {
			t.Fatal(err)
		}
	}

	if err := action.Handle(ctx, 7, "hello"); err == nil {
		t.Fatal("the sixth message in a minute was sent")
	}

	now.Travel(time.Minute)

	if err := action.Handle(ctx, 7, "hello"); err != nil {
		t.Fatal(err)
	}
}
```

## Available Methods

- `ratelimit.Limit`: `MaxAttempts` hits per `Decay` window in the bucket named `Key`. `ratelimit.Limit{MaxAttempts: 20, Decay: time.Hour}`
- `ratelimit.PerMinute(n int) Limit`: a limit of `n` hits a minute. `ratelimit.PerMinute(5)`
- `Limit.By(key string) Limit`: a copy of the limit with its bucket key set. `ratelimit.PerMinute(5).By(c.IP())`
- `ratelimit.Store`: keeps the window counters, through `Attempts` and `Hit`. See [Writing a Store](#writing-a-store).
- `ratelimit.NewRedisStore(client *redis.Client, prefix string) *RedisStore`: a store keeping counters in Valkey under `prefix`. `ratelimit.NewRedisStore(valkey, "flights:ratelimit:")`
- `RedisStore.Attempts(ctx, key) (int64, time.Duration, error)`: reads the counter and its remaining lifetime in one round trip.
- `RedisStore.Hit(ctx, key, decay) (int64, error)`: counts one attempt, starting the window's expiry on the first.
- `ratelimit.NewMemoryStore() *MemoryStore`: an empty in-process store, for tests. `ratelimit.NewMemoryStore()`
- `MemoryStore.Attempts(ctx, key) (int64, time.Duration, error)`: reads the open window, on `ctx`'s clock.
- `MemoryStore.Hit(ctx, key, decay) (int64, error)`: counts one attempt, opening a window on `ctx`'s clock when none is open.

The registry, `middleware.RateLimiters` (`NewRateLimiters`, `For`, `ForAll`, `Throttle`, `PerMinute`), is part of reef's [stock middleware](middleware.md); its use is described above.
