package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/simulate"
)

// The result names a winner only when it's certain.
func TestOverlayMatchResult(t *testing.T) {
	src := overlayFunctions(t, "ballsOf", "matchResult") + `
var MAX_OVERS = 20;
function r(score, wk, overs, target) {
  return matchResult({ score: score, wickets: wk, overs: overs, targetRuns: target,
    battingTeamName: 'Rival CC', bowlingTeamName: 'Home CC' });
}
console.log(JSON.stringify({
  firstInnings: r(150, 3, 12, 0),
  midChase: r(100, 3, 12.2, 150),
  needsOneWithOversLeft: r(149, 3, 12.2, 150),
  onWides: r(11, 0, 0.1, 10),
  lastWicketPair: r(150, 9, 19.5, 150),
  allOut: r(120, 10, 18.3, 150),
  outOfOvers: r(149 - 1, 6, 20, 150),
  oneRun: r(148, 10, 19, 150),
  tiedAllOut: r(149, 10, 19, 150),
  tiedOvers: r(149, 6, 20, 150),
  noName: matchResult({ score: 11, wickets: 0, overs: 1, targetRuns: 10 })
}));`
	var got map[string]string
	runJS(t, src, &got)
	for k, want := range map[string]string{
		"firstInnings": "", "midChase": "", "needsOneWithOversLeft": "",
		"onWides":        "Rival CC won by 10 wickets",
		"lastWicketPair": "Rival CC won by 1 wicket",
		"allOut":         "Home CC won by 29 runs",
		"outOfOvers":     "Home CC won by 1 run",
		"oneRun":         "Home CC won by 1 run",
		"tiedAllOut":     "Match tied",
		"tiedOvers":      "Match tied",
		"noName":         "",
	} {
		mustEq(t, got[k], want)
	}
}

// A chase won with overs left used to get no graphics at all: the summary only knew all out
// and out of overs, and the scorecard only knew a new innings starting.
func TestOverlayChaseWonShowsTheEndOfMatch(t *testing.T) {
	src := catchupStubs + `
function matchResult(s) { return s.targetRuns && s.score >= s.targetRuns ? 'Rival CC won' : ''; }
function showMatchResult(s, r) { shown.push('result ' + r); }
function triggerInningsSummary() { shown.push('innings summary'); }
function showScorecard() { shown.push('scorecard'); }
` + topLevelFunction(t, "processPCSData") + topLevelFunction(t, "detectEvents") + topLevelFunction(t, "parseOverString") +
		overlayFunctions(t, "noteLiveFrame", "classifyBall", "tickerOverOf", "newTickerBalls") + `
_lastCard = { batters: [{ name: 'A' }] }; _lastCardMeta = {};
function chase(overs, ticker, score) {
  t += 3000; shown = []; noteLiveFrame(t);
  var st = { overs: overs, last_ball: ticker, score: score, wickets: 0, innings: 2, targetRuns: 10,
    battingTeamName: 'Rival CC', batter1: { name: 'Smith', runs: 6, balls: 1, onStrike: true },
    batter2: { name: 'Jones', runs: 0, balls: 0 }, bowler: { name: 'Patel' } };
  detectEvents(st, null); processPCSData(st);
  return shown.slice();
}
var out = {};
chase(0, '', 0);
out.six = chase(0.1, '6', 6);
out.won = chase(0.1, '6 w+4', 11);
out.again = chase(0.1, '6 w+4', 11);
process.stdout.write(JSON.stringify(out));`
	var got map[string][]string
	runJS(t, src, &got)
	eqv(t, got["won"], []string{"result Rival CC won", "innings summary", "scorecard"})
	if len(got["again"]) != 0 {
		t.Errorf("shown twice: %v", got["again"])
	}
	for _, s := range got["six"] {
		if strings.Contains(s, "result") {
			t.Errorf("a result before the target: %v", got["six"])
		}
	}
}

