# Directory Structure

- [Introduction](#introduction)
- [The Root Directory](#the-root-directory)
    - [The `app` Directory](#the-app-directory)
    - [The `bootstrap` Directory](#the-bootstrap-directory)
    - [The `cmd` Directory](#the-cmd-directory)
    - [The `config` Directory](#the-config-directory)
    - [The `database` Directory](#the-database-directory)
    - [The `public` Directory](#the-public-directory)
    - [The `resources` Directory](#the-resources-directory)
    - [The `routes` Directory](#the-routes-directory)
    - [The `tests` Directory](#the-tests-directory)
- [The App Directory](#the-app-directory-1)
    - [The `actions` Directory](#the-actions-directory)
    - [The `ai` Directory](#the-ai-directory)
    - [The `console` Directory](#the-console-directory)
    - [The `data` Directory](#the-data-directory)
    - [The `enums` Directory](#the-enums-directory)
    - [The `events` Directory](#the-events-directory)
    - [The `exceptions` Directory](#the-exceptions-directory)
    - [The `http` Directory](#the-http-directory)
    - [The `jobs` Directory](#the-jobs-directory)
    - [The `listeners` Directory](#the-listeners-directory)
    - [The `mail` Directory](#the-mail-directory)
    - [The `models` Directory](#the-models-directory)
    - [The `policies` Directory](#the-policies-directory)
    - [The `providers` Directory](#the-providers-directory)
    - [The `services` Directory](#the-services-directory)
    - [Domain Packages](#domain-packages)
- [Inside reef](#inside-reef)

## Introduction

The structure below is the one reef's packages are designed around, shown for `flights`, the example flight-booking application used throughout these docs. It gives a large application the same starting point as a small one, and a Laravel developer will recognize almost every directory. Go itself imposes almost nothing (a package is a directory), so you are free to organize your application however you like; the one rule reef does impose is that the application imports reef and never the other way round.

Directory names are lowercase, because they are Go package names: Laravel's `app/Http/Controllers` is `app/http/controllers` here, and its package is `controllers`.

## The Root Directory

The root of the application is the directory holding its `go.mod`. In these docs' examples the module is `example.com/flights`.

### The `app` Directory

The `app` directory contains the core code of your application. We'll explore it in more detail [below](#the-app-directory-1); almost every package you write lives here. `app/app.go` holds the container, the `App` struct of long-lived services (the database pool, the cache store, the queue, the mailer, the auth guard, the router) and the `BindContext` method that puts the request-wide services on every context. See the [request lifecycle](lifecycle.md).

### The `bootstrap` Directory

The `bootstrap` directory assembles the framework. `bootstrap.Boot` opens the connections, `bootstrap.Build` builds the container, runs the providers, registers the routes and compiles the HTTP kernel, and `bootstrap.Console` builds the console kernel with the application's commands and the framework's (`migrate`, `queue:work`, `schedule:work`, `route:list`, ...). Tests call `Build` too, with a transaction and fakes in place of the real connections.

### The `cmd` Directory

The `cmd` directory holds the application's executables, one directory per binary, as is conventional in Go. `cmd/flights` is the CLI, the equivalent of `artisan` and `public/index.php` in one: `flights serve` serves HTTP, `flights queue:work` works the queue, `flights list` lists everything. `cmd/codegen` regenerates the models from the schema (`go generate ./app/models` runs it).

### The `config` Directory

The `config` directory is the `config` package: the typed `Config` struct and the `Load` function that reads it from the environment. Each file covers one area (`database.go`, `services.go`, `ai.go`, ...), much like Laravel's `config/*.php` files. See [configuration](configuration.md).

### The `database` Directory

The `database` directory contains your migrations, model factories and the model spec:

- `database/migrations` holds the SQL migrations, `NNNNN_name.sql`, embedded in the binary by `migrations.go`. See [migrations](migrations.md).
- `database/models.json` lists the tables that become models, with their casts, timestamps and route keys. See [models](models.md).
- `database/factories` holds a factory per model for tests. See [factories](factories.md).

### The `public` Directory

The `public` directory holds static files the application serves itself, embedded in the binary (`public.Files`). An application might serve the images its emails link to from `public/assets`. There is no `index.php`: the entry point is [`cmd/flights`](#the-cmd-directory).

### The `resources` Directory

The `resources` directory contains your [views](mail.md). `resources/views` holds the email templates, rendered with `html/template`.

### The `routes` Directory

The `routes` directory contains all of the route definitions for your application, one file per area. Route files only name routes, groups and middleware; the controllers and middleware they register arrive already built from the providers, in a `routes.Deps` struct:

- `web.go` registers the routes outside `/api` (the health check, broadcasting authorization, and the auth endpoints that `auth.go` fills in), and `api.go` the `/api` routes, which `flights.go`, `bookings.go`, `account.go` and `airports.go` fill in.
- `middleware.go` declares the `routes.Middleware` struct: the middleware groups (`Web`, `API`) and the aliases (`Auth`, `Guest`, `Throttle`, ...) route files name.
- `channels.go` holds the private broadcast channels' authorization rules.
- `console.go` holds the application's [schedule](scheduling.md).

See [routing](routing.md).

### The `tests` Directory

The `tests` directory contains your automated tests. The `tests` package is one you write: it boots the real application for a test (a `tests.Setup(t)` might return a `*tests.TestCase` with an HTTP client, fakes and a database transaction of its own), and `tests/feature` holds the feature tests that use it. Unit tests live beside the code they test, as `*_test.go` files, as is usual in Go. Run them with `go test ./...`. See [testing](testing.md#a-full-test-case).

## The App Directory

The majority of your application is housed in the `app` directory, under your module path: `example.com/flights/app/...`. Think of `app/http` and `app/console` as two ways into the core of your application: they parse what came in, call the actions and services that hold the logic, and shape what goes out.

Where an app package shares its name with a reef package (`middleware`, `events`, `mail`, `ai`), import the app's under an alias (`appmiddleware`, `appevents`, `appmail`, `appai`, and `aimiddleware` for `app/ai/middleware`) so a bare `middleware.` always means reef's.

### The `actions` Directory

The `actions` directory holds the application's multi-step flows. An action is a struct with one `Handle` method that does the work and hands back a discriminated outcome the controller maps to a response; it never shapes JSON itself. An `actions.BookFlight`, for example, might call two services and return an outcome naming which of them succeeded.

### The `ai` Directory

The `ai` directory holds the application's [AI](ai.md) features, built on reef's `ai` package: `ai/agents` the agents (such as the `Concierge` chat agent), `ai/tools` the tools an agent calls, `ai/prompts` the prompt templates, and `ai/middleware` (imported as `aimiddleware`) the step middleware wrapped around agent runs. The `ai` package itself, imported as `appai`, holds whatever the agents share.

### The `console` Directory

The `console/commands` directory contains your application's console commands. Each command declares its dependencies as exported fields, and the providers build it. See [console](console.md).

### The `data` Directory

The `data` directory holds Data objects: the typed structs the API serves. The ones registered in `app/data/registry` are written out as TypeScript declarations for the frontend by `flights types:generate`. See [TypeScript](typescript.md).

### The `enums` Directory

The `enums` directory holds the application's enumerations, string types with named constants.

### The `events` Directory

The `events` directory houses [event](events.md) types. Events alert other parts of the application that something happened, and a broadcastable event is also sent to its channels.

### The `exceptions` Directory

The `exceptions` directory holds the application's typed errors, the ones the HTTP kernel renders with a status and body of their own: a `CodedError`, for example, might render `{"error": ..., "code": ...}`, such as a 409 `fare_unavailable`. A controller fails with one of these as its error, never with a response that carries a 4xx or 5xx status. See [error handling](errors.md).

### The `http` Directory

The `http` directory contains your controllers, middleware and form requests, plus the clients for the third-party APIs you call:

- `app/http/controllers` holds every controller. An action returns its own response type and an error: `(data.BookingData, error)`, or `(*web.Response, error)` for a shaped answer.
- `app/http/middleware` holds the application's own middleware (imported as `appmiddleware`).
- `app/http/requests` holds the form requests, one per file, validated with `web.Validate[T]`. See [validation](validation.md#form-request-validation).
- `app/http/integrations` holds a package per third-party API: its connector and typed requests, built on the [HTTP client](http-client.md). A request package owns the wire types it sends: the airline API's package imports nothing of the app, and the actions convert the app's Data onto it.

### The `jobs` Directory

The `jobs` directory houses the [queued jobs](queues.md): a typed, JSON-serializable args struct per job, and its handler.

### The `listeners` Directory

The `listeners` directory contains the types that handle your [events](events.md).

### The `mail` Directory

The `mail` directory contains the [mailables](mail.md), one type per email the application sends. Import it as `appmail`.

### The `models` Directory

The `models` directory contains your models. For each table listed in `database/models.json`, `go generate ./app/models` writes `<model>_gen.go`: the struct, its column identifiers (`BookingColumns`, `BookingTableColumns`), its query type with the eager loaders for its relations, the relations themselves, and the row's own `Update`, `Refresh` and `Delete`. Scopes and behavior are hand-written in files beside it (`booking_scopes.go`, `user.go`), and are never overwritten. See [models](models.md).

### The `policies` Directory

The `policies` directory holds the authorization rules for records a user owns, such as a `BookingPolicy`.

### The `providers` Directory

The `providers` directory builds the application's shared services, once, at boot: the third-party clients, the controllers, the route middleware, the commands, the rate limiters, the event listeners and the jobs' dependencies. Nothing else builds a shared service. It plays the part Laravel's service providers play, without a container: each provider is a function that takes the container and returns what it built. See the [request lifecycle](lifecycle.md#focus-on-providers).

### The `services` Directory

The `services` directory holds the long-lived services the providers build and hand to controllers, actions, commands and jobs: a currency converter or a fare calculator, for example.

### Domain Packages

Some concepts deserve a small package named for what they hold rather than for a layer. A flight-booking application might have `pricing` (fares, taxes and fees), `seatmap` and `passwords` (reset tokens). A domain package takes a `context.Context`, never the request, so a job or command can run it; one that carries a request fact also binds it to the context and reads it back through a function such as `locale.From(ctx)`.

## Inside reef

reef itself is a flat set of packages, one per concern, each documented on its own page (see the [package index](README.md#package-index)):

```text
reef/
├── ai/            ai/aitest/
├── auth/
├── broadcasting/  broadcasting/broadcastingtest/
├── cache/
├── clock/         clock/clocktest/
├── codegen/
├── collect/
├── console/
├── cookie/
├── crypt/
├── db/            db/dbcmd/  db/factory/
├── env/
├── events/        events/eventstest/
├── hashing/
├── httpclient/    httpclient/httpclienttest/
├── jsonschema/
├── mail/          mail/mailtest/
├── migration/
├── ordered/
├── panics/
├── phpjson/
├── queue/         queue/queuetest/
├── ratelimit/
├── realtime/
├── schedule/
├── session/
├── support/
├── testkit/
├── typescript/
├── validation/    validation/rule/
└── web/           web/middleware/  web/webcmd/
```

A package's test helpers live in a sibling `xxxtest` package (`mailtest` beside `mail`), so production code never imports testing code. A library's console commands live in a sibling `xxxcmd` package the same way (`dbcmd` beside `db`, `webcmd` beside `web`), so the ORM and the router never link the console.
