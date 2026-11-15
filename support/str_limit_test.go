package support

import (
	"strings"
	"testing"
)

func TestStrLimitCutsAtFiftyDisplayColumns(t *testing.T) {
	cases := map[string]string{
		"Paris":                      "Paris",
		strings.Repeat("Paris ", 20): strings.Repeat("Paris ", 8) + "Pa...",
		"東京で一週間過ごしたいです。おすすめのホテルを教えてください。東京で一週間過ごしたい":                                "東京で一週間過ごしたいです。おすすめのホテルを教え...",
		"I want 🏖️ beaches and 🍷 wine in Portugal please, somewhere sunny and calm": "I want 🏖️ beaches and 🍷 wine in Portugal please,...",
		"Ünïcödé café crème brûlée à la carte — very long sentence here ok":         "Ünïcödé café crème brûlée à la carte — very long s...",
	}

	for input, want := range cases {
		if got := StrLimit(input, 50); got != want {
			t.Errorf("StrLimit(%q)\n got %q\nwant %q", input, got, want)
		}
	}
}
