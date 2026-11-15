package middleware

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/ratelimit"
	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/web"
)

func serveThrottled(t *testing.T, throttle web.Middleware) func() *testkit.TestResponse {
	t.Helper()

	return serveThrottledOn(t, throttle, nil)
}

// serveThrottledOn serves the throttled route on now, the clock every
// request's context carries.
func serveThrottledOn(t *testing.T, throttle web.Middleware, now *clock.Clock) func() *testkit.TestResponse {
	t.Helper()

	router := web.NewRouter()
	router.Get("/x", func(*web.Context) (any, error) { return "ok", nil }).Middleware(throttle)

	bind := func(ctx context.Context) context.Context { return clock.With(ctx, now) }
	kernel, err := web.NewKernel(bind, web.KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	client := testkit.NewClient(t, kernel).FromIP("198.51.100.4")

	return func() *testkit.TestResponse { return client.Get("/x") }
}

func TestRejectedRequestsAreNotCounted(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiters := NewRateLimiters(RateLimitersConfig{Store: store})
	limiters.For("public-read", func(c *web.Context) ratelimit.Limit { return ratelimit.PerMinute(2).By(c.IP()) })
	hit := serveThrottled(t, limiters.Throttle("public-read"))

	hit()
	if res := hit(); res.Status() != http.StatusOK || res.Header("X-RateLimit-Remaining") != "0" {
		t.Fatalf("second = %d remaining %q", res.Status(), res.Header("X-RateLimit-Remaining"))
	}

	for range 3 {
		res := hit()
		if res.Status() != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", res.Status())
		}

		if retry := res.Header("Retry-After"); retry != "60" && retry != "59" {
			t.Fatalf("Retry-After = %q, want the rest of the minute", retry)
		}
	}

	if hits, _, _ := store.Attempts(t.Context(), bucket("public-read", "198.51.100.4")); hits != 2 {
		t.Fatalf("the bucket holds %d attempts, want 2 (rejections don't count)", hits)
	}
}

// The window runs on the app clock: the 429 names when it resets, and a
// request past that is let through.
func TestTheWindowAndItsResetHeaderFollowTheAppClock(t *testing.T) {
	_, now := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	limiters := NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()})
	limiters.For("public-read", func(c *web.Context) ratelimit.Limit { return ratelimit.PerMinute(1).By(c.IP()) })
	hit := serveThrottledOn(t, limiters.Throttle("public-read"), now)

	hit()
	now.Travel(15 * time.Second)

	res := hit()
	if res.Status() != http.StatusTooManyRequests || res.Header("Retry-After") != "45" {
		t.Fatalf("second = %d, Retry-After %q", res.Status(), res.Header("Retry-After"))
	}

	if reset := res.Header("X-RateLimit-Reset"); reset != strconv.FormatInt(now.Now().Add(45*time.Second).Unix(), 10) {
		t.Fatalf("X-RateLimit-Reset = %s, want the window's end on the app clock", reset)
	}

	now.Travel(45 * time.Second)

	if res := hit(); res.Status() != http.StatusOK {
		t.Fatalf("past the window = %d, want 200", res.Status())
	}
}

func TestEveryLimitIsEnforcedAndTheTightestReportsHeaders(t *testing.T) {
	limiters := NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()})
	limiters.ForAll("chat", func(c *web.Context) []ratelimit.Limit {
		return []ratelimit.Limit{ratelimit.PerMinute(5).By("chat:guest"), ratelimit.PerMinute(2).By("chat-ip:" + c.IP())}
	})
	hit := serveThrottled(t, limiters.Throttle("chat"))

	if res := hit(); res.Header("X-RateLimit-Limit") != "2" || res.Header("X-RateLimit-Remaining") != "1" {
		t.Fatalf("limit %q remaining %q", res.Header("X-RateLimit-Limit"), res.Header("X-RateLimit-Remaining"))
	}

	hit()
	if res := hit(); res.Status() != http.StatusTooManyRequests || res.Header("X-RateLimit-Limit") != "2" {
		t.Fatalf("the tighter per-IP ceiling should 429: %d limit %q", res.Status(), res.Header("X-RateLimit-Limit"))
	}
}

func TestTheMostConstrainedLimitReportsTheHeaders(t *testing.T) {
	// A per-search 120 under a per-IP 480 reports the 120, not the last limit.
	limiters := NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()})
	limiters.ForAll("reads", func(c *web.Context) []ratelimit.Limit {
		return []ratelimit.Limit{ratelimit.PerMinute(120).By("reads:search"), ratelimit.PerMinute(480).By("reads-ip:" + c.IP())}
	})
	hit := serveThrottled(t, limiters.Throttle("reads"))

	if res := hit(); res.Header("X-RateLimit-Limit") != "120" || res.Header("X-RateLimit-Remaining") != "119" {
		t.Fatalf("limit %q remaining %q", res.Header("X-RateLimit-Limit"), res.Header("X-RateLimit-Remaining"))
	}
}

