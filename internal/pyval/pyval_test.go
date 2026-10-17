package pyval

import (
	"encoding/json"
	"strings"
	"testing"
)

func num(s string) json.Number { return json.Number(s) }

func TestTruthy(t *testing.T) {
	for _, c := range []struct {
		v    any
		want bool
	}{{nil, false}, {"", false}, {"0", true}, {0.0, false}, {2.0, true}, {num("0"), false}, {num("0.0"), false},
		{num("3"), true}, {[]any{}, false}, {[]any{1}, true}, {map[string]any{}, false}, {false, false}, {true, true}} {
		if got := Truthy(c.v); got != c.want {
			t.Errorf("Truthy(%#v) = %v", c.v, got)
		}
	}
}

func TestStrIsPythonsStr(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{{nil, "None"}, {true, "True"}, {false, "False"}, {"x", "x"}, {40.0, "40"}, {2.5, "2.5"},
		{num("40"), "40"}, {num("12.0"), "12.0"}, {num("1.50"), "1.5"}, {num("0.00001"), "1e-05"},
		{num("1e3"), "1000.0"}, {0.00001, "1e-05"}, {3, "3"}, {int64(-7), "-7"}} {
		if got := Str(c.v); got != c.want {
			t.Errorf("Str(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestIntIsPythonsInt(t *testing.T) {
	for _, c := range []struct {
		v    any
		want int
		ok   bool
	}{{" 42 ", 42, true}, {"1_000", 1000, true}, {3.9, 3, true}, {-3.9, -3, true}, {num("7"), 7, true},
		{num("7.8"), 7, true}, {true, 1, true}, {"4.0", 0, false}, {"", 0, false}, {nil, 0, false}} {
		got, err := Int(c.v)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("Int(%#v) = %d, %v", c.v, got, err)
		}
	}
	if IntOr("junk", 9) != 9 || IntOr("", 9) != 0 || IntOr(nil, 9) != 0 || IntOr("5", 9) != 5 {
		t.Error("IntOr")
	}
}

func TestFloatAndGet(t *testing.T) {
	if f, err := Float(" 2.5 "); err != nil || f != 2.5 {
		t.Error(f, err)
	}
	if _, err := Float(map[string]any{}); err == nil {
		t.Error("a dict isn't a float")
	}
	m := map[string]any{"a": 1.0}
	if Get(m, "a") != 1.0 || Get(m, "b") != nil || Get("not a map", "a") != nil {
		t.Error("Get")
	}
	if StrOr("", "dflt") != "dflt" || StrOr(0.0, "d") != "d" || !strings.HasPrefix(StrOr(1.5, "d"), "1.5") {
		t.Error("StrOr")
	}
}
