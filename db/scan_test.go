package db

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type bookingRow struct {
	ID         int64     `db:"id"`
	Name       string    `db:"name"`
	Status     string    `db:"status"`
	Passengers int       `db:"passengers"`
	DepartsAt  time.Time `db:"departs_at"`
	CreatedAt  time.Time `db:"created_at"`
	Ignored    string    `db:"-"`
}

// fakeRows serves n identical rows of the given columns without a database,
// so the scan path is measured on its own; rowsOf serves distinct rows.
type fakeRows struct {
	columns []pgconn.FieldDescription
	values  []any
	table   [][]any
	n, at   int
}

// rowsOf serves each of rows in turn, as the columns.
func rowsOf(columns []string, rows ...[]any) *fakeRows {
	fake := &fakeRows{table: rows, n: len(rows)}
	for _, name := range columns {
		fake.columns = append(fake.columns, pgconn.FieldDescription{Name: name})
	}

	return fake
}

func newFakeRows(n int, columns ...string) *fakeRows {
	rows := &fakeRows{n: n}
	for _, name := range columns {
		rows.columns = append(rows.columns, pgconn.FieldDescription{Name: name})
		rows.values = append(rows.values, sampleValue(strings.ToLower(name)))
	}

	return rows
}

func sampleValue(column string) any {
	switch column {
	case "id":
		return int64(42)
	case "passengers":
		return 3
	case "departs_at", "created_at":
		return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	default:
		return column + "-value"
	}
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return r.columns }
func (r *fakeRows) Values() ([]any, error)                       { return r.values, nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) TypeMap() *pgtype.Map                         { return nil }

func (r *fakeRows) Next() bool {
	r.at++
	if r.table != nil && r.at <= r.n {
		r.values = r.table[r.at-1]
	}

	return r.at <= r.n
}

func (r *fakeRows) Scan(dest ...any) error {
	for i, d := range dest {
		switch d := d.(type) {
		case nil:
			// pgx skips a nil destination.
		case pgx.RowScanner:
			return d.ScanRow(r)
		case *int64:
			*d = r.values[i].(int64)
		case **int64:
			*d = nil
			if v, ok := r.values[i].(int64); ok {
				*d = &v
			}
		case *int:
			*d = r.values[i].(int)
		case *string:
			*d = r.values[i].(string)
		case *time.Time:
			*d = r.values[i].(time.Time)
		case *any:
			*d = r.values[i]
		case sql.Scanner:
			if err := d.Scan(r.values[i]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected destination %T", d)
		}
	}

	return nil
}

func TestScansStructsByColumnName(t *testing.T) {
	rows := newFakeRows(2, "ID", "name", "added_later", "passengers", "departs_at")

	got, err := pgx.CollectRows(rows, rowsTo[bookingRow](&spec{}))
	if err != nil {
		t.Fatal(err)
	}

	want := bookingRow{ID: 42, Name: "name-value", Passengers: 3, DepartsAt: sampleValue("departs_at").(time.Time)}
	if len(got) != 2 || got[0] != want || got[1] != want {
		t.Fatalf("rows = %+v, want two of %+v", got, want)
	}
}

func benchmarkScan(b *testing.B, n int) {
	b.Helper()
	b.ReportAllocs()

	columns := []string{"id", "name", "status", "passengers", "departs_at", "created_at", "added_later"}
	for b.Loop() {
		if _, err := pgx.CollectRows(newFakeRows(n, columns...), rowsTo[bookingRow](&spec{})); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanStruct1Row(b *testing.B)    { benchmarkScan(b, 1) }
func BenchmarkScanStruct200Rows(b *testing.B) { benchmarkScan(b, 200) }
