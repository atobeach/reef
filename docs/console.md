# Console

- [Introduction](#introduction)
- [Writing Commands](#writing-commands)
    - [Command Structure](#command-structure)
    - [Closure Commands](#closure-commands)
    - [Exit Codes](#exit-codes)
- [Defining Input Expectations](#defining-input-expectations)
    - [Arguments](#arguments)
    - [Options](#options)
    - [Input Arrays](#input-arrays)
    - [Input Descriptions](#input-descriptions)
    - [Global Options](#global-options)
- [Command I/O](#command-io)
    - [Retrieving Input](#retrieving-input)
    - [Writing Output](#writing-output)
    - [Tables](#tables)
    - [Progress Bars](#progress-bars)
    - [Tasks and Details](#tasks-and-details)
- [Registering Commands](#registering-commands)
    - [Binding Services to the Context](#binding-services-to-the-context)
- [Running Commands](#running-commands)
- [Programmatically Executing Commands](#programmatically-executing-commands)
    - [Calling Commands From Other Commands](#calling-commands-from-other-commands)
    - [Calling Commands From Go Code](#calling-commands-from-go-code)
- [Reporting Errors](#reporting-errors)
- [Signal Handling](#signal-handling)
- [Testing Commands](#testing-commands)
- [Parsing Signatures and Input Directly](#parsing-signatures-and-input-directly)
- [Available Methods](#available-methods)

## Introduction

The `github.com/atobeach/reef/console` package is reef's take on Artisan: the command-line interface your application ships in its own binary. A command declares a signature (its name, arguments and options) in the same mini-language Artisan uses, and a console kernel parses `argv`, runs the command and turns its result into an exit code.

In the examples on this page the binary is `flights` (`go run ./cmd/flights <command>` from the application's root). To view a list of every command, you may use the `list` command:

```shell
flights list
```

Every command also comes with a help screen that displays its description, arguments and options. To view it, precede the command's name with `help`, or pass `--help`:

```shell
flights help schedules:refresh
flights schedules:refresh --help
```

reef has no `make:command` generator and no Tinker: a command is a small Go type you write by hand, and Go has no REPL to drop into.

## Writing Commands

Commands usually live in an `app/console/commands` package. A command is any type that implements the `console.Command` interface:

```go
type Command interface {
	Signature() string
	Description() string
	Handle(c *console.Context) error
}
```

### Command Structure

Let's take a look at an example command. The `Signature` method defines the command's name and its input expectations, `Description` is the one-liner `list` shows next to it, and `Handle` does the work. A command's dependencies are exported fields, filled in with a struct literal where the application builds its commands:

```go
package commands

import (
	"example.com/flights/app/jobs"
	"example.com/flights/app/models"
	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/queue"
)

// RefreshSchedules is `schedules:refresh`.
type RefreshSchedules struct {
	Queue queue.Dispatcher
}

// Signature implements console.Command.
func (RefreshSchedules) Signature() string {
	return "schedules:refresh {airport? : IATA code or name of the departure airport (omit to refresh all)}"
}

// Description implements console.Command.
func (RefreshSchedules) Description() string { return "Refresh departure schedules from the airline API" }

// Handle queues one RefreshDepartures per airport (or the named one).
func (cmd RefreshSchedules) Handle(c *console.Context) error {
	if input := c.Argument("airport"); input != "" {
		airport, err := models.Airports(c).WhereRaw("(code = ? or name = ?)", input, input).First()
		if err != nil {
			return err
		}

		if airport == nil {
			return c.Failf("Airport not found: %s", input)
		}

		if err := cmd.Queue.Dispatch(c, jobs.RefreshDepartures{AirportID: airport.ID}); err != nil {
			return err
		}

		c.Infof("Dispatched a schedule refresh for %s.", airport.Code)

		return nil
	}

	airports, err := models.Airports(c).OrderBy(models.AirportColumns.Code).Get()
	if err != nil {
		return err
	}

	for _, airport := range airports {
		if err := cmd.Queue.Dispatch(c, jobs.RefreshDepartures{AirportID: airport.ID}); err != nil {
			return err
		}
	}

	c.Infof("Dispatched schedule refreshes for %d airports.", len(airports))

	return nil
}
```

The `*console.Context` your `Handle` method receives embeds three things, so you rarely need anything else:

- a `context.Context`: the process context, canceled on `SIGINT`/`SIGTERM` when your entry point arranges it, with whatever the kernel's [bind function](#binding-services-to-the-context) attached (typically the database connection among others). That is why `models.Airports(c)` and `cmd.Queue.Dispatch(c, ...)` take `c` directly;
- the parsed `*console.Input`, so `c.Argument("airport")` reads an argument;
- the `*console.Output`, so `c.Infof(...)` writes a line.

> [!NOTE]
> For greater code reuse, it is good practice to keep your console commands light and let them defer to your application's actions and services to accomplish their tasks.

### Closure Commands

When a command doesn't deserve a type of its own, `console.Func` defines one inline from a signature, a description and a handler. An entry point may define its `serve` command this way, closing over the HTTP kernel it serves:

```go
func serveCommand(cfg config.Config, kernel *web.Kernel) console.Command {
	return console.Func("serve {--addr= : Listen address}", "Serve the application", func(c *console.Context) error {
		addr := c.Option("addr")
		if addr == "" {
			addr = cfg.App.Addr
		}

		// No ReadTimeout or WriteTimeout: either would cut a long stream.
		server := &http.Server{Addr: addr, Handler: kernel, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}

		// ...

		return server.ListenAndServe()
	})
}
```

reef's own commands (`queue:work`, `schedule:run`, `migrate`, `route:list`) are built with `console.Func` too, which is why packages such as `queue` and `schedule` hand you a `[]console.Command` to register. The ORM's and the router's commands live in `db/dbcmd` and `web/webcmd`, so neither library links the console.

### Exit Codes

The error `Handle` returns decides the process's exit code. The `console.Success` (0) and `console.Failure` (1) constants name the two common ones:

| `Handle` returns | What is printed | Exit code |
|---|---|---|
| `nil` | nothing | `0` |
| `c.Failf("File not found: %s", path)` | the sentence, as an error line | `1` |
| `console.Exit(3)` | nothing | `3` |
| any other error | `err.Error()`, as an error line | `1` |

`c.Failf` is the equivalent of Laravel's `$this->fail(...)`: it prints a full sentence meant for the operator and returns `console.Exit(console.Failure)` for you to return. Because the sentence is printed rather than wrapped into the error, it may start with a capital letter and end with a full stop, which Go error strings shouldn't:

```go
if _, err := os.Stat(path); err != nil {
	return c.Failf("File not found: %s", path)
}
```

`console.Exit(code)` ends the command with a code of its own without printing anything, for when the command has already said why. `console.Exit(console.Success)` is `nil`.

## Defining Input Expectations

When writing console commands, it is common to gather input from the user through arguments or options. reef makes it convenient to define the input you expect using the `Signature` method, in the same expressive, route-like syntax as Artisan.

### Arguments

All user supplied arguments and options are wrapped in curly braces. In the following example, the command defines one required argument, `path`, and one option, `--output`:

```go
func (ImportAirports) Signature() string {
	return "airports:import {path} {--output=}"
}
```

You may make an argument optional, or give it a default value:

```go
// Optional argument...
"schedules:refresh {airport?}"

// Optional argument with a default value...
"schedules:refresh {airport=LHR}"
```

A required argument that is missing, or a positional value nobody declared, stops the command before `Handle` runs, with the error and the command's usage line:

```text
Not enough arguments (missing: "path").

airports:import [options] <path>
```

### Options

Options, like arguments, are another form of user input. Options are prefixed by two hyphens (`--`) when they are provided on the command line. There are two types of options: those that receive a value and those that don't. Options that don't receive a value serve as a boolean "switch":

```go
"model:prune {--model=* : Models to prune} {--pretend}"
```

In this example (a trimmed version of reef's own `model:prune`), `--pretend` may be specified when calling the command; if it is, `c.Flag("pretend")` is `true`. Passing a value to such a switch (`--pretend=yes`) is an error.

#### Options With Values

An option followed by `=` expects a value. The value may be given as `--option=value` or as the next token, `--option value`:

```shell
flights airports:import airports.csv.gz --output=build/airports.csv.gz
flights airports:import airports.csv.gz --output build/airports.csv.gz
```

You may assign a default value by placing it after the `=`. If no value is passed for the option, the default is used:

```go
"routes:feature {--top=250 : How many of the busiest routes to feature}"
```

An option given without a value (`--output` as the last token, or followed by another option) counts as present but keeps its default, or `""` when it has none.

#### Option Shortcuts

To assign a shortcut when defining an option, specify it before the option name and use the `|` character as a delimiter:

```go
"mail:send {user} {--Q|queue=default}"
```

A shortcut takes its value as the next token or glued to it, so `-Q content` and `-Qcontent` both set `--queue` to `content`.

An option or shortcut the command doesn't declare is an error (`The "--force" option does not exist.`). `--` ends option parsing: every token after it is a positional argument, even one starting with a hyphen.

### Input Arrays

If you would like to define arguments or options that expect multiple input values, you may use the `*` character. An array argument collects every remaining positional value, so it must come last:

```go
// Required: at least one value...
"mail:send {user*}"

// Optional...
"mail:send {user?*}"

// With defaults...
"mail:send {user=*1,2}"
```

An array option is repeated on the command line, once per value, so `--model=FlightSearch --model=Booking` gives the following option two values:

```go
"model:prune {--model=* : Models to prune}"
```

An array option may also carry defaults after the `*`: `{--model=*FlightSearch,Booking}`.

### Input Descriptions

You may assign descriptions to input arguments and options by separating the name from the description with a colon surrounded by spaces. If you need a little extra room, the signature may span several lines; the whitespace in each description is folded to single spaces:

```go
func (ImportAirports) Signature() string {
	return `airports:import
		{path : Path to the airports CSV export (optionally .gz)}
		{--output= : Output gzipped CSV path (defaults to database/data/airports.csv.gz)}`
}
```

The descriptions appear on the command's `help` screen, along with each default and a "(multiple values allowed)" note for array options.

### Global Options

Every command also accepts the options Artisan users type out of habit:

| Option | Effect |
|---|---|
| `-h`, `--help` | Shows the command's help screen instead of running it. |
| `-q`, `--quiet` | Discards everything the command prints, error lines included. The exit code is unchanged. |
| `-n`, `--no-interaction` | Accepted; reef commands never ask questions. |
| `--ansi`, `--no-ansi` | Accepted; colors follow whether the output is a terminal. |
| `-v`, `-vv`, `-vvv`, `--verbose` | Accepted; verbosity is fixed. |
| `--env=` | Accepted; the environment comes from your configuration. |

## Command I/O

### Retrieving Input

While your command is executing, you will likely need to access the values of the arguments and options it accepts. All of them are methods on the context:

```go
func (cmd FeatureRoutes) Handle(c *console.Context) error {
	top, err := c.IntOption("top", 0)
	if err != nil {
		return err
	}

	// ...
}
```

| Method | Returns |
|---|---|
| `c.Argument("airport")` | The argument's value, its default, or `""`. |
| `c.ArgumentList("user")` | An array argument's values, or its defaults. |
| `c.HasArgument("airport")` | Whether the argument was given, which tells an omitted optional argument from an empty one. |
| `c.Option("output")` | The option's value (the last one, when given several times), its default, or `""`. |
| `c.OptionList("model")` | An array option's values, or its defaults. |
| `c.HasOption("output")` | Whether the option was given at all, with or without a value. |
| `c.Flag("pretend")` | Whether a value-less option was given. |
| `c.IntOption("top", 0)` | The option as an `int` of at least the given minimum. |
| `c.SecondsOption("timeout")` | The option, given in whole seconds, as a `time.Duration`. |

`IntOption` and `SecondsOption` return an error naming the option when the value isn't a whole number in range (`The "--top" option must be a whole number of at least 0.`). The message is a full sentence, so `Handle` may return it as it is. Because an absent option without a default reads as `""`, give such options a default in the signature.

A command reads only the names its signature declares, plus the global options (`quiet`, `verbose`, `env`…). Any other name is a typo that would read as empty or `false` forever, so every one of these methods panics on it (`console: demo declares no option "--forse"`), and so do `Option`, `OptionList`, `IntOption` and `SecondsOption` on a value-less option (`console: demo's "--force" takes no value; read it with Flag`). The kernel recovers the panic, reports it and fails the command, so a test that runs the line catches the mistake.

reef has no interactive prompts: commands never ask questions, so everything a command needs arrives as arguments and options.

### Writing Output

To send output to the console, you may use the `Line`, `Info`, `Comment`, `Warn` and `Error` methods. Each of these methods will use appropriate ANSI colors for its purpose, and each has a `printf`-style twin (`Linef`, `Infof`, `Commentf`, `Warnf`, `Errorf`):

```go
c.Info("Refreshing departure schedules...")
c.Infof("Imported %d %s, skipped %d.", imported, noun, skipped)
c.Warnf("schedules:import failed (%v); the next deploy or the nightly run imports them again.", err)
c.Errorf("Invalid %s format.", filepath.Base(path))
```

| Method | Style |
|---|---|
| `Line` / `Linef` | Plain text. |
| `Info` / `Infof` | Green. |
| `Comment` / `Commentf` | Yellow. |
| `Warn` / `Warnf` | Yellow. |
| `Error` / `Errorf` | White on red. |

Colors are only emitted when the output is a terminal, so logs, pipes and test buffers receive plain text. `NewLine` prints an empty line, and `Write` prints a message without a trailing newline (progress dots, for instance).

> [!NOTE]
> `Error` only prints a line. To fail the command, return `c.Failf(...)` or an error from `Handle`.

### Tables

The `Table` method makes it easy to correctly format multiple rows and columns of data. All you need to do is provide the column names and the rows; reef calculates the widths for you:

```go
c.Table(
	[]string{"Code", "Airport"},
	[][]string{
		{"LHR", "London Heathrow"},
		{"JFK", "New York JFK"},
	},
)
```

```text
+------+-----------------+
| Code | Airport         |
+------+-----------------+
| LHR  | London Heathrow |
| JFK  | New York JFK    |
+------+-----------------+
```

### Progress Bars

For long running tasks, it can be helpful to show a progress bar that informs users how complete the task is. `CreateProgressBar` builds a bar for a number of steps; `Start` draws it empty, `Advance` moves it one step, and `Finish` fills it and draws it again:

```go
bar := c.CreateProgressBar(len(records))
bar.Start()

for _, record := range records {
	importRecord(record)

	bar.Advance()
}

bar.Finish()
c.NewLine()
```

```text
   0/120 [░░░░░░░░░░░░░░░░░░░░░░░░░░░░]   0%
 120/120 [▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓] 100%
```

The bar never redraws in place: it prints one frame when it starts and one when it finishes, so it reads the same in a deploy log as on a terminal. The last frame is left without a newline, which is why the example ends with `c.NewLine()`.

### Tasks and Details

The `Task` method runs a function and prints its description, dotted out to the elapsed time and `DONE`, or `FAIL` when the function returns an error. The error is returned for you to handle. `TwoColumnDetail` prints the same dotted layout with any text on the right. For example, a `reference:load` command that loads airports and airlines only when they're missing could use both:

```go
for _, loader := range loaders {
	present, err := loader.Present(ctx)
	if err != nil {
		return err
	}

	if present {
		c.TwoColumnDetail(loader.Name(), "SKIPPED")

		continue
	}

	if err := c.Task(loader.Name(), func() error { return loader.Load(ctx) }); err != nil {
		return err
	}
}
```

```text
  SeedAirports ....................................................... SKIPPED
  SeedAirlines .................................................. 12.34ms DONE
```

Both fill the terminal's width: the `COLUMNS` environment variable when it is set, 80 columns otherwise, and never more than 150.

## Registering Commands

Commands are registered on a console kernel. `console.NewKernel` builds an empty one from the function that binds your services and a `console.KernelConfig`, and `Register` adds commands to it:

```go
kernel := console.NewKernel(a.BindContext, console.KernelConfig{
	Name:   "Flights",
	Report: errortracker.NewReporter(a.ErrorTracker, slog.Default()),
})

kernel.Register(Commands(a)...)
kernel.Register(dbcmd.PruneCommand(models.Prunables()))
kernel.Register(webcmd.RouteListCommand(a.Router))
```

| `KernelConfig` field | Purpose |
|---|---|
| `Name` | The heading `list` prints. |
| `Report` | Receives the errors commands [report](#reporting-errors) and the panics they raise. |

A signature without a name or a command name registered twice is a programming error, so `Register` panics at boot rather than at run time.

A typical application builds every command with a struct literal in one `Commands` function, handing over its dependencies explicitly, and its `bootstrap` package registers them alongside reef's own commands (`queue.Commands`, `schedule.Commands`, the migration commands and `route:list`):

```go
// Commands are the app's console commands.
func Commands(a *app.App) []console.Command {
	return []console.Command{
		commands.RefreshSchedules{Queue: a.Queue},
		commands.FeatureRoutes{},
		commands.CheckDelays{Events: a.Events},
		// ...
	}
}
```

reef has no service container to resolve a command's dependencies from, and no command discovery: a command runs only once it is registered.

### Binding Services to the Context

The bind function, `NewKernel`'s required first argument, runs before each command's `Handle`, on the context the command receives, just as the HTTP kernel binds services per request; `nil` binds nothing. A typical application uses it to put the database connection, the clock and the encrypter on the context, which is what lets a command write `models.Airports(c)`. See [the request lifecycle](lifecycle.md) for what a typical application binds.

## Running Commands

`Kernel.Run` is the whole command line: give it the context, the arguments after the binary's name, and where to write, and it returns the process exit code. A minimal entry point looks like this:

```go
func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kernel := console.NewKernel(nil, console.KernelConfig{Name: "Flights"})
	kernel.Register(commands.FeatureRoutes{})

	return kernel.Run(ctx, args, os.Stdout)
}
```

`Run` understands the same entry points as Artisan:

| Command line | What runs |
|---|---|
| (nothing), `list`, `help`, `--help`, `-h` | The command list: usage, the global options, then every command grouped by namespace (the part before the `:`), unnamespaced commands first. |
| `list queue` | The commands in the `queue` namespace only. |
| `help <command>` | The command's help screen. |
| `<command> ... --help` | The same help screen. |
| `<command> [options] [arguments]` | The command. |

An unknown name, whether run or passed to `help`, fails with `Command "x" is not defined.`, followed by the closest registered names under "Did you mean one of these?".

## Programmatically Executing Commands

### Calling Commands From Other Commands

Sometimes you may wish to call other commands from an existing command. You may do so using the `Call` method, which accepts the command name and its arguments as separate strings. The called command writes to the same output, and runs with the same context:

```go
func (cmd Deploy) Handle(c *console.Context) error {
	if err := c.Call("migrate"); err != nil {
		return err
	}

	return c.Call("reference:load")
}
```

When the called command fails, `Call` returns an `Exit` error carrying its exit code. Its message has already been printed, so `Handle` can return the error as it is and exit with the same code.

If you would like to call another command and suppress all of its output, you may use `CallSilently`. Its error still says why the command failed: it carries the lines the command printed as errors (`exit status 1: The schedules feed is not reachable…`), and returning it from `Handle` prints them. `HasCommand` reports whether the kernel defines a command, for optional integrations:

```go
if !c.HasCommand("schedules:import") {
	return errors.New(`The command "schedules:import" does not exist.`)
}

if err := c.CallSilently("schedules:import"); err != nil {
	c.Warnf("schedules:import failed (%v); the next deploy or the nightly run imports them again.", err)
}
```

> [!NOTE]
> `Call`, `CallSilently` and `HasCommand` need the context the kernel built. A `console.Context` you assemble by hand has no kernel: `HasCommand` is then `false`, and the other two return an error.

### Calling Commands From Go Code

Outside a command, `Kernel.Call` runs a command by name with already-split arguments and an `*Output` of your choosing, and returns its exit code. The [scheduler](scheduling.md) uses it to run scheduled commands:

```go
code := kernel.Call(ctx, "flights:check-delays", nil, console.NewOutput(&buffer))
```

`Call` prints the error and returns `console.Failure` for an unknown command, and prints the error followed by the usage line for input that doesn't match the signature. `Kernel.Has`, `Kernel.Names` and `Kernel.Definition` let you inspect the registry: a test can use `Definition` to prove every scheduled command is registered and that its arguments parse against its signature (see [inspecting events](scheduling.md#inspecting-events)).

## Reporting Errors

Sometimes a command should record an error without failing, such as one row of a batch that couldn't be processed. `c.Report` hands the error to the kernel's `Report` function (an error tracker, for example) and carries on; without a reporter, the error is printed as an error line instead. `Report(nil)` does nothing:

```go
for _, flight := range flights {
	if err := cmd.Events.Dispatch(c, appevents.FlightDelayed{Flight: flight, Minutes: delay}); err != nil {
		c.Report(err)

		continue
	}

	announced++
}
```

A command that panics doesn't take the binary down: the kernel recovers, hands the panic to the kernel's `Report` function (when one is configured) as `console: <command> panicked: <value>` wrapping a [`*panics.Error`](errors.md#recovered-panics) that carries its stack, prints the panic value as an error line, and exits with `console.Failure`.

## Signal Handling

reef doesn't install signal handlers of its own. Instead, a command runs on the context you pass to `Run`: build it with `signal.NotifyContext`, as in the entry point above, and `SIGINT`/`SIGTERM` cancel the command's context. Long-running commands such as `queue:work`, `schedule:work` and `serve` watch `c.Done()` and stop gracefully.

## Testing Commands

Run a command in a test with `testkit.RunCommand` (or a `tc.RunCommand` helper on your own [test case](testing.md#a-full-test-case)), which captures its output and offers assertions on its exit code and output:

```go
tc.RunCommand("flights:check-delays").AssertSuccessful().ExpectsOutput("Announced 1 delay(s).")
```

See [console tests](console-tests.md) for the full set of assertions.

## Parsing Signatures and Input Directly

The kernel parses everything for you, but the parser is public for tools that need it, such as a help page generator:

- `console.ParseSignature(signature)` returns a `console.Definition`: the command's `Name`, its `Arguments` and its `Options`. It only fails when the signature has no name.
- `console.ParseInput(definition, argv)` parses arguments (without the command name) against a definition into an `*Input`. Its errors are the full sentences the kernel prints.
- `Definition.Usage()` is the one-line synopsis the help screen and parse errors print, such as `airports:import [options] <path>`.

Each `console.Argument` has a `Name`, a `Description`, `Required`, `Array` (collects every remaining positional value) and `Default` (a `[]string`). Each `console.Option` has a `Name`, a one-letter `Shortcut`, a `Description`, `AcceptsValue` (false for a switch), `Array` (may be given several times) and `Default`:

```go
definition, err := console.ParseSignature("model:prune {--model=* : Models to prune} {--pretend}")
if err != nil {
	return err
}

input, err := console.ParseInput(definition, []string{"--model=WeatherForecast", "--pretend"})
if err != nil {
	return err
}

input.OptionList("model") // []string{"WeatherForecast"}
input.Flag("pretend")     // true
definition.Usage()        // "model:prune [options]"
```

## Available Methods

#### Package

- `console.Success`, `console.Failure`: the exit codes `0` and `1`.
- `console.Exit(code)`: ends a command with `code`, printing nothing; `Exit(Success)` is `nil`.
- `console.Func(signature, description, handle)`: a `Command` defined inline.
- `console.NewKernel(bind, config)`: an empty kernel; `bind` attaches your services to every command's context (`nil`: none).
- `console.NewOutput(w)`: an `*Output` writing to `w`, colored when `w` is a terminal.
- `console.ParseSignature(signature)`, `console.ParseInput(definition, argv)`: the parsers.

#### Command

- `Signature()`: the name, `{arguments}` and `{--options}`.
- `Description()`: the one-liner `list` shows.
- `Handle(c)`: runs the command; its error decides the exit code.

#### Context

- `Failf(format, args...)`: prints a failure line and returns `Exit(Failure)`.
- `Report(err)`: hands `err` to the kernel's reporter without failing.
- `Call(name, args...)`: runs another command with the same output.
- `CallSilently(name, args...)`: runs another command, keeping only why it failed.
- `HasCommand(name)`: whether the running kernel defines a command.

#### Input

- `Argument(name)`, `ArgumentList(name)`, `HasArgument(name)`.
- `Option(name)`, `OptionList(name)`, `HasOption(name)`, `Flag(name)`.
- `IntOption(name, minimum)`, `SecondsOption(name)`.
- Each panics on a name the signature doesn't declare (the global options excepted), and a value read panics on a value-less option: the kernel reports the panic and fails the command.

#### Output

- `Line`, `Info`, `Comment`, `Warn`, `Error`, and their `f` variants `Linef`, `Infof`, `Commentf`, `Warnf`, `Errorf`.
- `NewLine()`: an empty line. `Write(message)`: a message without a newline.
- `Table(headers, rows)`: a boxed table.
- `TwoColumnDetail(first, second)`: a dotted two-column line.
- `Task(description, task)`: runs `task`, printing `DONE` or `FAIL` with its run time.
- `CreateProgressBar(total)`: a `*ProgressBar`, with `Start()`, `Advance()` and `Finish()`.

#### Kernel

- `Register(commands...)`: adds commands; panics on a nameless signature or a duplicate.
- `Run(ctx, argv, w)`: the whole command line, returning the exit code.
- `Call(ctx, name, args, out)`: one command by name, returning its exit code.
- `Has(name)`, `Names()` (sorted), `Definition(name)`: the registry.

#### Signature Types

- `Definition`: `Name`, `Arguments`, `Options`, and `Usage()`.
- `Argument`: `Name`, `Description`, `Required`, `Array`, `Default`.
- `Option`: `Name`, `Shortcut`, `Description`, `AcceptsValue`, `Array`, `Default`.
- `KernelConfig`: `Name`, `Report`.
