package scoring

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func xiOf(prefix string) []Player {
	var out []Player
	for i := 1; i <= 11; i++ {
		out = append(out, Player{Number: fmt.Sprint(i), Name: fmt.Sprintf("%s%d", prefix, i)})
	}
	return out
}

var (
	xiA = xiOf("Alan Alpha")
	xiB = xiOf("Bob Beta")
)

func engine() *Innings {
	return NewInnings(xiA, xiB, "Alpha CC", "Beta CC", 20, nil, true)
}

func must(t *testing.T, e *Innings, kind string, runs ...int) {
	t.Helper()
	o := Outcome{Kind: kind, WicketType: "bowled"}
	if len(runs) > 0 {
		o.Runs = runs[0]
	}
	if _, err := e.ApplyOutcome(o); err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
}

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTheBookBalances(t *testing.T) {
	e := engine()
	for _, k := range []string{"1", "4", "dot", "6", "2"} {
		must(t, e, k)
	}
	eq(t, e.Total, 13)
	eq(t, e.LegalBalls, 5)
	eq(t, e.OverTokensNow, []string{"1", "4", ".", "6", "2"})
	// striker rotation: 1 swaps, 4 doesn't, dot doesn't, 6 doesn't, 2 doesn't
	eq(t, e.Striker.Name, "Alan Alpha2")
	eq(t, e.Striker.Runs, 12) // 4+6+2 after the single swapped them
	eq(t, e.NonStriker.Runs, 1)
}

func TestOverRolloverClearsTickerAndSwaps(t *testing.T) {
	e := engine()
	for i := 0; i < 6; i++ {
		must(t, e, "dot")
	}
	eq(t, len(e.OverTokensNow), 0) // NV Play semantics
	eq(t, e.OversStr(), "1.0")
	eq(t, e.Striker.Name, "Alan Alpha2") // ends swapped
	eq(t, e.BowlerForOver(0).Maidens, 1)
}

func TestExtrasAccounting(t *testing.T) {
	e := engine()
	must(t, e, "wide", 4)   // 5 to the total, no ball faced
	must(t, e, "noball", 2) // 3 total: 1 extra + 2 to the batter
	must(t, e, "bye", 1)
	must(t, e, "legbye", 4)
	eq(t, e.Total, 5+3+1+4)
	eq(t, e.Extras, 5+1+1+4)
	eq(t, e.LegalBalls, 2)              // wide/nb don't count
	eq(t, e.BowlerForOver(0).Runs, 5+3) // byes/legbyes not conceded
	eq(t, e.OverTokensNow, []string{"w+4", "2nb", "1b", "4lb"})
}

func TestWicketTypesAndCredit(t *testing.T) {
	e := engine()
	e.SetBowler("Bob Beta7")
	if _, err := e.ApplyOutcome(Outcome{Kind: "W", WicketType: "caught", Fielder: "Bob Beta3"}); err != nil {
		t.Fatal(err)
	}
	eq(t, e.Wkts, 1)
	eq(t, e.Batters[0].HowOut, "C BETA3 B BETA7")
	eq(t, e.GetBowler("Bob Beta7").Wkts, 1)
	eq(t, e.LastWicket.Type, "CAUGHT")
	// run out: never credited, non-striker can be the one out
	if _, err := e.ApplyOutcome(Outcome{Kind: "W", WicketType: "run out", OutNonStriker: true}); err != nil {
		t.Fatal(err)
	}
	eq(t, e.Wkts, 2)
	eq(t, e.GetBowler("Bob Beta7").Wkts, 1)
	eq(t, e.LastWicket.HowOut, "RUN OUT")
}

