# reef

- [Introduction](#introduction)
- [Design Principles](#design-principles)
    - [Laravel's Vocabulary, Go's Mechanism](#laravels-vocabulary-gos-mechanism)
    - [Explicit Wiring](#explicit-wiring)
    - [The Context Carries the Request](#the-context-carries-the-request)
    - [reef Never Imports the App](#reef-never-imports-the-app)
    - [Built to Be Tested](#built-to-be-tested)
- [How to Read These Docs](#how-to-read-these-docs)
- [Documentation](#documentation)
    - [Prologue](#prologue)
    - [Getting Started](#getting-started)
    - [Architecture Concepts](#architecture-concepts)
    - [The Basics](#the-basics)
    - [Digging Deeper](#digging-deeper)
    - [Security](#security)
    - [Database](#database)
    - [Testing](#testing)
- [Package Index](#package-index)

## Introduction

reef is a web application framework for Go with the shape of Laravel. It gives an application the pieces Laravel developers reach for every day: named routes and route groups, middleware, form requests and validation, models generated from your schema with scopes, casts and factories, queued jobs, events, mail, broadcasting over a Pusher-compatible realtime server, a scheduler, a console with `group:name {argument} {--option=}` signatures, an HTTP client for third-party APIs, AI agents with tools and structured output, and a testing layer that runs every test in parallel on a database of its own.

reef is the Go module `github.com/atobeach/reef`. Examples in these docs that import `example.com/flights/...` belong to an illustrative flight-booking application; everything under `github.com/atobeach/reef/...` is the framework.

## Design Principles

### Laravel's Vocabulary, Go's Mechanism

If you know Laravel, you already know where to look: routes live in `routes/`, controllers in `app/http/controllers`, form requests in `app/http/requests`, models in `app/models`, jobs in `app/jobs`, and your application's CLI lists its commands with `list` just like Artisan. Names follow Laravel's wherever reef has the feature: `Name("bookings.show")` and `APIResource` on a route, `ResolveRouteBinding`, `UpdateOrCreate` and `Latest` on a model query, `Remember` and `Flexible` on the cache, `WithoutOverlapping` on a scheduled task, `AssertDatabaseHas` in tests.

The mechanism underneath is plain Go. A controller action is a function of the request answering its own type and an error, `func(c *web.Context) ([]data.AirportListing, error)`, so the compiler checks what it sends; a middleware is a function that calls `next`; a job is a JSON-serializable struct; a model is generated from your schema, so the compiler checks every field you read or write. Errors are values, wrapped with `%w` and compared with `errors.Is` and `errors.As`: a failure travels back up the middleware chain as the error it is, so a middleware can inspect it, and the kernel renders it once, at the edge. Dependencies are passed in rather than resolved from a container, so you can always see where a controller's come from.

### Explicit Wiring

reef has no service container and no facades. Your application builds its shared services once, at boot, and hands them to the code that needs them: a controller or a command declares its dependencies as exported fields and is built with a struct literal. When a reef type has more than one setting it is built with `NewX(XConfig)`, applies its defaults inside, and is never configured after construction; a dependency it can't run without comes first, as a required argument (`auth.NewGuard(provider, config)`, and the bind every kernel takes):

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router:     a.Router,
	Middleware: []web.Middleware{middleware.Cors(cfg.Cors)},
	Debug:      cfg.App.Debug,
})
```

A middleware without settings is a function value (`appmiddleware.NoStore`); one with settings is a constructor that returns one (`auth.RequirePassword(timeout)`).

### The Context Carries the Request

Everything request-scoped rides on the `context.Context`: the database connection (or the test's transaction), the clock, the encrypter, the session, the signed-in user's id. reef's `*web.Context` and `*console.Context` are both a `context.Context`, so you pass them straight to a query or a service:

```go
booking, err := models.Bookings(c).ResolveRouteBinding(c.Param("booking"))
```

The `*web.Context` is also the whole of the request: you read its facts through accessors such as `c.Param`, `c.Header`, `c.Cookie` and `c.Body()`, and an application imports `net/http` only for its status and method constants, cookies, outbound clients and the server that runs the kernel.

Code below the controllers reads these facts from `ctx` (`clock.Now(ctx)`, `db.Conn(ctx)`, `auth.ID(ctx)`), never from package-level state. Request state is typed: there is no string-keyed bag on the request. A fact is bound under a key its package keeps unexported and read back through a function named for it, so the compiler checks its type, and a middleware binds one by deriving a new context from `c.Context()` and handing it to `c.WithContext`. A context taken with `c.Context()` never changes after that, while `c` itself answers for whatever is bound when it is asked, so hand work that outlives the handler `c.Context()` (see [The Request Is a Context](requests.md#the-request-is-a-context)). The [request lifecycle](lifecycle.md#the-requests-context) explains who binds what.

### reef Never Imports the App

reef is its own Go module, which your application adds with `go get` (see [installation](installation.md#adding-reef-to-an-application)). It never imports your application, so anything that names your product, a supplier or a business rule belongs in the app, and anything in reef is reusable by another application. reef keeps its package names (`web`, `middleware`, `events`, `mail`, `ai`); an app package that shares one is imported under an alias (`appmiddleware`, `appevents`, `appmail`, `appai`), and the standard library keeps its own names (`net/http` is `http`).

### Built to Be Tested

reef's services come with fakes and in-memory stores beside them: `mail.Fake`, `queue.Fake`, `broadcasting.Fake`, `cache.NewMemoryStore`, `httpclient.MockClient`, and the clock a test freezes and moves. The `xxxtest` packages (`mailtest`, `queuetest`, `httpclienttest`, `clocktest`, ...) assert on them, and `testkit` leases each parallel test a migrated database of its own. See [testing](testing.md).

## How to Read These Docs

The pages follow Laravel's documentation: each opens with a table of contents and an introduction, then shows how you use the feature, then the details, and ends with a reference list where a package has many functions. Where a Laravel reader would expect something reef deliberately leaves out, the page says so in a sentence and tells you what to do instead.

A few conventions hold throughout:

- reef's `web` package is imported as `web`, and the standard library's `net/http` as `http`.
- `c` is a `*web.Context` in a controller or middleware and a `*console.Context` in a command; `ctx` is any `context.Context`; `a` is the application's `*app.App` container; `tc` is a test's `*tests.TestCase`.
- `go doc github.com/atobeach/reef/<package>` prints the full API of any package; these pages explain how the pieces fit.

## Documentation

### Prologue

- [Introduction](README.md) (this page)

### Getting Started

- [Installation](installation.md)
- [Configuration](configuration.md)
- [Directory Structure](directory-structure.md)

### Architecture Concepts

- [Request Lifecycle](lifecycle.md)

### The Basics

- [Routing](routing.md)
- [Middleware](middleware.md)
- [CSRF Protection](csrf.md)
- [Controllers](controllers.md)
- [HTTP Requests](requests.md)
- [HTTP Responses](responses.md)
- [URL Generation](urls.md)
- [HTTP Session](session.md)
- [Validation](validation.md)
- [Error Handling](errors.md)
- [Logging](logging.md)

### Digging Deeper

- [Console](console.md)
- [Task Scheduling](scheduling.md)
- [Queues](queues.md)
- [Events](events.md)
- [Mail](mail.md)
- [Broadcasting](broadcasting.md)
- [Cache](cache.md)
- [HTTP Client](http-client.md)
- [AI](ai.md)
    - [Agents](ai-agents.md)
    - [Tools](ai-tools.md)
    - [Streaming](ai-streaming.md)
- [JSON Schema](json-schema.md)
- [TypeScript](typescript.md)
- [Helpers](helpers.md)
- [Compatibility](compatibility.md)

### Security

- [Authentication](authentication.md)
- [Hashing](hashing.md)
- [Encryption](encryption.md)
- [Rate Limiting](rate-limiting.md)

### Database

- [Getting Started](database.md)
- [Query Builder](queries.md)
- [Models](models.md)
- [Relationships](relationships.md)
- [Casts](casts.md)
- [Migrations](migrations.md)

### Testing

- [Getting Started](testing.md)
- [HTTP Tests](http-tests.md)
- [Database Testing](database-testing.md)
- [Model Factories](factories.md)
- [Mocking](mocking.md)
- [Console Tests](console-tests.md)

## Package Index

Every reef package, and the page that documents it:

| Package | What it is | Documented in |
| --- | --- | --- |
| `ai` | Agents, tools and structured output over an OpenAI-compatible provider | [AI](ai.md), [Agents](ai-agents.md), [Tools](ai-tools.md), [Streaming](ai-streaming.md) |
| `ai/aitest` | A scripted fake model provider for tests | [Mocking](mocking.md) |
| `auth` | The session guard, the signed-in user, the auth middleware and `Timebox` | [Authentication](authentication.md) |
| `broadcasting` | Broadcasting events to channels, channel authorization | [Broadcasting](broadcasting.md) |
| `broadcasting/broadcastingtest` | Assertions on `broadcasting.Fake` | [Mocking](mocking.md) |
| `cache` | The cache store and the typed `Remember` / `Flexible` helpers, locks | [Cache](cache.md) |
| `clock` | The app clock on the context, the JSON timestamp types and timestamp formats | [Helpers](helpers.md#dates-and-time), [Request Lifecycle](lifecycle.md#the-clock), [Compatibility](compatibility.md#timestamps) |
| `clock/clocktest` | A test's own frozen or sleep-faking clock | [Mocking](mocking.md) |
| `codegen` | Model generation from the migrated schema and `database/models.json` | [Models](models.md), [Relationships](relationships.md), [Casts](casts.md) |
| `collect` | `Map` and `GroupBy` over slices | [Helpers](helpers.md#collections) |
| `console` | Commands, signatures, the console kernel and output | [Console](console.md) |
| `cookie` | The encrypted cookie value format | [HTTP Responses](responses.md#cookies-and-encryption), [Compatibility](compatibility.md#encrypted-cookies) |
| `crypt` | The encrypter behind `APP_KEY` | [Encryption](encryption.md), [Compatibility](compatibility.md#encryption-payloads) |
| `db` | Connections, transactions, the query builder, eager loading | [Database](database.md), [Query Builder](queries.md), [Models](models.md), [Relationships](relationships.md), [Casts](casts.md) |
| `db/dbcmd` | The database's console commands (`model:prune`) | [Models](models.md#pruning-models), [Console](console.md) |
| `db/factory` | Model factories for tests | [Model Factories](factories.md) |
| `env` | Loading `.env` files and reading variables | [Configuration](configuration.md) |
| `events` | The event dispatcher and listeners | [Events](events.md) |
| `events/eventstest` | Assertions on a faked dispatcher | [Mocking](mocking.md) |
| `hashing` | Bcrypt password hashing | [Hashing](hashing.md), [Compatibility](compatibility.md#password-hashes) |
| `httpclient` | Connectors and typed requests for third-party APIs, the mock client | [HTTP Client](http-client.md) |
| `httpclient/httpclienttest` | Assertions on what the mock client was sent | [Mocking](mocking.md) |
| `jsonschema` | A fluent JSON Schema builder | [JSON Schema](json-schema.md) |
| `mail` | Mailables, the mailer and its transports | [Mail](mail.md) |
| `mail/mailtest` | Assertions on `mail.Fake` | [Mocking](mocking.md) |
| `migration` | SQL migrations on goose, adoption of an existing schema | [Migrations](migrations.md), [Compatibility](compatibility.md#migration-adoption) |
| `ordered` | A JSON object that keeps its key order | [Helpers](helpers.md#ordered-json-objects) |
| `panics` | A recovered panic kept as an error with its stack | [Error Handling](errors.md#recovered-panics) |
| `phpjson` | The PHP-compatible JSON dialect | [Compatibility](compatibility.md#the-php-json-dialect) |
| `queue` | Background jobs on River (Postgres) | [Queues](queues.md) |
| `queue/queuetest` | Assertions on `queue.Fake` | [Mocking](mocking.md) |
| `ratelimit` | Fixed-window attempt counters | [Rate Limiting](rate-limiting.md) |
| `realtime` | The Pusher-protocol WebSocket server | [Broadcasting](broadcasting.md) |
| `schedule` | The task scheduler | [Task Scheduling](scheduling.md) |
| `session` | The database-backed session and its typed attribute keys | [HTTP Session](session.md), [Compatibility](compatibility.md#sessions) |
| `support` | String, number, URL, UUID and pointer helpers | [Helpers](helpers.md) |
| `testkit` | Leased test databases, database assertions, response assertions | [Testing](testing.md), [HTTP Tests](http-tests.md), [Database Testing](database-testing.md), [Console Tests](console-tests.md) |
| `typescript` | TypeScript declarations for Go Data structs | [TypeScript](typescript.md) |
| `validation` | Running rule sets over input, error messages | [Validation](validation.md) |
| `validation/rule` | The validation rules | [Validation](validation.md) |
| `web` | Routing, the request context, responses, errors, form requests, the HTTP kernel | [Routing](routing.md), [Middleware](middleware.md), [Controllers](controllers.md), [HTTP Requests](requests.md), [HTTP Responses](responses.md), [URL Generation](urls.md), [Validation](validation.md#form-request-validation), [Error Handling](errors.md) |
| `web/middleware` | Stock middleware: cookies, CORS, CSRF, rate limiters | [Middleware](middleware.md), [CSRF Protection](csrf.md), [Rate Limiting](rate-limiting.md) |
| `web/webcmd` | The router's console commands (`route:list`) | [Routing](routing.md#listing-your-routes), [Console](console.md) |
