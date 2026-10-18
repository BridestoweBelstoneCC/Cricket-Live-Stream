package server

// The post-match result card's facts (tests/test_result_card.py, the server half; the
// renderer is tested in internal/resultcard). The PlayCricket cases are shaped on real
// 2026 scorecards the first version got wrong: a DLS win from the lower score, a
// conceded match with empty innings, and pairs cricket won by runs batting second.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type batRow struct{ name, howOut, runs, balls any }
type bowlRow struct{ name, overs, runs, wkts string }

func pcInn(teamID, name, runs, wkts, overs string, bats []batRow, bowls []bowlRow) M {
	b := []any{}
	for _, r := range bats {
		b = append(b, M{"batsman_name": r.name, "how_out": r.howOut, "runs": r.runs, "balls": r.balls})
	}
	w := []any{}
	for _, r := range bowls {
		w = append(w, M{"bowler_name": r.name, "overs": r.overs, "runs": r.runs, "wickets": r.wkts})
	}
	return M{"team_batting_id": teamID, "team_batting_name": name, "runs": runs, "wickets": wkts,
		"overs": overs, "bat": b, "bowl": w}
}

func pcMatch(innings []M, result, applied, desc, gameType string) M {
	if gameType == "" {
		gameType = "Standard"
	}
	inn := []any{}
	for _, i := range innings {
		inn = append(inn, i)
	}
	return M{"match_details": []any{M{
		"home_team_id": "1", "home_club_name": "Home and District CC", "home_team_name": "1st XI",
		"home_club_id": "111", "away_team_id": "2", "away_club_name": "Rivals CC",
		"away_team_name": "1st XI", "away_club_id": "222", "result": result,
		"result_applied_to": applied, "result_description": desc, "game_type": gameType,
		"league_name": "Devon Cricket League", "competition_name": "A Division",
		"match_date": "25/07/2026", "ground_name": "The Ground", "innings": inn}}}
}

var (
	usBat  = []batRow{{"Pat Smith", "ct", "78", "96"}, {"Al Jones", "not out", "20", "15"}, {"Ed Dnb", "did not bat", "", ""}}
	usBowl = []bowlRow{{"Sam Quick", "10", "25", "3"}, {"Tom Slow", "8", "40", "1"}}
)

func pcFactsSandbox(t *testing.T) {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"home_team": "Home and District CC", "home_club_id": "111", "playcricket_api_key": "k"})
}

func pcFacts(t *testing.T, md M) M {
	t.Helper()
	withPCGet(t, func(string, time.Duration) (M, error) { return md, nil })
	return BuildMatchFactsFromPC("7303595", "")
}

func sides(f M) []M { return listOfMaps(f["sides"]) }

func standardWin() M {
	return pcMatch([]M{pcInn("1", "x", "231", "7", "40", usBat, nil), pcInn("2", "y", "185", "10", "36.2", nil, usBowl)}, "W", "1", "", "")
}

func TestCardWinByRunsWithBothStars(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, standardWin())
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "by 46 runs"})
	us, them := sides(f)[0], sides(f)[1]
	if !(us["won"] == true && us["ours"] == true && them["won"] != true) {
		t.Fatal(us, them)
	}
	eqv(t, []any{us["club"], us["club_id"], us["score"]}, []any{"Home & District CC", "111", "231-7"})
	eqv(t, []any{them["score"], them["overs"]}, []any{"185", "All out · 36.2 overs"})
	var stars []any
	for _, s := range listOfMaps(f["stars"]) {
		stars = append(stars, []any{s["big"], s["small"], s["name"]})
	}
	eqv(t, stars, []any{[]any{"78", "(96)", "P SMITH"}, []any{"3-25", "10 ov", "S QUICK"}})
	eqv(t, f["competition"], "Devon League · A Division")
	eqv(t, f["date"], "25 Jul 2026")
}

func TestCardDLSWinFromLowerScore(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("2", "y", "200", "5", "40", nil, nil), pcInn("1", "x", "150", "7", "36", nil, nil)},
		"W", "1", "Home and District CC - 1st XI - Win on DLS/Run Rate", ""))
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "on DLS"})
	eqv(t, sides(f)[1]["won"], true)
}

