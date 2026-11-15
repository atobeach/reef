package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Spec declares which tables become models (database/models.json). Only
// listed tables are generated, and relations are generated only between
// listed tables, so framework tables (jobs, cache, migrations) stay out.
type Spec struct {
	Package string `json:"package"`
	// Generator is the command the generated-code marker names
	// ("go generate ./app/models").
	Generator string `json:"generator"`
	// LocalImports is the import-path prefix grouped after third-party
	// imports, as goimports' local-prefixes does.
	LocalImports string `json:"localImports"`
	// JSONEncoder is the func json casts encode with, as "import/path.Func"
	// (encoding/json.Marshal when unset): the spelling stored rows have.
	JSONEncoder string               `json:"jsonEncoder,omitempty"`
	Models      map[string]ModelSpec `json:"models"` // table → model
}

// ModelSpec names one model and declares its casts, timestamps and route key.
type ModelSpec struct {
	Name string `json:"name"`
	// Casts maps a column to its cast: "encrypted" (text sealed at rest),
	// "json" (a value written is stored encoded), "json:<Type>" (json, the
	// field typed as an exported type of the models package that decodes the
	// column), or "encrypted:json" (sealed and encoded).
	Casts map[string]string `json:"casts,omitempty"`
	// Timestamps false stops writes stamping created_at/updated_at.
	Timestamps *bool `json:"timestamps,omitempty"`
	// RouteKey is the column a route parameter names a row by (id when
	// unset): an integer, uuid or text column.
	RouteKey string `json:"routeKey,omitempty"`
}

// The casts a spec may declare; castTypedJSON prefixes a json cast's type.
const (
	castEncrypted     = "encrypted"
	castJSON          = "json"
	castTypedJSON     = "json:"
	castEncryptedJSON = "encrypted:json"
)

// LoadSpec reads a spec file.
func LoadSpec(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}

	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Spec{}, fmt.Errorf("codegen: %s: %w", path, err)
	}

	return spec, nil
}

// Generate renders one `<model>_gen.go` file per spec'd table, keyed by file name.
func Generate(spec Spec, schema Schema) (map[string][]byte, error) {
	files := map[string][]byte{}

	for _, table := range slices.Sorted(maps.Keys(spec.Models)) {
		model := spec.Models[table]

		columns, ok := schema.Columns[table]
		if !ok {
			return nil, fmt.Errorf("codegen: table %q is in the spec but not the schema", table)
		}

		source, err := render(spec, table, model, columns, schema.ForeignKeys)
		if err != nil {
			return nil, err
		}

		files[snake(model.Name)+"_gen.go"] = source
	}

	return files, nil
}

type field struct {
	Name, Type, Column, UDT, Cast string
}

type relation struct {
	Method, Model, Query, Constructor, Column, LocalField string
	// KeyType is a belongs-to key's Go type, without the pointer a nullable
	// key has.
	KeyType  string
	Nullable bool
}

// render writes one model's `_gen.go` source: imports, the row struct, its
// schema, column identifiers, query type and constructor, the row's own
// writes when it is keyed by a non-null id, then the relation methods.
func render(spec Spec, table string, model ModelSpec, columns []Column, foreignKeys []ForeignKey) ([]byte, error) {
	fields, fieldType, imports, err := modelFields(spec, table, model, columns)
	if err != nil {
		return nil, err
	}

	belongsTo, hasMany := relations(spec, table, foreignKeys, fieldType)
	if err := checkCollisions(table, fields, append(slices.Clone(belongsTo), hasMany...)); err != nil {
		return nil, err
	}

	keyed := keyedByID(fields)
	if keyed {
		imports["fmt"] = true
	}

	var out bytes.Buffer
	writeHeader(&out, spec, imports)
	writeModel(&out, table, model, fields, belongsTo)
	if keyed {
		if err := writeRowMethods(&out, model.Name, len(belongsTo) > 0); err != nil {
			return nil, fmt.Errorf("codegen: %s: %w", table, err)
		}
	}
	writeRelations(&out, model.Name, belongsTo, hasMany)

	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("codegen: formatting %s: %w\n%s", table, err, out.String())
	}

	return formatted, nil
}

