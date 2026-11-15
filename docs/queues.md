# Queues

- [Introduction](#introduction)
    - [Connections and Queues](#connections-and-queues)
    - [Driver Prerequisites](#driver-prerequisites)
- [Creating Jobs](#creating-jobs)
    - [Job Structure](#job-structure)
    - [Registering Handlers](#registering-handlers)
    - [Unique Jobs](#unique-jobs)
    - [Jobs and Secrets](#jobs-and-secrets)
- [Dispatching Jobs](#dispatching-jobs)
    - [Synchronous Dispatching](#synchronous-dispatching)
    - [Jobs and Database Transactions](#jobs-and-database-transactions)
    - [Customizing the Queue](#customizing-the-queue)
    - [Specifying Max Job Attempts and Timeout Values](#specifying-max-job-attempts-and-timeout-values)
    - [Error Handling](#error-handling)
- [Running the Queue Worker](#running-the-queue-worker)
    - [The queue:work Command](#the-queuework-command)
    - [Processing Several Queues](#processing-several-queues)
    - [Queue Workers and Deployment](#queue-workers-and-deployment)
    - [Job Expirations and Timeouts](#job-expirations-and-timeouts)
    - [Poll-Only Mode](#poll-only-mode)
- [Dealing With Failed Jobs](#dealing-with-failed-jobs)
    - [Cleaning Up After Failed Jobs](#cleaning-up-after-failed-jobs)
    - [Retention](#retention)
- [Job Events](#job-events)
- [Testing](#testing)
- [Available Methods](#available-methods)

## Introduction

While building your web application, you may have some tasks, such as refreshing a forecast from a third-party API or generating AI content, that take too long to perform during a typical web request. reef's queue package (`github.com/atobeach/reef/queue`) lets you move them to background jobs that a worker processes, so your application can respond to requests quickly.

reef's queue runs on [River](https://riverqueue.com), a job queue that stores its jobs in your Postgres database. A job is a typed, JSON-serializable Go struct; its handler is registered once; and dispatching one is a single call on the connection your code is handed, here a command's `Queue` field:

```go
err := cmd.Queue.Dispatch(ctx, jobs.UpdateAirportWeather{AirportID: airport.ID})
```

### Connections and Queues

Before getting started with reef queues, it is important to understand the distinction between "connections" and "queues". A connection is where jobs go when you dispatch them: anything implementing `queue.Dispatcher`:

```go
type Dispatcher interface {
	Dispatch(ctx context.Context, job queue.Job) error
}
```

reef ships three connections:

| Connection | Behavior |
|---|---|
| `*queue.River` | Stores the job in Postgres for a worker to run. Production's connection. |
| `queue.Sync` | Runs the job immediately, inline, and returns its error to the dispatcher. The test default. |
| `*queue.Fake` | Records the job without running it, for [tests](#testing). |

A typical application builds one at boot from `QUEUE_CONNECTION` (`database` for River, `sync` to run inline), keeps it as `a.Queue`, and hands it to every command, service and job that dispatches, as a `queue.Dispatcher` field or parameter. Where Laravel code would reach for the `Queue` facade, reef code calls the connection it was handed.

Within a connection, jobs are routed to named queues. A job goes to the `default` queue (`queue.DefaultQueue`) unless it [names another](#customizing-the-queue), and a worker processes the queues you tell it to. Some applications never push to more than one queue, while others use a separate queue to keep a large backfill from sitting ahead of transactional mail.

### Driver Prerequisites

River needs its own tables (`river_job`, `river_leader` and friends). The `queue:migrate` command creates or updates them, and is safe to run again. It prints each River migration it applies (`River migration 006 ... DONE`), or `Nothing to migrate.` when the tables are current:

```shell
flights queue:migrate
```

Your application's `migrate` command can run River's migrations right after the application's own, so the release command prepares the queue before any worker or dispatcher starts. If you call them from Go, `queue.Migrate(ctx, pool)` runs the same migrations on a `*pgxpool.Pool` and returns the versions it applied. See [migrations](migrations.md) for the `After` hook that makes this possible.

## Creating Jobs

Jobs usually live in an `app/jobs` package. reef has no generator: a job is a plain struct you write by hand.

### Job Structure

A job is any type implementing `queue.Job`, which has one method, `Kind`. The struct's fields are the job's payload, stored as JSON in the queue table, so they must be exported and serializable. Everything else about the job is opt-in, through optional methods on the same type:

```go
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"example.com/flights/app/models"
	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/queue"
)

// UpdateAirportWeather refreshes one airport's 14-day forecast from
// the weather service, upserted per day so re-runs stay idempotent.
type UpdateAirportWeather struct {
	AirportID int64 `json:"airport_id" river:"unique"`
}

var (
	_ queue.Tries          = UpdateAirportWeather{}
	_ queue.Backoff        = UpdateAirportWeather{}
	_ queue.Timeout        = UpdateAirportWeather{}
	_ queue.ShouldBeUnique = UpdateAirportWeather{}
	_ queue.Failer         = UpdateAirportWeather{}
)

// Kind implements queue.Job.
func (UpdateAirportWeather) Kind() string { return "update_airport_weather" }

// Tries implements queue.Tries.
func (UpdateAirportWeather) Tries() int { return 3 }

// Backoff implements queue.Backoff.
func (UpdateAirportWeather) Backoff() []time.Duration {
	return []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}
}

// Timeout implements queue.Timeout.
func (UpdateAirportWeather) Timeout() time.Duration { return 30 * time.Second }

// ShouldBeUnique marks the job unique per AirportID until it finishes.
func (UpdateAirportWeather) ShouldBeUnique() {}

// Handle fetches and stores the forecast.
func (j UpdateAirportWeather) Handle(ctx context.Context, connector *httpclient.Client) error {
	airport, err := models.Airports(ctx).FindOrFail(j.AirportID)
	if err != nil {
		return fmt.Errorf("loading airport %d: %w", j.AirportID, err)
	}

	// ...
}

// Failed implements queue.Failer: logs the exhausted job.
func (j UpdateAirportWeather) Failed(ctx context.Context, err error) {
	slog.ErrorContext(ctx, "UpdateAirportWeather: failed", "airport_id", j.AirportID, "error", err)
}
```

> [!WARNING]
> `Kind` names the job in the queue table. Never change it once jobs of that kind may be queued: a worker can't run a job whose kind it no longer knows.

The optional methods reef looks for, each an interface in the `queue` package, are:

| Method | Interface | Effect |
|---|---|---|
| `Tries() int` | `queue.Tries` | Total attempts before the job is discarded. |
| `Backoff() []time.Duration` | `queue.Backoff` | The wait before each retry. |
| `Timeout() time.Duration` | `queue.Timeout` | Cancels the handler's context after this long. |
| `ShouldBeUnique()` | `queue.ShouldBeUnique` | Keeps one copy queued per `river:"unique"` field values. |
| `Failed(ctx, err)` | `queue.Failer` | Called once the job has used its last attempt. |
| `Queue() string` | `queue.OnQueue` | The queue the job is routed to. |

reef finds these methods by type assertion when the job is dispatched and run, so a method whose signature slipped (a `Backoff() []int`, say) satisfies nothing and is silently skipped: the job just loses its backoff. Declare which ones a job means to implement in a `var` block beside its type, as the weather job above does. Then a slip is a compile error:

```go
var _ queue.Backoff = UpdateAirportWeather{}
```

Assert on the value the job is dispatched as: a value for a job dispatched by value, `(*T)(nil)` for one with pointer receivers.

You may notice the job's `Handle` method isn't one of them. A job's struct only carries data that survives the trip through the queue table; the services it needs are handed to it when its handler is registered.

### Registering Handlers

A worker needs to know, for every kind of job, the function that runs it. These handlers live in a `*queue.Workers` registry, built once with `queue.NewWorkers` and shared by every connection. `queue.Register` adds the handler for one job type, decoding the payload into your struct before it calls you:

```go
// Register adds every app job's handler to the worker registry.
func Register(w *queue.Workers, deps Deps) {
	queue.Register(w, func(ctx context.Context, job UpdateAirportWeather) error {
		return job.Handle(ctx, deps.Forecasts)
	})
	queue.Register(w, func(ctx context.Context, job RefreshDepartures) error {
		return job.Handle(ctx, deps.Airline, deps.Queue)
	})
}
```

Registering the same kind twice panics. Handlers may be registered before or after the River connection is built.

`queue.NewWorkers` takes one required argument, a bind function that attaches your application's services (typically the database connection, the clock and the encrypter) to each handler's context, as the HTTP kernel does per request; `nil` binds nothing. It runs for every job, including those the `sync` connection runs inline, so a bind function should keep a connection the dispatcher already bound; a sync job then sees the rows its dispatcher hasn't committed yet:

```go
a.Workers = queue.NewWorkers(func(ctx context.Context) context.Context {
	if _, err := db.Conn(ctx); err == nil {
		return ctx
	}

	return a.BindContext(ctx)
})
```

`Workers.Jobs()` lists a zero value of every registered job type, sorted by kind, for tests that hold every job to a rule (see [Job Expirations and Timeouts](#job-expirations-and-timeouts)).

### Unique Jobs

Sometimes, you may want to ensure that only one instance of a specific job is on the queue at any point in time. You may do so by implementing `ShouldBeUnique` on your job, and tagging the fields that identify it with `river:"unique"`:

```go
type UpdateAirportWeather struct {
	AirportID int64 `json:"airport_id" river:"unique"`
}

func (UpdateAirportWeather) ShouldBeUnique() {}
```

In this example, a second `UpdateAirportWeather` for the same `AirportID` is silently skipped (`Dispatch` returns `nil`) while the first is available, scheduled, running or waiting to retry. Once it has completed, or finally failed, the next one may be queued. When no field carries the tag, River compares the whole payload.

### Jobs and Secrets

A job's payload sits in the `river_job` table until it is swept, readable by anyone with database access. Rather than carry a secret, have a job carry the id of the record it needs, and load the record when it runs. Mail, which can't always do that, seals its queued payloads with the application key; see [queued mail](mail.md#queued-mail-and-secrets).

## Dispatching Jobs

Once you have written your job, you may dispatch it on a connection: call `Dispatch` with the context and the job. For example, a `schedules:refresh` command might dispatch one job per airport on the connection in its `Queue` field:

```go
func (cmd RefreshSchedules) Handle(c *console.Context) error {
	// ...

	for _, airport := range airports {
		if err := cmd.Queue.Dispatch(c, jobs.RefreshDepartures{AirportID: airport.ID}); err != nil {
			return err
		}
	}

	// ...
}
```

River refuses a job whose kind has no registered handler, and wraps any failure as `queue: dispatching <kind>: ...`.

reef has no delayed dispatch, job chains, batches, queued closures or job middleware. A handler that should be followed by another job dispatches it itself, as `RefreshDepartures` does with the dispatcher it is handed.

### Synchronous Dispatching

If you would like to dispatch a job immediately (synchronously), use the `queue.Sync` connection. The job runs on the dispatcher's context, and so inside its transaction, and its error is returned from `Dispatch`. A sync job gets one attempt, which is also its last, so its `Failed` method runs when it fails:

```go
connection := queue.Sync{Workers: a.Workers}

err := connection.Dispatch(ctx, jobs.UpdateAirportWeather{AirportID: airport.ID})
```

### Jobs and Database Transactions

When you dispatch on River inside a database transaction (`db.Transaction`, or the transaction a feature test runs in), the job is inserted on that transaction. Workers only see it once the surrounding work commits, and it vanishes if the transaction rolls back, so a job can never run against rows that were never committed:

```go
err := db.Transaction(ctx, func(ctx context.Context) error {
	if err := airport.Update(ctx, db.Values{models.AirportColumns.IsActive: true}); err != nil {
		return err
	}

	return cmd.Queue.Dispatch(ctx, jobs.UpdateAirportWeather{AirportID: airport.ID})
})
```

See [database transactions](database.md) for `db.Transaction`.

### Customizing the Queue

By pushing jobs to different queues, you may "categorize" your queued jobs and decide how many workers each gets. To route a job, implement `Queue`; an empty string means the default queue:

```go
// Queue isolates the `--all` backfill, thousands of jobs, on its own queue
// so it never sits ahead of transactional mail on the default queue.
func (j BackfillFareHistory) Queue() string {
	if j.All {
		return "backfill"
	}

	return ""
}
```

### Specifying Max Job Attempts and Timeout Values

#### Max Attempts

A job is attempted once unless you say otherwise. `RiverConfig.Tries` sets the default for every job (you might read it from `QUEUE_TRIES`), and a job's own `Tries` method overrides it:

```go
func (UpdateAirportWeather) Tries() int { return 3 }
```

The attempts are fixed when the job is dispatched, so a worker can't change them. `queue:work` accepts `--tries` only so existing scripts keep working.

#### Backoff

When a job fails, River retries it after an exponentially growing delay, so a failing upstream isn't hammered with instant retries. To choose the delays yourself, implement `Backoff`. Its first entry is the wait after the first failed attempt, and the last entry repeats for any later attempts:

```go
func (UpdateAirportWeather) Backoff() []time.Duration {
	return []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}
}
```

#### Timeout

A job without a `Timeout` method may run for as long as the worker's `--timeout`, 60 seconds by default. A job's own `Timeout` wins; when it passes, the handler's context is canceled:

```go
func (UpdateAirportWeather) Timeout() time.Duration { return 30 * time.Second }
```

Your handler must watch its context (pass it to every query and outbound request) for a timeout to stop it.

### Error Handling

If a handler returns an error, the attempt fails and the job is released back onto the queue to be retried, until it has used all of its attempts. A handler that panics doesn't take the worker down: the panic is recovered and fails the attempt like an error, `queue: <kind> panicked: <value>`, which is what River stores. River keeps only that text, so the panic is logged where it was recovered, `queue: job panicked` with the job's `kind` and the `stack`.

## Running the Queue Worker

### The queue:work Command

reef includes a console command that starts a queue worker and processes new jobs as they are pushed onto the queue. You may run the worker using the `queue:work` command. Once it has started, it continues to run until it is stopped:

```shell
flights queue:work
```

To keep the process running permanently in the background, use a process monitor such as systemd or Supervisor, running for example `flights queue:work --queue=default,backfill --sleep=1`. The worker is a long-lived process that keeps its code in memory, so it won't notice changes to your code until it is restarted: restart your workers when you deploy.

`queue:work` accepts the following options:

| Option | Default | Description |
|---|---|---|
| `connection` (argument) | | The connection to work; only `database` exists. |
| `--queue=` | `default` | The queues to work, comma-separated. |
| `--concurrency=` | `10` | How many jobs this process runs at once, per queue. |
| `--timeout=` | `60` | Seconds a job without its own `Timeout` may run. |
| `--sleep=` | `1` | Seconds between polls for new jobs (`0` keeps River's default, one second); see [poll-only mode](#poll-only-mode). |
| `--max-time=` | `0` | Seconds after which the worker stops cleanly; `0` runs until it is stopped. |
| `--tries=` | | Accepted for compatibility; attempts are fixed when a job is dispatched. |

Once started, the worker announces what it processes (`Processing jobs from the [default,backfill] queue.`). Under the `sync` connection there is nothing to work, and `queue:work` fails with `queue:work needs QUEUE_CONNECTION=database; the sync connection runs jobs inline.` A connection argument other than `database` fails with `Queue connection [redis] is not defined.`, and a negative or non-numeric `--timeout`, `--sleep` or `--max-time`, or a `--concurrency` below 1, fails with a sentence naming the option.

### Processing Several Queues

You may give the worker a comma-separated list of queues:

```shell
flights queue:work --queue=default,backfill
```

Unlike Laravel's `--queue=high,low`, the list isn't an order of priority: each queue gets its own `--concurrency` workers and is processed independently, so a backlog on one never holds up the other. That is how a long fare-history backfill on its own queue never delays mail.

### Queue Workers and Deployment

When the worker's context is canceled (`SIGINT`/`SIGTERM`, as a deploy sends) or `--max-time` passes, it stops gracefully: it takes no new jobs, gives running jobs up to the worker's `--timeout` plus five seconds to finish, then cancels them. `--max-time` lets a supervisor restart workers periodically:

```shell
flights queue:work --max-time=3600
```

From Go, `queue.Commands(connection, pool)` returns `queue:work` and `queue:migrate` for your [console kernel](console.md#registering-commands). `connection` returns the application's `*queue.River` (`nil` under the sync connection), and `pool` opens the database River's migrations target, returning a release function:

```go
k.Register(queue.Commands(func() *queue.River {
	river, _ := a.Queue.(*queue.River)

	return river
}, migrationPool(a, pool))...)
```

`queue:work` itself calls `River.Work(ctx, options)`, with a `queue.WorkOptions` built from its options: `Queues`, `Concurrency`, `Sleep`, `Timeout` and `MaxTime`. You may call it directly to run a worker inside another process; it returns once the worker has stopped.

### Job Expirations and Timeouts

`RiverConfig.RetryAfter` is how long a job may stay "running" before River presumes its worker crashed and retries it. It must exceed every job's timeout, or a slow job would run twice. A test over every registered job can guard that (`tests.Setup` is your own [test case](testing.md#a-full-test-case)):

```go
func TestKeepsRetryAfterAboveTheLongestJobTimeout(t *testing.T) {
	tc := tests.Setup(t)

	for _, job := range tc.App.Workers.Jobs() {
		if timeout := queue.TimeoutOf(job); timeout >= tc.App.Config.Queue.RetryAfter {
			t.Errorf("%s: timeout %s >= retry_after %s", job.Kind(), timeout, tc.App.Config.Queue.RetryAfter)
		}
	}
}
```

### Poll-Only Mode

By default, River wakes a worker the moment a job is inserted, using Postgres's `LISTEN`/`NOTIFY`, and `--sleep` only matters when the worker is idle. `LISTEN` needs a database session of its own, which a transaction pooler (PgBouncer in transaction mode) can't give. Set `RiverConfig.PollOnly` when your workers reach Postgres through one: the worker then polls every `--sleep`, which becomes the longest a new job waits, and River's leader election polls too, so a dead leader is replaced within about five seconds. An application deployed behind PgBouncer might default to polling and let `QUEUE_POLL_ONLY=false` turn it off.

The River connection is built with `queue.NewRiver`, from a `queue.RiverConfig`:

```go
connection, err := queue.NewRiver(queue.RiverConfig{
	Pool:       pool,
	Workers:    a.Workers,
	Tries:      cfg.Tries,
	RetryAfter: cfg.RetryAfter,
	PollOnly:   cfg.PollOnly,
	Logger:     slog.Default(),
})
```

| Field | Purpose |
|---|---|
| `Pool` | The Postgres pool the jobs are stored in. |
| `Workers` | The handler registry: what `Work` runs, and the kinds `Dispatch` accepts. |
| `Tries` | The default attempts for a job without a `Tries` method; at least 1. |
| `RetryAfter` | How long a running job may go before it is presumed dead and retried. |
| `PollOnly` | Poll for new jobs instead of listening for inserts. |
| `Logger` | River's logger; defaults to `slog.Default()` (see [logging](logging.md#loggers-reef-accepts)). |

A connection only inserts until you call `Work`, so API processes can dispatch without running jobs.

## Dealing With Failed Jobs

A job that has used all of its attempts is discarded. reef has no `failed_jobs` table or `queue:retry` command; discarded jobs stay in River's `river_job` table, in the `discarded` state, where you can inspect them.

### Cleaning Up After Failed Jobs

When a particular job fails for the last time, you may want to log it or alert someone. Implement `Failed` on the job; it is called once, with the error of the final attempt:

```go
// Failed implements queue.Failer: logs the exhausted job.
func (j UpdateAirportWeather) Failed(ctx context.Context, err error) {
	slog.ErrorContext(ctx, "UpdateAirportWeather: failed", "airport_id", j.AirportID, "error", err)
}
```

> [!WARNING]
> `Failed` receives the handler's context. If the last attempt timed out, that context is already canceled: use `context.WithoutCancel(ctx)` for any query or request `Failed` makes.

### Retention

Job payloads can carry personal details, so reef deletes a completed job almost at once (on River's next cleaner sweep) instead of keeping it for River's default 24 hours. Discarded jobs keep River's default retention of seven days.

## Job Events

`Workers.AfterProcessed` registers a function to run after every job a worker processes successfully. For example, an application may use it to flush the analytics events a job captured as soon as the job finishes:

```go
flush := listeners.FlushAnalytics{Analytics: a.Analytics}
a.Workers.AfterProcessed(func(context.Context, queue.Job) { flush.Handle() })
```

## Testing

When testing code that dispatches jobs, you may wish to instruct reef to not actually execute the job, since the job's code can be tested directly and separately of the code that dispatches it. A `*queue.Fake` is a connection that records every job instead of running it, and `queuetest.Pushed[T](fake)` returns the recorded jobs of one type. A [test case](testing.md#a-full-test-case) may offer a helper, `tc.FakeQueue()` below, that reboots the test's app on one:

```go
func TestRefreshDispatchesOneJobPerAirportOrTheNamedOne(t *testing.T) {
	tc := tests.Setup(t)
	fake := tc.FakeQueue()
	heathrow := factories.Airport(tc).Create(func(a *models.Airport) { a.Code, a.Name = "LHR", "London Heathrow" })
	factories.Airport(tc).Create(func(a *models.Airport) { a.Code, a.Name = "JFK", "New York JFK" })

	tc.RunCommand("schedules:refresh", "LHR").AssertSuccessful()

	queuetest.AssertCount(t, fake, 1)
	queuetest.AssertPushed(t, fake, func(job jobs.RefreshDepartures) bool {
		return job == jobs.RefreshDepartures{AirportID: heathrow.ID}
	})

	if pushed := queuetest.Pushed[jobs.RefreshDepartures](fake); pushed[0].AirportID != heathrow.ID {
		t.Fatalf("pushed %+v", pushed)
	}
}
```

To test a job itself, call its handler directly, or run it the way a worker would with `Workers.Handle(ctx, job, attempt, maxAttempts)`: bound context, timeout, `Failed` once the last attempt fails, and the `AfterProcessed` listeners on success. See [mocking](mocking.md) for the `queuetest` assertions.

## Available Methods

#### Package

- `queue.DefaultQueue`: `"default"`, the queue a job without a `Queue` method goes to.
- `queue.NewWorkers(bind)`: an empty handler registry; `bind` attaches your services to every handler's context (`nil`: none).
- `queue.Register[T](workers, handle)`: adds the handler for job type `T`; a kind registered twice panics.
- `queue.NewRiver(config)`: the River connection.
- `queue.Migrate(ctx, pool)`: creates or updates River's tables, returning the versions applied.
- `queue.Commands(connection, pool)`: the `queue:work` and `queue:migrate` commands.
- `queue.TriesOf(job, fallback)`: the job's `Tries`, or `fallback` when it declares none.
- `queue.TimeoutOf(job)`: the job's `Timeout`, or zero.
- `queue.BackoffFor(job, attempt)`: the wait after a failed attempt (1-based) and whether the job declares a backoff.
- `queue.InsertOpts(job, defaultTries)`: River's insert options for the job: its attempts, its queue and its uniqueness.

#### Interfaces

- `Job`: `Kind() string`.
- `Dispatcher`: `Dispatch(ctx, job) error`.
- `Tries`, `Backoff`, `Timeout`, `ShouldBeUnique`, `Failer`, `OnQueue`: the optional job methods.

#### Workers

- `Handle(ctx, job, attempt, maxAttempts)`: runs a job's handler as a worker would.
- `AfterProcessed(listener)`: runs `listener` after every successful job.
- `Jobs()`: a zero value of every registered job type, sorted by kind.

#### Connections

- `River.Dispatch(ctx, job)`: queues the job, on the context's transaction when there is one.
- `River.Work(ctx, options)`: processes jobs until `ctx` is canceled or `MaxTime` passes.
- `RiverConfig`: `Pool`, `Workers`, `Tries`, `RetryAfter`, `PollOnly`, `Logger`.
- `WorkOptions`: `Queues`, `Concurrency`, `Sleep`, `Timeout`, `MaxTime`.
- `Sync.Dispatch(ctx, job)`: runs the job now; `Sync` has one field, `Workers`.
- `Fake.Dispatch(ctx, job)`: records the job. `Fake.All()`: every recorded job, in dispatch order.
