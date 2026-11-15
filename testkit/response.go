package testkit

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/atobeach/reef/clock"
)

// TestResponse is a recorded response with chainable assertions.
type TestResponse struct {
	t       testing.TB
	status  int
	header  http.Header
	body    []byte
	cookies []*http.Cookie
	decoded any
	aborted bool
	// abortExpected is set by AssertAborted: a response cut off without it
	// fails the test when it ends.
	abortExpected bool
}

// Status is the code the kernel answered with.
func (r *TestResponse) Status() int {
	return r.status
}

// Body is the body as sent, for checks the assertions don't cover.
func (r *TestResponse) Body() string {
	return string(r.body)
}

// Header is a header's first value, "" when it is absent.
func (r *TestResponse) Header(name string) string {
	return r.header.Get(name)
}

// HeaderNames are the response's header names as written, sorted.
func (r *TestResponse) HeaderNames() []string {
	return slices.Sorted(maps.Keys(r.header))
}

// Cookies are every Set-Cookie on the response, in order.
func (r *TestResponse) Cookies() []*http.Cookie {
	return r.cookies
}

// AssertStatus fails unless the status matches.
func (r *TestResponse) AssertStatus(want int) *TestResponse {
	r.t.Helper()

	if got := r.status; got != want {
		r.t.Fatalf("status = %d, want %d; body: %s", got, want, truncate(r.Body()))
	}

	return r
}

// AssertOk is AssertStatus(200).
func (r *TestResponse) AssertOk() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusOK)
}

// AssertNoContent is AssertStatus(204) with an empty body.
func (r *TestResponse) AssertNoContent() *TestResponse {
	r.t.Helper()

	r.AssertStatus(http.StatusNoContent)
	if len(r.body) != 0 {
		r.t.Fatalf("204 body = %s, want empty", truncate(r.Body()))
	}

	return r
}

// AssertNotFound is AssertStatus(404).
func (r *TestResponse) AssertNotFound() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusNotFound)
}

// AssertCreated is AssertStatus(201).
func (r *TestResponse) AssertCreated() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusCreated)
}

// AssertUnauthorized is AssertStatus(401).
func (r *TestResponse) AssertUnauthorized() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusUnauthorized)
}

// AssertForbidden is AssertStatus(403).
func (r *TestResponse) AssertForbidden() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusForbidden)
}

// AssertMethodNotAllowed is AssertStatus(405).
func (r *TestResponse) AssertMethodNotAllowed() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusMethodNotAllowed)
}

// AssertConflict is AssertStatus(409).
func (r *TestResponse) AssertConflict() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusConflict)
}

// AssertGone is AssertStatus(410).
func (r *TestResponse) AssertGone() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusGone)
}

// AssertUnprocessable is AssertStatus(422).
func (r *TestResponse) AssertUnprocessable() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusUnprocessableEntity)
}

// AssertTooManyRequests is AssertStatus(429).
func (r *TestResponse) AssertTooManyRequests() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusTooManyRequests)
}

// AssertInternalServerError is AssertStatus(500).
func (r *TestResponse) AssertInternalServerError() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusInternalServerError)
}

// AssertAborted fails unless the handler cut the response off after it
// started (a stream that panicked): a client sees the connection drop
// instead of the body's end. A response cut off without it fails the test.
func (r *TestResponse) AssertAborted() *TestResponse {
	r.t.Helper()

	r.abortExpected = true
	if !r.aborted {
		r.t.Fatalf("the response completed, want it cut off; body: %s", truncate(r.Body()))
	}

	return r
}

// AssertServiceUnavailable is AssertStatus(503).
func (r *TestResponse) AssertServiceUnavailable() *TestResponse {
	r.t.Helper()

	return r.AssertStatus(http.StatusServiceUnavailable)
}

// AssertRedirect fails unless the response redirects to exactly location
// ("" accepts any redirect).
func (r *TestResponse) AssertRedirect(location string) *TestResponse {
	r.t.Helper()

	if code := r.status; code < 300 || code >= 400 {
		r.t.Fatalf("status = %d, want a redirect; body: %s", code, truncate(r.Body()))
	}

	if got := r.Header("Location"); location != "" && got != location {
		r.t.Fatalf("Location = %q, want %q", got, location)
	}

	return r
}

