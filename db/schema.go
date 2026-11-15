package db

import (
	"fmt"
	"slices"
	"time"

	"github.com/atobeach/reef/clock"
)

// Schema is what a generated model tells the builder about its table: the
// timestamps its writes stamp, the column a route parameter names a row by,
// and each column's Postgres type and cast.
type Schema struct {
	// CreatedAt and UpdatedAt are the columns writes stamp from the
	// context's clock (an insert both, an update the second) unless the
	// write sets them; empty is none.
	CreatedAt, UpdatedAt string
	// RouteKey is the column ResolveRouteBinding finds a row by; empty is id.
	RouteKey string
	Columns  map[string]Column
}

// Column is one column: its Postgres type (udt_name) and cast, if any.
type Column struct {
	Type string
	Cast Cast
}

// stored is values as the table stores them: each cast applied, timestamps
// stamped, times bound at the second.
func (s *spec) stored(values Values, inserting bool) (Values, error) {
	out := make(Values, len(values)+2)
	for column, value := range values {
		stored, err := s.store(column, value)
		if err != nil {
			return nil, err
		}

		out[column] = stored
	}

	if stamps := s.timestamps(inserting); len(stamps) > 0 {
		now := bindable(clock.Now(s.ctx))
		for _, column := range stamps {
			if _, set := out[column]; !set {
				out[column] = now
			}
		}
	}

	return out, nil
}

// store is value as column stores it: through the column's cast, bound.
func (s *spec) store(column string, value any) (any, error) {
	if cast := s.cast(column); cast != nil {
		set, err := cast.Set(s.ctx, value)
		if err != nil {
			return nil, fmt.Errorf("db: casting %s.%s: %w", s.table, column, err)
		}
		value = set
	}

	return bindable(value), nil
}

// timestamps are the columns a write stamps: created_at and updated_at on
// insert, updated_at on update, none on a table or WithoutTimestamps.
func (s *spec) timestamps(inserting bool) []string {
	if s.schema == nil || s.withoutTimestamps {
		return nil
	}

	stamps := []string{s.schema.CreatedAt, s.schema.UpdatedAt}
	if !inserting {
		stamps = stamps[1:]
	}

	return slices.DeleteFunc(stamps, func(column string) bool { return column == "" })
}

// unstamped is the timestamps a write stamps that columns leaves out.
func (s *spec) unstamped(columns []string, inserting bool) []string {
	return slices.DeleteFunc(s.timestamps(inserting), func(column string) bool { return slices.Contains(columns, column) })
}

// cast is the column's cast, nil when it has none.
func (s *spec) cast(column string) Cast {
	if s.schema == nil {
		return nil
	}

	return s.schema.Columns[column].Cast
}

// bindable is value as bound: a time in UTC at the second, as every
// timestamp(0) column stores it (Postgres would round a fraction up). It
// stays a time.Time, so a timestamptz column still gets an instant.
func bindable(value any) any {
	switch v := value.(type) {
	case time.Time:
		return v.UTC().Truncate(time.Second)
	case *time.Time:
		if v == nil {
			return nil
		}

		return v.UTC().Truncate(time.Second)
	}

	return value
}
