# HTTP Requests

- [Introduction](#introduction)
- [Interacting With the Request](#interacting-with-the-request)
    - [Accessing the Request](#accessing-the-request)
    - [The Request Is a Context](#the-request-is-a-context)
    - [Request Path, Host, and Method](#request-path-host-and-method)
    - [Request Headers](#request-headers)
    - [The Request Body](#the-request-body)
    - [Request IP Address](#request-ip-address)
    - [Content Negotiation](#content-negotiation)
    - [The Matched Route](#the-matched-route)
    - [Request-Scoped Values](#request-scoped-values)
- [Input](#input)
    - [Retrieving Input](#retrieving-input)
    - [Input Presence](#input-presence)
    - [Merging Additional Input](#merging-additional-input)
    - [Cookies](#cookies)
    - [Input Trimming and Normalization](#input-trimming-and-normalization)
    - [How Input Is Parsed](#how-input-is-parsed)
- [Files](#files)
- [Configuring Trusted Proxies](#configuring-trusted-proxies)
    - [Working With IP Ranges](#working-with-ip-ranges)
- [Available Methods](#available-methods)

## Introduction

reef's `web.Context` gives you an object-oriented way to work with the HTTP request your application is handling: its path, headers and client IP, the input and cookies it was sent with, and the route that matched it. Every controller action and every middleware receives one.

The `Context` is also a `context.Context`. Models, services and the clock all take it directly, so the request's deadline, its cancellation and the services bound to it (the database connection, the clock, whatever your middleware binds) travel with every call you make from a controller.

## Interacting With the Request

### Accessing the Request

A controller action is a function that receives the request's `*web.Context` and returns the value of its response type and an error. You don't need to ask a container for the request; it is the action's only argument:

```go
package controllers

import (
	"example.com/flights/app/models"
	"github.com/atobeach/reef/web"
)

// AirportController serves the airport pages.
type AirportController struct{}

// Show is GET /api/airports/{code}.
func (AirportController) Show(c *web.Context) (models.Airport, error) {
	return models.Airports(c).ResolveRouteBinding(c.Param("code"))
}
```

Middleware receive the same `*web.Context`, along with the `next` function that runs the rest of the pipeline. See the [middleware documentation](middleware.md) for how to write them, and [controllers](controllers.md) for how actions are organized.

#### Building a Request in a Unit Test

To unit test a function that takes a request, without routing one through the kernel, build it with `web.NewContext`. It takes the context the request carries, a method and a target, and returns a request to `example.com` (unless the target is an absolute URL naming its own host) with an empty body, no peer address (so `IP` is empty) and no matched route. You set its headers, cookies included, with `SetHeader`:

```go
c := web.NewContext(t.Context(), http.MethodGet, "/")
c.SetHeader("Accept-Language", "fr-FR")

locale := locales.Preferred(c)
```

Anything the code reads from the context, such as a [session](session.md#sessions-outside-a-request), goes in the context you pass. A method or target the standard library can't parse (`"/%zz"`) panics, as a test's own mistake should.

To test through the routes, middleware and error rendering instead, send the request with the [HTTP test client](http-tests.md).

#### Route Parameters

The `Param` method returns a route parameter by name: `{code}` in `/airports/{code}`, or the rest of the path for a `{path...}` wildcard. An absent parameter is an empty string. Defining parameters is covered in the [routing documentation](routing.md#route-parameters):

```go
r.Get("/assets/{path...}", func(c *web.Context) (*web.Response, error) {
	name := path.Clean("assets/" + c.Param("path"))

	// ...
})
```

### The Request Is a Context

`*web.Context` implements `context.Context`: its `Deadline`, `Done`, `Err` and `Value` methods answer for the request's context, which the `Context` method returns. That is why you can hand `c` straight to anything that takes a `context.Context`:

```go
user, err := models.Users(c).FindOrFail(id)

now := clock.Now(c)
```

When a request's client disconnects, `c.Done()` is closed and `c.Err()` reports the cancellation, just as they would for the standard library's request context.

`c` is the request as it is now. Its context methods answer for whatever is bound when they are asked, so a later bind shows through them, and through the values of any context you derive from `c` (`context.WithoutCancel(c)`, `context.WithValue(c, …)`). Cancellation is read once: a context derived with `context.WithCancel`, `context.WithTimeout` or `context.AfterFunc` takes `c`'s `Done` and `Deadline` when it is made and keeps that cancellation, and a `Done` channel read before a bind is not the one after it. Derive such a context after any bind it depends on: a `WithTimeout(c, d)` taken before the booking binds `context.WithoutCancel(c.Context())` is still cancelled when the client disconnects. `c.Context()` is a snapshot: the context bound so far, without `c` around it. Hand work that outlives the handler, such as a goroutine or a streamed chat turn, `c.Context()`:

```go
// The turn outlives the request's cancellation (the abort guard).
ctx := context.WithoutCancel(c.Context())
```

The context methods (`Deadline`, `Done`, `Err`, `Value`, `Context`) are safe to call from any goroutine, so a background refresh that was handed `c` can read it while the request goes on binding. The rest of `*web.Context` (`Body`, `All`, `SetHeader`, `QueueCookie`, `Terminating` and the like) belongs to the request's goroutine, as `*http.Request`'s methods do: `Body` and `All` cache what they read, the way `Request.ParseForm` caches into `r.Form`.

#### Binding Values to the Request Context

Middleware binds request-scoped facts and services for everything downstream with the `WithContext` method, which binds a new context in place of the request's. The binding is in place, so an outer middleware sees it too once `next` returns. The code below the controllers then reads those facts from the context it is handed, never by asserting it back to a `*web.Context`:

```go
type clientIPKey struct{}

// ShareClientIP binds the client IP for the services below the controllers.
func ShareClientIP(c *web.Context, next web.Handler) (any, error) {
	c.WithContext(context.WithValue(c.Context(), clientIPKey{}, c.IP()))

	return next(c)
}
```

> [!WARNING]
> Always derive the new context from `c.Context()`, never from `c` itself. A context derived from `c` and bound back into it would make `c` its own parent, so `WithContext` panics on one ("derive it from c.Context()"), as it does on a `nil` context, and the kernel reports the panic as a `500`. A context derived from another request's `*web.Context` binds as any other does.

### Request Path, Host, and Method

#### Retrieving the Request Path

The `Path` method returns the request's path without its surrounding slashes. A request for `https://example.com/api/airports/` returns `api/airports`, and the root returns `/`:

```go
path := c.Path() // "api/airports"
```

#### Inspecting the Request Path

The `Is` method reports whether the path matches any of the given patterns. Pattern slashes at either end are ignored, and `*` is the only wildcard. Unlike Go's `path.Match`, it also matches across slashes, so `api/*` covers every route under `/api`:

```go
if c.Is("api/*", "sanctum/csrf-cookie") {
	// ...
}
```

#### Retrieving the Request URL

The `FullURL` method returns the URL the client asked for, including its query string. The `Root` method returns just the scheme and host, without a trailing slash: the place a redirect to `/` should point. Both use the scheme and host the client used: behind a [trusted proxy](#configuring-trusted-proxies) they follow `X-Forwarded-Proto` and `X-Forwarded-Host`:

```go
url := c.FullURL() // "https://example.com/api/airports?currency=EUR"

root := c.Root() // "https://example.com"
```

The `IsSecure` method reports whether the request came over HTTPS: a TLS connection, or `X-Forwarded-Proto: https` from a trusted proxy.

#### Retrieving the Request Method

The `Method` method returns the HTTP verb. A `HEAD` request answered by a `GET` route reports `HEAD`:

```go
if c.Method() == http.MethodPost {
	// ...
}
```

### Request Headers

The `Header` method returns a request header's value, or an empty string when the header is absent. Header names are case-insensitive. When a header carries several values, `Header` returns the first:

```go
signature := c.Header("X-Webhook-Signature")
```

The `HasHeader` method reports whether the header was sent at all, which tells a header sent empty from an absent one:

```go
if c.HasHeader("X-Device-Id") {
	device := c.Header("X-Device-Id")
	// ...
}
```

A middleware that rewrites a request fact before anything reads it uses `SetHeader` and `RemoveHeader`. Both act on every line of the header, so a second, forged line never survives them (see [trusted proxies](#working-with-ip-ranges)):

```go
c.SetHeader("X-Forwarded-For", visitor)

c.RemoveHeader("X-Forwarded-For")
```

### The Request Body

The `Body` method returns the raw request body, whatever its method or content type. It is read once, up to the 8 MiB input cap, and kept, so it is the same bytes every call, before or after `All` parsed them. A body over the cap is the `413` error, and, as with `io.ReadAll`, a failed read returns its error along with the bytes read before it. A webhook verifies its signature over these bytes:

```go
payload, err := c.Body()
// Over the cap is the bad-signature 401: a 413 would tell a prober the cap.
if len(payload) > maxWebhookBody {
	return nil, web.Abort(http.StatusUnauthorized, "")
}
if err != nil {
	return nil, err
}

if !validSignature(payload, c.Header("X-Webhook-Signature")) {
	return nil, web.Abort(http.StatusUnauthorized, "")
}
```

### Request IP Address

The `IP` method returns the address of the client that made the request. It is resolved through the [trusted proxies](#configuring-trusted-proxies) you configure, so a header sent by an untrusted peer can never move it. The per-IP [rate limits](rate-limiting.md) are keyed on it:

```go
ratelimit.PerMinute(60).By("booking-ip:" + c.IP())
```

### Content Negotiation

reef offers a few methods for inspecting what the client accepts:

- `WantsJSON` reports whether the first type in the `Accept` header is JSON (it contains `/json` or `+json`).
- `IsXHR` reports an `X-Requested-With: XMLHttpRequest` request.
- `ExpectsJSON` reports a caller that should get JSON rather than a redirect: an XHR (without `X-PJAX`) whose `Accept` header is absent or starts with `*`, or any request that `WantsJSON`.

```go
if c.ExpectsJSON() {
	return map[string]bool{"two_factor": true}, nil
}

return web.Redirect("/two-factor-challenge"), nil
```

### The Matched Route

The `RouteName` method returns the name of the route handling the request, and `RouteMatched` reports whether a route matched at all. A route without a name reports an empty `RouteName` but still `RouteMatched`:

```go
if c.RouteName() == "bookings.show" {
	// ...
}
```

Route and group middleware run after routing, so they see the route straight away. Global middleware run before routing: they see the matched route only once `next` has returned:

```go
func LogMisses(c *web.Context, next web.Handler) (any, error) {
	value, err := next(c)

	if !c.RouteMatched() {
		slog.InfoContext(c, "no route matched", "path", c.Path())
	}

	return value, err
}
```

### Request-Scoped Values

A value that lives for one request, such as something a middleware loaded for the controller, is [bound to the request context](#binding-values-to-the-request-context) under an unexported key type and read back through a typed accessor. There is no string-keyed bag: the key type is private to the package that binds it, so nothing else can collide with it or read it as the wrong type. Here a controller binds the saved passenger a route parameter names this way:

```go
// passengerKey binds the passenger BindPassenger resolved.
type passengerKey struct{}

func (PassengerController) BindPassenger(c *web.Context, next web.Handler) (any, error) {
	passenger, err := models.SavedPassengers(c).ResolveRouteBinding(c.Param("passenger"))
	if err != nil {
		return nil, err
	}

	c.WithContext(context.WithValue(c.Context(), passengerKey{}, passenger))

	return next(c)
}

// boundPassenger is the passenger BindPassenger bound; ok is false on a route
// without it.
func boundPassenger(c *web.Context) (models.SavedPassenger, bool) {
	passenger, ok := c.Value(passengerKey{}).(models.SavedPassenger)

	return passenger, ok
}
```

A lookup asked for twice in one request is memoized the same way, under a key only the memo reads. Here `appmiddleware.RequestBooking` reads the row for the request's `booking_id` once, for the rate limiter, the policy check and the controller alike:

```go
func RequestBooking(c *web.Context) (*models.Booking, error) {
	if resolved, ok := c.Value(requestBookingKey{}).(resolvedBooking); ok {
		return resolved.booking, resolved.err
	}

	// ... look the row up ...

	c.WithContext(context.WithValue(c.Context(), requestBookingKey{}, resolved))

	return resolved.booking, resolved.err
}
```

A fact other code reads, such as the signed-in user (`auth.ID`), is bound by the middleware or the write that establishes it (`ResolveUser`, `Login`), never by a read: asking for a value never changes what another reader sees. A value kept in the session goes through a [`session.Key[T]`](session.md#interacting-with-the-session) instead.

## Input

### Retrieving Input

#### Retrieving All Input Data

The `All` method returns all of the request's input as a `validation.Input` (a `map[string]any`): the query string merged with the request body, with the body's keys winning. It returns an error only when the input can't be read, which you should return as is: a query string that can't be decoded and an oversized body render as a `400` and a `413`, and any other read failure is a `500` (see [request errors](errors.md#request-errors)):

```go
input, err := c.All()
if err != nil {
	return nil, err
}
```

Input is parsed once per request: later calls, including the ones `web.Validate` and `Input` make, return the same map. A failure is kept the same way: every later call returns the same error, never a partial map.

> [!NOTE]
> Most actions never read raw input at all. A [form request](validation.md#form-request-validation) validates the input and decodes it into a typed struct in one call: `request, err := web.Validate[requests.StoreBookingRequest](c)`.

#### Retrieving an Input Value

The `Input` method returns a single value from `All`, or `nil` when the key is absent (or when the input couldn't be read). Values keep their decoded types: strings are `string`, JSON numbers are `json.Number`, JSON booleans are `bool`, lists are `[]any` and objects are `map[string]any`. Values from a query string or a form body are always strings, or lists and objects of strings:

```go
if id, ok := c.Input("booking_id").(string); ok {
	// ...
}
```

You may use "dot" notation to reach into nested objects, and a numeric segment indexes a list:

```go
fareClass := c.Input("fare.class")
firstName := c.Input("passengers.0.first_name")
```

The `Array` method returns the list at a key, with `false` when there is none (an absent key, a string, an object not keyed `"0"` to `"n-1"`). An object keyed `"0"` to `"n-1"`, which some clients send for a list, reads as that list, in index order, so an empty object reads as an empty list:

```go
messages, _ := c.Array("messages")
```

#### Retrieving Input From the Query String

While `All` merges the query string with the body, the `Query` method reads only the query string. It returns an empty string when the key is absent, and the first value when the key repeats. Unlike `Input`, it returns the value without any [normalization](#input-trimming-and-normalization): untrimmed, and `""` rather than `nil` when blank:

```go
currency := strings.ToUpper(strings.TrimSpace(c.Query("currency")))
```

#### Retrieving String Input Values

The `String` method returns a scalar input value as text. A string is returned as is, a JSON number as it was written, and `true` as `"1"`. Anything else (an absent key, `null`, `false`, a list or an object) is an empty string:

```go
email := strings.ToLower(c.String("email"))
```

#### Retrieving Boolean Input Values

When dealing with checkbox-like inputs, the `Boolean` method returns `true` for `true`, `1`, `"1"`, `"true"`, `"on"` and `"yes"` (case-insensitive, surrounding spaces ignored), and `false` for everything else:

```go
remember := c.Boolean("remember")
```

### Input Presence

The `Has` method reports whether every key it is given is present, even with no value: `?error` and `?error=` both have `error`:

```go
if c.Has("error") {
	// The user cancelled on the provider's consent screen...
}
```

The `Filled` method reports whether a key is present and not blank. A string is blank when it is empty or only whitespace, and a list or an object is blank when it is empty. A number or a boolean, `false` included, is always filled:

```go
if c.Filled("recovery_code") {
	// ...
}
```

Because blank strings [become `nil`](#input-trimming-and-normalization), a key sent as `""` is present with a `nil` value: `Has` is how you tell a key sent as `null` (or blank) from one not sent at all.

### Merging Additional Input

The `Merge` method adds values to the request's input, replacing the keys it already has, so validation and every later read see them. The registration, profile and password-reset controllers lowercase the email this way before validating it:

```go
if email, ok := c.Input("email").(string); ok {
	c.Merge(map[string]any{"email": strings.ToLower(email)})
}
```

Merged values are taken as given: they aren't [trimmed or normalized](#input-trimming-and-normalization). When the input couldn't be read, `Merge` leaves it that way, and `All` still returns its error.

reef has no "old input": the frontend owns its form state, and a form request's `Validated()` keeps the keys as sent when a partial update needs them (see [validation](validation.md)).

### Cookies

The `Cookie` method returns a cookie's value, or an empty string when the request has no such cookie:

```go
currency := c.Cookie("currency")
```

Behind the [`middleware.Cookies`](middleware.md#encrypting-cookies) middleware, the cookies your application issues are encrypted, and `Cookie` returns the decrypted value. A cookie that fails to decrypt, because it was tampered with or sealed under another name or key, reads as absent. Cookies named in the middleware's exception list are read without decryption, only URL-decoded (the middleware URL-encodes every value it sends). A value that isn't valid URL encoding reads as absent either way. Ahead of that middleware (in a global middleware, for example), `Cookie` returns the raw value the browser sent. See [cookies and encryption](responses.md#cookies-and-encryption) for how outgoing cookies are sealed.

The `RawCookies` method returns the cookies as the browser sent them, in order and before any decryption, as standard library `*http.Cookie` values. It reads leniently: a malformed cookie (a third party's, say) is skipped, never the whole header. The `SetCookies` method replaces the cookie values `Cookie` reads from. Together they are how `middleware.Cookies` installs its decrypted view of the request; you only need them if you write a cookie middleware of your own:

```go
decrypted := map[string]string{}
for _, incoming := range c.RawCookies() {
	// ...
}
c.SetCookies(decrypted)
```

To send cookies back, attach them to the response or queue them on the context; see [attaching cookies to responses](responses.md#attaching-cookies-to-responses).

### Input Trimming and Normalization

reef normalizes every string in the input, from the query string and the body alike, at any depth:

- Surrounding whitespace is trimmed, except in fields named `password`, `password_confirmation` and `current_password`, whose whitespace is part of the secret.
- An empty string, or a string of only whitespace, becomes `nil`.

So a blank field fails a `required` rule instead of passing as `""`, and a blank optional field reads as `nil`. This normalization always runs; there is nothing to switch on or off.

### How Input Is Parsed

The body is parsed into the input for every method except `GET` and `HEAD`, according to its `Content-Type`:

| Content type | Input |
|---|---|
| a JSON type (`application/json`, `application/vnd.api+json`, anything ending in `json`) | the body's object; numbers stay `json.Number` |
| `application/x-www-form-urlencoded` | parsed like a query string |
| anything else (`multipart/form-data`, `text/plain`, none) | nothing |

A JSON body that isn't an object (blank, malformed, or a bare array) reads as empty input, so validation reports the missing fields instead of the request failing on its body.

Query strings and form bodies use the bracketed keys the frontend's query serializer and HTML forms send:

| Sent | Input |
|---|---|
| `child_ages[]=3&child_ages[]=7` | `{"child_ages": ["3", "7"]}` |
| `n[0]=a&n[1]=b` | `{"n": ["a", "b"]}` |
| `gap[0]=a&gap[2]=c` | `{"gap": {"0": "a", "2": "c"}}` (not `0..n-1`, so it stays an object) |
| `departure_times[outbound]=morning` | `{"departure_times": {"outbound": "morning"}}` |
| `q=one&q=two` | `{"q": "two"}` (without brackets, the last value wins) |
| `a.b=1&first+name=x` | `{"a_b": "1", "first_name": "x"}` (dots and spaces in a top-level name become underscores) |
| `0=a&1=b` | `{"0": "a", "1": "b"}` (the input itself always stays an object) |

A few limits keep one request from building unbounded structures:

- A body over 8 MiB makes `All` fail with a `413`.
- A key nested more than 64 levels deep is dropped.
- Parsing stops after 1,000 variables in the query string, and after 1,000 in a form body.
- A query string that can't be decoded makes `All` fail with a `400` and the message `Malformed query string.`.
- A form-urlencoded body with a malformed percent-escape, or a body that can't be read, makes `All` return that error as is, so it renders as a `500` and is [logged and reported](errors.md#reporting-errors).

The body is read once and kept: [`Body`](#the-request-body) returns the same bytes `All` parsed, so a webhook's signature check can still read it after a middleware asked for input.

## Files

reef reads no uploads: a `multipart/form-data` body adds nothing to the input, and there is no form-file or storage API. Its raw bytes are still what [`Body`](#the-request-body) returns, up to the 8 MiB cap, should an endpoint ever need to parse one itself.

## Configuring Trusted Proxies

When your application runs behind a load balancer or a CDN, the TCP peer is the proxy, not the client. Tell the kernel which peers to believe with the `TrustedProxies` field of its configuration, built by `web.ParseTrustedProxies`:

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router:         router,
	TrustedProxies: web.ParseTrustedProxies(env.List("TRUSTED_PROXIES", "*")),
})
```

`ParseTrustedProxies` accepts:

| Entries | Behavior |
|---|---|
| none (or a `nil` `*TrustedProxies`) | No proxy is trusted: `c.IP()` is the TCP peer and forwarded headers are ignored. This is the default. |
| `[]string{"*"}` | Only the immediate peer is trusted: the client is the rightmost `X-Forwarded-For` entry. |
| IPs and CIDRs (`"10.0.0.0/8"`, `"192.0.2.7"`) | A peer inside the ranges is trusted. The client is found by walking `X-Forwarded-For` from the right, skipping trusted hops, to the first untrusted address (the leftmost entry when every hop is trusted). |

A few details are worth knowing:

- Entries that don't parse are dropped rather than trusted, and `"*"` only means "the immediate peer" as the sole entry.
- A malformed address met while walking `X-Forwarded-For` from the right, before the first untrusted one, makes the whole chain untrustworthy: the client is then the TCP peer. Entries beyond the client are never read.
- A trusted peer that sends no `X-Forwarded-For` is itself the client.
- `X-Forwarded-Proto` (for `IsSecure`, `FullURL` and `Root`) and `X-Forwarded-Host` (for `FullURL` and `Root`) are read only from a trusted peer, and only their first value counts.

> [!WARNING]
> Trust no proxy unless one is really there. An application serving clients directly with `"*"` lets any client choose its own IP with an `X-Forwarded-For` header, and with it a fresh rate-limit bucket on every request.

### Working With IP Ranges

reef exports the helpers it resolves client IPs with, for middleware that vets an edge's own headers. `ParsePrefixes` parses IPs and CIDRs into `netip.Prefix` values (a bare IP becomes a single-address prefix), dropping anything malformed. `Contains` reports whether an IP falls inside any of them (an IPv4-mapped IPv6 address is compared as IPv4), and `ValidIP` reports a bare IPv4 or IPv6 address, with no zone and no port:

```go
// TrustCloudflare pins X-Forwarded-For to the visitor Cloudflare vouched for.
func TrustCloudflare(ranges []string) web.Middleware {
	cloudflare := web.ParsePrefixes(ranges)

	return func(c *web.Context, next web.Handler) (any, error) {
		peer := strings.TrimSpace(c.Header("X-Real-IP"))
		visitor := strings.TrimSpace(c.Header("CF-Connecting-IP"))

		if web.ValidIP(visitor) && web.Contains(cloudflare, peer) {
			c.SetHeader("X-Forwarded-For", visitor)
		}

		return next(c)
	}
}
```

## Available Methods

Every method of `*web.Context`, at a glance. Outside the kernel, `web.NewContext(ctx, method, target string) *web.Context` builds one for a [unit test](#building-a-request-in-a-unit-test).

| Method | Returns |
|---|---|
| `All() (validation.Input, error)` | all input, query and body merged |
| `Array(key string) ([]any, bool)` | the list at a key ([details](#retrieving-an-input-value)) |
| `Body() ([]byte, error)` | the raw body, read once and kept ([details](#the-request-body)) |
| `Boolean(key string) bool` | a checkbox-style input flag ([details](#retrieving-boolean-input-values)) |
| `Context() context.Context` | the context bound so far: a snapshot, to derive one to bind from or hand to work that outlives the handler |
| `Cookie(name string) string` | a cookie's (decrypted) value, `""` when absent |
| `Deadline() (time.Time, bool)` | the request context's deadline |
| `Done() <-chan struct{}` | closed when the request is canceled |
| `Err() error` | why the request context ended |
| `ExpectsJSON() bool` | whether to answer with JSON rather than a redirect |
| `Filled(key string) bool` | whether an input key is present and not blank |
| `FullURL() string` | scheme, host, path and query |
| `Has(keys ...string) bool` | whether every input key is present, even null ([details](#input-presence)) |
| `HasHeader(name string) bool` | whether the header was sent, even empty |
| `Header(name string) string` | a request header's first value |
| `IP() string` | the client address, through trusted proxies |
| `Input(key string) any` | one dotted input key |
| `Is(patterns ...string) bool` | whether the path matches a pattern |
| `IsSecure() bool` | whether the request came over HTTPS |
| `IsXHR() bool` | whether it is an `XMLHttpRequest` |
| `Merge(values map[string]any)` | adds values to the input ([details](#merging-additional-input)) |
| `Method() string` | the HTTP verb |
| `Param(name string) string` | a route parameter |
| `Path() string` | the path without surrounding slashes |
| `Query(name string) string` | a raw query-string value |
| `QueueCookie(cookie *http.Cookie)` | queues a cookie for the response ([details](responses.md#queueing-cookies)) |
| `QueuedCookies() []*http.Cookie` | the cookies queued so far |
| `RawCookies() []*http.Cookie` | the cookies as sent, before decryption |
| `RemoveHeader(name string)` | removes every line of a request header |
| `Root() string` | the site root: scheme and host |
| `RouteMatched() bool` | whether a route is handling the request |
| `RouteName() string` | the matched route's name |
| `SetCookies(values map[string]string)` | replaces the readable cookie values |
| `SetHeader(name, value string)` | replaces every line of a request header |
| `String(key string) string` | a scalar input value as text |
| `Terminating(fn func())` | runs `fn` once the request is done, however it ended ([details](middleware.md#terminable-middleware)) |
| `Value(key any) any` | a value bound to the request context |
| `WantsJSON() bool` | whether the first `Accept` type is JSON |
| `WithContext(ctx context.Context)` | binds `ctx` in place of the request's context; panics on one derived from `c` itself |
