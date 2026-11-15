package factory_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/db/factory"
	"github.com/atobeach/reef/testkit"
)

// place is a model as codegen writes one, with a relation slot no column
// backs.
type place struct {
	ID        int64      `db:"id"`
	Name      string     `db:"name"`
	Visits    int        `db:"visits"`
	Note      *string    `db:"note"`
	CreatedAt *time.Time `db:"created_at"`
	UpdatedAt *time.Time `db:"updated_at"`
	Loaded    struct{}   `db:"-"`
}

func (place) TableName() string { return "places" }

func (place) Schema() *db.Schema {
	return &db.Schema{CreatedAt: "created_at", UpdatedAt: "updated_at"}
}

type placeFactory struct {
	factory.Builder[place, *placeFactory]
}

// places names each row after the definition's run, so every row shows it
// was made afresh.
func places(tc factory.TestCase) *placeFactory {
	made := 0

	return factory.New(tc, &placeFactory{}, func(context.Context) place {
		made++

		return place{Name: fmt.Sprintf("Place %d", made), Visits: 1}
	})
}

func (f *placeFactory) Popular() *placeFactory {
	return f.State(func(p *place) { p.Visits = 100 })
}

type testCase struct {
	t   testing.TB
	ctx context.Context
}

func (tc testCase) T() testing.TB { return tc.t }

func (tc testCase) Context() context.Context { return tc.ctx }

// conn records each insert and returns it as stored with the next id, or
// fails with err.
type conn struct {
	sql  []string
	args [][]any
	err  error
}

func (c *conn) Query(_ context.Context, statement string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = append(c.sql, statement), append(c.args, args)
	if c.err != nil {
		return nil, c.err
	}

	return &returned{id: int64(len(c.sql))}, nil
}

func (c *conn) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("not used")
}

func (c *conn) QueryRow(context.Context, string, ...any) pgx.Row { panic("not used") }

func (c *conn) Begin(context.Context) (pgx.Tx, error) { panic("not used") }

// returned is the one row an insert's RETURNING reads back: its id.
type returned struct {
	pgx.Rows
	id   int64
	read bool
}

func (r *returned) Next() bool {
	next := !r.read
	r.read = true

	return next
}

func (r *returned) FieldDescriptions() []pgconn.FieldDescription {
	return []pgconn.FieldDescription{{Name: "id"}}
}

func (r *returned) Scan(dest ...any) error {
	*dest[0].(*int64) = r.id

	return nil
}

func (r *returned) Err() error { return nil }

func (r *returned) Close() {}

// frozen is the test clock, a fraction past the second the insert stamps.
var frozen = time.Date(2026, 10, 22, 12, 0, 0, 700_000_000, time.UTC)

func setup(t *testing.T) (testCase, *conn) {
	t.Helper()

	c := &conn{}
	ctx, _ := clocktest.FrozenAt(db.WithConn(t.Context(), c), frozen)

	return testCase{t: t, ctx: ctx}, c
}

