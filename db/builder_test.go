package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/atobeach/reef/clock/clocktest"
)

type widget struct {
	ID int64 `db:"id"`
}

func (widget) TableName() string { return "widgets" }

func (widget) Schema() *Schema { return nil }

type widgetQuery struct {
	Builder[widget, *widgetQuery]
}

func widgets() *widgetQuery {
	return NewQuery[widget](context.Background(), &widgetQuery{})
}

// Visible is a scope: it must chain before and after builder methods.
func (q *widgetQuery) Visible() *widgetQuery {
	return q.Where("visible", true)
}

func TestCompilesAFluentSelect(t *testing.T) {
	sql, args := widgets().
		Where("country_id", 7).
		Visible().
		Join("countries", "countries.id", "=", "widgets.country_id").
		Select("widgets.name", "countries.slug AS country_slug").
		SelectRaw("COALESCE(widgets.slug, ?) AS slug", "").
		WhereNotNull("published_at").
		WhereRaw("popularity > ? OR featured", 10).
		OrderByRaw("popularity IS NULL, popularity DESC").
		OrderBy("name").
		Limit(5).
		ToSQL()

	want := `select "widgets"."name", "countries"."slug" as "country_slug", COALESCE(widgets.slug, $3) AS slug ` +
		`from "widgets" inner join "countries" on "countries"."id" = "widgets"."country_id" ` +
		`where "country_id" = $1 and "visible" = $2 and "published_at" is not null and (popularity > $4 OR featured) ` +
		`order by popularity IS NULL, popularity DESC, "name" asc limit 5`
	if sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", sql, want)
	}

	if wantArgs := []any{7, true, "", 10}; !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
}

func TestDoubleQuestionMarkIsALiteralOperator(t *testing.T) {
	sql, args := widgets().WhereRaw("tags ?? ? AND id > ?", "beach", 5).ToSQL()

	if want := `select * from "widgets" where (tags ? $1 AND id > $2)`; sql != want {
		t.Fatalf("sql = %s, want %s", sql, want)
	}

	if !reflect.DeepEqual(args, []any{"beach", 5}) {
		t.Fatalf("args = %v", args)
	}
}

func TestRawFragmentsPanicOnBindingMismatch(t *testing.T) {
	assertPanics(t, map[string]func(){
		"too few bindings":  func() { widgets().WhereRaw("a = ? AND b = ?", 1) },
		"too many bindings": func() { widgets().WhereRaw("a = ?", 1, 2) },
	})
}

// assertPanics runs each case as a subtest that must panic.
func assertPanics(t *testing.T, cases map[string]func()) {
	t.Helper()

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			build()
		})
	}
}

func TestCompilesTheConditionsAndOrders(t *testing.T) {
	ctx, _ := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 12, 0, 0, 600_000_000, time.UTC))
	second := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	q := func() *widgetQuery { return NewQuery[widget](ctx, &widgetQuery{}) }

	cases := []struct {
		name  string
		query *widgetQuery
		sql   string
		args  []any
	}{
		{"Latest", q().Latest(), `select * from "widgets" order by "widgets"."created_at" desc`, nil},
		{"Latest by a column", q().Latest("id"), `select * from "widgets" order by "id" desc`, nil},
		{"WhereBetween", q().Where("a", 1).WhereBetween("latitude", 1.5, 2.5), `select * from "widgets" where "a" = $1 and "latitude" between $2 and $3`, []any{1, 1.5, 2.5}},
		{"WherePast", q().WherePast("expires_at"), `select * from "widgets" where "expires_at" < $1`, []any{second}},
		{"WhereNowOrFuture", q().WhereNowOrFuture("searches.expires_at"), `select * from "widgets" where "searches"."expires_at" >= $1`, []any{second}},
		{"WhereTodayOrAfter", q().WhereTodayOrAfter("date"), `select * from "widgets" where "date"::date >= $1`, []any{"2026-10-20"}},
		{"LockForUpdate", q().Where("id", 1).LockForUpdate().Limit(1), `select * from "widgets" where "id" = $1 limit 1 for update`, []any{1}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sql, args := c.query.ToSQL()
			if sql != c.sql {
				t.Fatalf("sql =\n%s\nwant\n%s", sql, c.sql)
			}

			if !reflect.DeepEqual(args, c.args) {
				t.Fatalf("args = %v, want %v", args, c.args)
			}
		})
	}
}

func TestLatestTakesAtMostOneColumn(t *testing.T) {
	assertPanics(t, map[string]func(){
		"two columns": func() { widgets().Latest("created_at", "id") },
	})
}

