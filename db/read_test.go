package db

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func TestPluckSelectsOneColumnOfEveryRow(t *testing.T) {
	ctx, conn, encrypter := schemaCase(t)

	sealed, err := encrypter.EncryptString("X1234567")
	if err != nil {
		t.Fatal(err)
	}

	conn.rows = rowsOf([]string{"secret"}, []any{sealed}, []any{sealed})

	secrets, err := accounts(ctx).Select("id", "notes").Where("price", 10).Pluck[string]("secret")
	if err != nil || !reflect.DeepEqual(secrets, []string{"X1234567", "X1234567"}) {
		t.Fatalf("Pluck = %q (%v), want the opened secrets", secrets, err)
	}

	if want := `select "secret" from "accounts" where "price" = $1`; conn.sql != want {
		t.Fatalf("sql = %s, want %s", conn.sql, want)
	}
}

func TestPluckAndValueReadAStructTypedColumnAsTheValue(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	conn.rows = rowsOf([]string{"nickname"}, []any{"Ana"}, []any{nil})

	nicknames, err := accounts(ctx).Pluck[sql.NullString]("nickname")
	if want := []sql.NullString{{String: "Ana", Valid: true}, {}}; err != nil || !reflect.DeepEqual(nicknames, want) {
		t.Fatalf("Pluck = %+v (%v), want %+v", nicknames, err, want)
	}

	conn.rows = rowsOf([]string{"nickname"}, []any{"Ana"})

	nickname, err := accounts(ctx).Value[sql.NullString]("nickname")
	if want := (sql.NullString{String: "Ana", Valid: true}); err != nil || nickname == nil || *nickname != want {
		t.Fatalf("Value = %+v (%v), want %+v", nickname, err, want)
	}
}

func TestPluckByMapsTheKeyColumnToTheValueColumn(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	conn.rows = rowsOf([]string{"code", "id"}, []any{"ES", int64(7)}, []any{"GB", int64(9)}, []any{"ES", int64(11)})

	ids, err := accounts(ctx).PluckBy[string, int64]("code", "id")
	if want := map[string]int64{"ES": 11, "GB": 9}; err != nil || !reflect.DeepEqual(ids, want) {
		t.Fatalf("PluckBy = %v (%v), want %v: a later row wins its key", ids, err, want)
	}

	if want := `select "code", "id" from "accounts"`; conn.sql != want {
		t.Fatalf("sql = %s, want %s", conn.sql, want)
	}
}

func TestValueIsTheFirstRowsColumnOrNil(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	conn.rows = rowsOf([]string{"id"}, []any{int64(7)})

	id, err := accounts(ctx).Where("price", 10).Value[int64]("id")
	if err != nil || id == nil || *id != 7 {
		t.Fatalf("Value = %v (%v), want 7", id, err)
	}

	if want := `select "id" from "accounts" where "price" = $1 limit 1`; conn.sql != want {
		t.Fatalf("sql = %s, want %s", conn.sql, want)
	}

	conn.rows = rowsOf([]string{"id"})

	if id, err := accounts(ctx).Where("price", 10).Value[int64]("id"); err != nil || id != nil {
		t.Fatalf("Value with no row = %v (%v), want nil", id, err)
	}
}

// accountListing is a row shape the model doesn't describe: the model's
// columns, embedded, plus a joined one.
type accountListing struct {
	account
	CountryName string `db:"country_name"`
}

func TestGetAsScansIntoAnotherRowShape(t *testing.T) {
	ctx, conn, encrypter := schemaCase(t)

	sealed, err := encrypter.EncryptString("X1234567")
	if err != nil {
		t.Fatal(err)
	}

	conn.rows = rowsOf([]string{"id", "secret", "country_name"}, []any{int64(7), sealed, "Spain"})

	rows, err := accounts(ctx).
		Join("countries", "countries.id", "=", "accounts.country_id").
		Select("accounts.id", "accounts.secret", "countries.name AS country_name").
		GetAs[accountListing]()
	if err != nil {
		t.Fatal(err)
	}

	want := accountListing{account: account{ID: 7, Secret: "X1234567"}, CountryName: "Spain"}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0], want) {
		t.Fatalf("GetAs = %+v, want %+v", rows, want)
	}
}

// GetAs skips a column its row type has no field for, as Get does, so a
// migration adding one never breaks a running read.
func TestGetAsSkipsExtraColumns(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	conn.rows = rowsOf([]string{"id", "country_name", "added_later"}, []any{int64(7), "Spain", "x"})

	rows, err := accounts(ctx).Select("accounts.*").GetAs[accountListing]()
	if want := []accountListing{{account: account{ID: 7}, CountryName: "Spain"}}; err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("GetAs = %+v (%v), want %+v", rows, err, want)
	}
}

// accountTotal declares its total through an embedded struct that isn't a
// model, so the field is its own and must be selected.
type accountTotal struct {
	totals
	ID int64 `db:"id"`
}

type totals struct {
	Total int64 `db:"total"`
}

