package rule

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/atobeach/reef/db"
)

// Exists requires a row of M whose column holds the value, queried on the
// request's connection.
func Exists[M db.Model](column string) Rule {
	table := tableOf[M]()

	return Rule{
		Name: "exists",
		query: func(f *Field) (bool, error) {
			value := scalarString(f.Value)
			if !storable(value) {
				return false, nil
			}

			return db.Table[int](f.Ctx, table).Where(column, value).Exists()
		},
		message: fixed("The selected :attribute is invalid."),
	}
}

// Unique starts a rule requiring that no row of M holds the value in the
// column; chain Ignore to skip the row being updated, then finish with Rule.
func Unique[M db.Model](column string) UniqueRule {
	return UniqueRule{table: tableOf[M](), column: column}
}

func tableOf[M db.Model]() string {
	var model M

	return model.TableName()
}

// UniqueRule builds a unique rule.
type UniqueRule struct {
	table   string
	column  string
	ignored func(ctx context.Context) *int64
}

// Ignore excludes the row whose primary key id reads from the request's
// context, so a user keeping their own email doesn't collide with
// themselves: Ignore(auth.ID). A nil id excludes nothing.
func (u UniqueRule) Ignore(id func(ctx context.Context) *int64) UniqueRule {
	u.ignored = id

	return u
}

// Rule finishes the builder.
func (u UniqueRule) Rule() Rule {
	return Rule{
		Name: "unique",
		query: func(f *Field) (bool, error) {
			value := scalarString(f.Value)
			if !storable(value) {
				// No row can hold it, so it isn't taken; the attribute's
				// other rules are what reject it.
				return true, nil
			}

			query := db.Table[int](f.Ctx, u.table).Where(u.column, value)
			if u.ignored != nil {
				if id := u.ignored(f.Ctx); id != nil {
					query = query.WhereOp("id", "<>", *id)
				}
			}

			taken, err := query.Exists()
			if err != nil {
				return false, err
			}

			return !taken, nil
		},
		message: fixed("The :attribute has already been taken."),
	}
}

// storable reports whether a text column could hold value: Postgres refuses
// a NUL byte or invalid UTF-8 (SQLSTATE 22021), turning junk input into a 500.
func storable(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
