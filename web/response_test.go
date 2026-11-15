package web

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
)

// The body is the redirect page browsers render for a client that ignores
// Location, the location HTML-escaped in it.
func TestRedirectIsAFoundWithALinkingPage(t *testing.T) {
	res := Redirect("https://example.test/?a=1&b=<2>")

	want := `<!DOCTYPE html>
<html>
    <head>
        <meta charset="UTF-8" />
        <meta http-equiv="refresh" content="0;url='https://example.test/?a=1&amp;b=&lt;2&gt;'" />

        <title>Redirecting to https://example.test/?a=1&amp;b=&lt;2&gt;</title>
    </head>
    <body>
        Redirecting to <a href="https://example.test/?a=1&amp;b=&lt;2&gt;">https://example.test/?a=1&amp;b=&lt;2&gt;</a>.
    </body>
</html>`

	if res.Status() != http.StatusFound || string(res.Body()) != want {
		t.Fatalf("response = %d\n%s", res.Status(), res.Body())
	}

	if got := res.Header("Location"); got != "https://example.test/?a=1&b=<2>" {
		t.Errorf("Location = %q", got)
	}

	if got := res.Header("Content-Type"); got != "text/html; charset=UTF-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestHeaderReadsAResponseHeaderByAnyCase(t *testing.T) {
	res := JSON(map[string]bool{"ok": true}).WithHeader("Vary", "Origin")

	if res.Header("vary") != "Origin" || res.Header("Content-Type") != "application/json" {
		t.Fatalf("Vary %q, Content-Type %q", res.Header("vary"), res.Header("Content-Type"))
	}

	if got := res.Header("X-Frame-Options"); got != "" {
		t.Fatalf("an unset header = %q", got)
	}
}

// ToResponse maps a value as the kernel sends it, and beside an error hands
// back a response holding only the error's status (or the response a layer
// further in decorated), with the error untouched. Nothing is a 500.
func TestToResponseIsTheOutcomeAMiddlewareDecorates(t *testing.T) {
	conflict, down := Abort(409, "Taken."), errors.New("db down")
	carrier := NoContent().WithHeader("X-Carried", "yes")

	cases := []struct {
		name        string
		value       any
		err         error
		status      int
		contentType string
		body        string
		// wantErr is the error's text, or its start for a wrapped one; ""
		// when there is none.
		wantErr string
	}{
		{"a string", "OK", nil, 200, "application/json", `"OK"`, ""},
		{"an HTML response", HTML("OK"), nil, 200, "text/html; charset=UTF-8", "OK", ""},
		{"nil", nil, nil, 500, "", "", errNoResponse.Error()},
		{"a nil response", (*Response)(nil), nil, 500, "", "", errNoResponse.Error()},
		{"a map", map[string]bool{"ok": true}, nil, 200, "application/json", `{"ok":true}`, ""},
		{"an expected error", nil, conflict, 409, "", "", "Taken."},
		{"an unexpected error", nil, down, 500, "", "", "db down"},
		{"a response beside an error", carrier, conflict, 204, "", "", "Taken."},
		{"an unencodable value", map[string]float64{"x": math.Inf(1)}, nil, 500, "", "", "web: encoding response: "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ToResponse(tc.value, tc.err)

			if res.Status() != tc.status || res.Header("Content-Type") != tc.contentType || string(res.Body()) != tc.body {
				t.Fatalf("response = %d %q %q", res.Status(), res.Header("Content-Type"), res.Body())
			}

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			case tc.err != nil && !errors.Is(err, tc.err):
				t.Fatalf("err = %v, want the error given, untouched", err)
			}

			if carried, ok := tc.value.(*Response); ok && carried != nil && res != carried {
				t.Fatal("the response beside the error was replaced")
			}
		})
	}
}
