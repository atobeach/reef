// Package support holds the generic helpers: random strings and UUIDs,
// ASCII folding, slugs, plurals, truncation, URL encoding and the number
// semantics stored data depends on.
package support

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
)

// RandomString is n characters of [A-Za-z0-9] from a CSPRNG: base64 of
// random bytes with the non-alphanumerics dropped.
func RandomString(n int) string {
	var out strings.Builder
	for out.Len() < n {
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			panic("support: no randomness: " + err.Error())
		}

		for _, r := range base64.StdEncoding.EncodeToString(buf) {
			if r != '+' && r != '/' && r != '=' && out.Len() < n {
				out.WriteRune(r)
			}
		}
	}

	return out.String()
}
