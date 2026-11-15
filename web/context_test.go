package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestNewContextIsARequestWithAnEmptyBody(t *testing.T) {
	c := NewContext(t.Context(), http.MethodPost, "/api/chat?thread=t1")

	if c.Method() != http.MethodPost || c.Path() != "api/chat" || c.Query("thread") != "t1" {
		t.Fatalf("request = %s %s ?thread=%s", c.Method(), c.Path(), c.Query("thread"))
	}

	if got := c.FullURL(); got != "http://example.com/api/chat?thread=t1" {
		t.Fatalf("FullURL = %s, want the example.com host", got)
	}

	if body, err := c.Body(); len(body) != 0 || err != nil {
		t.Fatalf("Body = %q, %v; want no bytes", body, err)
	}
}

func TestNewContextPanicsOnATargetNetHTTPCannotParse(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewContext accepted a malformed target")
		}
	}()

	NewContext(t.Context(), http.MethodGet, "/%zz")
}

// A header sent empty is present; Header can't tell it from an absent one.
func TestHasHeaderTellsAnEmptyHeaderFromAnAbsentOne(t *testing.T) {
	c := NewContext(t.Context(), http.MethodGet, "/")
	c.SetHeader("X-Empty", "")

	if !c.HasHeader("x-empty") || c.Header("X-Empty") != "" {
		t.Fatalf("HasHeader = %v, Header = %q; want present and empty", c.HasHeader("x-empty"), c.Header("X-Empty"))
	}

	if c.HasHeader("X-Absent") {
		t.Fatal("an absent header is present")
	}
}

// ResolveFlyClientIP pins X-Forwarded-For before anything reads the client
// IP: the pin and its removal act on every line, so a forged second line
// can't survive either, and IP is resolved again on every call.
func TestSetHeaderAndRemoveHeaderActOnEveryLineOfTheClientIP(t *testing.T) {
	for _, tt := range []struct {
		name    string
		rewrite func(c *Context)
		want    string
		lines   []string
	}{
		{"SetHeader", func(c *Context) { c.SetHeader("X-Forwarded-For", "198.51.100.7") }, "198.51.100.7", []string{"198.51.100.7"}},
		{"RemoveHeader", func(c *Context) { c.RemoveHeader("X-Forwarded-For") }, "192.0.2.1", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Add("X-Forwarded-For", "10.0.0.1")
			r.Header.Add("X-Forwarded-For", "6.6.6.6")
			c := newContext(r, ParseTrustedProxies([]string{"*"}))

			if got := c.IP(); got != "6.6.6.6" {
				t.Fatalf("IP = %s, want the forged rightmost line", got)
			}

			tt.rewrite(c)

			if got := c.IP(); got != tt.want {
				t.Fatalf("IP = %s, want %s", got, tt.want)
			}

			if lines := r.Header.Values("X-Forwarded-For"); !slices.Equal(lines, tt.lines) {
				t.Fatalf("X-Forwarded-For lines = %q, want %q", lines, tt.lines)
			}

			if c.HasHeader("X-Forwarded-For") != (tt.lines != nil) {
				t.Fatalf("HasHeader = %v after %s", c.HasHeader("X-Forwarded-For"), tt.name)
			}
		})
	}
}

// The body is read once and kept: what All parses is what a webhook signs.
func TestBodyIsTheRawBytesBeforeAndAfterAllParsedThem(t *testing.T) {
	const raw = `{"email":"  a@b.co  "}`
	c := jsonRequest(t, "/", raw)

	if body, err := c.Body(); string(body) != raw || err != nil {
		t.Fatalf("Body = %q, %v", body, err)
	}

	if input, err := c.All(); err != nil || input["email"] != "a@b.co" {
		t.Fatalf("All = %v, %v", input, err)
	}

	if body, err := c.Body(); string(body) != raw || err != nil {
		t.Fatalf("Body after All = %q, %v", body, err)
	}
}

func TestABodyOverTheCapIsA413OnEveryCall(t *testing.T) {
	c := jsonRequest(t, "/", oversized())

	for range 2 {
		body, err := c.Body()

		var httpErr *Error
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("Body error = %v, want a 413", err)
		}

		if len(body) != maxBodyBytes+1 || !bytes.HasPrefix([]byte(oversized()), body) {
			t.Fatalf("Body = %d bytes, want the first %d", len(body), maxBodyBytes+1)
		}
	}
}

// A broken body (a chunked upload cut off) is its error on every call, with
// the bytes read before it, and All renders that error rather than parsing
// the fragment as input.
func TestABodyThatFailsToReadIsItsErrorOnEveryCall(t *testing.T) {
	errCut := errors.New("connection reset")
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/",
		io.MultiReader(strings.NewReader(`{"a":`), iotest.ErrReader(errCut)))
	r.Header.Set("Content-Type", "application/json")
	c := newContext(r, nil)

	for range 2 {
		if body, err := c.Body(); string(body) != `{"a":` || !errors.Is(err, errCut) {
			t.Fatalf("Body = %q, %v; want the fragment and the read error", body, err)
		}
	}

	if input, err := c.All(); input != nil || !errors.Is(err, errCut) {
		t.Fatalf("All = %v, %v; want the read error", input, err)
	}
}

type boundKey struct{}

type unboundKey struct{}

// A context derived from Context, never from c, reads back through c, and a
// key bound nowhere ends at the root instead of looking itself up forever.
func TestAContextDerivedFromContextIsBoundToTheRequest(t *testing.T) {
	c := NewContext(t.Context(), http.MethodGet, "/")

	c.WithContext(context.WithValue(c.Context(), boundKey{}, "bound"))

	if got := c.Value(boundKey{}); got != "bound" {
		t.Fatalf("Value = %v, want the bound value", got)
	}

	if got := c.Value(unboundKey{}); got != nil {
		t.Fatalf("Value of an unbound key = %v", got)
	}
}

