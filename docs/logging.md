# Logging

- [Introduction](#introduction)
- [Configuration](#configuration)
    - [The Default Logger](#the-default-logger)
    - [Loggers reef Accepts](#loggers-reef-accepts)
- [Writing Log Messages](#writing-log-messages)
    - [Contextual Information](#contextual-information)
    - [Message Conventions](#message-conventions)
- [What reef Logs](#what-reef-logs)

## Introduction

To help you learn more about what's happening within your application, reef logs through Go's standard structured logger, `log/slog`. reef has no logging package, channels or stacks of its own: where Laravel would have you configure a Monolog channel, you choose an `slog.Handler` (text, JSON, or one of your own that wraps them) and install it as the default logger.

A few reef components take a `*slog.Logger` explicitly; everything else reef logs goes through `slog`'s default logger.

## Configuration

### The Default Logger

You configure logging by installing a default logger when your program starts, before you build anything that logs. For example, to write JSON lines to standard error at the info level and above:

```go
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Boot the application...
}
```

If you never call `slog.SetDefault`, Go's default logger writes text lines through the standard `log` package to standard error, at the info level and above. Many applications run with it.

### Loggers reef Accepts

Three components take a logger of their own. Each falls back to `slog.Default()` when you leave it `nil`, read once, when the component is built: install your default logger before building them.

| Option | Used for |
|---|---|
| `web.KernelConfig.Logger` | unexpected errors in HTTP requests ([error handling](errors.md#reporting-errors)) |
| `schedule.RunnerConfig.Logger` | failed scheduled events and lock errors ([task scheduling](scheduling.md)) |
| `queue.RiverConfig.Logger` | the queue's own logging, handed to River ([queues](queues.md)) |

Passing a logger is how you silence or capture these in a test. A test case might mount routes on a kernel that discards its logs:

```go
kernel, err := web.NewKernel(tc.App.BindContext, web.KernelConfig{
	Router: router,
	Logger: slog.New(slog.DiscardHandler),
})
```

## Writing Log Messages

You write log messages with `slog`'s functions, one per level: `DebugContext`, `InfoContext`, `WarnContext` and `ErrorContext`. Each takes a context, a message, and attributes as alternating keys and values:

```go
slog.WarnContext(ctx, "SyncFlightStatus: connection failed", "flight", flight.Number, "error", err)
```

### Contextual Information

Pass the context you have, so the handler can read anything bound to it. In a controller or a middleware that is the request itself, since a `*web.Context` is a `context.Context`:

```go
status, err := ctl.flightStatus(c, booking.FlightNumber)
if err != nil {
	// The booking still renders without live status: log it, never a 500.
	slog.ErrorContext(c, "bookings: loading flight status failed", "error", err)
	status = nil
}
```

reef doesn't add attributes to your log lines on its own. When several lines share attributes, derive a logger that carries them with `With`:

```go
logger := slog.Default().With("conversation", conversationID)

logger.InfoContext(ctx, "chat: turn started")
logger.InfoContext(ctx, "chat: turn finished", "steps", len(steps))
```

### Message Conventions

reef's own log lines follow three conventions, which you can enforce in your application with golangci-lint's `sloglint` (`kv-only`, `static-msg`, `context: scope`):

- **The message is fixed.** Never format values into it: put them in attributes, so a line can be searched by its message and filtered by its fields.
- **Attributes are key-value pairs**, not `slog.Attr` values mixed in.
- **A function holding a context logs with it**, through the `...Context` variants.

Prefixing the message with the component it comes from (`session: sweeping expired rows`) makes lines easy to find.

> [!NOTE]
> The `log` broadcasting driver is the one deliberate exception: its message names the event and channels, because the broadcast itself is the line's whole purpose.

## What reef Logs

reef logs a failure only when there is no caller left to return it to: one it deliberately absorbs so the work carries on, or one that happens after the response is decided. Its `log` drivers and the realtime server's shutdown log too. Everything else is returned as an error.

| Level | Message | Attributes | When |
|---|---|---|---|
| Error | `unhandled error` | `method`, `path`, `error`, and `stack` for a panic | an action or middleware failed with an unexpected error (one a middleware replaced included), a [stream function](responses.md#errors-in-a-stream) returned one while the client was connected, or one of them, the rendering of the outcome or a `Terminating` callback panicked (the kernel's `Logger`; see [error handling](errors.md#reporting-errors)) |
| Error | `stream cannot flush` | `path` | a stream started on a response writer that can't flush, so its body arrives only at the end (the kernel's `Logger`; see [streamed responses](responses.md#streamed-responses)) |
| Warn | `session: sweeping expired rows` | `error` | the [session](session.md) lottery's sweep failed |
| Warn | `auth: resolving the request's user` | `error` | loading the signed-in user failed where a guest is an acceptable answer ([authentication](authentication.md)) |
| Warn | `cache: put failed` | `key`, `error` | `Remember` or `Flexible` computed a value but couldn't store it ([cache](cache.md)) |
| Warn | `cache: background refresh failed` | `key`, `error` | `Flexible`'s background refresh failed |
| Error | `cache: background refresh panicked` | `key`, `error`, `stack` | `Flexible`'s background refresh panicked; the stale value stays |
| Warn | `cache: releasing lock` | `lock`, `error` | a lock couldn't be released (it expires with its TTL) |
| Error | `queue: job panicked` | `kind`, `error`, `stack` | a job's handler panicked, failing the attempt ([queues](queues.md)) |
| Error | `schedule: event failed` | `event`, `error`, and `stack` for a panic | a scheduled event failed (the runner's `Logger`; see [task scheduling](scheduling.md)) |
| Error | `schedule: overlap lock` | `event`, `error` | an overlapping event's lock couldn't be taken |
| Error | `schedule: leader lock` | `error` | the leader lock couldn't be claimed |
| Info | `mail (log transport)` | `to`, `subject`, `bytes` | a message was "sent" through the `log` transport ([mail](mail.md)) |
| Info | `Broadcasting [event] on channels [...] with payload:` | `payload` | an event was broadcast through the `log` driver ([broadcasting](broadcasting.md)) |
| Error | `realtime: publishing failed` | `event`, `error` | the realtime server's HTTP API couldn't publish an event |
| Error | `realtime: publishing a client event failed` | `event`, `channel`, `error` | a client event couldn't be published |
| Error | `realtime: fan-out subscription ended` | `error` | the subscription to other instances' messages ended |
| Info | `realtime: shutting down` | `connections` | the realtime server is draining |
| Warn | `ai: event listener failed` | `error` | a listener for an agent run's events failed; the run carries on ([AI](ai.md)) |
| Error | `ai: event listener failed` | `error`, `stack` | a listener for an agent run's events panicked; the run carries on |
| Error | `ai: storing a failed turn` | `conversation`, `error`, and `stack` for a panic | a failed chat turn couldn't be stored, or the store panicked |
| Error | `ai: run panicked` | `error`, `stack` | a tool, the agent's instructions, the provider's stream or storing the finished turn panicked during an agent run, failing it ([AI tools](ai-tools.md)) |

Unless noted, these go through `slog`'s default logger.
