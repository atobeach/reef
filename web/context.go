// Package web is the routing and request layer: named routes, route
// groups, a middleware pipeline and a request Context.
//
// A route's action returns its own type and an error. The value is sent as
// JSON (a *Response as built); the error, wherever it arose, reaches every
// middleware as the error it is and is rendered once, at the kernel's edge:
// an Abort, a missing model and an unexpected failure all render as
// `{"message": ...}` with their status, the shape Laravel renders.
package web

import (
	"context"
	"io"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/atobeach/reef/validation"
)

// Context is the request, and a context.Context carrying its deadline and
// bound services (the db connection), so models and services take it directly:
//
//	models.SavedSearches(c).ResolveRouteBinding(c.Param("savedSearch"))
//
// c is the request as it is now: its context methods answer for whatever is
// bound when they are asked, so a later WithContext shows through them, and
// through the values of any context derived from c. Cancellation is read
// once: a context derived with WithCancel, WithTimeout or AfterFunc takes
// c's Done and Deadline when it is made and keeps that cancellation, and a
// Done channel read before a bind is not the one after it, so derive one
// after any bind it depends on (a WithoutCancel included). c.Context() is a
// snapshot. Hand work that outlives the handler (a goroutine, a streamed
// turn) c.Context().
//
// The context methods are safe from any goroutine. The rest (Body, All,
// SetHeader, QueueCookie, Terminating…) belong to the request's goroutine,
// as *http.Request's do: Body and All cache what they read, the way
// Request.ParseForm caches into r.Form.
type Context struct {
	request *http.Request
	bound   atomic.Pointer[boundContext] // what WithContext bound last

	proxies *TrustedProxies
	route   *string // the matched route's name; nil before routing or on a miss

	cookies       map[string]string // the cookies middleware's decrypted view; nil before it runs
	queuedCookies []*http.Cookie
	body          []byte // Body, read once
	bodyErr       error  // Body's failure, returned with the bytes read before it
	bodyRead      bool
	input         validation.Input // All, parsed once
	inputErr      error            // All's failure, returned by every later call
	terminating   []func()         // Terminating's callbacks, in registration order
}

// boundContext boxes a bound context: atomic.Value refuses a store whose
// concrete type differs from the first.
type boundContext struct{ ctx context.Context }

// selfKey is what c's Value answers with c itself, so WithContext can tell
// a context derived from c, which would make c its own parent.
type selfKey struct{ c *Context }

func newContext(r *http.Request, proxies *TrustedProxies) *Context {
	c := &Context{request: r, proxies: proxies}
	c.bound.Store(&boundContext{ctx: r.Context()})

	return c
}

// NewContext builds a request outside the kernel, for unit tests: Host
// example.com, no peer, an empty body. An unparsable method or target panics.
func NewContext(ctx context.Context, method, target string) *Context {
	r, err := http.NewRequestWithContext(ctx, method, target, http.NoBody)
	if err != nil {
		panic("web: NewContext: " + err.Error())
	}

	if r.Host == "" {
		r.Host = "example.com"
	}

	return newContext(r, nil)
}

// Deadline implements context.Context.
func (c *Context) Deadline() (time.Time, bool) { return c.Context().Deadline() }

// Done implements context.Context.
func (c *Context) Done() <-chan struct{} { return c.Context().Done() }

// Err implements context.Context.
func (c *Context) Err() error { return c.Context().Err() }

// Value implements context.Context.
func (c *Context) Value(key any) any {
	if self, ok := key.(selfKey); ok && self.c == c {
		return c
	}

	return c.Context().Value(key)
}

// Context is the context.Context WithContext bound so far, without c
// wrapped around it: what to derive a context to bind from, and what to
// hand work that outlives the handler.
func (c *Context) Context() context.Context {
	return c.bound.Load().ctx
}

// WithContext binds ctx in place of the request's context — how middleware
// binds request scoped services and facts for everything downstream, and
// upstream once next returns. Derive ctx from c.Context(): one derived from
// c itself would make c its own parent, so it panics here rather than
// overflow the stack at the next lookup.
//
//	c.WithContext(db.WithConn(c.Context(), tx))
func (c *Context) WithContext(ctx context.Context) {
	if ctx == nil {
		panic("web: WithContext with a nil context")
	}

	if ctx.Value(selfKey{c}) != nil {
		panic("web: WithContext with a context derived from the request itself; derive it from c.Context()")
	}

	c.bound.Store(&boundContext{ctx: ctx})
}

// Param is a route parameter: `{slug}` in `/countries/{slug}`.
func (c *Context) Param(name string) string {
	return c.request.PathValue(name)
}

// Query is a query-string value, "" when absent.
func (c *Context) Query(name string) string {
	return c.request.URL.Query().Get(name)
}

// Header is a request header's first value, "" when absent.
func (c *Context) Header(name string) string {
	return c.request.Header.Get(name)
}

// HasHeader reports whether the request carries the header, even empty.
func (c *Context) HasHeader(name string) bool {
	return len(c.request.Header.Values(name)) > 0
}

// SetHeader replaces every line of a request header, so a middleware's
// rewrite leaves no forged second X-Forwarded-For line for c.IP() to read.
func (c *Context) SetHeader(name, value string) {
	c.request.Header.Set(name, value)
}

// RemoveHeader removes every line of a request header.
func (c *Context) RemoveHeader(name string) {
	c.request.Header.Del(name)
}

// maxBodyBytes caps a request body read into memory.
const maxBodyBytes = 8 << 20

// Body is the raw request body, read once and kept: what All parses and a
// webhook signs. Over the 8 MiB cap it is a 413; as with io.ReadAll, an error
// comes back with the bytes read before it.
func (c *Context) Body() ([]byte, error) {
	if !c.bodyRead {
		c.bodyRead = true
		c.body, c.bodyErr = c.readBody()
	}

	return c.body, c.bodyErr
}

