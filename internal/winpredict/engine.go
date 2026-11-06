package winpredict

import (
	"math"
	"sync"
)

// The ball-by-ball engine, the same idea as WASP (Brooker & Hogan): work back from the end of
// an innings through every state of balls left, wickets lost and runs needed, so a chance is
// what really can happen from here, not a bell curve laid over a projection.
//
// A chain like that has one known fault: every ball is independent, so totals come out far
// too alike (WASP's critics call it thin tails), and club totals vary a lot. Most of that
// spread is the day: the pitch, the outfield, who turned up. So the whole thing is run for a
// handful of "days" (condition multipliers on the scoring rate, spread to match the league's
// real spread of totals), and what's happened so far says which day this looks like: the
// runs and wickets of this innings, and in a chase, partly, the total just set.

const (
	DefaultWkts = 7.5 // wickets in a first innings, until there's enough league history

	// How much of a first-innings total's difference from par is put down to the day, for
	// the side chasing it. Set by judgement, not fitted: the rest is the two sides' bowling.
	// There's no ball-by-ball league data here to fit it from; TestAgainstRealSeason is how
	// to check it against a season's results. On the mid-chase check below, anything from
	// 0.2 to 0.5 did about as well.
	conditionsShare = 0.35

	// How far an innings' score may stray from expected before it says much about the day:
	// variance per expected run. A plain ball's is about 2.5; this is a lot more, as two
	// good batters (or one bad over) aren't the pitch. It was 6, by judgement, and a chase
	// that had lost early wickets was written off too soon: on 101 real club chases
	// (2025-26, rebuilt at each fall of wicket from PlayCricket scorecards), sides it gave
	// 24% won 37% of the time. At 25 they won 27%, and from a third of the way in it beat
	// the old model; it stopped improving beyond about 25.
	evidenceVariance = 25.0

	minConditions = 0.04 // the least spread of days, whatever the league says
	maxConditions = 0.50
	runsCap       = 3.5 // runs tracked, as a multiple of the expected total
)

// What a ball can bring. The template is roughly a club over; tilted to the scoring rate a
// state calls for, it gets more boundaries late on rather than just more of everything.
var (
	ballRuns     = [6]int{0, 1, 2, 3, 4, 6}
	ballTemplate = [6]float64{.50, .30, .07, .01, .10, .02}
	// Five days, at -2..+2 standard deviations, with their normal weights.
	dayZ = [5]float64{-2, -1, 0, 1, 2}
)

const tiltStep = 0.05

var (
	tiltOnce  sync.Once
	tiltTable [][6]float64 // index: mean runs per legal ball / tiltStep
)

// tilted returns the run distribution with the given mean: the template, exponentially
// tilted. Solved once for a grid of means, as it's needed for every state of every day.
func tilted(mean float64) *[6]float64 {
	tiltOnce.Do(func() {
		tiltTable = make([][6]float64, int(5.5/tiltStep)+1)
		for i := range tiltTable {
			target := math.Max(float64(i)*tiltStep, 0.02)
			lo, hi := -8.0, 4.0
			var p [6]float64
			for it := 0; it < 50; it++ {
				th := (lo + hi) / 2
				var sum, m float64
				for j, r := range ballRuns {
					p[j] = ballTemplate[j] * math.Exp(th*float64(r))
					sum += p[j]
				}
				for j, r := range ballRuns {
					p[j] /= sum
					m += p[j] * float64(r)
				}
				if m < target {
					lo = th
				} else {
					hi = th
				}
			}
			tiltTable[i] = p
		}
	})
	i := int(math.Round(mean / tiltStep))
	return &tiltTable[min(max(i, 0), len(tiltTable)-1)]
}

type ballOdds struct {
	out  float64
	runs [6]float64
}

// engine is a league model at one match length: every state's ball, for each of the days.
type engine struct {
	balls  int
	total  float64 // the expected total on an average day
	sigma  float64 // spread of the days (log scoring rate)
	rate   [5]float64
	prior  [5]float64
	odds   [5][][10]ballOdds // [day][balls left][wickets lost]
	curve  [5][]float64      // a chase's chance from its first ball, by target, for each day
	curveN int
}

func hazard(h0 float64, ballsLeft, wickets, totalBalls int, day float64) float64 {
	late := 1 - float64(ballsLeft)/float64(totalBalls)
	return math.Min(h0*(1+0.05*float64(wickets))*(1+0.6*late)/math.Sqrt(day), 0.5)
}

// calibrateHazard finds the wicket rate that gives the league's wickets in an innings.
func calibrateHazard(totalBalls int, wkts float64) float64 {
	lo, hi := 0.001, 0.2
	for it := 0; it < 30; it++ {
		h0 := (lo + hi) / 2
		var alive [11]float64
		alive[0] = 1
		for b := totalBalls; b >= 1; b-- {
			for w := 9; w >= 0; w-- {
				moved := alive[w] * hazard(h0, b, w, totalBalls, 1)
				alive[w] -= moved
				alive[w+1] += moved
			}
		}
		var fell float64
		for w, a := range alive {
			fell += float64(w) * a
		}
		if fell < wkts {
			lo = h0
		} else {
			hi = h0
		}
	}
	return (lo + hi) / 2
}

