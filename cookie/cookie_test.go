package cookie

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/atobeach/reef/crypt"
)

// The cookie vector is sealed the way Laravel's EncryptCookies does it: the
// cookie-name prefix, then Laravel's Encrypter (AES-256-CBC with an
// HMAC-SHA256 MAC). It was re-sealed under a new name with a re-implementation
// of that scheme, checked first to reproduce the original vector byte for byte.
func TestOpensAnIssuedCookie(t *testing.T) {
	raw, err := os.ReadFile("../crypt/testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}

	var v struct {
		Key    string `json:"key"`
		Cookie struct {
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			Value  string `json:"value"`
		} `json:"cookie"`
	}
	_ = json.Unmarshal(raw, &v)

	e, _ := crypt.New(v.Key)

	if got := Prefix(v.Cookie.Name, e.Key()); got != v.Cookie.Prefix {
		t.Fatalf("prefix = %q, issued cookie's = %q", got, v.Cookie.Prefix)
	}

	if got, ok := Decrypt(e, v.Cookie.Name, v.Cookie.Value); !ok || got != "P9qWY7Qtc41V1DswADWx8SVMV04sQE38ZwGSBUyY" {
		t.Fatalf("decrypt = %q, %v", got, ok)
	}

	// The same payload presented under another cookie's name is refused.
	if _, ok := Decrypt(e, "XSRF-TOKEN", v.Cookie.Value); ok {
		t.Fatal("a value sealed for one cookie must not open as another")
	}
}

func TestRoundTrips(t *testing.T) {
	e, _ := crypt.New("base64:a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s=")

	sealed, err := Encrypt(e, "remember_web_x", "5|token|hash")
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := Decrypt(e, "remember_web_x", sealed); !ok || got != "5|token|hash" {
		t.Fatalf("got %q, %v", got, ok)
	}
}
