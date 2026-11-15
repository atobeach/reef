package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/validation"
	"github.com/atobeach/reef/validation/rule"
)

func jsonRequest(t *testing.T, target string, body string) *Context {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	return newContext(req, nil)
}

// oversized is a JSON body past the 8 MB cap whose tail, read on its own,
// decodes to an empty object.
func oversized() string {
	return `{"email":"a@b.co","password":"secret","padding":"` + strings.Repeat("a", maxBodyBytes) + `"}`
}

// A body that couldn't be read is the same error on every later call, never
// its tail parsed as `{}`.
func TestUnreadableInputIsTheSameErrorOnEveryRead(t *testing.T) {
	cases := []struct {
		name   string
		target string
		body   string
		status int
	}{
		{"body over the cap", "/", oversized(), http.StatusRequestEntityTooLarge},
		{"malformed query", "/?q=%zz", `{"email":"a@b.co"}`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		c := jsonRequest(t, tc.target, tc.body)

		if c.Input("email") != nil || c.Has("email") || c.String("email") != "" {
			t.Errorf("%s: a reader saw input", tc.name)
		}

		for range 2 {
			var httpErr *Error
			if _, err := c.All(); !errors.As(err, &httpErr) || httpErr.Code != tc.status {
				t.Errorf("%s: All() = %v, want a %d", tc.name, err, tc.status)
			}
		}
	}
}

// A reader before validation (the CSRF token, a limiter key) leaves the
// oversized body a 413, not a 422 for fields it never got to read.
func TestABodyOverTheCapIsA413AfterAnEarlierRead(t *testing.T) {
	readsToken := func(c *Context, next Handler) (any, error) {
		_ = c.String("_token")

		return next(c)
	}

	router := NewRouter()
	router.Post("/login", func(c *Context) (string, error) {
		if _, err := Validate[loginRequest](c); err != nil {
			return "", err
		}

		return "ok", nil
	}).Middleware(readsToken)

	kernel, err := NewKernel(nil, KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	testkit.NewClient(t, kernel).
		Send(http.MethodPost, "/login", []byte(oversized()), "application/json").
		AssertStatus(http.StatusRequestEntityTooLarge)
}

func TestReadersIndexListsWithDottedKeys(t *testing.T) {
	c := jsonRequest(t, "/?seconds=", `{"passengers":[{"first_name":"Ana"}],"messages":{"1":"b","0":"a"}}`)

	if got := c.Input("passengers.0.first_name"); got != "Ana" {
		t.Errorf(`Input("passengers.0.first_name") = %v`, got)
	}

	if messages, ok := c.Array("messages"); !ok || len(messages) != 2 || messages[0] != "a" || messages[1] != "b" {
		t.Errorf(`Array("messages") = %v, %v`, messages, ok)
	}

	// An empty query value is present, and null.
	if !c.Has("seconds") || c.Input("seconds") != nil || c.Has("seconds", "minutes") {
		t.Errorf("Has(seconds) = %v, Input(seconds) = %v", c.Has("seconds"), c.Input("seconds"))
	}
}

type emailRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (emailRequest) Rules() rule.Set {
	return rule.Set{"email": {rule.Required, rule.Email}, "name": {rule.Required, rule.String}}
}

// Merged values replace the input's own for validation and every later read.
func TestMergeIsSeenByValidateAndAll(t *testing.T) {
	c := jsonRequest(t, "/", `{"email":"Ana@Example.COM"}`)

	c.Merge(map[string]any{"email": "ana@example.com", "name": "Ana"})

	request, err := Validate[emailRequest](c)
	if err != nil || request.Email != "ana@example.com" || request.Name != "Ana" {
		t.Fatalf("Validate = %+v, %v", request, err)
	}

	input, err := c.All()
	if err != nil || input["email"] != "ana@example.com" || input["name"] != "Ana" {
		t.Fatalf("All = %v, %v", input, err)
	}
}

// Merging into unreadable input leaves its error in place.
func TestMergeKeepsAnUnreadableBodyUnreadable(t *testing.T) {
	c := jsonRequest(t, "/", oversized())

	c.Merge(map[string]any{"email": "ana@example.com"})

	var httpErr *Error
	if _, err := Validate[emailRequest](c); !errors.As(err, &httpErr) || httpErr.Code != http.StatusRequestEntityTooLarge || c.Input("email") != nil {
		t.Fatalf("Validate = %v after merging into a 413", err)
	}
}

// requestAwareRequest's hook reads the request it validates, not only its
// input.
type requestAwareRequest struct{}

func (requestAwareRequest) Rules() rule.Set { return rule.Set{} }

func (requestAwareRequest) After(c *Context, errs *validation.Errors) error {
	if c.Param("code") != "SUMMER" {
		errs.Add("code", "Unknown code "+c.Param("code")+".")
	}

	return nil
}

func TestAfterHooksReceiveTheRequest(t *testing.T) {
	router := NewRouter()
	router.Post("/vouchers/{code}", func(c *Context) (string, error) {
		if _, err := Validate[requestAwareRequest](c); err != nil {
			return "", err
		}

		return "ok", nil
	})

	for path, want := range map[string]string{
		"/vouchers/SUMMER": `"ok"`,
		"/vouchers/WINTER": `{"message":"Unknown code WINTER.","errors":{"code":["Unknown code WINTER."]}}`,
	} {
		if res := serve(t, router, nil, http.MethodPost, path); res.Body() != want {
			t.Errorf("POST %s = %d %s", path, res.Status(), res.Body())
		}
	}
}
