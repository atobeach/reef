package validation

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/atobeach/reef/validation/rule"
)

type goldenCase struct {
	Name       string            `json:"name"`
	Rules      [][2]string       `json:"rules"`
	Input      map[string]any    `json:"input"`
	Messages   map[string]string `json:"messages"`
	Attributes map[string]string `json:"attributes"`
}

// TestMatchesRecordedResults replays testdata/cases.json against
// testdata/golden.json, the recorded messages each case must reproduce:
// pass/fail, every message, attribute order and the summary must match.
func TestMatchesRecordedResults(t *testing.T) {
	var cases []goldenCase
	readJSON(t, "testdata/cases.json", &cases)

	var golden map[string]json.RawMessage
	readJSON(t, "testdata/golden.json", &golden)

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			rules := rule.Set{}
			order := make([]string, 0, len(tc.Rules))
			for _, pair := range tc.Rules {
				rules[pair[0]] = parse(t, pair[1])
				order = append(order, pair[0])
			}

			v := New(t.Context(), numbers(t, tc.Input), rules, Options{Messages: tc.Messages, Attributes: tc.Attributes, Order: order})
			passed, err := v.Validate()
			if err != nil {
				t.Fatal(err)
			}

			want := golden[tc.Name]
			if string(want) == "null" {
				if !passed {
					t.Fatalf("recorded as passing, failed: %s", v.Errors().Summary())
				}

				return
			}

			if passed {
				t.Fatalf("recorded as failing, passed; want %s", want)
			}

			var expected struct {
				Message string          `json:"message"`
				Errors  json.RawMessage `json:"errors"`
			}
			if err := json.Unmarshal(want, &expected); err != nil {
				t.Fatal(err)
			}

			if got := v.Errors().Summary(); got != expected.Message {
				t.Errorf("summary:\n got  %q\n want %q", got, expected.Message)
			}

			gotErrors, err := json.Marshal(v.Errors())
			if err != nil {
				t.Fatal(err)
			}
			var wantErrors bytes.Buffer
			if err := json.Compact(&wantErrors, expected.Errors); err != nil {
				t.Fatal(err)
			}
			if string(gotErrors) != wantErrors.String() {
				t.Errorf("errors:\n got  %s\n want %s", gotErrors, wantErrors.String())
			}
		})
	}
}

// parse reads the cases' pipe syntax ("required|string|max:120") into rules.
// In the syntax the cases were recorded in, a regex sits between slashes and
// a date format is spelled in Y-m-d letters; both become their Go forms here.
func parse(t *testing.T, definition string) []rule.Rule {
	t.Helper()

	var rules []rule.Rule
	for part := range strings.SplitSeq(definition, "|") {
		name, arg, _ := strings.Cut(part, ":")
		number := func() float64 {
			n, err := strconv.ParseFloat(arg, 64)
			if err != nil {
				t.Fatalf("rule %q needs a number", part)
			}
			return n
		}

		switch name {
		case "required":
			rules = append(rules, rule.Required)
		case "sometimes":
			rules = append(rules, rule.Sometimes)
		case "nullable":
			rules = append(rules, rule.Nullable)
		case "bail":
			rules = append(rules, rule.Bail)
		case "string":
			rules = append(rules, rule.String)
		case "integer":
			rules = append(rules, rule.Integer)
		case "numeric":
			rules = append(rules, rule.Numeric)
		case "boolean":
			rules = append(rules, rule.Boolean)
		case "array":
			if arg == "" {
				rules = append(rules, rule.Array)
			} else {
				rules = append(rules, rule.ArrayKeys(strings.Split(arg, ",")...))
			}
		case "min":
			rules = append(rules, rule.Min(number()))
		case "max":
			rules = append(rules, rule.Max(number()))
		case "size":
			rules = append(rules, rule.Size(number()))
		case "in":
			rules = append(rules, rule.In(strings.Split(arg, ",")...))
		case "uuid":
			rules = append(rules, rule.UUID)
		case "email":
			rules = append(rules, rule.Email)
		case "regex":
			rules = append(rules, rule.Regex(regexp.MustCompile(strings.TrimSuffix(strings.TrimPrefix(arg, "/"), "/"))))
		case "timezone":
			rules = append(rules, rule.Timezone)
		case "date_format":
			rules = append(rules, rule.DateFormat(strings.NewReplacer("Y", "2006", "m", "01", "d", "02").Replace(arg)))
		case "after":
			rules = append(rules, rule.After(arg))
		case "after_or_equal":
			rules = append(rules, rule.AfterOrEqual(arg))
		case "required_with":
			rules = append(rules, rule.RequiredWith(strings.Split(arg, ",")...))
		default:
			t.Fatalf("golden parser: unknown rule %q", name)
		}
	}

	return rules
}

// numbers re-decodes JSON numbers as json.Number, the way request input is.
func numbers(t *testing.T, value map[string]any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}

	return out
}

func readJSON(t *testing.T, path string, dst any) {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
