// Package rule holds the validation rules a request declares per attribute:
//
//	func (ManualFlightSearch) Rules() rule.Set {
//		return rule.Set{
//			"origin":       {rule.Required, rule.String, rule.Max(120)},
//			"child_ages.*": {rule.Integer, rule.Min(0), rule.Max(17)},
//			"return_date":  {rule.Nullable, rule.DateFormat(time.DateOnly), rule.AfterOrEqual("departure_date")},
//		}
//	}
//
// Rule names are the keys custom messages use ("contact_email.required"),
// and the default messages are worded exactly as Laravel's are
// in a 422.
package rule

import "context"

// Set maps an attribute (dotted, `*` wildcards allowed) to its rules.
type Set map[string][]Rule

// Rule is one validation rule.
type Rule struct {
	// Name identifies the rule ("required", "max") in custom message keys:
	// "contact_email.required".
	Name string
	// Implicit rules run even when the attribute is absent or empty.
	Implicit bool
	// marker rules (sometimes, nullable, bail) steer how the others run and
	// never fail themselves.
	marker bool
	// param carries an argument other rules read (date_format's layout).
	param string
	// check decides a rule that reads only the input.
	check func(f *Field) bool
	// query decides a rule that reads outside the input (the database, the
	// user's stored hash). Its error means the rule couldn't decide, never
	// that the value is invalid.
	query func(f *Field) (bool, error)
	// message renders the default text; :attribute is substituted after.
	message func(f *Field) string
}

// IsMarker reports whether the rule only steers other rules.
func (r Rule) IsMarker() bool { return r.marker }

// Passes runs the rule. An error means it couldn't run (the database is
// down): the value is neither valid nor invalid.
func (r Rule) Passes(f *Field) (bool, error) {
	if r.query != nil {
		return r.query(f)
	}

	return r.check == nil || r.check(f), nil
}

// Message is the default, unsubstituted message.
func (r Rule) Message(f *Field) string { return r.message(f) }

// Field is what a rule sees: the expanded attribute, its value, and the
// whole input for rules that compare against other fields.
type Field struct {
	Ctx       context.Context
	Attribute string
	Value     any
	Present   bool
	Input     map[string]any
	// Rules are the attribute's full rule list: size rules check it for
	// numeric/integer to decide between length and magnitude.
	Rules []Rule
	// Display renders another attribute's name for messages (`after:x`).
	Display func(attribute string) string
	// Lookup reads another attribute's value from the input.
	Lookup func(attribute string) (any, bool)
}

// Markers.
var (
	// Sometimes skips every rule when the attribute is absent.
	Sometimes = Rule{Name: "sometimes", marker: true}
	// Nullable skips the non-implicit rules when the value is null.
	Nullable = Rule{Name: "nullable", marker: true}
	// Bail stops at the attribute's first failure.
	Bail = Rule{Name: "bail", marker: true}
)

// Custom is an app-defined rule with a fixed message — `current_password`
// checks the user's hash this way. passes returns an error only when it
// couldn't decide (a lookup failed), never for an invalid value.
func Custom(name string, passes func(f *Field) (bool, error), message string) Rule {
	return Rule{Name: name, query: passes, message: fixed(message)}
}

func fixed(message string) func(*Field) string {
	return func(*Field) string { return message }
}
