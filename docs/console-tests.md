# Console Tests

- [Introduction](#introduction)
- [Running Commands](#running-commands)
- [Success and Failure Expectations](#success-and-failure-expectations)
- [Input and Output Expectations](#input-and-output-expectations)
- [Testing a Command on Its Own](#testing-a-command-on-its-own)
- [Available Assertions](#available-assertions)

## Introduction

In addition to simplifying HTTP testing, reef provides a simple API for testing your application's [console commands](console.md). A test runs a command through the real console kernel, exactly as the CLI would, and asserts on its exit code and output. (`tests.Setup` is your application's own [test case](testing.md#building-a-test-case), not part of reef.)

```go
func TestQueueWorkRefusesTheSyncConnection(t *testing.T) {
	tc := tests.Setup(t)

	tc.RunCommand("queue:work").
		AssertFailed().
		ExpectsOutputToContain("QUEUE_CONNECTION=database")
}
```

## Running Commands

`testkit.RunCommand` runs a command through a `*console.Kernel` and captures everything it prints:

```go
result := testkit.RunCommand(t, tc.Ctx, kernel, "bookings:send-hold-warnings")
```

The arguments after the kernel are the command line as it follows the binary's name: the command, then its arguments and options, one per element. `RunCommand` returns a `*testkit.CommandResult`, whose `Code` field holds the exit code and `Output` field everything the command printed, errors included. The output is captured without styling, so you assert on plain text.

The context you pass is the one the command runs on, after the kernel's bind function (unless it was built with `nil`) adds your app's services to it. Pass the test's context, so the command's queries run in the test's transaction and read the test's clock. A test case helper can do this for you: `tc.RunCommand(argv...)` runs the command on `tc.Ctx` through the app's console kernel.

```go
tc.FreezeTime()
factories.Booking(tc).Create(func(b *models.Booking) {
	b.HoldExpiresAt = clock.Now(tc.Ctx).Add(10 * time.Minute)
})

tc.RunCommand("bookings:send-hold-warnings").
	AssertSuccessful().
	ExpectsOutput("Warned 1 booking(s).")
```

Everything the CLI accepts works here too: `list`, `help <command>`, `<command> --help`, and the global `--quiet` (`-q`), which discards the command's output.

> [!NOTE]
> reef's commands don't prompt for input, so there is nothing like `expectsQuestion`: a command takes everything it needs as arguments and options, and a test passes them in `argv`.

## Success and Failure Expectations

You may use `AssertExitCode` to assert that the command exited with a given code:

```go
tc.RunCommand("fares:refresh", "--days=10").AssertExitCode(0)
```

Since a command exits `0` (`console.Success`) when it succeeds and non-zero when it doesn't, you'll usually reach for `AssertSuccessful` and `AssertFailed` instead:

```go
tc.RunCommand("list").AssertSuccessful()

tc.RunCommand("queue:work").AssertFailed()
```

How a command ends decides the code it exits with:

| The command | Exit code | Output |
|---|---|---|
| returns `nil` from `Handle` | `0` | |
| returns an error | `1` | the error's message |
| returns `c.Failf(...)` | `1` | the formatted sentence |
| returns `console.Exit(code)` | `code` | nothing more |
| panics | `1` | the panic value (the panic also goes to the kernel's `Report`) |
| reads an argument or option its signature doesn't declare, or reads a flag's value | `1` | the panic it raises (`console: greet declares no argument "nmae"`) |
| is not registered | `1` | `Command "name" is not defined.`, with near names suggested |
| is given input it doesn't accept | `1` | the parse error, then the command's usage |

A misspelled name in a command's own `c.Argument(...)` or `c.Option(...)` therefore fails the first test that runs the command, instead of reading as empty forever.

```go
tc.RunCommand("bookings:scrub-passenger-data", "--force").
	AssertFailed().
	ExpectsOutput(`The "--force" option does not exist.`)
```

## Input and Output Expectations

`ExpectsOutput` asserts that some line of the output is exactly the given text, without its newline:

```go
tc.RunCommand("flights:refresh-status").
	AssertSuccessful().
	ExpectsOutput("Queued status refreshes for 1 flight(s).")
```

The `ExpectsOutputToContain` and `DoesntExpectOutputToContain` methods assert against a portion of the output, anywhere in it:

```go
result := tc.RunCommand("list").AssertSuccessful()
for _, name := range []string{"migrate", "queue:work", "schedule:run", "route:list"} {
	result.ExpectsOutputToContain(name)
}

// queue:work is the only worker command: there is no queue:listen.
tc.RunCommand("list").AssertSuccessful().DoesntExpectOutputToContain("queue:listen")
```

For anything these don't cover, such as the order of lines or a table's layout, read `result.Output` yourself:

```go
result := tc.RunCommand("route:list").AssertSuccessful()

lines := strings.Split(result.Output, "\n")
if !slices.ContainsFunc(lines, func(line string) bool { return strings.HasSuffix(line, " booking.store  ") }) {
	t.Errorf("route:list lacks booking.store:\n%s", result.Output)
}
```

> [!NOTE]
> Unlike the [response assertions](http-tests.md#available-assertions), a failed command assertion reports with `t.Errorf` and lets the test go on, so one run shows every expectation that missed. Each message names the command line and includes its full output.

## Testing a Command on Its Own

A command is a value whose dependencies are its fields, so a test may build a kernel holding just the command under test, with test doubles for its dependencies. Say `airports:import` shells out to the `duckdb` CLI to read an airport data file; its tests register it with a fake process runner and keep the app's HTTP mock:

```go
// runImportCommand runs airports:import with a faked duckdb runner.
func runImportCommand(t *testing.T, tc *tests.TestCase, runner commands.ProcessRunner, argv ...string) *testkit.CommandResult {
	t.Helper()

	kernel := console.NewKernel(nil, console.KernelConfig{Name: "flights"})
	kernel.Register(commands.ImportAirports{
		HTTP: tc.App.HTTP, DataPath: tc.App.Config.Storage.Path, Run: runner,
	})

	return testkit.RunCommand(t, tc.Ctx, kernel, append([]string{"airports:import"}, argv...)...)
}
```

A test then hands it the runner the case needs:

```go
failing := func(context.Context, time.Duration, []string) commands.ProcessResult {
	return commands.ProcessResult{Failed: true, ErrorOutput: "IO Error: No files found\n"}
}

runImportCommand(t, tc, failing, source).
	AssertFailed().
	ExpectsOutputToContain("DuckDB failed: IO Error: No files found")
```

A command whose dependencies the app already fakes under test (the HTTP mock, mail, the queue) needs none of this: run it as the app registers it, with `tc.RunCommand`.

A kernel built with a `nil` bind hands the command the context you pass, unchanged. This works for commands defined inline with `console.Func` as well:

```go
func TestGreetGreetsByName(t *testing.T) {
	kernel := console.NewKernel(nil, console.KernelConfig{Name: "flights"})
	kernel.Register(console.Func("greet {name : Who to greet}", "Greet someone", func(c *console.Context) error {
		c.Infof("Hello, %s!", c.Argument("name"))

		return nil
	}))

	testkit.RunCommand(t, t.Context(), kernel, "greet", "Ana").
		AssertSuccessful().
		ExpectsOutput("Hello, Ana!")
}
```

## Available Assertions

`testkit.CommandResult` has the following assertions. Each returns the result, so they chain.

- [AssertExitCode](#assertexitcode)
- [AssertFailed](#assertfailed)
- [AssertSuccessful](#assertsuccessful)
- [DoesntExpectOutputToContain](#doesntexpectoutputtocontain)
- [ExpectsOutput](#expectsoutput)
- [ExpectsOutputToContain](#expectsoutputtocontain)

#### AssertExitCode

Assert that the command exited with the given code:

```go
result.AssertExitCode(console.Failure)
```

#### AssertFailed

Assert that the command exited with any non-zero code:

```go
result.AssertFailed()
```

#### AssertSuccessful

Assert that the command exited `0`:

```go
result.AssertSuccessful()
```

#### DoesntExpectOutputToContain

Assert that the output does not contain the given text anywhere:

```go
result.DoesntExpectOutputToContain("SKIPPED")
```

#### ExpectsOutput

Assert that some line of the output is exactly the given text:

```go
result.ExpectsOutput("Nothing to migrate.")
```

#### ExpectsOutputToContain

Assert that the output contains the given text anywhere:

```go
result.ExpectsOutputToContain("River migration")
```
