package server

// The overlay's end-of-over sequence across BOTH innings of a simulated match, run through
// the real processPCSData and showOverSummary in node with the rest of the page stubbed.
// Found on a practice match (2026-11-16): the reply had no over summaries (and so no AI
// commentary, partnership, run rate or win predictor) because the "already shown" over
// number carried over from the first innings.

import (
	"regexp"
	"strings"
	"testing"

	"cricketstream/internal/simulate"
)

// overlayGlobals: the page's top-level single-line `let`/`var` state, so the extracted
// functions start from the same values they do in OBS.
func overlayGlobals(html string) string {
	script := html[strings.Index(html, "<script>"):]
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^(?:let|var) (_[A-Za-z0-9_]+)\s*=\s*([^;\n]*);`).FindAllStringSubmatch(script, -1) {
		out = append(out, "try { var "+m[1]+" = "+m[2]+"; } catch (e) { var "+m[1]+"; }")
	}
	return strings.Join(out, "\n")
}

func TestOverSummaryInBothInnings(t *testing.T) {
	m, err := simulate.NewMatch("full", 5, 7, true)
	if err != nil {
		t.Fatal(err)
	}
	var frames []M
	for _, fr := range m.Frames() {
		d := M{}
		for k, v := range fr.Data {
			d[k] = v
		}
		frames = append(frames, ParsePCSJSON(d))
	}
	html := page(t, "overlay.html")
	// Run as a classic script (vm.runInThisContext), as in the browser: in a node module
	// top-level vars aren't globals, and the stand-in for the page's missing names below
	// would answer for them too.
	body := `
var stub = new Proxy(function () {}, {
  get: function (t, k) { return k === Symbol.toPrimitive ? function () { return ''; } : stub; },
  set: function () { return true; },
  apply: function () { return stub; }
});
var out = process.stdout; console = {log: function () {}, warn: function () {}, error: function () {}};
var cfg = {graphics_over_summary: true, graphics_commentary_over: true, anthropic_key_set: true};
var asked = [];
function fetch(url, opts) {
  if (url === '/commentary/over/generate') asked.push({innings: _lastPCSinnings, over: JSON.parse(opts.body).over_num});
  return Promise.resolve({ok: true, json: function () { return Promise.resolve({}); }});
}
` + overlayGlobals(html) + `
var unknown = new Proxy({}, {
  has: function (t, k) { return typeof k === 'string' && !(k in globalThis); },
  get: function (t, k) { return k === Symbol.unscopables ? undefined : stub; }
});
with (unknown) {
` + topLevelFunction(t, "processPCSData") + topLevelFunction(t, "showOverSummary") + topLevelFunction(t, "parseOverString") +
		overlayFunctions(t, "noteLiveFrame", "classifyBall", "tickerOverOf", "newTickerBalls") + `
  var frames = ` + jsonOf(frames) + `;
  frames.forEach(function (f) { processPCSData(f); });
}
out.write(JSON.stringify(asked));
`
	var asked []struct{ Innings, Over int }
	runJS(t, "require('vm').runInThisContext("+jsonOf(body)+");", &asked)
	per := map[int][]int{}
	for _, a := range asked {
		per[a.Innings] = append(per[a.Innings], a.Over)
	}
	// Over-ending wickets skip their summary (the wicket's own graphics cover it), so not
	// every over is here — but each innings must have its own run of them from over 1 up.
	for _, inn := range []int{1, 2} {
		if len(per[inn]) < 2 {
			t.Errorf("innings %d: over summaries for overs %v (all asked: %v)", inn, per[inn], asked)
		}
	}
}