// modelFields maps the columns to struct fields, collecting the Go type per
// column (relations read nullability off it) and the imports the types and
// casts need.
func modelFields(spec Spec, table string, model ModelSpec, columns []Column) (fields []field, fieldType map[string]string, imports map[string]bool, err error) {
	imports = map[string]bool{"context": true, "github.com/atobeach/reef/db": true}

	known := map[string]bool{}
	fields = make([]field, 0, len(columns))
	fieldType = map[string]string{}
	for _, column := range columns {
		known[column.Name] = true

		cast := model.Casts[column.Name]
		goType, needs, err := goTypeFor(column, cast)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("codegen: %s.%s: %w", table, column.Name, err)
		}

		for _, path := range needs {
			imports[path] = true
		}

		castExpr, castImport, err := castFor(spec, cast)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("codegen: %s.%s: %w", table, column.Name, err)
		}

		if castImport != "" {
			imports[castImport] = true
		}

		fields = append(fields, field{Name: pascal(column.Name), Type: goType, Column: column.Name, UDT: column.UDT, Cast: castExpr})
		fieldType[column.Name] = goType
	}

	for _, column := range slices.Sorted(maps.Keys(model.Casts)) {
		if !known[column] {
			return nil, nil, nil, fmt.Errorf("codegen: %s.%s: a cast on a column the table doesn't have", table, column)
		}
	}

	if err := checkRouteKey(model.RouteKey, columns); err != nil {
		return nil, nil, nil, fmt.Errorf("codegen: %s: %w", table, err)
	}

	return fields, fieldType, imports, nil
}

// checkRouteKey refuses a route key the table doesn't have, or one of a type
// a route parameter can't be checked against before querying.
func checkRouteKey(routeKey string, columns []Column) error {
	if routeKey == "" {
		return nil
	}

	for _, column := range columns {
		if column.Name != routeKey {
			continue
		}

		if !slices.Contains([]string{"int2", "int4", "int8", "uuid", "text", "varchar", "bpchar"}, column.UDT) {
			return fmt.Errorf("route key %s is Postgres %q; use an integer, uuid or text column", routeKey, column.UDT)
		}

		return nil
	}

	return fmt.Errorf("route key %s is not a column of the table", routeKey)
}

// castFor is the db.Cast expression a declared cast generates, and the
// import its JSON encoder needs.
func castFor(spec Spec, cast string) (expr string, importPath string, err error) {
	if cast == castEncrypted {
		return "db.Encrypted", "", nil
	}

	if strings.HasPrefix(cast, castTypedJSON) {
		cast = castJSON
	}

	if cast != castJSON && cast != castEncryptedJSON {
		return "", "", nil
	}

	encoder := spec.JSONEncoder
	if encoder == "" {
		encoder = "encoding/json.Marshal"
	}

	dot := strings.LastIndex(encoder, ".")
	if dot < 0 || strings.Contains(encoder[dot:], "/") {
		return "", "", fmt.Errorf("jsonEncoder %q is not an import/path.Func", encoder)
	}

	importPath = encoder[:dot]
	call := importPath[strings.LastIndex(importPath, "/")+1:] + encoder[dot:]
	if cast == castJSON {
		return "db.JSON(" + call + ")", importPath, nil
	}

	return "db.EncryptedJSON(" + call + ")", importPath, nil
}

// checkCollisions refuses a field or relation method named like another
// field or method, which would not compile. Loaded is reserved for the
// eager-loaded relations, Update, Refresh and Delete for the row's writes.
func checkCollisions(table string, fields []field, relations []relation) error {
	taken := map[string]bool{"TableName": true, "Schema": true, "Loaded": true, "Update": true, "Refresh": true, "Delete": true}
	for _, f := range fields {
		if taken[f.Name] {
			return fmt.Errorf("codegen: %s: column %s collides with the model's %s; rename the column", table, f.Column, f.Name)
		}
		taken[f.Name] = true
	}

	for _, r := range relations {
		if taken[r.Method] {
			return fmt.Errorf("codegen: %s: relation %s collides with a field or method; rename it in the spec", table, r.Method)
		}
		taken[r.Method] = true
	}

	return nil
}

// writeHeader writes the generated-code marker, the package clause and the
// imports: standard library first, then module packages, as gofmt groups them.
func writeHeader(out *bytes.Buffer, spec Spec, imports map[string]bool) {
	fmt.Fprintf(out, "// Code generated by `%s` from the database schema; DO NOT EDIT.\n\npackage %s\n\nimport (\n", spec.Generator, spec.Package)

	paths := slices.Sorted(maps.Keys(imports))
	for _, local := range []bool{false, true} {
		if local {
			out.WriteString("\n")
		}

		for _, path := range paths {
			if strings.HasPrefix(path, spec.LocalImports) == local {
				fmt.Fprintf(out, "\t%q\n", path)
			}
		}
	}
	out.WriteString(")\n\n")
}

