package events

import (
	"context"
	"errors"
	"testing"

	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/broadcasting/broadcastingtest"
)

type orderPlaced struct{ ID int }

type statusChanged struct{ ID int }

func (e statusChanged) BroadcastOn() []broadcasting.Channel {
	return []broadcasting.Channel{broadcasting.NewChannel("orders.1")}
}
func (statusChanged) BroadcastAs() string  { return "status.changed" }
func (e statusChanged) BroadcastWith() any { return map[string]int{"id": e.ID} }

func TestListenersRunThenBroadcastableEventsPublish(t *testing.T) {
	broadcasts := &broadcasting.Fake{}
	d := NewDispatcher(broadcasts)

	var seen []int
	Listen(d, func(_ context.Context, e orderPlaced) error {
		seen = append(seen, e.ID)

		return nil
	})

	if err := d.Dispatch(context.Background(), orderPlaced{ID: 7}); err != nil {
		t.Fatal(err)
	}

	if err := d.Dispatch(context.Background(), statusChanged{ID: 3}); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 1 || seen[0] != 7 {
		t.Fatalf("the listener saw %v", seen)
	}

	broadcastingtest.AssertBroadcastOn(t, broadcasts, "orders.1", "status.changed")

	if sent := broadcasts.Sent(); len(sent) != 1 || string(sent[0].Payload) != `{"id":3}` {
		t.Fatalf("broadcast %+v, want only the status change with its payload", sent)
	}
}

func TestAListenerErrorPropagatesAndStopsTheBroadcast(t *testing.T) {
	broadcasts := &broadcasting.Fake{}
	d := NewDispatcher(broadcasts)

	Listen(d, func(context.Context, statusChanged) error { return errors.New("refused") })

	if err := d.Dispatch(context.Background(), statusChanged{}); err == nil {
		t.Fatal("expected the listener's error")
	}

	// Nothing is broadcast after a failing listener.
	broadcastingtest.AssertNothingBroadcast(t, broadcasts)
}
