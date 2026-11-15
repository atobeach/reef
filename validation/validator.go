// Package validation runs rule.Sets over request input (sometimes, nullable,
// bail, implicit rules, `*` wildcards, custom messages, after hooks),
// decodes the input into the request's struct, and collects the errors in a
// stable order, because a client renders the 422's messages, and its
// summary, as they arrive.
package validation

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/validation/rule"
)

// Options carry a request's customisations.
type Options struct {
	// Messages override defaults: "contact_email.required", or with
	// wildcards, "passengers.*.first_name.required".
	Messages map[string]string
	// Attributes rename attributes in messages: "dob" => "date of birth".
	Attributes map[string]string
	// Order ranks attribute patterns ("passengers", "passengers.*.first_name")
	// so errors, and the summary message, follow the request's declared
	// field order. A pattern not listed ranks with its top-level field.
	Order []string
}

// Validator validates one input.
type Validator struct {
	ctx     context.Context
	input   Input
	rules   rule.Set
	options Options
	errors  *Errors
	after   []func(v *Validator) error
	// mismatches are the values Decode found its struct can't hold.
	mismatches []mismatch
}

// New prepares a validator; nothing runs until Validate.
func New(ctx context.Context, input Input, rules rule.Set, options Options) *Validator {
	return &Validator{ctx: ctx, input: input, rules: rules, options: options, errors: &Errors{}}
}

// After registers a hook that runs once the rules have, even when they
// failed (check Errors().HasAny before relying on a field). A hook adds
// messages for invalid input and errors only when it couldn't check.
func (v *Validator) After(hook func(v *Validator) error) {
	v.after = append(v.after, hook)
}

// Errors is the message bag.
func (v *Validator) Errors() *Errors {
	return v.errors
}

// Input is the input's value at key (Input.Value).
func (v *Validator) Input(key string) any {
	return v.input.Value(key)
}

// Has reports whether every key is present in the input (Input.Has).
func (v *Validator) Has(keys ...string) bool {
	return v.input.Has(keys...)
}

// String is the input at key as text (Input.String).
func (v *Validator) String(key string) string {
	return v.input.String(key)
}

// Array is the list at key (Input.Array).
func (v *Validator) Array(key string) ([]any, bool) {
	return v.input.Array(key)
}

// Validated is the input the rules vouched for: each top-level field a
// ruled attribute was found in, whole and as sent (an explicit null
// included), in validation order, so an element-wise list (`child_ages.*`)
// comes after the plain fields.
func (v *Validator) Validated() *ordered.Object {
	type visit struct {
		field    string
		wildcard bool
	}

	var visits []visit
	elementwise := map[string]bool{}
	for _, pattern := range v.orderedAttributes() {
		wildcard := strings.Contains(pattern, "*")
		for _, attribute := range expand(v.input, pattern) {
			if _, present := lookup(v.input, attribute); !present {
				continue
			}

			field, _, _ := strings.Cut(attribute, ".")
			visits = append(visits, visit{field, wildcard})
			elementwise[field] = elementwise[field] || wildcard
		}
	}

	validated := ordered.NewObject()
	for _, visited := range visits {
		// An element-wise list is placed by its elements, not its own rules.
		if visited.wildcard == elementwise[visited.field] && !validated.Has(visited.field) {
			validated.Set(visited.field, v.input[visited.field])
		}
	}

	return validated
}

// Validate runs every rule and hook and reports whether the input passed.
// An error means one couldn't run (the database is down) and stops
// validation: a 422 would blame the user for an outage.
//
// A value Decode found its struct can't hold fails as the rule its field's
// type implies ("The airlines field must be a list."), but only when the
// rules and hooks found nothing: their errors already explain the input.
func (v *Validator) Validate() (bool, error) {
	for _, pattern := range v.orderedAttributes() {
		for _, attribute := range expand(v.input, pattern) {
			if err := v.validateAttribute(attribute, pattern, v.rules[pattern]); err != nil {
				return false, err
			}
		}
	}

	for _, hook := range v.after {
		if err := hook(v); err != nil {
			return false, fmt.Errorf("validation: after hook: %w", err)
		}
	}

	if v.errors.Count() == 0 {
		for _, m := range v.mismatches {
			v.errors.Add(m.attribute, v.render(m.attribute, m.expanded, m.name, m.message))
		}
	}

	return v.errors.Count() == 0, nil
}

