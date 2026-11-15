# Encryption

- [Introduction](#introduction)
- [Configuration](#configuration)
    - [Gracefully Rotating Encryption Keys](#gracefully-rotating-encryption-keys)
- [Using the Encrypter](#using-the-encrypter)
    - [Encrypting a Value](#encrypting-a-value)
    - [Decrypting a Value](#decrypting-a-value)
    - [Serialized Strings](#serialized-strings)
- [The Encrypter on the Context](#the-encrypter-on-the-context)
- [Using the Keys Directly](#using-the-keys-directly)
- [The Payload Format](#the-payload-format)
- [Available Methods](#available-methods)

## Introduction

reef's `crypt` package provides a simple interface for encrypting and decrypting text with AES-256-CBC. Every encrypted value is signed with a message authentication code (MAC), an HMAC-SHA256, so that its underlying value cannot be modified or tampered with once encrypted.

reef encrypts more than the values you seal yourself. The [`Cookies` middleware](middleware.md#encrypting-cookies) encrypts every cookie your application sends (bar the names you exempt), the [CSRF middleware](csrf.md#x-xsrf-token) opens the `X-XSRF-TOKEN` header (the `XSRF-TOKEN` cookie echoed back) with it, the `encrypted` and `encrypted:json` [casts](casts.md) seal model columns, and queued [mail](mail.md) is sealed while it waits when the mailer has an encrypter. All of them use the same `*crypt.Encrypter`, built from your application key.

There is no `Crypt` facade: you build one encrypter at boot and hand it to the code that needs it, or read it from the context.

## Configuration

Before using the encrypter, you need an application key: 32 random bytes, usually written as `base64:` followed by their base64 encoding in the `APP_KEY` environment variable. reef has no `key:generate` command; you may generate a key with any secure random source, for example:

```shell
echo "APP_KEY=base64:$(openssl rand -base64 32)"
```

You build the encrypter with `crypt.New`, passing the key and any [previous keys](#gracefully-rotating-encryption-keys). Build it once, at boot, and refuse to start with a bad key:

```go
import (
	"fmt"

	"github.com/atobeach/reef/crypt"
)

encrypter, err := crypt.New(cfg.App.Key, cfg.App.PreviousKeys...)
if err != nil {
	return nil, fmt.Errorf("APP_KEY: %w", err)
}
```

`crypt.New` accepts a key in either form `crypt.ParseKey` does: `base64:` followed by standard base64, or 32 raw bytes. A key that isn't valid base64, or isn't exactly 32 bytes, is an error.

### Gracefully Rotating Encryption Keys

If you change your application's encryption key, every cookie sealed with the old one, the session and remember-me cookies included, stops opening, so every user is signed out. In addition, it will no longer be possible to decrypt any data that was encrypted with your previous key.

To avoid this, pass your previous keys to `crypt.New` after the current one. A typical application reads them from the comma-separated `APP_PREVIOUS_KEYS` environment variable:

```ini
APP_KEY="base64:J63qRTDLub5NuZvP+kb8YIorGS6qFYHKVo6u7179stY="
APP_PREVIOUS_KEYS="base64:2nLsGFGzyoae2ax3EF2Lyq/hH6QghBGLIq5uL+Gp8/w="
```

```go
PreviousKeys: env.List("APP_PREVIOUS_KEYS", ""),
```

The encrypter always uses the current key when encrypting. When decrypting, it tries the current key first and then each previous key in order, opening the value with the first key whose MAC matches. Users keep using your application uninterrupted while you rotate, and data you re-save is sealed under the new key.

> [!NOTE]
> A previous key that isn't a valid key is a boot error too, just as a bad `APP_KEY` is.

> [!WARNING]
> Previous keys only help the encrypter. The [authentication guard](authentication.md#remembering-users) binds each remember-me cookie to the user's password with an HMAC keyed by the `HashKey` you give it, the current `APP_KEY` alone, so rotating the key still ends every remember-me sign-in: users stay signed in until their session ends, then sign in again.

## Using the Encrypter

### Encrypting a Value

You may encrypt a value using the `EncryptString` method of the encrypter. Each call uses a fresh random IV, so encrypting the same text twice gives two different payloads:

```go
sealed, err := encrypter.EncryptString(token)
if err != nil {
	return err
}
```

reef's mailer, when you give it an encrypter, does exactly this with every mailable it queues, so a message's recipients and contents never wait in the queue in the clear:

```go
sealed, err := m.config.Encrypter.EncryptString(string(plaintext))
```

> [!NOTE]
> For a model column, prefer the `encrypted` [cast](casts.md#encrypted-casting) in `database/models.json`: the model's writes seal the value with `EncryptString` for you, and queries open it, so your model holds the plain text.

### Decrypting a Value

You may decrypt a value using the `DecryptString` method. If the value cannot be decrypted, because it was tampered with, sealed under a key the encrypter doesn't hold, or isn't a payload at all, `DecryptString` returns `crypt.ErrInvalidPayload`:

```go
plain, err := encrypter.DecryptString(sealed)
if errors.Is(err, crypt.ErrInvalidPayload) {
	// The value was tampered with, or sealed under another key...
}
```

`ErrInvalidPayload` is the only error `DecryptString` returns, on purpose: telling a bad MAC from bad padding would hand an attacker a padding oracle.

### Serialized Strings

Laravel's `Crypt::encrypt` serializes its value with PHP's `serialize` before sealing it, so a string is stored as `s:16:"JBSWY3DPEHPK3PXP";`. Data sealed that way, such as the two-factor secrets and recovery codes Laravel Fortify stores, needs the same framing to read and write. The `Encrypt` and `Decrypt` methods add and remove it:

```go
secret, err := encrypter.Encrypt(twofactor.GenerateSecretKey())
if err != nil {
	return err
}

// ...

plain, err := encrypter.Decrypt(*user.TwoFactorSecret)
```

`Decrypt` returns `crypt.ErrInvalidPayload` when the payload doesn't open, and `crypt.ErrNotAString` when it opens but holds something other than a serialized string (an array, a number, or a string whose declared length is wrong). reef reads no other serialized values.

Use `EncryptString` and `DecryptString` for anything new; reach for `Encrypt` and `Decrypt` only where the stored data already carries the framing.

## The Encrypter on the Context

The `encrypted` casts seal and open columns deep inside the query builder, where no encrypter field can reach. So your application binds its encrypter onto every context its kernels hand out, with `crypt.With`. A typical application does it in `BindContext`, the bind its HTTP kernel, console kernel and queue workers all take as their first argument and run:

```go
// BindContext puts the app's connection, clock and encrypter on ctx: every
// request, command and queued job runs on them.
func (a *App) BindContext(ctx context.Context) context.Context {
	return crypt.With(clock.With(db.WithConn(ctx, a.DB), a.Clock), a.Encrypter)
}
```

`crypt.FromContext` returns the encrypter bound to a context, so code that only holds a `ctx` may use it too:

```go
encrypter, err := crypt.FromContext(ctx)
if err != nil {
	return err
}

sealed, err := encrypter.EncryptString(value)
```

When no encrypter was bound, `FromContext` returns `crypt.ErrNoEncrypter`. Writing an encrypted column on such a context fails with that error rather than storing the value in the clear: it means a context was built without your app's bind (`BindContext`), which is a programming error. See the [request lifecycle](lifecycle.md) for where contexts are bound.

## Using the Keys Directly

Occasionally you need the key itself, to sign something rather than encrypt it. The encrypter's `Key` method returns the current key's 32 bytes. reef's `cookie` package signs the name prefix of every cookie it seals with it, and a password broker may derive its reset tokens with an HMAC under it:

```go
mac := hmac.New(sha256.New, b.Encrypter.Key())
mac.Write([]byte(support.RandomString(40)))
token := hex.EncodeToString(mac.Sum(nil))
```

Sign with `Key` alone: a new signature always uses the current key, as a new payload does. The `Keys` method returns every key the encrypter holds: the current one first, then the previous ones in the order you passed them. Check a signature against each of them, so a value signed before a key rotation still verifies. This is how reef's `cookie` package recognizes a cookie's name prefix:

```go
for _, key := range encrypter.Keys() {
	if rest, ok := strings.CutPrefix(plain, cookie.Prefix(name, key)); ok {
		return rest, true
	}
}
```

The slices `Key` and `Keys` return share the encrypter's own memory: read them, never write to them.

`crypt.ParseKey` is the decoding `crypt.New` applies to each key: it returns the 32 key bytes of an `APP_KEY`-style string, or an error. You may use it to check a key on its own, say in a console command:

```go
if _, err := crypt.ParseKey(env.Get("APP_KEY", "")); err != nil {
	return err
}
```

## The Payload Format

A payload is Laravel's encrypter envelope, so values sealed by a Laravel application with the same key and its default `AES-256-CBC` cipher open in reef, and the other way round:

1. The plain text is padded (PKCS#7) and encrypted with AES-256-CBC under a random 16-byte IV.
2. The MAC is the hex HMAC-SHA256, under the key, of the base64 IV followed by the base64 ciphertext.
3. The payload is the standard base64 of the JSON object `{"iv": …, "value": …, "mac": …, "tag": ""}`; `tag` is always empty for CBC.

Decryption checks the MAC, in constant time, before it decrypts anything. See [compatibility](compatibility.md) for the other formats reef shares with Laravel, such as the cookie name prefix.

## Available Methods

- `crypt.New(appKey string, previousKeys ...string) (*Encrypter, error)`: an encrypter sealing with `appKey` and opening with it or any previous key. `crypt.New(cfg.App.Key, cfg.App.PreviousKeys...)`
- `crypt.ParseKey(appKey string) ([]byte, error)`: the 32 bytes of a `base64:…` or raw key. `key, err := crypt.ParseKey(appKey)`
- `Encrypter.EncryptString(plaintext string) (string, error)`: seals `plaintext` as is. `sealed, err := e.EncryptString("secret")`
- `Encrypter.DecryptString(encoded string) (string, error)`: opens a payload sealed under any known key. `plain, err := e.DecryptString(sealed)`
- `Encrypter.Encrypt(value string) (string, error)`: seals `value` framed as a PHP serialized string. `sealed, err := e.Encrypt(secret)`
- `Encrypter.Decrypt(payload string) (string, error)`: opens a payload `Encrypt` sealed, removing the framing. `secret, err := e.Decrypt(sealed)`
- `Encrypter.Key() []byte`: the current key. `hmac.New(sha256.New, e.Key())`
- `Encrypter.Keys() [][]byte`: the current key, then the previous ones. `for _, key := range e.Keys() { … }`
- `crypt.With(ctx context.Context, e *Encrypter) context.Context`: binds `e` to `ctx` for encrypted columns. `ctx = crypt.With(ctx, encrypter)`
- `crypt.FromContext(ctx context.Context) (*Encrypter, error)`: the encrypter bound to `ctx`, or `ErrNoEncrypter`. `e, err := crypt.FromContext(ctx)`
- `crypt.ErrInvalidPayload`: every way a payload can fail to open: bad encoding, a MAC mismatch or bad padding.
- `crypt.ErrNotAString`: a payload that opened but isn't a serialized string, from `Decrypt`.
- `crypt.ErrNoEncrypter`: no encrypter was bound to the context, from `FromContext` and the encrypted casts.
