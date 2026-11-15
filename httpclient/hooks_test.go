package httpclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

type orderedRead struct{}

func (orderedRead) Method() string   { return "GET" }
func (orderedRead) Endpoint() string { return "/info" }
func (orderedRead) OrderedQuery() QueryPairs {
	return QueryPairs{{"offerId", "a b"}, {"agencyCode", "x~y/z"}, {"cabin", "Any"}}
}

var errEnvelope = errors.New("envelope failure")

func TestConnectorHooksJudgeAndMapFailures(t *testing.T) {
	mock := NewMock(t, map[string]any{"orderedRead": JSON(200, map[string]any{"isSuccess": false})})
	client := New(Connector{
		BaseURL: "http://airline.test",
		HasRequestFailed: func(res *Response) bool {
			return res.Status >= 400 || string(res.Body()) != `{"isSuccess":true}`
		},
		MapError:   func(*Response) error { return errEnvelope },
		EncodeBody: func(any) ([]byte, error) { return []byte(`{}`), nil },
	}).WithMock(mock)

	res, err := client.Send(context.Background(), orderedRead{})
	if err != nil {
		t.Fatal(err)
	}

	if !res.Failed() || !errors.Is(res.Err(), errEnvelope) {
		t.Fatalf("a 200 failure envelope must fail with the mapped error: failed=%v err=%v", res.Failed(), res.Err())
	}

	if got := mock.Sent()[0].HTTP.URL.RawQuery; got != "offerId=a+b&agencyCode=x%7Ey%2Fz&cabin=Any" {
		t.Fatalf("query = %s, want pair order and form encoding", got)
	}
}

func TestPerRequestRetryInterval(t *testing.T) {
	var waited []time.Duration
	mock := NewMock(t, map[string]any{"orderedRead": Sequence{JSON(504, nil), JSON(200, map[string]any{})}})
	client := New(Connector{
		BaseURL: "http://airline.test",
		Retry:   func(_ Request, attempt int, _ *Response, _ error) bool { return attempt < 2 },
		RetryInterval: func(Request, int) time.Duration {
			waited = append(waited, time.Millisecond)

			return time.Millisecond
		},
	}).WithMock(mock)

	res, _ := client.Send(context.Background(), orderedRead{})
	if res.Status != 200 || len(waited) != 1 || len(mock.Sent()) != 2 {
		t.Fatalf("status %d, waits %v, sent %d", res.Status, waited, len(mock.Sent()))
	}
}
