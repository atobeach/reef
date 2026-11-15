// Package clocktest gives a test a clock of its own on a context, frozen or
// with Sleep faked, and asserts on the pauses a faked Sleep recorded.
//
//	ctx, now := clocktest.FrozenAt(t.Context(), at)
//	now.Travel(time.Hour)
//
//	ctx, sleeps := clocktest.FakeSleep(t.Context())
//	clocktest.AssertSlept(t, sleeps, 30*time.Second)
package clocktest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/atobeach/reef/clock"
)

// FrozenAt binds ctx a clock of its own, frozen at instant until the test
// moves it.
func FrozenAt(ctx context.Context, instant time.Time) (context.Context, *clock.Clock) {
	c := &clock.Clock{}
	c.TravelTo(instant)

	return clock.With(ctx, c), c
}

// FakeSleep binds ctx a clock of its own whose Sleep records its pauses
// instead of taking them.
func FakeSleep(ctx context.Context) (context.Context, *clock.Clock) {
	c := &clock.Clock{}
	c.FakeSleep()

	return clock.With(ctx, c), c
}

// AssertSlept fails unless c's faked Sleep paused for exactly want, in order;
// with no want, unless it never paused.
func AssertSlept(t testing.TB, c *clock.Clock, want ...time.Duration) {
	t.Helper()

	if slept := c.Slept(); !slices.Equal(slept, want) {
		t.Fatalf("clock: slept %v, want %v", slept, want)
	}
}
