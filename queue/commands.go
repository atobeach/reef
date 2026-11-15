package queue

import (
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/atobeach/reef/console"
)

// Commands are queue:work and queue:migrate. connection is the app's River
// connection (nil under sync); pool opens the database the migrations target
// and returns its release func.
func Commands(connection func() *River, pool func(c *console.Context) (*pgxpool.Pool, func(), error)) []console.Command {
	return []console.Command{
		console.Func(`queue:work
			{connection? : The name of the queue connection to work (only "database" exists)}
			{--queue= : The names of the queues to work, comma-separated}
			{--tries= : Accepted for compatibility; attempts are fixed per job when it is dispatched (the job's Tries or QUEUE_TRIES)}
			{--timeout=60 : The number of seconds a job without its own timeout can run}
			{--sleep=1 : Seconds between polls for new jobs (outside poll-only mode, new jobs also wake the worker at once)}
			{--max-time=0 : The maximum number of seconds the worker should run}
			{--concurrency=10 : How many jobs this process runs at once}`,
			"Start processing jobs on the queue as a daemon",
			func(c *console.Context) error { return work(c, connection()) }),
		console.Func("queue:migrate",
			"Create or update River's queue tables (`migrate` runs this too)",
			func(c *console.Context) error { return migrate(c, pool) }),
	}
}

// work runs queue:work on the River connection, nil under the sync connection.
func work(c *console.Context, river *River) error {
	if river == nil {
		return c.Failf("queue:work needs QUEUE_CONNECTION=database; the sync connection runs jobs inline.")
	}

	if name := c.Argument("connection"); name != "" && name != "database" {
		return c.Failf("Queue connection [%s] is not defined.", name)
	}

	queues := []string{DefaultQueue}
	if list := c.Option("queue"); list != "" {
		queues = strings.Split(list, ",")
	}

	options, err := workOptions(c, queues)
	if err != nil {
		return err
	}

	c.Infof("Processing jobs from the [%s] queue.", strings.Join(queues, ","))

	return river.Work(c, options)
}

// workOptions reads queue:work's numeric options; the error names the first
// invalid one.
func workOptions(c *console.Context, queues []string) (WorkOptions, error) {
	options := WorkOptions{Queues: queues}
	for name, target := range map[string]*time.Duration{"timeout": &options.Timeout, "sleep": &options.Sleep, "max-time": &options.MaxTime} {
		seconds, err := c.SecondsOption(name)
		if err != nil {
			return options, err
		}

		*target = seconds
	}

	concurrency, err := c.IntOption("concurrency", 1)
	if err != nil {
		return options, err
	}

	options.Concurrency = concurrency

	return options, nil
}

// migrate runs queue:migrate against the database pool opens.
func migrate(c *console.Context, pool func(c *console.Context) (*pgxpool.Pool, func(), error)) error {
	target, release, err := pool(c)
	if err != nil {
		return err
	}
	defer release()

	versions, err := Migrate(c, target)
	if err != nil {
		return err
	}

	if len(versions) == 0 {
		c.Info("Nothing to migrate.")

		return nil
	}

	for _, version := range versions {
		c.Linef("  River migration %03d ... DONE", version)
	}

	return nil
}
