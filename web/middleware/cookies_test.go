package middleware

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/atobeach/reef/cookie"
	"github.com/atobeach/reef/crypt"
	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/web"
)

// Cookies queued before a failure go out on its error response, sealed
// unless excepted, as on a success.
func TestCookiesQueuedBeforeAFailureAreSealedOnTheErrorResponse(t *testing.T) {
	encrypter, err := crypt.New("base64:a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s=")
	if err != nil {
		t.Fatal(err)
	}

	router := web.NewRouter()
	router.Post("/claim", func(c *web.Context) (any, error) {
		c.QueueCookie(&http.Cookie{Name: "sealed", Value: "secret"})
		c.QueueCookie(&http.Cookie{Name: "raw", Value: "plain"})

		return nil, web.Abort(http.StatusConflict, "Taken.")
	}).Middleware(Cookies(encrypter, "raw"))

	kernel, err := web.NewKernel(nil, web.KernelConfig{Router: router})
	if err != nil {
		t.Fatal(err)
	}

	res := testkit.NewClient(t, kernel).Post("/claim").
		AssertConflict().
		AssertBody(`{"message":"Taken."}`).
		AssertCookie("sealed").
		AssertCookie("raw")

	sealed, err := url.QueryUnescape(res.Cookie("sealed").Value)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := cookie.Decrypt(encrypter, "sealed", sealed); !ok || value != "secret" {
		t.Fatalf("sealed opens as %q, %v; want the queued value", value, ok)
	}
	if got := res.Cookie("raw").Value; got != "plain" {
		t.Fatalf("raw = %q, want it as sent", got)
	}
}
