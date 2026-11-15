package validation

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/atobeach/reef/validation/rule"
)

// sample is a decoded JSON body, numbers kept as json.Number as the request
// layer keeps them.
var sample = Input{
	"date":   "2026-10-21",
	"count":  json.Number("3"),
	"on":     true,
	"off":    false,
	"none":   nil,
	"blank":  "  ",
	"list":   []any{"a"},
	"empty":  []any{},
	"nested": map[string]any{"zone": "Europe/Athens"},
	"passengers": []any{
		map[string]any{"first_name": "Ana", "nickname": nil},
	},
	"keyed":      map[string]any{"1": "b", "0": "a"},
	"gap":        map[string]any{"0": "a", "2": "c"},
	"named":      map[string]any{"a": "x"},
	"object":     map[string]any{},
	"checkbox":   "on",
	"agreed":     "Yes ",
	"declined":   "no",
	"one":        json.Number("1"),
	"two":        json.Number("2"),
	"one_float":  float64(1),
	"one_string": "1",
}

func TestValueReadsDottedKeysThroughMapsAndLists(t *testing.T) {
	for key, want := range map[string]any{
		"date":                     "2026-10-21",
		"nested.zone":              "Europe/Athens",
		"passengers.0.first_name":  "Ana",
		"passengers.1.first_name":  nil,
		"passengers.-1.first_name": nil,
		"date.year":                nil,
		"absent":                   nil,
	} {
		if got := sample.Value(key); got != want {
			t.Errorf("Value(%q) = %#v, want %#v", key, got, want)
		}
	}
}

func TestHasCountsANullAsPresent(t *testing.T) {
	cases := []struct {
		keys []string
		want bool
	}{
		{[]string{"none"}, true},
		{[]string{"passengers.0.nickname"}, true},
		{[]string{"passengers.0.first_name"}, true},
		{[]string{"passengers.1"}, false},
		{[]string{"absent"}, false},
		{[]string{"date", "none"}, true},
		{[]string{"date", "absent"}, false},
	}

	for _, tc := range cases {
		if got := sample.Has(tc.keys...); got != tc.want {
			t.Errorf("Has(%q) = %v, want %v", tc.keys, got, tc.want)
		}
	}
}

func TestFilledRefusesNullBlankAndEmpty(t *testing.T) {
	for key, want := range map[string]bool{
		"date": true, "count": true, "off": true, "list": true, "nested": true,
		"none": false, "blank": false, "empty": false, "object": false, "absent": false,
	} {
		if got := sample.Filled(key); got != want {
			t.Errorf("Filled(%q) = %v, want %v", key, got, want)
		}
	}
}

// Text for scalars, true as "1"; "" for anything else.
func TestStringReadsScalarsAsText(t *testing.T) {
	for key, want := range map[string]string{
		"date": "2026-10-21", "count": "3", "on": "1", "off": "", "none": "", "list": "", "absent": "",
		"nested.zone": "Europe/Athens",
	} {
		if got := sample.String(key); got != want {
			t.Errorf("String(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestBooleanReadsCheckboxValues(t *testing.T) {
	for key, want := range map[string]bool{
		"on": true, "checkbox": true, "agreed": true, "one": true, "one_float": true, "one_string": true,
		"off": false, "declined": false, "two": false, "date": false, "none": false, "absent": false,
	} {
		if got := sample.Boolean(key); got != want {
			t.Errorf("Boolean(%q) = %v, want %v", key, got, want)
		}
	}
}

// A list, or an object keyed "0".."n-1" read in index order; a gap, a named
// key or a scalar is no list.
func TestArrayReadsListsAndIndexKeyedObjects(t *testing.T) {
	cases := []struct {
		key  string
		want []any
		ok   bool
	}{
		{"list", []any{"a"}, true},
		{"empty", []any{}, true},
		{"keyed", []any{"a", "b"}, true},
		{"object", []any{}, true},
		{"gap", nil, false},
		{"named", nil, false},
		{"date", nil, false},
		{"none", nil, false},
		{"absent", nil, false},
	}

	for _, tc := range cases {
		got, ok := sample.Array(tc.key)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Array(%q) = %#v, %v; want %#v, %v", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

// An after hook reads the input it validates with the same readers.
func TestTheValidatorReadsItsInput(t *testing.T) {
	v := New(t.Context(), sample, rule.Set{}, Options{})

	passengers, ok := v.Array("keyed")
	if v.Input("passengers.0.first_name") != "Ana" || !v.Has("none") || v.String("on") != "1" || !ok || len(passengers) != 2 {
		t.Fatalf("Input = %v, Has = %v, String = %q, Array = %v, %v",
			v.Input("passengers.0.first_name"), v.Has("none"), v.String("on"), passengers, ok)
	}
}
