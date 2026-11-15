package web

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// StreamWriter writes a streamed body and flushes it to the client.
type StreamWriter struct {
	ctx context.Context
	w   io.Writer
	rc  *http.ResponseController
}

// Context is cancelled when the client disconnects — stop producing then.
func (s *StreamWriter) Context() context.Context {
	return s.ctx
}

// Write writes raw bytes (without flushing).
func (s *StreamWriter) Write(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}

	return s.w.Write(p)
}

// Flush pushes everything written so far to the client.
func (s *StreamWriter) Flush() {
	// A writer that can't flush was logged when the stream started, and a
	// gone client fails the next Write.
	_ = s.rc.Flush()
}

// Data writes one SSE event, `data: <payload>\n\n`, and flushes it. A
// multi-line payload becomes one `data:` line per line, per the SSE spec.
func (s *StreamWriter) Data(payload string) error {
	var event strings.Builder
	for line := range strings.SplitSeq(payload, "\n") {
		event.WriteString("data: " + line + "\n")
	}
	event.WriteString("\n")

	if _, err := s.Write([]byte(event.String())); err != nil {
		return err
	}

	s.Flush()

	return nil
}

// Stream is a streamed response, such as server-sent events. Status and
// headers go out when fn starts, so middleware can still decorate them;
// after that, fn's error ends the stream and is reported; a panic is
// reported and cuts the connection. Flushing reaches through writers that
// wrap the connection's (an Unwrap method).
//
//	return web.Stream("text/event-stream", func(s *web.StreamWriter) error {
//		return s.Data(`{"type":"RUN_STARTED"}`)
//	}), nil
func Stream(contentType string, fn func(s *StreamWriter) error) *Response {
	res := &Response{status: http.StatusOK, header: http.Header{}, stream: fn}
	res.header.Set("Content-Type", contentType)
	res.header.Set("Cache-Control", "no-cache, private")
	// Tell nginx-style proxies not to buffer the stream.
	res.header.Set("X-Accel-Buffering", "no")

	return res
}
