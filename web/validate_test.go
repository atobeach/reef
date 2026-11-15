package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/validation"
	"github.com/atobeach/reef/validation/rule"
)

func TestParsesBracketedQueryStrings(t *testing.T) {
	got, err := parseQuery("child_ages[]=3&child_ages[]=7&departure_times[outbound]=morning&a.b=1&first+name=x&n[0]=a&n[1]=b&gap[0]=a&gap[2]=c&q=one&q=two")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"child_ages":      []any{"3", "7"},
		"departure_times": map[string]any{"outbound": "morning"},
		"a_b":             "1",
		"first_name":      "x",
		"n":               []any{"a", "b"},
		// Not 0..n-1, so it stays an object.
		"gap": map[string]any{"0": "a", "2": "c"},
		// Without brackets the last value wins.
		"q": "two",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseQuery =\n%#v\nwant\n%#v", got, want)
	}
}

// Top-level keys 0..n-1 are still the input's fields, never a list in its
// place.
func TestIndexKeysAtTheTopLevelStayFields(t *testing.T) {
	for _, target := range []string{"/?0=a&1=b", "/?1=b&0=a"} {
		input, err := newContext(httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil), nil).All()
		if want := (validation.Input{"0": "a", "1": "b"}); err != nil || !reflect.DeepEqual(input, want) {
			t.Errorf("GET %s: All() = %#v, %v; want %#v", target, input, err, want)
		}
	}
}

func TestAllMergesBodyOverQueryAndNormalizes(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/flights/search?origin=Query&page=2", strings.NewReader(`{"origin":"  Lisbon  ","destination":"","adults":2,"password":"  keep  "}`))
	req.Header.Set("Content-Type", "application/json")

	input, err := newContext(req, nil).All()
	if err != nil {
		t.Fatal(err)
	}

	if input["origin"] != "Lisbon" || input["destination"] != nil || input["page"] != "2" || input["password"] != "  keep  " {
		t.Fatalf("input = %#v", input)
	}

	if n, ok := input["adults"].(interface{ String() string }); !ok || n.String() != "2" {
		t.Fatalf("adults should stay a JSON number, got %#v", input["adults"])
	}
}

type passenger struct {
	FirstName string `json:"first_name"`
	Age       int    `json:"age"`
}

type bookingRequest struct {
	Email      string      `json:"contact_email"`
	Adults     int         `json:"adults"`
	ChildAges  []int       `json:"child_ages"`
	Passengers []passenger `json:"passengers"`
	Note       *string     `json:"note"`
}

func (bookingRequest) Rules() rule.Set {
	return rule.Set{
		"contact_email":           {rule.Required, rule.String, rule.Email},
		"adults":                  {rule.Required, rule.Integer, rule.Min(1), rule.Max(9)},
		"child_ages":              {rule.Nullable, rule.Array},
		"child_ages.*":            {rule.Integer, rule.Min(0), rule.Max(17)},
		"passengers":              {rule.Required, rule.Array, rule.Min(1)},
		"passengers.*.first_name": {rule.Required, rule.String, rule.Min(2)},
		"passengers.*.age":        {rule.Required, rule.Integer},
		"note":                    {rule.Nullable, rule.String},
	}
}

func (bookingRequest) Messages() map[string]string {
	return map[string]string{"contact_email.email": "Please enter a valid email address."}
}

func (r bookingRequest) After(_ *Context, errs *validation.Errors) error {
	if errs.HasAny("passengers") {
		return nil
	}

	if len(r.Passengers) > 9 {
		errs.Add("passengers", "At most 9 passengers can travel together.")
	}

	return nil
}

