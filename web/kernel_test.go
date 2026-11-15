package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/atobeach/reef/panics"
	"github.com/atobeach/reef/testkit"
)

// serve sends one request through a kernel on router and middleware.
func serve(t *testing.T, router *Router, middleware []Middleware, method string, path string) *testkit.TestResponse {
	t.Helper()

	kernel, err := NewKernel(nil, KernelConfig{Router: router, Middleware: middleware})
	if err != nil {
		t.Fatal(err)
	}

	return testkit.NewClient(t, kernel).Call(method, path)
}

// assertHeader asserts a table case's header: absent when want is "".
func assertHeader(t *testing.T, res *testkit.TestResponse, name string, want string) {
	t.Helper()

	if want == "" {
		res.AssertHeaderMissing(name)

		return
	}

	res.AssertHeader(name, want)
}

func TestMiddlewareRunsGlobalThenGroupThenRouteThenHandler(t *testing.T) {
	var order []string
	tag := func(name string) Middleware {
		return func(c *Context, next Handler) (any, error) {
			order = append(order, name+":before")
			value, err := next(c)
			order = append(order, name+":after")

			return value, err
		}
	}

	router := NewRouter()
	router.Prefix("/api").Middleware(tag("group")).Group(func(r *Router) {
		r.Get("/things/{id}", func(c *Context) (any, error) {
			order = append(order, "handler:"+c.Param("id"))

			return map[string]string{"id": c.Param("id")}, nil
		}).Middleware(tag("route"))
	})

	res := serve(t, router, []Middleware{tag("global")}, "GET", "/api/things/42")

	want := "global:before group:before route:before handler:42 route:after group:after global:after"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("order = %s", got)
	}

	if res.Status() != 200 || res.Body() != `{"id":"42"}` || res.Header("Content-Type") != "application/json" {
		t.Fatalf("response = %d %s %s", res.Status(), res.Header("Content-Type"), res.Body())
	}
}

