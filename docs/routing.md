# Routing

- [Introduction](#introduction)
- [Basic Routing](#basic-routing)
    - [The Default Route Files](#the-default-route-files)
    - [Available Router Methods](#available-router-methods)
    - [Redirect Routes](#redirect-routes)
    - [Listing Your Routes](#listing-your-routes)
- [Route Parameters](#route-parameters)
    - [Required Parameters](#required-parameters)
    - [Wildcard Parameters](#wildcard-parameters)
    - [Optional Parameters and Constraints](#optional-parameters-and-constraints)
    - [Route Precedence](#route-precedence)
- [Named Routes](#named-routes)
- [Route Groups](#route-groups)
    - [Middleware](#middleware)
    - [Route Prefixes](#route-prefixes)
    - [How Groups Nest](#how-groups-nest)
- [Route Model Binding](#route-model-binding)
    - [Binding Models in the Controller](#binding-models-in-the-controller)
    - [Customizing the Key](#customizing-the-key)
    - [Scoping Bindings](#scoping-bindings)
    - [Binding Middleware](#binding-middleware)
- [Fallback Routes](#fallback-routes)
- [Rate Limiting](#rate-limiting)
    - [Defining Rate Limiters](#defining-rate-limiters)
    - [Attaching Rate Limiters to Routes](#attaching-rate-limiters-to-routes)
    - [Per-Minute Limits](#per-minute-limits)
    - [How Limits Are Counted](#how-limits-are-counted)
    - [Rate Limit Responses](#rate-limit-responses)
- [Accessing the Current Route](#accessing-the-current-route)
- [Cross-Origin Resource Sharing (CORS)](#cross-origin-resource-sharing-cors)
- [Compiling Routes](#compiling-routes)
- [Available Methods](#available-methods)

## Introduction

reef's router lives in the `github.com/atobeach/reef/web` package. You register routes on a `*web.Router`, then hand the router to an HTTP kernel, which compiles every route once, at boot, and serves them through the standard library's `net/http`. Routes are matched by Go's `http.ServeMux`, so the path syntax (`{slug}`, `{path...}`) and the matching rules are the ones you already know from the standard library.

The most basic route accepts a path and a handler:

```go
router := web.NewRouter()

router.Get("/greeting", func(c *web.Context) (string, error) {
	return "Hello World", nil
})
```

A handler (a controller action) receives the request as a `*web.Context` and returns the type it answers and an error. The router's methods are generic, so the action declares that type itself (`(string, error)` above, `([]data.AirportListing, error)` for a controller's index) and the compiler checks every `return` against it. A `*web.Response` is sent as you built it; any other value, a string included, is encoded as JSON; HTML is only ever an explicit `web.HTML` response. A failure is always the error, rendered as a JSON error response with its own status. The [responses documentation](responses.md) and the [error documentation](errors.md) cover each case.

## Basic Routing

### The Default Route Files

reef doesn't load route files for you. By convention, your application keeps its routes in a `routes` package, one file per area, with an exported function per group of routes that receives the router. In a typical reef application, `routes.Web` registers the routes outside `/api` and `routes.API` registers the API, and `bootstrap/app.go` calls both, giving the API its `/api` prefix and its middleware group:

```go
routes.Web(a.Router, deps)
routes.API(a.Router.Prefix("/api").Middleware(deps.Middleware.API...), deps)
```

The route files only name routes, groups and middleware. The controllers and middleware they register are built elsewhere (in the application's `bootstrap` package) and handed in, which is why `routes.API` receives a `deps` value rather than building anything itself:

```go
// referenceRoutes are the public reference reads.
func referenceRoutes(r *web.Router, c Controllers, m Middleware) {
	r.Middleware(m.Throttle("public-read")).Group(func(r *web.Router) {
		r.Get("/airports", c.Airports.Index).Name("airports.index")
		r.Get("/airports/{code}", c.Airports.Show).Name("airports.show")
	})
}
```

Once every route is registered, the router goes to `web.NewKernel`. The `*web.Kernel` it returns implements the standard library's `http.Handler` (its `ServeHTTP` method runs the whole pipeline), so any `*http.Server` can serve it:

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router: router,
})
if err != nil {
	return err
}

server := &http.Server{Addr: "127.0.0.1:8000", Handler: kernel}

return server.ListenAndServe()
```

The first argument binds the application's services (the database connection, the clock, the encrypter) to every request's context; it is required, so a kernel can't be built without stating it (`nil` binds nothing). `KernelConfig` takes the [global middleware](middleware.md#global-middleware), the [trusted proxies](requests.md#configuring-trusted-proxies), the logger, the debug flag and the error reporter; the [request lifecycle](lifecycle.md) and [error handling](errors.md) cover the rest.

### Available Router Methods

The router allows you to register routes that respond to each HTTP verb:

```go
r.Get("/airports", handler)
r.Post("/bookings", handler)
r.Put("/user/password", handler)
r.Patch("/profiles/{profile}", handler)
r.Delete("/favourites/{airport}", handler)
```

A `Get` route also answers `HEAD` requests, as `ServeMux` does.

Sometimes you may need a route that responds to several verbs. You may do so with the `Match` method, which registers one route, with one handler and one middleware stack, answering every method in the list:

```go
r.Match([]string{http.MethodGet, http.MethodPost}, "/broadcasting/auth", handler).Name("broadcasting.auth")
```

That is how reef's [broadcasting](broadcasting.md) package registers the `/broadcasting/auth` route: `broadcasting.Routes` calls `Match` and returns the route, so your route file still names it.

There is no `Any` method and no `Options` method: list the verbs you mean with `Match`. CORS preflight `OPTIONS` requests never need a route of their own, because the [CORS middleware](#cross-origin-resource-sharing-cors) answers them before routing. reef doesn't read a `_method` field to spoof verbs either; send the real verb.

Every registration method returns the `*web.Route` it registered. Its `Name` and `Middleware` methods return the route again, so you may chain them, and its exported fields describe the route as registered, the group prefix included:

```go
route := r.Get("/airports/{code}", c.Airports.Show).Name("airports.show")

fmt.Println(route.Methods, route.Path, route.RouteName) // [GET] /api/airports/{code} airports.show
```

`Methods` lists the verbs the route was registered with (a `Get` route lists `GET`; the implicit `HEAD` isn't stored), `Path` is the full path with its placeholders, `RouteName` is the name given with `Name`, or `""`, and `ResponseType` is the type the action answers (`*web.Response`, or the type sent as JSON), inferred from the action when it was registered.

#### Dependency Injection

reef has no service container, so a handler never receives injected arguments: it always takes the `*web.Context` alone. The services a handler needs are fields on its controller, set when the controller is built. See [dependency injection and controllers](controllers.md#dependency-injection-and-controllers).

### Redirect Routes

reef has no `Route::redirect` shortcut. A route that only redirects is a handler that returns `web.Redirect`, which is a `302 Found` with a `Location` header and a small HTML body linking to the target:

```go
r.Get("/deals", func(*web.Context) (*web.Response, error) {
	return web.Redirect("/flights"), nil
}).Name("deals")
```

You may pick another redirect status with `WithStatus`:

```go
r.Get("/destinations", func(*web.Context) (*web.Response, error) {
	return web.Redirect("/airports").WithStatus(http.StatusMovedPermanently), nil
}).Name("destinations")
```

There are no view routes either: reef renders no templates. A handler that sends HTML returns `web.HTML(…)`; a plain string is JSON. More on redirects, including redirecting to a named route, is in the [responses documentation](responses.md#redirects).

### Listing Your Routes

The `route:list` command shows an overview of every route your application defines. It comes from `webcmd.RouteListCommand` (`github.com/atobeach/reef/web/webcmd`, so the router itself never links the console), which you register on your [console kernel](console.md) with the router:

```go
kernel.Register(webcmd.RouteListCommand(a.Router))
```

The command lists every route sorted by path (routes on the same path keep their registration order), one line each: the methods, padded to one column, with `HEAD` listed after `GET`, then the path, dotted out to the route's name:

```shell
flights route:list
```

```text
  GET|HEAD      /api/airports ............................... airports.index
  PUT|PATCH     /api/profiles/{profile} .................... profiles.update
  GET|HEAD|POST /broadcasting/auth ....................... broadcasting.auth

  Showing [3] routes
```

`route:list` takes no options. To inspect routes from code (a test asserting that every route is named, or that no action answers `any`, say), the router's `Routes` method returns every registered route in registration order:

```go
for _, route := range tc.App.Router.Routes() {
	if route.RouteName == "" {
		t.Errorf("%s %s has no name", strings.Join(route.Methods, "|"), route.Path)
	}
}
```

## Route Parameters

### Required Parameters

Sometimes you will need to capture segments of the path within your route. For example, you may need to capture an airport's code from the URL. You may do so by defining a route parameter, and read it with the context's `Param` method:

```go
r.Get("/airports/{code}", func(c *web.Context) (string, error) {
	return "Airport " + c.Param("code"), nil
}).Name("airports.show")
```

You may define as many route parameters as your route requires:

```go
r.Get("/flights/{origin}/{destination}", func(c *web.Context) (map[string]string, error) {
	return map[string]string{
		"origin":      c.Param("origin"),
		"destination": c.Param("destination"),
	}, nil
}).Name("flights.between")
```

Route parameters are always encased within `{}` braces, and a parameter's name must be a valid Go identifier, unique within its path. A parameter matches exactly one whole path segment: `/files/{name}.json` is not a valid path. `Param` returns the segment decoded, so a `%2F` in the URL reaches you as a `/` inside that one parameter, and returns `""` for a name the route doesn't have.

### Wildcard Parameters

A parameter ending in `...` matches the rest of the path, slashes included. It must be the last segment of the path:

```go
r.Get("/assets/{path...}", func(c *web.Context) (string, error) {
	return "Serving " + c.Param("path"), nil
}).Name("assets.show")
```

A request for `/assets/images/logo.png` gives `images/logo.png`; an application might serve the images its emails link to from a route like this. The bare `/assets`, with or without a trailing slash, is a `404`, never a redirect to `/assets/`. A route that matches the bare path itself, such as `/{slug}`, still wins it.

> [!WARNING]
> A wildcard hands you whatever the client sent, `..` segments included. Clean the value and check that it stays where you expect before using it as a file path, for example with `path.Clean`.

### Optional Parameters and Constraints

reef has no optional parameters and no regular expression constraints (`where`, `whereNumber`). Where a segment is optional, register a second route without it. A parameter that names a model has its shape checked by its [binding](#customizing-the-key): a malformed id is simply a missing record, a `404` rather than a query the database rejects. Check any other parameter's shape in the controller.

### Route Precedence

When two routes could match a request, the more specific one wins, whatever order you registered them in. `/airports/featured` beats `/airports/{code}`, and `/airports/{code}/departures` never competes with `/airports/{code}` at all. Two routes that overlap without one being more specific (`/airports/{code}` and `/airports/{iata}`) are a boot error, as is registering the same method and path twice; see [compiling routes](#compiling-routes).

A trailing slash is ignored: `/api/airports/` reaches the `/api/airports` route. The escaping before the slash is kept, so `/api/bookings/a%2Fb/` still gives a `reference` of `a/b`. A path that isn't clean (`/api//airports`, `/a/../airports`) is answered with a redirect to its cleaned form instead of being routed.

An encoded dot segment (`%2e%2e`) is never cleaned away: it is a segment like any other, so `/api/x/%2e%2e/booking` never reaches the `/api/booking` route. Unless a route has a parameter in that position, it is a `404` (`The route api/x/../booking could not be found.`), with or without a trailing slash.

## Named Routes

Named routes allow the convenient generation of URLs for specific routes. You may name a route by chaining the `Name` method onto the route definition:

```go
r.Get("/bookings/{reference}", c.Bookings.Show).Name("bookings.show")
```

Route names should always be unique. `web.NewKernel` refuses a name given to two routes, so a duplicate fails at boot rather than leaving URL generation to whichever route was registered first:

```text
web: route name "airports.index" is given to both GET /api/airports and GET /api/airlines
```

Unnamed routes never collide. reef has no route name prefixes for groups (`->name('admin.')`): give each route its full name.

#### Generating URLs to Named Routes

Once you have named a route, the router's `URL` method builds its path, filling the placeholders in order:

```go
path, err := router.URL("bookings.show", "QX7P2M") // "/api/bookings/QX7P2M"
```

The [URL generation documentation](urls.md#urls-for-named-routes) covers escaping, wildcards, and absolute URLs.

#### Inspecting the Current Route

To find out which named route is handling the current request, use the context's `RouteName` method:

```go
if c.RouteName() == "bookings.show" {
	// ...
}
```

## Route Groups

Route groups allow you to share route attributes, such as middleware or a path prefix, across a large number of routes without defining them on each route. The router's `Middleware` and `Prefix` methods each return a child router carrying the accumulated attributes, and `Group` registers routes on it:

```go
r.Prefix("/api").Middleware(throttle).Group(func(r *web.Router) {
	r.Get("/airports", airports.Index).Name("airports.index")
})
```

### Middleware

To assign [middleware](middleware.md) to all routes within a group, call `Middleware` before defining the group. Middleware run in the order they are listed, and groups nest:

```go
r.Middleware(m.Auth, m.CSRF).Group(func(r *web.Router) {
	r.Middleware(appmiddleware.NoStore).Group(func(r *web.Router) {
		r.APIResource("/profiles/{profile}", controllers.ProfileController{})

		r.Get("/bookings", c.Bookings.Index).Name("bookings.index")
		r.Get("/bookings/{reference}", c.Bookings.Show).Name("bookings.show")
	})

	r.Get("/favourites", c.Favourites.Index).Name("favourites.index")
})
```

The bookings routes run `Auth`, then `CSRF`, then `NoStore`; the favourites route runs only `Auth` and `CSRF`. Middleware attached to a single route with the route's own `Middleware` method run after the group's.

`Group` is only a way to lay routes out. A child router registers routes directly too, which reads well for a single route:

```go
r.Middleware(m.Throttle("search")).Post("/flights/search", c.Flights.Search).Name("flights.search")
```

### Route Prefixes

The `Prefix` method may be used to prefix each route in the group with a path. For example, you may want to prefix all route paths within the group with `admin`:

```go
r.Prefix("/admin").Group(func(r *web.Router) {
	r.Get("/users", users.Index).Name("admin.users.index") // GET /admin/users
})
```

Prefixes and paths are joined with exactly one slash between them, so `Prefix("/admin/")`, `Prefix("admin")` and `Prefix("/admin")` all give `/admin/users`. Prefixes nest like middleware do.

reef has no subdomain routing and no group-level controllers: a route matches on its path alone, and names its handler directly.

### How Groups Nest

Each call to `Prefix` or `Middleware` copies the attributes it was called on, so a child never changes its parent: you may keep a child router in a variable and register on it as often as you like. Every child shares the root router's list of routes, so a route registered on any of them shows up in `router.Routes()`, in `route:list`, and in the kernel.

A route takes its prefix and middleware at the moment it is registered. Adding middleware to a router afterwards (which returns a new child anyway) never reaches routes registered before.

## Route Model Binding

When a route parameter names a model, `ResolveRouteBinding` loads it: it finds the row whose route key equals the parameter, or returns a `db.ModelNotFoundError`, which renders as a `404`. Go has no type-hinted action parameters to bind implicitly, so the action binds explicitly, in its first line, and then has the model in hand.

### Binding Models in the Controller

Call `ResolveRouteBinding` on the model's query with the parameter:

```go
func (BookingController) Show(c *web.Context) (models.Booking, error) {
	booking, err := models.Bookings(c).ResolveRouteBinding(c.Param("booking"))
	if err != nil {
		return models.Booking{}, err
	}

	return booking, nil
}
```

When no booking matches, the client receives a `404` whose message names the model in words:

```json
{"message": "Booking not found."}
```

The binding is a query like any other, so it eager-loads what the action needs: `models.Bookings(c).WithFlight().ResolveRouteBinding(c.Param("booking"))`.

`ResolveRouteBinding` is a method of every generated model's query (see [models](models.md)). It needs the model's schema to know its key, so calling it on a `db.Table` query panics.

### Customizing the Key

A model binds by its `id` unless it declares another column as its `routeKey` in `database/models.json`, as Eloquent's `getRouteKeyName` would. Here bookings bind by their public reference, and airports by their IATA code:

```json
"bookings": { "name": "Booking", "routeKey": "reference" },
"airports": { "name": "Airport", "routeKey": "code" },
```

The key must be an integer, uuid or text column; `go generate ./app/models` refuses any other. Its type also checks the parameter: a value the column can't hold, such as `not-a-uuid` for a uuid key, `abc` for an integer one, or a NUL byte or invalid UTF-8 (`%00`, `%FF` in the path) for a text one, is the same `404`, answered without sending Postgres a query it would reject.

### Scoping Bindings

The query `ResolveRouteBinding` is called on scopes the binding, so a child record is bound through its parent, or a record through the signed-in user, by starting from that relation or scope:

```go
booking, err := user.Bookings(c).ResolveRouteBinding(c.Param("booking"))

scheduled, err := models.Flights(ctx).Scheduled().WithOrigin().ResolveRouteBinding(flightID)
```

A record outside the scope is as missing as one that doesn't exist: the same `404`. When a record that exists but isn't the user's should be a `403` instead, bind it unscoped and authorize it with a policy.

### Binding Middleware

The binding inside the action runs after the route's middleware. When it must happen before one of them, bind in a middleware ahead of it: bind the model to the request context under an unexported key type, and read it back in the action through a typed accessor. Here a saved passenger is bound this way, ahead of the password confirmation, so a missing passenger is a `404` whatever the confirmation's state:

```go
// BindPassenger binds `{passenger}` as middleware ahead of the password
// confirmation, so a missing passenger 404s whatever the confirmation.
func (PassengerController) BindPassenger(c *web.Context, next web.Handler) (any, error) {
	passenger, err := models.SavedPassengers(c).ResolveRouteBinding(c.Param("passenger"))
	if err != nil {
		return nil, err
	}

	c.WithContext(context.WithValue(c.Context(), passengerKey{}, passenger))

	return next(c)
}

// passengerKey binds the passenger BindPassenger resolved.
type passengerKey struct{}

// boundPassenger is the passenger BindPassenger bound; ok is false on a route
// without it.
func boundPassenger(c *web.Context) (models.SavedPassenger, bool) {
	passenger, ok := c.Value(passengerKey{}).(models.SavedPassenger)

	return passenger, ok
}
```

```go
r.Delete("/user/passengers/{passenger}", accounts.SavedPassengers.Destroy).
	Middleware(controllers.PassengerController{}.BindPassenger, m.ConfirmPassword).
	Name("passengers.destroy")
```

The middleware has to run where the route parameters are known, so attach it to the route or its group, never to the global stack. Request-scoped values are covered in the [request documentation](requests.md#request-scoped-values).

## Fallback Routes

reef has no `Route::fallback`. A request no route matches gets a JSON `404`, and a request whose path matches a route but not its method gets a JSON `405` listing the allowed methods, with an `Allow` header naming the same methods (`Allow: GET, HEAD`):

```json
{"message": "The route nowhere could not be found."}
```

```json
{"message": "The POST method is not supported for route airports. Supported methods: GET, HEAD."}
```

Both go through your [global middleware](middleware.md#global-middleware), so they carry the same CORS and security headers as any other response. If you want to answer misses yourself, a route on `/` does it: `ServeMux` treats the root as the whole tree, so a `/` route matches every path no other route claims, for its methods:

```go
r.Get("/", func(c *web.Context) (*web.Response, error) {
	return nil, web.Abort(http.StatusNotFound, "Nothing lives at "+c.Path()+".")
}).Name("fallback")
```

> [!WARNING]
> A `/` route also turns misses for its other methods into `405`s: with the `GET` route above, a `POST` to an unknown path is a `405` rather than a `404`. To match the root alone, register `/{$}` instead. The router's `URL` method counts `{$}` as a placeholder, so it can't build that route's URL.

## Rate Limiting

### Defining Rate Limiters

reef includes a throttle middleware you may use to limit the traffic for a given route or group of routes. Limiters are defined by name on an `*middleware.RateLimiters` registry from the `github.com/atobeach/reef/web/middleware` package, built once at boot with `middleware.NewRateLimiters`:

```go
limiters := middleware.NewRateLimiters(middleware.RateLimitersConfig{
	Store:  store,
	UserID: guard.UserID,
})
```

`RateLimitersConfig` has two fields:

- `Store` is the `ratelimit.Store` that counts attempts: `ratelimit.NewRedisStore` shares the counters across every server, and `ratelimit.NewMemoryStore` keeps them in process, for tests. See [rate limiting](rate-limiting.md) for the stores and the `ratelimit.Limit` type.
- `UserID`, when set, reports the signed-in user's id, so [per-minute limits](#per-minute-limits) key on the user rather than the IP. A guard's `UserID` method fits.

Define a limiter with `For`, giving it a name and a function that returns the request's limit. `ratelimit.PerMinute(n)` allows `n` hits a minute, and `By` sets the bucket the request counts in, such as its IP (for other windows, see [windows other than a minute](rate-limiting.md#windows-other-than-a-minute)):

```go
limiters.For("public-read", func(c *web.Context) ratelimit.Limit {
	return ratelimit.PerMinute(180).By(c.IP())
})

limiters.For("login", func(c *web.Context) ratelimit.Limit {
	return ratelimit.PerMinute(5).By(c.String("email") + "|" + c.IP())
})
```

To enforce several limits at once, use `ForAll`. Every limit it returns is enforced: a typical use is a per-user bucket under a wider per-IP ceiling, so people sharing one office address don't throttle each other while one address is still bounded. Here `userKey` is the application's own helper, the signed-in user's id or else the IP:

```go
limiters.ForAll("search", func(c *web.Context) []ratelimit.Limit {
	return []ratelimit.Limit{
		ratelimit.PerMinute(15).By("search:" + userKey(c)),
		ratelimit.PerMinute(45).By("search-ip:" + c.IP()),
	}
})
```

The limiter's name is part of every bucket it counts in, and the key you give is hashed (it may hold an email address, and is as long as the request makes it), so two limiters never share a bucket even when their keys are equal. Define every limiter at boot, before any route is registered.

### Attaching Rate Limiters to Routes

Rate limiters are attached to routes or groups with the `Throttle` method, which returns the named limiter's middleware:

```go
r.Middleware(limiters.Throttle("public-read")).Group(func(r *web.Router) {
	r.Get("/airports", c.Airports.Index).Name("airports.index")
	r.Get("/airports/{code}", c.Airports.Show).Name("airports.show")
})
```

`Throttle` looks the name up as you call it, and panics when no limiter has that name. A typo therefore stops the application at boot instead of shipping an unthrottled route, and it means every limiter must be defined before the routes that use it are registered.

Where the throttle sits in a route's middleware decides what it counts. With the throttle listed first, a request a later guard refuses (a `404` for an unknown booking, say) still costs the caller an attempt, so probing with bad ids is as expensive as a real request.

### Per-Minute Limits

For a plain "n requests a minute" limit, `PerMinute` returns a throttle without defining a named limiter:

```go
r.Get("/airports/search", c.AirportSearch.Index).Middleware(limiters.PerMinute(60)).Name("airports.search.index")
```

It keys on the signed-in user when `RateLimitersConfig.UserID` reports one, and on the client IP otherwise.

> [!WARNING]
> Every `PerMinute` route shares one bucket per caller, whatever its `n`. A hit on a `PerMinute(60)` route spends an attempt on every `PerMinute(6)` route as well, so a caller who made six requests to the first is already refused by the second. Define a named limiter when a route needs a bucket of its own.

### How Limits Are Counted

Each limit counts in fixed windows: the first hit opens a window of the limit's decay, every hit inside it counts, and the counter resets when the window ends. The Redis store's windows are Redis key expiries; the memory store's run on the clock the request's context carries, so a test that freezes or moves time moves them too.

A throttle walks its limits in order, checking each before counting a hit on it. A request refused by a limit isn't counted on that limit or the ones after it, but it has already counted on the limits before it. Refused requests therefore never extend a caller's own lockout.

The throttle fails closed. If the store can't be reached, the request fails with a `500` rather than passing unthrottled, because limiters guard paid upstream calls and money routes.

### Rate Limit Responses

Every response that passes a throttle, error responses included, carries the limit with the fewest attempts left (the first one, on a tie):

```text
X-RateLimit-Limit: 120
X-RateLimit-Remaining: 119
```

Once a limit is exceeded, the client receives a `429 Too Many Requests` with the headers a frontend needs to back off. `Retry-After` is the number of seconds until the window resets (at least `1`), and `X-RateLimit-Reset` is that moment as a Unix timestamp:

```text
HTTP/1.1 429 Too Many Requests
Retry-After: 45
X-RateLimit-Reset: 1792497645
X-RateLimit-Limit: 5
X-RateLimit-Remaining: 0

{"message":"Too Many Attempts."}
```

You may return the same response from your own code with `web.TooManyRequests(ctx, limit, retryAfter)`, which takes the limit and how long the caller must wait, and reads the current time from `ctx`'s clock.

## Accessing the Current Route

The context describes the route handling the request:

```go
c.Param("code")  // a route parameter
c.RouteName()    // "airports.show", or "" for an unnamed route
c.RouteMatched() // whether a route is handling the request at all
c.Path()         // "api/airports/lhr": the path without its outer slashes
c.Is("api/*")    // whether the path matches any of the patterns
c.Method()       // "GET"
```

`Is` matches shell-style patterns against `Path`, and its `*` crosses slashes, so `api/*` covers every API route. Route and group middleware run after routing, so they see all of these straight away. [Global middleware](middleware.md#global-middleware) run before routing: they see the parameters and the matched route only once `next` has returned. See [the matched route](requests.md#the-matched-route) in the request documentation.

## Cross-Origin Resource Sharing (CORS)

reef can automatically respond to CORS `OPTIONS` preflight requests and add CORS headers to your responses, with the `middleware.Cors` middleware. Add it to the kernel's global middleware, so preflights are answered for any path without a route of their own, and so `404`s carry the same headers as real routes:

```go
maxAge := 0

kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router: router,
	Middleware: []web.Middleware{
		middleware.Cors(middleware.CorsConfig{
			Paths:               []string{"api/*", "sanctum/csrf-cookie", "login", "logout"},
			AllowedOrigins:      []string{"https://example.com", "https://www.example.com"},
			SupportsCredentials: true,
			MaxAge:              &maxAge,
		}),
	},
})
```

`CorsConfig` has four fields:

- `Paths` are the path patterns that answer cross-origin requests, matched like `c.Is` (`api/*` covers everything under `/api`). Requests on any other path pass through untouched.
- `AllowedOrigins` are the origins allowed, compared exactly against the request's `Origin` header. There are no wildcards or patterns: `"*"` only matches an `Origin` header that is literally `*`.
- `SupportsCredentials` sends `Access-Control-Allow-Credentials: true`, so the browser sends cookies along.
- `MaxAge` is sent as `Access-Control-Max-Age` on preflights, in seconds. `nil` omits the header.

A preflight is an `OPTIONS` request carrying `Access-Control-Request-Method`. The middleware answers it with a `204` and it never reaches routing: the response allows the requested method and echoes the requested headers (any method and header are allowed), names the origin when it is allowed, sends the credentials header when `SupportsCredentials` is on, adds `Access-Control-Max-Age` when `MaxAge` is set, and varies on `Origin`, `Access-Control-Request-Method` and `Access-Control-Request-Headers`. On any other request, an allowed origin gets `Access-Control-Allow-Origin` (and the credentials header) on the response, and the response always varies on `Origin`, merged with any `Vary` value a handler set, so caches keep one copy per origin.

An `OPTIONS` request without `Access-Control-Request-Method`, or a preflight on a path outside `Paths`, isn't answered by the middleware: it goes on to routing like any other request, and without an `OPTIONS` route it gets the same `405` (or `404`) as any other method no route answers.

There are no settings for allowed methods, allowed headers, origin patterns or exposed headers.

## Compiling Routes

reef has no route cache, because there is nothing to cache: `web.NewKernel` compiles every route once, when the application boots, and returns an error if any of them can't be served:

- a route name given to two routes;
- the same method and path registered twice (including a method a `Match` route already answers), or two paths that overlap without one being more specific;
- a path `ServeMux` can't parse, such as a parameter that isn't a whole segment or a `{name...}` wildcard that isn't last.

A duplicate name is reported in reef's own words, as shown in [named routes](#named-routes); the others come back as `web: compiling routes: ` followed by `ServeMux`'s explanation of the conflict or the bad pattern.

The route registration methods themselves don't validate anything, with two exceptions that panic at registration because they can only be programming errors: `APIResource` with a path that doesn't end in its `{parameter}` (see [resource controllers](controllers.md#resource-controllers)), and `Throttle` with an undefined limiter name.

## Available Methods

#### `web.NewRouter`

`NewRouter()` returns an empty root router.

#### `Router.Get`, `Post`, `Put`, `Patch`, `Delete`

`func (r *Router) Get[T any](path string, action func(c *Context) (T, error)) *Route` registers a route for one verb and returns it; `T`, the action's response type, is inferred (`r.Get("/bookings", c.Bookings.Index)`). A `Get` route answers `HEAD` too.

#### `Router.Match`

`func (r *Router) Match[T any](methods []string, path string, action func(c *Context) (T, error)) *Route` registers one route answering every method in `methods`.

#### `Router.APIResource`

`func (r *Router) APIResource[Index, Store, Show, Update, Destroy any](member string, controller ResourceController[Index, Store, Show, Update, Destroy])` registers an API resource's five routes from its member path (`"/profiles/{profile}"`), each action's type inferred from the controller, a `web.ResourceController`. See [resource controllers](controllers.md#resource-controllers).

#### `Router.Prefix`

`r.Prefix(prefix)` returns a child router whose routes share a path prefix.

#### `Router.Middleware`

`r.Middleware(middleware...)` returns a child router whose routes run extra middleware, after the router's own.

#### `Router.Group`

`r.Group(func(r *web.Router) { ... })` calls the function with the router, so its routes take the router's attributes.

#### `Router.Routes`

`r.Routes()` returns every registered route, in registration order, from any router in the tree.

#### `Router.URL`

`r.URL(name, params...)` builds a named route's path. See [URL generation](urls.md#urls-for-named-routes).

#### `Route.Name`

`route.Name("airports.show")` names the route for URL generation. Names are unique.

#### `Route.Middleware`

`route.Middleware(middleware...)` appends middleware to this route alone, run after its group's.

#### `Route.Methods`, `Route.Path`, `Route.RouteName`, `Route.ResponseType`

The route's verbs, its full path, its name (`""` when unnamed), and the `reflect.Type` its action answers.

#### `webcmd.RouteListCommand`

`webcmd.RouteListCommand(router)` is the `route:list` console command.

#### `middleware.NewRateLimiters`

`NewRateLimiters(middleware.RateLimitersConfig{Store: store, UserID: userID})` returns an empty `*middleware.RateLimiters` registry.

#### `middleware.RateLimitersConfig`

The registry's settings: `Store`, the `ratelimit.Store` that counts attempts, and `UserID`, an optional `func(c *web.Context) *int64` reporting the signed-in user's id for [per-minute limits](#per-minute-limits).

#### `RateLimiters.For` and `RateLimiters.ForAll`

`limiters.For(name, limit)` defines a limiter with one limit; `limiters.ForAll(name, limits)` defines one with several, all enforced.

#### `RateLimiters.Throttle`

`limiters.Throttle(name)` is the named limiter's middleware. An undefined name panics.

#### `RateLimiters.PerMinute`

`limiters.PerMinute(n)` allows `n` hits a minute per user or IP, in one bucket shared by every `PerMinute` route.

#### `web.TooManyRequests`

`TooManyRequests(ctx, limit, retryAfter)` is the throttle's `429` error, with its headers.

#### `middleware.Cors`

`Cors(middleware.CorsConfig{...})` answers cross-origin requests and preflights for the configured paths and origins.

#### `middleware.CorsConfig`

The CORS settings: `Paths`, `AllowedOrigins`, `SupportsCredentials` and `MaxAge` (`*int`, `nil` omits the header). See [CORS](#cross-origin-resource-sharing-cors).