// NV Play's feed has no allInnings: without this the summary's columns stayed at their
// dashes, or whatever the last innings left in them.
func TestOverlaySummaryFilledFromTheCard(t *testing.T) {
	src := overlayFunctions(t, "fillSummaryFromCard") + `
var els = {};
function el(id) { return els[id] || (els[id] = { textContent: '' }); }
function fmtSurname(s) { return s.split(' ').pop(); }
function text(id) { return el(id).textContent; }
fillSummaryFromCard({ batters: [{ name: 'Al Reed', runs: 12, balls: 20 }, { name: 'Bo Hale', runs: 40, balls: 33 }],
  bowlers: [{ name: 'Cy Ward', w: 2, r: 30 }, { name: 'Di Pike', w: 2, r: 11 }, { name: 'Ed Vane', w: 0, r: 2 }] }, 60);
var out = { bat: text('summary-top-bat'), batScore: text('summary-top-bat-score'), bowl: text('summary-top-bowl'),
  fig: text('summary-top-bowl-fig'), extras: text('summary-extras') };
fillSummaryFromCard({ batters: [], bowlers: [] }, 60);
out.emptyBat = text('summary-top-bat'); out.emptyExtras = text('summary-extras');
console.log(JSON.stringify(out));`
	var got map[string]string
	runJS(t, src, &got)
	for k, want := range map[string]string{"bat": "Hale", "batScore": "40 (33)", "bowl": "Pike", "fig": "2-11",
		"extras": "8", "emptyBat": "—", "emptyExtras": "—"} {
		mustEq(t, got[k], want)
	}
}

// Play's starting: the show's own graphics come down and its queue goes. The game's stay.
func TestOverlayPlayStartingClearsTheShow(t *testing.T) {
	src := overlayFunctions(t, "clearShowGraphics") + `
var SHOW_PANELS = ['h2h-panel', 'lineup-panel', 'scorecard-panel'];
var queue = [{ id: 'h2h-panel' }, { id: 'over-summary-panel' }, { id: 'lineup-panel' }];
var removed = [];
function el(id) { return { classList: { remove: function () { removed.push(id); } } }; }
clearShowGraphics();
console.log(JSON.stringify({ left: queue.map(function (q) { return q.id; }), removed: removed }));`
	var got struct{ Left, Removed []string }
	runJS(t, src, &got)
	eqv(t, got.Left, []string{"over-summary-panel"})
	eqv(t, got.Removed, []string{"h2h-panel", "lineup-panel", "scorecard-panel"})

	html := page(t, "overlay.html")
	if !strings.Contains(html, "if (_preMatchShow) { _preMatchShow = false; clearShowGraphics(); }") {
		t.Error("the first live frame no longer ends the pre-match show")
	}
	if !strings.Contains(html, "if (!_break) showPreMatchInfo();") {
		t.Error("the pre-match show would run over the innings break again")
	}
}

// The innings break ends when the reply's openers are named, before the first ball.
func TestBreakEndsWhenTheReplysOpenersAreNamed(t *testing.T) {
	src := overlayFunctions(t, "ballsOf", "updateBreak", "endBreak", "isPreMatchPCS", "clearShowGraphics") + `
var SHOW_PANELS = [], queue = [], MAX_OVERS = 40, SERVER = '', cfg = {};
var _break = { kind: 'innings', innings: 1, balls: 240 }, _breakAt = null, _breakTimer = 0, _breakLoop = 0, _weatherBreak = false;
var ended = 0;
function fetch() { ended++; return { catch: function () {} }; }
function el() { return { classList: { remove: function () {} }, style: {} }; }
clearTimeout = clearInterval = function () {};
var out = [];
function f(st) { updateBreak(st); out.push(_break ? 'on' : 'off'); }
var none = { name: '—' }, a = { name: 'Reed' }, b = { name: 'Hale' };
f({ innings: 2, overs: 0, wickets: 0, batter1: none, batter2: none });
f({ innings: 2, overs: 0, wickets: 0, batter1: a, batter2: b });
console.log(JSON.stringify({ out: out, ended: ended }));`
	var got struct {
		Out   []string
		Ended int
	}
	runJS(t, src, &got)
	eqv(t, got.Out, []string{"on", "off"})
	mustEq(t, got.Ended, 1)
}

