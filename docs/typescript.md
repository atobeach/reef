# TypeScript

- [Introduction](#introduction)
- [Registering Types](#registering-types)
    - [Namespaces](#namespaces)
- [Generating the Declaration File](#generating-the-declaration-file)
- [Type Mapping](#type-mapping)
    - [Optional Fields](#optional-fields)
    - [Overriding a Field's Type](#overriding-a-fields-type)
- [Keeping the File in Sync](#keeping-the-file-in-sync)
- [Available Methods](#available-methods)

## Introduction

Your API serves JSON built from Go structs, and your frontend reads it. The `github.com/atobeach/reef/typescript` package writes TypeScript declarations for those structs, so the frontend type-checks against the shapes the API actually serves:

```ts
declare namespace App {
    namespace Data {
        export type FlightData = {
            id: number;
            number: string;
            terminal: string | null;
            segments: App.Data.Flight.SegmentData[];
        };
    }
}
```

Laravel applications get the same from spatie's `typescript-transformer`; in reef you list the types in a registry and render it. The output format (4-space indents, namespaces and types sorted alphabetically) is stable, so regenerating an unchanged registry produces an identical file.

## Registering Types

Create a `*typescript.Registry` with `typescript.NewRegistry`, map each Go package to a TypeScript namespace, and add the types to declare by passing their zero values. The examples on this page use the example flight-booking application, `example.com/flights`, whose `FlightData` struct is made up to show every kind of field:

```go
package registry

import (
	"github.com/atobeach/reef/typescript"
	"example.com/flights/app/data"
	"example.com/flights/app/data/flight"
)

// TypeScript is every declared Data type.
func TypeScript() *typescript.Registry {
	r := typescript.NewRegistry()
	r.Namespace("example.com/flights/app/data", "App.Data")
	r.Namespace("example.com/flights/app/data/flight", "App.Data.Flight")

	r.Add(
		data.FlightData{},
		flight.SegmentData{},
		flight.FareRuleData{},
	)

	return r
}
```

A larger application may keep its registry in `app/data/registry`, one file per area, each adding its own namespace and types.

### Namespaces

`Namespace` maps a Go package path to a dotted TypeScript namespace. A type's qualified name is its namespace plus its Go type name, so `flight.FareRuleData` above is `App.Data.Flight.FareRuleData`. Every package whose types are declared, or referenced from a declared field, needs a namespace; a type from an unmapped package fails with `typescript: package <path> has no namespace`.

A field whose type is another named struct is written as that struct's qualified name. Add the referenced type to the registry too, or the declaration will point at a type that is never declared.

## Generating the Declaration File

`Render` writes the whole declaration file: every registered type, grouped by namespace, with each namespace's types (sorted by name) before its nested namespaces (also sorted):

```go
source, err := registry.TypeScript().Render()
if err != nil {
	return err
}

return os.WriteFile("../frontend/lib/generated/app.d.ts", []byte(source), 0o600)
```

The registry above renders:

```ts
declare namespace App {
    namespace Data {
        export type FlightData = {
            id: number;
            number: string;
            terminal: string | null;
            refundable: boolean;
            segments: App.Data.Flight.SegmentData[];
            fare_rules: App.Data.Flight.FareRuleData[] | null;
            baggage: Record<string, string>;
            extra: any;
            legacy: Record<number | string, any> | null;
            updated_at: string;
            checked_at: string | null;
            times: {
                outbound?: string | null;
            };
            codes: (string | null)[];
        };
        namespace Flight {
            export type FareRuleData = {
                title: string;
                description: string;
            };
            export type SegmentData = {
                origin: string;
                destination: string;
            };
        }
    }
}
```

A type added twice is an error (`typescript: App.Data.Flight.FareRuleData is registered twice`), since it would be declared twice.

A typical application registers a `types:generate` command that renders the registry into a file its frontend imports, such as `frontend/lib/generated/app.d.ts`, and puts a `//go:generate` line that runs it in the registry package:

```shell
flights types:generate
```

## Type Mapping

Fields are declared in struct order, named by their `json` tag (or the field name when the tag has none). Unexported fields and fields tagged `json:"-"` are left out. The example `FlightData` struct rendered above is:

```go
type FlightData struct {
	ID         int64                  `json:"id"`
	Number     string                 `json:"number"`
	Terminal   *string                `json:"terminal"`
	Refundable bool                   `json:"refundable"`
	Segments   []flight.SegmentData   `json:"segments"`
	FareRules  *[]flight.FareRuleData `json:"fare_rules"`
	Baggage    map[string]string      `json:"baggage"`
	Extra      json.RawMessage        `json:"extra"`
	Legacy     []any                  `json:"legacy" ts:"Record<number | string, any> | null"`
	UpdatedAt  clock.Time             `json:"updated_at"`
	CheckedAt  *time.Time             `json:"checked_at"`
	Times      struct {
		Outbound *string `json:"outbound,omitempty"`
	} `json:"times"`
	Codes []*string `json:"codes"`
}
```

| Go | TypeScript |
|----|------------|
| `string` | `string` |
| Every integer and float kind | `number` |
| `bool` | `boolean` |
| `*T` | `T \| null` |
| `[]T`, `[N]T` | `T[]`, parenthesized when `T` is a union: `(string \| null)[]` |
| `map[K]V` | `Record<string, V>` |
| A named struct | Its qualified name, such as `App.Data.Flight.SegmentData` |
| An anonymous struct | An inline object |
| `time.Time`, and a struct that only embeds one (`clock.Time`, `clock.Atom`) | `string` |
| `json.RawMessage`, `any` and other interfaces | `any` |

Any other kind (a channel, a function, a complex number) fails with `typescript: no mapping for <type>`, prefixed with the struct and field it was found on.

> [!WARNING]
> The declarations follow the `json` tags, not a custom `MarshalJSON`. An embedded struct is declared as a field named after its type, rather than flattened the way `encoding/json` flattens it, and a `[]byte` is declared as `number[]` although `encoding/json` writes it as a base64 string. Give such fields a `ts` tag, or declare the fields on the struct itself.

### Optional Fields

A field whose `json` tag has `omitempty` is declared optional, `name?: type`, since the API may leave it out:

```go
Outbound *string `json:"outbound,omitempty"` // outbound?: string | null;
```

### Overriding a Field's Type

A `ts` tag replaces a field's TypeScript type outright, for when the Go type can't say what the JSON holds:

```go
CreatedAt *AtomTime `json:"created_at" ts:"string | null"`
Legacy    []any     `json:"legacy" ts:"Record<number | string, any> | null"`
```

## Keeping the File in Sync

A declaration file is only useful while it matches the structs. Commit the generated file and fail a test when it is stale. `typescript.Declarations` indexes a declaration file by qualified type name, each entry the type's `export type` block, so the failure names exactly which types drifted:

```go
func TestTheCommittedDeclarationFileIsUpToDate(t *testing.T) {
	root, err := codegen.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	committed, err := os.ReadFile(filepath.Join(root, "..", "frontend", "lib", "generated", "app.d.ts"))
	if err != nil {
		t.Fatal(err)
	}

	generated, err := TypeScript().Render()
	if err != nil {
		t.Fatal(err)
	}

	if generated == string(committed) {
		return
	}

	want, got := typescript.Declarations(string(committed)), typescript.Declarations(generated)
	var drifted []string
	for name, declaration := range got {
		if want[name] != declaration {
			drifted = append(drifted, name)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			drifted = append(drifted, name+" (no longer generated)")
		}
	}
	slices.Sort(drifted)

	t.Fatalf("app.d.ts is stale, run `flights types:generate`. Changed: %s", strings.Join(drifted, ", "))
}
```

`codegen.ModuleRoot` finds the directory holding `go.mod`, so the test reads the same file whichever package directory `go test` runs it from.

Since declarations follow the tags, you may also check each registered type's actual JSON against its declaration. `Types` lists the registered types and `Declaration` renders one at a time, so a test can marshal a zero value of each type and compare its keys with the declared fields:

```go
r := registry.TypeScript()

for _, typ := range r.Types() {
	declaration, err := r.Declaration(typ, 0)
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(reflect.New(typ).Interface())
	// Compare encoded's keys with the fields in declaration...
}
```

## Available Methods

| Function / Method | Description |
|-------------------|-------------|
| `typescript.NewRegistry()` | An empty registry. |
| `Namespace(pkgPath, namespace)` | Map a Go package to a TypeScript namespace. |
| `Add(values...)` | Declare the types of the given values (pass zero values: `flight.FareRuleData{}`). |
| `Render()` | The whole declaration file. |
| `Declaration(t, depth)` | One type's `export type X = {...};` block, indented `depth` levels. |
| `Name(t)` | A type's qualified TypeScript name, such as `App.Data.Flight.FareRuleData`. |
| `Types()` | The registered types, in registration order (a copy). |
| `typescript.Declarations(source)` | A declaration file's `export type` blocks, keyed by qualified type name. |

`Declaration` and `Name` take a `reflect.Type`:

```go
name, err := r.Name(reflect.TypeFor[flight.FareRuleData]()) // App.Data.Flight.FareRuleData
block, err := r.Declaration(reflect.TypeFor[flight.FareRuleData](), 0)
```

```ts
export type FareRuleData = {
    title: string;
    description: string;
};
```
