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
	// A whole literal id ('sv-' + k is built at run time, and not one).
	used := regexp.MustCompile(`\b(?:\$|txt|show|say)\('([\w-]+)'\s*[,)]`).FindAllStringSubmatch(pageScript(t), -1)
	if len(used) < 30 {
		t.Fatalf("only %d lookups found — has the page's helper naming changed?", len(used))
	}
	for _, m := range used {
		if !ids[m[1]] {
			t.Errorf("the script looks up #%s, which isn't on the page", m[1])
		}
	}
}

// The scorecard, overs and league table views, rendered against a small fake DOM: what a
// person in the bar would read.
func TestScreenPageMatchViews(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := pageScript(t)
	var funcs []string
	for _, name := range []string{"make", "sheet", "renderCard", "renderOvers", "renderTable"} {
		funcs = append(funcs, grabFunc(t, script, name))
	}
	test := `
function El(tag) { this.tag = tag; this.children = []; this.className = ''; this._t = ''; this.parent = null; }
El.prototype.appendChild = function (c) {
  if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1);
  c.parent = this; this.children.push(c); return c;
};
Object.defineProperty(El.prototype, 'firstChild', {get: function () { return this.children[0] || null; }});
Object.defineProperty(El.prototype, 'textContent', {
  get: function () { return this._t + this.children.map(function (c) { return c.textContent; }).join('|'); },
  set: function (v) { this._t = String(v); this.children = []; }});
var document = {createElement: function (t) { return new El(t); }};
var ids = {}, shown = {};
function $(id) { return ids[id] || (ids[id] = new El('div')); }
function show(id, on) { shown[id] = on; }
function txt(id, t) { $(id).textContent = t; }
function find(el, pred, out) { out = out || []; if (pred(el)) out.push(el); el.children.forEach(function (c) { find(c, pred, out); }); return out; }
function has(s, want) { if (s.indexOf(want) < 0) throw new Error(JSON.stringify(want) + " not in " + JSON.stringify(s)); }
` + strings.Join(funcs, "\n") + `
var cardData = {innings: [
  {innings: 1, batting_team: "Home CC", score: 187, wickets: 9, overs: "40", batters: [], bowlers: [], fall_of_wickets: []},
  {innings: 2, batting_team: "Away CC", score: 142, wickets: 4, overs: "28.3",
   batters: [{name: "J Cole", runs: 61, balls: 74, out: false}, {name: "P Hart", runs: 12, balls: 20, out: true}],
   bowlers: [{name: "M Fry", o: "6.3", r: 31, w: 2}], fall_of_wickets: [{batter: "P Hart", score: "23-1"}]}],
  recent_overs: [{over: 29, balls: ["1", "4", "W", "2wd", "•"], runs: 8, wickets: 1}, {over: 28, balls: ["1"], runs: 1, wickets: 0}]};
renderCard();
var card = $('card').textContent;
has(card, "Away CC142/4  (28.3 ov)"); // the heading's own text, then its figures (spaced by CSS)
if (card.indexOf("Away CC") > card.indexOf("Home CC")) throw new Error("the innings under way should come first");
has(card, "J Cole *|61|74"); has(card, "P Hart|12|20"); has(card, "Fall of wickets: 23-1 (P Hart)");
if (find($('card'), function (e) { return e.className === 'out'; }).map(function (e) { return e.textContent; }).join() !== "P Hart")
  throw new Error("only the out batter is dimmed");
if (shown.card_wait !== false) throw new Error("waiting message still shown");
renderOvers();
var ov = $('overs').textContent;
has(ov, "Over 29|1|4|W|2wd|•|8 runs · 1 wkt"); has(ov, "Over 28|1|1 run");
var classes = find($('overs'), function (e) { return /^b/.test(e.className); }).map(function (e) { return e.className; });
if (classes.indexOf("b w") < 0 || classes.indexOf("b four") < 0) throw new Error("ball colours: " + classes);
var tableData = {usable: true, name: "Division 1", home_index: 1, rows: [
  {position: 4, team: "Hatherleigh CC", played: 10, points: 40}, {position: 5, team: "Home CC", played: 10, points: 36}]};
renderTable();
has($('table_name').textContent, "Division 1");
var home = find($('table'), function (e) { return e.className === 'home'; });
if (home.length !== 1 || home[0].textContent !== "5|Home CC|10|36") throw new Error("home row: " + (home[0] || {}).textContent);
cardData = {innings: [], recent_overs: []};
renderCard(); renderOvers();
if (shown.card_wait !== true || shown.overs_wait !== true) throw new Error("no cricket yet should say so");
console.log("ok");
`
	f := filepath.Join(t.TempDir(), "views.js")
	os.WriteFile(f, []byte(test), 0o644)
	out, err := exec.Command(bin, f).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v\n%s", err, out)
	}
}

// grabFunc is one top-level function's source from the page's script.
func grabFunc(t *testing.T, script, name string) string {
	t.Helper()
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

// A score-only screen never shows a screen with buttons: the score while match day runs,
// otherwise "no match on" — never "Start the stream", the start choice or the switch-off.
func TestScreenPageScoreOnlyViews(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	test := `var S, pinStep = false, tab = 'get';
function laptop() { return (S && S.laptop) || {}; }
` + grabFunc(t, pageScript(t), "viewName") + `
function want(s, v) { S = s; var got = viewName(); if (got !== v) throw new Error(JSON.stringify(s) + ": " + got + ", want " + v); }
want({paired: false, view_only: true}, 'pair');
want({paired: true, view_only: true, reachable: false, laptop: null}, 'idle');
want({paired: true, view_only: true, reachable: false, waking_for: 5, laptop: null}, 'idle');
want({paired: true, view_only: true, reachable: true, laptop: {phase: 'home'}}, 'idle');
want({paired: true, view_only: true, reachable: true, laptop: {phase: 'stopped', power_off_in: 40}}, 'idle');
want({paired: true, view_only: true, reachable: true, laptop: {phase: 'starting'}}, 'idle');
want({paired: true, view_only: true, reachable: true, laptop: {phase: 'running', server_ready: false}}, 'idle');
want({paired: true, view_only: true, reachable: true, laptop: {phase: 'running', server_ready: true}}, 'score');
// The match-day screen is unchanged.
want({paired: true, reachable: false, laptop: null}, 'off');
want({paired: true, reachable: true, laptop: {phase: 'home'}}, 'ready');
want({paired: true, reachable: true, laptop: {phase: 'running', server_ready: true}}, 'get');
console.log("ok");
`
	f := filepath.Join(t.TempDir(), "scoreonly.js")
	os.WriteFile(f, []byte(test), 0o644)
	out, err := exec.Command(bin, f).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v\n%s", err, out)
	}
}
