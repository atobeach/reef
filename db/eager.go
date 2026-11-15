package db

import (
	"context"
	"fmt"
	"reflect"
)

// Eager declares a load run on the rows every read of the model returns
// (Get, First, FirstOrFail, Find, FindOrFail) and on the rows given to Load.
// The generated With<Relation> methods declare theirs through it.
func (b *Builder[T, Q]) Eager(load func(ctx context.Context, rows []T) error) Q {
	b.eager = append(b.eager, load)

	return b.self
}

// Load runs the declared eager loads on rows read elsewhere (a cache, an
// earlier query), filling their Loaded relations in place:
//
//	err := models.Airports(ctx).WithCountry().WithCity().Load(airports)
//
// It panics on a query that selects, joins, filters, orders, limits or
// locks: Load reads no rows of its own, so none of that would apply.
func (b *Builder[T, Q]) Load(rows []T) error {
	if b.spec.shaped() {
		panic("db: Load only runs eager loads; select, join, filter, order and limit the query that reads the rows")
	}

	return b.load(rows)
}

func (b *Builder[T, Q]) load(rows []T) error {
	if len(rows) == 0 {
		return nil
	}

	for _, load := range b.eager {
		if err := load(b.spec.ctx, rows); err != nil {
			return err
		}
	}

	return nil
}

// refuseEager panics when a read that doesn't return the model is asked to
// eager-load: there is no Loaded to fill.
func (b *Builder[T, Q]) refuseEager(read string) {
	if len(b.eager) > 0 {
		panic("db: " + read + " doesn't read the model, so it can't eager-load relations; read them with Get")
	}
}

// related is a model's query: every one embeds Builder, whose base method
// reaches it.
type related[P, Q any] interface {
	base() *Builder[P, Q]
}

func (b *Builder[T, Q]) base() *Builder[T, Q] {
	return b
}

// BelongsTo is the eager load a generated With<Relation> declares: relation
// names a row's foreign key and the slot its parent fills. The parents are
// read in one scoped query over the rows' distinct keys; a nil key, or one
// naming no row, gets a nil parent.
func BelongsTo[T, P any, Q related[P, Q], K comparable](query func(ctx context.Context) Q, scopes []func(q Q), relation func(row *T) (key *K, parent **P)) func(ctx context.Context, rows []T) error {
	return func(ctx context.Context, rows []T) error {
		var keys []K
		seen := map[K]bool{}
		for i := range rows {
			if key, _ := relation(&rows[i]); key != nil && !seen[*key] {
				seen[*key] = true
				keys = append(keys, *key)
			}
		}

		parents, err := parentsByKey(ctx, query, scopes, keys)
		if err != nil {
			return err
		}

		for i := range rows {
			key, parent := relation(&rows[i])

			*parent = nil
			if key != nil {
				*parent = parents[*key]
			}
		}

		return nil
	}
}

// parentsByKey reads the parents keys name, by key; no keys reads nothing.
func parentsByKey[P any, Q related[P, Q], K comparable](ctx context.Context, query func(ctx context.Context) Q, scopes []func(q Q), keys []K) (map[K]*P, error) {
	byKey := make(map[K]*P, len(keys))
	if len(keys) == 0 {
		return byKey, nil
	}

	q := query(ctx)
	for _, scope := range scopes {
		scope(q)
	}

	b := q.base()
	b.spec.selectKey()
	b.WhereIn(b.spec.table+".id", keys)

	parents, err := b.Get()
	if err != nil {
		return nil, fmt.Errorf("db: eager-loading %s: %w", b.spec.model, err)
	}

	id, keyType := keyField[P](), reflect.TypeFor[K]()
	for i := range parents {
		key := reflect.ValueOf(&parents[i]).Elem().FieldByIndex(id).Convert(keyType).Interface().(K)
		byKey[key] = &parents[i]
	}

	return byKey, nil
}

// keyField is the index path of P's id field, which its children's foreign
// keys name.
func keyField[P any]() []int {
	t := reflect.TypeFor[P]()

	path, ok := fieldsOf(t)["id"]
	if !ok {
		panic(fmt.Sprintf("db: %s has no id field to eager-load it by", t))
	}

	return path
}

// shaped reports whether the query selects, joins, filters, orders, limits,
// offsets or locks.
func (s *spec) shaped() bool {
	return len(s.selects) > 0 || len(s.joins) > 0 || len(s.wheres) > 0 || len(s.orders) > 0 ||
		s.limit > 0 || s.offset > 0 || s.lockForUpdate
}

// selectKey adds the table's id to a narrowed select that leaves it out, so
// eager-loaded rows can be matched by it.
func (s *spec) selectKey() {
	if len(s.selects) == 0 {
		return
	}

	key := wrap(s.table + ".id")
	for _, column := range s.selects {
		if column == key || column == wrap("id") || column == "*" || column == wrap(s.table)+".*" {
			return
		}
	}

	s.selects = append(s.selects, key)
}
