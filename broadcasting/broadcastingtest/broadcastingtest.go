// Package broadcastingtest asserts on what a broadcasting.Fake recorded. A
// failure lists the broadcasts that went out.
//
//	broadcastingtest.AssertBroadcastOn(t, fake, "orders.1", "status.changed")
package broadcastingtest

import (
	"slices"
	"strings"
	"testing"

	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/testkit"
)

// AssertBroadcastOn fails unless event went out on channel.
func AssertBroadcastOn(t testing.TB, f *broadcasting.Fake, channel string, event string) {
	t.Helper()

	for _, sent := range f.Sent() {
		if sent.Event == event && slices.Contains(sent.Channels, channel) {
			return
		}
	}

	t.Fatalf("broadcasting: %s not broadcast on %s; broadcast: %s", event, channel, summary(f))
}

// AssertBroadcastTimes fails unless event went out exactly n times.
func AssertBroadcastTimes(t testing.TB, f *broadcasting.Fake, event string, n int) {
	t.Helper()

	got := 0
	for _, sent := range f.Sent() {
		if sent.Event == event {
			got++
		}
	}

	if got != n {
		t.Fatalf("broadcasting: %s broadcast %d times, want %d; broadcast: %s", event, got, n, summary(f))
	}
}

// AssertBroadcastCount fails unless exactly n broadcasts went out, of any
// event.
func AssertBroadcastCount(t testing.TB, f *broadcasting.Fake, n int) {
	t.Helper()

	if got := len(f.Sent()); got != n {
		t.Fatalf("broadcasting: %d broadcasts, want %d; broadcast: %s", got, n, summary(f))
	}
}

// AssertNothingBroadcast fails if anything went out.
func AssertNothingBroadcast(t testing.TB, f *broadcasting.Fake) {
	t.Helper()

	if sent := f.Sent(); len(sent) > 0 {
		t.Fatalf("broadcasting: %d broadcasts, want none; broadcast: %s", len(sent), summary(f))
	}
}

// summary names each broadcast with its channels.
func summary(f *broadcasting.Fake) string {
	sent := f.Sent()

	names := make([]string, len(sent))
	for i, s := range sent {
		names[i] = s.Event + " on " + strings.Join(s.Channels, ",")
	}

	return testkit.Summary(names)
}