func TestValidateDecodesTypedRequests(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"contact_email":"a@b.co","adults":"2","child_ages":[3,"5"],"passengers":[{"first_name":"Ana","age":30}]}`))
	req.Header.Set("Content-Type", "application/json")

	got, err := Validate[bookingRequest](newContext(req, nil))
	if err != nil {
		t.Fatal(err)
	}

	want := bookingRequest{Email: "a@b.co", Adults: 2, ChildAges: []int{3, 5}, Passengers: []passenger{{FirstName: "Ana", Age: 30}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded = %+v", got)
	}
}

// profileRequest embeds ValidatedInput: an update writes only what was sent.
type profileRequest struct {
	ValidatedInput
	Name      *string `json:"name"`
	Nickname  *string `json:"nickname"`
	ChildAges []int   `json:"child_ages"`
	Bio       *string `json:"bio"`
}

func (profileRequest) Rules() rule.Set {
	return rule.Set{
		"name":         {rule.Sometimes, rule.String},
		"nickname":     {rule.Nullable, rule.String},
		"child_ages":   {rule.Nullable, rule.Array},
		"child_ages.*": {rule.Integer},
		"bio":          {rule.Nullable, rule.String},
	}
}

func TestValidatedIsThePresentRuledInputAsSentInFieldOrder(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		// An explicit null is kept, an absent field and an unruled key are not.
		{"present keys only", `{"bio":"Hi","extra":1,"nickname":null}`, `{"nickname":null,"bio":"Hi"}`},
		// An empty list has no elements to validate: it keeps its place.
		{"empty list in place", `{"bio":"Hi","child_ages":[],"name":"Ana"}`, `{"name":"Ana","child_ages":[],"bio":"Hi"}`},
		// A list validated element by element comes after the plain fields,
		// its values as sent ("2" stays a string).
		{"element-wise list last", `{"child_ages":[3,"2"],"bio":null,"name":"Ana"}`, `{"name":"Ana","bio":null,"child_ages":[3,"2"]}`},
		// The embedded ValidatedInput is not an input field.
		{"embedded type is no field", `{"ValidatedInput":1,"name":"Ana"}`, `{"name":"Ana"}`},
	}

	for _, tc := range cases {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")

		got, err := Validate[profileRequest](newContext(req, nil))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		encoded, err := json.Marshal(got.Validated())
		if err != nil {
			t.Fatal(err)
		}

		if string(encoded) != tc.want {
			t.Errorf("%s: Validated() = %s, want %s", tc.name, encoded, tc.want)
		}
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (loginRequest) Rules() rule.Set {
	return rule.Set{"email": {rule.Required, rule.Email}, "password": {rule.Required, rule.String}}
}

// The input layer leaves a password's surrounding whitespace alone, and so
// must decoding: it is part of the password.
func TestAPasswordDecodesWithItsSurroundingWhitespace(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"email":"  a@b.co ","password":"  keep me  "}`))
	req.Header.Set("Content-Type", "application/json")

	got, err := Validate[loginRequest](newContext(req, nil))
	if err != nil {
		t.Fatal(err)
	}

	if got != (loginRequest{Email: "a@b.co", Password: "  keep me  "}) {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestValidationFailuresRenderA422InFieldOrder(t *testing.T) {
	router := NewRouter()
	router.Post("/book", func(c *Context) (string, error) {
		if _, err := Validate[bookingRequest](c); err != nil {
			return "", err
		}

		return "ok", nil
	})

	kernel, _ := NewKernel(nil, KernelConfig{Router: router})
	res := testkit.NewClient(t, kernel).Send(http.MethodPost, "/book", []byte(`{"contact_email":"nope","adults":0,"passengers":[{"first_name":"A"}]}`), "application/json")

	want := `{"message":"Please enter a valid email address. (and 3 more errors)","errors":{` +
		`"contact_email":["Please enter a valid email address."],` +
		`"adults":["The adults field must be at least 1."],` +
		`"passengers.0.first_name":["The passengers.0.first_name field must be at least 2 characters."],` +
		`"passengers.0.age":["The passengers.0.age field is required."]}}`
	if res.Status() != 422 || res.Body() != want {
		t.Fatalf("response = %d\n%s\nwant\n%s", res.Status(), res.Body(), want)
	}
}

// An After hook reads its receiver, the decoded request, and runs even
// when the rules failed.
func TestAfterReadsTheDecodedRequest(t *testing.T) {
	passengers := strings.TrimSuffix(strings.Repeat(`{"first_name":"Ana","age":30},`, 10), ",")

	for _, tc := range []struct {
		name  string
		email string
		want  []string
	}{
		{"rules passed", "a@b.co", []string{"passengers"}},
		{"rules failed", "nope", []string{"contact_email", "passengers"}},
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"contact_email":"`+tc.email+`","adults":1,"passengers":[`+passengers+`]}`))
		req.Header.Set("Content-Type", "application/json")

		_, err := Validate[bookingRequest](newContext(req, nil))

		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || validationErr.Errors.Count() != len(tc.want) || !validationErr.Errors.HasAny(tc.want...) ||
			validationErr.Errors.Get("passengers")[0] != "At most 9 passengers can travel together." {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
}

// shapesRequest's fields are ruled only by presence, so its Go types alone
// decide what fits. Code carries a real rule, and After refuses one code.
type shapesRequest struct {
	Tags    []string `json:"tags"`
	Count   int      `json:"count"`
	Price   float64  `json:"price"`
	Contact struct {
		Email string `json:"email"`
	} `json:"contact"`
	Note *string `json:"note"`
	Code string  `json:"code"`
}

func (shapesRequest) Rules() rule.Set {
	return rule.Set{
		"tags":          {rule.Sometimes},
		"count":         {rule.Sometimes},
		"price":         {rule.Sometimes},
		"contact.email": {rule.Nullable},
		"note":          {rule.Nullable},
		"code":          {rule.Sometimes, rule.String, rule.Max(5)},
	}
}

func (shapesRequest) Messages() map[string]string {
	return map[string]string{"price.numeric": "A price is a number."}
}

func (shapesRequest) Attributes() map[string]string {
	return map[string]string{"count": "head count"}
}

func (r shapesRequest) After(_ *Context, errs *validation.Errors) error {
	if r.Code == "taken" {
		errs.Add("code", "That code is taken.")
	}

	return nil
}

// A value the request's type can't hold is a 422 on its path, worded as the
// rule the type implies, never a 500. It is reported only when the rules
// and hooks found nothing, since their errors already explain the input.
func TestAValueTheTypeCannotHoldIsA422OnItsPath(t *testing.T) {
	router := NewRouter()
	router.Post("/shapes", func(c *Context) (any, error) {
		return Validate[shapesRequest](c)
	})

	kernel, err := NewKernel(nil, KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{
			"each kind's rule, with the request's own message and attribute name",
			`{"tags":{"x":"a"},"count":"many","price":"cheap","contact":"x","note":{"a":1}}`, 422,
			`{"message":"The tags field must be a list. (and 4 more errors)","errors":{` +
				`"tags":["The tags field must be a list."],` +
				`"count":["The head count field must be an integer."],` +
				`"price":["A price is a number."],` +
				`"contact":["The contact field must be an array."],` +
				`"note":["The note field must be a string."]}}`,
		},
		{
			"a list element is shown raw, a nested field as its path",
			`{"tags":["a",["b"]],"contact":{"email":{"a":1}}}`, 422,
			`{"message":"The tags.1 field must be a string. (and 1 more error)","errors":{` +
				`"tags.1":["The tags.1 field must be a string."],` +
				`"contact.email":["The contact.email field must be a string."]}}`,
		},
		{
			"a rule failure suppresses them",
			`{"tags":{"x":"a"},"count":"many","code":"too long"}`, 422,
			`{"message":"The code field must not be greater than 5 characters.","errors":{"code":["The code field must not be greater than 5 characters."]}}`,
		},
		{
			"a hook's error suppresses them",
			`{"tags":{"x":"a"},"count":"many","code":"taken"}`, 422,
			`{"message":"That code is taken.","errors":{"code":["That code is taken."]}}`,
		},
		{
			"an index-keyed object fills a list and numeric text a number",
			`{"tags":{"1":"b","0":"a"},"count":"3","price":"4.5","note":null}`, 200,
			`{"tags":["a","b"],"count":3,"price":4.5,"contact":{"email":""},"note":null,"code":""}`,
		},
	}

	client := testkit.NewClient(t, kernel)
	for _, tc := range cases {
		res := client.Send(http.MethodPost, "/shapes", []byte(tc.body), "application/json")
		if res.Status() != tc.status || res.Body() != tc.want {
			t.Errorf("%s: response = %d\n%s\nwant %d\n%s", tc.name, res.Status(), res.Body(), tc.status, tc.want)
		}
	}
}

type strayRuleRequest struct {
	Name string `json:"name"`
}

func (strayRuleRequest) Rules() rule.Set {
	return rule.Set{"name": {rule.Required}, "nickname": {rule.String}}
}

type unruledFieldRequest struct {
	Name     string `json:"name"`
	Nickname string `json:"nickname"`
}

func (unruledFieldRequest) Rules() rule.Set { return rule.Set{"name": {rule.Required}} }

type unfillableFieldRequest struct {
	Updates chan int `json:"updates"`
}

func (unfillableFieldRequest) Rules() rule.Set { return rule.Set{"updates": {rule.Sometimes}} }

// nestedRequest's object field needs no rule: its own fields carry them.
type nestedRequest struct {
	Contact struct {
		Email string `json:"email"`
	} `json:"contact"`
}

func (nestedRequest) Rules() rule.Set { return rule.Set{"contact.email": {rule.Required}} }

// keptRequest's response is kept as sent, so a rule may reach inside it.
type keptRequest struct {
	Response any `json:"response"`
}

func (keptRequest) Rules() rule.Set {
	return rule.Set{"response": {rule.Required, rule.Array}, "response.id": {rule.Required, rule.String}}
}

type pointerMessagesRequest struct { //nolint:recvcheck // the pointer-only Messages is the defect this case plants.
	Name string `json:"name"`
}

func (pointerMessagesRequest) Rules() rule.Set { return rule.Set{"name": {rule.Required}} }

func (*pointerMessagesRequest) Messages() map[string]string {
	return map[string]string{"name.required": "Name, please."}
}

type validatorAfterRequest struct {
	Name string `json:"name"`
}

func (validatorAfterRequest) Rules() rule.Set { return rule.Set{"name": {rule.Required}} }

func (validatorAfterRequest) After(*Context, *validation.Validator) error { return nil }

// formRequestCase checks T both ways: CheckFormRequest, and Validate behind a
// route, whose refusal is the reported 500.
type formRequestCase struct {
	check    func() error
	validate func(c *Context) error
	body     string
	want     string
}

func checked[T FormRequest](body string, want string) formRequestCase {
	return formRequestCase{
		check:    CheckFormRequest[T],
		validate: func(c *Context) error { _, err := Validate[T](c); return err },
		body:     body,
		want:     want,
	}
}

// Every rule names a field and every field has a rule, so the struct is
// exactly the input the rules vouch for; an optional method Validate can't
// call is refused rather than silently skipped.
func TestValidateRefusesARequestWhoseRulesAndFieldsDisagree(t *testing.T) {
	cases := map[string]formRequestCase{
		"a rule naming no field":             checked[strayRuleRequest](`{"name":"Ana"}`, `validation: rule "nickname" names no field of web.strayRuleRequest`),
		"a field without a rule":             checked[unruledFieldRequest](`{"name":"Ana"}`, `validation: field "nickname" of web.unruledFieldRequest has no rule`),
		"a field no input fills":             checked[unfillableFieldRequest](`{}`, `validation: field "updates" of web.unfillableFieldRequest is a chan int, which no input fills`),
		"a nested struct with ruled fields":  checked[nestedRequest](`{"contact":{"email":"a@b.co"}}`, ""),
		"a rule inside a value kept as sent": checked[keptRequest](`{"response":{"id":"r1"}}`, ""),
		"Messages on a pointer receiver":     checked[pointerMessagesRequest](`{"name":"Ana"}`, "web: web.pointerMessagesRequest's Messages must be Messages() map[string]string, on a value receiver"),
		"After with another signature":       checked[validatorAfterRequest](`{"name":"Ana"}`, "web: web.validatorAfterRequest's After must be After(c *web.Context, errs *validation.Errors) error"),
		"a pointer type":                     checked[*nestedRequest](`{"contact":{"email":"a@b.co"}}`, "web: a form request must be a struct type"),
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.check(); (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || err.Error() != tc.want)) {
				t.Fatalf("CheckFormRequest = %v, want %q", err, tc.want)
			}

			var reported []error
			router := NewRouter()
			router.Post("/check", func(c *Context) (string, error) { return "ok", tc.validate(c) })

			kernel, err := NewKernel(nil, KernelConfig{Router: router, Report: func(_ *Context, err error) { reported = append(reported, err) }})
			if err != nil {
				t.Fatal(err)
			}

			res := testkit.NewClient(t, kernel).Send(http.MethodPost, "/check", []byte(tc.body), "application/json")
			if tc.want == "" {
				res.AssertOk()

				return
			}

			res.AssertInternalServerError()
			if len(reported) != 1 || reported[0].Error() != tc.want {
				t.Fatalf("reported = %v", reported)
			}
		})
	}
}

var errLookupDown = errors.New("lookup down")

// voucherRequest's rule and hook fail to run when their lookup is down.
type voucherRequest struct {
	Code string `json:"code"`
}

func (voucherRequest) Rules() rule.Set {
	return rule.Set{"code": {rule.Required, rule.Custom("voucher", func(f *rule.Field) (bool, error) {
		if f.Value == "rule-down" {
			return false, errLookupDown
		}

		return f.Value != "expired", nil
	}, "The voucher has expired.")}}
}

func (r voucherRequest) After(*Context, *validation.Errors) error {
	if r.Code == "hook-down" {
		return errLookupDown
	}

	return nil
}

// A rule or hook that couldn't run is the server's failure, reported as a
// 500, never a 422 telling the user their input is wrong.
func TestARuleThatCannotRunIsA500NotA422(t *testing.T) {
	var reported []error
	router := NewRouter()
	router.Post("/redeem", func(c *Context) (string, error) {
		if _, err := Validate[voucherRequest](c); err != nil {
			return "", err
		}

		return "ok", nil
	})

	kernel, err := NewKernel(nil, KernelConfig{Router: router, Report: func(_ *Context, err error) { reported = append(reported, err) }})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		code   string
		status int
		body   string
	}{
		{"rule-down", 500, `{"message":"Server Error"}`},
		{"hook-down", 500, `{"message":"Server Error"}`},
		{"expired", 422, `{"message":"The voucher has expired.","errors":{"code":["The voucher has expired."]}}`},
		{"SUMMER", 200, `"ok"`},
	}

	client := testkit.NewClient(t, kernel)
	for _, tc := range cases {
		res := client.Send(http.MethodPost, "/redeem", []byte(`{"code":"`+tc.code+`"}`), "application/json")

		if res.Status() != tc.status || res.Body() != tc.body {
			t.Errorf("%s: response = %d %s", tc.code, res.Status(), res.Body())
		}
	}

	if len(reported) != 2 || !errors.Is(reported[0], errLookupDown) || !errors.Is(reported[1], errLookupDown) {
		t.Fatalf("reported = %v", reported)
	}
}

func TestQueryParsingBoundsNestingAndVariableCount(t *testing.T) {
	// 64 levels are kept; 65 are dropped.
	got, err := parseQuery("b" + strings.Repeat("[x]", 64) + "=2&c" + strings.Repeat("[x]", 65) + "=3")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := got["b"]; !ok {
		t.Fatal("a 64-level key must be kept")
	}

	if _, ok := got["c"]; ok {
		t.Fatal("a 65-level key must be dropped")
	}

	// Everything past the 1000th variable is ignored.
	var pairs []string
	for i := range 1500 {
		pairs = append(pairs, "v"+strconv.Itoa(i)+"=1")
	}

	got, _ = parseQuery(strings.Join(pairs, "&"))
	if len(got) != 1000 {
		t.Fatalf("kept %d variables, want 1000", len(got))
	}
}

// A body of 2M nested brackets is one dropped variable, never a stack
// overflow.
func TestDeeplyNestedFormBodiesCannotExhaustTheStack(t *testing.T) {
	body := "a" + strings.Repeat("[]", 2_000_000) + "=1"

	got, err := parseQuery(body)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d keys, %v", len(got), err)
	}
}