// GetAs fails, before reading a row, on a field its row type declares that
// the result has no column for: a misspelled alias never reads as null. A
// field an embedded model brings reads as Get reads the model.
func TestGetAsNeedsAColumnForEveryFieldItDeclares(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	for name, rows := range map[string]*fakeRows{
		"no rows": rowsOf([]string{"id", "secret"}),
		"a row":   rowsOf([]string{"id", "secret"}, []any{int64(7), "sealed"}),
	} {
		conn.rows = rows

		listings, err := accounts(ctx).GetAs[accountListing]()
		if want := `db: scanning db.accountListing: the result has no column country_name; select it, or tag the field db:"-"`; err == nil || err.Error() != want || listings != nil {
			t.Errorf("%s: GetAs = %+v (%v), want %q", name, listings, err, want)
		}
	}

	conn.rows = rowsOf([]string{"id"}, []any{int64(7)})
	if _, err := accounts(ctx).GetAs[accountTotal](); err == nil || !strings.Contains(err.Error(), "no column total;") {
		t.Errorf("GetAs = %v, want the embedded plain struct's total missing", err)
	}

	conn.rows = rowsOf([]string{"secret"}, []any{"sealed"})
	if _, err := accounts(ctx).GetAs[accountTotal](); err == nil || !strings.Contains(err.Error(), "no columns id, total;") {
		t.Errorf("GetAs = %v, want every missing column, sorted", err)
	}
}

// A terminal read narrows a copy of its query (a select, a limit, a key, a
// changed-values condition), so the query is as it was afterwards.
func TestATerminalReadLeavesItsQueryAsItWas(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	for name, read := range map[string]func(q *accountQuery){
		"Get":                 func(q *accountQuery) { _, _ = q.Get() },
		"GetAs":               func(q *accountQuery) { _, _ = q.GetAs[map[string]any]() },
		"Pluck":               func(q *accountQuery) { _, _ = q.Pluck[int64]("id") },
		"PluckBy":             func(q *accountQuery) { _, _ = q.PluckBy[int64, string]("id", "secret") },
		"Value":               func(q *accountQuery) { _, _ = q.Value[int64]("id") },
		"First":               func(q *accountQuery) { _, _ = q.First() },
		"FirstOrFail":         func(q *accountQuery) { _, _ = q.FirstOrFail() },
		"Find":                func(q *accountQuery) { _, _ = q.Find(7) },
		"FindOrFail":          func(q *accountQuery) { _, _ = q.FindOrFail(7) },
		"ResolveRouteBinding": func(q *accountQuery) { _, _ = q.ResolveRouteBinding("7") },
		"Count":               func(q *accountQuery) { _, _ = q.Count() },
		"Exists":              func(q *accountQuery) { _, _ = q.Exists() },
		"Update":              func(q *accountQuery) { _, _ = q.Update(Values{"price": 5}) },
		"UpdateChanged":       func(q *accountQuery) { _, _ = q.UpdateChanged(Values{"price": 5}) },
		"Delete":              func(q *accountQuery) { _, _ = q.Delete() },
	} {
		t.Run(name, func(t *testing.T) {
			conn.rows = rowsOf([]string{"id"})
			q := accounts(ctx).Where("price", 10).OrderBy("id")
			before, beforeArgs := q.ToSQL()

			read(q)

			if after, afterArgs := q.ToSQL(); after != before || !reflect.DeepEqual(afterArgs, beforeArgs) {
				t.Fatalf("the query became\n%s %v\nwas\n%s %v", after, afterArgs, before, beforeArgs)
			}
		})
	}
}

func TestTwoFindsOnOneQueryEachFindTheirOwnRow(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	q := accounts(ctx).Where("price", 10)

	conn.rows = rowsOf([]string{"id"})
	_, _ = q.Find(1)
	conn.rows = rowsOf([]string{"id"})
	_, _ = q.Find(2)

	if want := `select * from "accounts" where "price" = $1 and "accounts"."id" = $2 limit 1`; conn.sql != want || !reflect.DeepEqual(conn.args, []any{10, 2}) {
		t.Fatalf("sql = %s %v, want %s [10 2]", conn.sql, conn.args, want)
	}
}

func TestWhereInBindsTheListAsOneArray(t *testing.T) {
	cases := []struct {
		name  string
		query *widgetQuery
		sql   string
		args  []any
	}{
		{"strings", widgets().WhereIn("code", []string{"ES", "GB"}), `select * from "widgets" where "code" = any($1)`, []any{[]string{"ES", "GB"}}},
		{"ids", widgets().WhereIn("id", []int64{7, 9}), `select * from "widgets" where "id" = any($1)`, []any{[]int64{7, 9}}},
		{"no ids", widgets().WhereIn("id", []int64{}), `select * from "widgets" where 0 = 1`, nil},
		{"no strings", widgets().WhereIn("code", []string(nil)), `select * from "widgets" where 0 = 1`, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			statement, args := c.query.ToSQL()
			if statement != c.sql || !reflect.DeepEqual(args, c.args) {
				t.Fatalf("sql = %s %v, want %s %v", statement, args, c.sql, c.args)
			}
		})
	}
}
