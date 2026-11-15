// Package httpclient is how the app calls third-party APIs: a Connector (base
// URL, auth, timeouts, retries, response hooks) sending typed Requests, with
// a MockClient keyed by request type so tests never reach the network.
//
//	type StartFlightSearch struct{ Origin, Destination string }
//
//	func (StartFlightSearch) Method() string   { return "POST" }
//	func (StartFlightSearch) Endpoint() string { return "/flights/search" }
//	func (r StartFlightSearch) Body() any      { return map[string]string{"from": r.Origin, "to": r.Destination} }
//
//	res, err := flights.Send(ctx, StartFlightSearch{"LHR", "JFK"})
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Request is one API call.
type Request interface {
	Method() string
	// Endpoint is appended to the connector's base URL.
	Endpoint() string
}

// WithQuery adds query parameters.
type WithQuery interface {
	Query() url.Values
}

// WithBody sends a JSON body (any json.Marshal-able value).
type WithBody interface {
	Body() any
}

// WithRawBody sends a non-JSON body as-is with its content type, over any
// WithBody.
type WithRawBody interface {
	RawBody() (body []byte, contentType string)
}

// QueryPairs are query parameters in the order they are sent.
type QueryPairs [][2]string

// WithOrderedQuery adds query parameters in their own order, over any
// WithQuery, for upstreams that read them positionally.
type WithOrderedQuery interface {
	OrderedQuery() QueryPairs
}

// WithTimeout is a request-level timeout overriding the connector's.
type WithTimeout interface {
	Timeout() time.Duration
}

// WithHeaders adds per-request headers.
type WithHeaders interface {
	Headers() map[string]string
}

// Retry decides whether to re-send after an attempt (1-based) failed with
// res (a received response) or err (a transport error).
type Retry func(req Request, attempt int, res *Response, err error) bool

// Hook observes every completed attempt — timing, telemetry, logging.
type Hook func(ctx context.Context, req Request, res *Response, err error, elapsed time.Duration)

// Connector configures a client for one upstream API.
type Connector struct {
	BaseURL        string
	Headers        map[string]string
	Timeout        time.Duration // whole request, including the body
	ConnectTimeout time.Duration
	Retry          Retry
	// RetryDelay waits between attempts; nil means no wait.
	RetryDelay func(attempt int) time.Duration
	// RetryInterval is a per-request wait, over RetryDelay when set.
	RetryInterval func(req Request, attempt int) time.Duration
	Hooks         []Hook
	// HasRequestFailed overrides what counts as a failed response, for APIs
	// that fail inside a 2xx envelope. It judges by status and body, never by
	// res.Failed(), which calls it.
	HasRequestFailed func(res *Response) bool
	// MapError maps a failed response to the error Err returns; nil keeps
	// the *StatusError.
	MapError func(res *Response) error
	// EncodeBody encodes WithBody bodies; nil means json.Marshal.
	EncodeBody func(body any) ([]byte, error)
	// Proxy routes every request through an egress proxy (ParseProxy); nil
	// means direct.
	Proxy *url.URL
}

// errInvalidProxy never echoes the setting: a proxy URL carries credentials.
var errInvalidProxy = errors.New("httpclient: not a proxy URL; want http, https, socks5 or socks5h://[user:pass@]host:port")

// ParseProxy parses an egress proxy setting ("" is none). A bad value fails
// at boot rather than sending traffic around the proxy.
func ParseProxy(setting string) (*url.URL, error) {
	if setting == "" {
		return nil, nil
	}

	proxy, err := url.Parse(setting)
	if err != nil || proxy.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, proxy.Scheme) {
		return nil, errInvalidProxy
	}

	return proxy, nil
}

// Client sends requests through a connector. Safe for concurrent use.
type Client struct {
	connector Connector
	http      *http.Client
	mock      *MockClient
}

// New builds a client.
func New(connector Connector) *Client {
	dialer := &net.Dialer{Timeout: connector.ConnectTimeout}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	if connector.Proxy != nil {
		transport.Proxy = http.ProxyURL(connector.Proxy)
	}

	return &Client{connector: connector, http: &http.Client{Timeout: connector.Timeout, Transport: transport}}
}

// WithMock returns a client answering from mock instead of the network.
func (c *Client) WithMock(mock *MockClient) *Client {
	clone := *c
	clone.mock = mock

	return &clone
}

