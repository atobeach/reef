package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atobeach/reef/testkit"
)

// messages is the text of what Report was handed, in order.
func (r *reportRecorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	messages := make([]string, len(r.reported))
	for i, err := range r.reported {
		messages[i] = err.Error()
	}

	return messages
}

// reportingKernel is a kernel on router and global middleware whose
// reports are recorded.
func reportingKernel(t *testing.T, router *Router, middleware ...Middleware) (*testkit.Client, *reportRecorder) {
	t.Helper()

	reports := &reportRecorder{}
	kernel, err := NewKernel(nil, KernelConfig{Router: router, Middleware: middleware, Logger: slog.New(slog.DiscardHandler), Report: reports.report})
	if err != nil {
		t.Fatal(err)
	}

	return testkit.NewClient(t, kernel), reports
}

// A controller's value is its answer only when it doesn't fail: a response
// returned beside an error lends that error none of its headers.
func TestAControllersValueBesideAnErrorIsDropped(t *testing.T) {
	router := NewRouter()
	router.Get("/x", func(*Context) (any, error) {
		return HTML("x").WithHeader("X-Leak", "1"), Abort(422, "No.")
	})

	serve(t, router, nil, "GET", "/x").
		AssertUnprocessable().
		AssertBody(`{"message":"No."}`).
		AssertHeaderMissing("X-Leak")
}

// An unexpected error a middleware answers in place of is still logged and
// reported, once, before the middleware's own failure; an expected one a
// middleware replaces is not, and one passed up, bare or wrapped, is
// reported once, at the edge, even when it can't be compared with ==.
func TestAMiddlewareThatFailsAfterNextStillReportsTheErrorItReplaced(t *testing.T) {
	failsAfter := func(c *Context, next Handler) (any, error) {
		_, _ = next(c)

		return nil, errors.New("save failed")
	}
	wraps := func(step string) Middleware {
		return func(c *Context, next Handler) (any, error) {
			value, err := next(c)
			if err != nil {
				return value, fmt.Errorf("%s: %w", step, err)
			}

			return value, nil
		}
	}
	passes := func(c *Context, next Handler) (any, error) { return next(c) }
	joins := func(c *Context, next Handler) (any, error) {
		value, err := next(c)

		return value, errors.Join(err, errors.New("save failed"))
	}
	dbDown := func(*Context) (any, error) { return nil, errors.New("db down") }
	tangled := func(*Context) (any, error) { return nil, failures{"db down", "disk full"} }

	cases := []struct {
		name       string
		handler    Handler
		middleware []Middleware
		want       []string
	}{
		{"an unexpected error replaced", dbDown, []Middleware{failsAfter}, []string{"db down", "save failed"}},
		{"an expected error replaced", func(*Context) (any, error) { return nil, Abort(422, "No.") }, []Middleware{failsAfter}, []string{"save failed"}},
		{"an unexpected error wrapped twice", dbDown, []Middleware{wraps("saving"), wraps("loading")}, []string{"saving: loading: db down"}},
		{"an uncomparable error passed on", tangled, []Middleware{passes}, []string{"db down; disk full"}},
		{"an uncomparable error wrapped", tangled, []Middleware{wraps("saving")}, []string{"saving: db down; disk full"}},
		{"an uncomparable error inside a joined one", tangled, []Middleware{joins}, []string{"db down; disk full\nsave failed"}},
		{"an uncomparable error replaced", tangled, []Middleware{failsAfter}, []string{"db down; disk full", "save failed"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			router.Get("/x", tc.handler).Middleware(tc.middleware...)

			client, reports := reportingKernel(t, router)
			client.Get("/x").AssertInternalServerError().AssertJSON(map[string]any{"message": "Server Error"})

			if got := reports.messages(); !slices.Equal(got, tc.want) {
				t.Fatalf("reported %q, want %q", got, tc.want)
			}
		})
	}
}

// failures is an error that can't be compared with ==, as a slice of
// errors often is.
type failures []string

func (f failures) Error() string { return strings.Join(f, "; ") }

