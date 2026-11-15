package phpjson

import (
	"strings"
	"unicode/utf8"

	"github.com/atobeach/reef/ordered"
)

// EncodeSubstitute is Encode with each invalid UTF-8 byte replaced by
// U+FFFD rather than failing, for outside text cut mid-rune.
func EncodeSubstitute(value any) ([]byte, error) {
	return Encode(substituteInvalid(value))
}

// substituteInvalidUTF8 replaces each invalid byte with U+FFFD.
func substituteInvalidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}

	var out strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			out.WriteRune(utf8.RuneError)
			i++

			continue
		}
		out.WriteString(s[i : i+size])
		i += size
	}

	return out.String()
}

func substituteInvalid(value any) any {
	switch v := value.(type) {
	case string:
		return substituteInvalidUTF8(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = substituteInvalid(item)
		}

		return out
	case *ordered.Object:
		out := ordered.NewObject()
		for _, key := range v.Keys() {
			item, _ := v.Get(key)
			out.Set(substituteInvalidUTF8(key), substituteInvalid(item))
		}

		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[substituteInvalidUTF8(key)] = substituteInvalid(item)
		}

		return out
	}

	return value
}
