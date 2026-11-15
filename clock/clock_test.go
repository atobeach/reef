package clock

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"
)

func TestAContextWithoutAClockReadsTheWallClockInUTC(t *testing.T) {
	got := Now(context.Background())

	if got.Location() != time.UTC || time.Since(got) > time.Minute || time.Since(got) < 0 {
		t.Fatalf("Now() = %v, want the wall clock in UTC", got)
	}
}

func TestTheBoundClockTravelsForItsContextOnly(t *testing.T) {
	frozen := time.Date(2026, 7, 27, 14, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	c := &Clock{}
	ctx := With(context.Background(), c)

	c.TravelTo(frozen)
	if got := Now(ctx); !got.Equal(frozen) || got.Location() != time.UTC {
		t.Fatalf("Now() = %v, want %v in UTC", got, frozen)
	}

	c.Travel(90 * time.Minute)
	if got := Now(ctx); !got.Equal(frozen.Add(90 * time.Minute)) {
		t.Fatalf("after travel Now() = %v", got)
	}

	if got := Now(With(context.Background(), &Clock{})); time.Since(got) > time.Minute {
		t.Fatalf("another context's clock = %v, want the wall clock", got)
	}
}

func TestTravellingAnUnfrozenClockFreezesItFromNow(t *testing.T) {
	c := &Clock{}
	c.Travel(time.Hour)

	first := c.Now()
	if until := time.Until(first); until < 59*time.Minute || until > time.Hour {
		t.Fatalf("travelled to %v, want an hour from now", first)
	}

	if again := c.Now(); !again.Equal(first) {
		t.Fatalf("a travelled clock moved on its own: %v then %v", first, again)
	}
}

func TestAFakedSleepOnlyRecords(t *testing.T) {
	c := &Clock{}
	c.FakeSleep()
	ctx := With(context.Background(), c)

	started := time.Now()
	Sleep(ctx, 30*time.Second)
	Sleep(ctx, 100*time.Millisecond)

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("a faked sleep paused for %v", elapsed)
	}

	if got := c.Slept(); len(got) != 2 || got[0] != 30*time.Second || got[1] != 100*time.Millisecond {
		t.Fatalf("slept %v", got)
	}
}

func TestARealSleepPausesUntilItsContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &Clock{}
		ctx, cancel := context.WithTimeout(With(context.Background(), c), 20*time.Millisecond)
		defer cancel()

		started := time.Now()
		Sleep(ctx, time.Minute)

		if elapsed := time.Since(started); elapsed != 20*time.Millisecond {
			t.Fatalf("slept %v, want until the context ended at 20ms", elapsed)
		}

		if got := c.Slept(); len(got) != 0 {
			t.Fatalf("an unfaked clock recorded %v", got)
		}
	})
}

func TestTimestampsRenderInTheirWireFormats(t *testing.T) {
	at := time.Date(2026, 7, 27, 16, 30, 55, 123456789, time.FixedZone("CEST", 2*60*60))

	cases := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"SQL binding, truncated to the second in UTC", func() (string, error) { return DateTimeString(at), nil }, "2026-07-27 14:30:55"},
		{"ISO string", func() (string, error) { return ISOString(at), nil }, "2026-07-27T14:30:55.123456Z"},
		{"Atom string keeps the offset", func() (string, error) { return AtomString(at), nil }, "2026-07-27T16:30:55+02:00"},
		{"Time JSON", func() (string, error) { raw, err := json.Marshal(Time{at}); return string(raw), err }, `"2026-07-27T14:30:55.123456Z"`},
		{"Atom JSON keeps the offset", func() (string, error) { raw, err := json.Marshal(Atom{at}); return string(raw), err }, `"2026-07-27T16:30:55+02:00"`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.got()
			if err != nil || got != c.want {
				t.Fatalf("got %s (%v), want %s", got, err, c.want)
			}
		})
	}
}

// TimeOf and AtomOf wrap a nullable column for a JSON field: null stays
// null, a time renders in its wire format.
func TestANullableTimeWrapsForItsWireFormat(t *testing.T) {
	at := time.Date(2026, 10, 20, 15, 2, 45, 0, time.UTC)

	raw, err := json.Marshal(struct {
		Time      *Time `json:"time"`
		Atom      *Atom `json:"atom"`
		UnsetTime *Time `json:"unset_time"`
		UnsetAtom *Atom `json:"unset_atom"`
	}{TimeOf(&at), AtomOf(&at), TimeOf(nil), AtomOf(nil)})
	if want := `{"time":"2026-10-20T15:02:45.000000Z","atom":"2026-10-20T15:02:45+00:00","unset_time":null,"unset_atom":null}`; err != nil || string(raw) != want {
		t.Fatalf("got %s (%v), want %s", raw, err, want)
	}
}

func TestTimestampsRoundTripThroughJSON(t *testing.T) {
	var decoded struct {
		Time  Time  `json:"time"`
		Atom  Atom  `json:"atom"`
		Unset *Time `json:"unset"`
	}

	if err := json.Unmarshal([]byte(`{"time":"2026-07-27T14:30:55.123456Z","atom":"2026-07-27T16:30:55+02:00","unset":null}`), &decoded); err != nil {
		t.Fatal(err)
	}

	if !decoded.Time.Equal(time.Date(2026, 7, 27, 14, 30, 55, 123456000, time.UTC)) || !decoded.Atom.Equal(time.Date(2026, 7, 27, 14, 30, 55, 0, time.UTC)) || decoded.Unset != nil {
		t.Fatalf("decoded = %+v", decoded)
	}

	if err := json.Unmarshal([]byte(`{"time":"27/07/2026"}`), &decoded); err == nil {
		t.Fatal("an unparseable time must fail")
	}
}

func TestTimeScansOnlyTimestamps(t *testing.T) {
	var scanned Time
	at := time.Date(2026, 7, 27, 14, 30, 55, 0, time.UTC)

	if err := scanned.Scan(at); err != nil || !scanned.Equal(at) {
		t.Fatalf("scanned %v (%v)", scanned, err)
	}

	if err := scanned.Scan("2026-07-27"); err == nil {
		t.Fatal("a string must not scan as a time")
	}

	if value, err := scanned.Value(); err != nil || value != at {
		t.Fatalf("Value() = %v (%v)", value, err)
	}
}
