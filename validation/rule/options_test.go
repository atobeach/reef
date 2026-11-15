package rule_test

import (
	"encoding/json"
	"testing"

	"github.com/atobeach/reef/validation/rule"
)

// Options compare as text, so a query string's "640" and a JSON 640 both
// match an int option.
func TestInComparesIntOptionsAsText(t *testing.T) {
	r := rule.In(640, 1280)

	for value, want := range map[any]bool{
		"640": true, json.Number("1280"): true, float64(640): true, 640: true,
		"641": false, "640.0": false, " 640": false, true: false, nil: false,
	} {
		if passed, message := check(t, r, value); passed != want || message != "The selected :attribute is invalid." {
			t.Errorf("In(640, 1280) on %#v = %v %q, want %v", value, passed, message, want)
		}
	}

	if passed, _ := check(t, r, []any{"640"}); passed {
		t.Error("an array must never match an option")
	}
}

// tier is a string enum whose String renders a label, not the value.
type tier string

func (t tier) String() string { return "Tier " + string(t) }

func (tier) Enum() []string { return []string{"gold", "silver"} }

func TestInComparesANamedStringByItsValue(t *testing.T) {
	r := rule.In[tier]("gold")

	if passed, _ := check(t, r, "gold"); !passed {
		t.Error(`In[tier]("gold") must accept "gold"`)
	}

	if passed, _ := check(t, r, "Tier gold"); passed {
		t.Error(`In[tier]("gold") must not accept the label`)
	}
}

func TestEnumAcceptsTheValuesTheTypeLists(t *testing.T) {
	r := rule.Enum[tier]()

	if r.Name != "enum" {
		t.Fatalf("Name = %q, want enum", r.Name)
	}

	for value, want := range map[any]bool{"gold": true, "silver": true, "bronze": false, "Gold": false} {
		if passed, message := check(t, r, value); passed != want || message != "The selected :attribute is invalid." {
			t.Errorf("Enum[tier] on %#v = %v %q, want %v", value, passed, message, want)
		}
	}

	if passed, _ := check(t, r, []any{"gold"}); passed {
		t.Error("an array must never match a case")
	}
}
