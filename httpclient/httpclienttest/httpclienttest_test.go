package httpclienttest_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/httpclient/httpclienttest"
	"github.com/atobeach/reef/testkit"
)

type searchFlights struct{ From, To string }

func (searchFlights) Method() string   { return "POST" }
func (searchFlights) Endpoint() string { return "/flights/search" }

type getOffers struct{ OfferID string }

func (getOffers) Method() string      { return "GET" }
func (getOffers) Endpoint() string    { return "/offers" }
func (p getOffers) Query() url.Values { return url.Values{"offerId": {p.OfferID}} }

type bookFlight struct{}

func (bookFlight) Method() string   { return "POST" }
func (bookFlight) Endpoint() string { return "/flights/book" }

// sent is a mock that was sent one search and three polls, one of them as a
// pointer, as a controller holding the request by address sends it.
func sent(t *testing.T) *httpclient.MockClient {
	t.Helper()

	mock := httpclient.NewMock(t, map[string]any{
		"searchFlights": httpclient.JSON(200, map[string]any{"offerId": "abc"}),
		"getOffers":     httpclient.JSON(200, map[string]any{}),
	})
	client := httpclient.New(httpclient.Connector{BaseURL: "http://flights.test"}).WithMock(mock)

	for _, req := range []httpclient.Request{searchFlights{"LHR", "JFK"}, getOffers{"abc"}, &getOffers{"abc"}, getOffers{"abc"}} {
		if _, err := client.Send(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}

	return mock
}

func TestMocksAnswerByRequestTypeInSequence(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{
		"getOffers": httpclient.Sequence{httpclient.JSON(200, map[string]string{"status": "pending"}), httpclient.JSON(200, map[string]string{"status": "complete"})},
	})
	client := httpclient.New(httpclient.Connector{BaseURL: "http://flights.test"}).WithMock(mock)

	var statuses []string
	for range 3 {
		res, err := client.Send(context.Background(), getOffers{"abc"})
		if err != nil {
			t.Fatal(err)
		}

		var body map[string]string
		_ = json.Unmarshal(res.Body(), &body)
		statuses = append(statuses, body["status"])
	}

	if strings.Join(statuses, ",") != "pending,complete,complete" {
		t.Fatalf("sequence = %v, want the last reply repeated", statuses)
	}
}

func TestAnUnmockedRequestFailsTheTest(t *testing.T) {
	recording := testkit.Record(t)
	client := httpclient.New(httpclient.Connector{BaseURL: "http://flights.test"}).WithMock(httpclient.NewMock(recording, map[string]any{}))

	if _, err := client.Send(context.Background(), bookFlight{}); err == nil {
		t.Fatal("an unmocked request must not succeed")
	}

	recording.AssertFailed("no mock for POST http://flights.test/flights/book (bookFlight)")
}

func TestSentFiltersByTypeCountingValuesAndPointersAlike(t *testing.T) {
	mock := sent(t)

	if polls := httpclienttest.Sent[getOffers](mock); len(polls) != 3 {
		t.Fatalf("polls = %d, want 3", len(polls))
	}

	if polls := httpclienttest.Sent[*getOffers](mock); len(polls) != 3 {
		t.Fatalf("polls by pointer type = %d, want 3", len(polls))
	}
}

func TestAssertionsPassOnWhatWasSentAndFailListingIt(t *testing.T) {
	cases := []struct {
		name   string
		assert func(t *testkit.RecordingT, m *httpclient.MockClient)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"times", func(t *testkit.RecordingT, m *httpclient.MockClient) {
			httpclienttest.AssertSentTimes[getOffers](t, m, 3)
		}, ""},
		{"times off by one", func(t *testkit.RecordingT, m *httpclient.MockClient) {
			httpclienttest.AssertSentTimes[getOffers](t, m, 2)
		}, "getOffers sent 3 times, want 2; sent: searchFlights, getOffers ×3"},
		{"not sent", func(t *testkit.RecordingT, m *httpclient.MockClient) { httpclienttest.AssertNotSent[bookFlight](t, m) }, ""},
		{"not sent but was", func(t *testkit.RecordingT, m *httpclient.MockClient) {
			httpclienttest.AssertNotSent[searchFlights](t, m)
		}, "searchFlights sent 1 times, want none"},
		{"count", func(t *testkit.RecordingT, m *httpclient.MockClient) { httpclienttest.AssertSentCount(t, m, 4) }, ""},
		{"count off", func(t *testkit.RecordingT, m *httpclient.MockClient) { httpclienttest.AssertSentCount(t, m, 1) }, "4 requests sent, want 1"},
		{"nothing sent", func(t *testkit.RecordingT, m *httpclient.MockClient) { httpclienttest.AssertNothingSent(t, m) }, "4 requests sent, want none; sent: searchFlights, getOffers ×3"},
		{"matched", func(t *testkit.RecordingT, m *httpclient.MockClient) {
			httpclienttest.AssertSent(t, m, func(s httpclient.Sent) bool { return s.HTTP.URL.Query().Get("offerId") == "abc" })
		}, ""},
		{"unmatched", func(t *testkit.RecordingT, m *httpclient.MockClient) {
			httpclienttest.AssertSent(t, m, func(s httpclient.Sent) bool { return s.HTTP.URL.Path == "/flights/book" })
		}, "no sent request matched the callback; sent: searchFlights, getOffers ×3"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recording := testkit.Record(t)
			c.assert(recording, sent(t))

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}

func TestAssertNothingSentPassesOnAnUntouchedMock(t *testing.T) {
	recording := testkit.Record(t)
	httpclienttest.AssertNothingSent(recording, httpclient.NewMock(t, map[string]any{}))

	recording.AssertPassed()
}