// oddsFor builds one day's ball at every state. Its runs come from the resources curve
// itself: the mean is exactly what makes the expected runs still to come add up, so the chain
// and the curve can't disagree about how many runs an innings is worth.
func oddsFor(totalBalls int, total, h0, day float64) [][10]ballOdds {
	out := make([][10]ballOdds, totalBalls+1)
	for b := 1; b <= totalBalls; b++ {
		for w := 0; w < 10; w++ {
			pw := hazard(h0, b, w, totalBalls, day)
			here := day * total * Resources(b, w, totalBalls)
			stay := day * total * Resources(b-1, w, totalBalls)
			fall := day * total * Resources(b-1, w+1, totalBalls)
			mean := here - (1-pw)*stay - pw*fall
			legal := tilted(math.Max(mean, 0) / (1 - pw))
			o := ballOdds{out: pw}
			for i, p := range legal {
				o.runs[i] = p * (1 - pw)
			}
			out[b][w] = o
		}
	}
	return out
}

func newEngine(m Model, oversLimit float64) *engine {
	totalBalls := max(int(oversLimit)*6, 6)
	total := math.Max(m.RPO*oversLimit, 1)
	wkts := m.Wkts
	if wkts <= 0 {
		wkts = DefaultWkts
	}
	e := &engine{balls: totalBalls, total: total}
	h0 := calibrateHazard(totalBalls, wkts)

	// How much do totals vary with no difference in the day? The rest of the league's spread
	// is the day.
	plain := oddsFor(totalBalls, total, h0, 1)
	dist := finalDist(plain, totalBalls, 0, int(total*runsCap)+6)
	var mean, ss float64
	for s, p := range dist {
		mean += float64(s) * p
	}
	for s, p := range dist {
		ss += (float64(s) - mean) * (float64(s) - mean) * p
	}
	cv, chain := 0.0, math.Sqrt(ss)/math.Max(mean, 1)
	if m.RPO > 0 {
		cv = m.SDRPO / m.RPO
	}
	e.sigma = math.Min(math.Max(math.Sqrt(math.Max(cv*cv-chain*chain, 0)), minConditions), maxConditions)

	var weights float64
	for k, z := range dayZ {
		e.rate[k] = math.Exp(e.sigma * z)
		e.prior[k] = math.Exp(-z * z / 2)
		weights += e.prior[k]
		if z == 0 {
			e.odds[k] = plain
		} else {
			e.odds[k] = oddsFor(totalBalls, total, h0, e.rate[k])
		}
	}
	for k := range e.prior {
		e.prior[k] /= weights
	}
	e.curveN = int(total*runsCap) + 6
	return e
}

// chaseLayer returns, for a day, the chance of winning with this many balls left, for every
// wickets lost and runs needed: index wickets*(maxNeed+1)+need. It keeps only the last layer,
// as a table of every state would be megabytes a day on the laptop that's streaming.
func chaseLayer(odds [][10]ballOdds, ballsLeft, maxNeed int) []float64 {
	row := maxNeed + 1
	prev, cur := make([]float64, 11*row), make([]float64, 11*row)
	for w := 0; w <= 10; w++ {
		prev[w*row] = 1 // got them
		if maxNeed >= 1 {
			prev[w*row+1] = 0.5 // level: a tie, half a win for both
		}
	}
	copy(cur[10*row:], prev[10*row:]) // all out is final, whatever's left
	for b := 1; b <= ballsLeft; b++ {
		for w := 0; w < 10; w++ {
			o := &odds[b][w]
			cur[w*row] = 1
			here, down := prev[w*row:(w+1)*row], prev[(w+1)*row:(w+2)*row]
			next := cur[w*row : (w+1)*row]
			for n := 1; n <= maxNeed; n++ {
				v := o.out * down[n]
				for i, r := range ballRuns {
					if n > r {
						v += o.runs[i] * here[n-r]
					} else {
						v += o.runs[i]
					}
				}
				next[n] = v
			}
		}
		prev, cur = cur, prev
	}
	return prev
}

// finalDist is the runs still to come from a state, as a distribution (capped at maxAdd).
func finalDist(odds [][10]ballOdds, ballsLeft, wickets, maxAdd int) []float64 {
	row := maxAdd + 1
	final := make([]float64, row)
	if wickets >= 10 {
		final[0] = 1
		return final
	}
	alive, next := make([]float64, 10*row), make([]float64, 10*row)
	alive[max(wickets, 0)*row] = 1
	for b := ballsLeft; b >= 1; b-- {
		clear(next)
		for w := 0; w < 10; w++ {
			o := &odds[b][w]
			for s := 0; s <= maxAdd; s++ {
				mass := alive[w*row+s]
				if mass < 1e-12 {
					continue
				}
				if w == 9 {
					final[s] += mass * o.out
				} else {
					next[(w+1)*row+s] += mass * o.out
				}
				for i, r := range ballRuns {
					next[w*row+min(s+r, maxAdd)] += mass * o.runs[i]
				}
			}
		}
		alive, next = next, alive
	}
	for i, mass := range alive {
		final[i%row] += mass
	}
	return final
}