func TestNewBatterArrivesAndCanBeSwapped(t *testing.T) {
	e := engine()
	must(t, e, "W")
	eq(t, e.Striker.Name, "Alan Alpha3") // auto next-in
	if err := e.ChooseNextBatter("Alan Alpha7"); err != nil {
		t.Fatal(err)
	}
	eq(t, e.Striker.Name, "Alan Alpha7")
	// once they've faced a ball, no swapping
	must(t, e, "1")
	if err := e.ChooseNextBatter("Alan Alpha3"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestManualBowlerSchedule(t *testing.T) {
	e := engine()
	e.SetBowler("Bob Beta2") // not in the default attack
	eq(t, e.BowlerForOver(0).Name, "Bob Beta2")
	for i := 0; i < 6; i++ {
		must(t, e, "dot")
	}
	eq(t, e.AwaitingNewOver(), true) // over 1 needs a bowler pick
	e.SetBowler("Bob Beta9")
	eq(t, e.AwaitingNewOver(), false)
	must(t, e, "1")
	eq(t, e.GetBowler("Bob Beta2").Legal, 6)
	eq(t, e.GetBowler("Bob Beta9").Legal, 1)
}

func TestMidOverBowlerChangeAndMaidens(t *testing.T) {
	e := engine()
	e.SetBowler("Bob Beta2")
	for _, k := range []string{"1", "1", "dot"} { // Beta2 concedes 2 off 3 balls
		must(t, e, k)
	}
	e.SetBowler("Bob Beta3") // injury — Beta3 finishes the over
	for i := 0; i < 3; i++ {
		must(t, e, "dot")
	}
	// A shared over is nobody's maiden, even though the finisher conceded nothing
	eq(t, e.GetBowler("Bob Beta3").Maidens, 0)
	// When the replaced bowler returns and bowls six dots, the stale count from the
	// interrupted over must not suppress the genuine maiden
	e.SetBowler("Bob Beta2")
	for i := 0; i < 6; i++ {
		must(t, e, "dot")
	}
	eq(t, e.GetBowler("Bob Beta2").Maidens, 1)
}

func TestValidation(t *testing.T) {
	e := engine()
	for _, o := range []Outcome{{Kind: "banana"}, {Kind: "wide", Runs: 9}, {Kind: "W", WicketType: "retired to the bar"}} {
		if _, err := e.ApplyOutcome(o); err == nil {
			t.Fatalf("%+v: expected an error", o)
		}
	}
	// A rejected wicket must leave the book untouched (the Python original counted the
	// ball before validating the wicket type).
	eq(t, e.Striker.Balls, 0)
	eq(t, e.LegalBalls, 0)
	e.End()
	if _, err := e.ApplyOutcome(Outcome{Kind: "1"}); err == nil {
		t.Fatal("innings declared over: expected an error")
	}
}

// ── Session ──────────────────────────────────────────────

func session(t *testing.T) *Session {
	t.Helper()
	var a, b []any
	for _, p := range xiA {
		a = append(a, p.Name)
	}
	for _, p := range xiB {
		b = append(b, p.Name)
	}
	return NewSession(Config{"home": "Home CC", "away": "Rival CC", "max_overs": 20,
		"home_xi": a, "away_xi": b}, filepath.Join(t.TempDir(), "manual_scoring.json"))
}

func ball(kind string) Event { return Event{"event": "ball", "kind": kind} }

func apply(t *testing.T, s *Session, evs ...Event) {
	t.Helper()
	for _, ev := range evs {
		if err := s.Apply(ev); err != nil {
			t.Fatalf("%v: %v", ev, err)
		}
	}
}

var eightBalls = []Event{ball("1"), ball("4"), ball("dot"), ball("W"), ball("2"), ball("6"), ball("dot"), ball("1")}

func TestUndoIsAnExactReplay(t *testing.T) {
	a := session(t)
	apply(t, a, eightBalls...)
	for i := 0; i < 3; i++ {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	b := session(t)
	apply(t, b, eightBalls[:5]...)
	eq(t, a.Current().Frame(1), b.Current().Frame(1))
	eq(t, a.Current().Total, b.Current().Total)
}

func TestUndoCrossesTheInningsBoundary(t *testing.T) {
	s := session(t)
	apply(t, s, ball("4"), Event{"event": "end_innings"}, Event{"event": "start_innings"})
	eq(t, s.InningsNo(), 2)
	s.Undo() // un-start innings 2
	eq(t, s.InningsNo(), 1)
	s.Undo() // un-end innings 1
	eq(t, s.Current().Complete(), false)
	apply(t, s, ball("6")) // play continues
	eq(t, s.Current().Total, 10)
}

// Found live (2026-08-20) driving /scoring through a real browser: tapping "Undo last
// ball" right after confirming the next over's bowler undoes the BOWLER PICK, not the
// previous delivery — undo replays the last EVENT, and a bowler pick is itself an event.
func TestUndoRightAfterABowlerPickRevertsThePick(t *testing.T) {
	s := session(t)
	for i := 0; i < 6; i++ {
		apply(t, s, ball("1"))
	}
	eq(t, s.Current().AwaitingNewOver(), true)
	totalBefore, eventsBefore := s.Current().Total, len(s.Events)
	apply(t, s, Event{"event": "bowler", "name": "Bob Beta4"})
	eq(t, s.Current().AwaitingNewOver(), false)
	s.Undo()
	eq(t, len(s.Events), eventsBefore)
	eq(t, s.Current().AwaitingNewOver(), true)
	eq(t, s.Current().Total, totalBefore)
}

func TestPersistenceRoundTrip(t *testing.T) {
	s := session(t)
	apply(t, s, eightBalls...)
	apply(t, s, Event{"event": "bowler", "name": "Bob Beta4"})
	restored, err := LoadSession(s.File)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, restored.Current().Frame(1), s.Current().Frame(1))
	eq(t, len(restored.Events), len(s.Events))
}

func TestBadEventIsNotRecorded(t *testing.T) {
	s := session(t)
	if err := s.Apply(ball("nope")); err == nil {
		t.Fatal("expected an error")
	}
	eq(t, len(s.Events), 0)
	if err := s.Apply(Event{"event": "start_innings"}); err == nil { // innings 1 not ended
		t.Fatal("expected an error")
	}
	eq(t, len(s.Events), 0)
}

func TestEditABallInACompletedOver(t *testing.T) {
	s := session(t)
	for _, k := range []string{"1", "dot", "dot", "dot", "dot", "dot", "2"} {
		apply(t, s, ball(k))
	}
	eq(t, s.Current().Total, 3)
	if err := s.EditBall(0, ball("4")); err != nil { // that "1" was really a 4
		t.Fatal(err)
	}
	eq(t, s.Current().Total, 6)
	eq(t, s.Current().OversStr(), "1.1") // position unchanged
}

func TestEditCanRemoveAWicket(t *testing.T) {
	s := session(t)
	apply(t, s, Event{"event": "ball", "kind": "W", "wicket_type": "bowled"}, ball("4"))
	eq(t, s.Current().Wkts, 1)
	s.EditBall(0, ball("dot")) // not out — a dot
	eq(t, s.Current().Wkts, 0)
	eq(t, s.Current().Total, 4)
}

func TestEditIsAnExactReplay(t *testing.T) {
	s := session(t)
	apply(t, s, ball("1"), ball("4"), ball("dot"), ball("2"))
	s.EditBall(1, ball("6"))
	ref := session(t)
	apply(t, ref, ball("1"), ball("6"), ball("dot"), ball("2"))
	eq(t, s.Current().Frame(1), ref.Current().Frame(1))
}

func TestEditWithDownstreamBowlerEventStaysValid(t *testing.T) {
	s := session(t)
	for i := 0; i < 6; i++ {
		apply(t, s, ball("dot")) // over 1 complete
	}
	apply(t, s, Event{"event": "bowler", "name": "Bob Beta8"}, ball("4"))
	s.EditBall(2, ball("1")) // correct a dot in over 1
	eq(t, s.Current().Total, 5)
	eq(t, s.Current().BowlerForOver(1).Name, "Bob Beta8")
}

func TestEditToInvalidBallRollsBack(t *testing.T) {
	s := session(t)
	apply(t, s, ball("1"), ball("4"), ball("2"))
	before := s.Current().Frame(1)
	if err := s.EditBall(1, ball("banana")); err == nil {
		t.Fatal("expected an error")
	}
	eq(t, s.Current().Frame(1), before) // restored exactly
	var kinds []string
	for _, ev := range s.Events {
		kinds = append(kinds, ev.Str("kind"))
	}
	eq(t, kinds, []string{"1", "4", "2"})
	// a rejected edit must leave the session fully usable
	apply(t, s, ball("6"))
	eq(t, s.Current().Total, 13)
}

// A wicket, a "someone else came in" pick, then the wicket is edited into runs: the
// 'batter' event is now lenient-only. Undo must replay leniently too, or it wedges
// mid-rebuild and leaves the live innings truncated.
func TestUndoAfterEditThatOrphanedABatterPick(t *testing.T) {
	s := session(t)
	apply(t, s, Event{"event": "ball", "kind": "W", "wicket_type": "bowled"},
		Event{"event": "batter", "name": "Alan Alpha7"}, ball("1"))
	if err := s.EditBall(0, ball("4")); err != nil {
		t.Fatal(err)
	}
	apply(t, s, ball("2"))
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	eq(t, s.Current().Total, 5)
	eq(t, s.Current().Wkts, 0)
}

func TestRestoreAfterEditThatOrphanedABatterPick(t *testing.T) {
	s := session(t)
	apply(t, s, Event{"event": "ball", "kind": "W", "wicket_type": "bowled"},
		Event{"event": "batter", "name": "Alan Alpha7"}, ball("1"))
	s.EditBall(0, ball("4"))
	restored, err := LoadSession(s.File)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, restored.Current().Frame(1), s.Current().Frame(1))
	eq(t, restored.Current().Wkts, 0)
}

func TestEditRejectsNonBallIndex(t *testing.T) {
	s := session(t)
	apply(t, s, ball("1"), Event{"event": "swap_strike"})
	if s.EditBall(1, ball("4")) == nil {
		t.Fatal("index 1 is a swap, not a ball")
	}
	if s.EditBall(9, ball("4")) == nil {
		t.Fatal("out of range")
	}
}

func TestBallListPositions(t *testing.T) {
	s := session(t)
	for _, k := range []string{"1", "4", "dot", "2", "6", "dot", "3"} {
		apply(t, s, ball(k))
	}
	bl := s.BallList(30)
	eq(t, len(bl), 7)
	eq(t, bl[0], BallPos{Index: 0, Innings: 1, Over: 1, Ball: 1, Token: "1"})
	last := bl[len(bl)-1]
	eq(t, [3]any{last.Over, last.Ball, last.Token}, [3]any{2, 1, "3"})
}

func TestScorecardTextHasBothDisciplinesAndTheResult(t *testing.T) {
	s := session(t)
	apply(t, s, ball("4"), Event{"event": "ball", "kind": "W", "wicket_type": "bowled"},
		Event{"event": "end_innings"}, Event{"event": "start_innings"}, ball("6"))
	card := s.ScorecardText()
	for _, want := range []string{"Home CC v Rival CC", "BATTING", "BOWLING", "Extras", "Alan Alpha1", "RESULT:", "won by"} {
		if !strings.Contains(card, want) {
			t.Fatalf("scorecard is missing %q:\n%s", want, card)
		}
	}
}
