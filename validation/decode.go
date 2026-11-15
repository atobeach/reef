package validation

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/atobeach/reef/collect"
	"github.com/atobeach/reef/validation/rule"
)

// The mismatches no reef rule describes: an object for a slice (the array
// rule takes any object; a list is only one keyed "0".."n-1"), and a value a
// self-decoding type refused. Each is a name for custom messages and a
// message, not a rule: nothing runs them.
const (
	listName      = "list"
	listMessage   = "The :attribute field must be a list."
	formatName    = "format"
	formatMessage = "The :attribute field format is invalid."
)

// Decode fills dst (a struct pointer) from input by `json` tag. Unlike
// encoding/json it takes query-string scalars ("3" into an int), a bool as
// Input.Boolean reads it, an object keyed "0".."n-1" as a list and an empty
// list as an empty object. Strings are taken as sent: the input layer
// trimmed all but passwords.
//
// It fills what fits: a value a field can't hold leaves the field's zero
// value, and the error names every such path. A dst that isn't a struct
// pointer is an error too.
func Decode(input map[string]any, dst any) error {
	mismatches, err := decode(input, dst)
	if err != nil || len(mismatches) == 0 {
		return err
	}

	paths := collect.Map(mismatches, func(m mismatch) string { return m.attribute })

	return fmt.Errorf("validation: %T can't hold the input at %s", dst, strings.Join(paths, ", "))
}

// Decode fills dst (a struct pointer) from the validator's input as Decode
// does, keeping what didn't fit for Validate to report. Its error is only a
// dst that isn't a struct pointer.
func (v *Validator) Decode(dst any) error {
	mismatches, err := decode(v.input, dst)
	v.mismatches = mismatches

	return err
}

func decode(input map[string]any, dst any) ([]mismatch, error) {
	target := reflect.ValueOf(dst)
	if target.Kind() != reflect.Pointer || target.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("validation: Decode needs a struct pointer, got %T", dst)
	}

	d := &decoder{}
	d.fillFields(target.Elem(), input, at{})

	return d.mismatches, nil
}

// mismatch is an input value its field's Go type can't hold, reported as a
// failure of the rule the type implies: that rule's name, which a custom
// message keys on, and its message.
type mismatch struct {
	at
	name    string
	message string
}

// at is where the decoder is: the attribute, and whether its path passed
// through a list element or map entry, which messages show raw, as they do
// an attribute expanded from a wildcard.
type at struct {
	attribute string
	expanded  bool
}

func (a at) field(name string) at { return at{join(a.attribute, name), a.expanded} }

func (a at) entry(key string) at { return at{join(a.attribute, key), true} }

// decoder fills a value from input, recording each value that doesn't fit
// and carrying on with the next field or element.
type decoder struct {
	mismatches []mismatch
}

// assign stores value into dst; a value that doesn't fit leaves dst zero.
func (d *decoder) assign(dst reflect.Value, value any, where at) {
	if !d.fill(dst, value, where) {
		dst.SetZero()
	}
}

// fill stores value into dst and reports whether dst could hold it. A
// struct, list or map that fits keeps whichever of its parts fit.
func (d *decoder) fill(dst reflect.Value, value any, where at) bool {
	if value == nil {
		dst.SetZero()

		return true
	}

	if dst.Kind() == reflect.Pointer {
		fresh := reflect.New(dst.Type().Elem())
		if !d.fill(fresh.Elem(), value, where) {
			return false
		}
		dst.Set(fresh)

		return true
	}

	if reflect.PointerTo(dst.Type()).Implements(unmarshalerType) {
		return d.unmarshal(dst, value, where)
	}

	switch dst.Kind() {
	case reflect.Struct:
		object, ok := asObject(value)
		if !ok {
			return d.fail(where, rule.Array.Name, rule.Array.Message(nil))
		}
		d.fillFields(dst, object, where)

		return true
	case reflect.Slice:
		return d.fillSlice(dst, value, where)
	case reflect.Map:
		return d.fillMap(dst, value, where)
	case reflect.Interface:
		dst.Set(reflect.ValueOf(value))

		return true
	case reflect.Bool:
		dst.SetBool(boolean(value))

		return true
	default:
		return d.fillScalar(dst, value, where)
	}
}

