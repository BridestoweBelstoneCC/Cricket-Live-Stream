package scoring

// For clubs (or match days) without NV Play/PCS Pro: a phone/tablet page of big buttons
// drives the SAME Innings engine the simulator uses, and the server renders its frames
// through the PCS parser — so the overlay, ball-by-ball DB, highlights tagging and every
// graphic work unchanged, exactly as if a scorer's feed were present.
//
// Event-sourced: every button press is an event; undo pops the last event and replays
// the rest through the (deterministic) engine, so the book after an undo is provably
// identical to never having pressed the button. The event log persists to
// manual_scoring.json after every action — a server restart or a dropped phone resumes
// mid-over.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cricketstream/internal/pyval"
)

// Event is one operator action exactly as the page sent it. Kept as a generic map so
// the saved log round-trips whatever fields the client included.
type Event map[string]any

func (ev Event) Str(key string) string {
	if v, ok := ev[key]; ok && v != nil {
		return pyval.Str(v)
	}
	return ""
}

// IntOf is Python's int(v or 0) on a JSON value; bad input errors.
func IntOf(v any) (int, error) {
	if !pyval.Truthy(v) {
		return 0, nil
	}
	return pyval.Int(v)
}

// Config is the match setup. Stored as a map for the same round-trip reason as Event.
type Config map[string]any

// Session is one manually scored match (two innings at most).
type Session struct {
	Config  Config
	Events  []Event
	Innings []*Innings
	// File is where the event log persists; "" disables saving (used for scratch replays).
	File string
}

