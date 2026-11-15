package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSideKind(t *testing.T) {
	for want, f := range map[string]M{
		sideMen:    {"home_club_id": "100", "home_team_name": "1st XI", "competition_name": "Devon League B"},
		sideJunior: {"away_club_id": "100", "away_team_name": "Under 13", "competition_name": "Devon Junior League"},
		sideLadies: {"home_club_id": "100", "home_team_name": "Women's XI", "competition_name": "Softball"},
		sideGirls:  {"home_club_id": "100", "home_team_name": "U15 Girls", "competition_name": ""},
	} {
		if got := sideKind(f, "100"); got != want {
			t.Errorf("%v: %q, want %q", f, got, want)
		}
	}
	// Only OUR team's name decides it: playing a ladies' side's club's men isn't ladies' cricket.
	f := M{"home_club_id": "100", "home_team_name": "2nd XI", "away_club_id": "200", "away_team_name": "Ladies"}
	if got := sideKind(f, "100"); got != sideMen {
		t.Errorf("the opposition's team name decided it: %q", got)
	}
}

func TestClubStatsTable(t *testing.T) {
	careerFake(t)
	UpdateState(func(s M) { s["former_club_ids"] = "900" })
	named := func(m M, team, comp, club string) M {
		m["home_team_name"], m["competition_name"], m["home_club_name"] = team, comp, club
		return m
	}
	lists := map[string][]any{
		"100-2026": {
			named(fixture("MEN", "02/06/2026", 100, 11, 200, 21), "1st XI", "Devon League", "Bridestowe & Belstone CC"),
			named(fixture("U13", "03/06/2026", 100, 13, 200, 23), "Under 13", "Junior League", "Bridestowe & Belstone CC"),
			named(fixture("LADIES", "04/06/2026", 100, 14, 200, 24), "Women's XI", "Softball", "Bridestowe & Belstone CC"),
		},
		"900-2024": {named(fixture("WAS", "01/06/2024", 900, 91, 200, 21), "Belstone 1st XI", "Devon League", "Belstone CC")},
	}
	details := map[string]M{
		"MEN": {"innings": []any{
			innings(11, "1st XI", 200, 5, "40", []any{pcBat("Karl Jones", 80, "not out"), pcBat("Sam Lee", 4, "b")}, nil),
			innings(21, "Rivals", 150, 10, "38", []any{M{"batsman_name": "Bob Rival", "runs": "9", "how_out": "ct", "fielder_name": "Karl Jones"}},
				[]any{M{"bowler_name": "Karl Jones", "wickets": "3", "runs": "25", "overs": "8.2"}}),
		}, "players": []any{M{"home_team": []any{M{"player_name": "Karl Jones"}, M{"player_name": "Sam Lee"}, M{"player_name": "Tom Field"}}}}},
		"U13": {"innings": []any{
			innings(13, "Under 13", 90, 3, "20", []any{pcBat("Amy Young", 31, "b"), pcBat("Sam Lee", 12, "retired hurt")}, nil),
		}},
		"LADIES": {"innings": []any{
			innings(14, "Women's XI", 120, 2, "20", []any{pcBat("Amy Young", 50, "ct")}, nil),
			innings(24, "Rivals Women", 100, 5, "20", nil, []any{M{"bowler_name": "Amy Young", "wickets": "3", "runs": "12", "overs": "4"}}),
		}},
		"WAS": {"innings": []any{
			innings(91, "Belstone 1st XI", 100, 2, "20", []any{pcBat("K Jones", 101, "ct")}, nil),
			innings(21, "Rivals", 50, 10, "20", nil, []any{M{"bowler_name": "Karl Jones", "wickets": "3", "runs": "20", "overs": "6"}}),
		}},
	}
	pcGetJSON = func(u string, _ time.Duration) (M, error) {
		p, _ := url.Parse(u)
		q := p.Query()
		if strings.HasSuffix(p.Path, "/matches.json") {
			return M{"matches": lists[q.Get("site_id")+"-"+q.Get("season")]}, nil
		}
		return M{"match_details": []any{details[q.Get("match_id")]}}, nil
	}
	c := buildCareer(LoadState())
	if e := gs(c, "error", ""); e != "" {
		t.Fatal(e)
	}
	careerMu.Lock()
	careerData = c
	careerMu.Unlock()
	st := ClubStats()
	// Through JSON, as the screen gets it.
	b, _ := json.Marshal(st)
	var got struct {
		Ready  bool
		Fields []string
		Clubs  []struct {
			ID, Name string
			Former   bool
		}
		Players []struct {
			Name string
			B    map[string][]int
		}
		Games map[string]int
	}
	if err := json.Unmarshal(b, &got); err != nil || !got.Ready {
		t.Fatalf("%v %s", err, b)
	}
	field := map[string]int{}
	for i, f := range got.Fields {
		field[f] = i
	}
	row := func(name, bucket string) []int {
		for _, p := range got.Players {
			if p.Name == name {
				return p.B[bucket]
			}
		}
		return nil
	}
	f := func(r []int, k string) int { return r[field[k]] }

	eqv(t, got.Games, map[string]int{"100:m": 1, "100:j": 1, "100:l": 1, "900:m": 1})
	if len(got.Clubs) != 2 || got.Clubs[0].ID != "100" || got.Clubs[0].Former || got.Clubs[1].Name != "Belstone CC" || !got.Clubs[1].Former {
		t.Fatalf("clubs %+v", got.Clubs)
	}
	// Karl Jones: 80* and 3-25 off 8.2 for the 1st XI, a catch; "K Jones" for Belstone is him
	// too (the milestones join them), with 101 and 3-20.
	men := row("Karl Jones", "100:m")
	eqv(t, []int{f(men, "apps"), f(men, "inn"), f(men, "no"), f(men, "runs"), f(men, "hs"), f(men, "hs_no"), f(men, "balls"),
		f(men, "b_runs"), f(men, "wkts"), f(men, "best_w"), f(men, "best_r"), f(men, "ct"), f(men, "fifties")},
		[]int{1, 1, 1, 80, 80, 1, 50, 25, 3, 3, 25, 1, 1})
	old := row("Karl Jones", "900:m")
	eqv(t, []int{f(old, "apps"), f(old, "runs"), f(old, "hundreds"), f(old, "best_w"), f(old, "best_r")}, []int{1, 101, 1, 3, 20})
	// Amy Young: junior and ladies' sides kept apart, full name in both.
	eqv(t, f(row("Amy Young", "100:j"), "runs"), 31)
	ladies := row("Amy Young", "100:l")
	eqv(t, []int{f(ladies, "runs"), f(ladies, "wkts"), f(ladies, "balls")}, []int{50, 3, 24})
	// Retired hurt is not out; a game on the team sheet alone still counts as a game.
	eqv(t, f(row("Sam Lee", "100:j"), "no"), 1)
	eqv(t, f(row("Sam Lee", "100:m"), "apps"), 1)
	// A team list never starts a record (career.go), so a name only ever on one isn't a row.
	if row("Tom Field", "100:m") != nil {
		t.Fatal("a name only on a team list got a row")
	}
	// The opposition never appears.
	if row("Bob Rival", "100:m") != nil {
		t.Fatal("an opposition player is in the table")
	}
	// No best bowling reads as -1, not 0-0.
	eqv(t, f(row("Sam Lee", "100:m"), "best_w"), -1)
}

func TestClubStatsWhileBuilding(t *testing.T) {
	careerFake(t)
	careerMu.Lock()
	careerBuilding = true // a build is under way
	careerMu.Unlock()
	if st := ClubStats(); st["ready"] != false {
		t.Fatalf("%v", st)
	}
}
