package web

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atobeach/reef/panics"
	"github.com/atobeach/reef/testkit"
)

// unwrapping hides the connection's Flusher behind Unwrap, as a
// hand-rolled middleware's writer does.
type unwrapping struct{ http.ResponseWriter }

func (u unwrapping) Unwrap() http.ResponseWriter { return u.ResponseWriter }

// opaque hides the connection's Flusher entirely.
type opaque struct{ http.ResponseWriter }

// through serves kernel with its writer wrapped by wrap.
func through(kernel http.Handler, wrap func(w http.ResponseWriter) http.ResponseWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kernel.ServeHTTP(wrap(w), r)
	})
}

// post sends a POST to the server's url and returns the open response,
// for the caller to close. A stream whose headers are held back fails it
// after 5s rather than hanging the test.
func post(t *testing.T, url string) *http.Response {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no response headers: %v", err)
	}

	return res
}

// Each event reaches the client as it is written, also through a writer
// that wraps the connection's and unwraps to it.
func TestStreamsEventsAsTheyAreProduced(t *testing.T) {
	cases := []struct {
		name string
		wrap func(w http.ResponseWriter) http.ResponseWriter
	}{
		{"on the connection's writer", func(w http.ResponseWriter) http.ResponseWriter { return w }},
		{"through a writer that unwraps to it", func(w http.ResponseWriter) http.ResponseWriter { return unwrapping{w} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			router := NewRouter()
			router.Post("/chat", func(*Context) (any, error) {
				return Stream("text/event-stream", func(s *StreamWriter) error {
					if err := s.Data(`{"type":"RUN_STARTED"}`); err != nil {
						return err
					}

					<-release

					return s.Data("[DONE]")
				}), nil
			})

			stamp := func(c *Context, next Handler) (any, error) {
				res, err := ToResponse(next(c))

				return res.WithHeader("X-Stamp", "yes"), err
			}
			kernel, _ := NewKernel(nil, KernelConfig{Router: router, Middleware: []Middleware{stamp}})
			server := httptest.NewServer(through(kernel, tc.wrap))
			defer server.Close()
			defer close(release)

			res := post(t, server.URL+"/chat")
			defer res.Body.Close()

			if res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("X-Stamp") != "yes" || res.Header.Get("Content-Length") != "" {
				t.Fatalf("headers = %v", res.Header)
			}

			reader := bufio.NewReader(res.Body)
			first := make(chan string, 1)
			go func() {
				line, _ := reader.ReadString('\n')
				first <- line
			}()

			select {
			case line := <-first:
				if line != "data: {\"type\":\"RUN_STARTED\"}\n" {
					t.Fatalf("first line = %q", line)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the first event was buffered")
			}
		})
	}
}

// A writer that can't flush is logged when the stream starts; the body
// still arrives whole, once the stream ends.
func TestAStreamThatCannotFlushIsLoggedAndStillSent(t *testing.T) {
	router := NewRouter()
	router.Post("/chat", func(*Context) (any, error) {
		return Stream("text/event-stream", func(s *StreamWriter) error {
			if err := s.Data("first"); err != nil {
				return err
			}

			return s.Data("[DONE]")
		}), nil
	})

	logs := &logRecorder{}
	kernel, _ := NewKernel(nil, KernelConfig{Router: router, Logger: logs.logger()})
	server := httptest.NewServer(through(kernel, func(w http.ResponseWriter) http.ResponseWriter { return opaque{w} }))
	defer server.Close()

	res := post(t, server.URL+"/chat")
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil || string(body) != "data: first\n\ndata: [DONE]\n\n" {
		t.Fatalf("body = %q (%v)", body, err)
	}

	if line := logs.line(t, "stream cannot flush"); line == nil || line["path"] != "chat" {
		t.Fatalf("logged %v, want stream cannot flush for chat", line)
	}
}

// A stream runs under the kernel's guard: a panic is logged with its stack,
// reported and cuts the connection, since its status is already out; an
// error while the client is there is reported; one after it left is not.
func TestAStreamsFailureIsReported(t *testing.T) {
	cases := []struct {
		name    string
		stream  func(s *StreamWriter) error
		leaves  bool
		aborted bool
		body    string
		report  string
	}{
		{"a panic after the first event", func(s *StreamWriter) error {
			_ = s.Data("first")
			panic("tool bug")
		}, false, true, "data: first\n\n", "panic: tool bug"},
		{"an error while the client is there", func(s *StreamWriter) error {
			_ = s.Data("first")

			return errors.New("encoding a frame")
		}, false, false, "data: first\n\n", "encoding a frame"},
		{"an error once the client left", func(s *StreamWriter) error { return s.Data("first") }, true, false, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			router.Get("/stream", func(*Context) (any, error) { return Stream("text/event-stream", tc.stream), nil })

			logs, reports := &logRecorder{}, &reportRecorder{}
			kernel, _ := NewKernel(nil, KernelConfig{Router: router, Logger: logs.logger(), Report: reports.report})

			client := testkit.NewClient(t, kernel)
			if tc.leaves {
				left, cancel := context.WithCancel(t.Context())
				cancel()
				client.WithContext(left)
			}

			res := client.Get("/stream").AssertOk().AssertBody(tc.body)
			if tc.aborted {
				res.AssertAborted()
			}

			if tc.report == "" {
				if len(reports.reported) != 0 {
					t.Fatalf("reported %v, want nothing", reports.reported)
				}

				return
			}

			if len(reports.reported) != 1 || reports.reported[0].Error() != tc.report {
				t.Fatalf("reported %v, want %q", reports.reported, tc.report)
			}
			if _, ok := errors.AsType[*panics.Error](reports.reported[0]); ok != tc.aborted {
				t.Fatalf("reported %v: a *panics.Error in the chain is %v, want %v", reports.reported[0], ok, tc.aborted)
			}

			_, stacked := logs.line(t, "unhandled error")["stack"]
			if stacked != tc.aborted {
				t.Fatalf("logged %v, want a stack only for the panic", logs.line(t, "unhandled error"))
			}
		})
	}
}

