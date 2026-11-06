package server

import (
	"strings"
	"testing"
)

// After the overlay goes more than a minute without a score (the server down or hung, OBS
// frozen), the first frame back must only bring the scorebar up to date. Before, every
// four and six from the gap flashed and asked for a replay one after another, and a wicket
// from minutes ago got its card and a replay of footage the buffer no longer held — seen
// in the match-day fault test's crash loop. The real processPCSData and detectEvents run
// here; everything they'd put on screen is a stub that records what it was asked for.
const catchupStubs = `
var cfg = { replay_enabled: true, graphics_player_card: false };
var MAX_OVERS = 40, document = { getElementById: function () { return null; } };
var shown = [];
var console = { log: function () {}, warn: function () {} };
setTimeout = function (f) { f(); return 0; };
function showFlash(k) { shown.push('flash ' + k); }
function triggerReplay(r) { shown.push('replay ' + r); }
function showOverSummary(n) { shown.push('summary ' + n); }
function autoCutCameraForOver() { shown.push('cut'); }
function overExtras() { shown.push('extras'); }
function triggerFOW(name, r, b, how, score, wkts) { shown.push('fow ' + score + '-' + wkts); }
function walkOutCut() { shown.push('walk-out'); }
function wicketReplayMs() { return 29000; }
function triggerMilestone() {} function triggerPartnership() {} function showLineup() {}
function triggerInningsSummary() {} function showScorecard() {} function updateBreak() {}
function walkOutBall() {} function updateSpell() {} function updateTicker() {}
function checkBowlerFiveFor() {} function updatePartnership() {} function rrRecordOver() {}
function updateRunRateTrend() {} function fmtSurname(s) { return s; }
function bId(b) { return b.name; } function isRealBatter() { return false; }
function updateHatTrickChain(balls, state) {
  balls.forEach(function (b) { if (b.cls === 'wicket') shown.push('chain W'); });
  _htPrevWickets = parseInt(state.wickets || 0);
}
var prev = { score: null, wickets: null, overs: null, partnership: 0, partnershipMilestone: 0,
  batsmen: [], lineupInnings: 0, summaryInnings: 0, milestonesFired: new Set() };
var _latestWinPrediction = null, _lastPCSinnings = 1, _lastPCSovers = -1, _currentOverBalls = [];
var _spells = {}, _hatTrick = { bowler: '', count: 0 }, _htPrevWickets = 0, _bowlerFived = {};
var _lastCard = null, _lastCardMeta = null, _rrData = { prevScore: 0, prevWickets: 0 };
var _lastPolledBowler = null, _lastWicketOver = -1, _overRunHistory = [];
var _lastBallSig = null, _lastBallCount = 0, _lastBallOver = -1;
var _playerCardVisible = false, _cardShownAtBall = -1, _pendingFOW = null;
var _pendingCards = [], _playerCardShown = {}, _wicketReplayAt = 0;
var _lastLiveAt = 0, _catchingUp = false, CATCH_UP_AFTER_MS = 60000;
var t = 1000000;
function frame(after, overs, ticker, score, wickets, striker) {
  t += after * 1000;
  shown = [];
  noteLiveFrame(t);
  var b1 = { name: 'Smith', runs: 10, balls: 12, onStrike: striker !== 'Jones' };
  var b2 = { name: 'Jones', runs: 5, balls: 8, onStrike: striker === 'Jones' };
  var st = { overs: overs, last_ball: ticker, score: score, wickets: wickets, innings: 1,
             batter1: b1, batter2: b2, bowler: { name: 'Patel' } };
  detectEvents(st, null);
  processPCSData(st);
  return shown.slice().sort();
}
`

// topLevelFunction cuts a function out at its closing brace in column 0. processPCSData
// holds the string '{{', which throws extractFunction's brace count.
func topLevelFunction(t *testing.T, name string) string {
	t.Helper()
	html := page(t, "overlay.html")
	start := strings.Index(html, "\nfunction "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	end := strings.Index(html[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("no end to %s", name)
	}
	return html[start : start+end+3]
}

func catchup(t *testing.T, body string) map[string][]string {
	t.Helper()
	src := catchupStubs + topLevelFunction(t, "processPCSData") + topLevelFunction(t, "detectEvents") + topLevelFunction(t, "parseOverString") +
		overlayFunctions(t, "noteLiveFrame", "classifyBall", "tickerOverOf", "newTickerBalls") +
		"\nvar out = {};\n" + body +
		"\nprocess.stdout.write(JSON.stringify(out));"
	var got map[string][]string
	runJS(t, src, &got)
	for k, v := range got {
		if v == nil {
			got[k] = []string{}
		}
	}
	return got
}

func TestOverlayCatchUpAfterAGapReplaysNothing(t *testing.T) {
	got := catchup(t, `
frame(0, 4.2, '1 .', 30, 1);
out.before = frame(3, 4.3, '1 . 4', 34, 1);
// Two minutes with no score: two overs, two sixes, a four and a wicket all went by.
out.back = frame(120, 6.3, '6 W 4', 61, 2);
out.next = frame(3, 6.4, '6 W 4 6', 67, 2);
out.overDone = frame(3, 7.0, '6 W 4 6', 67, 2);
`)
	eqv(t, got["before"], []string{"flash four", "replay Boundary"})
	eqv(t, got["back"], []string{}, "first frame after the gap")
	eqv(t, got["next"], []string{"flash six", "replay Six"}, "the first ball after catching up")
	eqv(t, got["overDone"], []string{"cut", "extras", "summary 7"})
}

func TestOverlayShortGapStillPlaysWhatItMissed(t *testing.T) {
	got := catchup(t, `
frame(0, 4.2, '1 .', 30, 1);
out.back = frame(45, 4.5, '1 . 4 W 6', 40, 2, 'Jones');
`)
	eqv(t, got["back"], []string{"chain W", "flash four", "flash six", "fow 40-2", "replay Boundary",
		"replay Six", "replay Wicket", "walk-out"})
}

func TestOverlayCatchUpThresholdIsAMinute(t *testing.T) {
	src := overlayFunctions(t, "noteLiveFrame") + `
var CATCH_UP_AFTER_MS = 60000, _lastLiveAt = 0, _catchingUp = false;
var console = { log: function () {} };
process.stdout.write(JSON.stringify([noteLiveFrame(500000), noteLiveFrame(560000),
  noteLiveFrame(620001), noteLiveFrame(622000)]));`
	var got []bool
	runJS(t, src, &got)
	eqv(t, got, []bool{false, false, true, false}, "first frame, 60s, 60.001s, 2s")
	if !strings.Contains(page(t, "overlay.html"), "noteLiveFrame(Date.now());") {
		t.Fatal("poll() no longer notes each /live frame")
	}
}