func (v *Validator) validateAttribute(attribute string, pattern string, rules []rule.Rule) error {
	has := func(marker rule.Rule) bool {
		return slices.ContainsFunc(rules, func(r rule.Rule) bool { return r.Name == marker.Name && r.IsMarker() })
	}

	value, present := lookup(v.input, attribute)
	if has(rule.Sometimes) && !present {
		return nil
	}

	field := &rule.Field{
		Ctx:       v.ctx,
		Attribute: attribute,
		Value:     value,
		Present:   present,
		Input:     v.input,
		Rules:     rules,
		Display:   func(other string) string { return v.display(other, false) },
		Lookup: func(other string) (any, bool) {
			return lookup(v.input, resolveSibling(attribute, pattern, other))
		},
	}

	nullable, bail := has(rule.Nullable), has(rule.Bail)
	for _, r := range rules {
		if r.IsMarker() || !validatable(r, field, nullable) {
			continue
		}

		passed, err := r.Passes(field)
		if err != nil {
			return fmt.Errorf("validation: %s rule on %s: %w", r.Name, attribute, err)
		}

		if passed {
			continue
		}

		v.errors.Add(attribute, v.message(attribute, strings.Contains(pattern, "*"), r, field))

		// A failed presence rule stops the attribute too: every other rule
		// would only restate "it's missing".
		if bail || r.Implicit {
			return nil
		}
	}

	return nil
}

// validatable reports whether a rule applies: non-implicit rules skip absent
// and blank values, and on nullable attributes skip null.
func validatable(r rule.Rule, f *rule.Field, nullable bool) bool {
	if r.Implicit {
		return true
	}

	if s, ok := f.Value.(string); ok && strings.TrimSpace(s) == "" {
		return false
	}

	if !f.Present {
		return false
	}

	return !nullable || f.Value != nil
}

// orderedAttributes sorts the rule patterns into error order, which also
// decides the 422's summary: plain patterns before wildcard ones (those in
// declared order), otherwise by their top-level field's declared rank.
func (v *Validator) orderedAttributes() []string {
	rank := map[string]int{}
	// A top-level field ranks where it or its first nested attribute is
	// declared ("tripDetails.booking_id" ranks tripDetails).
	topRank := map[string]int{}
	for i, name := range v.options.Order {
		rank[name] = i
		top, _, _ := strings.Cut(name, ".")
		if _, seen := topRank[top]; !seen {
			topRank[top] = i
		}
	}

	rankOf := func(ranks map[string]int, key string) int {
		if r, ok := ranks[key]; ok {
			return r
		}

		return len(rank)
	}

	attributes := slices.Collect(maps.Keys(v.rules))
	sort.SliceStable(attributes, func(i, j int) bool {
		a, b := attributes[i], attributes[j]
		wildA, wildB := strings.Contains(a, "*"), strings.Contains(b, "*")
		if wildA != wildB {
			return wildB
		}

		exactA, exactB := rankOf(rank, a), rankOf(rank, b)
		if wildA && exactA != exactB {
			return exactA < exactB
		}

		topA, _, _ := strings.Cut(a, ".")
		topB, _, _ := strings.Cut(b, ".")
		if ta, tb := rankOf(topRank, topA), rankOf(topRank, topB); ta != tb {
			return ta < tb
		}

		if exactA != exactB {
			return exactA < exactB
		}

		// Within one field, the container's rules run before its children's.
		return a < b
	})

	return attributes
}
