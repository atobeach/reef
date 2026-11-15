package schedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/panics"
)

// leaderLock prefixes the per-minute key whose holder runs that minute's
// one-server events.
const leaderLock = "framework/schedule-leader-"

// leaderTTL keeps a minute's leader key for an hour, so a scheduler whose
// clock lags by seconds still finds the minute claimed. The key names the
// minute, so it is never released.
const leaderTTL = time.Hour

// LeaderKey is the leader lock's name for the minute containing tick.
func LeaderKey(tick time.Time) string {
	return leaderLock + tick.UTC().Format("200601021504")
}

// RunnerConfig configures a schedule runner.
type RunnerConfig struct {
	Schedule *Schedule
	// Call runs a console command — the console kernel's Call.
	Call func(ctx context.Context, command string, args []string, out io.Writer) int
	// Locker provides the leader and overlap locks.
	Locker Locker
	// Out receives the "Running [...] DONE" progress lines; nil discards them.
	Out io.Writer
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// Runner runs a schedule's due events.
type Runner struct {
	config  RunnerConfig
	running sync.WaitGroup
}

// NewRunner builds a runner, filling the config's defaults. It reads the
// time off the context it runs on, so a frozen app clock decides which
// events are due.
func NewRunner(config RunnerConfig) *Runner {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	return &Runner{config: config}
}

func (r *Runner) printf(format string, args ...any) {
	if r.config.Out != nil {
		_, _ = fmt.Fprintf(r.config.Out, format+"\n", args...)
	}
}

// Tick runs the events due in now's minute, foreground ones in turn and
// background ones concurrently, one-server ones only when leader. It returns
// how many started once the foreground ones finish (Wait waits for the rest).
func (r *Runner) Tick(ctx context.Context, now time.Time, leader bool) int {
	started := 0

	for _, event := range r.config.Schedule.DueEvents(now) {
		if event.IsOnOneServer() && !leader {
			continue
		}

		release := func() {}
		if event.PreventsOverlaps() {
			unlock, ok, err := r.config.Locker.TryLock(ctx, event.MutexName(), event.OverlapExpiry())
			if err != nil {
				r.config.Logger.ErrorContext(ctx, "schedule: overlap lock", "event", event.Summary(), "error", err)

				continue
			}

			if !ok {
				r.printf("  %s Skipping [%s], as command already running on one server", now.Format(time.DateTime), event.Summary())

				continue
			}

			release = unlock
		}

		started++

		if event.RunsInBackground() {
			r.running.Add(1)

			go func() {
				defer r.running.Done()
				defer release()

				r.run(ctx, event)
			}()

			continue
		}

		r.run(ctx, event)
		release()
	}

	return started
}

// Wait blocks until every background event has finished.
func (r *Runner) Wait() {
	r.running.Wait()
}

// run runs one event and prints its outcome; an error or panic is logged,
// never fatal to the scheduler.
func (r *Runner) run(ctx context.Context, event *Event) {
	started := time.Now()
	r.printf("  %s Running [%s]", clock.Now(ctx).Format(time.DateTime), event.Summary())

	var err error
	var output bytes.Buffer

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("panic: %w", panics.New(recovered))
			}
		}()

		if event.callback != nil {
			err = event.callback(ctx)

			return
		}

		if code := r.config.Call(ctx, event.command, event.args, &output); code != 0 {
			err = fmt.Errorf("exit code %d: %s", code, strings.TrimSpace(output.String()))
		}
	}()

	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		r.printf("  %s [%s] FAIL (%s)", clock.Now(ctx).Format(time.DateTime), event.Summary(), elapsed)
		attributes := []any{"event", event.Summary(), "error", err}
		if recovered, ok := errors.AsType[*panics.Error](err); ok {
			attributes = append(attributes, "stack", recovered.Stack())
		}
		r.config.Logger.ErrorContext(ctx, "schedule: event failed", attributes...)

		return
	}

	r.printf("  %s [%s] DONE (%s)", clock.Now(ctx).Format(time.DateTime), event.Summary(), elapsed)
}

// RunOnce is `schedule:run`: one tick for the current minute. It claims the
// minute's leader key, so a second server's cron firing in the same minute
// finds it taken instead of re-running one-server events.
func (r *Runner) RunOnce(ctx context.Context) error {
	now := clock.Now(ctx)

	leader, err := r.claimLeader(ctx, now)
	if err != nil {
		return fmt.Errorf("schedule: leader lock: %w", err)
	}

	if !leader {
		r.printf("  Another server is running this minute's schedule; only non-onOneServer events run here.")
	}

	if r.Tick(ctx, now, leader) == 0 {
		r.printf("  No scheduled commands are ready to run.")
	}

	r.Wait()

	return nil
}

// claimLeader tries to take the minute's leader key.
func (r *Runner) claimLeader(ctx context.Context, tick time.Time) (leader bool, err error) {
	_, leader, err = r.config.Locker.TryLock(ctx, LeaderKey(tick), leaderTTL)

	return leader, err
}

// Work is `schedule:work`: it ticks at the top of every minute until ctx is
// cancelled, then waits for running events. Leadership is claimed afresh
// each minute, so exactly one scheduler leads it; a cache error leaves the
// minute leaderless rather than risk running one-server events twice.
func (r *Runner) Work(ctx context.Context) error {
	r.printf("  Running scheduled tasks every minute.")

	for {
		now := clock.Now(ctx)
		next := now.Truncate(time.Minute).Add(time.Minute)

		select {
		case <-ctx.Done():
			r.Wait()

			return nil
		case <-time.After(next.Sub(now)):
		}

		leader, err := r.claimLeader(ctx, next)
		if err != nil {
			r.config.Logger.ErrorContext(ctx, "schedule: leader lock", "error", err)
		}

		// Each minute's foreground events run in their own goroutine so a
		// slow one never delays the next tick.
		r.running.Add(1)

		go func(tick time.Time, leader bool) {
			defer r.running.Done()

			r.Tick(ctx, tick, leader)
		}(next, leader)
	}
}
