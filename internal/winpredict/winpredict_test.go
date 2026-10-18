package winpredict

import (
	"math"
	"testing"
)

var model = Model{RPO: 5.0, SDRPO: 1.2, Matches: 40}

func near(t *testing.T, got, want, delta float64) {
	t.Helper()
	if math.Abs(got-want) > delta {
		t.Fatalf("got %v, want %v ± %v", got, want, delta)
	}
}

func TestMatchesTheDLSShape(t *testing.T) {
	near(t, Resources(300, 0, 300), 1.0, 1e-7)
	// Published DLS reference points, to within a few percent
	for _, c := range []struct {
		balls, wkts int
		dls         float64
	}{{240, 0, 0.893}, {150, 0, 0.682}, {300, 5, 0.495}} {
		near(t, Resources(c.balls, c.wkts, 300), c.dls, 0.03)
	}
}

func TestMonotonic(t *testing.T) {
	if Resources(200, 2, 300) <= Resources(100, 2, 300) || Resources(200, 2, 300) <= Resources(200, 6, 300) {
		t.Fatal("not monotonic")
	}
	if Resources(0, 2, 300) != 0 || Resources(100, 10, 300) != 0 {
		t.Fatal("expected zero")
	}
}

func TestChaseExtremes(t *testing.T) {
	for _, c := range []struct {
		score, wkts int
		overs       float64
		want        int
	}{
		{200, 3, 30, 100}, {150, 10, 35, 0}, {150, 4, 40, 0}} {
		if got := Predict(2, c.score, c.wkts, c.overs, 40, model, 200).Batting; got != c.want {
			t.Fatalf("%+v: got %d", c, got)
		}
	}
}

func TestChaseIsNeverCertainWhileItCanTurn(t *testing.T) {
	easy := Predict(2, 190, 0, 30.0, 40, model, 200)
	if easy.Batting != 99 || easy.Bowling != 1 || *easy.Need != 10 || *easy.BallsLeft != 60 {
		t.Fatalf("%+v", easy)
	}
}

func TestMoreWicketsDownMeansLessLikely(t *testing.T) {
	if Predict(2, 100, 2, 20.0, 40, model, 200).Batting <= Predict(2, 100, 7, 20.0, 40, model, 200).Batting {
		t.Fatal("wickets should lower the chance")
	}
}

func TestFirstInningsProjection(t *testing.T) {
	par := Predict(1, 100, 2, 20.0, 40, model, 0)
	if par.Projected == nil || *par.Projected <= 100 {
		t.Fatalf("%+v", par)
	}
	if Predict(1, 160, 2, 20.0, 40, model, 0).Batting <= par.Batting {
		t.Fatal("a faster start should be more likely to win")
	}
}

func TestNoOversLimitNoPrediction(t *testing.T) {
	if Predict(1, 10, 0, 2.0, 0, model, 0) != nil {
		t.Fatal("expected nil")
	}
}

func ip(n int) *int { return &n }

func TestModelFallsBackWithLittleData(t *testing.T) {
	if !LeagueModel(nil).Default {
		t.Fatal("expected default")
	}
	var hist []Innings
	for i := 0; i < 8; i++ {
		hist = append(hist, Innings{OversLimit: 40, I1Runs: ip(200 + i)})
	}
	m := LeagueModel(hist)
	if m.Default {
		t.Fatal("expected a real model")
	}
	near(t, m.RPO, 5.0875, 0.0005)
}

func match(result, applied, gameType, overs string) map[string]any {
	return map[string]any{"match_details": []any{map[string]any{
		"no_of_overs": overs, "result": result, "result_applied_to": applied, "game_type": gameType,
		"innings": []any{
			map[string]any{"team_batting_id": "1", "runs": "180", "wickets": "6"},
			map[string]any{"team_batting_id": "2", "runs": "150", "wickets": "10"}}}}}
}

func TestInningsSummary(t *testing.T) {
	s := InningsSummary(match("W", "1", "Standard", "40"))
	if s.OversLimit != 40 || *s.I1Runs != 180 || *s.ChaseWon {
		t.Fatalf("%+v", s)
	}
	if !*InningsSummary(match("W", "2", "Standard", "40")).ChaseWon {
		t.Fatal("2 won the chase")
	}
	if !*InningsSummary(match("L", "1", "Standard", "40")).ChaseWon { // 1 lost
		t.Fatal("1 lost, so the chase won")
	}
	if InningsSummary(match("W", "1", "Pairs", "40")) != nil || InningsSummary(match("W", "1", "Standard", "")) != nil {
		t.Fatal("expected nil")
	}
}

func TestBacktest(t *testing.T) {
	var hist []Innings
	for _, r := range []int{150, 160, 170, 230, 240, 250} {
		won := r < 200
		hist = append(hist, Innings{OversLimit: 40, I1Runs: ip(r), ChaseWon: &won})
	}
	bt := Backtest(hist, nil)
	if bt.Chases != 6 || *bt.FavouriteWonPct != 100 || *bt.Brier >= 0.25 {
		t.Fatalf("%+v", bt)
	}
}
