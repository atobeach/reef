package jsonschema

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Enum is a type whose values are a fixed list: For restricts a field of
// that type to them, in order.
type Enum interface {
	Enum() []string
}

var enumType = reflect.TypeFor[Enum]()

// For is the schema of struct T's exported fields, in order, so a
// structured-output schema cannot drift from the type its answer decodes
// into. Each field is a required property named as encoding/json names it;
// a pointer is nullable and an Enum string type restricted to its values.
// Tags add the rest:
//
//	description:"…"           the property's description
//	items:"…"                 a list's item description
//	minItems:"1" maxItems:"3" a list's length bounds
//
// For panics on what it cannot describe as encoding/json would decode it: a
// map, an interface, a []byte, a type that decodes itself, `,string`, an
// empty struct or a duplicate name.
func For[T any]() Properties {
	return properties(reflect.TypeFor[T]())
}

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func properties(t reflect.Type) Properties {
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("jsonschema: For wants a struct, got %s", t))
	}

	props := fields(t)
	if len(props) == 0 {
		panic(fmt.Sprintf("jsonschema: For cannot describe %s: it has no fields", t))
	}

	seen := make(map[string]bool, len(props))
	for _, prop := range props {
		if seen[prop.Name] {
			panic(fmt.Sprintf("jsonschema: For cannot describe %s: two fields are named %q", t, prop.Name))
		}
		seen[prop.Name] = true
	}

	return props
}

// fields are t's properties, an embedded struct's spliced in where it sits.
func fields(t reflect.Type) Properties {
	var props Properties
	for field := range t.Fields() {
		if embedded, ok := promoted(field); ok {
			props = append(props, fields(embedded)...)

			continue
		}

		name, skip := jsonName(field)
		if skip {
			continue
		}

		props = append(props, Property{Name: name, Type: fieldType(field)})
	}

	return props
}

// promoted is the struct whose fields encoding/json lifts into the parent:
// an untagged embedded struct, or pointer to one.
func promoted(field reflect.StructField) (reflect.Type, bool) {
	tag := field.Tag.Get("json")
	if name, _, _ := strings.Cut(tag, ","); !field.Anonymous || tag == "-" || name != "" {
		return nil, false
	}

	t := field.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t, t.Kind() == reflect.Struct
}

// jsonName is the field's property name as encoding/json writes it; skip is
// an unexported or `json:"-"` field.
func jsonName(field reflect.StructField) (name string, skip bool) {
	tag := field.Tag.Get("json")
	if !field.IsExported() || tag == "-" {
		return "", true
	}

	name, options, _ := strings.Cut(tag, ",")
	if slices.Contains(strings.Split(options, ","), "string") {
		panic(fmt.Sprintf("jsonschema: For cannot describe field %s: the `,string` option decodes a number from a string", field.Name))
	}

	if name == "" {
		name = field.Name
	}

	return name, false
}

func fieldType(field reflect.StructField) Type {
	t := field.Type
	nullable := t.Kind() == reflect.Pointer
	if nullable {
		t = t.Elem()
	}

	node := nodeFor(t, field.Tag)
	describe(node, field.Tag.Get("description"))
	node.shared().nullable = nullable
	node.shared().required = true

	return node
}

// nodeFor is the node for a field's (dereferenced) type.
func nodeFor(t reflect.Type, tag reflect.StructTag) Type {
	if t.Implements(enumType) {
		if t.Kind() != reflect.String {
			panic(fmt.Sprintf("jsonschema: For cannot describe %s: an Enum must be a string type", t))
		}

		return String().EnumStrings(reflect.Zero(t).Interface().(Enum).Enum())
	}

	if pointer := reflect.PointerTo(t); pointer.Implements(jsonUnmarshaler) || pointer.Implements(textUnmarshaler) {
		panic(fmt.Sprintf("jsonschema: For cannot describe %s: it decodes itself", t))
	}

	switch t.Kind() {
	case reflect.String:
		return String()
	case reflect.Bool:
		return Boolean()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Integer()
	case reflect.Float32, reflect.Float64:
		return Number()
	case reflect.Struct:
		return Object(properties(t))
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			panic(fmt.Sprintf("jsonschema: For cannot describe a %s: encoding/json reads it as base64 text", t))
		}

		return arrayFor(t.Elem(), tag)
	default:
		panic(fmt.Sprintf("jsonschema: For cannot describe a %s", t))
	}
}

func arrayFor(elem reflect.Type, tag reflect.StructTag) *ArrayType {
	item := nodeFor(elem, "")
	describe(item, tag.Get("items"))

	array := Array().Items(item)
	if bound, ok := intTag(tag, "minItems"); ok {
		array.Min(bound)
	}
	if bound, ok := intTag(tag, "maxItems"); ok {
		array.Max(bound)
	}

	return array
}

func intTag(tag reflect.StructTag, key string) (int, bool) {
	value, ok := tag.Lookup(key)
	if !ok {
		return 0, false
	}

	bound, err := strconv.Atoi(value)
	if err != nil {
		panic(fmt.Sprintf("jsonschema: %s:%q is not an integer", key, value))
	}

	return bound, true
}

func describe(node Type, description string) {
	if description != "" {
		node.shared().description = &description
	}
}
