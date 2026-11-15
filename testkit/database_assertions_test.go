package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/testkit"
)

// rows is a query that counts n rows, or fails with err.
type rows struct {
	n   int64
	err error
}

func (r rows) Count() (int64, error) { return r.n, r.err }

func (rows) ToSQL() (sql string, args []any) {
	return `select * from "bookings" where "id" = $1`, []any{42}
}

func TestDatabaseAssertionsPassOnTheirCountAndFailSayingWhatMatched(t *testing.T) {
	broken := rows{err: errors.New("connection refused")}

	cases := []struct {
		name   string
		assert func(t *testkit.RecordingT)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"has", func(t *testkit.RecordingT) { testkit.AssertDatabaseHas(t, rows{n: 2}) }, ""},
		{
			"has nothing", func(t *testkit.RecordingT) { testkit.AssertDatabaseHas(t, rows{}) },
			`database: 0 rows match select * from "bookings" where "id" = $1 [42], want some`,
		},
		{"missing", func(t *testkit.RecordingT) { testkit.AssertDatabaseMissing(t, rows{}) }, ""},
		{
			"missing but there", func(t *testkit.RecordingT) { testkit.AssertDatabaseMissing(t, rows{n: 1}) },
			`database: 1 row matches select * from "bookings" where "id" = $1 [42], want none`,
		},
		{"count", func(t *testkit.RecordingT) { testkit.AssertDatabaseCount(t, rows{n: 3}, 3) }, ""},
		{"count off", func(t *testkit.RecordingT) { testkit.AssertDatabaseCount(t, rows{n: 3}, 1) }, "3 rows match"},
		{"empty", func(t *testkit.RecordingT) { testkit.AssertDatabaseEmpty(t, rows{}) }, ""},
		{"empty but not", func(t *testkit.RecordingT) { testkit.AssertDatabaseEmpty(t, rows{n: 2}) }, "2 rows match"},

		// A query that fails never passes, whatever it was asserting.
		{"has, query failed", func(t *testkit.RecordingT) { testkit.AssertDatabaseHas(t, broken) }, "connection refused"},
		{"missing, query failed", func(t *testkit.RecordingT) { testkit.AssertDatabaseMissing(t, broken) }, "connection refused"},
		{"count of none, query failed", func(t *testkit.RecordingT) { testkit.AssertDatabaseCount(t, broken, 0) }, "connection refused"},
		{"empty, query failed", func(t *testkit.RecordingT) { testkit.AssertDatabaseEmpty(t, broken) }, "connection refused"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recording := testkit.Record(t)
			c.assert(recording)

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}

// The builder is Countable: a query with no connection to run on fails the
// assertion, naming the query, instead of counting as no rows.
func TestABuilderQueryThatCannotRunFailsTheAssertion(t *testing.T) {
	recording := testkit.Record(t)
	testkit.AssertDatabaseMissing(recording, db.Table[int](context.Background(), "bookings").Where("id", 42))

	recording.AssertFailed(`database: counting select * from "bookings" where "id" = $1 [42]: ` + db.ErrNoConnection.Error())
}
