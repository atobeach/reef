# Middleware

- [Introduction](#introduction)
- [Defining Middleware](#defining-middleware)
    - [Middleware and Responses](#middleware-and-responses)
    - [Short-Circuiting the Request](#short-circuiting-the-request)
    - [Passing Values Downstream](#passing-values-downstream)
- [Registering Middleware](#registering-middleware)
    - [Global Middleware](#global-middleware)
    - [Assigning Middleware to Routes](#assigning-middleware-to-routes)
    - [Middleware Groups](#middleware-groups)
    - [Middleware Aliases](#middleware-aliases)
    - [Sorting Middleware](#sorting-middleware)
- [Middleware Parameters](#middleware-parameters)
- [Terminable Middleware](#terminable-middleware)
- [Available Middleware](#available-middleware)
    - [Encrypting Cookies](#encrypting-cookies)
    - [Middleware Elsewhere in reef](#middleware-elsewhere-in-reef)

## Introduction

Middleware provide a convenient mechanism for inspecting and filtering HTTP requests entering your application. For example, reef's authentication guard includes a middleware that verifies the user of your application is signed in. If the user is not, the middleware answers with a `401` straight away; if they are, it allows the request to proceed further into the application.

Additional middleware can be written to perform a variety of tasks besides authentication: a logging middleware might log every request, and a header middleware might harden every response. reef ships middleware for CORS, cookie encryption, CSRF protection and rate limiting in the `github.com/atobeach/reef/web/middleware` package, and the session and authentication packages bring their own. Your application's middleware typically live in `app/http/middleware`.

## Defining Middleware

A middleware is a function with the `web.Middleware` signature: it receives the request's `*web.Context` and `next`, the rest of the pipeline, and returns a value and an error, just as a [controller](controllers.md) does. `next` is a `web.Handler`, `func(c *web.Context) (any, error)`: the action with its response type erased, since a middleware wraps every route whatever its action answers. reef has no `make:middleware` generator; you write the function by hand. In this middleware, we will only allow access to the route if the supplied `token` input matches a specified value. Otherwise, we will redirect the user to `/home`:

```go
package middleware

import "github.com/atobeach/reef/web"

// EnsureTokenIsValid only lets a request through when its token input
// matches the secret.
func EnsureTokenIsValid(c *web.Context, next web.Handler) (any, error) {
	if c.String("token") != "my-secret-token" {
		return web.Redirect("/home"), nil
	}

	return next(c)
}
```

As you can see, if the given `token` does not match our secret token, the middleware returns an HTTP redirect to the client; otherwise, the request is passed further into the application. To pass the request deeper into the application (allowing the middleware to "pass"), you should call `next` with the context, and return what it returns.

It's best to envision middleware as a series of "layers" HTTP requests must pass through before they hit your application. Each layer can examine the request and even reject it entirely.

> [!NOTE]
> A middleware that needs nothing configured is a plain function (or a method value, such as `guard.Authenticate`). One that needs settings is a constructor returning a `web.Middleware`; see [middleware parameters](#middleware-parameters).

reef has no adapter for `func(http.Handler) http.Handler` middleware. The kernel is itself an `http.Handler`, so a standard library or ecosystem middleware that should run on the whole app wraps the kernel where the server is built.

### Middleware and Responses

A middleware can perform tasks before or after passing the request deeper into the application. This middleware performs its task **before** the request is handled:

```go
func BeforeMiddleware(c *web.Context, next web.Handler) (any, error) {
	// Perform action

	return next(c)
}
```

And this one performs its task **after** the request is handled:

```go
func AfterMiddleware(c *web.Context, next web.Handler) (any, error) {
	value, err := next(c)

	// Perform action

	return value, err
}
```

`next` returns what the rest of the pipeline returned, a value and an error, as a controller does. An error further in reaches the middleware as the error it is, wrapping included, so a middleware tells failures apart with `errors.As`, as any Go code does:

```go
value, err := next(c)
if invalid, ok := errors.AsType[*web.ValidationError](err); ok {
	slog.InfoContext(c, "validation failed", "fields", invalid.Errors.Keys())
}

return value, err
```

The kernel renders the error once, at its edge, after every middleware has returned. A panic further in arrives as an error too. A middleware may answer in place of an error, or fail after `next` on its own behalf; an unexpected error it replaced that way is still [logged and reported](errors.md#reporting-errors).

A middleware that changes the response, whatever it turned out to be, passes the outcome through `web.ToResponse`. On a value it gives the `*web.Response` the kernel would send; beside an error it gives the response a middleware further in already decorated, or else one holding only the error's status, and hands the error back untouched. Return both, and the kernel sends the headers and cookies you put on that response with the rendered error. A value that can't be encoded as JSON, or an outcome of nothing at all, comes back from `ToResponse` as an error, a `500`. Here an application's `NoStore` marks the response uncacheable whether the controller answered, validation failed or a guard refused:

```go
// NoStore marks a response uncacheable.
func NoStore(c *web.Context, next web.Handler) (any, error) {
	res, err := web.ToResponse(next(c))

	return res.WithHeader("Cache-Control", "max-age=0, no-store, private"), err
}
```

Nothing is written to the client until the whole pipeline has returned, so you may still change the headers and cookies of the response `ToResponse` gave you, and the status of a value's. When you need to know how the request went, read `res.Status()`, or one of its headers with `res.Header("Content-Type")`. The response's methods are covered in [changing responses in middleware](responses.md#changing-responses-in-middleware).

### Short-Circuiting the Request

To refuse a request, return an error before calling `next`; it is rendered with its own status. A middleware may also answer in the action's place with any value a controller could return:

```go
// EnsureBookingNotPaid refuses a booking that has already been paid.
func EnsureBookingNotPaid(c *web.Context, next web.Handler) (any, error) {
	booking, err := RequestBooking(c)
	if err != nil {
		return nil, err
	}

	if booking != nil && booking.PaidAt != nil {
		return nil, web.Abort(http.StatusConflict, "This booking has already been paid.")
	}

	return next(c)
}
```

The value a middleware returns is turned into a response exactly as a controller's is: a `*web.Response` is sent as built, anything else, a string included, as JSON, and an error as its JSON error response. The middleware wrapped around this one get the error itself from their own `next`, and decorate the refusal's response through `ToResponse` as they would a success. A panic in a middleware becomes a `500`, as it does in a controller.

> [!WARNING]
> Returning `nil, nil` is a bug, answered with a reported `500`. A middleware that means to pass the request on must return `next(c)`.

### Passing Values Downstream

A middleware often loads something the rest of the request needs. To bind a fact for the code below the controllers (actions, services and models, which only receive a `context.Context`), derive a new context from `c.Context()` and bind it with `WithContext`; everything downstream, `next` included, then sees it:

```go
// ShareLocale binds the visitor's preferred locale to the request's context.
func ShareLocale(c *web.Context, next web.Handler) (any, error) {
	if code := c.Cookie("locale"); code != "" {
		c.WithContext(locale.With(c.Context(), code))
	}

	return next(c)
}
```

Derive the new context from `c.Context()`, never from `c` itself: a context derived from `c` would make `c` its own parent, so `WithContext` panics on one and the kernel reports a `500`. A value only the controller needs, such as a [bound model](routing.md#binding-middleware), is bound the same way under an unexported key type the controller reads through a typed accessor. Both are covered in the [request documentation](requests.md#request-scoped-values).

## Registering Middleware

### Global Middleware

If you want a middleware to run during every HTTP request to your application, list it in the kernel's `Middleware` field. The first entry is the outermost layer:

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router: a.Router,
	Middleware: []web.Middleware{
		appmiddleware.ShareRequestContext,
		middleware.Cors(cfg.Cors),
		appmiddleware.SecurityHeaders(cfg.App.IsProduction()),
	},
})
```

Global middleware run before routing. That is what lets the [CORS middleware](routing.md#cross-origin-resource-sharing-cors) answer preflights for paths with no `OPTIONS` route, and what gives `404` and `405` responses the same headers as real routes. It also means a global middleware sees no route parameters and no matched route until `next` has returned.

### Assigning Middleware to Routes

If you would like to assign middleware to specific routes, you may chain the route's `Middleware` method when defining the route:

```go
r.Get("/bookings/{reference}", c.Bookings.Show).Middleware(appmiddleware.NoStore).Name("bookings.show")
```

You may assign several middleware to the route by passing them all; they run in the order given:

```go
r.Get("/checkout", c.Checkout.Show).Middleware(First, Second).Name("checkout.show")
```

To assign middleware to a group of routes, call the router's `Middleware` method before the group, as described in [route groups](routing.md#route-groups). A route's own middleware run after its group's.

#### Excluding Middleware

reef has no `withoutMiddleware`. Because a group's middleware are fixed when a route is registered, a route that shouldn't run one of them belongs outside that group: register it on the parent router, or split the group in two. A payment verification route, for instance, can be registered on its own child router so it gets a throttle of its own instead of the booking routes' shared one.

### Middleware Groups

Sometimes you may want to group several middleware under a single name, to make them easier to assign to routes. A middleware group is simply a `[]web.Middleware`, spread into `Middleware` with `...`:

```go
webGroup := []web.Middleware{cookies, startSession, csrf, a.Auth.ResolveUser}

r.Middleware(webGroup...).Group(func(r *web.Router) {
	r.Post("/login", c.Accounts.AuthenticatedSession.Store).Middleware(m.Throttle("login"), m.Guest).Name("login.store")
	r.Get("/user", c.Users.Show).Name("user.show")
})
```

reef predefines no groups, and nothing is applied to your routes automatically. A typical application defines two, built in its `bootstrap` package: `Web` for the browser routes outside `/api` (encrypted cookies, the session, the CSRF check, then the signed-in user) and `API` for everything under `/api` (the same, without the CSRF check). `bootstrap/app.go` applies `API` when it registers the `/api` routes:

```go
routes.API(a.Router.Prefix("/api").Middleware(deps.Middleware.API...), deps)
```

### Middleware Aliases

Laravel lets you refer to middleware by a short string alias. In reef, middleware are values, so the equivalent is a struct of named fields that your route files receive. Here the application's `routes.Middleware` holds its groups and aliases, built once at boot:

```go
// Middleware is what the route files name: the app's middleware groups and
// aliases, built once at boot.
type Middleware struct {
	Web []web.Middleware
	API []web.Middleware

	Auth, Guest     web.Middleware
	CSRF            web.Middleware
	ConfirmPassword web.Middleware
	// ...

	Throttle  func(name string) web.Middleware
	PerMinute func(n int) web.Middleware
}
```

```go
return routes.Middleware{
	Web: []web.Middleware{cookies, startSession, csrf, a.Auth.ResolveUser},
	API: []web.Middleware{cookies, startSession, a.Auth.ResolveUser},

	Auth:            a.Auth.Authenticate,
	Guest:           a.Auth.RedirectIfAuthenticated,
	CSRF:            csrf,
	ConfirmPassword: auth.RequirePassword(cfg.Auth.PasswordTimeout),
	// ...

	Throttle:  a.Limiters.Throttle,
	PerMinute: a.Limiters.PerMinute,
}
```

A route file then reads like a list of aliases (`m.Auth`, `m.Throttle("login")`), and the compiler catches a misspelled one. An alias that needs settings at the route, such as a throttle's name, is a field holding the constructor (`Throttle func(name string) web.Middleware`) rather than a built middleware.

### Sorting Middleware

reef has no priority list: middleware run in exactly the order you assign them, outermost first:

1. the global middleware, in the order of `KernelConfig.Middleware`;
2. each group's middleware, outer groups first, in the order given;
3. the route's own middleware, in the order given;
4. the controller.

On the way out, the same layers unwind in reverse. reef never reorders or deduplicates middleware, so a middleware assigned twice runs twice.

Order often carries meaning. Account routes that run `Auth` before `CSRF` give a guest a `401` rather than a `419`, and routes that run their throttle before an authorization check make a refused request still count against the caller. The `Web` group above resolves the user after the CSRF check, because a remember-me sign-in regenerates the token the check compares against.

## Middleware Parameters

A middleware can't receive extra arguments when it runs, so a middleware that needs settings is a constructor: a function taking the settings and returning the `web.Middleware`. For example, a middleware that requires the user to have a given role:

```go
// EnsureUserHasRole lets only users with role through.
func EnsureUserHasRole(role string) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		if !slices.Contains(roles(c), role) {
			return nil, web.Abort(http.StatusForbidden, "This action is unauthorized.")
		}

		return next(c)
	}
}
```

The parameters are passed when the route is defined:

```go
r.Put("/flights/{flight}", flights.Update).Middleware(EnsureUserHasRole("dispatcher")).Name("flights.update")
```

reef's own constructors follow the same shape: `auth.RequirePassword(timeout)`, `limiters.Throttle("login")`, `middleware.Cors(config)`. Build anything expensive in the constructor rather than in the function it returns, which runs on every request.

## Terminable Middleware

Responses are buffered, so work a middleware does after `next` returns still happens before the client receives the response:

```go
// LogDuration logs how long the request took.
func LogDuration(c *web.Context, next web.Handler) (any, error) {
	started := time.Now()
	res, err := web.ToResponse(next(c))

	slog.InfoContext(c, "request handled", "route", c.RouteName(), "status", res.Status(), "duration", time.Since(started))

	return res, err
}
```

Keep that work quick. Anything slow that can happen after the response has been sent belongs on a [queue](queues.md).

reef's terminate hook belongs to the request rather than the middleware: `c.Terminating(fn)` registers `fn` to run once the request is done, after the response has been written. It runs however the request ended: after a [streamed response](responses.md#streamed-responses) finished, when a middleware threw the response away (a session save that failed after the action returned), and after a panic. Callbacks run last registered first, as `t.Cleanup` does, and one that panics is reported while the rest still run.

Register the release of something as you take it, and no later path can leak it. Here a chat controller holds one of a fixed number of turn slots while its turn streams:

```go
select {
case ctl.Turns <- struct{}{}:
case <-c.Done():
	return c.Err()
}

// Held until the request is done: after the streamed turn ends, or at
// once when a middleware discards the stream and it never runs.
c.Terminating(func() { <-ctl.Turns })
```

Keep `Terminating` to cleanup: a buffered body's last bytes may still be on their way to the client when it runs.

## Available Middleware

The `github.com/atobeach/reef/web/middleware` package holds reef's stock middleware:

| Middleware | Purpose | Documented in |
| --- | --- | --- |
| `middleware.Cookies(encrypter, except...)` | Encrypts outgoing cookies, decrypts incoming ones, and attaches queued cookies. | [Encrypting cookies](#encrypting-cookies) |
| `middleware.Cors(config)` | Answers cross-origin requests and preflights. | [CORS](routing.md#cross-origin-resource-sharing-cors) |
| `middleware.PreventRequestForgery(config)` | Verifies the CSRF token on state-changing requests. | [CSRF protection](csrf.md) |
| `limiters.Throttle(name)`, `limiters.PerMinute(n)` | Rate limits a route, from a `middleware.RateLimiters` registry. | [Rate limiting](routing.md#rate-limiting) |

### Encrypting Cookies

`middleware.Cookies` seals your application's cookies with an encrypter, so the client can neither read nor forge them. List the cookies that should travel unencrypted after the encrypter:

```go
cookies := middleware.Cookies(a.Encrypter, "locale")
```

On the way in, it URL-decodes and decrypts every cookie and makes the plaintext what `c.Cookie` returns; a cookie that doesn't open under any of the encrypter's keys, or isn't valid URL encoding, reads as absent, and an excepted cookie reads as sent, URL-decoded. The cookies as they arrived stay readable through `c.RawCookies()`. On the way out, it attaches the cookies [queued](responses.md#queueing-cookies) on the request, seals every cookie except the excepted ones and the ones being deleted (a negative `MaxAge`), and URL-encodes every value. A cookie that fails to seal fails the request with a `500`.

Run it outside the session and CSRF middleware (earlier in the list), since the session reads its decrypted cookie and both attach cookies it must seal. The value format and the `cookie` package are covered in [cookies and encryption](responses.md#cookies-and-encryption).

> [!WARNING]
> Queued cookies only reach the response through this middleware. A route that doesn't run it never sends them.

### Middleware Elsewhere in reef

Other packages ship middleware of their own:

- `session.StartSession(config)` loads and saves the database session. See [session](session.md).
- `guard.Authenticate` answers guests with a `401`, `guard.RedirectIfAuthenticated` sends a signed-in user away from guest-only routes, and `guard.ResolveUser` resolves the user up front so `auth.ID(ctx)` answers anywhere downstream. See [authentication](authentication.md).
- `auth.RequirePassword(timeout)` requires a recent password confirmation, answering `423` otherwise. See [password confirmation](authentication.md#password-confirmation).
