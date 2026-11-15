package support

import (
	"regexp"
	"strings"
)

// ASCII transliterates to ASCII with voku/portable-ascii's "en" map ("Brașov"
// folds to "Brasov"), dropping anything unmapped. Stored slugs and ASCII
// names were built with this map, so it must not drift. Multi-character keys
// apply first, longest match winning.
func ASCII(value string) string {
	value = asciiMulti(value)

	var out strings.Builder
	out.Grow(len(value))

	for _, r := range value {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			// voku turns the whitespace controls into plain spaces...
			out.WriteByte(' ')
		case r == 0x10 || r == 0x13:
			// ...keeps these two controls verbatim (its control table skips them)...
			out.WriteRune(r)
		case r < 0x20:
			// ...and drops every other C0 control.
		case r < 0x80:
			out.WriteRune(r)
		default:
			out.WriteString(asciiTable[r])
		}
	}

	return out.String()
}

// latinWithCombiningMark is voku's trigger for using the full map on a short
// string: an ASCII letter followed by a combining diacritic ("A̧").
var latinWithCombiningMark = regexp.MustCompile(`[A-Za-z][\x{0300}-\x{036F}]`)

// asciiMulti replaces voku's multi-character keys, longest first. As in
// voku, a string up to 64 bytes only sees keys without printable ASCII,
// unless an ASCII letter carries a combining mark.
func asciiMulti(value string) string {
	if !strings.ContainsFunc(value, func(r rune) bool { return r >= 0x80 }) {
		return value
	}

	everyKey := len(value) > 64 || latinWithCombiningMark.MatchString(value)

	var out strings.Builder
	runes := []rune(value)
	for i := 0; i < len(runes); {
		matched := false
		for span := min(5, len(runes)-i); span >= 2; span-- {
			key := string(runes[i : i+span])
			replacement, ok := asciiMultiTable[key]
			if !ok || (!everyKey && strings.ContainsFunc(key, func(r rune) bool { return r >= 0x20 && r <= 0x7E })) {
				continue
			}

			out.WriteString(replacement)
			i += span
			matched = true

			break
		}

		if !matched {
			out.WriteRune(runes[i])
			i++
		}
	}

	return out.String()
}

// EscapeLike escapes LIKE's wildcards and backslash so a value matches
// literally: a typed "%" must not turn a keystroke into a match-everything
// scan.
func EscapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// FoldLower is a case- and accent-insensitive match key: the ASCII fold,
// lowercased.
func FoldLower(value string) string {
	return strings.ToLower(ASCII(value))
}