// A value that can't be encoded is one reported 500, and the middleware
// around it still decorate that 500.
func TestAValueThatFailsToEncodeIsADecoratedServerError(t *testing.T) {
	stamp := func(name string) Middleware {
		return func(c *Context, next Handler) (any, error) {
			res, err := ToResponse(next(c))

			return res.WithHeader(name, "yes"), err
		}
	}

	router := NewRouter()
	router.Get("/x", func(*Context) (any, error) { return map[string]float64{"x": math.Inf(1)}, nil })

	client, reports := reportingKernel(t, router, stamp("X-Stamp"), stamp("X-Inner"))
	client.Get("/x").
		AssertInternalServerError().
		AssertBody(`{"message":"Server Error"}`).
		AssertHeader("X-Stamp", "yes").
		AssertHeader("X-Inner", "yes")

	if got := reports.messages(); len(got) != 1 || !strings.Contains(got[0], "web: encoding response") {
		t.Fatalf("reported %q, want one encoding failure", got)
	}
}

// An error body that can't be encoded is the server's failure: the generic
// 500, reported under the type of the error whose body it was, never the
// error's own status with an empty body.
func TestAnErrorBodyThatFailsToEncodeIsAReportedServerError(t *testing.T) {
	router := NewRouter()
	router.Get("/x", func(*Context) (any, error) { return nil, fmt.Errorf("claiming: %w", unencodableError{}) })

	client, reports := reportingKernel(t, router)
	client.Get("/x").AssertInternalServerError().AssertBody(`{"message":"Server Error"}`)

	if got := reports.messages(); len(got) != 1 || !strings.Contains(got[0], "web: encoding the error body of web.unencodableError") {
		t.Fatalf("reported %q, want one encoding failure naming web.unencodableError", got)
	}
}

// unencodableError knows its status, but its body has no JSON form.
type unencodableError struct{}

func (unencodableError) Error() string   { return "unencodable" }
func (unencodableError) HTTPStatus() int { return 409 }
func (unencodableError) HTTPBody() any   { return map[string]float64{"x": math.Inf(1)} }

// A panic while the kernel renders the outcome, past every layer's guard,
// is the reported 500 a panicking handler is, never a dropped connection,
// and it still carries what the middleware put beside the error.
func TestAPanicWhileRenderingIsAReportedServerError(t *testing.T) {
	stamp := func(c *Context, next Handler) (any, error) {
		res, err := ToResponse(next(c))

		return res.WithHeader("X-Stamp", "yes"), err
	}
	passes := func(c *Context, next Handler) (any, error) { return next(c) }
	panickyMessage := func(*Context) (any, error) { return nil, panickyError{} }
	panickyValue := func(*Context) (any, error) { return panickyJSON{}, nil }

	cases := []struct {
		name       string
		handler    Handler
		middleware []Middleware
		stamped    bool
		value      string
	}{
		{"an error whose message panics", panickyMessage, nil, false, "nil bag"},
		{"an error whose message panics, decorated", panickyMessage, []Middleware{stamp}, true, "nil bag"},
		{"a value whose encoding panics", panickyValue, nil, false, "nil field"},
		{"a value whose encoding panics, passed on", panickyValue, []Middleware{passes}, false, "nil field"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			router.Get("/x", tc.handler)

			client, reports := reportingKernel(t, router, tc.middleware...)
			res := client.Get("/x").AssertInternalServerError().AssertBody(`{"message":"Server Error"}`)

			stamped := ""
			if tc.stamped {
				stamped = "yes"
			}

			assertHeader(t, res, "X-Stamp", stamped)

			if recovered := reports.panicked(t); recovered.Value != tc.value {
				t.Fatalf("panic value = %#v, want %q", recovered.Value, tc.value)
			}
		})
	}
}

// panickyError knows its status, but reading its message panics, as a
// method on a nil field does.
type panickyError struct{}

func (panickyError) Error() string   { panic("nil bag") }
func (panickyError) HTTPStatus() int { return 409 }

// panickyJSON panics as it is encoded.
type panickyJSON struct{}

func (panickyJSON) MarshalJSON() ([]byte, error) { panic("nil field") }

type reportKey struct{}

// Report sees the request as the middleware left it: what one bound on the
// way in is there when the failure is reported.
func TestAnErrorIsReportedWithWhatTheMiddlewareBound(t *testing.T) {
	bind := func(c *Context, next Handler) (any, error) {
		c.WithContext(context.WithValue(c.Context(), reportKey{}, "guest-1"))

		return next(c)
	}

	router := NewRouter()
	router.Get("/x", func(*Context) (any, error) { return nil, errors.New("db down") })

	var guests []any
	kernel, err := NewKernel(nil, KernelConfig{
		Router:     router,
		Middleware: []Middleware{bind},
		Logger:     slog.New(slog.DiscardHandler),
		Report:     func(c *Context, _ error) { guests = append(guests, c.Value(reportKey{})) },
	})
	if err != nil {
		t.Fatal(err)
	}

	testkit.NewClient(t, kernel).Get("/x").AssertInternalServerError()

	if len(guests) != 1 || guests[0] != "guest-1" {
		t.Fatalf("Report read %v, want the bound guest once", guests)
	}
}

