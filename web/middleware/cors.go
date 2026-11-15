package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/atobeach/reef/web"
)

// CorsConfig is which paths answer cross-origin requests, and for whom.
type CorsConfig struct {
	Paths               []string
	AllowedOrigins      []string
	SupportsCredentials bool
	// MaxAge is sent on preflights; nil omits the header.
	MaxAge *int
}

// Cors answers cross-origin requests for explicit origins, any method and
// header, with credentials. Preflights are answered here with a 204 and
// never reach routing.
func Cors(config CorsConfig) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		if !c.Is(config.Paths...) {
			return next(c)
		}

		origin := c.Header("Origin")
		allowed := origin != "" && slices.Contains(config.AllowedOrigins, origin)

		if c.Method() == http.MethodOptions && c.Header("Access-Control-Request-Method") != "" {
			return preflight(c, config, origin, allowed), nil
		}

		res, err := web.ToResponse(next(c))
		if allowed {
			res.WithHeader("Access-Control-Allow-Origin", origin)
			if config.SupportsCredentials {
				res.WithHeader("Access-Control-Allow-Credentials", "true")
			}
		}

		// Explicit origins make the response origin-dependent, so caches
		// (Cloudflare included) must key on it — even with no Origin header.
		vary(res, "Origin")

		return res, err
	}
}

// preflight answers an OPTIONS preflight, echoing the requested method and
// headers (any are allowed).
func preflight(c *web.Context, config CorsConfig, origin string, allowed bool) *web.Response {
	res := web.NoContent()
	if allowed {
		res.WithHeader("Access-Control-Allow-Origin", origin)
	}

	if config.SupportsCredentials {
		res.WithHeader("Access-Control-Allow-Credentials", "true")
	}

	res.WithHeader("Access-Control-Allow-Methods", strings.ToUpper(c.Header("Access-Control-Request-Method")))
	if headers := c.Header("Access-Control-Request-Headers"); headers != "" {
		res.WithHeader("Access-Control-Allow-Headers", headers)
	}

	if config.MaxAge != nil {
		res.WithHeader("Access-Control-Max-Age", strconv.Itoa(*config.MaxAge))
	}

	vary(res, "Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers")

	return res
}

// vary merges names into one comma-joined Vary header, keeping any value a
// handler already set.
func vary(res *web.Response, names ...string) {
	values := strings.Split(res.Header("Vary"), ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}

	for _, name := range names {
		if !slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(v, name) }) {
			values = append(values, name)
		}
	}

	res.WithHeader("Vary", strings.Join(slices.DeleteFunc(values, func(v string) bool { return v == "" }), ", "))
}
