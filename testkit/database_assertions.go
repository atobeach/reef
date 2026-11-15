package testkit

import (
	"fmt"
	"strconv"
	"testing"
)

// Countable is a query that counts its rows: a generated model query or a
// db.Table.
//
//	testkit.AssertDatabaseMissing(t, models.Bookings(ctx).WhereKey(booking.ID))
//	testkit.AssertDatabaseHas(t, db.Table[int](ctx, "country_user").Where("user_id", user.ID))
type Countable interface {
	Count() (int64, error)
	ToSQL() (sql string, args []any)
}

// AssertDatabaseHas fails unless some row matches q.
func AssertDatabaseHas(t testing.TB, q Countable) {
	t.Helper()

	assertRows(t, q, func(n int64) bool { return n > 0 }, "some")
}

// AssertDatabaseMissing fails if any row matches q.
func AssertDatabaseMissing(t testing.TB, q Countable) {
	t.Helper()

	assertRows(t, q, func(n int64) bool { return n == 0 }, "none")
}

// AssertDatabaseCount fails unless exactly want rows match q.
func AssertDatabaseCount(t testing.TB, q Countable, want int64) {
	t.Helper()

	assertRows(t, q, func(n int64) bool { return n == want }, strconv.FormatInt(want, 10))
}

// AssertDatabaseEmpty fails if any row matches q. Give it an unfiltered
// query (models.SavedSearches(ctx)) to assert its table is empty.
func AssertDatabaseEmpty(t testing.TB, q Countable) {
	t.Helper()

	assertRows(t, q, func(n int64) bool { return n == 0 }, "none")
}

// assertRows counts q's rows and fails unless ok accepts the count. A count
// that errors fails too: an assertion never passes on a query that didn't run.
func assertRows(t testing.TB, q Countable, ok func(n int64) bool, want string) {
	t.Helper()

	n, err := q.Count()
	if err != nil {
		t.Fatalf("database: counting %s: %v", describe(q), err)

		return
	}

	if ok(n) {
		return
	}

	match := "rows match"
	if n == 1 {
		match = "row matches"
	}

	t.Fatalf("database: %d %s %s, want %s", n, match, describe(q), want)
}

// describe is q's SQL with its bindings.
func describe(q Countable) string {
	sql, args := q.ToSQL()
	if len(args) == 0 {
		return sql
	}

	return fmt.Sprintf("%s %v", sql, args)
}
