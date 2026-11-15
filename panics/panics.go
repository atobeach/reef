// Package panics keeps a recovered panic as an error that knows where it
// happened, for the log line and the error tracker.
package panics

import (
	"fmt"
	"runtime"
	"strings"
)

// maxFrames bounds the stack kept: the panic site and the calls that led
// to it, deep enough for a tool run under the kernel's middleware.
const maxFrames = 64

// Error is a recovered panic: the value it was raised with and the stack it
// unwound. It has no Unwrap: a panic is a bug, so a panicked error that knows
// its status (a 404, a 422) never renders as one.
type Error struct {
	Value any
	pcs   []uintptr
}

// New keeps recovered with the panicking goroutine's stack. Call it while the
// panic is being recovered: in the deferred function, or a helper it calls.
func New(recovered any) *Error {
	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(2, pcs)

	return &Error{Value: recovered, pcs: pcs[:n]}
}

// Error is the value's text, as `panic(value)` prints it.
func (e *Error) Error() string {
	return fmt.Sprint(e.Value)
}

// Frames are the stack from the panic outward, the panicking frame (or the
// runtime's, for a runtime error) first. The recovery itself, and a deferred
// function that panicked again, are left out. The stack can't tell a
// re-raise from a new panic, so one raised afresh by a deferred function
// while another unwinds reads as the first: run cleanup that may panic once
// no panic is in flight, as the kernel does its Terminating callbacks.
func (e *Error) Frames() []runtime.Frame {
	var frames []runtime.Frame

	callers := runtime.CallersFrames(e.pcs)
	for {
		frame, more := callers.Next()
		frames = append(frames, frame)

		if !more {
			break
		}
	}

	// The outermost gopanic is the original panic: everything inside it is
	// the recovery, and any panic raised again while unwinding.
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Function == "runtime.gopanic" {
			return frames[i+1:]
		}
	}

	return frames
}

// Stack is Frames as text, "function\n\tfile:line\n" per frame, for a log
// line.
func (e *Error) Stack() string {
	var stack strings.Builder
	for _, frame := range e.Frames() {
		fmt.Fprintf(&stack, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
	}

	return stack.String()
}
