package support

import "testing"

// Expectations are half-up decimal rounding, midpoints included.
func TestRoundIsHalfUpAtAnyPrecision(t *testing.T) {
	cases := []struct {
		value  float64
		places int
		want   float64
	}{
		{1.005, 2, 1.01},
		{-1.005, 2, -1.01},
		{2.5, 0, 3},
		{-2.5, 0, -3},
		{-0.3, 0, -0},
		{12.649, 1, 12.6},
		{22.2389, 1, 22.2},
		{110.0 / 1.15, 4, 95.6522},
		{1234.5, -2, 1200},
		{3.0, 2, 3},
	}

	for _, c := range cases {
		if got := Round(c.value, c.places); got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v", c.value, c.places, got, c.want)
		}
	}
}

func TestThousandsGroupsDigitsInThrees(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 74123456: "74,123,456", -1234567: "-1,234,567", -12: "-12"} {
		if got := Thousands(n); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", n, got, want)
		}
	}
}
