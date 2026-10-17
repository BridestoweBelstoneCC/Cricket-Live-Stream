package server

// Pure parsing logic: ball tokens, PCS Pro JSON, widget JSON, names, ids
// (tests/test_parsing.py).

import (
	"fmt"
	"testing"

	"cricketstream/internal/obs"
)

// eqv compares by printed value, so an int and a float64 of the same number agree.
func eqv(t *testing.T, got, want any, what ...any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v: got %v, want %v", fmt.Sprint(what...), got, want)
	}
}

func TestClassifyBallDots(t *testing.T) {
	for _, tok := range []string{"", ".", "0", "xyz"} {
		b := ClassifyBall(tok)
		if b.Outcome != "dot" || b.Runs != 0 || !b.Legal {
			t.Fatalf("%q: %+v", tok, b)
		}
	}
}

func TestClassifyBallRuns(t *testing.T) {
	for _, r := range []int{1, 2, 3, 4, 6} {
		b := ClassifyBall(fmt.Sprint(r))
		if b.Runs != r || b.Outcome != fmt.Sprint(r) || !b.Legal {
			t.Fatalf("%d: %+v", r, b)
		}
	}
}

func TestWicketIsCapitalWOnly(t *testing.T) {
	b := ClassifyBall("W")
	if !b.Wicket || !b.Legal || b.Runs != 0 {
		t.Fatalf("%+v", b)
	}
	// lowercase w is a wide, never a wicket (NV Play codes are case-sensitive)
	b = ClassifyBall("w")
	if b.Wicket || b.Extra != "wide" || b.Runs != 1 || b.Legal {
		t.Fatalf("%+v", b)
	}
}

func TestWideWithRuns(t *testing.T) {
	b := ClassifyBall("w+4")
	if b.Extra != "wide" || b.Runs != 5 || b.Legal { // wide + 4 ran/boundary
		t.Fatalf("%+v", b)
	}
}

func TestNoball(t *testing.T) {
	b := ClassifyBall("1nb")
	if b.Runs != 2 || b.Extra != "noball" || b.Legal {
		t.Fatalf("%+v", b)
	}
	mustEq(t, ClassifyBall("2nb+4").Runs, 7) // 1 (nb) + 2 (pre) + 4 (post)
	mustEq(t, ClassifyBall("nb").Runs, 1)
}

func TestByesAndLegbyes(t *testing.T) {
	b := ClassifyBall("4lb")
	if b.Extra != "legbye" || b.Runs != 4 || !b.Legal {
		t.Fatalf("%+v", b)
	}
	b = ClassifyBall("2b")
	if b.Extra != "bye" || b.Runs != 2 || !b.Legal {
		t.Fatalf("%+v", b)
	}
	mustEq(t, ClassifyBall("1nb").Extra, "noball") // not swallowed by the trailing-'b' rule
}

func TestParseTickerSplitAndPlaceholder(t *testing.T) {
	mustEq(t, len(parseTicker("")), 0)
	mustEq(t, len(parseTicker("{{LastBall}}")), 0)
	balls := parseTicker("1 4 . W w+2")
	mustEq(t, len(balls), 5)
	var runs []int
	for _, b := range balls {
		runs = append(runs, b.Runs)
	}
	eqv(t, runs, []int{1, 4, 0, 0, 3})
	mustEq(t, balls[3].Wicket, true)
}

func TestParseTickerMiddleDot(t *testing.T) {
	var runs []int
	for _, b := range parseTicker("1·4·6") {
		runs = append(runs, b.Runs)
	}
	eqv(t, runs, []int{1, 4, 6})
}

func pcsBase(kv ...string) M {
	d := M{"batting_team": "Home CC", "bowling_team": "Opposition CC",
		"runs": "100", "wickets": "2", "overs": "20.0",
		"batter1_name": "SMITH", "batter1_runs": "40", "batter1_balls": "50",
		"batter2_name": "JONES", "batter2_runs": "30", "batter2_balls": "35",
		"bowler_name": "HARRISON", "bowler_overs": "4", "bowler_runs": "20",
		"bowler_wickets": "1", "last_ball": "1 4 ."}
	for i := 0; i+1 < len(kv); i += 2 {
		d[kv[i]] = kv[i+1]
	}
	return d
}

func withLatch(t *testing.T) {
	SetInningsLatch(1)
	t.Cleanup(func() { SetInningsLatch(1) })
}