// AssertJSONValidationErrors fails unless the response is a 422 whose
// `errors` holds every given key.
func (r *TestResponse) AssertJSONValidationErrors(keys ...string) *TestResponse {
	r.t.Helper()

	r.AssertUnprocessable()

	errs := r.validationErrors()
	for _, key := range keys {
		if _, ok := errs[key]; !ok {
			r.t.Fatalf("no validation error for %q in %s", key, truncate(r.Body()))
		}
	}

	return r
}

// AssertJSONValidationError fails unless the response is a 422 whose
// `errors[key]` lists message. The key is taken literally, dots and all, as
// the validator spells a nested field ("passengers.0.last_name").
func (r *TestResponse) AssertJSONValidationError(key string, message string) *TestResponse {
	r.t.Helper()

	r.AssertUnprocessable()

	messages, ok := r.validationErrors()[key].([]any)
	if !ok {
		r.t.Fatalf("no validation error for %q in %s", key, truncate(r.Body()))

		return r
	}

	for _, got := range messages {
		if got == message {
			return r
		}
	}

	r.t.Fatalf("validation errors for %q = %s, want %q", key, mustMarshal(messages), message)

	return r
}

// AssertJSONMissingValidationErrors fails if the body's `errors` holds any
// of keys (taken literally), or with no keys, any error at all.
func (r *TestResponse) AssertJSONMissingValidationErrors(keys ...string) *TestResponse {
	r.t.Helper()

	errs := r.validationErrors()
	if len(keys) == 0 && len(errs) > 0 {
		r.t.Fatalf("validation errors = %s, want none", mustMarshal(errs))
	}

	for _, key := range keys {
		if messages, ok := errs[key]; ok {
			r.t.Fatalf("validation errors for %q = %s, want none", key, mustMarshal(messages))
		}
	}

	return r
}

// validationErrors is the body's `errors` object, nil when it has none.
func (r *TestResponse) validationErrors() map[string]any {
	r.t.Helper()

	body, _ := r.JSON().(map[string]any)
	errs, _ := body["errors"].(map[string]any)

	return errs
}

// AssertBody fails unless the raw body is exactly want.
func (r *TestResponse) AssertBody(want string) *TestResponse {
	r.t.Helper()

	if got := r.Body(); got != want {
		r.t.Fatalf("body = %q, want %q", truncate(got), want)
	}

	return r
}

// Cookie is the first Set-Cookie of that name on the response, nil when
// absent.
func (r *TestResponse) Cookie(name string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == name {
			return c
		}
	}

	return nil
}

// AssertCookie fails unless the response sets the named cookie. A forget
// (Max-Age=0, or an Expires already past) sets nothing.
func (r *TestResponse) AssertCookie(name string) *TestResponse {
	r.t.Helper()

	if c := r.Cookie(name); c == nil || forgets(r.t.Context(), c) {
		r.t.Fatalf("no %s cookie set; Set-Cookie: %q", name, r.header.Values("Set-Cookie"))
	}

	return r
}

// AssertCookieMissing fails if the response sends the named cookie at all,
// a forget included.
func (r *TestResponse) AssertCookieMissing(name string) *TestResponse {
	r.t.Helper()

	if c := r.Cookie(name); c != nil {
		r.t.Fatalf("Set-Cookie %s, want no %s cookie", c, name)
	}

	return r
}

// AssertCookieExpired fails unless the response forgets the named cookie:
// a Set-Cookie that tells the browser to drop it.
func (r *TestResponse) AssertCookieExpired(name string) *TestResponse {
	r.t.Helper()

	if c := r.Cookie(name); c == nil || !forgets(r.t.Context(), c) {
		r.t.Fatalf("%s cookie not expired; Set-Cookie: %q", name, r.header.Values("Set-Cookie"))
	}

	return r
}

// forgets reports whether c tells the browser to drop the cookie. Max-Age
// wins over Expires (RFC 6265 §5.3), so Expires counts only without it.
func forgets(ctx context.Context, c *http.Cookie) bool {
	if c.MaxAge != 0 {
		return c.MaxAge < 0
	}

	return !c.Expires.IsZero() && !c.Expires.After(clock.Now(ctx))
}

// AssertHeader fails unless the header has exactly this value.
func (r *TestResponse) AssertHeader(name string, want string) *TestResponse {
	r.t.Helper()

	if got := r.Header(name); got != want {
		r.t.Fatalf("header %s = %q, want %q", name, got, want)
	}

	return r
}

