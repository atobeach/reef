package phpjson

import (
	"encoding/json"
	"testing"

	"github.com/atobeach/reef/ordered"
)

// bs is a literal backslash, so the expected \u escapes stay escapes here.
const bs = "\\"

// The round trip normalises stored JSON to the established encoding: `{}`
// and 0..n-1-keyed objects as lists, whole floats as ints, escaped slashes
// and \u-escaped non-ASCII. Stored rows and fingerprints depend on these
// exact bytes.
func TestRoundTripWritesTheEstablishedEncoding(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			`{"a":{},"b":[],"c":{"0":"x","1":"y"},"d":{"1":"x"}}`,
			`{"a":[],"b":[],"c":["x","y"],"d":{"1":"x"}}`,
		},
		{
			`[1.0,0.1,1e25,1e-5,0.0001,123456789012345678901,-0.0,2.5e17,1.5e18,3.0e-4,100]`,
			`[1,0.1,1.0e+25,1.0e-5,0.0001,1.2345678901234568e+20,-0,2.5e+17,1.5e+18,0.0003,100]`,
		},
		{
			`{"url":"https://x.test/a","t":"Café ☕ 😀 \u0001","q":"\"<>&"}`,
			`{"url":"https:\/\/x.test\/a","t":"Caf` + bs + `u00e9 ` + bs + `u2615 ` + bs + `ud83d` + bs + `ude00 \u0001","q":"\"<>&"}`,
		},
		{
			`[ {"type": "booking", "id": "x", "saved": false} ]`,
			`[{"type":"booking","id":"x","saved":false}]`,
		},
	}

	for _, c := range cases {
		got, err := Reencode([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}

		if string(got) != c.want {
			t.Errorf("reencode %s\n got  %s\n want %s", c.in, got, c.want)
		}
	}
}

func TestObjectsKeepKeyOrderAndExcept(t *testing.T) {
	decoded, err := ordered.Decode([]byte(`{"z":1,"a":2,"result":{"n":3}}`))
	if err != nil {
		t.Fatal(err)
	}

	object := decoded.(*ordered.Object)
	if !object.Has("result") {
		t.Fatal("result missing")
	}

	got, _ := Encode(object.Except("result"))
	if string(got) != `{"z":1,"a":2}` {
		t.Fatalf("except = %s", got)
	}

	object.Set("saved", true)
	got, _ = Encode(object)
	if string(got) != `{"z":1,"a":2,"result":{"n":3},"saved":true}` {
		t.Fatalf("set = %s", got)
	}
}

// The empty struct stays an object where an empty or list-keyed
// *ordered.Object collapses to a list.
func TestOnlyTheEmptyStructForcesAnEmptyObject(t *testing.T) {
	keyed := ordered.NewObject()
	keyed.Set("0", "x")

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"empty struct", struct{}{}, `{}`},
		{"empty object", ordered.NewObject(), `[]`},
		{"list-keyed object", keyed, `["x"]`},
	}

	for _, c := range cases {
		got, err := Encode(c.value)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		if string(got) != c.want {
			t.Errorf("%s = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestEncodeSubstituteReplacesInvalidUTF8(t *testing.T) {
	object := ordered.NewObject()
	object.Set("k\xff", []any{"a\xffb"})

	got, err := EncodeSubstitute(object)
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"k` + bs + `ufffd":["a` + bs + `ufffdb"]}`; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	if _, err := Encode(object); err == nil {
		t.Fatal("Encode accepted invalid UTF-8")
	}
}

// Marshal keeps encoding/json's structure (struct tags, sorted map keys, an
// empty object as `{}`) and respells only strings and floats.
func TestMarshalRespellsEncodingJSONOutput(t *testing.T) {
	got, err := Marshal(map[string]any{
		"city":  "São Paulo/Centro <&>",
		"lat":   0.00001,
		"emoji": "\U0001F600",
		"empty": ordered.NewObject(),
		"tags": struct {
			Name string `json:"name"`
		}{"a/b"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"city":"S` + bs + `u00e3o Paulo\/Centro <&>","emoji":"` + bs + `ud83d` + bs + `ude00","empty":{},"lat":1.0e-5,"tags":{"name":"a\/b"}}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestFloatSpellings(t *testing.T) {
	cast := map[float64]string{4.5: "4.5", 5: "5", 1e15: "1.0E+15", 1e14: "1.0E+14", 0.00001: "1.0E-5", 123456789012345: "1.2345678901234E+14", 0.1 + 0.2: "0.3", -0.004: "-0.004"}
	for value, want := range cast {
		if got := FloatString(value); got != want {
			t.Errorf("(string) %v = %q, want %q", value, got, want)
		}
	}

	encoded := map[float64]string{1e14: "100000000000000", 1e15: "1000000000000000", 0.0001: "0.0001", 0.00001: "1.0e-5", 1: "1", 12.30: "12.3", 1e20: "1.0e+20", 1.5e-7: "1.5e-7"}
	for value, want := range encoded {
		if got := jsonFloat(value); got != want {
			t.Errorf("json_encode(%v) = %q, want %q", value, got, want)
		}
	}
}

func TestReadsFollowPHPArraySemantics(t *testing.T) {
	value, err := ordered.Decode([]byte(`{"b": 1, "a": {"z": 2.0, "y": [1, "x"]}, "b.c": "literal"}`))
	if err != nil {
		t.Fatal(err)
	}

	if Value(value, "b") != json.Number("1") || Value(value, "a.y.1") != "x" || Value(value, "a.y.2") != nil {
		t.Fatal("dot-notation lookups")
	}
	if Value(value, "b.c") != "literal" {
		t.Fatal("the literal key wins over the dot path")
	}
	if _, ok := WholeNumber(Value(value, "a.z").(json.Number)); ok {
		t.Fatal("2.0 decodes as a float, not an integer")
	}
	if Count(Value(value, "a")) != 2 || len(Values(Value(value, "a.y"))) != 2 || !IsArray(Value(value, "a")) || IsArray("x") {
		t.Fatal("array reads")
	}

	keyed, _ := ordered.Decode([]byte(`{"0": "a", "1": "b"}`))
	sparse, _ := ordered.Decode([]byte(`{"1": "a"}`))
	if !IsList(keyed) || IsList(sparse) {
		t.Fatal("array_is_list on objects")
	}
}

func TestCastsSpellValuesAsPHPDoes(t *testing.T) {
	strings := []struct {
		value any
		want  string
	}{
		{json.Number("2.0"), "2"},
		{json.Number("12"), "12"},
		{json.Number("1e20"), "1.0E+20"},
		{true, "1"},
		{false, ""},
		{nil, ""},
		{"x", "x"},
		{[]any{}, "Array"},
		{ordered.NewObject(), "Array"},
	}
	for _, c := range strings {
		if got := String(c.value); got != c.want {
			t.Errorf("String(%#v) = %q, want %q", c.value, got, c.want)
		}
	}

	ints := []struct {
		value any
		want  int
	}{
		{json.Number("7.9"), 7}, {"12abc", 12}, {" 1e3x", 1000}, {"abc", 0}, {true, 1}, {nil, 0}, {[]any{1}, 0}, {3, 3}, {-2.7, -2},
	}
	for _, c := range ints {
		if got := Int(c.value); got != c.want {
			t.Errorf("Int(%#v) = %d, want %d", c.value, got, c.want)
		}
	}
}
