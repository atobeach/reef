package rule_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/validation/rule"
)

type country struct{}

func (country) TableName() string { return "countries" }

func (country) Schema() *db.Schema { return nil }

type user struct{}

func (user) TableName() string { return "users" }

func (user) Schema() *db.Schema { return nil }

// checker is a connection that records the existence check it is asked
// for and answers it with found.
type checker struct {
	found bool
	sql   string
	args  []any
}

func (c *checker) QueryRow(_ context.Context, statement string, args ...any) pgx.Row {
	c.sql, c.args = statement, args

	return answer(c.found)
}

func (c *checker) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("not used")
}

func (c *checker) Query(context.Context, string, ...any) (pgx.Rows, error) { panic("not used") }

func (c *checker) Begin(context.Context) (pgx.Tx, error) { panic("not used") }

type answer bool

func (found answer) Scan(dest ...any) error {
	*dest[0].(*bool) = bool(found)

	return nil
}

// Exists and Unique query the table the model names; Unique leaves out the
// row whose id the request's context names, if any.
func TestDatabaseRulesQueryTheModelsTable(t *testing.T) {
	ignoring := func(id *int64) func(context.Context) *int64 {
		return func(context.Context) *int64 { return id }
	}

	cases := []struct {
		name   string
		rule   rule.Rule
		found  bool
		passes bool
		sql    string
		args   []any
	}{
		{"exists, a row holds it", rule.Exists[country]("name"), true, true, `select exists(select 1 from "countries" where "name" = $1)`, []any{"Portugal"}},
		{"exists, no row holds it", rule.Exists[country]("name"), false, false, `select exists(select 1 from "countries" where "name" = $1)`, []any{"Portugal"}},
		{"unique, taken", rule.Unique[user]("email").Rule(), true, false, `select exists(select 1 from "users" where "email" = $1)`, []any{"Portugal"}},
		{"unique, free", rule.Unique[user]("email").Rule(), false, true, `select exists(select 1 from "users" where "email" = $1)`, []any{"Portugal"}},
		{"unique, ignoring a row", rule.Unique[user]("email").Ignore(ignoring(new(int64(7)))).Rule(), false, true, `select exists(select 1 from "users" where "email" = $1 and "id" <> $2)`, []any{"Portugal", int64(7)}},
		{"unique, no row to ignore", rule.Unique[user]("email").Ignore(ignoring(nil)).Rule(), true, false, `select exists(select 1 from "users" where "email" = $1)`, []any{"Portugal"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &checker{found: tc.found}
			f := &rule.Field{Ctx: db.WithConn(t.Context(), conn), Attribute: "value", Value: "Portugal", Present: true}

			passed, err := tc.rule.Passes(f)
			if err != nil || passed != tc.passes {
				t.Fatalf("Passes = %v (%v), want %v", passed, err, tc.passes)
			}

			if conn.sql != tc.sql || !reflect.DeepEqual(conn.args, tc.args) {
				t.Fatalf("sent %s %v, want %s %v", conn.sql, conn.args, tc.sql, tc.args)
			}
		})
	}
}
