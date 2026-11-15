package codegen

import (
	"bytes"
	"strings"
	"text/template"
)

// rowMethods are a keyed model's writes to its own row. Update and Refresh
// read the row back into the struct and keep the relations it loaded.
var rowMethods = template.Must(template.New("rowMethods").Parse(`
// Update writes values to the row, unless none of them differs from what is
{{- if .Loaded}}
// stored, and reads it back as stored, keeping the relations in Loaded. An
// encrypted value always differs: each seal is new.
{{- else}}
// stored, and reads it back as stored. An encrypted value always differs:
// each seal is new.
{{- end}}
func ({{.Receiver}} *{{.Model}}) Update(ctx context.Context, values db.Values) error {
	updated, err := {{.Query}}(ctx).WhereKey({{.Receiver}}.ID).UpdateChanged(values)
	if err != nil {
		return fmt.Errorf("updating {{.Words}} %v: %w", {{.Receiver}}.ID, err)
	}

	if len(updated) == 1 {
		{{- if .Loaded}}
		updated[0].Loaded = {{.Receiver}}.Loaded
		{{- end}}
		*{{.Receiver}} = updated[0]
	}

	return nil
}

// Refresh reads the row again.
{{- if .Loaded}} The relations in Loaded stay as loaded: load
// them again if their keys changed.
{{- end}}
func ({{.Receiver}} *{{.Model}}) Refresh(ctx context.Context) error {
	fresh, err := {{.Query}}(ctx).FindOrFail({{.Receiver}}.ID)
	if err != nil {
		return fmt.Errorf("refreshing {{.Words}} %v: %w", {{.Receiver}}.ID, err)
	}
{{if .Loaded}}
	fresh.Loaded = {{.Receiver}}.Loaded
{{- end}}
	*{{.Receiver}} = fresh

	return nil
}

// Delete deletes the row.
func ({{.Receiver}} *{{.Model}}) Delete(ctx context.Context) error {
	if _, err := {{.Query}}(ctx).WhereKey({{.Receiver}}.ID).Delete(); err != nil {
		return fmt.Errorf("deleting {{.Words}} %v: %w", {{.Receiver}}.ID, err)
	}

	return nil
}
`))

// keyedByID reports a model keyed by a non-null id column, the key its row
// methods write by.
func keyedByID(fields []field) bool {
	for _, f := range fields {
		if f.Column == "id" {
			return !strings.HasPrefix(f.Type, "*")
		}
	}

	return false
}

// writeRowMethods writes a keyed model's Update, Refresh and Delete; loaded
// says whether it has eager-loaded relations to keep.
func writeRowMethods(out *bytes.Buffer, name string, loaded bool) error {
	return rowMethods.Execute(out, struct {
		Model, Receiver, Query, Words string
		Loaded                        bool
	}{
		Model:    name,
		Receiver: strings.ToLower(name[:1]),
		Query:    pluralize(name),
		Words:    strings.ReplaceAll(snake(name), "_", " "),
		Loaded:   loaded,
	})
}
