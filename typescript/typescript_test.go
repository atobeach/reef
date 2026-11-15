package typescript

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type airport struct {
	Iata string `json:"iata"`
}

type guide struct {
	ID       int64             `json:"id"`
	Name     string            `json:"name"`
	Region   *string           `json:"region"`
	Hero     bool              `json:"hero"`
	Tips     []string          `json:"tips"`
	Maybe    *[]string         `json:"maybe"`
	Spf      map[string]string `json:"spf"`
	Airports []airport         `json:"airports"`
	Extra    json.RawMessage   `json:"extra"`
	Legacy   []any             `json:"legacy" ts:"Record<number | string, any> | null"`
	Times    struct {
		Outbound *string `json:"outbound,omitempty"`
	} `json:"times"`
	// hidden is unexported, so it is never declared.
	hidden string
}

type legs struct {
	Items []struct {
		Arrival *[]string `json:"arrival"`
	} `json:"items"`
	Codes []*string `json:"codes"`
}

// An inline object's nullable fields don't make the list a union: only a
// nullable element type is parenthesised.
func TestParenthesisesOnlyUnionElementTypes(t *testing.T) {
	r := NewRegistry()
	r.Namespace("github.com/atobeach/reef/typescript", "App.Data")

	got, err := r.Declaration(reflect.TypeFor[legs](), 0)
	if err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"export type legs = {",
		"    items: {",
		"        arrival: string[] | null;",
		"    }[];",
		"    codes: (string | null)[];",
		"};",
	}, "\n")
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestDeclaresEveryFieldKind(t *testing.T) {
	r := NewRegistry()
	r.Namespace("github.com/atobeach/reef/typescript", "App.Data")
	r.Add(guide{hidden: "not declared"}, airport{})

	got, err := r.Declaration(reflect.TypeFor[guide](), 2)
	if err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"        export type guide = {",
		"            id: number;",
		"            name: string;",
		"            region: string | null;",
		"            hero: boolean;",
		"            tips: string[];",
		"            maybe: string[] | null;",
		"            spf: Record<string, string>;",
		"            airports: App.Data.airport[];",
		"            extra: any;",
		"            legacy: Record<number | string, any> | null;",
		"            times: {",
		"                outbound?: string | null;",
		"            };",
		"        };",
	}, "\n")
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestIndexesAnExistingFile(t *testing.T) {
	source := "declare namespace App {\n    namespace Data {\n        export type A = {\n            x: {\n                y: string;\n            };\n        };\n        namespace City {\n            export type B = {\n                z: number;\n            };\n        }\n    }\n}\n"

	found := Declarations(source)
	if !strings.Contains(found["App.Data.A"], "y: string;") || !strings.Contains(found["App.Data.City.B"], "z: number;") || len(found) != 2 {
		t.Fatalf("found = %#v", found)
	}
}

func TestRenderRefusesATypeRegisteredTwice(t *testing.T) {
	r := NewRegistry()
	r.Namespace("github.com/atobeach/reef/typescript", "App.Data")
	r.Add(airport{}, airport{})

	if _, err := r.Render(); err == nil || !strings.Contains(err.Error(), "App.Data.airport is registered twice") {
		t.Fatalf("err = %v", err)
	}
}
