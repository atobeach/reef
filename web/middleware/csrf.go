package middleware

import (
	"crypto/subtle"
	"net/http"
	"slices"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/cookie"
	"github.com/atobeach/reef/crypt"
	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/web"
)

// XSRFCookie is the readable token cookie the SPA echoes back as X-XSRF-TOKEN.
const XSRFCookie = "XSRF-TOKEN"

// CsrfConfig is the session cookie settings the XSRF-TOKEN cookie copies,
// and the encrypter an echoed X-XSRF-TOKEN header is opened with.
type CsrfConfig struct {
	Encrypter *crypt.Encrypter
	Lifetime  time.Duration
	Path      string
	Domain    string
	Secure    bool
	SameSite  http.SameSite
	// Except are path patterns (Context.Is) never verified.
	Except []string
}

// TokenMismatch is the CSRF failure: a 419 "CSRF token mismatch." (Page
// Expired, a status net/http doesn't name).
func TokenMismatch() error {
	return web.Abort(419, "CSRF token mismatch.")
}

// PreventRequestForgery is the CSRF check for cookie-authenticated routes.
// Reads, excluded paths, same-origin fetches (Sec-Fetch-Site) and requests
// carrying the session's token pass, and get an XSRF-TOKEN cookie with the
// token as it stands after the request (login regenerates it). It must run
// inside Cookies, which seals the XSRF-TOKEN cookie, and StartSession, whose
// token it checks.
func PreventRequestForgery(config CsrfConfig) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		s := session.FromContext(c)
		if s == nil {
			return nil, TokenMismatch()
		}

		if !isReading(c) && !c.Is(config.Except...) && c.Header("Sec-Fetch-Site") != "same-origin" && !tokensMatch(c, s, config.Encrypter) {
			return nil, TokenMismatch()
		}

		res, err := web.ToResponse(next(c))

		//nolint:gosec // G124: not HttpOnly by design (the SPA reads it); Secure and SameSite follow the session config
		return res.WithCookie(&http.Cookie{
			Name:     XSRFCookie,
			Value:    s.Token(),
			Path:     config.Path,
			Domain:   config.Domain,
			Expires:  clock.Now(c).Add(config.Lifetime),
			MaxAge:   int(config.Lifetime.Seconds()),
			Secure:   config.Secure,
			HttpOnly: false,
			SameSite: config.SameSite,
		}), err
	}
}

func isReading(c *web.Context) bool {
	return slices.Contains([]string{http.MethodHead, http.MethodGet, http.MethodOptions}, c.Method())
}

// tokensMatch compares in constant time against the token from `_token`,
// X-CSRF-TOKEN, or the encrypted X-XSRF-TOKEN (the cookie value echoed back).
func tokensMatch(c *web.Context, s *session.Session, encrypter *crypt.Encrypter) bool {
	sessionToken := s.Token()
	token := requestToken(c, encrypter)

	return sessionToken != "" && token != "" && subtle.ConstantTimeCompare([]byte(sessionToken), []byte(token)) == 1
}

func requestToken(c *web.Context, encrypter *crypt.Encrypter) string {
	if token := c.String("_token"); token != "" {
		return token
	}

	if token := c.Header("X-CSRF-TOKEN"); token != "" {
		return token
	}

	header := c.Header("X-XSRF-TOKEN")
	if header == "" {
		return ""
	}

	// The header is the XSRF-TOKEN cookie echoed back, so it must open as
	// that cookie: a value sealed for another cookie's name is refused.
	token, ok := cookie.Decrypt(encrypter, XSRFCookie, header)
	if !ok {
		return ""
	}

	return token
}
