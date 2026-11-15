package httpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// MockResponse is a canned reply.
type MockResponse struct {
	Status int
	Body   []byte
	Header http.Header
	// Err simulates a transport failure (timeout, reset) instead of a reply.
	Err error
}

// JSON is a JSON mock reply from a Go value or a json.RawMessage.
func JSON(status int, body any) MockResponse {
	raw, ok := body.(json.RawMessage)
	if !ok {
		encoded, err := json.Marshal(body)
		if err != nil {
			panic(fmt.Sprintf("httpclient: mock body: %v", err))
		}
		raw = encoded
	}

	return MockResponse{Status: status, Body: raw, Header: http.Header{"Content-Type": {"application/json"}}}
}

// Sequence replies with each response in turn, repeating the last, for
// polls that progress.
type Sequence []MockResponse

// Sent is a request the mock received.
type Sent struct {
	Request Request
	HTTP    *http.Request
	Body    []byte
}

// MockClient answers by request type name (see Name); an unmocked request
// fails the test instead of reaching the network.
type MockClient struct {
	t          TestingT
	mu         sync.Mutex
	responses  map[string]Sequence
	responders map[string]Responder
	served     map[string]int
	sent       []Sent
}

// Responder answers a request from its content, for a request type whose
// reply depends on its parameters.
type Responder func(sent Sent) MockResponse

// TestingT is the part of testing.TB the mock fails through, so the testing
// package never links into a production binary.
type TestingT interface {
	Helper()
	Errorf(format string, args ...any)
}

// NewMock builds a mock from request names to replies (a MockResponse, a
// Sequence of them, or a Responder).
func NewMock(t TestingT, responses map[string]any) *MockClient {
	t.Helper()

	m := &MockClient{t: t, responses: map[string]Sequence{}, responders: map[string]Responder{}, served: map[string]int{}}
	for name, reply := range responses {
		m.set(name, reply)
	}

	return m
}

// Add sets or replaces the reply for a request type mid-test; a Sequence
// starts over.
func (m *MockClient) Add(name string, reply any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.set(name, reply)
}

// set installs reply for name, replacing whatever answered it before.
func (m *MockClient) set(name string, reply any) {
	delete(m.responders, name)
	switch r := reply.(type) {
	case MockResponse:
		m.responses[name] = Sequence{r}
	case Sequence:
		m.responses[name] = r
	case Responder:
		delete(m.responses, name)
		m.responders[name] = r
	default:
		panic(fmt.Sprintf("httpclient: mock for %s must be a MockResponse, Sequence or Responder, got %T", name, reply))
	}

	m.served[name] = 0
}

// respond records the request and answers it from the mocks.
func (m *MockClient) respond(c *Client, req Request, httpReq *http.Request, body []byte) (*Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := Name(req)
	sent := Sent{Request: req, HTTP: httpReq, Body: body}
	m.sent = append(m.sent, sent)

	var reply MockResponse
	if responder, ok := m.responders[name]; ok {
		reply = responder(sent)
	} else {
		sequence, ok := m.responses[name]
		if !ok || len(sequence) == 0 {
			m.t.Errorf("httpclient: no mock for %s %s (%s)", req.Method(), httpReq.URL, name)

			return nil, fmt.Errorf("httpclient: unmocked request %s", name)
		}

		index := min(m.served[name], len(sequence)-1)
		m.served[name]++
		reply = sequence[index]
	}

	if reply.Err != nil {
		return nil, reply.Err
	}

	header := reply.Header
	if header == nil {
		header = http.Header{}
	}

	return c.response(reply.Status, header, reply.Body, req), nil
}

// Sent lists every request received, in order.
func (m *MockClient) Sent() []Sent {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]Sent(nil), m.sent...)
}
