package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/atobeach/reef/panics"
)

type ping struct {
	Name string `json:"name"`
}

func (ping) Kind() string { return "ping" }

type flaky struct {
	ID int64 `json:"id" river:"unique"`
}

func (flaky) Kind() string             { return "flaky" }
func (flaky) Tries() int               { return 3 }
func (flaky) Timeout() time.Duration   { return 50 * time.Millisecond }
func (flaky) ShouldBeUnique()          {}
func (flaky) Queue() string            { return "slow" }
func (flaky) Backoff() []time.Duration { return []time.Duration{10 * time.Second, 30 * time.Second} }

var errFailedWith error

func (flaky) Failed(_ context.Context, err error) { errFailedWith = err }

func TestSyncRunsTheHandlerInlineWithListeners(t *testing.T) {
	type key struct{}

	workers := NewWorkers(func(ctx context.Context) context.Context { return context.WithValue(ctx, key{}, "bound") })

	var handled []string
	Register(workers, func(ctx context.Context, job ping) error {
		handled = append(handled, job.Name+":"+ctx.Value(key{}).(string))

		return nil
	})

	var processed []string
	workers.AfterProcessed(func(_ context.Context, job Job) { processed = append(processed, job.Kind()) })

	if err := (Sync{Workers: workers}).Dispatch(context.Background(), ping{Name: "a"}); err != nil {
		t.Fatal(err)
	}

	if len(handled) != 1 || handled[0] != "a:bound" || len(processed) != 1 {
		t.Fatalf("handled %v processed %v", handled, processed)
	}
}

func TestAFinalFailureCallsFailedAndSkipsJobProcessed(t *testing.T) {
	workers := NewWorkers(nil)
	boom := errors.New("upstream down")

	Register(workers, func(ctx context.Context, _ flaky) error {
		<-ctx.Done() // the job's Timeout cancels the context

		return errors.Join(boom, ctx.Err())
	})

	processed := 0
	workers.AfterProcessed(func(context.Context, Job) { processed++ })

	errFailedWith = nil
	if err := workers.Handle(context.Background(), flaky{ID: 1}, 1, 3); !errors.Is(err, boom) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}

	if errFailedWith != nil {
		t.Fatal("failed() only runs after the last attempt")
	}

	_ = workers.Handle(context.Background(), flaky{ID: 1}, 3, 3)
	if !errors.Is(errFailedWith, boom) || processed != 0 {
		t.Fatalf("failed with %v, processed %d", errFailedWith, processed)
	}
}

func panicsInAHandler(context.Context, ping) error { panic("nil map") }

// A panicking handler fails its attempt with the text River stores, and
// its stack is logged where it was recovered.
func TestAPanickingHandlerIsAnError(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workers := NewWorkers(nil)
	Register(workers, panicsInAHandler)

	err := workers.Handle(context.Background(), ping{}, 1, 1)
	if err == nil || err.Error() != "queue: ping panicked: nil map" {
		t.Fatalf("err = %v", err)
	}

	recovered, ok := errors.AsType[*panics.Error](err)
	if !ok {
		t.Fatalf("err = %v, want a *panics.Error in its chain", err)
	}
	if frames := recovered.Frames(); !strings.HasSuffix(firstOutsideRuntime(frames).Function, "queue.panicsInAHandler") {
		t.Fatalf("stack:\n%s", recovered.Stack())
	}

	var line struct {
		Msg   string `json:"msg"`
		Kind  string `json:"kind"`
		Stack string `json:"stack"`
	}
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil || line.Msg != "queue: job panicked" || line.Kind != "ping" || !strings.Contains(line.Stack, "queue.panicsInAHandler\n") {
		t.Fatalf("logged %s", logs.String())
	}
}

// firstOutsideRuntime is the panic site: a runtime error's first frames are
// the runtime's own.
func firstOutsideRuntime(frames []runtime.Frame) runtime.Frame {
	for _, frame := range frames {
		if !strings.HasPrefix(frame.Function, "runtime.") {
			return frame
		}
	}

	return runtime.Frame{}
}

func TestJobAttributesMapToRiverOptions(t *testing.T) {
	opts := InsertOpts(flaky{ID: 9}, 1)
	if opts.MaxAttempts != 3 || opts.Queue != "slow" || !opts.UniqueOpts.ByArgs {
		t.Fatalf("opts = %+v", opts)
	}

	for _, state := range []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable} {
		found := false
		for _, s := range opts.UniqueOpts.ByState {
			found = found || s == state
		}

		if !found {
			t.Errorf("unique states lack %s", state)
		}
	}

	for _, s := range opts.UniqueOpts.ByState {
		if s == rivertype.JobStateCompleted {
			t.Error("a finished job must release uniqueness, so it can be queued again")
		}
	}

	plain := InsertOpts(ping{}, 3)
	if plain.MaxAttempts != 3 || plain.Queue != DefaultQueue || plain.UniqueOpts.ByArgs {
		t.Fatalf("plain = %+v", plain)
	}

	if d, ok := BackoffFor(flaky{}, 1); !ok || d != 10*time.Second {
		t.Errorf("attempt 1 backoff = %s", d)
	}

	if d, _ := BackoffFor(flaky{}, 5); d != 30*time.Second {
		t.Errorf("the last backoff repeats: %s", d)
	}

	if _, ok := BackoffFor(ping{}, 1); ok {
		t.Error("no Backoff, no override")
	}
}

func TestFakeRecordsEveryJobInDispatchOrder(t *testing.T) {
	fake := &Fake{}
	_ = fake.Dispatch(context.Background(), ping{Name: "x"})
	_ = fake.Dispatch(context.Background(), flaky{ID: 2})

	if all := fake.All(); len(all) != 2 || all[0] != (ping{Name: "x"}) || all[1] != (flaky{ID: 2}) {
		t.Fatalf("all = %v", all)
	}
}

func TestRegisteringAKindTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()

	workers := NewWorkers(nil)
	Register(workers, func(context.Context, ping) error { return nil })
	Register(workers, func(context.Context, ping) error { return nil })
}
