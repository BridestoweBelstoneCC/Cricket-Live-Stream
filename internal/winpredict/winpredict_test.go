package winpredict

import (
	"encoding/json"
	"math"
	"os"
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

// The chain and the resources curve must agree on what an innings is worth, or the projection
// and the chance would be talking about different matches.
func TestAnInningsIsWorthItsExpectedTotal(t *testing.T) {
	e := newEngine(model, 40)
	dist := finalDist(e.odds[2], e.balls, 0, int(e.total*runsCap))
	var mean, sum float64
	for s, p := range dist {
		mean += float64(s) * p
		sum += p
	}
	near(t, sum, 1, 1e-6)
	near(t, mean, 200, 4)
}

func TestTheDaysSpreadTotalsLikeTheLeague(t *testing.T) {
	var ss, mean float64
	e := newEngine(model, 40)
	dist := make([]float64, int(e.total*runsCap)+1)
	for k := range e.odds {
		for s, p := range finalDist(e.odds[k], e.balls, 0, len(dist)-1) {
			dist[s] += e.prior[k] * p
		}
	}
	for s, p := range dist {
		mean += float64(s) * p
	}
	for s, p := range dist {
		ss += (float64(s) - mean) * (float64(s) - mean) * p
	}
	// model: 1.2 of 5.0 an over, so 48 runs on 200.
	near(t, math.Sqrt(ss), 48, 6)
}

func TestChaseChanceMovesTheRightWay(t *testing.T) {
	e := newEngine(model, 40)
	for _, c := range [][2][4]int{
		{{200, 100, 120, 2}, {200, 80, 120, 2}},  // more runs on the board
		{{200, 100, 120, 2}, {200, 100, 120, 6}}, // fewer wickets down
		{{200, 100, 120, 2}, {200, 100, 90, 2}},  // more balls left
		{{180, 100, 120, 2}, {220, 100, 120, 2}}, // a smaller target, same score
	} {
		a, b := c[0], c[1]
		if pa, pb := e.chase(a[0], a[1], a[2], a[3]), e.chase(b[0], b[1], b[2], b[3]); pa <= pb {
			t.Errorf("%v (%.3f) should beat %v (%.3f)", a, pa, b, pb)
		}
	}
	if p := e.chase(200, 0, 12, 0); p != 0 {
		t.Errorf("200 off 2 overs: %v", p)
	}
}

// Nothing's known before a ball is bowled, so it should be close to even, and the first innings
// and the chase should meet: a first innings that finishes on T, and the chase starting after
// it, are the same moment.
func TestTheInningsMeet(t *testing.T) {
	e := newEngine(model, 40)
	first, proj := e.firstInnings(0, 240, 0)
	near(t, first, 0.5, 0.08)
	near(t, proj, 200, 5)
	for _, total := range []int{150, 200, 260} {
		end, _ := e.firstInnings(total, 0, 5)
		near(t, end, 1-e.chaseFromStart(total+1), 1e-9)
	}
}

// A chase off to a flyer looks like a good day, so it's better placed than the same state
// reached by a side that's scraped there would be; the days are what's doing that.
func TestTheInningsSoFarSaysWhatKindOfDayItIs(t *testing.T) {
	e := newEngine(model, 40)
	flying := e.days(0, 150, 150, 2)
	crawling := e.days(0, 40, 150, 2)
	if flying[4]+flying[3] <= crawling[4]+crawling[3] {
		t.Fatalf("%v vs %v", flying, crawling)
	}
	var sum float64
	for _, w := range flying {
		sum += w
	}
	near(t, sum, 1, 1e-9)
}

func TestSameStateIsNotWorkedOutTwice(t *testing.T) {
	first := Predict(2, 77, 3, 15.2, 40, model, 201)
	second := Predict(2, 77, 3, 15.2, 40, model, 201)
	if first.Batting != second.Batting || *first.Need != 124 || *second.BallsLeft != 148 {
		t.Fatalf("%+v %+v", first, second)
	}
	if other := Predict(2, 150, 3, 25.0, 40, model, 201); other.Batting <= first.Batting {
		t.Fatalf("a stale answer: %+v", other)
	}
}

func BenchmarkFirstBallOfAChase(b *testing.B) {
	for b.Loop() {
		e := newEngine(Model{RPO: 5, SDRPO: 1.2}, 50)
		e.chaseFromStart(250)
	}
}

func BenchmarkABallInAChase(b *testing.B) {
	e := newEngine(Model{RPO: 5, SDRPO: 1.2}, 50)
	for b.Loop() {
		e.chase(301, 30, 270, 1)
	}
}

func BenchmarkABallInAFirstInnings(b *testing.B) {
	e := newEngine(Model{RPO: 5, SDRPO: 1.2}, 50)
	e.startCurve()
	for b.Loop() {
		e.firstInnings(30, 270, 1)
	}
}

// The old formula, kept to compare against: the score's distance from the runs left on the
// resources curve, on a normal distribution, with no idea of the day.
func oldChase(m Model, overs float64, target int) float64 {
	mean, sd := m.RPO*overs, m.SDRPO*overs
	return 1 - 0.5*(1+math.Erf((float64(target)-0.5-mean)/(sd+2)/math.Sqrt2))
}

// Against a real season, when there is one: both models, at the start of each chase, which is
// all the history can check. Point CRICKETSTREAM_WIN_HISTORY at a season_stats_cache.json.
func TestAgainstRealSeason(t *testing.T) {
	path := os.Getenv("CRICKETSTREAM_WIN_HISTORY")
	if path == "" {
		t.Skip("set CRICKETSTREAM_WIN_HISTORY to a season_stats_cache.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		History []Innings `json:"innings_history"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	m := LeagueModel(d.History)
	var oldB float64
	n := 0
	for _, h := range d.History {
		if h.ChaseWon == nil || h.OversLimit == 0 || h.I1Runs == nil || *h.I1Runs == 0 {
			continue
		}
		won := 0.0
		if *h.ChaseWon {
			won = 1
		}
		p := oldChase(m, float64(h.OversLimit), *h.I1Runs+1)
		oldB += (p - won) * (p - won)
		n++
	}
	if n == 0 {
		t.Fatal("no finished chases in that file")
	}
	bt := Backtest(d.History, &m)
	t.Logf("%d chases: old model Brier %.3f, this one %.3f (favourite won %d%%); 0.25 is a coin toss",
		n, oldB/float64(n), *bt.Brier, *bt.FavouriteWonPct)
}

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
