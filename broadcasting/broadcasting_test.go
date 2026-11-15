package broadcasting

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atobeach/reef/clock/clocktest"
)

type goldenRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   string            `json:"body"`
}

type golden struct {
	Key         string          `json:"key"`
	Secret      string          `json:"secret"`
	AppID       string          `json:"app_id"`
	Requests    []goldenRequest `json:"requests"`
	ChannelAuth struct {
		Channel  string            `json:"channel"`
		SocketID string            `json:"socket_id"`
		Response map[string]string `json:"response"`
	} `json:"channel_auth"`
}

// loadGolden reads the signed requests and channel auth originally captured
// from the reference Pusher publisher. The request entries were later
// re-signed with the same Pusher signing scheme (body_md5, then HMAC-SHA256
// over method, path and sorted query), checked first to reproduce the
// captured signatures exactly.
func loadGolden(t *testing.T) golden {
	t.Helper()

	raw, err := os.ReadFile("testdata/pusher_golden.json")
	if err != nil {
		t.Fatal(err)
	}

	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}

	return g
}

type captured struct {
	method string
	path   string
	query  url.Values
	body   []byte
	header http.Header
}

// broker is an httptest stand-in for the Pusher HTTP API that records calls.
func broker(t *testing.T, status int) (server *httptest.Server, calls func() []captured) {
	t.Helper()

	var mu sync.Mutex
	var recorded []captured

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		recorded = append(recorded, captured{method: r.Method, path: r.URL.Path, query: r.URL.Query(), body: body, header: r.Header.Clone()})
		mu.Unlock()

		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	return server, func() []captured {
		mu.Lock()
		defer mu.Unlock()

		return append([]captured(nil), recorded...)
	}
}

func pusherFor(t *testing.T, server *httptest.Server, g golden) *Pusher {
	t.Helper()

	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())

	return NewPusher(PusherConfig{AppID: g.AppID, Key: g.Key, Secret: g.Secret, Host: u.Hostname(), Port: port, Scheme: "http"})
}

