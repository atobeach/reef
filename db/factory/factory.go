// Package factory makes model rows for tests: each model's factory has a
// definition that makes a valid row, named states that change it, and
// Create, which takes the test's own overrides typed against the model.
//
//	type CityFactory struct {
//		factory.Builder[models.City, *CityFactory]
//	}
//
//	func City(tc factory.TestCase) *CityFactory {
//		return factory.New(tc, &CityFactory{}, func(context.Context) models.City {
//			return models.City{Name: "Valencia", Timezone: "Europe/Madrid"}
//		}).AfterMaking(func(c *models.City) {
//			if c.CountryID == 0 {
//				c.CountryID = Country(tc).Create().ID
//			}
//		})
//	}
//
//	func (f *CityFactory) Published() *CityFactory {
//		return f.State(func(c *models.City) { c.PublishedAt = new(clock.Now(f.Context())) })
//	}
//
//	valencia := factories.City(tc).Published().Create(func(c *models.City) { c.Slug = new("valencia") })
package factory

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/atobeach/reef/db"
)

// TestCase is the test a factory creates rows for: T fails it, and Context
// carries the connection (the test's transaction) and the clock rows are
// written with.
type TestCase interface {
	T() testing.TB
	Context() context.Context
}

// Builder makes rows of model T. F is the concrete factory type its chain
// methods return, so a factory's own states chain with each other and with
// the builder's methods.
//
// Every chain method returns a new factory and leaves the one it was called
// on as it was, so one preset serves every variant made from it:
//
//	trips := factories.Search(tc).For(user)
//	trips.Paid().Create() // paid
//	trips.Create()        // still unpaid
type Builder[T db.Model, F any] struct {
	tc                TestCase
	definition        func(ctx context.Context) T
	states            []func(*T)
	afterMaking       []func(*T)
	withoutTimestamps bool
	self              F
	// derive copies the factory self is around next: how each chain call
	// returns a new factory.
	derive func(next Builder[T, F]) F
}

// factory is satisfied by any struct embedding Builder[T, *F]: the embedded
// builder method is promoted to it, which is how New reaches the builder.
type factory[T db.Model, F any] interface {
	*F
	builder() *Builder[T, *F]
}

func (b *Builder[T, F]) builder() *Builder[T, F] {
	return b
}

// New wires a model's factory type (a struct embedding Builder) to tc and
// the definition of a row; each row runs the definition afresh.
func New[T db.Model, F any, P factory[T, F]](tc TestCase, f P, definition func(ctx context.Context) T) P {
	b := f.builder()
	b.tc, b.definition, b.self = tc, definition, f
	b.derive = func(next Builder[T, *F]) *F {
		derived := new(F)
		*derived = *next.self
		next.self = derived
		*P(derived).builder() = next

		return derived
	}

	return f
}

// with is a new factory: this one with change made to its builder. The
// slices are clipped so the new factory's appends never reach this one's.
func (b *Builder[T, F]) with(change func(next *Builder[T, F])) F {
	next := *b
	next.states, next.afterMaking = slices.Clip(b.states), slices.Clip(b.afterMaking)
	change(&next)

	return b.derive(next)
}

// State is this factory with one more change every row gets over the
// definition, after the states before it; a factory's named states are made
// of it.
func (b *Builder[T, F]) State(state func(*T)) F {
	return b.with(func(next *Builder[T, F]) { next.states = append(next.states, state) })
}

// AfterMaking is this factory with a change every row gets once its states
// and overrides are in, just before the insert: where a factory makes a
// required parent the test left unset.
func (b *Builder[T, F]) AfterMaking(callback func(*T)) F {
	return b.with(func(next *Builder[T, F]) { next.afterMaking = append(next.afterMaking, callback) })
}

// WithoutTimestamps is this factory with Create no longer stamping the
// model's timestamps: the row's own are written as made, a nil one as NULL
// (a row never updated).
func (b *Builder[T, F]) WithoutTimestamps() F {
	return b.with(func(next *Builder[T, F]) { next.withoutTimestamps = true })
}

// Context is the test's context, for a state that reads its clock.
func (b *Builder[T, F]) Context() context.Context {
	return b.tc.Context()
}

// Count makes Create insert n rows.
//
//	factories.Search(tc).For(user).Paid().Count(3).Create()
func (b *Builder[T, F]) Count(n int) *Many[T] {
	return &Many[T]{tc: b.tc, n: n, create: b.Create}
}

// Create inserts a row and returns it as stored: the definition, then the
// states, overrides and AfterMaking changes in order. Every column is
// written, a Go zero value included, never the table's default; only an
// unset id and unset timestamps are left to the insert.
func (b *Builder[T, F]) Create(overrides ...func(*T)) T {
	t := b.tc.T()
	t.Helper()

	row := b.definition(b.tc.Context())
	for _, change := range slices.Concat(b.states, overrides, b.afterMaking) {
		change(&row)
	}

	query := db.NewQuery[T](b.tc.Context(), &db.Rows[T]{})
	if b.withoutTimestamps {
		query = query.WithoutTimestamps()
	}

	created, err := query.Create(columns(row, !b.withoutTimestamps))
	if err != nil {
		t.Fatalf("factory %T: %v", row, err)
	}

	return created
}

// Many is a factory set to create n rows; Count makes one.
type Many[T db.Model] struct {
	tc     TestCase
	n      int
	create func(overrides ...func(*T)) T
}

// Create inserts the rows, each as the factory's Create inserts one.
func (m *Many[T]) Create(overrides ...func(*T)) []T {
	m.tc.T().Helper()

	rows := make([]T, m.n)
	for i := range rows {
		rows[i] = m.create(overrides...)
	}

	return rows
}

// columns is row as its insert writes it: each `db`-tagged field but an
// unset id, or an unset timestamp when the insert stamps them.
func columns[T db.Model](row T, stamping bool) db.Values {
	schema := row.Schema()
	values := db.Values{}

	for field, value := range reflect.ValueOf(row).Fields() {
		column, tagged := field.Tag.Lookup("db")
		if !tagged || column == "-" {
			continue
		}

		stamped := stamping && schema != nil && (column == schema.CreatedAt || column == schema.UpdatedAt)
		if value.IsZero() && (column == "id" || stamped) {
			continue
		}

		values[column] = value.Interface()
	}

	return values
}
