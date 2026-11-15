package db

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// structFields maps a struct type's column names to field index paths,
// computed once per type.
var structFields sync.Map // reflect.Type → map[string][]int

// structScanner scans one result set into T by column name, through each
// column's cast. Unlike pgx.RowToStructByNameLax it skips a column the struct
// doesn't know: a migration adding one must not break every running
// `SELECT *` until the models are regenerated.
func structScanner[T any](s *spec) pgx.RowToFunc[T] {
	return (&structScan[T]{spec: s}).scan
}

// structScan is one result set's column plan.
type structScan[T any] struct {
	spec    *spec
	paths   [][]int // field index path per column; nil skips the column
	casts   []Cast  // cast per column; nil reads it as is
	dest    []any
	discard any
}

func (s *structScan[T]) scan(row pgx.CollectableRow) (T, error) {
	var value T

	target := reflect.ValueOf(&value).Elem()
	if s.dest == nil {
		s.plan(target.Type(), row.FieldDescriptions())
	}

	for i, path := range s.paths {
		if path == nil {
			s.dest[i] = &s.discard

			continue
		}

		s.dest[i] = target.FieldByIndex(path).Addr().Interface()
		if s.casts[i] != nil {
			s.dest[i] = s.casts[i].Scanner(s.spec.ctx, s.dest[i])
		}
	}

	if err := row.Scan(s.dest...); err != nil {
		return value, fmt.Errorf("db: scanning %s: %w", target.Type(), err)
	}

	return value, nil
}

// plan matches the result set's columns to t's fields and the schema's casts.
func (s *structScan[T]) plan(t reflect.Type, columns []pgconn.FieldDescription) {
	s.paths = columnPaths(t, columns)
	s.casts = make([]Cast, len(columns))
	for i, column := range columns {
		s.casts[i] = s.spec.cast(strings.ToLower(column.Name))
	}
	s.dest = make([]any, len(columns))
}

// columnPaths matches a result set's columns to t's field index paths.
func columnPaths(t reflect.Type, columns []pgconn.FieldDescription) [][]int {
	fields := fieldsOf(t)
	paths := make([][]int, len(columns))
	for i, column := range columns {
		paths[i] = fields[strings.ToLower(column.Name)]
	}

	return paths
}

// fieldsOf maps lower-cased column names (the `db` tag, else the field name)
// to field index paths, the first field winning a name.
func fieldsOf(t reflect.Type) map[string][]int {
	if cached, ok := structFields.Load(t); ok {
		return cached.(map[string][]int)
	}

	fields := map[string][]int{}

	var walk func(t reflect.Type, prefix []int)
	walk = func(t reflect.Type, prefix []int) {
		for i := range t.NumField() {
			field := t.Field(i)
			path := append(append([]int(nil), prefix...), i)

			tag, hasTag := field.Tag.Lookup("db")
			if tag == "-" {
				continue
			}

			// Untagged embedded structs contribute their fields, as pgx's do.
			if field.Anonymous && !hasTag && field.Type.Kind() == reflect.Struct {
				walk(field.Type, path)

				continue
			}

			if !field.IsExported() {
				continue
			}

			name := tag
			if !hasTag || name == "" {
				name = field.Name
			}

			if _, taken := fields[strings.ToLower(name)]; !taken {
				fields[strings.ToLower(name)] = path
			}
		}
	}

	walk(t, nil)
	structFields.Store(t, fields)

	return fields
}

// requireColumns refuses a struct row shape declaring a field the result has
// no column for, so a misspelled alias fails rather than reading as null. A
// field an embedded model brings is exempt: like Get, it reads the model
// whatever the columns, so a `table.*` select keeps working while a
// migration adds or drops a column. With no columns, the statement failed
// and reports its own error.
func requireColumns(t reflect.Type, columns []pgconn.FieldDescription) error {
	if t.Kind() != reflect.Struct || t == reflect.TypeFor[time.Time]() || len(columns) == 0 {
		return nil
	}

	selected := make(map[string]bool, len(columns))
	for _, column := range columns {
		selected[strings.ToLower(column.Name)] = true
	}

	var missing []string
	for name, path := range fieldsOf(t) {
		if !selected[name] && !inEmbeddedModel(t, path) {
			missing = append(missing, name)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	if len(missing) == 1 {
		return fmt.Errorf("db: scanning %s: the result has no column %s; select it, or tag the field db:\"-\"", t, missing[0])
	}

	slices.Sort(missing)

	return fmt.Errorf("db: scanning %s: the result has no columns %s; select them, or tag the fields db:\"-\"", t, strings.Join(missing, ", "))
}

// inEmbeddedModel reports whether the field at path is reached through an
// embedded model.
func inEmbeddedModel(t reflect.Type, path []int) bool {
	for _, index := range path[:len(path)-1] {
		field := t.Field(index)
		if field.Type.Implements(reflect.TypeFor[Model]()) {
			return true
		}

		t = field.Type
	}

	return false
}

// rowsTo returns the scanner for one of s's result sets: by column name into
// structs, a column => value map for map[string]any, or positionally into
// scalars. Every shape reads a cast column through its cast, so a partial
// select of an encrypted column reads it opened.
func rowsTo[R any](s *spec) pgx.RowToFunc[R] {
	switch t := reflect.TypeFor[R](); {
	case t == reflect.TypeFor[map[string]any]():
		return func(row pgx.CollectableRow) (R, error) {
			values, err := s.mapRow(row)

			return any(values).(R), err
		}
	case t.Kind() == reflect.Struct && t != reflect.TypeFor[time.Time]():
		return structScanner[R](s)
	default:
		return valueScanner[R](s)
	}
}

// valueScanner scans a one-column row positionally into V, through the
// column's cast.
func valueScanner[V any](s *spec) pgx.RowToFunc[V] {
	return func(row pgx.CollectableRow) (V, error) {
		var value V
		err := s.scanColumns(row, &value)

		return value, err
	}
}

// scanColumns scans row positionally into dests, a column the query's
// schema casts read through its cast. A count mismatch is Scan's error.
func (s *spec) scanColumns(row pgx.CollectableRow, dests ...any) error {
	if columns := row.FieldDescriptions(); len(columns) == len(dests) {
		for i, dest := range dests {
			if cast := s.cast(strings.ToLower(columns[i].Name)); cast != nil {
				dests[i] = cast.Scanner(s.ctx, dest)
			}
		}
	}

	return row.Scan(dests...)
}

// mapRow is a row as a column => value map, each value as pgx reads it, and
// each cast column scanned again through its cast.
func (s *spec) mapRow(row pgx.CollectableRow) (map[string]any, error) {
	values, err := pgx.RowToMap(row)
	if err != nil || s.schema == nil {
		return values, err
	}

	columns := row.FieldDescriptions()
	opened := make([]any, len(columns))
	dest := make([]any, len(columns)) // nil skips a column
	cast := false
	for i, column := range columns {
		if c := s.cast(strings.ToLower(column.Name)); c != nil {
			dest[i], cast = c.Scanner(s.ctx, &opened[i]), true
		}
	}

	if !cast {
		return values, nil
	}

	if err := row.Scan(dest...); err != nil {
		return nil, fmt.Errorf("db: scanning %s: %w", s.table, err)
	}

	for i, column := range columns {
		if dest[i] != nil {
			values[column.Name] = opened[i]
		}
	}

	return values, nil
}

// modelName is the model a ModelNotFoundError names: the row type's name.
func modelName[T any]() string {
	name := reflect.TypeFor[T]().Name()
	if name == "" {
		return "Model"
	}

	return name
}