// Between the innings the scorebar shows the reply about to start: it used to show the
// pre-match "HOME — 0.0" and P1 until the openers were named.
func TestScorebarBetweenTheInningsShowsTheReply(t *testing.T) {
	src := overlayFunctions(t, "isReplyWaiting", "renderReplyWaiting", "renderScorebar", "updateTargetTracker",
		"oversDisplay", "fmtSurname") + `
var MAX_OVERS = 20, cfg = { home_team: 'Home CC' }, _weatherPrematch = false, _preMatchShow = false;
function clearShowGraphics() {}
var els = {};
function el(id) { return els[id] || (els[id] = { textContent: '', innerHTML: '', className: '', style: {}, classList: { remove: function () {} } }); }
var document = { getElementById: el };
var gap = { innings: 2, score: 0, wickets: 0, overs: 0, targetRuns: 151, ballsRemaining: 120,
  battingTeamName: 'Rival CC', bowlingTeamName: 'Home CC', statusText: '—',
  batter1: { name: '—', runs: 0, balls: 0, onStrike: true }, batter2: { name: '—', runs: 0, balls: 0 },
  bowler: { name: '—' } };
var out = { waiting: isReplyWaiting(gap), firstInnings: isReplyWaiting({ innings: 1, targetRuns: 0 }), none: isReplyWaiting(null) };
renderReplyWaiting(gap, 'Home CC', 'Rival CC');
out.team = el('batting-team-name').textContent; out.score = el('score-total').textContent;
out.badge = el('innings-badge').textContent; out.need = el('target-need').textContent;
out.status = el('status-text').textContent; out.runs = el('batter1-runs').textContent;
out.balls = el('batter2-balls').textContent;
gap.score = 140; gap.wickets = 10; gap.statusText = 'RR: 7.00';
renderReplyWaiting(gap, 'Home CC', 'Rival CC');
out.afterStatus = el('status-text').textContent;
console.log(JSON.stringify(out));`
	var got map[string]any
	runJS(t, src, &got)
	for k, want := range map[string]any{"waiting": true, "firstInnings": false, "none": false,
		"team": "RIVAL", "score": "0-0", "badge": "P2", "need": "Need 151", "status": "Innings break",
		"runs": "", "balls": "", "afterStatus": "RR: 7.00"} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if !strings.Contains(page(t, "overlay.html"), "} else if (isReplyWaiting(liveData.state)) {") {
		t.Error("the poll no longer gives the gap between the innings its own scorebar")
	}
}

// The practice names its openers before the first ball, and that frame is enough for a
// first-ball boundary to fire: it used to arrive with the openers, on the frame the overlay
// only takes its bearings from.
func TestPracticeNamesOpenersBeforeTheFirstBall(t *testing.T) {
	m, err := simulate.NewMatch("full", 2, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, fr := range m.Frames() {
		if fr.Label == "openers" {
			if fr.Data["batter1_name"] == "" || fr.Data["overs"] != "0" && fr.Data["overs"] != "0.0" {
				t.Errorf("openers frame: %q at %q", fr.Data["batter1_name"], fr.Data["overs"])
			}
			labels = append(labels, fr.Data["innings"])
		}
	}
	eqv(t, labels, []string{"1", "2"})

	got := catchup(t, `
out.named = frame(0, 0, '', 0, 0);
out.firstBall = frame(20, 0.1, '4', 4, 0);
`)
	eqv(t, got["firstBall"], []string{"flash four", "replay Boundary"})
}

// After a practice the scorer's old file is still the last match's. A change of folder isn't
// the scorer writing: taken for that, last week's final score stayed on air for 12 hours.
func TestPracticeLeavesTheOldScorerFileOld(t *testing.T) {
	sandbox(t, "")
	t.Cleanup(ResetAgentClient)
	ResetAgentClient()
	real := t.TempDir()
	file := filepath.Join(real, simulate.OutputFilename)
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	sim := t.TempDir()
	if err := os.WriteFile(filepath.Join(sim, simulate.OutputFilename), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := func(folder string) M { return M{"pcs_source": "local", "pcs_output_folder": folder} }
	if !leftoverFeed(st(real)) {
		t.Fatal("a two-day-old file isn't a leftover to begin with")
	}
	resetFeedPipeline() // the practice starts
	if leftoverFeed(st(sim)) {
		t.Fatal("the practice's own new file counted as a leftover")
	}
	resetFeedPipeline() // and ends
	if !leftoverFeed(st(real)) {
		t.Error("after the practice the old file passes for a new one")
	}
}
