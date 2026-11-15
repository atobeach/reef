package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"github.com/atobeach/reef/db"
)

// RiverConfig is the queue connection's settings.
type RiverConfig struct {
	// Pool is the Postgres the jobs are stored in.
	Pool *pgxpool.Pool
	// Workers runs the jobs (Work) and names the kinds Dispatch accepts.
	Workers *Workers
	// Tries is the default attempts for a job without a Tries method. River
	// fixes it per job at insert time, so a worker can't override it.
	Tries int
	// RetryAfter is how long a "running" job may go before it is presumed
	// dead and retried. It must exceed every job's timeout, or a slow job
	// would run twice (a test guards it).
	RetryAfter time.Duration
	// PollOnly makes the worker poll (every --sleep) instead of LISTENing for
	// inserts: LISTEN needs a session of its own, which a transaction pooler
	// can't give. Leader election polls too (a dead leader goes within ~5s).
	PollOnly bool
	Logger   *slog.Logger
}

// River is the Postgres queue connection.
type River struct {
	config RiverConfig
	insert *river.Client[pgx.Tx]
}

// NewRiver builds the connection. It only inserts until Work is called, so
// API processes can dispatch without running jobs.
func NewRiver(config RiverConfig) (*River, error) {
	if config.Tries <= 0 {
		config.Tries = 1
	}

	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	client, err := river.NewClient(riverpgxv5.New(config.Pool), &river.Config{
		Logger:                      config.Logger,
		MaxAttempts:                 config.Tries,
		CompletedJobRetentionPeriod: completedJobRetention,
		Workers:                     config.Workers.river,
	})
	if err != nil {
		return nil, fmt.Errorf("queue: river client: %w", err)
	}

	return &River{config: config, insert: client}, nil
}

// Dispatch queues the job. Inside a transaction (db.Transaction, or the test
// transaction) the row is inserted on it, so the job is only visible to
// workers once the surrounding work commits — and vanishes with a rollback.
func (r *River) Dispatch(ctx context.Context, job Job) error {
	opts := InsertOpts(job, r.config.Tries)

	var err error
	if conn, connErr := db.Conn(ctx); connErr == nil {
		if tx, ok := conn.(pgx.Tx); ok {
			_, err = r.insert.InsertTx(ctx, tx, job, opts)

			return wrapInsert(job, err)
		}
	}

	_, err = r.insert.Insert(ctx, job, opts)

	return wrapInsert(job, err)
}

func wrapInsert(job Job, err error) error {
	if err != nil {
		return fmt.Errorf("queue: dispatching %s: %w", job.Kind(), err)
	}

	return nil
}

// uniqueStates are every non-final state a ShouldBeUnique copy blocks a new
// one in: a completed or discarded copy no longer does.
var uniqueStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRetryable,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
}

// InsertOpts derives River's insert options from the job's optional methods.
func InsertOpts(job Job, defaultTries int) *river.InsertOpts {
	opts := &river.InsertOpts{
		MaxAttempts: TriesOf(job, defaultTries),
		Queue:       queueOf(job),
	}

	if _, unique := job.(ShouldBeUnique); unique {
		opts.UniqueOpts = river.UniqueOpts{ByArgs: true, ByState: uniqueStates}
	}

	return opts
}

// WorkOptions are `queue:work`'s options.
type WorkOptions struct {
	Queues []string
	// Concurrency is how many jobs run at once in this process.
	Concurrency int
	// Sleep is how often an idle worker polls (`--sleep`); unless PollOnly,
	// inserts also wake it at once.
	Sleep time.Duration
	// Timeout is the default per-job timeout (`--timeout`); a job's own
	// Timeout wins.
	Timeout time.Duration
	// MaxTime stops the worker cleanly after this long (`--max-time`), for a
	// supervisor to restart it; zero runs until ctx is cancelled.
	MaxTime time.Duration
}

