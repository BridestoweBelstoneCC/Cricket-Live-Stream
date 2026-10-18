package server

// Fixes from the 2026-10-02 rehearsal (tests/test_match_report.py): the match report
// must survive a server restart (and never be written from nothing), panel cooldowns
// must not lock out the wrong button, and reconcile must explain an unlinked match
// instead of showing a raw 404.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/ai"
)

type bt struct {
	name        string
	runs, balls int
}

type bw struct {
	name, overs string
	runs, wkts  int
}

// frame is an NV Play-shaped /live state, as LogLiveFigures sees it.
func frame(score, wickets int, overs float64, b1, b2 bt, bowler bw) M {
	return M{"innings": 1, "score": score, "wickets": wickets, "overs": overs,
		"battingTeamName": "Home CC", "ballsRemaining": 0,
		"batter1": M{"name": b1.name, "runs": b1.runs, "balls": b1.balls},
		"batter2": M{"name": b2.name, "runs": b2.runs, "balls": b2.balls},
		"bowler":  M{"name": bowler.name, "overs": bowler.overs, "runs": bowler.runs, "wickets": bowler.wkts}}
}

var (
	smith0 = bt{"Smith", 0, 0}
	jones0 = bt{"Jones", 0, 0}
	quick0 = bw{"Quick", "0", 0, 0}
)

func noModel(t *testing.T) *int {
	t.Helper()
	calls := 0
	saved := ai.Ask
	ai.Ask = func(context.Context, ai.Request) (string, error) {
		calls++
		return "", errors.New("test: no model")
	}
	t.Cleanup(func() { ai.Ask = saved })
	return &calls
}

func TestReportNoDataAnywhereRefused(t *testing.T) {
	sandbox(t, "")
	if _, has := buildMatchSummary(LoadState()); has {
		t.Fatal("a summary from nothing")
	}
}

func TestReportRestartKeepsScorersOwnFigures(t *testing.T) {
	sandbox(t, "")
	LogLiveFigures(frame(30, 0, 5.0, bt{"Smith", 24, 20}, bt{"Jones", 4, 10}, quick0))
	LogLiveFigures(frame(31, 1, 5.1, bt{"Brown", 0, 0}, bt{"Jones", 5, 11}, quick0))
	MatchLogReset() // the restart
	summary, has := buildMatchSummary(LoadState())
	if !has {
		t.Fatal("no data after a restart")
	}
	for _, want := range []string{"Innings 1: Home CC 31-1 (5.1 overs)", "Smith 24 (20 balls)",
		"Jones 5* (11 balls)", "Smith  at 31-1"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("missing %q in:\n%s", want, summary)
		}
	}
}

func TestReportPreMatchAndBlankFramesAreNotAnInnings(t *testing.T) {
	sandbox(t, "")
	LogLiveFigures(frame(0, 0, 0, smith0, jones0, quick0))
	MatchLogSnapshot(M{"innings": 1, "battingTeamName": "Home CC", "score": 0, "wickets": 0, "overs": 0.0})
	if _, has := buildMatchSummary(LoadState()); has {
		t.Fatal("0-0 counted as data")
	}
}

func TestReportOtherMatchesInDBIgnored(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"pc_match_id": "some_other_match"})
	LogLiveFigures(frame(4, 0, 0.1, smith0, jones0, quick0))
	setState(t, M{"pc_match_id": ""})
	if _, has := buildMatchSummary(LoadState()); has {
		t.Fatal("another match's figures used")
	}
}

func TestReportMemoryFillsInningsDBLacks(t *testing.T) {
	sandbox(t, "")
	MatchLogSnapshot(M{"innings": 1, "battingTeamName": "Home CC", "score": 50, "wickets": 2, "overs": 8.3})
	summary, _ := buildMatchSummary(LoadState())
	if !strings.Contains(summary, "Home CC 50-2 (8.3 overs)") {
		t.Fatal(summary)
	}
}

