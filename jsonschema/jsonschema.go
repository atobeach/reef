// Package jsonschema builds the JSON Schemas of AI tool parameters and
// structured output, fluently or derived from a Go type with For.
//
//	jsonschema.Properties{
//		{Name: "location", Type: jsonschema.String().Description(`Country or city name.`).Required()},
//		{Name: "topics", Type: jsonschema.Array().Items(jsonschema.String().EnumStrings(topics)).Nullable()},
//	}
//
// Each node serializes its keys in a fixed order (description and enum, the
// type's own, `type`, then `required`), so a tool definition's bytes change
// only when the schema does.
package jsonschema

import (
	"slices"

	"github.com/atobeach/reef/ordered"
)

// Type is one schema node.
type Type interface {
	// Serialize is the node as a JSON Schema object.
	Serialize() *ordered.Object
	shared() *common
}

// Property is one named object property, in declaration order.
type Property struct {
	Name string
	Type Type
}

// Properties are an object's properties in declaration order.
type Properties []Property

// String is a string node.
func String() *StringType {
	t := &StringType{}
	t.self = t

	return t
}

// Integer is an integer node.
func Integer() *IntegerType {
	t := &IntegerType{}
	t.self = t

	return t
}

// Number is a number node.
func Number() *NumberType {
	t := &NumberType{}
	t.self = t

	return t
}

// Boolean is a boolean node.
func Boolean() *BooleanType {
	t := &BooleanType{}
	t.self = t

	return t
}

// Array is an array node.
func Array() *ArrayType {
	t := &ArrayType{}
	t.self = t

	return t
}

// Object is an object node with the given properties.
func Object(properties Properties) *ObjectType {
	t := &ObjectType{properties: properties}
	t.self = t

	return t
}

// common holds the fields every node has.
type common struct {
	required    bool
	nullable    bool
	description *string
	enum        []any
}

func (c *common) shared() *common { return c }

// base is common plus the concrete node type T, so the fluent setters
// return it for chaining.
type base[T any] struct {
	common
	self *T
}

// Required marks the property required on its parent object.
func (b *base[T]) Required() *T {
	b.required = true

	return b.self
}

// Nullable adds "null" to the type.
func (b *base[T]) Nullable() *T {
	b.nullable = true

	return b.self
}

// Description sets the description.
func (b *base[T]) Description(value string) *T {
	b.description = &value

	return b.self
}

// EnumStrings restricts the value to the given strings, in order.
func (b *base[T]) EnumStrings(values []string) *T {
	list := make([]any, len(values))
	for i, value := range values {
		list[i] = value
	}
	b.enum = list

	return b.self
}

// head writes the shared fields, in their fixed order.
func (b *base[T]) head() *ordered.Object {
	o := ordered.NewObject()
	if b.description != nil {
		o.Set("description", *b.description)
	}
	if b.enum != nil {
		o.Set("enum", b.enum)
	}

	return o
}

// typeName is the `type` value, with "null" appended when nullable.
func (b *base[T]) typeName(name string) any {
	if b.nullable {
		return []any{name, "null"}
	}

	return name
}

// StringType is a `string` node.
type StringType struct {
	base[StringType]
	maxLength *int
}

// Max sets maxLength.
func (t *StringType) Max(length int) *StringType {
	t.maxLength = &length

	return t
}

// Serialize implements Type.
func (t *StringType) Serialize() *ordered.Object {
	o := t.head()
	setInt(o, "maxLength", t.maxLength)
	o.Set("type", t.typeName("string"))

	return o
}

// IntegerType is an `integer` node.
type IntegerType struct {
	base[IntegerType]
	minimum *int
	maximum *int
}

// Min sets minimum.
func (t *IntegerType) Min(value int) *IntegerType {
	t.minimum = &value

	return t
}

// Max sets maximum.
func (t *IntegerType) Max(value int) *IntegerType {
	t.maximum = &value

	return t
}

// Serialize implements Type.
func (t *IntegerType) Serialize() *ordered.Object {
	o := t.head()
	setInt(o, "minimum", t.minimum)
	setInt(o, "maximum", t.maximum)
	o.Set("type", t.typeName("integer"))

	return o
}