// verifySignature checks a request independently of RequestSignature:
// recompute the HMAC over METHOD\npath\nsorted params (minus the
// signature) and the body md5.
func verifySignature(t *testing.T, secret string, c captured) {
	t.Helper()

	sum := md5.Sum(c.body)
	if c.query.Get("body_md5") != hex.EncodeToString(sum[:]) {
		t.Errorf("body_md5 = %s, body hashes to %x", c.query.Get("body_md5"), sum)
	}

	var names []string
	for name := range c.query {
		if name != "auth_signature" {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	pairs := make([]string, len(names))
	for i, name := range names {
		pairs[i] = name + "=" + c.query.Get(name)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(c.method + "\n" + c.path + "\n" + strings.Join(pairs, "&")))

	if want := hex.EncodeToString(mac.Sum(nil)); c.query.Get("auth_signature") != want {
		t.Errorf("auth_signature = %s, want %s", c.query.Get("auth_signature"), want)
	}
}

// holdPayload has the hold-expiring event's payload keys, in order.
type holdPayload struct {
	BookingID        string  `json:"booking_id"`
	ExpiresAt        *string `json:"expires_at"`
	MinutesRemaining int     `json:"minutes_remaining"`
}

func TestPublishesTheGoldenRequestByteForByte(t *testing.T) {
	g := loadGolden(t)
	want := g.Requests[0]
	timestamp, _ := strconv.ParseInt(want.Query["auth_timestamp"], 10, 64)

	server, calls := broker(t, 200)
	pusher := pusherFor(t, server, g)

	// Signed at the golden's auth_timestamp, so the signature replays byte
	// for byte.
	signed, _ := clocktest.FrozenAt(t.Context(), time.Unix(timestamp, 0))
	expires := "2026-10-20T12:34:00.000000Z"
	err := pusher.Broadcast(signed, []string{"bookings.9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"}, "booking.hold-expiring",
		holdPayload{BookingID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", ExpiresAt: &expires, MinutesRemaining: 5})
	if err != nil {
		t.Fatal(err)
	}

	got := calls()
	if len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}

	call := got[0]
	if call.method != want.Method || call.path != want.Path {
		t.Errorf("request = %s %s, want %s %s", call.method, call.path, want.Method, want.Path)
	}

	if string(call.body) != want.Body {
		t.Errorf("body =\n%s\nwant\n%s", call.body, want.Body)
	}

	for name, value := range want.Query {
		if call.query.Get(name) != value {
			t.Errorf("query %s = %q, want %q (golden)", name, call.query.Get(name), value)
		}
	}

	if call.header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", call.header.Get("Content-Type"))
	}

	verifySignature(t, g.Secret, call)
}

func TestEncodesPayloadsInTheGoldenWireEncoding(t *testing.T) {
	g := loadGolden(t)
	want := g.Requests[1]
	timestamp, _ := strconv.ParseInt(want.Query["auth_timestamp"], 10, 64)

	server, calls := broker(t, 200)

	payload := struct {
		URL  string  `json:"url"`
		Name string  `json:"name"`
		HTML string  `json:"html"`
		N    float64 `json:"n"`
		None *string `json:"none"`
	}{URL: "https://example.com/a/b", Name: "Café ☕ 😀", HTML: "<b>&amp;</b>", N: 1.5}

	signed, _ := clocktest.FrozenAt(t.Context(), time.Unix(timestamp, 0))
	if err := pusherFor(t, server, g).Broadcast(signed, []string{"bookings.a", "bookings.b"}, "golden.encoding", payload); err != nil {
		t.Fatal(err)
	}

	call := calls()[0]
	if string(call.body) != want.Body {
		t.Errorf("body =\n%s\nwant\n%s", call.body, want.Body)
	}

	if call.query.Get("auth_signature") != want.Query["auth_signature"] {
		t.Errorf("signature = %s, want %s", call.query.Get("auth_signature"), want.Query["auth_signature"])
	}
}

func TestChunksChannelsByAHundred(t *testing.T) {
	g := loadGolden(t)
	server, calls := broker(t, 200)

	channels := make([]string, 150)
	for i := range channels {
		channels[i] = "search." + strconv.Itoa(i)
	}

	if err := pusherFor(t, server, g).Broadcast(context.Background(), channels, "e", map[string]int{}); err != nil {
		t.Fatal(err)
	}

	got := calls()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}

	var first struct {
		Channels []string `json:"channels"`
	}
	_ = json.Unmarshal(got[0].body, &first)

	if len(first.Channels) != 100 {
		t.Errorf("first chunk has %d channels", len(first.Channels))
	}
}

func TestABrokerRejectionIsAnError(t *testing.T) {
	g := loadGolden(t)
	server, _ := broker(t, 401)

	err := pusherFor(t, server, g).Broadcast(context.Background(), []string{"search.x"}, "e", struct{}{})
	if err == nil || !strings.Contains(err.Error(), "Pusher error") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsInvalidChannelNames(t *testing.T) {
	g := loadGolden(t)
	server, calls := broker(t, 200)

	if err := pusherFor(t, server, g).Broadcast(context.Background(), []string{"bad channel"}, "e", struct{}{}); err == nil {
		t.Fatal("a channel with a space must be rejected")
	}

	if len(calls()) != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestAuthorizesPrivateChannelsLikeTheGoldenVector(t *testing.T) {
	g := loadGolden(t)
	pusher := NewPusher(PusherConfig{Key: g.Key, Secret: g.Secret})

	auth, err := pusher.AuthorizeChannel(g.ChannelAuth.Channel, g.ChannelAuth.SocketID)
	if err != nil {
		t.Fatal(err)
	}

	if auth["auth"] != g.ChannelAuth.Response["auth"] {
		t.Errorf("auth = %s, want %s", auth["auth"], g.ChannelAuth.Response["auth"])
	}

	if _, err := pusher.AuthorizeChannel(g.ChannelAuth.Channel, "nope"); !errors.Is(err, ErrInvalidSocketID) {
		t.Errorf("bad socket id err = %v", err)
	}
}

func TestChannelRulesMatchPlaceholderPatterns(t *testing.T) {
	channels := &Channels{}
	channels.Channel("App.Models.User.{id}", func(_ context.Context, user User, params []string) bool {
		return user != nil && strconv.FormatInt(user.AuthID(), 10) == params[0]
	})

	ctx := context.Background()
	me := testUser(42)

	cases := []struct {
		name    string
		user    User
		channel string
		want    bool
	}{
		{"own private channel", me, "private-App.Models.User.42", true},
		{"someone else's", me, "private-App.Models.User.7", false},
		{"guest on a private channel", nil, "private-App.Models.User.42", false},
		{"no matching rule", me, "private-Other.42", false},
		{"empty name", me, "", false},
	}

	for _, tc := range cases {
		if got := channels.Allowed(ctx, tc.user, tc.channel); got != tc.want {
			t.Errorf("%s: Allowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type testUser int64

func (u testUser) AuthID() int64 { return int64(u) }

func TestFakeRecordsAndCanFail(t *testing.T) {
	fake := &Fake{Fail: func(channels []string, _ string) error {
		if channels[0] == "search.down" {
			return errors.New("connection refused")
		}

		return nil
	}}

	if err := fake.Broadcast(context.Background(), []string{"search.down"}, "e", struct{}{}); err == nil {
		t.Fatal("expected the configured failure")
	}

	if err := fake.Broadcast(context.Background(), []string{"search.up"}, "e", map[string]string{"a": "b/c"}); err != nil {
		t.Fatal(err)
	}

	sent := fake.Sent()
	if len(sent) != 1 || string(sent[0].Payload) != `{"a":"b\/c"}` {
		t.Fatalf("sent = %+v", sent)
	}
}