// writeModel writes the row struct (with its eager-loaded relations, when
// it belongs to any), TableName, the schema, the column identifiers, the
// query type and its constructor.
func writeModel(out *bytes.Buffer, table string, model ModelSpec, fields []field, belongsTo []relation) {
	name := model.Name
	fmt.Fprintf(out, "// %s is a row of `%s`.\ntype %s struct {\n", name, table, name)
	for _, f := range fields {
		fmt.Fprintf(out, "\t%s %s `db:%q`\n", f.Name, f.Type, f.Column)
	}

	if len(belongsTo) > 0 {
		fmt.Fprintf(out, "\n\t// Loaded holds the relations the query's With methods loaded.\n\tLoaded %sRelations `db:\"-\" json:\"-\"`\n", name)
	}
	out.WriteString("}\n\n")

	if len(belongsTo) > 0 {
		fmt.Fprintf(out, "// %sRelations are the relations %sQuery's With methods load, each nil\n// until loaded or when its key is null or names no row.\ntype %sRelations struct {\n", name, name, name)
		for _, r := range belongsTo {
			fmt.Fprintf(out, "\t%s *%s\n", r.Method, r.Model)
		}
		out.WriteString("}\n\n")
	}

	plural := pluralize(name)
	schema := strings.ToLower(name[:1]) + name[1:] + "Schema"
	fmt.Fprintf(out, "// TableName implements db.Model.\nfunc (%s) TableName() string { return %q }\n\n", name, table)
	fmt.Fprintf(out, "// Schema implements db.Model.\nfunc (%s) Schema() *db.Schema { return &%s }\n\n", name, schema)
	writeSchema(out, table, schema, model, fields)
	writeColumns(out, table, name, fields)
	fmt.Fprintf(out, "// %sQuery queries `%s`; scopes are methods on it.\ntype %sQuery struct {\n\tdb.Builder[%s, *%sQuery]\n}\n\n", name, table, name, name, name)
	fmt.Fprintf(out, "// %s starts a query over `%s`.\nfunc %s(ctx context.Context) *%sQuery {\n\treturn db.NewQuery[%s](ctx, &%sQuery{})\n}\n", plural, table, plural, name, name, name)
}

// writeSchema writes the db.Schema the table's writes and route bindings
// follow: its timestamps (unless the spec turns them off), route key, and
// every column's type and cast.
func writeSchema(out *bytes.Buffer, table string, schema string, model ModelSpec, fields []field) {
	fmt.Fprintf(out, "// %s is how `%s` is written: its timestamps, column types and casts.\nvar %s = db.Schema{\n", schema, table, schema)
	if model.Timestamps == nil || *model.Timestamps {
		for _, f := range fields {
			switch f.Column {
			case "created_at":
				out.WriteString("\tCreatedAt: \"created_at\",\n")
			case "updated_at":
				out.WriteString("\tUpdatedAt: \"updated_at\",\n")
			}
		}
	}

	if model.RouteKey != "" {
		fmt.Fprintf(out, "\tRouteKey: %q,\n", model.RouteKey)
	}

	out.WriteString("\tColumns: map[string]db.Column{\n")
	for _, f := range fields {
		if f.Cast != "" {
			fmt.Fprintf(out, "\t\t%q: {Type: %q, Cast: %s},\n", f.Column, f.UDT, f.Cast)

			continue
		}
		fmt.Fprintf(out, "\t\t%q: {Type: %q},\n", f.Column, f.UDT)
	}
	out.WriteString("\t},\n}\n\n")
}

// writeColumns writes the table's column identifiers: one struct type, a
// var of bare names (a query on the table alone, a write's keys) and a var
// qualified by the table (a scope, a join), so a renamed or dropped column
// breaks the build wherever it is named.
func writeColumns(out *bytes.Buffer, table string, name string, fields []field) {
	columns := strings.ToLower(name[:1]) + name[1:] + "Columns"
	fmt.Fprintf(out, "// %s names each column of `%s`.\ntype %s struct {\n", columns, table, columns)
	for _, f := range fields {
		fmt.Fprintf(out, "\t%s string\n", f.Name)
	}
	out.WriteString("}\n\n")

	fmt.Fprintf(out, "// %sColumns name the columns of `%s`, for a query on the table alone and the keys of a write.\nvar %sColumns = %s{\n", name, table, name, columns)
	for _, f := range fields {
		fmt.Fprintf(out, "\t%s: %q,\n", f.Name, f.Column)
	}
	out.WriteString("}\n\n")

	fmt.Fprintf(out, "// %sTableColumns name the columns of `%s` qualified by the table, for a scope or a join.\nvar %sTableColumns = %s{\n", name, table, name, columns)
	for _, f := range fields {
		fmt.Fprintf(out, "\t%s: %q,\n", f.Name, table+"."+f.Column)
	}
	out.WriteString("}\n\n")
}

