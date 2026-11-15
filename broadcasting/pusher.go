package broadcasting

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atobeach/reef/clock"
)

// PusherConfig addresses and authenticates against a Pusher HTTP API (the
// realtime server's, in production).
type PusherConfig struct {
	AppID  string
	Key    string
	Secret string
	Host   string
	Port   int
	Scheme string
}

// Pusher publishes through the Pusher Channels HTTP API, signing requests
// as Pusher requires.
type Pusher struct {
	config PusherConfig
	http   *http.Client
}

// NewPusher builds the publisher. Requests time out after 10s, so a dead
// broker can't stall the scheduler's minute.
func NewPusher(config PusherConfig) *Pusher {
	return &Pusher{config: config, http: &http.Client{Timeout: 10 * time.Second}}
}

// Broadcast implements Broadcaster: the channels in chunks of 100 (the
// HTTP API's per-event cap), the payload JSON-encoded into `data`.
func (p *Pusher) Broadcast(ctx context.Context, channels []string, event string, payload any) error {
	data, err := EncodeASCIIJSON(payload)
	if err != nil {
		return fmt.Errorf("broadcasting: encoding %s payload: %w", event, err)
	}

	for start := 0; start < len(channels); start += 100 {
		chunk := channels[start:min(start+100, len(channels))]

		if err := p.Trigger(ctx, chunk, event, string(data)); err != nil {
			return err
		}
	}

	return nil
}

// triggerBody is the events POST body: `channel` for one channel,
// `channels` for several, `data` the payload's JSON as a string.
func triggerBody(channels []string, event string, data string) ([]byte, error) {
	type single struct {
		Name    string `json:"name"`
		Data    string `json:"data"`
		Channel string `json:"channel"`
	}

	type multiple struct {
		Name     string   `json:"name"`
		Data     string   `json:"data"`
		Channels []string `json:"channels"`
	}

	if len(channels) == 1 {
		return EncodeASCIIJSON(single{Name: event, Data: data, Channel: channels[0]})
	}

	return EncodeASCIIJSON(multiple{Name: event, Data: data, Channels: channels})
}

// channelName is the Pusher channel-name alphabet.
var channelName = regexp.MustCompile(`^[-a-zA-Z0-9_=@,.;]+$`)

// Trigger POSTs one event to /apps/{id}/events.
func (p *Pusher) Trigger(ctx context.Context, channels []string, event string, data string) error {
	for _, channel := range channels {
		if !ValidChannelName(channel) {
			return fmt.Errorf("broadcasting: invalid channel name %q", channel)
		}
	}

	body, err := triggerBody(channels, event, data)
	if err != nil {
		return fmt.Errorf("broadcasting: encoding %s body: %w", event, err)
	}

	path := "/apps/" + p.config.AppID + "/events"
	sum := md5.Sum(body)
	query := SignedQuery(p.config.Key, p.config.Secret, "POST", path, map[string]string{"body_md5": hex.EncodeToString(sum[:])}, clock.Now(ctx).Unix())

	endpoint := fmt.Sprintf("%s://%s:%d%s?%s", p.config.Scheme, p.config.Host, p.config.Port, path, query)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("broadcasting: building request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pusher-Library", "reef")

	res, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("broadcasting: Pusher error: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 2048))

		return fmt.Errorf("broadcasting: Pusher error: %s (HTTP %d)", strings.TrimSpace(string(detail)), res.StatusCode)
	}

	return nil
}

// SignedQuery is a Pusher HTTP API query string: the auth_* params merged
// with params, sorted, and auth_signature appended. Values are signed raw
// and URL-encoded only on the wire.
func SignedQuery(key string, secret string, method string, path string, params map[string]string, timestamp int64) string {
	all := map[string]string{
		"auth_key":       key,
		"auth_timestamp": strconv.FormatInt(timestamp, 10),
		"auth_version":   "1.0",
	}
	for name, value := range params {
		all[name] = value
	}

	names := sortedKeys(all)

	encoded := make([]string, len(names))
	for i, name := range names {
		encoded[i] = url.QueryEscape(name) + "=" + url.QueryEscape(all[name])
	}

	return strings.Join(encoded, "&") + "&auth_signature=" + RequestSignature(secret, method, path, all)
}

// RequestSignature is an HTTP API call's auth_signature: the app secret's
// HMAC-SHA256 of "METHOD\npath\nk=v&k=v", params sorted and raw. The
// publisher signs with it and the realtime server verifies with it.
func RequestSignature(secret string, method string, path string, params map[string]string) string {
	names := sortedKeys(params)

	pairs := make([]string, len(names))
	for i, name := range names {
		pairs[i] = name + "=" + params[name]
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method + "\n" + path + "\n" + strings.Join(pairs, "&")))

	return hex.EncodeToString(mac.Sum(nil))
}

func sortedKeys(params map[string]string) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// ValidChannelName reports a Pusher-valid channel name: at most 200 of
// `[-a-zA-Z0-9_=@,.;]`.
func ValidChannelName(channel string) bool {
	return len(channel) <= 200 && channelName.MatchString(channel)
}

// ChannelSignature is the HMAC-SHA256 of `socket_id:channel[:channel_data]`
// with the app secret: a private subscription's `auth` after the key.
func ChannelSignature(secret string, socket string, channel string, channelData string) string {
	subject := socket + ":" + channel
	if channelData != "" {
		subject += ":" + channelData
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(subject))

	return hex.EncodeToString(mac.Sum(nil))
}

// socketID is the Pusher socket id shape, `digits.digits`.
var socketID = regexp.MustCompile(`^\d+\.\d+$`)

// ErrInvalidSocketID rejects a malformed socket_id.
var ErrInvalidSocketID = errors.New("broadcasting: invalid socket ID")

// AuthorizeChannel is the `{"auth":"key:hmac(socket_id:channel)"}` a
// private-channel subscription presents to the realtime server.
func (p *Pusher) AuthorizeChannel(channel string, socket string) (map[string]string, error) {
	if !channelName.MatchString(channel) {
		return nil, fmt.Errorf("broadcasting: invalid channel name %q", channel)
	}

	if !socketID.MatchString(socket) {
		return nil, ErrInvalidSocketID
	}

	return map[string]string{"auth": p.config.Key + ":" + ChannelSignature(p.config.Secret, socket, channel, "")}, nil
}
