# Models: Relationships

- [Introduction](#introduction)
- [Defining Relationships](#defining-relationships)
    - [Belongs To](#belongs-to)
    - [Has Many](#has-many)
    - [Has One](#has-one)
    - [Many to Many](#many-to-many)
    - [Which Foreign Keys Become Relationships](#which-foreign-keys-become-relationships)
- [Querying Relations](#querying-relations)
    - [Optional Relations](#optional-relations)
    - [Querying Relationship Existence](#querying-relationship-existence)
- [Eager Loading](#eager-loading)
    - [Constraining Eager Loads](#constraining-eager-loads)
    - [Nested Eager Loading](#nested-eager-loading)
    - [Lazy Eager Loading](#lazy-eager-loading)
- [Inserting and Updating Related Models](#inserting-and-updating-related-models)

## Introduction

Database tables are often related to one another: an airport belongs to a country, a country has many airports, a user has many bookings. reef's relationships come from your schema's foreign keys. When the [model generator](models.md#generating-models) reads the migrated schema, it gives each model a method per relation, and each method returns the related model's query:

```go
country, err := airport.Country(ctx).FirstOrFail()

airports, err := country.Airports(ctx).Active().OrderBy(models.AirportColumns.Name).Get()
```

Because a relation is just a query, you constrain it, scope it, count it or update through it like any other. There are no dynamic properties and no lazy loading: a relation runs its query when you call `Get`, `First` or another terminal method, and never before. To load a belongs-to relation for many models at once, [eager load](#eager-loading) it.

## Defining Relationships

You don't write relationships: you declare a foreign key in a [migration](migrations.md), run `go generate ./app/models`, and both ends of the relationship appear.

### Belongs To

A foreign key on a model's own table, such as `airports.country_id` referencing `countries.id`, gives the model a belongs-to method. It is named after the column without its `_id` suffix, in Pascal case, and filters the related model's query on its primary key:

```go
// Country is the `Country` this row belongs to (via country_id).
func (a Airport) Country(ctx context.Context) *CountryQuery {
	return Countries(ctx).WhereKey(a.CountryID)
}
```

```go
country, err := airport.Country(ctx).FirstOrFail()
```

Each belongs-to also gives the model's query a `With` method that [eager loads](#eager-loading) the relation into the model's `Loaded` field: `models.Airports(ctx).WithCountry()`.

### Has Many

The other end of the same key gives the referenced model a has-many method, named after the related model's constructor. `countries.id`, referenced by `airports.country_id`, gives `Country` an `Airports` method:

```go
// Airports are the rows pointing here through country_id.
func (c Country) Airports(ctx context.Context) *AirportQuery {
	return Airports(ctx).Where(AirportTableColumns.CountryID, c.ID)
}
```

The key is named by the related model's [qualified identifier](models.md#column-identifiers), so the method can be chained into a join.

```go
active, err := country.Airports(ctx).Active().OrderBy(models.AirportColumns.Name).Get()
```

When one table points at another through more than one column, each has-many method adds `By` and the column's name, so they stay distinct. `flights` references `airports` through both `origin_id` and `destination_id`, which gives `Airport` two methods:

```go
departures, err := airport.FlightsByOrigin(ctx).Count()
arrivals, err := airport.FlightsByDestination(ctx).Count()
```

### Has One

reef doesn't tell a has-one from a has-many: every foreign key pointing at a model generates a has-many method, even when a unique index means there is at most one row. If `seats.passenger_id` is unique, a passenger has at most one seat, and its generated method is still `Seats`. Read the single row with `First`:

```go
seat, err := passenger.Seats(ctx).First()
```

### Many to Many

A pivot table such as `airport_user` relates two models through its two foreign keys. reef doesn't generate many-to-many methods; list the pivot in `database/models.json` instead, and it becomes a model of its own, with a belongs-to for each side and a has-many of pivot rows on each related model. Name the pivot after what it holds:

```json
"airport_user": { "name": "Favourite" },
```

The user's favourite airports are then the user's pivot rows with each one's airport eager-loaded, and the pivot's own columns (its timestamps, say) sit on the row:

```go
favourites, err := user.Favourites(c).WithAirport().Get()

for _, favourite := range favourites {
	fmt.Println(favourite.Loaded.Airport.Name, favourite.CreatedAt)
}
```

Attaching is an insert of a pivot row, and `InsertOrIgnore` makes it idempotent against the pivot's unique index, with its timestamps stamped; detaching is a delete through the relation:

```go
_, err := models.Favourites(c).InsertOrIgnore(db.Values{models.FavouriteColumns.UserID: user.ID, models.FavouriteColumns.AirportID: airport.ID})

_, err = user.Favourites(c).Where(models.FavouriteColumns.AirportID, airport.ID).Delete()
```

### Which Foreign Keys Become Relationships

A foreign key generates a relationship when:

- it is a single-column key (composite keys are skipped);
- it references an `id` column;
- both of its tables are listed in `database/models.json`.

A relation whose method name would collide with a field or another method (a column named `country` beside a `country_id` key, say) fails generation, naming the table and the relation; rename the column, or the model in the spec. `Loaded` is reserved for [eager-loaded relations](#eager-loading), so a column named `loaded` fails generation too, and the `With` prefix on a query type is the generator's: don't start a [scope](models.md#query-scopes)'s name with it.

## Querying Relations

Every relation method returns the related model's query, with that model's scopes, so you can keep chaining:

```go
total, err := country.Airports(ctx).Active().Count()

trips, err := user.Bookings(ctx).Completed().Latest().Get()
```

Relation methods take the context to run on, like a model's constructor. In a controller, pass the request: `airport.Country(c)`.

### Optional Relations

When the foreign key column is nullable, the model holds a pointer, and the belongs-to method returns a query that matches no row while the key is `nil` (rather than one that queries `id = NULL`):

```go
// City is the `City` this row belongs to (via city_id).
func (a Airport) City(ctx context.Context) *CityQuery {
	if a.CityID == nil {
		return Cities(ctx).WhereRaw("1 = 0")
	}

	return Cities(ctx).WhereKey(*a.CityID)
}
```

So `First` returns `nil` both for an unset key and for a missing row:

```go
city, err := airport.City(ctx).First()
if err != nil {
	return err
}

if city == nil {
	// The airport has no city.
}
```

### Querying Relationship Existence

To retrieve models that have a related row, constrain the query with an `EXISTS` subquery in [`WhereRaw`](queries.md#raw-expressions), or a join:

```go
served, err := models.Cities(ctx).
	WhereRaw("exists (select 1 from airports where airports.city_id = cities.id and airports.active)").
	Get()
```

## Eager Loading

Calling a relation method inside a loop runs one query per model, the "N + 1" problem. Eager loading reads a relation for every row a query returns in one more query. Each belongs-to relation gives the model's query a `With<Relation>` method, and the related rows land in the model's generated `Loaded` field:

```go
airports, err := models.Airports(ctx).Active().WithCountry().Get()
if err != nil {
	return err
}

for _, airport := range airports {
	fmt.Println(airport.Name, airport.Loaded.Country.Name)
}
```

That is two queries however many airports there are: the airports, then their countries in one `WhereIn` over the distinct `country_id` values. Declare as many relations as you need, and each adds one query:

```go
profiles, err := user.Profiles(c).WithCountry().WithNationalityCountry().Latest().Get()
```

`Get`, `First`, `FirstOrFail`, `Find` and `FindOrFail` all run the eager loads, so a single model reads its relations the same way:

```go
booking, err := models.Bookings(ctx).WithFlight().FindOrFail(id)
```

`Loaded` holds a pointer per belongs-to relation. It stays `nil` until a `With` method loads it, and also when the foreign key is `NULL` or names no row, so code reading it handles `nil` the way a nullable column does. Rows naming the same parent share one loaded model. The field is tagged `db:"-" json:"-"`: it is never a column, and never part of the model's JSON (a cached model comes back without its relations; see [lazy eager loading](#lazy-eager-loading)).

> [!WARNING]
> A forgotten `With` reads exactly like a missing relation: `Loaded.Country` is `nil` either way. A test of code that reads `Loaded` should assert on the related data, not only that the read succeeded.

`GetAs`, `Pluck`, `PluckBy` and `Value` don't return the model, so there is nothing to fill: they panic when the query declares an eager load.

### Constraining Eager Loads

A `With` method takes scopes for the related model's query. A scope may filter, order or select, and a select that leaves out the key gets it added, so the loaded rows can still be matched to theirs. An airport search might read only the country columns its labels show:

```go
airports, err := models.Airports(ctx).
	RankedBusiestFirst().
	WithCountry(func(q *models.CountryQuery) { q.Select(models.CountryColumns.Code, models.CountryColumns.Name) }).
	Limit(limit).
	Get()
```

### Nested Eager Loading

A scope is the related model's query, so it can declare eager loads of its own. This loads every booking's flight, and each flight's destination airport:

```go
bookings, err := models.Bookings(ctx).
	WithFlight(func(q *models.FlightQuery) { q.WithDestination() }).
	Get()
```

### Lazy Eager Loading

To load relations onto models you already hold, such as rows from a [cache](cache.md) or from a query that couldn't declare them, give them to `Load`. It runs the declared eager loads and fills each row's `Loaded` in place:

```go
// The popular airports come from the cache, without relations.
airports, err := ctl.PopularAirports.Handle(c)
if err != nil {
	return nil, err
}

// countryLabel and citySubtitle are scopes selecting the columns an airport label shows.
if err := models.Airports(c).WithCountry(countryLabel).WithCity(citySubtitle).Load(airports); err != nil {
	return nil, err
}
```

`Load` reads no rows of its own, so it panics on a query that selects, joins, filters, orders, limits, offsets or locks; narrow the relations through the `With` methods' scopes instead. An empty slice runs nothing. For one model, load a one-element slice:

```go
profiles := []models.Profile{profile}
if err := models.Profiles(c).WithCountry().WithNationalityCountry().Load(profiles); err != nil {
	return data.ProfileData{}, err
}

return data.ProfileDataFromModel(profiles[0]), nil
```

Only belongs-to relations eager load. For the other end, query the has-many relation, or a [join scanned with `GetAs`](queries.md#scanning-into-another-shape) when you need columns from both sides in one row.

## Inserting and Updating Related Models

To create a related model, set its foreign key column in the values you insert:

```go
search, err := models.SavedSearches(ctx).Create(db.Values{
	models.SavedSearchColumns.UUID:                 support.UUID7(),
	models.SavedSearchColumns.Payload:              string(payload),
	models.SavedSearchColumns.DestinationAirportID: airport.ID,
})
```

Since a relation is a query, you may also update or delete the related rows through it. This removes every saved passenger a user has:

```go
_, err := user.SavedPassengers(ctx).Delete()
```