// writeRelations writes a query method per relation, and a With method
// eager-loading each belongs-to. A nullable belongs-to with no key matches
// no row, rather than querying `id = NULL`.
func writeRelations(out *bytes.Buffer, name string, belongsTo []relation, hasMany []relation) {
	receiver := strings.ToLower(name[:1])

	for _, r := range belongsTo {
		fmt.Fprintf(out, "\n// %s is the `%s` this row belongs to (via %s).\nfunc (%s %s) %s(ctx context.Context) *%s {\n", r.Method, r.Model, r.Column, receiver, name, r.Method, r.Query)
		key := "&" + receiver + "." + r.LocalField
		if r.Nullable {
			key = receiver + "." + r.LocalField
			fmt.Fprintf(out, "\tif %s.%s == nil {\n\t\treturn %s(ctx).WhereRaw(\"1 = 0\")\n\t}\n\n\treturn %s(ctx).WhereKey(*%s.%s)\n}\n", receiver, r.LocalField, r.Constructor, r.Constructor, receiver, r.LocalField)
		} else {
			fmt.Fprintf(out, "\treturn %s(ctx).WhereKey(%s.%s)\n}\n", r.Constructor, receiver, r.LocalField)
		}

		fmt.Fprintf(out, "\n// With%s eager-loads each row's Loaded.%s in one query; scopes narrow it.\nfunc (q *%sQuery) With%s(scopes ...func(q *%s)) *%sQuery {\n", r.Method, r.Method, name, r.Method, r.Query, name)
		fmt.Fprintf(out, "\treturn q.Eager(db.BelongsTo(%s, scopes, func(%s *%s) (*%s, **%s) { return %s, &%s.Loaded.%s }))\n}\n", r.Constructor, receiver, name, r.KeyType, r.Model, key, receiver, r.Method)
	}

	for _, r := range hasMany {
		fmt.Fprintf(out, "\n// %s are the rows pointing here through %s.\nfunc (%s %s) %s(ctx context.Context) *%s {\n\treturn %s(ctx).Where(%sTableColumns.%s, %s.ID)\n}\n", r.Method, r.Column, receiver, name, r.Method, r.Query, r.Constructor, r.Model, pascal(r.Column), receiver)
	}
}

// relations derives BelongsTo (FKs on this table) and HasMany (FKs pointing
// here) between spec'd tables. A table pointing here more than once gets
// disambiguated names: Airport.FlightsByOriginAirport, Airport.FlightsByDestinationAirport.
func relations(spec Spec, table string, foreignKeys []ForeignKey, fieldType map[string]string) (belongsTo []relation, hasMany []relation) {
	inbound := map[string]int{}
	for _, fk := range foreignKeys {
		if fk.RefTable == table {
			inbound[fk.Table]++
		}
	}

	for _, fk := range foreignKeys {
		if fk.RefColumn != "id" {
			continue
		}

		if fk.Table == table {
			target, ok := spec.Models[fk.RefTable]
			if !ok {
				continue
			}

			belongsTo = append(belongsTo, relation{
				Method:      pascal(strings.TrimSuffix(fk.Column, "_id")),
				Model:       target.Name,
				Query:       target.Name + "Query",
				Constructor: pluralize(target.Name),
				Column:      fk.Column,
				LocalField:  pascal(fk.Column),
				KeyType:     strings.TrimPrefix(fieldType[fk.Column], "*"),
				Nullable:    strings.HasPrefix(fieldType[fk.Column], "*"),
			})
		}

		if fk.RefTable == table {
			source, ok := spec.Models[fk.Table]
			if !ok {
				continue
			}

			method := pluralize(source.Name)
			if inbound[fk.Table] > 1 {
				method += "By" + pascal(strings.TrimSuffix(fk.Column, "_id"))
			}

			hasMany = append(hasMany, relation{
				Method:      method,
				Model:       source.Name,
				Query:       source.Name + "Query",
				Constructor: pluralize(source.Name),
				Column:      fk.Column,
			})
		}
	}

	sort.SliceStable(belongsTo, func(i, j int) bool { return belongsTo[i].Method < belongsTo[j].Method })
	sort.SliceStable(hasMany, func(i, j int) bool { return hasMany[i].Method < hasMany[j].Method })

	return belongsTo, hasMany
}

