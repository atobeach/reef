package testkit

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Transaction is a test's rolled-back transaction, counting the statements
// run on it and its savepoints so a test can assert how many queries ran.
type Transaction struct {
	pgx.Tx
	statements *atomic.Int64
}

// Exec runs a statement and counts it.
func (tx *Transaction) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.statements.Add(1)

	return tx.Tx.Exec(ctx, sql, args...)
}

// Query runs a statement and counts it.
func (tx *Transaction) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.statements.Add(1)

	return tx.Tx.Query(ctx, sql, args...)
}

// QueryRow runs a statement and counts it.
func (tx *Transaction) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.statements.Add(1)

	return tx.Tx.QueryRow(ctx, sql, args...)
}

// Begin opens a savepoint whose statements count as the transaction's own.
func (tx *Transaction) Begin(ctx context.Context) (pgx.Tx, error) {
	savepoint, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}

	return &Transaction{Tx: savepoint, statements: tx.statements}, nil
}

// CountQueries starts counting the statements run from now:
//
//	queries := tx.CountQueries(t)
//	// ... the work under test ...
//	queries.Assert(3)
func (tx *Transaction) CountQueries(t testing.TB) *QueryCounter {
	t.Helper()

	return &QueryCounter{t: t, statements: tx.statements, start: tx.statements.Load()}
}

// QueryCounter counts the statements a test transaction runs from the moment
// CountQueries made it.
type QueryCounter struct {
	t          testing.TB
	statements *atomic.Int64
	start      int64
}

// Assert fails unless exactly want statements ran since the counter started.
func (c *QueryCounter) Assert(want int64) {
	c.t.Helper()

	if got := c.statements.Load() - c.start; got != want {
		c.t.Errorf("ran %d queries, want %d", got, want)
	}
}
