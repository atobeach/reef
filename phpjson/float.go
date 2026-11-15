package phpjson

import (
	"math"
	"strconv"
	"strings"
)

// FloatString is a float as PHP's (string) cast spells it, as stored text
// holds it: 14 significant digits, whole numbers without a fraction, and
// "1.0E+15"/"1.0E-5" below 1e-4 or past 14 integer digits.
func FloatString(f float64) string {
	return formatFloat(f, 14, 'E')
}

// jsonFloat is a float in the dialect: the shortest digits that
// round-trip, no ".0" on whole numbers, and "1.0e-5" style below 1e-4 or
// past 17 integer digits.
func jsonFloat(f float64) string {
	return formatFloat(f, 0, 'e')
}

// formatFloat writes f with precision significant digits, or the shortest
// round-tripping digits for precision 0, switching to exponent form (with
// expChar) below 1e-4 or when the integer part needs more digits than the
// precision (17 for shortest).
func formatFloat(f float64, precision int, expChar byte) string {
	switch {
	case math.IsInf(f, 1):
		return "INF"
	case math.IsInf(f, -1):
		return "-INF"
	case math.IsNaN(f):
		return "NAN"
	}

	sign := ""
	if math.Signbit(f) {
		sign = "-"
		f = -f
	}
	if f == 0 {
		return sign + "0"
	}

	limit := precision
	var formatted string
	if precision == 0 {
		limit = 17
		formatted = strconv.FormatFloat(f, 'e', -1, 64)
	} else {
		formatted = strconv.FormatFloat(f, 'e', precision-1, 64)
	}

	mantissa, exp, _ := strings.Cut(formatted, "e")
	digits := strings.TrimRight(strings.Replace(mantissa, ".", "", 1), "0")
	if digits == "" {
		digits = "0"
	}
	exponent, _ := strconv.Atoi(exp)
	decpt := exponent + 1

	if decpt < -3 || decpt > limit {
		rest := digits[1:]
		if rest == "" {
			rest = "0"
		}
		expSign := "+"
		if decpt-1 < 0 {
			expSign = "-"
		}

		return sign + digits[:1] + "." + rest + string(expChar) + expSign + strconv.Itoa(absInt(decpt-1))
	}

	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits))
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}

	return v
}
