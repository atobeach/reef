package db

import (
	"cmp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/atobeach/reef/support"
)

// ResolveRouteBinding is the row a route parameter names by the model's
// route key (RouteKey, else id), or a ModelNotFoundError 404, also for a
// value the column can't hold, without a query Postgres would reject. The
// query it's called on scopes it and eager-loads for it:
//
//	search, err := models.SavedSearches(c).ResolveRouteBinding(c.Param("savedSearch"))
//	city, err := country.Cities(c).ResolveRouteBinding(c.Param("city")) // the country's own only
//
// It panics on a db.Table query, which has no schema to name the key.
func (b *Builder[T, Q]) ResolveRouteBinding(value string) (T, error) {
	if b.spec.schema == nil {
		panic("db: ResolveRouteBinding needs a model; " + b.spec.table + " has no schema")
	}

	key := cmp.Or(b.spec.schema.RouteKey, "id")

	bound, ok := routeKeyValue(b.spec.schema.Columns[key].Type, value)
	if !ok {
		var zero T

		return zero, &ModelNotFoundError{Model: b.spec.model}
	}

	s := b.spec.clone()
	s.whereOp(s.table+"."+key, "=", bound)

	return b.firstOrFail(s)
}

// routeKeyValue is value as a route key column of columnType compares it,
// and whether such a column can hold it at all.
func routeKeyValue(columnType string, value string) (any, bool) {
	if columnType == "uuid" {
		return value, support.IsUUID(value)
	}

	if bits := map[string]int{"int2": 16, "int4": 32, "int8": 64}[columnType]; bits > 0 {
		n, err := strconv.ParseInt(value, 10, bits)

		return n, err == nil
	}

	// A path segment unescapes to any bytes (%00, %FF), and Postgres text
	// holds neither a NUL nor invalid UTF-8.
	return value, utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
