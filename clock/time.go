// Package clock is the app clock, carried on the context so a test can
// freeze or move its own, and the timestamp formats clients parse and raw
// SQL binds.
package clock

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// isoLayout is the API's timestamp format: UTC with microseconds and Z.
const isoLayout = "2006-01-02T15:04:05.000000Z"

// Time is a timestamp that scans from Postgres and marshals as isoLayout.
//
//nolint:recvcheck // MarshalJSON/Value take a value so a plain Time field encodes; UnmarshalJSON/Scan must write through a pointer
type Time struct {
	time.Time
}

// TimeOf wraps t for a JSON field; nil stays nil.
func TimeOf(t *time.Time) *Time {
	if t == nil {
		return nil
	}

	return &Time{Time: *t}
}

// MarshalJSON emits `"2026-10-04T16:30:55.000000Z"`.
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + ISOString(t.Time) + `"`), nil
}

// UnmarshalJSON accepts the same format (and plain RFC 3339), so cached Data
// round-trips.
func (t *Time) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		return nil
	}

	parsed, err := time.Parse(`"`+time.RFC3339Nano+`"`, string(raw))
	if err != nil {
		return err
	}

	t.Time = parsed

	return nil
}

// Scan implements sql.Scanner for pgx.
func (t *Time) Scan(src any) error {
	value, ok := src.(time.Time)
	if !ok {
		return fmt.Errorf("clock: cannot scan %T", src)
	}

	t.Time = value

	return nil
}

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	return t.Time, nil
}
