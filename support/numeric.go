package support

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Round rounds half away from zero to places decimals (negative places round
// to tens, hundreds…), judged on the decimal value the float spells rather
// than its binary approximation: Round(1.005, 2) is 1.01, where a naive
// math.Round(x*100)/100 gives 1.0.
func Round(value float64, places int) float64 {
	if math.IsInf(value, 0) || math.IsNaN(value) || value == 0 {
		return value
	}

	exponent := math.Pow10(abs(places))
	scaled := value / exponent
	if places > 0 {
		scaled = value * exponent
	}

	var integral, next float64
	if value >= 0 {
		integral = math.Floor(scaled)
		next = integral + 1
	} else {
		integral = math.Ceil(scaled)
		next = integral - 1
	}

	// The floor of a scaled value can land one below the decimal the value
	// spells (0.285 * 100 = 28.499999999999996); take the next integral when
	// it reproduces the value exactly.
	if unscale(next, exponent, places) == value {
		integral = next
	}

	// Beyond double precision, rounding is meaningless.
	if math.Abs(integral) >= 1e16 {
		return value
	}

	edge := math.Abs(unscale(integral+math.Copysign(0.5, integral), exponent, places))
	if math.Abs(value) >= edge {
		integral += math.Copysign(1, integral)
	}

	return unscale(integral, exponent, places)
}

func unscale(v float64, exponent float64, places int) float64 {
	if places > 0 {
		return v / exponent
	}

	return v * exponent
}

func abs(v int) int {
	if v < 0 {
		return -v
	}

	return v
}

var numericPattern = regexp.MustCompile(`^[ \t\n\r\v\f]*[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?[ \t\n\r\v\f]*$`)

// IsNumeric reports a decimal numeric string: optional surrounding ASCII
// whitespace, a sign, digits with an optional fraction and exponent. Unlike
// strconv.ParseFloat it rejects hex, underscores, "Inf" and "NaN".
func IsNumeric(s string) bool {
	return numericPattern.MatchString(s)
}

// NumericToFloat is a numeric string's (see IsNumeric) value; 0 otherwise.
func NumericToFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.Trim(s, " \t\n\r\v\f"), 64)

	return f
}

// NumericToInt is a numeric string's integer value: integer strings convert
// directly, anything with a fraction or exponent truncates toward zero.
func NumericToInt(s string) int {
	trimmed := strings.Trim(s, " \t\n\r\v\f")
	if i, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return int(i)
	}

	return FloatToInt(NumericToFloat(trimmed))
}

// FloatToInt truncates toward zero (0 for NaN/Inf).
func FloatToInt(f float64) int {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}

	return int(f)
}

// Thousands is n with comma-grouped thousands ("-74,123,456"), as counts
// read in command output and prompts.
func Thousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}

	var out strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}

	return sign + out.String()
}