// A handler returns the value: a *Response goes out as built, anything else
// as JSON, a string and a typed nil included.
func TestAReturnedValueRendersByItsType(t *testing.T) {
	cases := []struct {
		name        string
		value       any
		status      int
		contentType string
		body        string
	}{
		{"a string", "OK", 200, "application/json", `"OK"`},
		{"an HTML response", HTML("<p>OK</p>"), 200, "text/html; charset=UTF-8", "<p>OK</p>"},
		{"a nil slice", []int(nil), 200, "application/json", "null"},
		{"a map", map[string]bool{"two_factor": true}, 200, "application/json", `{"two_factor":true}`},
		{"a struct", struct {
			ID string `json:"id"`
		}{"42"}, 200, "application/json", `{"id":"42"}`},
		{"a response", JSON(map[string]string{"id": "42"}).WithStatus(201), 201, "application/json", `{"id":"42"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			router.Get("/value", func(*Context) (any, error) { return tc.value, nil })

			serve(t, router, nil, "GET", "/value").
				AssertStatus(tc.status).
				AssertHeader("Content-Type", tc.contentType).
				AssertBody(tc.body)
		})
	}
}

// A string is JSON, escaped as JSON: a parameter echoed back never goes out
// as markup.
func TestAStringIsJSONNeverHTML(t *testing.T) {
	router := NewRouter()
	router.Get("/echo/{name}", func(c *Context) (string, error) { return c.Param("name"), nil })

	serve(t, router, nil, "GET", "/echo/%3Cscript%3Ealert(1)%3C%2Fscript%3E").
		AssertOk().
		AssertHeader("Content-Type", "application/json").
		AssertBody(`"\u003cscript\u003ealert(1)\u003c/script\u003e"`)
}

// An outcome of nothing, from an action or a middleware, is a bug: a
// reported 500, never an empty 200 or a dropped connection, with what a
// decorating middleware put on it.
func TestAnAnswerOfNothingIsAServerError(t *testing.T) {
	stamp := func(c *Context, next Handler) (any, error) {
		res, err := ToResponse(next(c))

		return res.WithHeader("X-Stamp", "yes"), err
	}

	cases := []struct {
		name       string
		route      func(r *Router) *Route
		middleware []Middleware
		stamp      string
	}{
		{"an action answering nil", func(r *Router) *Route {
			return r.Get("/x", func(*Context) (any, error) { return nil, nil })
		}, nil, ""},
		{"an action answering a nil response", func(r *Router) *Route {
			return r.Get("/x", func(*Context) (*Response, error) { return nil, nil })
		}, nil, ""},
		{"a middleware answering nil", func(r *Router) *Route {
			return r.Get("/x", func(*Context) (string, error) { return "unreached", nil }).
				Middleware(func(*Context, Handler) (any, error) { return nil, nil })
		}, nil, ""},
		{"a decorated action answering nil", func(r *Router) *Route {
			return r.Get("/x", func(*Context) (any, error) { return nil, nil })
		}, []Middleware{stamp}, "yes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			tc.route(router)

			client, reports := reportingKernel(t, router, tc.middleware...)
			res := client.Get("/x").AssertInternalServerError().AssertBody(`{"message":"Server Error"}`)
			assertHeader(t, res, "X-Stamp", tc.stamp)

			if len(reports.reported) != 1 || !errors.Is(reports.reported[0], errNoResponse) {
				t.Fatalf("reported %v, want errNoResponse once", reports.reported)
			}
		})
	}
}

func TestMiddlewareCanShortCircuitWithAnError(t *testing.T) {
	reached := false
	router := NewRouter()
	router.Get("/secret", func(*Context) (any, error) {
		reached = true

		return nil, nil
	}).Middleware(func(*Context, Handler) (any, error) { return nil, Abort(403, "Forbidden.") })

	res := serve(t, router, nil, "GET", "/secret")

	if reached || res.Status() != 403 || res.Body() != `{"message":"Forbidden."}` {
		t.Fatalf("reached=%v response = %d %s", reached, res.Status(), res.Body())
	}
}

func TestOuterMiddlewareDecoratesErrorResponses(t *testing.T) {
	router := NewRouter()
	router.Get("/boom", func(*Context) (any, error) { return nil, errors.New("database exploded") })

	stamp := func(c *Context, next Handler) (any, error) {
		res, err := ToResponse(next(c))

		return res.WithHeader("X-Stamp", "yes"), err
	}

	res := serve(t, router, []Middleware{stamp}, "GET", "/boom")

	// The real message stays in the logs; clients get the generic one.
	if res.Status() != 500 || res.Body() != `{"message":"Server Error"}` || res.Header("X-Stamp") != "yes" {
		t.Fatalf("response = %d %s stamp=%q", res.Status(), res.Body(), res.Header("X-Stamp"))
	}
}

// logRecorder is a kernel Logger whose JSON lines a test reads back; a
// stream's lines are written from the server's goroutine.
type logRecorder struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (l *logRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lines.Write(p)
}

func (l *logRecorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(l, nil))
}

// line is the first line logged with msg, nil when there is none.
func (l *logRecorder) line(t *testing.T, msg string) map[string]any {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()

	for raw := range bytes.SplitSeq(bytes.TrimSpace(l.lines.Bytes()), []byte("\n")) {
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err == nil && line["msg"] == msg {
			return line
		}
	}

	return nil
}

// reportRecorder is a kernel Report that keeps what it was handed.
type reportRecorder struct {
	mu       sync.Mutex
	reported []error
}

func (r *reportRecorder) report(_ *Context, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reported = append(r.reported, err)
}

// panicked is the one report's panic, failing unless exactly one error was
// reported and it holds a *panics.Error.
func (r *reportRecorder) panicked(t *testing.T) *panics.Error {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.reported) != 1 {
		t.Fatalf("reported %d errors, want 1: %v", len(r.reported), r.reported)
	}

	recovered, ok := errors.AsType[*panics.Error](r.reported[0])
	if !ok {
		t.Fatalf("reported %v, want a *panics.Error in its chain", r.reported[0])
	}

	return recovered
}

func panicsWithAValue(*Context) (any, error) { panic("nil map") }

func panicsWithAnAbort(*Context) (any, error) { panic(Abort(404, "gone")) }

func panicsInAMiddleware(*Context, Handler) (any, error) { panic("nil map") }

// A panic anywhere in the pipeline is the generic 500, logged with its
// stack and reported, even when its value is an error that knows its own
// status: a panic is a bug, never a 404.
func TestPanicsBecomeReportedServerErrors(t *testing.T) {
	cases := []struct {
		name     string
		route    func(router *Router)
		value    any
		function string
		debug    string
	}{
		{"a handler panicking a value", func(router *Router) { router.Get("/panic", panicsWithAValue) }, "nil map", "web.panicsWithAValue", "panic: nil map"},
		{"a handler panicking a renderable error", func(router *Router) { router.Get("/panic", panicsWithAnAbort) }, &Error{Code: 404, Message: "gone"}, "web.panicsWithAnAbort", "panic: gone"},
		{"a middleware", func(router *Router) {
			router.Get("/panic", func(*Context) (any, error) { return "ok", nil }).Middleware(panicsInAMiddleware)
		}, "nil map", "web.panicsInAMiddleware", "panic: nil map"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			tc.route(router)

			logs, reports := &logRecorder{}, &reportRecorder{}
			kernel, err := NewKernel(nil, KernelConfig{Router: router, Logger: logs.logger(), Report: reports.report})
			if err != nil {
				t.Fatal(err)
			}

			testkit.NewClient(t, kernel).Get("/panic").
				AssertInternalServerError().
				AssertJSON(map[string]any{"message": "Server Error"})

			if recovered := reports.panicked(t); !reflect.DeepEqual(recovered.Value, tc.value) {
				t.Fatalf("panic value = %#v, want %#v", recovered.Value, tc.value)
			}

			line := logs.line(t, "unhandled error")
			if stack, _ := line["stack"].(string); !strings.Contains(stack, tc.function+"\n") {
				t.Fatalf("logged %v, want a stack naming %s", line, tc.function)
			}

			debug, err := NewKernel(nil, KernelConfig{Router: router, Logger: slog.New(slog.DiscardHandler), Debug: true})
			if err != nil {
				t.Fatal(err)
			}

			testkit.NewClient(t, debug).Get("/panic").
				AssertInternalServerError().
				AssertJSON(map[string]any{"message": tc.debug})
		})
	}
}

// What a request registers with Terminating runs once it is done, however
// it ended: after its stream, and when nothing was written because a
// middleware threw the response away.
func TestTerminatingCallbacksRunOnceTheRequestIsDone(t *testing.T) {
	stream := func(ran *[]string, panicking bool) Handler {
		return func(c *Context) (any, error) {
			c.Terminating(func() { *ran = append(*ran, "terminated") })

			return Stream("text/event-stream", func(s *StreamWriter) error {
				*ran = append(*ran, "streamed")
				if panicking {
					panic("tool bug")
				}

				return s.Data("done")
			}), nil
		}
	}
	registersThen := func(ran *[]string, fail func() error) Handler {
		return func(c *Context) (any, error) {
			c.Terminating(func() { *ran = append(*ran, "terminated") })

			return nil, fail()
		}
	}
	discards := func(c *Context, next Handler) (any, error) {
		_, _ = next(c)

		return nil, errors.New("session: saving: connection reset")
	}
	discardsThenPanics := func(c *Context, next Handler) (any, error) {
		_, _ = next(c)

		panic("session: saving")
	}

	cases := []struct {
		name       string
		handler    func(ran *[]string) Handler
		middleware Middleware
		status     int
		aborted    bool
		want       string
	}{
		{"after the stream", func(ran *[]string) Handler { return stream(ran, false) }, nil, 200, false, "streamed terminated"},
		{"a middleware discarding the stream with an error", func(ran *[]string) Handler { return stream(ran, false) }, discards, 500, false, "terminated"},
		{"a middleware panicking after the stream came back", func(ran *[]string) Handler { return stream(ran, false) }, discardsThenPanics, 500, false, "terminated"},
		{"the handler failing after registering", func(ran *[]string) Handler {
			return registersThen(ran, func() error { return errors.New("db down") })
		}, nil, 500, false, "terminated"},
		{"the handler panicking after registering", func(ran *[]string) Handler {
			return registersThen(ran, func() error { panic("nil map") })
		}, nil, 500, false, "terminated"},
		{"the stream panicking", func(ran *[]string) Handler { return stream(ran, true) }, nil, 200, true, "streamed terminated"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran []string
			router := NewRouter()
			route := router.Get("/turn", tc.handler(&ran))
			if tc.middleware != nil {
				route.Middleware(tc.middleware)
			}

			kernel, err := NewKernel(nil, KernelConfig{Router: router, Logger: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}

			res := testkit.NewClient(t, kernel).Get("/turn").AssertStatus(tc.status)
			if tc.aborted {
				res.AssertAborted()
			}

			if got := strings.Join(ran, " "); got != tc.want {
				t.Fatalf("ran %q, want %q", got, tc.want)
			}
		})
	}
}

func streamBug(*StreamWriter) error { panic("stream bug") }

func releaseBug() { panic("release bug") }

// The callbacks run once no panic is in flight, so one that panics after
// its stream did is reported with its own stack, not the stream's.
func TestATerminatingCallbackThatPanicsIsReportedWhereItPanicked(t *testing.T) {
	router := NewRouter()
	router.Get("/turn", func(c *Context) (any, error) {
		c.Terminating(releaseBug)

		return Stream("text/event-stream", streamBug), nil
	})

	reports := &reportRecorder{}
	kernel, err := NewKernel(nil, KernelConfig{Router: router, Logger: slog.New(slog.DiscardHandler), Report: reports.report})
	if err != nil {
		t.Fatal(err)
	}

	testkit.NewClient(t, kernel).Get("/turn").AssertOk().AssertAborted()

	want := []string{"web.streamBug", "web.releaseBug"}
	if len(reports.reported) != len(want) {
		t.Fatalf("reported %v, want the stream's panic then the callback's", reports.reported)
	}
	for i, reported := range reports.reported {
		recovered, ok := errors.AsType[*panics.Error](reported)
		if !ok {
			t.Fatalf("report %d is %v, want a panic", i, reported)
		}
		if !strings.HasSuffix(recovered.Frames()[0].Function, want[i]) {
			t.Fatalf("report %d's first frame is %s, want %s; stack:\n%s", i, recovered.Frames()[0].Function, want[i], recovered.Stack())
		}
	}
}

// Callbacks run last registered first, and one that panics is reported
// while the ones registered before it still run.
func TestTerminatingCallbacksRunInReverseAndSurviveOneThatPanics(t *testing.T) {
	var ran []string
	router := NewRouter()
	router.Get("/turn", func(c *Context) (string, error) {
		c.Terminating(func() { ran = append(ran, "first") })
		c.Terminating(func() { panic("release bug") })
		c.Terminating(func() { ran = append(ran, "last") })

		return "ok", nil
	})

	reports := &reportRecorder{}
	kernel, err := NewKernel(nil, KernelConfig{Router: router, Logger: slog.New(slog.DiscardHandler), Report: reports.report})
	if err != nil {
		t.Fatal(err)
	}

	testkit.NewClient(t, kernel).Get("/turn").AssertOk().AssertBody(`"ok"`)

	if got := strings.Join(ran, " "); got != "last first" {
		t.Fatalf("ran %q, want %q", got, "last first")
	}
	if recovered := reports.panicked(t); recovered.Value != "release bug" {
		t.Fatalf("reported %v", recovered)
	}
}

func TestDuplicateRoutesFailAtBoot(t *testing.T) {
	handler := func(*Context) (any, error) { return nil, nil }

	cases := map[string]struct {
		routes func(r *Router)
		// prefix pins only the start where ServeMux words the rest of the message.
		exact, prefix string
	}{
		"one method and path": {
			routes: func(r *Router) {
				r.Get("/twice", handler)
				r.Get("/twice", handler)
			},
			prefix: "web: compiling routes: ",
		},
		"a method a Match route already answers": {
			routes: func(r *Router) {
				r.Match([]string{http.MethodGet, http.MethodPost}, "/twice", handler)
				r.Post("/twice", handler)
			},
			prefix: "web: compiling routes: ",
		},
		"one name": {
			routes: func(r *Router) {
				r.Get("/countries", handler).Name("countries.index")
				r.Get("/cities", handler).Name("countries.index")
			},
			exact: `web: route name "countries.index" is given to both GET /countries and GET /cities`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			router := NewRouter()
			tc.routes(router)

			_, err := NewKernel(nil, KernelConfig{Router: router})
			if err == nil {
				t.Fatal("expected a duplicate-route error")
			}

			switch got := err.Error(); {
			case tc.exact != "" && got != tc.exact:
				t.Fatalf("error = %q, want %q", got, tc.exact)
			case !strings.HasPrefix(got, tc.prefix):
				t.Fatalf("error = %q, want it to start %q", got, tc.prefix)
			}
		})
	}
}

func TestUnnamedRoutesNeverCollide(t *testing.T) {
	router := NewRouter()
	router.Get("/countries", func(*Context) (any, error) { return nil, nil })
	router.Get("/cities", func(*Context) (any, error) { return nil, nil })

	if _, err := NewKernel(nil, KernelConfig{Router: router}); err != nil {
		t.Fatal(err)
	}
}

func TestHeadIsServedByGetRoutes(t *testing.T) {
	router := NewRouter()
	router.Get("/up", func(*Context) (any, error) { return HTML("OK"), nil })

	if res := serve(t, router, nil, "HEAD", "/up"); res.Status() != 200 {
		t.Fatalf("status = %d", res.Status())
	}
}

// A Match route is one route: every method it lists runs the same handler
// and middleware, a GET among them answers HEAD, and any other method is a
// 405.
func TestMatchAnswersEachOfItsMethods(t *testing.T) {
	var ran []string
	router := NewRouter()
	router.Match([]string{http.MethodGet, http.MethodPost}, "/broadcasting/auth", func(c *Context) (any, error) {
		return c.Method(), nil
	}).Middleware(func(c *Context, next Handler) (any, error) {
		ran = append(ran, c.Method())

		return next(c)
	})

	for _, method := range []string{"GET", "POST", "HEAD"} {
		if res := serve(t, router, nil, method, "/broadcasting/auth"); res.Status() != 200 {
			t.Errorf("%s = %d", method, res.Status())
		}
	}

	if got := strings.Join(ran, " "); got != "GET POST HEAD" {
		t.Errorf("middleware ran for %s", got)
	}

	res := serve(t, router, nil, "DELETE", "/broadcasting/auth")
	if res.Status() != 405 || !strings.Contains(res.Body(), "Supported methods: GET, HEAD, POST.") {
		t.Fatalf("DELETE = %d %s", res.Status(), res.Body())
	}
	res.AssertHeader("Allow", "GET, HEAD, POST")
}

func TestNamedRoutesBuildUrls(t *testing.T) {
	handler := func(*Context) (any, error) { return nil, nil }
	router := NewRouter()
	router.Prefix("/api").Group(func(r *Router) {
		r.Get("/cities/{country}/{city}", handler).Name("cities.show")
		r.Get("/trips/{trackId}", handler).Name("trips.show")
	})
	router.Get("/assets/{path...}", handler).Name("assets.show")

	cases := []struct {
		name   string
		params []string
		want   string
	}{
		{"cities.show", []string{"portugal", "lisbon"}, "/api/cities/portugal/lisbon"},
		// A parameter stays one segment, whatever it holds.
		{"trips.show", []string{"a/b?c#d{e}f g"}, "/api/trips/a%2Fb%3Fc%23d%7Be%7Df%20g"},
		// A wildcard keeps its slashes and escapes each segment.
		{"assets.show", []string{"images/logo #1.png"}, "/assets/images/logo%20%231.png"},
	}

	for _, tc := range cases {
		url, err := router.URL(tc.name, tc.params...)
		if err != nil || url != tc.want {
			t.Errorf("URL(%q, %q) = %q, %v; want %q", tc.name, tc.params, url, err, tc.want)
		}
	}

	if _, err := router.URL("cities.show", "portugal"); err == nil {
		t.Error("expected a missing-parameter error")
	}

	if _, err := router.URL("trips.show", "a", "b"); err == nil {
		t.Error("expected a too-many-parameters error")
	}

	if _, err := router.URL("nope"); err == nil {
		t.Error("expected an undefined-route error")
	}
}

// A built URL reaches its route with the parameter as it was given.
func TestAnEscapedParameterRoundTrips(t *testing.T) {
	router := NewRouter()
	router.Get("/trips/{trackId}", func(c *Context) (string, error) { return c.Param("trackId"), nil }).Name("trips.show")

	url, err := router.URL("trips.show", "a/b?c#d")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{url, url + "/", url + "//"} {
		if res := serve(t, router, nil, "GET", path); res.Status() != 200 || res.Body() != `"a/b?c#d"` {
			t.Fatalf("GET %s = %d %q", path, res.Status(), res.Body())
		}
	}
}

func TestIsMatchesAcrossSlashes(t *testing.T) {
	c := newContext(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/countries/portugal", http.NoBody), nil)

	for pattern, want := range map[string]bool{
		"api/*":           true,
		"api/countries/*": true,
		"/api/*":          true,
		"bookings/*":      false,
		"api":             false,
	} {
		if got := c.Is(pattern); got != want {
			t.Errorf("Is(%q) = %v, want %v", pattern, got, want)
		}
	}
}

// appKey is a key an app might bind a value under.
type appKey string

// What the app binds is the app's: no key it picks reaches the framework's
// own request state (cookies, queued cookies, parsed input).
func TestAppValuesNeverShadowFrameworkState(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?q=real", http.NoBody)
	r.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	c := newContext(r, nil)

	for _, key := range []appKey{"__cookies", "__queued_cookies", "__input"} {
		c.WithContext(context.WithValue(c.Context(), key, "app data"))
	}

	if got := c.Cookie("theme"); got != "dark" {
		t.Errorf("Cookie = %q", got)
	}
	if got := c.Input("q"); got != "real" {
		t.Errorf("Input q = %v", got)
	}
	c.QueueCookie(&http.Cookie{Name: "queued"})
	if queued := c.QueuedCookies(); len(queued) != 1 || queued[0].Name != "queued" {
		t.Errorf("QueuedCookies = %v", queued)
	}
}

func TestClientIPOnlyTrustsForwardedHeadersFromTrustedProxies(t *testing.T) {
	request := func(peer string, forwardedFor string) *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		r.RemoteAddr = peer + ":1234"
		if forwardedFor != "" {
			r.Header.Set("X-Forwarded-For", forwardedFor)
		}

		return r
	}

	cases := []struct {
		name    string
		proxies []string
		peer    string
		xff     string
		want    string
	}{
		// No proxies configured trusts none: a directly exposed app's
		// client is its TCP peer, whatever the header claims.
		{"nil trusts no proxy", nil, "10.0.0.1", "1.1.1.1, 2.2.2.2", "10.0.0.1"},
		{"empty trusts no proxy", []string{}, "10.0.0.1", "1.1.1.1", "10.0.0.1"},
		{"no header", []string{"*"}, "10.0.0.1", "", "10.0.0.1"},
		// `*` trusts only the peer, so the rightmost entry is the client.
		{"star takes rightmost", []string{"*"}, "10.0.0.1", "1.1.1.1, 2.2.2.2", "2.2.2.2"},
		{"untrusted peer ignores header", []string{"10.0.0.0/8"}, "192.0.2.1", "1.1.1.1", "192.0.2.1"},
		{"skips trusted hops", []string{"10.0.0.0/8"}, "10.0.0.1", "1.1.1.1, 10.0.0.5", "1.1.1.1"},
		{"malformed chain falls back to peer", []string{"*"}, "10.0.0.1", "not-an-ip", "10.0.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTrustedProxies(tc.proxies).clientIP(request(tc.peer, tc.xff))
			if got != tc.want {
				t.Fatalf("clientIP = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTheKernelTrustsNoProxyUnlessConfigured(t *testing.T) {
	cases := []struct {
		name       string
		proxies    *TrustedProxies
		wantIP     string
		wantSecure bool
	}{
		{"unconfigured", nil, "192.0.2.1", false},
		{"star", ParseTrustedProxies([]string{"*"}), "2.2.2.2", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			router.Get("/ip", func(c *Context) (any, error) {
				return map[string]any{"ip": c.IP(), "secure": c.IsSecure()}, nil
			})

			kernel, err := NewKernel(nil, KernelConfig{Router: router, TrustedProxies: tc.proxies})
			if err != nil {
				t.Fatal(err)
			}

			res := testkit.NewClient(t, kernel).
				FromIP("192.0.2.1").
				WithHeader("X-Forwarded-For", "1.1.1.1, 2.2.2.2").
				WithHeader("X-Forwarded-Proto", "https").
				Get("/ip")

			want := fmt.Sprintf(`{"ip":%q,"secure":%t}`, tc.wantIP, tc.wantSecure)
			if res.Body() != want {
				t.Fatalf("body = %s, want %s", res.Body(), want)
			}
		})
	}
}

func TestTrailingSlashesMatchTheRoute(t *testing.T) {
	router := NewRouter()
	router.Get("/api/countries", func(*Context) (any, error) { return []string{}, nil })

	if res := serve(t, router, nil, "GET", "/api/countries/"); res.Status() != 200 {
		t.Fatalf("status = %d", res.Status())
	}
}

// The trim keeps the escaping before the slash: an encoded slash stays in
// its parameter, and an encoded dot segment stays a segment, with or without
// a trailing slash.
func TestTrailingSlashesKeepTheEscapingBeforeThem(t *testing.T) {
	router := NewRouter()
	router.Get("/countries", func(*Context) (string, error) { return "countries", nil })
	router.Get("/trips/{trackId}", func(c *Context) (string, error) { return c.Param("trackId"), nil })

	cases := []struct {
		path   string
		status int
		body   string
	}{
		{"/trips/a%2Fb/", 200, `"a/b"`},
		// Trimmed, the escaping no longer spells the path, so the decoded
		// path is matched.
		{"/trips/a%2F/", 200, `"a"`},
		{"/x/%2e%2e/countries/", 404, `{"message":"The route x/../countries could not be found."}`},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			serve(t, router, nil, "GET", tc.path).
				AssertStatus(tc.status).
				AssertBody(tc.body).
				AssertHeaderMissing("Location")
		})
	}
}

// A path only a wildcard's subtree answers is that route's 404, as a
// required wildcard's bare prefix is: never a redirect to its slash form,
// which the trailing-slash trim would send straight back.
func TestAWildcardsBarePrefixIsAMissNotALoop(t *testing.T) {
	router := NewRouter()
	router.Get("/assets/{path...}", func(c *Context) (string, error) { return c.Param("path"), nil })
	router.Get("/countries", func(*Context) (string, error) { return "countries", nil })

	miss := `{"message":"The route assets could not be found."}`
	cases := []struct {
		method, path string
		status       int
		body         string
		location     string
		allow        string
	}{
		{"GET", "/assets", 404, miss, "", ""},
		{"GET", "/assets/", 404, miss, "", ""},
		{"GET", "/assets//", 404, miss, "", ""},
		{"HEAD", "/assets", 404, miss, "", ""},
		{"GET", "/assets/x.png", 200, `"x.png"`, "", ""},
		// A cleaning redirect stays: to the route, or once to the bare
		// prefix's slash form, which then misses.
		{"GET", "//countries", 307, "", "/countries", ""},
		{"GET", "//assets", 307, "", "/assets/", ""},
		{"GET", "//", 307, "", "/", ""},
		{"POST", "/assets", 405, `{"message":"The POST method is not supported for route assets. Supported methods: GET, HEAD."}`, "", "GET, HEAD"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := serve(t, router, nil, tc.method, tc.path).AssertStatus(tc.status).AssertBody(tc.body)
			assertHeader(t, res, "Location", tc.location)
			assertHeader(t, res, "Allow", tc.allow)
		})
	}

	// A route that matches the bare prefix still wins it.
	router.Get("/{slug}", func(c *Context) (string, error) { return c.Param("slug"), nil })
	for _, slug := range []string{"assets", "x"} {
		serve(t, router, nil, "GET", "/"+slug).AssertOk().AssertBody(`"` + slug + `"`)
	}
}

// Wrapping a renderable error with context never turns it into a 500: the
// kernel finds it with errors.As and renders its own status and body.
func TestWrappedHTTPErrorsRenderTheirOwnResponse(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"abort", Abort(404, "Not here."), 404, `{"message":"Not here."}`},
		{"validation", ValidationFailed("code", "The code has expired."), 422, `{"message":"The code has expired.","errors":{"code":["The code has expired."]}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			router.Get("/guide", func(*Context) (any, error) {
				return nil, fmt.Errorf("loading guide for slug %q: %w", "x", tc.err)
			})

			res := serve(t, router, nil, "GET", "/guide")
			if res.Status() != tc.status || res.Body() != tc.body {
				t.Fatalf("response = %d %s", res.Status(), res.Body())
			}
		})
	}
}

