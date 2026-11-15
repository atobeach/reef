package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/web"
)

var errNoSession = errors.New("auth: no session on this request (StartSession must run first)")

// Authenticate refuses guests. It answers a guest with a
// JSON 401 "Unauthenticated." rather than a redirect to a login page.
func (g *Guard[U]) Authenticate(c *web.Context, next web.Handler) (any, error) {
	resolved := g.bindResolved(c)
	if resolved.err != nil {
		return nil, resolved.err
	}

	if _, ok := resolved.user.(U); !ok {
		return nil, web.Abort(http.StatusUnauthorized, "Unauthenticated.")
	}

	return next(c)
}

// RedirectIfAuthenticated keeps a signed-in user off guest-only routes with
// a 302 to the root, even for JSON callers: an SPA reads it as "already
// signed in".
func (g *Guard[U]) RedirectIfAuthenticated(c *web.Context, next web.Handler) (any, error) {
	resolved := g.bindResolved(c)
	if resolved.err != nil {
		return nil, resolved.err
	}

	if _, ok := resolved.user.(U); ok {
		return web.Redirect(c.Root()), nil
	}

	return next(c)
}

// ResolveUser resolves and binds the request's user before the route runs,
// so ID(ctx) answers anywhere downstream. A failure is logged and bound, so
// the route's Authenticate still fails the request.
func (g *Guard[U]) ResolveUser(c *web.Context, next web.Handler) (any, error) {
	if resolved := g.bindResolved(c); resolved.err != nil {
		slog.WarnContext(c, "auth: resolving the request's user", "error", resolved.err)
	}

	return next(c)
}

// UserID is the id of the user User answers with, where a failed lookup may
// read as a guest (the `sessions.user_id` link, a per-user throttle key),
// logging the failure. Like User, it resolves on a request no resolving
// middleware ran on: a session saved after the CSRF check refused one still
// names its user.
func (g *Guard[U]) UserID(c *web.Context) *int64 {
	user, ok, err := g.User(c)
	if err != nil {
		slog.WarnContext(c, "auth: resolving the request's user", "error", err)

		return nil
	}

	if !ok {
		return nil
	}

	id := user.AuthID()

	return &id
}

// RequirePassword answers 423 unless the password was confirmed within
// timeout, so the SPA asks for it before a sensitive change.
func RequirePassword(timeout time.Duration) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		if !passwordConfirmed(c, timeout) {
			return nil, web.Abort(http.StatusLocked, "Password confirmation required.")
		}

		return next(c)
	}
}

// passwordConfirmed reports a confirmation within timeout, in the whole
// seconds the session stores; never confirmed reads as long past.
func passwordConfirmed(c *web.Context, timeout time.Duration) bool {
	s := session.FromContext(c)

	return s != nil && clock.Now(c).Unix()-s.PasswordConfirmedAt().Unix() <= int64(timeout.Seconds())
}