// NumberType is a `number` node.
type NumberType struct {
	base[NumberType]
	minimum *float64
	maximum *float64
}

// Min sets minimum.
func (t *NumberType) Min(value float64) *NumberType {
	t.minimum = &value

	return t
}

// Max sets maximum.
func (t *NumberType) Max(value float64) *NumberType {
	t.maximum = &value

	return t
}

// Serialize implements Type.
func (t *NumberType) Serialize() *ordered.Object {
	o := t.head()
	if t.minimum != nil {
		o.Set("minimum", *t.minimum)
	}
	if t.maximum != nil {
		o.Set("maximum", *t.maximum)
	}
	o.Set("type", t.typeName("number"))

	return o
}

// BooleanType is a `boolean` node.
type BooleanType struct {
	base[BooleanType]
}

// Serialize implements Type.
func (t *BooleanType) Serialize() *ordered.Object {
	o := t.head()
	o.Set("type", t.typeName("boolean"))

	return o
}

// ArrayType is an `array` node.
type ArrayType struct {
	base[ArrayType]
	minItems *int
	maxItems *int
	items    Type
}

// Min sets minItems.
func (t *ArrayType) Min(value int) *ArrayType {
	t.minItems = &value

	return t
}

// Max sets maxItems.
func (t *ArrayType) Max(value int) *ArrayType {
	t.maxItems = &value

	return t
}

// Items sets the item schema.
func (t *ArrayType) Items(item Type) *ArrayType {
	t.items = item

	return t
}

// Serialize implements Type.
func (t *ArrayType) Serialize() *ordered.Object {
	o := t.head()
	setInt(o, "minItems", t.minItems)
	setInt(o, "maxItems", t.maxItems)
	if t.items != nil {
		o.Set("items", t.items.Serialize())
	}
	o.Set("type", t.typeName("array"))

	return o
}

// ObjectType is an `object` node.
type ObjectType struct {
	base[ObjectType]
	additionalProperties *bool
	properties           Properties
}

// Serialize implements Type: properties (when any), type, then the
// required list.
func (t *ObjectType) Serialize() *ordered.Object {
	o := t.head()
	if t.additionalProperties != nil {
		o.Set("additionalProperties", *t.additionalProperties)
	}

	var required []any
	if len(t.properties) > 0 {
		props := ordered.NewObject()
		for _, property := range t.properties {
			props.Set(property.Name, property.Type.Serialize())
			if property.Type.shared().required {
				required = append(required, property.Name)
			}
		}
		o.Set("properties", props)
	}

	o.Set("type", t.typeName("object"))
	if len(required) > 0 {
		o.Set("required", required)
	}

	return o
}

func setInt(o *ordered.Object, key string, value *int) {
	if value != nil {
		o.Set(key, *value)
	}
}

// ObjectSchema is a structured-output schema: the properties as a root
// object with every object in it closed to additional properties, as strict
// structured output requires.
func ObjectSchema(properties Properties) *ordered.Object {
	closed := false
	root := &ObjectType{properties: properties, additionalProperties: &closed}
	root.self = root

	return disableAdditionalProperties(root.Serialize())
}

// disableAdditionalProperties closes schema and every object nested under its
// properties or items, in place.
func disableAdditionalProperties(schema *ordered.Object) *ordered.Object {
	if isObjectType(schema) {
		schema.Set("additionalProperties", false)

		if properties, ok := nested(schema, "properties"); ok {
			for _, key := range properties.Keys() {
				if property, ok := nested(properties, key); ok {
					disableAdditionalProperties(property)
				}
			}
		}
	}

	if items, ok := nested(schema, "items"); ok {
		disableAdditionalProperties(items)
	}

	return schema
}

// isObjectType reports whether the node's type is, or includes, "object".
func isObjectType(schema *ordered.Object) bool {
	typ, _ := schema.Get("type")
	if list, ok := typ.([]any); ok {
		return slices.Contains(list, any("object"))
	}

	return typ == "object"
}

func nested(o *ordered.Object, key string) (*ordered.Object, bool) {
	value, _ := o.Get(key)
	object, ok := value.(*ordered.Object)

	return object, ok
}
