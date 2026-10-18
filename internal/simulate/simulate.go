// Package simulate rehearses the entire broadcast without a scorer: it simulates a
// realistic match ball by ball and writes NV Play-style scoreboard JSON to a folder,
// exactly as PCS Pro would. Point the control panel's "PCS Pro output folder" at the
// simulator's folder (or pass --configure to do that automatically) and the
// overlay/graphics/replay pipeline runs as if it were match day.
//
// Faithful to the real feed in the ways that have caused bugs before:
//   - the ball ticker (last_ball) does NOT update when an over completes: it keeps
//     showing the over as it was before its final ball — on the over-completing write and
//     every write after — until the next ball replaces it. The final delivery never
//     appears in any ticker; it's only in the score delta. Measured on a real match's feed
//     (801 of 857 such polls), and the cause of two shipped bugs this simulator used to
//     hide because it cleared the ticker instead. ClearingTicker restores the old
//     behaviour, which NV Play does occasionally show, so both paths can be rehearsed.
//   - batter names are blank pre-match (not {{placeholders}}), exactly as NV Play renders
//     the template before the scorer picks the openers
//   - innings 2 is signalled by runs_required > 0, and it drops to 0 on the winning runs
//     (the case the server's innings latch exists for)
//   - all values are strings, matching NV Play's template output
//
// The engine is deterministic for a given seed, so it doubles as a regression harness.
package simulate

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"cricketstream/internal/scoring"
)

// Squads: deliberately club-agnostic; two Smiths to rehearse the brothers path.
const (
	HomeTeam = "Home CC"
	AwayTeam = "Rival CC"
)

// xi builds an XI from alternating shirt numbers and names, in batting order.
func xi(pairs ...string) []scoring.Player {
	var out []scoring.Player
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, scoring.Player{Number: pairs[i], Name: pairs[i+1]})
	}
	return out
}

var (
	HomeXI = xi("21", "Peter Smith", "7", "James Smith", "14", "Oliver Hart",
		"9", "Karl Jones", "23", "Ben Walker", "4", "Sam Prowse",
		"11", "Tom Baker", "2", "Harry Cole", "17", "Jack Down",
		"6", "Lee Friend", "30", "Adam Gill")
	AwayXI = xi("5", "Rob North", "12", "Dan Vine", "3", "Max Reed",
		"19", "Joe Marsh", "8", "Ed Stone", "15", "Guy Bell",
		"1", "Ian Frost", "24", "Ali Khan", "10", "Ray Lamb",
		"13", "Cy Woods", "27", "Vic Moon")
)

type weighted struct {
	w    float64
	kind string
}

// Dismissal-type weights for SAMPLED wickets (manual scoring picks the type explicitly).
var simWicketWeights = []weighted{{45, "caught"}, {30, "bowled"}, {12, "lbw"}, {8, "run out"}, {5, "stumped"}}

// Innings is the shared scorer's-book engine plus random outcome sampling — all the
// bookkeeping lives in the engine; only the dice live here. The manual scoring page
// drives the SAME engine with operator button presses instead.
type Innings struct {
	*scoring.Innings
	rng                   *rand.Rand
	WicketBoost, RunBoost float64
}

func pick(rng *rand.Rand, opts []weighted) string {
	total := 0.0
	for _, o := range opts {
		total += o.w
	}
	p := rng.Float64() * total
	for _, o := range opts {
		p -= o.w
		if p <= 0 {
			return o.kind
		}
	}
	return opts[len(opts)-1].kind
}

func (s *Innings) sampleOutcome() string {
	w, r := s.WicketBoost, s.RunBoost
	k := pick(s.rng, []weighted{
		{300, "dot"}, {280 * r, "1"}, {85 * r, "2"}, {12, "3"},
		{95 * r, "4"}, {30 * r, "6"}, {42 * w, "W"},
		{38, "wide"}, {12, "noball"}, {8, "bye"}, {16, "legbye"},
	})
	return k
}

func (s *Innings) choice(opts []int) int { return opts[s.rng.Intn(len(opts))] }

// Ball bowls one randomly sampled delivery; returns the ticker token ("" if the innings
// was already complete).
func (s *Innings) Ball() string {
	if s.Complete() {
		return ""
	}
	s.OpenersSelected = true
	kind := s.sampleOutcome()
	o := scoring.Outcome{Kind: kind}
	switch kind {
	case "W":
		o.WicketType = pick(s.rng, simWicketWeights)
		bowler := s.BowlerForOver(s.CurrentOverNo())
		if o.WicketType == "caught" || o.WicketType == "stumped" || o.WicketType == "run out" {
			var others []string
			for _, f := range s.Fielders {
				if f != bowler.Name {
					others = append(others, f)
				}
			}
			o.Fielder = others[s.rng.Intn(len(others))]
		}
	case "wide":
		o.Runs = s.choice([]int{0, 0, 0, 1, 4})
	case "noball":
		o.Runs = s.choice([]int{0, 0, 1, 2, 4})
	case "bye", "legbye":
		o.Runs = s.choice([]int{1, 1, 2, 4})
	}
	tok, err := s.ApplyOutcome(o)
	if err != nil {
		panic(err) // sampled outcomes are always valid
	}
	return tok
}

