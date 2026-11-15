# Helpers

- [Introduction](#introduction)
- [Available Methods](#available-methods)
- [Strings](#strings)
- [Numbers](#numbers)
- [URLs](#urls)
- [UUIDs and Random Strings](#uuids-and-random-strings)
- [Pointers](#pointers)
- [Collections](#collections)
- [Other Utilities](#other-utilities)
    - [Dates and Time](#dates-and-time)
    - [Sleep](#sleep)
    - [Ordered JSON Objects](#ordered-json-objects)

## Introduction

reef includes a variety of small helper functions in the `support` package. Many of them are used by the framework itself; however, you are free to use them in your own application if you find them convenient. reef also ships three small packages you will reach for in most applications: `collect`, which maps and groups slices; `clock`, the application's clock and its timestamp formats; and `ordered`, a JSON object that remembers its key order.

```go
import "github.com/atobeach/reef/support"

slug := support.Slug("Palma de Mallorca") // "palma-de-mallorca"
```

Some of these helpers reproduce an exact output on purpose: `support.ASCII` and `support.Slug` reproduce Laravel's `Str::ascii` and `Str::slug` byte for byte, so they match the slugs and ASCII name columns a Laravel application stored, and `support.StripTags` reproduces PHP's `strip_tags`. Their behavior is fixed; [compatibility](compatibility.md#slugs-and-ascii-folding) has the details. JSON in the PHP-compatible dialect lives in `phpjson`, also documented on the [compatibility](compatibility.md#the-php-json-dialect) page.

## Available Methods

**Strings**

- [support.ASCII](#supportascii)
- [support.BeforeLast](#supportbeforelast)
- [support.EscapeLike](#supportescapelike)
- [support.Falsy](#supportfalsy)
- [support.FoldLower](#supportfoldlower)
- [support.MbTrim](#supportmbtrim)
- [support.Plural](#supportplural)
- [support.Slug](#supportslug)
- [support.Squish](#supportsquish)
- [support.StrLimit](#supportstrlimit)
- [support.StripTags](#supportstriptags)
- [support.Title](#supporttitle)
- [support.Transliterate](#supporttransliterate)
- [support.UcFirst](#supportucfirst)

**Numbers**

- [support.FloatToInt](#supportfloattoint)
- [support.IsNumeric](#supportisnumeric)
- [support.NumericToFloat](#supportnumerictofloat)
- [support.NumericToInt](#supportnumerictoint)
- [support.Round](#supportround)
- [support.Thousands](#supportthousands)

**URLs**

- [support.FormEncode](#supportformencode)
- [support.RawURLEncode](#supportrawurlencode)

**UUIDs and Random Strings**

- [support.IsUUID](#supportisuuid)
- [support.RandomString](#supportrandomstring)
- [support.UUID4](#supportuuid4)
- [support.UUID7](#supportuuid7)

**Pointers**

- [support.Deref](#supportderef)
- [support.NonEmpty](#supportnonempty)

**Collections**

- [collect.GroupBy](#collectgroupby)
- [collect.Map](#collectmap)

**Other Utilities**

- [clock.Now](#dates-and-time)
- [clock.With and clock.Clock](#binding-a-clock)
- [clock.Time and clock.TimeOf](#timestamps-in-json)
- [clock.Atom and clock.AtomOf](#dates-in-data-objects)
- [clock.ISOString, clock.AtomString and clock.DateTimeString](#formatting-timestamps)
- [clock.Sleep](#sleep)
- [ordered.Object and ordered.Decode](#ordered-json-objects)

## Strings

#### `support.ASCII`

The `support.ASCII` function transliterates a string to ASCII, character by character, and drops any character it has no ASCII spelling for. Tabs, carriage returns and newlines become spaces:

```go
support.ASCII("Brașov") // "Brasov"

support.ASCII("Zürich Łódź") // "Zurich Lodz"

support.ASCII("東京") // ""
```

> [!WARNING]
> The transliteration table is fixed: slugs and ASCII name columns already stored were built with it, and lookups compare against them. Characters outside Unicode's Basic Multilingual Plane (emoji, for example) are always dropped.

#### `support.BeforeLast`

The `support.BeforeLast` function returns everything before the last occurrence of the given value, or the whole string when the value does not occur:

```go
support.BeforeLast("search.flight.selected", ".") // "search.flight"
support.BeforeLast("search", ".")                 // "search"
```

#### `support.EscapeLike`

The `support.EscapeLike` function escapes the wildcards of a SQL `LIKE` pattern (`%` and `_`) and its backslash escape character, so a value typed by a user matches literally:

```go
support.EscapeLike(`50%_off`) // `50\%\_off`
```

Escape the user's text, then add your own wildcards. An airport typeahead matches this way, so a typed `%` can't turn a keystroke into a match-everything scan:

```go
escaped := support.EscapeLike(support.FoldLower(query))

airportIDs, err := models.Airports(ctx).WhereRaw("lower(name) like ?", "%"+escaped+"%").Pluck[int64](models.AirportColumns.ID)
```

#### `support.Falsy`

The `support.Falsy` function reports whether a string is loosely false: the empty string and `"0"`. Any other string, `"false"` included, is not:

```go
support.Falsy("")      // true
support.Falsy("0")     // true
support.Falsy("false") // false
```

#### `support.FoldLower`

The `support.FoldLower` function returns a case- and accent-insensitive key for matching: the [ASCII](#supportascii) transliteration, lowercased:

```go
support.FoldLower("São Paulo") // "sao paulo"
```

#### `support.MbTrim`

The `support.MbTrim` function trims whitespace from both ends of a string: everything `strings.TrimSpace` trims, plus NUL bytes and U+180E (the Mongolian vowel separator). Use it on text from upstream APIs, which often carries stray non-breaking spaces or NULs:

```go
support.MbTrim("\u00a0 Gatwick North \x00") // "Gatwick North"
```

#### `support.Plural`

The `support.Plural` function returns the English plural of a word. It checks the uninflected words first, then the irregular ones, then the regular rules, and matches the result to the case of the input (lowercase, UPPERCASE, first letter capitalized, or every word capitalized):

```go
support.Plural("city")       // "cities"
support.Plural("child")      // "children"
support.Plural("Person")     // "People"
support.Plural("SHEEP")      // "SHEEP"
support.Plural("Boarding Pass") // "Boarding Passes"
```

#### `support.Slug`

The `support.Slug` function generates a URL-friendly slug from a title: it is transliterated to [ASCII](#supportascii), underscores become dashes, `@` is spelled `at`, everything but letters, numbers, dashes and whitespace is dropped, and the result is lowercased with runs of dashes and whitespace collapsed to one dash:

```go
support.Slug("Rio de Janeiro")       // "rio-de-janeiro"
support.Slug("Red_Eye @ Dawn!")      // "red-eye-at-dawn"
support.Slug("Kraków Old Town")      // "krakow-old-town"
```

A title with no ASCII transliteration slugs to an empty string, so check for one before you store it:

```go
support.Slug("東京") // ""
```

#### `support.Squish`

The `support.Squish` function collapses every run of whitespace to a single space and trims both ends. It also treats the Hangul fillers as whitespace and trims zero-width spaces, left-to-right marks and byte-order marks from the ends:

```go
support.Squish("  Terminal   5 \n Heathrow  ") // "Terminal 5 Heathrow"
```

#### `support.StrLimit`

The `support.StrLimit` function truncates a string to the given number of display columns and appends `...`. East Asian wide and fullwidth characters count as two columns, and whitespace left at the cut is trimmed. Words are not preserved:

```go
support.StrLimit("The quick brown fox", 9) // "The quick..."
support.StrLimit("Heathrow Terminal 5", 8) // "Heathrow..."
support.StrLimit("short", 9)               // "short"

support.StrLimit("東京タワー", 4) // "東京..."
```

#### `support.StripTags`

The `support.StripTags` function removes every HTML tag from a string and keeps only its text. Comments, `<!DOCTYPE ...>` and `<?...?>` blocks are removed too, quotes inside a tag are respected, and a `<` followed by whitespace is kept as text. HTML entities are left as they are:

```go
support.StripTags("<p>Sun <b>&amp;</b> sea<!-- note --></p> a < b") // "Sun &amp; sea a < b"
```

#### `support.Title`

The `support.Title` function converts a string to title case the Unicode way: a letter that follows another letter (ignoring apostrophes and other case-ignorable characters in between) is lowercased, and every other letter starts a word and is capitalized:

```go
support.Title("o'hare international AIRPORT") // "O'hare International Airport"
support.Title("são paulo")               // "São Paulo"
```

#### `support.Transliterate`

The `support.Transliterate` function folds a string to ASCII more coarsely than [ASCII](#supportascii): ASCII passes through, accented Latin letters lose their marks, and every other character becomes `?`. A login rate limiter can key its attempts with it:

```go
support.Transliterate("José 東") // "Jose ?"
```

#### `support.UcFirst`

The `support.UcFirst` function uppercases the first character of a string:

```go
support.UcFirst("élan") // "Élan"
```

## Numbers

#### `support.FloatToInt`

The `support.FloatToInt` function truncates a float toward zero. `NaN` and the infinities are `0`, where a plain Go conversion would give an undefined result:

```go
support.FloatToInt(-3.9)       // -3
support.FloatToInt(math.NaN()) // 0
```

#### `support.IsNumeric`

The `support.IsNumeric` function reports whether a string is a decimal number: optional surrounding ASCII whitespace, an optional sign, digits with an optional fraction, and an optional exponent. Unlike `strconv.ParseFloat`, it rejects hexadecimal, underscores, `Inf` and `NaN`:

```go
support.IsNumeric(" 1.5e3 ") // true
support.IsNumeric(".5")      // true
support.IsNumeric("0x1A")    // false
support.IsNumeric("1_000")   // false
support.IsNumeric("NaN")     // false
```

#### `support.NumericToFloat`

The `support.NumericToFloat` function returns the value of a [numeric](#supportisnumeric) string, or `0` for anything else:

```go
support.NumericToFloat(" 12.50 ") // 12.5
support.NumericToFloat("12abc")   // 0
```

#### `support.NumericToInt`

The `support.NumericToInt` function returns the integer value of a numeric string. An integer string converts directly; one with a fraction or an exponent is truncated toward zero; anything else is `0`:

```go
support.NumericToInt("42")   // 42
support.NumericToInt("-7.9") // -7
support.NumericToInt("1e3")  // 1000
support.NumericToInt("abc")  // 0
```

#### `support.Round`

The `support.Round` function rounds half away from zero to the given number of decimal places. A negative number of places rounds to tens, hundreds and so on. Rounding is judged on the decimal the float spells, not its binary approximation, so `1.005` rounds up as you would expect:

```go
support.Round(1.005, 2)   // 1.01, where math.Round(x*100)/100 gives 1 for x := 1.005
support.Round(-2.5, 0)    // -3
support.Round(1234.5, -2) // 1200
```

Use it for any amount shown to a user or compared with stored data; a currency converter should round every converted fare with it.

#### `support.Thousands`

The `support.Thousands` function formats an integer with comma-separated thousands, for counts in command output and prompts:

```go
support.Thousands(-74123456) // "-74,123,456"
support.Thousands(999)       // "999"
```

## URLs

#### `support.FormEncode`

The `support.FormEncode` function escapes a value for a query string, form style: everything except `A-Z`, `a-z`, `0-9`, `_`, `.` and `-` is percent-encoded, and spaces become `+`:

```go
support.FormEncode("Palma de Mallorca ~/&") // "Palma+de+Mallorca+%7E%2F%26"
```

#### `support.RawURLEncode`

The `support.RawURLEncode` function percent-encodes a value per RFC 3986: only `A-Z`, `a-z`, `0-9`, `-`, `_`, `.` and `~` are left as they are, and spaces become `%20`:

```go
support.RawURLEncode("Palma de Mallorca ~/&") // "Palma%20de%20Mallorca%20~%2F%26"
```

## UUIDs and Random Strings

#### `support.IsUUID`

The `support.IsUUID` function reports whether a string is shaped like a UUID (`8-4-4-4-12` hexadecimal digits, any version, either case). Postgres fails a query that compares a `uuid` column with malformed text, so check a route parameter before you query with it:

```go
if !support.IsUUID(c.Param("uuid")) {
	return nil, web.NotFound()
}
```

#### `support.RandomString`

The `support.RandomString` function returns a string of the given length made of `A-Z`, `a-z` and `0-9`, drawn from a cryptographically secure source. reef uses it for session ids, CSRF tokens and remember tokens:

```go
token := support.RandomString(40)
```

#### `support.UUID4`

The `support.UUID4` function returns a random version 4 UUID, for identifiers whose order means nothing:

```go
support.UUID4() // "9530c469-50d5-48ef-b92a-b5fd5d5a62eb"
```

#### `support.UUID7`

The `support.UUID7` function returns a time-ordered version 7 UUID (RFC 9562) in lowercase hex, for an id you mint yourself: reef's conversation store mints its conversation and message ids with it, and an application can mint its booking or search ids the same way. UUIDs made in order sort in order, even within the same millisecond, so rows keyed by them can be read back in the order they were created:

```go
support.UUID7() // "01a162ea-10a4-7298-8a8b-e926db207289"
```

> [!NOTE]
> `UUID7` reads the real wall clock, never the context's [clock](#dates-and-time): the ordering is one sequence for the whole process, so a test that freezes its clock still gets ids that sort after the ones before them.

## Pointers

#### `support.Deref`

The `support.Deref` function returns the value a pointer points to, or the zero value for `nil`:

```go
terminal := support.Deref(flight.Terminal) // "" when the terminal is nil
```

For a pointer to a value, use Go's built-in `new`: `f.Terminal = new("5")`.

#### `support.NonEmpty`

The `support.NonEmpty` function is `Deref`'s inverse for an optional string: a pointer to the string, or `nil` when it is empty:

```go
flight.Terminal = support.NonEmpty(row.Terminal) // nil when the terminal is ""
```

## Collections

reef has no `Collection` type: a Go slice is the list, and the standard library's `slices`, `maps` and `iter` packages already sort, filter and search one. The `collect` package adds the two collection methods Go leaves out, `map` and `groupBy`, so you never write the make-and-index loop by hand:

```go
import "github.com/atobeach/reef/collect"
```

#### `collect.GroupBy`

The `collect.GroupBy` function groups a slice's items by the key the given function returns. It returns an iterator (an `iter.Seq2[K, []T]`) that you range over, yielding each key with its items. Keys come in the order they first appear and each group keeps its items in their original order, so a list you sorted stays in that order within each group, and the groups come in the order of their first items:

```go
fruit := []string{"pear", "apple", "plum", "avocado", "banana"}

for letter, group := range collect.GroupBy(fruit, func(f string) string { return f[:1] }) {
	fmt.Println(letter, group)
}

// p [pear plum]
// a [apple avocado]
// b [banana]
```

A departures board reads its flights in departure order, then groups them into one section per day:

```go
days := []DepartureDay{}
for date, group := range collect.GroupBy(flights, departureDay) {
	days = append(days, DepartureDay{
		Date:        date,
		FirstDepart: clock.AtomString(group[0].DepartsAt),
		FlightCount: len(group),
		Airlines:    groupByAirline(group),
	})
}
```

Breaking out of the loop stops the iteration, and an empty or `nil` slice yields nothing.

#### `collect.Map`

The `collect.Map` function returns a new slice holding the result of calling the given function on every item, in order:

```go
collect.Map([]int{3, 1, 2}, strconv.Itoa) // []string{"3", "1", "2"}
```

The result is never `nil`, even for an empty or `nil` slice, so a mapped list a controller returns always encodes as `[]`, never `null`. A saved-airports controller maps the eager-loaded pivot rows to the JSON it serves this way:

```go
return collect.Map(saved, func(row models.SavedAirport) SavedAirportData {
	airport := row.Loaded.Airport

	return SavedAirportData{
		ID: airport.ID, Code: airport.Code, Name: airport.Name, City: airport.City, Country: airport.Country,
		Pivot: savedPivot{UserID: row.UserID, AirportID: row.AirportID, CreatedAt: clock.TimeOf(row.CreatedAt), UpdatedAt: clock.TimeOf(row.UpdatedAt)},
	}
}), nil
```

A function that already has the right shape is passed as it is: `collect.Map(rows, data.AirportFromModel)`.

## Other Utilities

### Dates and Time

The `clock` package is the application's clock. Read the current time from the clock on the context, never from `time.Now`:

```go
import "github.com/atobeach/reef/clock"

expiresAt := clock.Now(ctx).Add(time.Hour)
```

`clock.Now(ctx)` always returns UTC, the application's time zone. The [kernels](lifecycle.md#the-clock) bind the container's clock onto every request, command and job, and a context without a clock reads the wall clock, so `clock.Now` is always safe to call.

#### Binding a Clock

A `*clock.Clock` is the wall clock until something freezes it. Its zero value is ready to use, and a `nil` clock is the wall clock too, which is what production binds. `clock.With` binds a clock onto a context:

```go
c := &clock.Clock{}
ctx = clock.With(ctx, c)
```

#### Moving the Clock

A test moves its own clock, and only the contexts that carry that clock see the change, so tests traveling in time can run in parallel:

```go
c.TravelTo(time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)) // frozen at 09:00
c.Travel(90 * time.Minute)                               // frozen at 10:30

c.Now() // 2026-07-27 10:30:00 +0000 UTC
```

`TravelTo` freezes the clock at the given instant (converted to UTC). `Travel` moves it by a duration, freezing it at the current time first if it was running. The clock's own `Now` method returns its current time in UTC; `clock.Now(ctx)` calls it on the context's clock. These methods are safe for concurrent use. In tests, prefer `clocktest.FrozenAt` or the test case's `tc.FreezeTime()` and `tc.Travel(d)`; see [mocking](mocking.md).

#### Timestamps in JSON

The `clock.Time` type is a timestamp that marshals as the API's timestamp format, UTC with microseconds and a `Z`, and scans from Postgres. Use it in the structs you serve and in the row types you scan a partial select into; generated models keep their columns as plain `time.Time`:

```go
// savedPivot is the airport_user row behind a saved airport.
type savedPivot struct {
	UserID    int64       `json:"user_id"`
	AirportID int64       `json:"airport_id"`
	CreatedAt *clock.Time `json:"created_at"`
	UpdatedAt *clock.Time `json:"updated_at"`
}
```

```json
{"user_id": 7, "airport_id": 12, "created_at": "2026-10-04T16:30:55.000000Z", "updated_at": "2026-10-04T16:30:55.000000Z"}
```

A nullable column is a `*time.Time` on its model. The `clock.TimeOf` function wraps one for a JSON field, and a `nil` stays `nil`, so the field encodes as `null`:

```go
savedPivot{
	UserID:    row.UserID,
	AirportID: row.AirportID,
	CreatedAt: clock.TimeOf(row.CreatedAt),
	UpdatedAt: clock.TimeOf(row.UpdatedAt),
}
```

`clock.Time` embeds `time.Time`, so every `time.Time` method works on it. Its `MarshalJSON` converts to UTC first; its `UnmarshalJSON` accepts the same format, and any RFC 3339 timestamp, so cached data round-trips; a JSON `null` leaves it unchanged. `Scan` (for pgx) accepts only a `time.Time`, so scan a nullable column into a `*clock.Time`, as an airport listing does for the rows it reads with [`GetAs`](queries.md):

```go
type AirportListing struct {
	Code       string      `db:"code" json:"code"`
	Name       string      `db:"name" json:"name"`
	ReviewedAt *clock.Time `db:"reviewed_at" json:"reviewed_at"`
	// ...
}
```

`Value` hands the `time.Time` back to the driver.

#### Dates in Data Objects

The `clock.Atom` type is a date held by a Data object rather than a model. It marshals as RFC 3339 to the second, with the time's own offset, and `clock.AtomOf` wraps a `*time.Time` for it the way `TimeOf` does for `clock.Time`:

```go
type FlightSearchInputData struct {
	Origin        string      `json:"origin"`
	Destination   string      `json:"destination"`
	DepartureDate clock.Atom  `json:"departureDate"`
	ReturnDate    *clock.Atom `json:"returnDate"`
	// ...
}

FlightSearchInputData{
	Origin:        p.Origin,
	Destination:   p.Destination,
	DepartureDate: clock.Atom{Time: p.DepartureDate},
	ReturnDate:    clock.AtomOf(p.ReturnDate), // nil for a one-way search
}
```

```json
{"origin": "LGW", "destination": "PMI", "departureDate": "2026-07-27T00:00:00+00:00", "returnDate": null}
```

`clock.Atom`'s `MarshalJSON` keeps the offset of the `time.Time` it holds, where `clock.Time` converts to UTC. A `timestamp(0)` column scans as UTC, so a date read from the database renders `+00:00`; build one from a UTC time when you make it yourself. Its `UnmarshalJSON` accepts RFC 3339, and `null` leaves it unchanged.

#### Formatting Timestamps

Three functions render a `time.Time` as a string:

```go
t := time.Date(2026, 10, 4, 15, 30, 55, 123456789, time.UTC)

clock.ISOString(t)      // "2026-10-04T15:30:55.123456Z"
clock.AtomString(t)     // "2026-10-04T15:30:55+00:00"
clock.DateTimeString(t) // "2026-10-04 15:30:55"
```

`clock.ISOString` is the API's timestamp format, the one `clock.Time` marshals to, and converts to UTC first. `clock.AtomString` is the date format `clock.Atom` marshals to, for a field that is a plain string; like `Atom`, it keeps the time's own offset.

`clock.DateTimeString` is a `timestamp(0)` column's own text, `Y-m-d H:i:s` in UTC with the fraction dropped. Use it where a value must read exactly as the column stores it: a `GET /user` endpoint might render `two_factor_confirmed_at` this way, the shape its frontend reads, while the user's other timestamps are `clock.Time`. It is also the value to bind in a statement you run yourself with `db.Exec`: Postgres rounds a bound time's fraction on a `timestamp(0)` column, where this string truncates it.

> [!NOTE]
> Queries you build never need `DateTimeString`. The query builder binds every `time.Time` in UTC, truncated to the second, and a scope compares a column with the context's clock through `q.WherePast(column)`, `q.WhereNowOrFuture(column)` and `q.WhereTodayOrAfter(column)`, or reads the time with `q.Now()` (see [the query builder](queries.md#additional-where-clauses)):
>
> ```go
> // Expired keeps fare holds that ran out before the booking was paid.
> func (q *FareHoldQuery) Expired() *FareHoldQuery {
> 	return q.WherePast(FareHoldTableColumns.ExpiresAt).WhereNull(FareHoldTableColumns.PaidAt)
> }
> ```

### Sleep

`clock.Sleep` pauses for a duration, or until the context ends, whichever comes first:

```go
// The airline API throttles searches per account, not just per IP, so
// requests stay paced even across several workers.
clock.Sleep(ctx, 100*time.Millisecond)
```

Sleeping through the clock lets a test fake the pause. On a clock whose sleep is faked, `clock.Sleep` records the duration and returns at once, so a test of retry back-off or rate-limit pacing runs instantly and can assert on the pauses:

```go
c := &clock.Clock{}
c.FakeSleep()
ctx = clock.With(ctx, c)

clock.Sleep(ctx, 2*time.Second)

c.Slept() // []time.Duration{2 * time.Second}
```

`FakeSleep` turns faking on; `Slept` returns the recorded pauses in order. A zero or negative duration returns at once on the wall clock. In tests, `clocktest.FakeSleep` and `clocktest.AssertSlept` do this for you; see [mocking](mocking.md).

### Ordered JSON Objects

A Go map forgets the order of its keys. That matters whenever JSON is re-encoded and compared byte for byte later (an AI model's tool arguments sent back with the conversation history, say) or read in the order the sender wrote it. The `ordered` package's `*ordered.Object` is a JSON object that keeps its keys in the order they were set or decoded:

```go
import "github.com/atobeach/reef/ordered"

args := ordered.NewObject()
args.Set("from", "PMI")
args.Set("to", "MAD")
args.Set("from", "BCN") // replaced in place: keeps its position

encoded, err := json.Marshal(args) // {"from":"BCN","to":"MAD"}
```

The zero value of `ordered.Object` is an empty object ready to use, and a `nil` `*ordered.Object` reads as empty (but you can't `Set` on it).

#### Decoding

`ordered.Decode` reads one JSON value of any kind. Objects become `*ordered.Object`, arrays `[]any`, and numbers `json.Number`, so an integer stays distinguishable from a float; strings, booleans and `null` decode to `string`, `bool` and `nil`:

```go
value, err := ordered.Decode([]byte(`{"z": 1, "a": [1.5, "x", null, true]}`))

object := value.(*ordered.Object)
object.Keys() // []string{"z", "a"}
```

If a key appears twice in an object, it keeps the position of its first occurrence and the value of its last. Anything after the value (`{} {}`) is an error.

An `*ordered.Object` is also an `encoding/json` `Unmarshaler`, so it can be a field of a struct you decode the usual way. It decodes as `Decode` does, and fails when the JSON is not an object. reef's `ai` package keeps a model's tool-call arguments in one:

```go
// ToolCall is one function call a model asked for. Arguments keep the
// model's key order (empty for `{}`), so they re-encode to the bytes stored
// rows and resent history already hold.
type ToolCall struct {
	ID        string
	Name      string
	Arguments *ordered.Object
	ResultID  *string
}
```

#### Available Methods

| Method | Description |
| --- | --- |
| `ordered.NewObject() *Object` | An empty object. |
| `o.Set(key string, value any)` | Adds a key at the end, or replaces an existing key's value in place. |
| `o.Get(key string) (any, bool)` | The value at a key, and whether the key is present. |
| `o.Has(key string) bool` | Whether the key is present, even with a `nil` value. |
| `o.Keys() []string` | The keys, in order. |
| `o.Values() []any` | The values, in key order. |
| `o.Len() int` | The number of keys. |
| `o.Except(drop ...string) *Object` | A shallow copy without the given keys. |
| `o.MarshalJSON() ([]byte, error)` | Encodes the object with `encoding/json`, in key order. `encoding/json` writes a `nil` `*Object` field as `null`. |
| `o.UnmarshalJSON(raw []byte) error` | Decodes a JSON object as `Decode` does. |

An encoder with other rules reads the object through `Keys` and `Get`: [`phpjson.Encode`](compatibility.md#the-php-json-dialect) writes it in the PHP-compatible dialect.
