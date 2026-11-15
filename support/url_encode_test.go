package support

import "testing"

func TestURLEncodingsKeepOnlyTheirUnreservedSets(t *testing.T) {
	cases := []struct {
		input, raw, form string
	}{
		{"Example Flights", "Example%20Flights", "Example+Flights"},
		{"a~b_c.d-e", "a~b_c.d-e", "a%7Eb_c.d-e"},
		{"x+y/z?&=", "x%2By%2Fz%3F%26%3D", "x%2By%2Fz%3F%26%3D"},
		{"Café.jpg", "Caf%C3%A9.jpg", "Caf%C3%A9.jpg"},
		{"me@example.test", "me%40example.test", "me%40example.test"},
	}

	for _, c := range cases {
		if got := RawURLEncode(c.input); got != c.raw {
			t.Errorf("RawURLEncode(%q) = %q, want %q", c.input, got, c.raw)
		}
		if got := FormEncode(c.input); got != c.form {
			t.Errorf("FormEncode(%q) = %q, want %q", c.input, got, c.form)
		}
	}
}