func (c *Context) readBody() ([]byte, error) {
	if c.request.Body == nil {
		return nil, nil
	}

	raw, err := io.ReadAll(io.LimitReader(c.request.Body, maxBodyBytes+1))
	if err != nil {
		return raw, err
	}

	if len(raw) > maxBodyBytes {
		return raw, Abort(http.StatusRequestEntityTooLarge, "")
	}

	return raw, nil
}

// Cookie is a cookie's value, "" when absent — decrypted when the cookies
// middleware ran, raw for the names it leaves unencrypted.
func (c *Context) Cookie(name string) string {
	if c.cookies != nil {
		return c.cookies[name]
	}

	cookie, err := c.request.Cookie(name)
	if err != nil {
		return ""
	}

	return cookie.Value
}

// RawCookies are the request's cookies as sent, in order, before the cookies
// middleware decrypts them: net/http's lenient reader, so a malformed cookie
// is skipped, never the whole header.
func (c *Context) RawCookies() []*http.Cookie {
	return c.request.Cookies()
}

// SetCookies replaces the readable cookie values (the cookies middleware's
// decrypted view of the request).
func (c *Context) SetCookies(values map[string]string) {
	c.cookies = values
}

// QueueCookie attaches a cookie to whatever response this request ends up
// with, for code that doesn't build the response.
func (c *Context) QueueCookie(cookie *http.Cookie) {
	c.queuedCookies = append(c.queuedCookies, cookie)
}

// QueuedCookies are the cookies queued so far, which the cookies middleware
// attaches to the response.
func (c *Context) QueuedCookies() []*http.Cookie {
	return c.queuedCookies
}

// RouteMatched reports whether a route is handling this request.
func (c *Context) RouteMatched() bool {
	return c.route != nil
}

// RouteName is the matched route's name ("" before routing or when unnamed).
func (c *Context) RouteName() string {
	if c.route == nil {
		return ""
	}

	return *c.route
}

// FullURL is the request URL: scheme and host as the client used them
// (through trusted proxies), path and query.
func (c *Context) FullURL() string {
	scheme := "http"
	if c.IsSecure() {
		scheme = "https"
	}

	host := c.request.Host
	if forwarded := c.Header("X-Forwarded-Host"); forwarded != "" && c.proxies.trusts(peerIP(c.request), peerIP(c.request)) {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}

	url := scheme + "://" + host + c.request.URL.Path
	if c.request.URL.RawQuery != "" {
		url += "?" + c.request.URL.RawQuery
	}

	return url
}

// Root is the site root: scheme and host as the client used them, no
// trailing slash — where a redirect to "/" points.
func (c *Context) Root() string {
	full := c.FullURL()
	scheme, rest, _ := strings.Cut(full, "://")
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")

	return scheme + "://" + host
}

// WantsJSON reports whether the first Accept type is JSON.
func (c *Context) WantsJSON() bool {
	first, _, _ := strings.Cut(c.Header("Accept"), ",")

	return strings.Contains(first, "/json") || strings.Contains(first, "+json")
}

// ExpectsJSON reports an XHR that accepts anything, or a request that wants
// JSON — a caller to answer with JSON rather than a redirect.
func (c *Context) ExpectsJSON() bool {
	if c.IsXHR() && c.Header("X-PJAX") == "" {
		if accept := c.Header("Accept"); accept == "" || strings.HasPrefix(strings.TrimSpace(accept), "*") {
			return true
		}
	}

	return c.WantsJSON()
}

// IsXHR reports an XMLHttpRequest (X-Requested-With).
func (c *Context) IsXHR() bool {
	return c.Header("X-Requested-With") == "XMLHttpRequest"
}

// Method is the HTTP verb.
func (c *Context) Method() string {
	return c.request.Method
}

// Path is the path without the surrounding slashes ("api/countries"; "/"
// for the root) — the form Is patterns and error messages use.
func (c *Context) Path() string {
	trimmed := strings.Trim(c.request.URL.Path, "/")
	if trimmed == "" {
		return "/"
	}

	return trimmed
}

// Is matches Path against shell patterns ("api/*"). Unlike path.Match, `*`
// also crosses slashes, so "api/*" covers every API route.
func (c *Context) Is(patterns ...string) bool {
	current := c.Path()
	for _, pattern := range patterns {
		if matchLoose(strings.Trim(pattern, "/"), current) {
			return true
		}
	}

	return false
}

// IP is the client address, resolved through the trusted proxies. It keys
// the per-IP rate-limit buckets.
func (c *Context) IP() string {
	return c.proxies.clientIP(c.request)
}

// IsSecure reports HTTPS, trusting X-Forwarded-Proto only from a trusted peer.
func (c *Context) IsSecure() bool {
	return c.proxies.isSecure(c.request)
}

// Terminating registers fn to run once the request is done: after the
// response is written, or when a middleware discarded it, even after a
// panic. Last registered runs first, as t.Cleanup does. Keep it to cleanup:
// a buffered body's last bytes may still be waiting for net/http's final
// flush.
func (c *Context) Terminating(fn func()) {
	c.terminating = append(c.terminating, fn)
}

func matchLoose(pattern string, subject string) bool {
	if pattern == subject {
		return true
	}

	// Escape path.Match's other metacharacters, then let `*` span segments by
	// matching against a slash-free rendition of both sides.
	const sep = "\x00"
	pattern = strings.NewReplacer("[", `\[`, "?", `\?`, "/", sep).Replace(pattern)
	matched, err := path.Match(pattern, strings.ReplaceAll(subject, "/", sep))

	return err == nil && matched
}
