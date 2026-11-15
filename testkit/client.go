package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atobeach/reef/panics"
)

// Client sends in-process requests through a handler (the HTTP kernel).
type Client struct {
	t       testing.TB
	handler http.Handler
	ctx     context.Context
	ip      string
	headers map[string]string
	cookies []requestCookie
}

// requestCookie is a cookie as a request carries it: just name=value.
type requestCookie struct{ name, value string }

// NewClient returns a client for handler. Its requests carry the test's
// context and come from 203.0.113.10, a documentation address.
func NewClient(t testing.TB, handler http.Handler) *Client {
	t.Helper()

	return &Client{t: t, handler: handler, ctx: t.Context(), ip: "203.0.113.10", headers: map[string]string{}}
}

// WithHeader sets a header on every following request.
func (c *Client) WithHeader(name string, value string) *Client {
	c.headers[name] = value

	return c
}

// WithCookie sends a raw, unencrypted cookie on every following request.
func (c *Client) WithCookie(name string, value string) *Client {
	c.cookies = append(c.cookies, requestCookie{name, value})

	return c
}

// FromIP sends the following requests from ip, the TCP peer the kernel sees.
func (c *Client) FromIP(ip string) *Client {
	c.ip = ip

	return c
}

// WithContext ties the following requests to ctx; ending it cancels the
// handler's context, as a browser navigating away does.
func (c *Client) WithContext(ctx context.Context) *Client {
	c.ctx = ctx

	return c
}

// Get sends a GET.
func (c *Client) Get(path string) *TestResponse {
	return c.Call(http.MethodGet, path)
}

// Post sends a POST without a body.
func (c *Client) Post(path string) *TestResponse {
	c.t.Helper()

	return c.Call(http.MethodPost, path)
}

// Call sends a method with no shorthand of its own, without a body (an
// OPTIONS preflight).
func (c *Client) Call(method string, path string) *TestResponse {
	c.t.Helper()

	return c.Send(method, path, nil, "")
}

// PostJSON sends value as a JSON body, accepting JSON back.
func (c *Client) PostJSON(path string, value any) *TestResponse {
	c.t.Helper()

	body, err := json.Marshal(value)
	if err != nil {
		c.t.Fatalf("encoding request body: %v", err)
	}

	return c.send(http.MethodPost, path, body, map[string]string{"Content-Type": "application/json", "Accept": "application/json"})
}

// Send sends any method with a raw body and content type.
func (c *Client) Send(method string, path string, body []byte, contentType string) *TestResponse {
	c.t.Helper()

	var headers map[string]string
	if body != nil {
		headers = map[string]string{"Content-Type": contentType}
	}

	return c.send(method, path, body, headers)
}

// send sends the request with these headers, then the client's own. The
// path is parsed as a request line's target, so an escape (%2F, %2e%2e) or
// a dot segment reaches the handler as sent.
func (c *Client) send(method string, path string, body []byte, headers map[string]string) *TestResponse {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req := httptest.NewRequestWithContext(c.ctx, method, path, reader)
	req.RemoteAddr = net.JoinHostPort(c.ip, "41000")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	for name, value := range c.headers {
		req.Header.Set(name, value)
	}
	for _, cookie := range c.cookies {
		//nolint:gosec // Secure, HttpOnly and SameSite are Set-Cookie attributes; a client never sends them.
		req.AddCookie(&http.Cookie{Name: cookie.name, Value: cookie.value})
	}

	recorder := httptest.NewRecorder()
	aborted, cutOff := c.serve(recorder, req)

	res := &TestResponse{
		t:       c.t,
		status:  recorder.Code,
		header:  recorder.Header(),
		body:    recorder.Body.Bytes(),
		cookies: recorder.Result().Cookies(),
		aborted: aborted,
	}

	if cutOff {
		// The kernel cuts a response off only when its stream panicked: a
		// bug, unless the test says it expects one.
		c.t.Cleanup(func() {
			if !res.abortExpected {
				c.t.Errorf("%s %s: the handler cut the response off (a stream that panicked); call AssertAborted if the test expects that", method, path)
			}
		})
	}

	return res
}

// serve runs the handler as net/http's server does: a panic aborts the
// response, keeping what was written before it. http.ErrAbortHandler is the
// handler cutting it off on purpose (cutOff), which the test must expect;
// any other panic fails the test at once (Errorf, which the goroutines a
// test sends from may call).
func (c *Client) serve(w http.ResponseWriter, req *http.Request) (aborted bool, cutOff bool) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}

		aborted = true
		if err, _ := recovered.(error); errors.Is(err, http.ErrAbortHandler) {
			cutOff = true

			return
		}

		p := panics.New(recovered)
		c.t.Errorf("the handler panicked: %v\n%s", p, p.Stack())
	}()

	c.handler.ServeHTTP(w, req)

	return false, false
}
