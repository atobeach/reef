# JSON Schema

- [Introduction](#introduction)
- [Building Schemas](#building-schemas)
    - [Properties](#properties)
    - [Required and Nullable Properties](#required-and-nullable-properties)
    - [Serializing a Schema](#serializing-a-schema)
- [Available Types](#available-types)
- [Deriving a Schema From a Go Type](#deriving-a-schema-from-a-go-type)
    - [Struct Tags](#struct-tags)
    - [Enums](#enums)
    - [What For Cannot Describe](#what-for-cannot-describe)
- [Object Schemas](#object-schemas)
- [Key Order](#key-order)

## Introduction

The `github.com/atobeach/reef/jsonschema` package is a fluent JSON Schema builder. reef's [AI agents](ai.md) use it in two places: a [tool's](ai-tools.md#tool-schemas) parameters, and an agent's [structured output](ai-agents.md#structured-output). You may build a schema by hand, or derive it from the Go struct the answer decodes into, so the schema and the type can never drift.

```go
jsonschema.Properties{
	{Name: "origin", Type: jsonschema.String().Description("Airport IATA code or city name.").Required()},
	{Name: "adults", Type: jsonschema.Integer().Min(1).Max(9).Required()},
	{Name: "cabins", Type: jsonschema.Array().Items(jsonschema.String().EnumStrings(cabins)).Nullable()},
}
```

## Building Schemas

### Properties

A schema is a `jsonschema.Properties` value: a slice of `jsonschema.Property`, each a `Name` and a `Type`. Properties keep their declaration order, so the schema a model sees lists them in the order you wrote them.

Each `Type` is built with one of the package's constructors (`String`, `Integer`, `Number`, `Boolean`, `Array` and `Object`) and refined with chained methods:

```go
func (*SearchFlights) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "origin", Type: jsonschema.String().Required()},
		{Name: "destination", Type: jsonschema.String().Required()},
		{Name: "max_price", Type: jsonschema.Number().Min(0)},
		{Name: "limit", Type: jsonschema.Integer().Min(1).Max(20)},
		{Name: "airlines", Type: jsonschema.Array().Items(jsonschema.String()).Nullable()},
	}
}
```

### Required and Nullable Properties

`Required` marks a property required on the object that holds it: its name joins that object's `required` list, in declaration order. `Nullable` adds `"null"` to the property's type, so `jsonschema.String().Nullable()` serializes as `"type": ["string", "null"]`. The two are independent: a required, nullable property must be present but may be `null`.

`Required` only has meaning on an object's property. On an array's item schema or a root node it is ignored.

### Serializing a Schema

Every type implements `jsonschema.Type`, whose `Serialize` method returns the node as an `*ordered.Object`, ready to marshal with `encoding/json`. Wrapping the properties above in `jsonschema.Object` and serializing it gives:

```go
encoded, err := json.Marshal(jsonschema.Object(properties).Serialize())
```

```json
{
    "properties": {
        "origin": {
            "description": "Airport IATA code or city name.",
            "type": "string"
        },
        "adults": {
            "minimum": 1,
            "maximum": 9,
            "type": "integer"
        },
        "cabins": {
            "items": {
                "enum": ["economy", "business"],
                "type": "string"
            },
            "type": ["array", "null"]
        }
    },
    "type": "object",
    "required": ["origin", "adults"]
}
```

You rarely serialize a schema yourself: the AI package does it when it sends a tool or a structured-output request. `Type` is implemented only by this package's node types.

## Available Types

#### String

`jsonschema.String()` is a `string` node (`*jsonschema.StringType`). `Max` sets `maxLength`:

```go
jsonschema.String().Description(`The 3-letter currency code, e.g. "EUR".`).Max(3).Required()
```

#### Integer

`jsonschema.Integer()` is an `integer` node (`*jsonschema.IntegerType`). `Min` and `Max` set `minimum` and `maximum`:

```go
jsonschema.Integer().Min(1).Max(9)
```

#### Number

`jsonschema.Number()` is a `number` node (`*jsonschema.NumberType`). `Min` and `Max` set `minimum` and `maximum`:

```go
jsonschema.Number().Min(0).Max(99999999).Description("The most the user will pay per passenger.")
```

#### Boolean

`jsonschema.Boolean()` is a `boolean` node (`*jsonschema.BooleanType`):

```go
jsonschema.Boolean().Description("Only show refundable fares.")
```

#### Array

`jsonschema.Array()` is an `array` node (`*jsonschema.ArrayType`). `Items` sets the item schema, and `Min` and `Max` set `minItems` and `maxItems`:

```go
jsonschema.Array().Items(jsonschema.String()).Min(1).Max(3)
```

#### Object

`jsonschema.Object(properties)` is an `object` node (`*jsonschema.ObjectType`) with the given properties:

```go
jsonschema.Object(jsonschema.Properties{
	{Name: "depart_date", Type: jsonschema.String().Required()},
	{Name: "return_date", Type: jsonschema.String().Required()},
}).Required()
```

An object without properties serializes without a `properties` key, and one without required properties without a `required` key.

#### Shared Methods

Every node has these methods, each returning the node itself for chaining:

| Method | Description |
|--------|-------------|
| `Description(value)` | Sets `description`. |
| `EnumStrings(values)` | Restricts the value to the given strings, in order (`enum`). |
| `Required()` | Marks the property required on its parent object. |
| `Nullable()` | Adds `"null"` to the type. |

```go
jsonschema.String().EnumStrings([]string{"economy", "premium_economy", "business", "first"})
```

## Deriving a Schema From a Go Type

`jsonschema.For[T]()` returns the properties of struct `T`'s exported fields, in field order. It is what `ai.StructuredOutput[T]` uses, so a structured agent's schema always matches the type its answer decodes into:

```go
// Perk is something a fare includes.
type Perk string

// Enum implements jsonschema.Enum.
func (Perk) Enum() []string { return []string{"baggage", "meals", "wifi"} }

// FareSummary is the fare summarizer's answer.
type FareSummary struct {
	Summary string   `json:"summary" description:"Two sentences."`
	Perks   []Perk   `json:"perks" minItems:"1" maxItems:"3"`
	Warning *string  `json:"warning"`
	Sources []Source `json:"sources"`
}

// Source is one cited source.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

properties := jsonschema.For[FareSummary]()
```

`For` reads the struct the way `encoding/json` would decode into it:

- Each field is a property named by its `json` tag (or the field name when the tag has none). Unexported fields and fields tagged `json:"-"` are skipped.
- Every property is required. A pointer field is nullable, so the model must still send the key, but may send `null`.
- An untagged embedded struct (or pointer to one) contributes its fields as if they were the parent's own.
- Strings become `string`, booleans `boolean`, every integer kind `integer`, floats `number`, structs nested `object`s, and slices and arrays `array`s of their element type.

> [!NOTE]
> `omitempty` does not make a property optional. If the model may leave a value out, make the field a pointer and tell the model it may send `null`.

### Struct Tags

Beyond `json`, `For` reads these tags:

| Tag | Description |
|-----|-------------|
| `description:"..."` | The property's description. |
| `items:"..."` | A list's item description. |
| `minItems:"1"` | A list's `minItems`. |
| `maxItems:"3"` | A list's `maxItems`. |

```go
type PackingTips struct {
	Tips []string `json:"tips" description:"Exactly 5 actionable tips" items:"A specific, actionable tip under 100 chars"`
}
```

### Enums

A string type that implements `jsonschema.Enum` restricts every field of that type to its values, in order:

```go
type Enum interface {
	Enum() []string
}
```

The `Perk` type above makes `perks` a list whose items must be `baggage`, `meals` or `wifi`. `Enum` is called on the type's zero value, so it should return a fixed list.

### What For Cannot Describe

`For` panics on anything it cannot describe the way `encoding/json` would decode it, so a mistake surfaces the first time the schema is built (in practice, the agent's first test):

- a `T` that isn't a struct, or a struct with no fields;
- two fields with the same JSON name;
- a map, an interface, or a `[]byte` (which `encoding/json` reads as base64 text);
- a type with its own `UnmarshalJSON` or `UnmarshalText`, such as `time.Time`;
- the `,string` tag option;
- a slice or array of pointers;
- an `Enum` that isn't a string type;
- a `minItems` or `maxItems` tag that isn't an integer.

## Object Schemas

`jsonschema.ObjectSchema(properties)` turns properties into a complete root schema: an `object` with `additionalProperties: false`, where every nested object (through `properties` and `items`, nullable ones included) is closed the same way, as strict structured output requires. The AI package sends tool parameters and structured-output schemas through it. The `FareSummary` struct above becomes:

```go
schema := jsonschema.ObjectSchema(jsonschema.For[FareSummary]())
```

```json
{
    "additionalProperties": false,
    "properties": {
        "summary": {
            "description": "Two sentences.",
            "type": "string"
        },
        "perks": {
            "minItems": 1,
            "maxItems": 3,
            "items": {
                "enum": ["baggage", "meals", "wifi"],
                "type": "string"
            },
            "type": "array"
        },
        "warning": {
            "type": ["string", "null"]
        },
        "sources": {
            "items": {
                "properties": {
                    "title": {"type": "string"},
                    "url": {"type": "string"}
                },
                "type": "object",
                "required": ["title", "url"],
                "additionalProperties": false
            },
            "type": "array"
        }
    },
    "type": "object",
    "required": ["summary", "perks", "warning", "sources"]
}
```

## Key Order

Each node writes its keys in a fixed order: the shared fields first (`description`, `enum`), then the type's own (`maxLength`; `minimum` and `maximum`; `minItems`, `maxItems` and `items`; an object's `properties`), then `type`, then an object's `required` list. A tool definition therefore reads description-first, and its bytes change only when the schema does. `ObjectSchema` writes the root's `additionalProperties` first, and appends it to each nested object after its `required` list, as the example above shows.
