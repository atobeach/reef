package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atobeach/reef/panics"
)

func TestParsesSignatures(t *testing.T) {
	definition, err := ParseSignature(`airports:import
		{path : Path to the airports CSV}
		{extra?* : Anything else}
		{--output= : Output gzipped CSV path}
		{--Q|queue=default : The queue}
		{--model=* : Models}
		{--regions=europe,asia : Regions}
		{--force : Re-upload}`)
	if err != nil {
		t.Fatal(err)
	}

	if definition.Name != "airports:import" {
		t.Fatalf("name = %q", definition.Name)
	}

	wantArgs := []Argument{
		{Name: "path", Description: "Path to the airports CSV", Required: true},
		{Name: "extra", Description: "Anything else", Array: true},
	}
	if !reflect.DeepEqual(definition.Arguments, wantArgs) {
		t.Errorf("arguments = %+v", definition.Arguments)
	}

	wantOptions := []Option{
		{Name: "output", Description: "Output gzipped CSV path", AcceptsValue: true},
		{Name: "queue", Shortcut: "Q", Description: "The queue", AcceptsValue: true, Default: []string{"default"}},
		{Name: "model", Description: "Models", AcceptsValue: true, Array: true},
		{Name: "regions", Description: "Regions", AcceptsValue: true, Default: []string{"europe,asia"}},
		{Name: "force", Description: "Re-upload"},
	}
	if !reflect.DeepEqual(definition.Options, wantOptions) {
		t.Errorf("options = %+v", definition.Options)
	}
}

func TestParsesLongAndShortOptions(t *testing.T) {
	definition, _ := ParseSignature("cmd {airport?} {--model=*} {--chunk=1000} {--Q|queue=} {--pretend}")

	in, err := ParseInput(definition, []string{"LHR", "--model=A", "--model", "B", "-Qhigh", "--pretend", "-n", "-vv"})
	if err != nil {
		t.Fatal(err)
	}

	if in.Argument("airport") != "LHR" || !in.HasArgument("airport") {
		t.Errorf("airport = %q", in.Argument("airport"))
	}

	if got := in.OptionList("model"); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("model = %v", got)
	}

	if in.Option("chunk") != "1000" || in.Option("queue") != "high" || !in.Flag("pretend") {
		t.Errorf("chunk=%q queue=%q pretend=%v", in.Option("chunk"), in.Option("queue"), in.Flag("pretend"))
	}

	empty, _ := ParseInput(definition, nil)
	if empty.HasArgument("airport") || empty.Argument("airport") != "" {
		t.Error("an omitted optional argument is absent")
	}
}

func TestInputErrorsAreFullSentences(t *testing.T) {
	definition, _ := ParseSignature("cmd {path} {--force} {--out=}")
	bare, _ := ParseSignature("bare")

	cases := []struct {
		definition Definition
		argv       []string
		want       string
	}{
		{definition, []string{}, `Not enough arguments (missing: "path").`},
		{definition, []string{"a", "b"}, `Too many arguments, expected arguments "path".`},
		{bare, []string{"x"}, `No arguments expected for "bare" command, got "x".`},
		{definition, []string{"a", "--nope"}, `The "--nope" option does not exist.`},
		{definition, []string{"a", "--force=1"}, `The "--force" option does not accept a value.`},
		{definition, []string{"a", "-z"}, `The "-z" option does not exist.`},
	}

	for _, tc := range cases {
		_, err := ParseInput(tc.definition, tc.argv)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: err = %v, want %q", tc.argv, err, tc.want)
		}
	}
}

func TestTypedOptionReaders(t *testing.T) {
	definition, _ := ParseSignature("cmd {--chunk=1000} {--timeout=60} {--port=}")

	in, err := ParseInput(definition, []string{"--port=8080"})
	if err != nil {
		t.Fatal(err)
	}

	if chunk, err := in.IntOption("chunk", 1); err != nil || chunk != 1000 {
		t.Errorf("chunk = %d, %v", chunk, err)
	}

	if port, err := in.IntOption("port", 1); err != nil || port != 8080 {
		t.Errorf("port = %d, %v", port, err)
	}

	if timeout, err := in.SecondsOption("timeout"); err != nil || timeout != time.Minute {
		t.Errorf("timeout = %v, %v", timeout, err)
	}
}