// The time conditions compare against the clock the query's context
// carries, so moving a frozen test clock moves them.
func TestTheTimeConditionsFollowTheQueryClock(t *testing.T) {
	ctx, now := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC))

	now.Travel(time.Hour)

	_, past := NewQuery[widget](ctx, &widgetQuery{}).WherePast("expires_at").ToSQL()
	_, today := NewQuery[widget](ctx, &widgetQuery{}).WhereTodayOrAfter("date").ToSQL()

	if want := time.Date(2026, 10, 21, 0, 30, 0, 0, time.UTC); past[0] != want || today[0] != "2026-10-21" {
		t.Fatalf("past %v, today %v: want the travelled clock", past, today)
	}
}

// Each scope's conditions are ANDed in a group and the groups ORed, numbered
// in order with the conditions around them.
func TestWhereAnyORsWhatEachScopeAdds(t *testing.T) {
	sql, args := widgets().
		Where("a", 1).
		WhereAny(
			func(q *widgetQuery) { q.Visible() },
			func(q *widgetQuery) { q.Where("b", 2).WhereNull("c") },
		).
		Where("d", 3).
		ToSQL()

	if want := `select * from "widgets" where "a" = $1 and (("visible" = $2) or ("b" = $3 and "c" is null)) and "d" = $4`; sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", sql, want)
	}

	if want := []any{1, true, 2, 3}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestWhereAnyRefusesAGroupThatCouldWidenTheList(t *testing.T) {
	assertPanics(t, map[string]func(){
		"no scopes":              func() { widgets().WhereAny() },
		"a scope adding nothing": func() { widgets().WhereAny(func(*widgetQuery) {}) },
		"a scope that orders":    func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible().OrderBy("name") }) },
		"a scope that limits":    func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible().Limit(1) }) },
		"a scope that offsets":   func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible().Offset(1) }) },
		"a scope that selects":   func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible().Select("id") }) },
		"a scope that joins": func() {
			widgets().WhereAny(func(q *widgetQuery) { q.Visible().Join("a", "a.id", "=", "widgets.a_id") })
		},
		"a scope that locks":            func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible().LockForUpdate() }) },
		"a scope that skips timestamps": func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible().WithoutTimestamps() }) },
		"a scope that eager-loads": func() {
			widgets().WhereAny(func(q *widgetQuery) { q.Visible().Eager(func(context.Context, []widget) error { return nil }) })
		},
		"a later scope adding nothing": func() { widgets().WhereAny(func(q *widgetQuery) { q.Visible() }, func(*widgetQuery) {}) },
	})
}

func TestRenumberShiftsPlaceholdersForUpdates(t *testing.T) {
	got := renumber(`"id" = $1 and "cancelled_at" is null and "x" = $2 and price > $10`, 3)

	if want := `"id" = $4 and "cancelled_at" is null and "x" = $5 and price > $13`; got != want {
		t.Fatalf("renumber = %s", got)
	}
}

// A column argument is a name, quoted; a select entry may also be `*` or
// `table.*`, and a select entry or a joined table may carry an alias. Any
// other string panics where the query is built, so no SQL is ever sent.
func TestAColumnArgumentIsAlwaysAnIdentifier(t *testing.T) {
	sql, args := widgets().
		Join("pg_class AS src", "src.oid", "=", "widgets.class_id").
		Select("*", "widgets.*", "widgets.name", "countries.slug AS country_slug", "countries.code as country_code").
		Where("information_schema.columns", 1).
		Where("public.widgets.id", 2).
		ToSQL()

	want := `select *, "widgets".*, "widgets"."name", "countries"."slug" as "country_slug", "countries"."code" as "country_code" ` +
		`from "widgets" inner join "pg_class" as "src" on "src"."oid" = "widgets"."class_id" ` +
		`where "information_schema"."columns" = $1 and "public"."widgets"."id" = $2`
	if sql != want || !reflect.DeepEqual(args, []any{1, 2}) {
		t.Fatalf("sql =\n%s %v\nwant\n%s [1 2]", sql, args, want)
	}

	assertPanics(t, map[string]func(){
		"Select an expression":       func() { widgets().Select("count(*)") },
		"Select a star mid-name":     func() { widgets().Select("a.*.b") },
		"Select a qualified alias":   func() { widgets().Select("countries.slug AS a.b") },
		"Where an expression":        func() { widgets().Where("lower(name)", "a") },
		"Where a quote":              func() { widgets().Where(`odd"name`, "a") },
		"Where an alias":             func() { widgets().Where("name AS n", "a") },
		"Where a star":               func() { widgets().Where("*", "a") },
		"OrderBy a direction":        func() { widgets().OrderBy("name desc") },
		"OrderByDesc a statement":    func() { widgets().OrderByDesc("id; drop table users") },
		"Pluck an expression":        func() { _, _ = widgets().Pluck[string]("upper(trim(code))") },
		"PluckBy an expression":      func() { _, _ = widgets().PluckBy[string, int64]("upper(trim(code))", "id") },
		"Value an expression":        func() { _, _ = widgets().Value[int64]("count(*)") },
		"WhereNull nothing":          func() { widgets().WhereNull("") },
		"WhereIn a condition":        func() { widgets().WhereIn("id) or (1=1", []int{1}) },
		"WhereBetween two words":     func() { widgets().WhereBetween("a b", 1, 2) },
		"Join a bare alias":          func() { widgets().Join("countries c", "c.id", "=", "widgets.country_id") },
		"Join on a condition":        func() { widgets().Join("countries", "countries.id = 1 or true", "=", "widgets.country_id") },
		"an Insert key":              func() { _ = widgets().Insert(Values{"name) values (1); --": 1}) },
		"an Upsert column":           func() { _ = widgets().Upsert([]string{"a b"}, [][]any{{1}}, []string{"id"}, nil) },
		"a four-part name":           func() { widgets().Where("flights.public.widgets.id", 1) },
		"a name with an alias space": func() { widgets().Select("name AS  ") },
	})
}

