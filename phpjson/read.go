package phpjson

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/support"
)

// IsArray reports a decoded object or list, what PHP decoded into an array.
func IsArray(value any) bool {
	switch value.(type) {
	case *ordered.Object, []any:
		return true
	}

	return false
}

// IsList reports a list, or an object keyed 0..n-1 in order (which the
// dialect writes as a list).
func IsList(value any) bool {
	switch v := value.(type) {
	case []any:
		return true
	case *ordered.Object:
		for i, key := range v.Keys() {
			if key != strconv.Itoa(i) {
				return false
			}
		}

		return true
	}

	return false
}

// Values are an object's or list's values in order (nil for anything else).
func Values(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case *ordered.Object:
		return v.Values()
	}

	return nil
}

// Count is an object's or list's length (0 for anything else).
func Count(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case *ordered.Object:
		return v.Len()
	}

	return 0
}

// Get reads key with dot notation: the literal key first, then each
// segment in turn, through objects and lists alike.
func Get(value any, key string) (any, bool) {
	if object, ok := value.(*ordered.Object); ok {
		if found, ok := object.Get(key); ok {
			return found, true
		}
	}

	current := value
	for segment := range strings.SplitSeq(key, ".") {
		next, ok := child(current, segment)
		if !ok {
			return nil, false
		}
		current = next
	}

	return current, true
}

// Value is Get without the presence flag: nil when absent.
func Value(value any, key string) any {
	found, _ := Get(value, key)

	return found
}

func child(value any, segment string) (any, bool) {
	switch v := value.(type) {
	case *ordered.Object:
		return v.Get(segment)
	case []any:
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= len(v) || strconv.Itoa(index) != segment {
			return nil, false
		}

		return v[index], true
	}

	return nil, false
}

// String is a decoded value as PHP's (string) cast spells it: an integer as
// written, a float as FloatString, true "1", false and null "", and an
// object or list "Array".
func String(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		if i, ok := WholeNumber(v); ok {
			return strconv.Itoa(i)
		}
		f, _ := v.Float64()

		return FloatString(f)
	case bool:
		if v {
			return "1"
		}

		return ""
	case nil:
		return ""
	}

	return "Array"
}

// Int is a decoded value as PHP's (int) cast reads it: floats truncate, a
// string takes its longest numeric prefix, truncated (0 when there is none),
// true is 1.
func Int(value any) int {
	switch v := value.(type) {
	case json.Number:
		if i, ok := WholeNumber(v); ok {
			return i
		}
		f, _ := v.Float64()

		return support.FloatToInt(f)
	case string:
		for end := len(v); end > 0; end-- {
			if support.IsNumeric(v[:end]) {
				return support.NumericToInt(v[:end])
			}
		}

		return 0
	case bool:
		if v {
			return 1
		}
	case int:
		return v
	case float64:
		return support.FloatToInt(v)
	}

	return 0
}

// WholeNumber reports whether a decoded number is written as an integer
// within int64 range, and its value.
func WholeNumber(n json.Number) (int, bool) {
	if strings.ContainsAny(string(n), ".eE") {
		return 0, false
	}

	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		return 0, false
	}

	return int(i), true
}
