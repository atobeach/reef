package web

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

// Handler is a route's action with its response type erased, and the rest of
// the pipeline a middleware calls as next: a value to send (a *Response for
// full control) or an error. Register actions through Get, Post, …, which keep
// the action's own type on its Route.
type Handler func(c *Context) (any, error)

// Middleware wraps next, the rest of the pipeline: return what next returns
// to pass the request on (decorated through ToResponse, if need be), or
// return early with a value or an error. An error further in reaches it as the
// error it is; the kernel renders it once, at its edge.
type Middleware func(c *Context, next Handler) (any, error)

// Route is one registered endpoint: one path and handler answering each of
// its methods.
type Route struct {
	Methods   []string
	Path      string
	RouteName string
	// ResponseType is what the route's action answers: *Response, sent as
	// built, or the type sent as JSON.
	ResponseType reflect.Type
	handler      Handler
	middleware   []Middleware
}

// Name names the route for URL generation ("cities.index"). Names are
// unique: NewKernel refuses a second route with the same one.
func (r *Route) Name(name string) *Route {
	r.RouteName = name

	return r
}

// Middleware appends route-level middleware, run after the group's.
func (r *Route) Middleware(middleware ...Middleware) *Route {
	r.middleware = append(r.middleware, middleware...)

	return r
}

// Router registers routes. Prefix and Middleware return a child router that
// carries the accumulated attributes, so groups nest:
//
//	r.Prefix("/api").Middleware(throttle).Group(func(r *web.Router) {
//		r.Get("/countries", countries.Index).Name("countries.index")
//	})
type Router struct {
	prefix     string
	middleware []Middleware
	registry   *registry
}

type registry struct {
	routes []*Route
}

// NewRouter returns an empty root router.
func NewRouter() *Router {
	return &Router{registry: &registry{}}
}

// Prefix returns a child router whose routes share a path prefix.
func (r *Router) Prefix(prefix string) *Router {
	return &Router{
		prefix:     joinPath(r.prefix, prefix),
		middleware: slices.Clone(r.middleware),
		registry:   r.registry,
	}
}

// Middleware returns a child router whose routes run extra middleware.
func (r *Router) Middleware(middleware ...Middleware) *Router {
	return &Router{
		prefix:     r.prefix,
		middleware: append(slices.Clone(r.middleware), middleware...),
		registry:   r.registry,
	}
}

// Group registers routes with this router's attributes.
func (r *Router) Group(routes func(r *Router)) {
	routes(r)
}

// Get registers a GET (and implicitly HEAD) route. T is the action's
// response type, inferred: r.Get("/trips", trips.Index).
func (r *Router) Get[T any](path string, action func(c *Context) (T, error)) *Route {
	return r.Match([]string{http.MethodGet}, path, action)
}

// Post registers a POST route.
func (r *Router) Post[T any](path string, action func(c *Context) (T, error)) *Route {
	return r.Match([]string{http.MethodPost}, path, action)
}

// Put registers a PUT route.
func (r *Router) Put[T any](path string, action func(c *Context) (T, error)) *Route {
	return r.Match([]string{http.MethodPut}, path, action)
}

// Patch registers a PATCH route.
func (r *Router) Patch[T any](path string, action func(c *Context) (T, error)) *Route {
	return r.Match([]string{http.MethodPatch}, path, action)
}

// Delete registers a DELETE route.
func (r *Router) Delete[T any](path string, action func(c *Context) (T, error)) *Route {
	return r.Match([]string{http.MethodDelete}, path, action)
}

// Match registers one route answering every method in methods.
func (r *Router) Match[T any](methods []string, path string, action func(c *Context) (T, error)) *Route {
	route := &Route{
		Methods:      slices.Clone(methods),
		Path:         joinPath(r.prefix, path),
		ResponseType: reflect.TypeFor[T](),
		handler:      func(c *Context) (any, error) { return action(c) },
		middleware:   slices.Clone(r.middleware),
	}
	r.registry.routes = append(r.registry.routes, route)

	return route
}

// Routes lists every registered route, in registration order.
func (r *Router) Routes() []*Route {
	return slices.Clone(r.registry.routes)
}

// URL builds a named route's path. Params fill its `{placeholders}` in
// order, path-escaped, so a `/`, `?` or `#` in one stays inside its segment;
// a `{rest...}` wildcard keeps its slashes.
func (r *Router) URL(name string, params ...string) (string, error) {
	index := slices.IndexFunc(r.registry.routes, func(route *Route) bool { return route.RouteName == name })
	if index < 0 {
		return "", fmt.Errorf("route %q is not defined", name)
	}

	segments := strings.Split(r.registry.routes[index].Path, "/")
	for i, segment := range segments {
		if !strings.HasPrefix(segment, "{") {
			continue
		}

		if len(params) == 0 {
			return "", fmt.Errorf("route %q: missing parameters", name)
		}

		segments[i] = escapeParam(params[0], strings.HasSuffix(segment, "...}"))
		params = params[1:]
	}

	if len(params) > 0 {
		return "", fmt.Errorf("route %q: too many parameters", name)
	}

	return strings.Join(segments, "/"), nil
}

// escapeParam path-escapes one parameter; a wildcard's value escapes each
// of its segments.
func escapeParam(param string, wildcard bool) string {
	if !wildcard {
		return url.PathEscape(param)
	}

	parts := strings.Split(param, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	return strings.Join(parts, "/")
}

func joinPath(prefix string, path string) string {
	joined := "/" + strings.Trim(strings.TrimRight(prefix, "/")+"/"+strings.TrimLeft(path, "/"), "/")
	if joined == "//" {
		return "/"
	}

	return joined
}