func TestReportBowlerRotatedOffGetsOverTheyFinished(t *testing.T) {
	sandbox(t, "")
	LogLiveFigures(frame(10, 1, 3.5, smith0, jones0, bw{"Quick", "3.5", 10, 1}))
	// The over-completing write: the bowler has already rotated.
	LogLiveFigures(frame(14, 2, 4.0, smith0, jones0, bw{"Slow", "0", 0, 0}))
	var quick M
	for _, b := range listOfMaps(gm(MatchFactsFromDB(CurrentMatchID()), "bowlers")["1"]) {
		if b["name"] == "Quick" {
			quick = b
		}
	}
	eqv(t, []any{quick["o"], quick["r"], quick["w"]}, []any{"4", 14, 2})
}

func TestReportEndpointRefusesWithoutCallingModel(t *testing.T) {
	s := newTestServer(t, "")
	setState(t, M{"anthropic_api_key": "sk-test"})
	calls := noModel(t)
	_, d := s.getJSON("/report/generate")
	mustEq(t, d["ok"], any(false))
	mustEq(t, d["error"], any(NoMatchDataError))
	mustEq(t, *calls, 0)
}

func TestReportAndSocialPostHaveSeparateCooldowns(t *testing.T) {
	s := newTestServer(t, "")
	noModel(t)
	mustEq(t, s.get("/report/generate").status, 200)
	mustEq(t, s.get("/report/generate?type=social").status, 200)
	mustEq(t, s.get("/report/generate").status, 429)
}

func TestReadingCachedUploadTestDoesNotStartCooldown(t *testing.T) {
	s := newTestServer(t, "")
	setState(t, M{"network_test_mbps": 10.0, "network_test_at": now()})
	calls := 0
	saved := measureUploadMbps
	measureUploadMbps = func() (float64, error) { calls++; return 12, nil }
	t.Cleanup(func() { measureUploadMbps = saved })
	mustEq(t, s.get("/obs/stream_check").status, 200) // panel load
	mustEq(t, s.get("/obs/stream_check").status, 200) // and again
	mustEq(t, s.get("/obs/stream_check?force=1").status, 200)
	mustEq(t, calls, 1)
	mustEq(t, s.get("/obs/stream_check?force=1").status, 429)
}

func TestTestCommentaryReturnsTheLineNotGenerating(t *testing.T) {
	// It used to return whatever had arrived after a fixed 3 seconds.
	s := newTestServer(t, "")
	setState(t, M{"anthropic_api_key": "sk-test"})
	saved := ai.Ask
	t.Cleanup(func() { ai.Ask = saved })
	ai.Ask = func(context.Context, ai.Request) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "Richards on the charge.", nil
	}
	st, d := s.postJSON("/commentary/test", M{})
	mustEq(t, st, 200)
	eqv(t, d["text"], "Richards on the charge.")
	ai.Ask = func(context.Context, ai.Request) (string, error) { return "", errors.New("401") }
	rateLimitMu.Lock()
	rateLimitTS = map[string]float64{}
	rateLimitMu.Unlock()
	_, d = s.postJSON("/commentary/test", M{})
	mustEq(t, d["ok"], any(false))
	if !strings.Contains(gs(d, "error", ""), "API key") {
		t.Fatal(d)
	}
}

func TestReconcileExplainsUnlinkedMatch(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"playcricket_api_key": "k"})
	withPCGet(t, func(string, time.Duration) (M, error) { t.Fatal("PlayCricket called"); return nil, nil })
	d := ReconcileMatch("2026-10-02_Home_v_Away")
	if !strings.Contains(gs(d, "error", ""), "isn't linked to a PlayCricket fixture") {
		t.Fatal(d)
	}
}

func TestManualPlayerCardButtonIgnoresAutomaticToggle(t *testing.T) {
	// The toggle is for automatic new-batter cards; the button used to report "showing"
	// while the overlay silently returned.
	src := overlaySource(t)
	for _, want := range []string{"showPlayerCardSplit(prev.batter1, prev.batter2, true)",
		"showPlayerCard(prev.batter1, true)", "if (!cfg.graphics_player_card && !manual) return;"} {
		if !strings.Contains(src, want) {
			t.Fatal(want)
		}
	}
}