// goTypeFor maps a Postgres type to Go, given the column's cast. Nullable
// columns become pointers, except json (a nil RawMessage already means NULL).
// An encrypted column holds text; one encrypted as json reads as raw JSON.
// A typed json cast's type is never a pointer: its zero value holds NULL.
func goTypeFor(column Column, cast string) (goType string, imports []string, err error) {
	if typeName, ok := strings.CutPrefix(cast, castTypedJSON); ok {
		return typedJSON(column, typeName)
	}

	switch cast {
	case "":
	case castEncrypted, castEncryptedJSON:
		if column.UDT != "text" && column.UDT != "varchar" {
			return "", nil, fmt.Errorf("an %s cast needs a text column, not Postgres %q", cast, column.UDT)
		}

		if cast == castEncryptedJSON {
			return "json.RawMessage", []string{"encoding/json"}, nil
		}
	case castJSON:
		if column.UDT != "json" && column.UDT != "jsonb" {
			return "", nil, fmt.Errorf("a json cast needs a json column, not Postgres %q", column.UDT)
		}
	default:
		return "", nil, fmt.Errorf("no cast %q; declare encrypted, json, json:<Type> or encrypted:json", cast)
	}

	switch column.UDT {
	case "json", "jsonb":
		return "json.RawMessage", []string{"encoding/json"}, nil
	case "bytea":
		return "[]byte", nil, nil
	case "_text", "_varchar":
		return "[]string", nil, nil
	}

	base := map[string]string{
		"int8": "int64", "int4": "int", "int2": "int16",
		"numeric": "float64", "float8": "float64", "float4": "float32",
		"bool":    "bool",
		"varchar": "string", "text": "string", "bpchar": "string", "uuid": "string", "time": "string",
		"timestamp": "time.Time", "timestamptz": "time.Time", "date": "time.Time",
	}[column.UDT]
	if base == "" {
		return "", nil, fmt.Errorf("no Go type for Postgres %q", column.UDT)
	}

	if base == "time.Time" {
		imports = []string{"time"}
	}

	if column.Nullable {
		base = "*" + base
	}

	return base, imports, nil
}

// typedJSON is the field type a `json:<Type>` cast names: an exported type
// of the models package, on a json or jsonb column.
func typedJSON(column Column, typeName string) (goType string, imports []string, err error) {
	if !token.IsIdentifier(typeName) || !token.IsExported(typeName) {
		return "", nil, fmt.Errorf("a typed json cast names an exported type of the models package, as json:ChildAges, not %q", typeName)
	}

	if column.UDT != "json" && column.UDT != "jsonb" {
		return "", nil, fmt.Errorf("a json cast needs a json column, not Postgres %q", column.UDT)
	}

	return typeName, nil, nil
}

// initialisms are upper-cased whole, per Go naming (ID, URL, ...).
var initialisms = map[string]bool{"id": true, "url": true, "uuid": true, "ip": true, "api": true, "json": true, "html": true, "http": true, "sql": true, "ttl": true, "utc": true}

// pascal turns a column name into a Go field name: country_id → CountryID.
func pascal(snakeName string) string {
	var out strings.Builder
	for part := range strings.SplitSeq(snakeName, "_") {
		if part == "" {
			continue
		}

		if initialisms[part] {
			out.WriteString(strings.ToUpper(part))

			continue
		}

		out.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}

	return out.String()
}

// snake turns a model name into its file name stem: FareRule → fare_rule.
func snake(pascalName string) string {
	var out strings.Builder
	for i, r := range pascalName {
		if unicode.IsUpper(r) && i > 0 {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToLower(r))
	}

	return out.String()
}

// pluralize covers English model names: City → Cities, Search → Searches,
// Coach → Coaches, Airport → Airports.
func pluralize(name string) string {
	lower := strings.ToLower(name)

	switch {
	case strings.HasSuffix(lower, "y") && len(lower) > 1 && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])):
		return name[:len(name)-1] + "ies"
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "ch"), strings.HasSuffix(lower, "sh"):
		return name + "es"
	default:
		return name + "s"
	}
}

// Write replaces dir's `*_gen.go` files with files, deleting generated files
// for models no longer in the spec. Hand-written files are never touched.
func Write(dir string, files map[string][]byte) (int, error) {
	existing, err := filepath.Glob(filepath.Join(dir, "*_gen.go"))
	if err != nil {
		return 0, err
	}

	for _, path := range existing {
		if _, keep := files[filepath.Base(path)]; !keep {
			if err := os.Remove(path); err != nil {
				return 0, err
			}
		}
	}

	for name, source := range files {
		//nolint:gosec // G306: generated Go source, checked in and readable like every other source file
		if err := os.WriteFile(filepath.Join(dir, name), source, 0o644); err != nil {
			return 0, err
		}
	}

	return len(files), nil
}
