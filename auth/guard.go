// Package auth resolves the signed-in user: the id stored under the login key
// in the session, falling back to the remember-me (recaller) cookie. A route
// behind Authenticate reads its user with User, the guard checks credentials
// (Validate, Attempt) inside a timebox, and every sign-in dispatches Login.
//
//	user := auth.User[models.User](c)
//	ok, err := guard.Attempt(c, auth.Credentials{Email: email, Password: password}, remember)
//	events.Listen(d, func(ctx context.Context, e auth.Login[models.User]) error { ... })
package auth

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/hashing"
	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/support"
	"github.com/atobeach/reef/web"
)

// rememberFor is the recaller cookie's lifetime: 400 days, the most
// browsers allow.
const rememberFor = 400 * 24 * time.Hour

// Authenticatable is what the guard needs from the user model.
type Authenticatable interface {
	AuthID() int64
	AuthPassword() string
	AuthRememberToken() string
}

// Provider loads and updates the guard's users.
type Provider[U Authenticatable] interface {
	// ByID loads a user; ok is false when the id no longer exists.
	ByID(ctx context.Context, id int64) (user U, ok bool, err error)
	// ByEmail loads the user an address belongs to; ok is false when nobody
	// has it.
	ByEmail(ctx context.Context, email string) (user U, ok bool, err error)
	// SaveRememberToken persists a new remember token for the user.
	SaveRememberToken(ctx context.Context, user U, token string) error
	// SavePassword persists hash as the user's password and returns the user
	// carrying it, so a sign-in after a rehash binds the recaller to the new
	// hash.
	SavePassword(ctx context.Context, user U, hash string) (U, error)
}

// Guard resolves and changes the request's user, loading users of type U
// through its provider.
type Guard[U Authenticatable] struct {
	provider Provider[U]
	config   GuardConfig
	loginKey session.Key[int64]
}

// GuardConfig configures a Guard; NewGuard fills in the defaults shown in
// parentheses.
type GuardConfig struct {
	// LoginKey is the session attribute holding the user id ("login").
	LoginKey string
	// RecallerCookie names the remember-me cookie, id|token|HMAC
	// ("remember").
	RecallerCookie string
	// HashKey is APP_KEY verbatim, "base64:" prefix and all: cookies already
	// issued carry an HMAC keyed with it.
	HashKey  string
	Domain   string
	Secure   bool
	SameSite http.SameSite
	// SessionTable holds the session rows LogoutOtherDevices deletes
	// ("sessions").
	SessionTable string
	// Hasher checks passwords and rehashes one made at another cost.
	Hasher *hashing.Hasher
	// Timebox is the least time a credential check takes, so an unknown
	// address answers no faster than a wrong password (200ms).
	Timebox time.Duration
	// Events receives Login on every sign-in (nil: none).
	Events *events.Dispatcher
}

// Login is dispatched on every sign-in, a remember-me one included.
type Login[U Authenticatable] struct {
	User     U
	Remember bool
}

var (
	// errNoHashKey refuses to boot a guard whose recaller cookies would bind
	// to the password through an HMAC keyed with nothing secret.
	errNoHashKey = errors.New("auth: GuardConfig.HashKey is empty; set it to the app key (APP_KEY)")
	// errNoHasher refuses to boot a guard that couldn't check a password.
	errNoHasher = errors.New("auth: GuardConfig.Hasher is nil; credential checks need one")
)

// NewGuard builds the guard over provider; an empty HashKey or a nil Hasher
// is a boot error.
func NewGuard[U Authenticatable](provider Provider[U], config GuardConfig) (*Guard[U], error) {
	if config.HashKey == "" {
		return nil, errNoHashKey
	}

	if config.Hasher == nil {
		return nil, errNoHasher
	}

	config.LoginKey = cmp.Or(config.LoginKey, "login")
	config.RecallerCookie = cmp.Or(config.RecallerCookie, "remember")
	config.SessionTable = cmp.Or(config.SessionTable, "sessions")
	config.Timebox = cmp.Or(config.Timebox, 200*time.Millisecond)
	if config.SameSite == 0 {
		config.SameSite = http.SameSiteLaxMode
	}

	return &Guard[U]{provider: provider, config: config, loginKey: session.Key[int64](config.LoginKey)}, nil
}

// LoginKey is the session attribute holding the signed-in user's id.
func (g *Guard[U]) LoginKey() string {
	return g.config.LoginKey
}

// RecallerCookie is the remember-me cookie's name.
func (g *Guard[U]) RecallerCookie() string {
	return g.config.RecallerCookie
}

