// Package middleware holds the framework's stock middleware: rate limiting,
// CORS, cookie encryption and CSRF protection.
package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/atobeach/reef/ratelimit"
	"github.com/atobeach/reef/web"
)

// RateLimitersConfig configures the limiter registry.
type RateLimitersConfig struct {
	Store ratelimit.Store
	// UserID, when set, keys PerMinute limits on the signed-in user rather
	// than the IP.
	UserID func(c *web.Context) *int64
}

// RateLimiters holds the named limiter definitions routes throttle with.
type RateLimiters struct {
	config  RateLimitersConfig
	limiter map[string]func(c *web.Context) []ratelimit.Limit
}

// NewRateLimiters returns an empty registry.
func NewRateLimiters(config RateLimitersConfig) *RateLimiters {
	return &RateLimiters{config: config, limiter: map[string]func(c *web.Context) []ratelimit.Limit{}}
}

// For defines a named limiter with one limit.
func (l *RateLimiters) For(name string, limit func(c *web.Context) ratelimit.Limit) {
	l.limiter[name] = func(c *web.Context) []ratelimit.Limit { return []ratelimit.Limit{limit(c)} }
}

// ForAll defines a named limiter with several limits, all enforced — a
// per-visitor bucket under a wider per-IP ceiling.
func (l *RateLimiters) ForAll(name string, limits func(c *web.Context) []ratelimit.Limit) {
	l.limiter[name] = limits
}

// Throttle enforces the named limiter. An undefined name panics at route
// registration, so a typo can never ship as an unthrottled route.
func (l *RateLimiters) Throttle(name string) web.Middleware {
	define, ok := l.limiter[name]
	if !ok {
		panic(fmt.Sprintf("rate limiter %q is not defined", name))
	}

	return l.throttle(func(c *web.Context) []ratelimit.Limit {
		limits := define(c)
		for i := range limits {
			limits[i].Key = bucket(name, limits[i].Key)
		}

		return limits
	})
}

// PerMinute allows n hits a minute, keyed on the signed-in user or else the
// client IP. Every PerMinute route shares that one bucket per caller,
// whatever its n.
func (l *RateLimiters) PerMinute(n int) web.Middleware {
	return l.throttle(func(c *web.Context) []ratelimit.Limit {
		caller := "ip:" + c.IP()
		if l.config.UserID != nil {
			if id := l.config.UserID(c); id != nil {
				caller = "user:" + strconv.FormatInt(*id, 10)
			}
		}

		return []ratelimit.Limit{ratelimit.PerMinute(n).By(bucket("per-minute", caller))}
	})
}

// bucket is a limit's store key. The caller's key is hashed: it can hold an
// email address (the login limiter) and is as long as the request makes it.
func bucket(limiter string, key string) string {
	sum := sha256.Sum256([]byte(key))

	return limiter + ":" + hex.EncodeToString(sum[:])
}

// throttle checks each limit before counting it, so a rejected request
// isn't a hit on the limit that rejected it (it still counts on those
// before). It fails closed: limiters guard paid upstream spend and money
// routes, so a store outage must not lift them.
func (l *RateLimiters) throttle(resolve func(c *web.Context) []ratelimit.Limit) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		limits := resolve(c)

		for _, limit := range limits {
			hits, resetIn, err := l.config.Store.Attempts(c, limit.Key)
			if err != nil {
				return nil, fmt.Errorf("throttle: %w", err)
			}

			if hits >= int64(limit.MaxAttempts) {
				return nil, web.TooManyRequests(c, limit.MaxAttempts, resetIn)
			}

			if _, err := l.config.Store.Hit(c, limit.Key, limit.Decay); err != nil {
				return nil, fmt.Errorf("throttle: %w", err)
			}
		}

		res, err := web.ToResponse(next(c))
		if reportErr := l.reportRemaining(c, res, limits); reportErr != nil {
			return nil, reportErr
		}

		return res, err
	}
}

// reportRemaining sets X-RateLimit-Limit/Remaining from the limit with the
// fewest attempts left (the first, on a tie): a per-search 120 under a
// per-IP 480 reports the 120.
func (l *RateLimiters) reportRemaining(c *web.Context, res *web.Response, limits []ratelimit.Limit) error {
	reported := int64(-1)
	for _, limit := range limits {
		hits, _, err := l.config.Store.Attempts(c, limit.Key)
		if err != nil {
			return fmt.Errorf("throttle: %w", err)
		}

		remaining := max(0, int64(limit.MaxAttempts)-hits)
		if reported >= 0 && reported <= remaining {
			continue
		}

		reported = remaining
		res.WithHeader("X-RateLimit-Limit", strconv.Itoa(limit.MaxAttempts))
		res.WithHeader("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
	}

	return nil
}
