package jsonschema_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/atobeach/reef/jsonschema"
)

type tier string

func (tier) Enum() []string { return []string{"gold", "silver"} }

type stop struct {
	Name  string `json:"name" description:"Where, e.g. \"Lisbon\""`
	Hours int    `json:"hours"`
}

type plan struct {
	Title    string   `json:"title" description:"A short title"`
	Note     *string  `json:"note" description:"Or null"`
	Tier     tier     `json:"tier"`
	Tags     []string `json:"tags" description:"Labels" items:"One word" minItems:"1" maxItems:"3"`
	Stops    []stop   `json:"stops"`
	Budget   float64  `json:"budget"`
	Flexible bool     `json:"flexible"`
	Ignored  string   `json:"-"`
}

// For writes the schema a hand-built one would: every field a required
// property named by its json tag, pointers nullable, tags describing and
// bounding, an Enum type restricting.
func TestForDerivesTheSchemaFromTheStruct(t *testing.T) {
	derived := jsonschema.ObjectSchema(jsonschema.For[plan]())

	built := jsonschema.ObjectSchema(jsonschema.Properties{
		{Name: "title", Type: jsonschema.String().Description("A short title").Required()},
		{Name: "note", Type: jsonschema.String().Description("Or null").Nullable().Required()},
		{Name: "tier", Type: jsonschema.String().EnumStrings([]string{"gold", "silver"}).Required()},
		{Name: "tags", Type: jsonschema.Array().Items(jsonschema.String().Description("One word")).Min(1).Max(3).Description("Labels").Required()},
		{Name: "stops", Type: jsonschema.Array().Items(jsonschema.Object(jsonschema.Properties{
			{Name: "name", Type: jsonschema.String().Description(`Where, e.g. "Lisbon"`).Required()},
			{Name: "hours", Type: jsonschema.Integer().Required()},
		})).Required()},
		{Name: "budget", Type: jsonschema.Number().Required()},
		{Name: "flexible", Type: jsonschema.Boolean().Required()},
	})

	got, _ := json.Marshal(derived)
	want, _ := json.Marshal(built)
	if string(got) != string(want) {
		t.Fatalf("derived:\n%s\nbuilt:\n%s", got, want)
	}
}

// An embedded struct's fields are the parent's own, as encoding/json
// decodes them; a named embedded struct is a nested property.
func TestForSplicesEmbeddedStructs(t *testing.T) {
	type Base struct {
		ID string `json:"id"`
	}
	type audited struct {
		By string `json:"by"`
	}
	type record struct {
		Base
		*audited
		Owner Base `json:"owner"`
		Named Base `json:"named"`
	}

	derived := jsonschema.ObjectSchema(jsonschema.For[record]())
	idOnly := jsonschema.Properties{{Name: "id", Type: jsonschema.String().Required()}}
	built := jsonschema.ObjectSchema(jsonschema.Properties{
		{Name: "id", Type: jsonschema.String().Required()},
		{Name: "by", Type: jsonschema.String().Required()},
		{Name: "owner", Type: jsonschema.Object(idOnly).Required()},
		{Name: "named", Type: jsonschema.Object(idOnly).Required()},
	})

	got, _ := json.Marshal(derived)
	want, _ := json.Marshal(built)
	if string(got) != string(want) {
		t.Fatalf("derived:\n%s\nbuilt:\n%s", got, want)
	}
}

type counted int

func (counted) Enum() []string { return []string{"one"} }

// For refuses every type whose schema would describe something other than
// what encoding/json decodes into it.
func TestForRefusesWhatItCannotDescribe(t *testing.T) {
	type inner struct {
		ID string `json:"id"`
	}

	cases := map[string]func(){
		"a map":          func() { jsonschema.For[struct{ Extra map[string]string }]() },
		"an interface":   func() { jsonschema.For[struct{ Extra any }]() },
		"bytes":          func() { jsonschema.For[struct{ Raw []byte }]() },
		"a self-decoder": func() { jsonschema.For[struct{ At time.Time }]() },
		"a raw message":  func() { jsonschema.For[struct{ Raw json.RawMessage }]() },
		"the string option": func() {
			jsonschema.For[struct {
				Count int `json:"count,string"`
			}]()
		},
		"a struct without fields": func() { jsonschema.For[struct{ Empty struct{} }]() },
		"no fields at all":        func() { jsonschema.For[struct{ hidden string }]() },
		"two fields of one name": func() {
			jsonschema.For[struct {
				inner
				ID string `json:"id"`
			}]()
		},
		"a non-string enum": func() { jsonschema.For[struct{ N counted }]() },
		"not a struct":      func() { jsonschema.For[[]string]() },
	}

	for name, describe := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("For accepted it")
				}
			}()

			describe()
		})
	}
}

// The bounds serialize in the fixed key order, ahead of the type.
func TestBoundsSerializeAheadOfTheType(t *testing.T) {
	for _, c := range []struct {
		node jsonschema.Type
		want string
	}{
		{jsonschema.String().Description("A place").Max(80), `{"description":"A place","maxLength":80,"type":"string"}`},
		{jsonschema.Number().Min(1).Max(99999999).Nullable(), `{"minimum":1,"maximum":99999999,"type":["number","null"]}`},
	} {
		if got, _ := json.Marshal(c.node.Serialize()); string(got) != c.want {
			t.Errorf("serialized %s, want %s", got, c.want)
		}
	}
}
