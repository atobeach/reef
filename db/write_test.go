package db

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// An insert names its columns in sorted order, so the statement is stable
// whatever order the values map iterates in, and binds each row's values in
// that order; a table's insert stamps nothing.
func TestInsertWritesEveryRowInOneStatementWithSortedColumns(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	if err := Table[widget](ctx, "widgets").Insert(Values{"name": "a", "country_id": 7}, Values{"country_id": 8, "name": "b"}); err != nil {
		t.Fatal(err)
	}

	if want := `insert into "widgets" ("country_id", "name") values ($1, $2), ($3, $4)`; conn.sql != want {
		t.Fatalf("sql = %s, want %s", conn.sql, want)
	}

	if want := []any{7, "a", 8, "b"}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

func TestInsertStampsEveryRowOfAModel(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	if err := accounts(ctx).Insert(Values{"price": 1}, Values{"price": 2}); err != nil {
		t.Fatal(err)
	}

	if want := `insert into "accounts" ("created_at", "price", "updated_at") values ($1, $2, $3), ($4, $5, $6)`; conn.sql != want {
		t.Fatalf("sql = %s, want %s", conn.sql, want)
	}

	second := frozen.Truncate(time.Second)
	if want := []any{second, 1, second, second, 2, second}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

func TestInsertRefusesRowsWhoseColumnsDiffer(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	err := accounts(ctx).Insert(Values{"price": 1}, Values{"notes": nil})
	if err == nil || !strings.Contains(err.Error(), "row 2 has the columns [created_at notes updated_at], the first row [created_at price updated_at]") {
		t.Fatalf("err = %v, want the differing columns named", err)
	}

	if conn.sql != "" {
		t.Fatalf("ran %s", conn.sql)
	}
}

func TestWritingNoRowsRunsNothing(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	for name, write := range map[string]func() error{
		"Insert": func() error { return accounts(ctx).Insert() },
		"InsertOrIgnore": func() error {
			inserted, err := accounts(ctx).InsertOrIgnore()
			if inserted != 0 {
				t.Errorf("InsertOrIgnore() inserted %d", inserted)
			}

			return err
		},
		"Upsert": func() error { return accounts(ctx).Upsert([]string{"id"}, nil, []string{"id"}, nil) },
	} {
		if err := write(); err != nil || conn.sql != "" {
			t.Errorf("%s with no rows ran %q (%v)", name, conn.sql, err)
		}
	}
}

func TestInsertOrIgnoreSkipsARowAUniqueIndexHolds(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	inserted, err := accounts(ctx).InsertOrIgnore(Values{"id": 1})
	if err != nil || inserted != 1 {
		t.Fatalf("InsertOrIgnore = %d (%v), want the affected count", inserted, err)
	}

	if want := `insert into "accounts" ("created_at", "id", "updated_at") values ($1, $2, $3) on conflict do nothing`; conn.sql != want {
		t.Fatalf("sql = %s, want %s", conn.sql, want)
	}
}

// UpdateOrCreate is one statement: the insert carries the attributes, the
// values and both timestamps, and a conflict over the attributes updates the
// values and updated_at, never created_at.
func TestUpdateOrCreateUpsertsOverTheAttributesKeepingCreatedAt(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	_, _ = accounts(ctx).UpdateOrCreate(Values{"price": 10, "id": 1}, Values{"notes": "hi", "settings": map[string]int{"a": 1}})

	want := `insert into "accounts" ("created_at", "id", "notes", "price", "settings", "updated_at") values ($1, $2, $3, $4, $5, $6) ` +
		`on conflict ("id", "price") do update set "notes" = excluded."notes", "settings" = excluded."settings", "updated_at" = excluded."updated_at" returning *`
	if conn.sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", conn.sql, want)
	}

	if conn.args[4] != `{"a":1}` {
		t.Fatalf("settings bound as %v, want the cast's JSON", conn.args[4])
	}
}

func TestUpdateOrCreateWithAnUpdatedAtOfItsOwnWritesThatOne(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	imported := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	_, _ = accounts(ctx).UpdateOrCreate(Values{"id": 1}, Values{"updated_at": imported})

	if want := ` on conflict ("id") do update set "updated_at" = excluded."updated_at" returning *`; !strings.HasSuffix(conn.sql, want) {
		t.Fatalf("sql = %s, want it to end %s", conn.sql, want)
	}

	if want := []any{frozen.Truncate(time.Second), 1, imported}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

// With nothing to update, a conflict leaves the stored row alone: Upsert does
// nothing, unless a model's updated_at is still to touch, and UpdateOrCreate
// rewrites its attributes with themselves, since DO NOTHING returns no row.
func TestAConflictWithNothingToUpdateKeepsTheStoredRow(t *testing.T) {
	for name, tc := range map[string]struct {
		write func(ctx context.Context)
		want  string
	}{
		"Upsert into a table": {
			write: func(ctx context.Context) {
				_ = Table[widget](ctx, "airports").Upsert([]string{"source", "source_id"}, [][]any{{"N", 1}}, []string{"source", "source_id"}, nil)
			},
			want: `insert into "airports" ("source", "source_id") values ($1, $2) on conflict ("source", "source_id") do nothing`,
		},
		"Upsert into a model": {
			write: func(ctx context.Context) {
				_ = accounts(ctx).Upsert([]string{"id"}, [][]any{{1}}, []string{"id"}, nil)
			},
			want: `insert into "accounts" ("id", "created_at", "updated_at") values ($1, $2, $3) on conflict ("id") do update set "updated_at" = excluded."updated_at"`,
		},
		"UpdateOrCreate on a table": {
			write: func(ctx context.Context) {
				_, _ = Table[widget](ctx, "widgets").UpdateOrCreate(Values{"name": "a", "country_id": 7}, Values{})
			},
			want: `insert into "widgets" ("country_id", "name") values ($1, $2) on conflict ("country_id", "name") ` +
				`do update set "country_id" = excluded."country_id", "name" = excluded."name" returning *`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, conn, _ := schemaCase(t)

			tc.write(ctx)

			if conn.sql != tc.want {
				t.Fatalf("sql =\n%s\nwant\n%s", conn.sql, tc.want)
			}
		})
	}
}

// Upsert binds positional rows through the model's casts, stamps the
// timestamps the columns leave out with one now, and updates only the named
// columns and updated_at on a conflict.
func TestUpsertWritesPositionalRowsThroughTheModel(t *testing.T) {
	ctx, conn, encrypter := schemaCase(t)

	if err := accounts(ctx).Upsert([]string{"id", "secret"}, [][]any{{1, "X1"}, {2, "X2"}}, []string{"id"}, []string{"secret"}); err != nil {
		t.Fatal(err)
	}

	want := `insert into "accounts" ("id", "secret", "created_at", "updated_at") values ($1, $2, $3, $4), ($5, $6, $7, $8) ` +
		`on conflict ("id") do update set "secret" = excluded."secret", "updated_at" = excluded."updated_at"`
	if conn.sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", conn.sql, want)
	}

	second := frozen.Truncate(time.Second)
	for i, plain := range []string{"X1", "X2"} {
		row := conn.args[i*4 : i*4+4]
		sealed, _ := row[1].(string)
		if opened, err := encrypter.DecryptString(sealed); err != nil || opened != plain || row[2] != second || row[3] != second {
			t.Errorf("row %d bound %v, want %q sealed and stamped %v", i+1, row, plain, second)
		}
	}
}

func TestUpsertIntoATableStampsNothing(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	if err := Table[widget](ctx, "airports").Upsert([]string{"source", "source_id", "name"}, [][]any{{"N", 1, "Heathrow"}}, []string{"source", "source_id"}, []string{"name"}); err != nil {
		t.Fatal(err)
	}

	if want := `insert into "airports" ("source", "source_id", "name") values ($1, $2, $3) on conflict ("source", "source_id") do update set "name" = excluded."name"`; conn.sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", conn.sql, want)
	}
}

func TestUpsertRefusesARowThatDoesNotMatchTheColumns(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	err := accounts(ctx).Upsert([]string{"id", "price"}, [][]any{{1, 10}, {2}}, []string{"id"}, []string{"price"})
	if err == nil || !strings.Contains(err.Error(), "row 2 has 1 values for 2 columns") {
		t.Fatalf("err = %v, want the short row named", err)
	}

	if conn.sql != "" {
		t.Fatalf("ran %s", conn.sql)
	}
}
