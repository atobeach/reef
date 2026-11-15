package httpclient

import (
	"context"
	"testing"
)

type sqlQuery struct{ SQL string }

func (sqlQuery) Method() string   { return "POST" }
func (sqlQuery) Endpoint() string { return "/" }
func (q sqlQuery) RawBody() (body []byte, contentType string) {
	return []byte(q.SQL), "text/plain"
}

func TestRawBodiesKeepTheirContentType(t *testing.T) {
	mock := NewMock(t, map[string]any{})
	mock.Add("sqlQuery", JSON(200, map[string]any{"data": []any{}}))
	client := New(Connector{BaseURL: "http://sql.example.test"}).WithMock(mock)

	if _, err := client.Send(context.Background(), sqlQuery{"SELECT 1"}); err != nil {
		t.Fatal(err)
	}

	if sent := mock.Sent()[0]; string(sent.Body) != "SELECT 1" || sent.HTTP.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("sent %s as %q", sent.Body, sent.HTTP.Header.Get("Content-Type"))
	}
}
