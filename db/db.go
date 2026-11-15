// Package db is the data layer: a fluent, typed query builder over pgx. The
// connection travels in the context.Context, so inside Transaction every
// query built from the callback's ctx runs on the transaction.
package db

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is what both a pool and a transaction can do, so builders never care
// which one they run on.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

type connKey struct{}

// ErrNoConnection means a query ran on a context the kernel never bound a
// connection to — a programming error, surfaced loudly instead of hanging.
var ErrNoConnection = errors.New("db: no connection bound to context (use db.WithConn)")

// Open connects a pool from a postgres:// URL and verifies it with a ping, so
// a bad DSN fails at boot rather than on the first request.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	config, err := PoolConfig(url)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return pool, nil
}

// PoolConfig parses a URL into the pool settings every pool uses. Queries
// run as unnamed statements (QueryExecModeExec): with pgx's statement cache,
// a column a migration adds while the app serves fails every cached
// `SELECT *` plan ("cached plan must not change result type").
func PoolConfig(url string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parse url: %w", err)
	}

	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	return config, nil
}

// WithConn binds a connection (pool or transaction) to the context.
func WithConn(ctx context.Context, conn Querier) context.Context {
	return context.WithValue(ctx, connKey{}, conn)
}

// Conn returns the connection bound to the context.
func Conn(ctx context.Context) (Querier, error) {
	conn, ok := ctx.Value(connKey{}).(Querier)
	if !ok || conn == nil {
		return nil, ErrNoConnection
	}

	return conn, nil
}

// Transaction runs fn inside a transaction, committing when it returns nil and
// rolling back on an error or panic, even once ctx is cancelled. Nested calls
// become savepoints, because pgx's Tx.Begin issues SAVEPOINT. Whether work
// outlives its client is the caller's choice: once ctx is done, it is rolled
// back rather than committed, and ctx's error is returned.
func Transaction(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	conn, err := Conn(ctx)
	if err != nil {
		return err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}

	// pgx refuses a done context, which kills a pooled connection or leaves
	// a savepoint in place; a rollback is cleanup that must run whoever left.
	rollback := context.WithoutCancel(ctx)
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback(rollback)
			panic(recovered)
		}

		if err != nil {
			_ = tx.Rollback(rollback)
		}
	}()

	if err := fn(WithConn(ctx, tx)); err != nil {
		return err
	}

	// A commit on a done context fails the same way: the connection is
	// closed, or the savepoint is marked released and so never rolled back.
	if err := ctx.Err(); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ModelNotFoundError is the error of FirstOrFail, FindOrFail and
// ResolveRouteBinding, rendered as a 404 whose message names the model in
// words: Model "SavedSearch" reads "Saved search not found.".
type ModelNotFoundError struct {
	Model string
}

func (e *ModelNotFoundError) Error() string {
	var words strings.Builder
	for i, r := range e.Model {
		if i > 0 && unicode.IsUpper(r) {
			words.WriteByte(' ')
			r = unicode.ToLower(r)
		}
		words.WriteRune(r)
	}

	return words.String() + " not found."
}

// HTTPStatus lets the HTTP kernel map the error without importing this package.
func (e *ModelNotFoundError) HTTPStatus() int {
	return http.StatusNotFound
}

// Exec runs a raw statement and returns the affected row count.
func Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	conn, err := Conn(ctx)
	if err != nil {
		return 0, err
	}

	tag, err := conn.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("db: %s: %w", sql, err)
	}

	return tag.RowsAffected(), nil
}
