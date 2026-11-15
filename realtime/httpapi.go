package realtime

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/clock"
)

// signatureTolerance is how far auth_timestamp may be from the server's
// clock, in seconds.
const signatureTolerance = 600

// Pusher's HTTP API limits: channels per event and events per batch.
const (
	maxEventChannels = 100
	maxBatchEvents   = 10
)

// readSigned reads the capped body and checks the app id and signature,
// answering the failure itself when ok is false.
func (s *Server) readSigned(w http.ResponseWriter, r *http.Request, appID string, path string) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			plain(w, http.StatusRequestEntityTooLarge, "Payload too large.")

			return nil, false
		}

		plain(w, http.StatusBadRequest, "Bad request.")

		return nil, false
	}

	if appID != s.cfg.App.ID {
		plain(w, http.StatusNotFound, "No matching application for ID ["+appID+"].")

		return nil, false
	}

	if !s.signatureValid(r, path, body) {
		plain(w, http.StatusUnauthorized, "Authentication signature invalid.")

		return nil, false
	}

	return body, true
}

// signatureValid checks the Pusher HTTP API signature over the query (route
// names excluded) and the body's md5, with auth_timestamp within tolerance.
// A body_md5 that doesn't match the body is rejected, as Pusher does.
func (s *Server) signatureValid(r *http.Request, path string, body []byte) bool {
	query := r.URL.Query()

	params := map[string]string{}
	for name, values := range query {
		switch name {
		case "auth_signature", "body_md5", "appId", "appKey", "channelName":
			continue
		}

		// A repeated key signs with its last value, as publishers parse it.
		params[name] = values[len(values)-1]
	}

	if len(body) > 0 {
		sum := md5.Sum(body)
		params["body_md5"] = hex.EncodeToString(sum[:])

		if sent := query.Get("body_md5"); sent != "" && sent != params["body_md5"] {
			return false
		}
	}

	if subtle.ConstantTimeCompare([]byte(query.Get("auth_key")), []byte(s.cfg.App.Key)) != 1 {
		return false
	}

	expected := broadcasting.RequestSignature(s.cfg.App.Secret, r.Method, path, params)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(query.Get("auth_signature"))) != 1 {
		return false
	}

	timestamp, err := strconv.ParseInt(query.Get("auth_timestamp"), 10, 64)
	if err != nil {
		return false
	}

	drift := clock.Now(r.Context()).Unix() - timestamp

	return drift <= signatureTolerance && drift >= -signatureTolerance
}

// validation collects field errors in rule order for the 422
// `{"field":["message",…]}` body publishers show.
type validation struct {
	order  []string
	errors map[string][]string
}

func (v *validation) add(field string, message string) {
	if v.errors == nil {
		v.errors = map[string][]string{}
	}

	if _, seen := v.errors[field]; !seen {
		v.order = append(v.order, field)
	}

	v.errors[field] = append(v.errors[field], message)
}

// write answers 422 with the errors, fields in the order they first
// failed.
func (v *validation) write(w http.ResponseWriter) {
	body := []byte("{")
	for i, field := range v.order {
		if i > 0 {
			body = append(body, ',')
		}

		name, _ := json.Marshal(field)
		messages, _ := json.Marshal(v.errors[field])
		body = append(append(append(body, name...), ':'), messages...)
	}

	body = append(body, '}')

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = w.Write(body)
}

// requiredString requires key to be a non-empty string, reporting under
// label; it returns the value.
func (v *validation) requiredString(fields map[string]json.RawMessage, key string, label string) string {
	raw, present := fields[key]
	if !present || string(raw) == "null" || string(raw) == `""` || string(raw) == "[]" {
		v.add(label, "The "+label+" field is required.")

		return ""
	}

	var value string
	if json.Unmarshal(raw, &value) != nil {
		v.add(label, "The "+label+" field must be a string.")
	}

	return value
}

// optionalString requires key, when present, to be a string.
func (v *validation) optionalString(fields map[string]json.RawMessage, key string, label string) string {
	raw, present := fields[key]
	if !present {
		return ""
	}

	var value string
	if json.Unmarshal(raw, &value) != nil {
		v.add(label, "The "+label+" field must be a string.")
	}

	return value
}

