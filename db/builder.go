package db

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/atobeach/reef/clock"
)

// Model is a generated row type: a plain struct whose `db` tags name its
// columns, its table, and the Schema its writes follow.
type Model interface {
	TableName() string
	Schema() *Schema
}

// Values is a column => value map for inserts and updates.
type Values map[string]any

// Builder is the fluent query builder. T is the row type it scans into; Q is
// the concrete query type its chain methods return, so a model's scopes and
// the builder's own methods chain in any order:
//
//	type CityQuery struct{ db.Builder[City, *CityQuery] }
//
//	func Cities(ctx context.Context) *CityQuery { return db.NewQuery[City](ctx, &CityQuery{}) }
//
//	func (q *CityQuery) Published() *CityQuery { return q.WhereNotNull(CityTableColumns.PublishedAt) }
//
//	models.Cities(c).Where(models.CityColumns.CountryID, id).Published().OrderBy(models.CityColumns.Name).Get()
//
// Column arguments are table and column names, quoted: a generated model's
// column identifiers, or a name as written for a table without a model. One
// that isn't a name panics where the query is built. Raw SQL enters only
// through SelectRaw, WhereRaw and OrderByRaw, whose `?` bindings compile to
// `$n`; write `??` for a literal question mark (the jsonb `?` operator).
//
// A chain method changes the builder it is called on; a terminal read
// (First, Find, Pluck, Value, ResolveRouteBinding, UpdateChanged, ...) runs on
// a copy, so the query is as it was afterwards.
type Builder[T any, Q any] struct {
	spec  *spec
	self  Q
	eager []func(ctx context.Context, rows []T) error
}

// query is satisfied by any struct embedding Builder[T, *Q]: the embedded
// attach method is promoted to it, which is how NewQuery reaches the builder.
type query[Q any] interface {
	*Q
	attach(spec *spec, self *Q)
}

func (b *Builder[T, Q]) attach(spec *spec, self Q) {
	b.spec = spec
	b.self = self
}

// NewQuery wires a model query type (a struct embedding Builder) to a fresh
// builder over the model's table and schema; generated models call it.
func NewQuery[T Model, Q any, P query[Q]](ctx context.Context, q P) P {
	var model T

	q.attach(&spec{ctx: ctx, table: model.TableName(), model: modelName[T](), schema: model.Schema()}, q)

	return q
}

// Rows is the query type for tables without a model, and for code generic
// over models (db.NewQuery[T](ctx, &db.Rows[T]{})).
type Rows[T any] struct {
	Builder[T, *Rows[T]]
}

// Table starts an ad-hoc query over any table, scanning into T. Its writes
// are taken as given: no timestamps, no casts.
func Table[T any](ctx context.Context, table string) *Rows[T] {
	rows := &Rows[T]{}
	rows.attach(&spec{ctx: ctx, table: table, model: modelName[T]()}, rows)

	return rows
}

// Now is the time on the clock the query's context carries, for scopes that
// compare against it.
func (b *Builder[T, Q]) Now() time.Time {
	return clock.Now(b.spec.ctx)
}

// Select replaces the column list: names, `*`, `table.*`, or a name with an
// `AS` alias.
func (b *Builder[T, Q]) Select(columns ...string) Q {
	b.spec.selects = nil

	return b.AddSelect(columns...)
}

// AddSelect appends to the column list.
func (b *Builder[T, Q]) AddSelect(columns ...string) Q {
	for _, column := range columns {
		b.spec.selects = append(b.spec.selects, wrapSelect(column))
	}

	return b.self
}

// SelectRaw appends a raw expression with bindings.
func (b *Builder[T, Q]) SelectRaw(expression string, bindings ...any) Q {
	b.spec.selects = append(b.spec.selects, b.spec.bind(expression, bindings))

	return b.self
}

// Join adds an INNER JOIN: Join("countries", "countries.id", "=", "cities.country_id").
// The table may carry an alias ("pg_class AS src"); the operator is one
// WhereOp takes.
func (b *Builder[T, Q]) Join(table string, first string, operator string, second string) Q {
	return b.join("inner", table, first, operator, second)
}

// LeftJoin adds a LEFT JOIN.
func (b *Builder[T, Q]) LeftJoin(table string, first string, operator string, second string) Q {
	return b.join("left", table, first, operator, second)
}

func (b *Builder[T, Q]) join(kind string, table string, first string, op string, second string) Q {
	b.spec.joins = append(b.spec.joins, fmt.Sprintf("%s join %s on %s %s %s", kind, wrapAliased(table), wrap(first), operator(op), wrap(second)))

	return b.self
}

