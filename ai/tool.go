package ai

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/support"
)

// Tool is a function the model may call. Its Name is also what stored tool
// calls name.
//
//	func (SwitchCurrency) Name() string        { return "SwitchCurrency" }
//	func (SwitchCurrency) Description() string { return "Switch the currency…" }
//	func (SwitchCurrency) Schema() jsonschema.Properties {
//		return jsonschema.Properties{
//			{Name: "currency", Type: jsonschema.String().Description("…").Required()},
//		}
//	}
//	func (t SwitchCurrency) Handle(ctx context.Context, req ai.Request) (string, error) { … }
//
// A returned error fails the whole run; tools report recoverable failures
// to the model inside their result instead.
type Tool interface {
	Name() string
	Description() string
	Schema() jsonschema.Properties
	Handle(ctx context.Context, req Request) (string, error)
}

// Request is the model's arguments with lenient readers, since a model's
// JSON drifts from its schema: an absent or null key reads as the zero
// value, a numeric string ("1.5") as its number.
type Request struct {
	Arguments        *ordered.Object
	ToolCallID       string
	ToolInvocationID string
}

// NewRequest builds a request from decoded arguments (nil means none).
func NewRequest(arguments *ordered.Object) Request {
	if arguments == nil {
		arguments = ordered.NewObject()
	}

	return Request{Arguments: arguments}
}

// RequestFromJSON builds a request from a JSON object literal — for tests.
func RequestFromJSON(raw string) Request {
	return NewRequest(decodeArguments(raw))
}

// Get is the raw argument at key, nil when absent.
func (r Request) Get(key string) any {
	value, _ := r.Arguments.Get(key)

	return value
}

// Has reports whether the key is present, even when its value is null.
func (r Request) Has(key string) bool {
	return r.Arguments.Has(key)
}

// Filled reports a key that is present and neither null nor a blank
// string; a boolean, number, list or object always counts as filled.
func (r Request) Filled(key string) bool {
	switch v := r.Get(key).(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	}

	return true
}

// String is the argument at key as a string: a number as its JSON
// spelling, a boolean as "true" or "false"; "" for null, a list or an
// object.
func (r Request) String(key string) string {
	return stringOf(r.Get(key))
}

// Float is the argument at key as a float: a number, or a decimal numeric
// string (support.IsNumeric); 0 otherwise, and for a value that is not
// finite, so "Infinity" or "NaN" never reach a price cap or a coordinate.
func (r Request) Float(key string) float64 {
	var f float64
	switch v := r.Get(key).(type) {
	case json.Number:
		f, _ = v.Float64()
	case string:
		if support.IsNumeric(v) {
			f = support.NumericToFloat(v)
		}
	}

	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}

	return f
}

// Integer is the argument at key as an int (see Float), fractions
// truncated.
func (r Request) Integer(key string) int {
	if n, ok := r.Get(key).(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}

	return int(r.Float(key))
}

// Array is the argument at key as a list (an object's values in order);
// null is empty, a scalar is a one-item list.
func (r Request) Array(key string) []any {
	switch v := r.Get(key).(type) {
	case nil:
		return []any{}
	case []any:
		return v
	case *ordered.Object:
		return v.Values()
	default:
		return []any{v}
	}
}

// stringOf is a decoded JSON scalar as text: a string as it is, a number
// as its JSON spelling, a boolean as "true" or "false"; "" for anything
// else.
func stringOf(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}

	return ""
}
