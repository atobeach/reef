package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/atobeach/reef/panics"
)

// KernelConfig wires the HTTP kernel: routes, global middleware, proxy
// trust and error reporting.
type KernelConfig struct {
	Router *Router
	// Middleware runs on every request, outermost first, before routing —
	// so CORS preflights and 404s get the same headers as real routes.
	Middleware []Middleware
	// TrustedProxies are the peers whose X-Forwarded-* headers are believed;
	// nil trusts none, so behind a load balancer pass ParseTrustedProxies.
	TrustedProxies *TrustedProxies
	Logger         *slog.Logger
	Debug          bool
	// Report receives every unexpected failure after it's logged: an error
	// that renders as a 500, a panic (a *panics.Error in the chain), a
	// stream that failed or panicked, and a Terminating callback that
	// panicked. An unexpected error a middleware replaced is reported too.
	// HTTP errors (404s, 422s) never reach it.
	Report func(c *Context, err error)
}

// Kernel is the http.Handler that runs the pipeline.
type Kernel struct {
	mux      *http.ServeMux
	pipeline func(c *Context) *Response
	proxies  *TrustedProxies
	logger   *slog.Logger
	debug    bool
	bind     func(ctx context.Context) context.Context
	report   func(c *Context, err error)
}

// NewKernel compiles the routes. bind attaches the app's services (the db
// connection, the clock) to every request's context; nil binds nothing. A
// duplicate method and path, or a route name used twice, fails here, at boot.
func NewKernel(bind func(ctx context.Context) context.Context, config KernelConfig) (kernel *Kernel, err error) {
	kernel = &Kernel{
		mux:     http.NewServeMux(),
		proxies: config.TrustedProxies,
		logger:  config.Logger,
		debug:   config.Debug,
		bind:    bind,
		report:  config.Report,
	}

	if kernel.logger == nil {
		kernel.logger = slog.Default()
	}

	routes := config.Router.Routes()
	if err := uniqueNames(routes); err != nil {
		return nil, err
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("web: compiling routes: %w", panics.New(recovered))
		}
	}()

	for _, route := range routes {
		kernel.handle(route)
	}

	pipeline := kernel.chain(config.Middleware, kernel.dispatch)
	kernel.pipeline = func(c *Context) *Response {
		return kernel.respond(c, pipeline)
	}

	return kernel, nil
}

// handle registers one ServeMux pattern per method, all running the one
// compiled chain. ServeMux answers HEAD on the GET pattern.
func (k *Kernel) handle(route *Route) {
	next := k.chain(route.middleware, action(route.handler))
	name := route.RouteName
	serve := func(w http.ResponseWriter, _ *http.Request) {
		matched := w.(*captureWriter)
		matched.context.route = &name
		matched.value, matched.err = next(matched.context)
	}

	for _, method := range route.Methods {
		k.mux.HandleFunc(method+" "+route.Path, serve)
	}
}

// uniqueNames refuses a route name given to two routes, which would leave
// URL building to whichever was registered first.
func uniqueNames(routes []*Route) error {
	named := map[string]string{}
	for _, route := range routes {
		if route.RouteName == "" {
			continue
		}

		endpoint := strings.Join(route.Methods, "|") + " " + route.Path
		if other, ok := named[route.RouteName]; ok {
			return fmt.Errorf("web: route name %q is given to both %s and %s", route.RouteName, other, endpoint)
		}

		named[route.RouteName] = endpoint
	}

	return nil
}

// ServeHTTP implements http.Handler.
func (k *Kernel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if k.bind != nil {
		r = r.WithContext(k.bind(r.Context()))
	}

	// A trailing slash is ignored, as clients and crawlers rely on; ServeMux
	// would 404 it. RawPath loses the same slashes, so an escape before them
	// (/trips/a%2Fb/) still matches as sent.
	if path := r.URL.Path; len(path) > 1 && strings.HasSuffix(path, "/") {
		r.URL.Path = strings.TrimRight(path, "/")
		r.URL.RawPath = strings.TrimRight(r.URL.RawPath, "/")
	}

	c := newContext(r, k.proxies)

	var aborted bool
	defer func() { //nolint:contextcheck // c is r's context; the linter can't see through *Context.
		k.terminate(c)

		// The status line is out, so no error response can follow: abort,
		// and the client sees a cut stream rather than a clean end. Raised
		// once the callbacks ran, so none runs while a panic is in flight,
		// where one panicking would be recovered with this panic's stack.
		if aborted {
			panic(http.ErrAbortHandler)
		}
	}()

	aborted = k.write(w, c, k.pipeline(c)) //nolint:contextcheck // c is r's context, as for terminate.
}

// write sends res. A stream's status and headers go out first, then its
// function runs under the same guard and Report as an action; aborted
// reports a stream that panicked, whose response must be cut off.
func (k *Kernel) write(w http.ResponseWriter, c *Context, res *Response) (aborted bool) {
	if res.stream == nil {
		res.write(w)

		return false
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			k.unexpected(c, fmt.Errorf("panic: %w", panics.New(recovered)))
			aborted = true
		}
	}()

	res.copyHeaders(w)
	w.WriteHeader(res.status)

	rc := http.NewResponseController(w)
	if err := rc.Flush(); errors.Is(err, http.ErrNotSupported) {
		k.logger.ErrorContext(c, "stream cannot flush", "path", c.Path())
	}

	if err := res.stream(&StreamWriter{ctx: c.Context(), w: w, rc: rc}); err != nil && c.Err() == nil {
		k.unexpected(c, err)
	}

	return false
}

// terminate runs the request's Terminating callbacks, last registered
// first. One that panics is reported, and the rest still run.
func (k *Kernel) terminate(c *Context) {
	for i := len(c.terminating) - 1; i >= 0; i-- {
		k.runTerminating(c, c.terminating[i])
	}
}

