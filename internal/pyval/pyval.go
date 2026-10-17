// Package pyval gives decoded JSON values (any) Python's semantics: truthiness, str()
// and int(). The original code leans on these everywhere it reads the scorer's feed,
// PlayCricket responses and the saved state, all of which are loosely typed (numbers
// arrive as strings, blanks as "" or null).
//
// Decode with json.Decoder.UseNumber() where the int/float distinction matters:
// json.Number keeps the literal ("40" vs "40.0"), exactly like Python's json module.
package pyval

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Truthy is Python's bool(v).
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// Str is Python's str(v).
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case float64:
		// A plain float64 has lost whether it was written as an int; treat integral
		// values as ints, which is what every JSON source here sends.
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprint(v)
}

// Get is Python's d.get(key) on a JSON object (nil for a missing key or non-object).
func Get(v any, key string) any {
	if m, ok := v.(map[string]any); ok {
		return m[key]
	}
	return nil
}

// StrOr is Python's str(v or default).
func StrOr(v any, def string) string {
	if !Truthy(v) {
		return def
	}
	return Str(v)
}

// Int is Python's int(v): ints, integral floats (truncated, like int(3.7)), and strings
// of digits (surrounding whitespace allowed). Anything else is an error.
func Int(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("cannot convert float %v to integer", x)
		}
		return int(math.Trunc(x)), nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), nil
		}
		f, err := x.Float64()
		if err != nil {
			return 0, err
		}
		return int(math.Trunc(f)), nil
	case string:
		s := strings.TrimSpace(x)
		s = strings.ReplaceAll(s, "_", "") // Python allows 1_000
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("invalid literal for int() with base 10: %q", x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("int() argument must be a string or a number, not %T", v)
}

// IntOr is Python's int(v or 0) with a fallback for bad input — the
// `try: int(x) except (TypeError, ValueError): default` idiom.
func IntOr(v any, def int) int {
	if !Truthy(v) {
		return 0
	}
	n, err := Int(v)
	if err != nil {
		return def
	}
	return n
}

// Float is Python's float(v).
func Float(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		return x.Float64()
	case string:
		return strconv.ParseFloat(strings.TrimSpace(x), 64)
	}
	return 0, fmt.Errorf("float() argument must be a string or a number, not %T", v)
}
