// Package winpredict is the live win probability for limited-overs club cricket.
//
// Two ingredients:
//   - How much batting is left: a DLS-style "resources" curve — the fraction of a full
//     innings' runs a side can still expect, from the balls left and the wickets in hand.
//     Same shape as Duckworth-Lewis (Z0·F(w)·(1 - e^(-b·u/F(w)))): late overs are worth
//     more when wickets are in hand, and lost wickets cap what's left however many overs
//     remain.
//   - How this league scores: the average and spread of first-innings totals from this
//     season's real scorecards (the season-stats download), as runs per over so
//     different formats (40, 45, 50 overs…) pool together.
//
// First innings: P(batting side wins) = P(the chase falls short of the projected total).
// Second innings: P(chasing side gets the runs still needed from the resources left).
// Both treat innings scores as normally distributed — a simple, honest model; Backtest()
// reports how it does on the league's own results.
package winpredict

import (
	"math"
	"strconv"
	"strings"

	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

const (
	DefaultRPO        = 5.0  // used until there's enough league history
	DefaultSDFraction = 0.24 // spread of totals as a fraction of the mean
	MinMatches        = 6
	Curve             = 1.6 // how strongly scoring accelerates late in an innings
	WicketPower       = 1.3 // F(w) = (wickets in hand / 10) ** 1.3
)

func phi(x float64) float64 { return 0.5 * (1 + math.Erf(x/math.Sqrt2)) }

// Resources is the fraction (0..1) of a full innings' expected runs still to come.
func Resources(ballsLeft, wicketsLost, totalBalls int) float64 {
	if totalBalls <= 0 || ballsLeft <= 0 || wicketsLost >= 10 {
		return 0
	}
	x := math.Min(float64(ballsLeft)/float64(totalBalls), 1)
	f := math.Pow(float64(10-max(wicketsLost, 0))/10, WicketPower)
	return f * (1 - math.Exp(-Curve*x/f)) / (1 - math.Exp(-Curve))
}

// Innings is one season match as the model sees it (innings_history rows).
type Innings struct {
	OversLimit int   `json:"overs_limit"`
	I1Runs     *int  `json:"i1_runs"`
	I1Wkts     *int  `json:"i1_wkts"`
	I2Runs     *int  `json:"i2_runs"`
	I2Wkts     *int  `json:"i2_wkts"`
	ChaseWon   *bool `json:"chase_won"`
}

type Model struct {
	RPO     float64 `json:"rpo"`
	SDRPO   float64 `json:"sd_rpo"`
	Matches int     `json:"matches"`
	Default bool    `json:"default"`
}

// LeagueModel builds the model from first-innings totals as runs per over of the
// limit. Falls back to club-cricket defaults with little data.
func LeagueModel(history []Innings) Model {
	var rates []float64
	for _, m := range history {
		if m.OversLimit != 0 && m.I1Runs != nil && *m.I1Runs > 0 {
			rates = append(rates, float64(*m.I1Runs)/float64(m.OversLimit))
		}
	}
	if len(rates) < MinMatches {
		return Model{RPO: DefaultRPO, SDRPO: DefaultRPO * DefaultSDFraction, Matches: len(rates), Default: true}
	}
	var sum float64
	for _, r := range rates {
		sum += r
	}
	mean := sum / float64(len(rates))
	var ss float64
	for _, r := range rates {
		ss += (r - mean) * (r - mean)
	}
	sd := math.Sqrt(ss / float64(len(rates)-1))
	return Model{RPO: mean, SDRPO: math.Max(sd, mean*0.08), Matches: len(rates)}
}

// Balls turns "22.3" or 22.3 into 135 balls (the digit after the point is balls).
func Balls(overs any) int {
	f, err := pyval.Float(overs)
	if err != nil {
		return 0
	}
	whole, part, _ := strings.Cut(pyfmt.Fixed(f, 1), ".")
	w, err1 := strconv.Atoi(whole)
	p, err2 := strconv.Atoi(part)
	if err1 != nil || err2 != nil {
		return 0
	}
	return w*6 + p
}

// Prediction is the model's answer for the side batting now. Need/BallsLeft are set
// in a chase, Projected in a first innings.
type Prediction struct {
	Need         *int `json:"need,omitempty"`
	BallsLeft    *int `json:"balls_left,omitempty"`
	Projected    *int `json:"projected,omitempty"`
	Batting      int  `json:"batting"`
	Bowling      int  `json:"bowling"`
	ModelMatches int  `json:"model_matches"`
	ModelDefault bool `json:"model_default"`
}

// Predict returns nil if it can't be worked out (no overs limit). target = runs needed
// to win (innings 2 only).
func Predict(innings, score, wickets int, overs any, oversLimit int, model Model, target int) *Prediction {
	if oversLimit == 0 {
		return nil
	}
	totalBalls := oversLimit * 6
	ballsLeft := max(totalBalls-Balls(overs), 0)
	mean := model.RPO * float64(oversLimit)
	sd := model.SDRPO * float64(oversLimit)
	r := Resources(ballsLeft, wickets, totalBalls)
	var p float64
	out := &Prediction{}
	if innings >= 2 && target != 0 {
		need := target - score
		switch {
		case need <= 0:
			p = 1
		case ballsLeft == 0 || wickets >= 10:
			p = 0
		default:
			mu := mean * r
			sigma := sd*math.Sqrt(math.Max(r, 1e-6)) + 2.0
			p = 1 - phi((float64(need)-0.5-mu)/sigma)
		}
		n, bl := max(target-score, 0), ballsLeft
		out.Need, out.BallsLeft = &n, &bl
	} else {
		projected := float64(score) + mean*r
		sigma1 := sd * math.Sqrt(math.Max(r, 0))
		p = phi((projected - mean) / math.Sqrt(sd*sd+sigma1*sigma1))
		pr := int(math.RoundToEven(projected))
		out.Projected = &pr
	}
	pct := int(math.RoundToEven(p * 100))
	if p > 0 && p < 1 {
		pct = min(max(pct, 1), 99) // never "100%" while it can still turn
	}
	out.Batting, out.Bowling = pct, 100-pct
	out.ModelMatches, out.ModelDefault = model.Matches, model.Default
	return out
}

type BacktestResult struct {
	Chases          int      `json:"chases"`
	FavouriteWonPct *int     `json:"favourite_won_pct"`
	Brier           *float64 `json:"brier"`
}

// Backtest is how the second-innings prediction would have done on the league's own
// results: the chase's win chance at the start of each chase, against what happened.
// Brier: 0 perfect, 0.25 = coin toss.
func Backtest(history []Innings, model *Model) BacktestResult {
	m := LeagueModel(history)
	if model != nil {
		m = *model
	}
	n, right := 0, 0
	brier := 0.0
	for _, h := range history {
		if h.ChaseWon == nil || h.OversLimit == 0 || h.I1Runs == nil || *h.I1Runs == 0 {
			continue
		}
		p := float64(Predict(2, 0, 0, 0, h.OversLimit, m, *h.I1Runs+1).Batting) / 100
		won := 0.0
		if *h.ChaseWon {
			won = 1
		}
		brier += (p - won) * (p - won)
		n++
		if (p >= 0.5) == *h.ChaseWon {
			right++
		}
	}
	if n == 0 {
		return BacktestResult{}
	}
	pct := int(math.RoundToEven(100 * float64(right) / float64(n)))
	b := pyfmt.Round(brier/float64(n), 3)
	return BacktestResult{Chases: n, FavouriteWonPct: &pct, Brier: &b}
}

func num(v any) *int {
	if v == nil {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(pyval.Str(v)))
	if err != nil {
		return nil
	}
	return &n
}

// InningsSummary reduces one season scorecard (PlayCricket match_detail JSON) to the
// facts the model needs, or nil for anything that isn't a normal completed
// limited-overs game (pairs/softball, no result, missing innings).
func InningsSummary(matchDetail map[string]any) *Innings {
	md := map[string]any{}
	if list, ok := matchDetail["match_details"].([]any); ok && len(list) > 0 {
		if m, ok := list[0].(map[string]any); ok {
			md = m
		}
	}
	gt := ""
	if v, ok := md["game_type"]; ok {
		gt = pyval.Str(v)
	}
	if strings.Contains(strings.ToLower(gt), "pairs") {
		return nil
	}
	noo := ""
	if v := md["no_of_overs"]; pyval.Truthy(v) {
		noo = pyval.Str(v)
	}
	limit, err := strconv.Atoi(strings.TrimSpace(noo))
	if err != nil {
		return nil
	}
	inns, _ := md["innings"].([]any)
	if limit <= 0 || len(inns) < 2 {
		return nil
	}
	i1, _ := inns[0].(map[string]any)
	i2, _ := inns[1].(map[string]any)
	r1, r2 := num(i1["runs"]), num(i2["runs"])
	if r1 == nil || r2 == nil {
		return nil
	}
	res, applied := "", ""
	if pyval.Truthy(md["result"]) {
		res = strings.ToUpper(pyval.Str(md["result"]))
	}
	if pyval.Truthy(md["result_applied_to"]) {
		applied = pyval.Str(md["result_applied_to"])
	}
	var chaseWon *bool
	if (res == "W" || res == "L") && applied != "" {
		chaser := ""
		if pyval.Truthy(i2["team_batting_id"]) {
			chaser = pyval.Str(i2["team_batting_id"])
		}
		v := (applied == chaser) == (res == "W")
		chaseWon = &v
	} else if res == "T" {
		v := false
		chaseWon = &v
	}
	return &Innings{OversLimit: limit, I1Runs: r1, I1Wkts: num(i1["wickets"]),
		I2Runs: r2, I2Wkts: num(i2["wickets"]), ChaseWon: chaseWon}
}
