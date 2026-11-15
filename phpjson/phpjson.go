// Package phpjson is the PHP-compatible JSON dialect that stored columns,
// fingerprints and cache keys already hold: "/" escaped, non-ASCII
// \u-escaped, whole floats without ".0", and an empty or 0..n-1-keyed object
// written as a list. Get, Value, String and Int read decoded values with
// PHP's array semantics. It is a compatibility layer: use encoding/json for
// anything whose bytes have no such history.
package phpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/atobeach/reef/ordered"
)

// Encode writes a decoded value in the dialect. It takes what ordered.Decode
// returns plus strings, ints, float64s, []string, map[string]any (keys
// sorted), json.RawMessage and struct{} (`{}`, the one empty object not
// written as a list); anything else is an error, so no struct slips in with
// encoding/json's spelling.
func Encode(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := encode(&out, value); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// Reencode is ordered.Decode then Encode: stored JSON normalised to the
// dialect.
func Reencode(raw []byte) (json.RawMessage, error) {
	value, err := ordered.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("phpjson: %w", err)
	}

	return Encode(value)
}

func encode(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case string:
		return encodeString(out, v)
	case json.Number:
		return encodeNumber(out, v)
	case int:
		out.WriteString(strconv.Itoa(v))
	case int64:
		out.WriteString(strconv.FormatInt(v, 10))
	case float64:
		return encodeFloat(out, v)
	case json.RawMessage:
		decoded, err := ordered.Decode(v)
		if err != nil {
			return fmt.Errorf("phpjson: %w", err)
		}

		return encode(out, decoded)
	case []any:
		return encodeList(out, v)
	case *ordered.Object:
		return encodeObject(out, v)
	case struct{}:
		out.WriteString("{}")
	case []string:
		list := make([]any, len(v))
		for i, item := range v {
			list[i] = item
		}

		return encodeList(out, list)
	case map[string]any:
		return encodeObject(out, sortedObject(v))
	default:
		return fmt.Errorf("phpjson: cannot encode %T", value)
	}

	return nil
}

// sortedObject orders a Go map's keys, so its encoding is deterministic.
func sortedObject(m map[string]any) *ordered.Object {
	object := ordered.NewObject()
	for _, key := range slices.Sorted(maps.Keys(m)) {
		object.Set(key, m[key])
	}

	return object
}

func encodeList(out *bytes.Buffer, list []any) error {
	out.WriteByte('[')
	for i, item := range list {
		if i > 0 {
			out.WriteByte(',')
		}
		if err := encode(out, item); err != nil {
			return err
		}
	}
	out.WriteByte(']')

	return nil
}

// encodeObject writes an empty or 0..n-1-keyed object as a list, as stored
// bytes have `{}` and `{"0":…}`.
func encodeObject(out *bytes.Buffer, object *ordered.Object) error {
	if !IsList(object) {
		return encodeMembers(out, object)
	}

	return encodeList(out, object.Values())
}

// encodeMembers writes the object as `{…}`.
func encodeMembers(out *bytes.Buffer, object *ordered.Object) error {
	out.WriteByte('{')
	for i, key := range object.Keys() {
		if i > 0 {
			out.WriteByte(',')
		}
		if err := encodeString(out, key); err != nil {
			return err
		}
		out.WriteByte(':')
		value, _ := object.Get(key)
		if err := encode(out, value); err != nil {
			return err
		}
	}
	out.WriteByte('}')

	return nil
}

// encodeNumber keeps integers that fit an int64 as ints; anything else
// (fractions, exponents, overflow) is written as a float.
func encodeNumber(out *bytes.Buffer, n json.Number) error {
	literal := n.String()
	if !strings.ContainsAny(literal, ".eE") {
		if i, err := strconv.ParseInt(literal, 10, 64); err == nil {
			out.WriteString(strconv.FormatInt(i, 10))

			return nil
		}
	}

	f, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		return err
	}

	return encodeFloat(out, f)
}

// encodeFloat writes jsonFloat's spelling: the shortest round-tripping
// digits, never a trailing ".0" on whole numbers (so 2.0 is stored as 2).
func encodeFloat(out *bytes.Buffer, f float64) error {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return errors.New("phpjson: Inf and NaN cannot be JSON encoded")
	}

	out.WriteString(jsonFloat(f))

	return nil
}

func encodeString(out *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return errors.New("phpjson: malformed UTF-8")
	}

	out.WriteString(Quote(s))

	return nil
}
