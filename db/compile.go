package db

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/atobeach/reef/collect"
)

// toSQL compiles the SELECT the builder describes.
func (s *spec) toSQL() (sql string, args []any) {
	columns := "*"
	if len(s.selects) > 0 {
		columns = strings.Join(s.selects, ", ")
	}

	sql, args = s.compile(columns, true)
	if s.lockForUpdate {
		sql += " for update"
	}

	return sql, args
}

// compile renders a SELECT of columns. COUNT leaves ordering, limit and
// offset off; the chunked DELETE subquery keeps them.
func (s *spec) compile(columns string, withOrdering bool) (sql string, args []any) {
	var query strings.Builder

	query.WriteString("select " + columns + " from " + wrap(s.table))
	for _, join := range s.joins {
		query.WriteString(" " + join)
	}

	if len(s.wheres) > 0 {
		query.WriteString(" where " + strings.Join(s.wheres, " and "))
	}

	if withOrdering {
		if len(s.orders) > 0 {
			query.WriteString(" order by " + strings.Join(s.orders, ", "))
		}

		if s.limit > 0 {
			query.WriteString(" limit " + strconv.Itoa(s.limit))
		}

		if s.offset > 0 {
			query.WriteString(" offset " + strconv.Itoa(s.offset))
		}
	}

	return query.String(), s.args
}

// clone is a copy of s for a terminal read to narrow, its slices copied so an
// append to the copy never writes into s's backing arrays.
func (s *spec) clone() *spec {
	c := *s
	c.selects = slices.Clone(s.selects)
	c.joins = slices.Clone(s.joins)
	c.wheres = slices.Clone(s.wheres)
	c.orders = slices.Clone(s.orders)
	c.args = slices.Clone(s.args)

	return &c
}

// whereOp adds `column <operator> value`.
func (s *spec) whereOp(column string, op string, value any) {
	s.wheres = append(s.wheres, fmt.Sprintf("%s %s %s", wrap(column), operator(op), s.placeholder(value)))
}

// whereKey filters on the table-qualified primary key.
func (s *spec) whereKey(id any) {
	s.whereOp(s.table+".id", "=", id)
}

// placeholder binds value and returns its `$n`.
func (s *spec) placeholder(value any) string {
	s.args = append(s.args, bindable(value))

	return "$" + strconv.Itoa(len(s.args))
}

// bind swaps each `?` in a raw fragment for the next `$n` placeholder.
func (s *spec) bind(fragment string, bindings []any) string {
	var out strings.Builder

	next := 0
	for i := 0; i < len(fragment); i++ {
		if fragment[i] != '?' {
			out.WriteByte(fragment[i])

			continue
		}

		if i+1 < len(fragment) && fragment[i+1] == '?' {
			out.WriteByte('?')
			i++

			continue
		}

		if next >= len(bindings) {
			panic(fmt.Sprintf("db: %q has more ? placeholders than bindings", fragment))
		}

		out.WriteString(s.placeholder(bindings[next]))
		next++
	}

	if next < len(bindings) {
		panic(fmt.Sprintf("db: %q has fewer ? placeholders than bindings", fragment))
	}

	return out.String()
}

// group runs scope and takes back the conditions it added, ANDed into one
// parenthesised condition for WhereAny.
func (s *spec) group(scope func()) string {
	before := *s
	scope()

	added := s.wheres[len(before.wheres):]
	if len(added) == 0 {
		panic("db: a WhereAny scope must add a condition")
	}

	if !slices.Equal(s.selects, before.selects) || !slices.Equal(s.joins, before.joins) || !slices.Equal(s.orders, before.orders) ||
		s.limit != before.limit || s.offset != before.offset ||
		s.lockForUpdate != before.lockForUpdate || s.withoutTimestamps != before.withoutTimestamps {
		panic("db: a WhereAny scope may only add conditions")
	}

	condition := "(" + strings.Join(added, " and ") + ")"
	s.wheres = s.wheres[:len(before.wheres)]

	return condition
}

// renumber shifts every `$n` in sql by offset.
func renumber(sql string, offset int) string {
	var out strings.Builder

	for i := 0; i < len(sql); i++ {
		if sql[i] != '$' {
			out.WriteByte(sql[i])

			continue
		}

		j := i + 1
		for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
			j++
		}

		if j == i+1 {
			out.WriteByte('$')

			continue
		}

		n, _ := strconv.Atoi(sql[i+1 : j])
		out.WriteString("$" + strconv.Itoa(n+offset))
		i = j - 1
	}

	return out.String()
}

// wrap double-quotes a table or column name ("cities.name" →
// "cities"."name"). A name has up to three parts, schema.table.column, so the
// builder's own qualified columns (WhereKey's table.id) hold for a
// schema-qualified table too. A column argument is code, never SQL, so
// anything else panics where the query is built: an expression goes through
// SelectRaw, WhereRaw or OrderByRaw.
func wrap(name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		if i > 2 || !isIdentifier(part) {
			panic(notAName(name))
		}

		parts[i] = quote(part)
	}

	return strings.Join(parts, ".")
}

// wrapAliased wraps a joined table or a select entry that may carry an
// alias: "pg_class AS src" → "pg_class" as "src".
func wrapAliased(name string) string {
	before, alias, ok := cutFold(name, " as ")
	if !ok {
		return wrap(name)
	}

	alias = strings.TrimSpace(alias)
	if !isIdentifier(alias) {
		panic(notAName(name))
	}

	return wrap(strings.TrimSpace(before)) + " as " + quote(alias)
}

// notAName is the panic for a column argument that isn't a name.
func notAName(name string) string {
	return fmt.Sprintf("db: %q is not a table or column name; write an expression with SelectRaw, WhereRaw or OrderByRaw", name)
}

// wrapSelect wraps a select entry: `*`, `table.*`, or a column with an
// optional alias.
func wrapSelect(entry string) string {
	if entry == "*" {
		return entry
	}

	if table, ok := strings.CutSuffix(entry, ".*"); ok {
		return wrap(table) + ".*"
	}

	return wrapAliased(entry)
}

// wrapAll wraps each name into a comma-separated list.
func wrapAll(names []string) string {
	return strings.Join(collect.Map(names, wrap), ", ")
}

// quote double-quotes a checked name, which holds no quote to escape.
func quote(name string) string {
	return `"` + name + `"`
}

// operator is a comparison WhereOp and Join accept, lower-cased; anything
// else panics, as a column argument that isn't a name does.
func operator(op string) string {
	switch lower := strings.ToLower(op); lower {
	case "=", "<>", "!=", "<", "<=", ">", ">=", "like", "not like", "ilike", "not ilike":
		return lower
	}

	panic(fmt.Sprintf("db: %q is not an operator WhereOp or Join takes (=, <>, !=, <, <=, >, >=, like, not like, ilike, not ilike); write the condition with WhereRaw", op))
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}

	return true
}

// cutFold is strings.Cut with a case-insensitive match; sep is lower case.
func cutFold(s string, sep string) (before, after string, found bool) {
	index := strings.Index(strings.ToLower(s), sep)
	if index < 0 {
		return s, "", false
	}

	return s[:index], s[index+len(sep):], true
}
