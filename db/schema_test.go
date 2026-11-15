package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/crypt"
)

type account struct {
	ID       int64           `db:"id"`
	Secret   string          `db:"secret"`
	Notes    *string         `db:"notes"`
	Settings json.RawMessage `db:"settings"`
	Vault    json.RawMessage `db:"vault"`
}

func (account) TableName() string { return "accounts" }

func (account) Schema() *Schema { return &accountSchema }

var accountSchema = Schema{
	CreatedAt: "created_at",
	UpdatedAt: "updated_at",
	Columns: map[string]Column{
		"id":         {Type: "int8"},
		"secret":     {Type: "text", Cast: Encrypted},
		"notes":      {Type: "text", Cast: Encrypted},
		"settings":   {Type: "json", Cast: JSON(json.Marshal)},
		"vault":      {Type: "text", Cast: EncryptedJSON(json.Marshal)},
		"price":      {Type: "numeric"},
		"created_at": {Type: "timestamp"},
		"updated_at": {Type: "timestamp"},
	},
}

type accountQuery struct {
	Builder[account, *accountQuery]
}

// recorder is a connection that records the statement it is sent; a
// one-value query answers with answer, and a query with rows.
type recorder struct {
	sql    string
	args   []any
	answer any
	rows   *fakeRows
}

var errRecorded = errors.New("recorded, not run")

