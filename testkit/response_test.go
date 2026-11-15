package testkit_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/atobeach/reef/testkit"
)

// respond is a recorded response with this status, headers and body.
func respond(status int, headers map[string]string, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// cutOff writes body, then aborts the response as a stream that panicked
// mid-way does.
func cutOff(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))

		panic(http.ErrAbortHandler)
	})
}

// setCookies answers 200 with these Set-Cookie headers.
func setCookies(cookies ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, c := range cookies {
			w.Header().Add("Set-Cookie", c)
		}
	})
}

func TestResponseAssertionsPassOnAMatchAndFailSayingWhatTheyGot(t *testing.T) {
	jsonOK := respond(200, map[string]string{"Content-Type": "application/json"}, `{"b":2,"a":[1,{"slug":"x"}],"price":1.5}`)
	invalid := respond(422, nil, `{"message":"invalid","errors":{"email":["The email field is required.","The email field must be a valid email address."],"departure_times.outbound":["The selected departure times.outbound is invalid."]}}`)
	nested := respond(422, nil, `{"errors":{"departure_times":{"outbound":["nested, not the validator's flat key"]}}}`)

	cases := []struct {
		name    string
		handler http.Handler
		assert  func(r *testkit.TestResponse)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"status", jsonOK, func(r *testkit.TestResponse) { r.AssertOk() }, ""},
		{"wrong status", jsonOK, func(r *testkit.TestResponse) { r.AssertCreated() }, "status = 200, want 201"},
		{"method not allowed", respond(405, nil, ""), func(r *testkit.TestResponse) { r.AssertMethodNotAllowed() }, ""},
		{"not a 405", jsonOK, func(r *testkit.TestResponse) { r.AssertMethodNotAllowed() }, "status = 200, want 405"},
		{"conflict", respond(409, nil, ""), func(r *testkit.TestResponse) { r.AssertConflict() }, ""},
		{"not a 409", jsonOK, func(r *testkit.TestResponse) { r.AssertConflict() }, "status = 200, want 409"},
		{"gone", respond(410, nil, ""), func(r *testkit.TestResponse) { r.AssertGone() }, ""},
		{"not a 410", jsonOK, func(r *testkit.TestResponse) { r.AssertGone() }, "status = 200, want 410"},
		{"too many requests", respond(429, nil, ""), func(r *testkit.TestResponse) { r.AssertTooManyRequests() }, ""},
		{"not a 429", jsonOK, func(r *testkit.TestResponse) { r.AssertTooManyRequests() }, "status = 200, want 429"},
		{"internal server error", respond(500, nil, ""), func(r *testkit.TestResponse) { r.AssertInternalServerError() }, ""},
		{"not a 500", jsonOK, func(r *testkit.TestResponse) { r.AssertInternalServerError() }, "status = 200, want 500"},
		{"service unavailable", respond(503, nil, ""), func(r *testkit.TestResponse) { r.AssertServiceUnavailable() }, ""},
		{"not a 503", jsonOK, func(r *testkit.TestResponse) { r.AssertServiceUnavailable() }, "status = 200, want 503"},
		{"exact JSON in any key order", jsonOK, func(r *testkit.TestResponse) {
			r.AssertJSON(map[string]any{"a": []any{1, map[string]any{"slug": "x"}}, "b": 2, "price": 1.5})
		}, ""},
		{"JSON missing a key", jsonOK, func(r *testkit.TestResponse) { r.AssertJSON(map[string]any{"b": 2}) }, "json = "},
		{"a path", jsonOK, func(r *testkit.TestResponse) { r.AssertJSONPath("a.1.slug", "x").AssertJSONPath("b", 2) }, ""},
		{"a path with another value", jsonOK, func(r *testkit.TestResponse) { r.AssertJSONPath("price", 2) }, `json path "price" = 1.5, want 2`},
		{"a missing path", jsonOK, func(r *testkit.TestResponse) { r.AssertJSONPath("a.5", 1) }, `json path "a.5" missing`},
		{"a count", jsonOK, func(r *testkit.TestResponse) { r.AssertJSONCount(2, "a") }, ""},
		{"a wrong count", jsonOK, func(r *testkit.TestResponse) { r.AssertJSONCount(3, "a") }, `json path "a" has 2 items, want 3`},
		{"a header", jsonOK, func(r *testkit.TestResponse) {
			r.AssertHeader("Content-Type", "application/json").AssertHeaderMissing("Location")
		}, ""},
		{"a header that is there", jsonOK, func(r *testkit.TestResponse) { r.AssertHeaderMissing("Content-Type") }, "header Content-Type"},
		{"validation errors", invalid, func(r *testkit.TestResponse) { r.AssertJSONValidationErrors("email") }, ""},
		{"a field without an error", invalid, func(r *testkit.TestResponse) { r.AssertJSONValidationErrors("name") }, `no validation error for "name"`},
		{"a validation message, any of the field's", invalid, func(r *testkit.TestResponse) {
			r.AssertJSONValidationError("email", "The email field is required.").
				AssertJSONValidationError("email", "The email field must be a valid email address.")
		}, ""},
		{"a validation key with dots, taken literally", invalid, func(r *testkit.TestResponse) {
			r.AssertJSONValidationError("departure_times.outbound", "The selected departure times.outbound is invalid.")
		}, ""},
		{"a dotted key never walks into a nested object", nested, func(r *testkit.TestResponse) {
			r.AssertJSONValidationError("departure_times.outbound", "nested, not the validator's flat key")
		}, `no validation error for "departure_times.outbound"`},
		{"another validation message", invalid, func(r *testkit.TestResponse) {
			r.AssertJSONValidationError("email", "The email field is required")
		}, `validation errors for "email" = ["The email field is required.","The email field must be a valid email address."], want "The email field is required"`},
		{"a validation message for a field without an error", invalid, func(r *testkit.TestResponse) {
			r.AssertJSONValidationError("name", "The name field is required.")
		}, `no validation error for "name"`},
		{"a validation message on a success", jsonOK, func(r *testkit.TestResponse) {
			r.AssertJSONValidationError("email", "The email field is required.")
		}, "status = 200, want 422"},
		{"missing validation errors", invalid, func(r *testkit.TestResponse) { r.AssertJSONMissingValidationErrors("name", "departure_times") }, ""},
		{"a validation error that is there", invalid, func(r *testkit.TestResponse) {
			r.AssertJSONMissingValidationErrors("name", "email")
		}, `validation errors for "email" = ["The email field is required.","The email field must be a valid email address."], want none`},
		{"no validation errors at all", jsonOK, func(r *testkit.TestResponse) { r.AssertJSONMissingValidationErrors() }, ""},
		{"any validation error, with no key named", invalid, func(r *testkit.TestResponse) { r.AssertJSONMissingValidationErrors() }, "want none"},
		{"a cookie", setCookies("theme=dark; Path=/; Max-Age=60"), func(r *testkit.TestResponse) { r.AssertCookie("theme") }, ""},
		{"a session cookie, no lifetime", setCookies("theme=dark; Path=/"), func(r *testkit.TestResponse) { r.AssertCookie("theme") }, ""},
		{"no such cookie", setCookies("lang=en; Path=/"), func(r *testkit.TestResponse) { r.AssertCookie("theme") }, `no theme cookie set; Set-Cookie: ["lang=en; Path=/"]`},
		{"a forget is no cookie", setCookies("theme=; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Max-Age=0"), func(r *testkit.TestResponse) {
			r.AssertCookie("theme")
		}, "no theme cookie set"},
		{"an Expires already past is no cookie", setCookies("theme=dark; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT"), func(r *testkit.TestResponse) {
			r.AssertCookie("theme")
		}, "no theme cookie set"},
		{"Max-Age outranks a past Expires", setCookies("theme=dark; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Max-Age=60"), func(r *testkit.TestResponse) {
			r.AssertCookie("theme")
		}, ""},
		{"a missing cookie", setCookies("lang=en; Path=/"), func(r *testkit.TestResponse) { r.AssertCookieMissing("theme") }, ""},
		{"a cookie that is there", setCookies("theme=dark; Path=/; Max-Age=60"), func(r *testkit.TestResponse) {
			r.AssertCookieMissing("theme")
		}, "Set-Cookie theme=dark; Path=/; Max-Age=60, want no theme cookie"},
		{"a forget is still a cookie sent", setCookies("theme=; Path=/; Max-Age=0"), func(r *testkit.TestResponse) {
			r.AssertCookieMissing("theme")
		}, "want no theme cookie"},
		{"an expired cookie", setCookies("theme=; Path=/; Max-Age=0"), func(r *testkit.TestResponse) { r.AssertCookieExpired("theme") }, ""},
		{"a cookie still set is not expired", setCookies("theme=dark; Path=/; Max-Age=60"), func(r *testkit.TestResponse) {
			r.AssertCookieExpired("theme")
		}, `theme cookie not expired; Set-Cookie: ["theme=dark; Path=/; Max-Age=60"]`},
		{"no such cookie is not expired", setCookies("lang=en; Path=/"), func(r *testkit.TestResponse) { r.AssertCookieExpired("theme") }, "theme cookie not expired"},
		{"a redirect", respond(302, map[string]string{"Location": "/home"}, ""), func(r *testkit.TestResponse) { r.AssertRedirect("/home") }, ""},
		{"no redirect", jsonOK, func(r *testkit.TestResponse) { r.AssertRedirect("") }, "status = 200, want a redirect"},
		{"no content", respond(204, nil, ""), func(r *testkit.TestResponse) { r.AssertNoContent() }, ""},
		{"aborted, with what was written before", cutOff("partial"), func(r *testkit.TestResponse) { r.AssertOk().AssertAborted().AssertBody("partial") }, ""},
		{"a completed response", jsonOK, func(r *testkit.TestResponse) { r.AssertAborted() }, "the response completed, want it cut off"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recording := testkit.Record(t)
			c.assert(testkit.NewClient(recording, c.handler).Get("/"))

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}

func TestTheResponseReadsBackItsStatusHeadersAndBody(t *testing.T) {
	res := testkit.NewClient(t, respond(201, map[string]string{"X-Request-Id": "r-1", "Location": "/trips/7"}, "made")).Get("/")

	if res.Status() != 201 || res.Body() != "made" || res.Header("location") != "/trips/7" || res.Header("X-Missing") != "" {
		t.Fatalf("status %d, body %q, Location %q", res.Status(), res.Body(), res.Header("location"))
	}

	if got, want := res.HeaderNames(), []string{"Location", "X-Request-Id"}; !slices.Equal(got, want) {
		t.Fatalf("HeaderNames = %q, want %q", got, want)
	}
}

func TestCookiesAreEverySetCookieInOrder(t *testing.T) {
	res := testkit.NewClient(t, setCookies(
		"theme=dark; Path=/; Domain=example.test; Max-Age=60; HttpOnly; Secure; SameSite=Lax",
		"theme=; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Max-Age=0",
		"lang=en; Path=/account; SameSite=Strict",
	)).Get("/")

	cookies := res.Cookies()
	if len(cookies) != 3 {
		t.Fatalf("Cookies = %v", cookies)
	}

	first, forget, lang := cookies[0], cookies[1], cookies[2]
	if first.Name != "theme" || first.Value != "dark" || first.Path != "/" || first.Domain != "example.test" || first.MaxAge != 60 ||
		!first.HttpOnly || !first.Secure || first.SameSite != http.SameSiteLaxMode {
		t.Errorf("first = %#v", first)
	}

	if forget.Name != "theme" || forget.Value != "" || forget.MaxAge >= 0 || !forget.Expires.Equal(time.Unix(0, 0)) {
		t.Errorf("a forget = %#v", forget)
	}

	if lang.Name != "lang" || lang.Path != "/account" || lang.HttpOnly || lang.Secure || lang.SameSite != http.SameSiteStrictMode {
		t.Errorf("lang = %#v", lang)
	}

	if res.Cookie("theme") != first || res.Cookie("lang") != lang || res.Cookie("missing") != nil {
		t.Errorf("Cookie(theme) = %v, Cookie(lang) = %v, Cookie(missing) = %v", res.Cookie("theme"), res.Cookie("lang"), res.Cookie("missing"))
	}
}

func TestValueAndPluckReadTheDecodedBody(t *testing.T) {
	res := testkit.NewClient(t, respond(200, nil, `{"cities":[{"slug":"paris"},{"slug":"rome"}]}`)).Get("/")

	if got := res.Value("cities.1.slug"); got != "rome" {
		t.Fatalf("Value = %v", got)
	}

	if got := res.Value("cities.9.slug"); got != nil {
		t.Fatalf("a missing path = %v, want nil", got)
	}

	if got := res.Pluck("cities", "slug"); len(got) != 2 || got[0] != "paris" || got[1] != "rome" {
		t.Fatalf("Pluck = %v", got)
	}
}

func TestEqualJSONComparesValuesAsTheirJSON(t *testing.T) {
	type point struct {
		X int `json:"x"`
	}

	cases := map[string]struct {
		got, want any
		equal     bool
	}{
		"an int and the stored number": {json.Number("42"), int64(42), true},
		"an int and a float64":         {42, 42.0, true},
		"a struct and its map":         {point{X: 1}, map[string]any{"x": 1}, true},
		"another number":               {json.Number("42"), 43, false},
		"a number and its string":      {"42", 42, false},
		"a map with another key":       {map[string]any{"x": 1}, map[string]any{"y": 1}, false},
	}

	for name, c := range cases {
		if got := testkit.EqualJSON(t, c.got, c.want); got != c.equal {
			t.Errorf("%s: EqualJSON(%v, %v) = %v, want %v", name, c.got, c.want, got, c.equal)
		}
	}
}

func TestSummaryCountsWhatAFakeRecordedInOrder(t *testing.T) {
	cases := map[string]struct {
		names []string
		want  string
	}{
		"nothing":  {nil, "nothing"},
		"one":      {[]string{"Start"}, "Start"},
		"repeated": {[]string{"Start", "Poll", "Poll", "Start", "Poll"}, "Start ×2, Poll ×3"},
	}

	for name, c := range cases {
		if got := testkit.Summary(c.names); got != c.want {
			t.Errorf("%s: Summary = %q, want %q", name, got, c.want)
		}
	}
}
