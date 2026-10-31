package server

// The overlay's and panel's own JavaScript, run in a real engine (node): the classifyBall
// parity with the server, the bowler-milestone chain, the ticker diff, the camera
// auto-cut, the panel's club accent — plus the text checks that keep the three copies
// of the scorebar styles in step and every panel field on the page (tests/test_js_parity,
// test_bowler_milestones, test_camera_autocut, test_scorebar_styles, test_panel_layout,
// scripts/check_panel_js.py). The pages are the embedded copies the binary serves.
// Without node the engine tests skip, as upstream's did.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"cricketstream/internal/assets"
)

func page(t *testing.T, name string) string {
	t.Helper()
	b, err := assets.Embedded(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func nodePath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no JS engine available (node not installed)")
	}
	return p
}

// runJS runs a program that prints one line of JSON and decodes it into out.
func runJS(t *testing.T, src string, out any) {
	t.Helper()
	node := nodePath(t)
	f := filepath.Join(t.TempDir(), "t.js")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, f)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, stderr.String())
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), out); err != nil {
		t.Fatalf("node printed %q: %v", b, err)
	}
}

// extractFunction pulls a whole function out of a page by brace counting, keeping an
// "async" in front of it (cutCameraScene's await is a syntax error without it).
func extractFunction(t *testing.T, html, name string) string {
	t.Helper()
	start := strings.Index(html, "function "+name)
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	if start >= 6 && html[start-6:start] == "async " {
		start -= 6
	}
	depth := 0
	for i := start; i < len(html); i++ {
		switch html[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : i+1]
			}
		}
	}
	t.Fatalf("could not extract %s", name)
	return ""
}

func overlayFunctions(t *testing.T, names ...string) string {
	html := page(t, "overlay.html")
	var parts []string
	for _, n := range names {
		parts = append(parts, extractFunction(t, html, n))
	}
	return strings.Join(parts, "\n")
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

// ── Every <script> block parses (scripts/check_panel_js.py) ──

func TestPagesJavaScriptParses(t *testing.T) {
	node := nodePath(t)
	re := regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)
	for _, name := range []string{"control.html", "overlay.html", "scoring.html"} {
		scripts := re.FindAllStringSubmatch(page(t, name), -1)
		if len(scripts) == 0 {
			t.Fatalf("%s: no <script> blocks — extraction is broken", name)
		}
		for i, s := range scripts {
			f := filepath.Join(t.TempDir(), "s.js")
			os.WriteFile(f, []byte(s[1]), 0o644)
			if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
				t.Errorf("%s script #%d: %s", name, i+1, out)
			}
		}
	}
}

// ── classifyBall: overlay (JS) vs the ball logger (Go) ──

var parityTokens = []string{"", ".", "0", "1", "2", "3", "4", "5", "6",
	"W", "w", "w+1", "w+4", "nb", "1nb", "2nb+4", "1b", "2b", "4b", "1lb", "4lb", "xyz", "14"}

// goClass maps ClassifyBall onto the overlay's display class.
func goClass(tok string) string {
	b := ClassifyBall(tok)
	switch {
	case b.Wicket:
		return "wicket"
	case b.Extra != "":
		return b.Extra
	case b.Outcome == "dot":
		return "dot"
	}
	n, _ := strconv.Atoi(b.Outcome)
	if n == 4 {
		return "four"
	}
	if n == 6 {
		return "six"
	}
	return ""
}

func TestClassifyBallJSAndGoAgreeOnEveryToken(t *testing.T) {
	js := extractFunction(t, page(t, "overlay.html"), "classifyBall") +
		"\nconst out = " + jsonOf(parityTokens) + ".map(classifyBall);\nconsole.log(JSON.stringify(out));"
	var res []M
	runJS(t, js, &res)
	mustEq(t, len(res), len(parityTokens))
	for i, tok := range parityTokens {
		if gs(res[i], "cls", "") != goClass(tok) {
			t.Errorf("JS and Go disagree on %q: js=%v go=%+v", tok, res[i], ClassifyBall(tok))
		}
	}
}

func TestClassifyBallRunTotals(t *testing.T) {
	// The overlay's over summary comes from the score delta, the DB logger sums these.
	for tok, runs := range map[string]int{"": 0, ".": 0, "0": 0, "1": 1, "4": 4, "6": 6, "W": 0,
		"w": 1, "w+4": 5, "nb": 1, "1nb": 2, "2nb+4": 7, "2b": 2, "4lb": 4, "xyz": 0, "14": 14} {
		mustEq(t, ClassifyBall(tok).Runs, runs, tok)
	}
}

// ── Bowler milestones (hat-trick chain, five-for) and the ticker diff ──

const milestoneStubs = `
var cfg = { graphics_milestones: true };
var fired = [];
function triggerBowlerMilestone(big, name, sub) { fired.push({big: big, name: name, sub: sub}); }
function fmtSurname(f){ if(!f||f==='—')return '—'; var p=f.trim().split(' '); return p[p.length-1]; }
var _hatTrick = { bowler: '', count: 0 };
var _htPrevWickets = 0;
var _bowlerFived = {};
var _lastPCSinnings = 1;
function poll(tokens, bowler, wickets, wicketType, prevOverBowler) {
  updateHatTrickChain(
    tokens.map(function(c){ return {cls: c}; }),
    {bowler: {name: bowler}, wickets: wickets, lastWicketType: wicketType || ''},
    prevOverBowler || null);
}
`

