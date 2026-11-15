// Package queuetest reads and asserts on the jobs a queue.Fake recorded.
// A failure lists the jobs that were pushed.
//
//	queuetest.AssertPushed(t, fake, func(job SendInvoice) bool { return job.OrderID == 7 })
package queuetest

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/atobeach/reef/queue"
	"github.com/atobeach/reef/testkit"
)

// Pushed is the recorded jobs of type T, in dispatch order.
func Pushed[T queue.Job](f *queue.Fake) []T {
	var jobs []T
	for _, job := range f.All() {
		if typed, ok := job.(T); ok {
			jobs = append(jobs, typed)
		}
	}

	return jobs
}

// AssertPushed fails unless a recorded job of type T satisfies match.
func AssertPushed[T queue.Job](t testing.TB, f *queue.Fake, match func(job T) bool) {
	t.Helper()

	for _, job := range Pushed[T](f) {
		if match(job) {
			return
		}
	}

	t.Fatalf("queue: no %s pushed matching the callback; pushed: %s", reflect.TypeFor[T](), summary(f))
}

// AssertPushedTimes fails unless exactly n jobs of type T were recorded.
func AssertPushedTimes[T queue.Job](t testing.TB, f *queue.Fake, n int) {
	t.Helper()

	if got := len(Pushed[T](f)); got != n {
		t.Fatalf("queue: %s pushed %d times, want %d; pushed: %s", reflect.TypeFor[T](), got, n, summary(f))
	}
}

// AssertCount fails unless exactly n jobs were recorded, of any type.
func AssertCount(t testing.TB, f *queue.Fake, n int) {
	t.Helper()

	if got := len(f.All()); got != n {
		t.Fatalf("queue: %d jobs pushed, want %d; pushed: %s", got, n, summary(f))
	}
}

// AssertNothingPushed fails if any job was recorded.
func AssertNothingPushed(t testing.TB, f *queue.Fake) {
	t.Helper()

	if pushed := f.All(); len(pushed) > 0 {
		t.Fatalf("queue: %d jobs pushed, want none; pushed: %s", len(pushed), summary(f))
	}
}

// summary names the recorded jobs.
func summary(f *queue.Fake) string {
	pushed := f.All()

	names := make([]string, len(pushed))
	for i, job := range pushed {
		names[i] = fmt.Sprintf("%T", job)
	}

	return testkit.Summary(names)
}
