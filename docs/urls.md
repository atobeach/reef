# URL Generation

- [Introduction](#introduction)
- [The Basics](#the-basics)
    - [Generating URLs](#generating-urls)
    - [Accessing the Current URL](#accessing-the-current-url)
    - [Accessing the Previous URL](#accessing-the-previous-url)
- [URLs for Named Routes](#urls-for-named-routes)
    - [Parameter Escaping](#parameter-escaping)
    - [Query Strings](#query-strings)
    - [Models](#models)
    - [Errors](#errors)
- [Absolute URLs](#absolute-urls)
- [What reef Leaves Out](#what-reef-leaves-out)

## Introduction

reef provides a few helpers to assist you in generating URLs for your application: the router builds the path of any [named route](routing.md#named-routes), and the request context knows the scheme and host the client used. They are primarily helpful when building links in your API responses, or when generating redirect responses to another part of your application.

## The Basics

### Generating URLs

reef has no `url()` helper. A path your application serves is just a string, and the request's `Root` method gives you the scheme and host to put in front of it. The root is the site as the client reached it, with no trailing slash, read through your [trusted proxies](requests.md#configuring-trusted-proxies):

```go
link := c.Root() + "/airports/" + url.PathEscape(airport.Code)

// https://api.example.com/airports/LHR
```

When the path belongs to one of your routes, prefer [building it from the route's name](#urls-for-named-routes), so it follows the route if its path ever changes.

### Accessing the Current URL

The request context describes the URL being handled:

```go
// The full URL, query string included...
c.FullURL() // https://api.example.com/api/airports/LHR?currency=GBP

// The scheme and host...
c.Root() // https://api.example.com

// The path, without its outer slashes...
c.Path() // api/airports/LHR
```

`FullURL` uses the scheme and host the client used: `X-Forwarded-Proto` and `X-Forwarded-Host` count only when they come from a trusted proxy. There is no separate method for the current URL without its query string; for any path but the root, `c.Root() + "/" + c.Path()` builds it. To test the path against patterns, use `c.Is("api/airports/*")`, whose `*` crosses slashes. The [request documentation](requests.md) covers these methods in full.

### Accessing the Previous URL

The session middleware records the URL of every page request (a `GET` that matched a route, isn't an XHR and isn't a prefetch) in the session's `_previous` attribute, with the route's name:

```go
type previousURL struct {
	URL   string  `json:"url"`
	Route *string `json:"route"` // nil when the route has no name
}

if s := session.FromContext(c); s != nil {
	if previous, ok := session.Key[previousURL]("_previous").Get(s); ok {
		fmt.Println(previous.URL, previous.Route)
	}
}
```

See [the previous URL](session.md#the-previous-url) in the session documentation.

## URLs for Named Routes

The router's `URL` method generates the path of a named route. Named routes allow you to generate URLs without being coupled to the path defined on the route, so if the route's path changes, your calls to `URL` need no changes. For example, imagine your application contains a route like the following:

```go
router.Prefix("/api").Group(func(r *web.Router) {
	r.Get("/flights/{origin}/{destination}", c.Flights.Between).Name("flights.between")
})
```

To generate its path, pass the route's name and a value for each placeholder, in the order they appear in the path:

```go
path, err := router.URL("flights.between", "LHR", "JFK")
if err != nil {
	return err
}

// /api/flights/LHR/JFK
```

`URL` returns the full path, group prefixes included, without a scheme or host. The parameters are positional strings, not a map: the first fills the first placeholder, and so on.

Your controllers reach the router the same way they reach any other service: as a field, set when the controller is built. A typical application's `bootstrap` package builds one `*web.Router` and keeps it on the app as `a.Router`:

```go
// BookingController serves the signed-in user's bookings.
type BookingController struct {
	Router *web.Router
}

// Index lists the bookings, each with a link to its detail.
func (ctl *BookingController) Index(c *web.Context) (map[string]string, error) {
	reference := "QX7P2M" // ...

	path, err := ctl.Router.URL("bookings.show", reference)
	if err != nil {
		return nil, err
	}

	return map[string]string{"url": c.Root() + path}, nil
}
```

To redirect to a named route, see [redirecting to named routes](responses.md#redirecting-to-named-routes).

### Parameter Escaping

Each parameter is path-escaped, so it stays inside its own segment whatever it holds: a `/`, `?` or `#` in a value is escaped rather than starting a new segment, a query string or a fragment, and the route receives the value back unchanged through `c.Param`:

```go
router.URL("bookings.show", "a/b?c#d") // /api/bookings/a%2Fb%3Fc%23d
```

A `{path...}` wildcard is the exception: its value keeps its slashes, and each of its segments is escaped on its own:

```go
router.URL("assets.show", "images/logo #1.png") // /assets/images/logo%20%231.png
```

### Query Strings

`URL` builds paths only. It doesn't turn extra parameters into a query string; extra parameters are an [error](#errors). Add a query string with the standard library's `net/url`:

```go
path, err := router.URL("airports.show", "LHR")
if err != nil {
	return "", err
}

return c.Root() + path + "?" + url.Values{"currency": {"GBP"}}.Encode(), nil

// https://api.example.com/api/airports/LHR?currency=GBP
```

### Models

`URL` takes strings, so pass the model's route key yourself: the column its `routeKey` names in `database/models.json`, or its id (see [customizing the key](routing.md#customizing-the-key)). That is the value `ResolveRouteBinding` finds the model by when the route is followed:

```go
path, err := router.URL("airports.show", airport.Code)
```

Convert a numeric key with `strconv.FormatInt(profile.ID, 10)`.

### Errors

`URL` returns an error rather than a broken path when:

- no route has the name: `route "bookings.shw" is not defined`;
- there are fewer parameters than placeholders: `route "flights.between": missing parameters`;
- there are more parameters than placeholders: `route "bookings.show": too many parameters`.

These are programming errors, so returning the error from your controller (a `500`) is usually right.

`URL` looks names up in registration order, and works as soon as a route is registered, before the kernel is built. Route names are only checked for uniqueness by `web.NewKernel`, which refuses a name given twice, so in a booted application each name builds exactly one route.

## Absolute URLs

`URL` returns a path, and `c.Root()` the origin the current request reached, so `c.Root() + path` is the absolute URL of a route on the same host. The authentication guard's `RedirectIfAuthenticated` middleware sends a signed-in user to the site root with exactly that: `web.Redirect(c.Root())`.

Outside a request (a [mailable](mail.md), a [queued job](queues.md), a [console command](console.md)) there is no request to take the host from. Keep your application's public URL in configuration and prefix the path with it; an application's emails might take `APP_URL` and `FRONTEND_URL` from its config for this reason.

## What reef Leaves Out

reef has no signed URLs (`URL::signedRoute`, the `signed` middleware), no URLs for controller actions (`action()`), no fluent URI objects and no URL default values. Link to routes by name, and when you need a link that can't be guessed or tampered with, put a random token in it (`support.RandomString(40)`), store the token, and look it up when the link is followed.
