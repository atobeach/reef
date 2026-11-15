package rule

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// String requires a string.
var String = Rule{
	Name:    "string",
	check:   func(f *Field) bool { _, ok := f.Value.(string); return ok },
	message: fixed("The :attribute field must be a string."),
}

// Integer accepts ints, whole floats and digit strings, since query strings
// carry numbers as text.
var Integer = Rule{
	Name:    "integer",
	check:   func(f *Field) bool { _, ok := asInt(f.Value); return ok },
	message: fixed("The :attribute field must be an integer."),
}

// Numeric accepts numbers and decimal number strings.
var Numeric = Rule{
	Name:    "numeric",
	check:   func(f *Field) bool { _, ok := asFloat(f.Value); return ok },
	message: fixed("The :attribute field must be a number."),
}

// Boolean accepts true, false, 1, 0, "1", "0".
var Boolean = Rule{
	Name: "boolean",
	check: func(f *Field) bool {
		switch v := f.Value.(type) {
		case bool:
			return true
		case string:
			return v == "0" || v == "1"
		default:
			n, ok := asInt(v)
			return ok && (n == 0 || n == 1)
		}
	},
	message: fixed("The :attribute field must be true or false."),
}

// Array requires a list or an object.
var Array = Rule{
	Name:    "array",
	check:   func(f *Field) bool { return isArray(f.Value) },
	message: fixed("The :attribute field must be an array."),
}

// ArrayKeys requires an object whose keys are all allowed; a list passes
// when its indexes are allowed keys.
func ArrayKeys(keys ...string) Rule {
	return Rule{
		Name: "array",
		check: func(f *Field) bool {
			switch v := f.Value.(type) {
			case map[string]any:
				for key := range v {
					if !slices.Contains(keys, key) {
						return false
					}
				}

				return true
			case []any:
				for i := range v {
					if !slices.Contains(keys, strconv.Itoa(i)) {
						return false
					}
				}

				return true
			default:
				return false
			}
		},
		message: fixed("The :attribute field must be an array."),
	}
}

func isArray(value any) bool {
	switch value.(type) {
	case []any, map[string]any:
		return true
	}

	return false
}

// asInt rejects a leading "+" and anything fractional, so "+3" and "1.5"
// fail the integer rule.
func asInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), v == float64(int64(v))
	case string:
		trimmed := strings.TrimSpace(v)
		n, err := strconv.ParseInt(trimmed, 10, 64)
		return n, err == nil && !strings.HasPrefix(trimmed, "+")
	case interface{ Int64() (int64, error) }:
		n, err := v.Int64()
		return n, err == nil
	}

	return 0, false
}

// asFloat accepts decimal notation only: ParseFloat's hex, underscore, Inf
// and NaN spellings are not numbers a user means.
func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case string:
		n, err := strconv.ParseFloat(strings.TrimLeft(v, " \t\n\r\v\f"), 64)
		trimmed := strings.TrimSpace(v)
		return n, err == nil && !strings.ContainsAny(v, "xXpP_") && !strings.EqualFold(trimmed, "inf") && !strings.EqualFold(trimmed, "nan")
	case interface{ Float64() (float64, error) }:
		n, err := v.Float64()
		return n, err == nil
	}

	return 0, false
}

// scalarString renders a value for comparison with string options and
// string lengths; booleans read as "1"/"0", as form inputs submit them.
func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "1"
		}
		return "0"
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
