package auth

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/events/eventstest"
	"github.com/atobeach/reef/hashing"
	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/validation"
	"github.com/atobeach/reef/validation/rule"
	"github.com/atobeach/reef/web"
)

// timebox is long enough to measure and short enough to keep the misses
// quick.
const timebox = 50 * time.Millisecond

// adaEmail signs in with "secret"; graceEmail is a social-only user with no
// password.
const (
	adaEmail   = "ada@example.com"
	graceEmail = "grace@example.com"
)

// hash is password's bcrypt at cost.
func hash(t *testing.T, cost int, password string) string {
	t.Helper()

	hashed, err := hashing.New(cost).Make(password)
	if err != nil {
		t.Fatal(err)
	}

	return hashed
}

// users are Ada, whose password was hashed at cost, and password-less Grace.
func users(t *testing.T, cost int) *stubProvider {
	t.Helper()

	return &stubProvider{users: []stubUser{
		{id: 1, email: adaEmail, password: hash(t, cost, "secret")},
		{id: 2, email: graceEmail},
	}}
}

// sessionRequest is a request carrying a fresh session, as StartSession
// leaves one.
func sessionRequest(t *testing.T) *web.Context {
	t.Helper()

	s, err := session.Decode("probe", "")
	if err != nil {
		t.Fatal(err)
	}

	return web.NewContext(session.With(t.Context(), s), http.MethodPost, "/login")
}

// rememberedRequest is a sessionRequest carrying the remember-me cookie
// value.
func rememberedRequest(t *testing.T, guard *Guard[stubUser], value string) *web.Context {
	t.Helper()

	c := sessionRequest(t)
	c.SetHeader("Cookie", guard.RecallerCookie()+"="+value)

	return c
}

// recallerFor is the remember-me cookie value for user id with token, bound
// to passwordHash.
func recallerFor(guard *Guard[stubUser], id int64, token string, passwordHash string) string {
	return strings.Join([]string{strconv.FormatInt(id, 10), token, guard.HashPasswordForCookie(passwordHash)}, "|")
}

// Every answer takes at least the timebox, a match included, and only the
// right password for a user who has one matches.
func TestValidateTakesTheTimeboxWhateverTheAnswer(t *testing.T) {
	guard := newTestGuard(t, users(t, testCost), GuardConfig{Timebox: timebox})

	for _, tt := range []struct {
		name  string
		creds Credentials
		match int64 // the id of the user handed back; 0 is a miss
	}{
		{"the right password", Credentials{Email: adaEmail, Password: "secret"}, 1},
		{"a wrong password", Credentials{Email: adaEmail, Password: "wrong"}, 0},
		{"an unknown email", Credentials{Email: "nobody@example.com", Password: "secret"}, 0},
		{"a password-less user", Credentials{Email: graceEmail, Password: "secret"}, 0},
		{"a password-less user and no password", Credentials{Email: graceEmail}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			started := time.Now()
			user, ok, err := guard.Validate(sessionRequest(t), tt.creds)
			if elapsed := time.Since(started); elapsed < timebox {
				t.Fatalf("answered in %v, under the %v timebox", elapsed, timebox)
			}

			if err != nil || ok != (tt.match != 0) || user.id != tt.match {
				t.Fatalf("Validate = %+v, %v, %v; want user %d", user, ok, err, tt.match)
			}
		})
	}
}

// A guard configured without a timebox still takes 200ms over a miss, so
// the timing defence is never off by omission.
func TestAnUnsetTimeboxIs200ms(t *testing.T) {
	guard := newTestGuard(t, users(t, testCost), GuardConfig{})

	started := time.Now()
	if _, ok, err := guard.Validate(sessionRequest(t), Credentials{Email: "nobody@example.com"}); ok || err != nil {
		t.Fatalf("Validate = %v, %v; want a miss", ok, err)
	}

	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Fatalf("a miss answered in %v, under the default 200ms", elapsed)
	}
}