// Work processes jobs until ctx is cancelled or MaxTime passes, then stops
// gracefully: running jobs get to finish (River's soft stop) before their
// contexts are cancelled.
func (r *River) Work(ctx context.Context, options WorkOptions) error {
	config, err := r.workConfig(options)
	if err != nil {
		return err
	}

	jobTimeout := config.JobTimeout

	client, err := river.NewClient(riverpgxv5.New(r.config.Pool), config)
	if err != nil {
		return fmt.Errorf("queue: river worker: %w", err)
	}

	runCtx := ctx
	if options.MaxTime > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, options.MaxTime)
		defer cancel()
	}

	// A done Start context is River's hard stop, and leaves its listener
	// reconnecting in a tight loop: the client runs on a context only Stop
	// ends, and runCtx only says when to stop softly.
	clientCtx, hardStop := context.WithCancel(context.WithoutCancel(ctx))
	defer hardStop()

	if err := client.Start(clientCtx); err != nil {
		return fmt.Errorf("queue: starting worker: %w", err)
	}

	<-runCtx.Done()

	// A fresh context: the run context is already done, and in-flight jobs
	// deserve their full soft-stop window.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobTimeout+5*time.Second)
	defer cancel()

	return client.Stop(stopCtx)
}

// workConfig is River's client config for one worker run.
func (r *River) workConfig(options WorkOptions) (*river.Config, error) {
	queues := map[string]river.QueueConfig{}
	for _, name := range options.Queues {
		queues[name] = river.QueueConfig{MaxWorkers: max(options.Concurrency, 1)}
	}

	if len(queues) == 0 {
		return nil, errors.New("queue: no queues to work")
	}

	jobTimeout := options.Timeout
	if jobTimeout <= 0 {
		jobTimeout = 60 * time.Second
	}

	config := &river.Config{
		Logger:                      r.config.Logger,
		MaxAttempts:                 r.config.Tries,
		CompletedJobRetentionPeriod: completedJobRetention,
		Queues:                      queues,
		Workers:                     r.config.Workers.river,
		JobTimeout:                  jobTimeout,
		RescueStuckJobsAfter:        r.config.RetryAfter,
		PollOnly:                    r.config.PollOnly,
	}
	if options.Sleep > 0 {
		// River rejects a poll interval below its 100ms fetch cooldown.
		config.FetchPollInterval = max(options.Sleep, 100*time.Millisecond)
	}

	return config, nil
}

// Migrate creates or updates River's tables (river_job, river_leader, ...)
// in the pool's database — the queue:migrate command. It is idempotent.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]int, error) {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return nil, fmt.Errorf("queue: river migrator: %w", err)
	}

	result, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return nil, fmt.Errorf("queue: river migrations: %w", err)
	}

	versions := make([]int, 0, len(result.Versions))
	for _, version := range result.Versions {
		versions = append(versions, version.Version)
	}

	return versions, nil
}

// riverWorker adapts a registered handler to River's Worker interface.
type riverWorker[T Job] struct {
	river.WorkerDefaults[T]

	workers *Workers
}

func (w *riverWorker[T]) Work(ctx context.Context, job *river.Job[T]) error {
	return w.workers.Handle(ctx, job.Args, job.Attempt, job.MaxAttempts)
}

// NextRetry applies the job's Backoff; without one River's exponential
// default applies, so a failing upstream isn't hammered with instant retries.
func (w *riverWorker[T]) NextRetry(job *river.Job[T]) time.Time {
	if delay, ok := BackoffFor(job.Args, job.Attempt); ok {
		return time.Now().Add(delay) //nolint:forbidigo // River schedules the retry against the database's wall clock
	}

	return time.Time{}
}

// Timeout applies the job's Timeout; zero falls back to the worker's --timeout.
func (w *riverWorker[T]) Timeout(job *river.Job[T]) time.Duration {
	return TimeoutOf(job.Args)
}

// completedJobRetention deletes a finished job on the cleaner's next sweep:
// job args can carry secrets (a reset link, an OTP, personal details), so
// they mustn't sit in river_job for River's default 24h. Discarded jobs keep
// River's 7 days.
const completedJobRetention = time.Second
