package clocktest_test

import (
	"context"
	"testing"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/testkit"
)

func TestAFrozenClockReadsItsInstantUntilMoved(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ctx, now := clocktest.FrozenAt(context.Background(), at)

	if got := clock.Now(ctx); !got.Equal(at) {
		t.Fatalf("Now() = %v, want %v", got, at)
	}

	now.Travel(time.Hour)
	if got := clock.Now(ctx); !got.Equal(at.Add(time.Hour)) {
		t.Fatalf("after travel Now() = %v", got)
	}
}

func TestAssertSleptMatchesTheRecordedPausesInOrder(t *testing.T) {
	cases := []struct {
		name  string
		slept []time.Duration
		want  []time.Duration
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"the pauses, in order", []time.Duration{time.Second, time.Minute}, []time.Duration{time.Second, time.Minute}, ""},
		{"never paused", nil, nil, ""},
		{"out of order", []time.Duration{time.Second, time.Minute}, []time.Duration{time.Minute, time.Second}, "clock: slept [1s 1m0s], want [1m0s 1s]"},
		{"paused when it shouldn't", []time.Duration{time.Second}, nil, "clock: slept [1s], want []"},
		{"never paused when it should", nil, []time.Duration{time.Second}, "clock: slept [], want [1s]"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, sleeps := clocktest.FakeSleep(context.Background())
			for _, d := range c.slept {
				clock.Sleep(ctx, d)
			}

			recording := testkit.Record(t)
			clocktest.AssertSlept(recording, sleeps, c.want...)

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}