func TestParsePCSBasicFields(t *testing.T) {
	withLatch(t)
	st := ParsePCSJSON(pcsBase())
	eqv(t, st["score"], 100)
	eqv(t, st["wickets"], 2)
	eqv(t, st["overs"], 20)
	eqv(t, gm(st, "batter1")["name"], "SMITH")
	eqv(t, st["innings"], 1)
}

func TestParsePCSPlaceholderAndBlankFallBack(t *testing.T) {
	withLatch(t)
	st := ParsePCSJSON(pcsBase("batter1_name", "{{Batter1Name}}", "batter2_name", ""))
	eqv(t, gm(st, "batter1")["name"], "—")
	eqv(t, gm(st, "batter2")["name"], "—")
}

func TestParsePCSBattingTeamFallbacks(t *testing.T) {
	withLatch(t)
	// Blank falls back to the "Batting" placeholder BY DESIGN (NV Play renders the
	// template with empty fields before the match is configured)...
	eqv(t, ParsePCSJSON(pcsBase("batting_team", ""))["battingTeamName"], "Batting")
	// ...and only an explicit em dash means "no state at all"
	if ParsePCSJSON(pcsBase("batting_team", "—")) != nil {
		t.Fatal("em dash should mean no state")
	}
}

func TestParsePCSStrikerDetection(t *testing.T) {
	withLatch(t)
	st := ParsePCSJSON(pcsBase("batter1_strike", "False", "batter2_strike", "True"))
	eqv(t, gm(st, "batter1")["onStrike"], false)
	eqv(t, gm(st, "batter2")["onStrike"], true)
	eqv(t, gm(ParsePCSJSON(pcsBase()), "batter1")["onStrike"], true) // unknown → batter1
}

func TestInningsLatchHoldsThroughWinningRuns(t *testing.T) {
	withLatch(t)
	eqv(t, ParsePCSJSON(pcsBase("runs_required", "50"))["innings"], 2)
	// target reached: runs_required drops to 0 but the latch must hold
	eqv(t, ParsePCSJSON(pcsBase("runs_required", "0"))["innings"], 2)
	// a genuinely fresh innings (near-empty card) resets the latch
	eqv(t, ParsePCSJSON(pcsBase("runs", "0", "wickets", "0", "overs", "0.0", "runs_required", "0"))["innings"], 1)
}

func TestRunsRequiredFallsBackToTarget(t *testing.T) {
	withLatch(t)
	eqv(t, ParsePCSJSON(pcsBase("target", "180"))["runsRequired"], 80)
	eqv(t, ParsePCSJSON(pcsBase("target", "180", "runs_required", "79"))["runsRequired"], 79)
}

func TestFullCardParsing(t *testing.T) {
	withLatch(t)
	st := ParsePCSJSON(pcsBase("card_b1_name", "A Opener", "card_b1_runs", "12", "card_b1_balls", "20",
		"card_b1_out", "b Jones", "card_b2_name", "{{unset}}",
		"card_w1_name", "B Bowler", "card_w1_o", "8.0", "card_w1_m", "1", "card_w1_r", "30", "card_w1_w", "2"))
	card := gm(st, "card")
	bats, bowls := listOfMaps(card["batters"]), listOfMaps(card["bowlers"])
	mustEq(t, len(bats), 1)
	eqv(t, bats[0]["name"], "A Opener")
	mustEq(t, len(bowls), 1)
	eqv(t, bowls[0]["w"], 2)
}

func widget(home, away, battedFirst, homeTeamID string) M {
	return M{"matches": []any{M{"match_id": "123",
		"home_club_name": "Home", "home_team_name": "1st XI",
		"away_club_name": "Away", "away_team_name": "1st XI",
		"home_team_score": home, "away_team_score": away,
		"batted_first": battedFirst, "home_team_id": homeTeamID}}}
}

func TestWidgetFirstInningsHomeBatting(t *testing.T) {
	st, pc := parseWidgetJSONScore(widget("120-4 (25.0)", "Yet to bat", "", "77"), 0)
	mustEq(t, pc, 123)
	eqv(t, st["innings"], 1)
	eqv(t, st["score"], 120)
	eqv(t, st["wickets"], 4)
	eqv(t, st["overs"], 25)
	if gs(st, "battingTeamName", "")[:4] != "Home" {
		t.Fatal(st["battingTeamName"])
	}
}

func TestWidgetSecondInningsAfterAllOut(t *testing.T) {
	st, _ := parseWidgetJSONScore(widget("200-10 (45.0)", "50-1 (10.0)", "77", "77"), 0)
	eqv(t, st["innings"], 2)
	if gs(st, "battingTeamName", "")[:4] != "Away" {
		t.Fatal(st["battingTeamName"])
	}
	eqv(t, st["score"], 50)
}

