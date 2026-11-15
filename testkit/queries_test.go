package testkit

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeTx answers every statement without a database.
type fakeTx struct {
	pgx.Tx
}

// noRows is a query's empty answer.
type noRows struct {
	pgx.Rows
}

func (noRows) Close() {}

func (fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return noRows{}, nil }

func (fakeTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (fakeTx) Begin(context.Context) (pgx.Tx, error) { return fakeTx{}, nil }

func TestCountQueriesCountsFromNowSavepointsIncluded(t *testing.T) {
	ctx := t.Context()
	tx := &Transaction{Tx: fakeTx{}, statements: &atomic.Int64{}}

	// Arranging the test runs statements too; they come before the count.
	_, _ = tx.Exec(ctx, "insert into users default values")

	queries := tx.CountQueries(t)
	recording := Record(t)
	miscounted := tx.CountQueries(recording)

	rows, _ := tx.Query(ctx, "select * from profiles")
	rows.Close()
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	savepoint.QueryRow(ctx, "select * from countries")
	_, _ = savepoint.Exec(ctx, "update profiles set name = 'Ana'")

	queries.Assert(3)

	miscounted.Assert(2)
	recording.AssertFailed("ran 3 queries, want 2")
}
