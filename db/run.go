package db

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/collect"
)

// spec is the untyped state behind every builder; its methods and the
// generic helpers below run the compiled statement on the context's connection.
type spec struct {
	ctx    context.Context
	table  string
	model  string
	schema *Schema
	// withoutTimestamps stops this query's writes stamping the schema's
	// timestamps.
	withoutTimestamps bool
	selects           []string
	joins             []string
	wheres            []string
	orders            []string
	limit             int
	offset            int
	lockForUpdate     bool
	args              []any
}

// get runs the compiled SELECT and scans every row into R.
func get[R any](s *spec) ([]R, error) {
	rows, err := s.query()
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, rowsTo[R](s))
}

// getAs is get for a row shape other than the model: before reading a row it
// checks the result has a column for every field R declares.
func getAs[R any](s *spec) ([]R, error) {
	rows, err := s.query()
	if err != nil {
		return nil, err
	}

	if err := requireColumns(reflect.TypeFor[R](), rows.FieldDescriptions()); err != nil {
		rows.Close()

		return nil, err
	}

	return pgx.CollectRows(rows, rowsTo[R](s))
}

// pluck reads the one selected column of every row into V positionally,
// whatever V's kind: a sql.NullString is the column's value, not a row.
func pluck[V any](s *spec) ([]V, error) {
	rows, err := s.query()
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, valueScanner[V](s))
}

// pluckBy reads a two-column result set into a key => value map.
func pluckBy[K comparable, V any](s *spec) (map[K]V, error) {
	rows, err := s.query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	plucked := map[K]V{}
	for rows.Next() {
		var (
			key   K
			value V
		)
		if err := s.scanColumns(rows, &key, &value); err != nil {
			return nil, err
		}

		plucked[key] = value
	}

	return plucked, rows.Err()
}

// query runs the compiled SELECT.
func (s *spec) query() (pgx.Rows, error) {
	conn, err := Conn(s.ctx)
	if err != nil {
		return nil, err
	}

	sql, args := s.toSQL()
	rows, err := conn.Query(s.ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("db: %s: %w", sql, err)
	}

	return rows, nil
}

// value is the first row's one selected column, or nil when there is none.
func value[V any](s *spec) (*V, error) {
	s.limit = 1

	return head(pluck[V](s))
}

// head is a read's first row, or nil when it found none.
func head[R any](rows []R, err error) (*R, error) {
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	return &rows[0], nil
}

// create runs the INSERT of one row, followed by clause, and reads back the
// row it wrote.
func create[R any](s *spec, values Values, clause string) (R, error) {
	var zero R

	sql, args, err := s.insertSQL([]Values{values})
	if err != nil {
		return zero, err
	}

	sql += clause
	written, err := returning[R](s, sql, args)
	if err != nil {
		return zero, err
	}

	if len(written) != 1 {
		return zero, fmt.Errorf("db: %s: wrote %d rows, not 1", sql, len(written))
	}

	return written[0], nil
}

// returning runs a write and reads back the rows it wrote. Postgres reports
// a write's failure (a constraint, a cast) while the rows are read, not when
// the statement is sent, so both name the statement.
func returning[R any](s *spec, sql string, args []any) ([]R, error) {
	conn, err := Conn(s.ctx)
	if err != nil {
		return nil, err
	}

	sql += " returning *"
	rows, err := conn.Query(s.ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("db: %s: %w", sql, err)
	}

	written, err := pgx.CollectRows(rows, rowsTo[R](s))
	if err != nil {
		return nil, fmt.Errorf("db: %s: %w", sql, err)
	}

	return written, nil
}

// insert runs the INSERT of rows, followed by clause, and returns how many
// it wrote; no rows is no statement.
func (s *spec) insert(rows []Values, clause string) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	sql, args, err := s.insertSQL(rows)
	if err != nil {
		return 0, err
	}

	return Exec(s.ctx, sql+clause, args...)
}

// insertSQL is the INSERT of rows as the table stores them. The columns are
// the first row's, sorted for a stable statement, and every row must carry
// the same ones.
func (s *spec) insertSQL(rows []Values) (sql string, args []any, err error) {
	var columns []string
	tuples := make([][]any, len(rows))
	for i, values := range rows {
		stored, err := s.stored(values, true)
		if err != nil {
			return "", nil, err
		}

		keys := slices.Sorted(maps.Keys(stored))
		if i == 0 {
			columns = keys
		}

		if !slices.Equal(keys, columns) {
			return "", nil, fmt.Errorf("db: inserting into %s: row %d has the columns %v, the first row %v", s.table, i+1, keys, columns)
		}

		tuples[i] = collect.Map(columns, func(column string) any { return stored[column] })
	}

	sql, args = s.valuesSQL(columns, tuples)

	return sql, args, nil
}

// upsert runs the INSERT of positional rows, updating the update columns of
// any that conflicts over uniqueBy. Casts apply, and the timestamps the
// columns leave out are stamped (only updated_at on update).
func (s *spec) upsert(columns []string, rows [][]any, uniqueBy []string, update []string) error {
	if len(rows) == 0 {
		return nil
	}

	stamps := s.unstamped(columns, true)
	var now []any
	if len(stamps) > 0 {
		now = slices.Repeat([]any{bindable(clock.Now(s.ctx))}, len(stamps))
	}

	tuples := make([][]any, len(rows))
	for i, row := range rows {
		if len(row) != len(columns) {
			return fmt.Errorf("db: upserting %s: row %d has %d values for %d columns", s.table, i+1, len(row), len(columns))
		}

		tuple := make([]any, len(columns), len(columns)+len(now))
		for j, value := range row {
			stored, err := s.store(columns[j], value)
			if err != nil {
				return err
			}
			tuple[j] = stored
		}
		tuples[i] = append(tuple, now...)
	}

	sql, args := s.valuesSQL(slices.Concat(columns, stamps), tuples)
	_, err := Exec(s.ctx, sql+s.onConflict(uniqueBy, update), args...)

	return err
}

