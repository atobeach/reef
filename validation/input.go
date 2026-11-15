package validation

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Input is request input and its readers. A dotted key indexes maps and
// lists alike ("passengers.0.first_name"), as the rules' attributes do.
type Input map[string]any

// Value is the value at key, nil when absent.
func (in Input) Value(key string) any {
	value, _ := lookup(in, key)

	return value
}

// Has reports whether every key is present, even if null.
func (in Input) Has(keys ...string) bool {
	for _, key := range keys {
		if _, ok := lookup(in, key); !ok {
			return false
		}
	}

	return true
}

// Filled reports a key that is present and not blank: not null, not
// whitespace, not an empty list or object.
func (in Input) Filled(key string) bool {
	switch v := in.Value(key).(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}

	return true
}

// String is a scalar as text ("1" for true), "" for null, false or a list
// or object.
func (in Input) String(key string) string {
	switch typed := in.Value(key).(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "1"
		}
	}

	return ""
}

// Boolean is a checkbox-style flag: true for true, 1, "1", "true", "on" and
// "yes" (any case); false otherwise.
func (in Input) Boolean(key string) bool {
	return boolean(in.Value(key))
}

// boolean is Boolean's reading of one value, which Decode shares for a bool
// field.
func boolean(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on", "yes":
			return true
		}

		return false
	default:
		return scalar(v) == "1"
	}
}

// Array is the list at key: a list, or an object keyed "0".."n-1", which
// some clients send for one. ok is false for anything else, a missing key
// included.
func (in Input) Array(key string) ([]any, bool) {
	return asList(in.Value(key))
}

// asList reads a list, or an object keyed "0".."n-1" in index order.
func asList(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	case map[string]any:
		list := make([]any, len(v))
		for i := range list {
			item, ok := v[strconv.Itoa(i)]
			if !ok {
				return nil, false
			}
			list[i] = item
		}

		return list, true
	}

	return nil, false
}
