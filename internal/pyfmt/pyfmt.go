// Package pyfmt reproduces the handful of Python number-formatting behaviours the
// original implementation's output depends on. Frames and JSON produced by the Go port
// must be byte-identical to the Python ones, because the overlay, the ball DB and the
// tests all compare these strings.
package pyfmt

import (
	"strconv"
	"strings"
)

// Round is Python's round(x, ndigits): correctly rounded on the exact binary value,
// ties to even. strconv does the same rounding, so round-trip through it.
func Round(x float64, ndigits int) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', ndigits, 64), 64)
	return v
}

// Float is Python's str(float): shortest repr, always with a decimal point
// ("100.0", not "100").
func Float(x float64) string {
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0"
	}
	return s
}

// Fixed is Python's f"{x:.{n}f}".
func Fixed(x float64, n int) string {
	return strconv.FormatFloat(x, 'f', n, 64)
}
