package auth

import (
	"context"
	"testing"
	"time"

	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/web"
)

// probeTime is the instant every probe request runs at.
var probeTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// probe is who a request comes from: signed in as user (nil: a guest), with
// a session holding attributes (nil: no session at all).
type probe struct {
	user       *stubUser
	attributes map[string]any
}

// serve sends GET / through middleware for p at probeTime. The route answers
// {"passed":true}, and an error reaching the exception reporter fails the
// test: these refusals are expected answers, never incidents.
func serve(t *testing.T, guard *Guard[stubUser], middleware web.Middleware, p probe) *testkit.TestResponse {
	t.Helper()

	router := web.NewRouter()
	router.Get("/", func(*web.Context) (any, error) { return map[string]bool{"passed": true}, nil }).Middleware(middleware)

	seed := func(c *web.Context, next web.Handler) (any, error) {
		if p.attributes != nil {
			s, err := session.Decode("probe", "")
			if err != nil {
				return nil, err
			}
			for key, value := range p.attributes {
				session.Key[any](key).Put(s, value)
			}
			c.WithContext(session.With(c.Context(), s))
		}

		if p.user != nil {
			guard.SetUser(c, *p.user)
		}

		return next(c)
	}

	bind := func(ctx context.Context) context.Context {
		ctx, _ = clocktest.FrozenAt(ctx, probeTime)

		return ctx
	}
	kernel, err := web.NewKernel(bind, web.KernelConfig{
		Router:     router,
		Middleware: []web.Middleware{seed},
		Report:     func(_ *web.Context, err error) { t.Errorf("reported %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}

	return testkit.NewClient(t, kernel).WithHeader("Accept", "application/json").Get("/")
}

func TestAuthenticateRefusesGuests(t *testing.T) {
	guard := testGuard(t)

	for _, tt := range []struct {
		name    string
		p       probe
		refused bool
	}{
		{"a guest without a session", probe{}, true},
		{"a guest with a session", probe{attributes: map[string]any{}}, true},
		{"a signed-in user", probe{user: &stubUser{id: 7}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := serve(t, guard, guard.Authenticate, tt.p)
			if tt.refused {
				res.AssertUnauthorized().AssertBody(`{"message":"Unauthenticated."}`)

				return
			}

			res.AssertOk().AssertBody(`{"passed":true}`)
		})
	}
}

// A signed-in user is sent to the site root, JSON caller or not.
func TestRedirectIfAuthenticatedSendsUsersToTheRoot(t *testing.T) {
	guard := testGuard(t)

	for _, tt := range []struct {
		name       string
		p          probe
		redirected bool
	}{
		{"a signed-in user", probe{user: &stubUser{id: 7}}, true},
		{"a guest", probe{attributes: map[string]any{}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := serve(t, guard, guard.RedirectIfAuthenticated, tt.p)
			if tt.redirected {
				res.AssertStatus(302).AssertRedirect("http://example.com")

				return
			}

			res.AssertOk().AssertBody(`{"passed":true}`)
		})
	}
}

// The confirmation is fresh for timeout, counted in the whole seconds the
// session stores; past it, or never confirmed, the caller gets the 423.
func TestRequirePasswordNeedsAFreshConfirmation(t *testing.T) {
	const timeout = 3 * time.Hour
	guard := testGuard(t)
	confirmed := func(ago time.Duration) probe {
		return probe{user: &stubUser{id: 7}, attributes: map[string]any{"auth.password_confirmed_at": probeTime.Add(-ago).Unix()}}
	}

	for _, tt := range []struct {
		name    string
		p       probe
		refused bool
	}{
		{"no session", probe{user: &stubUser{id: 7}}, true},
		{"never confirmed", probe{user: &stubUser{id: 7}, attributes: map[string]any{}}, true},
		{"confirmed just now", confirmed(0), false},
		{"confirmed exactly the timeout ago", confirmed(timeout), false},
		{"confirmed a second past the timeout", confirmed(timeout + time.Second), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := serve(t, guard, RequirePassword(timeout), tt.p)
			if tt.refused {
				res.AssertStatus(423).AssertBody(`{"message":"Password confirmation required."}`)

				return
			}

			res.AssertOk().AssertBody(`{"passed":true}`)
		})
	}
}
