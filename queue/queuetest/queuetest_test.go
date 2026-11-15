package queuetest_test

import (
	"context"
	"testing"

	"github.com/atobeach/reef/queue"
	"github.com/atobeach/reef/queue/queuetest"
	"github.com/atobeach/reef/testkit"
)

type ping struct{ N int }

func (ping) Kind() string { return "ping" }

type pong struct{}

func (pong) Kind() string { return "pong" }

// pushed is a fake that recorded two pings and a pong.
func pushed(t *testing.T) *queue.Fake {
	t.Helper()

	fake := &queue.Fake{}
	for _, job := range []queue.Job{ping{1}, pong{}, ping{2}} {
		if err := fake.Dispatch(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}

	return fake
}

func TestAssertionsMatchByJobTypeAndFailListingWhatWasPushed(t *testing.T) {
	cases := []struct {
		name   string
		assert func(t *testkit.RecordingT, f *queue.Fake)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"pushed", func(t *testkit.RecordingT, f *queue.Fake) {
			queuetest.AssertPushed(t, f, func(job ping) bool { return job.N == 2 })
		}, ""},
		{"not pushed", func(t *testkit.RecordingT, f *queue.Fake) {
			queuetest.AssertPushed(t, f, func(job ping) bool { return job.N == 3 })
		}, "no queuetest_test.ping pushed matching the callback; pushed: queuetest_test.ping ×2, queuetest_test.pong"},
		{"times", func(t *testkit.RecordingT, f *queue.Fake) { queuetest.AssertPushedTimes[ping](t, f, 2) }, ""},
		{"times off", func(t *testkit.RecordingT, f *queue.Fake) { queuetest.AssertPushedTimes[pong](t, f, 2) }, "queuetest_test.pong pushed 1 times, want 2"},
		{"count", func(t *testkit.RecordingT, f *queue.Fake) { queuetest.AssertCount(t, f, 3) }, ""},
		{"count off", func(t *testkit.RecordingT, f *queue.Fake) { queuetest.AssertCount(t, f, 1) }, "3 jobs pushed, want 1"},
		{"nothing pushed", func(t *testkit.RecordingT, f *queue.Fake) { queuetest.AssertNothingPushed(t, f) }, "3 jobs pushed, want none"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recording := testkit.Record(t)
			c.assert(recording, pushed(t))

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}

func TestPushedReadsTheJobsOfOneTypeInDispatchOrder(t *testing.T) {
	if pings := queuetest.Pushed[ping](pushed(t)); len(pings) != 2 || pings[0].N != 1 || pings[1].N != 2 {
		t.Fatalf("pings = %v", pings)
	}

	if pongs := queuetest.Pushed[pong](&queue.Fake{}); len(pongs) != 0 {
		t.Fatalf("an empty fake pushed %v", pongs)
	}
}

func TestAssertNothingPushedPassesOnAnEmptyFake(t *testing.T) {
	recording := testkit.Record(t)
	queuetest.AssertNothingPushed(recording, &queue.Fake{})

	recording.AssertPassed()
}
