# Task Scheduling

- [Introduction](#introduction)
- [Defining Schedules](#defining-schedules)
    - [Scheduling Console Commands](#scheduling-console-commands)
    - [Scheduling Closures](#scheduling-closures)
    - [Scheduling Queued Jobs](#scheduling-queued-jobs)
    - [Schedule Frequency Options](#schedule-frequency-options)
    - [Timezones](#timezones)
    - [Preventing Task Overlaps](#preventing-task-overlaps)
    - [Running Tasks on One Server](#running-tasks-on-one-server)
    - [Background Tasks](#background-tasks)
- [Running the Scheduler](#running-the-scheduler)
    - [Running the Scheduler Locally](#running-the-scheduler-locally)
    - [Listing Scheduled Tasks](#listing-scheduled-tasks)
    - [Registering the Scheduler Commands](#registering-the-scheduler-commands)
- [Task Output](#task-output)
- [Locks](#locks)
- [Testing the Schedule](#testing-the-schedule)
    - [Inspecting Events](#inspecting-events)
    - [Ticking a Runner](#ticking-a-runner)
- [Available Methods](#available-methods)

## Introduction

In the past, you may have written a cron configuration entry for each task you needed to schedule on your server. However, this can quickly become a pain because your task schedule is no longer in source control, and you must SSH into your server to view your existing cron entries or add additional entries.

reef's command scheduler (`github.com/atobeach/reef/schedule`) offers a fresh approach to managing scheduled tasks on your server. The scheduler allows you to fluently and expressively define your command schedule within your application itself. When using the scheduler, only a single long-running process (`schedule:work`) or a single cron entry (`schedule:run`) is needed on your server.

## Defining Schedules

You may define all of your scheduled tasks in one function that receives a `*schedule.Schedule`. A typical application calls it `routes.Schedule`, in `routes/console.go`, and it receives the services its closures need as parameters:

```go
package routes

import (
	"context"

	"example.com/flights/app/services"
	"github.com/atobeach/reef/schedule"
)

// Schedule is the app's recurring work, in definition order.
func Schedule(s *schedule.Schedule, rates *services.ExchangeRates) {
	s.AlwaysOnOneServer()

	s.Command("schedules:refresh").Daily().WithoutOverlapping().RunInBackground()
	s.Command("model:prune", "--model=FlightSearch").Daily()
	s.Command("flights:check-delays").EveryMinute().WithoutOverlapping()
	s.Command("schedules:import", "--source=latest").MonthlyOn(28, "04:30").WithoutOverlapping().RunInBackground()
	s.Command("routes:warm-fares").DailyAt("02:00").WithoutOverlapping().RunInBackground()

	s.Call(func(ctx context.Context) error {
		rates.Warm(ctx)

		return nil
	}).
		Name("exchange-rates:warm").
		Hourly()
}
```

`schedule.New()` builds the empty schedule the function fills in:

```go
s := schedule.New()
routes.Schedule(s, services.ExchangeRates)
```

### Scheduling Console Commands

The `Command` method schedules a [console command](console.md) by name. Its arguments and options follow as separate strings, exactly as the command would receive them from the command line:

```go
s.Command("model:prune", "--model=FlightSearch").Daily()
```

Scheduled commands run in the scheduler's own process, through the console kernel's `Call`, rather than in a child process: they share its database pool and cache connections, and run on its context.

> [!WARNING]
> A scheduled command that isn't registered on the kernel, or whose arguments don't match its signature, fails at every run. You can guard against both with a test that looks up every event's `CommandName()` on the kernel and parses its `Args()`; see [Inspecting Events](#inspecting-events).

### Scheduling Closures

The `Call` method schedules a Go function. The function receives the scheduler's context (with the database connection bound, as a command's is), and the error it returns marks the run as failed:

```go
s.Call(func(ctx context.Context) error {
	rates.Warm(ctx)

	return nil
}).
	Name("exchange-rates:warm").
	Hourly()
```

Always give a closure a `Name`. The name is how `schedule:list` and the logs refer to the event, and it keys the event's overlap lock: unnamed closures all read "Closure", and those sharing a frequency would share one lock.

### Scheduling Queued Jobs

There is no separate method for scheduling a [queued job](queues.md): schedule a closure that dispatches it. The scheduler then only pays for the insert, and a worker runs the job:

```go
s.Call(func(ctx context.Context) error {
	return dispatcher.Dispatch(ctx, jobs.UpdateAirportWeather{AirportID: heathrowID})
}).
	Name("weather:lhr").
	Daily()
```

Here `dispatcher` is the application's `queue.Dispatcher`, which your schedule function receives as a parameter, like `rates` above. For work spread over many rows, schedule a command that dispatches one job per row instead, as the `schedules:refresh` command on the [queues](queues.md#dispatching-jobs) page does.

### Schedule Frequency Options

An event without a frequency runs every minute. The following frequencies are available:

| Method | Description |
|---|---|
| `Cron("* * * * *")` | Run the task on a custom cron schedule. |
| `EveryMinute()` | Run the task every minute. |
| `Hourly()` | Run the task every hour, on the hour. |
| `Daily()` | Run the task every day at midnight. |
| `DailyAt("13:00")` | Run the task every day at 13:00. |
| `Monthly()` | Run the task on the first day of every month at 00:00. |
| `MonthlyOn(4, "15:00")` | Run the task every month on the 4th at 15:00. |

For any other cadence, pass a cron expression to `Cron`:

```go
// Every five minutes...
s.Command("bookings:scrub-passenger-data").Cron("*/5 * * * *")

// Weekdays at 08:30...
s.Command("fares:sync").Cron("30 8 * * 1-5")
```

`Cron` takes the five standard fields (minute, hour, day of month, month, day of week), each made of `*`, numbers, ranges (`1-5`), lists (`1,15`) and steps (`*/5`, `0-30/10`, or `5/15`, which runs from 5 to the field's maximum). Day of week accepts both `0` and `7` for Sunday, and when both day of month and day of week are restricted, a minute matching either is due, as in cron. Month and day names (`JAN`, `MON`) and macros (`@daily`) aren't supported.

> [!WARNING]
> An invalid cron expression, or a `DailyAt`/`MonthlyOn` time that isn't `HH:MM`, panics when the schedule is defined, so a typo fails at boot rather than silently never running.

### Timezones

The scheduler runs on UTC, always: every frequency, including `DailyAt` and `MonthlyOn` times, is read in UTC, and there is no per-event timezone. Convert local times to UTC when you define the event.

### Preventing Task Overlaps

By default, scheduled tasks will be run even if the previous instance of the task is still running. To prevent this, you may use the `WithoutOverlapping` method:

```go
s.Command("flights:check-delays").EveryMinute().WithoutOverlapping()
```

In this example, `flights:check-delays` runs every minute unless it is still running from the previous minute. The run is skipped, on any server, while another holds the event's lock, and the runner prints `Skipping [flights:check-delays], as command already running on one server`.

The lock is released as soon as the run finishes. If the process holding it dies mid-run, the lock lapses after 24 hours (`schedule.DefaultOverlapExpiry`). You may specify a shorter expiry instead:

```go
s.Command("fares:sync").Hourly().WithoutOverlapping(90 * time.Minute)
```

The locks live in the scheduler's [locker](#locks), which in production is the shared cache store. If the locker fails to answer, the run is skipped and the error is logged.

### Running Tasks on One Server

> [!WARNING]
> To utilize this feature, the scheduler's locker must be a store every server shares, such as the Valkey cache store.

If your application's scheduler runs on several servers, every server would run every task. `AlwaysOnOneServer` makes the whole schedule run on one server only. Unlike Laravel's per-event `onOneServer`, it covers every event, including those defined after the call:

```go
// The API, queue and scheduler share one image: the per-minute leader key
// in Valkey stops duplicate runs on scale-out.
s.AlwaysOnOneServer()
```

Every minute, each scheduler tries to claim that minute's leader key (`framework/schedule-leader-202610221430`, kept for an hour and never released). The one that claims it runs that minute's events; the others skip them. Leadership is claimed afresh every minute, so if the leader dies, another scheduler leads the next minute, and a dead leader costs at most the minute it had claimed.

If the cache can't be reached, `schedule:work` treats the minute as leaderless and skips its one-server events rather than risk running them twice, and `schedule:run` fails.

### Background Tasks

By default, events due in the same minute run one after another, in the order they were defined. If you have a long-running task, this may cause the following tasks to start much later than anticipated. `RunInBackground` lets the scheduler start the next due event without waiting for this one:

```go
s.Command("routes:warm-fares").DailyAt("02:00").WithoutOverlapping().RunInBackground()
```

A background event runs in a goroutine of the scheduler's process. `schedule:work` waits for it before it exits, and so does `schedule:run`.

reef's scheduler has no shell commands (`exec`), no maintenance-mode handling, no schedule groups and no task hooks (`before`, `onFailure`, `pingOnSuccess`): schedule a command or closure, and do whatever should follow a run inside it.

## Running the Scheduler

Now that we have learned how to define scheduled tasks, let's discuss how to actually run them on our server. The scheduler comes with two commands to run it.

`schedule:work` is a long-running process, and is what you run in production, kept alive by a process monitor such as systemd or Supervisor (for example `flights schedule:work`). It ticks at the top of every minute until it receives `SIGINT`/`SIGTERM`, then waits for running events to finish before it exits:

```shell
flights schedule:work
```

Each minute's tick runs in a goroutine of its own, so a slow minute never delays the next one.

`schedule:run` evaluates the schedule once, for the current minute, and exits when every event it started has finished. It is the command for a system cron:

```shell
* * * * * cd /path-to-your-project && flights schedule:run >> /dev/null 2>&1
```

Like `schedule:work`, `schedule:run` claims the minute's leader key first, so two servers whose cron fires in the same minute don't both run the [one-server](#running-tasks-on-one-server) events. The server that finds the key taken says so (`Another server is running this minute's schedule; only non-onOneServer events run here.`) and runs only the events that aren't one-server, which under `AlwaysOnOneServer` is none of them. When nothing was due, it prints `No scheduled commands are ready to run.`

### Running the Scheduler Locally

Typically, you would not add a scheduler cron entry to your local development machine. Instead, you may run `schedule:work` in a terminal, which will run the scheduler until you terminate it:

```shell
go run ./cmd/flights schedule:work
```

### Listing Scheduled Tasks

`schedule:list` prints every event with its cron expression, its summary, when it is next due, and its flags:

```shell
flights schedule:list
```

```text
  0 0 * * *      schedules:refresh                 Next Due: 2026-10-23 00:00:00  [one server, no overlap, background]
  0 0 * * *      model:prune --model=FlightSearch  Next Due: 2026-10-23 00:00:00  [one server]
  * * * * *      flights:check-delays              Next Due: 2026-10-22 14:31:00  [one server, no overlap]
  30 4 28 * *    schedules:import --source=latest  Next Due: 2026-10-28 04:30:00  [one server, no overlap, background]
  0 2 * * *      routes:warm-fares                 Next Due: 2026-10-23 02:00:00  [one server, no overlap, background]
  0 * * * *      exchange-rates:warm               Next Due: 2026-10-22 15:00:00  [one server]
```

### Registering the Scheduler Commands

`schedule.Commands` returns `schedule:run`, `schedule:work` and `schedule:list` for you to [register](console.md#registering-commands) on your console kernel. Its second argument builds a `*schedule.Runner` when `schedule:run` or `schedule:work` starts, so the runner can use the application's kernel and cache:

```go
func scheduleCommands(application *Application, k *console.Kernel) []console.Command {
	s := Schedule(application.Services)

	return schedule.Commands(s, func(*console.Context) *schedule.Runner {
		var locker schedule.Locker = schedule.NewMemoryLocker()
		if application.App.Cache != nil {
			locker = schedule.CacheLocker{Store: application.App.Cache}
		}

		return schedule.NewRunner(schedule.RunnerConfig{
			Schedule: s,
			Call: func(ctx context.Context, command string, args []string, out io.Writer) int {
				return k.Call(ctx, command, args, console.NewOutput(out))
			},
			Locker: locker,
			Out:    os.Stdout,
		})
	})
}
```

`schedule.NewRunner` takes a `schedule.RunnerConfig`:

| Field | Purpose |
|---|---|
| `Schedule` | The events to run. |
| `Call` | Runs a scheduled command and returns its exit code: the console kernel's `Call`, writing to `out`. |
| `Locker` | Provides the [leader and overlap locks](#locks). `schedule:run` and `schedule:work` always need one. |
| `Out` | Receives the runner's progress lines; `nil` discards them. |
| `Logger` | Receives failed runs; defaults to `slog.Default()`. See [logging](logging.md#what-reef-logs) for the lines it writes. |

## Task Output

`schedule:work` opens with `Running scheduled tasks every minute.`, and the runner prints a line when each event starts and another when it finishes, with its run time, to `RunnerConfig.Out`:

```text
  2026-10-22 02:00:00 Running [routes:warm-fares]
  2026-10-22 02:00:41 [routes:warm-fares] DONE (41.27s)
```

A scheduled command's own output is captured rather than printed. When the command exits with a non-zero code, the runner prints `FAIL` and logs `schedule: event failed` through `RunnerConfig.Logger`, with the event's summary and an error carrying the exit code and everything the command printed. A closure that returns an error is logged the same way, and an event that panics is recovered and logged as a failure (`panic: <value>`, with the panic's `stack`): one failing event never stops the scheduler.

## Locks

The scheduler's locks come from a `schedule.Locker`, an interface with a single method:

```go
TryLock(ctx context.Context, name string, ttl time.Duration) (release func(), ok bool, err error)
```

`TryLock` takes the named lock for at most `ttl` if it is free. `ok` is `false` when someone else holds it, and `release` frees it again, but only while this caller still owns it.

`schedule.CacheLocker` implements it over a [cache](cache.md) store's owner-tagged locks (`cache.NewLock`), so no scheduler needs a database session; production may reach Postgres through a transaction pooler. A crashed holder's lock lapses with its TTL. Releasing twice is harmless, and a release still goes through after the run's context has been canceled (a `SIGTERM` mid-run), rather than leaving the lock to sit out its TTL:

```go
locker := schedule.CacheLocker{Store: cache.NewRedisStore(valkey, "flights:cache:")}
```

`schedule.NewMemoryLocker()` is a `CacheLocker` over an in-process memory store, for tests and single-process use. Its locks only exclude events within the same process.

Two kinds of key are locked:

- the leader key for a minute, `schedule.LeaderKey(tick)`: `framework/schedule-leader-` followed by the minute in UTC, as `YYYYMMDDHHMM`;
- an event's overlap key, `event.MutexName()`: `framework/schedule-` followed by the SHA-1 of the event's cron expression and summary, so the same task on another server contends for the same key.

## Testing the Schedule

### Inspecting Events

`Schedule.Events()` lists the events in definition order, and each `*schedule.Event` describes itself, which lets a test hold the schedule to what it should be. A test can prove every scheduled command exists and that its arguments parse against its signature. `tests.Setup`, `tc.Services` and `tc.Console` here are helpers of your own [test case](testing.md#a-full-test-case):

```go
func TestEveryScheduledCommandIsRegistered(t *testing.T) {
	tc := tests.Setup(t)

	for _, event := range bootstrap.Schedule(tc.Services).Events() {
		name := event.CommandName()
		if name == "" {
			continue
		}

		definition, ok := tc.Console.Definition(name)
		if !ok {
			t.Errorf("%s is scheduled but not registered", name)

			continue
		}

		if _, err := console.ParseInput(definition, event.Args()); err != nil {
			t.Errorf("%s: %v", event.Summary(), err)
		}
	}
}
```

A value can parse and still be wrong, so an entry whose arguments name something may be run once as scheduled. For example, a test may run the nightly prune with `--pretend` added, which fails if its `--model` names no prunable model:

```go
argv := append([]string{event.CommandName()}, event.Args()...)
tc.RunCommand(append(argv, "--pretend")...).AssertSuccessful()
```

`Schedule.DueEvents(now)` lists the events due in the minute containing `now`, and `event.IsDue(now)` and `event.NextDue(now)` answer the same question for one event.

### Ticking a Runner

`Runner.Tick(ctx, now, leader)` runs the events due in `now`'s minute, as if `leader` had (or hadn't) claimed it, and returns how many events it started. It returns once the foreground events have finished; `Runner.Wait()` waits for the background ones. With a fake `Call`, a test can see exactly what a minute runs:

```go
var commands []string
call := func(_ context.Context, command string, _ []string, _ io.Writer) int {
	commands = append(commands, command)

	return 0
}

var out strings.Builder
runner := schedule.NewRunner(schedule.RunnerConfig{Schedule: s, Call: call, Locker: schedule.NewMemoryLocker(), Out: &out})

runner.Tick(tc.Ctx, time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC), true)
runner.Wait()

if !strings.Contains(out.String(), "[exchange-rates:warm] DONE") {
	t.Fatalf("output:\n%s", out.String())
}
```

`RunOnce(ctx)` and `Work(ctx)` are what `schedule:run` and `schedule:work` call. Both read the time from the [clock on `ctx`](helpers.md), so a test's frozen clock decides which events are due.

## Available Methods

#### Package

- `schedule.New()`: an empty schedule.
- `schedule.Commands(s, runner)`: the `schedule:run`, `schedule:work` and `schedule:list` commands.
- `schedule.NewRunner(config)`: a runner over a `RunnerConfig`, its `Logger` defaulted.
- `schedule.NewMemoryLocker()`: a `CacheLocker` over an in-process store.
- `schedule.LeaderKey(tick)`: the leader lock's name for the minute containing `tick`.
- `schedule.DefaultOverlapExpiry`: `WithoutOverlapping`'s default lock expiry, 24 hours.

#### Schedule

- `Command(name, args...)`: schedules a console command.
- `Call(fn)`: schedules a closure.
- `AlwaysOnOneServer()`: runs every event on the minute's leader only.
- `Events()`: the events, in definition order.
- `DueEvents(now)`: the events due in `now`'s minute.

#### Event

- Frequencies: `Cron(expression)`, `EveryMinute()`, `Hourly()`, `Daily()`, `DailyAt(clock)`, `Monthly()`, `MonthlyOn(day, clock)`.
- Constraints: `Name(name)`, `WithoutOverlapping(expiresAt...)`, `RunInBackground()`.
- `Expression()`: the cron expression.
- `CommandName()`: the scheduled command's name, `""` for a closure.
- `Args()`: the scheduled command's arguments after its name, a copy the caller may append to.
- `Summary()`: the name, the command line, or `"Closure"`.
- `MutexName()`: the overlap lock's key.
- `OverlapExpiry()`: the overlap lock's TTL.
- `IsDue(now)`, `NextDue(now)`: when the event runs.
- `IsOnOneServer()`, `PreventsOverlaps()`, `RunsInBackground()`: its flags.

#### Runner

- `Tick(ctx, now, leader)`: runs one minute's due events, returning how many started.
- `Wait()`: blocks until every background event has finished.
- `RunOnce(ctx)`: `schedule:run`.
- `Work(ctx)`: `schedule:work`.

#### Locks

- `Locker`: the `TryLock(ctx, name, ttl)` interface.
- `CacheLocker`: a `Locker` over its `Store` field, a `cache.Store`.
