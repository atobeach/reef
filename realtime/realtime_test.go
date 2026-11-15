package realtime

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atobeach/reef/broadcasting"
)

const (
	testAppID  = "123456"
	testKey    = "test-key"
	testSecret = "test-secret"
)

// harness is a server on httptest with its maintenance loop running.
type harness struct {
	t      *testing.T
	server *Server
	http   *httptest.Server
}

func start(t *testing.T, configure func(*Config)) *harness {
	t.Helper()

	cfg := Config{App: App{ID: testAppID, Key: testKey, Secret: testSecret}}
	if configure != nil {
		configure(&cfg)
	}

	server := NewServer(cfg, nil)
	ts := httptest.NewServer(server)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = server.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		server.Close(5 * time.Second)
		ts.Close()
	})

	return &harness{t: t, server: server, http: ts}
}

// pusher is the Go publisher pointed at this server.
func (h *harness) pusher() *broadcasting.Pusher {
	u, _ := url.Parse(h.http.URL)
	port, _ := strconv.Atoi(u.Port())

	return broadcasting.NewPusher(broadcasting.PusherConfig{AppID: testAppID, Key: testKey, Secret: testSecret, Host: u.Hostname(), Port: port, Scheme: "http"})
}

// client is a coder/websocket client speaking raw Pusher frames.
type client struct {
	t  *testing.T
	ws *websocket.Conn
}

func (h *harness) dial(key string, header http.Header) *client {
	h.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	endpoint := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/app/" + key + "?protocol=7&client=js&version=8.4.0&flash=false"

	ws, res, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: header})
	closeHandshake(res)
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}

	ws.SetReadLimit(1 << 20)
	h.t.Cleanup(func() { _ = ws.CloseNow() })

	return &client{t: h.t, ws: ws}
}

// closeHandshake closes a dial's handshake response body when there is one:
// coder/websocket owns it (nil) after a successful upgrade.
func closeHandshake(res *http.Response) {
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
}

// connect dials with the app key and returns the client and its socket id.
func (h *harness) connect() (c *client, socket string) {
	h.t.Helper()

	c = h.dial(testKey, nil)
	frame := c.read()

	match := regexp.MustCompile(`^\{"event":"pusher:connection_established","data":"\{\\"socket_id\\":\\"(\d+\.\d+)\\",\\"activity_timeout\\":30\}"\}$`).FindStringSubmatch(frame)
	if match == nil {
		h.t.Fatalf("connection_established frame = %s", frame)
	}

	return c, match[1]
}

func (c *client) read() string {
	c.t.Helper()

	frame, err := c.tryRead(5 * time.Second)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}

	return frame
}

func (c *client) tryRead(timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := c.ws.Read(ctx)

	return string(data), err
}

// expect reads one frame and compares it byte for byte.
func (c *client) expect(want string) {
	c.t.Helper()

	if got := c.read(); got != want {
		c.t.Fatalf("frame\n got %s\nwant %s", got, want)
	}
}

// expectClosed reads until the close and checks its code.
func (c *client) expectClosed(code websocket.StatusCode) {
	c.t.Helper()

	for range 10 {
		_, err := c.tryRead(5 * time.Second)
		if err == nil {
			continue
		}

		if got := websocket.CloseStatus(err); got != code {
			c.t.Fatalf("close status = %d (%v), want %d", got, err, code)
		}

		return
	}

	c.t.Fatal("connection never closed")
}

func (c *client) send(frame string) {
	c.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *client) subscribe(channel string, auth string) {
	c.t.Helper()

	data, _ := json.Marshal(map[string]string{"auth": auth, "channel": channel})
	c.send(`{"event":"pusher:subscribe","data":` + string(data) + `}`)
}

// barrier proves nothing else was queued before it: a ping's pong is the
// next frame.
func (c *client) barrier() {
	c.t.Helper()

	c.send(`{"event":"pusher:ping","data":{}}`)
	c.expect(`{"event":"pusher:pong"}`)
}

func errorJSON(code int, message string) string {
	return string(errorFrame(pusherError{Code: code, Message: message}))
}