func milestones(t *testing.T, body string) []M {
	t.Helper()
	js := milestoneStubs + overlayFunctions(t, "_chainWicket", "_chainBreak", "updateHatTrickChain", "checkBowlerFiveFor") +
		"\n" + body + "\nconsole.log(JSON.stringify(fired));"
	var fired []M
	runJS(t, js, &fired)
	return fired
}

func TestHatTrickThreeInThreeFiresOnce(t *testing.T) {
	f := milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['wicket'], 'A Nother', 3);
poll(['dot'],    'A Nother', 3);`)
	mustEq(t, len(f), 1)
	eqv(t, f[0]["sub"], "HAT-TRICK!")
	eqv(t, f[0]["name"], "Nother")
}

func TestHatTrickDotBallBreaksChain(t *testing.T) {
	mustEq(t, len(milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['dot'],    'A Nother', 2);
poll(['wicket'], 'A Nother', 3);`)), 0)
}

func TestHatTrickChainSurvivesOtherBowlersOver(t *testing.T) {
	f := milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['dot'], 'B Side', 2);
poll(['dot','1'], 'B Side', 2);
poll(['wicket'], 'A Nother', 3);`)
	mustEq(t, len(f), 1)
	eqv(t, f[0]["sub"], "HAT-TRICK!")
}

func TestHatTrickWidesAndNoballsChainNeutral(t *testing.T) {
	mustEq(t, len(milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wide'],   'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['noball'], 'A Nother', 2);
poll(['wicket'], 'A Nother', 3);`)), 1)
}

func TestHatTrickRunOutBreaksWithoutExtending(t *testing.T) {
	mustEq(t, len(milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2, 'RUN OUT');
poll(['wicket'], 'A Nother', 3);
poll(['wicket'], 'A Nother', 4);`)), 0)
}

func TestHatTrickOverCompletingWicketCountsViaDelta(t *testing.T) {
	// The ticker clears on the over-completing write, and state.bowler has ALREADY
	// rotated: the wicket is credited from the pre-rotation snapshot.
	f := milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll([], 'B Side', 3, '', {name: 'A Nother'});`)
	mustEq(t, len(f), 1)
	eqv(t, f[0]["name"], "Nother")
}

func TestHatTrickOverCompletingWicketNeverCreditsIncomingBowler(t *testing.T) {
	f := milestones(t, `
poll(['wicket'], 'B Side', 1);
poll(['wicket'], 'B Side', 2);
poll(['dot'], 'A Nother', 2);
poll([], 'B Side', 3, '', {name: 'A Nother'});
poll(['dot'], 'B Side', 3);
poll(['wicket'], 'A Nother', 4);
poll(['wicket'], 'A Nother', 5);`)
	mustEq(t, len(f), 1)
	eqv(t, []any{f[0]["name"], f[0]["sub"]}, []any{"Nother", "HAT-TRICK!"})
}

func TestHatTrickFinalBallInTickerGoesToFinishedOversBowler(t *testing.T) {
	f := milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
updateHatTrickChain([{cls: 'wicket'}], {bowler: {name: 'B Side'}, wickets: 3},
                    {name: 'A Nother'}, true);`)
	mustEq(t, len(f), 1)
	eqv(t, f[0]["name"], "Nother")
}

func TestHatTrickTwoBowlersWicketsNeverMix(t *testing.T) {
	mustEq(t, len(milestones(t, `
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'B Side',   2);
poll(['wicket'], 'A Nother', 3);`)), 0)
}

func TestFiveForFiresAtFiveOnceThenAgainAtSix(t *testing.T) {
	f := milestones(t, `
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 4, runs: 18, overs: '6.2'}});
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21, overs: '7.1'}});
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21, overs: '7.1'}});
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 6, runs: 23, overs: '7.4'}});`)
	var bigs []any
	for _, x := range f {
		bigs = append(bigs, x["big"])
	}
	eqv(t, bigs, []any{"5-21", "6-23"})
	contains(t, gs(f[0], "sub", ""), "5 wicket haul")
}

func TestFiveForRespectsMilestonesToggle(t *testing.T) {
	mustEq(t, len(milestones(t, `
cfg.graphics_milestones = false;
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21}});
cfg.graphics_milestones = true;
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21}});`)), 1)
}

func TestFiveForOnOverCompletingDelivery(t *testing.T) {
	f := milestones(t, `
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 4, runs: 18, overs: '6.5'}});
_htPrevWickets = 4;
poll([], 'B Side', 5, '', {name: 'A Nother', wickets: 4, runs: 18, overs: '6.5'});`)
	mustEq(t, len(f), 1)
	eqv(t, []any{f[0]["big"], f[0]["name"]}, []any{"5-18", "Nother"})
}

func TestFiveForRunOutOnFinalBallIsNot(t *testing.T) {
	mustEq(t, len(milestones(t, `
_htPrevWickets = 4;
poll([], 'B Side', 5, 'RUN OUT', {name: 'A Nother', wickets: 4, runs: 18});`)), 0)
}

func TestFiveForPlaceholderBowlerIgnored(t *testing.T) {
	mustEq(t, len(milestones(t, `
checkBowlerFiveFor({bowler: {name: '—', wickets: 5, runs: 21}});
checkBowlerFiveFor({});`)), 0)
}

// newBalls mirrors processPCSData's bookkeeping around tickerOverOf/newTickerBalls across
// NV Play's three over-completing behaviours.
func newBalls(t *testing.T, polls [][2]string) [][]string {
	t.Helper()
	var b strings.Builder
	b.WriteString(overlayFunctions(t, "tickerOverOf", "newTickerBalls"))
	b.WriteString(`
