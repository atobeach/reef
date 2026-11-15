// Package queue runs background jobs on River (Postgres): a job is a typed,
// JSON-serialisable args struct; its handler is registered once; whatever is
// handed the connection as its Dispatcher dispatches it:
// `cmd.Queue.Dispatch(ctx, jobs.UpdateCountryWeather{CountryID: id})`.
//
// A job tunes its handling through optional methods on the args type:
//
//	func (J) Tries() int                → River MaxAttempts
//	func (J) Backoff() []time.Duration  → NextRetry per attempt
//	func (J) Timeout() time.Duration    → the handler's ctx deadline
//	func (J) ShouldBeUnique()           → River unique insert, keyed by the
//	                                      `river:"unique"` fields
//	func (J) Failed(ctx, err)           → called after the last attempt
//	func (J) Queue() string             → the queue it is routed to
//
// River's own tables (rivermigrate) are created by `migrate` after the
// app's, so a release has them before any worker or dispatcher starts;
// `queue:migrate` runs River's alone.
package queue

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/riverqueue/river"

	"github.com/atobeach/reef/panics"
)

// Job is a job's payload — River's JobArgs. Kind names it in the queue
// table, so it must never change once jobs of that kind may be queued.
type Job interface {
	Kind() string
}

// Optional job behaviours, detected on the args type.
type (
	// Tries is the total attempts before the job is discarded.
	Tries interface{ Tries() int }
	// Backoff is the wait before each retry, the last value repeating for
	// later attempts.
	Backoff interface{ Backoff() []time.Duration }
	// Timeout cancels the handler's context after the returned duration.
	Timeout interface{ Timeout() time.Duration }
	// ShouldBeUnique keeps one copy per `river:"unique"` field values: a
	// duplicate is skipped until the first finishes or finally fails.
	ShouldBeUnique interface{ ShouldBeUnique() }
	// Failer is called once the job has used its last attempt.
	Failer interface {
		Failed(ctx context.Context, err error)
	}
	// OnQueue routes the job to a named queue (default "default").
	OnQueue interface{ Queue() string }
)

// DefaultQueue is the queue a job without a Queue method goes to.
const DefaultQueue = "default"

// Dispatcher pushes jobs — the queue connection.
type Dispatcher interface {
	Dispatch(ctx context.Context, job Job) error
}

// handler runs a decoded job of one kind.
type handler struct {
	run  func(ctx context.Context, job Job) error
	zero Job
}

// Workers is the registry of job handlers, shared by the sync connection
// and the River worker.
type Workers struct {
	mu       sync.RWMutex
	bind     func(ctx context.Context) context.Context
	handlers map[string]handler
	// river is River's view of the same registry, kept in step by Register,
	// so a connection built before a handler is registered still accepts
	// its jobs.
	river *river.Workers
	// afterProcessed runs after every successful job.
	afterProcessed []func(ctx context.Context, job Job)
}

// NewWorkers builds an empty registry. bind attaches the app's services
// (the database connection, the clock) to a handler's context, as the HTTP
// kernel does per request; nil binds nothing.
func NewWorkers(bind func(ctx context.Context) context.Context) *Workers {
	return &Workers{bind: bind, handlers: map[string]handler{}, river: river.NewWorkers()}
}

// Register adds the handler for job type T; a kind registered twice panics.
func Register[T Job](w *Workers, handle func(ctx context.Context, job T) error) {
	var zero T

	kind := zero.Kind()

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.handlers[kind]; exists {
		panic(fmt.Sprintf("queue: job kind %q registered twice", kind))
	}

	w.handlers[kind] = handler{
		run: func(ctx context.Context, job Job) error {
			typed, ok := job.(T)
			if !ok {
				return fmt.Errorf("queue: %s handler got %T", kind, job)
			}

			return handle(ctx, typed)
		},
		zero: zero,
	}
	river.AddWorker[T](w.river, &riverWorker[T]{workers: w})
}

// AfterProcessed registers a listener for every successfully processed job.
func (w *Workers) AfterProcessed(listener func(ctx context.Context, job Job)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.afterProcessed = append(w.afterProcessed, listener)
}

