package rule_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/atobeach/reef/clock/clocktest"
	"github.com/atobeach/reef/validation/rule"
)

// check runs a rule on one value: whether it passed, and its message.
func check(t *testing.T, r rule.Rule, value any) (passed bool, message string) {
	t.Helper()

	f := &rule.Field{Attribute: "value", Value: value, Present: true}
	passed, err := r.Passes(f)
	if err != nil {
		t.Fatal(err)
	}

	return passed, r.Message(f)
}

// A date format is a Go layout; the message names it in the letters the
// forms show.
func TestDateFormatTakesAGoLayout(t *testing.T) {
	cases := []struct {
		layout  string
		value   any
		passes  bool
		message string
	}{
		{time.DateOnly, "2026-02-28", true, "The :attribute field must match the format Y-m-d."},
		{time.DateOnly, "2026-02-30", false, "The :attribute field must match the format Y-m-d."},
		{time.DateOnly, "2026-2-28", false, "The :attribute field must match the format Y-m-d."},
		{"02/01/2006", "31/12/1990", true, "The :attribute field must match the format d/m/Y."},
		{"02/01/2006", "1990-12-31", false, "The :attribute field must match the format d/m/Y."},
		{time.DateTime, "2026-10-21 09:05:00", true, "The :attribute field must match the format Y-m-d H:i:s."},
		{time.DateOnly, 20261021, false, "The :attribute field must match the format Y-m-d."},
	}

	for _, tc := range cases {
		passed, message := check(t, rule.DateFormat(tc.layout), tc.value)
		if passed != tc.passes || message != tc.message {
			t.Errorf("DateFormat(%q) on %v = %v %q, want %v %q", tc.layout, tc.value, passed, message, tc.passes, tc.message)
		}
	}
}

// A layout element the message can't name would show the user a format
// they can't type, and a letter spelling would match only itself, so either
// fails where the rule is declared.
func TestDateFormatPanicsOnALayoutItCannotName(t *testing.T) {
	for _, layout := range []string{
		"Jan 2, 2006", time.RFC3339, time.Kitchen, "2006-1-2", "06-01-02",
		"Y-m-d", "d/m/Y", "Y-m-d H:i:s",
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("DateFormat(%q) must panic", layout)
				}
			}()

			rule.DateFormat(layout)
		}()
	}
}

func TestRegexMatchesAGoPattern(t *testing.T) {
	r := rule.Regex(regexp.MustCompile(`^[A-Za-z]{3}$`))

	for value, want := range map[any]bool{"EUR": true, "eur": true, "EURO": false, "/EUR/": false, 123: false} {
		if passed, message := check(t, r, value); passed != want || message != "The :attribute field format is invalid." {
			t.Errorf("Regex on %v = %v %q, want %v", value, passed, message, want)
		}
	}
}

// "today" is midnight UTC on the field's clock, so a frozen app clock
// decides which dates have passed.
func TestTodayIsTheDayOnTheAppClock(t *testing.T) {
	ctx, _ := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC))

	cases := []struct {
		rule   rule.Rule
		value  string
		passes bool
	}{
		{rule.After("today"), "2026-10-21", true},
		{rule.After("today"), "2026-10-20", false},
		{rule.AfterOrEqual("today"), "2026-10-20", true},
		{rule.AfterOrEqual("today"), "2026-10-19", false},
	}

	for _, c := range cases {
		f := &rule.Field{Ctx: ctx, Attribute: "date", Value: c.value, Present: true, Lookup: func(string) (any, bool) { return nil, false }}
		if passed, err := c.rule.Passes(f); err != nil || passed != c.passes {
			t.Errorf("%s:today on %s = %v (%v), want %v", c.rule.Name, c.value, passed, err, c.passes)
		}
	}
}
