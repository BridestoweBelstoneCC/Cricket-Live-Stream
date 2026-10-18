// Package scoring is the deterministic scorer's-book core shared by two frontends:
//
//   - the match simulator, which feeds it RANDOMLY SAMPLED outcomes (broadcast rehearsal)
//   - the manual scoring page (/scoring), which feeds it OPERATOR BUTTON PRESSES, so a
//     club with no NV Play/PCS Pro can drive the full overlay from a phone
//
// Everything downstream (the overlay, ball-by-ball DB, highlights tagging, graphics)
// speaks the NV Play frame dialect, so Frame() renders the exact field set
// scoreboard.template produces. ApplyOutcome() is pure bookkeeping — no randomness —
// which is what makes the manual page's undo trivially correct: replay the event list
// minus the last entry and the book must come out identical (tests assert this).
//
// Outcome kinds and their extra parameters:
//
//	"dot" "1" "2" "3" "4" "6"   — off the bat (no params)
//	"W"                          — wicket; WicketType, Fielder, OutNonStriker
//	"wide"                       — Runs = runs RAN/boundary beyond the wide itself
//	"noball"                     — Runs = runs off the bat (counts to the batter)
//	"bye" / "legbye"             — Runs = byes taken (1/2/4)
package scoring

import (
	"fmt"
	"strconv"
	"strings"

	"cricketstream/internal/pyfmt"
)

