package support

import (
	"math"
	"testing"
)

// Expected values are the spellings the stored data already holds.

func TestRoundIsHalfUpOnTheWrittenValue(t *testing.T) {
	cases := map[float64]float64{1.005: 1.01, 0.285: 0.29, -1.005: -1.01, 2.675: 2.68, 1.004: 1.0, 245.5 / 7: 35.07, 0: 0}
	for value, want := range cases {
		if got := Round(value, 2); got != want {
			t.Errorf("round(%v, 2) = %v, want %v", value, got, want)
		}
	}

	if got := Round(-0.004, 2); got != 0 || !math.Signbit(got) {
		t.Errorf("round(-0.004, 2) = %v, want -0", got)
	}
	if got := Round(1234.5, 0); got != 1235 {
		t.Errorf("round(1234.5) = %v", got)
	}
}

func TestIsNumeric(t *testing.T) {
	for value, want := range map[string]bool{"12": true, " 12": true, "12 ": true, "1e3": true, ".5": true, "+1": true, "-1.5": true, "": false, "abc": false, "1x": false, "0x1A": false, ".": false} {
		if got := IsNumeric(value); got != want {
			t.Errorf("is_numeric(%q) = %v", value, got)
		}
	}
	if NumericToInt("1e3") != 1000 || NumericToInt(" 12.7") != 12 {
		t.Error("(int) on numeric strings")
	}
}

func TestStrHelpers(t *testing.T) {
	titles := map[string]string{"KING-size bed": "King-Size Bed", "queen o'brien": "Queen O'brien", "double": "Double", "2nd floor": "2Nd Floor"}
	for value, want := range titles {
		if got := Title(value); got != want {
			t.Errorf("Title(%q) = %q, want %q", value, got, want)
		}
	}

	if got := Squish("  resort  \t fee "); got != "resort fee" {
		t.Errorf("squish = %q", got)
	}
	if got := MbTrim("  EUR \n"); got != "EUR" {
		t.Errorf("mb_trim = %q", got)
	}
	if got := BeforeLast("London (Any)", " (Any)"); got != "London" {
		t.Errorf("beforeLast = %q", got)
	}
	if got := BeforeLast("Paris", " (Any)"); got != "Paris" {
		t.Errorf("beforeLast = %q", got)
	}
}

func TestNonEmptyIsDerefsInverseForAnOptionalString(t *testing.T) {
	if got := NonEmpty(""); got != nil {
		t.Errorf("NonEmpty(\"\") = %q, want nil", *got)
	}

	for _, value := range []string{"Valencia", " ", "0"} {
		if got := NonEmpty(value); got == nil || *got != value {
			t.Errorf("NonEmpty(%q) = %v, want a pointer to it", value, got)
		}
		if got := Deref(NonEmpty(value)); got != value {
			t.Errorf("Deref(NonEmpty(%q)) = %q", value, got)
		}
	}
}