// Where adds `column = value`.
func (b *Builder[T, Q]) Where(column string, value any) Q {
	return b.WhereOp(column, "=", value)
}

// WhereOp adds `column <operator> value`. The operator is a comparison (=,
// <>, !=, <, <=, >, >=) or [not] [i]like, in either case; any other panics,
// so write that condition with WhereRaw.
func (b *Builder[T, Q]) WhereOp(column string, operator string, value any) Q {
	b.spec.whereOp(column, operator, value)

	return b.self
}

// WhereKey filters on the primary key, table-qualified so a join can't make
// it ambiguous.
func (b *Builder[T, Q]) WhereKey(id any) Q {
	b.spec.whereKey(id)

	return b.self
}

// WhereNull adds `column IS NULL`.
func (b *Builder[T, Q]) WhereNull(column string) Q {
	b.spec.wheres = append(b.spec.wheres, wrap(column)+" is null")

	return b.self
}

// WhereNotNull adds `column IS NOT NULL`.
func (b *Builder[T, Q]) WhereNotNull(column string) Q {
	b.spec.wheres = append(b.spec.wheres, wrap(column)+" is not null")

	return b.self
}

// WhereIn adds `column = ANY($n)`; an empty slice matches nothing.
func (b *Builder[T, Q]) WhereIn[V any](column string, values []V) Q {
	if len(values) == 0 {
		b.spec.wheres = append(b.spec.wheres, "0 = 1")

		return b.self
	}

	b.spec.wheres = append(b.spec.wheres, fmt.Sprintf("%s = any(%s)", wrap(column), b.spec.placeholder(values)))

	return b.self
}

// WhereRaw adds a raw condition with bindings.
func (b *Builder[T, Q]) WhereRaw(condition string, bindings ...any) Q {
	b.spec.wheres = append(b.spec.wheres, "("+b.spec.bind(condition, bindings)+")")

	return b.self
}

// WhereBetween adds `column BETWEEN low AND high`, both ends included.
func (b *Builder[T, Q]) WhereBetween(column string, low any, high any) Q {
	b.spec.wheres = append(b.spec.wheres, fmt.Sprintf("%s between %s and %s", wrap(column), b.spec.placeholder(low), b.spec.placeholder(high)))

	return b.self
}

// WherePast keeps rows whose column is before now, on the query's clock.
func (b *Builder[T, Q]) WherePast(column string) Q {
	return b.WhereOp(column, "<", b.Now())
}

// WhereNowOrFuture keeps rows whose column is now or later, on the query's
// clock.
func (b *Builder[T, Q]) WhereNowOrFuture(column string) Q {
	return b.WhereOp(column, ">=", b.Now())
}

// WhereTodayOrAfter keeps rows whose column falls on today or a later day,
// on the query's clock.
func (b *Builder[T, Q]) WhereTodayOrAfter(column string) Q {
	b.spec.wheres = append(b.spec.wheres, fmt.Sprintf("%s::date >= %s", wrap(column), b.spec.placeholder(b.Now().Format(time.DateOnly))))

	return b.self
}

// WhereAny keeps rows that match any of the scopes, each scope's
// conditions ANDed:
//
//	q.WhereAny(func(q *SearchQuery) { q.Completed() }, func(q *SearchQuery) { q.AwaitingPayment() })
//
// It panics on no scopes, or on a scope that adds no condition or changes
// anything but the wheres: an empty OR would widen the list, and the rest
// would be lost.
func (b *Builder[T, Q]) WhereAny(scopes ...func(q Q)) Q {
	if len(scopes) == 0 {
		panic("db: WhereAny needs at least one scope")
	}

	groups := make([]string, len(scopes))
	for i, scope := range scopes {
		eager := len(b.eager)
		groups[i] = b.spec.group(func() { scope(b.self) })

		if len(b.eager) != eager {
			panic("db: a WhereAny scope may only add conditions")
		}
	}

	b.spec.wheres = append(b.spec.wheres, "("+strings.Join(groups, " or ")+")")

	return b.self
}

// OrderBy sorts ascending.
func (b *Builder[T, Q]) OrderBy(column string) Q {
	b.spec.orders = append(b.spec.orders, wrap(column)+" asc")

	return b.self
}

// OrderByDesc sorts descending.
func (b *Builder[T, Q]) OrderByDesc(column string) Q {
	b.spec.orders = append(b.spec.orders, wrap(column)+" desc")

	return b.self
}

