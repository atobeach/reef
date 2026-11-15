package phpjson

import (
	"strings"
	"unicode/utf16"
)

// Quote is s as a JSON string in the dialect: "/" escaped as "\/", every
// non-ASCII character \u-escaped (astral ones as a surrogate pair), and
// "<", ">", "&" left as they are.
func Quote(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '/':
			out.WriteString(`\/`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			switch {
			case r < 0x20:
				writeUnicodeEscape(&out, r)
			case r < 0x80:
				out.WriteRune(r)
			case r > 0xffff:
				high, low := utf16.EncodeRune(r)
				writeUnicodeEscape(&out, high)
				writeUnicodeEscape(&out, low)
			default:
				writeUnicodeEscape(&out, r)
			}
		}
	}
	out.WriteByte('"')

	return out.String()
}

// writeUnicodeEscape writes r (at most 0xFFFF) as \uXXXX, lowercase hex.
func writeUnicodeEscape(out *strings.Builder, r rune) {
	const hexDigits = "0123456789abcdef"

	out.WriteString(`\u`)
	out.WriteByte(hexDigits[(r>>12)&0xF])
	out.WriteByte(hexDigits[(r>>8)&0xF])
	out.WriteByte(hexDigits[(r>>4)&0xF])
	out.WriteByte(hexDigits[r&0xF])
}