// channelList checks published channel names: the Pusher channel-name
// pattern and the 100-channel cap per event.
func (v *validation) channelList(channels []string, label string) {
	if len(channels) > maxEventChannels {
		v.add(label, fmt.Sprintf("The %s field must not have more than %d items.", label, maxEventChannels))
	}

	for _, channel := range channels {
		if !broadcasting.ValidChannelName(channel) {
			v.add(label, "The "+label+" field contains an invalid channel name.")

			return
		}
	}
}

// serveEvents is POST /apps/{id}/events: one event to `channel` or
// `channels`, minus the optional socket_id.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request, appID string, path string) {
	body, ok := s.readSigned(w, r, appID, path)
	if !ok {
		return
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		plain(w, http.StatusBadRequest, "Bad request.")

		return
	}

	var v validation
	name := v.requiredString(fields, "name", "name")
	data := v.requiredString(fields, "data", "data")
	channels := v.eventChannels(fields)
	socket := v.optionalString(fields, "socket_id", "socket_id")
	v.optionalString(fields, "info", "info")

	if v.order != nil {
		v.write(w)

		return
	}

	if !s.publish(w, r, Message{Channels: channels, Event: name, Data: data, ExceptSocketID: socket}) {
		return
	}

	// `info` (channel occupancy) isn't supported: publishers never ask.
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

// eventChannels reads an event's targets: `channels` (a list) when given,
// else the single `channel`; one of the two is required.
func (v *validation) eventChannels(fields map[string]json.RawMessage) []string {
	if raw, present := fields["channels"]; present && string(raw) != "null" {
		var channels []string

		switch {
		case json.Unmarshal(raw, &channels) != nil:
			v.add("channels", "The channels field must be an array.")
		case len(channels) == 0:
			v.add("channels", "The channels field is required when channel is not present.")
		default:
			v.channelList(channels, "channels")
		}

		return channels
	}

	if _, single := fields["channel"]; !single {
		v.add("channels", "The channels field is required when channel is not present.")
		v.add("channel", "The channel field is required when channels is not present.")

		return nil
	}

	channel := v.requiredString(fields, "channel", "channel")
	if channel == "" {
		return nil
	}

	v.channelList([]string{channel}, "channel")

	return []string{channel}
}

// serveBatchEvents is POST /apps/{id}/batch_events: up to ten events,
// each to one channel.
func (s *Server) serveBatchEvents(w http.ResponseWriter, r *http.Request, appID string, path string) {
	body, ok := s.readSigned(w, r, appID, path)
	if !ok {
		return
	}

	var payload struct {
		Batch json.RawMessage `json:"batch"`
	}

	if json.Unmarshal(body, &payload) != nil {
		plain(w, http.StatusBadRequest, "Bad request.")

		return
	}

	var v validation
	var items []map[string]json.RawMessage

	switch {
	case len(payload.Batch) == 0 || string(payload.Batch) == "null" || string(payload.Batch) == "[]":
		v.add("batch", "The batch field is required.")
	case json.Unmarshal(payload.Batch, &items) != nil:
		v.add("batch", "The batch field must be an array.")
	case len(items) > maxBatchEvents:
		v.add("batch", fmt.Sprintf("The batch field must not have more than %d items.", maxBatchEvents))
	}

	messages := make([]Message, len(items))
	for i, item := range items {
		prefix := "batch." + strconv.Itoa(i) + "."
		name := v.requiredString(item, "name", prefix+"name")
		data := v.requiredString(item, "data", prefix+"data")
		channel := v.requiredString(item, "channel", prefix+"channel")
		if channel != "" {
			v.channelList([]string{channel}, prefix+"channel")
		}
		socket := v.optionalString(item, "socket_id", prefix+"socket_id")
		v.optionalString(item, "info", prefix+"info")

		messages[i] = Message{Channels: []string{channel}, Event: name, Data: data, ExceptSocketID: socket}
	}

	if v.order != nil {
		v.write(w)

		return
	}

	for _, m := range messages {
		if !s.publish(w, r, m) {
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"batch":{}}`))
}

// publish hands m to the fan-out, answering 500 when it fails.
func (s *Server) publish(w http.ResponseWriter, r *http.Request, m Message) bool {
	if err := s.Publish(r.Context(), m); err != nil {
		slog.ErrorContext(r.Context(), "realtime: publishing failed", "event", m.Event, "error", err)
		plain(w, http.StatusInternalServerError, "Internal server error.")

		return false
	}

	return true
}
