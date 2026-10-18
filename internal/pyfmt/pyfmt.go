// Package pyfmt reproduces the handful of Python number-formatting behaviours the
// original implementation's output depends on. Frames and JSON produced by the Go port
// must be byte-identical to the Python ones, because the overlay, the ball DB and the
// tests all compare these strings.
package pyfmt

import (
	"math"
	"strconv"
	"strings"
)

// Round is Python's round(x, ndigits): correctly rounded on the exact binary value,
// ties to even. strconv does the same rounding, so round-trip through it.
func Round(x float64, ndigits int) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', ndigits, 64), 64)
	return v
}

// Float is Python's str(float): the shortest repr that round-trips, always with a
// decimal point ("100.0", not "100"), in exponent form outside 1e-4 <= |x| < 1e16
// ("1e-05", "1e+16") exactly as Python switches.
func Float(x float64) string {
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		return "nan"
	}
	e := strconv.FormatFloat(x, 'e', -1, 64) // shortest digits, e.g. "1.2345e+16"
	exp, _ := strconv.Atoi(e[strings.LastIndexByte(e, 'e')+1:])
	if x != 0 && (exp < -4 || exp >= 16) {
		return e // Go and Python agree here: at least two exponent digits
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// Fixed is Python's f"{x:.{n}f}".
func Fixed(x float64, n int) string {
	return strconv.FormatFloat(x, 'f', n, 64)
}
