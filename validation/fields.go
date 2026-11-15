package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/atobeach/reef/collect"
	"github.com/atobeach/reef/validation/rule"
)

// fieldPath is one json path of a request struct.
type fieldPath struct {
	path string
	// typ is the path's type, pointers removed.
	typ reflect.Type
	// element marks a list element ("child_ages.*"): its rules are optional.
	element bool
	// object marks a struct, whose own fields carry the rules.
	object bool
	// kept marks a value the decoder hands over as sent (any, a map, a type
	// that decodes itself), so rules may reach inside it.
	kept bool
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// fieldPaths lists t's json paths depth first: fields by tag, `*` for a list
// element, an untagged embedded struct's fields as t's own. A value kept as
// sent is a path, never walked into.
func fieldPaths(t reflect.Type) []fieldPath {
	var paths []fieldPath

	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		t = indirect(t)
		if keptAsSent(t) {
			return
		}

		switch t.Kind() {
		case reflect.Struct:
			for i := range t.NumField() {
				field := t.Field(i)
				if promoted(field) {
					walk(field.Type, prefix)

					continue
				}

				name, ok := jsonName(field)
				if !ok {
					continue
				}

				path := join(prefix, name)
				paths = append(paths, describe(path, field.Type, false))
				walk(field.Type, path)
			}
		case reflect.Slice:
			path := join(prefix, "*")
			paths = append(paths, describe(path, t.Elem(), true))
			walk(t.Elem(), path)
		default:
			// A scalar is a leaf: its parent already listed its path.
		}
	}

	walk(t, "")

	return paths
}

func describe(path string, t reflect.Type, element bool) fieldPath {
	t = indirect(t)
	kept := keptAsSent(t)

	return fieldPath{path: path, typ: t, element: element, object: !kept && t.Kind() == reflect.Struct, kept: kept}
}

// FieldOrder lists a request struct's attribute patterns depth first —
// "passengers", "passengers.*", "passengers.*.first_name" — for Options.Order.
func FieldOrder(t reflect.Type) []string {
	return collect.Map(fieldPaths(t), func(p fieldPath) string { return p.path })
}

// CheckFields reports where a request struct t and its rules disagree, so
// that t holds exactly the input the rules vouch for: a rule key naming no
// field (a key inside a value kept as sent names that value), a field with
// no rule key of its own (a struct's fields carry its rules, and a list
// element's are optional), and a field no input can fill (a chan, a func).
// nil when they agree.
func CheckFields(t reflect.Type, rules rule.Set) error {
	paths := map[string]fieldPath{}
	var problems []string
	for _, p := range fieldPaths(t) {
		paths[p.path] = p

		if _, ruled := rules[p.path]; !ruled && !p.element && !p.object {
			problems = append(problems, fmt.Sprintf("validation: field %q of %s has no rule", p.path, t))
		}

		if !fillable(p.typ) {
			problems = append(problems, fmt.Sprintf("validation: field %q of %s is a %s, which no input fills", p.path, t, p.typ))
		}
	}

	for key := range rules {
		if !names(paths, key) {
			problems = append(problems, fmt.Sprintf("validation: rule %q names no field of %s", key, t))
		}
	}

	slices.Sort(problems)

	return errors.Join(collect.Map(problems, errors.New)...)
}

// names reports whether a rule key is one of paths, or lies inside one kept
// as sent ("seatMap.rows.x" when the rows are an any).
func names(paths map[string]fieldPath, key string) bool {
	if _, ok := paths[key]; ok {
		return true
	}

	for prefix := key; strings.Contains(prefix, "."); {
		prefix = prefix[:strings.LastIndexByte(prefix, '.')]
		if paths[prefix].kept {
			return true
		}
	}

	return false
}

// keptAsSent reports a type the decoder hands its value over to whole: any,
// a map, or a type that decodes itself (json.RawMessage).
func keptAsSent(t reflect.Type) bool {
	return t.Kind() == reflect.Interface || t.Kind() == reflect.Map || reflect.PointerTo(t).Implements(unmarshalerType)
}

// fillable reports whether the decoder can fill a value of t; a struct's
// fields and a list's elements are paths of their own, checked as such.
func fillable(t reflect.Type) bool {
	t = indirect(t)
	if reflect.PointerTo(t).Implements(unmarshalerType) {
		return true
	}

	switch t.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Interface:
		return t.NumMethod() == 0
	case reflect.Map:
		return t.Key().Kind() == reflect.String && fillable(t.Elem())
	default:
		return false
	}
}

func indirect(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}

func join(prefix string, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}

// promoted reports an untagged embedded struct, whose fields are read as
// the outer struct's own.
func promoted(field reflect.StructField) bool {
	return field.Anonymous && field.Tag.Get("json") == "" && field.Type.Kind() == reflect.Struct
}

func jsonName(field reflect.StructField) (string, bool) {
	if !field.IsExported() {
		return "", false
	}

	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false
	}

	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}

	return name, true
}
