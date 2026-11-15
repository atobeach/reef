package panics_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/atobeach/reef/panics"
)

// recovering runs fn and keeps its panic through a helper between the
// recover and New, as the kernel's guard and the AI run do.
func recovering(fn func()) (err *panics.Error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = keep(recovered)
		}
	}()

	fn()

	return nil
}

func keep(recovered any) *panics.Error {
	return panics.New(recovered)
}

//go:noinline
func indexOutOfRange(items []int, i int) int {
	return items[i]
}

//go:noinline
func boom() {
	panic("boom")
}

// rolledBack panics, and its deferred function panics again with the value,
// as db.Transaction does after its rollback.
//
//go:noinline
func rolledBack() {
	defer func() {
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
	}()

	boom()
}

// firstOutsideRuntime is the first frame a reader of the stack would look
// at: a runtime error's first frames are the runtime's own.
func firstOutsideRuntime(t *testing.T, err *panics.Error) runtime.Frame {
	t.Helper()

	for _, frame := range err.Frames() {
		if !strings.HasPrefix(frame.Function, "runtime.") {
			return frame
		}
	}

	t.Fatalf("no frame outside the runtime in\n%s", err.Stack())

	return runtime.Frame{}
}

func TestTheFramesStartWhereThePanicHappened(t *testing.T) {
	cases := []struct {
		name     string
		panics   func()
		function string
		message  string
	}{
		{"a runtime error", func() { indexOutOfRange([]int{1}, 3) }, "panics_test.indexOutOfRange", "runtime error: index out of range [3] with length 1"},
		{"a plain value", boom, "panics_test.boom", "boom"},
		{"a value panicked again while unwinding", rolledBack, "panics_test.boom", "boom"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := recovering(tc.panics)
			if err == nil {
				t.Fatal("nothing panicked")
			}

			if got := firstOutsideRuntime(t, err).Function; !strings.HasSuffix(got, tc.function) {
				t.Fatalf("first frame = %s, want %s; stack:\n%s", got, tc.function, err.Stack())
			}
			if err.Error() != tc.message {
				t.Fatalf("Error() = %q, want %q", err.Error(), tc.message)
			}
		})
	}
}

// A panic is a bug, never the expected error it was raised with.
func TestAPanickedErrorIsNotTheErrorItCarries(t *testing.T) {
	err := recovering(func() { panic(context.Canceled) })

	if errors.Is(err, context.Canceled) {
		t.Fatal("a panicked context.Canceled must not read as a cancellation")
	}
	if value, ok := err.Value.(error); !ok || !errors.Is(value, context.Canceled) || err.Error() != "context canceled" {
		t.Fatalf("Value = %v, Error() = %q", err.Value, err.Error())
	}
}

func TestTheStackNamesEachFrameAndWhereItIs(t *testing.T) {
	err := recovering(boom)

	frames := err.Frames()
	stack := err.Stack()
	for _, frame := range frames {
		want := fmt.Sprintf("%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !strings.Contains(stack, want) {
			t.Fatalf("stack lacks %q:\n%s", want, stack)
		}
	}

	if !strings.HasPrefix(stack, frames[0].Function+"\n\t") || !strings.Contains(stack, "panics_test.go:") {
		t.Fatalf("stack =\n%s", stack)
	}
	if strings.Contains(stack, "runtime.gopanic") || strings.Contains(stack, "panics_test.keep") {
		t.Fatalf("the recovery is part of the stack:\n%s", stack)
	}
}