// Latest sorts newest first: by column, else by the table's created_at.
func (b *Builder[T, Q]) Latest(column ...string) Q {
	switch len(column) {
	case 0:
		return b.OrderByDesc(b.spec.table + ".created_at")
	case 1:
		return b.OrderByDesc(column[0])
	}

	panic("db: Latest takes at most one column")
}

// OrderByRaw appends a raw ORDER BY fragment ("popularity desc nulls last").
func (b *Builder[T, Q]) OrderByRaw(expression string, bindings ...any) Q {
	b.spec.orders = append(b.spec.orders, b.spec.bind(expression, bindings))

	return b.self
}

// Limit caps the row count.
func (b *Builder[T, Q]) Limit(n int) Q {
	b.spec.limit = n

	return b.self
}

// Offset skips rows.
func (b *Builder[T, Q]) Offset(n int) Q {
	b.spec.offset = n

	return b.self
}

// LockForUpdate locks the selected rows until the transaction ends
// (SELECT ... FOR UPDATE), so it belongs inside db.Transaction. Count,
// Exists, Update and Delete ignore it.
func (b *Builder[T, Q]) LockForUpdate() Q {
	b.spec.lockForUpdate = true

	return b.self
}

// Create inserts one row and returns it as stored (ids, defaults,
// triggers). A model's casts apply and its timestamps are stamped.
func (b *Builder[T, Q]) Create(values Values) (T, error) {
	return create[T](b.spec, values, "")
}

// Insert inserts the rows in one statement and reads nothing back. Every row
// carries the same columns; a model's casts apply and its timestamps are
// stamped.
func (b *Builder[T, Q]) Insert(rows ...Values) error {
	_, err := b.spec.insert(rows, "")

	return err
}

// InsertOrIgnore inserts the rows, skipping any a unique index already
// holds, and returns how many it inserted. It is the race-safe idempotent
// insert: a read-then-insert lets two requests both see the row missing.
func (b *Builder[T, Q]) InsertOrIgnore(rows ...Values) (int64, error) {
	return b.spec.insert(rows, " on conflict do nothing")
}

// UpdateOrCreate updates the row matching attributes with values, or creates
// it from both, and returns it as stored. It is one atomic statement
// (INSERT ... ON CONFLICT DO UPDATE), so the attribute columns need a unique
// index of their own; an update keeps created_at and touches updated_at.
func (b *Builder[T, Q]) UpdateOrCreate(attributes Values, values Values) (T, error) {
	row := Values{}
	maps.Copy(row, attributes)
	maps.Copy(row, values)

	keys := slices.Sorted(maps.Keys(attributes))
	update := slices.Sorted(maps.Keys(values))
	if len(update) == 0 {
		// DO NOTHING returns no row, so with no values a conflict rewrites
		// the attributes with themselves to read the stored row back.
		update = keys
	}

	return create[T](b.spec, row, b.spec.onConflict(keys, update))
}

// Upsert inserts the rows and updates the update columns of any that
// conflict over uniqueBy, in one statement; with no update columns a
// conflicting row is left as stored. Rows are positional (pgx.CopyFrom's
// shape), so a bulk load holds no map per row.
func (b *Builder[T, Q]) Upsert(columns []string, rows [][]any, uniqueBy []string, update []string) error {
	return b.spec.upsert(columns, rows, uniqueBy, update)
}

// Update sets values on every matching row and returns how many changed.
// The count is how an atomic claim (`WHERE claimed_at IS NULL`) knows it won
// the race.
func (b *Builder[T, Q]) Update(values Values) (int64, error) {
	return b.spec.update(values)
}

// UpdateChanged is Update for the matching rows where some value differs
// from what is stored, returning them as stored: a write that changes
// nothing isn't made and leaves updated_at alone. Values compare as their
// column's type; an encrypted value always differs (each seal is new). A
// db.Table query needs a struct or map[string]any row type.
func (b *Builder[T, Q]) UpdateChanged(values Values) ([]T, error) {
	return updateChanged[T](b.spec.clone(), values)
}

// WithoutTimestamps stops this query's writes stamping the model's
// timestamps, for a write that isn't a change to the row's content.
func (b *Builder[T, Q]) WithoutTimestamps() Q {
	b.spec.withoutTimestamps = true

	return b.self
}

// Delete removes every matching row (every row, with no wheres) and returns
// how many. With Limit or a join it goes through a ctid subquery: Postgres
// has no DELETE ... LIMIT.
func (b *Builder[T, Q]) Delete() (int64, error) {
	return b.spec.delete()
}

// ToSQL compiles the SELECT and its bindings, for debugging and tests.
func (b *Builder[T, Q]) ToSQL() (sql string, args []any) {
	return b.spec.toSQL()
}
