package testkit_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/atobeach/reef/testkit"
)

// echo answers with what the request carried, so a test can read back the
// headers, cookie, body and peer the client sent.
var echo = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	visitor, _ := r.Cookie("visitor")
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	if len(body) == 0 {
		body = []byte(`""`)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"method": r.Method, "accept": r.Header.Get("Accept"), "type": r.Header.Get("Content-Type"),
		"locale": r.Header.Get("X-Locale"), "visitor": cookieValue(visitor), "body": json.RawMessage(body), "ip": ip,
	})
})

func cookieValue(c *http.Cookie) string {
	if c == nil {
		return ""
	}

	return c.Value
}

func TestTheClientSendsWhatEachCallSays(t *testing.T) {
	cases := []struct {
		name string
		send func(client *testkit.Client) *testkit.TestResponse
		want map[string]any
	}{
		{
			"a JSON post accepts JSON back",
			func(client *testkit.Client) *testkit.TestResponse {
				return client.PostJSON("/x", map[string]int{"n": 1})
			},
			map[string]any{"method": "POST", "accept": "application/json", "type": "application/json", "locale": "en-GB", "visitor": "tok-1", "body": map[string]any{"n": 1}, "ip": "203.0.113.10"},
		},
		{
			"a get carries no body",
			func(client *testkit.Client) *testkit.TestResponse { return client.Get("/x") },
			map[string]any{"method": "GET", "accept": "", "type": "", "locale": "en-GB", "visitor": "tok-1", "body": "", "ip": "203.0.113.10"},
		},
		{
			"a post carries no body",
			func(client *testkit.Client) *testkit.TestResponse { return client.Post("/x") },
			map[string]any{"method": "POST", "accept": "", "type": "", "locale": "en-GB", "visitor": "tok-1", "body": "", "ip": "203.0.113.10"},
		},
		{
			"a raw send keeps its content type",
			func(client *testkit.Client) *testkit.TestResponse {
				return client.Send("PUT", "/x", []byte(`"raw"`), "text/plain")
			},
			map[string]any{"method": "PUT", "accept": "", "type": "text/plain", "locale": "en-GB", "visitor": "tok-1", "body": "raw", "ip": "203.0.113.10"},
		},
		{
			"FromIP is the peer the handler sees",
			func(client *testkit.Client) *testkit.TestResponse { return client.FromIP("198.51.100.7").Get("/x") },
			map[string]any{"method": "GET", "accept": "", "type": "", "locale": "en-GB", "visitor": "tok-1", "body": "", "ip": "198.51.100.7"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := testkit.NewClient(t, echo).WithHeader("X-Locale", "en-GB").WithCookie("visitor", "tok-1")

			c.send(client).AssertOk().AssertJSON(c.want)
		})
	}
}

// A client that leaves mid-request cancels the handler's context, as a
// browser navigating off does.
func TestWithContextEndsTheRequestWhenTheClientLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		waits := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				w.WriteHeader(499)
			case <-time.After(2 * time.Second):
				w.WriteHeader(http.StatusOK)
			}
		})

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		testkit.NewClient(t, waits).WithContext(ctx).Get("/").AssertStatus(499)
	})
}

// A handler that panics with anything but http.ErrAbortHandler fails the
// test with the panic and its stack, and the response reads as cut off.
func TestAHandlerThatPanicsFailsTheTest(t *testing.T) {
	recording := testkit.Record(t)
	panicking := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))

		panic("nil map")
	})

	testkit.NewClient(recording, panicking).Get("/").AssertAborted().AssertBody("partial")

	recording.AssertFailed("the handler panicked: nil map\n")
	recording.AssertFailed("client_test.go:")
}

// deferredCleanups is a test whose cleanups run when the test says, so it
// can read what they recorded before it ends.
type deferredCleanups struct {
	testing.TB
	pending []func()
}

func (d *deferredCleanups) Cleanup(fn func()) { d.pending = append(d.pending, fn) }

func (d *deferredCleanups) end() {
	for i := len(d.pending) - 1; i >= 0; i-- {
		d.pending[i]()
	}
}

// A response cut off by its handler is a stream that panicked: a test that
// doesn't say it expects one (AssertAborted) fails when it ends, even if it
// checked nothing the abort changed.
func TestAResponseCutOffFailsTheTestUnlessItIsExpected(t *testing.T) {
	cases := []struct {
		name    string
		assert  func(r *testkit.TestResponse)
		failure string
	}{
		{"unexpected", func(r *testkit.TestResponse) { r.AssertOk() }, "GET /stream: the handler cut the response off"},
		{"expected", func(r *testkit.TestResponse) { r.AssertOk().AssertAborted() }, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test := &deferredCleanups{TB: t}
			recording := testkit.Record(test)

			tc.assert(testkit.NewClient(recording, cutOff("partial")).Get("/stream"))
			test.end()

			if tc.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(tc.failure)
			}
		})
	}
}
