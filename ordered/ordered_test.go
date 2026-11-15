package ordered

import (
	"encoding/json"
	"testing"
)

func TestDecodeKeepsKeyOrderAndNumberSpelling(t *testing.T) {
	value, err := Decode([]byte(`{"b": 1, "a": {"z": 2.0, "y": [1, "x"]}, "b": 3}`))
	if err != nil {
		t.Fatal(err)
	}

	object := value.(*Object)
	if keys := object.Keys(); len(keys) != 2 || keys[0] != "b" || keys[1] != "a" {
		t.Fatalf("keys = %v (a repeated key keeps its first position)", keys)
	}
	if b, _ := object.Get("b"); b != json.Number("3") {
		t.Fatalf("b = %#v, want the last value as a json.Number", b)
	}

	nested, _ := object.Get("a")
	if z, _ := nested.(*Object).Get("z"); z != json.Number("2.0") {
		t.Fatalf("z = %#v, want its literal spelling", z)
	}
}

func TestDecodeRejectsTrailingData(t *testing.T) {
	for _, raw := range []string{`{} {}`, `[1] x`, `{"a":`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("Decode(%s) succeeded", raw)
		}
	}
}

func TestObjectMarshalsWithEncodingJSONInKeyOrder(t *testing.T) {
	object := &Object{}
	object.Set("z", "a/é<")
	object.Set("a", []any{json.Number("1.50"), nil})
	object.Set("z", "replaced")
	object.Set("empty", NewObject())

	got, err := json.Marshal(map[string]any{"o": object})
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"o":{"z":"replaced","a":[1.50,null],"empty":{}}}`; string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestObjectUnmarshalsAsAStructField(t *testing.T) {
	var payload struct {
		Arguments *Object `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(`{"arguments":{"to":"LHR","from":"JFK"}}`), &payload); err != nil {
		t.Fatal(err)
	}

	if keys := payload.Arguments.Keys(); len(keys) != 2 || keys[0] != "to" {
		t.Fatalf("keys = %v", keys)
	}

	if err := json.Unmarshal([]byte(`{"arguments":[1]}`), &payload); err == nil {
		t.Fatal("a list decoded into an Object")
	}
}

func TestNilAndZeroObjectsReadAsEmpty(t *testing.T) {
	var missing *Object
	if missing.Len() != 0 || missing.Has("a") || len(missing.Values()) != 0 || missing.Except().Len() != 0 {
		t.Fatal("a nil object must read as empty")
	}

	object := Object{}
	object.Set("a", 1)
	if values := object.Values(); len(values) != 1 || values[0] != 1 {
		t.Fatalf("values = %v", values)
	}
	if except := object.Except("a"); except.Len() != 0 {
		t.Fatalf("except kept %v", except.Keys())
	}
}