func TestConnectionEstablishedAndPingPong(t *testing.T) {
	h := start(t, nil)
	c, socket := h.connect()

	if !regexp.MustCompile(`^\d+\.\d+$`).MatchString(socket) {
		t.Fatalf("socket id %q", socket)
	}

	c.barrier()

	// A pong (and any message) is activity; it gets no reply.
	c.send(`{"event":"pusher:pong","data":{}}`)
	c.barrier()

	if h.server.Connections() != 1 {
		t.Fatalf("connections = %d", h.server.Connections())
	}
}

func TestUnknownAppKeyIsRefused(t *testing.T) {
	h := start(t, nil)
	c := h.dial("wrong", nil)

	c.expect(`{"event":"pusher:error","data":"{\"code\":4001,\"message\":\"Application does not exist\"}"}`)
	c.expectClosed(4001)
}

func TestMalformedAndUnknownMessagesAre4200(t *testing.T) {
	h := start(t, nil)
	c, _ := h.connect()

	invalid := `{"event":"pusher:error","data":"{\"code\":4200,\"message\":\"Invalid message format\"}"}`

	for _, frame := range []string{
		`not json`,
		`{"data":{}}`,
		`{"event":"pusher:nope","data":{}}`,
		`{"event":"pusher:subscribe","data":{}}`,
		`{"event":"pusher:subscribe","data":{"channel":"bad channel!"}}`,
		`{"event":"client-typing","data":{}}`,
	} {
		c.send(frame)
		c.expect(invalid)
	}

	// Still connected.
	c.barrier()
}

func TestSubscribesToPublicChannelsAndReceivesHTTPAPIEvents(t *testing.T) {
	h := start(t, nil)
	subscriber, _ := h.connect()
	bystander, _ := h.connect()

	subscriber.subscribe("flights.abc", "")
	subscriber.expect(`{"event":"pusher_internal:subscription_succeeded","data":"{}","channel":"flights.abc"}`)

	// Data as a JSON string, the way pusher-js sends it.
	subscriber.send(`{"event":"pusher:subscribe","data":"{\"channel\":\"flights.def\"}"}`)
	subscriber.expect(`{"event":"pusher_internal:subscription_succeeded","data":"{}","channel":"flights.def"}`)

	payload := struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}{URL: "https://example.com/a", Name: "Café"}

	if err := h.pusher().Broadcast(context.Background(), []string{"flights.abc"}, "booking.hold-expiring", payload); err != nil {
		t.Fatal(err)
	}

	// The Pusher frame byte for byte: event, data (the payload JSON as a
	// string, slashes and non-ASCII escaped), channel.
	subscriber.expect(`{"event":"booking.hold-expiring","data":"{\"url\":\"https:\\\/\\\/example.com\\\/a\",\"name\":\"Caf\\u00e9\"}","channel":"flights.abc"}`)

	// Several channels: one frame per channel, each naming its own.
	if err := h.pusher().Broadcast(context.Background(), []string{"flights.abc", "flights.def", "flights.nobody"}, "multi", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}

	got := []string{subscriber.read(), subscriber.read()}
	want := map[string]bool{
		`{"event":"multi","data":"{\"n\":1}","channel":"flights.abc"}`: true,
		`{"event":"multi","data":"{\"n\":1}","channel":"flights.def"}`: true,
	}

	for _, frame := range got {
		if !want[frame] {
			t.Fatalf("unexpected frame %s", frame)
		}

		delete(want, frame)
	}

	// The unsubscribed connection got nothing.
	bystander.barrier()

	// After unsubscribing, events stop.
	subscriber.send(`{"event":"pusher:unsubscribe","data":{"channel":"flights.abc"}}`)
	subscriber.barrier()

	if err := h.pusher().Broadcast(context.Background(), []string{"flights.abc"}, "late", map[string]int{}); err != nil {
		t.Fatal(err)
	}

	subscriber.barrier()
}

