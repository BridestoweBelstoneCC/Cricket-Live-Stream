package server

// The /scoring surface end-to-end against a live server — including manual frames
// flowing through /live into the overlay pipeline and the ball-by-ball DB
// (tests/test_manual_scoring.py's HTTP half; the engine half is in internal/scoring).

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func setupMatch(t *testing.T, s *testServer) M {
	t.Helper()
	st, body := s.postJSON("/scoring/setup", M{"home": "Home CC", "away": "Rival CC", "max_overs": 20,
		"home_xi": "P Smith\nJ Smith\nO Hart", "away_xi": "", "batting_first": "home"})
	mustEq(t, st, 200, body)
	mustEq(t, body["ok"], any(true))
	return gm(body, "state")
}

func TestScoringPageServes(t *testing.T) {
	s := newTestServer(t, "")
	r := s.get("/scoring")
	mustEq(t, r.status, 200)
	if !bytes.HasPrefix(r.body, []byte("<!DOCTYPE html")) || !bytes.Contains(r.body, []byte("Manual Scoring")) {
		t.Fatal("not the scoring page")
	}
}

func TestScoringStateInactiveBeforeSetup(t *testing.T) {
	s := newTestServer(t, "")
	_, body := s.getJSON("/scoring/state")
	mustEq(t, body["active"], any(false))
}

func TestScoringFullFlowDrivesOverlayPipeline(t *testing.T) {
	s := newTestServer(t, "")
	st := setupMatch(t, s)
	mustEq(t, st["active"], any(true))
	eqv(t, gm(st, "striker")["name"], "P Smith")
	// scoring implies stream-safe settings
	mustEq(t, LoadState()["demo_mode"], any(false))
	mustEq(t, LoadState()["use_widget"], any(false))
	for _, b := range []M{{"kind": "1"}, {"kind": "4"}, {"kind": "W", "wicket_type": "caught", "fielder": "Rival CC 5"}} {
		code, d := s.postJSON("/scoring/ball", b)
		mustEq(t, code, 200, d)
	}
	_, d := s.getJSON("/scoring/state")
	eqv(t, d["score"], 5)
	eqv(t, d["wickets"], 1)
	eqv(t, d["this_over"], []any{"1", "4", "W"})
	eqv(t, d["new_batter"], "O Hart")
	// /live: manual outranks everything, masquerades as the PCS feed, and the ball logger
	// writes the manual over into the DB exactly like a real feed
	_, live := s.getJSON("/live")
	mustEq(t, live["source"], any("pcs"))
	mustEq(t, live["feed"], any("manual"))
	eqv(t, gm(live, "state")["score"], 5)
	mustEq(t, rowsStr(queryRows(t, "SELECT outcome FROM balls WHERE over=0 ORDER BY ball")), "[[1] [4] [W]]")
}

// Manual scoring chosen at launch, nothing set up yet: /live must already be the scorer's
// feed (the overlay polls it every 2.5s), not the widget (every ~20s, so the first ball
// took up to ~23s to show).
func TestLiveIsTheFeedWhenManualChosenBeforeSetup(t *testing.T) {
	s := newTestServer(t, "")
	UpdateState(func(st M) { st["use_widget"] = false }) // never PlayCricket from a test
	_, live := s.getJSON("/live")
	mustEq(t, live["source"], any("widget")) // no feed of any kind configured
	t.Setenv(ManualTodayEnv, "1")
	_, live = s.getJSON("/live")
	mustEq(t, live["source"], any("pcs"))
	mustEq(t, live["state"], any(nil))
}

func TestScoringUndoAndResetOverHTTP(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	s.postJSON("/scoring/ball", M{"kind": "6"})
	code, d := s.postJSON("/scoring/undo", M{})
	mustEq(t, code, 200)
	eqv(t, gm(d, "state")["score"], 0)
	code, _ = s.postJSON("/scoring/reset", M{})
	mustEq(t, code, 200)
	_, body := s.getJSON("/scoring/state")
	mustEq(t, body["active"], any(false))
	if fileExists(ManualScoringFile) {
		t.Fatal("reset left the saved session behind")
	}
}