func TestCreateWritesEveryColumnAsMadeButAnUnsetIdOrTimestamp(t *testing.T) {
	stamped := frozen.Truncate(time.Second)
	imported := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	cases := []struct {
		name   string
		create func(tc testCase) place
		sql    string
		args   []any
	}{
		{
			"the definition, stamped",
			func(tc testCase) place { return places(tc).Create() },
			`insert into "places" ("created_at", "name", "note", "updated_at", "visits") values ($1, $2, $3, $4, $5) returning *`,
			[]any{stamped, "Place 1", (*string)(nil), stamped, 1},
		},
		{
			"states, then overrides in order",
			func(tc testCase) place {
				return places(tc).Popular().Create(func(p *place) { p.Visits++ }, func(p *place) { p.Visits, p.Note = p.Visits*2, new("quiet") })
			},
			`insert into "places" ("created_at", "name", "note", "updated_at", "visits") values ($1, $2, $3, $4, $5) returning *`,
			[]any{stamped, "Place 1", new("quiet"), stamped, 202},
		},
		{
			"after making, once the overrides are in",
			func(tc testCase) place {
				return places(tc).AfterMaking(func(p *place) { p.Visits *= 10 }).Create(func(p *place) { p.Visits++ })
			},
			`insert into "places" ("created_at", "name", "note", "updated_at", "visits") values ($1, $2, $3, $4, $5) returning *`,
			[]any{stamped, "Place 1", (*string)(nil), stamped, 20},
		},
		{
			"an id and a timestamp the test sets",
			func(tc testCase) place {
				return places(tc).Create(func(p *place) { p.ID, p.CreatedAt = 42, &imported })
			},
			`insert into "places" ("created_at", "id", "name", "note", "updated_at", "visits") values ($1, $2, $3, $4, $5, $6) returning *`,
			[]any{imported, int64(42), "Place 1", (*string)(nil), stamped, 1},
		},
		{
			"without timestamps, an unset one is NULL",
			func(tc testCase) place { return places(tc).WithoutTimestamps().Create() },
			`insert into "places" ("created_at", "name", "note", "updated_at", "visits") values ($1, $2, $3, $4, $5) returning *`,
			[]any{nil, "Place 1", (*string)(nil), nil, 1},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc, conn := setup(t)

			if created := c.create(tc); created.ID != 1 {
				t.Fatalf("created = %+v, want the row as stored", created)
			}

			if len(conn.sql) != 1 || conn.sql[0] != c.sql {
				t.Fatalf("sql = %q\nwant %s", conn.sql, c.sql)
			}

			if !reflect.DeepEqual(conn.args[0], c.args) {
				t.Fatalf("args = %v, want %v", conn.args[0], c.args)
			}
		})
	}
}

func TestEveryChainCallLeavesTheFactoryItWasCalledOnAsItWas(t *testing.T) {
	tc, conn := setup(t)

	// Three states leave the slice room to grow in place, which a derived
	// factory must never write into.
	base := places(tc).Popular().State(func(p *place) { p.Visits++ }).State(func(p *place) { p.Visits++ })
	tens := base.State(func(p *place) { p.Visits += 10 })
	thousands := base.State(func(p *place) { p.Visits += 1000 })

	tens.Create()
	thousands.Create()
	base.WithoutTimestamps().AfterMaking(func(p *place) { p.Visits = 0 }).Create()
	base.Create()

	stamped := frozen.Truncate(time.Second)
	for i, want := range [][]any{
		{stamped, "Place 1", (*string)(nil), stamped, 112},
		{stamped, "Place 2", (*string)(nil), stamped, 1102},
		{nil, "Place 3", (*string)(nil), nil, 0},
		{stamped, "Place 4", (*string)(nil), stamped, 102},
	} {
		if !reflect.DeepEqual(conn.args[i], want) {
			t.Fatalf("row %d = %v, want %v", i, conn.args[i], want)
		}
	}
}

func TestCountCreatesThatManyRowsEachMadeAfresh(t *testing.T) {
	tc, conn := setup(t)

	created := places(tc).Popular().Count(3).Create(func(p *place) { p.Visits++ })

	if len(created) != 3 || created[0].ID != 1 || created[2].ID != 3 {
		t.Fatalf("created = %+v", created)
	}

	for i, args := range conn.args {
		if name, visits := args[1], args[4]; name != fmt.Sprintf("Place %d", i+1) || visits != 101 {
			t.Fatalf("row %d = %v", i, args)
		}
	}
}

func TestAFailedInsertFailsTheTestNamingTheModel(t *testing.T) {
	tc, conn := setup(t)
	conn.err = errors.New("connection refused")
	recording := testkit.Record(t)
	tc.t = recording

	places(tc).Create()

	recording.AssertFailed("factory factory_test.place: db: insert into")
	recording.AssertFailed("connection refused")
}
