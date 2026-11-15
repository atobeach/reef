package phpjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Marshal writes a typed Go value in the dialect: encoding/json picks the
// structure (tags, `{}` for an empty object), then strings and floats are
// respelled. Encode is for decoded values, keeping PHP's array rules.
func Marshal(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("phpjson: %w", err)
	}

	return respell(encoded)
}

// respell rewrites every string and non-integer number of encoding/json's
// output in the dialect.
func respell(encoded []byte) ([]byte, error) {
	var out bytes.Buffer
	for i := 0; i < len(encoded); {
		c := encoded[i]
		switch {
		case c == '"':
			end := i + 1
			for end < len(encoded) && encoded[end] != '"' {
				if encoded[end] == '\\' {
					end++
				}
				end++
			}
			var s string
			if err := json.Unmarshal(encoded[i:end+1], &s); err != nil {
				return nil, fmt.Errorf("phpjson: %w", err)
			}
			out.WriteString(Quote(s))
			i = end + 1
		case c == '-' || (c >= '0' && c <= '9'):
			end := i
			for end < len(encoded) && strings.IndexByte("+-0123456789.eE", encoded[end]) >= 0 {
				end++
			}
			// A fraction or exponent makes a float (0.00001 is 1.0e-5).
			number := string(encoded[i:end])
			if strings.ContainsAny(number, ".eE") {
				if f, err := strconv.ParseFloat(number, 64); err == nil {
					number = jsonFloat(f)
				}
			}
			out.WriteString(number)
			i = end
		default:
			out.WriteByte(c)
			i++
		}
	}

	return out.Bytes(), nil
}
