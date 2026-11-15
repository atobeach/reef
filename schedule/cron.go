package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cron is a parsed five-field expression (minute hour day-of-month month
// day-of-week) of `*`, numbers, `a-b`, lists and `/step`, each field a
// bitset of the values it allows.
type cron struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

// fieldBounds are each field's allowed values; day-of-week takes 7 as Sunday.
var fieldBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// parseCron parses a five-field cron expression.
func parseCron(expression string) (cron, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return cron{}, fmt.Errorf("schedule: %q is not a five-field cron expression", expression)
	}

	var sets [5]uint64
	for i, field := range fields {
		set, err := parseField(field, fieldBounds[i][0], fieldBounds[i][1])
		if err != nil {
			return cron{}, fmt.Errorf("schedule: %q: %w", expression, err)
		}

		sets[i] = set
	}

	// Sunday is both 0 and 7.
	if sets[4]&(1<<7) != 0 {
		sets[4] |= 1
	}

	return cron{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: fields[2] == "*", dowStar: fields[4] == "*",
	}, nil
}

// parseField parses one comma-separated field into the bitset of its values
// within low-high. A bare value with a step ("5/15") runs to high.
func parseField(field string, low, high int) (uint64, error) {
	var set uint64

	for _, part := range strings.Split(field, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			parsed, err := strconv.Atoi(stepPart)
			if err != nil || parsed <= 0 {
				return 0, fmt.Errorf("bad step %q", part)
			}

			step = parsed
		}

		from, to := low, high
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			var errA, errB error
			from, errA = strconv.Atoi(a)
			to, errB = strconv.Atoi(b)
			if errA != nil || errB != nil {
				return 0, fmt.Errorf("bad range %q", part)
			}
		default:
			value, err := strconv.Atoi(rangePart)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", part)
			}

			from = value
			if hasStep {
				to = high
			} else {
				to = value
			}
		}

		if from < low || to > high || from > to {
			return 0, fmt.Errorf("%q out of range %d-%d", part, low, high)
		}

		for v := from; v <= to; v += step {
			set |= 1 << uint(v)
		}
	}

	return set, nil
}

// matches reports whether the minute containing t is due. Day-of-month and
// day-of-week combine with OR when both are restricted, as in cron.
func (c cron) matches(t time.Time) bool {
	if c.minute&(1<<uint(t.Minute())) == 0 || c.hour&(1<<uint(t.Hour())) == 0 || c.month&(1<<uint(t.Month())) == 0 {
		return false
	}

	domMatch := c.dom&(1<<uint(t.Day())) != 0
	dowMatch := c.dow&(1<<uint(t.Weekday())) != 0

	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowMatch
	case c.dowStar:
		return domMatch
	default:
		return domMatch || dowMatch
	}
}

// next is the first due minute strictly after t (within ~4 years).
func (c cron) next(t time.Time) time.Time {
	candidate := t.Truncate(time.Minute).Add(time.Minute)
	for range 4 * 366 * 24 * 60 {
		if c.matches(candidate) {
			return candidate
		}

		candidate = candidate.Add(time.Minute)
	}

	return time.Time{}
}
