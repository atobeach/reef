# Models: Casts

- [Introduction](#introduction)
- [Declaring Casts](#declaring-casts)
    - [Where Casts Apply](#where-casts-apply)
- [Encrypted Casting](#encrypted-casting)
    - [Encrypted JSON Casting](#encrypted-json-casting)
- [JSON Casting](#json-casting)
    - [Typed JSON Columns](#typed-json-columns)
    - [Choosing the JSON Encoder](#choosing-the-json-encoder)
- [Timestamps](#timestamps)
    - [Disabling Timestamps](#disabling-timestamps)
    - [Writes That Are Not Changes](#writes-that-are-not-changes)
    - [Time Precision](#time-precision)
- [Custom Casts](#custom-casts)
- [Available Functions](#available-functions)

## Introduction

Casts convert a column's value between what your app holds and what the database stores. A model's `encrypted` column holds plain text in the struct and a sealed payload in the table; a `json` column takes any Go value on write and stores it encoded, and a `json:<Type>` column also reads back as a Go type of your own. reef has three casts, `encrypted`, `json` (optionally typed, `json:<Type>`) and `encrypted:json`, and you declare them per column in `database/models.json`.

This page also covers the other thing a model's schema tells its writes: which [timestamps](#timestamps) to stamp.

## Declaring Casts

Add a `casts` map to a model in `database/models.json`, naming each cast column, and regenerate the models with `go generate ./app/models`:

```json
"bookings": {
  "name": "Booking",
  "casts": {
    "passenger_data": "encrypted:json",
    "fare_snapshot": "json",
    "child_ages": "json:ChildAges"
  }
}
```

The generator writes the casts into the model's schema, beside each column's Postgres type, and types each field for its cast:

```go
type Booking struct {
	// ...
	ChildAges     ChildAges       `db:"child_ages"`
	FareSnapshot  json.RawMessage `db:"fare_snapshot"`
	PassengerData json.RawMessage `db:"passenger_data"`
}

var bookingSchema = db.Schema{
	CreatedAt: "created_at",
	UpdatedAt: "updated_at",
	Columns: map[string]db.Column{
		"passenger_data": {Type: "text", Cast: db.EncryptedJSON(phpjson.Marshal)},
		"fare_snapshot":  {Type: "json", Cast: db.JSON(phpjson.Marshal)},
		"child_ages":     {Type: "json", Cast: db.JSON(phpjson.Marshal)},
		// ...
	},
}
```

Generation fails when a cast doesn't suit its column: `encrypted` and `encrypted:json` need a `text` or `varchar` column, `json` and `json:<Type>` a `json` or `jsonb` column. A `json:<Type>` cast's type must be an exported identifier of the models package (`json:childAges`, `json:data.ChildAges` and a bare `json:` fail), and there is no typed `encrypted:json:<Type>`. A cast on a column the table doesn't have, or a cast name other than these, fails too.

### Where Casts Apply

Casts are part of a model, so they apply to the model's own writes and to queries started from its constructor:

- a model's `Update`, and a query's `Create`, `Insert`, `InsertOrIgnore`, `UpdateOrCreate`, `Upsert`, `Update` and `UpdateChanged`, write each value through its column's cast;
- every read of the model's query reads cast columns through their casts, whatever it scans into: the model, a partial `Select`, a [`GetAs`](queries.md#scanning-into-another-shape) shape, a `map[string]any` row, or single columns read with [`Pluck`, `PluckBy` or `Value`](queries.md#retrieving-a-list-of-column-values).

Writes and reads through `db.Table`, `db.Exec` or the pgx connection from `db.Conn` apply no casts: they see the column as stored.

> [!NOTE]
> Casts apply to the values a query writes and the columns it reads, not to the values you compare against in `Where` clauses.

## Encrypted Casting

The `encrypted` cast keeps a column's text sealed at rest. The model's field holds the plain text (a `string`, or `*string` for a nullable column), and the column holds it encrypted with the [encrypter](encryption.md) the context carries:

```json
"profiles": {
  "name": "Profile",
  "casts": {
    "birth_date": "encrypted",
    "passport_number": "encrypted"
  }
}
```

```go
if err := profile.Update(ctx, db.Values{
	models.ProfileColumns.PassportNumber: passportNumber, // sealed on the way in
}); err != nil {
	return err
}

fmt.Println(*profile.PassportNumber) // read back, opened on the way out
```

The encrypter is bound to the context by your app's `BindContext`, with `crypt.With`, alongside the connection (see [the connection lives in the context](database.md#the-connection-lives-in-the-context)). Sealing or opening a value on a context without one fails with `crypt.ErrNoEncrypter`, rather than storing the value in the clear. The stored payload is Laravel's encrypter envelope, so rows Laravel sealed with the same `APP_KEY` open unchanged (see [compatibility](compatibility.md)).

An encrypted column takes text: writing a `string` or `*string` seals it, `nil` stores `NULL`, and any other value fails the write (`an encrypted column takes text, not int`). A stored value that doesn't open with the app's keys fails the read.

> [!WARNING]
> Every seal uses a fresh random IV, so the same text never encrypts to the same payload twice. You can't find a row by an encrypted column's value, and a model's [`Update`](models.md#updates) (like [`UpdateChanged`](queries.md#updating-only-what-changed)) always counts an encrypted value as changed.

### Encrypted JSON Casting

The `encrypted:json` cast combines the two: a value written is encoded as the [`json` cast](#json-casting) encodes it, then sealed as `encrypted` seals text. The model's field is a `json.RawMessage` holding the opened JSON, ready to decode:

```go
var passengers []Passenger
if err := json.Unmarshal(booking.PassengerData, &passengers); err != nil {
	return fmt.Errorf("decoding passengers: %w", err)
}
```

A `nil` value, or a value that encodes to JSON `null`, stores `NULL`.

## JSON Casting

The `json` cast is for `json` and `jsonb` columns you write Go values to. When you write through a model or its query:

- a `json.RawMessage` is stored as given: it is already JSON;
- `nil`, a `nil` `json.RawMessage` and any value that encodes to `null` store SQL `NULL`;
- any other value is encoded with the model spec's JSON encoder, a Go `string` included: `"Paris"` stores the JSON string `"Paris"`, as Laravel's `json` cast stores a string. To store JSON text you already hold, pass it as a `json.RawMessage`.

```go
err := booking.Update(ctx, db.Values{
	models.BookingColumns.FareSnapshot: snapshot, // a struct, encoded on the way in
})
```

On the way out, the field keeps the raw JSON as a `json.RawMessage` (as every `json` and `jsonb` field does, cast or not), and you decode it into a typed struct where you use it, unless the cast names a type.

### Typed JSON Columns

When every reader of a column decodes it the same way, name the type in the cast, as Eloquent names a custom cast class: `"child_ages": "json:ChildAges"`. The field becomes that type, an exported type you declare in the models package, and pgx decodes the column into it with `encoding/json` on every read, calling its `UnmarshalJSON` when it has one. An error it returns fails the read of the row. SQL `NULL` reads as the type's zero value without calling it, so the type is never a pointer and must hold `NULL` as its zero value (a nil slice or map).

```go
// ChildAges are the ages of the children a booking's fare was priced for.
type ChildAges []int

// UnmarshalJSON reads every stored shape of the column.
func (a *ChildAges) UnmarshalJSON(raw []byte) error {
	// ...
}
```

Writes still go through the cast's encoder, so give the type no `MarshalJSON` or `driver.Valuer` of its own: `ChildAges{3, 5}` stores `[3,5]`, `ChildAges{}` stores `[]`, and a nil one stores `NULL`, exactly as the plain values the type wraps would.

### Choosing the JSON Encoder

The JSON casts encode with the func named by the spec's `jsonEncoder`, written as `import/path.Func`; it must have the signature `func(any) ([]byte, error)`. The default is `encoding/json.Marshal`. An application that shares its database with a Laravel app may use `github.com/atobeach/reef/phpjson.Marshal`, so the bytes it stores are spelled the way the rows Laravel already wrote are (see [compatibility](compatibility.md)):

```json
{
  "package": "models",
  "jsonEncoder": "github.com/atobeach/reef/phpjson.Marshal",
  "models": { }
}
```

## Timestamps

When a model's table has `created_at` and `updated_at` columns, the generator records them in the model's schema, and writes through the model or its query stamp them from the clock the context carries:

- `Create`, `Insert` and `InsertOrIgnore` stamp both;
- `UpdateOrCreate` and `Upsert` stamp both on the rows they insert, and only `updated_at` on the rows they update, so `created_at` stays the time the row was first stored;
- a query's `Update` stamps `updated_at`, and so do a model's `Update` and a query's `UpdateChanged`, but only on the rows they actually change;
- a value the write sets itself wins over the stamp.

```go
user, err := models.Users(ctx).Create(db.Values{models.UserColumns.Name: name, models.UserColumns.Email: email})

// user.CreatedAt and user.UpdatedAt are clock.Now(ctx), to the second.
```

Because the time comes from `clock.Now(ctx)`, a test that freezes or moves its clock controls the timestamps too (see [mocking](mocking.md)). A table with only one of the two columns stamps just that one. Writes through `db.Table` and `db.Exec` stamp nothing: set the columns yourself.

### Disabling Timestamps

If a model's table has the columns but its writes shouldn't stamp them, set `"timestamps": false` for the model in `database/models.json`:

```json
"agent_conversations": { "name": "AgentConversation", "timestamps": false }
```

### Writes That Are Not Changes

Sometimes one write shouldn't count as a change to the row's content. `WithoutTimestamps` stops a single query's writes stamping anything. Saving a user's remember token is a good example, since signing in isn't a change to the account:

```go
_, err := models.Users(ctx).
	WhereKey(user.ID).
	WithoutTimestamps().
	Update(db.Values{models.UserColumns.RememberToken: token})
```

### Time Precision

Every `time.Time` (or `*time.Time`) the query builder binds, timestamps included, is sent in UTC and truncated to the second, which is how a `timestamp(0)` column stores it; Postgres would round a fraction up instead. It is still sent as a `time.Time`, so a `timestamptz` column receives an instant. `db.Exec` binds its arguments as given.

## Custom Casts

A cast is any value implementing `db.Cast`:

```go
type Cast interface {
	// Set is the stored form of a value written to the column.
	Set(ctx context.Context, value any) (any, error)
	// Scanner wraps dest, the field or value the column scans into.
	Scanner(ctx context.Context, dest any) any
}
```

`Set` receives each value written to the column and returns what to bind. `Scanner` receives the destination the column scans into (a pointer to the field) and returns what pgx should scan into instead: `dest` itself, or a `sql.Scanner` that converts and assigns to it.

The generator only emits the three built-in casts, so a custom cast belongs in a [hand-written model's](models.md#generic-code-over-models) schema:

```go
// lowercase stores text lower-cased and reads it as stored.
type lowercase struct{}

func (lowercase) Set(_ context.Context, value any) (any, error) {
	if s, ok := value.(string); ok {
		return strings.ToLower(s), nil
	}

	return value, nil
}

func (lowercase) Scanner(_ context.Context, dest any) any {
	return dest
}

var subscriberSchema = db.Schema{
	CreatedAt: "created_at",
	UpdatedAt: "updated_at",
	Columns: map[string]db.Column{
		"email": {Type: "varchar", Cast: lowercase{}},
	},
}

func (Subscriber) Schema() *db.Schema { return &subscriberSchema }
```

## Available Functions

#### `db.Schema`

What a model tells the builder about its table. `CreatedAt` and `UpdatedAt` name the columns its writes stamp (empty means none); `RouteKey` names the column [`ResolveRouteBinding`](routing.md#route-model-binding) finds a row by (empty means `id`); `Columns` maps each column to its `db.Column`.

#### `db.Column`

One column of a schema: `Type`, its Postgres type name (`udt_name`: `int8`, `text`, `jsonb`, ...), which a model's `Update` and `UpdateChanged` compare values as; and `Cast`, its `db.Cast`, or `nil` for none.

#### `db.Cast`

The interface a cast implements: `Set` for writes, `Scanner` for reads.

#### `db.Encrypted`

The `encrypted` cast, a `db.Cast` value: text sealed with the context's encrypter.

#### `db.JSON`

`JSON(encode func(any) ([]byte, error)) db.Cast` is the `json` cast (and the typed `json:<Type>` one), encoding every written value with `encode` but a `json.RawMessage`, which is stored as given.

#### `db.EncryptedJSON`

`EncryptedJSON(encode func(any) ([]byte, error)) db.Cast` is the `encrypted:json` cast: encoded with `encode`, then sealed.
