# Model Factories

- [Introduction](#introduction)
- [Defining Model Factories](#defining-model-factories)
    - [Writing a Factory](#writing-a-factory)
    - [Factory States](#factory-states)
    - [Factory Callbacks](#factory-callbacks)
- [Creating Models Using Factories](#creating-models-using-factories)
    - [Persisting Models](#persisting-models)
    - [Overriding Attributes](#overriding-attributes)
    - [Creating Many Models](#creating-many-models)
    - [Sequences](#sequences)
    - [Timestamps](#timestamps)
- [Factory Relationships](#factory-relationships)
    - [Belongs To Relationships](#belongs-to-relationships)
    - [Has Many Relationships](#has-many-relationships)
    - [Many to Many Relationships](#many-to-many-relationships)
- [The Test Case](#the-test-case)
- [Available Methods](#available-methods)

## Introduction

When testing your application, you will need a few rows in the database. Instead of spelling out every column of each one, reef lets you define a factory per [model](models.md): a definition that makes a valid row, named states that change it, and `Create`, which takes the test's own overrides, typed against the model. Factories are built on `reef/db/factory`.

To see what a factory looks like, here is a user factory from an application's `database/factories/users.go`:

```go
package factories

import (
	"context"
	"fmt"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/db/factory"

	"example.com/flights/app/models"
)

// UserPassword is every factory user's password, and UserPasswordHash a
// cheap bcrypt of it, so factory users sign in without paying production's
// hashing cost.
const (
	UserPassword     = "password"
	UserPasswordHash = "$2y$04$esrkzcB0tJxztBYIMqJZr.S2pK60kcf7rEzxOXKOIYtXaEFe5MoJi"
)

// UserFactory makes users.
type UserFactory struct {
	factory.Builder[models.User, *UserFactory]
}

// User makes a verified password user.
func User(tc factory.TestCase) *UserFactory {
	return factory.New(tc, &UserFactory{}, func(ctx context.Context) models.User {
		n := next()

		return models.User{
			Name:            fmt.Sprintf("Traveller %d", n),
			Email:           fmt.Sprintf("traveller%d@example.com", n),
			EmailVerifiedAt: new(clock.Now(ctx)),
			Password:        new(UserPasswordHash),
			RememberToken:   new(fmt.Sprintf("rt%08d", n)),
		}
	})
}

// Unverified leaves the user's email unverified.
func (f *UserFactory) Unverified() *UserFactory {
	return f.State(func(u *models.User) { u.EmailVerifiedAt = nil })
}
```

And a test uses it like this:

```go
user := factories.User(tc).Create()

unverified := factories.User(tc).Unverified().Create(func(u *models.User) {
	u.Email = "ana@example.com"
})
```

Factories are only for tests: they take the [test case](#the-test-case) and fail it when an insert fails.

## Defining Model Factories

### Writing a Factory

There is no `make:factory` command and no `HasFactory` discovery: a factory is a type and a constructor function you write in your app's factories package (conventionally `database/factories`). The type embeds `factory.Builder[T, F]`, where `T` is the model and `F` is a pointer to the factory type itself, so every chain method returns your type and your states chain with the builder's methods:

```go
// AirportFactory makes airports.
type AirportFactory struct {
	factory.Builder[models.Airport, *AirportFactory]
}
```

The constructor takes the test case and wires the type to it with `factory.New`, giving the definition of a row:

```go
// Airport makes an inactive airport.
func Airport(tc factory.TestCase) *AirportFactory {
	return factory.New(tc, &AirportFactory{}, func(context.Context) models.Airport {
		n := next()

		return models.Airport{
			Code:     fmt.Sprintf("X%05d", n),
			Name:     fmt.Sprintf("Airport %d", n),
			Timezone: "Europe/London",
		}
	})
}
```

The definition runs afresh for every row, so it is where unique values come from. reef has no fake-data generator; one counter for the whole test binary is enough to derive unique codes and references from:

```go
// sequence makes unique codes and references across a test binary.
var sequence atomic.Int64

func next() int64 {
	return sequence.Add(1)
}
```

The definition receives the test's context, so a value that depends on the time reads the test's clock with `clock.Now(ctx)`, and a frozen clock shows up in the rows.

### Factory States

States are named changes you may apply to a factory in any combination. Write each as a method on the factory that calls `State` with a function changing the row:

```go
// Lapsed is a booking whose hold ended five minutes ago.
func (f *BookingFactory) Lapsed() *BookingFactory {
	return f.State(func(b *models.Booking) { b.HoldExpiresAt = clock.Now(f.Context()).Add(-5 * time.Minute) })
}
```

`f.Context()` is the test's context, for a state that reads the test's clock. States run over the definition in the order they were chained, so a later state sees (and may override) what an earlier one did. A state may build on others, which is how a booking factory can walk a booking through its lifecycle:

```go
// Seated is a booking with a seat and a checked bag chosen, not yet paid.
func (f *BookingFactory) Seated() *BookingFactory {
	return f.State(func(b *models.Booking) {
		b.SeatNumber = new("14C")
		b.Bags = 1
	})
}

// Paid is seated, then paid with a payment reference.
func (f *BookingFactory) Paid() *BookingFactory {
	return f.Seated().State(func(b *models.Booking) {
		b.PaymentReference = new(fmt.Sprintf("PAY-%012d", next()))
		b.PaidAt = new(clock.Now(f.Context()))
	})
}
```

Every chain method returns a **new** factory and leaves the one it was called on as it was. One preset therefore serves every variant made from it:

```go
bookings := factories.Booking(tc).For(user)

paid := bookings.Paid().Create() // paid
open := bookings.Create()        // still unpaid
```

### Factory Callbacks

`AfterMaking` registers a change every row gets once its states and overrides are in, just before the insert. It is the place to fill in what the test left unset, most often a required parent:

```go
func Airport(tc factory.TestCase) *AirportFactory {
	return factory.New(tc, &AirportFactory{}, func(context.Context) models.Airport {
		// ...
	}).AfterMaking(func(a *models.Airport) {
		if a.CountryID == 0 {
			a.CountryID = Country(tc).Create().ID
		}
	})
}
```

Because it runs after the overrides, the parent is only made when neither a state nor the test named one. There is no `afterCreating`: `Create` returns the stored row, so do whatever comes next in the test (or in a helper) with the row it returns.

## Creating Models Using Factories

### Persisting Models

`Create` inserts a row and returns it as stored, with its id, its timestamps and anything a trigger set:

```go
spain := factories.Country(tc).Create()
valencia := factories.Airport(tc).For(spain).Active().Create()
```

The row is built in a fixed order: the definition, then the states in the order chained, then the overrides passed to `Create`, then the `AfterMaking` changes. It is inserted through the query builder with the model's table and schema, on the test's context, so it lands in the test's transaction and the model's [casts](casts.md) apply: an encrypted column is given as plaintext and sealed on the way in. If the insert fails, the test fails with `factory models.Airport: ...`.

> [!WARNING]
> `Create` writes every column, so a column the definition leaves at Go's zero value is stored as that zero value (a `0`, a `""`, a `false`, a `NULL` for a nil pointer), never as the table's default. Only an unset id, numbered by its sequence, and unset timestamps, stamped by the insert, are left to the database. Give the definition every value the row should have.

There is no `make` that builds a row without inserting it. When a test needs a model value that isn't stored, write the struct literal.

### Overriding Attributes

Pass `Create` one or more functions that change the row. They run after the states, in order, and are typed against the model, so a misspelled column is a compile error:

```go
factories.Airport(tc).Create(func(a *models.Airport) {
	a.Code = "VLC"
	a.Name = "Valencia"
	a.Timezone = "Europe/Madrid"
	a.AvgWeeklyFlights = new(950)
})
```

A one-off change you'd like to keep on a preset may also be applied with `State` directly:

```go
endingSoon := factories.Booking(tc).State(func(b *models.Booking) { b.HoldExpiresAt = clock.Now(tc.Ctx).Add(9 * time.Minute) })

seated := endingSoon.Seated().Create()
plain := endingSoon.Create()
```

### Creating Many Models

`Count` makes `Create` insert that many rows and return them as a slice. Each row runs the definition afresh and gets the same states and overrides:

```go
bookings := factories.Booking(tc).For(user).Paid().Count(3).Create()

factories.Airport(tc).For(australia).Count(8).Create(func(a *models.Airport) { a.AvgWeeklyFlights = new(100) })
```

### Sequences

To vary a column from row to row, let an override keep its own position. `Count(n).Create` creates the rows one after another, calling the overrides once per row:

```go
codes := []string{"CDG", "ORY"}
i := 0

factories.Airport(tc).Count(len(codes)).Create(func(a *models.Airport) {
	a.Code = codes[i]
	i++
})
```

### Timestamps

A model whose [schema](casts.md) has `created_at` and `updated_at` gets them stamped by the insert from the test's clock whenever the row leaves them unset; a value the row sets wins. That is usually what you want, and with a [frozen clock](mocking.md#interacting-with-time) the stamps are predictable.

When a test needs a row exactly as some other writer would have left it, such as one that was never updated, use `WithoutTimestamps`. `Create` then stamps nothing: the row's own timestamps are written as made, and a nil one as `NULL`:

```go
// Never updated: no updated_at.
factories.Country(tc).WithoutTimestamps().Create(func(c *models.Country) {
	c.Name = "Thailand"
	c.Code = "TH"
})
```

## Factory Relationships

reef factories don't have Laravel's `has`, `for` or magic relationship methods. Relationships are plain columns on the model, and a factory sets them with states, which keeps every row a test makes visible in the test.

### Belongs To Relationships

Give a child's factory a `For` state that points it at a parent, and an [`AfterMaking`](#factory-callbacks) callback that makes a parent when the test names none:

```go
// For puts the airport in country.
func (f *AirportFactory) For(country models.Country) *AirportFactory {
	return f.State(func(a *models.Airport) { a.CountryID = country.ID })
}
```

A test then either names the parent or lets the factory make one, and never leaves a stray parent behind when it names one:

```go
spain := factories.Country(tc).Create()

factories.Airport(tc).For(spain).Create() // in Spain
factories.Airport(tc).Create()            // in a new country of its own
```

To share one parent between many rows, create it once and pass it to each `For`, as `spain` is above.

### Has Many Relationships

Create the parent first, then its children `For` it:

```go
user := factories.User(tc).Create()
factories.Profile(tc).For(user).Count(3).Create()
```

### Many to Many Relationships

Create both sides, then insert the pivot rows through the pivot's [model](relationships.md#many-to-many), which stamps their timestamps:

```go
user := factories.User(tc).Create()
valencia := factories.Airport(tc).Create()

if _, err := models.Favourites(tc.Ctx).Create(db.Values{"user_id": user.ID, "airport_id": valencia.ID}); err != nil {
	t.Fatal(err)
}
```

## The Test Case

A factory constructor takes a `factory.TestCase`, the test it makes rows for:

```go
type TestCase interface {
	T() testing.TB
	Context() context.Context
}
```

`T` is the test the factory fails when an insert fails. `Context` carries the connection (the test's transaction) and the clock the rows are written with. A `*tests.TestCase` of your own (see [Building a Test Case](testing.md#building-a-test-case)) can implement both, which is why `factories.User(tc)` takes the test case directly. A unit test that has a context of its own may satisfy the interface with a small struct:

```go
type testCase struct {
	t   testing.TB
	ctx context.Context
}

func (tc testCase) T() testing.TB            { return tc.t }
func (tc testCase) Context() context.Context { return tc.ctx }
```

## Available Methods

#### `factory.New`

```go
func New[T db.Model, F any, P factory[T, F]](tc TestCase, f P, definition func(ctx context.Context) T) P
```

Wires a factory type (a struct embedding `Builder`) to the test case and the definition of a row, and returns it. `T` is inferred from the definition, `F` and `P` from the factory pointer: `factory.New(tc, &AirportFactory{}, func(context.Context) models.Airport {...})`.

#### `State`

Returns a new factory with one more change every row gets over the definition, after the states before it. Your named states are made of it.

```go
f.State(func(b *models.Booking) { b.PaidAt = new(clock.Now(f.Context())) })
```

#### `AfterMaking`

Returns a new factory with a change every row gets after its states and overrides, just before the insert.

```go
f.AfterMaking(func(a *models.Airport) { a.Code = strings.ToUpper(a.Code) })
```

#### `WithoutTimestamps`

Returns a new factory whose `Create` stamps no timestamps: the row's own are written as made, a nil one as `NULL`.

```go
factories.Country(tc).WithoutTimestamps().Create()
```

#### `Context`

The test's context, for a state that reads the test's clock.

```go
clock.Now(f.Context())
```

#### `Count`

Returns a `*factory.Many[T]` whose `Create` inserts `n` rows.

```go
factories.Booking(tc).For(user).Paid().Count(3).Create()
```

#### `Create`

Inserts a row, after applying the given overrides, and returns it as stored.

```go
factories.Airport(tc).Create(func(a *models.Airport) { a.Code = "VLC" })
```

#### `Many.Create`

Inserts the `Count` rows, each as `Create` inserts one, and returns them in order.

```go
airports := factories.Airport(tc).Count(2).Create()
```
