package rule

import (
	"reflect"
	"strings"
)

// Required fails on absent, null, blank strings and empty arrays.
var Required = Rule{
	Name: "required", Implicit: true,
	check:   func(f *Field) bool { return f.Present && !empty(f.Value) },
	message: fixed("The :attribute field is required."),
}

// RequiredWith is required when any of the other attributes is filled.
func RequiredWith(others ...string) Rule {
	return Rule{
		Name: "required_with", Implicit: true,
		check: func(f *Field) bool {
			for _, other := range others {
				if value, ok := f.Lookup(other); ok && !empty(value) {
					return f.Present && !empty(f.Value)
				}
			}

			return true
		},
		message: func(f *Field) string {
			names := make([]string, len(others))
			for i, other := range others {
				names[i] = f.Display(other)
			}

			return "The :attribute field is required when " + strings.Join(names, " / ") + " is present."
		},
	}
}

// Confirmed requires `<attribute>_confirmation` to hold the same value,
// compared strictly: a "1" never confirms a 1.
var Confirmed = Rule{
	Name: "confirmed",
	check: func(f *Field) bool {
		other, ok := f.Lookup(f.Attribute + "_confirmation")

		return ok && reflect.DeepEqual(other, f.Value)
	},
	message: fixed("The :attribute field confirmation does not match."),
}

func empty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}

	return false
}
