// Package console is the command-line interface: commands declared by a
// signature, a kernel that parses argv with the usual long/short option
// conventions, and the output helpers commands write with.
//
//	type ImportCountries struct{}
//
//	func (ImportCountries) Signature() string   { return "countries:import {path : The JSON export}" }
//	func (ImportCountries) Description() string { return "Import countries from a JSON export" }
//
//	func (ImportCountries) Handle(c *console.Context) error {
//		path := c.Argument("path")
//		if _, err := os.Stat(path); err != nil {
//			return c.Failf("File not found: %s", path)
//		}
//
//		count, err := importCountries(c, path)
//		if err != nil {
//			return err
//		}
//
//		c.Infof("Imported %d countries.", count)
//
//		return nil
//	}
//
// A Handle that returns nil exits 0; any other error is printed and exits 1.
// c.Failf prints a sentence for the operator and fails the same way; Exit
// sets another code silently. `<command> [options] [arguments]` runs one,
// `list` lists them.
package console

import (
	"context"
	"errors"
	"strconv"
)

// Exit codes: what Kernel.Run returns and Exit takes.
const (
	Success = 0
	Failure = 1
)

// Command is one console command.
type Command interface {
	// Signature is the command's name, {arguments} and {--options}; see
	// ParseSignature.
	Signature() string
	// Description is the one-liner `list` shows.
	Description() string
	// Handle runs the command; its error decides the exit code (see the
	// package doc).
	Handle(c *Context) error
}

// exitError ends a command with code, printing only the reason a silenced
// command gave: otherwise the command has already said why.
type exitError struct {
	code   int
	reason string
}

func (e *exitError) Error() string {
	status := "exit status " + strconv.Itoa(e.code)
	if e.reason == "" {
		return status
	}

	return status + ": " + e.reason
}

// Exit ends a command with code, printing nothing; Exit(Success) is nil.
func Exit(code int) error {
	if code == Success {
		return nil
	}

	return &exitError{code: code}
}

// exitCode is a Handle's error as the process exit code.
func exitCode(out *Output, err error) int {
	if err == nil {
		return Success
	}

	var exit *exitError
	if errors.As(err, &exit) {
		if exit.reason != "" {
			out.Error(exit.reason)
		}

		return exit.code
	}

	out.Error(err.Error())

	return Failure
}

// Context is what a command's Handle receives: the cancellation context
// (with the database connection bound, so queries take it directly), the
// parsed input and the output.
type Context struct {
	context.Context
	*Input
	*Output
	kernel *Kernel
}

// Failf prints a failure line, a full sentence for the operator ("File not
// found: x") rather than a Go error string, and returns Exit(Failure) for
// Handle to return.
func (c *Context) Failf(format string, args ...any) error {
	c.Errorf(format, args...)

	return Exit(Failure)
}

// Report hands an error to the kernel's reporter without failing the
// command; without a reporter it is printed.
func (c *Context) Report(err error) {
	if err == nil {
		return
	}

	if c.kernel != nil && c.kernel.config.Report != nil {
		c.kernel.config.Report(c, err)

		return
	}

	c.Error(err.Error())
}

// Call runs another command with the same output. A failure comes back as
// an Exit error with the command's code, its message already printed, so a
// Handle can return it as it is.
func (c *Context) Call(name string, args ...string) error {
	if c.kernel == nil {
		return errors.New("console: Call needs a kernel")
	}

	return Exit(c.kernel.Call(c.Context, name, args, c.Output))
}

// CallSilently is Call with the other command's output discarded, except
// why it failed: the error carries the lines it printed as errors, for a
// Handle returning it to print.
func (c *Context) CallSilently(name string, args ...string) error {
	if c.kernel == nil {
		return errors.New("console: CallSilently needs a kernel")
	}

	out := silenced()
	code := c.kernel.Call(c.Context, name, args, out)
	if code == Success {
		return nil
	}

	return &exitError{code: code, reason: out.keptErrors()}
}

// HasCommand reports whether the running kernel defines a command.
func (c *Context) HasCommand(name string) bool {
	return c.kernel != nil && c.kernel.Has(name)
}

// funcCommand is a command defined inline by Func.
type funcCommand struct {
	signature   string
	description string
	handle      func(c *Context) error
}

// Signature implements Command.
func (f funcCommand) Signature() string { return f.signature }

// Description implements Command.
func (f funcCommand) Description() string { return f.description }

// Handle implements Command.
func (f funcCommand) Handle(c *Context) error { return f.handle(c) }

// Func defines a command inline, without a type of its own.
func Func(signature, description string, handle func(c *Context) error) Command {
	return funcCommand{signature: signature, description: description, handle: handle}
}
