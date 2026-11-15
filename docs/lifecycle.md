# Request Lifecycle

- [Introduction](#introduction)
- [Lifecycle Overview](#lifecycle-overview)
    - [First Steps](#first-steps)
    - [Building the Application](#building-the-application)
    - [HTTP / Console Kernels](#http--console-kernels)
    - [Routing](#routing)
    - [Finishing Up](#finishing-up)
- [Commands, Jobs and Scheduled Tasks](#commands-jobs-and-scheduled-tasks)
    - [Console Commands](#console-commands)
    - [Queued Jobs](#queued-jobs)
    - [Scheduled Tasks](#scheduled-tasks)
- [The Request's Context](#the-requests-context)
    - [What the Kernels Bind](#what-the-kernels-bind)
    - [Why Services Ride the Context](#why-services-ride-the-context)
    - [What Middleware Binds](#what-middleware-binds)
    - [The Clock](#the-clock)
- [Focus on Providers](#focus-on-providers)

## Introduction

When using any tool in the "real world", you feel more confident if you understand how that tool works. Application development is no different. When you understand how your framework handles a request, everything feels less "magical", and you will know where to look when something surprises you.

This page gives you a high-level overview of how a reef application starts, how an HTTP request travels through it, and how a console command, a queued job and a scheduled task do the same. It follows `flights`, the example flight-booking application used throughout these docs, from its entry point to the bytes it writes. If you don't understand every term right away, don't lose heart: get a basic grasp of what is going on, and your knowledge will grow as you explore the rest of the documentation.

## Lifecycle Overview

### First Steps

The entry point for everything a reef application does is its `main` package: `cmd/flights/main.go` in these docs' examples. There is no web server configuration that routes requests to a script; the binary is the server, and serving HTTP is one of its commands (`flights serve`), alongside `flights queue:work`, `flights schedule:work`, `flights migrate` and the rest.

`main` does very little itself:

1. `env.Load(".")` fills the process environment from the nearest `.env` (see [configuration](configuration.md)).
2. `config.Load()` reads the typed configuration from the environment, and fails if a setting can't be used.
3. `signal.NotifyContext` gives the command a context that SIGINT and SIGTERM cancel, so `serve` drains and workers stop gracefully on deploy.
4. `bootstrap.Boot` opens the Postgres pool and the Valkey client and builds the application. Both connect lazily, so `flights list` works without either; only `serve` pings them at boot, to fail there rather than on the first request.
5. `bootstrap.Console` builds the console kernel, and `main` hands it the command line.

### Building the Application

`bootstrap.Build` assembles the application from its configuration and its connections. Tests call the same function with a transaction and fakes in place of the real connections, so a test boots exactly what production runs:

```go
// Build assembles the app and its HTTP kernel on the given connections.
func Build(cfg config.Config, conns Connections) (*Application, error) {
	signer := pusher(cfg.Broadcasting)

	a, err := newApp(cfg, conns, signer)
	if err != nil {
		return nil, err
	}

	if err := registerBackground(a, cfg, conns); err != nil {
		return nil, err
	}

	registerAI(a, cfg)

	services := providers.NewServices(a)
	providers.RegisterRateLimiters(a.Limiters, a.Auth)
	providers.RegisterEventListeners(a, services)
	providers.RegisterJobs(a, services)

	deps := providers.Routes(a, services, signer)
	routes.Web(a.Router, deps)
	routes.API(a.Router.Prefix("/api").Middleware(deps.Middleware.API...), deps)

	kernel, err := newKernel(a, cfg)
	if err != nil {
		return nil, err
	}

	return &Application{App: a, Services: services, Middleware: deps.Middleware, Kernel: kernel}, nil
}
```

In order, it:

1. builds the container, `*app.App`: the encrypter from `APP_KEY`, the broadcaster, the event dispatcher, the password hasher, the auth guard, the rate limiters, the router and the airline API connector;
2. wires the queue's workers and connection and the mailer, then the AI manager;
3. runs the [providers](#focus-on-providers), which build every shared service, register the rate limiters, event listeners and job handlers, and build the controllers and route middleware;
4. registers the routes from the route files;
5. compiles the HTTP kernel.

Building does no I/O: every connection is dialed on first use, and every closure it registers runs later, on a request's own context.

### HTTP / Console Kernels

There are two kernels, and every unit of work enters the application through one of them.

The **HTTP kernel**, `*web.Kernel`, is a `net/http` handler. `web.NewKernel` compiles the routes into a `net/http` `ServeMux` and wraps it in the global middleware; it fails at boot if two routes share a method and path or a name. Its first argument binds the application's services to every request, and its configuration is where the application decides how requests are handled as a whole:

```go
web.NewKernel(a.BindContext, web.KernelConfig{
	Router: a.Router,
	// The client IP is pinned before anything reads it, and CORS answers
	// preflights before routing.
	Middleware: []web.Middleware{
		appmiddleware.ResolveClientIP(cfg.Services.LoadBalancerIPs),
		appmiddleware.ShareRequestContext,
		middleware.Cors(cfg.Cors),
		appmiddleware.SecurityHeaders(cfg.App.IsProduction()),
	},
	TrustedProxies: web.ParseTrustedProxies(cfg.TrustedProxies),
	Logger:         slog.Default(),
	Debug:          cfg.App.Debug,
	Report: func(c *web.Context, err error) {
		a.ErrorTracker.Capture(c, err, map[string]any{"path": c.Path()})
	},
})
```

The global middleware runs on every request, before routing, so a CORS preflight or a 404 gets the same headers a real route does. `TrustedProxies` names the peers whose `X-Forwarded-*` headers are believed. `a.BindContext` puts the application's services on every request's context. `Report` is told of every unexpected error, after it is logged; an application might send it to an error tracker (see [error handling](errors.md)).

The **console kernel**, `*console.Kernel`, is the registry of commands the CLI dispatches to. It is built with the same bind first, `console.NewKernel(a.BindContext, config)`, and a `console.KernelConfig` holding the name `list` prints and a `Report` function for the errors commands report and the panics they raise. `flights serve` is itself a command on this kernel: it starts a `net/http` server whose handler is the HTTP kernel.

Think of each kernel as a big black box that represents your entire application: feed the HTTP kernel requests and it returns responses; feed the console kernel an argument list and it returns an exit code.

### Routing

When a request arrives, the HTTP kernel:

1. calls its bind function (`a.BindContext`) on the request's context, so the database connection, the clock and the encrypter are available to everything that follows;
2. drops a trailing slash from the path (`/api/airports/` is `/api/airports`), keeping any escaped character before it as it was sent;
3. wraps the request in a `*web.Context` and runs the global middleware, outermost first;
4. matches the route. A miss is answered with a JSON 404 (`{"message": "The route api/nope could not be found."}`), and so is the bare prefix of a wildcard route (`/assets` when only `/assets/{path...}` exists). A path that exists for other methods gets a JSON 405 naming the methods it supports, in its message and its `Allow` header. A `HEAD` request is answered by the `GET` route, and a path that isn't clean (`//`, `/../`) is redirected to its clean form;
5. runs the route's middleware, the groups' middleware first (outermost group first) and then the route's own;
6. calls the controller action.

Middleware filters and examines the request on its way in. An `/api` group, for example, might decrypt the cookies, start the session and resolve the signed-in user, while an `Auth` alias answers a visitor who isn't signed in with a 401 before the controller ever runs. Any middleware may return early, with a value or an error, instead of calling `next`. You can read more in the [middleware documentation](middleware.md).

### Finishing Up

A controller action returns the value of the type it declares and an error, and they travel back out through the route's middleware and the global middleware as they are: each middleware's `next` returns them, so a middleware sees a failure further in as the error it is. A middleware that decorates whatever comes back takes it through `web.ToResponse`: the cookies middleware encrypts and attaches the cookies queued during the request, the session middleware saves the session, the security headers middleware adds its headers, on a success and a failure alike.

A panic in a controller or a middleware is recovered into an error, so one bad handler never takes the server down, and the middleware outside it see that error.

Once the last middleware has returned, the kernel turns the outcome into a response, once:

- a `*web.Response` is sent as it is (HTML only ever goes out this way, as `web.HTML`);
- any other value, a `string` included, is encoded as JSON, at once, so an encoding failure becomes a 500 rather than a truncated body;
- an outcome of nothing (`nil, nil`, or a nil `*web.Response`) is a bug, logged, passed to `Report` and answered with a 500;
- an error is rendered: one that knows its HTTP status (a 404, a 422, `web.Abort`) as its own JSON body and status; anything else, a recovered panic included, is logged, passed to `Report`, and answered with a 500 whose message is `Server Error` unless debug mode is on. The headers and cookies the middleware put on the response beside it go out with it.

A panic while rendering (in an error's `Error` or `HTTPBody`, or a value's `MarshalJSON`) is recovered the same way and answered with a reported 500.

Finally the kernel writes the status, the headers and cookies, and the body (or streams it, for a streamed response), and then the request's `Terminating` callbacks run, last registered first ([terminable middleware](middleware.md#terminable-middleware)). The function of a streamed response and the `Terminating` callbacks run under the same guard as a handler: a panic there is logged with its stack and reported, and a stream is cut off, since its status has already gone out. We've now completed our journey through the entire request lifecycle!

## Commands, Jobs and Scheduled Tasks

### Console Commands

`console.Kernel.Run` handles a command line the way Artisan does: no arguments, `list` or `--help` prints the command list; `help <command>` or `<command> --help` prints one command's usage; anything else is a command name and its arguments. For a command, the kernel:

1. looks the command up, suggesting near names when it doesn't exist;
2. parses the arguments against the command's signature, printing the error and the usage when they don't fit;
3. calls its bind function on the context, just as the HTTP kernel does for a request;
4. calls the command's `Handle` with a `*console.Context`, which is a `context.Context` carrying the parsed input and the output.

`Handle` returning `nil` exits 0; any other error is printed and exits 1, and `console.Exit(code)` chooses another code. A panic is reported with its stack, printed and fails the command. See [console](console.md).

### Queued Jobs

A queued job runs on a worker (`flights queue:work`), or inline with the `sync` connection. Either way it runs through the `queue.Workers` registry, which calls its own bind function on the job's context before the handler, applies the job's timeout, and turns a panic into a failed attempt. A typical application's workers bind the same services as a request, except that a job run inline keeps its dispatcher's connection, so it sees rows the dispatcher hasn't committed yet:

```go
a.Workers = queue.NewWorkers(func(ctx context.Context) context.Context {
	// A sync job keeps its dispatcher's connection, so it sees rows
	// that transaction hasn't committed yet.
	if _, err := db.Conn(ctx); err == nil {
		return ctx
	}

	return a.BindContext(ctx)
})
```

See [queues](queues.md).

### Scheduled Tasks

`flights schedule:work` runs the [scheduler](scheduling.md) in a loop. When a scheduled command is due, the scheduler runs it through the console kernel's `Call`, so a scheduled command goes through exactly the lifecycle above: its arguments are parsed, its context is bound, and its exit code decides whether the run failed.

## The Request's Context

Everything a unit of work needs, beyond its own arguments, rides on its `context.Context`. reef's `*web.Context` and `*console.Context` both are one, so you pass them straight to a query, a service or a job:

```go
func (ctl *AirportController) Show(c *web.Context) (data.AirportData, error) {
	code := c.Param("code")
	key := fmt.Sprintf("airports:%s", code)

	return cache.Remember(c, ctl.Cache, key, airportCacheTTL, func(ctx context.Context) (data.AirportData, error) {
		return ctl.buildAirport(ctx, code)
	})
}
```

The controller passes `c` to the cache, and the cache hands the builder a `ctx` that carries the same database connection, clock and encrypter, so `buildAirport` can run its queries with `models.Airports(ctx)` without being told where the database is.

This is what makes reef safe to run concurrently and to test in parallel: nothing request-scoped is stored in a package variable or on the container, and a test swaps a fact by binding another value.

### What the Kernels Bind

A typical application's `BindContext` binds three things, and both kernels and the queue workers call it. Each of them takes the bind as its first, required argument (`web.NewKernel(a.BindContext, config)`, `console.NewKernel(a.BindContext, config)`, `queue.NewWorkers(bind)`), so a runner can't be built without stating what it binds; `nil` binds nothing, which only reef's own tests want:

```go
// BindContext puts the app's connection, clock and encrypter on ctx: every
// request, command and queued job runs on them.
func (a *App) BindContext(ctx context.Context) context.Context {
	return crypt.With(clock.With(db.WithConn(ctx, a.DB), a.Clock), a.Encrypter)
}
```

| Fact | Bound with | Read with |
| --- | --- | --- |
| The database connection (a pool, or a test's transaction) | `db.WithConn` | `db.Conn(ctx)`, or any model query: `models.Users(ctx)` |
| The clock | `clock.With` | `clock.Now(ctx)`, `clock.Sleep(ctx, d)` |
| The encrypter | `crypt.With` | `crypt.FromContext(ctx)`, used by encrypted casts |

See [database](database.md), [the clock](#the-clock) and [encryption](encryption.md).

### Why Services Ride the Context

The connection (or the open transaction), the clock and the encrypter ride the context rather than a handle passed down, on purpose. `db.Transaction` swaps its transaction into the context it hands its callback, so nested actions, event listeners and inline jobs join the transaction with no handle threaded through, and a test's rolled-back transaction covers every query a request runs. A handle passed down instead would still leave the outer one in scope inside a transaction, one habit away from querying outside it, so it would remove no hazard. A missing binding fails loudly (`db.ErrNoConnection`, `crypt.ErrNoEncrypter`), and the required bind argument means it can't be forgotten. Three rules keep it sound:

- Inside a `db.Transaction` callback, query only through the `ctx` it hands you, never the `c` or `ctx` still in scope outside it, and start no background work there.
- Background work inherits the connection its context carries: a `cache.Flexible` refresh shares the request's connection, so in every rolled-back test its goroutine shares the test's transaction.
- Work that outlives the handler (a goroutine, a streamed response) takes `c.Context()`, a snapshot of what is bound so far, rather than `c` itself, whose answers follow every later bind.

### What Middleware Binds

Request-scoped facts are bound by the middleware that establishes them, and read by the code below the controllers through a function named for the fact:

| Fact | Bound by | Read with |
| --- | --- | --- |
| The session | `session.StartSession` | `session.FromContext(ctx)` |
| The signed-in user's id | the guard's `ResolveUser`, `Authenticate` and `RedirectIfAuthenticated`, and `Login`, `SetUser` and `Logout`; `Guard.User` only reads | `auth.ID(ctx)` |
| The visitor's locale (application-defined) | `appmiddleware.ShareRequestContext` | `locale.From(ctx)` |

A middleware binds a fact with `c.WithContext`, deriving the new context from `c.Context()`. An application's own `ShareRequestContext` might bind the visitor's locale this way, reading the request through the `*web.Context`'s own accessors (`c.Cookie`, `c.Header`), as every reef application does; there is no `*http.Request` to reach for:

```go
ctx := c.Context()
// The locale cookie travels unencrypted, so it reads the same ahead of
// the cookies middleware.
tag := c.Cookie("flights_locale")
if tag == "" {
	tag = locale.FromHeader(c.Header("Accept-Language"))
}
c.WithContext(locale.With(ctx, tag))

return next(c)
```

> [!NOTE]
> Below the controllers, read a fact from `ctx` with its function (`auth.ID(ctx)`, `locale.From(ctx)`), never by asserting `ctx` back to a `*web.Context`. A job or a command has no HTTP request, and binds its own facts. A read never binds: asking for a fact never changes what another reader sees.

### The Clock

Every reef application reads the time from the clock on its context:

```go
holdExpiresAt := clock.Now(ctx).Add(15 * time.Minute)
```

`clock.Now` is always in UTC. In production the container's clock is `nil`, which is the wall clock. In a test, the clock is one the test owns: it freezes it, moves it forward, or fakes `clock.Sleep` so a pause is recorded instead of taken, without affecting any other test running in parallel. Read the time from the context everywhere except when measuring elapsed time (a linter rule forbidding `time.Now` keeps it that way), so nothing can bypass the test's clock. The test case your application writes (see [a full test case](testing.md#a-full-test-case)) can expose the clock as methods:

```go
tc := tests.Setup(t)
tc.FreezeTime()
tc.Travel(16 * time.Minute)
```

The query builder reads the same clock. A scope that compares a column with now, such as `q.WherePast(FareHoldTableColumns.ExpiresAt)`, uses the clock on the query's context, so a test that travels past a fare hold's expiry sees the hold expire. The clock also stamps `created_at` and `updated_at` on every write through a model.

The [helpers](helpers.md#dates-and-time) page documents the `clock` package in full, and [mocking](mocking.md) covers `clocktest` and time travel in tests.

## Focus on Providers

Providers are the key to bootstrapping a reef application. The container is created, the providers build the services on it, the routes are registered with what the providers built, and the request is handed to the kernel. It's really that simple!

reef has no service container to bind into, so a provider is simply a function (a typical application keeps them in `app/providers`) that takes the container (and, when it needs them, the other services) and returns or registers what it built:

```go
// Services are the app's shared singletons.
type Services struct {
	Airline           actions.Airline
	ExchangeRates     *services.ExchangeRates
	CurrencyConverter *services.CurrencyConverter
	// ...
}

// NewServices builds the services on the app's connections.
func NewServices(a *app.App) *Services {
	cfg := a.Config.Services
	rates := services.NewExchangeRates(a.HTTP(exchangerates.Connector()), a.Cache, cfg.ExchangeRates.Base, cfg.ExchangeRates.TTL)
	converter := services.NewCurrencyConverter(rates)
	// ...
}
```

A controller or a command declares its dependencies as exported fields, and the provider builds it with a struct literal; nothing builds a service from the container at run time:

```go
&controllers.FlightSearchController{Search: actions.NewSearchFlights(airline), Airports: airports, Currency: converter}
```

An application might split its providers by what they build: `services.go` (the shared services), `routes.go` (the controllers and route dependencies), `middleware.go` (the route middleware groups and aliases), `rate_limiters.go`, `events.go`, `console.go` (the commands), `ai.go` (the AI tool sets) and `auth.go` (the guard's user provider). For a large application, more providers, each building one area, keep the wiring easy to find.