func TestUnexpectedErrorsAreReportedButHTTPErrorsAreNot(t *testing.T) {
	var reported []error
	router := NewRouter()
	router.Get("/boom", func(*Context) (any, error) { return nil, errors.New("db down") })
	router.Get("/gone", func(*Context) (any, error) { return nil, Abort(404, "") })

	kernel, _ := NewKernel(nil, KernelConfig{Router: router, Report: func(_ *Context, err error) { reported = append(reported, err) }})
	client := testkit.NewClient(t, kernel)
	for _, path := range []string{"/boom", "/gone", "/missing"} {
		client.Get(path)
	}

	if len(reported) != 1 || reported[0].Error() != "db down" {
		t.Fatalf("reported = %v", reported)
	}
}

func TestAPanickingReporterDoesNotBreakTheResponse(t *testing.T) {
	router := NewRouter()
	router.Get("/boom", func(*Context) (any, error) { return nil, errors.New("db down") })

	kernel, _ := NewKernel(nil, KernelConfig{Router: router, Report: func(*Context, error) { panic("reporter bug") }})
	res := testkit.NewClient(t, kernel).Get("/boom")

	if res.Status() != 500 || res.Body() != `{"message":"Server Error"}` {
		t.Fatalf("response = %d %s", res.Status(), res.Body())
	}
}

