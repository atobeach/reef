# Database Testing

- [Introduction](#introduction)
    - [Resetting the Database After Each Test](#resetting-the-database-after-each-test)
- [Leasing a Test Database](#leasing-a-test-database)
    - [Migrating a Scratch Database](#migrating-a-scratch-database)
- [Committing in a Test](#committing-in-a-test)
- [Model Factories](#model-factories)
- [Running Seeders](#running-seeders)
- [Testing Against Valkey](#testing-against-valkey)
- [Available Assertions](#available-assertions)
    - [Counting Queries](#counting-queries)

## Introduction

reef provides a variety of tools and assertions to make it easier to test database-driven applications. Every test gets a migrated Postgres database of its own and a transaction that is rolled back when it ends, so tests run in parallel without ever seeing each other's rows. [Model factories](factories.md) make the rows a test needs in one line, and the database assertions check the rows a test leaves behind. We'll discuss each of these below.

The examples on this page use a test case of the kind you write yourself, whose `tests.Setup` wires all of this up (see [Building a Test Case](testing.md#building-a-test-case)). `tests.Setup` and the `tc` helpers aren't part of reef; the reef functions underneath are named as we go.

### Resetting the Database After Each Test

Before going further, let's discuss how a test's data is cleaned up so it never interferes with the next test. `testkit.RefreshDatabase` opens a transaction on the test's database and rolls it back when the test ends. It returns a `*testkit.Transaction`, a `pgx.Tx` that also [counts the queries](#counting-queries) run on it. Bind the transaction to the context your test (and your app, through the bind function its kernel takes) runs queries on, and everything the test writes disappears with it:

```go
pool := testkit.Database(t, adminURL, testURL, "shop_testing", migrations.FS)
tx := testkit.RefreshDatabase(t, pool)

ctx := db.WithConn(context.Background(), tx)
```

Every test therefore starts from the empty, migrated schema. Nothing is truncated and nothing is migrated between tests, so resetting costs one `ROLLBACK`.

Code under test that opens a transaction of its own with `db.Transaction` still works: on the test transaction, `Begin` issues a `SAVEPOINT`, so the inner "transaction" commits into the test's transaction and is rolled back with it.

> [!NOTE]
> A test on a rolled-back transaction holds one connection for its whole run, and every query it makes goes through it. Work that must be seen by another connection, such as a real race between two requests or a queue worker polling the jobs table, needs [real commits](#committing-in-a-test).

## Leasing a Test Database

`testkit.Database` leases the test a migrated database of its own until the test ends, so tests running in parallel never see or lock each other's rows:

```go
func Database(t testing.TB, adminURL string, testURL string, database string, migrations fs.FS) *pgxpool.Pool
```

- `adminURL` is a connection allowed to `CREATE DATABASE`, such as the server's `postgres` database.
- `testURL` names the server (and credentials) to lease on. Its database path is replaced by each lease's own name.
- `database` is the prefix the leases are named from.
- `migrations` are your [migrations](migrations.md), usually an `embed.FS`.

Leases are named `<database>_<package hash>_<n>`: one set per test package, since `go test` runs packages concurrently, with the hash taken from the test binary's import path so every checkout of the app names them the same. A lease is created the first time a run needs it, and kept afterwards: when a test ends, its database goes back to an idle list for the next test in the same package, and it stays on the server for the next run.

Each lease records a fingerprint of the migrations it was built from. When the first test of a run opens it and the fingerprint has changed, the lease drops its `public` schema and rebuilds it from your migrations, then River's queue tables (see [Queues](queues.md)), exactly as `migrate` builds a fresh database. Each lease's pool opens at most 16 connections. If anything goes wrong, the test fails with `test database: ...`.

A typical test case leases on the server its own connection names, with the prefix read from `DB_TEST_DATABASE` (default `flights_testing`):

```go
func testDatabase(t *testing.T, database config.Database) *pgxpool.Pool {
	t.Helper()

	testDB := database
	testDB.Database = env.Get("DB_TEST_DATABASE", "flights_testing")
	admin := database
	admin.Database = "postgres"

	return testkit.Database(t, admin.URL(), testDB.URL(), testDB.Database, migrations.FS)
}
```

> [!WARNING]
> Rebuilding a lease drops its `public` schema. To keep a misconfigured prefix from wiping a real database, reef refuses any database whose name does not contain `test`. Two checkouts running their suites at the same time must use different prefixes, or they will lease the same databases.

### Migrating a Scratch Database

The building blocks under `Database` are exported for tools that need a migrated database outside a test. `testkit.Migrated` opens a database on `testURL`'s server, creating it and migrating it when it is missing or its migrations changed, and returns a pool on it. The same rule applies: its name must contain `test`. A [model generator](models.md#running-the-generator) may read the schema from such a scratch database:

```go
scratch, admin := cfg.Database, cfg.Database
scratch.Database = env.Get("DB_TEST_DATABASE", "flights_testing") + "_codegen"
admin.Database = "postgres"

pool, err := testkit.Migrated(ctx, admin.URL(), scratch.URL(), scratch.Database, migrations.FS)
if err != nil {
	return err
}
defer pool.Close()
```

`testkit.CreateDatabase(ctx, admin, name)` creates a database on an admin `*pgx.Conn` unless it already exists. It is useful for a test that builds a database from scratch on purpose, such as a test of the migrations themselves:

```go
conn, err := pgx.Connect(ctx, adminURL)
if err != nil {
	t.Fatal(err)
}

if err := testkit.CreateDatabase(ctx, conn, "flights_testing_migrate_fresh"); err != nil {
	t.Fatal(err)
}
```

## Committing in a Test

Some behavior only shows up across connections: two requests racing for the same conditional `UPDATE` (an [atomic claim](queries.md#update-statements)), a unique job that another connection must see, a River worker. For those, skip `RefreshDatabase` and bind the leased pool itself, so each request takes its own connection and every write commits. A test case can offer this as an option, such as `tests.Committed()`, and keep the pool on the test case as `tc.Pool` either way:

```go
func TestOnlyOneOfTwoRacingRequestsClaimsTheSeat(t *testing.T) {
	tc := tests.Setup(t, tests.Committed())
	user := factories.User(tc).Create()

	t.Cleanup(func() {
		if _, err := tc.Pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID); err != nil {
			t.Errorf("cleaning up: %v", err)
		}
	})

	// ...
}
```

> [!WARNING]
> Nothing rolls a committed test back. The database is leased to the next test as soon as this one ends, so a test that commits must delete every row it committed, in a `t.Cleanup` that uses a fresh context (the test's own context is already canceled by then).

## Model Factories

When testing, you will need a few rows in the database before the test runs. Instead of spelling out every column, define a [model factory](factories.md) per model once, and create rows in the test with just the columns the test is about:

```go
func TestAPaidBookingShowsInTheTravellersTrips(t *testing.T) {
	tc := tests.Setup(t)
	user := factories.User(tc).Create()
	factories.Booking(tc).For(user).Paid().Create()

	tc.ActingAs(user).GetJSON("/api/bookings").AssertOk().AssertJSONCount(1, "")
}
```

Factories write through the test case's context, so their rows live in the test's transaction. See the [factory documentation](factories.md) to learn how to write them.

## Running Seeders

reef has no seeder classes. Create the rows a test needs with factories, so each test says what it depends on. When your app does load reference data with a console command of its own, a test may run it like any other command with [`RunCommand`](console-tests.md).

## Testing Against Valkey

Most tests use in-memory stores for the cache and rate limiter. Code that relies on Valkey itself, such as locks shared between servers, is tested against the real server with `testkit.Valkey`. It connects a fresh client to the local Valkey at `testkit.ValkeyAddr` (`127.0.0.1:6379`) and returns it with a random key prefix the test owns:

```go
func TestSchedulerLocksAreExclusiveAcrossServers(t *testing.T) {
	client, prefix := testkit.Valkey(t)

	first := schedule.CacheLocker{Store: cache.NewRedisStore(client, prefix)}
	second := schedule.CacheLocker{Store: cache.NewRedisStore(testkit.ValkeyClient(t), prefix)}

	release, ok, err := first.TryLock(context.Background(), "nightly-report", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	defer release()

	if _, ok, _ := second.TryLock(context.Background(), "nightly-report", time.Minute); ok {
		t.Fatal("a held lock must not be granted twice")
	}
}
```

When the test ends, every key under its prefix is deleted and the client is closed. The server is shared with other tests (and perhaps other apps), so a test never flushes it: keep every key under the prefix. `testkit.ValkeyClient(t)` is another client on the same server, closed when the test ends, for a second process or server sharing the store.

Without a reachable Valkey the test is skipped, unless the `CI` environment variable is set, in which case it fails: CI must provide the server.

## Available Assertions

reef provides several database assertions. Each takes a `testkit.Countable`, a query that can count its rows: a [generated model query](models.md) or a [`db.Table`](queries.md) query, with whatever `Where` clauses pick the rows you mean. An assertion counts the matching rows and fails the test (`t.Fatalf`) with the query's SQL and bindings when the count is wrong. A query that errors fails the assertion too: an assertion never passes on a query that didn't run.

```go
type Countable interface {
	Count() (int64, error)
	ToSQL() (sql string, args []any)
}
```

A test case may offer each assertion as a method (`tc.AssertDatabaseHas(q)`), which passes the test along for you.

The examples below name columns as strings: the [drift test](models.md#keeping-models-in-sync) refuses a string naming a model's column only outside the tests, so a test may use a string or the model's [column identifier](models.md#column-identifiers) (`Where(models.BookingColumns.FlightID, id)` matches the same rows).

#### AssertDatabaseCount

Assert that exactly the given number of rows match the query:

```go
testkit.AssertDatabaseCount(t, models.Bookings(tc.Ctx).Where("flight_id", id), 1)
```

#### AssertDatabaseEmpty

Assert that no row matches the query. Give it an unfiltered model query to assert its table is empty:

```go
testkit.AssertDatabaseEmpty(t, models.SavedSearches(tc.Ctx))
```

#### AssertDatabaseHas

Assert that at least one row matches the query:

```go
testkit.AssertDatabaseHas(t, db.Table[int](tc.Ctx, "airport_user").
	Where("user_id", user.ID).
	Where("airport_id", airport.ID))
```

#### AssertDatabaseMissing

Assert that no row matches the query:

```go
testkit.AssertDatabaseMissing(t, models.SavedPassengers(tc.Ctx).WhereKey(passenger.ID))
```

`AssertDatabaseMissing` and `AssertDatabaseEmpty` check the same thing; use the one that says what you mean.

### Counting Queries

To prove a request runs the queries you expect, and no more (an eager load that keeps a list from querying once per row, say), count the statements on the test transaction. `CountQueries` returns a `*testkit.QueryCounter` that starts counting from the moment you call it, and its `Assert(want)` fails the test unless exactly `want` statements ran since. The test case in [Building a Test Case](testing.md#building-a-test-case) calls it `tc.CountQueries()`:

```go
browser := tc.ActingAs(user)
queries := tc.CountQueries()

browser.GetJSON("/api/profiles").AssertOk()

// The session's read and write and the signed-in user, then the
// profiles and their two countries.
queries.Assert(6)
```

Every statement counts: the app's, the middleware's (a session read, the signed-in user) and the test's own, so arrange the rows before you start counting. The `*testkit.Transaction` counts in its `Exec`, `Query` and `QueryRow`, one statement each. Its `Begin` opens a savepoint (what a `db.Transaction` inside the code under test becomes) whose statements count toward the same total; the `SAVEPOINT` itself, and the statement that releases or rolls it back, are not counted. A [committed](#committing-in-a-test) test has no test transaction to count, and `tc.CountQueries()` fails it.

A wrong count is reported with `t.Errorf` (`ran 7 queries, want 6`), so the test goes on and shows its other failures too.

Without such a test case, take the counter from the transaction `RefreshDatabase` returned: `queries := tx.CountQueries(t)`.