func (d *decoder) fail(where at, name, message string) bool {
	d.mismatches = append(d.mismatches, mismatch{at: where, name: name, message: message})

	return false
}

// unmarshal leaves a type that decodes itself (json.RawMessage, a time) to
// encoding/json, handing it the value as JSON.
func (d *decoder) unmarshal(dst reflect.Value, value any, where at) bool {
	fresh := reflect.New(dst.Type())

	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, fresh.Interface())
	}

	if err != nil {
		return d.fail(where, formatName, formatMessage)
	}
	dst.Set(fresh.Elem())

	return true
}

func (d *decoder) fillFields(dst reflect.Value, object map[string]any, where at) {
	for i := range dst.NumField() {
		field := dst.Type().Field(i)
		if promoted(field) {
			d.fillFields(dst.Field(i), object, where)

			continue
		}

		name, ok := jsonName(field)
		if !ok {
			continue
		}

		if value, present := object[name]; present {
			d.assign(dst.Field(i), value, where.field(name))
		}
	}
}

func (d *decoder) fillSlice(dst reflect.Value, value any, where at) bool {
	list, ok := asList(value)
	if !ok {
		return d.fail(where, listName, listMessage)
	}

	slice := reflect.MakeSlice(dst.Type(), len(list), len(list))
	for i, item := range list {
		d.assign(slice.Index(i), item, where.entry(strconv.Itoa(i)))
	}
	dst.Set(slice)

	return true
}

func (d *decoder) fillMap(dst reflect.Value, value any, where at) bool {
	object, ok := asObject(value)
	if !ok {
		return d.fail(where, rule.Array.Name, rule.Array.Message(nil))
	}

	m := reflect.MakeMapWithSize(dst.Type(), len(object))
	// In key order, so the mismatches report the same way every run.
	for _, key := range slices.Sorted(maps.Keys(object)) {
		element := reflect.New(dst.Type().Elem()).Elem()
		d.assign(element, object[key], where.entry(key))
		m.SetMapIndex(reflect.ValueOf(key).Convert(dst.Type().Key()), element)
	}
	dst.Set(m)

	return true
}

// fillScalar parses the input's text form into a scalar field, so a query
// string's "3" fills an int. A list or object is no scalar's text.
func (d *decoder) fillScalar(dst reflect.Value, value any, where at) bool {
	implied, ok := scalarRule(dst.Kind())
	if !ok {
		// CheckFields refuses such a field before a form request decodes.
		return d.fail(where, formatName, formatMessage)
	}

	switch value.(type) {
	case []any, map[string]any:
		return d.fail(where, implied.Name, implied.Message(nil))
	}

	text := scalar(value)
	if dst.Kind() == reflect.String {
		dst.SetString(text)

		return true
	}

	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return d.fail(where, implied.Name, implied.Message(nil))
	}

	switch dst.Kind() {
	case reflect.Float32, reflect.Float64:
		dst.SetFloat(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n < 0 {
			return d.fail(where, implied.Name, implied.Message(nil))
		}
		dst.SetUint(uint64(n))
	default:
		dst.SetInt(int64(n))
	}

	return true
}

// scalarRule is the rule a scalar field's kind implies; ok is false for a
// kind no input fills.
func scalarRule(kind reflect.Kind) (rule.Rule, bool) {
	switch kind {
	case reflect.String:
		return rule.String, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rule.Integer, true
	case reflect.Float32, reflect.Float64:
		return rule.Numeric, true
	default:
		return rule.Rule{}, false
	}
}

// asObject accepts objects, and an empty list as an empty object: a client
// that can't tell the two apart sends [] for {}.
func asObject(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case []any:
		return map[string]any{}, len(v) == 0
	}

	return nil, false
}

func scalar(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "1"
		}
		return "0"
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