// panicked runs fn and hands back what it panicked with, nil when it
// returned.
func panicked(fn func()) (recovered any) {
	defer func() { recovered = recover() }()

	fn()

	return nil
}

// A context derived from c would make c its own parent, and the next lookup
// of any other key would overflow the stack, which no recover catches: the
// bind panics instead, and c keeps what it had.
func TestAContextDerivedFromTheRequestItselfCannotBeBound(t *testing.T) {
	for _, tt := range []struct {
		name   string
		derive func(t *testing.T, c *Context) context.Context
	}{
		{"WithValue", func(_ *testing.T, c *Context) context.Context {
			return context.WithValue(c, unboundKey{}, "cycle")
		}},
		{"WithoutCancel", func(_ *testing.T, c *Context) context.Context { return context.WithoutCancel(c) }},
		{"WithCancel", func(t *testing.T, c *Context) context.Context {
			t.Helper()

			ctx, cancel := context.WithCancel(c)
			t.Cleanup(cancel)

			return ctx
		}},
		{"WithValue over a Context built on it", func(_ *testing.T, c *Context) context.Context {
			return context.WithValue(NewContext(c, http.MethodGet, "/"), unboundKey{}, "cycle")
		}},
		{"nil", func(*testing.T, *Context) context.Context { return nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := NewContext(t.Context(), http.MethodGet, "/")
			c.WithContext(context.WithValue(c.Context(), boundKey{}, "before"))

			// Fail before anything reads c: had the bind gone through, the
			// next lookup would never return.
			if ctx := tt.derive(t, c); panicked(func() { c.WithContext(ctx) }) == nil {
				t.Fatalf("WithContext bound the %s context", tt.name)
			}

			if got := c.Value(boundKey{}); got != "before" {
				t.Fatalf("Value = %v, want the value bound before", got)
			}
		})
	}
}

// Only a context derived from c itself is refused: one from c.Context(), or
// one over another request's Context, binds.
func TestAContextFromContextOrAnotherRequestBinds(t *testing.T) {
	c := NewContext(t.Context(), http.MethodGet, "/")
	other := NewContext(t.Context(), http.MethodGet, "/other")

	for _, ctx := range []context.Context{
		context.WithValue(c.Context(), boundKey{}, "from Context"),
		context.WithValue(other, boundKey{}, "over another request"),
	} {
		if recovered := panicked(func() { c.WithContext(ctx) }); recovered != nil {
			t.Fatalf("WithContext refused %v: %v", ctx.Value(boundKey{}), recovered)
		}

		if got := c.Value(boundKey{}); got != ctx.Value(boundKey{}) {
			t.Fatalf("Value = %v, want %v", got, ctx.Value(boundKey{}))
		}
	}
}

// A goroutine handed c (cache.Flexible's refresh) reads it while the request
// goroutine binds: under -race, a plain field would be a data race.
func TestTheContextMethodsAreSafeFromAnyGoroutine(t *testing.T) {
	c := NewContext(t.Context(), http.MethodGet, "/")
	stop := make(chan struct{})
	read := make(chan struct{})

	go func() {
		defer close(read)

		for {
			_ = c.Value(boundKey{})
			_ = c.Done()
			_ = c.Err()
			_, _ = c.Deadline()

			select {
			case <-stop:
				return
			default:
			}
		}
	}()

	for i := range 1000 {
		c.WithContext(context.WithValue(c.Context(), boundKey{}, i))
	}

	close(stop)
	<-read

	if got := c.Value(boundKey{}); got != 999 {
		t.Fatalf("Value = %v, want the last bind", got)
	}
}

// c is the request as it is now: a context derived from c sees a later
// bind's values, and the snapshot c.Context() gave before it doesn't. Its
// cancellation is the one bound when it was derived: a WithoutCancel bound
// after it doesn't keep it running.
func TestAContextDerivedFromTheRequestFollowsLaterBinds(t *testing.T) {
	disconnected, disconnect := context.WithCancel(t.Context())
	c := NewContext(disconnected, http.MethodGet, "/")
	derived := context.WithValue(c, unboundKey{}, "derived")
	cancellable, stop := context.WithCancel(c)
	defer stop()
	snapshot := c.Context()

	c.WithContext(context.WithValue(context.WithoutCancel(c.Context()), boundKey{}, "later"))
	disconnect()

	if c.Err() != nil || cancellable.Err() == nil {
		t.Fatalf("c.Err = %v, derived-before Err = %v; want c past the disconnect and the earlier context cancelled", c.Err(), cancellable.Err())
	}

	if got := derived.Value(boundKey{}); got != "later" {
		t.Fatalf("derived Value = %v, want the later bind", got)
	}

	if got := snapshot.Value(boundKey{}); got != nil {
		t.Fatalf("snapshot Value = %v, want nothing bound after it", got)
	}
}

// One malformed third-party cookie must never sign a visitor out: it is
// skipped, and every other cookie is kept as sent, in order.
func TestRawCookiesSkipAMalformedCookieAndKeepTheRestInOrder(t *testing.T) {
	c := NewContext(t.Context(), http.MethodGet, "/")
	c.SetHeader("Cookie", `theme=dark; b@d=1; theme=light; quote=x"y; visitor_id=token`)

	var got []string
	for _, cookie := range c.RawCookies() {
		got = append(got, cookie.Name+"="+cookie.Value)
	}

	if want := []string{"theme=dark", "theme=light", "visitor_id=token"}; !slices.Equal(got, want) {
		t.Fatalf("RawCookies = %v, want %v", got, want)
	}
}
