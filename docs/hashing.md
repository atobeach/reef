# Hashing

- [Introduction](#introduction)
- [Configuration](#configuration)
- [Basic Usage](#basic-usage)
    - [Hashing Passwords](#hashing-passwords)
    - [Verifying That a Password Matches a Hash](#verifying-that-a-password-matches-a-hash)
    - [Determining if a Password Needs to be Rehashed](#determining-if-a-password-needs-to-be-rehashed)
- [Hash Algorithm Verification](#hash-algorithm-verification)
- [The Hash Format](#the-hash-format)
- [Available Methods](#available-methods)

## Introduction

reef's `hashing` package provides secure bcrypt hashing for storing user passwords. The [authentication guard](authentication.md) checks passwords with it, and you'll use it yourself wherever your application sets one: registration, a password change or a reset.

Bcrypt is a great choice for hashing passwords because its "work factor" is adjustable, which means that the time it takes to generate a hash can be increased as hardware power increases. When hashing passwords, slow is good. The longer an algorithm takes to hash a password, the longer it takes malicious users to generate "rainbow tables" of all possible string hash values that may be used in brute force attacks against applications.

reef hashes with bcrypt only; there are no Argon2 drivers and no `Hash` facade. You build one `*hashing.Hasher` and hand it to the code that needs it.

## Configuration

A `Hasher` holds a single setting, its bcrypt cost, in the `Rounds` field. You may build one with `hashing.New`:

```go
import "github.com/atobeach/reef/hashing"

hasher := hashing.New(cfg.Auth.BcryptRounds)
```

A typical application reads the cost from `BCRYPT_ROUNDS`, defaulting to `12`, and sets `4` in its `.env.testing`, bcrypt's minimum, so tests that create users stay fast:

```go
BcryptRounds: env.Int("BCRYPT_ROUNDS", 12),
```

Build the hasher once, at boot, and hand the same value to the guard (`auth.GuardConfig.Hasher`) and to the account actions that set passwords.

> [!WARNING]
> Keep `Rounds` between 4 and 31. Below 4, Go's bcrypt quietly hashes at its default cost of 10, which then never equals `Rounds`, so every hash [needs a rehash](#determining-if-a-password-needs-to-be-rehashed). Above 31, `Make` returns an error. A zero `Hasher{}` falls in the first case.

## Basic Usage

### Hashing Passwords

You may hash a password by calling the `Make` method on a `Hasher`. Here, a registration action hashes the new user's password before inserting the row; its controller validated the input with a `RegisterRequest` and hands over a typed `NewUser`:

```go
import (
	"context"
	"fmt"

	"example.com/flights/app/models"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/hashing"
)

// CreateNewUser creates a newly registered user.
type CreateNewUser struct {
	Hasher *hashing.Hasher
}

// NewUser is a validated registration.
type NewUser struct {
	Name     string
	Email    string
	Password string
}

func (a CreateNewUser) Create(ctx context.Context, input NewUser) (models.User, error) {
	password, err := a.Hasher.Make(input.Password)
	if err != nil {
		return models.User{}, fmt.Errorf("hashing the new user's password: %w", err)
	}

	user, err := models.Users(ctx).Create(db.Values{
		models.UserColumns.Name:     input.Name,
		models.UserColumns.Email:    input.Email,
		models.UserColumns.Password: password,
	})
	if err != nil {
		return models.User{}, fmt.Errorf("creating the user: %w", err)
	}

	return user, nil
}
```

`Make` returns the hash as a 60-character string such as `$2y$12$…`, salted afresh on every call, so hashing the same password twice gives two different hashes. It only fails when bcrypt does, such as for a cost above 31; the error is wrapped with `hashing:`.

### Verifying That a Password Matches a Hash

The `hashing.Check` function allows you to verify that a given plain-text string corresponds to a given hash. It is a package function, not a `Hasher` method, since a hash carries its own cost:

```go
if hashing.Check(password, user.AuthPassword()) {
	// ...
}
```

`Check` returns `false`, never an error, for a wrong password and for anything that isn't a bcrypt hash, an empty string included. A social-only account with no password therefore never matches.

You don't need `Check` to sign users in: the guard's `Attempt` and `Validate` methods call it for you, inside a [timebox](authentication.md#manually-authenticating-users). Nor to check a signed-in user's current password: the [`auth.CurrentPassword`](validation.md#current_password) validation rule calls it for you.

A hash is for any secret you only ever need to check, never read back. A password broker, for example, stores only the bcrypt of each reset token it mails, and checks the token a user brings back against it:

```go
return hashing.Check(token, row.Token), nil
```

### Determining if a Password Needs to be Rehashed

The `NeedsRehash` method allows you to determine if the work factor used by the hasher has changed since the password was hashed. It returns `true` when the hash's cost differs from `Rounds`, and for anything that isn't a bcrypt hash:

```go
if hasher.NeedsRehash(hashed) {
	hashed, err = hasher.Make(plain)
}
```

The guard already does this on every successful credential check and saves the new hash through your user provider, so raising `BCRYPT_ROUNDS` upgrades accounts as they sign in. See [automatic password rehashing](authentication.md#automatic-password-rehashing).

## Hash Algorithm Verification

Laravel's `Hash::check` throws when handed a hash of another algorithm. reef's `Check` simply doesn't match it: a value that isn't a well-formed bcrypt hash is never accepted. When you need to tell the two apart yourself, the package offers two functions.

`hashing.IsBcrypt` reports whether a string has a bcrypt hash's shape: 60 characters starting with `$2y$`, `$2a$` or `$2b$`:

```go
if !hashing.IsBcrypt(row.Token) {
	return false, nil
}
```

`hashing.Cost` reads the work factor from a bcrypt hash, and returns an error for anything `IsBcrypt` rejects:

```go
cost, err := hashing.Cost(*user.Password)
if err != nil {
	return err
}

if cost != hasher.Rounds {
	// ...
}
```

## The Hash Format

reef writes hashes in the format PHP's `password_hash` (and so Laravel) writes them, so a users table shared with, or inherited from, a Laravel application keeps signing everyone in:

- **The `$2y$` prefix.** Go's bcrypt writes `$2a$`; the algorithm is the same, and `Make` rewrites the prefix to `$2y$`. `Check` accepts all three prefixes.
- **The 72-byte limit.** Bcrypt only reads the first 72 bytes of a password. Go's bcrypt refuses longer input, so `Make` and `Check` cut the password at 72 bytes first, the same bytes PHP's `password_hash` reads, so two passwords that share their first 72 bytes match the same hash.

See [compatibility](compatibility.md) for the other formats reef shares with Laravel.

## Available Methods

- `hashing.New(rounds int) *Hasher`: a hasher at the given bcrypt cost. `hasher := hashing.New(12)`
- `Hasher.Rounds`: the bcrypt cost `Make` hashes at and `NeedsRehash` compares with. `hashing.Hasher{Rounds: 12}`
- `Hasher.Make(value string) (string, error)`: hashes `value` with a fresh salt, as a `$2y$` hash. `hashed, err := hasher.Make("password")`
- `hashing.Check(value string, hashed string) bool`: whether `value` matches `hashed`, at whatever cost it was made; `false` for an empty or non-bcrypt hash. `ok := hashing.Check("password", hashed)`
- `Hasher.NeedsRehash(hashed string) bool`: whether `hashed` was made at another cost, or isn't a bcrypt hash. `if hasher.NeedsRehash(hashed) { … }`
- `hashing.IsBcrypt(hashed string) bool`: whether `hashed` is a 60-character `$2y$`, `$2a$` or `$2b$` hash. `hashing.IsBcrypt(hashed)`
- `hashing.Cost(hashed string) (int, error)`: the work factor a bcrypt hash was made at. `cost, err := hashing.Cost(hashed)`
