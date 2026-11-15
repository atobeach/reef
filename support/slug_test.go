package support

import "testing"

func TestSlugIsASCIILowerDashed(t *testing.T) {
	cases := map[string]string{
		"Paris":              "paris",
		"São Paulo":          "sao-paulo",
		"東京":                 "",
		"New_York  City":     "new-york-city",
		"Hello@World":        "hello-at-world",
		"  --Côte d’Azur!! ": "cote-dazur",
		"Kraków, Poland":     "krakow-poland",
	}

	for input, want := range cases {
		if got := Slug(input); got != want {
			t.Errorf("Slug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUcwordsLeavesMultibyteWordsIntact(t *testing.T) {
	cases := map[string]string{
		"wine bar":   "Wine Bar",
		"a €uro":     "A €uro",
		"art ñandú":  "Art ñandú",
		"x\tyz  ábc": "X\tYz  ábc",
	}

	for input, want := range cases {
		if got := ucwords(input); got != want {
			t.Errorf("ucwords(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPluralFollowsEnglishInflection(t *testing.T) {
	cases := map[string]string{
		"restaurant": "restaurants", "amusement park": "amusement parks", "cafe": "cafes",
		"beach": "beaches", "gallery": "galleries", "museum": "museums", "bar": "bars",
		"shopping": "shoppings", "art": "art", "food": "food", "nightlife": "nightlives",
		"Church": "Churches", "sightseeing": "sightseeings", "MUSEUM": "MUSEUMS",
		"hostel": "hostels", "bus": "buses", "wine bar": "wine bars", "library": "libraries",
	}

	for input, want := range cases {
		if got := Plural(input); got != want {
			t.Errorf("Plural(%q) = %q, want %q", input, got, want)
		}
	}
}