func (k *Kernel) runTerminating(c *Context, fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			k.unexpected(c, fmt.Errorf("panic: %w", panics.New(recovered)))
		}
	}()

	fn()
}

// dispatch matches the route and runs it. A miss, ServeMux's subtree
// redirect included, is a 404 error; a 405's Allow header names the route's
// methods.
func (k *Kernel) dispatch(c *Context) (any, error) {
	// ServeMux sets the path values on c.request itself, so the route runs
	// on the same Context.
	capture := &captureWriter{context: c}
	k.mux.ServeHTTP(capture, c.request)

	if c.RouteMatched() {
		return capture.value, capture.err
	}

	switch capture.status {
	case http.StatusMethodNotAllowed:
		allow := capture.header.Get("Allow")

		return nil, &Error{
			Code: http.StatusMethodNotAllowed,
			Message: fmt.Sprintf(
				"The %s method is not supported for route %s. Supported methods: %s.",
				c.Method(), c.Path(), allow,
			),
			Headers: map[string]string{"Allow": allow},
		}
	case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		location := capture.header.Get("Location")
		if !trimUndoes(c, location) {
			// ServeMux cleans paths (`//`, `/../`) with a redirect; keep it.
			return &Response{status: capture.status, header: http.Header{"Location": {location}}}, nil
		}
	}

	return nil, Abort(http.StatusNotFound, fmt.Sprintf("The route %s could not be found.", c.Path()))
}

// trimUndoes reports ServeMux's subtree redirect (/assets to /assets/ for
// /assets/{path...}), which the trailing-slash trim would undo: a miss, not
// a redirect. A slash-only path, trimmed empty, keeps its redirect to /.
func trimUndoes(c *Context, location string) bool {
	target, err := url.Parse(location)

	return err == nil && target.Path != "/" && target.EscapedPath() == c.request.URL.EscapedPath()+"/"
}

// action runs a controller under guard. A value it returns beside an error
// is dropped: only a middleware's response travels with an error.
func action(handler Handler) Handler {
	return func(c *Context) (any, error) {
		value, err := guard(func() (any, error) { return handler(c) })
		if err != nil {
			return nil, err
		}

		return value, nil
	}
}

// chain wraps next in middleware, first entry outermost, each layer under
// guard.
func (k *Kernel) chain(middleware []Middleware, next Handler) Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handle, inner := middleware[i], next
		next = func(c *Context) (any, error) {
			var handed error
			value, err := guard(func() (any, error) {
				return handle(c, func(c *Context) (any, error) {
					value, err := inner(c)
					handed = err

					return value, err
				})
			})

			// A middleware that fails after next, or answers in place of the
			// error it was handed, still has that error logged and reported.
			if handed != nil && !isExpected(handed) && !passedOn(err, handed) {
				k.unexpected(c, handed)
			}

			return value, err
		}
	}

	return next
}

// passedOn reports whether err is handed or wraps it. errors.Is never matches
// an uncomparable error (a slice of errors, a struct holding a map) that has
// no Is method, so one of those is compared by value along err's chain: a
// middleware that passes it on must not have it reported twice.
func passedOn(err, handed error) bool {
	if errors.Is(err, handed) {
		return true
	}

	return !reflect.TypeOf(handed).Comparable() && holds(err, handed)
}

// holds reports whether a link of err's chain, through either Unwrap, is
// deeply equal to target.
func holds(err, target error) bool {
	if reflect.DeepEqual(err, target) {
		return true
	}

	switch wrapper := err.(type) { //nolint:errorlint // the chain is walked one link at a time, as errors.Is does
	case interface{ Unwrap() error }:
		return holds(wrapper.Unwrap(), target)
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(wrapper.Unwrap(), func(inner error) bool { return holds(inner, target) })
	}

	return false
}

// respond runs the pipeline and renders its outcome, once. An error goes out
// with the headers and cookies of a *Response the middleware returned beside
// it. Rendering runs the app's code too (an error's Error or HTTPBody, a
// value's MarshalJSON), and a panic there, or anywhere a layer's guard
// doesn't reach, is a reported 500 like a handler's, never a dropped
// connection.
func (k *Kernel) respond(c *Context, pipeline Handler) (res *Response) {
	var carrier *Response
	defer func() {
		if recovered := recover(); recovered != nil {
			res = decorate(k.renderError(c, fmt.Errorf("panic: %w", panics.New(recovered))), carrier)
		}
	}()

	carrier, err := ToResponse(pipeline(c))
	if err == nil {
		return carrier
	}

	return decorate(k.renderError(c, err), carrier)
}

// decorate gives a rendered error the headers and cookies middleware put on
// carrier, the response beside it: a decoration wins over the error's own
// header, as a later Set would, but carrier's status is never applied.
func decorate(rendered, carrier *Response) *Response {
	if carrier == nil {
		return rendered
	}

	for name, values := range carrier.header {
		rendered.header[name] = values
	}
	rendered.cookies = append(rendered.cookies, carrier.cookies...)

	return rendered
}

// guard turns a panic into an error, so one bad handler is a 500, not a crash.
func guard(fn func() (any, error)) (value any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			value, err = nil, fmt.Errorf("panic: %w", panics.New(recovered))
		}
	}()

	return fn()
}

// captureWriter is what ServeMux writes to: a matched route hands its
// outcome back through it; a miss leaves the status (404, 405, a redirect)
// and headers ServeMux would have sent.
type captureWriter struct {
	context *Context
	value   any
	err     error
	header  http.Header
	status  int
}

// Header is allocated lazily: only a miss writes headers.
func (w *captureWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}

	return w.header
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	return len(b), nil
}

func (w *captureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
