package winpredict

import "math"

// Team strength. The league model says how a typical side scores; a 1st XI top of the
// division chasing a side at the bottom isn't typical. Each team gets a batting and a
// bowling rating from its innings this season: how far above or below par it scored, and
// let others score, as a log of the scoring rate, the same units as the engine's days.
//
// Checked on 484 Devon league matches (2025-26, 69 teams), each predicted from only the
// games before it: before a ball's bowled the chance went from a coin toss (Brier 0.250)
// to 0.230, and the first innings improved throughout. In a chase it adds little, as the
// target already says most of it. Counting last season's games too made it worse: sides
// change.

// TeamInnings is one innings as the ratings see it.
type TeamInnings struct {
	Bat, Bowl                string // team ids: batting, bowling
	Runs, Wkts, Balls, Limit int
}

// Rating is a team's: Bat above 0 scores faster than par, Bowl above 0 keeps the other
// side below it.
type Rating struct {
	Bat, Bowl float64
	Innings   int
}

// Matchup is how far each side's scoring is expected to be from par today (log rate).
type Matchup struct {
	First, Second float64
}

// ratingShrink is how many innings' worth of "average" a rating starts with, so a team
// seen twice isn't rated on two games. Of 3, 6 and 12, 12 held up best at every stage.
const ratingShrink = 12.0

// fullInnings is what an innings was heading for: the runs over the share of the batting
// resources it used. A chase won at 150-3 in 30 overs was going well past 150.
func fullInnings(t TeamInnings) (float64, bool) {
	total := t.Limit * 6
	if total <= 0 || t.Runs <= 0 {
		return 0, false
	}
	used := 1 - Resources(total-min(t.Balls, total), t.Wkts, total)
	if used < 0.25 {
		return 0, false // too little of it to say much
	}
	return float64(t.Runs) / used, true
}

// TeamRatings rates every team in these innings. Par is each format's own average, so a
// 20-over game and a 50-over one rate on the same scale.
func TeamRatings(innings []TeamInnings) map[string]Rating {
	type obs struct {
		bat, bowl string
		dev       float64
	}
	sum, n := map[int]float64{}, map[int]int{}
	var all []TeamInnings
	var full []float64
	for _, t := range innings {
		if f, ok := fullInnings(t); ok && t.Bat != "" && t.Bowl != "" {
			all = append(all, t)
			full = append(full, f)
			sum[t.Limit] += math.Log(f / float64(t.Limit))
			n[t.Limit]++
		}
	}
	var o []obs
	for i, t := range all {
		par := sum[t.Limit] / float64(n[t.Limit])
		o = append(o, obs{t.Bat, t.Bowl, math.Log(full[i]/float64(t.Limit)) - par})
	}
	bat, bowl, cnt := map[string]float64{}, map[string]float64{}, map[string]int{}
	for _, x := range o {
		cnt[x.bat]++
		cnt[x.bowl]++
	}
	// dev = bat[batting side] - bowl[bowling side], fitted by alternating shrunk means.
	for it := 0; it < 20; it++ {
		bs, bn := map[string]float64{}, map[string]float64{}
		for _, x := range o {
			bs[x.bat] += x.dev + bowl[x.bowl]
			bn[x.bat]++
		}
		for k := range bs {
			bat[k] = bs[k] / (bn[k] + ratingShrink)
		}
		ws, wn := map[string]float64{}, map[string]float64{}
		for _, x := range o {
			ws[x.bowl] += bat[x.bat] - x.dev
			wn[x.bowl]++
		}
		for k := range ws {
			bowl[k] = ws[k] / (wn[k] + ratingShrink)
		}
	}
	out := map[string]Rating{}
	for k, c := range cnt {
		out[k] = Rating{Bat: bat[k], Bowl: bowl[k], Innings: c}
	}
	return out
}

// HistoryMatchup is today's matchup from the season's scorecards, with first and second the
// team ids batting first and second. Rows from before these were kept (no team ids) count
// for nothing.
func HistoryMatchup(history []Innings, first, second string) Matchup {
	if first == "" || second == "" {
		return Matchup{}
	}
	var tin []TeamInnings
	add := func(bat, bowl string, runs, wkts, balls *int, limit int) {
		if bat == "" || bowl == "" || runs == nil || balls == nil {
			return
		}
		w := 0
		if wkts != nil {
			w = *wkts
		}
		tin = append(tin, TeamInnings{bat, bowl, *runs, w, *balls, limit})
	}
	for _, h := range history {
		add(h.I1Team, h.I2Team, h.I1Runs, h.I1Wkts, h.I1Balls, h.OversLimit)
		add(h.I2Team, h.I1Team, h.I2Runs, h.I2Wkts, h.I2Balls, h.OversLimit)
	}
	return MatchupFor(TeamRatings(tin), first, second)
}

// MatchupFor is today's matchup from the ratings; an unrated team counts as average.
func MatchupFor(r map[string]Rating, first, second string) Matchup {
	a, b := r[first], r[second]
	return Matchup{First: a.Bat - b.Bowl, Second: b.Bat - a.Bowl}
}