// Middleware outside routing sees which route, if any, handled the request.
func TestOuterMiddlewareSeesTheMatchedRoute(t *testing.T) {
	var matched []string
	record := func(c *Context, next Handler) (any, error) {
		value, err := next(c)
		matched = append(matched, fmt.Sprintf("%v:%s", c.RouteMatched(), c.RouteName()))

		return value, err
	}

	router := NewRouter()
	router.Get("/countries/{slug}", func(c *Context) (string, error) { return c.Param("slug"), nil }).Name("countries.show")
	router.Get("/unnamed", func(*Context) (*Response, error) { return NoContent(), nil })

	if res := serve(t, router, []Middleware{record}, "GET", "/countries/portugal"); res.Body() != `"portugal"` {
		t.Fatalf("body = %q", res.Body())
	}

	serve(t, router, []Middleware{record}, "GET", "/unnamed")
	serve(t, router, []Middleware{record}, "GET", "/missing")

	if want := "true:countries.show true: false:"; strings.Join(matched, " ") != want {
		t.Fatalf("matched = %v, want %s", matched, want)
	}
}

func TestRoutingMissesRenderAsJSONErrors(t *testing.T) {
	router := NewRouter()
	router.Get("/countries", func(*Context) (any, error) { return "ok", nil })

	cases := []struct {
		method, path string
		status       int
		want         string
		allow        string
	}{
		{"POST", "/countries", 405, "The POST method is not supported for route countries. Supported methods: GET, HEAD.", "GET, HEAD"},
		// Without preflight headers, OPTIONS is a method like any other.
		{"OPTIONS", "/countries", 405, "The OPTIONS method is not supported for route countries. Supported methods: GET, HEAD.", "GET, HEAD"},
		{"GET", "/nowhere", 404, "The route nowhere could not be found.", ""},
	}

	for _, tc := range cases {
		res := serve(t, router, nil, tc.method, tc.path)
		if res.Status() != tc.status || !strings.Contains(res.Body(), tc.want) {
			t.Errorf("%s %s = %d %s", tc.method, tc.path, res.Status(), res.Body())
		}
		assertHeader(t, res, "Allow", tc.allow)
	}

	if res := serve(t, router, nil, "GET", "/a/../countries"); res.Status()/100 != 3 || res.Header("Location") != "/countries" {
		t.Errorf("unclean path = %d %q", res.Status(), res.Header("Location"))
	}
}