func TestCardLossByWickets(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("1", "x", "150", "10", "35", nil, nil), pcInn("2", "y", "151", "3", "30", nil, nil)}, "W", "2", "", ""))
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"loss", "by 7 wickets"})
}

func TestCardConcededMatchHasNoFakeScores(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("1", "x", "", "", "", nil, nil), pcInn("2", "y", "", "", "", nil, nil)}, "CON", "", "Rivals CC - 1st XI - Conceded", ""))
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "by concession"})
	eqv(t, []any{sides(f)[0]["score"], sides(f)[1]["score"]}, []any{"", ""})
	mustEq(t, len(listOfMaps(f["stars"])), 0)
}

func TestCardPairsWonByRunsBlankHowOutStillBatted(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("2", "y", "66", "4", "16.0", nil, nil),
		pcInn("1", "x", "108", "5", "16.0", []batRow{{"Kim Mac", nil, "26", "16"}}, nil)}, "W", "1", "", "Pairs"))
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "by 42 runs"})
	eqv(t, listOfMaps(f["stars"])[0]["big"], "26")
}

func TestCardTie(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("1", "x", "150", "6", "40", nil, nil), pcInn("2", "y", "150", "9", "40", nil, nil)}, "T", "", "", ""))
	eqv(t, f["outcome"], "tie")
	for _, s := range sides(f) {
		if s["won"] == true {
			t.Fatal("a winner in a tie")
		}
	}
}

func TestCardCaptionFieldsStillPresent(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, standardWin())
	contains(t, gs(f, "result", ""), "WIN BY 46 RUNS")
	eqv(t, f["team2_score"], "185 (All out · 36.2 overs)")
	eqv(t, f["performer1"], "P SMITH 78 (96)")
}

func TestCardResultLineUsesPanelsAbbreviation(t *testing.T) {
	// It read config.ini's "abbreviation" key (never a state key), so every result line
	// said the first five letters of the club name instead.
	pcFactsSandbox(t)
	setState(t, M{"home_abbrev": "HDCC"})
	eqv(t, pcFacts(t, standardWin())["result"], "HDCC WIN BY 46 RUNS")
}

func TestCardPlaceholderAbbreviationFallsBackToClubName(t *testing.T) {
	pcFactsSandbox(t)
	setState(t, M{"home_team": "Hometown and District CC", "home_abbrev": "HOME"})
	eqv(t, pcFacts(t, standardWin())["result"], "HOMET WIN BY 46 RUNS")
}

// ── The streamed match without a published scorecard: the scorer's own figures ──

func liveFactsSandbox(t *testing.T) {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"home_team": "Home CC", "away_team": "Rivals CC", "home_club_id": "111",
		"away_club_id": "222", "max_overs": 50, "match_url": "", "pc_match_id": ""})
}

func logInnings(n int, team string, score, wkts int, overs float64, remaining int, batter bt, bowler bw) {
	LogLiveFigures(M{"innings": n, "score": score, "wickets": wkts, "overs": overs,
		"battingTeamName": team, "ballsRemaining": remaining,
		"batter1": M{"name": batter.name, "runs": batter.runs, "balls": batter.balls},
		"batter2": M{"name": "Other", "runs": 0, "balls": 0},
		"bowler":  M{"name": bowler.name, "overs": bowler.overs, "runs": bowler.runs, "wickets": bowler.wkts}})
}

func TestCardFinishedChaseIsAResultNoAIInvolved(t *testing.T) {
	liveFactsSandbox(t)
	calls := noModel(t)
	logInnings(1, "RIVALS CC 1ST", 120, 8, 20.0, 0, smith0, bw{"Our Bowler", "4", 20, 3})
	logInnings(2, "HOME CC 1ST", 121, 3, 15.0, 0, bt{"Pat Smith", 64, 40}, quick0)
	f := GenerateSocialGraphicFacts()
	mustEq(t, *calls, 0)
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "by 7 wickets"})
	eqv(t, []any{sides(f)[0]["club_id"], sides(f)[1]["club_id"]}, []any{"222", "111"})
	var stars []any
	for _, s := range listOfMaps(f["stars"]) {
		stars = append(stars, []any{s["big"], s["name"]})
	}
	eqv(t, stars, []any{[]any{"64*", "P SMITH"}, []any{"3-20", "O BOWLER"}})
}

