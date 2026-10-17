package pyfmt

import (
	"encoding/json"
	"math"
	"os/exec"
	"strings"
	"testing"
)

var tenth = 0.1 // a variable, so the compiler can't fold 0.1+0.2 exactly

func TestKnownValues(t *testing.T) {
	for _, c := range []struct {
		x    float64
		want string
	}{{100, "100.0"}, {2.5, "2.5"}, {tenth + 2*tenth, "0.30000000000000004"}, {math.Copysign(0, -1), "-0.0"}, {1e16, "1e+16"},
		{0.00001, "1e-05"}, {0.0001, "0.0001"}, {123456789012345.6, "123456789012345.6"}} {
		if got := Float(c.x); got != c.want {
			t.Errorf("Float(%v) = %q, want %q", c.x, got, c.want)
		}
	}
	for _, c := range []struct {
		x    float64
		n    int
		want float64
	}{{2.675, 2, 2.67}, {0.125, 2, 0.12}, {0.375, 2, 0.38}, {1.5, 0, 2}, {2.5, 0, 2}, {-2.5, 0, -2}} {
		if got := Round(c.x, c.n); got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v", c.x, c.n, got, c.want)
		}
	}
	if got := Fixed(2.675, 2); got != "2.67" {
		t.Errorf("Fixed = %q", got)
	}
}

func python(t *testing.T) string {
	t.Helper()
	for _, c := range []string{"python3", "python", "py"} {
		if p, err := exec.LookPath(c); err == nil && exec.Command(p, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			return p
		}
	}
	t.Skip("no Python 3")
	return ""
}

// Python's own answers for a spread of awkward values: halves, binary near-misses,
// large, tiny, negative.
func TestDifferentialAgainstPython(t *testing.T) {
	py := python(t)
	var xs []float64
	for i := -2000; i <= 2000; i += 7 {
		xs = append(xs, float64(i)/8, float64(i)/1000, float64(i)*1.005, float64(i)/3)
	}
	xs = append(xs, 0, 1e15, 1e16, 1.23456e17, 5e-5, 1e-4, 9.99e-5, 0.1, 0.7, 2.675, 1e-7, 123456.789, math.MaxInt32)
	in, _ := json.Marshal(xs)
	script := `
import json, sys
xs = json.loads(sys.stdin.read())
out = []
for x in xs:
    x = float(x)
    out.append([repr(float(x)), [repr(round(x, n)) for n in (0, 1, 2, 3, 4)], [f"{x:.{n}f}" for n in (0, 1, 2, 3)]])
print(json.dumps(out))
`
	cmd := exec.Command(py, "-I", "-c", script)
	cmd.Stdin = strings.NewReader(string(in))
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want [][]any
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	for i, x := range xs {
		w := want[i]
		if got := Float(x); got != w[0] {
			t.Errorf("str(%v): Go %q, Python %q", x, got, w[0])
		}
		for j, n := range []int{0, 1, 2, 3, 4} {
			if got := Float(Round(x, n)); got != w[1].([]any)[j] {
				t.Errorf("round(%v, %d): Go %q, Python %q", x, n, got, w[1].([]any)[j])
			}
		}
		for j, n := range []int{0, 1, 2, 3} {
			if got := Fixed(x, n); got != w[2].([]any)[j] {
				t.Errorf("f'{%v:.%df}': Go %q, Python %q", x, n, got, w[2].([]any)[j])
			}
		}
	}
}