// HashPasswordForCookie is an HMAC of the password hash, so the recaller
// cookie dies when the password changes without carrying the bcrypt hash
// itself.
func (g *Guard[U]) HashPasswordForCookie(passwordHash string) string {
	mac := hmac.New(sha256.New, []byte(g.config.HashKey))
	mac.Write([]byte(passwordHash))

	return hex.EncodeToString(mac.Sum(nil))
}

// dispatchLogin tells the Login listeners of a sign-in; the first error
// fails the request.
func (g *Guard[U]) dispatchLogin(c *web.Context, user U, remember bool) error {
	if g.config.Events == nil {
		return nil
	}

	if err := g.config.Events.Dispatch(c, Login[U]{User: user, Remember: remember}); err != nil {
		return fmt.Errorf("auth: dispatching Login for user %d: %w", user.AuthID(), err)
	}

	return nil
}

type userKey struct{}

// resolvedUser is what a context knows of its signed-in user once resolved:
// the user (nil: a guest), or the error resolving them failed with.
type resolvedUser struct {
	user Authenticatable
	err  error
}

// WithUser binds user (nil: a guest) as ctx's signed-in user, as the guard
// does for a request or a job or test does for the user it acts as.
func WithUser(ctx context.Context, user Authenticatable) context.Context {
	return context.WithValue(ctx, userKey{}, resolvedUser{user: user})
}

// ID is ctx's signed-in user id: nil for a guest, outside a request, before
// ResolveUser or Authenticate ran, or when resolving the user failed.
func ID(ctx context.Context) *int64 {
	resolved, _ := ctx.Value(userKey{}).(resolvedUser)
	if resolved.user == nil {
		return nil
	}

	id := resolved.user.AuthID()

	return &id
}

// User is the signed-in user on a route behind Authenticate, kept current by
// Login, SetUser and Logout. It panics where no user is signed in.
func User[U Authenticatable](c *web.Context) U {
	resolved, _ := c.Value(userKey{}).(resolvedUser)

	user, ok := resolved.user.(U)
	if !ok {
		panic(fmt.Sprintf("auth: User[%s] with no signed-in user; the route must run Authenticate", reflect.TypeFor[U]()))
	}

	return user
}

// User is the session's user, else a valid remember-me cookie's (which signs
// them back in); ok is false for a guest. It reads the answer ResolveUser,
// Authenticate or RedirectIfAuthenticated bound, failure included; before
// any of them ran it resolves for this call and binds nothing, so ID(c)
// still answers nil (a remember-me sign-in binds, as Login does).
func (g *Guard[U]) User(c *web.Context) (user U, ok bool, err error) {
	resolved, bound := c.Value(userKey{}).(resolvedUser)
	if !bound {
		resolved = g.resolveUser(c)
	}

	user, ok = resolved.user.(U)

	return user, ok, resolved.err
}

// bindResolved is the request's resolved user, resolved and bound for
// everything downstream unless an answer is bound already: the one place a
// resolved user is bound, so a failure fails every later caller too.
func (g *Guard[U]) bindResolved(c *web.Context) resolvedUser {
	if resolved, bound := c.Value(userKey{}).(resolvedUser); bound {
		return resolved
	}

	resolved := g.resolveUser(c)
	c.WithContext(context.WithValue(c.Context(), userKey{}, resolved))

	return resolved
}

// resolveUser resolves the request's user into what a context knows of them.
func (g *Guard[U]) resolveUser(c *web.Context) resolvedUser {
	user, ok, err := g.resolve(c)
	if err != nil {
		return resolvedUser{err: err}
	}

	if !ok {
		return resolvedUser{}
	}

	return resolvedUser{user: user}
}

// bindUser makes user (nil: a guest) the request's for everything
// downstream.
func bindUser(c *web.Context, user Authenticatable) {
	c.WithContext(WithUser(c.Context(), user))
}

// resolve finds the request's user: the session's, else the recaller's.
func (g *Guard[U]) resolve(c *web.Context) (user U, ok bool, err error) {
	s := session.FromContext(c)
	if s == nil {
		return user, false, nil
	}

	if id, stored := g.loginKey.Get(s); stored {
		if user, ok, err = g.provider.ByID(c, id); err != nil || ok {
			return user, ok, err
		}
	}

	return g.viaRemember(c, s)
}

// recaller is a parsed remember-me cookie.
type recaller struct {
	id    int64
	token string
	hash  string
}

func parseRecaller(value string) (recaller, bool) {
	parts := strings.SplitN(value, "|", 3)
	if len(parts) != 3 || parts[1] == "" {
		return recaller{}, false
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)

	return recaller{id: id, token: parts[1], hash: parts[2]}, err == nil
}

