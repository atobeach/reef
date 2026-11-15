# Database: Query Builder

- [Introduction](#introduction)
- [Running Database Queries](#running-database-queries)
    - [Retrieving All Rows From a Table](#retrieving-all-rows-from-a-table)
    - [Retrieving a Single Row or Column](#retrieving-a-single-row-or-column)
    - [Retrieving a List of Column Values](#retrieving-a-list-of-column-values)
    - [Aggregates](#aggregates)
    - [Building a Fresh Query Each Time](#building-a-fresh-query-each-time)
- [Select Statements](#select-statements)
    - [Scanning Into Another Shape](#scanning-into-another-shape)
- [Raw Expressions](#raw-expressions)
- [Joins](#joins)
- [Basic Where Clauses](#basic-where-clauses)
    - [Where Clauses](#where-clauses)
    - [Additional Where Clauses](#additional-where-clauses)
    - [Logical Grouping](#logical-grouping)
- [Ordering, Limit and Offset](#ordering-limit-and-offset)
- [Conditional Clauses](#conditional-clauses)
- [Insert Statements](#insert-statements)
    - [Upserts](#upserts)
- [Update Statements](#update-statements)
    - [Updating Only What Changed](#updating-only-what-changed)
- [Delete Statements](#delete-statements)
- [Pessimistic Locking](#pessimistic-locking)
- [Debugging](#debugging)
- [Available Methods](#available-methods)
    - [Package Functions and Types](#package-functions-and-types)

## Introduction

reef's query builder gives you a fluent, typed way to build and run queries against PostgreSQL. It is the same builder whether you query a table directly, with `db.Table`, or through a [generated model](models.md), whose constructor (`models.Airports(ctx)`) returns a builder with the model's scopes added.

The builder binds every value you pass as a query parameter, so values never need escaping, and it double-quotes the table and column names you pass, so a reserved word is a safe column name. A column argument is always a name, never SQL: anything else panics where the query is built (see [raw expressions](#raw-expressions)).

The builder is generic over two types: `db.Builder[T, Q]` scans rows into `T`, and its chain methods return `Q`, the concrete query type. That is what lets a model's scopes and the builder's own methods chain in any order:

```go
airports, err := models.Airports(ctx).
	Where(models.AirportColumns.CountryID, country.ID).
	Active().
	OrderBy(models.AirportColumns.Name).
	Get()
```

Every builder runs on the connection bound to the context it was built from (see [the connection lives in the context](database.md#the-connection-lives-in-the-context)).

> [!NOTE]
> To keep them short, most examples on this page name columns as strings, as a `db.Table` query over a table without a model must. On a model's query, name each column by the model's generated identifier instead, as the example above and the model excerpts below do: `models.AirportColumns.Name` on a query of the table alone and as a write's key, `models.AirportTableColumns.Name` in a scope or a join (see [column identifiers](models.md#column-identifiers)).

> [!NOTE]
> The builder covers selects with joins, wheres, ordering, paging and row locks, inserts, upserts, updates and deletes. It has no `groupBy`, `union` or `increment`; write those statements in SQL with [`db.Exec` or `db.Conn`](database.md#running-sql-queries).

## Running Database Queries

### Retrieving All Rows From a Table

You may use `db.Table` to begin a query over any table. Its type parameter is the row type the results scan into, and `Get` runs the query and returns a slice of them:

```go
type sessionRow struct {
	ID           string `db:"id"`
	UserID       *int64 `db:"user_id"`
	LastActivity int    `db:"last_activity"`
}

sessions, err := db.Table[sessionRow](ctx, "sessions").Get()
if err != nil {
	return err
}

for _, session := range sessions {
	fmt.Println(session.ID)
}
```

Rows scan into a struct by column name:

- A field's column is its `db` tag, or its field name when it has none, matched case-insensitively. A `db:"-"` field is skipped.
- A column the struct has no field for is skipped, not an error, so a migration that adds a column never breaks a running `SELECT *`.
- A field the result set has no column for keeps its zero value. `GetAs` is stricter about the fields its row type declares itself (see [scanning into another shape](#scanning-into-another-shape)).
- The fields of an untagged embedded struct count as the outer struct's own, so you may extend a model with extra columns (see [scanning into another shape](#scanning-into-another-shape)).
- A nullable column needs a pointer field (or a `json.RawMessage`), as with pgx.

If you would rather not declare a struct, `map[string]any` scans each row into a column => value map:

```go
rows, err := db.Table[map[string]any](ctx, "sessions").Where("user_id", userID).Get()
```

`db.Table` returns a `*db.Rows[T]`, the query type for tables without a model. A table's writes through `db.Table` are taken as given: no [casts and no timestamps](casts.md), which belong to models.

### Retrieving a Single Row or Column

If you just need a single row, use `First`. It adds `LIMIT 1` and returns a pointer to the row, or `nil` when no row matches:

```go
token, err := models.PasswordResetTokens(ctx).Where(models.PasswordResetTokenColumns.Email, user.Email).First()
if err != nil {
	return err
}

if token == nil {
	// No reset was requested.
}
```

To fetch a row by its `id` column, use `Find`:

```go
airport, err := models.Airports(ctx).Find(airportID)
```

`FirstOrFail` and `FindOrFail` return the row itself, or a `*db.ModelNotFoundError` when there is none. The HTTP kernel renders that error as a 404, so a controller may simply return it (see [not found exceptions](models.md#not-found-exceptions)):

```go
airport, err := models.Airports(c).Active().Where("code", code).FirstOrFail()
```

If you don't need an entire row, you may extract a single value from it with `Value`. It selects only that column of the first matching row and returns a pointer to the value, or `nil` when no row matches:

```go
countryID, err := models.Airports(c).Where("code", destination).Value[int64]("country_id")
if err != nil {
	return nil, err
}

if countryID == nil {
	// No airport has that code.
}
```

### Retrieving a List of Column Values

If you would like a slice holding one column's values, use `Pluck`. Its type parameter is the column's Go type:

```go
names, err := country.Airports(ctx).Active().RankedBusiestFirst().Pluck[string]("name")
```

`PluckBy` returns a map instead: each row's first named column is the key and its second the value. When two rows share a key, the later row wins:

```go
idsByCode, err := models.Airports(ctx).PluckBy[string, int64]("code", "id")
```

> [!NOTE]
> Laravel's `pluck('id', 'code')` names the value column first. `PluckBy` names the key column first, the way the `map[K]V` it returns reads.

`Pluck`, `PluckBy` and `Value` select only the columns they name, on a copy of the query, so the query itself keeps its own select. Each argument is a column name (`code`, `countries.code`), never an expression or an alias: normalize the values in Go once they are read, or select an expression with [`SelectRaw`](#raw-expressions) and read it with `GetAs`. As with rows, a nullable column needs a pointer type, such as `Pluck[*string]`.

A query whose row type isn't a struct (a `string`, an `int64`, a `time.Time`, ...) scans each row's single column into it, so `Get` on such a query reads a list too:

```go
codes, err := db.Table[string](ctx, "airports").Select("code").Where("active", true).Get()
```

### Aggregates

`Count` returns the number of matching rows, `Exists` reports whether there are any, and `DoesntExist` whether there are none:

```go
total, err := country.Airports(ctx).Active().Count()

saved, err := models.SavedPassengers(ctx).Where("passport_number", number).Exists()

allPlaced, err := models.Airports(ctx).WhereNull("city_id").DoesntExist()
```

`Count` ignores the query's ordering, limit and offset. `Exists` and `DoesntExist` ask Postgres for `SELECT EXISTS (...)`, which stops at the first matching row where a count would read every one. For other aggregates, select the expression with [`SelectRaw`](#raw-expressions) and scan it with `GetAs`:

```go
sums, err := models.Airports(ctx).
	Where("country_id", country.ID).
	SelectRaw("coalesce(sum(avg_weekly_flights), 0)").
	GetAs[int64]()
```

### Building a Fresh Query Each Time

A terminal read leaves its query as it was: `First`, `FirstOrFail`, `Find`, `FindOrFail`, `ResolveRouteBinding`, `Pluck`, `PluckBy`, `Value` and `UpdateChanged` add their limit, key condition, select or changed-values condition to a copy, and the other reads and writes add nothing. So two `Find`s on one query each find their own row, and a `Pluck` before a `Get` doesn't narrow the `Get`.

A chain method is different: it changes the builder it is called on (and returns it), which is what lets a [scope](models.md#query-scopes) passed to `WhereAny` or a `With<Relation>` add its conditions. A builder is therefore not a template: `a, b := q.Completed(), q.AwaitingPayment()` are one query holding both scopes' conditions. Build a new query for each statement you run, and when several queries share the same base, write a function that returns it, such as one for the airports near a point:

```go
// withinRadius is the active airports inside the radius: the cheap bounding
// box, then the exact Haversine cut.
func withinRadius(ctx context.Context, latitude float64, longitude float64) *models.AirportQuery {
	latDelta, lngDelta := geo.BoxDeltas(latitude, NearbyAirportRadiusKm)
	haversine, bindings := geo.Haversine(models.AirportColumns.Latitude, models.AirportColumns.Longitude, latitude, longitude)

	return models.Airports(ctx).
		Where(models.AirportColumns.Active, true).
		WhereBetween(models.AirportColumns.Latitude, latitude-latDelta, latitude+latDelta).
		WhereBetween(models.AirportColumns.Longitude, longitude-lngDelta, longitude+lngDelta).
		WhereRaw(haversine+" <= ?", append(bindings, NearbyAirportRadiusKm)...)
}

count, err := withinRadius(ctx, lat, lng).Count()
busiest, err := withinRadius(ctx, lat, lng).OrderByDesc(models.AirportColumns.AvgWeeklyFlights).Limit(10).Get()
```

## Select Statements

A query selects `*` unless you say otherwise. `Select` replaces the column list; `AddSelect` appends to it:

```go
rows, err := models.Airports(ctx).Select("id", "code", "name").WhereIn("id", airportIDs).Get()

query := models.Countries(ctx).Select("id")
query.AddSelect("code", "name")
```

A select entry is a column name, plain or qualified by its table (and that by its schema, `information_schema.columns.table_name`), which is double-quoted (`airports.name` becomes `"airports"."name"`); `*` or `airports.*` (which becomes `"airports".*`); or a column name with an `AS` alias, in either case: `countries.code AS country_code`. Anything else, such as `count(*)`, panics where the query is built: select an expression with [`SelectRaw`](#raw-expressions).

### Scanning Into Another Shape

A join or a partial select often produces rows that don't fit the model. `GetAs` runs the query and scans its rows into a different type: a struct, a `map[string]any`, or a single value such as `int64`. Embedding the model in a struct keeps all of its fields:

```go
type statusUpdate struct {
	models.FlightStatusUpdate
	FlightNumber *string    `db:"flight_number"`
	DepartsAt    *time.Time `db:"departs_at"`
	ArrivesAt    *time.Time `db:"arrives_at"`
}

rows, err := models.FlightStatusUpdates(c).
	LeftJoin("flights", models.FlightTableColumns.ID, "=", models.FlightStatusUpdateTableColumns.FlightID).
	Select(
		"flight_status_updates.*",
		models.FlightTableColumns.Number+" AS flight_number",
		models.FlightTableColumns.DepartsAt+" AS departs_at",
		models.FlightTableColumns.ArrivesAt+" AS arrives_at",
	).
	Latest(models.FlightStatusUpdateTableColumns.PublishedAt).
	GetAs[statusUpdate]()
```

Every field the row type declares itself needs a column in the result: `GetAs` checks the result's columns before it reads a row, so a misspelled alias (`flight_nmber`) fails even a query that matched nothing, rather than reading as `null`. The error names each missing column; select it, or tag a field you fill yourself `db:"-"`. A column the row type has no field for is skipped, as with `Get`.

The fields an embedded model brings are exempt, and read as `Get` reads the model: whatever columns the result has. That keeps `flight_status_updates.*` working while a deploy is half done, both when a migration has added a column the running code's model lacks and when one has dropped a column the running code's model still has (a deploy typically migrates before the new servers start).

`GetAs` still reads the model's [cast](casts.md) columns through their casts, so an encrypted column in a partial select comes back opened. Its rows aren't the model, so they have no relations to fill: `GetAs` panics on a query that declares an [eager load](relationships.md#eager-loading), and so do `Pluck`, `PluckBy` and `Value`.

## Raw Expressions

Sometimes you need an expression the builder can't spell. `SelectRaw`, `WhereRaw` and `OrderByRaw` insert a raw SQL fragment. Fragments take `?` placeholders, which the builder numbers into Postgres's `$n` parameters in order:

```go
// A formula with three ? placeholders, and the bindings they take, in order.
haversine, bindings := geo.Haversine("latitude", "longitude", latitude, longitude)

airports, err := models.Airports(ctx).
	SelectRaw("airports.*, "+haversine+" AS distance_km", bindings...).
	WhereRaw(haversine+" <= ?", append(bindings, radiusKm)...).
	OrderByRaw("avg_weekly_flights desc nulls last").
	OrderBy("distance_km").
	Limit(limit).
	GetAs[NearbyAirport]()
```

Write `??` for a literal question mark, such as the jsonb `?` operator:

```go
withLounges, err := models.Airports(ctx).WhereRaw("facilities ?? ?", "lounges").Get()
```

A fragment with more `?` placeholders than bindings, or fewer, panics while the query is being built: it is a programming error, caught the first time the code runs.

> [!WARNING]
> Raw fragments are inserted into the query as written. Never build a fragment from user input; pass every value as a binding. They are the only way SQL enters a query: every other method's column and table arguments are names, and `WhereOp`'s and `Join`'s operator one from a fixed list, so `OrderBy(sortParam)` panics on anything but a column name rather than running it.

## Joins

The `Join` method adds an `INNER JOIN`. Its arguments are the table to join, then the two columns and the operator of the join condition. `LeftJoin` adds a `LEFT JOIN` the same way:

```go
listings, err := models.Airports(ctx).
	Active().
	Join("countries", "countries.id", "=", "airports.country_id").
	Select("airports.code", "airports.name", "countries.name AS country_name").
	SelectRaw("COALESCE(airports.timezone, 'UTC') AS timezone").
	GetAs[airportListing]()
```

The joined table may carry an alias: `Join("pg_class AS src", "src.oid", "=", "pg_constraint.conrelid")`. The two columns are names, and the operator is one `WhereOp` takes (`=`, `<>`, `!=`, `<`, `<=`, `>`, `>=`, `like`, `not like`, `ilike`, `not ilike`); a join on anything else is a condition for `WhereRaw`. Once a query joins, qualify the columns you name (`airports.name`) wherever they would be ambiguous. `WhereKey`, and `Latest()` with no column, qualify their own; qualify a column you pass to `Latest`, as `Latest(models.FlightStatusUpdateTableColumns.PublishedAt)` does above.

## Basic Where Clauses

### Where Clauses

The `Where` method adds a `column = value` condition. Every condition you add is joined to the others with `AND`:

```go
users, err := models.Users(ctx).Where("provider", "google").WhereNull("password").Get()
```

For any other comparison, use `WhereOp`, passing the operator as its second argument:

```go
recent, err := models.FlightStatusUpdates(ctx).
	WhereOp("published_at", ">=", clock.Now(ctx).AddDate(0, 0, -30)).
	Get()

matches, err := models.Airports(ctx).WhereOp("name", "ilike", "san%").Get()
```

The operator is one of `=`, `<>`, `!=`, `<`, `<=`, `>`, `>=`, `like`, `not like`, `ilike` and `not ilike`, in either case (it is written lower-case). Any other panics where the query is built; write that condition with [`WhereRaw`](#raw-expressions). Laravel keeps a list too, but falls back to `=` where reef fails loudly.

A `time.Time` (or `*time.Time`) bound anywhere in a query, as a where value, a raw binding or a written value, is sent in UTC and truncated to the second, as a `timestamp(0)` column stores it (Postgres would otherwise round a fraction up).

### Additional Where Clauses

**WhereBetween**

`WhereBetween` checks that a column's value lies between two values, both ends included:

```go
airports, err := models.Airports(ctx).
	Active().
	WhereBetween("latitude", latitude-radius, latitude+radius).
	WhereBetween("longitude", longitude-padding, longitude+padding).
	Get()
```

**WhereIn**

`WhereIn` checks that a column's value is in a slice, of any element type. The slice is bound as one array parameter (`column = ANY($1)`), so its length doesn't change the statement, and an empty slice matches no rows rather than failing:

```go
airports, err := models.Airports(ctx).WhereIn("id", ids).Get()
```

**WhereNull / WhereNotNull**

`WhereNull` checks that a column is `NULL`; `WhereNotNull` that it isn't:

```go
unpaid, err := models.Bookings(ctx).WhereNull("paid_at").Get()
```

**WherePast / WhereNowOrFuture**

`WherePast` keeps rows whose column is before now, and `WhereNowOrFuture` rows whose column is now or later. "Now" is the time on the clock the query's context carries, so a test that freezes or moves its clock moves these conditions with it (see [mocking](mocking.md)):

```go
lapsed, err := models.Bookings(ctx).WherePast("bookings.hold_expires_at").WhereNull("bookings.paid_at").Get()
```

**WhereTodayOrAfter**

`WhereTodayOrAfter` compares the column's date with today's date on the same clock, so a `date` column matches from today on, and a timestamp anywhere in today matches too:

```go
departures, err := airport.FlightsByOrigin(ctx).WhereTodayOrAfter("departs_at").OrderBy("departs_at").Limit(7).Get()
```

**WhereKey**

`WhereKey` filters on the primary key, the `id` column, qualified with the table name so it stays unambiguous after a join:

```go
passengers, err := models.SavedPassengers(ctx).WhereKey(passengerID).Get()
```

### Logical Grouping

The builder joins its conditions with `AND`, and has no `orWhere`. When a query should match any of several sets of conditions, pass each set to `WhereAny` as a scope: a function that receives the query and adds conditions to it. The conditions within a scope are `AND`ed, the scopes are `OR`ed, and the whole group is one parenthesized condition beside the rest of the query. Here a scope keeps the flights that are cancelled or delayed:

```go
// Disrupted keeps flights that are cancelled or delayed.
func (q *FlightQuery) Disrupted() *FlightQuery {
	return q.WhereAny(
		func(q *FlightQuery) { q.Where(FlightTableColumns.Cancelled, true) },
		func(q *FlightQuery) { q.Delayed() },
	)
}
```

```sql
select * from "flights" where (("flights"."cancelled" = $1) or ("flights"."delayed_until" is not null))
```

Because each scope is the model's query, it may call the model's own [scopes](models.md#combining-scopes). `WhereAny` panics when it is given no scopes, or a scope that adds no condition or changes anything but the conditions (a select, a join, an ordering, a limit, a lock, an eager load): an empty group would match every row, and the rest would be lost.

> [!NOTE]
> Laravel's `whereAny` compares several columns against one value. reef's takes scopes, so it covers the `orWhere` groups you would write in a Laravel closure.

For an `OR` that is simpler in SQL, use `WhereRaw`, which wraps its fragment in parentheses, so it groups correctly with the conditions around it:

```go
withExtras, err := models.Bookings(ctx).
	WhereNull("bookings.paid_at").
	WhereRaw("bookings.seat_id is not null or bookings.bags > 0 or bookings.meal is not null").
	Get()
```

## Ordering, Limit and Offset

`OrderBy` sorts by a column ascending and `OrderByDesc` descending; `OrderByRaw` appends a raw fragment to the `ORDER BY` clause. Each call adds to the ordering, in order:

```go
airports, err := models.Airports(ctx).
	OrderByDesc("airports.major_airport").
	OrderByRaw("airports.avg_weekly_flights desc nulls last").
	OrderBy("airports.name").
	Get()
```

`Latest` sorts newest first: by the table's `created_at` column (qualified with the table name) or, when you name one, by that column as written. Passing more than one column panics:

```go
passengers, err := user.SavedPassengers(c).Latest().Get()

booking, err := models.Bookings(ctx).Where("user_id", userID).Latest("id").First()
```

`Limit` caps the number of rows returned and `Offset` skips rows; a value of zero (or less) leaves the clause off:

```go
page, err := models.Airports(ctx).OrderBy("name").Offset(40).Limit(20).Get()
```

## Conditional Clauses

reef has no `when`: in Go, a clause that only sometimes applies is an `if`. A chain method changes the query it is called on, so you don't need to reassign the query:

```go
query := models.Airports(ctx).OrderBy("name")
if countryID != 0 {
	query.Where("country_id", countryID)
}

airports, err := query.Get()
```

## Insert Statements

The `Create` method inserts one row from a `db.Values` map (column => value) and returns it as stored, read back with `RETURNING *`, so ids, defaults and anything a trigger set are filled in:

```go
search, err := models.SavedSearches(ctx).Create(db.Values{
	"uuid":                   support.UUID7(),
	"payload":                string(payload),
	"destination_airport_id": airportID,
})
```

On a model's query, every insert applies the model's casts and stamps its `created_at` and `updated_at` from the context's clock (see [casts](casts.md)). On a `db.Table` query, the values are written as given.

If you don't need the row back, `Insert` writes one or more rows in a single statement and reads nothing. Every row must carry the same columns: rows that differ fail the insert before anything is written. With no rows, nothing runs.

```go
err := models.FlightStatusUpdates(ctx).Insert(db.Values{
	"flight_id":    flightID,
	"status":       status,
	"published_at": clock.Now(ctx),
})
```

The `InsertOrIgnore` method skips any row a unique index already holds, and returns how many rows it inserted. It is the race-safe way to make an insert idempotent: reading first and inserting when nothing was found lets two concurrent requests both see the row missing, and the loser trips the unique index.

```go
inserted, err := models.Favourites(ctx).InsertOrIgnore(db.Values{"user_id": user.ID, "airport_id": airportID})
```

### Upserts

`UpdateOrCreate` updates the row matching its first map with the values in its second, or creates it from both, in one atomic statement, and returns the row as stored. [Upserts](models.md#upserts) in the models guide describes it.

The `Upsert` method inserts many rows, or updates the ones that already exist, in one statement. Its rows are positional, one value per column in the order the first argument names them, so a bulk load of a million rows builds no map per row. The third argument names the columns that identify a row, and the fourth the columns to update when it already exists:

```go
err := models.Airports(ctx).Upsert(
	[]string{"code", "name", "timezone", "latitude", "longitude"},
	[][]any{
		{"LHR", "London Heathrow", "Europe/London", 51.47, -0.45},
		{"JFK", "John F. Kennedy International", "America/New_York", 40.64, -73.78},
	},
	[]string{"code"},
	[]string{"name", "timezone", "latitude", "longitude"},
)
```

A row with more or fewer values than there are columns fails the upsert before anything is written. On a model's query, `Upsert` applies the model's casts, stamps `created_at` and `updated_at` on the rows it inserts and touches only `updated_at` on the rows it updates. With no columns to update, a row that already exists is left as stored (bar a model's `updated_at`).

> [!WARNING]
> Both are `INSERT ... ON CONFLICT DO UPDATE`, and Postgres finds the existing row through a unique index: the identifying columns need one of their own, or the write fails with "there is no unique or exclusion constraint matching the ON CONFLICT specification".

## Update Statements

The `Update` method sets values on every row the query matches and returns how many rows it changed. On a model's query, it applies the model's casts and touches `updated_at`:

```go
_, err := country.Airports(ctx).Update(db.Values{"active": false})
```

To update the one row a model was read from, call the model's own [`Update`](models.md#updates) instead: `user.Update(ctx, values)`.

The affected count is how you write an atomic claim. Put the condition that makes the write legal on the `UPDATE` itself, and check whether your request won: of any number of racing requests, such as two passengers choosing the same seat, exactly one flips the column from `NULL`.

```go
claimed, err := models.Seats(ctx).
	WhereKey(seat.ID).
	WhereNull("passenger_id").
	Update(db.Values{"passenger_id": passenger.ID})
if err != nil {
	return false, err
}

return claimed == 1, nil
```

Updates use the query's where clauses only: its joins, ordering, limit and lock don't apply, so constrain an update by the updated table's own columns.

> [!WARNING]
> An `Update` or `Delete` with no where clause affects every row in the table.

### Updating Only What Changed

`UpdateChanged` is `Update` restricted to the matching rows where at least one of the values differs from what is stored, and it returns the rows it changed, as stored (`RETURNING *`). A write that would change nothing is not made, and so doesn't touch `updated_at`. A model's own `Update` is `UpdateChanged` on its row; on a query, it combines with a condition that must still hold, such as a change to a booking that must not have been paid meanwhile:

```go
updated, err := models.Bookings(ctx).WhereKey(booking.ID).WhereNull(models.BookingColumns.PaidAt).UpdateChanged(values)
```

Each value is compared as its column's type, so a numeric `150.00` equals `150`, and `json`/`jsonb` columns compare as `jsonb`, so equal documents are no change whatever their key order or spacing. A column the model's schema doesn't type (or any column of a `db.Table` query) compares as text. An empty `db.Values` runs nothing and returns no rows.

The changed rows are read back into the query's row type, so a `db.Table` query needs a struct (or `map[string]any`) row type: a single-value one such as `db.Table[int]` can't hold a row, and the `UPDATE` it made is then reported as an error.

> [!NOTE]
> An encrypted value always differs from the stored one, because each seal is new: `UpdateChanged` always writes an [encrypted column](casts.md#encrypted-casting).

## Delete Statements

The `Delete` method removes every matching row and returns how many it removed:

```go
_, err := user.SavedPassengers(ctx).Delete()
```

To delete the one row a model was read from, call the model's own [`Delete`](models.md#deleting-models): `passenger.Delete(ctx)`.

Postgres has no `DELETE ... LIMIT`, but the builder supports one: with a `Limit` (or a join), `Delete` removes at most that many rows, in the query's order, through a `ctid` subquery. [Pruning](models.md#pruning-models) uses this to delete in chunks:

```go
deleted, err := models.FareQuotes(ctx).Prunable().Limit(1000).Delete()
```

## Pessimistic Locking

To lock the rows a query reads until the surrounding transaction ends, call `LockForUpdate`. It adds `FOR UPDATE` to the `SELECT`, so another transaction that wants to lock or write the same rows waits until yours commits or rolls back. Here a seat is locked while it is assigned, so two requests racing for the same seat are handled one after the other:

```go
err := db.Transaction(c, func(ctx context.Context) error {
	seat, err := models.Seats(ctx).WhereKey(seatID).LockForUpdate().First()
	if err != nil {
		return err
	}

	if seat == nil || seat.PassengerID != nil {
		return web.Abort(http.StatusConflict, "That seat is no longer available.")
	}

	return seat.Update(ctx, db.Values{
		models.SeatColumns.PassengerID: passengerID,
		models.SeatColumns.AssignedAt:  clock.Now(ctx),
	})
})
```

A lock lasts as long as its transaction, so `LockForUpdate` belongs inside [`db.Transaction`](database.md#database-transactions): outside one, the `SELECT` is its own transaction and the lock is gone when it returns. The reads that return rows (`Get`, `First`, `FirstOrFail`, `Find`, `FindOrFail`, `ResolveRouteBinding`, `GetAs`, `Pluck`, `PluckBy` and `Value`) take the lock; `Count`, `Exists`, `DoesntExist`, `Update` and `Delete` ignore it. reef has no shared lock.

## Debugging

`ToSQL` compiles the query's `SELECT` and returns it with its bindings, without running anything. It is handy in tests and while debugging a scope:

```go
sql, args := models.Airports(ctx).Where("country_id", 7).Active().OrderBy("name").ToSQL()

// select * from "airports" where "country_id" = $1 and "airports"."active" = $2 order by "name" asc
// [7 true]
```

## Available Methods

Every query type, a model's `models.XQuery` and `*db.Rows[T]` alike, embeds `db.Builder[T, Q]`, so all of these are available on it. Chain methods return the query; the others run it.

#### `AddSelect`

Appends columns to the select list: `q.Select("id").AddSelect("name")`.

#### `Count`

Returns the number of matching rows as an `int64`, ignoring ordering, limit and offset: `n, err := q.Count()`.

#### `Create`

Inserts one row and returns it as stored: `user, err := models.Users(ctx).Create(db.Values{"name": name, "email": email})`.

#### `Delete`

Deletes the matching rows (at most `Limit` of them, when set) and returns how many: `n, err := q.Where("email", email).Delete()`.

#### `DoesntExist`

Reports whether no row matches: `none, err := q.WhereNull("city_id").DoesntExist()`.

#### `Eager`

Declares a load run on the rows the query's reads return; the generated `With<Relation>` methods call it, and you rarely do. See [eager loading](relationships.md#eager-loading).

#### `Exists`

Reports whether any row matches: `ok, err := q.Where("code", code).Exists()`.

#### `Find`

Returns a pointer to the row whose `id` is the given value, or `nil`: `airport, err := models.Airports(ctx).Find(id)`.

#### `FindOrFail`

Returns the row whose `id` is the given value, or a `*db.ModelNotFoundError`: `airport, err := models.Airports(ctx).FindOrFail(id)`.

#### `First`

Returns a pointer to the first row, or `nil`: `row, err := q.OrderBy("name").First()`.

#### `FirstOrFail`

Returns the first row, or a `*db.ModelNotFoundError`: `airport, err := q.Where("code", code).FirstOrFail()`.

#### `Get`

Runs the query and returns every row, with its [eager loads](relationships.md#eager-loading) run: `rows, err := q.Get()`. `First`, `FirstOrFail`, `Find` and `FindOrFail` run them too.

#### `GetAs`

Runs the query and scans its rows into another type, for a partial select or a join; every field the type declares itself needs a column, and it panics on a query that eager-loads: `rows, err := q.Join(...).Select(...).GetAs[airportListing]()`. See [scanning into another shape](#scanning-into-another-shape).

#### `Insert`

Inserts one or more rows, each with the same columns, and reads nothing back: `err := models.Favourites(ctx).Insert(values)`.

#### `InsertOrIgnore`

Inserts the rows, skipping any a unique index already holds, and returns how many it inserted: `inserted, err := q.InsertOrIgnore(values)`.

#### `Join`

Adds an `INNER JOIN`: `q.Join("countries", "countries.id", "=", "airports.country_id")`.

#### `Latest`

Sorts newest first, by the table's `created_at` or the one column you name: `q.Latest()`, `q.Latest("id")`.

#### `LeftJoin`

Adds a `LEFT JOIN`: `q.LeftJoin("flights", "flights.id", "=", "flight_status_updates.flight_id")`.

#### `Limit`

Caps the row count (zero means no limit): `q.Limit(10)`.

#### `Load`

Runs the declared eager loads on rows read elsewhere, filling their `Loaded` relations in place: `err := models.Airports(ctx).WithCountry().Load(airports)`. See [lazy eager loading](relationships.md#lazy-eager-loading).

#### `LockForUpdate`

Locks the rows the query reads until the transaction ends (`SELECT ... FOR UPDATE`): `seat, err := q.Where("flight_id", flightID).LockForUpdate().First()`. See [pessimistic locking](#pessimistic-locking).

#### `Now`

Returns the time on the clock the query's context carries, for [scopes](models.md#scopes-and-the-clock) that compare against it: `q.WhereOp(FareQuoteTableColumns.QuotedOn, "<", q.Now().AddDate(0, 0, -14))`.

#### `Offset`

Skips rows: `q.Offset(20)`.

#### `OrderBy`

Sorts ascending by a column: `q.OrderBy("name")`.

#### `OrderByDesc`

Sorts descending by a column: `q.OrderByDesc("created_at")`.

#### `OrderByRaw`

Appends a raw `ORDER BY` fragment with `?` bindings: `q.OrderByRaw("airports.avg_weekly_flights desc nulls last")`.

#### `Pluck`

Selects one column, named, and returns its value from every row, leaving the query's own select as it was: `names, err := q.Pluck[string]("name")`.

#### `PluckBy`

Selects two columns, named, and returns a map from the first to the second: `ids, err := q.PluckBy[string, int64]("code", "id")`.

#### `ResolveRouteBinding`

Returns the row a route parameter names by the model's route key (`id` unless it declares a `routeKey`), or a `*db.ModelNotFoundError`, a malformed value included: `booking, err := models.Bookings(c).ResolveRouteBinding(c.Param("booking"))`. It panics on a `db.Table` query, which has no schema to name the key. See [route model binding](routing.md#route-model-binding).

#### `Select`

Replaces the column list: `q.Select("id", "countries.code AS country_code")`.

#### `SelectRaw`

Appends a raw select expression with `?` bindings: `q.SelectRaw("COALESCE(airports.timezone, 'UTC') AS timezone")`.

#### `ToSQL`

Returns the compiled `SELECT` and its bindings without running it: `sql, args := q.ToSQL()`.

#### `Update`

Sets values on every matching row and returns how many changed: `n, err := country.Airports(ctx).Update(db.Values{"active": false})`. A model in hand updates its own row with [its `Update`](models.md#updates).

#### `UpdateChanged`

Like `Update`, but only for rows where some value differs from what is stored, and returning those rows as stored: `rows, err := q.WhereKey(id).WhereNull("paid_at").UpdateChanged(values)`.

#### `UpdateOrCreate`

Updates the row matching the attributes, or creates it, in one statement, and returns it as stored: `quote, err := models.FareQuotes(ctx).UpdateOrCreate(db.Values{"flight_id": id, "quoted_on": date}, values)`.

#### `Upsert`

Inserts positional rows, updating the named columns of any row that conflicts over the identifying ones: `err := models.Airports(ctx).Upsert(columns, rows, []string{"code"}, update)`.

#### `Value`

Returns a pointer to one column of the first row, or `nil` when no row matches: `id, err := q.Where("name", name).Value[int64]("id")`.

#### `Where`

Adds `column = value`: `q.Where("country_id", id)`.

#### `WhereAny`

Keeps rows matching any of the scopes, each scope's conditions `AND`ed: `q.WhereAny(func(q *BookingQuery) { q.Completed() }, func(q *BookingQuery) { q.AwaitingPayment() })`. See [logical grouping](#logical-grouping).

#### `WhereBetween`

Adds `column BETWEEN low AND high`, both ends included: `q.WhereBetween("latitude", south, north)`.

#### `WhereIn`

Adds `column = ANY(values)` for a slice of any element type; an empty slice matches nothing: `q.WhereIn("id", ids)`.

#### `WhereKey`

Filters on the table-qualified `id` column: `q.WhereKey(id)`.

#### `WhereNotNull`

Adds `column IS NOT NULL`: `q.WhereNotNull("published_at")`.

#### `WhereNowOrFuture`

Keeps rows whose column is now or later, on the query's clock: `q.WhereNowOrFuture("bookings.hold_expires_at")`.

#### `WhereNull`

Adds `column IS NULL`: `q.WhereNull("paid_at")`.

#### `WhereOp`

Adds `column <operator> value`, the operator a comparison or `[not] [i]like`: `q.WhereOp("avg_weekly_flights", ">", 1000)`.

#### `WherePast`

Keeps rows whose column is before now, on the query's clock: `q.WherePast("bookings.hold_expires_at")`.

#### `WhereRaw`

Adds a parenthesized raw condition with `?` bindings: `q.WhereRaw("lower(name) like ?", "%"+term+"%")`.

#### `WhereTodayOrAfter`

Keeps rows whose column falls on today or a later day, on the query's clock: `q.WhereTodayOrAfter("departs_at")`.

#### `WithoutTimestamps`

Stops this query's writes stamping the model's timestamps: `q.WhereKey(id).WithoutTimestamps().Update(values)`. See [timestamps](casts.md#timestamps).

### Package Functions and Types

#### `db.Table`

`Table[T any](ctx context.Context, table string) *db.Rows[T]` starts a query over any table, scanning into `T`. Its writes apply no casts and no timestamps.

#### `db.Rows`

`Rows[T]` is the query type `db.Table` returns: a struct embedding `db.Builder[T, *db.Rows[T]]` with no scopes. It is also the query type for code generic over models (see [generic code over models](models.md#generic-code-over-models)).

#### `db.NewQuery`

`NewQuery[T db.Model](ctx, q)` wires a query type (a struct embedding `db.Builder`) to a fresh builder over model `T`'s table and schema, and returns it; every generated constructor calls it (see [generic code over models](models.md#generic-code-over-models)).

#### `db.BelongsTo`

`BelongsTo(query, scopes, relation)` is the eager load a generated `With<Relation>` declares for a belongs-to: `relation` names a row's foreign key and its `Loaded` slot, and the parents are read in one query over the distinct keys.

#### `db.Values`

`Values` is a `map[string]any` of column => value for `Create`, `Insert`, `InsertOrIgnore`, `UpdateOrCreate`, `Update` and `UpdateChanged`.

#### `db.Builder`

`Builder[T, Q]` is the builder itself, which every query type embeds; you only name it when [declaring a query type](models.md#generic-code-over-models).
