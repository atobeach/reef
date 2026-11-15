# Database: Getting Started

- [Introduction](#introduction)
    - [Configuration](#configuration)
    - [The Connection Lives in the Context](#the-connection-lives-in-the-context)
- [Running SQL Queries](#running-sql-queries)
    - [Running a General Statement](#running-a-general-statement)
    - [Running Select Queries](#running-select-queries)
    - [Query Errors](#query-errors)
- [Database Transactions](#database-transactions)
    - [Nested Transactions](#nested-transactions)
    - [Queued Jobs and Transactions](#queued-jobs-and-transactions)
- [Available Functions](#available-functions)

## Introduction

reef talks to PostgreSQL, through a [pgx](https://github.com/jackc/pgx) connection pool, and gives you three ways to work with it, from the lowest level up:

- raw SQL, with `db.Exec` and the pgx connection itself (this page);
- the fluent [query builder](queries.md), for any table;
- [generated models](models.md): a typed struct, query type and relations per table, with [eager loading](relationships.md#eager-loading) for belongs-to relations and [casts and timestamps](casts.md) applied on every write.

All three live in the `github.com/atobeach/reef/db` package and all three find their connection the same way: in the `context.Context` you pass them.

### Configuration

A pool is opened from a `postgres://` URL. `db.Open` parses the URL, connects the pool and pings it, so a bad DSN or an unreachable server fails at boot rather than on the first request:

```go
import "github.com/atobeach/reef/db"

pool, err := db.Open(ctx, "postgres://postgres@127.0.0.1:5432/flights")
if err != nil {
	return err
}
defer pool.Close()
```

Any setting pgx reads from a connection string works here too, such as `?pool_max_conns=20` or `?sslmode=require`. Your app decides where the URL comes from; a typical application builds it from the `DB_*` variables in its `config` package (see [configuration](configuration.md)).

If you would rather dial lazily, so commands such as `flights list` run without a database, build the pool yourself from `db.PoolConfig`, as an application's `bootstrap` package might:

```go
poolConfig, err := db.PoolConfig(cfg.Database.URL())
if err != nil {
	return nil, nil, fmt.Errorf("db: %w", err)
}

pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
if err != nil {
	return nil, nil, fmt.Errorf("db: %w", err)
}
```

Always start from `db.PoolConfig` (which `db.Open` uses too) rather than `pgxpool.ParseConfig`. It runs every query as an unnamed statement (`pgx.QueryExecModeExec`, still a single round trip) instead of pgx's default per-connection prepared statement cache. With that cache, a column added by a migration while the app is serving makes Postgres reject every cached `SELECT *` or `RETURNING *` plan ("cached plan must not change result type"), and inside a transaction that error aborts the whole transaction.

> [!NOTE]
> In `QueryExecModeExec`, pgx chooses each parameter's Postgres type from its Go type. A Go value with more than one possible Postgres type (a `map[string]string` could be `json` or `hstore`) is rejected as an argument; encode it to a JSON string first, or let a model's [`json` cast](casts.md#json-casting) do it.

### The Connection Lives in the Context

reef has no global connection and no `DB` facade. Instead, the connection travels in the `context.Context`: `db.WithConn` binds a pool (or a transaction) to a context, and every query built from that context runs on it. This is what makes [transactions](#database-transactions) compose: inside the callback, every query built from the callback's `ctx` runs on the transaction, without a connection argument threaded through your call chain.

You rarely call `db.WithConn` yourself. The HTTP kernel, the console kernel and the queue workers each take a bind function as their first argument (`web.NewKernel`, `console.NewKernel`, `queue.NewWorkers`) and run it on every request, command and job (see [lifecycle](lifecycle.md)); a `nil` bind function binds nothing, which suits a test of code that never queries. A typical application binds its pool, its clock and its encrypter in one place:

```go
// BindContext puts the app's connection, clock and encrypter on ctx: every
// request, command and queued job runs on them.
func (a *App) BindContext(ctx context.Context) context.Context {
	return crypt.With(clock.With(db.WithConn(ctx, a.DB), a.Clock), a.Encrypter)
}
```

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router: router,
})
```

A `*web.Context` and a `*console.Context` are themselves a `context.Context`, so a controller passes `c` straight to a query: `models.Airports(c).Active().Get()`.

A standalone program, such as a code generator, binds the pool itself:

```go
ctx = db.WithConn(ctx, pool)

schema, err := codegen.Introspect(ctx)
```

`db.Conn` returns the connection bound to a context, as a `db.Querier`: the small interface both a `*pgxpool.Pool` and a `pgx.Tx` satisfy (`Exec`, `Query`, `QueryRow` and `Begin`), so nothing in reef cares which one it runs on.

> [!WARNING]
> A query run on a context that no connection was bound to fails with `db.ErrNoConnection` ("db: no connection bound to context (use db.WithConn)"). This is always a programming error, typically a `context.Background()` passed where the request's context belonged, and it fails loudly instead of hanging.

## Running SQL Queries

### Running a General Statement

The `db.Exec` function runs a raw statement on the context's connection and returns the number of affected rows. Raw SQL uses Postgres's own `$1`, `$2` placeholders:

```go
updated, err := db.Exec(ctx,
	`UPDATE airports SET name = v.name
	 FROM (VALUES ($1::text, $2::text)) AS v(code, name)
	 WHERE airports.code = v.code`,
	code, name)
if err != nil {
	return err
}

if updated == 0 {
	// No airport has that code.
}
```

Reach for `db.Exec` when a statement is clearer in SQL than in the builder: a bulk `UPDATE ... FROM`, a `DELETE` with a subquery. For everything else, the [query builder](queries.md) writes the SQL for you, idempotent inserts and upserts included (see [insert statements](queries.md#insert-statements)).

> [!NOTE]
> `db.Exec` sends the statement as written: a [model's casts and timestamps](casts.md) only apply to writes made through that model's query. Stamp `created_at` and `updated_at` yourself in raw SQL, from `clock.Now(ctx)`.

### Running Select Queries

Most reads go through the query builder, which scans rows into structs for you, `db.Table` for any table and the generated constructors for models:

```go
codes, err := db.Table[string](ctx, "airports").Select("code").Where("is_active", true).Get()
```

For a statement the builder can't express, such as a CTE, a `UNION` or a `GROUP BY`, or a result you would rather stream row by row than collect, take the connection with `db.Conn` and use pgx directly. Here an `airports:export` command streams every airport into a snapshot file:

```go
conn, err := db.Conn(c)
if err != nil {
	return err
}

// numeric(10,7) coordinates are exported as their exact text: ::text keeps
// the snapshot byte-identical.
rows, err := conn.Query(c, `select airports.code, airports.name,
	airports.latitude::text, airports.longitude::text, countries.code as country_code
	from airports inner join countries on countries.id = airports.country_id
	order by airports.code asc`)
if err != nil {
	return err
}
defer rows.Close()

for rows.Next() {
	var code, name, latitude, longitude, countryCode string
	if err := rows.Scan(&code, &name, &latitude, &longitude, &countryCode); err != nil {
		return err
	}

	// Write the line...
}

return rows.Err()
```

Inside a [transaction](#database-transactions), `db.Conn` returns the transaction, so the statement runs on it. You don't need raw SQL to lock rows: the builder's [`LockForUpdate`](queries.md#pessimistic-locking) adds `FOR UPDATE` to any query.

> [!WARNING]
> pgx reads columns as they are stored. An [encrypted column](casts.md#encrypted-casting) read through `db.Conn` comes back sealed; read it through the query builder (a model query, or `GetAs` on one) to have it opened.

### Query Errors

Errors from `db.Exec` and the query builder name the statement that failed (`db: insert into "searches" ...: <cause>`) and wrap pgx's error with `%w`, so you may inspect it with `errors.As`:

```go
var pgErr *pgconn.PgError
if errors.As(err, &pgErr) && pgErr.Code == "23505" {
	// A unique violation.
}
```

A row that isn't there is not an error for `First`, `Find` and `Value` (they return `nil`); `FirstOrFail`, `FindOrFail` and `ResolveRouteBinding` return a `*db.ModelNotFoundError`, which the HTTP kernel renders as a 404. See [retrieving single models](models.md#retrieving-single-models).

## Database Transactions

You may use `db.Transaction` to run a set of operations inside a database transaction. If the callback returns `nil`, the transaction is committed; if it returns an error or panics, the transaction is rolled back (and the panic carries on up the stack). The rollback runs even when `ctx` was canceled, a client leaving mid-request, since pgx refuses a done context and the work would otherwise survive. Whether the work should outlive its client is the caller's choice (pass `context.WithoutCancel(ctx)` when it must): once `ctx` is done, a callback that returns `nil` is still rolled back rather than committed, and `ctx`'s error is returned. Otherwise the callback's error is returned unchanged:

```go
err := db.Transaction(ctx, func(ctx context.Context) error {
	for _, line := range lines {
		if _, err := models.Airports(ctx).Where(models.AirportColumns.Code, line.Code).Update(db.Values{
			models.AirportColumns.Name: line.Name,
		}); err != nil {
			return err
		}
	}

	return nil
})
```

The callback receives a new `ctx` with the transaction bound to it. Every query you build from that `ctx` runs on the transaction, however deep in your call chain it is built.

> [!WARNING]
> Build every query inside the callback from the callback's `ctx`. A query built from the outer context (in Go, usually because the callback's parameter was named something else, or a captured `c` was used) runs on the pool, outside the transaction, and is neither rolled back nor able to see the transaction's uncommitted rows.

There is no manual `beginTransaction` / `commit` API: return an error from the callback to roll back. If you need to know whether a write raced another request, prefer a [conditional update](queries.md#update-statements) over a transaction with a read-then-write; when the work between the read and the write can't be one statement, read the row with [`LockForUpdate`](queries.md#pessimistic-locking) inside the transaction, so a racing request waits for yours to commit.

### Nested Transactions

`db.Transaction` may be nested. A transaction started on a context that already carries one becomes a savepoint (pgx's `Tx.Begin` issues `SAVEPOINT`): an error in the inner callback rolls back only the inner work, and the outer callback may carry on.

This is also why transactions work in tests. Each feature test runs inside a transaction that is rolled back when the test ends (see [database testing](database-testing.md)), and the code under test's own `db.Transaction` calls become savepoints inside it.

### Queued Jobs and Transactions

A job dispatched on the River queue from inside a transaction is inserted on that transaction, so workers only see it once the surrounding work commits, and it vanishes with a rollback. You don't need an `afterCommit` option; see [queues](queues.md).

## Available Functions

#### `db.Open`

`Open(ctx context.Context, url string) (*pgxpool.Pool, error)` connects a pool from a `postgres://` URL using `PoolConfig`'s settings and pings it; on a failed ping the pool is closed and the error returned.

#### `db.PoolConfig`

`PoolConfig(url string) (*pgxpool.Config, error)` parses a URL into the pool settings every reef pool uses (unnamed statements, `pgx.QueryExecModeExec`). Pass it to `pgxpool.NewWithConfig` to dial lazily.

#### `db.WithConn`

`WithConn(ctx context.Context, conn db.Querier) context.Context` binds a pool or a transaction to the context.

#### `db.Conn`

`Conn(ctx context.Context) (db.Querier, error)` returns the connection bound to the context, or `db.ErrNoConnection`.

#### `db.Querier`

The interface a `*pgxpool.Pool` and a `pgx.Tx` share:

```go
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}
```

#### `db.ErrNoConnection`

The error every query returns when its context carries no connection.

#### `db.Exec`

`Exec(ctx context.Context, sql string, args ...any) (int64, error)` runs a raw statement and returns the affected row count.

#### `db.Transaction`

`Transaction(ctx context.Context, fn func(ctx context.Context) error) error` runs `fn` in a transaction (a savepoint when one is already open), committing on `nil` and rolling back on an error or a panic, even once `ctx` is canceled; a `ctx` done by the time `fn` returns `nil` rolls back too, returning `ctx`'s error.