// recalls reports whether cookie signs user back in. Token and hash are
// compared in constant time, the hash against the CURRENT password, so a
// changed or removed password kills every earlier cookie.
func (g *Guard[U]) recalls(cookie recaller, user U) bool {
	if cookie.id != user.AuthID() {
		return false
	}

	if stored := user.AuthRememberToken(); stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(cookie.token)) != 1 {
		return false
	}

	password := user.AuthPassword()

	return password != "" && subtle.ConstantTimeCompare([]byte(g.HashPasswordForCookie(password)), []byte(cookie.hash)) == 1
}

// viaRemember signs the user in from the remember-me cookie, when it
// recalls them.
func (g *Guard[U]) viaRemember(c *web.Context, s *session.Session) (user U, ok bool, err error) {
	var guest U

	cookie, parsed := parseRecaller(c.Cookie(g.config.RecallerCookie))
	if !parsed {
		return guest, false, nil
	}

	if user, ok, err = g.provider.ByID(c, cookie.id); err != nil || !ok || !g.recalls(cookie, user) {
		return guest, false, err
	}

	g.loginKey.Put(s, user.AuthID())
	s.RegenerateWithToken(true)

	bindUser(c, user)
	if err := g.dispatchLogin(c, user, true); err != nil {
		return guest, false, err
	}

	return user, true, nil
}

// Login signs user in under a regenerated session (against fixation),
// issuing the recaller cookie when remembering.
func (g *Guard[U]) Login(c *web.Context, user U, remember bool) error {
	s := session.FromContext(c)
	if s == nil {
		return errNoSession
	}

	s.RegenerateWithToken(true)
	g.loginKey.Put(s, user.AuthID())

	if remember {
		token := user.AuthRememberToken()
		if token == "" {
			token = support.RandomString(60)
			if err := g.provider.SaveRememberToken(c, user, token); err != nil {
				return err
			}
		}

		g.queueRecaller(c, user, token)
	}

	bindUser(c, user)

	return g.dispatchLogin(c, user, remember)
}

// queueRecaller queues the remember-me cookie, id|token|HMAC(password hash).
func (g *Guard[U]) queueRecaller(c *web.Context, user U, token string) {
	value := strconv.FormatInt(user.AuthID(), 10) + "|" + token + "|" + g.HashPasswordForCookie(user.AuthPassword())
	c.QueueCookie(g.recallerCookie(value, int(rememberFor.Seconds()), clock.Now(c).Add(rememberFor)))
}

// SetUser replaces the request's resolved user after its row changed,
// leaving the session alone.
func (g *Guard[U]) SetUser(c *web.Context, user U) {
	bindUser(c, user)
}

// Logout signs the user out, cycling the remember token so stolen recaller
// cookies die.
func (g *Guard[U]) Logout(c *web.Context) error {
	user, ok, err := g.User(c)
	if err != nil {
		return err
	}

	if ok && user.AuthRememberToken() != "" {
		if err := g.provider.SaveRememberToken(c, user, support.RandomString(60)); err != nil {
			return err
		}
	}

	if s := session.FromContext(c); s != nil {
		s.Invalidate()
	}

	// Expired with the attributes it was set with, or a browser keeps a
	// domain-scoped cookie alive.
	c.QueueCookie(g.recallerCookie("", -1, time.Unix(0, 0)))
	bindUser(c, nil)

	return nil
}

// LogoutOtherDevices signs user out everywhere but this request, cycling the
// remember token and deleting their other session rows.
func (g *Guard[U]) LogoutOtherDevices(c *web.Context, user U) error {
	if err := g.provider.SaveRememberToken(c, user, support.RandomString(60)); err != nil {
		return err
	}

	if err := session.DestroyOtherSessions(c, g.config.SessionTable, user.AuthID()); err != nil {
		return fmt.Errorf("auth: ending user %d's other sessions: %w", user.AuthID(), err)
	}

	return nil
}

func (g *Guard[U]) recallerCookie(value string, maxAge int, expires time.Time) *http.Cookie {
	//nolint:gosec // G124: HttpOnly always; Secure and SameSite follow the session config (Secure is off on plain-http dev)
	return &http.Cookie{
		Name:     g.config.RecallerCookie,
		Value:    value,
		Path:     "/",
		Domain:   g.config.Domain,
		Expires:  expires,
		MaxAge:   maxAge,
		Secure:   g.config.Secure,
		HttpOnly: true,
		SameSite: g.config.SameSite,
	}
}