func TestTypedOptionReadersNameTheInvalidOption(t *testing.T) {
	definition, _ := ParseSignature("cmd {--chunk=} {--sleep=}")

	cases := []struct {
		argv []string
		read func(in *Input) error
		want string
	}{
		{[]string{"--chunk=abc"}, intReader("chunk", 1), `The "--chunk" option must be a whole number of at least 1.`},
		{[]string{"--chunk=0"}, intReader("chunk", 1), `The "--chunk" option must be a whole number of at least 1.`},
		{[]string{"--chunk=16abc"}, intReader("chunk", 0), `The "--chunk" option must be a whole number of at least 0.`},
		{nil, intReader("chunk", 0), `The "--chunk" option must be a whole number of at least 0.`},
		{[]string{"--sleep=-1"}, secondsReader("sleep"), `The "--sleep" option must be a whole number of seconds.`},
		{[]string{"--sleep=1.5"}, secondsReader("sleep"), `The "--sleep" option must be a whole number of seconds.`},
	}

	for _, tc := range cases {
		in, err := ParseInput(definition, tc.argv)
		if err != nil {
			t.Fatal(err)
		}

		if err := tc.read(in); err == nil || err.Error() != tc.want {
			t.Errorf("%v: err = %v, want %q", tc.argv, err, tc.want)
		}
	}
}

func intReader(name string, minimum int) func(*Input) error {
	return func(in *Input) error {
		_, err := in.IntOption(name, minimum)

		return err
	}
}

func secondsReader(name string) func(*Input) error {
	return func(in *Input) error {
		_, err := in.SecondsOption(name)

		return err
	}
}

func TestDoubleDashEndsOptions(t *testing.T) {
	definition, _ := ParseSignature("cmd {args*}")

	in, err := ParseInput(definition, []string{"--", "--not-an-option", "x"})
	if err != nil {
		t.Fatal(err)
	}

	if got := in.ArgumentList("args"); !reflect.DeepEqual(got, []string{"--not-an-option", "x"}) {
		t.Errorf("args = %v", got)
	}
}

func TestReadingANameTheSignatureNeverDeclaredPanics(t *testing.T) {
	definition, _ := ParseSignature("cmd {airport?} {--force} {--chunk=}")
	in, err := ParseInput(definition, nil)
	if err != nil {
		t.Fatal(err)
	}

	const (
		noArgument = `console: cmd declares no argument "contry"`
		noOption   = `console: cmd declares no option "--chunck"`
		noFlag     = `console: cmd declares no option "--forse"`
		noValue    = `console: cmd's "--force" takes no value; read it with Flag`
	)
	cases := []struct {
		read string
		call func()
		want string
	}{
		{`Argument("contry")`, func() { in.Argument("contry") }, noArgument},
		{`HasArgument("contry")`, func() { in.HasArgument("contry") }, noArgument},
		{`ArgumentList("contry")`, func() { in.ArgumentList("contry") }, noArgument},
		{`Option("chunck")`, func() { in.Option("chunck") }, noOption},
		{`OptionList("chunck")`, func() { in.OptionList("chunck") }, noOption},
		{`IntOption("chunck")`, func() { _, _ = in.IntOption("chunck", 1) }, noOption},
		{`SecondsOption("chunck")`, func() { _, _ = in.SecondsOption("chunck") }, noOption},
		{`HasOption("forse")`, func() { in.HasOption("forse") }, noFlag},
		{`Flag("forse")`, func() { in.Flag("forse") }, noFlag},
		{`Option("force")`, func() { in.Option("force") }, noValue},
		{`OptionList("force")`, func() { in.OptionList("force") }, noValue},
		{`IntOption("force")`, func() { _, _ = in.IntOption("force", 1) }, noValue},
		{`Flag("quiet")`, func() { in.Flag("quiet") }, ""},
		{`Flag("verbose")`, func() { in.Flag("verbose") }, ""},
		{`Option("env")`, func() { in.Option("env") }, ""},
	}

	for _, tc := range cases {
		t.Run(tc.read, func(t *testing.T) {
			defer func() {
				if got := fmt.Sprint(recover()); tc.want == "" && got != "<nil>" || tc.want != "" && got != tc.want {
					t.Errorf("panic = %s, want %q", got, tc.want)
				}
			}()

			tc.call()
		})
	}
}