// valuesSQL is the INSERT of rows, each bound in columns' order.
func (s *spec) valuesSQL(columns []string, rows [][]any) (sql string, args []any) {
	tuples := make([]string, len(rows))
	args = make([]any, 0, len(rows)*len(columns))
	for i, row := range rows {
		placeholders := make([]string, len(row))
		for j, value := range row {
			args = append(args, value)
			placeholders[j] = "$" + strconv.Itoa(len(args))
		}
		tuples[i] = "(" + strings.Join(placeholders, ", ") + ")"
	}

	sql = fmt.Sprintf("insert into %s (%s) values %s", wrap(s.table), wrapAll(columns), strings.Join(tuples, ", "))

	return sql, args
}

// onConflict turns an INSERT conflicting over uniqueBy into an update of
// the update columns and updated_at (created_at stays), or with nothing to
// update leaves the stored row alone. A unique index must cover uniqueBy.
func (s *spec) onConflict(uniqueBy []string, update []string) string {
	columns := slices.Concat(update, s.unstamped(update, false))
	if len(columns) == 0 {
		return fmt.Sprintf(" on conflict (%s) do nothing", wrapAll(uniqueBy))
	}

	sets := collect.Map(columns, func(column string) string {
		return wrap(column) + " = excluded." + wrap(column)
	})

	return fmt.Sprintf(" on conflict (%s) do update set %s", wrapAll(uniqueBy), strings.Join(sets, ", "))
}

// update writes values to the matching rows and returns how many it wrote.
func (s *spec) update(values Values) (int64, error) {
	sql, args, err := s.updateSQL(values, false)
	if err != nil {
		return 0, err
	}

	return Exec(s.ctx, sql, args...)
}

// updateChanged writes values to the matching rows where any of them
// differs from what is stored, and reads back the rows it wrote; no values
// is no statement.
func updateChanged[R any](s *spec, values Values) ([]R, error) {
	if len(values) == 0 {
		return nil, nil
	}

	sql, args, err := s.updateSQL(values, true)
	if err != nil {
		return nil, err
	}

	return returning[R](s, sql, args)
}

// updateSQL is the UPDATE of values on the matching rows; onlyChanged also
// requires one of them to differ from what is stored.
func (s *spec) updateSQL(values Values, onlyChanged bool) (sql string, args []any, err error) {
	stored, err := s.stored(values, false)
	if err != nil {
		return "", nil, err
	}

	if onlyChanged {
		s.whereChanged(values, stored)
	}

	// SET placeholders must precede the WHERE ones already bound, so the
	// statement is numbered afresh: SET values first, then the where args.
	columns := slices.Sorted(maps.Keys(stored))
	sets := make([]string, len(columns))
	args = make([]any, 0, len(columns)+len(s.args))
	for i, column := range columns {
		args = append(args, stored[column])
		sets[i] = fmt.Sprintf("%s = $%d", wrap(column), i+1)
	}

	sql = fmt.Sprintf("update %s set %s", wrap(s.table), strings.Join(sets, ", "))
	if len(s.wheres) > 0 {
		sql += " where " + renumber(strings.Join(s.wheres, " and "), len(columns))
	}

	return sql, append(args, s.args...), nil
}

// whereChanged keeps rows where a written column differs from its stored
// value, compared as the column's type (a numeric 150.00 equals 150), as text
// when the schema doesn't type it, and json as jsonb, which has equality.
func (s *spec) whereChanged(values Values, stored Values) {
	columns := slices.Sorted(maps.Keys(values))
	conditions := make([]string, len(columns))
	for i, column := range columns {
		placeholder := s.placeholder(stored[column])

		var kind string
		if s.schema != nil {
			kind = s.schema.Columns[column].Type
		}

		switch kind {
		case "":
			conditions[i] = fmt.Sprintf("%s::text is distinct from cast(%s as text)", wrap(column), placeholder)
		case "json", "jsonb":
			conditions[i] = fmt.Sprintf("%s::jsonb is distinct from cast(%s as jsonb)", wrap(column), placeholder)
		default:
			conditions[i] = fmt.Sprintf("%s is distinct from cast(%s as %s)", wrap(column), placeholder, kind)
		}
	}

	s.wheres = append(s.wheres, "("+strings.Join(conditions, " or ")+")")
}

func (s *spec) delete() (int64, error) {
	if s.limit > 0 || len(s.joins) > 0 {
		ctid := wrap(s.table + ".ctid")
		inner, args := s.compile(ctid, true)

		return Exec(s.ctx, fmt.Sprintf("delete from %s where %s in (%s)", wrap(s.table), ctid, inner), args...)
	}

	sql := "delete from " + wrap(s.table)
	if len(s.wheres) > 0 {
		sql += " where " + strings.Join(s.wheres, " and ")
	}

	return Exec(s.ctx, sql, s.args...)
}

func (s *spec) count() (int64, error) {
	sql, args := s.compile("count(*)", false)

	return scalar[int64](s.ctx, sql, args)
}

// exists asks Postgres to stop at the first match, where a count would read
// every one.
func (s *spec) exists() (bool, error) {
	sql, args := s.compile("1", false)

	return scalar[bool](s.ctx, "select exists("+sql+")", args)
}

// scalar runs a one-value query.
func scalar[V any](ctx context.Context, sql string, args []any) (V, error) {
	var value V

	conn, err := Conn(ctx)
	if err != nil {
		return value, err
	}

	if err := conn.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
		return value, fmt.Errorf("db: %s: %w", sql, err)
	}

	return value, nil
}
