package ai

import (
	"testing"

	"github.com/atobeach/reef/ordered"
)

// A tool result goes back to the provider as text: a string as it is, null
// as nothing, anything else (a boolean included) as JSON.
func TestAToolResultIsSentBackAsText(t *testing.T) {
	object := ordered.NewObject()
	object.Set("city", "Paris/Nord")

	cases := map[string]struct {
		result any
		want   string
	}{
		"a string":  {"noon", "noon"},
		"null":      {nil, ""},
		"true":      {true, "true"},
		"false":     {false, "false"},
		"an object": {object, `{"city":"Paris/Nord"}`},
		"a list":    {[]any{"a", "é"}, `["a","é"]`},
	}

	for name, c := range cases {
		if got := (ToolResult{Result: c.result}).text(nil); got != c.want {
			t.Errorf("%s: text = %q, want %q", name, got, c.want)
		}
	}
}
