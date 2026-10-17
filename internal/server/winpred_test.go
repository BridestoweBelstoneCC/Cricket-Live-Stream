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