// Jobs lists a zero value of every registered job type, sorted by kind, for
// assertions over the job set (RetryAfter outlasting every Timeout).
func (w *Workers) Jobs() []Job {
	w.mu.RLock()
	defer w.mu.RUnlock()

	jobs := make([]Job, 0, len(w.handlers))
	for _, h := range w.handlers {
		jobs = append(jobs, h.zero)
	}

	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Kind() < jobs[j].Kind() })

	return jobs
}

// Handle runs a job's handler in this process, as a worker would: bound
// context, timeout, Failed once the last of maxAttempts has failed, and the
// AfterProcessed listeners on success.
func (w *Workers) Handle(ctx context.Context, job Job, attempt, maxAttempts int) error {
	w.mu.RLock()
	h, ok := w.handlers[job.Kind()]
	w.mu.RUnlock()

	if !ok {
		return fmt.Errorf("queue: no handler registered for job kind %q", job.Kind())
	}

	if w.bind != nil {
		ctx = w.bind(ctx)
	}

	if timeout := TimeoutOf(job); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	err := runRecovering(ctx, h.run, job)
	if err != nil {
		if failer, ok := job.(Failer); ok && attempt >= maxAttempts {
			failer.Failed(ctx, err)
		}

		return err
	}

	w.mu.RLock()
	listeners := append([]func(context.Context, Job){}, w.afterProcessed...)
	w.mu.RUnlock()

	for _, listener := range listeners {
		listener(ctx, job)
	}

	return nil
}

// runRecovering turns a panicking handler into an error, so one bad job
// fails its attempt instead of taking the worker down. The stack is logged
// here: River stores only the error's text.
func runRecovering(ctx context.Context, run func(context.Context, Job) error, job Job) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			p := panics.New(recovered)
			slog.ErrorContext(ctx, "queue: job panicked", "kind", job.Kind(), "error", p, "stack", p.Stack())
			err = fmt.Errorf("queue: %s panicked: %w", job.Kind(), p)
		}
	}()

	return run(ctx, job)
}

// TriesOf is a job's Tries, or fallback when it declares none.
func TriesOf(job Job, fallback int) int {
	if t, ok := job.(Tries); ok && t.Tries() > 0 {
		return t.Tries()
	}

	return fallback
}

// TimeoutOf is a job's Timeout, zero when it declares none.
func TimeoutOf(job Job) time.Duration {
	if t, ok := job.(Timeout); ok {
		return t.Timeout()
	}

	return 0
}

func queueOf(job Job) string {
	if q, ok := job.(OnQueue); ok && q.Queue() != "" {
		return q.Queue()
	}

	return DefaultQueue
}

// BackoffFor is the wait after the given failed attempt (1-based): the
// attempt's entry in its Backoff, the last entry repeating. ok is false when
// the job declares no backoff.
func BackoffFor(job Job, attempt int) (time.Duration, bool) {
	b, ok := job.(Backoff)
	if !ok || len(b.Backoff()) == 0 {
		return 0, false
	}

	delays := b.Backoff()

	return delays[min(max(attempt, 1), len(delays))-1], true
}

// Sync is the `sync` connection: the job runs inline, its error returned to
// the dispatcher (QUEUE_CONNECTION=sync, the test default).
type Sync struct {
	Workers *Workers
}

// Dispatch runs the job now, on the caller's context (and so its
// transaction).
func (s Sync) Dispatch(ctx context.Context, job Job) error {
	// One inline attempt that is also the last, so Failed runs on error.
	return s.Workers.Handle(ctx, job, 1, 1)
}

// Fake records dispatched jobs without running them, for tests (queuetest).
type Fake struct {
	mu     sync.Mutex
	pushed []Job
}

// Dispatch records the job.
func (f *Fake) Dispatch(_ context.Context, job Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pushed = append(f.pushed, job)

	return nil
}

// All is every recorded job, in dispatch order.
func (f *Fake) All() []Job {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]Job(nil), f.pushed...)
}
