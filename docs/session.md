# HTTP Session

- [Introduction](#introduction)
    - [Configuration](#configuration)
    - [The Sessions Table](#the-sessions-table)
- [Interacting With the Session](#interacting-with-the-session)
    - [Retrieving the Session](#retrieving-the-session)
    - [Session Keys](#session-keys)
    - [Retrieving Data](#retrieving-data)
    - [Storing Data](#storing-data)
    - [Flash Data](#flash-data)
    - [Deleting Data](#deleting-data)
    - [Regenerating the Session ID](#regenerating-the-session-id)
- [The CSRF Token](#the-csrf-token)
- [Password Confirmation](#password-confirmation)
- [The Previous URL](#the-previous-url)
- [Logging Out Other Devices](#logging-out-other-devices)
- [Sessions Outside a Request](#sessions-outside-a-request)
- [How Sessions Are Loaded and Saved](#how-sessions-are-loaded-and-saved)
- [Available Methods](#available-methods)

## Introduction

Since HTTP-driven applications are stateless, sessions give you a way to keep information about a user across requests: who signed in, a login waiting on its two-factor challenge, the `state` of an OAuth round trip.

reef's session is database-backed. Each session is a row in a `sessions` table, keyed by a random 40-character id that the browser carries in an encrypted, `HttpOnly` cookie. The row's payload is the session's attributes as base64-encoded JSON. That storage format is fixed on purpose: it is the one the sessions already stored were written in (see [compatibility](compatibility.md)).

reef has a single session driver. There are no file, cache or Redis drivers, and no session blocking.

### Configuration

The session is started by the `session.StartSession` middleware, which you add to the route groups that need a session, configured with a `session.Config`:

```go
cookies := middleware.Cookies(a.Encrypter, "locale")

startSession := session.StartSession(session.Config{
	Cookie:   cfg.Session.Cookie,   // SESSION_COOKIE, "flights_session"
	Lifetime: cfg.Session.Lifetime, // SESSION_LIFETIME minutes, 120
	Path:     cfg.Session.Path,
	Domain:   cfg.Session.Domain,
	Secure:   cfg.Session.Secure,
	SameSite: cfg.Session.SameSite,
	Table:    cfg.Session.Table,   // "sessions"
	Lottery:  cfg.Session.Lottery, // [2, 100]
	UserID: func(c *web.Context, _ *session.Session) *int64 {
		return a.Auth.UserID(c)
	},
})

webGroup := []web.Middleware{cookies, startSession, csrf, a.Auth.ResolveUser}
```

`StartSession` must run inside the [cookie middleware](responses.md#cookies-and-encryption): it reads the session id from the already-decrypted cookie, and relies on the cookie middleware to encrypt the cookie it sends back.

The `session.Config` fields are:

| Field | Description |
|---|---|
| `Cookie` | The session cookie's name. |
| `Lifetime` | How long an idle session lives. Every request slides it: the cookie is re-sent with a fresh expiry, and a row last active longer ago than this reads as empty. |
| `Path`, `Domain` | The cookie's `Path` and `Domain` attributes. |
| `Secure` | Send the cookie over HTTPS only. Leave it off for plain-HTTP local development. |
| `SameSite` | The cookie's `SameSite` mode (`http.SameSiteLaxMode`, for example). |
| `Table` | The table the rows live in. |
| `Lottery` | The chance that a request sweeps expired rows from the table: `[2]int{2, 100}` sweeps on 2% of requests. A zero `Lottery` never sweeps. |
| `UserID` | A `session.UserResolver` that reports the signed-in user's id for the row's `user_id` column; see [logging out other devices](#logging-out-other-devices). |

The session cookie is always `HttpOnly`, and its `Expires` and `Max-Age` are set from `Lifetime`.

### The Sessions Table

You create the table with a [migration](migrations.md):

```sql
-- +goose Up
CREATE TABLE sessions (
    id varchar(255) PRIMARY KEY,
    user_id bigint,
    ip_address varchar(45),
    user_agent text,
    payload text NOT NULL,
    last_activity integer NOT NULL
);

CREATE INDEX sessions_user_id_index ON sessions (user_id);
CREATE INDEX sessions_last_activity_index ON sessions (last_activity);
```

`id` must be the primary key: a new session is saved with one `INSERT ... ON CONFLICT (id) DO UPDATE`, which needs its unique index. `last_activity` is a Unix timestamp, and `user_agent` is truncated to 500 bytes before it is stored. The table needs no `created_at` or `updated_at` columns: the session writes its rows through [`db.Table`](queries.md#retrieving-all-rows-from-a-table), which stamps no timestamps, on the connection the request's context carries.

## Interacting With the Session

### Retrieving the Session

`session.FromContext` returns the request's session. Since the `*web.Context` is a `context.Context`, you pass it straight in:

```go
s := session.FromContext(c)
```

`FromContext` returns `nil` when no session is bound: on a route without the `StartSession` middleware, or in a job or a command. Code that may run in either should check:

```go
if s := session.FromContext(c); s != nil {
	s.RegenerateWithToken(false)
}
```

### Session Keys

Attributes are read and written through a `session.Key[T]`: a typed name for an attribute that holds a `T`. It is the one way in; the session has no untyped `Get` or `Put`. Declare the keys a package uses as constants, beside the code that owns them:

```go
// Session keys of a login parked for the 2FA challenge.
const (
	ChallengedUserID   session.Key[int64] = "login.id"
	challengedRemember session.Key[bool]  = "login.remember"
)

const oauthState session.Key[string] = "state"
```

You may use "dot" notation to reach inside a nested object, as stored sessions do: `login.id` is the `id` inside the `login` object. A top-level key that contains the dot itself wins over the nested path.

### Retrieving Data

The `Get` method reads the attribute. Its second result is `false` when the attribute is absent, `null`, or not a `T`, so a value of the wrong type never reads as `T`'s zero value:

```go
id, ok := ChallengedUserID.Get(s)
```

A value put during this request and one decoded from the store read the same way: both go through JSON into `T`. So a struct key reads its object whether it was put a moment ago or loaded from the row:

```go
// pendingLink is a Google sign-in waiting for its password check. Its fields
// are declared in JSON-name order, the order the stored object has always
// had.
type pendingLink struct {
	Email      string `json:"email"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

const pendingLinkKey session.Key[pendingLink] = "social_link"

pending, ok := pendingLinkKey.Get(s)
```

#### Reading Numbers and Times

A number reads back as a `json.Number` once it has been stored, but as whatever type you put during the request that put it, so a `Key[int64]` reads an integer in any of its forms: an `int` or `int64`, a `float64` or a `json.Number` (a fraction truncates), or a numeric string. Store a time as a Unix timestamp under a `Key[int64]`, and read it back with `time.Unix`:

```go
const SocialLoginAt session.Key[int64] = "auth.social_login_at"

if at, ok := SocialLoginAt.Get(s); ok && clock.Now(c).Unix()-at <= int64(timeout.Seconds()) {
	// ...
}
```

The password confirmation has its own methods, `PasswordConfirmed(now)` and `PasswordConfirmedAt()`, which read and write `auth.password_confirmed_at` this way; `PasswordConfirmedAt` is the zero time when the password was never confirmed.

#### Reading an Attribute As Stored

A `Key[any]` returns the attribute exactly as the session holds it, `json.Number`s and `map[string]any`s included. It is the explicit untyped path, for tests and tools that inspect the raw session:

```go
value, ok := session.Key[any]("login.id").Get(s)
```

#### Retrieving All Session Data

The `All` method returns a copy of every attribute. The copy is shallow: adding or removing its keys leaves the session alone, but nested objects are shared with it:

```go
data := s.All()
```

#### Retrieving and Deleting an Item

The `Pull` method reads an attribute, as `Get` does, and removes it in one call, which suits single-use values such as an OAuth `state`:

```go
stored, _ := oauthState.Pull(s)
```

### Storing Data

The `Put` method sets an attribute. Every dot in the key nests, creating the intermediate objects as needed (and replacing any intermediate value that isn't an object):

```go
oauthState.Put(s, support.RandomString(40))

ChallengedUserID.Put(s, user.ID)
challengedRemember.Put(s, remember)
```

Values are stored as JSON when the session is saved, so a key's `T` must be a type `encoding/json` can encode. A struct is encoded in the order its fields are declared; declare them in the order the stored object has, so rows written before read and compare the same.

### Flash Data

Sometimes you may wish to store an item in the session for the next request only. A key's `Flash` method does so: the value is readable for the rest of this request and throughout the next one, then removed:

```go
const status session.Key[string] = "status"

status.Flash(s, "Your booking was saved.")
```

If the request that reads flashed data needs it to survive one more request, call the session's `Reflash`: the values the previous request flashed are kept for the next request too:

```go
s.Reflash()
```

### Deleting Data

The `Forget` method removes an attribute. It removes an exact top-level key first, and otherwise follows the dots:

```go
ChallengedUserID.Forget(s)
```

The `Invalidate` method empties the session entirely, moves it to a new id, deletes the old row and mints a new CSRF token. It is what signing out does:

```go
s.Invalidate()
```

### Regenerating the Session ID

Regenerating the session id defends against session fixation, where an attacker plants a known session id in a victim's browser. The `Regenerate` method moves the session's attributes to a new id. When you pass `true`, the row the request arrived with is deleted when the session is saved; with `false`, it is left to expire:

```go
s.Regenerate(true)
```

The `RegenerateWithToken` method regenerates the id and the CSRF token together, so neither a fixated id nor an old token survives a sign-in. The [authentication guard](authentication.md) calls `RegenerateWithToken(true)` whenever it signs a user in:

```go
s.RegenerateWithToken(false)
```

The `ID` method returns the session's current id.

## The CSRF Token

Every session carries a CSRF token in its `_token` attribute. The `Token` method returns it, and `RegenerateToken` replaces it with a fresh random token. A new session gets a token straight away, and a loaded session without one is given one. The [CSRF middleware](csrf.md) compares requests against this token and sends it to the frontend in the `XSRF-TOKEN` cookie:

```go
token := s.Token()

s.RegenerateToken()
```

## Password Confirmation

The `PasswordConfirmed` method records that the user just confirmed their password, as a Unix timestamp in the `auth.password_confirmed_at` attribute, and `PasswordConfirmedAt` reads it back (the zero time when the password has never been confirmed). The `auth.RequirePassword` middleware reads it to decide whether a sensitive route needs the password again; see [password confirmation](authentication.md#password-confirmation):

```go
if !ok {
	return nil, web.ValidationFailed("password", "The provided password was incorrect.")
}

session.FromContext(c).PasswordConfirmed(clock.Now(c))
```

## The Previous URL

After a page request, `StartSession` records the full URL and route name in the `_previous` attribute: only for a `GET` that matched a route, isn't an XHR, and isn't a prefetch (`Purpose: prefetch`). The `SetPreviousURL` method records it the same way, should you need to do so yourself; an empty route name is stored as `null`:

```go
s.SetPreviousURL(c.FullURL(), c.RouteName())
```

## Logging Out Other Devices

When `Config.UserID` is set, every save writes the signed-in user's id to the row's `user_id` column. The resolver runs at save time, after any sign-in or sign-out the request did, and returns `nil` for a guest:

```go
type UserResolver func(c *web.Context, s *Session) *int64
```

That column is what `session.DestroyOtherSessions` relies on. It deletes every session row signed in as the given user except the context's own session (or every one of them, when the context carries none, as a job's or a command's doesn't), so after a password change a session opened before it, on any device, stops working. The guard's `LogoutOtherDevices` calls it for you (see [invalidating sessions on other devices](authentication.md#invalidating-sessions-on-other-devices)):

```go
if err := session.DestroyOtherSessions(c, "sessions", user.ID); err != nil {
	return nil, err
}
```

## Sessions Outside a Request

`session.Decode` builds a session from an id and a stored payload, for reading or seeding a session outside a request, as a test does for the browser it drives. An empty payload is an empty session. `Encode` returns the payload a session is stored as. `session.With` binds a session to a context, which is what `StartSession` does for a request:

```go
s, err := session.Decode("probe", "")
if err != nil {
	t.Fatal(err)
}
session.Key[int64]("login.id").Put(s, 7)

payload, err := s.Encode()
if err != nil {
	t.Fatal(err)
}

ctx := session.With(t.Context(), s)
```

To unit test code that reads the request's session, bind the session to the context of a request built with [`web.NewContext`](requests.md#building-a-request-in-a-unit-test), as `StartSession` would have left it:

```go
c := web.NewContext(session.With(t.Context(), s), http.MethodPost, "/login")
```

> [!NOTE]
> `Decode` doesn't mint a CSRF token: a session decoded from an empty payload has none until you call `RegenerateToken`.

## How Sessions Are Loaded and Saved

`StartSession` reads the session id from the session cookie:

- When the cookie is missing, or isn't a well-formed id (exactly 40 ASCII letters and digits), a new session starts, with a new random id and CSRF token.
- When a row exists and was active within `Lifetime`, its attributes are loaded. A payload that doesn't decode reads as empty.
- When the row has expired or doesn't exist, the session keeps the id but starts with no attributes.

After the rest of the pipeline has run, the middleware:

1. records [the previous URL](#the-previous-url) for a page request;
2. ages the [flash data](#flash-data), deletes the old row if the session was regenerated with `destroy`, and writes the row with the user id, the client IP, the user agent, the payload and the current time as `last_activity`;
3. sweeps expired rows, if the [lottery](#configuration) says so (a failed sweep is logged and otherwise ignored);
4. attaches the session cookie, with a fresh expiry, to the response, an error response included.

All of this runs whether the action succeeded or failed, so a failed request still slides the session and keeps what it wrote to it.

A session whose row existed when the request began is saved with an `UPDATE` only. If another request deleted the row meanwhile (a sign-out on another tab), the session stays deleted rather than being written back. A new session is inserted, and a concurrent first request that inserted the same id first is updated instead.

If loading the row fails, the request fails with that error before the action runs. If saving it fails, the request fails with that error instead of whatever the action answered, and no session cookie is sent. Either renders as a `500`. When saving fails after the action failed with an unexpected error of its own, that error is still [reported](errors.md#reporting-errors) beside the save's.

> [!NOTE]
> The session is saved before a [streamed response](responses.md#streamed-responses) starts streaming, so changes made inside a stream function are never saved.

## Available Methods

The session's methods:

| Method | Description |
|---|---|
| `All() map[string]any` | a shallow copy of every attribute |
| `Encode() (string, error)` | the payload the session is stored as |
| `ID() string` | the session id |
| `Invalidate()` | empties the session and moves it to a new id |
| `PasswordConfirmed(now time.Time)` | stamps the password confirmation time |
| `PasswordConfirmedAt() time.Time` | when the password was last confirmed |
| `Reflash()` | keeps the values the previous request flashed for one more request |
| `Regenerate(destroy bool)` | moves the session to a new id |
| `RegenerateToken()` | mints a new CSRF token |
| `RegenerateWithToken(destroy bool)` | moves to a new id and mints a new CSRF token |
| `SetPreviousURL(url, route string)` | records `_previous` |
| `Token() string` | the CSRF token |

A `session.Key[T]`'s methods, the one way to read and write an attribute (dots nest):

| Method | Description |
|---|---|
| `Get(s *Session) (T, bool)` | reads the attribute; `false` when absent, `null` or not a `T` |
| `Put(s *Session, value T)` | sets the attribute |
| `Pull(s *Session) (T, bool)` | reads the attribute, as `Get` does, and removes it |
| `Forget(s *Session)` | removes the attribute |
| `Flash(s *Session, value T)` | sets the attribute for the next request only |

And the package's functions and types:

| Identifier | Description |
|---|---|
| `session.StartSession(config Config) web.Middleware` | loads, saves and re-sends the session around each request |
| `session.Config` | the cookie and table settings ([fields](#configuration)) |
| `session.UserResolver` | reports the signed-in user's id for the `user_id` column |
| `session.FromContext(ctx context.Context) *Session` | the context's session, `nil` when none is bound |
| `session.With(ctx context.Context, s *Session) context.Context` | binds a session to a context |
| `session.Decode(id, payload string) (*Session, error)` | a session from a stored payload |
| `session.DestroyOtherSessions(ctx context.Context, table string, userID int64) error` | deletes the user's other session rows |
| `session.Session` | one request's view of the session |
| `session.Key[T]` | a typed name for an attribute holding a `T` ([details](#session-keys)) |