var lastSig = null, lastOver = -1, lastCount = 0, out = [];
function poll(overs, ticker) {
  var parsed = ticker ? ticker.split(' ') : [];
  var tOver = tickerOverOf(overs);
  if (ticker !== lastSig) {
    if (lastOver >= 0) out.push(newTickerBalls(parsed, tOver, lastOver, lastCount));
    lastSig = ticker; lastCount = parsed.length; lastOver = tOver;
  } else { out.push([]); }
}
`)
	for _, p := range polls {
		fmt.Fprintf(&b, "poll(%s, %s);\n", p[0], jsonOf(p[1]))
	}
	b.WriteString("console.log(JSON.stringify(out));")
	var out [][]string
	runJS(t, b.String(), &out)
	return out
}

func TestTickerStaleThenNextOver(t *testing.T) {
	eqv(t, newBalls(t, [][2]string{{"4.4", "1 . 4 2"}, {"4.5", "1 . 4 2 6"}, {"5.0", "1 . 4 2 6"}, {"5.1", "4"}}),
		[][]string{{"6"}, {}, {"4"}})
}

func TestTickerFinalBallShownOnCompletingWrite(t *testing.T) {
	// Used to re-fire the whole over (the 4 again) and then swallow the next over's 4.
	eqv(t, newBalls(t, [][2]string{{"4.5", "1 . 4 2 ."}, {"5.0", "1 . 4 2 . 6"}, {"5.1", "4"}}), [][]string{{"6"}, {"4"}})
}

func TestTickerClearedOnCompletingWrite(t *testing.T) {
	eqv(t, newBalls(t, [][2]string{{"4.5", "1 . 4 2 ."}, {"5.0", ""}, {"5.1", "6"}}), [][]string{{}, {"6"}})
}

func TestTickerInningsStartIsNotAFinishedOver(t *testing.T) {
	eqv(t, newBalls(t, [][2]string{{"0.0", ""}, {"0.1", "4"}}), [][]string{{"4"}})
}

// ── Camera auto-cut at the over boundary: the two gates and replay-collision avoidance ──

const autocutStubs = `
var SERVER = 'http://test';
var cfg = {};
var _lastReplayTime = 0;
var fetchCalls = [];
function fetch(url, opts) {
  fetchCalls.push({url: url, body: JSON.parse(opts.body)});
  return Promise.resolve({ok: true, text: function(){return Promise.resolve('');}});
}
var _cameraAutoCutTimer = null;
var _walkOut = null;
var _pendingTimeout = null;
function setTimeout(fn, ms) { _pendingTimeout = {fn: fn, ms: ms}; return 1; }
function clearTimeout(id) { _pendingTimeout = null; }
function flushTimeout() { var t = _pendingTimeout; _pendingTimeout = null; if (t) t.fn(); }
function scenesRequested() { return fetchCalls.map(function(c){
  return c.body.scene + (c.body.hold ? ' hold ' + c.body.hold + ' back ' + c.body.back : ''); }); }
var console = { log: function(){ process.stderr.write(Array.prototype.slice.call(arguments).join(' ') + '\n'); },
               warn: function(){ process.stderr.write(Array.prototype.slice.call(arguments).join(' ') + '\n'); } };
`

func autocut(t *testing.T, body string) []string {
	t.Helper()
	js := autocutStubs + overlayFunctions(t, "autoCutCameraForOver", "cutCameraScene") + "\n" + body +
		"\nprocess.stdout.write(JSON.stringify({scenes: scenesRequested()}));"
	var out struct{ Scenes []string }
	runJS(t, js, &out)
	if out.Scenes == nil {
		out.Scenes = []string{}
	}
	return out.Scenes
}

const twoCams = `cfg = { graphics_camera_auto_cut: true, camera2_configured: true,
        obs_bowler_scene: 'Main-Bowler', obs_main_scene: 'Main', replay_duration: 18 };
