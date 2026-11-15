// Package testkit is the framework's testing layer: a migrated database
// leased to each test, with per-test rollback (RefreshDatabase), assertions
// on its rows (AssertDatabaseHas and the rest) and on how many queries ran
// (CountQueries), and fluent response assertions (TestResponse).
package testkit

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/migration"
	"github.com/atobeach/reef/queue"
)

// leaseConnections bounds each leased database's pool: a test holds one
// connection for its transaction, and a committed race a dozen at most.
const leaseConnections = 16

// databases are the test binary's leased databases, idle ones kept for the
// next test.
var databases struct {
	mu    sync.Mutex
	idle  []*pgxpool.Pool
	count int
}

// Database leases the test a migrated database of its own until it ends, so
// parallel tests never see or lock each other's rows. Leases are
// <database>_<package hash>_<n>, since packages test concurrently; they are
// kept between runs and rebuilt when a migration changes, so two checkouts
// testing at once need different prefixes.
func Database(t testing.TB, adminURL string, testURL string, database string, migrations fs.FS) *pgxpool.Pool {
	t.Helper()

	pool, err := lease(context.Background(), adminURL, testURL, database, migrations)
	if err != nil {
		t.Fatalf("test database: %v", err)
	}

	t.Cleanup(func() {
		databases.mu.Lock()
		defer databases.mu.Unlock()

		databases.idle = append(databases.idle, pool)
	})

	return pool
}

// lease hands out an idle database, or opens the next one.
func lease(ctx context.Context, adminURL string, testURL string, database string, migrations fs.FS) (*pgxpool.Pool, error) {
	databases.mu.Lock()
	if n := len(databases.idle); n > 0 {
		pool := databases.idle[n-1]
		databases.idle = databases.idle[:n-1]
		databases.mu.Unlock()

		return pool, nil
	}

	databases.count++
	n := databases.count
	databases.mu.Unlock()

	name, err := leaseName(database, n)
	if err != nil {
		return nil, err
	}

	return Migrated(ctx, adminURL, testURL, name, migrations)
}

// leaseName is the nth lease's database, named by the test binary's import
// path so it is the same wherever the checkout lives.
func leaseName(database string, n int) (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("naming the test database: the test binary carries no build info")
	}

	sum := fnv.New32a()
	_, _ = sum.Write([]byte(info.Path))

	return fmt.Sprintf("%s_%08x_%d", database, sum.Sum32(), n), nil
}

// RefreshDatabase opens a transaction that is rolled back when the test ends,
// so every test starts from the empty migrated schema.
func RefreshDatabase(t testing.TB, pool *pgxpool.Pool) *Transaction {
	t.Helper()

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}

	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	return &Transaction{Tx: tx, statements: &atomic.Int64{}}
}

// Migrated opens database on testURL's server, creating and migrating it
// when it is missing or a migration changed: a leased test database, or code
// generation's scratch one.
func Migrated(ctx context.Context, adminURL string, testURL string, database string, migrations fs.FS) (*pgxpool.Pool, error) {
	// This drops `public`: a misconfigured name pointing at a real database
	// must fail, not wipe it.
	if !strings.Contains(database, "test") {
		return nil, fmt.Errorf("refusing to use %q as the test database: its name must contain \"test\"", database)
	}

	hash, err := migration.Fingerprint(migrations)
	if err != nil {
		return nil, err
	}

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return nil, fmt.Errorf("connect admin: %w", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	if err := CreateDatabase(ctx, admin, database); err != nil {
		return nil, err
	}

	leased, err := url.Parse(testURL)
	if err != nil {
		return nil, fmt.Errorf("test database url: %w", err)
	}
	leased.Path = "/" + database

	config, err := db.PoolConfig(leased.String())
	if err != nil {
		return nil, err
	}
	config.MaxConns = leaseConnections

	testPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := migrate(ctx, testPool, migrations, hash); err != nil {
		testPool.Close()

		return nil, err
	}

	return testPool, nil
}

// CreateDatabase creates the database unless it exists.
func CreateDatabase(ctx context.Context, admin *pgx.Conn, database string) error {
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", database).Scan(&exists); err != nil {
		return err
	}

	if exists {
		return nil
	}

	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		return fmt.Errorf("create %s: %w", database, err)
	}

	return nil
}

// migrate rebuilds the public schema from the migrations when their
// fingerprint changed.
func migrate(ctx context.Context, testPool *pgxpool.Pool, migrations fs.FS, hash string) error {
	var current string
	err := testPool.QueryRow(ctx, "SELECT hash FROM public.testkit_schema").Scan(&current)
	if err == nil && current == hash {
		return nil
	}

	var pgErr interface{ SQLState() string }
	// 42P01: the fingerprint table doesn't exist yet (a fresh database).
	missingTable := errors.As(err, &pgErr) && pgErr.SQLState() == "42P01"
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !missingTable {
		return err
	}

	if _, err := testPool.Exec(ctx, "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public"); err != nil {
		return fmt.Errorf("reset schema: %w", err)
	}

	migrator, err := migration.New(testPool, migrations)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	if _, err := migrator.Up(ctx); err != nil {
		return err
	}

	if _, err := queue.Migrate(ctx, testPool); err != nil {
		return err
	}

	_, err = testPool.Exec(ctx, "CREATE TABLE public.testkit_schema (hash text NOT NULL); INSERT INTO public.testkit_schema (hash) VALUES ('"+hash+"')")

	return err
}
