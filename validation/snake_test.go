package validation

import "testing"

// Messages print camelCase request keys (a seat map's rowId)
// snake-cased, one underscore per capital.
func TestDisplayedAttributesAreSnakeCased(t *testing.T) {
	for in, want := range map[string]string{
		"seatMap.rowId":  "seat_map.row_id",
		"departure_date": "departure_date",
		"email":          "email",
		"fareRulesJSON":  "fare_rules_j_s_o_n",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}
