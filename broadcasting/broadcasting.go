// Package broadcasting publishes ShouldBroadcast events to channels through
// a Broadcaster (the realtime server's Pusher HTTP API in production, so
// pusher-js clients receive them unchanged) and authorises private-channel
// subscriptions (Routes, Channels).
package broadcasting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"
)

// Channel is a channel an event goes out on.
type Channel struct {
	Name string
}

// NewChannel is a public channel: anyone may subscribe.
func NewChannel(name string) Channel {
	return Channel{Name: name}
}

// ShouldBroadcast is a broadcastable event. Publishing is never queued
// here: the events dispatcher publishes inline, after the listeners.
type ShouldBroadcast interface {
	BroadcastOn() []Channel
	// BroadcastAs is the event name clients listen for (".name" in a
	// Laravel Echo client).
	BroadcastAs() string
	// BroadcastWith is the payload: a struct, not a map, so clients see a
	// fixed key order.
	BroadcastWith() any
}

// Broadcaster publishes an event to channels.
type Broadcaster interface {
	Broadcast(ctx context.Context, channels []string, event string, payload any) error
}

// Broadcast publishes e on its channels; an event with none is a no-op.
func Broadcast(ctx context.Context, broadcaster Broadcaster, e ShouldBroadcast) error {
	channels := e.BroadcastOn()
	if len(channels) == 0 {
		return nil
	}

	names := make([]string, len(channels))
	for i, channel := range channels {
		names[i] = channel.Name
	}

	return broadcaster.Broadcast(ctx, names, e.BroadcastAs(), e.BroadcastWith())
}

// Null drops every broadcast.
type Null struct{}

// Broadcast implements Broadcaster.
func (Null) Broadcast(context.Context, []string, string, any) error { return nil }

// Log writes broadcasts to the log.
type Log struct{}

// Broadcast implements Broadcaster.
func (Log) Broadcast(ctx context.Context, channels []string, event string, payload any) error {
	body, err := EncodeASCIIJSON(payload)
	if err != nil {
		return fmt.Errorf("broadcasting: encoding %s payload: %w", event, err)
	}

	//nolint:sloglint // the log driver's output is this line, its event and channels in the message
	slog.InfoContext(ctx, fmt.Sprintf("Broadcasting [%s] on channels [%s] with payload:", event, strings.Join(channels, ", ")), "payload", string(body))

	return nil
}

// Sent is one recorded broadcast.
type Sent struct {
	Channels []string
	Event    string
	Payload  json.RawMessage
}

// Fake records broadcasts for broadcastingtest. Fail, when set, rejects a
// call, standing in for a realtime-server outage.
type Fake struct {
	mu   sync.Mutex
	sent []Sent
	Fail func(channels []string, event string) error
}

// Broadcast implements Broadcaster.
func (f *Fake) Broadcast(_ context.Context, channels []string, event string, payload any) error {
	if f.Fail != nil {
		if err := f.Fail(channels, event); err != nil {
			return err
		}
	}

	body, err := EncodeASCIIJSON(payload)
	if err != nil {
		return fmt.Errorf("broadcasting: encoding %s payload: %w", event, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, Sent{Channels: append([]string(nil), channels...), Event: event, Payload: body})

	return nil
}

// Sent is everything broadcast, in order.
func (f *Fake) Sent() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]Sent(nil), f.sent...)
}

// EncodeASCIIJSON is the wire encoding of every broadcast payload and
// realtime frame: slashes escaped, non-ASCII as `\uXXXX` and `<>&` as they
// are. The bytes are the contract: the HTTP API signs the body's md5, and
// clients were captured against them.
func EncodeASCIIJSON(v any) ([]byte, error) {
	var buf bytes.Buffer

	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(v); err != nil {
		return nil, err
	}

	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	var out bytes.Buffer
	out.Grow(len(raw))

	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)

		switch {
		case r == '/':
			// Only ever inside strings: JSON has no bare slashes.
			out.WriteString(`\/`)
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		case r >= 0x80:
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			out.Write(raw[:size])
		}

		raw = raw[size:]
	}

	return out.Bytes(), nil
}
