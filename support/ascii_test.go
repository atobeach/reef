package support

import "testing"

// Pairs recorded from voku/portable-ascii's "en" map on the same inputs.
func TestAsciiTransliterates(t *testing.T) {
	for in, want := range map[string]string{
		"Brașov":     "Brasov",
		"Rânca":      "Ranca",
		"São Paulo":  "Sao Paulo",
		"Zürich":     "Zurich",
		"Kraków":     "Krakow",
		"Łódź":       "Lodz",
		"Straße":     "Strasse",
		"Ærøskøbing": "AEroskobing",
		"Ǿ ǽ":        "O ae",
		"Москва":     "Moskva",
		"Αθήνα":      "Athina",
		"Hà Nội":     "Ha Noi",
		"İstanbul":   "Istanbul",
		"éclair":    "eclair",
		"北京 Tokyo":   " Tokyo",
		"Tab\there":  "Tab here",
		"Œuvre":      "OEuvre",
		"Đà Lạt":     "Da Lat",
		"Reykjavík":  "Reykjavik",
		"Malmö":      "Malmo",
		"½ № ™ €":    "   EUR",
		// Multi-character keys fold as a unit.
		"ၸလၢႆးၶုမ်ႇ": "lon",
		"ကျောက်ဆည်":  "kyaauksai",
		"A̧ngle":     "Angle",
		"Aιxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx": "Aixxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
	} {
		if got := ASCII(in); got != want {
			t.Errorf("ASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFoldLowerIsTheTypeaheadKey(t *testing.T) {
	if got := FoldLower("BRAȘOV"); got != "brasov" {
		t.Fatalf("FoldLower = %q", got)
	}
}
