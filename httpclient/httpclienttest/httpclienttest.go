// Package httpclienttest asserts on what an httpclient.MockClient was sent,
// by request type. A failure lists the requests that did go out.
//
//	httpclienttest.AssertSentTimes[BookFlight](t, mock, 1)
//	httpclienttest.AssertNotSent[ChargeCard](t, mock)
//	httpclienttest.AssertSent(t, mock, func(s httpclient.Sent) bool { return s.HTTP.URL.Host == "api.example.com" })
package httpclienttest

import (
	"testing"

	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/testkit"
)

// Sent is every request of type R the mock received, in order. A value and
// a pointer to one count as the same type, as the mock answers them alike.
func Sent[R httpclient.Request](m *httpclient.MockClient) []httpclient.Sent {
	var zero R

	name := httpclient.Name(zero)

	var sent []httpclient.Sent
	for _, s := range m.Sent() {
		if httpclient.Name(s.Request) == name {
			sent = append(sent, s)
		}
	}

	return sent
}

// AssertSent fails unless some sent request satisfies match (its URL,
// headers or body).
func AssertSent(t testing.TB, m *httpclient.MockClient, match func(s httpclient.Sent) bool) {
	t.Helper()

	for _, s := range m.Sent() {
		if match(s) {
			return
		}
	}

	t.Fatalf("httpclient: no sent request matched the callback; sent: %s", summary(m))
}

// AssertSentTimes fails unless exactly n requests of type R were sent.
func AssertSentTimes[R httpclient.Request](t testing.TB, m *httpclient.MockClient, n int) {
	t.Helper()

	if got := len(Sent[R](m)); got != n {
		var zero R
		t.Fatalf("httpclient: %s sent %d times, want %d; sent: %s", httpclient.Name(zero), got, n, summary(m))
	}
}

// AssertNotSent fails if any request of type R was sent.
func AssertNotSent[R httpclient.Request](t testing.TB, m *httpclient.MockClient) {
	t.Helper()

	if got := len(Sent[R](m)); got > 0 {
		var zero R
		t.Fatalf("httpclient: %s sent %d times, want none; sent: %s", httpclient.Name(zero), got, summary(m))
	}
}

// AssertSentCount fails unless exactly n requests were sent, of any type.
func AssertSentCount(t testing.TB, m *httpclient.MockClient, n int) {
	t.Helper()

	if got := len(m.Sent()); got != n {
		t.Fatalf("httpclient: %d requests sent, want %d; sent: %s", got, n, summary(m))
	}
}

// AssertNothingSent fails if any request was sent.
func AssertNothingSent(t testing.TB, m *httpclient.MockClient) {
	t.Helper()

	if sent := m.Sent(); len(sent) > 0 {
		t.Fatalf("httpclient: %d requests sent, want none; sent: %s", len(sent), summary(m))
	}
}

// summary names what the mock was sent.
func summary(m *httpclient.MockClient) string {
	sent := m.Sent()

	names := make([]string, len(sent))
	for i, s := range sent {
		names[i] = httpclient.Name(s.Request)
	}

	return testkit.Summary(names)
}
