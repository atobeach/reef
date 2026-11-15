package validation

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/atobeach/reef/validation/rule"
)

func (v *Validator) message(attribute string, expanded bool, r rule.Rule, f *rule.Field) string {
	return v.render(attribute, expanded, r.Name, r.Message(f))
}

// render is the message for the named rule failing on attribute: the custom
// message ("contact_email.required"), else fallback, its :attribute
// placeholders filled with the attribute's display name.
func (v *Validator) render(attribute string, expanded bool, name string, fallback string) string {
	message, ok := matchKey(v.options.Messages, attribute+"."+name)
	if !ok {
		message = fallback
	}

	display := v.display(attribute, expanded)

	return strings.NewReplacer(
		":attribute", display,
		":Attribute", upperFirst(display),
		":ATTRIBUTE", strings.ToUpper(display),
	).Replace(message)
}

// display renders an attribute for messages: a custom name, else snake case
// with spaces ("departure_date" → "departure date"). Attributes expanded from
// a wildcard ("passengers.0.first_name") are shown raw.
func (v *Validator) display(attribute string, expanded bool) string {
	if name, ok := matchKey(v.options.Attributes, attribute); ok {
		return name
	}

	if expanded {
		return attribute
	}

	return strings.ReplaceAll(snake(attribute), "_", " ")
}

// matchKey looks key up exactly, else against the wildcard keys in sorted
// order, so overlapping patterns resolve the same way every run.
func matchKey(m map[string]string, key string) (string, bool) {
	if value, ok := m[key]; ok {
		return value, true
	}

	for _, pattern := range slices.Sorted(maps.Keys(m)) {
		if strings.Contains(pattern, "*") && glob(pattern).MatchString(key) {
			return m[pattern], true
		}
	}

	return "", false
}

// snake splits camelCase request keys for messages: "seatMap.rowId" →
// "seat_map.row_id".
func snake(value string) string {
	var out strings.Builder
	for i, r := range value {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('_')
		}
		out.WriteRune(r)
	}

	return strings.ToLower(out.String())
}

func glob(pattern string) *regexp.Regexp {
	return regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$")
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}
