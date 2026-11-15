package migration

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

// AdoptFunc picks, from the pending migrations in version order, a leading
// run (goose's history stays gapless) whose schema the database already
// holds. It may read through tx but must not change the schema.
type AdoptFunc func(ctx context.Context, tx *sql.Tx, pending []Migration) ([]Migration, error)

// Adopt records as applied, without running them, the pending migrations
// adopt picks, for a database another tool first migrated. One transaction
// under goose's advisory lock id keeps servers migrating at once from interleaving,
// and an error changes nothing.
func (m *Migrator) Adopt(ctx context.Context, adopt AdoptFunc) (recorded []Migration, err error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("migration: adopt: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", lock.DefaultLockID); err != nil {
		return nil, fmt.Errorf("migration: adopt lock: %w", err)
	}

	applied, err := m.appliedVersions(ctx, tx)
	if err != nil {
		return nil, err
	}

	var pending []Migration
	for _, migration := range m.migrations {
		if _, ok := applied[migration.Version]; !ok {
			pending = append(pending, migration)
		}
	}

	if len(pending) == 0 {
		return nil, tx.Commit()
	}

	recorded, err = adopt(ctx, tx, pending)
	if err != nil {
		return nil, err
	}

	for i, migration := range recorded {
		if i >= len(pending) || migration.Version != pending[i].Version {
			return nil, fmt.Errorf("migration: adopt picked %s out of order: only a leading run of the pending migrations can be recorded", migration.Name)
		}
	}

	if err := recordApplied(ctx, tx, recorded); err != nil {
		return nil, err
	}

	return recorded, tx.Commit()
}

// recordApplied marks the migrations applied in goose's version table,
// creating the table exactly as goose would (with its version-0 row) when it
// is missing.
func recordApplied(ctx context.Context, tx *sql.Tx, record []Migration) error {
	if len(record) == 0 {
		return nil
	}

	store, err := database.NewStore(database.DialectPostgres, VersionTable)
	if err != nil {
		return fmt.Errorf("migration: %w", err)
	}

	exists, err := tableExists(ctx, tx, VersionTable)
	if err != nil {
		return err
	}

	if !exists {
		if err := store.CreateVersionTable(ctx, tx); err != nil {
			return fmt.Errorf("migration: creating %s: %w", VersionTable, err)
		}

		if err := store.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
			return fmt.Errorf("migration: seeding %s: %w", VersionTable, err)
		}
	}

	for _, migration := range record {
		if err := store.Insert(ctx, tx, database.InsertRequest{Version: migration.Version}); err != nil {
			return fmt.Errorf("migration: recording %s: %w", migration.Name, err)
		}
	}

	return nil
}
