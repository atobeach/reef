package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/atobeach/reef/hashing"
	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/web"
)

type stubUser struct {
	id       int64
	email    string
	password string
	token    string
}

func (u stubUser) AuthID() int64             { return u.id }
func (u stubUser) AuthPassword() string      { return u.password }
func (u stubUser) AuthRememberToken() string { return u.token }

// stubProvider keeps the guard's users in memory, and the guard's writes
// land on them.
type stubProvider struct{ users []stubUser }

func (p *stubProvider) ByID(_ context.Context, id int64) (stubUser, bool, error) {
	return p.find(func(u stubUser) bool { return u.id == id })
}

func (p *stubProvider) ByEmail(_ context.Context, email string) (stubUser, bool, error) {
	return p.find(func(u stubUser) bool { return u.email == email })
}

func (p *stubProvider) SaveRememberToken(_ context.Context, user stubUser, token string) error {
	user.token = token
	p.save(user)

	return nil
}

func (p *stubProvider) SavePassword(_ context.Context, user stubUser, hash string) (stubUser, error) {
	user.password = hash
	p.save(user)

	return user, nil
}

func (p *stubProvider) find(match func(stubUser) bool) (stubUser, bool, error) {
	i := slices.IndexFunc(p.users, match)
	if i < 0 {
		return stubUser{}, false, nil
	}

	return p.users[i], true, nil
}

func (p *stubProvider) save(user stubUser) {
	if i := slices.IndexFunc(p.users, func(u stubUser) bool { return u.id == user.id }); i >= 0 {
		p.users[i] = user
	}
}

// testCost is bcrypt's minimum cost, the guard's in these tests.
const testCost = bcrypt.MinCost

func testGuard(t *testing.T) *Guard[stubUser] {
	t.Helper()

	return newTestGuard(t, &stubProvider{}, GuardConfig{})
}

// countingProvider counts the guard's ByID lookups.
type countingProvider struct {
	*stubProvider
	byID int
}

func (p *countingProvider) ByID(ctx context.Context, id int64) (stubUser, bool, error) {
	p.byID++

	return p.stubProvider.ByID(ctx, id)
}

// newTestGuard builds a guard over provider with config, keyed and hashing
// at testCost.
func newTestGuard(t *testing.T, provider Provider[stubUser], config GuardConfig) *Guard[stubUser] {
	t.Helper()

	config.HashKey = "base64:key"
	config.Hasher = hashing.New(testCost)

	guard, err := NewGuard(provider, config)
	if err != nil {
		t.Fatal(err)
	}

	return guard
}

// A guard refuses to boot without the key its recaller HMAC is keyed with,
// or without a hasher to check passwords.
func TestNewGuardRefusesAMissingKeyOrHasher(t *testing.T) {
	hasher := hashing.New(testCost)

	for _, tt := range []struct {
		name   string
		config GuardConfig
		want   error
	}{
		{"no hash key", GuardConfig{Hasher: hasher}, errNoHashKey},
		{"no hasher", GuardConfig{HashKey: "base64:key"}, errNoHasher},
		{"both", GuardConfig{HashKey: "base64:key", Hasher: hasher}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewGuard(&stubProvider{}, tt.config); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// ID reads the user a context runs for: a job or test binds one with
// WithUser, as the guard does for a request.
func TestIDIsTheBoundUserOrNone(t *testing.T) {
	if id := ID(t.Context()); id != nil {
		t.Fatalf("unbound id = %d", *id)
	}

	if id := ID(WithUser(t.Context(), stubUser{id: 42})); id == nil || *id != 42 {
		t.Fatalf("bound id = %v", id)
	}

	if id := ID(WithUser(WithUser(t.Context(), stubUser{id: 42}), nil)); id != nil {
		t.Fatalf("a guest bound over a user = %d", *id)
	}
}

// User follows the request's user as the guard changes it, and panics where
// no user is signed in: a route that skipped Authenticate.
func TestUserIsTheRequestsCurrentUser(t *testing.T) {
	guard := testGuard(t)

	c := web.NewContext(t.Context(), http.MethodGet, "/")
	assertUserPanics(t, c)

	guard.SetUser(c, stubUser{id: 7})
	if got := User[stubUser](c); got.id != 7 {
		t.Fatalf("User = %+v, want the user SetUser bound", got)
	}

	if err := guard.Logout(c); err != nil {
		t.Fatal(err)
	}

	assertUserPanics(t, c)
}

// A read binds nothing: before any resolving middleware ran, User resolves
// for its caller and ID(c) stays unanswered, while UserID still names the
// user (a session saved after the CSRF check refused the request). ResolveUser
// binds the answer, and User reads it back without another lookup.
func TestUserReadsAndOnlyTheMiddlewareBinds(t *testing.T) {
	provider := &countingProvider{stubProvider: &stubProvider{users: []stubUser{{id: 7}}}}
	guard := newTestGuard(t, provider, GuardConfig{})
	c := sessionRequest(t)
	session.Key[int64](guard.LoginKey()).Put(session.FromContext(c), 7)

	if user, ok, err := guard.User(c); err != nil || !ok || user.id != 7 {
		t.Fatalf("User = %+v, %v, %v; want user 7", user, ok, err)
	}

	if id := ID(c); id != nil {
		t.Fatalf("ID = %d after a read; User must bind nothing", *id)
	}

	if id := guard.UserID(c); id == nil || *id != 7 {
		t.Fatalf("UserID = %v, want 7", id)
	}

	var downstream *int64
	if _, err := guard.ResolveUser(c, func(c *web.Context) (any, error) {
		downstream = ID(c)

		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	if downstream == nil || *downstream != 7 {
		t.Fatalf("ID downstream of ResolveUser = %v, want 7", downstream)
	}

	lookups := provider.byID
	if user, ok, err := guard.User(c); err != nil || !ok || user.id != 7 || provider.byID != lookups {
		t.Fatalf("User = %+v, %v, %v after %d more lookups; want the bound user 7", user, ok, err, provider.byID-lookups)
	}
}

func assertUserPanics(t *testing.T, c *web.Context) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("User returned for a guest; it must panic")
		}
	}()

	User[stubUser](c)
}
