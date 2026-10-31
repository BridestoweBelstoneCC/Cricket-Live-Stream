package server

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func pcBat(name string, runs int, how string) M {
	return M{"batsman_name": name, "runs": fmt.Sprint(runs), "how_out": how}
}

func pcBowl(name string, wkts, runs int) M {
	return M{"bowler_name": name, "wickets": fmt.Sprint(wkts), "runs": fmt.Sprint(runs)}
}

// careerFake: our club 100 (1st XI 11, 2nd XI 12). Seasons 2026 (with today's match),
// 2025, an empty 2024 and 2023 (the start of the records), and a 2022 that must never be
// asked for.
func careerFake(t *testing.T) *fakePC {
	t.Helper()
	sandbox(t, "")
	UpdateState(func(s M) {
		s["playcricket_api_key"], s["home_club_id"], s["home_team"] = "k", "100", "Home CC"
	})
	today := time.Date(2026, 6, 6, 10, 0, 0, 0, time.Local)
	f := &fakePC{
		seasons: map[int][]any{
			2026: {fixture("A", "02/06/2026", 100, 11, 200, 21), fixture("TODAY", "06/06/2026", 100, 11, 200, 21)},
			2025: {fixture("B", "01/06/2025", 300, 31, 100, 12)},
			2022: {fixture("OLD", "01/06/2022", 100, 11, 200, 21)},
		},
		details: map[string]M{
			"A": {"innings": []any{
				innings(11, "Home CC 1st XI", 200, 5, "40", []any{pcBat("Peter Smith", 120, "b X"), pcBat("Karl Jones", 55, "not out"),
					pcBat("Ann Able", 0, "did not bat")}, []any{pcBowl("Bob Rival", 3, 40)}),
				innings(21, "Rivals CC 1st XI", 150, 10, "38", []any{pcBat("Bob Rival", 70, "ct")},
					[]any{pcBowl("Karl Jones", 5, 30), pcBowl("Peter Smith", 1, 20)}),
			}},
			"B": {"innings": []any{
				innings(31, "Others CC", 99, 10, "30", nil, []any{pcBowl("Karl Jones", 2, 20)}),
				innings(12, "Home CC 2nd XI", 100, 2, "20", []any{pcBat("Peter Smith", 30, "ct"), pcBat("P Smith", 12, "b")}, nil),
			}},
			"TODAY": {"innings": []any{innings(11, "Home CC 1st XI", 300, 0, "40", []any{pcBat("Karl Jones", 300, "not out")}, nil)}},
		},
	}
	savedNow, savedGet, savedSleep := careerNow, pcGetJSON, pcSleep
	careerNow, pcGetJSON, pcSleep = func() time.Time { return today }, f.get, func(time.Duration) {}
	t.Cleanup(func() {
		careerNow, pcGetJSON, pcSleep = savedNow, savedGet, savedSleep
		careerMu.Lock()
		careerData, careerBuilding = nil, false
		careerMu.Unlock()
	})
	return f
}

func TestBuildCareer(t *testing.T) {
	f := careerFake(t)
	c := buildCareer(LoadState())
	if e := gs(c, "error", ""); e != "" {
		t.Fatal(e)
	}
	// 2026 and 2025 played, 2024 and 2023 empty: the records start in 2025; 2022 never asked.
	if gi(c, "since", 0) != 2025 || gi(c, "matches", 0) != 2 || f.count("season 2022") != 0 || f.count("detail TODAY") != 0 {
		t.Fatalf("since %v, matches %v, calls %v", c["since"], c["matches"], f.calls)
	}
	jones := careerRecord(c, "JONES")
	// 55 not out (today's 300 isn't counted); 5-30 and 2-20 with the ball, bowling for us
	// on the OTHER side's innings.
	eqv(t, []any{jones["runs"], jones["inn"], jones["fifties"], jones["wkts"], jones["fivefors"]}, []any{55, 1, 1, 7, 1})
	// "Peter Smith" and "P Smith" could be one person on two accounts: neither is matched.
	if careerRecord(c, "SMITH") != nil || careerRecord(c, "Peter Smith") != nil {
		t.Fatal("an ambiguous Smith was matched")
	}
	// The opposition's players aren't in it, nor is a DNB an innings.
	if careerRecord(c, "Rival") != nil || careerRecord(c, "Able") != nil {
		t.Fatal("not our player, or didn't bat")
	}
	// A second build reads past seasons and old scorecards from the cache; this season's
	// list and a scorecard under a week old (it may still be corrected) are asked again.
	before := len(f.calls)
	buildCareer(LoadState())
	if again := f.calls[before:]; strings.Join(again, ",") != "season 2026,detail A" {
		t.Fatalf("second build asked for %v", again)
	}
}

