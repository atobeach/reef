# Testing: Getting Started

- [Introduction](#introduction)
- [Environment](#environment)
- [Creating Tests](#creating-tests)
    - [Building a Test Case](#building-a-test-case)
    - [A Full Test Case](#a-full-test-case)
- [Running Tests](#running-tests)
    - [Running Tests in Parallel](#running-tests-in-parallel)
    - [Reporting Test Coverage](#reporting-test-coverage)
- [Writing Custom Assertions](#writing-custom-assertions)
    - [Naming What a Fake Recorded](#naming-what-a-fake-recorded)
    - [Testing an Assertion](#testing-an-assertion)

## Introduction

reef is built with testing in mind. Tests are plain Go tests: they live in `_test.go` files, use the standard `testing` package, and run with `go test`. There is no separate runner and no assertion library to learn. On top of that, reef ships a testing layer of its own:

- [`reef/testkit`](#creating-tests) leases each test a migrated Postgres database and wraps it in a transaction that is rolled back when the test ends, asserts on the rows a test leaves behind and on [how many queries it ran](database-testing.md#counting-queries), sends in-process HTTP requests with chainable [response assertions](http-tests.md), and runs [console commands](console-tests.md) with assertions on their exit code and output.
- [`reef/db/factory`](factories.md) builds model factories: a valid row in one call, named states, and overrides typed against the model.
- Every service with an outside effect has an in-memory fake and an `xxxtest` package that asserts on it: `mailtest`, `queuetest`, `eventstest`, `broadcastingtest`, `httpclienttest`, `aitest` and `clocktest`. They are covered in [Mocking](mocking.md).

Like Laravel, reef encourages two kinds of tests. **Unit tests** sit next to the code they test, in the same directory, and exercise a function or a type without booting the app. **Feature tests** boot the whole app and drive it the way a client does: an HTTP request through the real kernel, a console command through the real console kernel. In a typical reef application, feature tests live in `tests/feature` (package `feature_test`), and the helpers they share live in `tests`. Most of your tests should be feature tests: they give you the most confidence that the system as a whole works.

## Environment

reef has no `phpunit.xml`. Instead, the test case you write (see [below](#building-a-test-case)) loads the test environment itself, typically from a `.env.testing` file in your module root, with `env.LoadFile`. Unlike `env.Load`, `LoadFile` fails when the file is missing, and, as always with reef's [configuration](configuration.md), anything already exported in the shell wins over the file. That is how CI, or a Postgres on another port, points the suite at its own server without editing the file.

A `.env.testing` like this one shows the kind of settings a test environment wants:

```ini
APP_ENV=testing
# Any fixed 32-byte key will do.
APP_KEY=base64:dGVzdGluZy1rZXktdGVzdGluZy1rZXktdGVzdGluZyE=

DB_HOST=127.0.0.1
DB_PORT=5432
DB_USERNAME=postgres

# Production cost would make every factory user take a quarter second.
BCRYPT_ROUNDS=4
# Jobs run inline, on the test transaction.
QUEUE_CONNECTION=sync
```

Beyond the environment, a test case swaps the app's outside services for test doubles before it boots: in-memory stores for the [cache](cache.md) (`cache.NewMemoryStore()`) and [rate limiter](rate-limiting.md) (`ratelimit.NewMemoryStore()`), a [`mail.Fake`](mocking.md#mail-fake) transport, a [`broadcasting.Fake`](mocking.md#broadcast-fake), an [`httpclient.MockClient`](mocking.md#http-client-mock) that fails the test on any request it has no reply for, and a [`clock.Clock`](mocking.md#interacting-with-time) of the test's own. Nothing a test does reaches the network or leaks into another test.

> [!NOTE]
> Keep real third-party credentials out of `.env.testing`. A test that needs a setting (an API key, a feature switch) sets it on the config it boots with, and the HTTP mock answers the calls that key would authorize.

## Creating Tests

There is no `make:test` command: create a `_test.go` file and write a `TestXxx` function. A feature test reads like this:

```go
package feature_test

import (
	"testing"

	"example.com/flights/app/models"
	"example.com/flights/database/factories"
	"example.com/flights/tests"
)

func TestTheAirportsIndexListsActiveAirports(t *testing.T) {
	tc := tests.Setup(t)

	factories.Airport(tc).Create(func(a *models.Airport) {
		a.Code = "LHR"
		a.Name = "London Heathrow"
	})

	tc.Get("/api/airports").
		AssertOk().
		AssertJSONCount(1, "").
		AssertJSONPath("0.code", "LHR")
}
```

`tests.Setup` and `tc` are not part of reef: they are your application's test case, a small helper you write once on top of `testkit` (see [Building a Test Case](#building-a-test-case)). Here `tests.Setup` boots the app for this one test; `factories.Airport` inserts a row inside the test's transaction; `tc.Get` sends a request through the real HTTP kernel and returns a [`TestResponse`](http-tests.md#available-assertions).

Name tests as sentences that state the behavior (`TestRejectsABookingForAFlightThatHasDeparted`), and prefer a table of cases when several cases differ only in data. Shared helpers belong in one place, your app's `tests` package, rather than copied from file to file.

### Building a Test Case

Laravel gives every feature test a base `TestCase` class. reef has no base class to extend: you write one small `Setup` function, once, in your app's `tests` package, and it assembles reef's pieces for each test:

1. `t.Parallel()`, so the test runs alongside the others.
2. [`testkit.Database`](database-testing.md#leasing-a-test-database) to lease the test a migrated database of its own, and [`testkit.RefreshDatabase`](database-testing.md#resetting-the-database-after-each-test) to open the transaction that is rolled back when the test ends: a `*testkit.Transaction`, which also [counts the queries](database-testing.md#counting-queries) run on it.
3. A context carrying that transaction (`db.WithConn`) and a clock of the test's own (`clock.With`). Queries, factories and assertions all take this context.
4. The fakes, handed to the app the way its real services would be.
5. The app's HTTP kernel, with a [`testkit.Client`](http-tests.md#making-requests) sending requests through it.

Here is a trimmed-down test case for an app whose routes are registered by `routes.API`, given the services they send mail and call APIs with:

```go
// Package tests boots the app for feature tests.
package tests

import (
	"context"
	"os"
	"testing"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/mail"
	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/web"

	"example.com/flights/database/migrations"
	"example.com/flights/routes"
)

// TestCase is one booted app plus a client sending requests through it.
type TestCase struct {
	*testkit.Client
	// Ctx carries the test transaction and the test's clock.
	Ctx   context.Context
	Clock *clock.Clock
	Mail  *mail.Fake
	HTTP  *httpclient.MockClient

	t  testing.TB
	tx *testkit.Transaction
}

// T and Context make the test case a factory.TestCase.
func (tc *TestCase) T() testing.TB            { return tc.t }
func (tc *TestCase) Context() context.Context { return tc.Ctx }

// CountQueries counts the statements run on the test transaction from now.
func (tc *TestCase) CountQueries() *testkit.QueryCounter {
	return tc.tx.CountQueries(tc.t)
}

// Setup boots the app for one test, on a database leased to it.
func Setup(t *testing.T) *TestCase {
	t.Helper()
	t.Parallel()

	pool := testkit.Database(t,
		os.Getenv("TEST_ADMIN_URL"), // a connection to the server's postgres database
		os.Getenv("TEST_DB_URL"),    // the same server: its path is replaced per lease
		"flights_testing", migrations.FS)
	tx := testkit.RefreshDatabase(t, pool)

	now := &clock.Clock{}
	bind := func(ctx context.Context) context.Context {
		return clock.With(db.WithConn(ctx, tx), now)
	}

	tc := &TestCase{
		Ctx:   bind(context.Background()),
		Clock: now,
		Mail:  &mail.Fake{},
		HTTP:  httpclient.NewMock(t, nil),
		t:     t,
		tx:    tx,
	}

	// The app's routes, built on the fakes instead of the real mailer and APIs.
	router := web.NewRouter()
	routes.API(router, tc.Mail, tc.HTTP)

	kernel, err := web.NewKernel(bind, web.KernelConfig{Router: router})
	if err != nil {
		t.Fatalf("booting the HTTP kernel: %v", err)
	}

	tc.Client = testkit.NewClient(t, kernel)

	return tc
}
```

The kernel's bind function is what puts each request on the test's transaction and clock: the handler of a request your test sends sees the rows your test created, and the time your test froze.

### A Full Test Case

As your suite grows, your test case grows with it. None of the helpers below ship with reef; they are conveniences you add to your own `tests` package, each a few lines over the reef function it wraps. The other testing pages use them in examples, so here is what a full `tests.Setup(t, opts...)` might do:

- load `.env.testing` and the config, with a throwaway storage directory per test;
- lease a database on the server the app's own connection names (with a prefix overridable by `DB_TEST_DATABASE`, so two checkouts testing at once never share leases);
- boot the real app with in-memory cache and rate-limit stores, `tc.Mail`, `tc.Broadcasts`, `tc.HTTP` and the test's clock;
- be itself a client on the app's kernel (`tc.Get`, `tc.PostJSON`), with `tc.NewClient()` for a second visitor, `tc.Browser()` and `tc.ActingAs(user)` for the SPA as the server sees it (see [Building Your Own Client](http-tests.md#building-your-own-client)), and `tc.Mount(router)` for routes of the test's own, served on the test transaction;
- expose the console kernel (`tc.Console`, run with `tc.RunCommand`), a recording queue on demand (`tc.FakeQueue()`), time travel (`tc.FreezeTime()`, `tc.TravelTo(at)`, `tc.Travel(d)`, `tc.FakeSleep()`), the [database assertions](database-testing.md#available-assertions) as methods, and a [query counter](database-testing.md#counting-queries) (`tc.CountQueries()`);
- keep what a test reaches into in fields: `tc.Ctx` (bound as the app binds a request: the test transaction, the test's clock and the encrypter), `tc.App` (the booted app: its config, mailer, event dispatcher and guard) and `tc.Pool` (the leased database, outside the transaction).

Its options adjust the boot: `tests.WithHTTP(replies)` answers outbound calls by request name, `tests.WithConfig(func(cfg *config.Config) {...})` changes a setting before the app boots, and `tests.Committed()` skips the rolled-back transaction for tests that need real commits (see [Committing in a Test](database-testing.md#committing-in-a-test)).

```go
tc := tests.Setup(t,
	tests.WithHTTP(map[string]any{
		"ListFlights": httpclient.JSON(200, map[string]any{"flights": []any{}}),
	}),
	tests.WithConfig(func(cfg *config.Config) { cfg.Services.Airline.APIKey = "test-key" }),
)

tc.Get("/api/flights?from=LHR&to=JFK&date=2026-12-01").AssertOk().AssertJSON(map[string]any{"flights": []any{}})
```

## Running Tests

Run the suite with `go test`. While you work, run only the tests that prove your change, then the whole suite with the race detector before you finish:

```shell
go test ./tests/feature -run TestRejectsABookingForAFlightThatHasDeparted
go test -race ./...
```

### Running Tests in Parallel

Go runs test packages concurrently, and within a package it runs every test that calls `t.Parallel()` alongside the others. A test case built as [above](#building-a-test-case) calls it for you.

Parallel tests need no coordination because nothing is shared: each test holds a [leased database](database-testing.md#leasing-a-test-database) until it ends, and its own stores, fakes and clock. The leases are named `<database>_<package hash>_<n>`, one set per test package, created and migrated the first time they are needed and then kept between runs, so a second run starts at full speed. When a migration changes, each lease drops and rebuilds its schema the first time a run opens it.

> [!WARNING]
> Two checkouts of the same app running their suites at the same time would lease the same databases. Give each checkout its own database prefix (for example, read it from a `DB_TEST_DATABASE` variable).

### Reporting Test Coverage

Coverage is Go's own: pass `-cover` for a summary, or `-coverprofile` to write a profile you can open with `go tool cover`. Feature tests exercise packages other than their own, so use `-coverpkg` to count the code they reach:

```shell
go test -coverpkg=./app/... -coverprofile=cover.out ./tests/feature
go tool cover -html=cover.out
```

## Writing Custom Assertions

Your app will grow assertions of its own: a browser that asserts on its session, a fake for one of your services. reef's assertion helpers follow two conventions you may reuse.

### Naming What a Fake Recorded

Every reef fake assertion, when it fails, says what the fake did record, so you see at once why the assertion missed. `testkit.Summary` builds that list: it names each distinct entry in the order first recorded, with a count when it repeats, or `nothing` when the list is empty.

```go
testkit.Summary([]string{"SearchFlights", "GetFare", "GetFare", "GetFare"})
// "SearchFlights, GetFare ×3"

testkit.Summary(nil)
// "nothing"
```

A fake of your own can use it in its failure messages:

```go
// AssertCharged fails unless amount was charged.
func AssertCharged(t testing.TB, f *payments.Fake, amount int64) {
	t.Helper()

	names := make([]string, 0, len(f.Charges()))
	for _, charge := range f.Charges() {
		if charge.Amount == amount {
			return
		}
		names = append(names, fmt.Sprintf("charge of %d", charge.Amount))
	}

	t.Fatalf("payments: nothing charged %d; charged: %s", amount, testkit.Summary(names))
}
```

### Testing an Assertion

An assertion deserves tests of its own: that it passes when it should, and that it fails with a useful message when it shouldn't. `testkit.Record(t)` wraps the real test in a `*testkit.RecordingT`, a `testing.TB` that records failures instead of failing. Hand it to the assertion, then check what it recorded:

```go
func TestAssertStatusNamesTheStatusItGot(t *testing.T) {
	recording := testkit.Record(t)

	testkit.NewClient(recording, http.NotFoundHandler()).Get("/").AssertOk()

	recording.AssertFailed("status = 404, want 200")
}
```

`RecordingT` methods:

- `Error`, `Errorf`: record a failure.
- `Fatal`, `Fatalf`: record a failure and, unlike `testing`'s, **return**, so the assertion may go on and record more.
- `Failed`: reports whether anything was recorded.
- `Helper`: does nothing (it implements `testing.TB`).
- `AssertPassed`: fails the real test if anything was recorded.
- `AssertFailed(message)`: fails the real test unless a failure was recorded and the first one, the one a real test would have stopped at, contains `message`.

Everything else (`Cleanup`, `TempDir`, `Context`, `Name` and the rest) passes through to the real test, which is the embedded `testing.TB`.

> [!NOTE]
> Because `Fatal` returns under a `RecordingT`, an assertion under test keeps running after its first failure. Return right after a `Fatal` wherever the rest of your assertion would misbehave on the value that failed, as reef's own assertions do.
