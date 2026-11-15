package support

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Transliterate folds value to ASCII for the login limiter's key: ASCII
// passes through, accented Latin letters lose their marks (é → e), and
// anything without an ASCII form becomes "?".
func Transliterate(value string) string {
	var out strings.Builder
	for _, r := range norm.NFKD.String(value) {
		switch {
		case r <= unicode.MaxASCII:
			out.WriteRune(r)
		case unicode.Is(unicode.Mn, r):
			// Combining marks split off by NFKD: drop them.
		default:
			out.WriteByte('?')
		}
	}

	return out.String()
}