`

// The cut back is the server's (camhold.go: only if OBS is still on camera 2), after
// camera_over_hold seconds.
func TestAutoCutHoldsBowlerEndAndAsksForTheWayBack(t *testing.T) {
	eqv(t, autocut(t, twoCams+"autoCutCameraForOver();\nflushTimeout();"), []string{"Main-Bowler hold 8 back Main"})
	eqv(t, autocut(t, twoCams+"cfg.camera_over_hold = 40;\nautoCutCameraForOver();"), []string{"Main-Bowler hold 40 back Main"})
}

func TestAutoCutLeavesCameraTwoToTheWalkOut(t *testing.T) {
	eqv(t, autocut(t, twoCams+"_walkOut = {balls: 60, innings: 1};\nautoCutCameraForOver();"), []string{})
}

func TestAutoCutNoopWhenToggleOff(t *testing.T) {
	eqv(t, autocut(t, "cfg = { graphics_camera_auto_cut: false, camera2_configured: true };\nautoCutCameraForOver();\nflushTimeout();"), []string{})
}

func TestAutoCutNoopWithoutSecondCamera(t *testing.T) {
	eqv(t, autocut(t, "cfg = { graphics_camera_auto_cut: true, camera2_configured: false };\nautoCutCameraForOver();\nflushTimeout();"), []string{})
}

func TestAutoCutSkipsWhileReplayStillRunning(t *testing.T) {
	// A replay from earlier in the over could still be on screen: skip both legs rather
	// than race the replay's own scene transition.
	eqv(t, autocut(t, twoCams+"_lastReplayTime = Date.now() - 5000;\nautoCutCameraForOver();\nflushTimeout();"), []string{})
}

func TestAutoCutStaleReplayDoesNotBlockLaterOver(t *testing.T) {
	eqv(t, autocut(t, twoCams+"_lastReplayTime = Date.now() - 60000;\nautoCutCameraForOver();\nflushTimeout();"), []string{"Main-Bowler hold 8 back Main"})
}

// ── Scorebar styles: the three places that define them must agree ──

func scorebarStyles(t *testing.T) (overlay, control string, js, picker []string) {
	t.Helper()
	overlay, control = page(t, "overlay.html"), page(t, "control.html")
	m := regexp.MustCompile(`(?s)const SCOREBAR_STYLES = \[(.*?)\];`).FindStringSubmatch(overlay)
	if m == nil {
		t.Fatal("SCOREBAR_STYLES not found in overlay.html")
	}
	for _, s := range regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(m[1], -1) {
		js = append(js, s[1])
	}
	p := regexp.MustCompile(`(?s)<select id="scorebar_style">(.*?)</select>`).FindStringSubmatch(control)
	if p == nil {
		t.Fatal("scorebar_style picker not found in control.html")
	}
	for _, s := range regexp.MustCompile(`<option value="([a-z]+)"`).FindAllStringSubmatch(p[1], -1) {
		picker = append(picker, s[1])
	}
	return
}

func TestEveryJSStyleHasACSSBlock(t *testing.T) {
	overlay, _, js, _ := scorebarStyles(t)
	for _, s := range js {
		contains(t, overlay, "body.style-"+s+" #scorebar")
	}
}

func TestPickerOffersClassicPlusEveryJSStyle(t *testing.T) {
	_, _, js, picker := scorebarStyles(t)
	want := append([]string{"classic"}, js...)
	sort.Strings(want)
	sort.Strings(picker)
	eqv(t, picker, want)
}

func TestClassicIsNotAStyleClass(t *testing.T) {
	overlay, _, js, _ := scorebarStyles(t)
	for _, s := range js {
		if s == "classic" {
			t.Fatal("classic in SCOREBAR_STYLES")
		}
	}
	if strings.Contains(overlay, "body.style-classic") {
		t.Fatal("a body.style-classic block")
	}
}

func TestDefaultScorebarStyleIsSelectable(t *testing.T) {
	_, _, _, picker := scorebarStyles(t)
	want := gs(DefaultState(), "scorebar_style", "")
	for _, p := range picker {
		if p == want {
			return
		}
	}
	t.Fatal(want)
}

func TestDarkStylesOverrideInlineBowlerColour(t *testing.T) {
	// applyColours() sets #bowler-name's colour inline to #fff; a style that puts the
	// bowler on a light background must win that with !important or the name vanishes.
	overlay, _, _, _ := scorebarStyles(t)
	for _, s := range []string{"minimal", "block"} { // the two with a light bowler block
		if !regexp.MustCompile(`body\.style-` + s + ` #bowler-name \{[^}]*color:[^;]*!important`).MatchString(overlay) {
			t.Errorf("%s: #bowler-name's colour must be !important", s)
		}
	}
}

func TestConfigExampleDocumentsEveryStyle(t *testing.T) {
	_, _, js, _ := scorebarStyles(t)
	ex := page(t, "config.example.ini")
	contains(t, ex, "scorebar_style")
	for _, s := range append([]string{"classic"}, js...) {
		contains(t, ex, s)
	}
}

func TestPreviewModeNeverTouchesMutatingEndpoints(t *testing.T) {
	// /live advances event detection and /commands POPS the queue: an open panel's style
	// preview must never poll, or it eats the OBS overlay's events and commands mid-match.
	overlay := page(t, "overlay.html")
	contains(t, overlay, "if (_params.get('preview') === '1') {")
	if !regexp.MustCompile(`if \(new URLSearchParams\(location\.search\)\.get\('preview'\) !== '1'\) \{\s*\n\s*setInterval\(pollCommands`).MatchString(overlay) {
		t.Fatal("pollCommands is no longer gated on preview mode")
	}
}

func TestPlayerCardAccentsAreTeamColoured(t *testing.T) {
	overlay := page(t, "overlay.html")
	for _, id := range []string{"pc-accent", "pc-accent-right", "pc-label", "pc-label-right"} {
		contains(t, overlay, `id="`+id+`"`, "'"+id+"'")
	}
}

func TestEveryStyleKeepsTheSharedBarHeight(t *testing.T) {
	// 72px is load-bearing: graphic panels sit at bottom:72px.
	overlay, _, js, _ := scorebarStyles(t)
	for _, s := range js {
		m := regexp.MustCompile(`(?s)body\.style-` + s + ` #scorebar \{(.*?)\}`).FindStringSubmatch(overlay)
		if m == nil {
			t.Fatalf("no #scorebar block for %s", s)
		}
		if strings.Contains(m[1], "height") {
			t.Fatalf("style %s overrides the shared 72px height", s)
		}
	}
}

// ── The control panel's structure (tabs, pinned bar, preview, club accent) ──

var tabs = []string{"match", "graphics", "after", "setup"}

