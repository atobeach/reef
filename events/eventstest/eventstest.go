// Package eventstest asserts on the events a faked events.Dispatcher
// recorded. A failure lists the events that were dispatched.
//
//	d.Fake(OrderShipped{})
//	eventstest.AssertDispatched(t, d, func(e OrderShipped) bool { return e.ID == 7 })
package eventstest

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/testkit"
)

// AssertDispatched fails unless a recorded event of type T satisfies match.
func AssertDispatched[T any](t testing.TB, d *events.Dispatcher, match func(event T) bool) {
	t.Helper()

	for _, event := range events.Dispatched[T](d) {
		if match(event) {
			return
		}
	}

	t.Fatalf("events: no %s dispatched matching the callback; dispatched: %s", reflect.TypeFor[T](), summary(d))
}

// AssertDispatchedTimes fails unless exactly n events of type T were
// recorded.
func AssertDispatchedTimes[T any](t testing.TB, d *events.Dispatcher, n int) {
	t.Helper()

	if got := len(events.Dispatched[T](d)); got != n {
		t.Fatalf("events: %s dispatched %d times, want %d; dispatched: %s", reflect.TypeFor[T](), got, n, summary(d))
	}
}

// summary names the recorded events.
func summary(d *events.Dispatcher) string {
	dispatched := events.Dispatched[any](d)

	names := make([]string, len(dispatched))
	for i, event := range dispatched {
		names[i] = fmt.Sprintf("%T", event)
	}

	return testkit.Summary(names)
}
