package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/atobeach/reef/cache"
	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/testkit"
)

func at(value string) time.Time {
	t, err := time.Parse(time.DateTime, value)
	if err != nil {
		panic(err)
	}

	return t
}

func TestFrequenciesCompileToCronExpressions(t *testing.T) {
	s := New()

	cases := map[string]*Event{
		"* * * * *":   s.Command("a").EveryMinute(),
		"0 * * * *":   s.Command("b").Hourly(),
		"0 0 * * *":   s.Command("c").Daily(),
		"0 2 * * *":   s.Command("d").DailyAt("02:00"),
		"30 3 * * *":  s.Command("e").DailyAt("03:30"),
		"0 0 1 * *":   s.Command("f").Monthly(),
		"30 4 28 * *": s.Command("g").MonthlyOn(28, "04:30"),
	}

	for want, event := range cases {
		if event.Expression() != want {
			t.Errorf("%s: expression %q, want %q", event.Summary(), event.Expression(), want)
		}
	}
}

func TestCronMatching(t *testing.T) {
	cases := []struct {
		expression string
		at         string
		due        bool
	}{
		{"* * * * *", "2026-10-20 13:37:42", true},
		{"0 * * * *", "2026-10-20 13:00:59", true},
		{"0 * * * *", "2026-10-20 13:01:00", false},
		{"0 0 1 * *", "2026-11-01 00:00:00", true},
		{"0 0 1 * *", "2026-11-02 00:00:00", false},
		{"30 4 28 * *", "2026-10-28 04:30:00", true},
		{"*/15 9-17 * * 1-5", "2026-10-20 09:45:00", true},
		{"*/15 9-17 * * 1-5", "2026-10-24 09:45:00", false}, // a Saturday
		{"0 0 * * 0", "2026-10-25 00:00:00", true},          // Sunday as 0
		{"0 0 * * 7", "2026-10-25 00:00:00", true},          // and as 7
		{"0 0 1,15 * *", "2026-10-15 00:00:00", true},
	}

	for _, tc := range cases {
		c, err := parseCron(tc.expression)
		if err != nil {
			t.Fatal(err)
		}

		if got := c.matches(at(tc.at)); got != tc.due {
			t.Errorf("%q at %s: due = %v, want %v", tc.expression, tc.at, got, tc.due)
		}
	}

	if _, err := parseCron("61 * * * *"); err == nil {
		t.Error("out-of-range minute must fail")
	}

	if next := s0("30 4 28 * *").NextDue(at("2026-10-20 13:00:00")); !next.Equal(at("2026-10-28 04:30:00")) {
		t.Errorf("next = %s", next)
	}
}

func s0(expression string) *Event {
	return New().Command("x").Cron(expression)
}

func TestAlwaysOnOneServerCoversEveryEvent(t *testing.T) {
	s := New()
	before := s.Command("a").Daily()
	s.AlwaysOnOneServer()
	after := s.Call(func(context.Context) error { return nil }).Name("warm").Hourly()

	if !before.IsOnOneServer() || !after.IsOnOneServer() {
		t.Fatal("AlwaysOnOneServer applies to every event")
	}

	if after.Summary() != "warm" || before.MutexName() == after.MutexName() {
		t.Errorf("summary %q, mutexes %q %q", after.Summary(), before.MutexName(), after.MutexName())
	}
}

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) call(_ context.Context, command string, args []string, _ io.Writer) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, strings.TrimSpace(command+" "+strings.Join(args, " ")))

	if command == "fails" {
		return 1
	}

	return 0
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.calls...)
}

