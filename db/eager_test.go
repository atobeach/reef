package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// town, region and nation are models as codegen writes them: a town may
// belong to a region, a region always belongs to a nation.
type town struct {
	ID       int64         `db:"id"`
	Name     string        `db:"name"`
	RegionID *int64        `db:"region_id"`
	Loaded   townRelations `db:"-" json:"-"`
}

type townRelations struct {
	Region *region
}

func (town) TableName() string { return "towns" }

func (town) Schema() *Schema { return nil }

type townQuery struct {
	Builder[town, *townQuery]
}

func towns(ctx context.Context) *townQuery {
	return NewQuery[town](ctx, &townQuery{})
}

func (q *townQuery) WithRegion(scopes ...func(q *regionQuery)) *townQuery {
	return q.Eager(BelongsTo(regions, scopes, func(t *town) (*int64, **region) { return t.RegionID, &t.Loaded.Region }))
}

type region struct {
	ID       int64           `db:"id"`
	Name     string          `db:"name"`
	NationID int64           `db:"nation_id"`
	Loaded   regionRelations `db:"-" json:"-"`
}

type regionRelations struct {
	Nation *nation
}

func (region) TableName() string { return "regions" }

func (region) Schema() *Schema { return nil }

type regionQuery struct {
	Builder[region, *regionQuery]
}

func regions(ctx context.Context) *regionQuery {
	return NewQuery[region](ctx, &regionQuery{})
}

func (q *regionQuery) WithNation(scopes ...func(q *nationQuery)) *regionQuery {
	return q.Eager(BelongsTo(nations, scopes, func(r *region) (*int64, **nation) { return &r.NationID, &r.Loaded.Nation }))
}

type nation struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

func (nation) TableName() string { return "nations" }

func (nation) Schema() *Schema { return nil }

type nationQuery struct {
	Builder[nation, *nationQuery]
}

func nations(ctx context.Context) *nationQuery {
	return NewQuery[nation](ctx, &nationQuery{})
}

// script is a connection that answers each query with the next of its
// result sets, recording every statement it is sent.
type script struct {
	results    []*fakeRows
	statements []string
	args       [][]any
}

func (s *script) Query(_ context.Context, statement string, args ...any) (pgx.Rows, error) {
	s.statements = append(s.statements, statement)
	s.args = append(s.args, args)

	if len(s.results) == 0 {
		return nil, errRecorded
	}

	rows := s.results[0]
	s.results = s.results[1:]

	return rows, nil
}

func (s *script) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("not used")
}

func (s *script) QueryRow(context.Context, string, ...any) pgx.Row { panic("not used") }

func (s *script) Begin(context.Context) (pgx.Tx, error) { panic("not used") }

func scripted(t *testing.T, results ...*fakeRows) (context.Context, *script) {
	t.Helper()

	conn := &script{results: results}

	return WithConn(t.Context(), conn), conn
}

func (s *script) assertStatements(t *testing.T, want ...string) {
	t.Helper()

	if !reflect.DeepEqual(s.statements, want) {
		t.Fatalf("statements =\n%s\nwant\n%s", strings.Join(s.statements, "\n"), strings.Join(want, "\n"))
	}
}

func townRows(rows ...[]any) *fakeRows {
	return rowsOf([]string{"id", "name", "region_id"}, rows...)
}

func regionRows(rows ...[]any) *fakeRows {
	return rowsOf([]string{"id", "name", "nation_id"}, rows...)
}

func TestGetLoadsEveryRowsParentInOneQuery(t *testing.T) {
	ctx, conn := scripted(t,
		townRows(
			[]any{int64(1), "Positano", int64(7)},
			[]any{int64(2), "Amalfi", int64(7)},
			[]any{int64(3), "Nowhere", nil},
			[]any{int64(4), "Atlantis", int64(9)},
		),
		regionRows([]any{int64(7), "Campania", int64(1)}),
	)

	rows, err := towns(ctx).WithRegion().Get()
	if err != nil {
		t.Fatal(err)
	}

	conn.assertStatements(t, `select * from "towns"`, `select * from "regions" where "regions"."id" = any($1)`)
	if want := []any{[]int64{7, 9}}; !reflect.DeepEqual(conn.args[1], want) {
		t.Fatalf("region keys = %v, want each key once: %v", conn.args[1], want)
	}

	if rows[0].Loaded.Region == nil || rows[0].Loaded.Region.Name != "Campania" || rows[1].Loaded.Region != rows[0].Loaded.Region {
		t.Fatalf("Positano and Amalfi load = %+v, %+v, want one Campania", rows[0].Loaded.Region, rows[1].Loaded.Region)
	}

	if rows[2].Loaded.Region != nil || rows[3].Loaded.Region != nil {
		t.Fatalf("a nil key and a key naming no row load %+v and %+v, want nil", rows[2].Loaded.Region, rows[3].Loaded.Region)
	}
}

func TestEveryReadOfTheModelRunsItsEagerLoads(t *testing.T) {
	for name, read := range map[string]func(q *townQuery) (town, error){
		"First": func(q *townQuery) (town, error) {
			row, err := q.First()
			if row == nil {
				return town{}, err
			}

			return *row, err
		},
		"FirstOrFail": func(q *townQuery) (town, error) { return q.FirstOrFail() },
		"Find": func(q *townQuery) (town, error) {
			row, err := q.Find(1)
			if row == nil {
				return town{}, err
			}

			return *row, err
		},
		"FindOrFail": func(q *townQuery) (town, error) { return q.FindOrFail(1) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, conn := scripted(t, townRows([]any{int64(1), "Positano", int64(7)}), regionRows([]any{int64(7), "Campania", int64(1)}))

			row, err := read(towns(ctx).WithRegion())
			if err != nil {
				t.Fatal(err)
			}

			if row.Loaded.Region == nil || row.Loaded.Region.Name != "Campania" {
				t.Fatalf("Loaded.Region = %+v, want Campania", row.Loaded.Region)
			}

			if len(conn.statements) != 2 {
				t.Fatalf("statements = %q, want the town's and the region's", conn.statements)
			}
		})
	}
}

