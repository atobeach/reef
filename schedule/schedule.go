// Package schedule runs the app's periodic tasks with fluent frequencies,
// driven by `schedule:work` (a long-running loop) or `schedule:run` (one
// tick, for cron).
//
//	s.AlwaysOnOneServer()
//	s.Command("bookings:send-reminders").EveryMinute().WithoutOverlapping()
//	s.Call(warmRates).Name("exchange-rates:warm").Hourly()
//
// Both locks live in the cache store, so no scheduler needs a database
// session (production reaches Postgres through a transaction pooler). The
// scheduler that claims a minute's leader key runs its one-server events, so
// a dead leader costs at most that minute; a WithoutOverlapping event holds
// its MutexName lock for the run.
package schedule

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Schedule is the set of scheduled events.
type Schedule struct {
	events      []*Event
	onOneServer bool
}

// New builds an empty schedule.
func New() *Schedule {
	return &Schedule{}
}

// AlwaysOnOneServer makes every event, including ones added after this call,
// run on one server only: the minute's leader.
func (s *Schedule) AlwaysOnOneServer() {
	s.onOneServer = true
}

// Command schedules a console command with its arguments.
func (s *Schedule) Command(name string, args ...string) *Event {
	return s.add(&Event{command: name, args: args})
}

// Call schedules a closure. Give it a Name: it keys the overlap lock and
// the logs.
func (s *Schedule) Call(fn func(ctx context.Context) error) *Event {
	return s.add(&Event{callback: fn})
}

// add registers an event, every minute until a frequency says otherwise.
func (s *Schedule) add(event *Event) *Event {
	event.expression = "* * * * *"
	event.schedule = s
	s.events = append(s.events, event)

	return event
}

// Events lists the scheduled events in definition order.
func (s *Schedule) Events() []*Event {
	return append([]*Event(nil), s.events...)
}

// DueEvents are the events due in the minute containing now.
func (s *Schedule) DueEvents(now time.Time) []*Event {
	var due []*Event
	for _, event := range s.events {
		if event.IsDue(now) {
			due = append(due, event)
		}
	}

	return due
}

// Event is one scheduled task.
type Event struct {
	schedule    *Schedule
	command     string
	args        []string
	callback    func(ctx context.Context) error
	name        string
	expression  string
	overlapping bool
	expiresAt   time.Duration
	background  bool
}

// Cron sets a raw cron expression; an invalid one panics at definition time.
func (e *Event) Cron(expression string) *Event {
	if _, err := parseCron(expression); err != nil {
		panic(err)
	}

	e.expression = expression

	return e
}

// EveryMinute runs the event every minute.
func (e *Event) EveryMinute() *Event { return e.Cron("* * * * *") }

// Hourly runs the event at the top of every hour.
func (e *Event) Hourly() *Event { return e.Cron("0 * * * *") }

// Daily runs the event at midnight UTC.
func (e *Event) Daily() *Event { return e.Cron("0 0 * * *") }

// DailyAt runs the event every day at clock ("02:00", UTC).
func (e *Event) DailyAt(clock string) *Event {
	hour, minute := parseClock(clock)

	return e.Cron(fmt.Sprintf("%d %d * * *", minute, hour))
}

// Monthly runs the event at midnight UTC on the 1st.
func (e *Event) Monthly() *Event { return e.Cron("0 0 1 * *") }

// MonthlyOn runs the event on the given day of the month at clock (UTC).
func (e *Event) MonthlyOn(day int, clock string) *Event {
	hour, minute := parseClock(clock)

	return e.Cron(fmt.Sprintf("%d %d %d * *", minute, hour, day))
}

// parseClock splits "HH:MM"; a bad clock panics at definition time.
func parseClock(clock string) (hour, minute int) {
	if _, err := fmt.Sscanf(clock, "%d:%d", &hour, &minute); err != nil {
		panic(fmt.Sprintf("schedule: bad time %q", clock))
	}

	return hour, minute
}

// DefaultOverlapExpiry is WithoutOverlapping's default lock expiry: how long a
// crashed run's lock blocks the event.
const DefaultOverlapExpiry = 24 * time.Hour

// WithoutOverlapping skips a run while the previous one is still going, on
// any server. The lock lapses after expiresAt (default DefaultOverlapExpiry)
// if its holder dies mid-run.
func (e *Event) WithoutOverlapping(expiresAt ...time.Duration) *Event {
	e.overlapping = true
	e.expiresAt = DefaultOverlapExpiry

	if len(expiresAt) > 0 && expiresAt[0] > 0 {
		e.expiresAt = expiresAt[0]
	}

	return e
}

// OverlapExpiry is the overlap lock's TTL.
func (e *Event) OverlapExpiry() time.Duration {
	if e.expiresAt <= 0 {
		return DefaultOverlapExpiry
	}

	return e.expiresAt
}

// RunInBackground lets the tick start the next due event without waiting
// for this one.
func (e *Event) RunInBackground() *Event {
	e.background = true

	return e
}

// Name names the event; Call events need one, or they all read "Closure"
// and share an overlap lock per expression.
func (e *Event) Name(name string) *Event {
	e.name = name

	return e
}

// Expression is the cron expression.
func (e *Event) Expression() string { return e.expression }

// CommandName is the scheduled command's name ("" for a Call).
func (e *Event) CommandName() string { return e.command }

// Args is the scheduled command's argv after its name, a copy the caller
// may append to.
func (e *Event) Args() []string { return slices.Clone(e.args) }

// IsOnOneServer reports whether only the minute's leader runs the event.
func (e *Event) IsOnOneServer() bool {
	return e.schedule.onOneServer
}

// PreventsOverlaps reports whether the event runs WithoutOverlapping.
func (e *Event) PreventsOverlaps() bool { return e.overlapping }

// RunsInBackground reports whether the event runs in the background.
func (e *Event) RunsInBackground() bool { return e.background }

// Summary is how `schedule:list` and logs name the event: its name, the
// command line, or "Closure".
func (e *Event) Summary() string {
	if e.callback != nil {
		if e.name != "" {
			return e.name
		}

		return "Closure"
	}

	return strings.TrimSpace(e.command + " " + strings.Join(e.args, " "))
}

// MutexName is the key of the event's overlap lock:
// framework/schedule-sha1(expression + summary), so the same task on another
// server contends for the same key.
func (e *Event) MutexName() string {
	sum := sha1.Sum([]byte(e.expression + e.Summary()))

	return "framework/schedule-" + hex.EncodeToString(sum[:])
}

// IsDue reports whether the event runs in the minute containing now (UTC).
func (e *Event) IsDue(now time.Time) bool {
	c, err := parseCron(e.expression)
	if err != nil {
		return false
	}

	return c.matches(now.UTC())
}

// NextDue is the next run after now.
func (e *Event) NextDue(now time.Time) time.Time {
	c, err := parseCron(e.expression)
	if err != nil {
		return time.Time{}
	}

	return c.next(now.UTC())
}