type streamKey struct{}

// The stream runs once the route's middleware returned, and its context is
// the request's as it is then, with what they bound.
func TestAStreamSeesWhatTheRouteMiddlewareBound(t *testing.T) {
	bind := func(c *Context, next Handler) (any, error) {
		c.WithContext(context.WithValue(c.Context(), streamKey{}, "bound"))

		return next(c)
	}

	router := NewRouter()
	router.Get("/stream", func(*Context) (any, error) {
		return Stream("text/plain", func(s *StreamWriter) error {
			value, _ := s.Context().Value(streamKey{}).(string)
			_, err := s.Write([]byte(value))

			return err
		}), nil
	}).Middleware(bind)

	kernel, _ := NewKernel(nil, KernelConfig{Router: router})

	if res := testkit.NewClient(t, kernel).Get("/stream").AssertOk(); res.Body() != "bound" {
		t.Fatalf("the stream read %q, want the bound value", res.Body())
	}
}

func TestStreamsStopWhenTheClientLeaves(t *testing.T) {
	stopped := make(chan struct{}, 1)
	router := NewRouter()
	router.Get("/forever", func(*Context) (any, error) {
		return Stream("text/event-stream", func(s *StreamWriter) error {
			for {
				if err := s.Data("tick"); err != nil {
					stopped <- struct{}{}

					return err
				}
				time.Sleep(5 * time.Millisecond)
			}
		}), nil
	})

	kernel, _ := NewKernel(nil, KernelConfig{Router: router})
	server := httptest.NewServer(kernel)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/forever", http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(res.Body).ReadString('\n')
	cancel()
	_ = res.Body.Close()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream kept producing after the client left")
	}
}
