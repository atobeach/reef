package realtime

import (
	"context"
	"crypto/subtle"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/atobeach/reef/clock"
)

// serveSocket upgrades, checks the app key, origin and quota, then reads
// client frames until the connection ends.
func (s *Server) serveSocket(w http.ResponseWriter, r *http.Request, key string) {
	// Origins are checked after the upgrade so the client gets a Pusher
	// error (and a close code pusher-js won't retry) rather than a bare 403.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}

	ctx := r.Context()

	if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.App.Key)) != 1 {
		s.refuse(ctx, ws, errApplicationMissing)

		return
	}

	if !s.originAllowed(r.Header.Get("Origin")) {
		s.refuse(ctx, ws, errOriginNotAllowed)

		return
	}

	ws.SetReadLimit(s.cfg.App.MaxMessageSize)

	c, refusal := s.register(ctx, ws)
	if c == nil {
		s.refuse(ctx, ws, refusal)

		return
	}
	defer s.active.Done()

	go c.writeLoop(s.cfg.WriteTimeout) //nolint:contextcheck // writes run on c.writeCtx, derived from the request in newConn

	c.enqueue(establishedFrame(c.id, int(s.cfg.App.ActivityTimeout/time.Second)))

	s.readLoop(ctx, c)

	s.unregister(c)
	c.terminate(websocket.StatusNormalClosure, "", false)
	<-c.writerDone
}

// readLoop handles c's frames until the socket fails or c is terminated.
func (s *Server) readLoop(ctx context.Context, c *conn) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		// A terminated connection stops its own read too.
		select {
		case <-c.done:
		case <-readCtx.Done():
		}
		<-c.writerDone
		cancel()
	}()

	for {
		_, data, err := c.ws.Read(readCtx)
		if err != nil {
			return
		}

		c.touch(clock.Now(ctx))
		s.message(ctx, c, data)
	}
}

// refuse sends a Pusher error and closes with its 40xx code, so pusher-js
// gives up rather than hammering the server.
func (s *Server) refuse(ctx context.Context, ws *websocket.Conn, e pusherError) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.WriteTimeout)
	defer cancel()

	_ = ws.Write(ctx, websocket.MessageText, errorFrame(e))
	_ = ws.Close(websocket.StatusCode(e.Code), e.Message)
}

// originAllowed accepts any origin under `*`, else an Origin whose host
// matches one pattern.
func (s *Server) originAllowed(origin string) bool {
	if s.anyOrig {
		return true
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" {
		return false
	}

	for _, pattern := range s.origins {
		if pattern.MatchString(parsed.Hostname()) {
			return true
		}
	}

	return false
}

// register adds a connection within the quota; a nil conn comes with the
// error to refuse it with.
func (s *Server) register(ctx context.Context, ws *websocket.Conn) (*conn, pusherError) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closing {
		return nil, pusherError{Code: int(closeReconnect), Message: "Server shutting down"}
	}

	if s.cfg.App.MaxConnections > 0 && len(s.conns) >= s.cfg.App.MaxConnections {
		return nil, errOverQuota
	}

	id := newSocketID()
	for s.conns[id] != nil {
		id = newSocketID()
	}

	c := newConn(ctx, id, ws, s.cfg.SendBuffer, clock.Now(ctx))
	s.conns[id] = c
	s.active.Add(1)

	return c, pusherError{}
}

// unregister unsubscribes c from everything and forgets it.
func (s *Server) unregister(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for channel := range c.subscriptions {
		s.leave(c, channel)
	}

	delete(s.conns, c.id)
}

// newSocketID is a Pusher socket id, `\d+\.\d+` as publishers validate it.
func newSocketID() string {
	//nolint:gosec // G404: a socket id is public (the client echoes it to auth endpoints); it only has to be unique, and register retries collisions
	return strconv.Itoa(rand.IntN(1_000_000_000)+1) + "." + strconv.Itoa(rand.IntN(1_000_000_000)+1)
}
