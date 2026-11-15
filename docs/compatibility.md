# Compatibility

- [Introduction](#introduction)
- [The PHP JSON Dialect](#the-php-json-dialect)
    - [When to Use It](#when-to-use-it)
    - [Encoding Decoded Values](#encoding-decoded-values)
    - [Marshaling Go Values](#marshaling-go-values)
    - [Normalizing Stored JSON](#normalizing-stored-json)
    - [Text From Outside](#text-from-outside)
    - [Reading Decoded Values](#reading-decoded-values)
    - [Casting Like PHP](#casting-like-php)
    - [Available Functions](#available-functions)
- [Encryption Payloads](#encryption-payloads)
- [Encrypted Cookies](#encrypted-cookies)
- [Sessions](#sessions)
- [Authentication](#authentication)
- [CSRF Tokens](#csrf-tokens)
- [Password Hashes](#password-hashes)
- [JSON Columns](#json-columns)
- [Timestamps](#timestamps)
- [Slugs and ASCII Folding](#slugs-and-ascii-folding)
- [Migration Adoption](#migration-adoption)
- [Realtime](#realtime)
- [Environment Files](#environment-files)

## Introduction

reef reads and writes several of Laravel's stored and wire formats byte for byte. A cookie a browser holds, a session row, an encrypted column, a password hash, a JSON column or a migrated schema written by a Laravel application reads the same in a reef application, and the other way round. That is deliberate: if you're moving an existing Laravel application to reef, the reef application can take over the data, users and sessions the Laravel one made, without asking anyone to sign in again or rewriting a single row.

This page lists every such format, what is fixed about it, and where it is documented in full. When you write something new, prefer the plain Go way (`encoding/json` and tagged structs, for example) unless the bytes have to match data that already exists.

## The PHP JSON Dialect

The `phpjson` package writes and reads JSON the way PHP's `json_encode` and array casts do. It exists for bytes that already exist in that dialect: stored columns, fingerprints and cache keys computed from encoded JSON, and the conversation history an AI model is sent again. The dialect differs from `encoding/json` in a few ways:

| | `encoding/json` | `phpjson` |
| --- | --- | --- |
| `/` | `/` | `\/` |
| Non-ASCII (`é`, `😀`) | the UTF-8 bytes | `\u00e9`, `\ud83d\ude00` |
| `<`, `>`, `&` | `\u003c`, `\u003e`, `\u0026` | as they are |
| A decoded whole number written `2.0` (a `json.Number`) | `2.0` | `2` |
| A small float (`0.00001`) | `0.00001` | `1.0e-5` |
| An empty object, or one keyed `"0"`, `"1"`, ... in order (from `Encode`) | `{}` | `[]`, a list |

### When to Use It

`phpjson` is a compatibility layer, not a house format. Use `encoding/json` and tagged structs for anything whose bytes have no such history. Reach for `phpjson` when output must match bytes already stored or already compared: a fingerprint over a JSON payload, a column whose stored rows other code compares byte for byte, a request body that resends stored history.

### Encoding Decoded Values

`phpjson.Encode` writes a value you decoded with [`ordered.Decode`](helpers.md#ordered-json-objects), keeping PHP's array rules: an object whose keys are `"0"`, `"1"`, ... in order, or an empty object, is written as a list, because that is how PHP decoded and re-encoded it:

```go
value, err := ordered.Decode([]byte(`{"city": "Palma/Mallorca", "price": 12.0, "tags": {}, "list": {"0": "a", "1": "b"}}`))
if err != nil {
	return err
}

encoded, err := phpjson.Encode(value)
// {"city":"Palma\/Mallorca","price":12,"tags":[],"list":["a","b"]}
```

`Encode` accepts what `ordered.Decode` returns (`*ordered.Object`, `[]any`, `json.Number`, `string`, `bool` and `nil`), plus Go `int`, `int64`, `float64`, `[]string`, `map[string]any` (written with its keys sorted), `json.RawMessage` (decoded and re-encoded) and the empty struct, `struct{}{}`, which is the one way to write an empty object as `{}`. Any other type is an error, so a struct can't slip in with `encoding/json`'s spelling:

```go
phpjson.Encode(struct{}{})       // {}
phpjson.Encode(map[string]any{}) // []
phpjson.Encode([]int{1})         // error: phpjson: cannot encode []int
```

Numbers keep their written form where they can: a `json.Number` that is an integer within `int64` range is written as an integer, and anything else as a float in the dialect's spelling (the shortest digits that round-trip, no `.0` on whole numbers, and an exponent such as `1.0e-5` below `0.0001` or past 17 integer digits). `NaN` and the infinities are an error, and so is a string that isn't valid UTF-8.

### Marshaling Go Values

`phpjson.Marshal` writes any Go value in the dialect's spelling. `encoding/json` decides the structure, so struct tags, `omitempty` and custom marshalers all apply, an empty map stays `{}`, and map keys are sorted; then every string is re-quoted and every non-integer number re-spelled in the dialect:

```go
type FlightSelection struct {
	Route string  `json:"route"`
	Price float64 `json:"price"`
}

encoded, err := phpjson.Marshal(FlightSelection{Route: "LGW/PMI", Price: 12.5})
// {"route":"LGW\/PMI","price":12.5}
```

Use `Marshal` for typed values whose stored bytes were made in the dialect, and `Encode` for decoded values whose PHP array rules (an empty object written as `[]`) you need to keep.

### Normalizing Stored JSON

`phpjson.Reencode` decodes stored JSON and encodes it again in the dialect, returning a `json.RawMessage` you can put straight in a response. For example, an application might send a stored `segments` column this way, so a stored empty object goes out as the list the client expects:

```go
for _, row := range rows {
	// Re-encoding turns a stored empty object into the [] the client
	// expects.
	segments, err := phpjson.Reencode(row.Segments)
	if err != nil {
		return nil, err
	}

	listings = append(listings, SavedTripListing{ID: row.ID, Title: row.Title, Segments: segments, SavedAt: clock.AtomOf(row.SavedAt)})
}
```

### Text From Outside

`phpjson.EncodeSubstitute` is `Encode` that replaces every byte of an invalid UTF-8 sequence with U+FFFD instead of failing, in strings and object keys at any depth. Use it for text from outside the application, which can arrive cut in the middle of a character:

```go
encoded, err := phpjson.EncodeSubstitute([]any{"caf\xc3"}) // ["caf\ufffd"]
```

`phpjson.Quote` spells a single string in the dialect, and `phpjson.FloatString` a float the way PHP's `(string)` cast does: 14 significant digits, whole numbers without a fraction, and scientific notation (`1.0E+15`, `1.0E-5`) below `0.0001` or past 14 integer digits. Use `FloatString` for numbers written into text that PHP code produced before, such as a prompt an AI model is sent again:

```go
phpjson.Quote("<a href=\"/x\">é</a>") // "<a href=\"\/x\">\u00e9<\/a>"

phpjson.FloatString(0.1 + 0.2) // "0.3"
phpjson.FloatString(1e15)      // "1.0E+15"
```

### Reading Decoded Values

`phpjson.Get` and `phpjson.Value` read a decoded value with "dot" notation, through objects and lists alike. A key containing a dot is tried literally first; list indexes are written as plain decimal numbers:

```go
payload, err := ordered.Decode(body)
if err != nil {
	return err
}

departsAt, ok := phpjson.Get(payload, "itineraries.0.segments.0.departs_at")
airline := phpjson.Value(payload, "itineraries.0.segments.0.carrier.name") // nil when absent
```

The other readers inspect what a value is:

```go
phpjson.IsArray(value) // an object or a list: what PHP decoded into an array
phpjson.IsList(value)  // a list, or an object keyed "0", "1", ... in order
phpjson.Count(value)   // an object's or list's length; 0 for anything else
phpjson.Values(value)  // an object's or list's values in order; nil for anything else
```

An airline API integration whose upstream payloads vary in their field types reads them this way:

```go
if data := phpjson.Value(payload, "data"); phpjson.IsArray(data) && phpjson.IsList(data) {
	// ...
}
```

### Casting Like PHP

`phpjson.String` and `phpjson.Int` convert a decoded value as PHP's `(string)` and `(int)` casts do, for code whose output must reproduce text made that way:

```go
phpjson.String(json.Number("3"))   // "3"
phpjson.String(json.Number("3.0")) // "3"
phpjson.String(true)               // "1"
phpjson.String(false)              // ""
phpjson.String(nil)                // ""
phpjson.String(object)             // "Array"

phpjson.Int(json.Number("3.9")) // 3
phpjson.Int("12abc")            // 12: the longest numeric prefix
phpjson.Int("abc")              // 0
phpjson.Int(true)               // 1
```

`phpjson.WholeNumber` reports whether a decoded number is written as an integer (no fraction or exponent, within `int64` range) and returns its value:

```go
n, ok := phpjson.WholeNumber(json.Number("42"))  // 42, true
n, ok = phpjson.WholeNumber(json.Number("42.0")) // 0, false
```

> [!WARNING]
> `String` expects a decoded value. A Go `int` or `float64` is not one, and reads as `"Array"`; pass a `json.Number`, or use `strconv` for Go numbers. `Int` also accepts Go `int` and `float64`.

### Available Functions

| Function | Description |
| --- | --- |
| `phpjson.Encode(value any) ([]byte, error)` | Writes a decoded value in the dialect, keeping PHP's array rules. |
| `phpjson.EncodeSubstitute(value any) ([]byte, error)` | `Encode`, replacing invalid UTF-8 with U+FFFD instead of failing. |
| `phpjson.Marshal(value any) ([]byte, error)` | Writes any Go value through `encoding/json`, respelled in the dialect. |
| `phpjson.Reencode(raw []byte) (json.RawMessage, error)` | `ordered.Decode` then `Encode`. |
| `phpjson.Quote(s string) string` | A JSON string in the dialect. |
| `phpjson.FloatString(f float64) string` | A float as PHP's `(string)` cast spells it. |
| `phpjson.Get(value any, key string) (any, bool)` | Reads a dot path, and whether it was present. |
| `phpjson.Value(value any, key string) any` | `Get` without the flag; `nil` when absent. |
| `phpjson.Values(value any) []any` | An object's or list's values in order. |
| `phpjson.Count(value any) int` | An object's or list's length. |
| `phpjson.IsArray(value any) bool` | Whether the value is a decoded object or list. |
| `phpjson.IsList(value any) bool` | Whether the value is a list, or an object keyed `0..n-1` in order. |
| `phpjson.String(value any) string` | A decoded value as PHP's `(string)` cast spells it. |
| `phpjson.Int(value any) int` | A decoded value as PHP's `(int)` cast reads it. |
| `phpjson.WholeNumber(n json.Number) (int, bool)` | Whether a number is written as an integer, and its value. |

## Encryption Payloads

`crypt.Encrypter` writes Laravel's encrypter envelope: AES-256-CBC, with an HMAC-SHA256 over the IV and the ciphertext, as base64 of the JSON object `{"iv", "value", "mac", "tag"}`. `APP_KEY` has Laravel's format too: `base64:` followed by 32 base64-encoded bytes (a raw 32-byte string is accepted as well), and `APP_PREVIOUS_KEYS` lists the keys a payload may also have been sealed with.

`Encrypt` and `Decrypt` frame the value as a PHP serialized string (`s:16:"...";`), the framing Laravel's `encrypt()` helper writes, so a two-factor secret sealed by one application opens in the other. `EncryptString` and `DecryptString` seal the text as it is, as Laravel's `encryptString()` does. See [encryption](encryption.md).

## Encrypted Cookies

Every encrypted cookie's plaintext is prefixed with `hex(HMAC-SHA1(name + "v2", key)) + "|"` before it is sealed, as Laravel's cookie encryption does, so a value can't be replayed under another cookie's name. The `cookie` package holds the format:

```go
sealed, err := cookie.Encrypt(encrypter, "flights_session", sessionID)

value, ok := cookie.Decrypt(encrypter, "flights_session", sealed)
```

`cookie.Prefix(name, key)` returns the prefix itself. `cookie.Decrypt` accepts a value sealed under any of the encrypter's keys and treats anything else as absent. See [HTTP responses](responses.md#cookies-and-encryption).

## Sessions

The `session` package stores sessions where and how Laravel's database session driver does with JSON serialization: rows in a `sessions` table (`id`, `user_id`, `ip_address`, `user_agent`, `payload`, `last_activity`), the payload base64-encoded JSON, keyed by a 40-character id that travels in the encrypted session cookie. The attributes Laravel keeps in the payload keep their names: `_token` (the CSRF token), `_flash` (`{"old": [...], "new": [...]}`), `_previous` (`{"url": ..., "route": ...}`) and `auth.password_confirmed_at`, which a dotted key reads from the nested `auth` object. See [HTTP session](session.md).

## Authentication

The session guard is configured with the names of its session key and remember-me cookie, and an application moving over from Laravel passes Laravel's: `login_web_` and `remember_web_` followed by `sha1("Illuminate\Auth\SessionGuard")`:

```go
LoginKey:       "login_web_59ba36addc2b2f9401580f014c7f58ea4e30989d",
RecallerCookie: "remember_web_59ba36addc2b2f9401580f014c7f58ea4e30989d",
```

The remember-me cookie's value is Laravel's recaller, `id|token|hash`: the user's id, their 60-character remember token, and an HMAC-SHA256 of their password hash keyed with `APP_KEY` exactly as written, `base64:` prefix included (`GuardConfig.HashKey`). A recaller issued by either application signs the user in on the other. See [authentication](authentication.md).

## CSRF Tokens

CSRF protection reads the token where Laravel's does: the `_token` input field, the `X-CSRF-TOKEN` header, or the `X-XSRF-TOKEN` header holding the encrypted `XSRF-TOKEN` cookie the single-page application echoes back. A passing response re-sends the `XSRF-TOKEN` cookie. See [CSRF protection](csrf.md).

## Password Hashes

The `hashing` package writes bcrypt hashes with the `$2y$` prefix PHP's `password_hash` writes, and cuts passwords at bcrypt's 72-byte limit as PHP does, so every stored hash keeps checking. A hash made at another cost is upgraded when its user next signs in. See [hashing](hashing.md).

## JSON Columns

A model's `json`, typed `json:<Type>` and `encrypted:json` [casts](casts.md) encode a value with the function `database/models.json` names as `jsonEncoder` (`codegen.Spec.JSONEncoder`), `encoding/json.Marshal` when unset. An application moving over from Laravel names `phpjson.Marshal`, so a row written now has the same bytes as the rows already stored:

```json
{
  "package": "models",
  "generator": "go generate ./app/models",
  "localImports": "example.com/flights",
  "jsonEncoder": "github.com/atobeach/reef/phpjson.Marshal",
  "models": {
    "profiles": {
      "name": "Profile",
      "casts": { "preferences": "json", "recovery_codes": "encrypted:json", "seat_choices": "json:SeatChoices" }
    }
  }
}
```

The `encrypted` casts seal with the [encrypter](#encryption-payloads) on the context, so an encrypted column written by either application opens in the other. See [models](models.md).

## Timestamps

The `clock` package's formats are the ones a Laravel application's JSON carries, so a frontend written against one reads the other unchanged. `clock.Time` marshals as Laravel serializes a model's dates, UTC with microseconds and a `Z` (`2026-10-04T16:30:55.000000Z`); `clock.Atom` as Carbon's `toAtomString()`, to the second with the offset (`2026-07-27T00:00:00+00:00`); and `clock.DateTimeString` writes the `Y-m-d H:i:s` text a `timestamp(0)` column holds, which is how an uncast column reads. See [helpers](helpers.md#dates-and-time).

## Slugs and ASCII Folding

`support.ASCII` transliterates with the "en" map of voku/portable-ascii, the library behind Laravel's `Str::ascii`, and `support.Slug` follows `Str::slug`, so the slugs and folded name columns stored by a Laravel application are the ones reef computes for the same text. `support.Plural`, `support.Title`, `support.StrLimit` and `support.StripTags` likewise reproduce `Str::plural`, `Str::title`, `Str::limit` and PHP's `strip_tags` where stored or compared text depends on them. See [helpers](helpers.md).

## Migration Adoption

reef runs migrations with goose, recorded in the `goose_db_version` table. A database first migrated by another tool, such as Laravel's migrator, already holds the schema some migrations create. `migration.Commands.Adopt` (and `Migrator.Adopt`) records those migrations as applied without running them, inside one transaction under goose's advisory lock, before `migrate` runs the rest:

```go
migration.Commands{
	FS:    migrations.FS,
	Dir:   filepath.Join("database", "migrations"),
	Pool:  migrationPool(a, appPool),
	Adopt: migrations.LaravelHandover,
}
```

Here `migrations.LaravelHandover` is a function you write in your migrations package when moving a Laravel application to reef: it might read Laravel's `migrations` table (never writing it) and match each pending migration against the Laravel migrations that table lists, so a database Laravel migrated continues on goose from the right version. The function must return a leading run of the pending migrations, so goose's history stays gapless. See [migrations](migrations.md).

## Realtime

The `realtime` server speaks the Pusher Channels protocol (version 7) and the Pusher HTTP API, byte for byte, which is also what Laravel Reverb speaks: the frontend's `laravel-echo` and `pusher-js` clients connect to it unchanged, and any Pusher publisher can trigger events on it. Its command is named `reverb:start`, and an application may read its settings from the `REVERB_*` variables Laravel uses. Broadcast payloads and frames are encoded with `broadcasting.EncodeASCIIJSON`, the same spelling as `phpjson`'s strings (slashes escaped, non-ASCII `\u`-escaped), because the HTTP API signs the body's bytes. See [broadcasting](broadcasting.md).

## Environment Files

`env` reads `.env` files the way Laravel's does where it matters for existing files: `null` and `(null)` read as unset, `(true)` and `(false)` as booleans, and an exported variable always wins over the file. See [configuration](configuration.md#environment-variable-types).
