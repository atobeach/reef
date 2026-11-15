// Package typescript writes TypeScript declarations for Go Data structs
// (`declare namespace App { namespace Data { … } }`), sorted and 4-space
// indented so regenerations stay diff-free. Pointers become `T | null`,
// `omitempty` an optional field, maps `Record<string, V>`, times strings and
// json.RawMessage or any `any`; a `ts:"..."` tag overrides a field's type.
package typescript

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

const indent = "    "

// Registry maps Go packages to TypeScript namespaces and lists the types to
// declare.
type Registry struct {
	namespaces map[string]string // Go package path → "App.Data.City"
	types      []reflect.Type
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{namespaces: map[string]string{}}
}

// Namespace maps a Go package to a TypeScript namespace.
func (r *Registry) Namespace(pkgPath string, namespace string) {
	r.namespaces[pkgPath] = namespace
}

// Add declares types (pass zero values: flight.SegmentData{}).
func (r *Registry) Add(values ...any) {
	for _, value := range values {
		r.types = append(r.types, reflect.TypeOf(value))
	}
}

// Name is a type's qualified TypeScript name, e.g. App.Data.Flight.SegmentData.
func (r *Registry) Name(t reflect.Type) (string, error) {
	namespace, ok := r.namespaces[t.PkgPath()]
	if !ok {
		return "", fmt.Errorf("typescript: package %s has no namespace", t.PkgPath())
	}

	return namespace + "." + t.Name(), nil
}

// Declaration renders one type's `export type X = {...};` at depth levels of
// indentation.
func (r *Registry) Declaration(t reflect.Type, depth int) (string, error) {
	body, err := r.object(t, depth)
	if err != nil {
		return "", err
	}

	return strings.Repeat(indent, depth) + "export type " + t.Name() + " = " + body + ";", nil
}

// Render writes the whole declaration file, types grouped by namespace and
// sorted; a type registered twice is an error.
func (r *Registry) Render() (string, error) {
	tree := &namespaceNode{children: map[string]*namespaceNode{}}
	seen := map[reflect.Type]bool{}
	for _, t := range r.types {
		name, err := r.Name(t)
		if err != nil {
			return "", err
		}

		if seen[t] {
			return "", fmt.Errorf("typescript: %s is registered twice", name)
		}
		seen[t] = true

		parts := strings.Split(name, ".")
		node := tree
		for _, part := range parts[:len(parts)-1] {
			child, ok := node.children[part]
			if !ok {
				child = &namespaceNode{children: map[string]*namespaceNode{}}
				node.children[part] = child
			}
			node = child
		}
		node.types = append(node.types, t)
	}

	var out strings.Builder
	for _, root := range slices.Sorted(maps.Keys(tree.children)) {
		if err := r.writeNamespace(&out, root, tree.children[root], 0, true); err != nil {
			return "", err
		}
	}

	return out.String(), nil
}

type namespaceNode struct {
	children map[string]*namespaceNode
	types    []reflect.Type
}

func (r *Registry) writeNamespace(out *strings.Builder, name string, node *namespaceNode, depth int, root bool) error {
	pad := strings.Repeat(indent, depth)
	if root {
		out.WriteString("declare namespace " + name + " {\n")
	} else {
		out.WriteString(pad + "namespace " + name + " {\n")
	}

	slices.SortFunc(node.types, func(a, b reflect.Type) int { return strings.Compare(a.Name(), b.Name()) })
	for _, t := range node.types {
		declaration, err := r.Declaration(t, depth+1)
		if err != nil {
			return err
		}
		out.WriteString(declaration + "\n")
	}

	for _, child := range slices.Sorted(maps.Keys(node.children)) {
		if err := r.writeNamespace(out, child, node.children[child], depth+1, false); err != nil {
			return err
		}
	}

	out.WriteString(pad + "}\n")

	return nil
}

func (r *Registry) object(t reflect.Type, depth int) (string, error) {
	var out strings.Builder
	out.WriteString("{\n")

	for i := range t.NumField() {
		field := t.Field(i)
		name, optional, ok := jsonField(field)
		if !ok {
			continue
		}

		tsType := field.Tag.Get("ts")
		if tsType == "" {
			var err error
			if tsType, err = r.typeOf(field.Type, depth+1); err != nil {
				return "", fmt.Errorf("%s.%s: %w", t.Name(), field.Name, err)
			}
		}

		marker := ":"
		if optional {
			marker = "?:"
		}

		out.WriteString(strings.Repeat(indent, depth+1) + name + marker + " " + tsType + ";\n")
	}

	out.WriteString(strings.Repeat(indent, depth) + "}")

	return out.String(), nil
}

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
)

func (r *Registry) typeOf(t reflect.Type, depth int) (string, error) {
	if t == rawType {
		return "any", nil
	}

	switch t.Kind() {
	case reflect.Pointer:
		inner, err := r.typeOf(t.Elem(), depth)
		if err != nil {
			return "", err
		}

		return inner + " | null", nil
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Interface:
		return "any", nil
	case reflect.Slice, reflect.Array:
		inner, err := r.typeOf(t.Elem(), depth)
		if err != nil {
			return "", err
		}

		// Only a top-level union needs parentheses; an inline object's own
		// nullable fields don't.
		if strings.HasSuffix(inner, " | null") || (strings.Contains(inner, " | ") && !strings.HasPrefix(inner, "{")) {
			inner = "(" + inner + ")"
		}

		return inner + "[]", nil
	case reflect.Map:
		inner, err := r.typeOf(t.Elem(), depth)
		if err != nil {
			return "", err
		}

		return "Record<string, " + inner + ">", nil
	case reflect.Struct:
		if t == timeType || (t.NumField() == 1 && t.Field(0).Anonymous && t.Field(0).Type == timeType) {
			return "string", nil
		}

		if t.Name() == "" {
			return r.object(t, depth)
		}

		return r.Name(t)
	default:
		return "", fmt.Errorf("typescript: no mapping for %s", t)
	}
}

func jsonField(field reflect.StructField) (name string, optional bool, ok bool) {
	if !field.IsExported() {
		return "", false, false
	}

	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}

	name, options, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}

	return name, strings.Contains(options, "omitempty"), true
}

// Declarations indexes a declaration file by qualified type name
// ("App.Data.Flight.SegmentData" → its `export type` block), so the
// staleness test can name which declarations drifted.
func Declarations(source string) map[string]string {
	found := map[string]string{}

	var stack []string
	lines := strings.Split(source, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		switch {
		case strings.HasPrefix(line, "declare namespace "), strings.HasPrefix(line, "namespace "):
			name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(line, "declare "), "namespace "), " {")
			stack = append(stack, name)
		case strings.HasPrefix(line, "export type "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "export type "), " ")
			block := []string{lines[i]}
			depth := strings.Count(line, "{") - strings.Count(line, "}")
			for depth > 0 && i+1 < len(lines) {
				i++
				block = append(block, lines[i])
				depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			}

			found[strings.Join(append(slices.Clone(stack), name), ".")] = strings.Join(block, "\n")
		case line == "}":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}

	return found
}

// Types are the registered types, in registration order, for tests that
// check each type's encoding against its declaration.
func (r *Registry) Types() []reflect.Type {
	return slices.Clone(r.types)
}