func TestCardDefendedTotalEndsOnScorersOversLimit(t *testing.T) {
	// A 40-over game with the panel left on 50: ballsRemaining says 40.
	liveFactsSandbox(t)
	logInnings(1, "HOME CC 1ST", 200, 6, 40.0, 0, smith0, quick0)
	logInnings(2, "RIVALS CC 1ST", 100, 3, 20.0, 120, smith0, quick0)
	logInnings(2, "RIVALS CC 1ST", 180, 7, 40.0, 0, smith0, quick0)
	f := GenerateSocialGraphicFacts()
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "by 20 runs"})
}

func TestCardSecondInningsStillGoingClaimsNothing(t *testing.T) {
	liveFactsSandbox(t)
	logInnings(1, "RIVALS CC 1ST", 120, 8, 20.0, 0, smith0, quick0)
	logInnings(2, "HOME CC 1ST", 60, 3, 9.0, 66, smith0, quick0)
	f := GenerateSocialGraphicFacts()
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"live", ""})
}

func TestCardUnrecognisedTeamNamesNeverClaimAWin(t *testing.T) {
	liveFactsSandbox(t)
	logInnings(1, "SOME XI", 120, 8, 20.0, 0, smith0, quick0)
	logInnings(2, "OTHER 1ST XI", 121, 3, 15.0, 0, smith0, quick0)
	f := GenerateSocialGraphicFacts()
	eqv(t, f["outcome"], "")
	eqv(t, []any{sides(f)[0]["club"], sides(f)[1]["club"]}, []any{"Some XI", "Other 1st XI"})
	eqv(t, sides(f)[1]["won"], true)
}

func TestCardLinkedMatchMidChaseOnPlayCricketIsNotAResult(t *testing.T) {
	liveFactsSandbox(t)
	setState(t, M{"pc_match_id": "7303595", "playcricket_api_key": "k"})
	md := pcMatch([]M{pcInn("2", "y", "220", "7", "40", nil, nil), pcInn("1", "x", "60", "2", "12", nil, nil)}, "", "", "", "")
	gl(md, "match_details")[0].(M)["no_of_overs"] = "40"
	logInnings(1, "RIVALS CC 1ST", 220, 7, 40.0, 0, smith0, quick0)
	logInnings(2, "HOME CC 1ST", 61, 2, 12.1, 167, smith0, quick0)
	withPCGet(t, func(string, time.Duration) (M, error) { return md, nil })
	f := GenerateSocialGraphicFacts()
	eqv(t, f["outcome"], "live")
	eqv(t, sides(f)[1]["score"], "61-2") // the live figures, one ball on
}

func TestCardNoDataIsRefused(t *testing.T) {
	liveFactsSandbox(t)
	if GenerateSocialGraphicFacts()["ok"] == true {
		t.Fatal("facts from nothing")
	}
}

func TestFetchTodaysMatchSavesOppositionClubNotTeamLabel(t *testing.T) {
	s := newTestServer(t, "")
	saved := FetchTodaysMatch
	t.Cleanup(func() { FetchTodaysMatch = saved })
	FetchTodaysMatch = func(string, string) M {
		return M{"away_team": "1st XI", "away_club": "Rivals CC", "away_club_id": "222", "match_id": "99"}
	}
	s.getJSON("/match/fetch")
	eqv(t, LoadState()["away_team"], "Rivals CC")
}

func TestCardNoResultYetMidChaseIsAnUpdate(t *testing.T) {
	pcFactsSandbox(t)
	md := pcMatch([]M{pcInn("2", "y", "220", "7", "40", nil, nil), pcInn("1", "x", "60", "2", "12", nil, nil)}, "", "", "", "")
	gl(md, "match_details")[0].(M)["no_of_overs"] = "40"
	f := pcFacts(t, md)
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"live", ""})
	for _, s := range sides(f) {
		if s["won"] == true {
			t.Fatal("a winner mid-chase")
		}
	}
}

func TestCardNoResultYetButChaseComplete(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("2", "y", "220", "7", "40", nil, nil), pcInn("1", "x", "221", "2", "35", nil, nil)}, "", "", "", ""))
	eqv(t, f["outcome"], "win")
}