func TestNoKeysQueriesNoParents(t *testing.T) {
	for name, rows := range map[string]*fakeRows{
		"no rows":       townRows(),
		"only nil keys": townRows([]any{int64(3), "Nowhere", nil}),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, conn := scripted(t, rows)

			if _, err := towns(ctx).WithRegion().Get(); err != nil {
				t.Fatal(err)
			}

			conn.assertStatements(t, `select * from "towns"`)
		})
	}
}

func TestAScopedSelectStillReadsTheKey(t *testing.T) {
	for name, tc := range map[string]struct {
		scope func(q *regionQuery)
		sql   string
	}{
		"the key added":        {func(q *regionQuery) { q.Select("name") }, `select "name", "regions"."id" from "regions" where "regions"."id" = any($1)`},
		"the key kept":         {func(q *regionQuery) { q.Select("id", "name") }, `select "id", "name" from "regions" where "regions"."id" = any($1)`},
		"every column kept":    {func(q *regionQuery) { q.Where("name", "Campania") }, `select * from "regions" where "name" = $1 and "regions"."id" = any($2)`},
		"a qualified key kept": {func(q *regionQuery) { q.Select("regions.id", "name") }, `select "regions"."id", "name" from "regions" where "regions"."id" = any($1)`},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, conn := scripted(t,
				townRows([]any{int64(1), "Positano", int64(7)}),
				rowsOf([]string{"name", "id"}, []any{"Campania", int64(7)}),
			)

			rows, err := towns(ctx).WithRegion(tc.scope).Get()
			if err != nil {
				t.Fatal(err)
			}

			conn.assertStatements(t, `select * from "towns"`, tc.sql)
			if region := rows[0].Loaded.Region; region == nil || region.Name != "Campania" {
				t.Fatalf("Loaded.Region = %+v, want Campania keyed by its id", region)
			}
		})
	}
}

func TestEagerLoadsNest(t *testing.T) {
	ctx, conn := scripted(t,
		townRows([]any{int64(1), "Positano", int64(7)}),
		regionRows([]any{int64(7), "Campania", int64(1)}),
		rowsOf([]string{"id", "name"}, []any{int64(1), "Italy"}),
	)

	rows, err := towns(ctx).WithRegion(func(q *regionQuery) { q.WithNation() }).Get()
	if err != nil {
		t.Fatal(err)
	}

	conn.assertStatements(t,
		`select * from "towns"`,
		`select * from "regions" where "regions"."id" = any($1)`,
		`select * from "nations" where "nations"."id" = any($1)`,
	)

	if nation := rows[0].Loaded.Region.Loaded.Nation; nation == nil || nation.Name != "Italy" {
		t.Fatalf("Loaded.Region.Loaded.Nation = %+v, want Italy", nation)
	}
}

func TestLoadFillsRowsReadElsewhere(t *testing.T) {
	ctx, conn := scripted(t, regionRows([]any{int64(7), "Campania", int64(1)}))
	rows := []town{{ID: 1, RegionID: new(int64(7))}, {ID: 2}}

	if err := towns(ctx).WithRegion().Load(rows); err != nil {
		t.Fatal(err)
	}

	conn.assertStatements(t, `select * from "regions" where "regions"."id" = any($1)`)
	if rows[0].Loaded.Region == nil || rows[0].Loaded.Region.Name != "Campania" || rows[1].Loaded.Region != nil {
		t.Fatalf("Loaded = %+v, %+v, want Campania and nil", rows[0].Loaded, rows[1].Loaded)
	}
}

func TestLoadRefusesAQueryShapingRowsItNeverReads(t *testing.T) {
	assertPanics(t, map[string]func(){
		"a where":  func() { _ = towns(t.Context()).Where("name", "Positano").WithRegion().Load(nil) },
		"a select": func() { _ = towns(t.Context()).Select("id").WithRegion().Load(nil) },
		"an order": func() { _ = towns(t.Context()).OrderBy("name").WithRegion().Load(nil) },
		"a limit":  func() { _ = towns(t.Context()).Limit(1).WithRegion().Load(nil) },
		"a join": func() {
			_ = towns(t.Context()).Join("regions", "regions.id", "=", "towns.region_id").WithRegion().Load(nil)
		},
	})
}

func TestReadsThatDontReturnTheModelRefuseEagerLoads(t *testing.T) {
	assertPanics(t, map[string]func(){
		"GetAs":   func() { _, _ = towns(t.Context()).WithRegion().GetAs[struct{ ID int64 }]() },
		"Pluck":   func() { _, _ = towns(t.Context()).WithRegion().Pluck[int64]("id") },
		"PluckBy": func() { _, _ = towns(t.Context()).WithRegion().PluckBy[int64, string]("id", "name") },
		"Value":   func() { _, _ = towns(t.Context()).WithRegion().Value[string]("name") },
	})
}

func TestAFailedEagerLoadFailsTheRead(t *testing.T) {
	ctx, _ := scripted(t, townRows([]any{int64(1), "Positano", int64(7)}))

	rows, err := towns(ctx).WithRegion().Get()
	if !errors.Is(err, errRecorded) || !strings.Contains(err.Error(), "eager-loading region") || rows != nil {
		t.Fatalf("Get = %v, %v; want no rows and the region load's error", rows, err)
	}
}
