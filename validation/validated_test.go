package validation

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/atobeach/reef/validation/rule"
)

// Validated keeps a field the rules reached, whole and as sent; the
// passengers are validated element by element, so they come last.
func TestValidatedKeepsTheFieldsTheRulesReached(t *testing.T) {
	rules := rule.Set{
		"passengers":              {rule.Nullable, rule.Array},
		"passengers.*.first_name": {rule.Required, rule.String},
		"contact":                 {rule.Sometimes, rule.Array},
		"contact.email":           {rule.Nullable, rule.String},
		"options.bags":            {rule.Sometimes, rule.Integer},
		"note":                    {rule.Nullable, rule.String},
	}
	order := []string{"passengers", "passengers.*", "passengers.*.first_name", "contact", "contact.email", "options", "options.bags", "note"}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			"nested values kept whole",
			`{"note":null,"passengers":[{"first_name":"Ana","age":3}],"contact":{"email":"a@b.co","x":1},"junk":1}`,
			`{"contact":{"email":"a@b.co","x":1},"note":null,"passengers":[{"age":3,"first_name":"Ana"}]}`,
		},
		// A field reached only through a nested rule counts once the nested
		// key is present.
		{"nested rule reaches its field", `{"passengers":[],"options":{"bags":2}}`, `{"passengers":[],"options":{"bags":2}}`},
		{"nested rule without its key", `{"passengers":[],"options":{"seats":2}}`, `{"passengers":[]}`},
	}

	for _, tc := range cases {
		var input map[string]any
		if err := json.Unmarshal([]byte(tc.input), &input); err != nil {
			t.Fatal(err)
		}

		v := New(t.Context(), input, rules, Options{Order: order})
		if passed, err := v.Validate(); !passed || err != nil {
			t.Fatalf("%s: %s, %v", tc.name, v.Errors().Summary(), err)
		}

		got, err := json.Marshal(v.Validated())
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != tc.want {
			t.Errorf("%s: Validated() = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// An untagged embedded struct's fields decode as the outer struct's own.
func TestDecodePromotesEmbeddedStructFields(t *testing.T) {
	type Dates struct {
		DepartsOn string `json:"departs_on"`
	}

	type trip struct {
		Dates
		Passengers int `json:"passengers"`
	}

	var got trip
	if err := Decode(map[string]any{"departs_on": "2026-10-21", "passengers": "3", "Dates": "ignored"}, &got); err != nil {
		t.Fatal(err)
	}

	if got != (trip{Dates: Dates{DepartsOn: "2026-10-21"}, Passengers: 3}) {
		t.Fatalf("decoded = %+v", got)
	}

	if order := FieldOrder(reflect.TypeFor[trip]()); len(order) != 2 || order[0] != "departs_on" || order[1] != "passengers" {
		t.Fatalf("FieldOrder = %v", order)
	}
}

// The array rules count [] as an empty array whether keys or a list were
// meant, so it decodes into an object as an empty one; a non-empty list
// still doesn't.
func TestDecodeReadsAnEmptyListAsAnEmptyObject(t *testing.T) {
	type bands struct {
		Outbound *string `json:"outbound"`
	}

	type search struct {
		Times  bands             `json:"times"`
		Labels map[string]string `json:"labels"`
	}

	var got search
	if err := Decode(map[string]any{"times": []any{}, "labels": []any{}}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Times.Outbound != nil || len(got.Labels) != 0 {
		t.Fatalf("decoded = %+v", got)
	}

	if err := Decode(map[string]any{"times": []any{"06:00-11:59"}}, &got); err == nil {
		t.Fatal("a non-empty list is no object")
	}
}

// A bool field reads as Input.Boolean does, so a checkbox's "on" is true.
func TestDecodeReadsABoolAsACheckbox(t *testing.T) {
	type login struct {
		Remember bool `json:"remember"`
	}

	for value, want := range map[any]bool{
		"on": true, "Yes": true, "true": true, "1": true, json.Number("1"): true, true: true,
		"off": false, "0": false, json.Number("0"): false, false: false, "maybe": false, nil: false,
	} {
		got := login{Remember: !want}
		if err := Decode(map[string]any{"remember": value}, &got); err != nil || got.Remember != want {
			t.Errorf("remember %#v decoded as %v (%v), want %v", value, got.Remember, err, want)
		}
	}
}

// Decode fills every field whose value fits and names every path whose
// value doesn't; an object is no string's text.
func TestDecodeFillsWhatFitsAndNamesWhatDoesNot(t *testing.T) {
	type order struct {
		Name   string    `json:"name"`
		Items  []int     `json:"items"`
		Count  int       `json:"count"`
		Price  float64   `json:"price"`
		Placed time.Time `json:"placed"`
		Note   string    `json:"note"`
	}

	got := order{Name: "stale"}
	err := Decode(map[string]any{
		"name":   map[string]any{"a": json.Number("1")},
		"items":  map[string]any{"x": json.Number("1")},
		"count":  "many",
		"price":  "cheap",
		"placed": "yesterday",
		"note":   "fine",
	}, &got)

	if want := "validation: *validation.order can't hold the input at name, items, count, price, placed"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %s", err, want)
	}

	if !reflect.DeepEqual(got, order{Note: "fine"}) {
		t.Fatalf("decoded = %+v", got)
	}
}

// A list element that doesn't fit is left zero and named by its index; the
// rest of the list decodes.
func TestDecodeKeepsTheElementsThatFit(t *testing.T) {
	type picks struct {
		Codes []string `json:"codes"`
	}

	var got picks
	err := Decode(map[string]any{"codes": []any{"A", []any{"B"}, json.Number("3")}}, &got)

	if err == nil || err.Error() != "validation: *validation.picks can't hold the input at codes.1" || !reflect.DeepEqual(got.Codes, []string{"A", "", "3"}) {
		t.Fatalf("decoded %v, %v", got.Codes, err)
	}
}

// A value kept as sent (a type that decodes itself, a map, an any) is a path
// of its own, never walked into.
func TestFieldOrderStopsAtAValueKeptAsSent(t *testing.T) {
	type panel struct {
		Items []json.RawMessage   `json:"items"`
		Meta  map[string][]string `json:"meta"`
		Raw   any                 `json:"raw"`
	}

	if order := FieldOrder(reflect.TypeFor[panel]()); !reflect.DeepEqual(order, []string{"items", "items.*", "meta", "raw"}) {
		t.Fatalf("FieldOrder = %v", order)
	}
}
