package server

// GET /match/scorecard for the clubhouse screen: the scorer's own figures, innings in
// order, the last six overs as a scorer writes them — and never a side effect.

import (
	"database/sql"
	"strings"
	"testing"
)

func seedScorecard(t *testing.T) {
	t.Helper()
	setState(t, M{"pc_match_id": "sc1"})
	DBInit()
	err := withDB(func(tx *sql.Tx) error {
		ex := func(q string, args ...any) {
			if _, err := tx.Exec(q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		ex("INSERT INTO live_innings VALUES ('sc1',1,'Home CC',187,9,'40',240,'')")
		ex("INSERT INTO live_innings VALUES ('sc1',2,'Away CC',142,4,'28.3',240,'')")
		ex("INSERT INTO live_innings VALUES ('other',1,'Someone',5,0,'1',240,'')") // another match: never shown
		ex("INSERT INTO live_batting (match_id,innings,name,runs,balls,out) VALUES ('sc1',2,'J Cole',61,74,0)")
		ex("INSERT INTO live_batting (match_id,innings,name,runs,balls,out) VALUES ('sc1',2,'P Hart',12,20,1)")
		ex("INSERT INTO live_bowling VALUES ('sc1',2,'M Fry','6.3',31,2)")
		ex("INSERT INTO live_fow VALUES ('sc1',2,1,'P Hart',23)")
		// Overs 21..29 of the second innings (over is 0-based: 28 is the 29th over).
		for over := 20; over <= 28; over++ {
			ex("INSERT INTO balls (match_id,innings,over,ball,outcome,runs,is_wicket) VALUES ('sc1',2,?,1,'1',1,0)", over)
		}
		ex("INSERT INTO balls (match_id,innings,over,ball,outcome,runs,is_wicket) VALUES ('sc1',2,28,2,'4',4,0)")
		ex("INSERT INTO balls (match_id,innings,over,ball,outcome,runs,is_wicket) VALUES ('sc1',2,28,3,'W',0,1)")
		ex("INSERT INTO balls (match_id,innings,over,ball,outcome,runs,is_wicket) VALUES ('sc1',2,28,4,'wide',3,0)")
		ex("INSERT INTO balls (match_id,innings,over,ball,outcome,runs,is_wicket) VALUES ('sc1',2,28,5,'dot',0,0)")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMatchScorecard(t *testing.T) {
	sandbox(t, "")
	seedScorecard(t)
	sc := MatchScorecard()
	inns := gl(sc, "innings")
	if len(inns) != 2 {
		t.Fatalf("innings: %v", inns)
	}
	first, second := inns[0].(M), inns[1].(M)
	if first["batting_team"] != "Home CC" || first["score"] != 187 || second["overs"] != "28.3" {
		t.Fatalf("innings order/figures: %v / %v", first, second)
	}
	bats := gl(second, "batters")
	if len(bats) != 2 || bats[0].(M)["name"] != "J Cole" || bats[0].(M)["out"] != false || bats[1].(M)["out"] != true {
		t.Fatalf("batters (batting order, out flags): %v", bats)
	}
	if b := gl(second, "bowlers"); len(b) != 1 || b[0].(M)["w"] != 2 {
		t.Fatalf("bowlers: %v", b)
	}
	if f := gl(second, "fall_of_wickets"); len(f) != 1 || f[0].(M)["score"] != "23-1" || f[0].(M)["batter"] != "P Hart" {
		t.Fatalf("fall of wickets: %v", f)
	}
	if len(gl(first, "batters")) != 0 || len(gl(first, "fall_of_wickets")) != 0 {
		t.Fatalf("first innings should have empty (not missing) lists: %v", first)
	}

	overs := gl(sc, "recent_overs")
	if len(overs) != recentOversShown {
		t.Fatalf("%d recent overs, want %d: %v", len(overs), recentOversShown, overs)
	}
	newest := overs[0].(M)
	if newest["over"] != 29 || strings.Join(toStrings(gl(newest, "balls")), " ") != "1 4 W 2wd •" ||
		newest["runs"] != 8 || newest["wickets"] != 1 {
		t.Fatalf("newest over: %v", newest)
	}
	if overs[len(overs)-1].(M)["over"] != 24 {
		t.Fatalf("oldest shown: %v", overs[len(overs)-1])
	}
}

func toStrings(l []any) []string {
	out := make([]string, len(l))
	for i, v := range l {
		out[i], _ = v.(string)
	}
	return out
}

func TestMatchScorecardBeforeAnyCricket(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"pc_match_id": "empty"})
	DBInit()
	sc := MatchScorecard()
	if sc["ok"] != true || len(gl(sc, "innings")) != 0 || len(gl(sc, "recent_overs")) != 0 {
		t.Fatalf("empty: %v", sc)
	}
}

func TestBallLabel(t *testing.T) {
	for _, c := range []struct {
		outcome string
		runs    int
		want    string
	}{
		{"dot", 0, "•"}, {"", 0, "•"}, {"W", 0, "W"}, {"4", 4, "4"}, {"6", 6, "6"},
		{"wide", 1, "wd"}, {"wide", 5, "4wd"}, {"noball", 1, "nb"}, {"noball", 5, "4nb"},
		{"legbye", 1, "1lb"}, {"bye", 4, "4b"},
	} {
		if got := ballLabel(c.outcome, c.runs); got != c.want {
			t.Errorf("ballLabel(%q, %d) = %q, want %q", c.outcome, c.runs, got, c.want)
		}
	}
}

// Open like /live/view, and read-only: polling it logs nothing and moves no event state.
func TestScorecardRouteIsOpenAndReadOnly(t *testing.T) {
	ts := newTestServer(t, "pw")
	seedScorecard(t)
	count := func() int {
		n := 0
		withDB(func(tx *sql.Tx) error { return tx.QueryRow("SELECT COUNT(*) FROM balls").Scan(&n) })
		return n
	}
	before := count()
	for i := 0; i < 3; i++ {
		code, out := ts.getJSON("/match/scorecard")
		if code != 200 || out["ok"] != true || len(gl(out, "innings")) != 2 {
			t.Fatalf("%d %v", code, out)
		}
	}
	if count() != before {
		t.Fatal("reading the scorecard wrote to the ball log")
	}
}