func TestPerMinuteKeysOnTheUserWhenSignedIn(t *testing.T) {
	signedIn, other := int64(42), int64(43)
	user := &signedIn
	limiters := NewRateLimiters(RateLimitersConfig{
		Store:  ratelimit.NewMemoryStore(),
		UserID: func(*web.Context) *int64 { return user },
	})
	hit := serveThrottled(t, limiters.PerMinute(1))

	if res := hit(); res.Status() != http.StatusOK {
		t.Fatalf("user 42's first request = %d", res.Status())
	}

	res := hit()
	if res.Status() != http.StatusTooManyRequests {
		t.Fatalf("user 42's second request = %d, want 429", res.Status())
	}

	if retry := res.Header("Retry-After"); retry != "60" && retry != "59" {
		t.Fatalf("Retry-After = %q, want the rest of the minute", retry)
	}

	// The same IP signed out, and another user on it, have buckets of their own.
	user = nil
	if res := hit(); res.Status() != http.StatusOK {
		t.Fatalf("the IP's first request = %d", res.Status())
	}

	user = &other
	if res := hit(); res.Status() != http.StatusOK {
		t.Fatalf("user 43's first request = %d", res.Status())
	}
}

// A caller has one per-minute bucket, whatever each PerMinute route's n: a
// hit on one route spends an attempt on every other.
func TestPerMinuteRoutesShareTheCallersBucket(t *testing.T) {
	limiters := NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()})
	tight, wide := serveThrottled(t, limiters.PerMinute(1)), serveThrottled(t, limiters.PerMinute(5))

	tight()

	if res := wide(); res.Status() != http.StatusOK || res.Header("X-RateLimit-Remaining") != "3" {
		t.Fatalf("the wide route = %d remaining %q, want 200 with 3 left", res.Status(), res.Header("X-RateLimit-Remaining"))
	}

	if res := tight(); res.Status() != http.StatusTooManyRequests {
		t.Fatalf("the tight route after both hits = %d, want 429", res.Status())
	}
}

func TestUndefinedLimiterNamesPanicAtRegistration(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()

	NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()}).Throttle("typo")
}

// A throttled route's failures carry the rate-limit headers too, so the
// client backs off from a 422 as it would from a success.
func TestAThrottledRoutesErrorsCarryTheRateLimitHeaders(t *testing.T) {
	limiters := NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()})
	limiters.For("login", func(c *web.Context) ratelimit.Limit { return ratelimit.PerMinute(5).By(c.IP()) })

	router := web.NewRouter()
	router.Post("/login", func(*web.Context) (any, error) {
		return nil, web.ValidationFailed("email", "These credentials do not match our records.")
	}).Middleware(limiters.Throttle("login"))

	kernel, err := web.NewKernel(nil, web.KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	testkit.NewClient(t, kernel).Post("/login").
		AssertUnprocessable().
		AssertJSONValidationError("email", "These credentials do not match our records.").
		AssertHeader("X-RateLimit-Limit", "5").
		AssertHeader("X-RateLimit-Remaining", "4")
}

// The throttle refuses with the 429 error itself, so a middleware around it
// tells a throttled request apart by its type, and the response keeps the
// error's retry header.
func TestTheThrottlesRefusalReachesTheMiddlewareAroundItAsAnError(t *testing.T) {
	limiters := NewRateLimiters(RateLimitersConfig{Store: ratelimit.NewMemoryStore()})
	limiters.For("public-read", func(c *web.Context) ratelimit.Limit { return ratelimit.PerMinute(1).By(c.IP()) })

	var refused *web.Error
	observe := func(c *web.Context, next web.Handler) (any, error) {
		value, err := next(c)
		refused, _ = errors.AsType[*web.Error](err)

		return value, err
	}

	router := web.NewRouter()
	router.Get("/x", func(*web.Context) (any, error) { return "ok", nil }).Middleware(observe, limiters.Throttle("public-read"))

	_, now := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	kernel, err := web.NewKernel(func(ctx context.Context) context.Context { return clock.With(ctx, now) }, web.KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	client := testkit.NewClient(t, kernel)
	client.Get("/x").AssertOk()
	if refused != nil {
		t.Fatalf("a request let through was seen as %v", refused)
	}

	client.Get("/x").AssertTooManyRequests().AssertHeader("Retry-After", "60")
	if refused == nil || refused.Code != http.StatusTooManyRequests || refused.Headers["X-RateLimit-Remaining"] != "0" {
		t.Fatalf("the middleware saw %+v, want the 429 with none remaining", refused)
	}
}
