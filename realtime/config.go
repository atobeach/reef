// Package realtime is a WebSocket server speaking the Pusher Channels
// protocol (v7) and HTTP API byte for byte, so unmodified pusher-js clients
// and Pusher publishers work against it. One process holds every
// connection; a Fanout carries publishes between several.
package realtime

import (
	"cmp"
	"strings"
	"time"
)

// Config is the server's listen address and limits plus its one app.
type Config struct {
	// Host and Port are where the server listens; the host defaults to
	// loopback, never 0.0.0.0.
	Host string
	Port int
	// Path prefixes every route.
	Path string
	// MaxRequestSize caps an HTTP API request body.
	MaxRequestSize int64
	App            App

	// SendBuffer is how many frames may queue for one connection before it
	// is closed as a slow consumer, so a stalled client can't grow memory.
	SendBuffer int
	// WriteTimeout bounds one frame write; a connection that can't take a
	// frame in this long is closed.
	WriteTimeout time.Duration
	// MaxSubscriptions caps the channels one connection may join, so one
	// client can't mint unbounded channel entries.
	MaxSubscriptions int
	// CheckInterval is how often inactive connections are pinged and stale
	// ones pruned.
	CheckInterval time.Duration
}

// App is the one Pusher app the server accepts: its credentials and
// per-app limits.
type App struct {
	ID     string
	Key    string
	Secret string
	// AllowedOrigins are host patterns an Origin header must match, `*`
	// matching any run of characters (alone, any origin).
	AllowedOrigins []string
	// PingInterval is how long a connection may stay silent before the
	// server pings it; one still silent a check later is pruned.
	PingInterval time.Duration
	// ActivityTimeout is what connection_established tells the client: it
	// pings after this much silence.
	ActivityTimeout time.Duration
	// MaxConnections caps concurrent connections; 0 means no limit.
	MaxConnections int
	// MaxMessageSize caps one client frame in bytes; bigger closes 1009.
	MaxMessageSize int64
	// AcceptClientEventsFrom is "members" (the channel's subscribers),
	// "all", or anything else to refuse client events.
	AcceptClientEventsFrom string
	RateLimit              RateLimit
}

// RateLimit caps client messages per connection per window.
type RateLimit struct {
	Enabled     bool
	MaxAttempts int
	Decay       time.Duration
	// Terminate closes the connection on the limit instead of only
	// answering 4301.
	Terminate bool
}

// withDefaults fills every unset field, so a Config with only the app's
// credentials is a working loopback server.
func (c Config) withDefaults() Config {
	c.Host = cmp.Or(c.Host, "127.0.0.1")
	c.Port = cmp.Or(c.Port, 8080)
	c.Path = strings.TrimRight(c.Path, "/")
	c.MaxRequestSize = positiveOr(c.MaxRequestSize, 10_000)
	c.SendBuffer = positiveOr(c.SendBuffer, 128)
	c.WriteTimeout = positiveOr(c.WriteTimeout, 10*time.Second)
	c.MaxSubscriptions = positiveOr(c.MaxSubscriptions, 100)
	c.CheckInterval = positiveOr(c.CheckInterval, 60*time.Second)

	if len(c.App.AllowedOrigins) == 0 {
		c.App.AllowedOrigins = []string{"*"}
	}

	c.App.PingInterval = positiveOr(c.App.PingInterval, 60*time.Second)
	c.App.ActivityTimeout = positiveOr(c.App.ActivityTimeout, 30*time.Second)
	c.App.MaxMessageSize = positiveOr(c.App.MaxMessageSize, 10_000)
	c.App.AcceptClientEventsFrom = cmp.Or(c.App.AcceptClientEventsFrom, "members")
	c.App.RateLimit.MaxAttempts = positiveOr(c.App.RateLimit.MaxAttempts, 60)
	c.App.RateLimit.Decay = positiveOr(c.App.RateLimit.Decay, 60*time.Second)

	return c
}

// positiveOr is value, or fallback when value is unset (zero or negative).
func positiveOr[T int | int64 | time.Duration](value, fallback T) T {
	if value <= 0 {
		return fallback
	}

	return value
}
