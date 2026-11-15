# HTTP Responses

- [Introduction](#introduction)
- [Creating Responses](#creating-responses)
    - [Strings, Structs and Maps](#strings-structs-and-maps)
    - [Response Objects](#response-objects)
    - [Attaching Headers to Responses](#attaching-headers-to-responses)
    - [Attaching Cookies to Responses](#attaching-cookies-to-responses)
    - [Queueing Cookies](#queueing-cookies)
    - [Cookies and Encryption](#cookies-and-encryption)
    - [Changing Responses in Middleware](#changing-responses-in-middleware)
- [Redirects](#redirects)
    - [Redirecting to Named Routes](#redirecting-to-named-routes)
    - [Redirecting to External Domains](#redirecting-to-external-domains)
    - [Redirecting With Flashed Session Data](#redirecting-with-flashed-session-data)
- [Other Response Types](#other-response-types)
    - [JSON Responses](#json-responses)
    - [HTML Responses](#html-responses)
    - [Raw Bytes and Files](#raw-bytes-and-files)
    - [Empty Responses](#empty-responses)
- [Streamed Responses](#streamed-responses)
    - [Event Streams (SSE)](#event-streams-sse)
    - [Stopping When the Client Leaves](#stopping-when-the-client-leaves)
    - [Errors in a Stream](#errors-in-a-stream)
- [Available Methods](#available-methods)

## Introduction

Every controller action and middleware in reef returns two things: a value and an error. The kernel turns the value into the HTTP response the client receives, and the error into the matching [error response](errors.md). Most actions just return a struct and let reef encode it as JSON; when you need control over the status, headers or cookies, you return a `*web.Response` instead.

Responses are fully buffered: nothing is written to the client until the whole pipeline has returned, so a middleware can still change the headers and cookies of whatever `next` returned, through [`web.ToResponse`](#changing-responses-in-middleware).

## Creating Responses

### Strings, Structs and Maps

The most basic response is a value returned from an action. An action declares the type it answers, and the kernel converts the value according to that type:

| Returned value | Response |
|---|---|
| a `*web.Response` | sent as is |
| `nil`, or a nil `*web.Response` | a reported `500` `{"message":"Server Error"}`: an action answers a value or an error |
| a `string` | `200` `application/json`, the string encoded as a JSON string |
| anything else | `200` `application/json`, the value encoded with `encoding/json` |

So a struct, a map, a slice or a model becomes a JSON response:

```go
// Invoke lists the FAQ items.
func (FaqController) Invoke(_ *web.Context) ([]faq.Item, error) {
	return faq.Items(), nil
}
```

The value is encoded with `encoding/json`, so its `json` tags shape the body. `encoding/json` sorts a map's keys; return a struct (or an `ordered.Object`, see [helpers](helpers.md)) when the frontend depends on key order.

> [!NOTE]
> If the value can't be encoded (a channel, a `NaN`), the client gets a `500` instead of a truncated body, and the failure is [reported](errors.md#reporting-errors) like any other unexpected error.

### Response Objects

When you need to set the status code, headers or cookies, send a non-JSON body, or answer more than one shape, declare the action `(*web.Response, error)` and return a `*web.Response`. You build one with a constructor (`web.JSON`, `web.HTML`, `web.Bytes`, `web.NoContent`, `web.Redirect` or `web.Stream`) and adjust it with chainable methods:

```go
func (BookingController) Store(c *web.Context) (*web.Response, error) {
	// ...

	return web.JSON(map[string]string{"reference": booking.Reference}).WithStatus(http.StatusCreated), nil
}
```

The `WithStatus` method sets the status code and returns the same response, so calls chain. It is for a success's status (a `201`, a redirect's `301`): a failure is returned as the error, never as a response carrying a `4xx` or `5xx`, so the middleware around the action and the error reporter see it as a failure (see [error handling](errors.md)).

An action's value counts only when its error is `nil`. A value returned beside an error, a `*web.Response` included, is dropped, headers and cookies with it, so return the zero value with the error. Only a middleware can put headers and cookies beside an error, through [`web.ToResponse`](#changing-responses-in-middleware).

### Attaching Headers to Responses

The `WithHeader` method sets a header, replacing any value it already had, and returns the response:

```go
return web.Bytes(image, "image/png").
	WithHeader("Cache-Control", "max-age=604800, public"), nil
```

### Attaching Cookies to Responses

The `WithCookie` method attaches a standard library `*http.Cookie` to the response. You set its attributes (expiry, path, domain, `Secure`, `HttpOnly`, `SameSite`) on the cookie itself:

```go
return web.NoContent().WithCookie(&http.Cookie{
	Name:     "theme",
	Value:    "dark",
	Path:     "/",
	MaxAge:   int((365 * 24 * time.Hour).Seconds()),
	Secure:   c.IsSecure(),
	HttpOnly: true,
	SameSite: http.SameSiteLaxMode,
}), nil
```

#### Expiring Cookies Early

To remove a cookie from the browser, send it again with a negative `MaxAge` (sent as `Max-Age=0`), using the same path and domain it was issued with:

```go
return web.NoContent().WithCookie(&http.Cookie{
	Name:   "theme",
	Path:   "/",
	MaxAge: -1,
}), nil
```

### Queueing Cookies

Code that doesn't build the response, such as a helper that remembers a traveller's preferred currency deep inside an action, may queue a cookie on the request instead with `QueueCookie`. The cookie is attached to whatever response the request ends up with, error responses included. `QueuedCookies` returns what has been queued so far:

```go
func Remember(r Request, code string) {
	// ...

	r.QueueCookie(&http.Cookie{Name: "currency", Value: code, Path: "/", HttpOnly: true})
}
```

Here `currency.Remember` takes a small interface naming the methods it uses (`QueueCookie`), which a `*web.Context` satisfies, so the currency package stays below the HTTP layer; callers still write `currency.Remember(c, "EUR")`.

> [!WARNING]
> Queued cookies are attached by the `middleware.Cookies` middleware on its way out. A route that doesn't run that middleware never sends them.

### Cookies and Encryption

The `middleware.Cookies` middleware encrypts every cookie your application sends and decrypts every cookie it receives, so cookies can't be read or forged by the client. You add it to the routes that use cookies, outside the session and CSRF middleware (see [encrypting cookies](middleware.md#encrypting-cookies)), listing the cookies that should travel unencrypted after the encrypter:

```go
cookies := middleware.Cookies(encrypter, "locale")
```

On the way out, the middleware first attaches any [queued cookies](#queueing-cookies), then seals every cookie except the excepted names and those being expired (a negative `MaxAge`), and URL-encodes every value. A cookie that can't be sealed fails the request with that error, a reported `500` that drops the response and its cookies, so none ever goes out unsealed. On the way in, every value is URL-decoded first, and the excepted names are then read as they are; a cookie that isn't valid URL encoding, or that fails to open, reads as absent through `c.Cookie`.

#### The Encrypted Cookie Format

The `cookie` package is the sealed format itself, should you need to seal or open a cookie outside the middleware (a test that plants a session cookie, for example). The plaintext is prefixed with an HMAC of the cookie's name, so a value sealed for one cookie can't be replayed as another (a session id passed off as an XSRF token):

```go
sealed, err := cookie.Encrypt(encrypter, "flights_session", id)

value, ok := cookie.Decrypt(encrypter, "XSRF-TOKEN", raw)
```

- `cookie.Encrypt(encrypter, name, value)` seals `value` for the cookie `name` with the encrypter's current key.
- `cookie.Decrypt(encrypter, name, value)` opens an incoming value. `ok` is `false` for anything that isn't a valid payload for this cookie name under one of the encrypter's keys (previous keys included); treat such a cookie as absent.
- `cookie.Prefix(name, key)` is the prefix sealed in front of the value: `hex(HMAC-SHA1(name + "v2", key)) + "|"`.

The format is fixed because browsers already hold cookies sealed this way; see [compatibility](compatibility.md). Keys and the encrypter itself are covered in [encryption](encryption.md).

### Changing Responses in Middleware

`next` returns a value and an error, as a controller does. To decorate whichever comes back, pass both to `web.ToResponse`, change the `*web.Response` it gives you, and return it with the error:

```go
// SecurityHeaders adds the hardening headers a handler hasn't set itself.
func SecurityHeaders(c *web.Context, next web.Handler) (any, error) {
	res, err := web.ToResponse(next(c))

	if res.Header("X-Frame-Options") == "" {
		res.WithHeader("X-Frame-Options", "DENY")
	}

	return res, err
}
```

On a value, `ToResponse` gives the response the kernel would send: a `*web.Response` as it is, anything else, a string included, encoded as JSON. A value that fails to encode comes back as a `500` and the encoding error, and an outcome of nothing (`nil, nil`, or a nil `*web.Response`) as a `500` and an error saying so, both of which the kernel renders and reports. Beside an error, it never renders the error: the kernel does that once, after every middleware has returned. The response it gives you then stands in for the error's: `Status` is the error's status, `Header` and `Cookies` read only what middleware put there, and `Body` is `nil`. The kernel sends the rendered error with those headers (a decoration wins over the error's own) and cookies, but always with the error's own status, so `WithStatus` beside an error changes nothing. When a response from a layer further in is already beside the error, `ToResponse` hands that one back, so every layer decorates the same response.

`Body` is the buffered body (`nil` for a [streamed response](#streamed-responses)), and `Cookies` returns the attached cookies, which a middleware may rewrite in place, as the cookie middleware does when it seals them.

## Redirects

The `web.Redirect` function returns a `302 Found` to the given location. Its body is a small HTML page linking to the location, for clients that don't follow the `Location` header:

```go
return web.Redirect("/login?error=google_failed"), nil
```

To redirect with another status, chain `WithStatus`:

```go
return web.Redirect("/bookings").WithStatus(http.StatusSeeOther), nil
```

### Redirecting to Named Routes

Build the path of a [named route](urls.md#urls-for-named-routes) with the router's `URL` method, then redirect to it. Give the controller the router as a dependency:

```go
type BookingController struct {
	Router *web.Router
}

func (ctl *BookingController) Latest(c *web.Context) (*web.Response, error) {
	reference, err := ctl.latestReference(c)
	if err != nil {
		return nil, err
	}

	location, err := ctl.Router.URL("bookings.show", reference)
	if err != nil {
		return nil, err
	}

	return web.Redirect(location), nil
}
```

`URL` returns a path. When the client needs an absolute URL, prefix it with `c.Root()`, the scheme and host the client used (see [absolute URLs](urls.md#absolute-urls)).

### Redirecting to External Domains

`web.Redirect` takes any location, so redirecting off-site is the same call:

```go
return web.Redirect(google.AuthURL + "?" + query), nil
```

### Redirecting With Flashed Session Data

Redirecting and [flashing data to the session](session.md#flash-data) are usually done at the same time. Flash the data on the request's session through a typed key, then return the redirect:

```go
const status session.Key[string] = "status"

status.Flash(session.FromContext(c), "Your booking was saved.")

return web.Redirect("/bookings"), nil
```

## Other Response Types

### JSON Responses

The `web.JSON` function encodes a value as `application/json` immediately, so an encoding failure becomes a `500` rather than a truncated body. Returning a value directly does the same thing; reach for `web.JSON` when you want to chain a status, a header or a cookie:

```go
return web.JSON(map[string]string{"reference": booking.Reference}).WithStatus(http.StatusCreated), nil
```

### HTML Responses

The `web.HTML` function sends a string as `text/html; charset=UTF-8`. It is the only way an action sends HTML: a plain `string` it returns is JSON, so a parameter echoed back can never go out as markup. A health check might answer this way:

```go
r.Get("/up", func(*web.Context) (*web.Response, error) {
	return web.HTML("OK"), nil
}).Name("health")
```

### Raw Bytes and Files

The `web.Bytes` function sends a body with the content type you give it. reef has no file-download helper: read the file (from an `embed.FS`, say) and send its bytes:

```go
r.Get("/assets/{path...}", func(c *web.Context) (*web.Response, error) {
	name := path.Clean("assets/" + c.Param("path"))
	if !strings.HasPrefix(name, "assets/") {
		return nil, web.NotFound()
	}

	body, err := fs.ReadFile(public.Files, name)
	if err != nil {
		return nil, web.NotFound()
	}

	contentType := cmp.Or(mime.TypeByExtension(path.Ext(name)), "application/octet-stream")

	return web.Bytes(body, contentType).WithHeader("Cache-Control", "public, max-age=31536000"), nil
}).Name("assets.show")
```

To have the browser save the file instead, add a `Content-Disposition: attachment; filename="..."` header with `WithHeader`.

### Empty Responses

The `web.NoContent` function returns a bodiless `204 No Content`, the usual answer to an update or a delete:

```go
func (ctl *FavouriteController) Destroy(c *web.Context) (*web.Response, error) {
	// ...

	return web.NoContent(), nil
}
```

## Streamed Responses

The `web.Stream` function returns a response whose body is produced while it is sent: server-sent events, or any long answer the client should start reading early. You give it the content type and a function that writes the body through a `*web.StreamWriter`:

```go
return web.Stream("application/x-ndjson", func(s *web.StreamWriter) error {
	for _, line := range lines {
		if _, err := s.Write(line); err != nil {
			return err
		}
		s.Flush()
	}

	return nil
}), nil
```

A streamed response starts out with status `200` (chain `WithStatus` for another), your content type, `Cache-Control: no-cache, private` and `X-Accel-Buffering: no` (so nginx-style proxies don't buffer it), and no `Content-Length`.

The stream function runs only after the whole pipeline has returned. Middleware can therefore still change the status and headers of a streamed response, just like a buffered one; once the function starts, they are on the wire.

`Write` writes raw bytes without flushing them; `Flush` pushes everything written so far to the client. Flushing goes through `http.ResponseController`, so it reaches the connection through a writer that wraps it (a middleware's response recorder, say) as long as the wrapper has an `Unwrap() http.ResponseWriter` method. A writer that can't flush at all is logged when the stream starts, `stream cannot flush` with the path, and the body then arrives whole when the stream ends.

> [!WARNING]
> Because the function runs after the pipeline has returned, the middleware have already finished: the session has been saved, and anything a middleware set up for the request (a database transaction, say) has been closed. Do the work that needs them in the action, before returning the stream. Something the action holds for the stream (a slot, a lock) is released with [`c.Terminating`](middleware.md#terminable-middleware), which also runs when a middleware throws the stream away and it never starts.

### Event Streams (SSE)

For server-sent events, use the `text/event-stream` content type and write each event with the `Data` method. It writes `data: <payload>` followed by a blank line and flushes, so the client receives the event at once. A payload with several lines becomes one `data:` line per line, as the SSE specification requires:

```go
return web.Stream("text/event-stream", func(s *web.StreamWriter) error {
	if err := s.Data(`{"type":"RUN_STARTED"}`); err != nil {
		return err
	}

	// ...

	return s.Data("[DONE]")
}), nil
```

### Stopping When the Client Leaves

The `Context` method returns the request's context as the pipeline left it, carrying the services the kernel binds and whatever the middleware bound (the clock, the signed-in user), and canceled when the client disconnects. Once it is, every `Write` (and therefore `Data`) fails with the context's error, so a loop that checks its writes stops on its own:

```go
return web.Stream("text/event-stream", func(s *web.StreamWriter) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.Context().Done():
			return nil
		case <-ticker.C:
			if err := s.Data(`{"type":"PING"}`); err != nil {
				return err
			}
		}
	}
}), nil
```

When the work must finish even if the client leaves (persisting a chat turn whatever happens to the connection, say), run it on a context detached with `context.WithoutCancel` and ignore failed writes rather than returning them.

### Errors in a Stream

Once the stream has started, the status line is already sent, so an error can only end the stream: it can't become an error response. The function runs under the same recovery and [reporting](errors.md#reporting-errors) as an action:

- If it returns an error while the client is still connected, the error is logged as `unhandled error` and handed to the kernel's `Report`, and the response ends there. An error after the client left is neither.
- If it panics, the panic is logged with its `stack` and reported, and the connection is cut off: the client sees an interrupted response, never a clean end it could take for a complete body.

## Available Methods

#### Constructors

| Function | Response |
|---|---|
| `web.JSON(value any) *Response` | `200` `application/json`, encoded now |
| `web.HTML(body string) *Response` | `200` `text/html; charset=UTF-8`; the one way to send HTML |
| `web.Bytes(body []byte, contentType string) *Response` | `200` with the given content type |
| `web.NoContent() *Response` | `204`, no body |
| `web.Redirect(location string) *Response` | `302` with a `Location` header and a linking HTML page |
| `web.Stream(contentType string, fn func(s *StreamWriter) error) *Response` | `200`, streamed by `fn` |
| `web.ToResponse(value any, err error) (*Response, error)` | the outcome `next` returned, as a response a middleware decorates: a value's response (a string as JSON, nothing as a `500` and an error), or, beside an error, one holding only the error's status, the error handed back untouched |

#### `*web.Response`

| Method | Description |
|---|---|
| `WithStatus(status int) *Response` | sets the status code |
| `WithHeader(name, value string) *Response` | sets a header, replacing any existing value |
| `WithCookie(cookie *http.Cookie) *Response` | attaches a cookie |
| `Status() int` | the status code |
| `Header(name string) string` | a header's value, `""` when unset |
| `Body() []byte` | the buffered body (`nil` for a stream) |
| `Cookies() []*http.Cookie` | the attached cookies |

#### `*web.StreamWriter`

| Method | Description |
|---|---|
| `Write(p []byte) (int, error)` | writes raw bytes, without flushing; fails once the client has left |
| `Flush()` | pushes everything written so far to the client |
| `Data(payload string) error` | writes one SSE event and flushes it |
| `Context() context.Context` | canceled when the client disconnects |

#### Cookies on the Request

| Method | Description |
|---|---|
| `c.QueueCookie(cookie *http.Cookie)` | attaches a cookie to whatever response the request ends up with |
| `c.QueuedCookies() []*http.Cookie` | the cookies queued so far |

#### The `cookie` Package

| Function | Description |
|---|---|
| `cookie.Encrypt(encrypter *crypt.Encrypter, name, value string) (string, error)` | seals a value for the cookie `name` |
| `cookie.Decrypt(encrypter *crypt.Encrypter, name, value string) (string, bool)` | opens a value sealed for `name` under any of the encrypter's keys |
| `cookie.Prefix(name string, key []byte) string` | the name-binding prefix sealed in front of the value |