func panelIDs(panel string) []string {
	var ids []string
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(panel, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func jsArray(panel, name string) []string {
	m := regexp.MustCompile(`\b` + name + `\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(panel)
	if m == nil {
		return nil
	}
	var out []string
	for _, s := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, s[1])
	}
	return out
}

func TestPanelIDsAreUnique(t *testing.T) {
	seen, dupes := map[string]int{}, []string{}
	for _, id := range panelIDs(page(t, "control.html")) {
		if seen[id]++; seen[id] == 2 {
			dupes = append(dupes, id)
		}
	}
	mustEq(t, len(dupes), 0, dupes)
}

func TestPanelEverySavedSettingStillOnThePage(t *testing.T) {
	// saveState() collects these by id; a field lost in a restructure silently stops
	// saving. The server-managed ones are filled by Fetch today's match, or retired.
	panel := page(t, "control.html")
	fields := append(append(jsArray(panel, "FIELDS"), jsArray(panel, "NUM_FIELDS")...), jsArray(panel, "BOOL_FIELDS")...)
	if len(fields) <= 40 {
		t.Fatalf("only %d fields found", len(fields))
	}
	ids := map[string]bool{}
	for _, id := range panelIDs(panel) {
		ids[id] = true
	}
	managed := map[string]bool{"match_url": true, "umpire1_name": true, "umpire2_name": true,
		"competition_name": true, "pc_match_id": true, "replay_motto": true, "poll_interval": true}
	for _, f := range fields {
		if !ids[f] && !managed[f] {
			t.Error("missing field:", f)
		}
	}
}

func TestPanelEveryHandlerExists(t *testing.T) {
	panel := page(t, "control.html")
	for _, m := range regexp.MustCompile(`on(?:click|change|input|keydown)="(?:if\([^)]*\))?([A-Za-z_]\w*)\(`).FindAllStringSubmatch(panel, -1) {
		if !regexp.MustCompile(`(?:async\s+)?function\s+` + m[1] + `\s*\(`).MatchString(panel) {
			t.Error("handler not defined:", m[1])
		}
	}
}

func TestPanelFourTabsEachWithAPanel(t *testing.T) {
	panel := page(t, "control.html")
	for _, tab := range tabs {
		contains(t, panel, `data-tab="`+tab+`"`, `id="tab-`+tab+`"`)
	}
	// Match day is shown in the markup itself, so nothing flashes before the script.
	contains(t, panel, `class="tab-panel active" id="tab-match"`)
}

func TestPanelTabVisibilityIsClassOnly(t *testing.T) {
	// An "#tab-match{display:block}" rule outranked ".tab-panel{display:none}" and pinned
	// Match day open on every tab.
	if regexp.MustCompile(`#tab-\w+\s*\{[^}]*display`).MatchString(page(t, "control.html")) {
		t.Fatal("an #tab-x display rule")
	}
}

func TestPanelSaveAndStatusPinnedOnEveryTab(t *testing.T) {
	panel := page(t, "control.html")
	top := panel[strings.Index(panel, `<div id="topbar">`):strings.Index(panel, `<div class="status" id="status">`)]
	contains(t, top, `id="save-btn"`, `id="live-status"`)
	i := strings.Index(panel, "#topbar{")
	contains(t, panel[i:i+200], "position:sticky")
}

func TestPanelMessagesPointAtTheNewPlaces(t *testing.T) {
	panel := page(t, "control.html")
	var server strings.Builder
	for _, s := range packageSource(t) {
		server.WriteString(s)
	}
	for _, stale := range []string{"Match card", "Camera card", "AI Commentary card", "Stream card"} {
		if strings.Contains(panel, stale) || strings.Contains(server.String(), stale) {
			t.Error("stale place name:", stale)
		}
	}
}

func TestPanelNoClubNameInDefaults(t *testing.T) {
	if strings.Contains(page(t, "control.html"), "'BRIDES'") {
		t.Fatal("a club's name in the panel defaults")
	}
}

func TestScorebarPreviewFrameIsFullCanvasShiftedToBar(t *testing.T) {
	// The preview was blank: the overlay draws the bar at the bottom of a 1080px canvas,
	// and the frame was 72px tall.
	fn := extractFunction(t, page(t, "control.html"), "scaleScorebarPreview")
	contains(t, fn, "PREVIEW_CANVAS_H + 'px'", "frame.style.top = (-_previewBarTop * scale)",
		"if (scale > 0)", "if (top > 0)") // measured only while visible
	contains(t, extractFunction(t, page(t, "control.html"), "showTab"), "scaleScorebarPreview()")
}

// accent runs applyClubAccent for each colour and returns the CSS properties it set.
func accent(t *testing.T, hexes []string) map[string]map[string]string {
	t.Helper()
	panel := page(t, "control.html")
	var fns []string
	for _, n := range []string{"_hexRgb", "_lum", "_contrast", "_lighter", "_css", "applyClubAccent"} {
		fns = append(fns, extractFunction(t, panel, n))
	}
	js := "var PANEL_BG = [11, 20, 34];\nvar props = {};\n" +
		"var document = {documentElement: {style: {setProperty: function (k, v) { props[k] = v; }," +
		" removeProperty: function (k) { delete props[k]; }}}};\n" + strings.Join(fns, "\n") + "\n" +
		"var out = {};\n" + jsonOf(hexes) + ".forEach(function (h) { props = {}; applyClubAccent(h); out[h] = Object.assign({}, props); });\n" +
		"process.stdout.write(JSON.stringify(out));"
	var out map[string]map[string]string
	runJS(t, js, &out)
	return out
}

func contrastOn(t *testing.T, css string) float64 {
	t.Helper()
	var rgb []float64
	for _, m := range regexp.MustCompile(`\d+`).FindAllString(css, -1) {
		v, _ := strconv.ParseFloat(m, 64)
		rgb = append(rgb, v)
	}
	lum := func(c []float64) float64 {
		var l [3]float64
		for i, v := range c[:3] {
			v /= 255
			if v <= 0.03928 {
				l[i] = v / 12.92
			} else {
				l[i] = math.Pow((v+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
	}
	a, b := lum(rgb), lum([]float64{11, 20, 34})
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}

func TestClubAccentEveryKitColourReadsOnDarkPanel(t *testing.T) {
	colours := []string{"#1a3a5c", "#000000", "#7b2d2d", "#f5c842", "#ffffff", "#c62828", "#0a7a3a"}
	out := accent(t, colours)
	for _, c := range colours {
		if contrastOn(t, out[c]["--accent"]) < 4.4 || contrastOn(t, out[c]["--accent-strong"]) < 2.9 {
			t.Error("low contrast for", c, out[c])
		}
	}
}

func TestClubAccentLightKitsGetDarkButtonText(t *testing.T) {
	out := accent(t, []string{"#f5c842", "#ffffff", "#1a3a5c"})
	eqv(t, []any{out["#f5c842"]["--on-accent"], out["#ffffff"]["--on-accent"], out["#1a3a5c"]["--on-accent"]},
		[]any{"#0a0e14", "#0a0e14", "#ffffff"})
}

func TestClubAccentNavyStaysBlueNotGrey(t *testing.T) {
	out := accent(t, []string{"#1a3a5c"})
	var v []int
	for _, m := range regexp.MustCompile(`\d+`).FindAllString(out["#1a3a5c"]["--accent-strong"], -1) {
		n, _ := strconv.Atoi(m)
		v = append(v, n)
	}
	if v[2]-v[0] <= 80 {
		t.Fatal(out["#1a3a5c"])
	}
}

func TestClubAccentNotAColourFallsBackToDefaults(t *testing.T) {
	out := accent(t, []string{"", "blue", "#12"})
	for _, k := range []string{"", "blue", "#12"} {
		if len(out[k]) != 0 {
			t.Fatal(k, out[k])
		}
	}
}

// The pre-match countdown: minutes to cfg.match_time today, and when the panel shows at
// all (not with no time, once it's passed, or hours ahead).
func TestPrematchCountdown(t *testing.T) {
	src := overlayFunctions(t, "minutesToStart", "untilText", "fillCountdownPanel") + `
var els = {};
function el(id) { return els[id] || (els[id] = {style: {}, textContent: ''}); }
var cfg = {};
function at(mins) { var d = new Date(Date.now() + mins * 60000 + 20000);
  return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2); }
var out = {};
cfg.match_time = at(25); out.m = minutesToStart(); out.shown = fillCountdownPanel(); out.left = el('countdown-left').textContent;
out.label = el('countdown-label').textContent.indexOf('Play starts at ' + cfg.match_time) === 0;
cfg.match_time = at(95); out.hr = untilText(minutesToStart());
cfg.match_time = ''; out.none = minutesToStart(); out.noneShown = fillCountdownPanel();
cfg.match_time = at(-30); out.pastShown = fillCountdownPanel();
cfg.match_time = at(300); out.farShown = fillCountdownPanel();
cfg.match_time = '13.30'; out.dot = minutesToStart() !== null;
console.log(JSON.stringify(out));
`
	var got map[string]any
	runJS(t, src, &got)
	// "25 min" from now lands on 25 or 26 whole minutes, depending on this second.
	m, _ := got["m"].(float64)
	if (m != 25 && m != 26) || got["shown"] != true || got["left"] != fmt.Sprintf("in %d min", int(m)) || got["label"] != true {
		t.Fatalf("25 min to go: %v", got)
	}
	if got["hr"] != "in 1 hr 35 min" && got["hr"] != "in 1 hr 36 min" {
		t.Errorf("over an hour: %v", got["hr"])
	}
	if got["none"] != nil || got["noneShown"] != false || got["pastShown"] != false || got["farShown"] != false || got["dot"] != true {
		t.Fatalf("when it doesn't show: %v", got)
	}
}

// Club milestones: straps one after another, never two at once; off means none.
func TestClubMilestoneStraps(t *testing.T) {
	src := overlayFunctions(t, "showClubMilestone", "showStrap", "nextClubStrap") + `
var _clubStraps = [], _clubStrapOn = false, cfg = { home_colour: '#123456' };
var els = {}, shown = [], timers = [];
function el(id) { return els[id] || (els[id] = { style: {}, textContent: '', classList: {
  add: function () { shown.push(els['club-strap-text'].textContent + ' / ' + els['club-strap-sub'].textContent); },
  remove: function () {} } }); }
setTimeout = function (f) { timers.push(f); };
function run() { while (timers.length) timers.shift()(); }
showClubMilestone({ text: 'A passes 1,000 runs for the club', sub: 'since 2014' });
showClubMilestone({ text: 'A: first fifty for the club', sub: 'since 2014' });
var before = timers.length;
showClubMilestone({});
run();
cfg.graphics_club_milestones = false;
showClubMilestone({ text: 'off' });
run();
console.log(JSON.stringify({ shown: shown, oneTimer: before === 1, colour: els['club-strap'].style.borderLeftColor }));
`
	var got struct {
		Shown    []string
		OneTimer bool
		Colour   string
	}
	runJS(t, src, &got)
	if strings.Join(got.Shown, " | ") != "A passes 1,000 runs for the club / since 2014 | A: first fifty for the club / since 2014" ||
		!got.OneTimer || got.Colour != "#123456" {
		t.Fatalf("%+v", got)
	}
}

// Season leaders: both lists shown, names escaped; nothing when the season is empty or the
// switch is off.
func TestSeasonLeadersPanel(t *testing.T) {
	src := overlayFunctions(t, "leaderRows", "fillLeadersPanel") + `
var els = {};
function el(id) { return els[id] || (els[id] = { style: {}, textContent: '', innerHTML: '' }); }
function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;'); }
var cfg = { home_team: 'Home CC' }, out = {};
var _seasonLeaders = null;
out.none = fillLeadersPanel();
_seasonLeaders = { runs: [], wkts: [] }; out.empty = fillLeadersPanel();
_seasonLeaders = { runs: [{ name: 'P <Smith>', runs: 412, inn: '9', avg: '51.50' }], wkts: [] };
out.shown = fillLeadersPanel(); out.runs = el('leaders-runs').innerHTML; out.wkts = el('leaders-wkts').innerHTML;
out.title = el('leaders-title').textContent;
cfg.graphics_season_leaders = false; out.off = fillLeadersPanel();
console.log(JSON.stringify(out));
`
	var got map[string]any
	runJS(t, src, &got)
	runs, _ := got["runs"].(string)
	if got["none"] != false || got["empty"] != false || got["off"] != false || got["shown"] != true {
		t.Fatalf("when it shows: %v", got)
	}
	if !strings.Contains(runs, "P &lt;Smith>") || !strings.Contains(runs, ">412<") || !strings.Contains(runs, "avg 51.50") ||
		got["wkts"] != "—" || got["title"] != "Home CC" {
		t.Fatalf("panel: %v", got)
	}
}

// The walk-out: camera 2 once the wicket's replays are done (main + side-on), held by
// the server; a ball bowled first cuts back at once, and only if still on camera 2; off,
// or without camera 2, nothing.
func TestWalkOut(t *testing.T) {
	src := overlayFunctions(t, "wicketReplayMs", "walkOutCut", "walkOutBall", "ballsOf", "cutCameraScene") + `
var SERVER = '', cfg = { camera_walkout: true, camera2_configured: true, replay_duration: 18,
  obs_bowler_scene: 'Clubhouse', obs_main_scene: 'Main' };
var _walkOut = null, _walkOutTimer = null, timers = [], posts = [];
var report = console.log.bind(console);
console = { log: function () {}, warn: function () {} };
setTimeout = function (f, ms) { timers.push({ f: f, ms: ms }); return timers.length; };
clearTimeout = function () {};
function fetch(u, o) { posts.push(o.body); return Promise.resolve({ ok: true }); }
function fire() { var t = timers.shift(); t.f(); return t.ms; }
var out = {};
out.plain = wicketReplayMs();
cfg.replay_side_on = true; out.sideOn = wicketReplayMs();
walkOutCut(wicketReplayMs(), 'walk-out', { overs: 12.3, innings: 1 });
out.delay = fire();
out.cut = posts.slice();
walkOutBall({ overs: 12.3, innings: 1 }); out.sameBall = posts.length;
walkOutBall({ overs: 12.4, innings: 1 }); out.back = posts[posts.length - 1];
out.over = _walkOut;
cfg.camera_walkout = false; posts = []; timers = [];
walkOutCut(1000, 'walk-out', { overs: 1, innings: 1 }); out.off = timers.length + posts.length;
cfg.camera_walkout = true; cfg.camera2_configured = false;
walkOutCut(1000, 'walk-out', { overs: 1, innings: 1 }); out.noCam = timers.length + posts.length;
cfg.camera2_configured = true;
walkOutCut(0, 'walk-off', { overs: 40, innings: 1 }); fire();
walkOutBall({ overs: 0, innings: 2 }); out.inn2Opened = posts.length;
walkOutBall({ overs: 0.1, innings: 2 }); out.inn2Ball = posts.length;
report(JSON.stringify(out));
`
	var got map[string]any
	runJS(t, src, &got)
	if got["plain"] != 29000.0 || got["sideOn"] != 47000.0 || got["delay"] != 47000.0 {
		t.Fatalf("timing: %v", got)
	}
	if fmt.Sprint(got["cut"]) != `[{"scene":"Clubhouse","hold":35,"back":"Main"}]` || got["sameBall"] != 1.0 ||
		got["back"] != `{"scene":"Main","if_on":"Clubhouse"}` || got["over"] != nil {
		t.Fatalf("cuts: %v", got)
	}
	if got["off"] != 0.0 || got["noCam"] != 0.0 || got["inn2Opened"] != 1.0 || got["inn2Ball"] != 2.0 {
		t.Fatalf("when not: %v", got)
	}
}

// Rain on its way: a strap only while play's on and dry, rain due within 30 minutes, once
// an hour; never in a break or before play, or with the radar switched off.
func TestRainWarning(t *testing.T) {
	src := overlayFunctions(t, "rainWarning", "_scoreStarted", "ballsOf") + `
var cfg = {}, _break = null, _breakAt = null, _rainWarnedAt = 0, straps = [];
function showStrap(label, m) { straps.push(label + ': ' + m.text); }
var soon = { rain_now: false, rain_starts: '15:30', rain_in_min: 15 };
var out = [];
rainWarning(soon); out.push(straps.length);                       // before play
_breakAt = { state: { overs: 12.3, score: 60 } };
rainWarning({ rain_now: false, rain_starts: '16:30', rain_in_min: 75 }); out.push(straps.length);  // not soon
rainWarning(soon); out.push(straps.length);                       // yes
rainWarning(soon); out.push(straps.length);                       // not again within the hour
_rainWarnedAt = 0; _break = { kind: 'drinks' }; rainWarning(soon); out.push(straps.length);
_break = null; rainWarning({ rain_now: true, rain_starts: '' }); out.push(straps.length);
cfg.graphics_rain_radar = false; rainWarning(soon); out.push(straps.length);
console.log(JSON.stringify({ counts: out, first: straps[0] }));
`
	var got struct {
		Counts []int
		First  string
	}
	runJS(t, src, &got)
	if fmt.Sprint(got.Counts) != "[0 0 1 1 1 1 1]" || got.First != "Weather: Rain approaching — likely around 15:30" {
		t.Fatalf("%+v", got)
	}
}

// Breaks: drinks start after the drinks over and end at the next ball — not on the next
// poll whose ticker still holds the finished over (what made the old drinks card vanish);
// the innings break lasts through the change of innings until the reply's first ball.
func TestBreakDetection(t *testing.T) {
	src := overlayFunctions(t, "ballsOf", "updateBreak", "startBreak", "endBreak", "fillBreakPanel", "breakGraphics") + `
var BREAK_LABELS = { drinks: 'Drinks', innings: 'Innings break', rain: 'Rain delay', 'break': 'Break in play' };
var _break = null, _breakAt = null, _drinksDone = {}, _inningsBreakDone = false;
var _breakTimer = null, _breakLoop = null, _weatherBreak = false, _weatherOK = true;
var MAX_OVERS = 40, SERVER = '', queue = [], graphicActive = false, _lastCard = null, _lastCardMeta = null;
var cfg = { drinks_over: 20, home_team: 'Home CC', away_team: 'Rivals CC' };
var posts = [], queued = [];
var els = {};
function el(id) { return els[id] || (els[id] = { style: {}, textContent: '', classList: {
  s: {}, add: function (c) { this.s[c] = 1; }, remove: function (c) { delete this.s[c]; }, contains: function (c) { return !!this.s[c]; } } }); }
function fetch(u, o) { posts.push(u + ' ' + ((o && o.body) || '')); return { catch: function () {} }; }
setTimeout = function (f) { f(); return 0; };
setInterval = function () { return 0; };
clearTimeout = clearInterval = function () {};
function queueGraphic(id) { queued.push(id); }
function showScorecard() {}
var walkOuts = [];
function walkOutCut(ms, reason) { walkOuts.push(reason + '@' + ms); }
function wicketReplayMs() { return 31000; }
function loadPregameForm() {}
function fillLeadersPanel() { return false; }
var radarLoads = 0;
function rainAbout() { return false; }
function loadRadar() { radarLoads++; }
function fillRadarPanel() { return false; }
function oversDisplay(o) { return String(o); }
function frame(inn, overs, wkts, score) { updateBreak({ innings: inn, overs: overs, wickets: wkts || 0, score: score || 0, battingTeamName: 'Home CC' }); }
var log = [];
function note(what) { log.push(what + ':' + (_break ? _break.kind : '-') + ':' + posts.length); }
frame(1, 19.5); note('19.5');
frame(1, 20); note('drinks over done');           // the over-completing write
frame(1, 20); note('stale ticker, same overs');    // the old code ended it here
frame(1, 20); note('still drinks');
frame(1, 20.1); note('first ball');
frame(1, 39.5, 7, 187);
frame(1, 40, 7, 188); note('innings over');
var weatherDuring = el('weather-widget').classList.contains('show');
var sub = el('break-sub').textContent;
frame(2, 0); note('innings 2 opened, no ball yet');
frame(2, 0.1); note('reply under way');
frame(2, 20); note('innings 2 drinks');
frame(2, 20.1);
frame(2, 20); note('no second drinks this innings');
startBreak('rain', false, 0); note('rain from the panel');
frame(2, 20); note('rain, no ball');
frame(2, 20.1); note('rain over');
console.log(JSON.stringify({ log: log, posts: posts, queued: queued, weather: weatherDuring,
  weatherAfter: el('weather-widget').classList.contains('show'), sub: sub }));
`
	var got struct {
		Log, Posts, Queued    []string
		Weather, WeatherAfter bool
		Sub                   string
	}
	runJS(t, src, &got)
	want := []string{"19.5:-:0", "drinks over done:drinks:1", "stale ticker, same overs:drinks:1", "still drinks:drinks:1",
		"first ball:-:2", "innings over:innings:3", "innings 2 opened, no ball yet:innings:3", "reply under way:-:4",
		"innings 2 drinks:drinks:5", "no second drinks this innings:-:6", "rain from the panel:rain:6",
		"rain, no ball:rain:6", "rain over:-:7"}
	if strings.Join(got.Log, " | ") != strings.Join(want, " | ") {
		t.Fatalf("got  %s\nwant %s", strings.Join(got.Log, " | "), strings.Join(want, " | "))
	}
	if !strings.Contains(got.Posts[0], `/break/start {"kind":"drinks"}`) || !strings.HasPrefix(got.Posts[1], "/break/end") {
		t.Fatalf("posts: %v", got.Posts)
	}
	if !got.Weather || got.WeatherAfter {
		t.Error("the weather card goes up for the break and comes down at the first ball")
	}
	if got.Sub != "Rivals CC need 189 to win" {
		t.Errorf("the innings break card: %q", got.Sub)
	}
	if len(got.Queued) == 0 || got.Queued[0] != "break-panel" {
		t.Errorf("break graphics: %v", got.Queued)
	}
}