func TestCardCantTellWhichSideIsOurs(t *testing.T) {
	pcFactsSandbox(t)
	setState(t, M{"home_team": "Somewhere Else CC", "home_club_id": ""})
	f := pcFacts(t, pcMatch([]M{pcInn("1", "x", "231", "7", "40", nil, nil), pcInn("2", "y", "185", "10", "36", nil, nil)}, "W", "1", "", ""))
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"", ""})
	eqv(t, sides(f)[0]["won"], true)
	mustEq(t, len(listOfMaps(f["stars"])), 0)
}

func TestCardConcededWithNoInningsAtAll(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch(nil, "CON", "", "Rivals CC - 1st XI - Conceded", ""))
	eqv(t, []any{f["outcome"], f["margin"]}, []any{"win", "by concession"})
	clubs := []string{gs(sides(f)[0], "club", ""), gs(sides(f)[1], "club", "")}
	if !(clubs[0] == "Home & District CC" && clubs[1] == "Rivals CC" || clubs[1] == "Home & District CC" && clubs[0] == "Rivals CC") {
		t.Fatal(clubs)
	}
}

func TestCardPairsTwelveWicketsIsNotAllOut(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("2", "y", "66", "12", "16.0", nil, nil), pcInn("1", "x", "108", "5", "16.0", nil, nil)}, "W", "1", "", "Pairs"))
	eqv(t, []any{sides(f)[0]["score"], sides(f)[0]["overs"]}, []any{"66-12", "16.0 overs"})
}

func TestCardAwayDayBadgesComeFromFixture(t *testing.T) {
	pcFactsSandbox(t)
	f := pcFacts(t, pcMatch([]M{pcInn("2", "y", "150", "9", "40", nil, nil), pcInn("1", "x", "151", "4", "30", nil, nil)}, "W", "1", "", ""))
	ids := map[any]bool{sides(f)[0]["club_id"]: true, sides(f)[1]["club_id"]: true}
	if !ids["111"] || !ids["222"] {
		t.Fatal(ids)
	}
}

// ── One logo per sponsor on the card: an original and its transparent copies count once ──

func sponsorFamily(t *testing.T) string {
	t.Helper()
	sandbox(t, "")
	os.MkdirAll(SponsorDir, 0o755)
	for _, n := range []string{"1.png", "2.jpg", "3.png", "10.png"} {
		os.WriteFile(filepath.Join(SponsorDir, n), nil, 0o644)
	}
	return SponsorDir
}

func cardNames(dir, current string) []string {
	var out []string
	for _, p := range sponsorLogosForCard(dir, current) {
		out = append(out, filepath.Base(p))
	}
	return out
}

func TestSponsorFamilyNoCopiesShowsEverythingInIDOrder(t *testing.T) {
	d := sponsorFamily(t)
	eqv(t, cardNames(d, ""), []string{"1.png", "2.jpg", "3.png", "10.png"})
}

func TestSponsorFamilyCopyReplacesItsOriginal(t *testing.T) {
	d := sponsorFamily(t)
	recordSponsorVariant("3", "2")
	eqv(t, cardNames(d, "3"), []string{"1.png", "3.png", "10.png"})
	eqv(t, cardNames(d, "2"), []string{"1.png", "2.jpg", "10.png"}) // after Undo
	eqv(t, cardNames(d, "1"), []string{"1.png", "3.png", "10.png"}) // the newest copy
}

func TestSponsorFamilyCopyOfACopyCountsOnce(t *testing.T) {
	d := sponsorFamily(t)
	recordSponsorVariant("3", "2")
	recordSponsorVariant("10", "3")
	eqv(t, cardNames(d, "10"), []string{"1.png", "10.png"})
}

func TestSponsorFamilyDifferentFolderIgnoresTheRecord(t *testing.T) {
	sponsorFamily(t)
	recordSponsorVariant("3", "2")
	other := t.TempDir()
	for _, n := range []string{"2.jpg", "3.png"} {
		os.WriteFile(filepath.Join(other, n), nil, 0o644)
	}
	eqv(t, cardNames(other, "3"), []string{"2.jpg", "3.png"})
}