// The builder qualifies its own columns with the table's name, so on a
// schema-qualified table WhereKey, Find, Latest() and a limited Delete name
// schema.table.column.
func TestASchemaQualifiedTableQualifiesTheBuildersOwnColumns(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	columns := func() *Rows[struct{}] { return Table[struct{}](ctx, "information_schema.columns") }
	table := `"information_schema"."columns"`

	if sql, _ := columns().WhereKey(7).Latest().ToSQL(); sql != `select * from `+table+` where `+table+`."id" = $1 order by `+table+`."created_at" desc` {
		t.Errorf("WhereKey and Latest: %s", sql)
	}

	_, _ = columns().Find(7)
	if want := `select * from ` + table + ` where ` + table + `."id" = $1 limit 1`; conn.sql != want {
		t.Errorf("Find sent %s, want %s", conn.sql, want)
	}

	_, _ = columns().Limit(1).Delete()
	if want := `delete from ` + table + ` where ` + table + `."ctid" in (select ` + table + `."ctid" from ` + table + ` limit 1)`; conn.sql != want {
		t.Errorf("Delete sent %s, want %s", conn.sql, want)
	}
}

// WhereOp and Join take a comparison or [not] [i]like, written lower-case;
// any other operator panics.
func TestWhereOpTakesAnOperatorFromTheList(t *testing.T) {
	for op, want := range map[string]string{
		"=": "=", "<>": "<>", "!=": "!=", "<": "<", "<=": "<=", ">": ">", ">=": ">=",
		"like": "like", "not like": "not like", "ilike": "ilike", "not ilike": "not ilike", "ILIKE": "ilike", "Not Like": "not like",
	} {
		if sql, _ := widgets().WhereOp("name", op, "a").ToSQL(); sql != `select * from "widgets" where "name" `+want+` $1` {
			t.Errorf("WhereOp(%q) = %s", op, sql)
		}
	}

	assertPanics(t, map[string]func(){
		"a regex match":     func() { widgets().WhereOp("name", "~", "a") },
		"is":                func() { widgets().WhereOp("name", "is", nil) },
		"nothing":           func() { widgets().WhereOp("name", "", "a") },
		"a condition":       func() { widgets().WhereOp("name", "= 'x' or true --", "a") },
		"a Join regex":      func() { widgets().Join("countries", "countries.id", "~", "widgets.country_id") },
		"a LeftJoin clause": func() { widgets().LeftJoin("countries", "countries.id", "= 1 or", "widgets.country_id") },
	})
}

func TestQueriesWithoutABoundConnectionFailLoudly(t *testing.T) {
	if _, err := widgets().Get(); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection", err)
	}
	if err := widgets().Insert(Values{"name": "a"}); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection", err)
	}
}

func TestModelNotFoundNamesTheModelInWords(t *testing.T) {
	for model, want := range map[string]string{"Country": "Country not found.", "SavedSearch": "Saved search not found."} {
		err := &ModelNotFoundError{Model: model}

		if err.Error() != want || err.HTTPStatus() != 404 {
			t.Errorf("%s: got %q / %d, want %q / 404", model, err.Error(), err.HTTPStatus(), want)
		}
	}
}

// A scope compares against the clock its query's context carries, so a
// frozen test clock moves it.
func TestAQueryReadsTheClockItsContextCarries(t *testing.T) {
	ctx, _ := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))

	q := NewQuery[widget](ctx, &widgetQuery{})
	if got := q.Now(); !got.Equal(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("Now() = %v, want the frozen clock", got)
	}
}
