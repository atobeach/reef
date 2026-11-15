# Configuration

- [Introduction](#introduction)
- [Environment Configuration](#environment-configuration)
    - [Environment Variable Types](#environment-variable-types)
    - [Retrieving Environment Configuration](#retrieving-environment-configuration)
    - [The Environment File Format](#the-environment-file-format)
    - [Determining the Current Environment](#determining-the-current-environment)
- [Accessing Configuration Values](#accessing-configuration-values)
    - [Failing at Boot](#failing-at-boot)
- [Debug Mode](#debug-mode)
- [Available Functions](#available-functions)

## Introduction

A reef application keeps its configuration in a typed struct that it reads from the environment once, at boot. In a typical reef application that is the `config` package: `config.Load()` returns a `config.Config` with a field for every setting the application reads, grouped the way Laravel groups its `config/*.php` files (`App`, `Session`, `Mail`, `Database`, `Redis`, `Queue`, `Broadcasting`, ...).

There is no `config()` helper and no configuration repository to query at run time. The config struct is handed to the code that needs it when the application is assembled, and a setting's type is checked by the compiler rather than cast at each use.

## Environment Configuration

It is often helpful to have different configuration values based on where the application is running: a log mail transport on your machine and the real one in production, say. reef's `env` package reads a `.env` file into the process environment, and your config package reads the values from there.

The file fills in values; it never overrides them. Any variable already exported in the process environment (a deployment secret, a CI variable, a one-off `APP_DEBUG=false go run ./cmd/flights serve`) wins over the same key in the file.

Call `env.Load` first thing, before reading the configuration. It looks for a `.env` in the directory you pass and then in each parent directory, and loads the first one it finds, so a command run from a subdirectory of your checkout still finds the checkout's `.env`:

```go
if err := env.Load("."); err != nil {
	slog.Error("loading .env", "error", err)

	return console.Failure
}

cfg, err := config.Load()
```

A missing `.env` is not an error: production supplies everything through the process environment. Any other failure, such as a file that can't be read, is.

If you are developing with a team, keep a `.env.example` in source control with every variable your application reads and a placeholder value, so other developers can see what they need to set.

#### Environment File Security

Your `.env` file should not be committed to source control: each developer and each server needs its own values, and anyone who can read the repository would otherwise read your credentials. Keep `.env` in `.gitignore` and commit only the example.

#### Additional Environment Files

`env.LoadFile` loads one named file. Unlike `Load`, it does not search parent directories, and a missing file is an error. A test suite can load `.env.testing` from the module root this way, and never the developer's `.env`, so a developer's settings can't change what the tests see:

```go
root, err := codegen.ModuleRoot()
if err != nil {
	t.Fatalf("finding the module root: %v", err)
}

if err := env.LoadFile(filepath.Join(root, ".env.testing")); err != nil {
	t.Fatalf("loading .env.testing: %v", err)
}
```

Because a loaded file never overwrites a variable that is already set, the first value loaded for a key is the one that sticks. If you load more than one file, load the most specific one first. Whatever the shell exports still wins over all of them, which is how CI points the tests' `DB_*` variables at its own Postgres.

### Environment Variable Types

Every variable is a string in the environment. The `env` functions convert them, and they understand the spellings `.env` files conventionally use:

| `.env` value | `env.Get` | `env.Bool` |
| --- | --- | --- |
| `true`, `(true)`, `1`, `yes`, `on` | the text as written | `true` |
| `false`, `(false)`, `0`, `no`, `off`, `empty`, `(empty)` | the text as written | `false` |
| `null`, `(null)` | the fallback | the fallback |
| empty (`KEY=`) or unset | the fallback | the fallback |

`env.Bool` ignores case and surrounding parentheses, so `TRUE` and `(on)` are `true` too; any other text is the fallback. Treating `null` as unset matters for files written for Laravel, which spell an absent value `REDIS_PASSWORD=null`.

If you need a value that contains spaces, enclose it in double or single quotes:

```ini
APP_NAME="Example Flights"
```

### Retrieving Environment Configuration

Your config package reads each variable with one of four functions. The second argument is the fallback, returned when the variable is unset, empty, `null` or `(null)`:

```go
import "github.com/atobeach/reef/env"

App{
	Env:          env.Get("APP_ENV", "production"),
	Debug:        env.Bool("APP_DEBUG", false),
	URL:          env.Get("APP_URL", "http://localhost"),
	Key:          env.Get("APP_KEY", ""),
	PreviousKeys: env.List("APP_PREVIOUS_KEYS", ""),
	// Loopback by default: never expose a dev server on every interface.
	Addr: env.Get("APP_ADDR", "127.0.0.1:8000"),
	// Kept under the platform's kill timeout, so in-flight requests
	// finish before the process is stopped.
	ShutdownTimeout: time.Duration(env.Int("APP_SHUTDOWN_TIMEOUT", 30)) * time.Second,
}
```

- `env.Get(key, fallback)` returns the variable as a string.
- `env.Bool(key, fallback)` parses it as a boolean, using the spellings in the [table above](#environment-variable-types).
- `env.Int(key, fallback)` parses it as a decimal integer; text that isn't one (`"10s"`, `"1.5"`) is the fallback, not an error.
- `env.List(key, fallback)` splits it on commas, trims each item and drops the blank ones. When the variable is unset, empty, `null` or `(null)`, the fallback string is split the same way, so `env.List("TRUSTED_PROXIES", "*")` is `[]string{"*"}` and `env.List("APP_PREVIOUS_KEYS", "")` is `nil`:

```ini
CORS_ALLOWED_ORIGINS=https://example.com, https://www.example.com,
```

```go
env.List("CORS_ALLOWED_ORIGINS", "http://localhost:3000")
// []string{"https://example.com", "https://www.example.com"}
```

> [!WARNING]
> Durations have no function of their own. Read them as a number of a fixed unit and convert, as in `time.Duration(env.Int("SESSION_LIFETIME", 120)) * time.Minute`, and name the unit in the variable's comment in `.env.example`.

### The Environment File Format

`env.Load` and `env.LoadFile` read one `KEY=value` pair per line:

```ini
# A comment line.
APP_ENV=local
export APP_URL=http://localhost:8000
APP_NAME="Example Flights"
MAIL_FROM_ADDRESS='hello@example.com'
APP_DEBUG=true # an inline comment
SECRET="keeps # inside quotes"
REDIS_PASSWORD=null
```

- Blank lines and lines starting with `#` are skipped, as is any line without an `=`.
- A leading `export ` is ignored, so a file can also be `source`d by a shell.
- The key and the value are trimmed of surrounding whitespace.
- A value wrapped in matching double or single quotes loses the quotes and keeps everything between them, `#` included. Nothing inside is unescaped or interpolated: `\n` stays a backslash and an `n`, and `${OTHER}` stays as written.
- In an unquoted value, a space followed by `#` starts a comment. A comment after a quoted value makes it unquoted: `KEY="x" # note` reads as `"x"`, quotes and all, so keep comments on their own line there.
- Each value is a single line; there are no multi-line values.
- When a key appears twice, the first value wins, since a loaded value is never overwritten.

### Determining the Current Environment

The current environment is the `APP_ENV` variable, which your config reads like any other. A good default is `production`, so a server that forgets to set it gets the safe behavior, and gives the config a method to ask:

```go
// IsProduction reports APP_ENV=production.
func (a App) IsProduction() bool {
	return a.Env == "production"
}
```

Code that behaves differently per environment takes the answer as an argument rather than reading the variable itself. A security headers middleware, for example, is built with `appmiddleware.SecurityHeaders(cfg.App.IsProduction())`.

## Accessing Configuration Values

Read a value from the config struct you were given, never from the environment: `env` belongs in your config package and nowhere else. The application's `bootstrap.Build` receives the `config.Config`, keeps it on the container as `a.Config`, and passes each component the part it needs when it builds it:

```go
a.Mail = mail.NewMailer(mail.MailerConfig{
	Transport: mailTransport(cfg, conns.Mail),
	From:      cfg.Mail.FromAddress,
	// ...
})
```

Many reef packages define their own config struct, and an application's config can hold one directly so it is read in one place. For example, an application's `Config` might hold `middleware.CorsConfig` for CORS and `realtime.Config` for the realtime server:

```go
type Config struct {
	App  App
	Cors middleware.CorsConfig
	// Realtime is the WebSocket server `flights reverb:start` runs.
	Realtime realtime.Config
	// ...
}
```

Since the configuration is read once into memory, there is nothing to cache and nothing to clear: a changed variable takes effect when the process restarts.

In tests you change a setting by changing the struct, not the environment. A test helper such as `tests.WithConfig` edits the test's copy of the config before the application boots, so parallel tests never see each other's settings.

### Failing at Boot

A setting that can't be used as given should stop the application at boot, not fail quietly on the first request. Have your `config.Load` return an error for such values, and let the entry point exit with it:

```go
cfg, err := config.Load()
if err != nil {
	slog.Error("loading config", "error", err)

	return console.Failure
}
```

reef's constructors follow the same rule: `crypt.New` refuses an `APP_KEY` that isn't 32 bytes, and `web.NewKernel` refuses a route name given to two routes.

## Debug Mode

The HTTP kernel's `Debug` setting decides how much an unexpected error reveals. Set it from `APP_DEBUG`:

```go
kernel, err := web.NewKernel(a.BindContext, web.KernelConfig{
	Router: a.Router,
	Debug:  cfg.App.Debug,
	// ...
})
```

With `Debug` off, the body of every unexpected error's 500 is `{"message": "Server Error"}`. With it on, the message is the error's own text, so you can see what failed without reading the log. Errors that carry their own status, such as a 404, a 422 or `web.Abort(500, "Flight search is down.")`, render the same either way (see [error handling](errors.md)).

> [!WARNING]
> For local development you may set `APP_DEBUG=true`. In production it must always be `false`: an error's text can include SQL, upstream responses or other details your users should never see.

## Available Functions

The `env` package is the whole of reef's environment support:

#### `env.Load`

`env.Load(dir string) error` loads the first `.env` found in `dir` or any parent directory. A missing file is not an error.

```go
err := env.Load(".")
```

#### `env.LoadFile`

`env.LoadFile(path string) error` loads the file at `path`. A missing file is an error (one that satisfies `errors.Is(err, fs.ErrNotExist)`).

```go
err := env.LoadFile(filepath.Join(root, ".env.testing"))
```

#### `env.Get`

`env.Get(key, fallback string) string` returns the variable, or `fallback` when it is unset, empty, `null` or `(null)`.

```go
host := env.Get("DB_HOST", "127.0.0.1")
```

#### `env.Bool`

`env.Bool(key string, fallback bool) bool` parses `true`, `1`, `yes`, `on` (and their negatives, plus `empty`), with or without parentheses, in any case; anything else is `fallback`.

```go
debug := env.Bool("APP_DEBUG", false)
```

#### `env.Int`

`env.Int(key string, fallback int) int` parses a decimal integer, or returns `fallback` when the variable is unset, empty, `null`, `(null)` or malformed.

```go
port := env.Int("DB_PORT", 5432)
```

#### `env.List`

`env.List(key, fallback string) []string` splits the variable (or `fallback`, when the variable is unset, empty, `null` or `(null)`) on commas, trimming each item and dropping blanks.

```go
proxies := env.List("TRUSTED_PROXIES", "*")
```