func TestWidgetBothBattedFirstNotAllOutDefaultsInnings1(t *testing.T) {
	st, _ := parseWidgetJSONScore(widget("200-8 (45.0)", "50-1 (10.0)", "77", "77"), 0)
	eqv(t, st["innings"], 1)
}

func TestWidgetNoMatches(t *testing.T) {
	if st, _ := parseWidgetJSONScore(M{"matches": []any{}}, 0); st != nil {
		t.Fatal(st)
	}
}

func TestNormNameKey(t *testing.T) {
	for _, raw := range []string{"P SMITH", "p.smith", "P_Smith", "psmith"} {
		mustEq(t, normNameKey(raw), "psmith", raw)
	}
}

func TestNameKeysTiers(t *testing.T) {
	mustEq(t, nameKeys("K J JONES"), keys{"kjjones", "kjones", "jones"})
	mustEq(t, nameKeys("Kevin Jones").initsur, "kjones")
	mustEq(t, nameKeys("WALKER"), keys{"walker", "walker", "walker"})
	mustEq(t, nameKeys(""), keys{})
}

func TestTeamKeyYouthAndOrdinals(t *testing.T) {
	for _, n := range []string{"U11", "u-13", "Under 15", "Under-9s Colts", "Junior Girls", "Kwik Cricket"} {
		mustEq(t, teamKey(n), "youth", n)
	}
	mustEq(t, teamKey("1st XI"), "1st")
	mustEq(t, teamKey("2nd XI"), "2nd")
	mustEq(t, teamKey("Third Team"), "3rd")
	mustEq(t, teamKey("Sunday Friendly"), "")
}

func TestShortName(t *testing.T) {
	mustEq(t, shortName("Peter Smith", false), "P SMITH")
	mustEq(t, shortName("Smith", false), "SMITH")
	// youth form: first name + surname initial, only when a full first name exists
	mustEq(t, shortName("Jack Smith", true), "Jack S")
	mustEq(t, shortName("J Smith", true), "J SMITH")
}

func TestResolvePlayerRosterCrosscheck(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"roster": M{"21": "Peter Smith"}})
	check := func(name, num, want string, wantBy bool) {
		t.Helper()
		got, by := resolvePlayer(name, num)
		if got != want || by != wantBy {
			t.Fatalf("resolvePlayer(%q, %q) = %q, %v", name, num, got, by)
		}
	}
	check("SMITH", "21", "Peter Smith", true) // number → roster, surname agrees
	check("JONES", "21", "JONES", false)      // an away player wearing 21
	check("SMITH", "", "SMITH", false)
}

func TestExtractPCMatchIDForms(t *testing.T) {
	mustEq(t, extractPCMatchID("1234567"), "1234567")
	mustEq(t, extractPCMatchID("https://play-cricket.com/website/results/1234567"), "1234567")
	mustEq(t, extractPCMatchID("https://x.com/scorecard/1234567?tab=batting"), "1234567")
	mustEq(t, extractPCMatchID(""), "")
	mustEq(t, extractPCMatchID("no digits here"), "")
}

func TestBitrateRecommendationTiers(t *testing.T) {
	r := obs.RecommendStreamSettings(1.0) // 750 safe
	if r.Resolution != "720p" || r.FPS != 30 || r.BitrateKbps < 800 {
		t.Fatalf("%+v", r)
	}
	mustEq(t, obs.RecommendStreamSettings(3.0).Resolution, "720p")
	mustEq(t, obs.RecommendStreamSettings(5.0).Resolution, "1080p")
	r = obs.RecommendStreamSettings(50.0)
	if r.Resolution != "1080p" || r.BitrateKbps > 6000 {
		t.Fatalf("%+v", r)
	}
}

func TestGroundCoordsFallback(t *testing.T) {
	sandbox(t, "")
	check := func(kv M, lat, lon float64) {
		t.Helper()
		UpdateState(func(s M) {
			delete(s, "weather_lat")
			delete(s, "weather_lon")
			for k, v := range kv {
				s[k] = v
			}
		})
		a, b := groundCoords()
		if a != lat || b != lon {
			t.Fatalf("%v → %v,%v", kv, a, b)
		}
	}
	check(M{}, groundLat, groundLon)
	check(M{"weather_lat": "51.5", "weather_lon": "-0.1"}, 51.5, -0.1)
	check(M{"weather_lat": "junk", "weather_lon": "-0.1"}, groundLat, groundLon)
}
