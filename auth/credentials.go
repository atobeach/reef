package auth

import (
	"fmt"

	"github.com/atobeach/reef/hashing"
	"github.com/atobeach/reef/validation/rule"
	"github.com/atobeach/reef/web"
)

// Credentials are what a user signs in with: the address, looked up exactly
// as given, and the password as typed.
type Credentials struct {
	Email    string
	Password string
}

// CurrentPassword is the `current_password` rule: the value is the signed-in
// user's password. A guest, and a user without a password (signed up
// through a provider), never pass.
var CurrentPassword = rule.Custom("current_password", func(f *rule.Field) (bool, error) {
	value, ok := f.Value.(string)
	resolved, _ := f.Ctx.Value(userKey{}).(resolvedUser)

	return ok && resolved.user != nil && hashing.Check(value, resolved.user.AuthPassword()), nil
}, "The password is incorrect.")

// Validate checks creds without signing in: ok is false for an unknown
// address, a wrong password and a password-less user alike, and every answer
// takes at least the timebox so none can be timed. A match made at another
// cost is rehashed.
func (g *Guard[U]) Validate(c *web.Context, creds Credentials) (user U, ok bool, err error) {
	ok, err = Timebox(c, g.config.Timebox, func(func()) (bool, error) {
		found, matched, err := g.check(c, creds)
		user = found

		return matched, err
	})

	return user, ok, err
}

// Attempt signs in the user creds match, remembered when asked; false is a
// miss. A miss takes at least the timebox; a sign-in reveals nothing new, so
// it leaves early.
func (g *Guard[U]) Attempt(c *web.Context, creds Credentials, remember bool) (bool, error) {
	return Timebox(c, g.config.Timebox, func(returnEarly func()) (bool, error) {
		user, ok, err := g.check(c, creds)
		if err != nil || !ok {
			return false, err
		}

		if err := g.Login(c, user, remember); err != nil {
			return false, err
		}

		returnEarly()

		return true, nil
	})
}

// check matches creds against the user's hash; a password-less user never
// matches. A hash made at another cost is replaced, so raising the bcrypt
// cost upgrades accounts as they sign in.
func (g *Guard[U]) check(c *web.Context, creds Credentials) (user U, ok bool, err error) {
	var none U

	user, ok, err = g.provider.ByEmail(c, creds.Email)
	if err != nil || !ok {
		return none, false, err
	}

	if !hashing.Check(creds.Password, user.AuthPassword()) {
		return none, false, nil
	}

	if !g.config.Hasher.NeedsRehash(user.AuthPassword()) {
		return user, true, nil
	}

	if user, err = g.rehash(c, user, creds.Password); err != nil {
		return none, false, err
	}

	return user, true, nil
}

// rehash saves password hashed at the guard's cost and hands back the user
// carrying the new hash.
func (g *Guard[U]) rehash(c *web.Context, user U, password string) (U, error) {
	hash, err := g.config.Hasher.Make(password)
	if err != nil {
		return user, fmt.Errorf("auth: rehashing user %d's password: %w", user.AuthID(), err)
	}

	rehashed, err := g.provider.SavePassword(c, user, hash)
	if err != nil {
		return user, err
	}

	// The recaller binds to the hash, so this device's cookie is re-issued or
	// a password confirmation would sign it out. Only a cookie that already
	// recalled the user is, or a forged one would skip the two-factor
	// challenge after Validate.
	if cookie, parsed := parseRecaller(c.Cookie(g.config.RecallerCookie)); parsed && g.recalls(cookie, user) {
		g.queueRecaller(c, rehashed, cookie.token)
	}

	return rehashed, nil
}
