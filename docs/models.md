# Models: Getting Started

- [Introduction](#introduction)
- [Generating Models](#generating-models)
    - [The Model Spec](#the-model-spec)
    - [Running the Generator](#running-the-generator)
    - [Keeping Models in Sync](#keeping-models-in-sync)
- [Model Conventions](#model-conventions)
    - [Table and Model Names](#table-and-model-names)
    - [Primary Keys](#primary-keys)
    - [Column Types](#column-types)
    - [Column Identifiers](#column-identifiers)
    - [Timestamps](#timestamps)
- [Retrieving Models](#retrieving-models)
    - [Refreshing Models](#refreshing-models)
- [Retrieving Single Models](#retrieving-single-models)
    - [Not Found Exceptions](#not-found-exceptions)
- [Inserting and Updating Models](#inserting-and-updating-models)
    - [Inserts](#inserts)
    - [Updates](#updates)
    - [Upserts](#upserts)
- [Deleting Models](#deleting-models)
- [Pruning Models](#pruning-models)
- [Query Scopes](#query-scopes)
    - [Local Scopes](#local-scopes)
    - [Combining Scopes](#combining-scopes)
    - [Dynamic Scopes](#dynamic-scopes)
    - [Scopes and the Clock](#scopes-and-the-clock)
- [Adding Behavior to Models](#adding-behavior-to-models)
- [Generic Code Over Models](#generic-code-over-models)
- [Available Functions](#available-functions)
    - [Model Generation](#model-generation)
    - [Models and Pruning](#models-and-pruning)

## Introduction

A reef model is generated Go code. For every table you list in `database/models.json`, the generator writes a `<model>_gen.go` file holding:

- a plain struct with one typed field per column;
- its `TableName` and `Schema` methods (the `db.Model` interface), the schema carrying each column's Postgres type, the model's [casts and timestamps](casts.md) and its [route key](routing.md#customizing-the-key);
- two sets of [column identifiers](#column-identifiers) naming every column, bare (`AirportColumns`) and qualified by the table (`AirportTableColumns`);
- a query type, on which you write the model's [scopes](#query-scopes);
- a constructor that starts a query;
- a method per [relationship](relationships.md), and for each belongs-to a `With` method on the query that [eager loads](relationships.md#eager-loading) it into the struct's `Loaded` field;
- for a model keyed by a non-null `id` column, `Update`, `Refresh` and `Delete` methods that [write its own row](#updates).

Here is part of a generated `Airport`:

```go
// Airport is a row of `airports`.
type Airport struct {
	ID               int64           `db:"id"`
	Code             string          `db:"code"`
	Name             string          `db:"name"`
	CountryID        int64           `db:"country_id"`
	CityID           *int64          `db:"city_id"`
	Active           bool            `db:"active"`
	AvgWeeklyFlights *int            `db:"avg_weekly_flights"`
	Facilities       json.RawMessage `db:"facilities"`
	CreatedAt        *time.Time      `db:"created_at"`
	UpdatedAt        *time.Time      `db:"updated_at"`
	// ...

	// Loaded holds the relations the query's With methods loaded.
	Loaded AirportRelations `db:"-" json:"-"`
}

// AirportRelations are the relations AirportQuery's With methods load, each nil
// until loaded or when its key is null or names no row.
type AirportRelations struct {
	City    *City
	Country *Country
}

// airportSchema is how `airports` is written: its timestamps, column types and casts.
var airportSchema = db.Schema{
	CreatedAt: "created_at",
	UpdatedAt: "updated_at",
	RouteKey:  "code",
	Columns: map[string]db.Column{
		"id":   {Type: "int8"},
		"code": {Type: "varchar"},
		"name": {Type: "varchar"},
		// ...
	},
}

// airportColumns names each column of `airports`.
type airportColumns struct {
	ID        string
	Code      string
	Name      string
	CountryID string
	// ...
}

// AirportColumns name the columns of `airports`, for a query on the table alone and the keys of a write.
var AirportColumns = airportColumns{
	ID:        "id",
	Code:      "code",
	Name:      "name",
	CountryID: "country_id",
	// ...
}

// AirportTableColumns name the columns of `airports` qualified by the table, for a scope or a join.
var AirportTableColumns = airportColumns{
	ID:        "airports.id",
	Code:      "airports.code",
	Name:      "airports.name",
	CountryID: "airports.country_id",
	// ...
}

// AirportQuery queries `airports`; scopes are methods on it.
type AirportQuery struct {
	db.Builder[Airport, *AirportQuery]
}

// Airports starts a query over `airports`.
func Airports(ctx context.Context) *AirportQuery {
	return db.NewQuery[Airport](ctx, &AirportQuery{})
}

// Country is the `Country` this row belongs to (via country_id).
func (a Airport) Country(ctx context.Context) *CountryQuery {
	return Countries(ctx).WhereKey(a.CountryID)
}

// WithCountry eager-loads each row's Loaded.Country in one query; scopes narrow it.
func (q *AirportQuery) WithCountry(scopes ...func(q *CountryQuery)) *AirportQuery {
	return q.Eager(db.BelongsTo(Countries, scopes, func(a *Airport) (*int64, **Country) { return &a.CountryID, &a.Loaded.Country }))
}
```

Because the columns are discovered when the code is generated, the compiler checks every field you read and every column you name through an identifier: once the models are regenerated, a column a migration renamed or dropped breaks the build rather than a request.

A model is a plain struct read from a row: it has no `save` and no attributes beyond its fields. You read rows with the model's query, write the row you hold through the model itself (`user.Update(ctx, values)`, `passenger.Delete(ctx)`), and write many rows with a query. Everything on the [query builder](queries.md) page works on a model's query.

## Generating Models

### The Model Spec

`database/models.json` declares which tables become models. Only the tables it lists are generated, and relations are only generated between listed tables, so framework tables such as `jobs`, `cache` and goose's `goose_db_version` stay out:

```json
{
  "package": "models",
  "generator": "go generate ./app/models",
  "localImports": "example.com/flights",
  "jsonEncoder": "github.com/atobeach/reef/phpjson.Marshal",
  "models": {
    "agent_conversations": { "name": "AgentConversation", "timestamps": false },
    "airports": { "name": "Airport", "routeKey": "code" },
    "bookings": {
      "name": "Booking",
      "routeKey": "reference",
      "casts": {
        "passenger_data": "encrypted:json",
        "fare_snapshot": "json",
        "child_ages": "json:ChildAges"
      }
    },
    "countries": { "name": "Country", "routeKey": "code" },
    "profiles": {
      "name": "Profile",
      "casts": {
        "birth_date": "encrypted",
        "passport_number": "encrypted"
      }
    }
  }
}
```

| Key | Meaning |
|-----|---------|
| `package` | The Go package the files declare. |
| `generator` | The command the `// Code generated by ... DO NOT EDIT.` marker names. |
| `localImports` | The import path prefix grouped after the other imports, as goimports' `-local` does. |
| `jsonEncoder` | The func `json` and `encrypted:json` casts encode with, as `import/path.Func`. Defaults to `encoding/json.Marshal`. |
| `models` | Table name => model: its `name`, its `casts` (column => `encrypted`, `json`, `json:<Type>` or `encrypted:json`, see [casts](casts.md)), `timestamps` (`false` stops writes stamping `created_at` / `updated_at`, see [timestamps](casts.md#timestamps)) and `routeKey` (the column a route parameter binds it by, `id` when unset, see [route model binding](routing.md#customizing-the-key)). |

### Running the Generator

reef supplies the generator's steps in the `github.com/atobeach/reef/codegen` package; your app supplies a small command that strings them together. The command lives in its own `main` package that imports none of your app's models, so it still builds when the generated code it is about to replace doesn't. A `cmd/codegen` command like this one migrates a scratch database, reads its schema and writes the models:

```go
func run(ctx context.Context, adminURL, scratchURL, scratchName string) error {
	root, err := codegen.ModuleRoot()
	if err != nil {
		return err
	}

	// A scratch database built by running every migration (testkit.Migrated).
	pool, err := testkit.Migrated(ctx, adminURL, scratchURL, scratchName, migrations.FS)
	if err != nil {
		return err
	}
	defer pool.Close()

	spec, err := codegen.LoadSpec(filepath.Join(root, "database", "models.json"))
	if err != nil {
		return err
	}

	schema, err := codegen.Introspect(db.WithConn(ctx, pool))
	if err != nil {
		return err
	}

	files, err := codegen.Generate(spec, schema)
	if err != nil {
		return err
	}

	written, err := codegen.Write(filepath.Join(root, "app", "models"), files)
	if err != nil {
		return err
	}

	fmt.Printf("codegen: %d models written\n", written)

	return nil
}
```

Reading a database built from the migrations, rather than whatever state a developer's own database is in, makes the output reproducible. A `go:generate` directive in the models package wires the command to `go generate`:

```go
// Package models holds the database models.
package models

//go:generate go run ../../cmd/codegen models
```

So after every migration you run:

```shell
go generate ./app/models
```

> [!WARNING]
> `codegen.Write` replaces every `*_gen.go` file in the directory and deletes the generated file of a model no longer in the spec. Never edit a `_gen.go` file: put a model's scopes and behavior in a hand-written `<model>.go` beside it, which the generator never touches.

Generation fails, with an error naming the table (and the column, where there is one), when:

- the spec lists a table the schema doesn't have;
- a cast names a column the table doesn't have, isn't one of the [casts](casts.md#declaring-casts), or doesn't suit its column's type;
- a `json:<Type>` cast's type isn't an exported identifier of the models package (`json:childAges` and `json:data.ChildAges` fail);
- a column has a Postgres type with no Go mapping (see [column types](#column-types));
- a `routeKey` isn't a column of the table, or isn't an integer, `uuid` or text column;
- the `jsonEncoder` a JSON cast needs isn't written as `import/path.Func`;
- a field or relation method would collide with another field or method: a column named `loaded` collides with the eager-loaded relations' `Loaded` field, one named `table_name` or `schema` with the `db.Model` methods, and one named `update`, `refresh` or `delete` with the row's own writes.

### Keeping Models in Sync

A migration without a regeneration should fail CI, not production. Run the generator in a test against the migrated test database and compare the result with the files on disk:

```go
func TestGeneratedModelsMatchTheSchema(t *testing.T) {
	tc := tests.Setup(t)

	root, err := codegen.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	spec, err := codegen.LoadSpec(filepath.Join(root, "database", "models.json"))
	if err != nil {
		t.Fatal(err)
	}

	schema, err := codegen.Introspect(tc.Ctx)
	if err != nil {
		t.Fatal(err)
	}

	files, err := codegen.Generate(spec, schema)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(root, "app", "models", name))
		if err != nil || string(got) != string(want) {
			t.Errorf("app/models/%s is stale: run `go generate ./app/models`", name)
		}
	}
}
```

The identifiers only protect the code that uses them, so you may add a second test, say `TestModelColumnsAreNamedByTheirIdentifiers`, that parses the app's non-test code and fails, naming the file and line, on a string literal naming a model's column where a builder method takes a column or as a `db.Values` literal's key, on a qualified identifier as a write's key (in a `db.Values` literal or assigned to by index), and on a bare identifier set in a scope (see [column identifiers](#column-identifiers)). It reads the syntax alone, so it can't tell a `db.Values` map from any other once the map is a variable: a string assigned to by index (`values["paid_at"] = v`), a set bound to a local outside a scope, and an untyped `map[string]any` handed to a write go unseen.

## Model Conventions

### Table and Model Names

Nothing is inferred from a class name: the spec's key is the table and its `name` the model. From the model name, the generator derives:

- the file name, in snake case: `SavedSearch` writes `saved_search_gen.go`;
- the query type: `SavedSearchQuery`;
- the constructor, pluralized: `City` becomes `Cities`, `Search` becomes `Searches`, `FareClass` becomes `FareClasses`, `Airport` becomes `Airports`. A name ending in a consonant and `y` takes `ies`; one ending in `s`, `x`, `ch` or `sh` takes `es`; any other takes `s`.

Field names are the column names in Pascal case, with Go's initialisms upper-cased whole (`id`, `url`, `uuid`, `ip`, `api`, `json`, `html`, `http`, `sql`, `ttl`, `utc`): `country_id` becomes `CountryID`, `image_url` becomes `ImageURL`, `tsa_precheck` becomes `TsaPrecheck`.

### Primary Keys

reef assumes a model's primary key is its `id` column: `Find`, `FindOrFail`, `WhereKey`, `ResolveRouteBinding` (unless the model declares a `routeKey`), the generated [relations](relationships.md), [eager loading](relationships.md#eager-loading) and the row's own `Update`, `Refresh` and `Delete` all use it. A table keyed otherwise, or by a nullable `id`, is still a model, without those three methods; query it with `Where` on its key column:

```go
currency, err := models.Currencies(ctx).Where(models.CurrencyColumns.Code, "EUR").First()
```

### Column Types

Each column's Go type follows its Postgres type. A nullable column becomes a pointer, except where Go's zero value already means `NULL`:

| Postgres | Go | Nullable |
|----------|----|----------|
| `int8` | `int64` | `*int64` |
| `int4` | `int` | `*int` |
| `int2` | `int16` | `*int16` |
| `numeric`, `float8` | `float64` | `*float64` |
| `float4` | `float32` | `*float32` |
| `bool` | `bool` | `*bool` |
| `varchar`, `text`, `bpchar`, `uuid`, `time` | `string` | `*string` |
| `timestamp`, `timestamptz`, `date` | `time.Time` | `*time.Time` |
| `json`, `jsonb` | `json.RawMessage` | `json.RawMessage` |
| `bytea` | `[]byte` | `[]byte` |
| `_text`, `_varchar` | `[]string` | `[]string` |

Any other type (`geometry`, say) fails generation with `no Go type for Postgres "geometry"`; leave such a table out of the spec and query it with `db.Table`. A cast can change a field's type: an `encrypted:json` column is a `json.RawMessage`, and a `json:<Type>` cast types the field as the named models-package type, never a pointer (see [casts](casts.md#typed-json-columns)).

### Column Identifiers

A query or a write names a column by its model's generated identifier, never a string. Each model has two sets, sharing one struct type whose fields are named like the row's:

- `<Model>Columns` holds the bare names (`BookingColumns.PaidAt` is `"paid_at"`), for a query on the model's table alone and for the keys of a write: an `INSERT`'s columns and an `UPDATE`'s `SET` take the bare name, so a `db.Values` key is always bare;
- `<Model>TableColumns` holds the names qualified by the table (`BookingTableColumns.PaidAt` is `"bookings.paid_at"`), for a [scope](#local-scopes), which may run on a query that joins, and for a join's columns.

```go
claimed, err := models.Bookings(ctx).
	WhereKey(booking.ID).
	WhereNull(models.BookingColumns.PaidAt).
	Update(db.Values{models.BookingColumns.PaidAt: clock.Now(ctx)})
```

```go
type airportCountry struct {
	Name        string `db:"name"`
	CountryCode string `db:"country_code"`
}

rows, err := models.Airports(ctx).
	Join("countries", models.CountryTableColumns.ID, "=", models.AirportTableColumns.CountryID).
	Select(models.AirportTableColumns.Name, models.CountryTableColumns.Code+" AS country_code").
	GetAs[airportCountry]()
```

An identifier is the column's plain name, so an alias is concatenated onto it; a table name (`Join`'s first argument) and `"airports.*"` stay strings. A site naming many columns may bind a set to a short local first (`airport := models.AirportTableColumns`). The generated has-many relations use the qualified set (`Where(BookingTableColumns.UserID, u.ID)`).

The compiler checks that a named column exists: once the models are regenerated, a migration that renames or drops `paid_at` breaks the build at every `BookingColumns.PaidAt`. It doesn't check a value's type, or that a column belongs to the query's model: `CountryColumns.Name` inside an `Airports` query is the same `"name"`, so name the column through the query's own model. A raw fragment (`WhereRaw`, `OrderByRaw`, `SelectRaw`) is SQL text and names its columns as SQL does. The [drift test's string check](#keeping-models-in-sync) refuses a string naming a model's column outside the tests; tests name columns as strings, as they also do for the framework's tables that have no model.

### Timestamps

When a model's table has `created_at` and `updated_at` columns, an insert stamps both and an update stamps `updated_at`, from the clock the context carries, unless the write sets them itself. Set `"timestamps": false` in the spec to turn this off for a model, or call `WithoutTimestamps` for one write. The [timestamps](casts.md#timestamps) section has the details.

## Retrieving Models

A model's constructor starts a query over its table, and `Get` returns the matching rows as a slice of the model:

```go
airports, err := models.Airports(ctx).Active().OrderBy(models.AirportColumns.Name).Get()
if err != nil {
	return nil, err
}

for _, airport := range airports {
	fmt.Println(airport.Name)
}
```

The constructor takes the context the query runs on: in a controller, pass the request's `c`. Each model query is a [query builder](queries.md), so `Where`, `Join`, `OrderBy`, `Limit` and the rest are all available, along with the model's scopes.

To read only some columns, `Select` them; the fields you didn't select keep their zero values. To read a shape the model doesn't have, such as a join, scan the query into another type with [`GetAs`](queries.md#scanning-into-another-shape).

### Refreshing Models

A model value never changes behind your back: a query that writes its row doesn't update the struct you hold. To read the row again, call its `Refresh` method:

```go
if err := booking.Refresh(ctx); err != nil {
	return err
}
```

`Refresh` reads every column again and keeps the relations already in `Loaded`; load them again if the write may have changed their keys. A row that no longer exists returns a `*db.ModelNotFoundError`, wrapped with the model and its `id`.

## Retrieving Single Models

`Find` returns a pointer to the model with the given `id`, and `First` the first model a query matches; both return `nil` when there is none:

```go
airport, err := models.Airports(ctx).Find(airportID)
if err != nil {
	return err
}

if airport == nil {
	// No such airport.
}

booking, err := models.Bookings(ctx).Where(models.BookingColumns.Reference, reference).First()
```

### Not Found Exceptions

Sometimes a missing model should end the request with a 404. `FindOrFail`, `FirstOrFail` and [`ResolveRouteBinding`](routing.md#route-model-binding) return the model itself, or a `*db.ModelNotFoundError` when there is none:

```go
func (AirportController) Show(c *web.Context) (models.Airport, error) {
	airport, err := models.Airports(c).Active().Where(models.AirportColumns.Code, c.Query("code")).FirstOrFail()
	if err != nil {
		return models.Airport{}, err
	}

	return airport, nil
}
```

The HTTP kernel renders a `*db.ModelNotFoundError` as a 404 whose message names the model in words, even when it was wrapped with `%w` on the way up:

```json
{ "message": "Airport not found." }
```

The error's `Model` field is the model's name (the row type's Go name), its `Error` method the message, and its `HTTPStatus` method returns 404; the HTTP kernel renders any error with an `HTTPStatus` method this way (see [errors](errors.md)).

To answer the same way when you decide a model is missing yourself, return the error with the model's name:

```go
return nil, &db.ModelNotFoundError{Model: "SavedSearch"} // "Saved search not found."
```

## Inserting and Updating Models

### Inserts

To insert a row, call `Create` on the model's query with a `db.Values` map of column => value, each column named by its bare [identifier](#column-identifiers). It returns the model as stored, with its `id`, column defaults and timestamps filled in:

```go
passenger, err := models.SavedPassengers(c).Create(db.Values{
	models.SavedPassengerColumns.UserID:         userID,
	models.SavedPassengerColumns.Name:           name,
	models.SavedPassengerColumns.DateOfBirth:    dateOfBirth,
	models.SavedPassengerColumns.PassportNumber: passportNumber,
})
```

The model's casts apply to the values (an encrypted column is sealed, a `json` column encoded), and `created_at` and `updated_at` are stamped.

When you don't need the row back, `Insert` writes one or more rows in a single statement and reads nothing, and `InsertOrIgnore` skips any row a unique index already holds. Both are described in [insert statements](queries.md#insert-statements).

There is no mass assignment protection to configure, because there are no attributes to fill: the values you pass are the columns written. Build them from a validated [form request](validation.md)'s typed fields, never from the raw input.

### Updates

To update the row a model was read from, call its `Update` method with the columns to change:

```go
if err := user.Update(c, db.Values{models.UserColumns.PreferredCurrency: "EUR"}); err != nil {
	return nil, err
}
```

`Update` writes only when some value differs from what is stored, and then touches `updated_at`, so saving a row's own values again neither writes nor makes the row look recently changed. The comparison runs in the `UPDATE`'s `WHERE`, against the stored row, each value compared as its column's type. The same statement returns the row (`RETURNING *`), which `Update` reads back into the struct: its new values, `updated_at` and any column a trigger set are there without a second query, and the relations in `Loaded` stay as loaded. When nothing differs, nothing is written and the struct is left as it was. A failure comes back wrapped with the model and its `id` (`updating profile 7: ...`).

> [!NOTE]
> An encrypted value always differs from the stored one, because each seal uses a fresh IV: `Update` always writes an [encrypted column](casts.md#encrypted-casting) it is given, and touches `updated_at`.

To update many rows, constrain a model's query and call `Update` on it. It returns the number of rows changed, and touches `updated_at`:

```go
airport := models.AirportColumns
if _, err := models.Airports(ctx).Where(airport.Code, code).Update(db.Values{airport.AvgWeeklyFlights: weeklyFlights}); err != nil {
	return err
}
```

The affected count of a conditional `Update` is how you make a write race-safe; it and `UpdateChanged`, the query form of a model's `Update`, are described in [update statements](queries.md#update-statements).

### Upserts

`UpdateOrCreate` updates the row matching its first map with the values in its second, or creates the row from both when none matches, and returns it as stored:

```go
quote := models.FareQuoteColumns
row, err := models.FareQuotes(ctx).UpdateOrCreate(
	db.Values{quote.FlightID: flight.ID, quote.QuotedOn: date},
	db.Values{quote.Amount: amount, quote.Currency: currency},
)
```

It is one atomic `INSERT ... ON CONFLICT DO UPDATE`, never a read followed by a write, so two concurrent calls can't both create the row. An update sets the values and `updated_at`, and leaves `created_at` as it was. With an empty second map, a row that already exists is returned as stored, only its `updated_at` touched.

> [!WARNING]
> Postgres finds the existing row through a unique index, so the columns in the first map need one of their own (here `fare_quotes`' unique `(flight_id, quoted_on)`). Without it the write fails with "there is no unique or exclusion constraint matching the ON CONFLICT specification".

To upsert many rows in one statement, use `Upsert`, described in [upserts](queries.md#upserts).

## Deleting Models

To delete the row a model was read from, call its `Delete` method:

```go
if err := passenger.Delete(c); err != nil {
	return nil, err
}
```

To delete many rows, constrain a model's query and call `Delete` on it; it returns how many it removed:

```go
if _, err := user.SavedPassengers(ctx).Delete(); err != nil {
	return err
}
```

reef has no soft deletes. If a model needs them, add a nullable `deleted_at` column and a scope that excludes it, and call the scope where it applies.

## Pruning Models

Sometimes you will want to delete models that are no longer needed, such as fare quotes far in the past. First, write a scope that selects the prunable rows, conventionally named `Prunable`:

```go
// Prunable keeps fare quotes older than two weeks.
func (q *FareQuoteQuery) Prunable() *FareQuoteQuery {
	return q.WhereOp(FareQuoteTableColumns.QuotedOn, "<", q.Now().AddDate(0, 0, -14))
}
```

Then declare the model prunable with `db.MassPrunable`, giving it the model's name and a function that builds a fresh prune query (each chunk of the prune runs its own). The name becomes the returned `db.Prunable`'s `Model` field: what `--model` and `--except` match and what the command prints.

```go
// FareQuotePrunable registers FareQuote's prune query with `model:prune`.
func FareQuotePrunable() db.Prunable {
	return db.MassPrunable("FareQuote", func(ctx context.Context) *FareQuoteQuery {
		return FareQuotes(ctx).Prunable()
	})
}
```

Collect your prunable models in one list and register the `model:prune` command over it with `dbcmd.PruneCommand` (`github.com/atobeach/reef/db/dbcmd`; see [console](console.md)):

```go
// Prunables are the models `model:prune` knows.
func Prunables() []db.Prunable {
	return []db.Prunable{
		FareQuotePrunable(),
	}
}
```

```go
kernel.Register(dbcmd.PruneCommand(models.Prunables()))
```

The command runs on two methods of `db.Prunable` you can call yourself: `Count(ctx)` is how many rows a prune would delete (what `--pretend` prints), and `PruneAll(ctx, chunk, progress)` deletes them `chunk` rows at a time, handing `progress` the running total after each chunk.

Finally, [schedule](scheduling.md) it:

```go
s.Command("model:prune", "--model=FareQuote").Daily()
```

`model:prune` deletes each model's prunable rows in chunks, so one huge `DELETE` never holds locks on the whole table, and prints the running total after each chunk (`FareQuote 1000 records`). It accepts these options:

| Option | Meaning |
|--------|---------|
| `--model=*` | Prune only these models (matched by name, case-insensitively). |
| `--except=*` | Prune every model but these. |
| `--chunk=1000` | Rows per `DELETE` (at least 1). |
| `--pretend` | Count the prunable rows of each model instead of deleting them. |

A `--model` or `--except` name that matches no prunable model fails the command before anything is pruned, so a typo in the schedule can't turn the nightly prune into a no-op that succeeds, or prune the model it was meant to spare:

```shell
flights model:prune --model=FareQoute

No prunable model is named "FareQoute". Prunable models: FareQuote.
```

When the names are valid but select nothing (`--except` naming every model), it prints `No prunable models found.` and succeeds.

```shell
flights model:prune --pretend

120 [FareQuote] records will be pruned.
```

Pruning deletes rows with a query (each chunk is a `DELETE` with a limit, see [delete statements](queries.md#delete-statements)); no model code runs per row. To prune from your own code, call `PruneAll` on a `db.Prunable`, with a chunk size and an optional progress callback that receives the running total:

```go
deleted, err := models.FareQuotePrunable().PruneAll(ctx, 500, nil)
```

## Query Scopes

### Local Scopes

A scope is a method on the model's query type that adds constraints and returns the query. Write them in the hand-written file beside the generated one (`airport.go` beside `airport_gen.go`):

```go
// Active keeps airports currently served.
func (q *AirportQuery) Active() *AirportQuery {
	return q.Where(AirportTableColumns.Active, true)
}
```

```go
// RankedBusiestFirst orders major airports first, then by weekly flight volume.
func (q *AirportQuery) RankedBusiestFirst() *AirportQuery {
	return q.OrderByDesc(AirportTableColumns.MajorAirport).
		OrderByRaw("airports.avg_weekly_flights desc nulls last").
		OrderBy(AirportTableColumns.Name)
}
```

Because the builder's methods return the model's own query type, scopes and builder methods chain in any order:

```go
airports, err := models.Airports(ctx).Where(models.AirportColumns.CountryID, country.ID).Active().OrderBy(models.AirportColumns.Name).Get()
```

A scope names its columns through the qualified set (`AirportTableColumns.Active` is `airports.active`), as it may be used on a query that joins; the [drift test](#keeping-models-in-sync) refuses a bare identifier in one.

There are no global scopes: a constraint every query needs is a scope you call. For example, a `Bookings(ctx).Held()` scope is spelled out wherever only bookings still on hold qualify.

### Combining Scopes

Calling scopes one after another `AND`s their constraints. To keep the rows that match any of several scopes instead, pass them to [`WhereAny`](queries.md#logical-grouping), each wrapped in a function that receives the query. A traveller's trips page might list paid bookings, bookings awaiting payment and refunded ones:

```go
// Listed keeps the bookings a traveller's trips page shows: paid, awaiting
// payment, or refunded.
func (q *BookingQuery) Listed() *BookingQuery {
	return q.WhereAny(
		func(q *BookingQuery) { q.Completed() },
		func(q *BookingQuery) { q.AwaitingPayment() },
		func(q *BookingQuery) { q.Refunded() },
	)
}
```

Each scope's own constraints stay `AND`ed inside its group, so the combined scope is just another scope: `user.Bookings(c).Listed().Latest().Get()`.

### Dynamic Scopes

A scope may take parameters, like any other method:

```go
// HoldEndingBetween keeps unpaid bookings whose hold ends between from
// (exclusive) and until (inclusive).
func (q *BookingQuery) HoldEndingBetween(from time.Time, until time.Time) *BookingQuery {
	return q.WhereNull(BookingTableColumns.PaidAt).
		WhereOp(BookingTableColumns.HoldExpiresAt, "<=", until).
		WhereOp(BookingTableColumns.HoldExpiresAt, ">", from)
}
```

### Scopes and the Clock

A scope that compares against "now" should take the time from the query, never from `time.Now`: the query reads the clock its context carries, and tests freeze and move that clock (see [mocking](mocking.md)), so the scope follows them. `WherePast`, `WhereNowOrFuture` and `WhereTodayOrAfter` (see [additional where clauses](queries.md#additional-where-clauses)) compare a column with that clock's now or today:

```go
// Lapsed keeps bookings whose hold ended before they were paid.
func (q *BookingQuery) Lapsed() *BookingQuery {
	return q.WherePast(BookingTableColumns.HoldExpiresAt).WhereNull(BookingTableColumns.PaidAt)
}

// Held keeps bookings still on hold: unpaid, and the hold not yet ended.
func (q *BookingQuery) Held() *BookingQuery {
	return q.WhereNowOrFuture(BookingTableColumns.HoldExpiresAt).WhereNull(BookingTableColumns.PaidAt)
}
```

For any other comparison, `q.Now()` returns the time on the same clock, as the [prune scope](#pruning-models) above uses it: `q.WhereOp(FareQuoteTableColumns.QuotedOn, "<", q.Now().AddDate(0, 0, -14))`.

## Adding Behavior to Models

A model is a plain struct, so behavior is methods on it, written beside the generated file like scopes. A `User` model implements reef's `auth.Authenticatable` (see [authentication](authentication.md)) in `user.go`; here are two of its methods:

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
```

A method that writes takes the context to write on. Here is `Booking.MarkPaid`, an [atomic claim](queries.md#update-statements) that refreshes the struct afterwards:

```go
func (b *Booking) MarkPaid(ctx context.Context) (bool, error) {
	claimed, err := Bookings(ctx).
		WhereKey(b.ID).
		WhereNull(BookingColumns.PaidAt).
		Update(db.Values{BookingColumns.PaidAt: clock.Now(ctx)})
	if err != nil {
		return false, fmt.Errorf("marking booking %s paid: %w", b.Reference, err)
	}

	if err := b.Refresh(ctx); err != nil {
		return false, fmt.Errorf("marking booking %s paid: %w", b.Reference, err)
	}

	return claimed == 1, nil
}
```

A claim stays a query rather than the model's `Update`: the affected count is what tells the winner from a loser, and `Update`, a no-op when nothing differs, can't tell a lost race from a write that changed nothing.

A model has no events (`creating`, `saved`, ...). Do the work in the action or job that makes the write, or [dispatch an event](events.md) from it.

## Generic Code Over Models

Every generated model satisfies `db.Model`:

```go
type Model interface {
	TableName() string
	Schema() *Schema
}
```

`db.NewQuery` is what a generated constructor calls: it wires a query type (any struct embedding `db.Builder`) to a fresh builder over the model's table and schema. To write code that works for any model, pass it `&db.Rows[T]{}`, the query type with no scopes. reef's [factories](factories.md) are built this way:

```go
// countRows counts any model's rows.
func countRows[T db.Model](ctx context.Context) (int64, error) {
	return db.NewQuery[T](ctx, &db.Rows[T]{}).Count()
}

total, err := countRows[models.Airport](ctx)
```

You may also write a model by hand for a table you'd rather not generate: a struct with `db` tags, the two `db.Model` methods (a `nil` schema means no casts, no timestamps and no `ResolveRouteBinding`, which panics without a schema to name the key), a query type embedding `db.Builder` and a constructor:

```go
type Setting struct {
	Key   string `db:"key"`
	Value string `db:"value"`
}

func (Setting) TableName() string { return "settings" }

func (Setting) Schema() *db.Schema { return nil }

type SettingQuery struct {
	db.Builder[Setting, *SettingQuery]
}

func Settings(ctx context.Context) *SettingQuery {
	return db.NewQuery[Setting](ctx, &SettingQuery{})
}
```

## Available Functions

The `github.com/atobeach/reef/codegen` package generates the model files. Its steps are separate functions so the app's generator command, and the drift test, can run each one. What a generated model is to the builder, its not-found error and its pruning live in `github.com/atobeach/reef/db`, and the `model:prune` command in `github.com/atobeach/reef/db/dbcmd`, so the ORM never links the console.

### Model Generation

#### `codegen.Spec`

The parsed `database/models.json`:

| Field | JSON | Meaning |
|-------|------|---------|
| `Package` | `package` | The package clause of the generated files. |
| `Generator` | `generator` | The command named in the generated-code marker (`go generate ./app/models`). |
| `LocalImports` | `localImports` | The import path prefix grouped after the others. |
| `JSONEncoder` | `jsonEncoder` | The `import/path.Func` that `json` casts encode with; `encoding/json.Marshal` when empty. |
| `Models` | `models` | Table => `ModelSpec`. |

#### `codegen.ModelSpec`

One model: `Name` (`name`), the model's Go name; `Casts` (`casts`), column => `"encrypted"`, `"json"`, `"json:<Type>"` or `"encrypted:json"`; `Timestamps` (`timestamps`), a `*bool` that turns the timestamps off when `false` (unset means on); and `RouteKey` (`routeKey`), the integer, uuid or text column `ResolveRouteBinding` finds a row by (unset means `id`).

#### `codegen.LoadSpec`

`LoadSpec(path string) (codegen.Spec, error)` reads and parses a spec file.

#### `codegen.Introspect`

`Introspect(ctx context.Context) (codegen.Schema, error)` reads the `public` schema's columns and its single-column foreign keys over the connection bound to `ctx`.

#### `codegen.Schema`

The introspected schema: `Columns`, table => its `[]codegen.Column` in ordinal order, and `ForeignKeys`, a `[]codegen.ForeignKey`.

#### `codegen.Column`

One column: `Name`, `UDT` (the Postgres type name: `int8`, `varchar`, `timestamptz`, `jsonb`, ...) and `Nullable`.

#### `codegen.ForeignKey`

A single-column foreign key: `Table`.`Column` references `RefTable`.`RefColumn`. Only keys referencing an `id` column become [relations](relationships.md).

#### `codegen.Generate`

`Generate(spec codegen.Spec, schema codegen.Schema) (map[string][]byte, error)` renders one gofmt-formatted `<model>_gen.go` per spec'd table, keyed by file name.

#### `codegen.Write`

`Write(dir string, files map[string][]byte) (int, error)` writes the files into `dir`, deletes any other `*_gen.go` file there, and returns how many it wrote. Files not ending in `_gen.go` are never touched.

#### `codegen.ModuleRoot`

`ModuleRoot() (string, error)` is the nearest directory holding a `go.mod`, searching up from the working directory (`go generate` runs from the generating package's directory). Tests use it to find module files too. It returns `fs.ErrNotExist` when there is none.

### Models and Pruning

#### `db.Model`

The interface every generated model satisfies: `TableName() string` and `Schema() *db.Schema` (a `nil` schema means no casts, no timestamps and no `ResolveRouteBinding`). `db.NewQuery` takes it as its type parameter; see [generic code over models](#generic-code-over-models).

#### `db.ModelNotFoundError`

The error `FirstOrFail`, `FindOrFail` and `ResolveRouteBinding` return when there is no row (a model's `Refresh` returns it wrapped). Its `Model` field is the model's Go name; `Error()` spells it in words (`"Saved search not found."`), and `HTTPStatus()` returns 404, so the HTTP kernel renders it as a 404 even when wrapped: `return &db.ModelNotFoundError{Model: "SavedSearch"}`. See [not found exceptions](#not-found-exceptions).

#### `db.MassPrunable`

`MassPrunable[Q](model string, query func(ctx context.Context) Q) db.Prunable` declares a model's prune query: `query` builds a fresh query on every call (any query type with `Limit`, `Count` and `Delete`, which every model query has), as each chunk of a prune needs its own builder. `model` is the name `--model` and `--except` match: `db.MassPrunable("FareQuote", func(ctx context.Context) *FareQuoteQuery { return FareQuotes(ctx).Prunable() })`.

#### `db.Prunable`

A prunable model, as `MassPrunable` returns it. Its `Model` field is the model's name; `Count(ctx context.Context) (int64, error)` is how many rows a prune would delete; and `PruneAll(ctx context.Context, chunk int, progress func(total int64)) (int64, error)` deletes them `chunk` rows at a time, handing `progress` (when not `nil`) the running total after each chunk, and returns the total. A failed chunk returns the rows deleted so far with an error naming the model (`pruning FareQuote: ...`).

#### `dbcmd.PruneCommand`

`PruneCommand(prunables []db.Prunable) console.Command` is the `model:prune` command over your prunable models, with its `--model`, `--except`, `--chunk` and `--pretend` options: `kernel.Register(dbcmd.PruneCommand(models.Prunables()))`. See [pruning models](#pruning-models).
