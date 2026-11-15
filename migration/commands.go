package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/atobeach/reef/console"
)

// Commands configures the migrate commands.
type Commands struct {
	// Pool opens the database to migrate and returns its release func. It
	// should be a direct session (see New), not a transaction pooler.
	Pool func(c *console.Context) (*pgxpool.Pool, func(), error)
	// FS holds the migrations (embedded in the binary).
	FS fs.FS
	// Dir is where make:migration writes, relative to the working directory
	// (the module root): "database/migrations".
	Dir string
	// Adopt, when set, runs before the migrations to record the ones a
	// database migrated by an earlier tool already holds (see Migrator.Adopt).
	Adopt func(c *console.Context, tx *sql.Tx, pending []Migration) ([]Migration, error)
	// After runs once the app's migrations are up — River's queue tables —
	// so one migrate command prepares the whole database.
	After func(c *console.Context, pool *pgxpool.Pool) error
}

// All is migrate, migrate:status, migrate:rollback and make:migration.
func (cmds Commands) All() []console.Command {
	return []console.Command{
		console.Func(`migrate
			{--force : Accepted so deploy scripts can pass it; the command never prompts}`,
			"Run the database migrations (and River's queue migrations)",
			func(c *console.Context) error {
				return cmds.withMigrator(c, cmds.up)
			}),
		console.Func("migrate:status", "Show the status of each migration",
			func(c *console.Context) error {
				return cmds.withMigrator(c, status)
			}),
		console.Func(`migrate:rollback
			{--step=1 : The number of migrations to be reverted}`,
			"Rollback the last database migrations (never the baseline)",
			func(c *console.Context) error {
				steps, err := c.IntOption("step", 1)
				if err != nil {
					return err
				}

				return cmds.withMigrator(c, func(c *console.Context, m *Migrator, _ *pgxpool.Pool) error {
					return rollback(c, m, steps)
				})
			}),
		console.Func(`make:migration
			{name : The name of the migration, e.g. add_votes_to_users_table}`,
			"Create a new migration file",
			func(c *console.Context) error {
				path, err := Create(cmds.Dir, c.Argument("name"))
				if err != nil {
					return err
				}

				c.Infof("Migration [%s] created successfully.", path)

				return nil
			}),
	}
}

// up runs Adopt and every pending migration, then After.
func (cmds Commands) up(c *console.Context, m *Migrator, pool *pgxpool.Pool) error {
	var recorded []Migration
	if cmds.Adopt != nil {
		var err error
		recorded, err = m.Adopt(c, func(_ context.Context, tx *sql.Tx, pending []Migration) ([]Migration, error) {
			return cmds.Adopt(c, tx, pending)
		})
		if err != nil {
			return err
		}
	}

	for _, migration := range recorded {
		c.Linef("  %s ... RECORDED (its schema is already there)", migration.Name)
	}

	results, err := m.Up(c)
	for _, result := range results {
		state := "DONE"
		if result.Error != nil {
			state = "FAIL"
		}

		c.Linef("  %s ... %s %s", resultName(result), truncate(result.Duration), state)
	}

	if err != nil {
		return err
	}

	if len(results) == 0 && len(recorded) == 0 {
		c.Info("Nothing to migrate.")
	}

	if cmds.After != nil {
		return cmds.After(c, pool)
	}

	return nil
}

// status prints each migration as Ran or Pending, dot-padded to one column.
func status(c *console.Context, m *Migrator, _ *pgxpool.Pool) error {
	statuses, err := m.Status(c)
	if err != nil {
		return err
	}

	width := 0
	for _, status := range statuses {
		width = max(width, len(status.Name))
	}

	for _, status := range statuses {
		state := "Pending"
		if status.Applied {
			state = "Ran"
		}

		c.Linef("  %s %s %s", status.Name, strings.Repeat(".", width-len(status.Name)+3), state)
	}

	return nil
}

func rollback(c *console.Context, m *Migrator, steps int) error {
	results, err := m.Rollback(c, steps)
	for _, result := range results {
		c.Linef("  %s ... %s DONE", resultName(result), truncate(result.Duration))
	}

	if err == nil && len(results) == 0 {
		c.Info("Nothing to rollback.")
	}

	return err
}

// withMigrator runs fn with a migrator on the configured pool.
func (cmds Commands) withMigrator(c *console.Context, fn func(c *console.Context, m *Migrator, pool *pgxpool.Pool) error) (err error) {
	pool, release, err := cmds.Pool(c)
	if err != nil {
		return err
	}
	defer release()

	m, err := New(pool, cmds.FS)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, m.Close()) }()

	return fn(c, m, pool)
}

var migrationName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Create writes dir/NNNNN_name.sql, numbered one past the newest migration.
func Create(dir string, name string) (string, error) {
	if !migrationName.MatchString(name) {
		return "", fmt.Errorf("the migration name %q must be snake_case (letters, digits, underscores)", name)
	}

	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("%s not found: run make:migration from the module root", dir)
	}

	existing, err := Parse(os.DirFS(dir))
	if err != nil {
		return "", err
	}

	next := int64(1)
	for _, migration := range existing {
		if strings.TrimPrefix(migration.Name, fmt.Sprintf("%05d_", migration.Version)) == name {
			return "", fmt.Errorf("a migration named %s already exists: %s", name, migration.Name)
		}

		next = max(next, migration.Version+1)
	}

	path := filepath.Join(dir, fmt.Sprintf("%05d_%s.sql", next, name))
	//nolint:gosec // G302: a checked-in migration source file, readable like every other source file
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}

	_, err = file.WriteString(stub)

	return path, errors.Join(err, file.Close())
}

// stub is a new migration. Each runs in a transaction; CREATE/DROP INDEX
// CONCURRENTLY can't, and needs `-- +goose NO TRANSACTION` above Up.
const stub = `-- Why this change (the column, the index, the query it serves).

-- +goose Up


-- +goose Down

`

// resultName is a goose result's migration name, without the extension.
func resultName(result *goose.MigrationResult) string {
	return strings.TrimSuffix(filepath.Base(result.Source.Path), ".sql")
}

func truncate(d time.Duration) string {
	return d.Round(10 * time.Microsecond).String()
}
