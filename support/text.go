package support

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Deref is p's value, or the zero value for nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T

		return zero
	}

	return *p
}

// NonEmpty is a pointer to s, or nil when s is empty: Deref's inverse for an
// optional string.
func NonEmpty(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// trimCharacters are what MbTrim strips: ASCII whitespace, NUL and the
// Unicode space characters.
var trimCharacters = " \f\n\r\t\v\x00" + string([]rune{
	0x00a0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200a,
	0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0x0085, 0x180e,
})

// MbTrim trims whitespace from both ends of upstream text: everything
// strings.TrimSpace trims plus NUL and U+180E (Mongolian vowel separator).
func MbTrim(s string) string {
	return strings.Trim(s, trimCharacters)
}

// Title title-cases s the Unicode way: a cased letter after a cased letter
// (case-ignorable characters such as apostrophes in between) is lowercased,
// every other letter starts a word and is title-cased.
func Title(s string) string {
	var out strings.Builder
	inWord := false
	for _, r := range s {
		switch {
		case isCased(r):
			if inWord {
				out.WriteRune(unicode.ToLower(r))
			} else {
				out.WriteRune(unicode.ToTitle(r))
			}
			inWord = true
		case isCaseIgnorable(r):
			out.WriteRune(r)
		default:
			out.WriteRune(r)
			inWord = false
		}
	}

	return out.String()
}

func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r)
}

func isCaseIgnorable(r rune) bool {
	if slices.Contains(caseIgnorable, r) {
		return true
	}

	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// caseIgnorable are the Word_Break MidLetter/MidNumLet/Single_Quote
// characters (apostrophes, periods, colons, middle dots…) Unicode counts as
// case-ignorable beyond the general categories.
var caseIgnorable = []rune{
	'\'', '.', ':', '^', '`', 0x00a8, 0x00ad, 0x00af, 0x00b4, 0x00b7, 0x00b8, 0x2018, 0x2019, 0x2024,
	0x2027, 0xfe13, 0xfe52, 0xfe55, 0xff07, 0xff0e, 0xff1a,
}

// Squish collapses runs of whitespace to one space and trims the ends.
func Squish(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		// The Hangul fillers render blank but aren't Unicode spaces.
		return unicode.IsSpace(r) || r == 0x3164 || r == 0x1160
	})

	// The trim also strips the zero-width space, LTR mark and BOM.
	return strings.Trim(strings.Join(fields, " "), " "+string([]rune{0x200b, 0x200e, 0xfeff}))
}

// UcFirst uppercases the first character.
func UcFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}

	return strings.ToUpper(string(r)) + s[size:]
}

// BeforeLast is everything before the last occurrence of search, or the
// whole subject when it doesn't occur.
func BeforeLast(subject string, search string) string {
	if search == "" {
		return subject
	}

	index := strings.LastIndex(subject, search)
	if index < 0 {
		return subject
	}

	return subject[:index]
}

// Falsy is a string's loose falsiness: "" and "0", which filtering a list
// of values drops.
func Falsy(s string) bool {
	return s == "" || s == "0"
}
