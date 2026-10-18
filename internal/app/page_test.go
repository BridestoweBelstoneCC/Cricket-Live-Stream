package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func node(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	return p
}

var scriptRx = regexp.MustCompile(`(?s)<script>(.*)</script>`)

// The window's page: its script parses, and the activity log's lines are sorted the way
// the console showed them (✓ ok, ⚠ warning, ✗ problem), with the consoles' boxes and
// Ctrl+C advice left out of the getting-ready list.
func TestPageScript(t *testing.T) {
	bin := node(t)
	m := scriptRx.FindStringSubmatch(pageHTML)
	if m == nil {
		t.Fatal("no script in app.html")
	}
	script := m[1]
	grab := func(name string) string {
		i := strings.Index(script, "function "+name+"(")
		if i < 0 {
			t.Fatalf("no function %s", name)
		}
		depth, j := 0, strings.Index(script[i:], "{")+i
		for k := j; k < len(script); k++ {
			switch script[k] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return script[i : k+1]
				}
			}
		}
		t.Fatalf("unterminated %s", name)
		return ""
	}
	test := `new Function(` + jsString(script) + `);
` + grab("kind") + `
var logLines = [];
` + grab("stepLines") + `
var cases = {"  ✓ Club: X": "ok", "  ✔  Seeded": "ok", "  ⚠ API: no key": "warn", "  [!!] OBS is open": "warn",
  "  ✗ Error": "bad", "   PROBLEM: Port": "bad", "    Loading config.ini...": ""};
for (var k in cases) if (kind(k) !== cases[k]) throw new Error("kind(" + k + ") = " + kind(k));
logLines = [{n: 1, t: "before"}, {n: 2, t: "  ✓ Local mode"}, {n: 3, t: "  ╔════╗"}, {n: 4, t: "  ║   Control panel → x ║"},
  {n: 5, t: "  ╠══════╣"}, {n: 6, t: "  Keep this window open while streaming. Ctrl+C to stop."}, {n: 7, t: "   "},
  {n: 8, t: "  Press Ctrl+C to stop the server."}, {n: 9, t: "  ✓ Telemetry logging"}];
var got = JSON.stringify(stepLines(1));
if (got !== JSON.stringify(["  ✓ Local mode", "  ✓ Telemetry logging"])) throw new Error("steps " + got);
console.log("ok");
`
	f := filepath.Join(t.TempDir(), "page.js")
	os.WriteFile(f, []byte(test), 0o644)
	out, err := exec.Command(bin, f).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v\n%s", err, out)
	}
}

func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "`", "\\`", "${", "\\${")
	return "`" + r.Replace(s) + "`"
}
