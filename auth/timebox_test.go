package auth

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// A miss takes the whole box however fast it was, a path that leaves early
// or a caller that went away takes none of it, and the function's own value
// and error come back every time. The bubble's clock makes elapsed time
// exact.
func TestTimeboxWaitsOutTheBoxUnlessLeftEarly(t *testing.T) {
	const box = time.Second
	errMiss := errors.New("miss")

	for _, tt := range []struct {
		name      string
		work      time.Duration
		early     bool
		cancelled bool
		want      time.Duration
	}{
		{name: "a fast miss takes the box", want: box},
		{name: "a slow miss takes its own time", work: 2 * box, want: 2 * box},
		{name: "leaving early takes no box", work: box / 4, early: true, want: box / 4},
		{name: "a cancelled caller ends the wait", cancelled: true, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				if tt.cancelled {
					cancel()
				}

				started := time.Now()
				value, err := Timebox(ctx, box, func(returnEarly func()) (int, error) {
					time.Sleep(tt.work)

					if tt.early {
						returnEarly()
					}

					return 42, errMiss
				})

				if took := time.Since(started); took != tt.want {
					t.Errorf("took %v, want %v", took, tt.want)
				}

				if value != 42 || !errors.Is(err, errMiss) {
					t.Errorf("Timebox = %d, %v; want fn's 42, %v", value, err, errMiss)
				}
			})
		})
	}
}
