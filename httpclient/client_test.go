package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type searchFlights struct{ From, To string }

func (searchFlights) Method() string             { return "POST" }
func (searchFlights) Endpoint() string           { return "/flights/search" }
func (s searchFlights) Body() any                { return map[string]string{"from": s.From, "to": s.To} }
func (searchFlights) Headers() map[string]string { return map[string]string{"X-Request": "start"} }

type getOffers struct{ OfferID string }

func (getOffers) Method() string      { return "GET" }
func (getOffers) Endpoint() string    { return "offers" }
func (p getOffers) Query() url.Values { return url.Values{"offerId": {p.OfferID}} }

func TestSendsTypedRequestsThroughTheConnector(t *testing.T) {
	var seen *http.Request
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, body = r, must(io.ReadAll(r.Body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"offerId":"abc"}}`))
	}))
	defer server.Close()

	client := New(Connector{BaseURL: server.URL + "/api/", Headers: map[string]string{"x-api-key": "secret"}, Timeout: 5 * time.Second})

	res, err := client.Send(context.Background(), searchFlights{"LHR", "JFK"})
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Data struct {
			OfferID string `json:"offerId"`
		} `json:"data"`
	}
	if err := res.JSON(&decoded); err != nil || decoded.Data.OfferID != "abc" {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}

	if seen.URL.Path != "/api/flights/search" || seen.Header.Get("x-api-key") != "secret" || seen.Header.Get("X-Request") != "start" ||
		seen.Header.Get("Content-Type") != "application/json" || string(body) != `{"from":"LHR","to":"JFK"}` {
		t.Fatalf("request = %s %s %v %s", seen.Method, seen.URL, seen.Header, body)
	}
}

func TestRetriesOnlyWhatThePolicyAllows(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)

			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	// Re-send idempotent reads once on a gateway 5xx, the usual upstream policy.
	client := New(Connector{BaseURL: server.URL, Retry: func(req Request, attempt int, res *Response, _ error) bool {
		return req.Method() == "GET" && attempt < 2 && res != nil && res.Status >= 502
	}})

	res, err := client.Send(context.Background(), getOffers{"abc"})
	if err != nil || res.Status != 200 || calls != 2 {
		t.Fatalf("status %v, calls %d, err %v", res, calls, err)
	}

	calls = 0
	res, _ = client.Send(context.Background(), searchFlights{})
	if res.Status != http.StatusBadGateway || calls != 1 {
		t.Fatalf("a POST must not be re-sent: status %d, calls %d", res.Status, calls)
	}

	if err := res.Err(); !IsStatus(err, 502) {
		t.Fatalf("Err = %v", err)
	}
}

func TestHooksSeeEveryAttempt(t *testing.T) {
	var names []string
	client := New(Connector{BaseURL: "http://airline.test", Hooks: []Hook{func(_ context.Context, req Request, _ *Response, _ error, _ time.Duration) {
		names = append(names, Name(req))
	}}}).WithMock(NewMock(t, map[string]any{"getOffers": JSON(200, map[string]any{})}))

	_, _ = client.Send(context.Background(), getOffers{"x"})
	if len(names) != 1 || names[0] != "getOffers" {
		t.Fatalf("hooks saw %v", names)
	}
}

func TestTransportErrorsCanBeMocked(t *testing.T) {
	timeout := errors.New("i/o timeout")
	client := New(Connector{BaseURL: "http://airline.test"}).WithMock(NewMock(t, map[string]any{"getOffers": MockResponse{Err: timeout}}))

	if _, err := client.Send(context.Background(), getOffers{}); !errors.Is(err, timeout) {
		t.Fatalf("err = %v", err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}
