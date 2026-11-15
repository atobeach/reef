# Error Handling

- [Introduction](#introduction)
- [Configuration](#configuration)
    - [Debug Mode](#debug-mode)
    - [Kernel Options](#kernel-options)
- [Handling Errors](#handling-errors)
    - [Returning Errors](#returning-errors)
    - [Reporting Errors](#reporting-errors)
    - [Rendering Errors](#rendering-errors)
    - [Renderable Errors](#renderable-errors)
    - [Panics](#panics)
- [HTTP Errors](#http-errors)
    - [Aborting the Request](#aborting-the-request)
    - [Not Found Errors](#not-found-errors)
    - [Validation Errors](#validation-errors)
    - [Rate Limit Errors](#rate-limit-errors)
    - [Routing Errors](#routing-errors)
    - [Request Errors](#request-errors)
- [Errors reef Renders](#errors-reef-renders)

## Introduction

In reef, errors are values. A controller action or a middleware that fails returns an error alongside its value, and the HTTP kernel turns it into the JSON response the client receives. There is no exception handler class to register: an expected failure (a missing model, invalid input, a forbidden action) carries its own status and body, and anything else is an unexpected failure that is logged, reported and answered with a `500`.

Every error response is JSON: `{"message": "..."}` with the error's status, unless the error [renders a body of its own](#renderable-errors).

## Configuration

Error handling is configured on the HTTP kernel. `web.NewKernel` builds the kernel from the function that binds your services to every request's context (see [request lifecycle](lifecycle.md); `nil` binds nothing) and a `web.KernelConfig`, and the kernel is the `http.Handler` you serve:

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router:         a.Router,
	Middleware:     []web.Middleware{middleware.Cors(cfg.Cors)},
	TrustedProxies: web.ParseTrustedProxies(cfg.TrustedProxies),
	Logger:         slog.Default(),
	Debug:          cfg.App.Debug,
	Report: func(c *web.Context, err error) {
		a.ErrorTracker.Capture(c, err, map[string]any{"path": c.Path()})
	},
})
if err != nil {
	return err
}

server := &http.Server{
	Addr:              cfg.App.Addr,
	Handler:           kernel,
	ReadHeaderTimeout: 10 * time.Second,
	IdleTimeout:       2 * time.Minute,
}
```

`NewKernel` compiles the routes and returns an error, at boot rather than on the first request, when two routes share a method and path or a route name is used twice.

### Debug Mode

The `Debug` option decides how much an unexpected error tells the client. When it is off, every unexpected error answers `{"message": "Server Error"}`. When it is on, the message is the error's full text, including whatever context it was wrapped with on the way up:

```json
{"message": "loading airport LHR: db: SELECT ...: connection refused"}
```

A typical application reads it from the `APP_DEBUG` environment variable. Errors that carry their own status are unaffected: they always render their own message.

> [!WARNING]
> Never turn on `Debug` in production. Wrapped errors carry SQL, file paths and upstream responses that your users should not see.

### Kernel Options

| Field | Description |
|---|---|
| `Router` | The `*web.Router` whose routes the kernel serves; see [routing](routing.md). |
| `Middleware` | Global middleware, run on every request, outermost first, before routing, so CORS preflights and 404s get the same headers as real routes; see [global middleware](middleware.md#global-middleware). |
| `TrustedProxies` | The peers whose `X-Forwarded-*` headers are believed. `nil` trusts none; see [configuring trusted proxies](requests.md#configuring-trusted-proxies). |
| `Logger` | Where unexpected errors are logged. `nil` uses `slog.Default()`; see [logging](logging.md#loggers-reef-accepts). |
| `Debug` | Whether a `500` shows the error's text; see [debug mode](#debug-mode). |
| `Report` | A function that receives every unexpected error; see [reporting errors](#reporting-errors). |

The kernel also ignores a trailing slash in the request path (`/api/airports/` is `/api/airports`, and an escape before the slash, as in `/bookings/a%2Fb/`, still matches as sent), and answers `HEAD` on every `GET` route. Its `ServeHTTP` method is the `http.Handler` implementation, so you may also mount it under a standard library mux; a test drives it through the [HTTP test client](http-tests.md), `testkit.NewClient(t, kernel)`.

## Handling Errors

### Returning Errors

An action returns an error by returning it. Wrap it with context as it travels up, as you would anywhere in Go; the kernel looks through the wrapping:

```go
func (ctl *AirportController) Show(c *web.Context) (data.AirportData, error) {
	airport, err := models.Airports(c).ResolveRouteBinding(c.Param("code"))
	if err != nil {
		return data.AirportData{}, fmt.Errorf("loading airport %s: %w", c.Param("code"), err)
	}

	// ...
}
```

Here a missing airport is still a `404`, because the error `ResolveRouteBinding` returned is found inside the wrapping, while a dropped database connection is a `500`.

Return every failure as the error, a `404` or a `422` as much as a `500`, never as a `*web.Response` you gave a `4xx` or `5xx` status. Only then do the middleware outside the action, `errors.As` and the error reporter see it as the one failure it is. Whatever value an action returns beside an error is dropped, so return its zero value.

An error is rendered once, by the kernel, after every middleware has returned. On the way out each middleware sees it as the error it is, and one that decorates puts its headers and cookies on the response [`ToResponse`](responses.md#changing-responses-in-middleware) gives it. A middleware may also fail on its own behalf, by returning an error instead of calling `next`:

```go
func EnsureMapboxConfigured(token string) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		if token == "" {
			return nil, web.Abort(http.StatusServiceUnavailable, "Mapbox is not configured.")
		}

		return next(c)
	}
}
```

### Reporting Errors

An unexpected error, one that carries no status of its own, is logged and reported before the client gets its `500`:

1. The kernel's `Logger` ([logging](logging.md)) records it at the error level, with the message `unhandled error` and the request's `method`, `path` and the `error`, plus the `stack` when it is a [panic](#panics).
2. The kernel's `Report` function, if you set one, receives the request and the error, to send it to your error tracker:

```go
Report: func(c *web.Context, err error) {
	a.ErrorTracker.Capture(c, err, map[string]any{"path": c.Path()})
},
```

`Report` receives the error exactly as it was returned, wrapping included. It is called for every unexpected failure: an error that renders as a `500`, a panic in an action or a middleware (a `*panics.Error` in the chain), a [streamed response](responses.md#errors-in-a-stream) whose function failed or panicked, and a [`Terminating`](middleware.md#terminable-middleware) callback that panicked. Errors that carry their own status (404s, 422s, 429s, every `web.Abort`) are expected outcomes: they are neither logged nor reported.

An unexpected error a middleware replaces is still logged and reported, once: when a middleware fails after `next` (a session save after the action's own failure) or answers in place of the error it was handed, the kernel reports the error it was handed, then the middleware's own as it renders. A middleware that passes the error on, bare or wrapped with `%w`, leaves it to be reported once, at the edge; an error `==` can't compare (a slice of errors, a struct holding a map) counts as passed on when a link of the returned error's chain is deeply equal to it.

> [!NOTE]
> A `Report` function that panics can't turn one failure into two: the kernel recovers the panic and still sends the `500`.

### Rendering Errors

The kernel decides how to render an error by looking through its chain, with `errors.As`, for an error that knows its status:

```go
interface {
	error
	HTTPStatus() int
}
```

When it finds one, the response has that status and the body `{"message": ...}` holding that error's own message, not the text of whatever wrapped it. When that error also has an `HTTPBody() any` method, its result is encoded as the body instead; a body with no JSON form is the server's failure, rendered as the `500` and reported as `web: encoding the error body of <the error's type>: …`. When the chain holds a `*web.Error` with `Headers`, they are set on the response.

When the chain holds no such error, the response is a `500`, [logged and reported](#reporting-errors).

Because the contract is an interface, a package can define renderable errors without importing `web` at all. The database layer's `db.ModelNotFoundError` is one: it renders as a `404` even though `db` knows nothing of HTTP.

### Renderable Errors

You may define your own renderable errors by giving a type an `HTTPStatus` method, and an `HTTPBody` method when `{"message": ...}` isn't the shape you need. Here a `CodedError` renders an `{error}` body, with a `code` a client can tell some failures apart by:

```go
// CodedError is an expected failure answered `{"error": Message}` with
// Status, and `"code": Code` beside it when the frontend tells it apart by one.
type CodedError struct {
	Status  int
	Code    string
	Message string
}

type codedBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func (e *CodedError) Error() string {
	return e.Message
}

// HTTPStatus is the response status.
func (e *CodedError) HTTPStatus() int {
	return e.Status
}

// HTTPBody is the coded body.
func (e *CodedError) HTTPBody() any {
	return codedBody{Error: e.Message, Code: e.Code}
}
```

Return it from anywhere below the controller, wrapped or not, and the client receives exactly that body:

```go
return nil, &CodedError{Status: 403, Code: "booking_disabled", Message: "Booking is switched off on this environment."}
```

```json
{"error":"Booking is switched off on this environment.","code":"booking_disabled"}
```

Without a code, the same error is the bare `{error}` body:

```go
return data.FareData{}, &CodedError{Status: http.StatusNotFound, Message: "No fare exists for this flight."}
```

```json
{"error":"No fare exists for this flight."}
```

A body with more fields than that, such as a failed flight search's summary beside its message, is a struct of its own, carried by an error whose `HTTPBody` returns it.

Use a struct for the body when the key order matters to the frontend: `encoding/json` sorts a map's keys.

### Panics

A panic in an action, a middleware, the function of a [streamed response](responses.md#errors-in-a-stream) or a [`Terminating`](middleware.md#terminable-middleware) callback doesn't crash the server. The kernel recovers it as a `*panics.Error`, wrapped in an error whose text reads `panic: <value>`, which is then [logged with its `stack` and reported](#reporting-errors). An action's or a middleware's panic is answered with a `500` like any other unexpected error. So is one while the kernel renders the outcome, after every middleware has returned (an error whose `Error` or `HTTPBody` panics, a value whose `MarshalJSON` does); that `500` still carries the headers and cookies the middleware put beside the error. A stream has sent its status already, so it is cut off instead: the client sees the connection drop rather than the body's end. A `Terminating` callback runs after the response is written, so its panic is only logged and reported, and the remaining callbacks still run.

A panic is a bug, never an expected outcome: a panicked `web.Abort(404, …)` or `db.ModelNotFoundError` is still a reported `500`, because a `*panics.Error` doesn't unwrap to the value it was raised with.

#### Recovered Panics

The `reef/panics` package is how every part of reef that recovers a panic (the kernel, the queue's workers, the scheduler, the console kernel, an AI run, a background cache refresh) keeps it as an error that knows where it happened. Call `panics.New` in the deferred function that recovers, or a helper it calls:

```go
defer func() {
	if recovered := recover(); recovered != nil {
		err = fmt.Errorf("booking chain panicked: %w", panics.New(recovered))
	}
}()
```

- `panics.New(recovered any) *panics.Error` keeps the value with the panicking goroutine's stack, up to 64 frames.
- `Value any` is the value the panic was raised with, the one way to reach it, since `*panics.Error` has no `Unwrap`.
- `Error()` is the value's text (`fmt.Sprint(value)`); the stack is never part of it, so an error tracker groups every occurrence as one issue.
- `Frames() []runtime.Frame` is the stack from the panic outward, the panicking frame (or the runtime's own, for a runtime error) first; the recovery, and a deferred function that panicked again, are left out. A stack can't tell a re-raised panic from a new one, so a panic raised afresh by a deferred function while another unwinds reads as the first: run cleanup that may panic once no panic is in flight, as the kernel runs its `Terminating` callbacks.
- `Stack() string` is the frames as text, `function\n\tfile:line\n` per frame, for a log line's `stack` attribute.

Find one in a chain with `errors.AsType[*panics.Error](err)`.

## HTTP Errors

### Aborting the Request

The `web.Abort` function returns an error that ends the request with the given status and message. Return it from an action or a middleware:

```go
return nil, web.Abort(http.StatusForbidden, "This action is unauthorized.")
```

```json
{"message":"This action is unauthorized."}
```

`Abort` returns a `*web.Error`, whose `Error` method returns its message and whose `HTTPStatus` method returns its code. When the response needs headers too, build the `web.Error` yourself:

```go
return nil, &web.Error{
	Code:    http.StatusServiceUnavailable,
	Message: "Flight search is down for maintenance.",
	Headers: map[string]string{"Retry-After": "600"},
}
```

| Field | Description |
|---|---|
| `Code` | The response status. |
| `Message` | The `message` in the body. |
| `Headers` | Headers set on the response, replacing any value they had. |

### Not Found Errors

The `web.NotFound` function is a `404` with an empty message, `{"message": ""}`. Use it when the resource's name should not appear in the answer, as when a user asks for a row someone else owns:

```go
if id := auth.ID(c); id == nil || booking.UserID != *id {
	return nil, web.NotFound()
}
```

A model query's `FirstOrFail`, `FindOrFail` and `ResolveRouteBinding` fail with a `*db.ModelNotFoundError` instead, a `404` whose message names the model in words: `{"message": "Booking not found."}`. To answer the same way yourself, return one naming the model:

```go
return nil, &db.ModelNotFoundError{Model: "Booking"}
```

### Validation Errors

When input fails [validation](validation.md#validation-error-response-format), `web.Validate` returns a `*web.ValidationError`, which renders as a `422` holding a summary message and every failing attribute's messages, in the order the attributes were validated:

```json
{
    "message": "The contact email field is required. (and 1 more error)",
    "errors": {
        "contact_email": ["The contact email field is required."],
        "adults": ["The adults field must be at least 1."]
    }
}
```

The summary is the first message, followed by `(and 1 more error)` or `(and N more errors)` when there are others. The `Errors` field holds the `*validation.Errors` bag, and `Error()` returns the summary. `ValidationError` is a [renderable error](#renderable-errors) like any other: its `HTTPStatus` method returns `422` and its `HTTPBody` method returns the body above.

For a check no rule expresses (a wrong password, an expired code), `web.ValidationFailed` builds the same `422` for one attribute and the messages you give:

```go
_, ok, err := ctl.Auth.Validate(c, auth.Credentials{Email: user.Email, Password: password})
if err != nil {
	return nil, err
}

if !ok {
	return nil, web.ValidationFailed("password", "The provided password was incorrect.")
}
```

A rule that couldn't run at all (a `unique` rule whose database is down) is not a validation failure: `web.Validate` returns that error as is, and it renders as a `500`.

### Rate Limit Errors

The `web.TooManyRequests` function is the `429` the [rate limiter](rate-limiting.md#throttled-responses) answers with once a client has used up its attempts. Its message is `Too Many Attempts.`, and it carries the headers a client needs to back off:

| Header | Value |
|---|---|
| `Retry-After` | the seconds until the next attempt is allowed: `retryAfter` rounded, at least `1` |
| `X-RateLimit-Reset` | the Unix time at which it is allowed, read from the context's clock |
| `X-RateLimit-Limit` | the `limit` you pass |
| `X-RateLimit-Remaining` | `0` |

```go
return nil, web.TooManyRequests(c, limit.MaxAttempts, resetIn)
```

### Routing Errors

When no route matches the request, routing returns a `404` or a `405` `*web.Error`, which the global middleware see as the error it is and the kernel renders like any other HTTP error, never reported:

| Status | Message |
|---|---|
| `404` | `The route api/nowhere could not be found.` |
| `405` | `The POST method is not supported for route api/airports. Supported methods: GET, HEAD.` |

The `405` carries an `Allow` header naming the same methods (`Allow: GET, HEAD`). The bare prefix of a [wildcard route](routing.md#wildcard-parameters) (`/assets`, or `/assets/`, for `/assets/{path...}`) is a `404` too, never a redirect. A path that isn't clean (`//` or `/../` in it) is redirected to its cleaned form.

### Request Errors

Reading [input](requests.md#how-input-is-parsed) fails when the request can't be parsed. `c.All()` (and so `web.Validate`) returns the error, as `c.Body()` does for an oversized or unreadable body, and you return it as is:

| Status | Message | When |
|---|---|---|
| `400` | `Malformed query string.` | the query string can't be decoded |
| `413` | (empty) | the body is over 8 MiB |
| `500` | `Server Error` | a form-urlencoded body with a malformed percent-escape, or a body that can't be read (the error is returned as is, so it is [logged and reported](#reporting-errors)) |

reef has no HTML error pages: every error response, a `404` or a `500` alike, is JSON.

## Errors reef Renders

The errors reef itself returns, and where each is documented:

| Status | Message | Returned by |
|---|---|---|
| `400` | `Malformed query string.` | [`c.All`](requests.md#how-input-is-parsed) |
| `401` | `Unauthenticated.` | the `Authenticate` middleware ([authentication](authentication.md)) |
| `403` | (empty) | a refused channel subscription ([broadcasting](broadcasting.md)) |
| `404` | `The route ... could not be found.`, `... not found.`, or empty | [routing](#routing-errors), `FirstOrFail`, `FindOrFail` and `ResolveRouteBinding`, `web.NotFound` |
| `405` | `The ... method is not supported for route ...` | [routing](#routing-errors) |
| `413` | (empty) | [`c.Body`](requests.md#the-request-body), and so `c.All` |
| `419` | `CSRF token mismatch.` | the CSRF middleware ([CSRF protection](csrf.md#token-mismatches)) |
| `422` | the validation summary | [`web.Validate`](validation.md), `web.ValidationFailed` |
| `423` | `Password confirmation required.` | `auth.RequirePassword` ([authentication](authentication.md)) |
| `429` | `Too Many Attempts.` | the throttle middleware ([rate limiting](rate-limiting.md)) |
| `500` | `Server Error` (the error's text in debug mode) | any unexpected error or panic |
