package realtime

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/coder/websocket"

	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/clock"
)

// clientMessage is a frame from the client.
type clientMessage struct {
	Event   *string         `json:"event"`
	Data    json.RawMessage `json:"data"`
	Channel json.RawMessage `json:"channel"`
}

// message handles one client frame: rate limit first, then a `pusher:`
// protocol event or a client event.
func (s *Server) message(ctx context.Context, c *conn, raw []byte) {
	if limit := s.cfg.App.RateLimit; limit.Enabled && !c.allow(clock.Now(ctx), limit) {
		c.enqueue(errorFrame(errRateLimited))

		if limit.Terminate {
			c.terminate(websocket.StatusCode(errRateLimited.Code), errRateLimited.Message, true)
		}

		return
	}

	var msg clientMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Event == nil || *msg.Event == "" {
		c.enqueue(errorFrame(errInvalidMessage))

		return
	}

	data := unwrapJSONString(msg.Data)

	if event, ok := strings.CutPrefix(*msg.Event, "pusher:"); ok {
		s.pusherEvent(c, event, data)

		return
	}

	s.clientEvent(ctx, c, *msg.Event, msg.Channel, data)
}

// unwrapJSONString reads a data string holding JSON as that JSON, since
// pusher-js sends `data` either inline or as a string of JSON.
func unwrapJSONString(data json.RawMessage) json.RawMessage {
	var inner string
	if json.Unmarshal(data, &inner) == nil && json.Valid([]byte(inner)) {
		return json.RawMessage(inner)
	}

	return data
}

// subscription is pusher:subscribe's data.
type subscription struct {
	Channel     *string `json:"channel"`
	Auth        *string `json:"auth"`
	ChannelData *string `json:"channel_data"`
}

// pusherEvent handles a `pusher:` protocol event. Anything malformed or
// unknown is the catch-all 4200.
func (s *Server) pusherEvent(c *conn, event string, data json.RawMessage) {
	switch event {
	case "ping":
		c.enqueue(eventFrame("pusher:pong"))
	case "pong":
		// touch() already ran for the message.
	case "subscribe", "unsubscribe":
		var payload subscription
		if json.Unmarshal(data, &payload) != nil || payload.Channel == nil {
			c.enqueue(errorFrame(errInvalidMessage))

			return
		}

		if event == "unsubscribe" {
			s.mu.Lock()
			s.leave(c, *payload.Channel)
			s.mu.Unlock()

			return
		}

		s.subscribe(c, *payload.Channel, payload.Auth, payload.ChannelData)
	default:
		c.enqueue(errorFrame(errInvalidMessage))
	}
}

// subscribe joins c to channel once admitted, within the per-connection
// subscription cap; resubscribing is a no-op that still succeeds.
func (s *Server) subscribe(c *conn, channel string, auth *string, channelData *string) {
	if refusal, ok := s.admit(c.id, channel, auth, channelData); !ok {
		c.enqueue(errorFrame(refusal))

		return
	}

	s.mu.Lock()
	_, already := c.subscriptions[channel]
	if !already && len(c.subscriptions) >= s.cfg.MaxSubscriptions {
		s.mu.Unlock()
		c.enqueue(errorFrame(errTooManyChannels))

		return
	}

	subscribers := s.channels[channel]
	if subscribers == nil {
		subscribers = map[*conn]struct{}{}
		s.channels[channel] = subscribers
	}

	subscribers[c] = struct{}{}
	c.subscriptions[channel] = struct{}{}
	s.mu.Unlock()

	c.enqueue(subscribedFrame(channel))
}

// admit decides whether socket may join channel: public channels for
// anyone, `private…` with a valid auth; presence and cache channels are
// refused.
func (s *Server) admit(socket string, channel string, auth *string, channelData *string) (pusherError, bool) {
	if !broadcasting.ValidChannelName(channel) {
		return errInvalidMessage, false
	}

	if channelData != nil && *channelData != "" && !json.Valid([]byte(*channelData)) {
		return errInvalidMessage, false
	}

	switch {
	case strings.HasPrefix(channel, "private-cache-"), strings.HasPrefix(channel, "presence-cache-"), strings.HasPrefix(channel, "cache"):
		return errCacheUnsupported, false
	case strings.HasPrefix(channel, "private"):
		if !s.authorized(socket, channel, auth, channelData) {
			return errUnauthorized, false
		}
	case strings.HasPrefix(channel, "presence"):
		return errPresenceUnsupported, false
	}

	return pusherError{}, true
}

// authorized checks a private subscription's `key:signature` auth in
// constant time; the key must be this app's.
func (s *Server) authorized(socket string, channel string, auth *string, channelData *string) bool {
	if auth == nil {
		return false
	}

	key, signature, ok := strings.Cut(*auth, ":")
	if !ok || key != s.cfg.App.Key {
		return false
	}

	data := ""
	if channelData != nil {
		data = *channelData
	}

	expected := broadcasting.ChannelSignature(s.cfg.App.Secret, socket, channel, data)

	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// leave drops c from channel, removing the channel once empty. Callers
// hold s.mu.
func (s *Server) leave(c *conn, channel string) {
	delete(c.subscriptions, channel)

	subscribers := s.channels[channel]
	if subscribers == nil {
		return
	}

	delete(subscribers, c)
	if len(subscribers) == 0 {
		delete(s.channels, channel)
	}
}

// clientEvent whispers a `client-` event to the channel's other
// subscribers when the app accepts client events; other event names are
// dropped silently.
func (s *Server) clientEvent(ctx context.Context, c *conn, event string, channelRaw json.RawMessage, data json.RawMessage) {
	var channel string
	if json.Unmarshal(channelRaw, &channel) != nil || channel == "" || !validClientData(data) {
		c.enqueue(errorFrame(errInvalidMessage))

		return
	}

	if !strings.HasPrefix(event, "client-") {
		return
	}

	mode := s.cfg.App.AcceptClientEventsFrom
	if mode != "all" && mode != "members" {
		c.enqueue(errorFrame(errClientEventsOff))

		return
	}

	if mode == "members" {
		s.mu.RLock()
		_, member := c.subscriptions[channel]
		s.mu.RUnlock()

		if !member {
			c.enqueue(errorFrame(errNotMember))

			return
		}
	}

	if len(data) == 0 {
		data = json.RawMessage("null")
	}

	if err := s.Publish(ctx, Message{Channels: []string{channel}, Event: event, ClientData: data, ExceptSocketID: c.id}); err != nil {
		slog.ErrorContext(ctx, "realtime: publishing a client event failed", "event", event, "channel", channel, "error", err)
	}
}

// validClientData accepts a client event's data when absent, null, an
// object or a list.
func validClientData(data json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(data))

	return trimmed == "" || trimmed == "null" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}