func TestTickRunsOnlyDueEvents(t *testing.T) {
	s := New()
	s.Command("every-minute").EveryMinute()
	s.Command("hourly").Hourly()
	s.Command("model:prune", "--model=X").Daily()
	s.Command("fails").EveryMinute()

	rec := &recorder{}
	var out bytes.Buffer
	runner := NewRunner(RunnerConfig{Schedule: s, Call: rec.call, Locker: NewMemoryLocker(), Out: &out})

	if started := runner.Tick(context.Background(), at("2026-10-20 00:00:00"), true); started != 4 {
		t.Fatalf("started %d at midnight", started)
	}

	rec2 := &recorder{}
	runner = NewRunner(RunnerConfig{Schedule: s, Call: rec2.call, Locker: NewMemoryLocker(), Out: &out})
	runner.Tick(context.Background(), at("2026-10-20 13:07:00"), true)

	if got := rec2.all(); len(got) != 2 || got[0] != "every-minute" || got[1] != "fails" {
		t.Fatalf("13:07 ran %v", got)
	}

	if calls := rec.all(); calls[2] != "model:prune --model=X" {
		t.Errorf("args lost: %v", calls)
	}

	if !strings.Contains(out.String(), "[fails] FAIL") || !strings.Contains(out.String(), "[every-minute] DONE") {
		t.Errorf("progress output:\n%s", out.String())
	}
}

func TestOneServerEventsOnlyRunOnTheLeader(t *testing.T) {
	s := New()
	s.AlwaysOnOneServer()
	s.Command("leader-only").EveryMinute()

	rec := &recorder{}
	runner := NewRunner(RunnerConfig{Schedule: s, Call: rec.call, Locker: NewMemoryLocker()})

	if started := runner.Tick(context.Background(), at("2026-10-20 10:00:00"), false); started != 0 || len(rec.all()) != 0 {
		t.Fatalf("a follower started %d: %v", started, rec.all())
	}

	runner.Tick(context.Background(), at("2026-10-20 10:00:00"), true)

	if got := rec.all(); len(got) != 1 || got[0] != "leader-only" {
		t.Fatalf("the leader ran %v", got)
	}
}

func TestWithoutOverlappingSkipsWhileTheLastRunHoldsTheLock(t *testing.T) {
	s := New()
	event := s.Command("slow").EveryMinute().WithoutOverlapping()

	locker := NewMemoryLocker()
	release, ok, _ := locker.TryLock(context.Background(), event.MutexName(), time.Hour)
	if !ok {
		t.Fatal("lock")
	}

	rec := &recorder{}
	var out bytes.Buffer
	runner := NewRunner(RunnerConfig{Schedule: s, Call: rec.call, Locker: locker, Out: &out})

	runner.Tick(context.Background(), at("2026-10-20 10:00:00"), true)
	if len(rec.all()) != 0 || !strings.Contains(out.String(), "Skipping [slow]") {
		t.Fatalf("ran while locked: %v\n%s", rec.all(), out.String())
	}

	release()
	runner.Tick(context.Background(), at("2026-10-20 10:01:00"), true)

	if len(rec.all()) != 1 {
		t.Fatal("must run once the previous run released the lock")
	}

	// The run released its own lock.
	if _, ok, _ := locker.TryLock(context.Background(), event.MutexName(), time.Hour); !ok {
		t.Fatal("the overlap lock leaked")
	}
}

func TestBackgroundEventsDoNotBlockTheTick(t *testing.T) {
	s := New()
	gate := make(chan struct{})
	var finished atomic.Bool

	s.Call(func(context.Context) error {
		<-gate
		finished.Store(true)

		return nil
	}).Name("slow").EveryMinute().RunInBackground()

	rec := &recorder{}
	s.Command("quick").EveryMinute()

	runner := NewRunner(RunnerConfig{Schedule: s, Call: rec.call, Locker: NewMemoryLocker()})
	runner.Tick(context.Background(), at("2026-10-20 10:00:00"), true)

	if len(rec.all()) != 1 || finished.Load() {
		t.Fatal("the foreground event must run while the background one is still going")
	}

	close(gate)
	runner.Wait()

	if !finished.Load() {
		t.Fatal("Wait returns after background events finish")
	}
}