var (
	WicketTypes = []string{"bowled", "caught", "lbw", "run out", "stumped"}
	RunKinds    = []string{"dot", "1", "2", "3", "4", "6"}
	ExtraKinds  = []string{"wide", "noball", "bye", "legbye"}
)

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Surname is the last word of a name, upper-cased (PCS's convention).
func Surname(full string) string {
	f := strings.Fields(full)
	if len(f) == 0 {
		return ""
	}
	return strings.ToUpper(f[len(f)-1])
}

type Batter struct {
	Number, Name              string
	Runs, Balls, Fours, Sixes int
	HowOut                    string // "" = not out / yet to bat
}

func (b *Batter) SR() float64 {
	if b.Balls == 0 {
		return 0
	}
	return pyfmt.Round(float64(b.Runs)/float64(b.Balls)*100, 1)
}

type Bowler struct {
	Number, Name               string
	Legal, Runs, Wkts, Maidens int
	overConceded               int // runs off this bowler in the current over
}

func (w *Bowler) OversStr() string {
	if w.Legal%6 != 0 {
		return fmt.Sprintf("%d.%d", w.Legal/6, w.Legal%6)
	}
	return strconv.Itoa(w.Legal / 6)
}

// FormatDismissal is the PCS-style abbreviated howout string (what LastWicketHowOut
// carries), plus whether the bowler is credited.
func FormatDismissal(wicketType, bowlerName, fielder string) (string, bool) {
	bSur, fSur := "", ""
	if bowlerName != "" {
		bSur = Surname(bowlerName)
	}
	if fielder != "" {
		fSur = Surname(fielder)
	}
	switch wicketType {
	case "caught":
		if fSur != "" {
			return "C " + fSur + " B " + bSur, true
		}
		return "C B " + bSur, true
	case "bowled":
		return "B " + bSur, true
	case "lbw":
		return "LBW B " + bSur, true
	case "stumped":
		if fSur != "" {
			return "ST " + fSur + " B " + bSur, true
		}
		return "ST B " + bSur, true
	}
	// run out — never credited to the bowler
	if fSur != "" {
		return "RUN OUT (" + fSur + ")", false
	}
	return "RUN OUT", false
}

// Player is one (shirt number, name) pair of an XI.
type Player struct{ Number, Name string }

type LastWicket struct {
	HowOut, Batter, Bowler, Fielder, Type string
}

// OverTokens is one completed over's ticker tokens and runs.
type OverTokens struct {
	Tokens []string
	Runs   int
}

// Innings is one innings, one delivery at a time via ApplyOutcome(). Frame() renders
// the current state as the exact map NV Play's scoreboard.template produces (all
// strings).
type Innings struct {
	BattingName, BowlingName string
	MaxOvers                 int
	Target                   *int
	Batters                  []*Batter
	Fielders                 []string
	// Default bowling attack: the last five in the order, two ends alternating,
	// 4-over spells. Manual scoring overrides per over via SetBowler().
	Attack         []*Bowler
	ExtraBowlers   []*Bowler       // bowlers named via SetBowler() outside the default 5
	BowlerSchedule map[int]*Bowler // over_no -> Bowler (manual override)
	Striker        *Batter
	NonStriker     *Batter
	NextBat        int
	Total, Wkts    int
	LegalBalls     int
	Extras         int
	OverTokensNow  []string // this over's ticker tokens
	OverRuns       int      // team runs this over
	overBowlers    map[*Bowler]bool
	OverHistory    []int        // runs per completed over
	TokenHistory   []OverTokens // per completed over — for tests and the ball list
	PRuns, PBalls  int
	LastWicket     LastWicket
	ForceComplete  bool
	// Pre-match: batter names render blank until this flips (the overlay's "hasn't
	// really started" signal). Manual scoring passes true — the operator has already
	// picked the XI, so the scorebar should show at 0-0.
	OpenersSelected bool
}

func NewInnings(battingXI, bowlingXI []Player, battingName, bowlingName string,
	maxOvers int, target *int, openersSelected bool) *Innings {
	e := &Innings{
		BattingName: battingName, BowlingName: bowlingName,
		MaxOvers: maxOvers, Target: target,
		BowlerSchedule:  map[int]*Bowler{},
		overBowlers:     map[*Bowler]bool{},
		NextBat:         2,
		OpenersSelected: openersSelected,
	}
	for _, p := range battingXI {
		e.Batters = append(e.Batters, &Batter{Number: p.Number, Name: p.Name})
	}
	for _, p := range bowlingXI {
		e.Fielders = append(e.Fielders, p.Name)
	}
	if len(bowlingXI) > 6 {
		for _, p := range bowlingXI[6:] {
			e.Attack = append(e.Attack, &Bowler{Number: p.Number, Name: p.Name})
		}
	}
	e.Striker, e.NonStriker = e.Batters[0], e.Batters[1]
	return e
}

// ── Bowlers ───────────────────────────────────────────────

func (e *Innings) AllBowlers() []*Bowler {
	out := make([]*Bowler, 0, len(e.Attack)+len(e.ExtraBowlers))
	return append(append(out, e.Attack...), e.ExtraBowlers...)
}

func (e *Innings) GetBowler(name string) *Bowler {
	for _, w := range e.AllBowlers() {
		if w.Name == name {
			return w
		}
	}
	w := &Bowler{Name: name}
	e.ExtraBowlers = append(e.ExtraBowlers, w)
	return w
}

func (e *Innings) BowlerForOver(overNo int) *Bowler {
	if w, ok := e.BowlerSchedule[overNo]; ok {
		return w
	}
	end := overNo % 2
	var cycle []*Bowler
	for i := end; i < len(e.Attack); i += 2 {
		cycle = append(cycle, e.Attack[i])
	}
	if len(cycle) == 0 {
		cycle = e.Attack
	}
	if len(cycle) == 0 {
		cycle = []*Bowler{e.GetBowler(e.Fielders[len(e.Fielders)-1])}
	}
	return cycle[((overNo/2)/4)%len(cycle)]
}

// SetBowler pins who bowls the upcoming/current over.
func (e *Innings) SetBowler(name string) {
	e.BowlerSchedule[e.CurrentOverNo()] = e.GetBowler(name)
}

// SetBowlerFor pins who bowls a given over.
func (e *Innings) SetBowlerFor(name string, overNo int) {
	e.BowlerSchedule[overNo] = e.GetBowler(name)
}

// ── State queries ─────────────────────────────────────────

func (e *Innings) CurrentOverNo() int { return e.LegalBalls / 6 }

func (e *Innings) Complete() bool {
	return e.ForceComplete || e.Wkts >= 10 || e.LegalBalls >= e.MaxOvers*6 ||
		(e.Target != nil && e.Total >= *e.Target)
}

// AwaitingNewOver is true right after an over completes: the next over hasn't started
// and the manual UI should offer a bowler pick (unless one is already scheduled).
func (e *Innings) AwaitingNewOver() bool {
	_, scheduled := e.BowlerSchedule[e.CurrentOverNo()]
	return e.LegalBalls > 0 && e.LegalBalls%6 == 0 && !e.Complete() && !scheduled &&
		len(e.OverTokensNow) == 0
}

func (e *Innings) batterIndex(b *Batter) int {
	for i, x := range e.Batters {
		if x == b {
			return i
		}
	}
	return -1
}

// ── The book ──────────────────────────────────────────────

func (e *Innings) dismiss(bowler *Bowler, wicketType, fielder string, outNonStriker bool) error {
	if !contains(WicketTypes, wicketType) {
		return fmt.Errorf("unknown wicket type %s", pyRepr(wicketType))
	}
	out := e.Striker
	if outNonStriker {
		out = e.NonStriker
	}
	howout, credit := FormatDismissal(wicketType, bowler.Name, fielder)
	out.HowOut = howout
	e.Wkts++
	if credit {
		bowler.Wkts++
	}
	lw := LastWicket{HowOut: howout, Batter: fmt.Sprintf("%s %d", Surname(out.Name), out.Runs),
		Fielder: fielder, Type: strings.ToUpper(wicketType)}
	if credit {
		lw.Bowler = bowler.Name
	}
	e.LastWicket = lw
	e.PRuns, e.PBalls = 0, 0
	if e.NextBat < len(e.Batters) {
		replacement := e.Batters[e.NextBat]
		e.NextBat++
		if outNonStriker {
			e.NonStriker = replacement
		} else {
			e.Striker = replacement // new batter takes strike
		}
	}
	return nil
}

// ChooseNextBatter swaps the auto-arrived new batter (who must not have faced a ball
// yet) for a different not-yet-batted player — real batting orders change on the day.
func (e *Innings) ChooseNextBatter(name string) error {
	var arrived *Batter
	for _, b := range []*Batter{e.Striker, e.NonStriker} {
		if b.HowOut == "" && b.Balls == 0 && b.Runs == 0 && e.batterIndex(b) == e.NextBat-1 {
			arrived = b
		}
	}
	if arrived == nil {
		return fmt.Errorf("no just-arrived batter to swap — they've already faced a ball")
	}
	var chosen *Batter
	if e.NextBat <= len(e.Batters) {
		for _, b := range e.Batters[e.NextBat:] {
			if b.Name == name && b.HowOut == "" {
				chosen = b
				break
			}
		}
	}
	if chosen == nil {
		return fmt.Errorf("%s is not a yet-to-bat player", pyRepr(name))
	}
	i, j := e.batterIndex(arrived), e.batterIndex(chosen)
	e.Batters[i], e.Batters[j] = e.Batters[j], e.Batters[i]
	if e.Striker == arrived {
		e.Striker = chosen
	} else {
		e.NonStriker = chosen
	}
	return nil
}

// SwapStrike is a manual correction — the book can't always tell who crossed.
func (e *Innings) SwapStrike() { e.Striker, e.NonStriker = e.NonStriker, e.Striker }

// End declares the innings over (rain, declaration, operator's call).
func (e *Innings) End() { e.ForceComplete = true }

// Outcome is one delivery. WicketType is only read for "W".
type Outcome struct {
	Kind          string
	Runs          int
	WicketType    string
	Fielder       string
	OutNonStriker bool
}

// ApplyOutcome applies one delivery to the book and returns the ticker token.
// Deterministic — the manual page's undo/replay and the save-file reload both depend
// on that.
func (e *Innings) ApplyOutcome(o Outcome) (string, error) {
	if e.Complete() {
		return "", fmt.Errorf("innings is already complete")
	}
	kind := o.Kind
	if !contains(RunKinds, kind) && !contains(ExtraKinds, kind) && kind != "W" {
		return "", fmt.Errorf("unknown outcome kind %s", pyRepr(kind))
	}
	runs := o.Runs
	if runs < 0 || runs > 6 {
		return "", fmt.Errorf("runs out of range: %d", runs)
	}
	if kind == "W" && !contains(WicketTypes, o.WicketType) {
		// Checked before anything changes, so a rejected wicket leaves the book as it was.
		return "", fmt.Errorf("unknown wicket type %s", pyRepr(o.WicketType))
	}
	e.OpenersSelected = true
	bowler := e.BowlerForOver(e.CurrentOverNo())
	e.overBowlers[bowler] = true // a mid-over change (injury) means >1 per over
	token, teamRuns, ran := "", 0, 0

	switch {
	case kind == "W":
		e.Striker.Balls++
		bowler.Legal++
		if err := e.dismiss(bowler, o.WicketType, o.Fielder, o.OutNonStriker); err != nil {
			return "", err
		}
		token = "W"
	case kind == "wide":
		ran = runs
		teamRuns = 1 + ran
		e.Extras += teamRuns
		bowler.Runs += teamRuns
		bowler.overConceded += teamRuns
		token = "w"
		if ran != 0 {
			token = fmt.Sprintf("w+%d", ran)
		}
	case kind == "noball":
		bat := runs
		teamRuns = 1 + bat
		e.Extras++
		e.Striker.Runs += bat
		e.Striker.Balls++
		if bat == 4 {
			e.Striker.Fours++
		}
		if bat == 6 {
			e.Striker.Sixes++
		}
		bowler.Runs += teamRuns
		bowler.overConceded += teamRuns
		if bat != 4 && bat != 6 {
			ran = bat
		}
		token = "nb"
		if bat != 0 {
			token = fmt.Sprintf("%dnb", bat)
		}
	case kind == "bye" || kind == "legbye":
		ran = runs
		if ran == 0 {
			ran = 1
		}
		teamRuns = ran
		e.Extras += ran
		e.Striker.Balls++
		bowler.Legal++
		suffix := "b"
		if kind == "legbye" {
			suffix = "lb"
		}
		token = fmt.Sprintf("%d%s", ran, suffix)
		if ran == 4 {
			ran = 0 // boundary byes: no crossing
		}
	case kind == "dot":
		e.Striker.Balls++
		bowler.Legal++
		token = "."
	default: // runs off the bat
		n, _ := strconv.Atoi(kind)
		teamRuns = n
		e.Striker.Runs += n
		e.Striker.Balls++
		if n == 4 {
			e.Striker.Fours++
		}
		if n == 6 {
			e.Striker.Sixes++
		}
		bowler.Legal++
		bowler.Runs += n
		bowler.overConceded += n
		if n != 4 && n != 6 {
			ran = n
		}
		token = kind
	}

	legal := kind != "wide" && kind != "noball"
	if legal {
		e.LegalBalls++
		if kind != "W" { // dismiss just reset the NEW pair's partnership
			e.PBalls++
		}
	}
	e.Total += teamRuns
	e.PRuns += teamRuns
	e.OverRuns += teamRuns
	e.OverTokensNow = append(e.OverTokensNow, token)

	if ran%2 == 1 {
		e.Striker, e.NonStriker = e.NonStriker, e.Striker
	}

	// Over completed? Roll everything over — the FRAME after this ball must already show
	// the cleared ticker (NV Play semantics; the overlay handles the missing final ball
	// via the score delta).
	if legal && e.LegalBalls%6 == 0 {
		// A maiden needs the WHOLE over from one bowler with nothing conceded — an over
		// shared after a mid-over change is nobody's maiden (ACS convention).
		if bowler.overConceded == 0 && len(e.overBowlers) == 1 {
			bowler.Maidens++
		}
		// Reset every bowler who bowled in the over, not just the finisher — a replaced
		// bowler's stale count would suppress a genuine maiden later.
		for w := range e.overBowlers {
			w.overConceded = 0
		}
		e.overBowlers = map[*Bowler]bool{}
		e.OverHistory = append(e.OverHistory, e.OverRuns)
		e.TokenHistory = append(e.TokenHistory,
			OverTokens{Tokens: append([]string(nil), e.OverTokensNow...), Runs: e.OverRuns})
		e.OverTokensNow, e.OverRuns = nil, 0
		e.Striker, e.NonStriker = e.NonStriker, e.Striker
	}
	return token, nil
}

// ── Frame rendering (NV Play scoreboard.template dialect) ─

func (e *Innings) OversStr() string { return fmt.Sprintf("%d.%d", e.LegalBalls/6, e.LegalBalls%6) }

// Frame renders the innings as NV Play's scoreboard.template would.
func (e *Innings) Frame(inningsNo int) map[string]string {
	ovFloat := float64(e.LegalBalls) / 6
	pre := !e.OpenersSelected
	b1, b2 := e.Striker, e.NonStriker
	var bowler *Bowler
	if !pre {
		bowler = e.BowlerForOver(e.CurrentOverNo())
	}
	runRate := "0.00"
	if ovFloat != 0 {
		runRate = pyfmt.Fixed(pyfmt.Round(float64(e.Total)/ovFloat, 2), 2)
	}
	lastOver := 0
	if len(e.OverHistory) > 0 {
		lastOver = e.OverHistory[len(e.OverHistory)-1]
	}
	f := map[string]string{
		"batting_team": e.BattingName, "bowling_team": e.BowlingName,
		"innings": strconv.Itoa(inningsNo),
		"runs":    strconv.Itoa(e.Total), "wickets": strconv.Itoa(e.Wkts), "overs": e.OversStr(),
		"partnership_runs": strconv.Itoa(e.PRuns), "partnership_balls": strconv.Itoa(e.PBalls),
		"last_ball":      strings.Join(e.OverTokensNow, " "),
		"last_over_runs": strconv.Itoa(lastOver),
		"run_rate":       runRate,
		// NB: no "over_history" key — scoreboard.template doesn't map one, so the real
		// feed never carries it; the server tolerates its absence.
		"last_wicket_howout":  e.LastWicket.HowOut,
		"last_wicket_batter":  e.LastWicket.Batter,
		"last_wicket_bowler":  e.LastWicket.Bowler,
		"last_wicket_fielder": e.LastWicket.Fielder,
		"last_wicket_type":    e.LastWicket.Type,
		"max_overs":           strconv.Itoa(e.MaxOvers),
	}
	pick := func(blank, val string) string {
		if pre {
			return blank
		}
		return val
	}
	for _, s := range []struct {
		tag    string
		b      *Batter
		strike string
	}{{"batter1", b1, "True"}, {"batter2", b2, "False"}} {
		f[s.tag+"_name"] = pick("", s.b.Name)
		f[s.tag+"_number"] = pick("", s.b.Number)
		f[s.tag+"_runs"] = pick("0", strconv.Itoa(s.b.Runs))
		f[s.tag+"_balls"] = pick("0", strconv.Itoa(s.b.Balls))
		f[s.tag+"_strike"] = pick("", s.strike)
		f[s.tag+"_fours"] = pick("0", strconv.Itoa(s.b.Fours))
		f[s.tag+"_sixes"] = pick("0", strconv.Itoa(s.b.Sixes))
		f[s.tag+"_sr"] = pick("0", pyfmt.Float(s.b.SR()))
	}
	if pre {
		f["bowler_name"], f["bowler_overs"], f["bowler_runs"] = "", "0", "0"
		f["bowler_wickets"], f["bowler_maidens"] = "0", "0"
	} else {
		f["bowler_name"] = bowler.Name
		f["bowler_overs"] = bowler.OversStr()
		f["bowler_runs"] = strconv.Itoa(bowler.Runs)
		f["bowler_wickets"] = strconv.Itoa(bowler.Wkts)
		f["bowler_maidens"] = strconv.Itoa(bowler.Maidens)
	}
	if e.Target != nil {
		need := max(0, *e.Target-e.Total)
		ballsLeft := max(0, e.MaxOvers*6-e.LegalBalls)
		f["target"] = strconv.Itoa(*e.Target)
		f["runs_required"] = strconv.Itoa(need)
		f["balls_remaining"] = strconv.Itoa(ballsLeft)
		f["req_rate"] = "0.00"
		if ballsLeft != 0 {
			f["req_rate"] = pyfmt.Fixed(float64(need)/float64(ballsLeft)*6, 2)
		}
	} else {
		f["target"], f["runs_required"], f["balls_remaining"], f["req_rate"] = "", "", "", ""
	}
	// Full-card fields — every batter who has batted (or is in), bowlers with figures
	batted := e.Batters[:min(max(e.NextBat, 2), len(e.Batters))]
	for i := 1; i <= 11; i++ {
		k := fmt.Sprintf("card_b%d_", i)
		if !pre && i <= len(batted) {
			b := batted[i-1]
			atCrease := (b == e.Striker || b == e.NonStriker) && b.HowOut == ""
			f[k+"name"] = b.Name
			f[k+"runs"] = strconv.Itoa(b.Runs)
			f[k+"balls"] = strconv.Itoa(b.Balls)
			f[k+"out"] = ""
			if !atCrease {
				f[k+"out"] = b.HowOut
			}
		} else {
			f[k+"name"], f[k+"out"], f[k+"runs"], f[k+"balls"] = "", "", "", ""
		}
	}
	var bowled []*Bowler
	for _, w := range e.AllBowlers() {
		if w.Legal != 0 || w.Runs != 0 {
			bowled = append(bowled, w)
		}
	}
	for i := 1; i <= 11; i++ {
		k := fmt.Sprintf("card_w%d_", i)
		if !pre && i <= len(bowled) {
			w := bowled[i-1]
			f[k+"name"] = w.Name
			f[k+"o"] = w.OversStr()
			f[k+"m"] = strconv.Itoa(w.Maidens)
			f[k+"r"] = strconv.Itoa(w.Runs)
			f[k+"w"] = strconv.Itoa(w.Wkts)
		} else {
			f[k+"name"], f[k+"o"], f[k+"m"], f[k+"r"], f[k+"w"] = "", "", "", "", ""
		}
	}
	return f
}

// pyRepr mimics Python's repr() of a str in error messages ('x').
func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}
