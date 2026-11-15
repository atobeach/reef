package realtime

import (
	"encoding/json"
	"fmt"

	"github.com/atobeach/reef/broadcasting"
)

// pusherError is a `pusher:error` the server sends, its code per the
// Pusher protocol.
type pusherError struct {
	Code    int
	Message string
}

// The errors the server sends; clients match on the messages verbatim.
var (
	errApplicationMissing  = pusherError{4001, "Application does not exist"}
	errOverQuota           = pusherError{4004, "Application is over connection quota"}
	errUnauthorized        = pusherError{4009, "Connection is unauthorized"}
	errOriginNotAllowed    = pusherError{4009, "Origin not allowed"}
	errNotMember           = pusherError{4009, "The client is not a member of the specified channel."}
	errInvalidMessage      = pusherError{4200, "Invalid message format"}
	errPongTimeout         = pusherError{4201, "Pong reply not received in time"}
	errClientEventsOff     = pusherError{4301, "The app does not have client messaging enabled."}
	errRateLimited         = pusherError{4301, "Rate limit exceeded"}
	errPresenceUnsupported = pusherError{4009, "Presence channels are not supported"}
	errCacheUnsupported    = pusherError{4009, "Cache channels are not supported"}
	errTooManyChannels     = pusherError{4100, "Too many subscriptions"}
)

// The close codes pusher-js acts on: 4000-4099 stop reconnecting, 4100-4199
// reconnect after a back-off, 4200-4299 reconnect at once.
const (
	closeOverCapacity = 4100
	closeReconnect    = 4200
	closePongTimeout  = 4201
)

// frame encodes a server frame in the publishers' wire encoding.
func frame(v any) []byte {
	body, err := broadcasting.EncodeASCIIJSON(v)
	if err != nil {
		// Only strings and RawMessages built here are encoded.
		panic(fmt.Sprintf("realtime: encoding frame: %v", err))
	}

	return body
}

// jsonString is v encoded, to be wrapped again as a JSON string: Pusher
// frames carry `data` as a string of JSON.
func jsonString(v any) string {
	return string(frame(v))
}

// errorFrame is `{"event":"pusher:error","data":"{\"code\":…,\"message\":…}"}`.
func errorFrame(e pusherError) []byte {
	type body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}

	type payload struct {
		Event string `json:"event"`
		Data  string `json:"data"`
	}

	return frame(payload{Event: "pusher:error", Data: jsonString(body(e))})
}

// eventFrame is an event with no channel or data: `{"event":"pusher:pong"}`.
func eventFrame(event string) []byte {
	type payload struct {
		Event string `json:"event"`
	}

	return frame(payload{Event: event})
}

// establishedFrame is pusher:connection_established, activity_timeout in
// seconds.
func establishedFrame(socketID string, activityTimeout int) []byte {
	type body struct {
		SocketID        string `json:"socket_id"`
		ActivityTimeout int    `json:"activity_timeout"`
	}

	type payload struct {
		Event string `json:"event"`
		Data  string `json:"data"`
	}

	return frame(payload{Event: "pusher:connection_established", Data: jsonString(body{SocketID: socketID, ActivityTimeout: activityTimeout})})
}

// subscribedFrame is pusher_internal:subscription_succeeded; data is `{}`
// for public and private channels.
func subscribedFrame(channel string) []byte {
	type payload struct {
		Event   string `json:"event"`
		Data    string `json:"data"`
		Channel string `json:"channel"`
	}

	return frame(payload{Event: "pusher_internal:subscription_succeeded", Data: "{}", Channel: channel})
}

// Message is one published event, as it travels through a Fanout.
type Message struct {
	// Channels the event goes to; connections not subscribed get nothing.
	Channels []string `json:"channels"`
	Event    string   `json:"event"`
	// Data is an HTTP API event's `data`, the payload's JSON as a string.
	Data string `json:"data,omitempty"`
	// ClientData is a client event's `data`, the JSON the client sent.
	ClientData json.RawMessage `json:"client_data,omitempty"`
	// ExceptSocketID skips one connection: the HTTP API's socket_id, or the
	// client event's sender.
	ExceptSocketID string `json:"except_socket_id,omitempty"`
}

// channelFrame is the frame one channel's subscribers receive, keyed event,
// data, channel for HTTP API events and event, channel, data for client
// events, as clients expect.
func (m Message) channelFrame(channel string) []byte {
	if m.ClientData != nil {
		type payload struct {
			Event   string          `json:"event"`
			Channel string          `json:"channel"`
			Data    json.RawMessage `json:"data"`
		}

		// Plain encoding/json: the escaping pass would misread escapes
		// already inside the client's raw JSON.
		body, err := json.Marshal(payload{Event: m.Event, Channel: channel, Data: m.ClientData})
		if err != nil {
			return errorFrame(errInvalidMessage)
		}

		return body
	}

	type payload struct {
		Event   string `json:"event"`
		Data    string `json:"data"`
		Channel string `json:"channel"`
	}

	return frame(payload{Event: m.Event, Data: m.Data, Channel: channel})
}