// Scenarios are the rehearsals on offer.
var Scenarios = map[string]string{
	"full":     "Complete match from pre-match blanks through both innings to the result",
	"chase":    "Jump straight into a tense final-overs run chase (innings 2 live)",
	"century":  "An opener in the 80s closing in on a hundred (milestone + replay rehearsal)",
	"collapse": "Wickets tumbling — FOW cards, new-batter player cards, bowler figures",
}

// ScenarioNames in a stable order.
func ScenarioNames() []string {
	var n []string
	for k := range Scenarios {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Frame is one write of the scoreboard file, labelled with the match phase.
type Frame struct {
	Label string
	Data  map[string]string
}

// Match produces the frames for a whole scenario. Pure — no sleeping, no I/O — so tests
// can consume it directly; the CLI adds pacing and file writes.
type Match struct {
	Scenario    string
	MaxOvers    int
	StaleTicker bool
	rng         *rand.Rand
	Innings     []*Innings
}

func NewMatch(scenario string, maxOvers int, seed int64, staleTicker bool) (*Match, error) {
	if _, ok := Scenarios[scenario]; !ok {
		return nil, fmt.Errorf("unknown scenario %q — one of %s", scenario, strings.Join(ScenarioNames(), ", "))
	}
	return &Match{Scenario: scenario, MaxOvers: maxOvers, StaleTicker: staleTicker,
		rng: rand.New(rand.NewSource(seed))}, nil
}

func (m *Match) newInnings(second bool, target *int, maxOvers int, wicketBoost, runBoost float64) *Innings {
	bat, bowl, batName, bowlName := HomeXI, AwayXI, HomeTeam, AwayTeam
	if second {
		bat, bowl, batName, bowlName = AwayXI, HomeXI, AwayTeam, HomeTeam
	}
	if maxOvers == 0 {
		maxOvers = m.MaxOvers
	}
	inn := &Innings{Innings: scoring.NewInnings(bat, bowl, batName, bowlName, maxOvers, target, false),
		rng: m.rng, WicketBoost: wicketBoost, RunBoost: runBoost}
	m.Innings = append(m.Innings, inn)
	return inn
}

// fastForward advances an innings silently (no frames) until until(inn) is true.
func fastForward(inn *Innings, until func(*Innings) bool) {
	for !inn.Complete() && !until(inn) {
		inn.Ball()
	}
	inn.TokenHistory = nil // frames start fresh; history is per-emitted-play only
}

// Frames plays the scenario and returns every frame in order.
func (m *Match) Frames() []Frame {
	var out []Frame
	emit := func(label string, inn *Innings, n int) { out = append(out, Frame{label, m.frame(inn, n)}) }
	play := func(inn *Innings, n int) {
		for !inn.Complete() {
			inn.Ball()
			emit("ball", inn, n)
		}
		for i := 0; i < 3; i++ { // hold the final state
			emit("end", inn, n)
		}
	}
	switch m.Scenario {
	case "chase":
		inn1 := m.newInnings(false, nil, 0, 1, 1)
		fastForward(inn1, func(*Innings) bool { return false }) // play innings 1 out silently
		target := inn1.Total + 1
		inn2 := m.newInnings(true, &target, 0, 1, 1)
		fastForward(inn2, func(i *Innings) bool {
			return target-i.Total <= 40 || i.MaxOvers*6-i.LegalBalls <= 30
		})
		play(inn2, 2)
	case "century":
		// A longer innings and docile bowling so an opener reliably reaches the 80s
		inn := m.newInnings(false, nil, max(m.MaxOvers, 40), 0.2, 1.5)
		fastForward(inn, func(i *Innings) bool {
			top := 0
			for _, b := range i.Batters {
				top = max(top, b.Runs)
			}
			return top >= 85
		})
		play(inn, 1)
	case "collapse":
		inn := m.newInnings(false, nil, 0, 1, 1)
		fastForward(inn, func(i *Innings) bool { return i.LegalBalls >= 8*6 })
		inn.WicketBoost = 5.0
		play(inn, 1)
	default:
		// full match: pre-match blanks → innings 1 → break → innings 2 → result
		inn1 := m.newInnings(false, nil, 0, 1, 1)
		for i := 0; i < 3; i++ {
			emit("prematch", inn1, 1)
		}
		play(inn1, 1)
		for i := 0; i < 3; i++ { // innings break holds
			emit("break", inn1, 1)
		}
		target := inn1.Total + 1
		inn2 := m.newInnings(true, &target, 0, 1, 1)
		for i := 0; i < 2; i++ {
			emit("prematch2", inn2, 2) // openers not yet picked
		}
		play(inn2, 2)
	}
	return out
}

// frame is the engine's frame with NV Play's ticker behaviour layered on: the engine
// clears its ticker when an over completes (right for the manual /scoring page, which
// shares it), but NV Play keeps showing the finished over until the next ball.
func (m *Match) frame(inn *Innings, n int) map[string]string {
	f := inn.Frame(n)
	if m.StaleTicker && f["last_ball"] == "" && len(inn.TokenHistory) > 0 &&
		inn.LegalBalls != 0 && inn.LegalBalls%6 == 0 {
		toks := inn.TokenHistory[len(inn.TokenHistory)-1].Tokens
		f["last_ball"] = strings.Join(toks[:len(toks)-1], " ") // all but the final ball
	}
	return f
}
