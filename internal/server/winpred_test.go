package server

// The live win predictor's server side and its overlay panel (tests/test_win_predictor.py,
// ServerTests and OverlayTests; the model itself is tested in internal/winpredict).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func withSeasonHistory(t *testing.T, hist []any) {
	t.Helper()
	seasonStatsMu.Lock()
	saved := seasonStats
	seasonStats = M{"innings_history": hist}
	seasonStatsMu.Unlock()
	ResetWinHistoryCache()
	t.Cleanup(func() {
		seasonStatsMu.Lock()
		seasonStats = saved
		seasonStatsMu.Unlock()
		ResetWinHistoryCache()
	})
}

func winHist() []any {
	var hist []any
	for i := 0; i < 12; i++ {
		hist = append(hist, M{"overs_limit": 40, "i1_runs": 200 + i, "chase_won": true})
	}
	for i := 0; i < 12; i++ {
		hist = append(hist, M{"overs_limit": 20, "i1_runs": 150 + i, "chase_won": true})
	}
	return hist
}

func chaseState(kv M) M {
	st := M{"innings": 2, "score": 100, "wickets": 3, "overs": 20.0, "targetRuns": 201,
		"ballsRemaining": 120, "runsRequired": 101}
	for k, v := range kv {
		st[k] = v
	}
	return st
}

func TestWinPredictionUsesTheMatchsOwnFormat(t *testing.T) {
	withSeasonHistory(t, winHist())
	w := WinPrediction(chaseState(nil), M{"max_overs": 50})
	if w == nil {
		t.Fatal("no prediction")
	}
	mustEq(t, w.ModelMatches, 12) // the 40-over games only
	mustEq(t, *w.Need, 101)
}

func TestWinPredictionTargetFromRunsRequiredWhenNotMapped(t *testing.T) {
	withSeasonHistory(t, winHist())
	w := WinPrediction(chaseState(M{"targetRuns": 0}), M{})
	if w == nil || w.Need == nil {
		t.Fatal("no prediction")
	}
	mustEq(t, *w.Need, 101)
}

func TestWinHistorySurvivesARestartViaTheCacheFile(t *testing.T) {
	sandbox(t, "")
	withSeasonHistory(t, nil) // as after a restart
	var rows []any
	for i := 0; i < 11; i++ {
		rows = append(rows, M{"overs_limit": 40, "i1_runs": 210})
	}
	b, _ := json.Marshal(M{"innings_history": rows})
	if err := os.WriteFile(SeasonStatsCache, b, 0o644); err != nil {
		t.Fatal(err)
	}
	w := WinPrediction(chaseState(nil), M{})
	if w == nil {
		t.Fatal("no prediction from the cache file")
	}
	mustEq(t, w.ModelMatches, 11)
	if filepath.Dir(SeasonStatsCache) == "" {
		t.Fatal("sandbox didn't move the cache")
	}
}

func TestWinPredictionSwitchedOffOrPreMatch(t *testing.T) {
	withSeasonHistory(t, winHist())
	if WinPrediction(chaseState(nil), M{"graphics_win_predictor": false}) != nil {
		t.Error("switched off but still predicting")
	}
	if WinPrediction(chaseState(M{"score": 0, "wickets": 0, "overs": 0}), M{}) != nil {
		t.Error("a pre-match frame got a prediction")
	}
}

func TestWinPredictorPanelIsInTheOverSequence(t *testing.T) {
	html := page(t, "overlay.html")
	contains(t, html, `id="winpred-display"`)
	contains(t, html, "if (fillWinPredictor())                queueGraphic('winpred-display'")
	contains(t, html, "cfg.graphics_win_predictor === false")
	// It runs inside showOverSummary, which has no `state` in scope: reading one threw a
	// ReferenceError there and skipped the rest of the over sequence.
	i, j := strings.Index(html, "function _fillWinPredictor()"), strings.Index(html, "// ── Camera auto-cut")
	if i < 0 || j < i {
		t.Fatal("_fillWinPredictor not found")
	}
	if regexp.MustCompile(`state`).MatchString(html[i:j]) {
		t.Error("_fillWinPredictor reads `state`")
	}
	contains(t, html, "try { return _fillWinPredictor(); }")
}

// Team strength: our side (team 10) beats theirs (team 20) all season. Batting first,
// before a ball, the predictor says so; the same frame for another match, or before the
// fixture's teams are known, is the league average.
func TestWinPredictionKnowsTodaysTeams(t *testing.T) {
	hist := winHist()
	for i := 0; i < 8; i++ {
		hist = append(hist,
			M{"overs_limit": 40, "i1_runs": 280, "i1_wkts": 5, "i1_balls": 240, "i1_team": "10",
				"i2_runs": 140, "i2_wkts": 10, "i2_balls": 200, "i2_team": "30", "chase_won": false},
			M{"overs_limit": 40, "i1_runs": 130, "i1_wkts": 10, "i1_balls": 190, "i1_team": "20",
				"i2_runs": 131, "i2_wkts": 3, "i2_balls": 160, "i2_team": "30", "chase_won": true})
	}
	withSeasonHistory(t, hist)
	setTeams := func(teams M) {
		seasonStatsMu.Lock()
		seasonStats["today_teams"] = teams
		seasonStatsMu.Unlock()
	}
	cfg := M{"max_overs": 40, "home_team": "Home Town CC", "pc_match_id": "555"}
	first := M{"innings": 1, "score": 4, "wickets": 0, "overs": 1.0, "ballsRemaining": 234,
		"battingTeamName": "Home Town CC 1st XI"}
	avg := WinPrediction(first, cfg).Batting
	setTeams(M{"match_id": "555", "ours": "10", "theirs": "20"})
	ours := WinPrediction(first, cfg).Batting
	if ours < avg+10 {
		t.Fatalf("our strong side batting first: %d, average %d", ours, avg)
	}
	first["battingTeamName"] = "Rivals CC"
	if theirs := WinPrediction(first, cfg).Batting; theirs > avg-10 {
		t.Fatalf("their weak side batting first: %d, average %d", theirs, avg)
	}
	// Chasing: the side batting now is the chaser.
	chase := chaseState(M{"battingTeamName": "Home Town CC", "overs": 0.1, "score": 1, "wickets": 0,
		"ballsRemaining": 239, "runsRequired": 200})
	ourChase := WinPrediction(chase, cfg).Batting
	chase["battingTeamName"] = "Rivals CC"
	if theirChase := WinPrediction(chase, cfg).Batting; ourChase <= theirChase {
		t.Fatalf("chasing 200: ours %d, theirs %d", ourChase, theirChase)
	}
	setTeams(M{"match_id": "999", "ours": "10", "theirs": "20"})
	first["battingTeamName"] = "Home Town CC"
	mustEq(t, WinPrediction(first, cfg).Batting, avg)
}

func TestTodayTeamsOursFirst(t *testing.T) {
	home := M{"home_club_id": "29434", "home_team_id": "10", "away_club_id": "777", "away_team_id": "20"}
	mustEq(t, jsonOf(todayTeams(home, "29434", "555")), `{"match_id":"555","ours":"10","theirs":"20"}`)
	mustEq(t, jsonOf(todayTeams(home, "777", "555")), `{"match_id":"555","ours":"20","theirs":"10"}`)
	if todayTeams(nil, "29434", "555") != nil || todayTeams(M{"home_team_id": "10"}, "29434", "555") != nil {
		t.Fatal("no fixture, no teams")
	}
}
