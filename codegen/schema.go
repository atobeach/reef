// Package codegen generates model code from a migrated Postgres schema and
// the spec's casts and timestamps. The columns are discovered once at build
// time, so the compiler checks every field access.
package codegen

import (
	"context"
	"fmt"

	"github.com/atobeach/reef/db"
)

// Column is one table column as the model generator reads it from the
// schema.
type Column struct {
	Name     string
	UDT      string // Postgres type name: int8, varchar, timestamptz, jsonb, ...
	Nullable bool
}

// ForeignKey is a single-column FK: Table.Column references RefTable.RefColumn.
type ForeignKey struct {
	Table     string
	Column    string
	RefTable  string
	RefColumn string
}

// Schema is the introspected public schema.
type Schema struct {
	Columns     map[string][]Column // table → columns in ordinal order
	ForeignKeys []ForeignKey
}

type columnRow struct {
	Table    string `db:"table_name"`
	Column   string `db:"column_name"`
	UDT      string `db:"udt_name"`
	Nullable string `db:"is_nullable"`
}

type foreignKeyRow struct {
	Table     string `db:"table_name"`
	Column    string `db:"column_name"`
	RefTable  string `db:"ref_table"`
	RefColumn string `db:"ref_column"`
}

// Introspect reads the public schema over the connection bound to ctx.
func Introspect(ctx context.Context) (Schema, error) {
	columns, err := db.Table[columnRow](ctx, "information_schema.columns").
		Select("table_name", "column_name", "udt_name", "is_nullable").
		Where("table_schema", "public").
		OrderBy("table_name").
		OrderBy("ordinal_position").
		Get()
	if err != nil {
		return Schema{}, fmt.Errorf("codegen: columns: %w", err)
	}

	// pg_constraint rather than information_schema: the latter's
	// constraint_column_usage can't pair columns of composite keys.
	foreignKeys, err := db.Table[foreignKeyRow](ctx, "pg_constraint").
		SelectRaw("src.relname AS table_name, a.attname AS column_name, ref.relname AS ref_table, ra.attname AS ref_column").
		Join("pg_class AS src", "src.oid", "=", "pg_constraint.conrelid").
		Join("pg_class AS ref", "ref.oid", "=", "pg_constraint.confrelid").
		Join("pg_namespace AS ns", "ns.oid", "=", "src.relnamespace").
		Join("pg_attribute AS a", "a.attrelid", "=", "pg_constraint.conrelid").
		Join("pg_attribute AS ra", "ra.attrelid", "=", "pg_constraint.confrelid").
		Where("pg_constraint.contype", "f").
		Where("ns.nspname", "public").
		WhereRaw("cardinality(pg_constraint.conkey) = 1").
		WhereRaw("a.attnum = pg_constraint.conkey[1] AND ra.attnum = pg_constraint.confkey[1]").
		OrderBy("table_name").
		OrderBy("column_name").
		Get()
	if err != nil {
		return Schema{}, fmt.Errorf("codegen: foreign keys: %w", err)
	}

	schema := Schema{Columns: map[string][]Column{}}
	for _, row := range columns {
		schema.Columns[row.Table] = append(schema.Columns[row.Table], Column{Name: row.Column, UDT: row.UDT, Nullable: row.Nullable == "YES"})
	}

	for _, row := range foreignKeys {
		schema.ForeignKeys = append(schema.ForeignKeys, ForeignKey(row))
	}

	return schema, nil
}
