# HTTP Tests

- [Introduction](#introduction)
- [Making Requests](#making-requests)
    - [Customizing Request Headers](#customizing-request-headers)
    - [Client Address and Context](#client-address-and-context)
    - [Cookies](#cookies)
    - [Session and Authentication](#session-and-authentication)
    - [Debugging Responses](#debugging-responses)
    - [Exception Handling](#exception-handling)
- [Testing JSON APIs](#testing-json-apis)
    - [Asserting Exact JSON Matches](#asserting-exact-json-matches)
    - [Asserting on JSON Paths](#asserting-on-json-paths)
    - [Reading the Response](#reading-the-response)
- [Testing Validation](#testing-validation)
- [Building Your Own Client](#building-your-own-client)
- [Available Assertions](#available-assertions)
    - [Response Assertions](#response-assertions)
    - [Validation Assertions](#validation-assertions)

## Introduction

reef provides a fluent API for sending HTTP requests to your application and examining the responses. Requests never touch the network: `testkit.Client` builds a `net/http` request, hands it to your HTTP kernel's `ServeHTTP`, and records the response, so a test exercises the real routes, middleware, validation and error rendering in-process. Take a look at the feature test below, which uses your application's own [test case](testing.md#building-a-test-case), `tests.Setup`:

```go
func TestTheHealthCheckResponds(t *testing.T) {
	tc := tests.Setup(t)

	tc.Get("/up").AssertOk().AssertBody("OK")
}
```

`Get` returns a `*testkit.TestResponse`, and each of its assertions returns the response again, so they chain. An assertion that fails stops the test (`t.Fatalf`) and says what it found, the response body included.

## Making Requests

Create a client for any `net/http` handler, usually your HTTP kernel, with `testkit.NewClient`. Your [test case](testing.md#building-a-test-case) normally builds one for you; the one in [Building a Test Case](testing.md#building-a-test-case) embeds it, so `tc.Get(...)` is the client's `Get`:

```go
client := testkit.NewClient(t, kernel)

client.Get("/api/airports").AssertOk()
```

The client has a method per kind of request:

| Method | Sends |
|---|---|
| `Get(path)` | a GET without a body |
| `Post(path)` | a POST without a body |
| `PostJSON(path, value)` | a POST whose body is `value` encoded as JSON, with `Content-Type: application/json` and `Accept: application/json` |
| `Send(method, path, body, contentType)` | any method with a raw body; `Content-Type` is set to `contentType` whenever `body` is not nil |
| `Call(method, path)` | any method without a body, for a method with no shorthand (an `OPTIONS` preflight) |

For a JSON `PUT`, `PATCH` or `DELETE`, encode the body yourself and use `Send`. `Send` is also how you post a body `PostJSON` can't produce, such as malformed JSON:

```go
body, _ := json.Marshal(map[string]any{"quantity": 2})
client.Send(http.MethodPatch, "/api/orders/7", body, "application/json").AssertOk()

// A malformed JSON body reads as empty input, so validation reports the missing fields.
client.Send(http.MethodPost, "/api/orders", []byte(`{"items": [`), "application/json").
	AssertJSONValidationErrors("items")
```

`Send` sets no `Accept` header; add one with [`WithHeader`](#customizing-request-headers) when the route looks at it.

Every request carries the test's context (`t.Context()`) and comes from the client address `203.0.113.10:41000`, a documentation address, so code that reads the visitor's IP sees a stable public one; [`WithContext` and `FromIP`](#client-address-and-context) change either. A path may carry a query string (`/api/places/search?q=par`), and it reaches the kernel exactly as written: an escape such as `%2F` or `%2e%2e`, or a dot segment, is not cleaned up by the client.

> [!NOTE]
> The client is a plain API caller: it sends exactly the headers and cookies you give it and never stores a cookie a response sets. There is no cookie jar and no CSRF token. If you need a browser, [build one](#building-your-own-client) on top of `testkit.Client`.

### Customizing Request Headers

You may use `WithHeader` to set a header on every following request the client sends. It returns the client, so it chains into the request:

```go
tc.WithHeader("Accept", "application/json").Get("/user").AssertUnauthorized()
```

The client's own headers are set after the ones a method sets for you, so they win: `WithHeader("Accept", "text/event-stream")` followed by `PostJSON` sends `Accept: text/event-stream`.

> [!WARNING]
> `WithHeader` and `WithCookie` change the client itself, not a copy: the header stays on every later request in the test. When a test needs a client without it, create another one with `testkit.NewClient` (or a test case helper such as `tc.NewClient()`, a second client on the same app, database and stores). The same goes for `FromIP` and `WithContext` below. A test that sends from several goroutines at once gives each its own client, built before they start.

### Client Address and Context

You may use `FromIP` to send the following requests from another address, the TCP peer the kernel sees. It is how a test proves a rate limit keyed by IP. A [browser](#building-your-own-client) of your own may have a `FromIP` too, which it hands to the client it sends through:

```go
tc.Browser().FromIP("198.51.100.7").PostJSON("/login", credentials).AssertOk()
```

A trusted proxy's forwarded headers are still read on top of it, so a test of the proxy rules sends them with `WithHeader("X-Forwarded-For", ...)`.

You may use `WithContext` to tie the following requests to a context of your own. When it ends, the client has gone away, as a browser that navigates off has: the handler's request context is cancelled. A test can close a tab mid-stream this way:

```go
ctx, cancel := context.WithCancel(context.Background())
go func() { done <- tc.NewClient().WithContext(ctx).WithHeader("Accept", "text/event-stream").PostJSON("/api/chat", payload) }()

cancel()
```

### Cookies

You may use `WithCookie` to send a cookie, as `name=value` in the `Cookie` header, on every following request:

```go
tc.WithCookie("locale", "en-GB").Get("/api/airports").AssertOk()
```

The value is sent exactly as given. That is what you want for a cookie your app never encrypts, such as a plain locale preference. For a cookie the [`middleware.Cookies`](middleware.md#encrypting-cookies) middleware decrypts, seal the value the way the middleware expects first, with `cookie.Encrypt` and URL-escaping (an invalid encrypted cookie reads as absent):

```go
sealed, err := cookie.Encrypt(tc.App.Encrypter, "theme", "dark")
if err != nil {
	t.Fatal(err)
}

tc.WithCookie("theme", url.QueryEscape(sealed)).Get("/api/airports").AssertOk()
```

Only `name=value` travels, the way a browser's `Cookie` header carries it: none of the `Set-Cookie` attributes. To send back a cookie a response set, pass its name and value: `WithCookie(c.Name, c.Value)`.

### Session and Authentication

reef's client has no `actingAs`: a session lives in your app's session store, and signing in is a value in it. A test case signs a client in by writing the session itself and sending its cookie. The test case in these examples does exactly that with a `Browser` (see [Building Your Own Client](#building-your-own-client)): `tc.ActingAs(user)` writes a session row holding the user's id under the guard's key and returns a browser carrying its cookie.

```go
func TestTheSignedInUserCanReadTheirProfile(t *testing.T) {
	tc := tests.Setup(t)
	user := factories.User(tc).Create()

	tc.ActingAs(user).GetJSON("/user").
		AssertOk().
		AssertJSONPath("id", user.ID)
}
```

See [Authentication](authentication.md) for the guard and its session key, and [Session](session.md) for the session store.

### Debugging Responses

A `TestResponse` hands back what the kernel answered through its accessors (see [Reading the Response](#reading-the-response)). You may log them while you investigate:

```go
res := tc.Get("/api/airports")

t.Log(res.Status(), res.HeaderNames(), res.Body())
```

You often won't need to: a failing assertion reports the body it got, cut to its first 500 bytes.

> [!NOTE]
> A test never builds an `httptest.ResponseRecorder` or reads one: send the request through `testkit.Client` (or a client built on it, such as a `tests.Browser` of your own) and read the answer through `TestResponse`. A `forbidigo` lint rule that forbids `httptest.NewRecorder` everywhere but `testkit`'s client holds every test to it.

### Exception Handling

There is no `withoutExceptionHandling`: every error a handler returns renders through the kernel exactly as in production. An error with an HTTP status (an abort, a missing model, a validation failure) renders its own JSON body. Anything unexpected is a 500 whose body is `{"message": "Server Error"}`, unless the kernel was built with `Debug: true`, in which case the message is the error's text. Either way, the unexpected error itself is logged through the kernel's `Logger` and handed to its `Report` function. See [Error Handling](errors.md).

The client serves a request as `net/http`'s server does. A handler that panics with `http.ErrAbortHandler` (the kernel does, for a [stream that panicked](responses.md#errors-in-a-stream) after its status went out) leaves the response cut off, holding what was written before it: assert it with [AssertAborted](#assertaborted). A stream that panics is a bug, so a response cut off without `AssertAborted` fails the test when it ends, whatever else the test checked. A handler that panics with anything else, which the kernel never lets through, fails the test with `the handler panicked:`, the value and its stack, instead of crashing the test binary.

## Testing JSON APIs

Most of a reef app's routes return JSON, and most of your assertions will be on it. `PostJSON` sends a JSON body and asks for JSON back; the [browser](#building-your-own-client) below adds `GetJSON`, `PutJSON` and `DeleteJSON` in the same style:

```go
func TestMalformedCurrencyCodesAreRejected(t *testing.T) {
	tc := tests.Setup(t)
	user := factories.User(tc).Create()

	tc.ActingAs(user).PutJSON("/user/currency", map[string]any{"currency": "POUNDS"}).
		AssertJSONValidationErrors("currency").
		AssertJSONPath("message", "The currency field format is invalid.")
}
```

The body is decoded once, the first time an assertion needs it, and reused by every assertion after. An assertion that needs JSON fails the test if the body isn't JSON.

### Asserting Exact JSON Matches

`AssertJSON` asserts that the whole body equals the value you give it. Both sides are compared as decoded JSON, so key order and number formatting don't matter, and you may pass maps, slices or structs:

In these booking tests, `e` is a test case with a bookable flight already in place:

```go
tc.Get("/api/flights?from=LHR&to=JFK&date=2026-12-01").AssertOk().AssertJSON(map[string]any{"flights": []any{}})

e.PostJSON("/api/bookings", payload).
	AssertForbidden().
	AssertJSON(map[string]any{"error": "Booking is switched off on this environment.", "code": "booking_disabled"})
```

> [!WARNING]
> `AssertJSON` is an exact match, like Laravel's `assertExactJson`, not a subset: a key in the body that your expectation lacks fails it. To check part of a body, use [`AssertJSONPath`](#asserting-on-json-paths).

### Asserting on JSON Paths

`AssertJSONPath` checks one value by its dotted path: object keys by name, array elements by index. The test fails if the path is missing or holds anything else:

```go
e.PostJSON("/api/bookings", bookingPayload()).
	AssertOk().
	AssertJSONPath("redirectUrl", "https://pay.example.com/redirect/abc").
	AssertJSONPath("booking.reference", "QX7P2M").
	AssertJSONPath("booking.passengers.0.seat", "14C")
```

The expected value is compared the way `AssertJSON` compares, so `AssertJSONPath("0.avg_weekly_flights", 95)` matches the `95` in the body although Go decoded it as a `float64`, and a struct matches the object it encodes to.

`AssertJSONCount` checks the length of the array at a path; pass `""` for a body that is itself an array:

```go
tc.ActingAs(user).GetJSON("/api/bookings").AssertOk().AssertJSONCount(1, "")
tc.Get("/api/airports/search?q=par").AssertOk().AssertJSONCount(2, "airports")
```

### Reading the Response

When an assertion doesn't fit, read the response and check it yourself:

- `Status()` is the status code.
- `Body()` is the raw body as a string.
- `Header(name)` is a header's first value, or `""` when it is absent; the name is matched in any case.
- `HeaderNames()` are the response's header names as written, sorted, for a check that looks at every header.
- `JSON()` is the decoded body as an `any` (a `map[string]any`, a `[]any`, ...).
- `Value(path)` is the value at a dotted path, or `nil` when the path is missing.
- `Pluck(path, key)` is `key` of every object in the array at `path`, in order (a missing key is `nil`). It fails the test if `path` is not an array.
- `Cookie(name)` is the response's first `Set-Cookie` of that name as a `*http.Cookie`, or `nil`.
- `Cookies()` is every `Set-Cookie` on the response, in order, each a `*http.Cookie` with its attributes (`Path`, `Domain`, `Expires`, `MaxAge`, which is negative for a forget, `HttpOnly`, `Secure`, `SameSite`).

```go
image := tc.Get("/api/airports/FCO/map?w=640&h=640").AssertOk()
for _, name := range image.HeaderNames() {
	if strings.Contains(strings.ToLower(name), "access_token") {
		t.Fatalf("token leaked in header %s", name)
	}
}

secret, _ := tc.ActingAs(user).GetJSON("/user/two-factor-secret-key").AssertOk().Value("secretKey").(string)
if secret == "" {
	t.Fatal("no secret key")
}

airports := tc.Get("/api/airports/search?q=par")
if got := airports.Pluck("airports", "code"); !slices.Equal(got, []any{"CDG", "ORY"}) {
	t.Fatalf("airports = %v", got)
}
```

Decoded JSON numbers are `float64`, so `res.Value("id") == user.ID` is never true for an `int64` id. Compare through JSON instead with `testkit.EqualJSON`, which reports whether two values are equal once each is round-tripped through JSON, the comparison `AssertJSON` uses: an `int`, a `float64` and a `json.Number` of one value are equal, and a struct equals its map.

```go
if !testkit.EqualJSON(t, res.Value("user.id"), user.ID) {
	t.Fatalf("user.id = %v, want %d", res.Value("user.id"), user.ID)
}
```

## Testing Validation

A request that fails [validation](validation.md) answers 422 with `{"message": ..., "errors": {...}}`, each key of `errors` a field and its value the list of messages. Three assertions read it.

`AssertJSONValidationErrors` asserts a 422 with an error for every key you name:

```go
e.PostJSON("/api/booking", payload).
	AssertJSONValidationErrors("passengers.0.expiry_date", "passengers.0.birth_date")
```

`AssertJSONValidationError` asserts a 422 whose errors for one key include an exact message:

```go
tc.ActingAs(user).PostJSON("/api/profiles", profile).
	AssertJSONValidationError("nationality", "The selected nationality is invalid.")
```

`AssertJSONMissingValidationErrors` asserts that none of the named keys has an error, or, called without keys, that the body has no errors at all:

```go
e.PostJSON("/api/booking", payload).
	AssertJSONValidationErrors("passengers.0.expiry_date").
	AssertJSONMissingValidationErrors("passengers.0.last_name")
```

Keys are taken literally, dots and all, the way the validator spells a nested field: `passengers.0.last_name` is one key of `errors`, never a path into it.

> [!NOTE]
> `AssertJSONMissingValidationErrors` does not check the status, but it does decode the body: a response without a JSON body (a 204, an HTML page) fails it. Pair it with a status assertion when that is what you mean.

## Building Your Own Client

`testkit.Client` covers API callers. When a test needs something it doesn't do (keep cookies between requests, echo a CSRF token), build a client of your own that sends each request through a fresh `testkit.Client` and reads the cookies back from `res.Cookies()`, so every response assertion on this page works on what it returns:

```go
// Browser keeps the cookies each response sets and sends them back.
type Browser struct {
	t       testing.TB
	handler http.Handler
	jar     map[string]string
}

// Get sends a GET with the jar's cookies.
func (b *Browser) Get(path string) *testkit.TestResponse {
	b.t.Helper()

	client := testkit.NewClient(b.t, b.handler).WithHeader("User-Agent", "my-test-browser")
	for name, value := range b.jar {
		client.WithCookie(name, value)
	}

	res := client.Get(path)
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(b.jar, c.Name)

			continue
		}

		b.jar[c.Name] = c.Value
	}

	return res
}
```

A fresh client per request carries exactly the jar's cookies of that moment, never a cookie the jar has since dropped.

A complete `tests/browser.go` might grow from there: a jar that drops forgotten cookies (an `Expires` already past included), its own `User-Agent` and `FromIP`, JSON requests with `Accept: application/json` and `X-Requested-With: XMLHttpRequest`, the XSRF token fetched from `/sanctum/csrf-cookie` and echoed back as `X-XSRF-TOKEN` exactly as the SPA does (`WithoutCSRFToken` stops it, to prove the 419), a `WithCookie` that seals the value as the cookies middleware would, `WithSession` and `ActingAs` to write its session row, and session assertions of its own (`AssertSessionHas`, `AssertSessionMissing`, `AssertAuthenticatedAs`, `AssertGuest`).

## Available Assertions

### Response Assertions

`testkit.TestResponse` has the following assertions. Each fails the test at once (`t.Fatalf`) and returns the response, so they chain.

- [AssertAborted](#assertaborted)
- [AssertBody](#assertbody)
- [AssertConflict](#assertconflict)
- [AssertCookie](#assertcookie)
- [AssertCookieExpired](#assertcookieexpired)
- [AssertCookieMissing](#assertcookiemissing)
- [AssertCreated](#assertcreated)
- [AssertForbidden](#assertforbidden)
- [AssertGone](#assertgone)
- [AssertHeader](#assertheader)
- [AssertHeaderMissing](#assertheadermissing)
- [AssertInternalServerError](#assertinternalservererror)
- [AssertJSON](#assertjson)
- [AssertJSONCount](#assertjsoncount)
- [AssertJSONPath](#assertjsonpath)
- [AssertMethodNotAllowed](#assertmethodnotallowed)
- [AssertNoContent](#assertnocontent)
- [AssertNotFound](#assertnotfound)
- [AssertOk](#assertok)
- [AssertRedirect](#assertredirect)
- [AssertServiceUnavailable](#assertserviceunavailable)
- [AssertStatus](#assertstatus)
- [AssertTooManyRequests](#asserttoomanyrequests)
- [AssertUnauthorized](#assertunauthorized)
- [AssertUnprocessable](#assertunprocessable)

#### AssertAborted

Assert that the handler cut the response off after it started, as a stream that panicked is: a client sees the connection drop instead of the body's end. It also tells the client the abort is expected; without it, a cut-off response fails the test when it ends. The body is what was written before it:

```go
res.AssertOk().AssertAborted().AssertBody("data: first\n\n")
```

#### AssertBody

Assert that the raw body is exactly the given string:

```go
res.AssertBody(`{"message":"Unauthenticated."}`)
```

#### AssertConflict

Assert that the response has a conflict (409) HTTP status code:

```go
res.AssertConflict()
```

#### AssertCookie

Assert that the response sets the given cookie. A `Set-Cookie` that forgets the cookie (`Max-Age` below zero, which is how `Max-Age=0` arrives, or, without `Max-Age`, an `Expires` that is not after now) sets nothing and fails the assertion:

```go
res.AssertCookie("visitor_id")
```

`Max-Age` outranks `Expires`, as browsers treat them. An `Expires` is compared with the wall clock, since the test's own context carries no reef clock.

#### AssertCookieExpired

Assert that the response forgets the given cookie: a `Set-Cookie` of that name whose `Max-Age` is below zero, or, without `Max-Age`, whose `Expires` is not after now:

```go
res.AssertCookieExpired("visitor_id")
```

#### AssertCookieMissing

Assert that the response sends no `Set-Cookie` of the given name at all. A forget counts as sending one, so this fails on a cookie being cleared:

```go
res.AssertCookieMissing(tc.App.Auth.RecallerCookie())
```

#### AssertCreated

Assert that the response has a 201 HTTP status code:

```go
res.AssertCreated()
```

#### AssertForbidden

Assert that the response has a forbidden (403) HTTP status code:

```go
res.AssertForbidden()
```

#### AssertGone

Assert that the response has a gone (410) HTTP status code:

```go
res.AssertGone()
```

#### AssertHeader

Assert that the given header has exactly the given value. Only the header's first value is compared, and a missing header reads as `""`:

```go
res.AssertHeader("X-RateLimit-Limit", "5")
```

#### AssertHeaderMissing

Assert that the response does not carry the given header:

```go
res.AssertHeaderMissing("Strict-Transport-Security")
```

#### AssertInternalServerError

Assert that the response has an "Internal Server Error" (500) HTTP status code:

```go
res.AssertInternalServerError()
```

#### AssertJSON

Assert that the body equals the given value once both are decoded as JSON. See [Asserting Exact JSON Matches](#asserting-exact-json-matches):

```go
res.AssertJSON(map[string]any{"posts": []any{}})
```

#### AssertJSONCount

Assert that the array at the given path (`""` for the root) has the expected number of items:

```go
res.AssertJSONCount(8, "")
```

#### AssertJSONPath

Assert that the value at the given dotted path equals the expected value. See [Asserting on JSON Paths](#asserting-on-json-paths):

```go
res.AssertJSONPath("booking.flight.data.pnr", "ABC123")
```

#### AssertMethodNotAllowed

Assert that the response has a method not allowed (405) HTTP status code:

```go
res.AssertMethodNotAllowed()
```

#### AssertNoContent

Assert that the response has a 204 HTTP status code and an empty body:

```go
res.AssertNoContent()
```

#### AssertNotFound

Assert that the response has a not found (404) HTTP status code:

```go
res.AssertNotFound()
```

#### AssertOk

Assert that the response has a 200 HTTP status code:

```go
res.AssertOk()
```

#### AssertRedirect

Assert that the response is a redirect (a 3xx status) whose `Location` is exactly the given URI. Pass `""` to accept a redirect anywhere:

```go
res.AssertRedirect("http://example.com")

res.AssertRedirect("")
```

#### AssertServiceUnavailable

Assert that the response has a "Service Unavailable" (503) HTTP status code:

```go
res.AssertServiceUnavailable()
```

#### AssertStatus

Assert that the response has the given HTTP status code. On failure, the message shows the status it got and the start of the body:

```go
res.AssertStatus(http.StatusPaymentRequired)
```

#### AssertTooManyRequests

Assert that the response has a too many requests (429) HTTP status code:

```go
res.AssertTooManyRequests()
```

#### AssertUnauthorized

Assert that the response has an unauthorized (401) HTTP status code:

```go
res.AssertUnauthorized()
```

#### AssertUnprocessable

Assert that the response has an unprocessable entity (422) HTTP status code:

```go
res.AssertUnprocessable()
```

### Validation Assertions

#### AssertJSONMissingValidationErrors

Assert that the body's `errors` holds none of the given keys, or with no keys, no error at all. The status is not checked:

```go
res.AssertJSONMissingValidationErrors("passengers.0.last_name")

res.AssertJSONMissingValidationErrors()
```

#### AssertJSONValidationError

Assert that the response is a 422 whose errors for the given key include the given message:

```go
res.AssertJSONValidationError("nationality", "The selected nationality is invalid.")
```

#### AssertJSONValidationErrors

Assert that the response is a 422 with an error for every given key:

```go
res.AssertJSONValidationErrors("currency")
```
