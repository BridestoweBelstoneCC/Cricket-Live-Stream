package winpredict

import (
	"reflect"
	"testing"
)

// A small league: A scores big and keeps others down, C the opposite, B in between.
func strengthLeague() []Innings {
	var h []Innings
	game := func(t1, t2 string, r1, w1, b1, r2, w2, b2 int) {
		won := r2 > r1
		h = append(h, Innings{OversLimit: 40, I1Runs: ip(r1), I1Wkts: ip(w1), I1Balls: ip(b1),
			I2Runs: ip(r2), I2Wkts: ip(w2), I2Balls: ip(b2), ChaseWon: &won, I1Team: t1, I2Team: t2})
	}
	for i := 0; i < 6; i++ {
		game("A", "B", 260, 6, 240, 180, 10, 230)
		game("B", "C", 220, 7, 240, 150, 10, 210)
		game("C", "A", 140, 10, 200, 141, 2, 150) // A won with 15 overs left: rated at its pace
	}
	return h
}

func TestTeamRatingsOrderTheSides(t *testing.T) {
	var tin []TeamInnings
	for _, h := range strengthLeague() {
		tin = append(tin, TeamInnings{h.I1Team, h.I2Team, *h.I1Runs, *h.I1Wkts, *h.I1Balls, h.OversLimit},
			TeamInnings{h.I2Team, h.I1Team, *h.I2Runs, *h.I2Wkts, *h.I2Balls, h.OversLimit})
	}
	r := TeamRatings(tin)
	if !(r["A"].Bat > r["B"].Bat && r["B"].Bat > r["C"].Bat) {
		t.Fatalf("batting: %+v", r)
	}
	if !(r["A"].Bowl > r["B"].Bowl && r["B"].Bowl > r["C"].Bowl) {
		t.Fatalf("bowling: %+v", r)
	}
	if r["A"].Innings != 24 {
		t.Fatalf("A batted and bowled in 24 innings: %+v", r["A"])
	}
}

func TestMatchupMovesTheChance(t *testing.T) {
	h := strengthLeague()
	m := Model{RPO: 5, SDRPO: 1.2, Wkts: 7.5, Matches: 50}
	strong := HistoryMatchup(h, "A", "C") // A batting first against C
	weak := HistoryMatchup(h, "C", "A")
	if strong.First <= 0 || strong.Second >= 0 || weak.First >= 0 {
		t.Fatalf("matchups: %+v %+v", strong, weak)
	}
	avg := PredictMatchup(1, 0, 0, 0.0, 40, m, 0, Matchup{}).Batting
	a := PredictMatchup(1, 0, 0, 0.0, 40, m, 0, strong).Batting
	c := PredictMatchup(1, 0, 0, 0.0, 40, m, 0, weak).Batting
	if !(a > avg+10 && c < avg-10) {
		t.Fatalf("before a ball: A %d, average %d, C %d", a, avg, c)
	}
	// The same target is easier for the stronger chaser.
	if PredictMatchup(2, 0, 0, 0.0, 40, m, 200, HistoryMatchup(h, "C", "A")).Batting <=
		PredictMatchup(2, 0, 0, 0.0, 40, m, 200, HistoryMatchup(h, "A", "C")).Batting {
		t.Fatal("A chasing 200 against C should beat C chasing 200 against A")
	}
}

func TestNoMatchupIsTheLeagueModel(t *testing.T) {
	m := Model{RPO: 5, SDRPO: 1.2, Wkts: 7.5, Matches: 50}
	for _, c := range [][4]int{{1, 0, 0, 0}, {1, 120, 3, 150}, {2, 0, 0, 0}, {2, 90, 4, 120}} {
		ov := float64(c[3]/6) + float64(c[3]%6)/10
		a := Predict(c[0], c[1], c[2], ov, 40, m, 201)
		b := PredictMatchup(c[0], c[1], c[2], ov, 40, m, 201, Matchup{})
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%v: %+v vs %+v", c, a, b)
		}
	}
	// Unknown teams, or rows from before team ids were kept, change nothing.
	old := []Innings{{OversLimit: 40, I1Runs: ip(200), I2Runs: ip(150)}}
	if HistoryMatchup(old, "A", "B") != (Matchup{}) || HistoryMatchup(strengthLeague(), "", "B") != (Matchup{}) {
		t.Fatal("expected no matchup")
	}
}

func TestInningsSummaryKeepsTeamsAndBalls(t *testing.T) {
	d := match("W", "1", "Standard", "40")
	inns := d["match_details"].([]any)[0].(map[string]any)["innings"].([]any)
	inns[0].(map[string]any)["overs"] = "40"
	inns[1].(map[string]any)["overs"] = "35.4"
	s := InningsSummary(d)
	if s.I1Team != "1" || s.I2Team != "2" || *s.I1Balls != 240 || *s.I2Balls != 214 {
		t.Fatalf("%+v", s)
	}
	if s := InningsSummary(match("W", "1", "Standard", "40")); s.I1Balls != nil {
		t.Fatal("no overs, no balls")
	}
}