func TestScoringDoubleSetupNeedsResetOrForce(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	s.postJSON("/scoring/ball", M{"kind": "1"})
	code, d := s.postJSON("/scoring/setup", M{"home": "X", "away": "Y"})
	mustEq(t, code, 400)
	if !strings.Contains(gs(d, "error", ""), "in progress") {
		t.Fatal(d)
	}
	code, _ = s.postJSON("/scoring/setup", M{"home": "X", "away": "Y", "force": true})
	mustEq(t, code, 200)
}

func TestScoringBadBallRejectedCleanly(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	code, _ := s.postJSON("/scoring/ball", M{"kind": "banana"})
	mustEq(t, code, 400)
	_, d := s.getJSON("/scoring/state")
	eqv(t, d["score"], 0)
}

func TestScoringEditBallOverHTTP(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	s.postJSON("/scoring/ball", M{"kind": "1"})
	s.postJSON("/scoring/ball", M{"kind": "4"})
	code, d := s.getJSON("/scoring/balls")
	mustEq(t, code, 200)
	mustEq(t, len(gl(d, "balls")), 2)
	// correct the first ball (1 → 6); the drive was really a six
	code, d = s.postJSON("/scoring/edit", M{"index": 0, "kind": "6"})
	mustEq(t, code, 200, d)
	eqv(t, gm(d, "state")["score"], 10)
	// the corrected over flows through to the ball DB via /live too
	s.getJSON("/live")
	mustEq(t, rowsStr(queryRows(t, "SELECT outcome FROM balls WHERE over=0 ORDER BY ball")), "[[6] [4]]")
}

func TestScoringEditBadIndexIs400(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	s.postJSON("/scoring/ball", M{"kind": "1"})
	code, _ := s.postJSON("/scoring/edit", M{"index": 9, "kind": "4"})
	mustEq(t, code, 400)
}

func TestScoringLastOverRecapInState(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	for _, k := range []string{"1", "dot", "2", "dot", "4", "dot"} { // over 1 = 7 runs
		s.postJSON("/scoring/ball", M{"kind": k})
	}
	_, st := s.getJSON("/scoring/state")
	mustEq(t, st["awaiting_bowler"], any(true))
	lo := gm(st, "last_over")
	eqv(t, lo["num"], 1)
	eqv(t, lo["runs"], 7)
	eqv(t, lo["balls"], []any{"1", ".", "2", ".", "4", "."})
}

func TestScoringScorecardEndpoint(t *testing.T) {
	s := newTestServer(t, "")
	setupMatch(t, s)
	s.postJSON("/scoring/ball", M{"kind": "4"})
	r := s.get("/scoring/scorecard")
	mustEq(t, r.status, 200)
	for _, want := range []string{"BATTING", "Home CC"} {
		if !bytes.Contains(r.body, []byte(want)) {
			t.Fatal(want)
		}
	}
}

func TestScoringBallsAndScorecardEmptyBeforeSetup(t *testing.T) {
	s := newTestServer(t, "")
	code, d := s.getJSON("/scoring/balls")
	mustEq(t, code, 200)
	mustEq(t, d["ok"], any(false))
	mustEq(t, fmt.Sprint(d["balls"]), "[]")
	if _, isList := d["balls"].([]any); !isList {
		t.Fatalf("balls is %T, want a list", d["balls"])
	}
	mustEq(t, s.get("/scoring/scorecard").status, 404)
}

func TestScoringPostsAreTokenGated(t *testing.T) {
	// No overlay carve-out here: the scoring page has its own login flow
	s := newTestServer(t, testPW)
	for _, c := range []struct {
		p string
		b M
	}{{"/scoring/ball", M{"kind": "4"}}, {"/scoring/setup", M{"home": "X"}}, {"/scoring/edit", M{"index": 0, "kind": "4"}}} {
		code, _ := s.postJSON(c.p, c.b)
		mustEq(t, code, 401, c.p)
	}
	// ...but the page itself and its read-only views stay open (login happens in-page)
	for _, p := range []string{"/scoring", "/scoring/state", "/scoring/balls", "/scoring/scorecard"} {
		if st := s.get(p).status; st != 200 && st != 404 { // 404 only = no session yet
			t.Fatal(p, st)
		}
	}
}
