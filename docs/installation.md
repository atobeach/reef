# Installation

- [Introduction](#introduction)
    - [Why reef?](#why-reef)
- [Adding reef to an Application](#adding-reef-to-an-application)
    - [Requirements](#requirements)
- [Creating an Application](#creating-an-application)
    - [Configuration](#configuration)
    - [The Container](#the-container)
    - [A Controller and a Route](#a-controller-and-a-route)
    - [Bootstrapping the Kernels](#bootstrapping-the-kernels)
    - [The Entry Point](#the-entry-point)
- [Initial Configuration](#initial-configuration)
    - [Environment Based Configuration](#environment-based-configuration)
    - [Databases and Migrations](#databases-and-migrations)
- [Running the Application](#running-the-application)
- [Next Steps](#next-steps)

## Introduction

reef is a web application framework for Go with expressive, Laravel-shaped APIs. It provides the structure and the starting point for an application (routing, middleware, validation, models, queues, mail, broadcasting, AI agents and testing) so you can spend your time on the parts that are yours.

### Why reef?

- **You already know its vocabulary.** Routes have names and groups, requests are validated by form requests, models have scopes, casts and factories, jobs are dispatched to a queue, and the CLI lists its `group:name` commands with `list`. If you have built a Laravel application, the [directory structure](directory-structure.md) will feel like home.
- **The compiler checks your work.** Models are generated from your migrated schema, so a misspelled field is a build error. Form requests, jobs, events and mailables are typed structs.
- **One binary does everything.** The same executable serves HTTP, works the queue, runs the scheduler, migrates the database and runs the realtime server; a deploy only changes its arguments.
- **Testing is built in.** Every feature test runs in parallel on a database leased to it, with fakes for mail, queues, broadcasts and outbound HTTP, and a clock of its own.

## Adding reef to an Application

reef is the Go module `github.com/atobeach/reef`. Add it to your application's module with `go get`:

```shell
go get github.com/atobeach/reef
```

The `go` command records reef and its own dependencies (pgx, go-redis, River, goose, coder/websocket, golang.org/x/crypto and golang.org/x/text) in your `go.mod` and `go.sum`.

### Requirements

reef requires Go 1.27.3 or later. Beyond the Go toolchain, an application built on reef needs:

- **PostgreSQL.** The [query builder](queries.md), [sessions](session.md), [migrations](migrations.md) and the [queue](queues.md) (River) all run on Postgres through pgx.
- **A Redis-compatible store**, such as Valkey, for the [cache](cache.md), [rate limits](rate-limiting.md) and scheduler locks in production. Tests use the in-memory stores instead.

## Creating an Application

reef has no installer or project template: an application is a Go module that imports reef's packages. The smallest useful one has a typed configuration, a container holding its long-lived services, a route file, a bootstrap package that builds the kernels, and an entry point. The examples below build the start of `flights`, the example flight-booking application used throughout these docs, with exactly that layout:

```text
flights/
├── app/
│   ├── app.go
│   └── http/controllers/airport_controller.go
├── bootstrap/
│   ├── app.go
│   └── console.go
├── cmd/flights/main.go
├── config/config.go
├── database/migrations/
│   ├── migrations.go
│   └── 00001_create_airports_table.sql
├── routes/api.go
├── .env
└── go.mod
```

### Configuration

Configuration is a plain struct, read from the environment once at boot with the [`env`](configuration.md) package. Nothing else in the application reads environment variables:

```go
// Package config is the app's typed settings, read once from the
// environment at boot.
package config

import (
	"time"

	"github.com/atobeach/reef/env"
)

// Config is every setting the app reads.
type Config struct {
	App      App
	Database Database
}

// App is the application's identity, keys and listen address.
type App struct {
	Env             string
	Debug           bool
	Key             string
	PreviousKeys    []string
	Addr            string
	ShutdownTimeout time.Duration
}

// Database is the Postgres connection.
type Database struct {
	URL string
}

// IsProduction reports APP_ENV=production.
func (a App) IsProduction() bool {
	return a.Env == "production"
}

// Load reads the config from the environment (call env.Load first).
func Load() Config {
	return Config{
		App: App{
			Env:             env.Get("APP_ENV", "production"),
			Debug:           env.Bool("APP_DEBUG", false),
			Key:             env.Get("APP_KEY", ""),
			PreviousKeys:    env.List("APP_PREVIOUS_KEYS", ""),
			Addr:            env.Get("APP_ADDR", "127.0.0.1:8000"),
			ShutdownTimeout: time.Duration(env.Int("APP_SHUTDOWN_TIMEOUT", 30)) * time.Second,
		},
		Database: Database{
			URL: env.Get("DATABASE_URL", "postgres://postgres@127.0.0.1:5432/flights"),
		},
	}
}
```

### The Container

The container is a struct of the services that live as long as the process: the database pool, the clock, the encrypter, the router, and (as the application grows) the cache, the queue, the mailer and the auth guard. Its `BindContext` method puts the services every unit of work needs onto a context; each kernel takes it as its first argument and calls it for every request, command and queued job:

```go
// Package app holds the application container: the long-lived services
// the providers build everything else from.
package app

import (
	"context"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/crypt"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/web"

	"example.com/flights/config"
)

// App is the container.
type App struct {
	Config config.Config
	DB     db.Querier
	// Clock is the app's time: nil is the wall clock; a test passes one it
	// freezes and moves.
	Clock     *clock.Clock
	Encrypter *crypt.Encrypter
	Router    *web.Router
}

// BindContext puts the app's connection, clock and encrypter on ctx: every
// request, command and queued job runs on them.
func (a *App) BindContext(ctx context.Context) context.Context {
	return crypt.With(clock.With(db.WithConn(ctx, a.DB), a.Clock), a.Encrypter)
}
```

> [!NOTE]
> The container holds nothing request-scoped. The request's facts (its session, its user, its transaction in a test) ride on the request's context, so no state can leak from one request into the next. The [request lifecycle](lifecycle.md#the-requests-context) describes what is bound where.

### A Controller and a Route

A controller action takes the request's `*web.Context` and returns a value and an error. A value is sent as JSON; an error is rendered by the kernel (see [error handling](errors.md)):

```go
// Package controllers holds the HTTP controllers the routes dispatch to.
package controllers

import (
	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/web"
)

// AirportController answers for airports.
type AirportController struct{}

// Airport is what Show answers.
type Airport struct {
	Code string     `json:"code"`
	At   clock.Time `json:"at"`
}

// Show answers the airport code in the path.
func (AirportController) Show(c *web.Context) (Airport, error) {
	return Airport{Code: c.Param("code"), At: clock.Time{Time: clock.Now(c)}}, nil
}
```

Route files only name routes, groups and middleware. Every route gets a unique name, which [URL generation](urls.md) uses:

```go
// Package routes registers every endpoint the app serves.
package routes

import (
	"github.com/atobeach/reef/web"

	"example.com/flights/app/http/controllers"
)

// API registers the /api routes.
func API(r *web.Router) {
	r.Get("/airports/{code}", controllers.AirportController{}.Show).Name("airports.show")
}
```

See [routing](routing.md) and [controllers](controllers.md) for everything a route file and a controller can do.

### Bootstrapping the Kernels

The bootstrap package assembles the application: it builds the container, registers the routes, and compiles the HTTP kernel, which is the `net/http` handler the server runs. `web.NewKernel` fails at boot, not at request time, when two routes share a method and path or a name:

```go
// Package bootstrap assembles the app: the container, the routes and the
// HTTP and console kernels.
package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/crypt"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/web"
	"github.com/atobeach/reef/web/webcmd"

	"example.com/flights/app"
	"example.com/flights/config"
	"example.com/flights/routes"
)

// Build assembles the app and its HTTP kernel on the given connection.
func Build(cfg config.Config, conn db.Querier) (*app.App, *web.Kernel, error) {
	encrypter, err := crypt.New(cfg.App.Key, cfg.App.PreviousKeys...)
	if err != nil {
		return nil, nil, fmt.Errorf("APP_KEY: %w", err)
	}

	a := &app.App{Config: cfg, DB: conn, Encrypter: encrypter, Router: web.NewRouter()}
	routes.API(a.Router.Prefix("/api"))

	kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
		Router: a.Router,
		Logger: slog.Default(),
		Debug:  cfg.App.Debug,
	})
	if err != nil {
		return nil, nil, err
	}

	return a, kernel, nil
}

// Console is the console kernel: the framework's commands plus any extra
// the entry point adds.
func Console(a *app.App, extra ...console.Command) *console.Kernel {
	k := console.NewKernel(a.BindContext, console.KernelConfig{Name: "Flights"})
	k.Register(webcmd.RouteListCommand(a.Router))
	k.Register(migrationCommands(a)...)
	k.Register(extra...)

	return k
}
```

The console kernel is the registry the CLI dispatches to. `webcmd.RouteListCommand` gives you `route:list`; the [migration commands](#databases-and-migrations) are registered below; your own commands are added the same way (see [console](console.md)).

### The Entry Point

The entry point loads `.env`, reads the configuration, opens the database pool, builds the application and hands the command line to the console kernel. Serving HTTP is just another command, so `flights serve`, `flights migrate` and `flights route:list` all run the same binary:

```go
// Command flights is the app's CLI: `flights serve`, `flights list`.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/env"
	"github.com/atobeach/reef/web"

	"example.com/flights/bootstrap"
	"example.com/flights/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if err := env.Load("."); err != nil {
		slog.Error("loading .env", "error", err)

		return console.Failure
	}

	cfg := config.Load()

	// SIGINT/SIGTERM cancel the command's context: serve drains.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// pgxpool dials on first use, so `flights list` works without Postgres.
	poolConfig, err := db.PoolConfig(cfg.Database.URL)
	if err != nil {
		slog.Error("parsing DATABASE_URL", "error", err)

		return console.Failure
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		slog.Error("connecting", "error", err)

		return console.Failure
	}
	defer pool.Close()

	a, kernel, err := bootstrap.Build(cfg, pool)
	if err != nil {
		slog.Error("booting", "error", err)

		return console.Failure
	}

	return bootstrap.Console(a, serveCommand(cfg, kernel)).Run(ctx, args, os.Stdout)
}

// serveCommand runs the HTTP server until the context is cancelled, then
// drains in-flight requests.
func serveCommand(cfg config.Config, kernel *web.Kernel) console.Command {
	return console.Func("serve {--addr= : Listen address}", "Serve the application", func(c *console.Context) error {
		addr := c.Option("addr")
		if addr == "" {
			addr = cfg.App.Addr
		}

		// No ReadTimeout or WriteTimeout: either would cut a long stream.
		server := &http.Server{Addr: addr, Handler: kernel, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}

		failed := make(chan error, 1)
		go func() {
			slog.InfoContext(c, "serving", "addr", addr)
			if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				failed <- err
			}
		}()

		select {
		case err := <-failed:
			return err
		case <-c.Done():
		}

		shutdown, cancel := context.WithTimeout(context.WithoutCancel(c), cfg.App.ShutdownTimeout)
		defer cancel()

		return server.Shutdown(shutdown)
	})
}
```

`db.PoolConfig` parses the URL into the settings every reef pool uses; `db.Open` does the same and pings the database, which suits a command that must fail fast. See [database](database.md) for both.

## Initial Configuration

### Environment Based Configuration

Settings that differ between your machine and production live in a `.env` file in your application's root, which [`env.Load`](configuration.md#environment-configuration) reads at startup. Anything already exported in the process environment wins over the file, so production can supply every value as a real environment variable and ship no `.env` at all.

The one value you must set before anything boots is `APP_KEY`, the 32-byte key the [encrypter](encryption.md) seals cookies and encrypted columns with. reef has no `key:generate` command; generate one with any secure random source:

```shell
echo "APP_KEY=base64:$(openssl rand -base64 32)" >> .env
```

An empty or malformed key stops the application at boot, because `crypt.New` refuses any key that isn't 32 bytes.

### Databases and Migrations

Migrations are plain SQL files in `database/migrations`, embedded in the binary so a production machine can migrate without the source tree:

```go
// Package migrations holds the schema migrations, embedded so the binary
// migrates a database without the source tree.
package migrations

import "embed"

// FS is every migration, NNNNN_name.sql in version order.
//
//go:embed *.sql
var FS embed.FS
```

The `migration` package turns them into the `migrate`, `migrate:status`, `migrate:rollback` and `make:migration` commands. Register them on the console kernel with the pool they should run on:

```go
package bootstrap

import (
	"errors"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/migration"

	"example.com/flights/app"
	"example.com/flights/database/migrations"
)

// migrationCommands are migrate, migrate:status, migrate:rollback and
// make:migration over database/migrations.
func migrationCommands(a *app.App) []console.Command {
	return migration.Commands{
		FS:  migrations.FS,
		Dir: filepath.Join("database", "migrations"),
		Pool: func(c *console.Context) (*pgxpool.Pool, func(), error) {
			pool, ok := a.DB.(*pgxpool.Pool)
			if !ok {
				return nil, nil, errors.New("migrations need a database connection")
			}

			return pool, func() {}, nil
		},
	}.All()
}
```

Each file is a goose migration, numbered in order:

```sql
-- +goose Up
CREATE TABLE airports (id bigserial PRIMARY KEY, code text NOT NULL UNIQUE, name text NOT NULL);
```

The first migration is the schema's baseline, so it has no `Down` section: `migrate:rollback` refuses to roll it back (`migration.ErrBaseline`), because undoing it would drop every table. Every later migration should have a `-- +goose Down` that reverses its `Up`.

> [!NOTE]
> `//go:embed *.sql` refuses to compile an empty directory, so write the first migration by hand as `database/migrations/00001_create_airports_table.sql`. From then on, `make:migration` writes the next numbered file for you, from your module root.

Create your database, then run the migrations:

```shell
go run ./cmd/flights migrate
go run ./cmd/flights make:migration add_timezone_to_airports_table
```

The [migrations](migrations.md) page covers writing migrations, rolling back, running them on a direct (non-pooled) connection, and adopting a database another tool migrated first. Once the schema exists, [models](models.md) are generated from it.

## Running the Application

With a `.env` in place, serve the application:

```shell
go run ./cmd/flights serve
```

The server listens on `APP_ADDR`, `127.0.0.1:8000` by default, and `curl http://127.0.0.1:8000/api/airports/LHR` answers with JSON. `go run ./cmd/flights list` lists every command, and `go run ./cmd/flights route:list` every route with its name.

> [!WARNING]
> Default your listen address to loopback, as the example does. Binding `0.0.0.0` exposes a development server on every network interface; in production pass the address you mean explicitly.

## Next Steps

Now that you have an application, you may be wondering what to learn next. We recommend reading these pages in order:

- [Configuration](configuration.md)
- [Directory Structure](directory-structure.md)
- [Request Lifecycle](lifecycle.md)
- [Routing](routing.md)
- [Controllers](controllers.md)
- [Validation](validation.md)
- [Models](models.md)
- [Testing](testing.md)