func NewSession(config Config, file string) *Session {
	cfg := Config{}
	for k, v := range config {
		cfg[k] = v
	}
	str := func(k string) string {
		s, _ := cfg[k].(string)
		return strings.TrimSpace(s)
	}
	cfg["home"] = orDefault(str("home"), "Home CC")
	cfg["away"] = orDefault(str("away"), "Opposition CC")
	mo, err := IntOf(cfg["max_overs"])
	if err != nil || mo == 0 {
		mo = 40
	}
	cfg["max_overs"] = max(1, min(120, mo))
	if cfg["batting_first"] == "away" {
		cfg["batting_first"] = "away"
	} else {
		cfg["batting_first"] = "home"
	}
	s := &Session{Config: cfg, File: file}
	s.startInnings()
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (s *Session) cfgStr(k string) string { v, _ := s.Config[k].(string); return v }

func (s *Session) MaxOvers() int {
	switch v := s.Config["max_overs"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 40
}

// xi pads/trims a list of names to a full XI; blanks become "Home CC 4" style
// placeholders so the card and lineup graphics always have eleven rows.
func xi(names any, sideLabel string) []Player {
	var clean []string
	if list, ok := names.([]any); ok {
		for _, n := range list {
			if t := strings.TrimSpace(fmt.Sprint(n)); t != "" && n != nil {
				clean = append(clean, t)
			}
		}
	} else if list, ok := names.([]string); ok {
		for _, n := range list {
			if t := strings.TrimSpace(n); t != "" {
				clean = append(clean, t)
			}
		}
	}
	for len(clean) < 11 {
		clean = append(clean, fmt.Sprintf("%s %d", sideLabel, len(clean)+1))
	}
	out := make([]Player, 11)
	for i := range out {
		out[i] = Player{Name: clean[i]}
	}
	return out
}

func (s *Session) sides(inningsNo int) (bat, bowl []Player, batName, bowlName string) {
	home, away := s.cfgStr("home"), s.cfgStr("away")
	homeBats := (s.cfgStr("batting_first") == "home") == (inningsNo == 1)
	if homeBats {
		return xi(s.Config["home_xi"], home), xi(s.Config["away_xi"], away), home, away
	}
	return xi(s.Config["away_xi"], away), xi(s.Config["home_xi"], home), away, home
}

func (s *Session) startInnings() {
	n := len(s.Innings) + 1
	bat, bowl, batName, bowlName := s.sides(n)
	var target *int
	if n == 2 {
		t := s.Innings[0].Total + 1
		target = &t
	}
	s.Innings = append(s.Innings, NewInnings(bat, bowl, batName, bowlName, s.MaxOvers(), target, true))
}

func (s *Session) Current() *Innings { return s.Innings[len(s.Innings)-1] }
func (s *Session) InningsNo() int    { return len(s.Innings) }
func (s *Session) MatchOver() bool   { return s.InningsNo() == 2 && s.Current().Complete() }

// ── Events ───────────────────────────────────────────────

// applyEvent with lenient=true is used when REPLAYING after an edit: a corrected ball
// can make later auxiliary events impossible (a next-batter pick for someone who no
// longer arrived, a bowler set for an over that moved) — those are skipped rather than
// wedging the whole rebuild. Ball events themselves are never skipped.
func (s *Session) applyEvent(ev Event, lenient bool) error {
	kind := ev.Str("event")
	inn := s.Current()
	if lenient && (kind == "batter" || kind == "bowler" || kind == "swap_strike") {
		_ = s.applyEvent(ev, false)
		return nil
	}
	if lenient && kind == "start_innings" {
		if s.InningsNo() == 1 && !inn.Complete() {
			inn.End() // the edit un-completed innings 1 — declare it closed
		}
	}
	switch kind {
	case "ball":
		runs, err := IntOf(ev["runs"])
		if err != nil {
			return err
		}
		wt := "bowled"
		if v, ok := ev["wicket_type"]; ok {
			wt = ev.Str("wicket_type")
			if v == nil {
				wt = "None"
			}
		}
		_, err = inn.ApplyOutcome(Outcome{Kind: ev.Str("kind"), Runs: runs, WicketType: wt,
			Fielder: strings.TrimSpace(ev.Str("fielder")), OutNonStriker: pyval.Truthy(ev["out_non_striker"])})
		return err
	case "bowler":
		name := strings.TrimSpace(ev.Str("name"))
		if name == "" {
			return errors.New("bowler name required")
		}
		inn.SetBowler(name)
	case "batter":
		return inn.ChooseNextBatter(strings.TrimSpace(ev.Str("name")))
	case "swap_strike":
		inn.SwapStrike()
	case "end_innings":
		inn.End()
	case "start_innings":
		if s.InningsNo() != 1 {
			return errors.New("the second innings has already started")
		}
		if !inn.Complete() {
			return errors.New("end the first innings before starting the second")
		}
		s.startInnings()
	default:
		if _, present := ev["event"]; !present {
			return errors.New("unknown event None")
		}
		return fmt.Errorf("unknown event %s", pyRepr(kind))
	}
	return nil
}

// Apply validates and applies one operator action, records it, persists.
func (s *Session) Apply(ev Event) error {
	if err := s.applyEvent(ev, false); err != nil { // fails before anything is recorded
		return err
	}
	s.Events = append(s.Events, ev)
	s.Save()
	return nil
}

func (s *Session) Undo() (Event, error) {
	if len(s.Events) == 0 {
		return nil, errors.New("nothing to undo")
	}
	dropped := s.Events[len(s.Events)-1]
	s.Events = s.Events[:len(s.Events)-1]
	// Lenient: an earlier EditBall() may have left auxiliary events in the log that only
	// replay leniently — a strict replay would wedge mid-rebuild and leave the live
	// innings truncated. On a never-edited log the two are identical.
	_ = s.rebuild(true)
	s.Save()
	return dropped, nil
}

// rebuild replays the event log from a fresh book — the engine is deterministic, so
// this is exact. Also how a saved session is restored after a restart. If a replay
// fails (a rejected edit), s.Events is left INTACT so the caller can roll back.
func (s *Session) rebuild(lenient bool) error {
	events := s.Events
	s.Innings = nil
	s.startInnings()
	for _, ev := range events {
		if err := s.applyEvent(ev, lenient); err != nil { // was valid when first applied
			return err
		}
	}
	return nil
}

// EditBall replaces ball event `index` and replays the innings around it — the whole
// point of event sourcing. Auxiliary events invalidated by the correction are skipped
// (lenient replay); if the corrected BALL itself can't replay, everything is restored
// untouched and the error returned.
func (s *Session) EditBall(index int, newEv Event) error {
	if index < 0 || index >= len(s.Events) || s.Events[index].Str("event") != "ball" {
		return errors.New("that isn't a ball that can be edited")
	}
	old := s.Events[index]
	s.Events[index] = newEv
	if err := s.rebuild(true); err != nil {
		s.Events[index] = old
		_ = s.rebuild(true)
		return err
	}
	s.Save()
	return nil
}

// BallPos is one entry of the edit-a-ball picker.
type BallPos struct {
	Index   int    `json:"index"`
	Innings int    `json:"innings"`
	Over    int    `json:"over"`
	Ball    int    `json:"ball"`
	Token   string `json:"token"`
}

// BallList returns the most recent ball events with their positions, newest last.
// Computed by replaying a fresh book (cheap, deterministic, no side effects).
func (s *Session) BallList(limit int) []BallPos {
	temp := NewSession(s.Config, "")
	var out []BallPos
	for i, ev := range s.Events {
		if ev.Str("event") == "ball" {
			inn := temp.Current()
			innNo, over, ball := temp.InningsNo(), inn.CurrentOverNo(), len(inn.OverTokensNow)+1
			_ = temp.applyEvent(ev, true)
			inn = temp.Innings[innNo-1]
			token := "?"
			if n := len(inn.OverTokensNow); n > 0 {
				token = inn.OverTokensNow[n-1]
			} else if n := len(inn.TokenHistory); n > 0 {
				t := inn.TokenHistory[n-1].Tokens
				token = t[len(t)-1]
			}
			out = append(out, BallPos{Index: i, Innings: innNo, Over: over + 1, Ball: ball, Token: token})
		} else {
			_ = temp.applyEvent(ev, true)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func plural(n int, word string) string {
	if n != 1 {
		return word + "s"
	}
	return word
}

// ScorecardText is a plain-text scorecard for BOTH innings — everything Play-Cricket's
// manual result entry asks for (their API is read-only, so transcription is the best
// possible).
func (s *Session) ScorecardText() string {
	lines := []string{
		fmt.Sprintf("%s v %s — %s", s.cfgStr("home"), s.cfgStr("away"), time.Now().Format("02 January 2006")),
		fmt.Sprintf("%d overs per innings", s.MaxOvers()), "",
	}
	for n, inn := range s.Innings {
		lines = append(lines,
			fmt.Sprintf("═══ Innings %d: %s %d-%d (%s ov) ═══", n+1, inn.BattingName, inn.Total, inn.Wkts, inn.OversStr()),
			"",
			fmt.Sprintf("%-24s %-28s %4s %4s %3s %3s", "BATTING", "", "R", "B", "4s", "6s"))
		for _, b := range inn.Batters {
			if b.Balls == 0 && b.Runs == 0 && b.HowOut == "" && b != inn.Striker && b != inn.NonStriker {
				continue // didn't bat
			}
			how := "not out"
			if b.HowOut != "" {
				how = b.HowOut
			}
			lines = append(lines, fmt.Sprintf("%-24s %-28s %4d %4d %3d %3d",
				b.Name, strings.ToLower(how), b.Runs, b.Balls, b.Fours, b.Sixes))
		}
		lines = append(lines,
			fmt.Sprintf("%-24s %-28s %4d", "Extras", "", inn.Extras),
			fmt.Sprintf("%-24s %-28s %4d", "TOTAL", fmt.Sprintf("(%d wkts, %s ov)", inn.Wkts, inn.OversStr()), inn.Total),
			"",
			fmt.Sprintf("%-24s %6s %3s %4s %3s", "BOWLING", "O", "M", "R", "W"))
		for _, w := range inn.AllBowlers() {
			if w.Legal == 0 && w.Runs == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("%-24s %6s %3d %4d %3d", w.Name, w.OversStr(), w.Maidens, w.Runs, w.Wkts))
		}
		lines = append(lines, "")
	}
	if s.MatchOver() {
		inn1, inn2 := s.Innings[0], s.Innings[1]
		target := 0
		if inn2.Target != nil {
			target = *inn2.Target
		}
		switch {
		case inn2.Total >= target:
			margin := 10 - inn2.Wkts
			lines = append(lines, fmt.Sprintf("RESULT: %s won by %d %s", inn2.BattingName, margin, plural(margin, "wicket")))
		case inn1.Total > inn2.Total:
			diff := inn1.Total - inn2.Total
			lines = append(lines, fmt.Sprintf("RESULT: %s won by %d %s", inn1.BattingName, diff, plural(diff, "run")))
		default:
			lines = append(lines, "RESULT: Match tied")
		}
	}
	lines = append(lines, "", "(Generated by CricketStream Overlay manual scoring — enter into "+
		"Play-Cricket result entry; the API is read-only so this can't be "+
		"submitted automatically.)")
	return strings.Join(lines, "\n")
}

// ── Persistence ──────────────────────────────────────────

type saved struct {
	Config Config  `json:"config"`
	Events []Event `json:"events"`
}

// Save writes the log atomically. Scoring continues in memory on failure — losing
// restart-resilience beats losing the ball.
func (s *Session) Save() {
	if s.File == "" {
		return
	}
	events := s.Events
	if events == nil {
		events = []Event{}
	}
	data, err := json.Marshal(saved{Config: s.Config, Events: events})
	if err == nil {
		err = writeFileSync(s.File+".tmp", data)
	}
	if err == nil {
		err = os.Rename(s.File+".tmp", s.File)
	}
	if err != nil {
		log.Printf("  ⚠  Manual scoring: could not save session (%v)", err)
	}
}

func writeFileSync(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// LoadSession restores a saved session.
func LoadSession(file string) (*Session, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var d struct {
		Config Config  `json:"config"`
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	s := NewSession(d.Config, file)
	s.Events = d.Events
	// Lenient for the same reason as Undo(): a session saved after an EditBall() holds
	// events that only replay leniently, and a strict replay here would make the whole
	// saved match "unreadable" after a restart.
	if err := s.rebuild(true); err != nil {
		return nil, err
	}
	return s, nil
}