// A match whose hash was made at another cost is rehashed at the guard's
// through SavePassword, and Validate hands back the user carrying the new
// hash. A hash at the guard's cost is left alone.
func TestValidateRehashesAStaleCostThroughSavePassword(t *testing.T) {
	provider := users(t, testCost+1)
	guard := newTestGuard(t, provider, GuardConfig{Timebox: time.Nanosecond})
	creds := Credentials{Email: adaEmail, Password: "secret"}

	user, ok, err := guard.Validate(sessionRequest(t), creds)
	if err != nil || !ok {
		t.Fatalf("Validate = %v, %v", ok, err)
	}

	saved := provider.users[0].password
	if cost, _ := hashing.Cost(saved); cost != testCost || !hashing.Check("secret", saved) {
		t.Fatalf("saved %q, want a hash of the password at cost %d", saved, testCost)
	}

	if user.password != saved {
		t.Fatalf("Validate handed back hash %q, want the saved %q", user.password, saved)
	}

	if _, _, err := guard.Validate(sessionRequest(t), creds); err != nil || provider.users[0].password != saved {
		t.Fatalf("a hash at the current cost was rewritten (err %v)", err)
	}
}

// A rehash would kill the request's own remember-me cookie, which binds to
// the old hash, so the cookie is re-issued against the new one, and the
// re-issue signs the user back in once the session is gone.
func TestARehashReissuesTheRequestsOwnRecaller(t *testing.T) {
	provider := users(t, testCost+1)
	provider.users[0].token = "token"
	guard := newTestGuard(t, provider, GuardConfig{Timebox: time.Nanosecond})
	stale := provider.users[0]
	c := rememberedRequest(t, guard, recallerFor(guard, stale.id, stale.token, stale.password))

	if _, ok, err := guard.Validate(c, Credentials{Email: adaEmail, Password: "secret"}); err != nil || !ok {
		t.Fatalf("Validate = %v, %v", ok, err)
	}

	ada := provider.users[0]
	want := recallerFor(guard, ada.id, "token", ada.password)
	if queued := c.QueuedCookies(); len(queued) != 1 || queued[0].Value != want {
		t.Fatalf("queued %v, want the recaller %q bound to the new hash", queued, want)
	}

	if user, ok, err := guard.User(rememberedRequest(t, guard, want)); err != nil || !ok || user.id != ada.id {
		t.Fatalf("the re-issued recaller signed in %+v, %v, %v; want Ada", user, ok, err)
	}
}

// Only a cookie that recalled the user before the rehash is re-issued, so a
// forged one never becomes a working sign-in: at login Validate runs before
// the two-factor challenge.
func TestARehashLeavesAnyOtherRecallerAlone(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cookie func(guard *Guard[stubUser], ada stubUser) string
	}{
		{"a wrong token", func(g *Guard[stubUser], ada stubUser) string {
			return recallerFor(g, ada.id, "forged", ada.password)
		}},
		{"another user's id", func(g *Guard[stubUser], ada stubUser) string {
			return recallerFor(g, 2, ada.token, ada.password)
		}},
		{"another password hash", func(g *Guard[stubUser], ada stubUser) string {
			return recallerFor(g, ada.id, ada.token, "another hash")
		}},
		{"an empty cookie", func(*Guard[stubUser], stubUser) string { return "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := users(t, testCost+1)
			provider.users[0].token = "token"
			guard := newTestGuard(t, provider, GuardConfig{Timebox: time.Nanosecond})
			c := rememberedRequest(t, guard, tt.cookie(guard, provider.users[0]))

			if _, ok, err := guard.Validate(c, Credentials{Email: adaEmail, Password: "secret"}); err != nil || !ok {
				t.Fatalf("Validate = %v, %v", ok, err)
			}

			if cost, _ := hashing.Cost(provider.users[0].password); cost != testCost {
				t.Fatalf("the password wasn't rehashed (cost %d)", cost)
			}

			if queued := c.QueuedCookies(); len(queued) != 0 {
				t.Fatalf("queued %v for a cookie that didn't recall Ada", queued)
			}
		})
	}
}

