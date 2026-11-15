package realtime

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/atobeach/reef/clock"
)

// Server is the Pusher-protocol WebSocket server and HTTP API, an
// http.Handler whose maintenance loop is Run.
type Server struct {
	cfg     Config
	origins []*regexp.Regexp
	anyOrig bool
	fanout  Fanout

	mu       sync.RWMutex
	conns    map[string]*conn
	channels map[string]map[*conn]struct{}
	closing  bool
	active   sync.WaitGroup
}

// NewServer builds a server; unset limits take their defaults. A nil
// fanout delivers in-process (one instance).
func NewServer(cfg Config, fanout Fanout) *Server {
	cfg = cfg.withDefaults()

	s := &Server{
		cfg:      cfg,
		fanout:   fanout,
		conns:    map[string]*conn{},
		channels: map[string]map[*conn]struct{}{},
	}

	for _, pattern := range cfg.App.AllowedOrigins {
		if pattern == "*" {
			s.anyOrig = true

			continue
		}

		// `*` matches anything (dots included), the rest literally.
		expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
		s.origins = append(s.origins, regexp.MustCompile(expression))
	}

	return s
}

// Run pings inactive connections and prunes stale ones every CheckInterval
// and delivers what other instances publish, until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	if s.fanout != nil {
		go func() {
			if err := s.fanout.Subscribe(ctx, s.deliver); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "realtime: fan-out subscription ended", "error", err)
			}
		}()
	}

	ticker := time.NewTicker(s.cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.checkConnections(ctx)
		}
	}
}

// Close disconnects every client with 1001, so pusher-js reconnects to another
// instance, and waits up to timeout for them to go. Each client first
// gets the frame being written and those queued for it (each within
// WriteTimeout): a write cut short drops the connection without its close
// frame.
func (s *Server) Close(timeout time.Duration) {
	s.mu.Lock()
	s.closing = true
	for _, c := range s.conns {
		c.terminate(websocket.StatusGoingAway, "Server shutting down", true)
	}
	s.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		s.active.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(timeout):
	}
}

// Connections is the number of open connections.
func (s *Server) Connections() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.conns)
}

// Publish sends m to every subscriber, across instances when a Fanout is set.
func (s *Server) Publish(ctx context.Context, m Message) error {
	if s.fanout != nil {
		return s.fanout.Publish(ctx, m)
	}

	s.deliver(m)

	return nil
}

// deliver hands each channel's subscribers the frame naming that channel.
// Enqueueing never blocks, so one slow client can't hold up the rest.
func (s *Server) deliver(m Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, channel := range m.Channels {
		subscribers := s.channels[channel]
		if len(subscribers) == 0 {
			continue
		}

		frame := m.channelFrame(channel)
		for c := range subscribers {
			if c.id == m.ExceptSocketID {
				continue
			}

			c.enqueue(frame)
		}
	}
}

// checkConnections pings connections silent for a ping interval and prunes
// those still silent one check after their ping (4201).
func (s *Server) checkConnections(ctx context.Context) {
	now := clock.Now(ctx)

	s.mu.RLock()
	conns := make([]*conn, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.RUnlock()

	for _, c := range conns {
		if !c.inactive(now, s.cfg.App.PingInterval) {
			continue
		}

		if c.pinged.Load() {
			c.enqueue(errorFrame(errPongTimeout))
			c.terminate(closePongTimeout, errPongTimeout.Message, true)

			continue
		}

		// coder/websocket answers control pings without surfacing them, so
		// only pusher:ping/pong are tracked.
		c.enqueue(eventFrame("pusher:ping"))
		c.pinged.Store(true)
	}
}
