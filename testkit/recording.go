package testkit

import (
	"fmt"
	"strings"
	"testing"
)

// RecordingT is a testing.TB that records failures instead of failing, for
// testing an assertion itself. Everything else (cleanups, temp dirs) passes
// through to the real test.
type RecordingT struct {
	testing.TB
	failures []string
}

// Record wraps the real test t.
func Record(t testing.TB) *RecordingT {
	t.Helper()

	return &RecordingT{TB: t}
}

// Helper implements testing.TB.
func (r *RecordingT) Helper() {}

// Error records a failure.
func (r *RecordingT) Error(args ...any) { r.failures = append(r.failures, fmt.Sprint(args...)) }

// Errorf records a failure.
func (r *RecordingT) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// Fatal records a failure; unlike testing's, it returns, so the assertion
// may record more (AssertFailed reads the first, where a test would stop).
func (r *RecordingT) Fatal(args ...any) { r.Error(args...) }

// Fatalf records a failure and returns, as Fatal does.
func (r *RecordingT) Fatalf(format string, args ...any) { r.Errorf(format, args...) }

// Failed reports whether anything was recorded.
func (r *RecordingT) Failed() bool { return len(r.failures) > 0 }

// AssertPassed fails the real test if anything was recorded.
func (r *RecordingT) AssertPassed() {
	r.TB.Helper()

	if len(r.failures) > 0 {
		r.TB.Fatalf("unexpected failure: %q", r.failures)
	}
}

// AssertFailed fails the real test unless a failure was recorded and the
// first, the one a real test would stop at, contains message.
func (r *RecordingT) AssertFailed(message string) {
	r.TB.Helper()

	if len(r.failures) == 0 || !strings.Contains(r.failures[0], message) {
		r.TB.Fatalf("failures = %q, want the first to contain %q", r.failures, message)
	}
}

// Summary names what a fake recorded, in first-recorded order with counts
// ("SearchFlights, GetOffers ×3", or "nothing"), for failure messages.
func Summary(names []string) string {
	var order []string

	counts := map[string]int{}
	for _, name := range names {
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}

	if len(order) == 0 {
		return "nothing"
	}

	for i, name := range order {
		if counts[name] > 1 {
			order[i] += fmt.Sprintf(" ×%d", counts[name])
		}
	}

	return strings.Join(order, ", ")
}
