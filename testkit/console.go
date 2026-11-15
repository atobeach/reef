package testkit

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/atobeach/reef/console"
)

// CommandResult is a finished console command run, with assertions on its
// exit code and output.
type CommandResult struct {
	t      testing.TB
	name   string
	Code   int
	Output string
}

// RunCommand runs a console command through the kernel, capturing its output.
func RunCommand(t testing.TB, ctx context.Context, kernel *console.Kernel, argv ...string) *CommandResult {
	t.Helper()

	var out bytes.Buffer
	code := kernel.Run(ctx, argv, &out)

	return &CommandResult{t: t, name: strings.Join(argv, " "), Code: code, Output: out.String()}
}

// AssertSuccessful fails unless the command exited 0.
func (r *CommandResult) AssertSuccessful() *CommandResult {
	r.t.Helper()

	return r.AssertExitCode(console.Success)
}

// AssertFailed fails unless the command exited non-zero.
func (r *CommandResult) AssertFailed() *CommandResult {
	r.t.Helper()

	if r.Code == console.Success {
		r.t.Errorf("command %s: expected failure, exited 0\n%s", r.name, r.Output)
	}

	return r
}

// AssertExitCode fails unless the command exited with code.
func (r *CommandResult) AssertExitCode(code int) *CommandResult {
	r.t.Helper()

	if r.Code != code {
		r.t.Errorf("command %s: exit code %d, want %d\n%s", r.name, r.Code, code, r.Output)
	}

	return r
}

// ExpectsOutput fails unless some output line equals line.
func (r *CommandResult) ExpectsOutput(line string) *CommandResult {
	r.t.Helper()

	for _, got := range strings.Split(r.Output, "\n") {
		if got == line {
			return r
		}
	}

	r.t.Errorf("command %s: output lacks line %q\n%s", r.name, line, r.Output)

	return r
}

// ExpectsOutputToContain fails unless the output contains text.
func (r *CommandResult) ExpectsOutputToContain(text string) *CommandResult {
	r.t.Helper()

	if !strings.Contains(r.Output, text) {
		r.t.Errorf("command %s: output lacks %q\n%s", r.name, text, r.Output)
	}

	return r
}

// DoesntExpectOutputToContain fails if the output contains text.
func (r *CommandResult) DoesntExpectOutputToContain(text string) *CommandResult {
	r.t.Helper()

	if strings.Contains(r.Output, text) {
		r.t.Errorf("command %s: output unexpectedly has %q\n%s", r.name, text, r.Output)
	}

	return r
}
