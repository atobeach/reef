# HTTP Client

- [Introduction](#introduction)
- [Defining Connectors](#defining-connectors)
- [Defining Requests](#defining-requests)
- [Making Requests](#making-requests)
    - [Request Data](#request-data)
    - [Headers](#headers)
    - [Authentication](#authentication)
    - [Timeout](#timeout)
    - [Retries](#retries)
    - [Error Handling](#error-handling)
    - [Proxies](#proxies)
    - [Streaming Responses](#streaming-responses)
- [Concurrent Requests](#concurrent-requests)
- [Hooks](#hooks)
- [Testing](#testing)
    - [Faking Responses](#faking-responses)
    - [Inspecting Requests](#inspecting-requests)
    - [Preventing Stray Requests](#preventing-stray-requests)
- [Available Methods](#available-methods)
    - [Connector Options](#connector-options)
    - [Request Interfaces](#request-interfaces)
    - [Response Methods](#response-methods)

## Introduction

reef provides a small, typed API around Go's `net/http` client, focused on the way applications actually talk to third-party APIs. Instead of building URLs and headers at every call site, you describe each upstream API once as a **connector** (its base URL, headers, timeouts, retry policy, failure rules and hooks), and each call as a **request**: a small Go type that knows its method, endpoint, query and body. If you've used Saloon with Laravel, the shape will feel familiar.

A flight-booking application might talk to an airline's booking API, a weather service, an analytics database and an AI gateway this way, and its tests answer every one of those requests from a mock keyed by the request's type, so no test ever reaches the network.

## Defining Connectors

A connector is an `httpclient.Connector` value. Pass it to `httpclient.New` to build a `*httpclient.Client`, which is safe for concurrent use, so build it once and share it. For example, a connector for a weather service might look like this:

```go
import (
	"time"

	"github.com/atobeach/reef/httpclient"
)

// Connector is the forecast API.
func Connector() httpclient.Connector {
	return httpclient.Connector{
		BaseURL:        "https://api.weather.example/v1",
		ConnectTimeout: 5 * time.Second,
		Timeout:        10 * time.Second,
	}
}
```

Every field is optional; the [reference](#connector-options) lists them all. A service that calls the API receives the client as a dependency, built in a [provider](lifecycle.md):

```go
forecasts := services.NewAirportWeather(httpclient.New(weather.Connector()), store)
```

```go
func NewAirportWeather(client *httpclient.Client, store cache.Store) *AirportWeather {
	return &AirportWeather{client: client, cache: store}
}
```

An application may build its clients through a small helper of its own, `a.HTTP(connector)`, which calls `httpclient.New` and, when a test has installed a [mock](#faking-responses), returns the client's `WithMock` copy instead, so no test can reach a live API.

## Defining Requests

A request is any type with a `Method` and an `Endpoint`, the `httpclient.Request` interface. The endpoint is appended to the connector's base URL, with exactly one slash between them. Here is a request for the weather service's daily forecast:

```go
// GetForecast is the daily forecast for one coordinate.
type GetForecast struct {
	Latitude  float64
	Longitude float64
}

// Method implements httpclient.Request.
func (GetForecast) Method() string { return "GET" }

// Endpoint implements httpclient.Request.
func (GetForecast) Endpoint() string { return "/forecast" }

// Query implements httpclient.WithQuery.
func (r GetForecast) Query() url.Values {
	return url.Values{
		"latitude":  {strconv.FormatFloat(r.Latitude, 'f', -1, 64)},
		"longitude": {strconv.FormatFloat(r.Longitude, 'f', -1, 64)},
		"daily":     {"temperature_2m_max,weather_code"},
	}
}
```

A request adds a query string, a body, headers or its own timeout by implementing more small interfaces, such as `WithQuery` above. They're described in the sections below and listed in the [reference](#request-interfaces). `Send` finds each by type assertion, so declare the ones a request means to implement beside its type, and a slipped signature fails the build instead of dropping the query:

```go
var _ httpclient.WithQuery = GetForecast{}
```

The request's type name, as returned by `httpclient.Name(req)`, is its identity: [mocks](#faking-responses), [hooks](#hooks) and error messages all use it. A pointer to a request has the same name as the request itself.

## Making Requests

To make a request, pass it to the client's `Send` method with a context:

```go
res, err := client.Send(ctx, GetForecast{Latitude: 39.47, Longitude: -0.38})
if err != nil {
	return Forecast{}, fmt.Errorf("fetching the forecast: %w", err)
}

if err := res.Err(); err != nil {
	return Forecast{}, fmt.Errorf("fetching the forecast: %w", err)
}

var forecast Forecast
if err := res.JSON(&forecast); err != nil {
	return Forecast{}, err
}
```

`Send` returns an `*httpclient.Response`, which you may use to inspect the response:

```go
res.Status                     // int
res.Header.Get("Content-Type") // http.Header
res.Body()                     // []byte
res.JSON(&dst)                 // error
res.Successful()               // bool: a 2xx
res.Failed()                   // bool: a 4xx or 5xx, or the connector's own rule
res.Err()                      // error: nil unless Failed
res.Request()                  // httpclient.Request: the request it answers
```

The whole body is read before `Send` returns, so there is nothing to close.

> [!NOTE]
> A `404` or a `500` is a response, not an error: `Send`'s `err` is for failures where no response arrived at all (DNS, a refused connection, a timeout, a reset) and for a request that couldn't be built, such as a body that failed to encode. Always check `res.Err()` or `res.Failed()` as well.

### Request Data

#### Query Parameters

Implement `WithQuery` to add query parameters from a `url.Values`, as `GetForecast` does above. Its keys are encoded in sorted order.

Some upstreams read their query parameters by position. For those, implement `WithOrderedQuery` instead, returning `httpclient.QueryPairs`, which are sent in the order you list them. For example, a request to an airline API that expects its parameters in a fixed order:

```go
type GetFareRules struct {
	OfferID string
	Cabin   string
}

func (GetFareRules) Method() string   { return "GET" }
func (GetFareRules) Endpoint() string { return "/v2/offers/fare-rules" }

// OrderedQuery implements httpclient.WithOrderedQuery.
func (r GetFareRules) OrderedQuery() httpclient.QueryPairs {
	return httpclient.QueryPairs{{"offerId", r.OfferID}, {"cabin", r.Cabin}}
}
```

`QueryPairs.Encode` form-encodes each key and value (a space becomes `+`, and everything but letters, digits and `-_.` is percent-encoded, `~` included), and `QueryPairs.Get` returns the first value for a key:

```go
q := httpclient.QueryPairs{{"offerId", "a b"}, {"cabin", "economy"}}

q.Encode()       // offerId=a+b&cabin=economy
q.Get("offerId") // "a b", true
```

When a request implements both interfaces, `WithOrderedQuery` wins. An empty query adds no `?` to the URL.

#### JSON Bodies

Implement `WithBody` to send a JSON body. `Body` returns any value `encoding/json` can marshal, and the request is sent with `Content-Type: application/json`. A struct with JSON tags keeps the upstream's field names out of your request type:

```go
type CreateBooking struct {
	OfferID      string
	TotalAmount  int64
	ContactEmail string
}

func (CreateBooking) Method() string   { return "POST" }
func (CreateBooking) Endpoint() string { return "/v2/bookings" }

type createBookingBody struct {
	OfferID      string `json:"offerId"`
	TotalAmount  int64  `json:"totalAmount"`
	ContactEmail string `json:"contactEmail"`
}

// Body implements httpclient.WithBody.
func (r CreateBooking) Body() any {
	return createBookingBody{OfferID: r.OfferID, TotalAmount: r.TotalAmount, ContactEmail: r.ContactEmail}
}
```

If an upstream needs a different JSON encoder, set the connector's `EncodeBody`; it encodes every `WithBody` body in place of `json.Marshal`. reef's [AI](ai.md) gateway uses it so an application can resend conversation history in exactly the encoding it stored it with.

#### Raw Bodies

To send a body that isn't JSON, implement `WithRawBody`, which returns the bytes and their content type. It takes precedence over `WithBody`. For example, a request that uploads a CSV file of fares:

```go
// RawBody implements httpclient.WithRawBody.
func (r *UploadFares) RawBody() (body []byte, contentType string) {
	return r.CSV, "text/csv"
}
```

### Headers

Every request is sent with `Accept: application/json`, plus a `Content-Type` when it has a body. A connector's `Headers` are set next, so they may override those defaults, and a request that implements `WithHeaders` sets its own headers last, overriding the connector's:

```go
type GetImage struct {
	Path string
}

func (GetImage) Method() string     { return "GET" }
func (r GetImage) Endpoint() string { return r.Path }

// Headers implements httpclient.WithHeaders.
func (GetImage) Headers() map[string]string {
	return map[string]string{"Accept": "*/*"}
}
```

### Authentication

There are no authentication helpers: credentials are headers. Put an API's credentials on its connector, and every request carries them. For example, an airline connector that sends an API key and a bearer token:

```go
httpclient.Connector{
	BaseURL: cfg.BaseURL,
	Headers: map[string]string{
		"X-Api-Key":     cfg.APIKey,
		"Authorization": "Bearer " + cfg.BearerToken,
		"User-Agent":    "Flights/1.0",
	},
}
```

A credential that varies per call belongs on the request, through `WithHeaders`.

### Timeout

A connector's `Timeout` bounds the whole request, from connecting to reading the last byte of the body, and its `ConnectTimeout` bounds dialing the connection (Go's default transport separately limits a TLS handshake to 10 seconds). Both default to zero, meaning no limit, so set them on every connector.

A request that needs a different limit may implement `WithTimeout`. A positive duration replaces the connector's `Timeout` for that request, longer or shorter:

```go
type GetSchedules struct{}

func (GetSchedules) Method() string   { return "GET" }
func (GetSchedules) Endpoint() string { return "/v2/schedules" }

// Timeout implements httpclient.WithTimeout: one large export, at the
// endpoint's mercy.
func (GetSchedules) Timeout() time.Duration { return 5 * time.Minute }
```

The context you pass to `Send` bounds the request too, so a deadline on it applies whatever the timeouts say.

### Retries

Requests are never retried unless the connector says so. Its `Retry` function, an `httpclient.Retry`, is consulted after every attempt that failed, whether no response arrived (`err` is set and `res` is `nil`) or the response [failed](#error-handling), and returns whether to send the request again. `attempt` counts from 1:

```go
httpclient.Connector{
	BaseURL: "https://api.example.com",
	Retry: func(req httpclient.Request, attempt int, res *httpclient.Response, err error) bool {
		if attempt >= 3 {
			return false
		}

		return err != nil || slices.Contains([]int{502, 503, 504}, res.Status)
	},
	RetryDelay: func(attempt int) time.Duration {
		return time.Duration(attempt) * 200 * time.Millisecond
	},
}
```

`RetryDelay` is the wait before the next attempt, given the attempt that just failed. When the wait depends on the request, set `RetryInterval` instead, which receives the request too and takes precedence over `RetryDelay`. Without either, the request is re-sent at once. If the context ends during a wait, `Send` returns the last response together with the context's error.

When `Retry` declines, `Send` returns the last attempt's response and error, exactly as if there had been no retry policy.

> [!WARNING]
> Only retry requests that are safe to send twice. A `GET` can usually be repeated, but a `POST` that creates something may have succeeded before the connection dropped, and sending it again would create it twice. Retry those only when the API offers an idempotency key.

### Error Handling

Because a failed response isn't an error, you decide how to treat it. `res.Failed()` reports a `4xx` or `5xx` status, and `res.Err()` turns a failed response into an error, `nil` otherwise. By default that error is an `*httpclient.StatusError`, carrying the request's name, the status and the body:

```go
if err := res.Err(); err != nil {
	if httpclient.IsStatus(err, http.StatusNotFound, http.StatusGone) {
		return nil
	}

	var status *httpclient.StatusError
	if errors.As(err, &status) {
		slog.WarnContext(ctx, "airline fare rules failed", "request", status.Request, "status", status.Status, "body", status.BodyPreview())
	}

	return err
}
```

`IsStatus` reports whether an error, however deeply wrapped, is a `StatusError` with one of the given statuses.

A `StatusError`'s message (its `Error` method) names only the request and the status, as in `httpclient: GetFareRules failed with 503`. Upstream bodies can echo back personal data, and error messages travel into logs and error tracking, so the body stays on the `Body` field. `BodyPreview` returns its first 300 bytes, followed by `…` when there is more, for a log line you write deliberately.

`res.JSON` returns an error naming the request and the status when the body isn't valid JSON.

#### Customizing Failure

Some APIs report failure inside a `200`. A connector's `HasRequestFailed` replaces the rule for what counts as a failed response, and `res.Failed()`, `res.Err()` and the retry loop all consult it. Its `MapError` maps a failed response to the error `res.Err()` returns; returning `nil` keeps the `*StatusError`. For example, a connector for an API that wraps every answer in a `success` envelope may treat `success: false` as a failure, and map one of the API's error codes to an error type of your own:

```go
httpclient.Connector{
	BaseURL: cfg.BaseURL,
	HasRequestFailed: func(res *httpclient.Response) bool {
		if res.Status >= 400 {
			return true
		}

		var body struct {
			Success bool `json:"success"`
		}
		if json.Unmarshal(res.Body(), &body) != nil {
			return true
		}

		return !body.Success
	},
	MapError: func(res *httpclient.Response) error {
		if airline.ErrorCode(res) == "rate_limited" {
			return &airline.RateLimitedError{}
		}

		return nil
	},
}
```

> [!WARNING]
> `HasRequestFailed` must judge by the status and the body alone. Calling `res.Failed()` from inside it calls `HasRequestFailed` again, forever.

### Proxies

To send a connector's traffic through an egress proxy, set its `Proxy` to the proxy's URL. `httpclient.ParseProxy` builds one from a configuration setting: an empty setting is `nil` (no proxy), and anything that isn't an `http`, `https`, `socks5` or `socks5h` URL with a host is an error, so a typo fails at boot instead of quietly sending traffic around the proxy. The error never repeats the setting, since a proxy URL may carry credentials:

```go
proxy, err := httpclient.ParseProxy(env.Get("AIRLINE_PROXY", ""))
if err != nil {
	return fmt.Errorf("AIRLINE_PROXY: %w", err)
}

connector := httpclient.Connector{BaseURL: "https://api.airline.example", Proxy: proxy}
```

When `Proxy` is `nil`, the client keeps Go's default transport behavior, which honors the `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` environment variables.

### Streaming Responses

For server-sent events and other long responses you want to read as they arrive, use `Stream` instead of `Send`. It returns the response's status and headers along with the body, unread. A failed response is read whole and returned with a `nil` body instead, so you handle it like any other failure:

```go
res, body, err := client.Stream(ctx, ChatStream{Model: "openai/gpt-5"})
if err != nil {
	return err
}

if err := res.Err(); err != nil {
	return err
}
defer body.Close()

// Read events from body as they arrive...
```

You must close the body. `Stream` sends the request once, never retrying, because a streamed body can't be replayed, and its hooks run once, when the headers arrive.

> [!NOTE]
> `Stream` decides whether a response failed from its headers, before any of the body is read, so a connector's `HasRequestFailed` is handed an empty body. An envelope check such as the `success` rule [above](#customizing-failure) would judge every streamed response failed and read it whole, so stream only through connectors whose failure rule needs nothing but the status. Under a [mock](#faking-responses), the canned body is already there when the rule runs.

> [!WARNING]
> `Stream` ignores `WithTimeout`, and the connector's `Timeout` covers reading the whole body, so it would cut a long stream off. A connector you stream through should leave `Timeout` at zero, set a `ConnectTimeout`, and bound each stream with a deadline on its context, as reef's [AI](ai.md) gateway does.

## Concurrent Requests

There is no request pool: a `Client` is safe for concurrent use, so you may send several requests at once from your own goroutines.

## Hooks

A connector's `Hooks` observe every completed attempt, for timing, telemetry and logging. Each `httpclient.Hook` receives the context, the request, the response (`nil` when no response arrived), the error, and how long the attempt took. Hooks run in order after every attempt, retried attempts included, before the retry decision:

```go
httpclient.Connector{
	BaseURL: "https://api.example.com",
	Hooks: []httpclient.Hook{
		func(ctx context.Context, req httpclient.Request, res *httpclient.Response, err error, elapsed time.Duration) {
			status := 0
			if res != nil {
				status = res.Status
			}

			slog.InfoContext(ctx, "outbound request", "request", httpclient.Name(req), "status", status, "error", err, "elapsed_ms", elapsed.Milliseconds())
		},
	},
}
```

A hook can just as well record a timing metric for every call.

## Testing

### Faking Responses

`httpclient.NewMock` builds a `*httpclient.MockClient` that answers requests by their [type name](#defining-requests), and a client's `WithMock` method returns a copy of the client that answers from the mock instead of the network:

```go
func TestTheForecastIsRead(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{
		"GetForecast": httpclient.JSON(200, map[string]any{
			"daily": map[string]any{"time": []string{"2026-10-22"}},
		}),
	})
	client := httpclient.New(Connector()).WithMock(mock)

	forecast, err := fetchForecast(t.Context(), client)
	if err != nil || len(forecast.Daily.Time) != 1 {
		t.Fatalf("forecast = %+v, err = %v", forecast, err)
	}

	httpclienttest.AssertSentTimes[GetForecast](t, mock, 1)
}
```

Only the network is replaced: the mocked client still builds every request, applies the connector's failure rules and retry policy, and runs its hooks.

Each reply is one of three things:

- A `httpclient.MockResponse`: a canned `Status`, `Body` and `Header`, or an `Err` that simulates a transport failure such as a timeout or a reset. `httpclient.JSON(status, body)` builds one from a Go value, or from a `json.RawMessage` sent as is, with a JSON content type; it panics on a value `encoding/json` can't encode, failing the test where the reply is written.
- A `httpclient.Sequence` of responses, replied in turn and then repeating the last, for polls that progress.
- A `httpclient.Responder`, a function that builds the reply from the `httpclient.Sent` request, for a request type whose reply depends on its parameters.

```go
mock := httpclient.NewMock(t, map[string]any{
	"PollFlightResults": httpclient.Sequence{
		httpclient.JSON(200, map[string]any{"status": "pending"}),
		httpclient.JSON(200, map[string]any{"status": "pending"}),
		httpclient.JSON(200, map[string]any{"status": "complete"}),
	},
	"GetFareRules": httpclient.MockResponse{Status: 503, Body: []byte("upstream down")},
	"GetSchedules": httpclient.MockResponse{Err: context.DeadlineExceeded},
	"PopularRoutes": httpclient.Responder(func(sent httpclient.Sent) httpclient.MockResponse {
		if sent.HTTP.URL.Query().Get("origin") == "LHR" {
			return httpclient.JSON(200, json.RawMessage(`{"data":[{"destination":"JFK"}]}`))
		}

		return httpclient.JSON(200, json.RawMessage(`{"data":[]}`))
	}),
})
```

Any other value panics, including a bare function literal: wrap it in `httpclient.Responder(...)` as above. To change a reply part-way through a test, call `Add`, which sets or replaces the reply for a request name; a `Sequence` starts over:

```go
mock.Add("GetForecast", httpclient.JSON(500, nil))
```

If your [test case](testing.md#a-full-test-case) builds every client on one shared mock, a test can pass its replies in when it boots and add more later. Here `tests.WithHTTP` and `tc.HTTP` are such a test case's own helpers:

```go
tc := tests.Setup(t, tests.WithHTTP(map[string]any{
	"GetForecast": httpclient.JSON(200, forecast),
}))

tc.HTTP.Add("GetForecast", httpclient.JSON(500, nil))
```

> [!NOTE]
> Mocks match on the bare type name, without the package, so two request types named alike in different packages share a reply. Also note that a connector's retry delays are real waits even under a mock; keep them short in tests.

### Inspecting Requests

The mock records every request it receives. `Sent` returns them in order, each an `httpclient.Sent` with the typed `Request`, the fully built `*http.Request` (`HTTP`: its URL and headers), and the encoded `Body`:

```go
for _, sent := range mock.Sent() {
	t.Log(httpclient.Name(sent.Request), sent.HTTP.URL, string(sent.Body))
}
```

The `httpclienttest` package asserts on what was sent by request type, listing what went out when it fails:

```go
httpclienttest.AssertSentTimes[airline.CreateBooking](t, tc.HTTP, 1)
httpclienttest.AssertNotSent[weather.GetClimateHistory](t, tc.HTTP)
```

Its full set of assertions is documented in [mocking](mocking.md).

### Preventing Stray Requests

A mock never lets a request through to the network. A request with no reply fails the test with `httpclient: no mock for {method} {url} ({name})`, and `Send` returns an `httpclient: unmocked request {name}` error to the code under test.

`NewMock` takes an `httpclient.TestingT`, the `Helper` and `Errorf` part of `testing.TB`, rather than a `*testing.T`, so the `testing` package never links into your production binary; pass your test's `t`.

## Available Methods

### Connector Options

| Field | Description |
| --- | --- |
| `BaseURL` | Prefixed to every request's endpoint. |
| `Headers` | Sent with every request, after the defaults and before the request's own. |
| `Timeout` | Bounds the whole request, body included. Zero means no limit. |
| `ConnectTimeout` | Bounds dialing the connection. Zero means no limit. |
| `Retry` | Decides whether to re-send after a failed attempt; `nil` never retries. |
| `RetryDelay` | The wait before the next attempt; `nil` doesn't wait. |
| `RetryInterval` | A per-request wait; takes precedence over `RetryDelay`. |
| `Hooks` | Observe every completed attempt. |
| `HasRequestFailed` | Replaces the "4xx or 5xx" rule for a failed response. |
| `MapError` | Maps a failed response to the error `Err` returns; `nil` keeps the `*StatusError`. |
| `EncodeBody` | Encodes `WithBody` bodies; `nil` means `json.Marshal`. |
| `Proxy` | Routes every request through an egress proxy; see `ParseProxy`. |

### Request Interfaces

| Interface | Method | Description |
| --- | --- | --- |
| `Request` | `Method() string`, `Endpoint() string` | Required: the HTTP method, and the path appended to the base URL. |
| `WithQuery` | `Query() url.Values` | Query parameters, keys sorted. |
| `WithOrderedQuery` | `OrderedQuery() QueryPairs` | Query parameters in their own order. Takes precedence over `WithQuery`. |
| `WithBody` | `Body() any` | A JSON body. |
| `WithRawBody` | `RawBody() (body []byte, contentType string)` | A body sent as is. Takes precedence over `WithBody`. |
| `WithHeaders` | `Headers() map[string]string` | Headers for this request, overriding the connector's. |
| `WithTimeout` | `Timeout() time.Duration` | Replaces the connector's `Timeout` for this request (`Send` only). |

### Response Methods

| Member | Description |
| --- | --- |
| `Status` | The status code. |
| `Header` | The response headers. |
| `Body()` | The raw body. After a successful `Stream`, read the body `Stream` returned instead. |
| `JSON(dst)` | Decodes the body into `dst`. |
| `Successful()` | Whether the status is `2xx`. |
| `Failed()` | Whether the status is `4xx` or `5xx`, or what the connector's `HasRequestFailed` says. |
| `Err()` | `nil` unless `Failed`; then the connector's `MapError`, else a `*StatusError`. |
| `Request()` | The request this response answers. |