// AssertHeaderMissing fails if the header is present.
func (r *TestResponse) AssertHeaderMissing(name string) *TestResponse {
	r.t.Helper()

	if got := r.header.Values(name); len(got) > 0 {
		r.t.Fatalf("header %s = %q, want it absent", name, got)
	}

	return r
}

// AssertJSON fails unless the body equals want once both are decoded, so key
// order and number formatting don't matter.
func (r *TestResponse) AssertJSON(want any) *TestResponse {
	r.t.Helper()

	if got, expected := r.JSON(), normalize(r.t, want); !reflect.DeepEqual(got, expected) {
		r.t.Fatalf("json = %s\nwant   %s", mustMarshal(got), mustMarshal(expected))
	}

	return r
}

// AssertJSONPath checks one value by dotted path ("0.slug", "items.2.iata").
func (r *TestResponse) AssertJSONPath(path string, want any) *TestResponse {
	r.t.Helper()

	got, ok := lookup(r.JSON(), path)
	if !ok {
		r.t.Fatalf("json path %q missing in %s", path, truncate(r.Body()))
	}

	if expected := normalize(r.t, want); !reflect.DeepEqual(got, expected) {
		r.t.Fatalf("json path %q = %s, want %s", path, mustMarshal(got), mustMarshal(expected))
	}

	return r
}

// AssertJSONCount checks the length of the array at path ("" for the root).
func (r *TestResponse) AssertJSONCount(want int, path string) *TestResponse {
	r.t.Helper()

	value, ok := lookup(r.JSON(), path)
	list, isList := value.([]any)
	if !ok || !isList {
		r.t.Fatalf("json path %q is not an array in %s", path, truncate(r.Body()))
	}

	if len(list) != want {
		r.t.Fatalf("json path %q has %d items, want %d", path, len(list), want)
	}

	return r
}

// JSON decodes the body (once).
func (r *TestResponse) JSON() any {
	r.t.Helper()

	if r.decoded == nil {
		if err := json.Unmarshal(r.body, &r.decoded); err != nil {
			r.t.Fatalf("body is not JSON: %v; body: %s", err, truncate(r.Body()))
		}
	}

	return r.decoded
}

// Value is the decoded body's value at a dotted path ("cities.0.label"),
// nil when the path is missing.
func (r *TestResponse) Value(path string) any {
	r.t.Helper()

	return valueAt(r.JSON(), path)
}

// Pluck is the key of every object in the array at path, in order.
func (r *TestResponse) Pluck(path string, key string) []any {
	r.t.Helper()

	list, ok := r.Value(path).([]any)
	if !ok {
		r.t.Fatalf("json path %q is not an array in %s", path, truncate(r.Body()))
	}

	plucked := make([]any, len(list))
	for i, item := range list {
		plucked[i] = valueAt(item, key)
	}

	return plucked
}

// valueAt reads a dotted path ("passengers.0.firstName") of a decoded JSON
// value, nil when the path is missing.
func valueAt(value any, path string) any {
	found, _ := lookup(value, path)

	return found
}

func lookup(value any, path string) (any, bool) {
	if path == "" {
		return value, true
	}

	for segment := range strings.SplitSeq(path, ".") {
		switch node := value.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil, false
			}
			value = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			value = node[index]
		default:
			return nil, false
		}
	}

	return value, true
}

// EqualJSON reports whether got and want are equal once each is
// round-tripped through JSON, as AssertJSON compares: an int, a float64 and
// a json.Number of one value are equal, and a struct equals its map.
func EqualJSON(t testing.TB, got any, want any) bool {
	t.Helper()

	return reflect.DeepEqual(normalize(t, got), normalize(t, want))
}

// normalize round-trips an expectation through JSON so Go values compare
// equal to decoded ones (int vs float64, structs vs maps).
func normalize(t testing.TB, value any) any {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("expectation is not JSON-encodable: %v", err)
	}

	var decoded any
	_ = json.Unmarshal(raw, &decoded)

	return decoded
}

func mustMarshal(value any) string {
	raw, _ := json.Marshal(value)

	return string(raw)
}

func truncate(s string) string {
	if len(s) > 500 {
		return s[:500] + "…"
	}

	return s
}
