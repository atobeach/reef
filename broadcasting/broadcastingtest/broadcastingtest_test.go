package broadcastingtest_test

import (
	"context"
	"testing"

	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/broadcasting/broadcastingtest"
	"github.com/atobeach/reef/testkit"
)

// broadcast is a fake that published two status changes and a ping.
func broadcast(t *testing.T) *broadcasting.Fake {
	t.Helper()

	fake := &broadcasting.Fake{}
	for _, b := range []struct {
		channels []string
		event    string
	}{{[]string{"orders.1"}, "status.changed"}, {[]string{"orders.2", "admin"}, "status.changed"}, {[]string{"health"}, "ping"}} {
		if err := fake.Broadcast(context.Background(), b.channels, b.event, map[string]int{}); err != nil {
			t.Fatal(err)
		}
	}

	return fake
}

func TestAssertionsMatchByChannelAndEventAndFailListingWhatWentOut(t *testing.T) {
	cases := []struct {
		name   string
		assert func(t *testkit.RecordingT, f *broadcasting.Fake)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"on a channel", func(t *testkit.RecordingT, f *broadcasting.Fake) {
			broadcastingtest.AssertBroadcastOn(t, f, "admin", "status.changed")
		}, ""},
		{"on another channel", func(t *testkit.RecordingT, f *broadcasting.Fake) {
			broadcastingtest.AssertBroadcastOn(t, f, "orders.3", "status.changed")
		}, "status.changed not broadcast on orders.3; broadcast: status.changed on orders.1, status.changed on orders.2,admin, ping on health"},
		{"times", func(t *testkit.RecordingT, f *broadcasting.Fake) {
			broadcastingtest.AssertBroadcastTimes(t, f, "status.changed", 2)
		}, ""},
		{"times off", func(t *testkit.RecordingT, f *broadcasting.Fake) {
			broadcastingtest.AssertBroadcastTimes(t, f, "ping", 2)
		}, "ping broadcast 1 times, want 2"},
		{"count", func(t *testkit.RecordingT, f *broadcasting.Fake) { broadcastingtest.AssertBroadcastCount(t, f, 3) }, ""},
		{"count off", func(t *testkit.RecordingT, f *broadcasting.Fake) {
			broadcastingtest.AssertBroadcastCount(t, f, 2)
		}, "3 broadcasts, want 2; broadcast: status.changed on orders.1, status.changed on orders.2,admin, ping on health"},
		{"nothing", func(t *testkit.RecordingT, f *broadcasting.Fake) { broadcastingtest.AssertNothingBroadcast(t, f) }, "3 broadcasts, want none"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recording := testkit.Record(t)
			c.assert(recording, broadcast(t))

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}

func TestAssertNothingBroadcastPassesOnAnEmptyFake(t *testing.T) {
	recording := testkit.Record(t)
	broadcastingtest.AssertNothingBroadcast(recording, &broadcasting.Fake{})

	recording.AssertPassed()
}
