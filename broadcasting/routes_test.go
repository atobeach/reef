package broadcasting

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/web"
)

// ownChannel lets a user join only their own App.Models.User.{id} channel.
func ownChannel(c *Channels) {
	c.Channel("App.Models.User.{id}", func(_ context.Context, user User, params []string) bool {
		return user != nil && strconv.FormatInt(user.AuthID(), 10) == params[0]
	})
}

// authRoute serves Routes for user 42, on s when s is set.
func authRoute(t *testing.T, signer *Pusher, s *session.Session) *web.Kernel {
	t.Helper()

	withSession := func(c *web.Context, next web.Handler) (any, error) {
		if s != nil {
			c.WithContext(session.With(c.Context(), s))
		}

		return next(c)
	}

	router := web.NewRouter()
	Routes(router.Middleware(withSession), ownChannel, signer, func(*web.Context) (User, error) { return testUser(42), nil })

	kernel, err := web.NewKernel(nil, web.KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	return kernel
}

// subscribe asks to join channel as socket 1234.5678, in the query string.
func subscribe(t *testing.T, kernel *web.Kernel, method string, channel string) *testkit.TestResponse {
	t.Helper()

	return testkit.NewClient(t, kernel).Call(method, "/broadcasting/auth?channel_name="+channel+"&socket_id=1234.5678")
}

func TestTheAuthRouteSignsOnGetAndPost(t *testing.T) {
	signer := NewPusher(PusherConfig{AppID: "1", Key: "app-key", Secret: "app-secret"})
	want, err := signer.AuthorizeChannel("private-App.Models.User.42", "1234.5678")
	if err != nil {
		t.Fatal(err)
	}

	kernel := authRoute(t, signer, nil)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		res := subscribe(t, kernel, method, "private-App.Models.User.42")

		var got map[string]string
		if err := json.Unmarshal([]byte(res.Body()), &got); err != nil || res.Status() != 200 || !maps.Equal(got, want) {
			t.Errorf("%s = %d %s, want %v", method, res.Status(), res.Body(), want)
		}

		if res := subscribe(t, kernel, method, "private-App.Models.User.7"); res.Status() != 403 {
			t.Errorf("%s someone else's channel = %d", method, res.Status())
		}
	}
}

// A JSON body's fields are read as text, so a numeric socket id signs as
// its digits.
func TestTheAuthRouteReadsAJSONBodyAsText(t *testing.T) {
	signer := NewPusher(PusherConfig{AppID: "1", Key: "app-key", Secret: "app-secret"})
	want, err := signer.AuthorizeChannel("private-App.Models.User.42", "1234.5678")
	if err != nil {
		t.Fatal(err)
	}

	res := testkit.NewClient(t, authRoute(t, signer, nil)).Send(http.MethodPost, "/broadcasting/auth",
		[]byte(`{"channel_name":"private-App.Models.User.42","socket_id":1234.5678}`), "application/json")

	var got map[string]string
	if err := json.Unmarshal([]byte(res.Body()), &got); err != nil || res.Status() != 200 || !maps.Equal(got, want) {
		t.Fatalf("response = %d %s, want %v", res.Status(), res.Body(), want)
	}
}

// Without a signer (the null and log broadcasters) every subscription is an
// empty 200.
func TestTheAuthRouteWithoutASignerIsAnEmpty200(t *testing.T) {
	res := subscribe(t, authRoute(t, nil, nil), http.MethodPost, "private-App.Models.User.7")

	if res.Status() != 200 || res.Body() != "" {
		t.Fatalf("response = %d %q", res.Status(), res.Body())
	}
}

// The subscription is no page view: last request's flashed values survive
// it for the next page, signed or not.
func TestTheAuthRouteKeepsTheFlashedValues(t *testing.T) {
	signers := map[string]*Pusher{
		"signed":   NewPusher(PusherConfig{AppID: "1", Key: "app-key", Secret: "app-secret"}),
		"unsigned": nil,
	}

	for name, signer := range signers {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"status": "saved", "_flash": map[string]any{"old": []string{"status"}, "new": []string{}}})
			s, err := session.Decode("session-id", base64.StdEncoding.EncodeToString(raw))
			if err != nil {
				t.Fatal(err)
			}

			subscribe(t, authRoute(t, signer, s), http.MethodPost, "private-App.Models.User.42")

			want := map[string]any{"old": []any{}, "new": []any{"status"}}
			if got := s.All()["_flash"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("_flash = %v, want %v", got, want)
			}
		})
	}
}
