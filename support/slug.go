package support

import (
	"regexp"
	"strings"
)

var (
	slugUnderscores = regexp.MustCompile(`_+`)
	slugDisallowed  = regexp.MustCompile(`[^-\pL\pN\s]+`)
	slugSeparators  = regexp.MustCompile(`[-\s]+`)
)

// Slug is the URL slug of a title: ASCII-transliterated, underscores to dashes,
// "@" spelled "at", everything but letters, numbers, dashes and whitespace
// dropped, lowercased, runs of dashes/whitespace collapsed, dashes trimmed.
// A name with no transliteration ("東京") slugs to "".
func Slug(title string) string {
	title = ASCII(title)
	title = slugUnderscores.ReplaceAllString(title, "-")
	title = strings.ReplaceAll(title, "@", "-at-")
	title = slugDisallowed.ReplaceAllString(strings.ToLower(title), "")
	title = slugSeparators.ReplaceAllString(title, "-")

	return strings.Trim(title, "-")
}
