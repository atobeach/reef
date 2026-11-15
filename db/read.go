package db

// Get runs the query, scans every row into T and runs the eager loads.
func (b *Builder[T, Q]) Get() ([]T, error) {
	rows, err := get[T](b.spec)
	if err != nil {
		return nil, err
	}

	if err := b.load(rows); err != nil {
		return nil, err
	}

	return rows, nil
}

// GetAs runs the query and scans every row into R instead of the model: for
// a partial select or a join the model doesn't describe. A field R declares
// needs a column in the result, or GetAs fails before reading a row; a field
// an embedded model brings reads as Get reads the model, and an extra column
// is skipped. It panics when the query eager-loads: R has no relations to
// fill.
func (b *Builder[T, Q]) GetAs[R any]() ([]R, error) {
	b.refuseEager("GetAs")

	return getAs[R](b.spec)
}

// Pluck selects one column and returns its value from every row.
func (b *Builder[T, Q]) Pluck[V any](column string) ([]V, error) {
	b.refuseEager("Pluck")

	s := b.spec.clone()
	s.selects = []string{wrap(column)}

	return pluck[V](s)
}

// PluckBy selects two columns and returns a key => value map, the key column
// named first as the map reads; a later row overwrites an earlier one's key.
func (b *Builder[T, Q]) PluckBy[K comparable, V any](key string, value string) (map[K]V, error) {
	b.refuseEager("PluckBy")

	s := b.spec.clone()
	s.selects = []string{wrap(key), wrap(value)}

	return pluckBy[K, V](s)
}

// Value returns one column of the first row, or nil when there is none.
func (b *Builder[T, Q]) Value[V any](column string) (*V, error) {
	b.refuseEager("Value")

	s := b.spec.clone()
	s.selects = []string{wrap(column)}

	return value[V](s)
}

// First returns the first row, or nil when there is none.
func (b *Builder[T, Q]) First() (*T, error) {
	return b.first(b.spec.clone())
}

// FirstOrFail returns the first row, or a ModelNotFoundError (rendered as a 404).
func (b *Builder[T, Q]) FirstOrFail() (T, error) {
	return b.firstOrFail(b.spec.clone())
}

// Find returns the row with this primary key, or nil.
func (b *Builder[T, Q]) Find(id any) (*T, error) {
	s := b.spec.clone()
	s.whereKey(id)

	return b.first(s)
}

// FindOrFail returns the row with this primary key, or a ModelNotFoundError.
func (b *Builder[T, Q]) FindOrFail(id any) (T, error) {
	s := b.spec.clone()
	s.whereKey(id)

	return b.firstOrFail(s)
}

// first is First on s, a copy of the query's spec.
func (b *Builder[T, Q]) first(s *spec) (*T, error) {
	s.limit = 1

	rows, err := get[T](s)
	if err != nil {
		return nil, err
	}

	if err := b.load(rows); err != nil {
		return nil, err
	}

	return head(rows, nil)
}

// firstOrFail is FirstOrFail on s, a copy of the query's spec.
func (b *Builder[T, Q]) firstOrFail(s *spec) (T, error) {
	var zero T

	row, err := b.first(s)
	if err != nil {
		return zero, err
	}

	if row == nil {
		return zero, &ModelNotFoundError{Model: s.model}
	}

	return *row, nil
}

// Count returns the number of matching rows, ignoring order and limit.
func (b *Builder[T, Q]) Count() (int64, error) {
	return b.spec.count()
}

// Exists reports whether any row matches.
func (b *Builder[T, Q]) Exists() (bool, error) {
	return b.spec.exists()
}

// DoesntExist reports whether no row matches.
func (b *Builder[T, Q]) DoesntExist() (bool, error) {
	exists, err := b.spec.exists()
	if err != nil {
		return false, err
	}

	return !exists, nil
}
