package clock

import (
	"context"
	"sync"
	"time"
)

// dateTimeLayout is a timestamp(0) column's text: 'Y-m-d H:i:s' in the app
// timezone (UTC), microseconds dropped.
const dateTimeLayout = "2006-01-02 15:04:05"

// Clock is the app's time, the wall clock in UTC until a test freezes or
// moves it. It rides the context, so one test's time travel never moves
// another's. A nil Clock is the wall clock.
type Clock struct {
	mu        sync.Mutex
	frozen    *time.Time
	fakeSleep bool
	slept     []time.Duration
}

type contextKey struct{}

// With binds c to ctx, for Now and Sleep to read.
func With(ctx context.Context, c *Clock) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// Now is the current time on ctx's clock, in UTC (the app's timezone).
func Now(ctx context.Context) time.Time {
	return from(ctx).Now()
}

// Sleep pauses for d, or until ctx ends. On a clock whose sleep a test faked
// it only records d, so paced code tests instantly.
func Sleep(ctx context.Context, d time.Duration) {
	if from(ctx).recordSleep(d) || d <= 0 {
		return
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func from(ctx context.Context) *Clock {
	c, _ := ctx.Value(contextKey{}).(*Clock)

	return c
}

// Now is the clock's current time in UTC.
func (c *Clock) Now() time.Time {
	if c == nil {
		return time.Now().UTC()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now()
}

// TravelTo freezes the clock at t.
func (c *Clock) TravelTo(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	frozen := t.UTC()
	c.frozen = &frozen
}

// Travel moves the clock by d, freezing it first when it isn't frozen yet.
func (c *Clock) Travel(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	moved := c.now().Add(d)
	c.frozen = &moved
}

// now reads the clock; c.mu is held.
func (c *Clock) now() time.Time {
	if c.frozen != nil {
		return *c.frozen
	}

	return time.Now().UTC()
}

// FakeSleep makes Sleep record its pauses instead of taking them.
func (c *Clock) FakeSleep() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.fakeSleep = true
}

// Slept lists the pauses a faked Sleep recorded, in order.
func (c *Clock) Slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.slept...)
}

// recordSleep records d and reports true when the clock's sleep is faked.
func (c *Clock) recordSleep(d time.Duration) bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fakeSleep {
		c.slept = append(c.slept, d)
	}

	return c.fakeSleep
}

// DateTimeString renders t as 'Y-m-d H:i:s' in UTC, for raw column text and
// a raw statement's binding: Postgres rounds a bound time's fraction on a
// `timestamp(0)` column where the builder truncates.
func DateTimeString(t time.Time) string {
	return t.UTC().Format(dateTimeLayout)
}

// ISOString renders t in isoLayout: UTC with microseconds and Z.
func ISOString(t time.Time) string {
	return t.UTC().Format(isoLayout)
}
