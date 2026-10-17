package htmlesc

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

var samples = []string{"", "plain", `<script>alert("x")</script>`, "Tom & Jerry's", "a > b < c",
	"&amp; already", "O'Brien \"Bas\" <b>", "£5 · ✓ — é", "&#x27;"}

func TestKnown(t *testing.T) {
	if got := E(`<a href="x">O'Neil & co</a>`); got != "&lt;a href=&quot;x&quot;&gt;O&#x27;Neil &amp; co&lt;/a&gt;" {
		t.Error(got)
	}
}

func TestDifferentialAgainstPython(t *testing.T) {
	var py string
	for _, c := range []string{"python3", "python", "py"} {
		if p, err := exec.LookPath(c); err == nil && exec.Command(p, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no Python 3")
	}
	in, _ := json.Marshal(samples)
	cmd := exec.Command(py, "-I", "-c", `
import html, json, sys
xs = json.loads(sys.stdin.buffer.read().decode("utf-8"))
sys.stdout.buffer.write(json.dumps([html.escape(x) for x in xs]).encode())
`)
	cmd.Stdin = strings.NewReader(string(in))
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	json.Unmarshal(b, &want)
	for i, s := range samples {
		if E(s) != want[i] {
			t.Errorf("%q: Go %q, Python %q", s, E(s), want[i])
		}
	}
}