func (r *recorder) Exec(_ context.Context, statement string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = statement, args

	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (r *recorder) Query(_ context.Context, statement string, args ...any) (pgx.Rows, error) {
	r.sql, r.args = statement, args
	if r.rows == nil {
		return nil, errRecorded
	}

	return r.rows, nil
}

func (r *recorder) QueryRow(_ context.Context, statement string, args ...any) pgx.Row {
	r.sql, r.args = statement, args

	return answerRow{answer: r.answer}
}

func (r *recorder) Begin(context.Context) (pgx.Tx, error) { panic("not used") }

// answerRow scans its answer into the one destination, or fails as errRecorded
// with none set.
type answerRow struct {
	answer any
}

func (row answerRow) Scan(dest ...any) error {
	if row.answer == nil {
		return errRecorded
	}

	reflect.ValueOf(dest[0]).Elem().Set(reflect.ValueOf(row.answer))

	return nil
}

// frozen is the test clock, a fraction past the second the row must store.
var frozen = time.Date(2026, 10, 22, 12, 0, 0, 700_000_000, time.UTC)

func schemaCase(t *testing.T) (context.Context, *recorder, *crypt.Encrypter) {
	t.Helper()

	encrypter, err := crypt.New("base64:a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s=")
	if err != nil {
		t.Fatal(err)
	}

	conn := &recorder{}
	ctx, _ := clocktest.FrozenAt(t.Context(), frozen)

	return crypt.With(WithConn(ctx, conn), encrypter), conn, encrypter
}

func accounts(ctx context.Context) *accountQuery {
	return NewQuery[account](ctx, &accountQuery{})
}

func TestCreateStampsTheTimestampsFromTheClockAtTheSecond(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	_, _ = accounts(ctx).Create(Values{"id": 1})

	if want := `insert into "accounts" ("created_at", "id", "updated_at") values ($1, $2, $3) returning *`; conn.sql != want {
		t.Fatalf("sql = %s", conn.sql)
	}

	second := frozen.Truncate(time.Second)
	if want := []any{second, 1, second}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

// Create reads back the one row it inserted; a statement that answers with
// none (a trigger that skipped it, say) is an error naming it, not a zero row.
func TestCreateRefusesAnAnswerOtherThanOneRow(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	conn.rows = rowsOf([]string{"id"})

	_, err := accounts(ctx).Create(Values{"id": 1})
	if want := `db: insert into "accounts" ("created_at", "id", "updated_at") values ($1, $2, $3): wrote 0 rows, not 1`; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestATimestampTheWriteSetsWins(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	imported := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	_, _ = accounts(ctx).Create(Values{"created_at": imported})

	if want := []any{imported, frozen.Truncate(time.Second)}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

func TestUpdateTouchesOnlyUpdatedAt(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	if _, err := accounts(ctx).WhereKey(1).Update(Values{"price": 10}); err != nil {
		t.Fatal(err)
	}

	if want := `update "accounts" set "price" = $1, "updated_at" = $2 where "accounts"."id" = $3`; conn.sql != want {
		t.Fatalf("sql = %s", conn.sql)
	}
}

func TestWritesWithoutTimestampsStampNothing(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	for name, write := range map[string]func(){
		"WithoutTimestamps": func() { _, _ = accounts(ctx).WhereKey(1).WithoutTimestamps().Update(Values{"price": 10}) },
		"a table, not a model": func() {
			_, _ = Table[account](ctx, "accounts").Where("id", 1).Update(Values{"price": 10})
		},
	} {
		write()

		if want := `update "accounts" set "price" = $1 where `; len(conn.sql) < len(want) || conn.sql[:len(want)] != want {
			t.Errorf("%s: sql = %s", name, conn.sql)
		}
	}
}

func TestTimesBindInUTCAtTheSecond(t *testing.T) {
	local := time.FixedZone("CEST", 2*60*60)
	at := time.Date(2026, 10, 22, 14, 0, 0, 999_000_000, local)
	var unset *time.Time

	_, args := widgets().Where("a", at).Where("b", &at).Where("c", unset).ToSQL()

	want := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if args[0] != want || args[1] != want || args[2] != nil {
		t.Fatalf("args = %v, want %v twice and nil", args, want)
	}
}

// Postgres rounds a fraction to the nearest second on a timestamp(0)
// column; a write stores the second the time falls in.
func TestAFractionOfASecondIsWrittenAsTheSecondItFallsIn(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	at := time.Date(2026, 10, 22, 12, 0, 0, 600_000_000, time.UTC)

	if _, err := accounts(ctx).WhereKey(1).WithoutTimestamps().Update(Values{"created_at": at}); err != nil {
		t.Fatal(err)
	}

	if want := []any{time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), 1}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

// An UPDATE numbers its SET values first, so a WhereAny group's
// placeholders move after them.
func TestAnUpdateRenumbersAWhereAnyGroup(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	_, _ = accounts(ctx).
		Where("price", 5).
		WhereAny(func(q *accountQuery) { q.WhereKey(1) }, func(q *accountQuery) { q.WhereKey(2) }).
		WithoutTimestamps().
		Update(Values{"price": 10})

	if want := `update "accounts" set "price" = $1 where "price" = $2 and (("accounts"."id" = $3) or ("accounts"."id" = $4))`; conn.sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", conn.sql, want)
	}

	if want := []any{10, 5, 1, 2}; !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("args = %v, want %v", conn.args, want)
	}
}

// Only a SELECT takes the row lock: counting, checking, updating or deleting
// through the same query never does.
func TestOnlyASelectTakesTheRowLock(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	locked := func() *accountQuery { return accounts(ctx).WhereKey(1).LockForUpdate() }

	cases := []struct {
		name string
		run  func()
		sql  string
	}{
		{"First", func() { _, _ = locked().First() }, `select * from "accounts" where "accounts"."id" = $1 limit 1 for update`},
		{"Count", func() { _, _ = locked().Count() }, `select count(*) from "accounts" where "accounts"."id" = $1`},
		{"Exists", func() { _, _ = locked().Exists() }, `select exists(select 1 from "accounts" where "accounts"."id" = $1)`},
		{"Update", func() { _, _ = locked().WithoutTimestamps().Update(Values{"price": 10}) }, `update "accounts" set "price" = $1 where "accounts"."id" = $2`},
		{"Delete", func() { _, _ = locked().Delete() }, `delete from "accounts" where "accounts"."id" = $1`},
		{"Delete a chunk", func() { _, _ = locked().Limit(5).Delete() }, `delete from "accounts" where "accounts"."ctid" in (select "accounts"."ctid" from "accounts" where "accounts"."id" = $1 limit 5)`},
	}

	for _, c := range cases {
		c.run()

		if conn.sql != c.sql {
			t.Errorf("%s: sql =\n%s\nwant\n%s", c.name, conn.sql, c.sql)
		}
	}
}

// Exists asks for the first match only, not a count of every one.
func TestExistsAndDoesntExistAskForTheFirstMatch(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	for _, found := range []bool{true, false} {
		conn.answer = found

		exists, err := accounts(ctx).Where("price", 10).Exists()
		if err != nil || exists != found {
			t.Fatalf("Exists() = %v, %v with a match %v", exists, err, found)
		}

		if want := `select exists(select 1 from "accounts" where "price" = $1)`; conn.sql != want {
			t.Fatalf("sql = %s", conn.sql)
		}

		doesntExist, err := accounts(ctx).Where("price", 10).DoesntExist()
		if err != nil || doesntExist == found {
			t.Fatalf("DoesntExist() = %v, %v with a match %v", doesntExist, err, found)
		}
	}
}

func TestAnEncryptedColumnIsWrittenSealedAndReadOpened(t *testing.T) {
	ctx, conn, encrypter := schemaCase(t)

	_, _ = accounts(ctx).Create(Values{"secret": "X1234567", "notes": (*string)(nil)})

	// created_at, notes, secret, updated_at
	if conn.args[1] != nil {
		t.Fatalf("a nil stays NULL, got %v", conn.args[1])
	}

	sealed, _ := conn.args[2].(string)
	if plain, err := encrypter.DecryptString(sealed); err != nil || plain != "X1234567" {
		t.Fatalf("stored %q opens to %q (%v)", sealed, plain, err)
	}

	rows := &fakeRows{
		n:       1,
		columns: []pgconn.FieldDescription{{Name: "secret"}, {Name: "notes"}},
		values:  []any{sealed, nil},
	}
	got, err := pgx.CollectRows(rows, rowsTo[account](accounts(ctx).spec))
	if err != nil {
		t.Fatal(err)
	}

	if got[0].Secret != "X1234567" || got[0].Notes != nil {
		t.Fatalf("scanned %+v", got[0])
	}
}

// A partial select reads an encrypted column opened too, into a scalar or a
// map; a table query, which has no schema, reads what is stored.
func TestAPartialSelectReadsAnEncryptedColumnOpened(t *testing.T) {
	ctx, _, encrypter := schemaCase(t)

	sealed, err := encrypter.EncryptString("X1234567")
	if err != nil {
		t.Fatal(err)
	}

	secret := func() *fakeRows {
		return &fakeRows{n: 1, columns: []pgconn.FieldDescription{{Name: "secret"}}, values: []any{sealed}}
	}

	scalars, err := pgx.CollectRows(secret(), rowsTo[string](accounts(ctx).spec))
	if err != nil || scalars[0] != "X1234567" {
		t.Fatalf("scalar = %v (%v)", scalars, err)
	}

	row := &fakeRows{
		n:       1,
		columns: []pgconn.FieldDescription{{Name: "id"}, {Name: "secret"}, {Name: "notes"}},
		values:  []any{int64(7), sealed, nil},
	}
	maps, err := pgx.CollectRows(row, rowsTo[map[string]any](accounts(ctx).spec))
	if want := map[string]any{"id": int64(7), "secret": "X1234567", "notes": nil}; err != nil || !reflect.DeepEqual(maps[0], want) {
		t.Fatalf("map = %v (%v), want %v", maps, err, want)
	}

	stored, err := pgx.CollectRows(secret(), rowsTo[string](Table[string](ctx, "accounts").spec))
	if err != nil || stored[0] != sealed {
		t.Fatalf("a table query read %v (%v), want the stored ciphertext", stored, err)
	}
}

func TestAnEncryptedColumnNeverStoresTheClear(t *testing.T) {
	ctx, _, _ := schemaCase(t)

	if _, err := accounts(ctx).Create(Values{"secret": 42}); err == nil {
		t.Fatal("a value that isn't text must fail the write")
	}

	unbound := WithConn(t.Context(), &recorder{})
	if _, err := accounts(unbound).Create(Values{"secret": "X"}); !errors.Is(err, crypt.ErrNoEncrypter) {
		t.Fatalf("err = %v, want ErrNoEncrypter", err)
	}
}

// A JSON column stores every value encoded, a Go string as a JSON string;
// only a json.RawMessage, already JSON, is stored as given.
func TestAJSONColumnStoresTheEncodedValue(t *testing.T) {
	cast := JSON(json.Marshal)

	for name, tc := range map[string]struct {
		value any
		want  any
	}{
		"nil":                   {nil, nil},
		"a string is a string":  {"Paris", `"Paris"`},
		"JSON text is a string": {`{"a": 1}`, `"{\"a\": 1}"`},
		"raw as given":          {json.RawMessage(`[1,2]`), `[1,2]`},
		"a nil raw":             {json.RawMessage(nil), nil},
		"a value encoded":       {map[string]int{"b": 2}, `{"b":2}`},
		"a null is NULL":        {(*struct{})(nil), nil},
	} {
		got, err := cast.Set(t.Context(), tc.value)
		if err != nil || got != tc.want {
			t.Errorf("%s: Set = %v (%v), want %v", name, got, err, tc.want)
		}
	}
}

func TestAnEncryptedJSONColumnSealsTheEncodedValue(t *testing.T) {
	ctx, _, encrypter := schemaCase(t)

	set, err := accountSchema.Columns["vault"].Cast.Set(ctx, map[string]string{"email": "a/b@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	sealed, _ := set.(string)
	if plain, err := encrypter.DecryptString(sealed); err != nil || plain != `{"email":"a/b@example.com"}` {
		t.Fatalf("stored opens to %q (%v)", plain, err)
	}

	var vault json.RawMessage
	scanner, _ := accountSchema.Columns["vault"].Cast.Scanner(ctx, &vault).(sql.Scanner)
	if err := scanner.Scan(sealed); err != nil || string(vault) != `{"email":"a/b@example.com"}` {
		t.Fatalf("scanned %s (%v)", vault, err)
	}

	if set, err := accountSchema.Columns["vault"].Cast.Set(ctx, nil); set != nil || err != nil {
		t.Fatalf("nil stores %v (%v), want NULL", set, err)
	}

	set, err = accountSchema.Columns["vault"].Cast.Set(ctx, "Paris")
	if err != nil {
		t.Fatal(err)
	}

	sealed, _ = set.(string)
	if plain, err := encrypter.DecryptString(sealed); err != nil || plain != `"Paris"` {
		t.Fatalf("a string stored opens to %q (%v), want the JSON string", plain, err)
	}
}

// UpdateChanged compares each value as its column's type in the WHERE, and
// reads back the rows it changed; with nothing to write it runs nothing.
func TestUpdateChangedWritesOnlyARowThatDiffersAndReadsItBack(t *testing.T) {
	ctx, conn, encrypter := schemaCase(t)
	sealed, err := encrypter.EncryptString("X1234567")
	if err != nil {
		t.Fatal(err)
	}
	conn.rows = rowsOf([]string{"id", "secret"}, []any{int64(1), sealed})

	updated, err := accounts(ctx).WhereKey(1).UpdateChanged(Values{"price": 150, "settings": map[string]int{"a": 1}, "nickname": "Al"})
	if err != nil {
		t.Fatal(err)
	}

	want := `update "accounts" set "nickname" = $1, "price" = $2, "settings" = $3, "updated_at" = $4 ` +
		`where "accounts"."id" = $5 and ("nickname"::text is distinct from cast($6 as text) ` +
		`or "price" is distinct from cast($7 as numeric) or "settings"::jsonb is distinct from cast($8 as jsonb)) returning *`
	if conn.sql != want {
		t.Fatalf("sql =\n%s\nwant\n%s", conn.sql, want)
	}

	if !reflect.DeepEqual(conn.args[5:], []any{"Al", 150, `{"a":1}`}) {
		t.Fatalf("compared %v", conn.args[5:])
	}

	if want := []account{{ID: 1, Secret: "X1234567"}}; !reflect.DeepEqual(updated, want) {
		t.Fatalf("read back %+v, want %+v, its secret opened", updated, want)
	}

	conn.sql = ""
	if updated, err := accounts(ctx).WhereKey(1).UpdateChanged(Values{}); updated != nil || err != nil || conn.sql != "" {
		t.Fatalf("nothing to write ran %q", conn.sql)
	}
}