type ctxKey struct{}

func TestKernelRunsCommandsWithBoundContext(t *testing.T) {
	var reported []error
	bind := func(ctx context.Context) context.Context { return context.WithValue(ctx, ctxKey{}, "bound") }
	k := NewKernel(bind, KernelConfig{
		Name:   "Flights",
		Report: func(_ context.Context, err error) { reported = append(reported, err) },
	})

	k.Register(
		Func("greet {name} {--shout}", "Say hello", func(c *Context) error {
			message := "Hello " + c.Argument("name") + " (" + c.Value(ctxKey{}).(string) + ")"
			if c.Flag("shout") {
				message = strings.ToUpper(message)
			}

			c.Info(message)
			c.Report(errors.New("noticed"))

			return nil
		}),
		Func("boom", "Panics", func(*Context) error { panic("kaboom") }),
		Func("demo {--force}", "Reads a typo", func(c *Context) error {
			if c.Flag("forse") {
				c.Info("forced")
			}

			return nil
		}),
	)

	var out bytes.Buffer
	if code := k.Run(context.Background(), []string{"greet", "Ana", "--shout"}, &out); code != Success {
		t.Fatalf("code = %d: %s", code, out.String())
	}

	if strings.TrimSpace(out.String()) != "HELLO ANA (BOUND)" {
		t.Errorf("output = %q", out.String())
	}

	if len(reported) != 1 || reported[0].Error() != "noticed" {
		t.Errorf("reported = %v", reported)
	}

	out.Reset()
	if code := k.Run(context.Background(), []string{"boom"}, &out); code != Failure || !strings.Contains(out.String(), "kaboom") {
		t.Errorf("a panic must fail the command: %d %q", code, out.String())
	}

	// The panic is reported with its stack as frames, never as text in its
	// message, so every occurrence groups as one issue.
	if len(reported) != 2 || reported[1].Error() != "console: boom panicked: kaboom" {
		t.Errorf("reported = %q", reported)
	} else if _, ok := errors.AsType[*panics.Error](reported[1]); !ok {
		t.Errorf("reported %v, want a *panics.Error in its chain", reported[1])
	}

	// Reading a name the signature never declared fails the command, loudly,
	// rather than reading as false forever.
	out.Reset()
	if code := k.Run(context.Background(), []string{"demo", "--force"}, &out); code != Failure || !strings.Contains(out.String(), `console: demo declares no option "--forse"`) {
		t.Errorf("an undeclared read must fail the command: %d %q", code, out.String())
	}

	if len(reported) != 3 || reported[2].Error() != `console: demo panicked: console: demo declares no option "--forse"` {
		t.Errorf("reported = %q", reported)
	}

	out.Reset()
	if code := k.Run(context.Background(), []string{"gret"}, &out); code != Failure || !strings.Contains(out.String(), `Command "gret" is not defined.`) || !strings.Contains(out.String(), "greet") {
		t.Errorf("unknown command: %d %q", code, out.String())
	}

	out.Reset()
	if code := k.Run(context.Background(), []string{"greet", "x", "--quiet"}, &out); code != Success || out.Len() != 0 {
		t.Errorf("--quiet must silence output: %q", out.String())
	}
}

// A Handle's error is its exit code: a plain error is printed and fails,
// Failf prints the sentence once, and Exit sets the code without a word.
func TestHandleErrorsBecomeExitCodes(t *testing.T) {
	k := NewKernel(nil, KernelConfig{Name: "Flights"})
	k.Register(
		Func("ok", "Succeeds", func(*Context) error { return nil }),
		Func("broken", "Errors", func(*Context) error { return errors.New("connection refused") }),
		Func("missing", "Fails with a sentence", func(c *Context) error { return c.Failf("File not found: %s.", "x.tsv") }),
		Func("partial", "Exits 3", func(c *Context) error {
			c.Warn("2 of 5 failed.")

			return Exit(3)
		}),
		Func("clean", "Exits Success", func(*Context) error { return Exit(Success) }),
		Func("wrapped", "Wraps an Exit", func(*Context) error { return fmt.Errorf("step: %w", Exit(4)) }),
	)

	cases := []struct {
		argv   []string
		code   int
		output string
	}{
		{[]string{"ok"}, Success, ""},
		{[]string{"broken"}, Failure, "connection refused\n"},
		{[]string{"missing"}, Failure, "File not found: x.tsv.\n"},
		{[]string{"partial"}, 3, "2 of 5 failed.\n"},
		{[]string{"clean"}, Success, ""},
		{[]string{"wrapped"}, 4, ""},
	}

	for _, tc := range cases {
		var out bytes.Buffer
		if code := k.Run(context.Background(), tc.argv, &out); code != tc.code || out.String() != tc.output {
			t.Errorf("%v: code %d, output %q; want %d, %q", tc.argv, code, out.String(), tc.code, tc.output)
		}
	}
}

