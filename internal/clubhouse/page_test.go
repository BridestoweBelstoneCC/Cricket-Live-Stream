package clubhouse

// The Pi's page itself: its script parses, every element it looks up is on the page, and
// the club colour it paints with stays readable on the board green.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var scriptRx = regexp.MustCompile(`(?s)<script>(.*)</script>`)

func pageScript(t *testing.T) string {
	t.Helper()
	m := scriptRx.FindStringSubmatch(screenHTML)
	if m == nil {
		t.Fatal("no script in clubhouse.html")
	}
	return m[1]
}

// A $('id') or txt('id', ...) for an id the page doesn't have fails only when that screen
// is reached — on a Pi in a clubhouse, mid-match. Caught here instead.
func TestScreenPageElementsExist(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(screenHTML, -1) {
		ids[m[1]] = true
	}
	used := regexp.MustCompile(`\b(?:\$|txt|show|say)\('([\w-]+)'`).FindAllStringSubmatch(pageScript(t), -1)
	if len(used) < 30 {
		t.Fatalf("only %d lookups found — has the page's helper naming changed?", len(used))
	}
	for _, m := range used {
		if !ids[m[1]] {
			t.Errorf("the script looks up #%s, which isn't on the page", m[1])
		}
	}
}

func TestScreenPageScript(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := pageScript(t)
	grab := func(name string) string {
		i := strings.Index(script, "function "+name+"(")
		if i < 0 {
			t.Fatalf("no function %s", name)
		}
		depth := 0
		for k := strings.Index(script[i:], "{") + i; k < len(script); k++ {
			switch script[k] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return script[i : k+1]
				}
			}
		}
		t.Fatalf("unterminated %s", name)
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, "`", "\\`", "${", "\\${")
	test := "new Function(`" + r.Replace(script) + "`);\n" + grab("mins") + "\n" + grab("readableAccent") + "\n" + grab("contrastOnNight") + `
function eq(a, b) { if (a !== b) throw new Error(JSON.stringify(a) + " !== " + JSON.stringify(b)); }
eq(mins(0), "0:00"); eq(mins(59), "0:59"); eq(mins(61), "1:01"); eq(mins(-5), "0:00"); eq(mins(undefined), "0:00");
// Dark kit colours are lifted until they read on the board (4.5:1, as text needs);
// bright ones are left be.
function rgbOf(hex) { return [1, 3, 5].map(function (i) { return parseInt(hex.substr(i, 2), 16) / 255; }); }
["#000080", "#0000ff", "#800000", "#0b3d1e", "#4b0082", "#000000", "#1a1a1a"].forEach(function (c) {
  var a = readableAccent(c);
  if (!/^#[0-9a-f]{6}$/.test(a.hex)) throw new Error(c + " gave " + a.hex);
  var k = contrastOnNight(rgbOf(a.hex));
  if (k < 4.4) throw new Error(c + " stays too dark: " + a.hex + " is " + k.toFixed(2) + ":1");
});
eq(readableAccent("#f5d76e").hex, "#f5d76e");
// Navy keeps its hue: blue stays the strongest channel.
var n = readableAccent("#000080").hex, rgb = rgbOf(n);
if (!(rgb[2] > rgb[0] && rgb[2] > rgb[1])) throw new Error("navy lost its blue: " + n);
console.log("ok");
`
	f := filepath.Join(t.TempDir(), "page.js")
	os.WriteFile(f, []byte(test), 0o644)
	out, err := exec.Command(bin, f).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v\n%s", err, out)
	}
}