// Send performs the request, retrying per the connector's policy. A failed
// response is returned, not an error (see res.Err); err is a transport
// failure.
func (c *Client) Send(ctx context.Context, req Request) (*Response, error) {
	for attempt := 1; ; attempt++ {
		started := time.Now()
		res, err := c.attempt(ctx, req)

		for _, hook := range c.connector.Hooks {
			hook(ctx, req, res, err, time.Since(started))
		}

		failed := err != nil || (res != nil && res.Failed())
		if !failed || c.connector.Retry == nil || !c.connector.Retry(req, attempt, res, err) {
			return res, err
		}

		var delay time.Duration
		switch {
		case c.connector.RetryInterval != nil:
			delay = c.connector.RetryInterval(req, attempt)
		case c.connector.RetryDelay != nil:
			delay = c.connector.RetryDelay(attempt)
		}

		if delay > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
}

// attempt sends the request once, reading the whole body.
func (c *Client) attempt(ctx context.Context, req Request) (*Response, error) {
	httpReq, body, err := c.build(ctx, req)
	if err != nil {
		return nil, err
	}

	if c.mock != nil {
		return c.mock.respond(c, req, httpReq, body)
	}

	client := c.http
	if t, ok := req.(WithTimeout); ok && t.Timeout() > 0 {
		clone := *c.http
		clone.Timeout = t.Timeout()
		client = &clone
	}

	httpRes, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpRes.Body.Close()

	raw, err := io.ReadAll(httpRes.Body)
	if err != nil {
		return nil, fmt.Errorf("httpclient: reading %s response: %w", Name(req), err)
	}

	return c.response(httpRes.StatusCode, httpRes.Header, raw, req), nil
}

// response wraps a received reply with the connector's failure policy.
func (c *Client) response(status int, header http.Header, body []byte, req Request) *Response {
	return &Response{
		Status:           status,
		Header:           header,
		body:             body,
		request:          req,
		hasRequestFailed: c.connector.HasRequestFailed,
		mapError:         c.connector.MapError,
	}
}

// build turns a typed request into the HTTP request, returning the body it
// carries for the mock to record.
func (c *Client) build(ctx context.Context, req Request) (*http.Request, []byte, error) {
	body, contentType, err := c.encodeBody(req)
	if err != nil {
		return nil, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method(), c.target(req), bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("httpclient: building %s: %w", Name(req), err)
	}

	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", contentType)
	}

	for name, value := range c.connector.Headers {
		httpReq.Header.Set(name, value)
	}

	if h, ok := req.(WithHeaders); ok {
		for name, value := range h.Headers() {
			httpReq.Header.Set(name, value)
		}
	}

	return httpReq, body, nil
}

// target is the request's full URL: base URL, endpoint and query string.
func (c *Client) target(req Request) string {
	target := strings.TrimRight(c.connector.BaseURL, "/") + "/" + strings.TrimLeft(req.Endpoint(), "/")

	switch q := req.(type) {
	case WithOrderedQuery:
		if pairs := q.OrderedQuery(); len(pairs) > 0 {
			target += "?" + pairs.Encode()
		}
	case WithQuery:
		if values := q.Query(); len(values) > 0 {
			target += "?" + values.Encode()
		}
	}

	return target
}

// encodeBody is the request's body and content type; a request without one
// has a nil body.
func (c *Client) encodeBody(req Request) (body []byte, contentType string, err error) {
	switch b := req.(type) {
	case WithRawBody:
		body, contentType = b.RawBody()

		return body, contentType, nil
	case WithBody:
		encode := json.Marshal
		if c.connector.EncodeBody != nil {
			encode = c.connector.EncodeBody
		}

		body, err = encode(b.Body())
		if err != nil {
			return nil, "", fmt.Errorf("httpclient: encoding %s body: %w", Name(req), err)
		}
	}

	return body, "application/json", nil
}

// Name is a request's type name — what mocks and telemetry key on.
func Name(req Request) string {
	t := reflect.TypeOf(req)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t.Name()
}

// Encode joins the pairs in order, form-encoding each key and value.
func (q QueryPairs) Encode() string {
	parts := make([]string, 0, len(q))
	for _, pair := range q {
		parts = append(parts, formEncode(pair[0])+"="+formEncode(pair[1]))
	}

	return strings.Join(parts, "&")
}

// Get is the first value for key, "" when absent.
func (q QueryPairs) Get(key string) (string, bool) {
	for _, pair := range q {
		if pair[0] == key {
			return pair[1], true
		}
	}

	return "", false
}

// formEncode is url.QueryEscape that also escapes "~", the encoding the
// upstreams were integrated against.
func formEncode(s string) string {
	var out strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.':
			out.WriteByte(c)
		case c == ' ':
			out.WriteByte('+')
		default:
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}

	return out.String()
}

// Response is a received response.
type Response struct {
	Status           int
	Header           http.Header
	body             []byte
	request          Request
	hasRequestFailed func(res *Response) bool
	mapError         func(res *Response) error
}

// Request is the request this response answers.
func (r *Response) Request() Request { return r.request }

// Body is the raw body.
func (r *Response) Body() []byte { return r.body }

// Successful is a 2xx.
func (r *Response) Successful() bool { return r.Status >= 200 && r.Status < 300 }

// Failed is a 4xx or 5xx, or whatever the connector's HasRequestFailed says.
func (r *Response) Failed() bool {
	if r.hasRequestFailed != nil {
		return r.hasRequestFailed(r)
	}

	return r.Status >= 400
}

// JSON decodes the body into dst.
func (r *Response) JSON(dst any) error {
	if err := json.Unmarshal(r.body, dst); err != nil {
		return fmt.Errorf("httpclient: %s returned non-JSON (status %d): %w", Name(r.request), r.Status, err)
	}

	return nil
}

// Err is the error for a failed response, nil otherwise: the connector's
// MapError, else a *StatusError.
func (r *Response) Err() error {
	if !r.Failed() {
		return nil
	}

	if r.mapError != nil {
		if err := r.mapError(r); err != nil {
			return err
		}
	}

	return &StatusError{Request: Name(r.request), Status: r.Status, Body: r.body}
}

// StatusError is a failed response as an error. Error() names only the
// request and status: an upstream body can echo personal data, and error
// messages travel to logs and error tracking.
type StatusError struct {
	Request string
	Status  int
	Body    []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("httpclient: %s failed with %d", e.Request, e.Status)
}

// BodyPreview is the first 300 bytes of the body, for a deliberate log line.
func (e *StatusError) BodyPreview() string {
	preview := string(e.Body)
	if len(preview) > 300 {
		preview = preview[:300] + "…"
	}

	return preview
}

// IsStatus reports whether err is a StatusError with one of the statuses.
func IsStatus(err error, statuses ...int) bool {
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		return false
	}

	for _, status := range statuses {
		if statusErr.Status == status {
			return true
		}
	}

	return false
}