// Call hands back the called command's exit code as an Exit error, so a
// Handle that returns it exits the same way without printing twice.
// CallSilently drops the called command's output but not why it failed.
func TestCallPassesTheCalledCommandsExitCodeThrough(t *testing.T) {
	k := NewKernel(nil, KernelConfig{Name: "Flights"})
	k.Register(
		Func("inner", "Fails loudly", func(c *Context) error {
			c.Info("inner ran")

			return errors.New("inner failed")
		}),
		Func("partial", "Exits 3", func(c *Context) error {
			c.Warn("2 of 5 failed.")

			return Exit(3)
		}),
		Func("outer", "Calls inner", func(c *Context) error { return c.Call("inner") }),
		Func("quiet-outer", "Calls inner silently", func(c *Context) error { return c.CallSilently("inner") }),
		Func("partial-outer", "Calls partial", func(c *Context) error { return c.Call("partial") }),
		Func("quiet-partial-outer", "Calls partial silently", func(c *Context) error { return c.CallSilently("partial") }),
		Func("tolerant-outer", "Carries on past inner", func(c *Context) error {
			if err := c.CallSilently("inner"); err != nil {
				c.Warnf("inner skipped (%v).", err)
			}

			return nil
		}),
	)

	cases := []struct {
		command string
		code    int
		output  string
	}{
		{"outer", Failure, "inner ran\ninner failed\n"},
		{"quiet-outer", Failure, "inner failed\n"},
		{"partial-outer", 3, "2 of 5 failed.\n"},
		{"quiet-partial-outer", 3, ""},
		{"tolerant-outer", Success, "inner skipped (exit status 1: inner failed).\n"},
	}

	for _, tc := range cases {
		var out bytes.Buffer
		if code := k.Run(context.Background(), []string{tc.command}, &out); code != tc.code || out.String() != tc.output {
			t.Errorf("%s: code %d, output %q; want %d, %q", tc.command, code, out.String(), tc.code, tc.output)
		}
	}
}

func TestFormattedOutputLines(t *testing.T) {
	var buf bytes.Buffer
	out := NewOutput(&buf)

	out.Linef("%d rows", 3)
	out.Infof("Imported %d.", 2)
	out.Commentf("%s:", "Usage")
	out.Warnf("Skipped %q.", "Atlantis")
	out.Errorf("  ✗ %s: %v", "Spain", errors.New("timeout"))

	want := "3 rows\nImported 2.\nUsage:\nSkipped \"Atlantis\".\n  ✗ Spain: timeout\n"
	if buf.String() != want {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestListAndHelp(t *testing.T) {
	k := NewKernel(nil, KernelConfig{Name: "Flights"})
	k.Register(
		Func("bookings:scrub-passenger-data", "Null the PII", func(*Context) error { return nil }),
		Func("model:prune {--model=* : Models to prune}", "Prune models", func(*Context) error { return nil }),
	)

	var out bytes.Buffer
	k.Run(context.Background(), nil, &out)

	list := out.String()
	for _, want := range []string{"Available commands:", " model\n", "model:prune", " bookings\n", "bookings:scrub-passenger-data", "Null the PII"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}

	out.Reset()
	k.Run(context.Background(), []string{"model:prune", "--help"}, &out)

	if help := out.String(); !strings.Contains(help, "Prune models") || !strings.Contains(help, "--model[=MODEL]") || !strings.Contains(help, "multiple values allowed") {
		t.Errorf("help:\n%s", help)
	}
}

func TestDuplicateNamesPanicAtRegistration(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()

	k := NewKernel(nil, KernelConfig{Name: "Flights"})
	k.Register(Func("a", "", nil), Func("a", "", nil))
}
