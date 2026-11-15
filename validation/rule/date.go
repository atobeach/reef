package rule

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/atobeach/reef/clock"
)

// DateFormat requires a date in exactly the given Go layout (time.DateOnly):
// it must parse and format back to itself. The message spells the layout in
// the letters the forms show ("d/m/Y"), so a layout of anything but 2006 01
// 02 15 04 05 elements panics.
func DateFormat(layout string) Rule {
	format := formatLetters(layout)

	return Rule{
		Name: "date_format", param: layout,
		check: func(f *Field) bool {
			s, ok := f.Value.(string)
			if !ok {
				return false
			}

			parsed, err := time.Parse(layout, s)

			return err == nil && parsed.Format(layout) == s
		},
		message: func(*Field) string { return "The :attribute field must match the format " + format + "." },
	}
}

// layoutLetters names each layout element a message can spell.
var layoutLetters = strings.NewReplacer("2006", "Y", "01", "m", "02", "d", "15", "H", "04", "i", "05", "s")

// formatLetters spells a layout the way a message shows it: "02/01/2006" is
// "d/m/Y". A letter in the layout ("Y-m-d" would match only itself) or a
// digit left over panics rather than reject every date.
func formatLetters(layout string) string {
	format := layoutLetters.Replace(layout)
	if strings.ContainsFunc(layout, unicode.IsLetter) || strings.ContainsFunc(format, unicode.IsDigit) {
		panic(fmt.Sprintf("rule: DateFormat takes a Go layout of 2006 01 02 15 04 05 elements, got %q", layout))
	}

	return format
}

// After requires a date after another field's date, or after a literal date
// such as "today".
func After(other string) Rule {
	return compareDates("after", other, "The :attribute field must be a date after %s.", func(a, b time.Time) bool { return a.After(b) })
}

// AfterOrEqual is After that also accepts the same date.
func AfterOrEqual(other string) Rule {
	return compareDates("after_or_equal", other, "The :attribute field must be a date after or equal to %s.", func(a, b time.Time) bool { return !a.Before(b) })
}

func compareDates(name string, other string, message string, ok func(a, b time.Time) bool) Rule {
	return Rule{
		Name: name,
		check: func(f *Field) bool {
			value, valid := parseDate(f.Ctx, f.Value, f.Rules)
			if !valid {
				return false
			}

			if reference, isField := f.Lookup(other); isField {
				ref, refValid := parseDate(f.Ctx, reference, f.Rules)

				return refValid && ok(value, ref)
			}

			ref, refValid := parseDate(f.Ctx, other, nil)

			return refValid && ok(value, ref)
		},
		message: func(f *Field) string {
			if _, isField := f.Lookup(other); isField {
				return fmt.Sprintf(message, f.Display(other))
			}

			return fmt.Sprintf(message, other)
		},
	}
}

// parseDate reads a date with the attribute's own date_format when it has
// one, else the ISO shapes, plus "today" (midnight UTC).
func parseDate(ctx context.Context, value any, rules []Rule) (time.Time, bool) {
	s, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}

	if s == "today" {
		now := clock.Now(ctx)

		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), true
	}

	for _, r := range rules {
		if r.Name == "date_format" {
			parsed, err := time.Parse(r.param, s)

			return parsed, err == nil
		}
	}

	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339} {
		if parsed, err := time.Parse(layout, s); err == nil {
			return parsed, true
		}
	}

	return time.Time{}, false
}