// A middleware finds the error further in with errors.As, through any
// wrapping, and the response is still that error's own.
func TestAMiddlewareSeesTheErrorFurtherInAsTheErrorItIs(t *testing.T) {
	var seen *Error
	router := NewRouter()
	router.Get("/claim", func(*Context) (any, error) {
		return nil, fmt.Errorf("claiming: %w", Abort(409, "Taken."))
	}).Middleware(func(c *Context, next Handler) (any, error) {
		value, err := next(c)
		seen, _ = errors.AsType[*Error](err)

		return value, err
	})

	serve(t, router, nil, "GET", "/claim").AssertConflict().AssertBody(`{"message":"Taken."}`)

	if seen == nil || seen.Code != 409 {
		t.Fatalf("the middleware saw %v, want the 409", seen)
	}
}

// Routing answers a miss with an error, so a global middleware sees a 404
// or a 405 as the *Error it is, the 405 naming the route's methods.
func TestAGlobalMiddlewareSeesARoutingMissAsAnError(t *testing.T) {
	cases := []struct {
		method, path string
		code         int
		allow        string
	}{
		{"GET", "/nowhere", 404, ""},
		{"POST", "/countries", 405, "GET, HEAD"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var seen *Error
			observe := func(c *Context, next Handler) (any, error) {
				value, err := next(c)
				seen, _ = errors.AsType[*Error](err)

				return value, err
			}

			router := NewRouter()
			router.Get("/countries", func(*Context) (any, error) { return "ok", nil })

			res := serve(t, router, []Middleware{observe}, tc.method, tc.path).AssertStatus(tc.code)
			assertHeader(t, res, "Allow", tc.allow)

			if seen == nil || seen.Code != tc.code || seen.Headers["Allow"] != tc.allow {
				t.Fatalf("the middleware saw %+v, want a %d allowing %q", seen, tc.code, tc.allow)
			}
		})
	}
}

// What middleware put on the response beside an error goes out with the
// rendered error, over the error's own headers: the inner decoration of the
// rate limit wins over the 429's, as a later Set would.
func TestDecorationsBesideAnErrorGoOutWithIt(t *testing.T) {
	outer := func(c *Context, next Handler) (any, error) {
		res, err := ToResponse(next(c))

		return res.WithHeader("X-Outer", "yes").WithCookie(&http.Cookie{Name: "outer", Value: "1"}), err
	}
	inner := func(c *Context, next Handler) (any, error) {
		res, err := ToResponse(next(c))

		return res.WithHeader("X-RateLimit-Limit", "9"), err
	}

	router := NewRouter()
	router.Get("/x", func(c *Context) (any, error) {
		return nil, TooManyRequests(c, 5, time.Minute)
	}).Middleware(outer, inner)

	res := serve(t, router, nil, "GET", "/x").
		AssertTooManyRequests().
		AssertBody(`{"message":"Too Many Attempts."}`).
		AssertHeader("Content-Type", "application/json").
		AssertHeader("Retry-After", "60").
		AssertHeader("X-Outer", "yes").
		AssertHeader("X-RateLimit-Limit", "9").
		AssertCookie("outer")

	if got := res.Cookie("outer").Value; got != "1" {
		t.Fatalf("outer cookie = %q", got)
	}
}

// An error goes out with its own status: the status on the response beside
// it is what ToResponse read, never what the kernel sends.
func TestAnErrorKeepsItsStatusWhateverTheResponseBesideItSays(t *testing.T) {
	router := NewRouter()
	router.Get("/x", func(*Context) (any, error) { return nil, Abort(409, "Taken.") }).
		Middleware(func(c *Context, next Handler) (any, error) {
			res, err := ToResponse(next(c))

			return res.WithStatus(http.StatusOK), err
		})

	serve(t, router, nil, "GET", "/x").AssertConflict().AssertBody(`{"message":"Taken."}`)
}
