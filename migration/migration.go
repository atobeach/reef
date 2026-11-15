// Package migration runs the schema migrations on goose: embedded
// NNNNN_name.sql files applied in order, serialised across servers
// by a Postgres advisory lock. Adopt records, without running them, the
// migrations whose schema a database already holds from an earlier history.
package migration

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

// VersionTable is goose's bookkeeping table (its default name).
const VersionTable = "goose_db_version"

// Migration is one migration file goose found, identified by its version.
type Migration struct {
	Version int64
	// Name is the file name without the extension: "00002_add_x_to_y".
	Name string
}

// Status is one migration's state on the database.
type Status struct {
	Migration
	Applied bool
}

// Migrator runs the migrations in FS against one database.
type Migrator struct {
	db         *sql.DB
	provider   *goose.Provider
	migrations []Migration
}

// New builds a migrator over the pool (it never closes the pool). The pool
// must be a real session — the direct endpoint, not a transaction pooler —
// because the advisory lock is session-scoped.
func New(pool *pgxpool.Pool, fsys fs.FS) (*Migrator, error) {
	migrations, err := Parse(fsys)
	if err != nil {
		return nil, err
	}

	db := stdlib.OpenDBFromPool(pool)

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_ = db.Close()

		return nil, err
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys,
		goose.WithDisableGlobalRegistry(true),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migration: %w", err)
	}

	return &Migrator{db: db, provider: provider, migrations: migrations}, nil
}

// Close releases the database/sql handle (not the pool).
func (m *Migrator) Close() error {
	return m.db.Close()
}

// Up runs every pending migration in order, each in its own transaction
// (unless the file says `-- +goose NO TRANSACTION`).
func (m *Migrator) Up(ctx context.Context) ([]*goose.MigrationResult, error) {
	results, err := m.provider.Up(ctx)
	if err != nil {
		return results, fmt.Errorf("migration: %w", err)
	}

	return results, nil
}

// Status reports every migration, applied or pending. It reads only: on a
// database with no version table yet, everything is pending.
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	applied, err := m.appliedVersions(ctx, m.db)
	if err != nil {
		return nil, err
	}

	statuses := make([]Status, 0, len(m.migrations))
	for _, migration := range m.migrations {
		_, ok := applied[migration.Version]
		statuses = append(statuses, Status{Migration: migration, Applied: ok})
	}

	return statuses, nil
}

// ErrBaseline refuses a rollback that would include the first migration: its
// down would drop every table.
var ErrBaseline = errors.New("the schema baseline cannot be rolled back")

// Rollback rolls back the last steps applied migrations, newest first.
func (m *Migrator) Rollback(ctx context.Context, steps int) ([]*goose.MigrationResult, error) {
	if steps < 1 {
		return nil, fmt.Errorf("migration: --step must be at least 1, got %d", steps)
	}

	statuses, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}

	var applied []Status
	for _, status := range statuses {
		if status.Applied {
			applied = append(applied, status)
		}
	}

	if len(applied) == 0 {
		return nil, nil
	}

	targets := applied[max(len(applied)-steps, 0):]
	if len(m.migrations) > 0 && targets[0].Version == m.migrations[0].Version {
		return nil, fmt.Errorf("migration: %s: %w", targets[0].Name, ErrBaseline)
	}

	results, err := m.provider.DownTo(ctx, applied[len(applied)-len(targets)-1].Version)
	if err != nil {
		return results, fmt.Errorf("migration: %w", err)
	}

	return results, nil
}

// appliedVersions reads goose's version table; a missing table is no versions.
func (m *Migrator) appliedVersions(ctx context.Context, conn database.DBTxConn) (map[int64]struct{}, error) {
	exists, err := tableExists(ctx, conn, VersionTable)
	if err != nil || !exists {
		return map[int64]struct{}{}, err
	}

	// goose's own reading: a version is applied when its latest row says so.
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT ON (version_id) version_id, is_applied
		FROM `+VersionTable+` ORDER BY version_id, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("migration: reading %s: %w", VersionTable, err)
	}
	defer rows.Close()

	applied := map[int64]struct{}{}
	for rows.Next() {
		var version int64
		var isApplied bool
		if err := rows.Scan(&version, &isApplied); err != nil {
			return nil, err
		}

		if isApplied && version > 0 {
			applied[version] = struct{}{}
		}
	}

	return applied, rows.Err()
}

func tableExists(ctx context.Context, conn database.DBTxConn, table string) (bool, error) {
	var exists bool
	err := conn.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("migration: checking for %s: %w", table, err)
	}

	return exists, nil
}

// Parse reads the migrations in fsys: NNNNN_name.sql files.
func Parse(fsys fs.FS) ([]Migration, error) {
	files, err := sqlFiles(fsys)
	if err != nil {
		return nil, err
	}

	migrations := make([]Migration, 0, len(files))
	for _, file := range files {
		name := strings.TrimSuffix(file, ".sql")
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if !ok || err != nil || version < 1 {
			return nil, fmt.Errorf("migration: %s: the name must be NNNNN_name.sql", file)
		}

		migrations = append(migrations, Migration{Version: version, Name: name})
	}

	slices.SortFunc(migrations, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })

	for i := 1; i < len(migrations); i++ {
		if migrations[i].Version == migrations[i-1].Version {
			return nil, fmt.Errorf("migration: %s and %s share version %d", migrations[i-1].Name, migrations[i].Name, migrations[i].Version)
		}
	}

	return migrations, nil
}

// sqlFiles lists the .sql files at the root of fsys, in name order.
func sqlFiles(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && path.Ext(entry.Name()) == ".sql" {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}

// Fingerprint hashes every migration file, so a test database is rebuilt
// exactly when a migration changes.
func Fingerprint(fsys fs.FS) (string, error) {
	files, err := sqlFiles(fsys)
	if err != nil {
		return "", err
	}

	digest := sha256.New()
	for _, file := range files {
		source, err := fs.ReadFile(fsys, file)
		if err != nil {
			return "", fmt.Errorf("migration: %w", err)
		}

		digest.Write(fmt.Appendf(nil, "%s\x00%d\x00", file, len(source)))
		digest.Write(source)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}
