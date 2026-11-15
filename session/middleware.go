package session

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/web"
)

// Config is the session cookie and table settings.
type Config struct {
	Cookie   string        // e.g. "app-session"
	Lifetime time.Duration // SESSION_LIFETIME minutes
	Path     string
	Domain   string
	Secure   bool
	SameSite http.SameSite
	Table    string
	// Lottery is the GC chance: [2, 100] sweeps expired rows on 2% of requests.
	Lottery [2]int
	// UserID, when set, fills the row's user_id column.
	UserID UserResolver
}

// UserResolver reports the signed-in user's id for the row's user_id
// column, read at save time, after any sign-in or out.
type UserResolver func(c *web.Context, s *Session) *int64

type sessionKey struct{}

// With binds s as ctx's session, as StartSession does or a test seeding one.
func With(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// FromContext is ctx's session (bound by StartSession or With); nil when none
// is bound.
func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey{}).(*Session)

	return s
}

// StartSession loads the session named by the decrypted cookie, runs the
// request, then saves the row and re-sends the cookie, so every request
// slides the session's lifetime. It must run inside the cookies middleware.
func StartSession(config Config) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		s, err := load(c, config)
		if err != nil {
			return nil, err
		}

		c.WithContext(With(c.Context(), s))

		res, err := web.ToResponse(next(c))

		// The previous URL is a page's, never an XHR's or a prefetch's.
		if c.Method() == http.MethodGet && c.RouteMatched() && !c.IsXHR() && c.Header("Purpose") != "prefetch" {
			s.SetPreviousURL(c.FullURL(), c.RouteName())
		}

		if saveErr := save(c, config, s); saveErr != nil {
			return nil, saveErr
		}

		//nolint:gosec // G404: the expired-row sweep lottery needs no unpredictability
		if config.Lottery[1] > 0 && rand.IntN(config.Lottery[1]) < config.Lottery[0] {
			// Best effort: a failed sweep only leaves expired rows for the next one.
			if _, sweepErr := db.Table[struct{}](c, config.Table).WhereOp("last_activity", "<=", clock.Now(c).Add(-config.Lifetime).Unix()).Delete(); sweepErr != nil {
				slog.WarnContext(c, "session: sweeping expired rows", "error", sweepErr)
			}
		}

		//nolint:gosec // G124: Secure and SameSite follow the session config (Secure is off on plain-http dev)
		return res.WithCookie(&http.Cookie{
			Name:     config.Cookie,
			Value:    s.ID(),
			Path:     config.Path,
			Domain:   config.Domain,
			Expires:  clock.Now(c).Add(config.Lifetime),
			MaxAge:   int(config.Lifetime.Seconds()),
			Secure:   config.Secure,
			HttpOnly: true,
			SameSite: config.SameSite,
		}), err
	}
}

type row struct {
	Payload      string `db:"payload"`
	LastActivity int64  `db:"last_activity"`
}

// load continues a valid id's live row; a valid id whose row expired or
// vanished keeps the id with empty attributes; anything else starts afresh.
func load(c *web.Context, config Config) (*Session, error) {
	id := c.Cookie(config.Cookie)
	if !validID(id) {
		return newSession(), nil
	}

	stored, err := db.Table[row](c, config.Table).Select("payload", "last_activity").WhereKey(id).First()
	if err != nil {
		return nil, err
	}

	s := &Session{id: id, attributes: map[string]any{}, exists: stored != nil}
	if stored != nil && stored.LastActivity > clock.Now(c).Add(-config.Lifetime).Unix() {
		if attributes, err := decode(stored.Payload); err == nil {
			s.attributes = attributes
		}
	}

	if s.Token() == "" {
		s.RegenerateToken()
	}

	return s, nil
}

// save ages the flash data, then writes the row. A row that existed at load
// is only UPDATEd, so one deleted meanwhile (a racing logout) stays deleted.
func save(c *web.Context, config Config, s *Session) error {
	s.ageFlash()

	payload, err := encode(s.attributes)
	if err != nil {
		return fmt.Errorf("session: encoding: %w", err)
	}

	var userID *int64
	if config.UserID != nil {
		userID = config.UserID(c, s)
	}

	agent := c.Header("User-Agent")
	if len(agent) > 500 {
		agent = agent[:500]
	}

	if s.previousID != "" {
		if _, err := db.Table[struct{}](c, config.Table).WhereKey(s.previousID).Delete(); err != nil {
			return err
		}
	}

	values := db.Values{"user_id": userID, "ip_address": c.IP(), "user_agent": agent, "payload": payload, "last_activity": clock.Now(c).Unix()}
	if s.exists {
		_, err = db.Table[struct{}](c, config.Table).WhereKey(s.ID()).Update(values)

		return err
	}

	// A duplicate id (a concurrent first request) updates instead.
	_, err = db.Table[struct{}](c, config.Table).UpdateOrCreate(db.Values{"id": s.ID()}, values)

	return err
}

// DestroyOtherSessions deletes every session row signed in as userID but
// ctx's own (every one, when ctx has no session), found by the user_id
// column save writes.
func DestroyOtherSessions(ctx context.Context, table string, userID int64) error {
	keep := ""
	if s := FromContext(ctx); s != nil {
		keep = s.ID()
	}

	_, err := db.Table[struct{}](ctx, table).Where("user_id", userID).WhereOp("id", "<>", keep).Delete()

	return err
}