// A failing or panicking event is printed and logged, never fatal to the
// scheduler; a panic's line carries its stack.
func TestACallbackErrorOrPanicIsLoggedNotFatal(t *testing.T) {
	s := New()
	s.Call(func(context.Context) error { return errors.New("feed down") }).Name("warm").EveryMinute()
	s.Call(func(context.Context) error { panic("boom") }).Name("explodes").EveryMinute()

	var out, logs bytes.Buffer
	runner := NewRunner(RunnerConfig{Schedule: s, Locker: NewMemoryLocker(), Out: &out, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	runner.Tick(context.Background(), at("2026-10-20 10:00:00"), true)

	if strings.Count(out.String(), "FAIL") != 2 {
		t.Fatalf("output:\n%s", out.String())
	}

	failed := map[string]map[string]any{}
	for raw := range bytes.SplitSeq(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err == nil && line["msg"] == "schedule: event failed" {
			failed[line["error"].(string)] = line
		}
	}

	if stack, _ := failed["panic: boom"]["stack"].(string); !strings.Contains(stack, "schedule.TestACallbackErrorOrPanicIsLoggedNotFatal") {
		t.Fatalf("the panic's line = %v, want its stack", failed["panic: boom"])
	}
	if warm, ok := failed["feed down"]; !ok || warm["stack"] != nil {
		t.Fatalf("the error's line = %v, want one without a stack", warm)
	}
}

// RunOnce claims the minute: a second server's cron in the same minute runs
// none of the one-server events, and the next minute is claimed afresh.
func TestRunOnceClaimsTheMinuteForOneServer(t *testing.T) {
	s := New()
	s.AlwaysOnOneServer()
	s.Command("leader-only").EveryMinute()
	s.Command("also-leader-only").EveryMinute()

	locker := NewMemoryLocker()
	first := &recorder{}
	second := &recorder{}

	ctx, now := clocktest.FrozenAt(t.Context(), at("2026-10-20 10:00:30"))

	_ = NewRunner(RunnerConfig{Schedule: s, Call: first.call, Locker: locker}).RunOnce(ctx)
	_ = NewRunner(RunnerConfig{Schedule: s, Call: second.call, Locker: locker}).RunOnce(ctx)

	if got := first.all(); len(got) != 2 {
		t.Fatalf("the leader ran %v", got)
	}

	if got := second.all(); len(got) != 0 {
		t.Fatalf("the second server ran %v: one-server events run once per minute", got)
	}

	now.TravelTo(at("2026-10-20 10:01:05"))
	_ = NewRunner(RunnerConfig{Schedule: s, Call: second.call, Locker: locker}).RunOnce(ctx)

	if got := second.all(); len(got) != 2 || got[0] != "leader-only" {
		t.Fatalf("the next minute is anyone's: %v", got)
	}
}

// valkeyLockers are two schedulers' lockers on one real Valkey, each with its
// own client (two servers), under a key prefix the test owns.
func valkeyLockers(t *testing.T) (a, b CacheLocker, client *redis.Client, prefix string) {
	t.Helper()

	client, prefix = testkit.Valkey(t)

	return CacheLocker{Store: cache.NewRedisStore(client, prefix)},
		CacheLocker{Store: cache.NewRedisStore(testkit.ValkeyClient(t), prefix)},
		client, prefix
}

// Two schedulers racing for the same minute on Valkey: exactly one runs the
// one-server event, and the leader key lives an hour.
func TestOnlyOneSchedulerLeadsAMinuteOnValkey(t *testing.T) {
	a, b, client, prefix := valkeyLockers(t)

	s := New()
	s.AlwaysOnOneServer()
	s.Command("tick").EveryMinute()

	now := at("2026-10-20 10:00:00")
	ctx, _ := clocktest.FrozenAt(t.Context(), now)

	recorders := []*recorder{{}, {}}
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i, locker := range []Locker{a, b} {
		wg.Add(1)

		go func() {
			defer wg.Done()
			<-start

			if err := NewRunner(RunnerConfig{Schedule: s, Call: recorders[i].call, Locker: locker}).RunOnce(ctx); err != nil {
				t.Error(err)
			}
		}()
	}

	close(start)
	wg.Wait()

	if runs := len(recorders[0].all()) + len(recorders[1].all()); runs != 1 {
		t.Fatalf("the event ran %d times in one minute across two schedulers", runs)
	}

	ttl := client.TTL(context.Background(), prefix+LeaderKey(now)).Val()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Fatalf("leader key ttl = %s, want an hour", ttl)
	}
}

// WithoutOverlapping across two schedulers on Valkey: while one server's run
// holds the event's lock (24h by default), the other skips it; the lock goes
// when the run finishes, and an event's own expiry sets the TTL.
func TestWithoutOverlappingHoldsAcrossSchedulersOnValkey(t *testing.T) {
	a, b, client, prefix := valkeyLockers(t)

	s := New()
	gate := make(chan struct{})
	started := make(chan struct{})

	slow := s.Call(func(context.Context) error {
		close(started)
		<-gate

		return nil
	}).Name("slow").EveryMinute().WithoutOverlapping().RunInBackground()

	brief := s.Command("brief").Hourly().WithoutOverlapping(10 * time.Minute)

	var outA, outB bytes.Buffer
	first := NewRunner(RunnerConfig{Schedule: s, Locker: a, Out: &outA})
	second := NewRunner(RunnerConfig{Schedule: s, Locker: b, Out: &outB})

	first.Tick(context.Background(), at("2026-10-20 10:01:00"), true)
	<-started

	ttl := client.TTL(context.Background(), prefix+slow.MutexName()).Val()
	if ttl < 23*time.Hour || ttl > 24*time.Hour {
		t.Fatalf("overlap lock ttl = %s, want 24h", ttl)
	}

	if started := second.Tick(context.Background(), at("2026-10-20 10:02:00"), true); started != 0 {
		t.Fatalf("the second scheduler started %d events while the first one's run holds the lock", started)
	}

	if !strings.Contains(outB.String(), "Skipping [slow]") {
		t.Fatalf("output:\n%s", outB.String())
	}

	close(gate)
	first.Wait()

	if exists := client.Exists(context.Background(), prefix+slow.MutexName()).Val(); exists != 0 {
		t.Fatal("the overlap lock must be released when the run finishes")
	}

	// The event's own expiry: hold the lock as a running server would and
	// read its TTL.
	release, ok, err := b.TryLock(context.Background(), brief.MutexName(), brief.OverlapExpiry())
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	defer release()

	if ttl := client.TTL(context.Background(), prefix+brief.MutexName()).Val(); ttl < 9*time.Minute || ttl > 10*time.Minute {
		t.Fatalf("custom expiry ttl = %s, want 10m", ttl)
	}
}

func TestOverlapExpiryDefaultsTo24Hours(t *testing.T) {
	s := New()

	if got := s.Command("a").WithoutOverlapping().OverlapExpiry(); got != 24*time.Hour {
		t.Errorf("default = %s", got)
	}

	if got := s.Command("b").WithoutOverlapping(5 * time.Minute).OverlapExpiry(); got != 5*time.Minute {
		t.Errorf("custom = %s", got)
	}
}

// A release after the lock lapsed and someone else took it must leave the new
// holder's lock alone.
func TestAnExpiredHoldersReleaseKeepsTheNextHoldersLock(t *testing.T) {
	locker := CacheLocker{Store: cache.NewMemoryStore()}
	ctx, now := clocktest.FrozenAt(t.Context(), time.Now())

	release, ok, _ := locker.TryLock(ctx, "event", time.Minute)
	if !ok {
		t.Fatal("first")
	}

	now.Travel(2 * time.Minute)

	if _, ok, _ := locker.TryLock(ctx, "event", time.Minute); !ok {
		t.Fatal("an expired lock must be free")
	}

	release()

	if _, ok, _ := locker.TryLock(ctx, "event", time.Minute); ok {
		t.Fatal("the expired holder's release freed the next holder's lock")
	}
}

// The runner reads the clock on its context, so a frozen app clock decides
// which events are due.
func TestARunnerReadsTheAppClock(t *testing.T) {
	ctx, _ := clocktest.FrozenAt(t.Context(), at("2026-10-20 03:30:00"))

	s := New()
	s.Command("cities:generate-content").DailyAt("03:30")
	s.Command("cities:generate-climate").DailyAt("04:00")

	rec := &recorder{}
	if err := NewRunner(RunnerConfig{Schedule: s, Call: rec.call, Locker: NewMemoryLocker()}).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(rec.calls, []string{"cities:generate-content"}) {
		t.Fatalf("ran %v", rec.calls)
	}
}