func TestPrivateChannelAuth(t *testing.T) {
	h := start(t, nil)
	c, socket := h.connect()

	auth, err := h.pusher().AuthorizeChannel("private-App.Models.User.1", socket)
	if err != nil {
		t.Fatal(err)
	}

	c.subscribe("private-App.Models.User.1", auth["auth"])
	c.expect(`{"event":"pusher_internal:subscription_succeeded","data":"{}","channel":"private-App.Models.User.1"}`)

	unauthorized := `{"event":"pusher:error","data":"{\"code\":4009,\"message\":\"Connection is unauthorized\"}"}`

	otherSocket := broadcasting.ChannelSignature(testSecret, "1.1", "private-App.Models.User.2", "")
	wrongSecret := broadcasting.ChannelSignature("other", socket, "private-App.Models.User.2", "")
	rightSignature := broadcasting.ChannelSignature(testSecret, socket, "private-App.Models.User.2", "")

	for _, bad := range []string{
		"",
		rightSignature,                   // no key half
		"other-key:" + rightSignature,    // another app's key
		testKey + ":" + otherSocket,      // signed for another socket
		testKey + ":" + wrongSecret,      // signed with another secret
		testKey + ":" + auth["auth"][9:], // signed for another channel
	} {
		c.subscribe("private-App.Models.User.2", bad)
		c.expect(unauthorized)
	}

	// No auth field at all.
	c.send(`{"event":"pusher:subscribe","data":{"channel":"private-App.Models.User.2"}}`)
	c.expect(unauthorized)

	// A private channel's events reach the authorised subscriber.
	if err := h.pusher().Broadcast(context.Background(), []string{"private-App.Models.User.1"}, "hello", map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}

	c.expect(`{"event":"hello","data":"{\"ok\":true}","channel":"private-App.Models.User.1"}`)
}

func TestPresenceAndCacheChannelsAreRefused(t *testing.T) {
	h := start(t, nil)
	c, socket := h.connect()

	signature := testKey + ":" + broadcasting.ChannelSignature(testSecret, socket, "presence-room", `{"user_id":1}`)
	c.send(`{"event":"pusher:subscribe","data":{"channel":"presence-room","auth":"` + signature + `","channel_data":"{\"user_id\":1}"}}`)
	c.expect(errorJSON(4009, "Presence channels are not supported"))

	for _, channel := range []string{"cache-x", "private-cache-x", "presence-cache-x"} {
		c.subscribe(channel, "")
		c.expect(errorJSON(4009, "Cache channels are not supported"))
	}
}

func TestClientEvents(t *testing.T) {
	t.Run("members by default", func(t *testing.T) {
		h := start(t, nil)
		sender, socket := h.connect()
		other, otherSocket := h.connect()

		auth := func(s string) string {
			return testKey + ":" + broadcasting.ChannelSignature(testSecret, s, "private-chat", "")
		}

		sender.send(`{"event":"client-typing","channel":"private-chat","data":{"a":1}}`)
		sender.expect(errorJSON(4009, "The client is not a member of the specified channel."))

		sender.subscribe("private-chat", auth(socket))
		sender.read()
		other.subscribe("private-chat", auth(otherSocket))
		other.read()

		sender.send(`{"event":"client-typing","channel":"private-chat","data":{"a":1}}`)
		other.expect(`{"event":"client-typing","channel":"private-chat","data":{"a":1}}`)

		// Not echoed to the sender; non-`client-` events are dropped.
		sender.send(`{"event":"other","channel":"private-chat","data":{}}`)
		sender.barrier()
		other.barrier()
	})

	t.Run("disabled", func(t *testing.T) {
		h := start(t, func(c *Config) { c.App.AcceptClientEventsFrom = "none" })
		c, _ := h.connect()

		c.send(`{"event":"client-typing","channel":"flights.x","data":{}}`)
		c.expect(errorJSON(4301, "The app does not have client messaging enabled."))
	})
}

// signedRequest builds a POST signed the way Pusher publishers sign it,
// then lets the test tamper with it.
func (h *harness) signedRequest(path string, body string, timestamp int64, tamper func(q url.Values) url.Values) *http.Request {
	sum := md5.Sum([]byte(body))
	query := broadcasting.SignedQuery(testKey, testSecret, "POST", path, map[string]string{"body_md5": hex.EncodeToString(sum[:])}, timestamp)

	values, _ := url.ParseQuery(query)
	if tamper != nil {
		values = tamper(values)
	}

	return h.request(http.MethodPost, path+"?"+values.Encode(), strings.NewReader(body))
}

// request builds a request to the server, bound to the test's context.
func (h *harness) request(method string, path string, body io.Reader) *http.Request {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), method, h.http.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}

	return req
}

// do sends req and returns the status and the body.
func do(t *testing.T, req *http.Request) (status int, body string) {
	t.Helper()

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	reply, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(reply)
}

