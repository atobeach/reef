package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// recordingTx is a transaction that records the contexts it was rolled
// back and committed on.
type recordingTx struct {
	pgx.Tx
	rollbacks []context.Context
	commits   int
}

func (tx *recordingTx) Rollback(ctx context.Context) error {
	tx.rollbacks = append(tx.rollbacks, ctx)

	return nil
}

func (tx *recordingTx) Commit(context.Context) error {
	tx.commits++

	return nil
}

// beginsTx is a connection whose Begin hands out tx.
type beginsTx struct {
	Querier
	tx *recordingTx
}

func (q beginsTx) Begin(context.Context) (pgx.Tx, error) {
	return q.tx, nil
}

type callerKey struct{}

// A client that leaves mid-transaction still gets its work rolled back,
// never committed: pgx refuses a done context, so the rollback runs on one
// that isn't, carrying the caller's values.
func TestATransactionRollsBackAfterItsCallerLeft(t *testing.T) {
	cases := []struct {
		name      string
		panicking bool
		// then is the rest of fn, once the caller left.
		then func(ctx context.Context) error
	}{
		{"fn returns the cancellation", false, func(ctx context.Context) error { return ctx.Err() }},
		{"fn panics", true, func(context.Context) error { panic("nil map") }},
		{"fn finishes after its caller left", false, func(context.Context) error { return nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &recordingTx{}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), callerKey{}, "guest-1"))
			ctx = WithConn(ctx, beginsTx{tx: tx})

			var err error
			recovered := func() (recovered any) {
				defer func() { recovered = recover() }()

				err = Transaction(ctx, func(ctx context.Context) error {
					cancel()

					return tc.then(ctx)
				})

				return nil
			}()

			if tc.panicking && recovered != "nil map" {
				t.Fatalf("recovered %v, want the panic raised again", recovered)
			}
			if !tc.panicking && !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the cancellation", err)
			}

			if len(tx.rollbacks) != 1 || tx.commits != 0 {
				t.Fatalf("rolled back %d times, committed %d; want one rollback", len(tx.rollbacks), tx.commits)
			}
			if rollback := tx.rollbacks[0]; rollback.Err() != nil || rollback.Value(callerKey{}) != "guest-1" {
				t.Fatalf("rolled back on a context with Err %v and caller %v", rollback.Err(), rollback.Value(callerKey{}))
			}
		})
	}
}
