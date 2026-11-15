package rule

import (
	_ "embed"
	"fmt"
	"net/mail"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// In requires one of the values, compared as text, so the query string's
// "640" matches In(640, 1280); arrays never match.
func In[T ~string | ~int](values ...T) Rule {
	options := make([]string, len(values))
	for i, value := range values {
		options[i] = optionText(value)
	}

	return Rule{
		Name:    "in",
		check:   func(f *Field) bool { return slices.Contains(options, scalarString(f.Value)) && !isArray(f.Value) },
		message: fixed("The selected :attribute is invalid."),
	}
}

// optionText is an option's text through reflect, never fmt: a named string
// type's String method may render a label instead of the value.
func optionText[T ~string | ~int](value T) string {
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.String {
		return v.String()
	}

	return strconv.FormatInt(v.Int(), 10)
}

// Enum requires one of the enum's backing values, as T lists them.
func Enum[T interface {
	~string
	Enum() []string
}]() Rule {
	var enum T

	r := In(enum.Enum()...)
	r.Name = "enum"

	return r
}

// Password requires a string of at least minimum characters.
func Password(minimum int) Rule {
	return Rule{
		Name: "password",
		check: func(f *Field) bool {
			s, ok := f.Value.(string)

			return ok && utf8.RuneCountInString(s) >= minimum
		},
		message: func(f *Field) string {
			if _, ok := f.Value.(string); !ok {
				return "The :attribute field must be a string."
			}

			return fmt.Sprintf("The :attribute field must be at least %d characters.", minimum)
		},
	}
}

// UUID requires a UUID of any version, in either case.
var UUID = Rule{
	Name:    "uuid",
	check:   func(f *Field) bool { s, ok := f.Value.(string); return ok && uuidPattern.MatchString(s) },
	message: fixed("The :attribute field must be a valid UUID."),
}

var uuidPattern = regexp.MustCompile(`^[\da-fA-F]{8}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{12}$`)

// Email is a lenient RFC address check: a bare address, no display name.
var Email = Rule{
	Name: "email",
	check: func(f *Field) bool {
		s, ok := f.Value.(string)
		if !ok || !strings.Contains(s, "@") {
			return false
		}

		address, err := mail.ParseAddress(s)

		return err == nil && address.Address == s && address.Name == ""
	},
	message: fixed("The :attribute field must be a valid email address."),
}

// Regex requires a string the pattern matches. Compile the pattern once, at
// package level, so a typo fails at boot.
func Regex(pattern *regexp.Regexp) Rule {
	return Rule{
		Name:    "regex",
		check:   func(f *Field) bool { s, ok := f.Value.(string); return ok && pattern.MatchString(s) },
		message: fixed("The :attribute field format is invalid."),
	}
}

// Timezone requires a canonical IANA identifier. Backward-compatible aliases
// such as Europe/Nicosia are rejected so stored timezones stay canonical;
// Go can't enumerate its zone database, so timezones.txt is a fixed list.
var Timezone = Rule{
	Name: "timezone",
	check: func(f *Field) bool {
		s, ok := f.Value.(string)

		return ok && timezones[s]
	},
	message: fixed("The :attribute field must be a valid timezone."),
}

//go:embed timezones.txt
var timezoneList string

var timezones = func() map[string]bool {
	set := map[string]bool{}
	for name := range strings.SplitSeq(strings.TrimSpace(timezoneList), "\n") {
		set[name] = true
	}

	return set
}()
