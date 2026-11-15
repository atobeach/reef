# Controllers

- [Introduction](#introduction)
- [Writing Controllers](#writing-controllers)
    - [Basic Controllers](#basic-controllers)
    - [Single Action Controllers](#single-action-controllers)
    - [Controller Return Values](#controller-return-values)
- [Controller Middleware](#controller-middleware)
- [Resource Controllers](#resource-controllers)
    - [Actions Handled by Resource Controllers](#actions-handled-by-resource-controllers)
    - [Partial Resource Routes](#partial-resource-routes)
    - [Nested Resources](#nested-resources)
    - [Naming Resource Routes](#naming-resource-routes)
    - [Supplementing Resource Controllers](#supplementing-resource-controllers)
    - [Middleware and Resource Controllers](#middleware-and-resource-controllers)
- [Dependency Injection and Controllers](#dependency-injection-and-controllers)
- [Keeping Controllers Thin](#keeping-controllers-thin)

## Introduction

Instead of defining all of your request handling logic as closures in your route files, you may wish to organize this behavior using controller types. Controllers can group related request handling logic into a single type. For example, a `ProfileController` might handle all incoming requests related to traveler profiles, including showing, creating, updating, and deleting them. By convention, controllers live in your application's `app/http/controllers` package, one controller per file.

## Writing Controllers

### Basic Controllers

A controller is a struct whose methods are functions of the request answering their response type and an error: they take the request's `*web.Context` and return the value they send and an error. reef has no `make:controller` generator and no base controller to embed; any struct will do. Let's take a look at an example of a basic controller:

```go
package controllers

import (
	"context"
	"time"

	"example.com/flights/app/models"
	"github.com/atobeach/reef/cache"
	"github.com/atobeach/reef/web"
)

// AirportController serves the airports flights depart from and arrive at.
type AirportController struct {
	Cache cache.Store
}

// Index lists the airports.
func (ctl *AirportController) Index(c *web.Context) ([]models.Airport, error) {
	return models.Airports(c).OrderBy(models.AirportColumns.Name).Get()
}

// Show is one airport, by its IATA code.
func (ctl *AirportController) Show(c *web.Context) (models.Airport, error) {
	code := c.Param("code")

	return cache.Remember(c, ctl.Cache, "airports:"+code, time.Hour, func(ctx context.Context) (models.Airport, error) {
		return models.Airports(ctx).ResolveRouteBinding(code)
	})
}
```

Once you have written a controller and its methods, you may define routes to them by passing the method values to the router:

```go
r.Get("/airports", c.Airports.Index).Name("airports.index")
r.Get("/airports/{code}", c.Airports.Show).Name("airports.show")
```

The router infers each action's response type from the method value, so the route files read the same whatever an action answers, and the compiler checks every `return` in the action against its declared type. When an incoming request matches the route, the method runs with the request's context; it reads route parameters with `c.Param`, as `Show` does above, and loads the model a parameter names with [`ResolveRouteBinding`](routing.md#route-model-binding), a `404` when there is none. A method value is bound to its controller when the route is registered, so `c.Airports` must already be built at that point; see [dependency injection](#dependency-injection-and-controllers).

> [!NOTE]
> The `*web.Context` is a `context.Context` too, carrying the request's deadline, cancellation and bound services. Pass it straight to models, caches and outbound calls, as `models.Airports(c)` does.

### Single Action Controllers

If a controller action is particularly complex, you might find it convenient to dedicate an entire controller type to that single action. By convention, its one method is called `Invoke`:

```go
// FaqController serves the FAQ items shown on the marketing site.
type FaqController struct{}

// Invoke lists the FAQ items.
func (FaqController) Invoke(*web.Context) ([]faq.Item, error) {
	return faq.Items(), nil
}
```

A controller with no dependencies needs no construction, so the route may name a zero value directly:

```go
r.Get("/faqs", controllers.FaqController{}.Invoke).Name("faqs.index")
```

A handler doesn't have to be a method at all. Any function of the request answering its response type and an error is a controller action:

```go
// CsrfCookie is an empty 204 for the XSRF-TOKEN cookie the web group's CSRF
// middleware attaches; the SPA calls it before its first state change.
func CsrfCookie(*web.Context) (*web.Response, error) {
	return web.NoContent(), nil
}
```

```go
r.Get("/sanctum/csrf-cookie", controllers.CsrfCookie).Name("sanctum.csrf-cookie")
```

### Controller Return Values

What a controller returns decides the response. An action declares the type it answers: `(*web.Response, error)` when it sets the status or headers, sends a non-JSON body or answers more than one shape, and the value's own type otherwise:

| Returned | Response |
| --- | --- |
| a `*web.Response` | sent as built: `web.JSON(v).WithStatus(201)`, `web.NoContent()`, `web.Redirect(url)`, `web.HTML(body)`, a stream |
| a `string` | `200` with the string encoded as JSON (HTML is only ever `web.HTML`) |
| `nil`, or a nil `*web.Response` | a reported `500`: answer a value or an error |
| any other value | `200` with the value encoded as JSON |
| an error | the error's JSON response, with its status |

A failure is always returned as the error, never as a `*web.Response` carrying a `4xx` or `5xx`: that way every middleware around the action, `errors.As` and the error reporter see it as the failure it is. When an action returns an error, the value beside it is discarded, so return the zero value with it, as the examples on this page do; only a [middleware](middleware.md#middleware-and-responses) can put headers and cookies on an error's response.

An error that knows its status (`web.Abort(404, "...")`, a validation failure, `db.ModelNotFoundError`) renders as that status with its own JSON body (`{"message": ...}`, plus `errors` for a validation failure), even when you wrap it with `fmt.Errorf("...: %w", err)`. Any other error is a `500` whose message is `Server Error` unless debug mode is on, and a panic is caught and treated the same way. See [responses](responses.md) and [error handling](errors.md).

Validate input with a form request before using it; [validation](validation.md) covers `web.Validate`:

```go
request, err := web.Validate[requests.ProfileRequest](c)
if err != nil {
	return nil, err
}
```

## Controller Middleware

[Middleware](middleware.md) are assigned to a controller's routes in your route files:

```go
r.Get("/bookings/{reference}", c.Bookings.Show).Middleware(appmiddleware.NoStore).Name("bookings.show")
```

reef has no `HasMiddleware` interface and no middleware attributes: a controller never declares its own middleware, so the route files stay the one place to see what runs in front of an action. A controller may still provide middleware, though: a method with the `web.Middleware` signature is a middleware, and a route may name it. A saved-passenger controller can bind the `{passenger}` parameter ahead of the password confirmation this way (see [binding middleware](routing.md#binding-middleware)):

```go
r.Delete("/user/passengers/{passenger}", accounts.SavedPassengers.Destroy).
	Middleware(controllers.PassengerController{}.BindPassenger, m.ConfirmPassword).
	Name("passengers.destroy")
```

## Resource Controllers

If you think of each model in your application as a "resource", it is typical to perform the same set of actions against each one. For example, a user's saved passenger profiles can be listed, created, read, updated and deleted.

Because of this common use case, reef's resource routing assigns the typical CRUD routes to a controller with a single line of code. A resource controller implements `web.ResourceController`, the interface of the five actions:

```go
type ResourceController[Index, Store, Show, Update, Destroy any] interface {
	Index(c *web.Context) (Index, error)
	Store(c *web.Context) (Store, error)
	Show(c *web.Context) (Show, error)
	Update(c *web.Context) (Update, error)
	Destroy(c *web.Context) (Destroy, error)
}
```

Each action answers its own type, and `APIResource` infers all five from the controller you pass it.

Here is a traveler profiles controller, in outline:

```go
// ProfileController is the traveller profiles resource.
type ProfileController struct{}

// Index lists the user's profiles, newest first.
func (ProfileController) Index(c *web.Context) ([]models.Profile, error) {
	return auth.User[models.User](c).Profiles(c).Latest().Get()
}

// Store creates a profile for the signed-in user (201).
func (ProfileController) Store(c *web.Context) (*web.Response, error) {
	request, err := web.Validate[requests.ProfileRequest](c)
	if err != nil {
		return nil, err
	}

	profile, err := models.Profiles(c).Create(db.Values{
		models.ProfileColumns.UserID:    auth.User[models.User](c).ID,
		models.ProfileColumns.FirstName: request.FirstName,
		// ...
	})
	if err != nil {
		return nil, err
	}

	return web.JSON(profile).WithStatus(http.StatusCreated), nil
}

// Show is one of the user's profiles.
func (ProfileController) Show(c *web.Context) (models.Profile, error) {
	return auth.User[models.User](c).Profiles(c).ResolveRouteBinding(c.Param("profile"))
}

// Update changes one of the user's profiles.
func (ProfileController) Update(c *web.Context) (models.Profile, error) {
	// ...
}

// Destroy deletes one of the user's profiles (204).
func (ProfileController) Destroy(c *web.Context) (*web.Response, error) {
	profile, err := auth.User[models.User](c).Profiles(c).ResolveRouteBinding(c.Param("profile"))
	if err != nil {
		return nil, err
	}

	if err := profile.Delete(c); err != nil {
		return nil, err
	}

	return web.NoContent(), nil
}
```

Next, you may register a resource route that points to the controller with the router's `APIResource` method, giving it the resource's member path, the path of one record with its parameter:

```go
r.APIResource("/profiles/{profile}", controllers.ProfileController{})
```

This single route declaration creates multiple routes to handle the resource's actions. Remember, you can always get a quick overview of your application's routes by running the [`route:list`](routing.md#listing-your-routes) command.

### Actions Handled by Resource Controllers

| Verb | Path | Action | Route Name |
| --- | --- | --- | --- |
| `GET` | `/profiles` | `Index` | `profiles.index` |
| `POST` | `/profiles` | `Store` | `profiles.store` |
| `GET` | `/profiles/{profile}` | `Show` | `profiles.show` |
| `PUT`/`PATCH` | `/profiles/{profile}` | `Update` | `profiles.update` |
| `DELETE` | `/profiles/{profile}` | `Destroy` | `profiles.destroy` |

`PUT` and `PATCH` are one route, answered by `Update`. reef registers API resources only: there are no `create` and `edit` routes, since reef renders no forms. The member path must end in its `{parameter}`; `APIResource` panics at registration when it doesn't, since that can only be a programming error.

Like any route, a resource's routes take the prefix and middleware of the router you register them on. Under an `/api` prefix the paths above become `/api/profiles` and `/api/profiles/{profile}`, while the names stay `profiles.index` and so on.

Each member action starts by binding its `{profile}` with `ResolveRouteBinding`, which answers a missing record with a `404` itself: see [route model binding](routing.md#route-model-binding). Binding through the user's own profiles makes another user's profile a `404` too; bind unscoped and authorize with a policy instead when that case should be a `403` (see [scoping bindings](routing.md#scoping-bindings)). reef has no `missing` callback.

### Partial Resource Routes

`APIResource` always registers all five actions; it has no `only` or `except`. When a resource should handle a subset of them, register those routes yourself, with the names `APIResource` would have given them:

```go
r.Get("/profiles", controllers.ProfileController{}.Index).Name("profiles.index")
r.Get("/profiles/{profile}", controllers.ProfileController{}.Show).Name("profiles.show")
```

Such a controller needn't implement `web.ResourceController` at all.

### Nested Resources

Sometimes you may need to define routes to a nested resource. For example, a flight may have many seats. To nest the resource, give `APIResource` the full member path:

```go
r.APIResource("/flights/{flight}/seats/{seat}", &controllers.FlightSeatController{})
```

This registers the five routes on `/flights/{flight}/seats` and `/flights/{flight}/seats/{seat}`. The controller reads both parameters with `c.Param`, and should bind the seat through its flight so a seat can only be reached under the flight it belongs to (see [scoping bindings](routing.md#scoping-bindings)):

```go
// Show is one of the flight's seats.
func (ctl *FlightSeatController) Show(c *web.Context) (models.Seat, error) {
	flight, err := models.Flights(c).ResolveRouteBinding(c.Param("flight"))
	if err != nil {
		return models.Seat{}, err
	}

	return flight.Seats(c).ResolveRouteBinding(c.Param("seat"))
}
```

### Naming Resource Routes

A resource's routes are named after the static segments of the parent path, joined with dots, followed by the action: `/profiles/{profile}` gives `profiles.index` through `profiles.destroy`, and `/flights/{flight}/seats/{seat}` gives `flights.seats.index` through `flights.seats.destroy`. The router's prefix is not part of the name.

The names can't be overridden. When you need different names, register the routes yourself, as in [partial resource routes](#partial-resource-routes). The parameter's name is whatever you write in the member path, so there is nothing to rename there either.

### Supplementing Resource Controllers

If you need to add routes to a resource controller beyond the default set, register them alongside the resource:

```go
r.Get("/profiles/default", controllers.ProfileController{}.Default).Name("profiles.default")
r.APIResource("/profiles/{profile}", controllers.ProfileController{})
```

Order doesn't matter: `/profiles/default` is more specific than `/profiles/{profile}`, so it [wins wherever it is registered](routing.md#route-precedence).

Remember to keep your controllers focused. If you find yourself routinely needing methods outside of the typical set of resource actions, consider splitting your controller into two, smaller controllers.

### Middleware and Resource Controllers

`APIResource` returns nothing to chain, so to assign middleware to a resource's routes, register the resource on a router that carries them:

```go
r.Middleware(appmiddleware.NoStore).APIResource("/profiles/{profile}", controllers.ProfileController{})
```

Or, within a group:

```go
r.Middleware(appmiddleware.NoStore).Group(func(r *web.Router) {
	r.APIResource("/profiles/{profile}", controllers.ProfileController{})
})
```

To give one action middleware of its own, register that action's route yourself, as in [partial resource routes](#partial-resource-routes).

## Dependency Injection and Controllers

reef has no service container, so nothing is resolved for you. A controller declares the services it needs as exported fields, and your application builds it once, at boot, with a struct literal:

```go
// UserController serves the signed-in user's /user endpoints.
type UserController struct {
	Auth *auth.Guard[models.User]
}

// Show is the signed-in user, or a 401 `{}` for a guest.
func (ctl *UserController) Show(c *web.Context) (models.User, error) {
	user, ok, err := ctl.Auth.User(c)
	if err != nil {
		return models.User{}, err
	}

	if !ok {
		return models.User{}, &exceptions.HTTPResponseError{Status: http.StatusUnauthorized, Body: struct{}{}}
	}

	return user, nil
}
```

In a typical application, every controller with dependencies is built at boot, in the `bootstrap` package, and the route files receive the built controllers; a stateless one is the [zero value](#single-action-controllers) its route names directly:

```go
return routes.Controllers{
	Users:     &controllers.UserController{Auth: a.Auth},
	Airports:  &controllers.AirportController{Cache: a.Cache /* ... */},
	// ...
}
```

Building controllers this way keeps every dependency visible at the call site, makes a controller trivial to construct in a test, and means a missing dependency is a compile error rather than a container lookup failing at runtime. A controller is shared by every request, so keep request state in the `*web.Context`, never in its fields.

There is no method injection either: an action always receives just the context. The request's input, route parameters, signed-in user and bound services are all read from it (`c.Param`, `web.Validate`, `auth.User`, `models.Airports(c)`).

## Keeping Controllers Thin

A controller should read the request, call into your application, and shape the response. When an endpoint runs a multi-step flow (several queries, a call to a third-party API, a mail to send), put the flow in a type of its own with one `Handle` method that returns an outcome, and let the controller map that outcome to the response. These types are often called actions. An `actions.ChangeSeat`, for example, might return a `SeatChangeOutcome` (changed or taken), and the controller maps each to its response, a `409` when the seat is already taken. Actions take a `context.Context`, never shape JSON, and can be tested without HTTP.
