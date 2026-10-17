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
var _pendingTimeout = null;
function setTimeout(fn, ms) { _pendingTimeout = {fn: fn, ms: ms}; return 1; }
function clearTimeout(id) { _pendingTimeout = null; }
function flushTimeout() { var t = _pendingTimeout; _pendingTimeout = null; if (t) t.fn(); }
function scenesRequested() { return fetchCalls.map(function(c){ return c.body.scene; }); }
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

func TestAutoCutToBowlerEndThenRevertsToMain(t *testing.T) {
	eqv(t, autocut(t, twoCams+"autoCutCameraForOver();\nflushTimeout();"), []string{"Main-Bowler", "Main"})
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

func TestAutoCutSkipsRevertIfReplayStartsDuringBowlerEndWindow(t *testing.T) {
	eqv(t, autocut(t, twoCams+"autoCutCameraForOver();\n_lastReplayTime = Date.now() + 1000;\nflushTimeout();"), []string{"Main-Bowler"})
}

func TestAutoCutStaleReplayDoesNotBlockLaterOver(t *testing.T) {
	eqv(t, autocut(t, twoCams+"_lastReplayTime = Date.now() - 60000;\nautoCutCameraForOver();\nflushTimeout();"), []string{"Main-Bowler", "Main"})
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
	if !regexp.MustCompile(`body\.style-minimal #bowler-name \{[^}]*color:[^;]*!important`).MatchString(overlay) {
		t.Fatal("Minimal must force #bowler-name's colour over applyColours' inline #fff")
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
