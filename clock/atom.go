package clock

import "time"

// atomLayout is the API's date format for Data objects (not models):
// "2026-07-27T00:00:00+00:00".
const atomLayout = "2006-01-02T15:04:05-07:00"

// Atom is a date held by a Data object (not a model), in atomLayout.
type Atom struct {
	time.Time
}

// AtomOf wraps t for a Data field; nil stays nil.
func AtomOf(t *time.Time) *Atom {
	if t == nil {
		return nil
	}

	return &Atom{Time: *t}
}

// AtomString renders t in atomLayout, keeping its offset: a stored
// `timestamp(0)` scans as UTC, so it reads "+00:00".
func AtomString(t time.Time) string {
	return t.Format(atomLayout)
}

// MarshalJSON emits `"2026-07-27T00:00:00+00:00"`.
func (t Atom) MarshalJSON() ([]byte, error) {
	return []byte(`"` + AtomString(t.Time) + `"`), nil
}

// UnmarshalJSON accepts the same format.
func (t *Atom) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		return nil
	}

	parsed, err := time.Parse(`"`+time.RFC3339+`"`, string(raw))
	if err != nil {
		return err
	}

	t.Time = parsed

	return nil
}
