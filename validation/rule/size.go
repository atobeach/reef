package rule

import (
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"
)

// Min is a minimum length, magnitude or item count.
func Min(n float64) Rule {
	return sizeRule("min", n, func(size float64) bool { return size >= n }, sizeMessages{
		numeric: "The :attribute field must be at least %s.",
		text:    "The :attribute field must be at least %s characters.",
		array:   "The :attribute field must have at least %s items.",
	})
}

// Max is a maximum length, magnitude or item count.
func Max(n float64) Rule {
	return sizeRule("max", n, func(size float64) bool { return size <= n }, sizeMessages{
		numeric: "The :attribute field must not be greater than %s.",
		text:    "The :attribute field must not be greater than %s characters.",
		array:   "The :attribute field must not have more than %s items.",
	})
}

// Size is an exact length, magnitude or item count.
func Size(n float64) Rule {
	return sizeRule("size", n, func(size float64) bool { return size == n }, sizeMessages{
		numeric: "The :attribute field must be %s.",
		text:    "The :attribute field must be %s characters.",
		array:   "The :attribute field must contain %s items.",
	})
}

// sizeMessages are a size rule's wordings per kind; %s is the limit.
type sizeMessages struct {
	numeric, text, array string
}

func sizeRule(name string, limit float64, ok func(size float64) bool, messages sizeMessages) Rule {
	formatted := strconv.FormatFloat(limit, 'f', -1, 64)

	return Rule{
		Name: name,
		check: func(f *Field) bool {
			size, measurable := sizeOf(f)

			return measurable && ok(size)
		},
		message: func(f *Field) string {
			switch kindOf(f) {
			case kindNumeric:
				return fmt.Sprintf(messages.numeric, formatted)
			case kindArray:
				return fmt.Sprintf(messages.array, formatted)
			default:
				return fmt.Sprintf(messages.text, formatted)
			}
		},
	}
}

type kind int

const (
	kindString kind = iota
	kindNumeric
	kindArray
)

// kindOf picks what a size rule measures: magnitude when the attribute is
// declared numeric/integer and holds a number, count for arrays, else
// string length — so `max:9` reads "12" as twelve only on a numeric field.
func kindOf(f *Field) kind {
	if hasNumericRule(f.Rules) {
		if _, ok := asFloat(f.Value); ok {
			return kindNumeric
		}
	}

	if isArray(f.Value) {
		return kindArray
	}

	return kindString
}

func sizeOf(f *Field) (float64, bool) {
	switch kindOf(f) {
	case kindNumeric:
		return asFloat(f.Value)
	case kindArray:
		switch v := f.Value.(type) {
		case []any:
			return float64(len(v)), true
		case map[string]any:
			return float64(len(v)), true
		}
	case kindString:
	}

	// Anything else is sized by its character count.
	return float64(utf8.RuneCountInString(scalarString(f.Value))), true
}

func hasNumericRule(rules []Rule) bool {
	return slices.ContainsFunc(rules, func(r Rule) bool { return r.Name == "numeric" || r.Name == "integer" })
}
