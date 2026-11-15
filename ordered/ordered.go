// Package ordered is a JSON object that keeps its keys in the order they
// were set or decoded, and a decoder that produces one, for JSON that is
// re-encoded byte for byte or read in the sender's order. An Object marshals
// with encoding/json; another encoder (reef/phpjson) reads it through Keys
// and Get.
package ordered

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Object is a JSON object whose keys keep their insertion order. The zero
// value is an empty object ready to use, and a nil *Object reads as empty.
type Object struct {
	keys   []string
	values map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{values: map[string]any{}}
}

// Set adds or replaces a key; a new key goes last, a replaced one keeps its
// position.
func (o *Object) Set(key string, value any) {
	if o.values == nil {
		o.values = map[string]any{}
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Get is the value at key and whether it is present.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}

	value, ok := o.values[key]

	return value, ok
}

// Has reports whether the key is present, even with a null value.
func (o *Object) Has(key string) bool {
	_, ok := o.Get(key)

	return ok
}

// Keys are the keys in order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}

	return o.keys
}

// Values are the values in key order.
func (o *Object) Values() []any {
	values := make([]any, 0, o.Len())
	for _, key := range o.Keys() {
		values = append(values, o.values[key])
	}

	return values
}

// Len is the number of keys.
func (o *Object) Len() int {
	return len(o.Keys())
}

// Except is a shallow copy without the given keys.
func (o *Object) Except(drop ...string) *Object {
	out := NewObject()
	for _, key := range o.Keys() {
		if !slices.Contains(drop, key) {
			out.Set(key, o.values[key])
		}
	}

	return out
}

// MarshalJSON writes the object with encoding/json, in key order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range o.Keys() {
		if i > 0 {
			out.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("ordered: encoding key %q: %w", key, err)
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		value, err := json.Marshal(o.values[key])
		if err != nil {
			return nil, fmt.Errorf("ordered: encoding %q: %w", key, err)
		}
		out.Write(value)
	}
	out.WriteByte('}')

	return out.Bytes(), nil
}

// UnmarshalJSON decodes a JSON object as Decode does, so an Object can be a
// field of a struct encoding/json fills.
func (o *Object) UnmarshalJSON(raw []byte) error {
	decoded, err := Decode(raw)
	if err != nil {
		return err
	}

	object, ok := decoded.(*Object)
	if !ok {
		return fmt.Errorf("ordered: want a JSON object, got %T", decoded)
	}
	*o = *object

	return nil
}

// Decode reads one JSON value: objects become *Object, arrays []any, numbers
// json.Number (so an integer stays distinguishable from a float), plus
// string, bool and nil. A key repeated within an object keeps its first
// position and its last value.
func Decode(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	value, err := decodeValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("ordered: %w", err)
	}

	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("ordered: trailing data after the JSON value")
	}

	return value, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}

	switch delim {
	case '{':
		return decodeObject(decoder)
	case '[':
		return decodeList(decoder)
	}

	return nil, fmt.Errorf("unexpected %v", delim)
}

func decodeObject(decoder *json.Decoder) (*Object, error) {
	object := NewObject()
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyToken.(string)
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		object.Set(key, value)
	}

	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	return object, nil
}

func decodeList(decoder *json.Decoder) ([]any, error) {
	list := []any{}
	for decoder.More() {
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		list = append(list, value)
	}

	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	return list, nil
}
