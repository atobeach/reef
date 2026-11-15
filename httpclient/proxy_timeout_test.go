package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type slowRequest struct{ timeout time.Duration }

func (slowRequest) Method() string           { return "GET" }
func (slowRequest) Endpoint() string         { return "/slow" }
func (r slowRequest) Timeout() time.Duration { return r.timeout }

// A connector Proxy sends every request through the proxy (an absolute
// request URI).
func TestAConnectorProxyRoutesRequestsThroughIt(t *testing.T) {
	var proxied string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = r.RequestURI
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	proxyURL, err := ParseProxy(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	client := New(Connector{BaseURL: "http://api.example.invalid", Proxy: proxyURL})

	res, err := client.Send(context.Background(), getOffers{OfferID: "1"})
	if err != nil || res.Status != 204 {
		t.Fatalf("res = %v, err = %v", res, err)
	}

	if proxied != "http://api.example.invalid/offers?offerId=1" {
		t.Fatalf("proxied %q", proxied)
	}
}

// A proxy setting that can't be used is refused, never a silent direct
// connection, and the refusal never echoes its credentials.
func TestParseProxyRefusesWhatItCannotUse(t *testing.T) {
	cases := []struct {
		setting string
		want    string
	}{
		{"", ""},
		{"http://user:secret@proxy.internal:8080", "http://user:secret@proxy.internal:8080"},
		{"socks5://proxy.internal:1080", "socks5://proxy.internal:1080"},
		{"socks5h://proxy.internal:1080", "socks5h://proxy.internal:1080"},
		{"proxy.internal:8080", "error"},
		{"http://", "error"},
		{"ftp://proxy.internal:21", "error"},
		{"http://user:se cret@proxy.internal:8080", "error"},
	}

	for _, tc := range cases {
		proxy, err := ParseProxy(tc.setting)

		got := ""
		switch {
		case err != nil:
			got = "error"
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "proxy.internal") {
				t.Errorf("%q: the error echoes the setting: %v", tc.setting, err)
			}
		case proxy != nil:
			got = proxy.String()
		}

		if got != tc.want {
			t.Errorf("ParseProxy(%q) = %q, want %q", tc.setting, got, tc.want)
		}
	}
}

// A request's own Timeout overrides the connector's, both ways.
func TestARequestTimeoutOverridesTheConnectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(Connector{BaseURL: server.URL, Timeout: 20 * time.Millisecond})

	if _, err := client.Send(context.Background(), slowRequest{timeout: 2 * time.Second}); err != nil {
		t.Fatalf("a longer request timeout still timed out: %v", err)
	}

	if _, err := client.Send(context.Background(), slowRequest{}); err == nil {
		t.Fatal("the connector timeout no longer applies")
	}
}
