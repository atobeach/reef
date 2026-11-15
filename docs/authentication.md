# Authentication

- [Introduction](#introduction)
    - [Database Considerations](#database-considerations)
- [Authentication Quickstart](#authentication-quickstart)
    - [Building the Guard](#building-the-guard)
    - [Retrieving the Authenticated User](#retrieving-the-authenticated-user)
    - [Protecting Routes](#protecting-routes)
    - [Redirecting Authenticated Users](#redirecting-authenticated-users)
    - [Login Throttling](#login-throttling)
- [Manually Authenticating Users](#manually-authenticating-users)
    - [Timeboxing Your Own Checks](#timeboxing-your-own-checks)
    - [Remembering Users](#remembering-users)
    - [Validating Credentials Without Signing In](#validating-credentials-without-signing-in)
    - [Other Authentication Methods](#other-authentication-methods)
- [Logging Out](#logging-out)
    - [Invalidating Sessions on Other Devices](#invalidating-sessions-on-other-devices)
- [Password Confirmation](#password-confirmation)
    - [Configuration](#configuration)
    - [Routing](#routing)
    - [Requiring Confirmation](#requiring-confirmation)
    - [Validating the Current Password](#validating-the-current-password)
- [The User Outside Controllers](#the-user-outside-controllers)
    - [Resolving the User Up Front](#resolving-the-user-up-front)
    - [Acting as a User in Jobs and Tests](#acting-as-a-user-in-jobs-and-tests)
- [Adding Custom User Providers](#adding-custom-user-providers)
    - [The User Provider Contract](#the-user-provider-contract)
    - [The Authenticatable Contract](#the-authenticatable-contract)
- [Automatic Password Rehashing](#automatic-password-rehashing)
- [Events](#events)
- [Available Methods](#available-methods)
    - [Package Functions](#package-functions)
    - [Guard Methods](#guard-methods)
    - [Types](#types)

## Introduction

Many web applications provide a way for their users to sign in and out. Doing this well is easy to get subtly wrong, so reef's `auth` package gives you the pieces you need: a session guard that knows who the current user is, middleware that keeps guests (or signed-in users) off a route, credential checks that can't be timed, "remember me" cookies, password confirmation and a `Login` event.

At its core, reef's authentication is made up of a guard and a provider, as Laravel's is. The guard, `auth.Guard[U]`, decides who the request's user is: the user id stored in the session and, failing that, a valid remember-me cookie. The provider, `auth.Provider[U]`, loads users from your persistent storage and saves the few things the guard changes. `U` is your own user model; the guard only needs the three methods of `auth.Authenticatable` from it.

There is no guard registry, `Auth` facade or `config/auth.php`. You build the guard once, in your application's providers, and hand it to the controllers and middleware that need it as a field. The guard is built for a JSON API: a guest on a protected route gets a `401` JSON response, never a redirect to a login page.

> [!NOTE]
> The guard keeps its state in the [session](session.md), so `session.StartSession` must run before any of the guard's middleware or methods. Without a session every request is a guest, and `Login` returns an error.

### Database Considerations

Your users table needs an integer primary key, a `password` column of at least 60 characters (bcrypt hashes are 60 bytes long; make it a nullable `varchar(255)` if social-only accounts have no password) and a nullable `remember_token` column of at least 60 characters (reef mints 60-character tokens; Laravel's default `varchar(100)` is fine).

If you would like to [sign users out of their other devices](#invalidating-sessions-on-other-devices), your sessions table must also record which user each session belongs to, in a `user_id` column. The session middleware fills it when you give `session.Config` a `UserID` resolver; see [the sessions table](session.md#the-sessions-table) and [logging out other devices](session.md#logging-out-other-devices).

## Authentication Quickstart

### Building the Guard

You build the guard with `auth.NewGuard`, passing your user provider and an `auth.GuardConfig`. Build the guard once, at boot, over your users table (`accounts.UserProvider` is the [user provider](#adding-custom-user-providers) shown further down):

```go
import (
	"example.com/flights/app/accounts"
	"github.com/atobeach/reef/auth"
	"github.com/atobeach/reef/hashing"
)

hasher := hashing.New(cfg.Auth.BcryptRounds)

guard, err := auth.NewGuard(accounts.UserProvider{}, auth.GuardConfig{
	LoginKey:       cfg.Auth.LoginKey,
	RecallerCookie: cfg.Auth.RecallerCookie,
	HashKey:        cfg.App.Key,
	Domain:         cfg.Session.Domain,
	Secure:         cfg.Session.Secure,
	SameSite:       cfg.Session.SameSite,
	SessionTable:   cfg.Session.Table,
	Hasher:         hasher,
	Timebox:        cfg.Auth.LoginTimebox,
	Events:         dispatcher,
})
if err != nil {
	return nil, err
}
```

`NewGuard` returns an error, rather than a guard you can't trust, when `HashKey` is empty or `Hasher` is nil. Every other option has a default:

| Option | Default | Description |
| --- | --- | --- |
| `LoginKey` | `"login"` | The session attribute holding the signed-in user's id. |
| `RecallerCookie` | `"remember"` | The name of the remember-me cookie. |
| `HashKey` | required | Your `APP_KEY`, verbatim: the `"base64:…"` string, not the decoded key. It keys the HMAC that binds a remember-me cookie to the user's password. |
| `Domain` | `""` | The remember-me cookie's `Domain`. Give it your session cookie's. |
| `Secure` | `false` | The remember-me cookie's `Secure` flag. Give it your session cookie's. |
| `SameSite` | `http.SameSiteLaxMode` | The remember-me cookie's `SameSite` mode, used when the field is left at zero. |
| `SessionTable` | `"sessions"` | The table [`LogoutOtherDevices`](#invalidating-sessions-on-other-devices) deletes other sessions from. |
| `Hasher` | required | The [`*hashing.Hasher`](hashing.md) whose cost passwords are kept at: a password that matches a hash made at another cost is [rehashed](#automatic-password-rehashing) with it. The match itself is `hashing.Check`, which reads the cost from the hash. |
| `Timebox` | `200ms` | The least time a credential check takes, so an unknown address answers no faster than a wrong password. |
| `Events` | `nil` | The `*events.Dispatcher` told of every sign-in through the [`Login` event](#events). Nil dispatches nothing. |

> [!NOTE]
> If you're moving an existing Laravel application to reef, set `LoginKey` and `RecallerCookie` to the names Laravel derives (`login_web_59ba…`, `remember_web_59ba…`), so sessions and remember-me cookies issued by the Laravel application keep working. See [compatibility](compatibility.md).

Next, give the guard's middleware to your routes. A typical application keeps the guard on its `*app.App` as `a.Auth`, and its bootstrap builds the middleware groups and aliases its route files name; the guard's `ResolveUser` runs at the end of each group, after the cookies and the session:

```go
routes.Middleware{
	Web: []web.Middleware{cookies, startSession, csrf, a.Auth.ResolveUser},
	API: []web.Middleware{cookies, startSession, a.Auth.ResolveUser},

	Auth:            a.Auth.Authenticate,
	Guest:           a.Auth.RedirectIfAuthenticated,
	ConfirmPassword: auth.RequirePassword(cfg.Auth.PasswordTimeout),
}
```

> [!WARNING]
> In a group with CSRF protection, put `ResolveUser` after the CSRF check. A remember-me sign-in regenerates the CSRF token, so resolving the user first would change the token the check compares against. See [CSRF protection](csrf.md).

### Retrieving the Authenticated User

On a route behind `Authenticate`, you may read the signed-in user with the generic `auth.User` function, naming your user type:

```go
import (
	"example.com/flights/app/models"
	"github.com/atobeach/reef/auth"
	"github.com/atobeach/reef/web"
)

func (ctl *BookingsController) Index(c *web.Context) ([]models.Booking, error) {
	user := auth.User[models.User](c)

	return user.Bookings(c).Latest().Get()
}
```

`auth.User` never touches the database: it reads the user the guard already resolved for this request, and the generated model's relations (`user.Bookings(c)`) scope your queries to them. Because there is always a user behind `Authenticate`, it returns the user rather than an `ok` flag, and it panics if no user is signed in. Only call it on routes that run `Authenticate`.

#### Determining if the Current User is Authenticated

On a route that serves guests and users alike, ask the guard instead. Its `User` method returns the user, whether there is one, and any error loading them:

```go
func (ctl *UserController) Show(c *web.Context) (models.User, error) {
	user, ok, err := ctl.Auth.User(c)
	if err != nil {
		return models.User{}, err
	}

	if !ok {
		return models.User{}, &exceptions.HTTPResponseError{Status: http.StatusUnauthorized, Body: struct{}{}}
	}

	return user, nil
}
```

`User` only reads. Once `ResolveUser`, `Authenticate` or `RedirectIfAuthenticated` ran, it returns the answer they resolved and bound for the request, a failure included, with no further lookup. Before any of them ran, it resolves the session's user for that call and binds nothing, so `auth.ID(c)` still answers nil; the exception is a remember-me sign-in, which binds the user as `Login` does. If all you need is the id, `auth.ID(c)` returns a `*int64`, nil for a guest; see [the user outside controllers](#the-user-outside-controllers).

### Protecting Routes

The guard's `Authenticate` method is a middleware that only lets signed-in users through. A guest gets a `401` with the JSON body `{"message": "Unauthenticated."}`:

```go
r.Middleware(m.Auth).Group(func(r *web.Router) {
	r.Post("/logout", accounts.AuthenticatedSession.Destroy).Name("logout")
	r.Put("/user/password", accounts.Password.Update).Name("user-password.update")
})
```

Since a method value is a middleware, you may also hand `guard.Authenticate` to a single route's `Middleware` call. If loading the user fails (the database is down, or a remember-me sign-in's `Login` listener returned an error), `Authenticate` returns that error, so the request fails (a `500`) rather than passing as a guest. See [middleware](middleware.md) for how groups and route middleware run.

### Redirecting Authenticated Users

The guard's `RedirectIfAuthenticated` middleware is the opposite of `Authenticate`: it keeps signed-in users off guest-only routes such as login and registration. A signed-in user gets a `302` redirect to the site root (the request's scheme and host), even when the caller asked for JSON, which a single-page app reads as "already signed in":

```go
r.Post("/login", accounts.AuthenticatedSession.Store).Middleware(m.Throttle("login"), m.Guest).Name("login.store")
r.Post("/register", accounts.RegisteredUser.Store).Middleware(m.Throttle("register"), m.Guest).Name("register.store")
```

### Login Throttling

reef doesn't throttle sign-ins on its own; you define a [rate limiter](rate-limiting.md) for the login route. This one allows five attempts a minute per email address and IP, lowercasing and transliterating the address so case or accent variants share one bucket:

```go
limiters.For("login", func(c *web.Context) ratelimit.Limit {
	email := strings.ToLower(c.String("email"))

	return ratelimit.PerMinute(5).By(support.Transliterate(email + "|" + c.IP()))
})
```

Run the throttle before `RedirectIfAuthenticated`, as the routes above do, so a signed-in user still spends an attempt.

## Manually Authenticating Users

You'll usually sign users in from your own controller. The guard's `Attempt` method takes the request, an `auth.Credentials` value and whether to remember the user. If the address belongs to a user and the password matches their hash, the user is signed in and `Attempt` returns `true`:

```go
import (
	"strings"

	"example.com/flights/app/http/requests"
	"github.com/atobeach/reef/auth"
	"github.com/atobeach/reef/web"
)

// Store is POST /login.
func (ctl *AuthenticatedSessionController) Store(c *web.Context) (*web.Response, error) {
	request, err := web.Validate[requests.LoginRequest](c)
	if err != nil {
		return nil, err
	}

	credentials := auth.Credentials{Email: strings.ToLower(request.Email), Password: request.Password}

	ok, err := ctl.Auth.Attempt(c, credentials, request.Remember)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, web.ValidationFailed("email", "These credentials do not match our records.")
	}

	return web.NoContent(), nil
}
```

Read the credentials from the [form request](validation.md#form-request-validation)'s typed fields, as above, rather than reading the input again.

`Attempt` answers `false`, with a nil error, for an unknown address, a wrong password and a user without a password alike, so your response can't be used to find out which addresses have accounts. An error means something failed along the way: loading the user, saving a rehashed password or a new remember token, or a `Login` listener.

The address is looked up exactly as given, so normalize it first (the controller above lowercases it); the password is checked as typed. On success the guard regenerates the session id and the CSRF token, to prevent session fixation, stores the user's id in the session and dispatches the [`Login` event](#events). You don't need to regenerate the session yourself.

> [!NOTE]
> A failed `Attempt` takes at least the guard's `Timebox` (200ms by default), so a miss on an unknown address can't be told from a wrong password by timing it. A successful sign-in reveals nothing new, so it returns as soon as it's done. The timebox runs on the wall clock: freezing the [test clock](mocking.md#interacting-with-time) doesn't skip it.

### Timeboxing Your Own Checks

Any other check whose speed could tell an attacker whether an account exists, such as sending a password reset link, takes the same defence through `auth.Timebox`. It runs a function and then waits out the rest of a fixed duration, so the call takes the same time whatever path the function took: a request for an address that doesn't exist must take as long as one for an address that does, or timing the answer gives the accounts away.

The function receives a `returnEarly` callback. Call it on the path that may finish quickly because it reveals nothing new, such as a successful sign-in. The guard's own `Attempt`, inside the `auth` package, uses it this way:

```go
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
```

`Timebox` returns whatever the function returned, error included; an error does not skip the wait. The wait ends early if the context is canceled. It measures real elapsed time, not the context's clock, so it is not affected by a test's frozen clock. A password broker may run both of its flows in one, with the `cfg.Auth.LoginTimebox` the guard is built with.

### Remembering Users

If you would like to provide "remember me" functionality, pass `true` as the third argument to `Attempt` (or [`Login`](#other-authentication-methods)). The user stays signed in after their session ends, until they log out or change their password:

```go
ok, err := ctl.Auth.Attempt(c, credentials, true)
```

When remembering, the guard reuses the user's remember token or, when they have none, mints a 60-character one and saves it through your provider's `SaveRememberToken`. It then queues the remember-me cookie, named by `RecallerCookie`, holding `id|token|HMAC(password hash)`. The cookie is `HttpOnly`, has the path `/`, takes `Domain`, `Secure` and `SameSite` from the guard's config and lasts 400 days, the most browsers allow. Like your other cookies, the [`Cookies` middleware](middleware.md) encrypts it on the way out.

On a later request whose session holds no user, the guard reads the cookie, loads the user by id, and signs them back in only when the token matches their stored `remember_token` and the HMAC matches their **current** password hash (both compared in constant time). Changing a password therefore ends every remember-me cookie issued before it, and a user without a password is never recalled. A recalled user gets a new session id and CSRF token, exactly as a fresh sign-in does, and the `Login` event is dispatched with `Remember: true`.

The HMAC is the guard's `HashPasswordForCookie` method, keyed with `HashKey`. A test that builds a remember-me cookie by hand may use it, with the guard's `RecallerCookie` method for the cookie's name (here through a test browser of your own [test case](testing.md#a-full-test-case), which encrypts the cookie it sends):

```go
value := strings.Join([]string{strconv.FormatInt(user.ID, 10), *user.RememberToken, tc.App.Auth.HashPasswordForCookie(*user.Password)}, "|")

tc.Browser().WithCookie(tc.App.Auth.RecallerCookie(), value).GetJSON("/user").AssertOk()
```

### Validating Credentials Without Signing In

Sometimes you need to check a user's credentials without signing them in: before a two-factor challenge, or when a signed-in user confirms their password. The guard's `Validate` method checks the credentials and returns the user they belong to:

```go
user, ok, err := ctl.Auth.Validate(c, credentials)
if err != nil {
	return nil, err
}

if !ok {
	return nil, web.ValidationFailed("email", "These credentials do not match our records.")
}

if twofactor.Challenges(user) {
	parkTwoFactorLogin(session.FromContext(c), user, request.Remember, false)

	return map[string]bool{"two_factor": true}, nil
}
```

Here `twofactor.Challenges` and `parkTwoFactorLogin` are the application's own: the login waits in the session for the second factor. A user without two-factor authentication is then signed in with `Attempt`, which checks the credentials again inside a timebox of its own.

`Validate` always takes at least the timebox, whatever the answer, so neither a miss nor a match can be timed. Like `Attempt`, it [rehashes](#automatic-password-rehashing) a password hashed at another cost.

### Other Authentication Methods

#### Authenticate a User Instance

If you need to sign in a user you already hold, such as one you just registered or one who completed a social sign-in, pass them to the guard's `Login` method with whether to remember them:

```go
user, err := ctl.CreateNewUser.Create(c, account.NewUser{Name: request.Name, Email: request.Email, Password: request.Password})
if err != nil {
	return nil, err
}

if err := ctl.Auth.Login(c, user, request.Remember); err != nil {
	return nil, err
}
```

`Login` does everything `Attempt` does once the credentials match: it regenerates the session, stores the id, issues the remember-me cookie when asked and dispatches `Login`. It returns an error when the request has no session, or when saving a new remember token or a `Login` listener fails. reef has no `loginUsingId` or `once`: load the user through your models and call `Login`.

#### Replacing the Request's User

When a request changes the signed-in user's row, such as confirming two-factor authentication, you may hand the guard the fresh row with `SetUser`, so `auth.User` returns it for the rest of the request. The session isn't touched:

```go
user := auth.User[models.User](c)

if err := user.Update(c, db.Values{models.UserColumns.TwoFactorConfirmedAt: clock.Now(c)}); err != nil {
	return nil, err
}

ctl.Auth.SetUser(c, user)
```

A model's `Update` reads its row back into the value you hold, so there is nothing to load again: `user` is already the fresh row. For a value type such as `models.User`, `auth.User` returns a copy of the user the guard resolved, so until you call `SetUser`, a later `auth.User` call in the same request still sees the old row.

## Logging Out

To sign the user out, call the guard's `Logout` method:

```go
// Destroy is POST /logout.
func (ctl *AuthenticatedSessionController) Destroy(c *web.Context) (*web.Response, error) {
	if err := ctl.Auth.Logout(c); err != nil {
		return nil, err
	}

	return web.NoContent(), nil
}
```

`Logout` cycles the user's remember token (when they have one), so a stolen remember-me cookie dies with this sign-out; invalidates the session, which empties it and moves it to a new id and a new CSRF token; expires the remember-me cookie, with the same domain and flags it was set with; and makes the rest of the request a guest's. You don't need to invalidate the session or regenerate the CSRF token yourself.

### Invalidating Sessions on Other Devices

reef also lets you sign a user out of every other device while keeping the current one signed in. You'll typically do this after a password change or a password reset:

```go
request, err := web.Validate[requests.UpdatePasswordRequest](c)
if err != nil {
	return nil, err
}

if err := ctl.UpdateUserPassword.Update(c, user, request.Password); err != nil {
	return nil, err
}

// The change also signs out every other device; this session stays.
if err := ctl.Auth.LogoutOtherDevices(c, user); err != nil {
	return nil, err
}
```

`LogoutOtherDevices` cycles the user's remember token, so every remember-me cookie issued before stops matching, and deletes every other row of the guard's `SessionTable` whose `user_id` is the user's. Unlike Laravel's, it takes the user rather than their password: check the password (or finish the password change) before you call it.

> [!WARNING]
> The sessions table only knows who a session belongs to when the session middleware records it: give `session.Config` a `UserID` resolver, for example `UserID: func(c *web.Context, _ *session.Session) *int64 { return a.Auth.UserID(c) }`. Without it, no row matches and other devices stay signed in. See [logging out other devices](session.md#logging-out-other-devices).

> [!NOTE]
> The current device keeps its session, but its own remember-me cookie stops matching too, since the token it carries was cycled. It stays signed in for as long as its session lasts.

## Password Confirmation

While building your application, you may have actions that require the user to confirm their password before they go ahead, such as turning off two-factor authentication or deleting their account. reef includes a middleware that requires a recent confirmation, and the session keeps the time of the last one.

### Configuration

The confirmation lasts for the timeout you pass to `auth.RequirePassword`. A typical application reads it from `AUTH_PASSWORD_TIMEOUT`, in seconds, with a default of three hours:

```go
PasswordTimeout: time.Duration(env.Int("AUTH_PASSWORD_TIMEOUT", 10800)) * time.Second,
```

### Routing

First, define a route that checks the password. It runs behind `Authenticate`, checks the signed-in user's password with `Validate`, and stamps the session with [`PasswordConfirmed`](session.md#password-confirmation) when it matches:

```go
// Store is POST /user/confirm-password.
func (ctl *ConfirmablePasswordController) Store(c *web.Context) (*web.Response, error) {
	user := auth.User[models.User](c)

	_, ok, err := ctl.Auth.Validate(c, auth.Credentials{Email: user.Email, Password: c.String("password")})
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, web.ValidationFailed("password", "The provided password was incorrect.")
	}

	session.FromContext(c).PasswordConfirmed(clock.Now(c))

	return web.NoContent(), nil
}
```

The route is a password oracle for whoever holds the session, so throttle it like your login route:

```go
r.Post("/user/confirm-password", accounts.ConfirmablePassword.Store).Middleware(m.Throttle("confirm-password")).Name("password.confirm.store")
```

### Requiring Confirmation

Then assign `auth.RequirePassword` to any route that needs a recent confirmation, after `Authenticate`:

```go
r.Middleware(m.Auth).Group(func(r *web.Router) {
	r.Middleware(auth.RequirePassword(3 * time.Hour)).Group(func(r *web.Router) {
		r.Delete("/user/two-factor-authentication", accounts.TwoFactorAuthentication.Destroy).Name("two-factor.disable")
		r.Get("/user/two-factor-recovery-codes", accounts.RecoveryCode.Index).Name("two-factor.recovery-codes")
	})
})
```

Without a confirmation in the last `timeout`, the caller gets a `423 Locked` with `{"message": "Password confirmation required."}`; your frontend asks for the password, posts it to the confirmation route and retries. The check compares the whole seconds the session stores, so a confirmation exactly `timeout` old still counts, and a session that never confirmed reads as long past.

> [!NOTE]
> `RequirePassword` returns a plain `web.Middleware`, so you may wrap it. A `RequireRecentAuthentication` middleware of your own could let a social-only account, which has no password to confirm, pass with a recent social sign-in and otherwise defer to `auth.RequirePassword`.

### Validating the Current Password

A form that changes something sensitive may ask for the current password alongside the change instead. `auth.CurrentPassword` is the `current_password` [validation rule](validation.md#current_password): it passes when the value is the password of the user bound to the request's context, so it runs in the same pass as the form's other rules and one 422 reports every problem. A guest, and a user without a password, never pass. Here is a password change form:

```go
func (UpdatePasswordRequest) Rules() rule.Set {
	return rule.Set{
		"current_password": {rule.Required, rule.String, auth.CurrentPassword},
		"password":         passwordRules(),
	}
}
```

Its message is "The password is incorrect."; a form request words its own under `current_password.current_password`. The route must run behind `Authenticate` (or `ResolveUser`), which binds the user the rule reads.

## The User Outside Controllers

### Resolving the User Up Front

Code below your controllers, such as actions, AI tools and models, receives a `context.Context` rather than the request. Once the guard has resolved the request's user, `auth.ID` reads their id from any context derived from the request:

```go
// Create stores a booking for the fare, owned by ctx's signed-in user (none
// for a guest checkout).
func (a CreateBooking) Create(ctx context.Context, flightID int64, fare models.Fare) (*models.Booking, error) {
	booking, err := models.CreateBooking(ctx, models.NewBooking{
		FlightID:  flightID,
		Reference: newReference(),
		Amount:    fare.Amount,
		Currency:  fare.Currency,
		UserID:    auth.ID(ctx),
	})
	if err != nil {
		return nil, err
	}

	return &booking, nil
}
```

`auth.ID` returns nil for a guest, outside a request, before the user has been resolved, and when resolving them failed. To make sure the user is resolved before your route runs, put the guard's `ResolveUser` middleware at the end of your middleware groups, as in [Building the Guard](#building-the-guard). `ResolveUser` lets guests through as guests; when loading the user fails it logs the error and binds it, so a route behind `Authenticate` still fails the request. The middleware and the writes (`Login`, `SetUser`, `Logout`) are what bind the user; reading it with the guard's `User` or `UserID` never does, a remember-me sign-in aside.

The guard's `UserID` method is for the places where a failed lookup may read as a guest: the session row's `user_id`, or a per-user throttle key. It returns the id of the user `User` answers with, or nil, and logs a failure instead of returning it. Like `User`, it resolves on a request no resolving middleware ran on, so a session saved after the CSRF check refused a request still names its user:

```go
limiters.For("confirm-password", func(c *web.Context) ratelimit.Limit {
	key := ""
	if id := guard.UserID(c); id != nil {
		key = strconv.FormatInt(*id, 10)
	}

	return ratelimit.PerMinute(6).By(key + "|" + c.IP())
})
```

### Acting as a User in Jobs and Tests

A queued job or a test may bind the user it acts as onto its own context with `auth.WithUser`; everything that reads `auth.ID` below it sees that user. Passing nil binds a guest:

```go
// The booking the action creates belongs to user.
booking, err := actions.CreateBooking{}.Create(auth.WithUser(tc.Ctx, user), flight.ID, fare)
```

For HTTP tests, a typical test case signs a browser in with `tc.ActingAs(user)`, which writes the user's id under the guard's `LoginKey` into a fresh session; see [HTTP tests](http-tests.md#session-and-authentication).

## Adding Custom User Providers

The guard never loads or saves a user itself. It goes through the `auth.Provider[U]` you hand `NewGuard`, so your users may live in any table, or anywhere else. Here is a provider that is a struct with no fields, querying the generated `models.Users` builder:

```go
import (
	"context"
	"fmt"

	"example.com/flights/app/models"
	"github.com/atobeach/reef/db"
)

// UserProvider loads the guard's users from the users table.
type UserProvider struct{}

func (UserProvider) ByID(ctx context.Context, id int64) (models.User, bool, error) {
	user, err := models.Users(ctx).Find(id)
	if err != nil {
		return models.User{}, false, fmt.Errorf("loading user %d: %w", id, err)
	}

	if user == nil {
		return models.User{}, false, nil
	}

	return *user, true, nil
}

func (UserProvider) ByEmail(ctx context.Context, email string) (models.User, bool, error) {
	user, err := models.Users(ctx).Where(models.UserColumns.Email, email).First()
	if err != nil {
		return models.User{}, false, fmt.Errorf("loading the user by email: %w", err)
	}

	if user == nil {
		return models.User{}, false, nil
	}

	return *user, true, nil
}

func (UserProvider) SaveRememberToken(ctx context.Context, user models.User, token string) error {
	if _, err := models.Users(ctx).WhereKey(user.ID).WithoutTimestamps().Update(db.Values{models.UserColumns.RememberToken: token}); err != nil {
		return fmt.Errorf("saving user %d's remember token: %w", user.ID, err)
	}

	return nil
}

func (UserProvider) SavePassword(ctx context.Context, user models.User, hash string) (models.User, error) {
	if err := user.Update(ctx, db.Values{models.UserColumns.Password: hash}); err != nil {
		return models.User{}, err
	}

	return user, nil
}
```

The guard calls the provider with the request as the context, so the database connection and clock your kernel binds are there.

### The User Provider Contract

`auth.Provider[U]` has four methods:

```go
type Provider[U Authenticatable] interface {
	ByID(ctx context.Context, id int64) (user U, ok bool, err error)
	ByEmail(ctx context.Context, email string) (user U, ok bool, err error)
	SaveRememberToken(ctx context.Context, user U, token string) error
	SavePassword(ctx context.Context, user U, hash string) (U, error)
}
```

- `ByID` loads the user the session or a remember-me cookie names. Return `ok == false`, not an error, when the id no longer exists: a deleted account's session then reads as a guest.
- `ByEmail` loads the user an address belongs to, `ok == false` when nobody has it. The guard passes the address exactly as the credentials hold it.
- `SaveRememberToken` stores a new remember token: at a remembered sign-in when the user has none, and when `Logout` or `LogoutOtherDevices` cycles it.
- `SavePassword` stores a [rehashed](#automatic-password-rehashing) password and returns the user carrying it, so a remember-me cookie issued in the same request binds to the new hash.

### The Authenticatable Contract

Your user type implements `auth.Authenticatable`: its id, its password hash and its remember token, each as the guard needs it. Here it is implemented on a generated `models.User`, whose nullable columns are pointers:

```go
// AuthID implements auth.Authenticatable.
func (u User) AuthID() int64 { return u.ID }

// AuthPassword implements auth.Authenticatable; social-only users have no
// password.
func (u User) AuthPassword() string {
	if u.Password == nil {
		return ""
	}

	return *u.Password
}

// AuthRememberToken implements auth.Authenticatable.
func (u User) AuthRememberToken() string {
	if u.RememberToken == nil {
		return ""
	}

	return *u.RememberToken
}
```

Return an empty string for a missing password or token. A user whose `AuthPassword` is empty never matches a password and is never recalled by a remember-me cookie.

## Automatic Password Rehashing

reef hashes passwords with bcrypt at the cost your [`Hasher`](hashing.md) is configured with. When `Attempt` or `Validate` matches a password whose stored hash was made at a different cost, the guard hashes the password again at the current cost and saves it through your provider's `SavePassword`. Raising `BCRYPT_ROUNDS` therefore upgrades accounts as their owners sign in, without any action on your part.

The remember-me cookie is bound to the password hash, so a rehash would end it. If the request carried a remember-me cookie that already recalls the user, the guard re-issues it, with the same token, against the new hash, so a password confirmation doesn't sign the device out once its session ends. Only such a cookie is re-issued: a cookie that doesn't recall the user is never turned into one that does, so `Validate` can't sign anyone in past the two-factor challenge that follows it. A failed rehash fails the sign-in with the error.

## Events

The guard dispatches an `auth.Login[U]` event on every sign-in, through the `Events` dispatcher in its config: from `Login` (and so from `Attempt`), and when a remember-me cookie signs a user back in. The event carries the `User` and whether they asked to be `Remember`ed. You may listen for it like any other [event](events.md):

```go
events.Listen(dispatcher, func(ctx context.Context, e auth.Login[models.User]) error {
	return listeners.RecordLastLogin(ctx, e.User.ID)
})
```

A listener that returns an error fails the sign-in: `Login` returns it, wrapped with the user's id. For a remember-me sign-in, the error is remembered for the rest of the request, so `Authenticate` fails it with a `500` rather than letting it through signed in.

`Login` is the only event the guard dispatches; there are no `Attempting`, `Failed` or `Logout` events. Dispatch your own from your controllers if you need one, such as a `Registered` event after creating an account. In tests, you may fake the event and assert on it with `eventstest` (see [testing events](events.md#testing)):

```go
tc.App.Events.Fake(auth.Login[models.User]{})

browser.PostJSON("/login", map[string]any{"email": user.Email, "password": factories.UserPassword, "remember": true}).AssertOk()

eventstest.AssertDispatched(t, tc.App.Events, func(e auth.Login[models.User]) bool {
	return e.User.ID == user.ID && e.Remember
})
```

## Available Methods

### Package Functions

- `auth.NewGuard[U](provider Provider[U], config GuardConfig) (*Guard[U], error)`: builds the guard; an empty `HashKey` or a nil `Hasher` is an error. See [Building the Guard](#building-the-guard).
- `auth.User[U](c *web.Context) U`: the signed-in user on a route behind `Authenticate`; panics when there is none. `user := auth.User[models.User](c)`
- `auth.ID(ctx context.Context) *int64`: the signed-in user's id, nil for a guest or before the user is resolved. `if id := auth.ID(ctx); id != nil { … }`
- `auth.WithUser(ctx context.Context, user Authenticatable) context.Context`: binds `user` (nil: a guest) as `ctx`'s signed-in user, for jobs and tests. `ctx = auth.WithUser(ctx, user)`
- `auth.RequirePassword(timeout time.Duration) web.Middleware`: a `423` unless the session confirmed the password within `timeout`. `r.Middleware(auth.RequirePassword(3 * time.Hour))`
- `auth.Timebox[T](ctx context.Context, d time.Duration, fn func(returnEarly func()) (T, error)) (T, error)`: runs `fn`, then waits out the rest of `d` unless `fn` called `returnEarly`, handing back `fn`'s value and error. See [Timeboxing Your Own Checks](#timeboxing-your-own-checks).
- `auth.CurrentPassword rule.Rule`: the `current_password` validation rule, passing for the signed-in user's own password. See [Validating the Current Password](#validating-the-current-password). `"current_password": {rule.Required, rule.String, auth.CurrentPassword}`

### Guard Methods

- `Attempt(c, creds, remember) (bool, error)`: signs in the user `creds` match; `false` is a miss, which takes at least the timebox. See [Manually Authenticating Users](#manually-authenticating-users).
- `Validate(c, creds) (U, bool, error)`: checks `creds` without signing in, always taking at least the timebox. See [Validating Credentials](#validating-credentials-without-signing-in).
- `Login(c, user, remember) error`: signs `user` in, regenerating the session and issuing the remember-me cookie when asked. See [Other Authentication Methods](#other-authentication-methods).
- `Logout(c) error`: signs the user out, invalidating the session and cycling the remember token. See [Logging Out](#logging-out).
- `LogoutOtherDevices(c, user) error`: cycles the remember token and deletes the user's other session rows. See [Invalidating Sessions on Other Devices](#invalidating-sessions-on-other-devices).
- `User(c) (U, bool, error)`: the request's user; `ok` is false for a guest. It reads the answer the resolving middleware bound, and before any of them ran resolves for the call and binds nothing (a remember-me sign-in excepted). See [Retrieving the Authenticated User](#retrieving-the-authenticated-user).
- `SetUser(c, user)`: replaces the request's resolved user without touching the session. `ctl.Auth.SetUser(c, fresh)`
- `UserID(c) *int64`: the id of the user `User` answers with, nil for a guest or when the lookup failed (which is logged). `UserID: guard.UserID`
- `Authenticate(c, next) (any, error)`: middleware answering a guest `401 {"message": "Unauthenticated."}`. `Auth: guard.Authenticate`
- `RedirectIfAuthenticated(c, next) (any, error)`: middleware answering a signed-in user with a `302` to the site root. `Guest: guard.RedirectIfAuthenticated`
- `ResolveUser(c, next) (any, error)`: middleware resolving and binding the user before the route runs, so `auth.ID` answers downstream. `[]web.Middleware{cookies, startSession, guard.ResolveUser}`
- `LoginKey() string`: the session attribute holding the user's id. A test case's `tc.ActingAs(user)` can be `tc.Browser().WithSession(map[string]any{tc.App.Auth.LoginKey(): user.ID})`.
- `RecallerCookie() string`: the remember-me cookie's name. `browser.Cookie(tc.App.Auth.RecallerCookie())`
- `HashPasswordForCookie(passwordHash string) string`: the hex HMAC-SHA256 of a password hash, keyed with `HashKey`, that a remember-me cookie carries. See [Remembering Users](#remembering-users).

### Types

- `auth.Guard[U]`: the session guard over users of type `U`; build it with `NewGuard`.
- `auth.GuardConfig`: the guard's options (`LoginKey`, `RecallerCookie`, `HashKey`, `Domain`, `Secure`, `SameSite`, `SessionTable`, `Hasher`, `Timebox`, `Events`). See the [options table](#building-the-guard).
- `auth.Provider[U]`: loads and saves the guard's users (`ByID`, `ByEmail`, `SaveRememberToken`, `SavePassword`). See [The User Provider Contract](#the-user-provider-contract).
- `auth.Authenticatable`: what the guard needs from your user model (`AuthID`, `AuthPassword`, `AuthRememberToken`). See [The Authenticatable Contract](#the-authenticatable-contract).
- `auth.Credentials`: an `Email`, looked up exactly as given, and a `Password`, as typed. `auth.Credentials{Email: email, Password: password}`
- `auth.Login[U]`: the event dispatched on every sign-in, with its `User` and `Remember` fields. See [Events](#events).
