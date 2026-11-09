package server

import (
	"testing"
	"time"
)

func pcOut(batter, how, fielder string) M {
	return M{"batsman_name": batter, "runs": "4", "how_out": how, "fielder_name": fielder}
}

func TestFieldingHow(t *testing.T) {
	for in, want := range map[string]string{"ct": "ct", "CAUGHT": "ct", "Caught and bowled": "ct", "c&b": "ct",
		"st": "st", "Stumped": "st", "run out": "ro", "RUN OUT": "ro", "b": "", "lbw": "", "hit wicket": "",
		"not out": "", "": ""} {
		mustEq(t, fieldingHow(in), want, in)
	}
}

// Catches, stumpings and run outs come from the other side's innings, credited to the fielder
// PlayCricket names (a caught and bowled names the bowler). Nobody named is nobody's, and the
// other side's catches in our innings aren't ours.
func TestCareerCountsFielding(t *testing.T) {
	accs := map[string]*careerAcc{}
	careerMatch(M{"innings": []any{
		innings(11, "Home CC", 120, 4, "20", []any{pcBat("Peter Smith", 30, "ct"),
			pcOut("Karl Jones", "ct", "Bob Rival")}, nil),
		innings(21, "Rivals CC", 100, 9, "20", []any{
			pcOut("Bob Rival", "ct", "Karl Jones"), pcOut("Rob North", "ct", "Karl Jones"),
			pcOut("Dan Vine", "ct", "Peter Smith"), // caught and bowled: the bowler's catch
			pcOut("Max Reed", "st", "Sam Keeper"), pcOut("Joe Marsh", "run out", "Karl Jones"),
			pcOut("Ed Stone", "ct", ""), pcOut("Guy Bell", "ct", "Unsure"), pcOut("Ian Frost", "b", "Karl Jones"),
		}, []any{pcBowl("Peter Smith", 3, 20)}),
	}}, fixture("A", "02/06/2026", 100, 11, 200, 21), "100", func(n string) *careerAcc {
		if accs[n] == nil {
			accs[n] = &careerAcc{name: n}
		}
		return accs[n]
	})
	got := map[string][]int{}
	for n, a := range accs {
		got[n] = []int{a.ct, a.st, a.ro}
	}
	eqv(t, got, map[string][]int{"Karl Jones": {2, 0, 1}, "Peter Smith": {1, 0, 0}, "Sam Keeper": {0, 1, 0}})
	if accs["Bob Rival"] != nil || accs["Unsure"] != nil {
		t.Fatal("the other side's fielder, or nobody, got a record")
	}
	// Joined spellings add their fielding too.
	a, b := &careerAcc{ct: 3, st: 1, ro: 2}, &careerAcc{ct: 4, st: 0, ro: 1}
	a.add(b)
	eqv(t, []int{a.ct, a.st, a.ro}, []int{7, 1, 3})
	eqv(t, (&careerAcc{name: "X", ct: 7, st: 1, ro: 3}).record()["ct"], 7)
}

func TestCatchMilestoneTexts(t *testing.T) {
	rec := M{"name": "Karl Jones", "ct": 48}
	eqv(t, catchMilestones(rec, 1), []string(nil))
	eqv(t, catchMilestones(rec, 2), []string{"Karl Jones: 50 catches for the club"})
	eqv(t, catchMilestones(rec, 0), []string(nil))
}

// Live: our fielder's catch reaches a round number once, counted only in the innings we're
// fielding. While the feed's last-wicket fields still describe the last innings' final wicket
// (their fielder catching one of ours), that's never filed as a catch of this innings.
func TestLogCatchMilestone(t *testing.T) {
	sandbox(t, "")
	UpdateState(func(s M) { s["home_team"] = "Home CC" })
	careerMu.Lock()
	careerData = M{"date": time.Now().Format("2006-01-02"), "since": 2014, "lookup": careerLookup([]M{
		{"name": "Karl Jones", "ct": 49},
		{"name": "Bob Rival", "ct": 49}, // shares no name with ours: only proves theirs isn't counted
	})}
	careerMu.Unlock()
	t.Cleanup(func() {
		careerMu.Lock()
		careerData = nil
		careerMu.Unlock()
	})
	popCommands()
	frame := func(inn int, team string, score, wkts int, out, fielder, how string) {
		st := M{"innings": inn, "battingTeamName": team, "score": score, "wickets": wkts, "overs": 5.0,
			"batter1": M{"name": "A Bat", "runs": 1}, "batter2": M{"name": "B Bat", "runs": 1}, "bowler": M{"name": "C Bowl"},
			"lastWicketBatter": out, "lastWicketFielder": fielder, "lastWicketType": how}
		LogLiveFigures(st)
		LogClubMilestones(st)
	}
	milestones := func() []string {
		got, _ := popCommands()["club_milestone"].([]any)
		var out []string
		for _, g := range got {
			out = append(out, gs(g.(M), "text", ""))
		}
		return out
	}
	// We bat first; their Bob Rival catches our last batter.
	frame(1, "Home CC", 120, 10, "JONES 4", "Bob Rival", "CAUGHT")
	// We field. The fields still show that wicket, first before and then just after our
	// first wicket: neither is a catch in this innings.
	frame(2, "Rivals CC", 10, 0, "JONES 4", "Bob Rival", "CAUGHT")
	frame(2, "Rivals CC", 12, 1, "JONES 4", "Bob Rival", "CAUGHT")
	eqv(t, milestones(), []string(nil))
	// The fields catch up: Karl Jones's catch is his 50th.
	frame(2, "Rivals CC", 12, 1, "NORTH 6", "Karl Jones", "CAUGHT")
	frame(2, "Rivals CC", 15, 1, "NORTH 6", "Karl Jones", "CAUGHT")
	eqv(t, milestones(), []string{"Karl Jones: 50 catches for the club"})
	// A run out isn't a catch.
	careerMu.Lock()
	careerData["lookup"] = careerLookup([]M{{"name": "Karl Jones", "ct": 50}, {"name": "Sam Keeper", "ct": 49}})
	careerMu.Unlock()
	frame(2, "Rivals CC", 20, 2, "VINE 3", "Sam Keeper", "RUN OUT")
	eqv(t, milestones(), []string(nil))
}