func TestHTTPAPIVerifiesRequests(t *testing.T) {
	h := start(t, nil)
	c, _ := h.connect()
	c.subscribe("flights.abc", "")
	c.read()

	path := "/apps/" + testAppID + "/events"
	body := `{"name":"x","data":"{}","channel":"flights.abc"}`
	now := time.Now().Unix()

	cases := []struct {
		name   string
		req    *http.Request
		status int
		reply  string
	}{
		{"unsigned", h.request(http.MethodPost, path, strings.NewReader(body)), 401, "Authentication signature invalid."},
		{"expired", h.signedRequest(path, body, now-601, nil), 401, "Authentication signature invalid."},
		// A minute past the tolerance, not a second: the server reads its own
		// clock later, and a second ticking over in between would bring
		// now+601 back within it.
		{"from the future", h.signedRequest(path, body, now+660, nil), 401, "Authentication signature invalid."},
		{"bad body_md5", h.signedRequest(path, body, now, func(q url.Values) url.Values {
			q.Set("body_md5", strings.Repeat("0", 32))
			return q
		}), 401, "Authentication signature invalid."},
		{"body swapped after signing", func() *http.Request {
			req := h.signedRequest(path, body, now, nil)
			swapped := `{"name":"y","data":"{}","channel":"flights.abc"}`
			req.Body = io.NopCloser(strings.NewReader(swapped))
			req.ContentLength = int64(len(swapped))
			return req
		}(), 401, "Authentication signature invalid."},
		{"bad signature", h.signedRequest(path, body, now, func(q url.Values) url.Values {
			q.Set("auth_signature", strings.Repeat("a", 64))
			return q
		}), 401, "Authentication signature invalid."},
		{"wrong key", h.signedRequest(path, body, now, func(q url.Values) url.Values {
			q.Set("auth_key", "other")
			return q
		}), 401, "Authentication signature invalid."},
		{"unknown app", h.signedRequest("/apps/999/events", body, now, nil), 404, "No matching application for ID [999]."},
		{"too large", h.signedRequest(path, `{"name":"x","data":"`+strings.Repeat("a", 10_001)+`","channel":"flights.abc"}`, now, nil), 413, "Payload too large."},
		{"invalid", h.signedRequest(path, `{"data":5}`, now, nil), 422, `{"name":["The name field is required."],"data":["The data field must be a string."],"channels":["The channels field is required when channel is not present."],"channel":["The channel field is required when channels is not present."]}`},
		{"bad channel name", h.signedRequest(path, `{"name":"x","data":"{}","channels":["ok","no spaces"]}`, now, nil), 422, `{"channels":["The channels field contains an invalid channel name."]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reply := do(t, tc.req)
			if status != tc.status || reply != tc.reply {
				t.Fatalf("got %d %s, want %d %s", status, reply, tc.status, tc.reply)
			}
		})
	}

	// None of them published anything.
	c.barrier()

	// GET on the events route is a 405; unknown routes 404; /up is healthy.
	res, err := http.DefaultClient.Do(h.request(http.MethodGet, path, http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "POST" {
		t.Fatalf("GET events = %d", res.StatusCode)
	}

	if status, _ := do(t, h.request(http.MethodGet, "/nope", http.NoBody)); status != http.StatusNotFound {
		t.Fatalf("GET /nope = %d", status)
	}

	status, reply := do(t, h.request(http.MethodGet, "/up", http.NoBody))
	if status != 200 || reply != `{"health":"OK"}` {
		t.Fatalf("/up = %d %s", status, reply)
	}

	// The signed original goes through, with the HTTP API's `{}` reply.
	status, reply = do(t, h.signedRequest(path, body, now, nil))
	if status != 200 || reply != "{}" {
		t.Fatalf("signed = %d %s", status, reply)
	}

	c.expect(`{"event":"x","data":"{}","channel":"flights.abc"}`)
}

func TestHTTPAPIExcludesSocketIDAndServesBatches(t *testing.T) {
	h := start(t, nil)
	first, firstSocket := h.connect()
	second, _ := h.connect()

	for _, c := range []*client{first, second} {
		c.subscribe("flights.abc", "")
		c.read()
	}

	now := time.Now().Unix()

	status, reply := do(t, h.signedRequest("/apps/"+testAppID+"/events", `{"name":"x","data":"1","channel":"flights.abc","socket_id":"`+firstSocket+`"}`, now, nil))
	if status != 200 || reply != "{}" {
		t.Fatalf("events = %d %s", status, reply)
	}

	second.expect(`{"event":"x","data":"1","channel":"flights.abc"}`)
	first.barrier()

	status, reply = do(t, h.signedRequest("/apps/"+testAppID+"/batch_events", `{"batch":[{"name":"a","data":"1","channel":"flights.abc"},{"name":"b","data":"2","channel":"flights.abc"}]}`, now, nil))
	if status != 200 || reply != `{"batch":{}}` {
		t.Fatalf("batch = %d %s", status, reply)
	}

	first.expect(`{"event":"a","data":"1","channel":"flights.abc"}`)
	first.expect(`{"event":"b","data":"2","channel":"flights.abc"}`)

	status, reply = do(t, h.signedRequest("/apps/"+testAppID+"/batch_events", `{"batch":[{"name":"a"}]}`, now, nil))
	// A batch item's errors name it by its key path, in the message too.
	if status != 422 || reply != `{"batch.0.data":["The batch.0.data field is required."],"batch.0.channel":["The batch.0.channel field is required."]}` {
		t.Fatalf("batch invalid = %d %s", status, reply)
	}
}

func TestBurstReachesAFastSubscriberInOrder(t *testing.T) {
	h := start(t, nil)
	c, _ := h.connect()
	c.subscribe("flights.abc", "")
	c.read()

	for i := range 100 {
		h.server.deliver(Message{Channels: []string{"flights.abc"}, Event: "n", Data: strconv.Itoa(i)})
	}

	for i := range 100 {
		c.expect(`{"event":"n","data":"` + strconv.Itoa(i) + `","channel":"flights.abc"}`)
	}
}

func TestSlowConsumerIsDisconnectedNotBuffered(t *testing.T) {
	h := start(t, func(c *Config) { c.SendBuffer = 4 })
	c, _ := h.connect()
	c.subscribe("flights.abc", "")
	c.read()

	h.server.mu.RLock()
	var server *conn
	for _, sc := range h.server.conns {
		server = sc
	}
	h.server.mu.RUnlock()

	// The client stops reading; ~9 KB frames fill the socket buffers, then
	// the 4-frame queue, and the server cuts it off rather than grow.
	big := strings.Repeat("x", 9000)
	deadline := time.Now().Add(10 * time.Second)

	for h.server.Connections() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("slow consumer never disconnected")
		}

		h.server.deliver(Message{Channels: []string{"flights.abc"}, Event: "big", Data: big})

		if queued := len(server.send); queued > 4 {
			t.Fatalf("queue grew to %d", queued)
		}

		time.Sleep(100 * time.Microsecond)
	}

	<-server.writerDone

	if server.closeCode != closeOverCapacity {
		t.Fatalf("close code = %d", server.closeCode)
	}
}

func TestConnectionLimit(t *testing.T) {
	h := start(t, func(c *Config) { c.App.MaxConnections = 1 })
	first, _ := h.connect()

	second := h.dial(testKey, nil)
	second.expect(errorJSON(4004, "Application is over connection quota"))
	second.expectClosed(4004)

	first.barrier()
}

func TestMessageSizeLimit(t *testing.T) {
	h := start(t, func(c *Config) { c.App.MaxMessageSize = 100 })
	c, _ := h.connect()

	c.send(`{"event":"pusher:ping","data":{"pad":"` + strings.Repeat("a", 200) + `"}}`)
	c.expectClosed(websocket.StatusMessageTooBig)
}

func TestSubscriptionLimit(t *testing.T) {
	h := start(t, func(c *Config) { c.MaxSubscriptions = 2 })
	c, _ := h.connect()

	c.subscribe("a", "")
	c.read()
	c.subscribe("b", "")
	c.read()
	c.subscribe("a", "") // resubscribing doesn't count
	c.read()

	c.subscribe("c", "")
	c.expect(errorJSON(4100, "Too many subscriptions"))
}

func TestAllowedOrigins(t *testing.T) {
	h := start(t, func(c *Config) { c.App.AllowedOrigins = []string{"*.example.com", "localhost"} })

	for _, origin := range []string{"https://www.example.com", "http://localhost:3000"} {
		c := h.dial(testKey, http.Header{"Origin": {origin}})
		if frame := c.read(); !strings.Contains(frame, "connection_established") {
			t.Fatalf("%s: %s", origin, frame)
		}
	}

	for _, origin := range []string{"https://evil.com", "https://example.com.evil.com", ""} {
		c := h.dial(testKey, http.Header{"Origin": {origin}})
		c.expect(errorJSON(4009, "Origin not allowed"))
		c.expectClosed(4009)
	}
}

func TestInactiveConnectionsArePingedThenPruned(t *testing.T) {
	// A pong must land within one check of its ping; 200ms leaves room for a
	// loaded -race runner.
	h := start(t, func(c *Config) {
		c.App.PingInterval = 300 * time.Millisecond
		c.CheckInterval = 200 * time.Millisecond
	})

	// Silent: pinged, then pruned with 4201.
	silent, _ := h.connect()
	silent.expect(`{"event":"pusher:ping"}`)
	silent.expect(errorJSON(4201, "Pong reply not received in time"))
	silent.expectClosed(closePongTimeout)

	// Answering pings keeps a connection alive.
	alive, _ := h.connect()
	for range 3 {
		alive.expect(`{"event":"pusher:ping"}`)
		alive.send(`{"event":"pusher:pong","data":{}}`)
	}

	if h.server.Connections() != 1 {
		t.Fatalf("connections = %d", h.server.Connections())
	}
}

func TestRateLimiting(t *testing.T) {
	h := start(t, func(c *Config) {
		c.App.RateLimit = RateLimit{Enabled: true, MaxAttempts: 3, Decay: time.Minute, Terminate: true}
	})
	c, _ := h.connect()

	for range 3 {
		c.barrier()
	}

	c.send(`{"event":"pusher:ping","data":{}}`)
	c.expect(errorJSON(4301, "Rate limit exceeded"))
	c.expectClosed(4301)
}

func TestCloseSendsGoingAway(t *testing.T) {
	h := start(t, nil)
	c, _ := h.connect()

	closed := make(chan struct{})
	go func() {
		// Close waits for the client's half of the handshake.
		h.server.Close(5 * time.Second)
		close(closed)
	}()

	c.expectClosed(websocket.StatusGoingAway)
	<-closed

	if h.server.Connections() != 0 {
		t.Fatalf("connections = %d", h.server.Connections())
	}
}

func TestPathPrefix(t *testing.T) {
	h := start(t, func(c *Config) { c.Path = "/ws/" })

	if status, _ := do(t, h.request(http.MethodGet, "/ws/up", http.NoBody)); status != http.StatusOK {
		t.Fatalf("/ws/up = %d", status)
	}

	if status, _ := do(t, h.request(http.MethodGet, "/up", http.NoBody)); status != http.StatusNotFound {
		t.Fatalf("/up = %d", status)
	}

	// The signature covers the path without the prefix: publishers sign
	// the Pusher path, not where it is mounted.
	sum := md5.Sum([]byte(`{"name":"x","data":"1","channel":"a"}`))
	query := broadcasting.SignedQuery(testKey, testSecret, "POST", "/apps/"+testAppID+"/events", map[string]string{"body_md5": hex.EncodeToString(sum[:])}, time.Now().Unix())

	req := h.request(http.MethodPost, "/ws/apps/"+testAppID+"/events?"+query, bytes.NewReader([]byte(`{"name":"x","data":"1","channel":"a"}`)))
	if status, reply := do(t, req); status != 200 {
		t.Fatalf("prefixed events = %d %s", status, reply)
	}
}

func TestServeListensAndDrains(t *testing.T) {
	if got := Address("[::]", 8080); got != "[::]:8080" {
		t.Fatalf("Address = %s", got)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- Serve(ctx, listener, Config{App: App{ID: testAppID, Key: testKey, Secret: testSecret}}, nil)
	}()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	ws, res, err := websocket.Dial(dialCtx, "ws://"+listener.Addr().String()+"/app/"+testKey, nil)
	closeHandshake(res)
	if err != nil {
		t.Fatal(err)
	}

	c := &client{t: t, ws: ws}
	c.read()

	cancel()
	c.expectClosed(websocket.StatusGoingAway)

	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