// days weighs the five days: the prior moved by centre (log scoring rate), then by this
// innings so far (runs from the part of its resources already used).
func (e *engine) days(centre float64, score, ballsLeft, wickets int) [5]float64 {
	centre = math.Min(math.Max(centre, -2*e.sigma), 2*e.sigma)
	used := 1 - Resources(ballsLeft, wickets, e.balls)
	var w [5]float64
	var sum float64
	best := math.Inf(-1)
	logs := [5]float64{}
	for k, z := range dayZ {
		// The prior's own weight is N(z; 0, 1); moving its centre multiplies by this ratio.
		c := centre / e.sigma
		l := math.Log(e.prior[k]) + z*c - c*c/2
		if used > 0 {
			exp := e.rate[k] * e.total * used
			v := evidenceVariance*math.Max(exp, 1) + 4
			d := float64(score) - exp
			l -= d*d/(2*v) + 0.5*math.Log(v)
		}
		logs[k] = l
		best = math.Max(best, l)
	}
	for k, l := range logs {
		w[k] = math.Exp(l - best)
		sum += w[k]
	}
	for k := range w {
		w[k] /= sum
	}
	return w
}

// chaseCentre is where the chasing side's day starts: partly what the target says about the
// pitch, and the chasing side's own strength (strength.go). The part of the target the two
// sides explain (mu.First) says nothing about the pitch, so it's taken out first.
func chaseCentre(target int, e *engine, mu Matchup) float64 {
	return conditionsShare*(math.Log(math.Max(float64(target), 1)/e.total)-mu.First) + mu.Second
}

// startCurve is each day's chance from a chase's first ball, for every target. Slow to build
// and the same all match, so kept.
func (e *engine) startCurve() {
	if e.curve[0] != nil {
		return
	}
	row := e.curveN + 1
	for k := range e.curve {
		layer := chaseLayer(e.odds[k], e.balls, e.curveN)
		e.curve[k] = append([]float64(nil), layer[:row]...)
	}
}

// chaseFromStart is the chase's chance before a ball's bowled.
func (e *engine) chaseFromStart(target int, mu Matchup) float64 {
	if target <= 0 {
		return 1
	}
	if target > e.curveN {
		return 0
	}
	e.startCurve()
	w := e.days(chaseCentre(target, e, mu), 0, e.balls, 0)
	var p float64
	for k := range w {
		p += w[k] * e.curve[k][target]
	}
	return p
}

// chase is the chasing side's chance from where it is now.
func (e *engine) chase(target, score, ballsLeft, wickets int, mu Matchup) float64 {
	need := target - score
	switch {
	case need <= 0:
		return 1
	case ballsLeft <= 0 || wickets >= 10:
		return 0
	case need > 6*ballsLeft:
		return 0 // not even all sixes
	}
	if ballsLeft >= e.balls && wickets == 0 && score == 0 {
		return e.chaseFromStart(target, mu)
	}
	w := e.days(chaseCentre(target, e, mu), score, ballsLeft, wickets)
	row := need + 1
	var p float64
	for k := range w {
		if w[k] < 1e-4 {
			continue // a day this can't be: don't work out what it would do
		}
		p += w[k] * chaseLayer(e.odds[k], ballsLeft, need)[wickets*row+need]
	}
	return p
}

// firstInnings is the batting side's chance, and where it's heading, in a first innings: every
// total it could reach, each against the chance of that total being chased.
func (e *engine) firstInnings(score, ballsLeft, wickets int, mu Matchup) (float64, float64) {
	w := e.days(mu.First, score, ballsLeft, wickets)
	maxAdd := min(6*max(ballsLeft, 0), int(e.total*runsCap)+6)
	var p, projected float64
	for k := range w {
		if w[k] < 1e-4 {
			continue
		}
		for add, mass := range finalDist(e.odds[k], max(ballsLeft, 0), wickets, maxAdd) {
			if mass == 0 {
				continue
			}
			total := score + add
			projected += w[k] * mass * float64(total)
			p += w[k] * mass * (1 - e.chaseFromStart(total+1, mu))
		}
	}
	return p, projected
}

// One engine per match length and model, and the last answer: /live asks every poll and the
// score only changes every ball or so.
type predictKey struct {
	innings, score, wickets, ballsLeft, target int
	overs                                      float64
	model                                      Model
	mu                                         Matchup
}

type engineKey struct {
	model Model
	overs float64
}

var (
	engineMu   sync.Mutex
	engines    = map[engineKey]*engine{}
	lastKey    predictKey
	lastChance float64
	lastProj   float64
	lastSet    bool
)

// engineFor must be called with engineMu held.
func engineFor(m Model, oversLimit float64) *engine {
	k := engineKey{m, oversLimit}
	if e := engines[k]; e != nil {
		return e
	}
	if len(engines) >= 8 { // a backtest across formats; a match only ever uses one
		clear(engines)
	}
	e := newEngine(m, oversLimit)
	engines[k] = e
	return e
}