func TestClubMilestoneTexts(t *testing.T) {
	rec := M{"name": "Peter Smith", "runs": 980, "inn": 40, "fifties": 0, "hundreds": 0, "wkts": 96, "fivefors": 2}
	eqv(t, clubMilestones(rec, 19, 0, true), []string(nil))
	eqv(t, clubMilestones(rec, 25, 0, true), []string{"Peter Smith passes 1,000 runs for the club"})
	eqv(t, clubMilestones(rec, 52, 0, true), []string{"Peter Smith passes 1,000 runs for the club", "Peter Smith: first fifty for the club"})
	rec["hundreds"], rec["runs"] = 2, 1600
	eqv(t, clubMilestones(rec, 104, 0, true), []string{"Peter Smith: 3rd hundred for the club"})
	eqv(t, clubMilestones(rec, 60, 0, true), []string(nil)) // not a first fifty: there are hundreds
	eqv(t, clubMilestones(rec, 0, 4, false), []string{"Peter Smith: 100 wickets for the club"})
	eqv(t, clubMilestones(rec, 0, 5, false), []string{"Peter Smith: 100 wickets for the club", "Peter Smith: 3rd five-for for the club"})
	for n, want := range map[int]string{1: "first", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 21: "21st", 113: "113th"} {
		if ordinal(n) != want {
			t.Errorf("ordinal(%d) = %s", n, ordinal(n))
		}
	}
	if !runMarks(2500) || runMarks(3500) || !runMarks(4000) || !wicketMarks(150) || wicketMarks(250) || !wicketMarks(300) {
		t.Error("marks")
	}
}

// Live: once per match (a moment, so a chapter), only our batters while we bat and our
// bowler while we field; the overlay gets each once with the "since" line.
func TestLogClubMilestones(t *testing.T) {
	sandbox(t, "")
	UpdateState(func(s M) { s["home_team"] = "Home CC" })
	careerMu.Lock()
	careerData = M{"date": time.Now().Format("2006-01-02"), "since": 2014, "lookup": careerLookup([]M{
		{"name": "Peter Smith", "runs": 990, "inn": 50, "fifties": 3, "hundreds": 0, "wkts": 49, "fivefors": 0},
		{"name": "Bob Rival", "runs": 990},
	})}
	careerMu.Unlock()
	t.Cleanup(func() {
		careerMu.Lock()
		careerData = nil
		careerMu.Unlock()
	})
	popCommands()
	// Bob Rival is in the lookup only to show an opposition batter is never checked.
	bat := func(team, batter string, runs int) M {
		return M{"battingTeamName": team, "batter1": M{"name": batter, "runs": runs}, "batter2": M{"name": "Newman", "runs": runs},
			"bowler": M{"name": "Smith", "wickets": 1}}
	}
	LogClubMilestones(bat("Home CC 1st XI", "Smith", 9))
	if c := popCommands()["club_milestone"]; c != nil && c != false {
		t.Fatalf("too early: %v", c)
	}
	LogClubMilestones(bat("Home CC 1st XI", "Smith", 10))
	LogClubMilestones(bat("Home CC 1st XI", "Smith", 14)) // still past it: not again
	got, _ := popCommands()["club_milestone"].([]any)
	if len(got) != 1 || gs(got[0].(M), "text", "") != "Peter Smith passes 1,000 runs for the club" ||
		gs(got[0].(M), "sub", "") != "Club records on PlayCricket since 2014" {
		t.Fatalf("commands: %v", got)
	}
	// Fielding: our bowler's 50th wicket — and never the opposition's batter.
	LogClubMilestones(bat("Rivals CC", "Rival", 500))
	got, _ = popCommands()["club_milestone"].([]any)
	if len(got) != 1 || gs(got[0].(M), "text", "") != "Peter Smith: 50 wickets for the club" {
		t.Fatalf("fielding: %v", got)
	}
	var kinds []string
	for _, m := range MatchMoments(CurrentMatchID()) {
		kinds = append(kinds, m.Kind+": "+m.Text)
	}
	eqv(t, kinds, []string{"club: Peter Smith passes 1,000 runs for the club", "club: Peter Smith: 50 wickets for the club"})
	// Switched off: nothing.
	UpdateState(func(s M) { s["graphics_club_milestones"] = false })
	careerMu.Lock()
	careerData["lookup"] = careerLookup([]M{{"name": "Karl Jones", "runs": 495}})
	careerMu.Unlock()
	LogClubMilestones(M{"battingTeamName": "Home CC", "batter1": M{"name": "Jones", "runs": 10}})
	if c, _ := popCommands()["club_milestone"].([]any); len(c) != 0 {
		t.Fatalf("switched off: %v", c)
	}
}
