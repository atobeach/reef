package auth

import (
	"context"
	"time"
)

// Timebox runs fn and waits out the rest of d, so a failed credential check
// can't be timed to enumerate users. fn calls returnEarly on a path that may
// finish fast (a successful login reveals nothing new). fn's value and error
// come back as it returned them; a cancelled ctx ends the wait. The box is
// measured on the wall clock, never the context's.
func Timebox[T any](ctx context.Context, d time.Duration, fn func(returnEarly func()) (T, error)) (T, error) {
	started := time.Now()
	early := false

	value, err := fn(func() { early = true })

	if remaining := d - time.Since(started); !early && remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}

	return value, err
}
