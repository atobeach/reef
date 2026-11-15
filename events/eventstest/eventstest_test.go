package eventstest_test

import (
	"context"
	"testing"

	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/broadcasting/broadcastingtest"
	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/events/eventstest"
	"github.com/atobeach/reef/testkit"
)

type orderPlaced struct{ ID int }

type statusChanged struct{ ID int }

func (statusChanged) BroadcastOn() []broadcasting.Channel {
	return []broadcasting.Channel{broadcasting.NewChannel("orders.1")}
}
func (statusChanged) BroadcastAs() string  { return "status.changed" }
func (e statusChanged) BroadcastWith() any { return map[string]int{"id": e.ID} }

func TestFakeRecordsOnlyTheFakedTypes(t *testing.T) {
	broadcasts := &broadcasting.Fake{}
	d := events.NewDispatcher(broadcasts)

	ran := 0
	events.Listen(d, func(context.Context, orderPlaced) error { ran++; return nil })

	d.Fake(statusChanged{})

	for _, event := range []any{statusChanged{ID: 1}, statusChanged{ID: 2}, orderPlaced{ID: 1}} {
		if err := d.Dispatch(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	eventstest.AssertDispatchedTimes[statusChanged](t, d, 2)
	eventstest.AssertDispatched(t, d, func(e statusChanged) bool { return e.ID == 2 })

	// Unfaked listeners still run; faked events don't broadcast.
	if ran != 1 {
		t.Fatalf("the unfaked listener ran %d times, want 1", ran)
	}

	broadcastingtest.AssertNothingBroadcast(t, broadcasts)
}

func TestAssertionsFailListingWhatWasDispatched(t *testing.T) {
	cases := []struct {
		name   string
		assert func(t *testkit.RecordingT, d *events.Dispatcher)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"dispatched", func(t *testkit.RecordingT, d *events.Dispatcher) {
			eventstest.AssertDispatched(t, d, func(e orderPlaced) bool { return e.ID == 7 })
		}, ""},
		{"not dispatched", func(t *testkit.RecordingT, d *events.Dispatcher) {
			eventstest.AssertDispatched(t, d, func(e orderPlaced) bool { return e.ID == 8 })
		}, "no eventstest_test.orderPlaced dispatched matching the callback; dispatched: eventstest_test.orderPlaced, eventstest_test.statusChanged"},
		{"times", func(t *testkit.RecordingT, d *events.Dispatcher) {
			eventstest.AssertDispatchedTimes[statusChanged](t, d, 1)
		}, ""},
		{"times off", func(t *testkit.RecordingT, d *events.Dispatcher) {
			eventstest.AssertDispatchedTimes[statusChanged](t, d, 0)
		}, "eventstest_test.statusChanged dispatched 1 times, want 0"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := events.NewDispatcher(nil)
			d.Fake()

			for _, event := range []any{orderPlaced{ID: 7}, statusChanged{ID: 3}} {
				if err := d.Dispatch(context.Background(), event); err != nil {
					t.Fatal(err)
				}
			}

			recording := testkit.Record(t)
			c.assert(recording, d)

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}