// A match signs the user in, dispatches Login and leaves the timebox early.
// The recaller binds to the hash as saved, so a sign-in that rehashed still
// remembers the user.
func TestAttemptSignsInAndDispatchesLogin(t *testing.T) {
	dispatcher := events.NewDispatcher(nil)
	dispatcher.Fake(Login[stubUser]{})
	provider := users(t, testCost+1)
	// Long enough that only a sign-in leaving early beats it.
	const long = 5 * time.Second
	guard := newTestGuard(t, provider, GuardConfig{Timebox: long, Events: dispatcher})
	c := sessionRequest(t)

	started := time.Now()
	ok, err := guard.Attempt(c, Credentials{Email: adaEmail, Password: "secret"}, true)
	if err != nil || !ok {
		t.Fatalf("Attempt = %v, %v", ok, err)
	}

	if elapsed := time.Since(started); elapsed >= long {
		t.Fatalf("a sign-in waited out the timebox (%v)", elapsed)
	}

	if id, _ := session.Key[int64](guard.LoginKey()).Get(session.FromContext(c)); id != 1 || User[stubUser](c).id != 1 {
		t.Fatalf("signed in as %d, want Ada", id)
	}

	eventstest.AssertDispatched(t, dispatcher, func(e Login[stubUser]) bool { return e.User.id == 1 && e.Remember })

	ada := provider.users[0]
	recaller := recallerFor(guard, ada.id, ada.token, ada.password)
	if queued := c.QueuedCookies(); len(queued) != 1 || queued[0].Value != recaller {
		t.Fatalf("queued %v, want the recaller %q", queued, recaller)
	}
}

// A miss takes the whole timebox and signs no one in.
func TestAttemptSignsNoOneInOnAMiss(t *testing.T) {
	dispatcher := events.NewDispatcher(nil)
	dispatcher.Fake(Login[stubUser]{})
	guard := newTestGuard(t, users(t, testCost), GuardConfig{Timebox: timebox, Events: dispatcher})
	c := sessionRequest(t)

	started := time.Now()
	ok, err := guard.Attempt(c, Credentials{Email: adaEmail, Password: "wrong"}, true)
	if elapsed := time.Since(started); elapsed < timebox {
		t.Fatalf("answered in %v, under the %v timebox", elapsed, timebox)
	}

	if err != nil || ok {
		t.Fatalf("Attempt = %v, %v; want a miss", ok, err)
	}

	if _, signedIn := session.Key[any](guard.LoginKey()).Get(session.FromContext(c)); signedIn || ID(c) != nil || len(c.QueuedCookies()) != 0 {
		t.Fatal("a miss signed someone in")
	}

	eventstest.AssertDispatchedTimes[Login[stubUser]](t, dispatcher, 0)
}

// The current_password rule checks the value against the user bound in the
// request's context: only their own password passes.
func TestCurrentPasswordIsTheSignedInUsersPassword(t *testing.T) {
	provider := users(t, testCost)
	ada, grace := provider.users[0], provider.users[1]

	for _, tc := range []struct {
		name   string
		user   Authenticatable
		value  any
		passes bool
	}{
		{"their password", ada, "secret", true},
		{"a wrong password", ada, "secret ", false},
		{"a non-string", ada, json.Number("1"), false},
		{"a user without a password", grace, "secret", false},
		{"a guest", nil, "secret", false},
	} {
		ctx := WithUser(t.Context(), tc.user)
		v := validation.New(ctx, validation.Input{"current_password": tc.value}, rule.Set{"current_password": {CurrentPassword}}, validation.Options{})

		passed, err := v.Validate()
		if err != nil || passed != tc.passes {
			t.Errorf("%s: passed = %v, %v", tc.name, passed, err)
		}

		if !tc.passes && v.Errors().Get("current_password")[0] != "The password is incorrect." {
			t.Errorf("%s: errors = %v", tc.name, v.Errors().Get("current_password"))
		}
	}

	// A context the guard never bound is a guest's.
	unbound := validation.New(t.Context(), validation.Input{"current_password": "secret"}, rule.Set{"current_password": {CurrentPassword}}, validation.Options{})
	if passed, err := unbound.Validate(); passed || err != nil {
		t.Errorf("unbound: passed = %v, %v", passed, err)
	}
}
