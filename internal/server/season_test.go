package server

// Season-stats aggregation from PlayCricket scorecards (tests/test_season_stats.py) and
// the league-table backend (tests/test_league_table.py) — pure logic, network faked.

import (
	"errors"
	"testing"
	"time"
)

func match(innings ...M) M {
	inn := make([]any, len(innings))
	for i, x := range innings {
		inn[i] = x
	}
	return M{"match_details": []any{M{"innings": inn}}}
}

func bat(name string, runs any, howOut string) M {
	if howOut == "" {
		howOut = "ct Someone"
	}
	return M{"batsman_name": name, "how_out": howOut, "runs": pyStr(runs), "balls": "30", "fours": "0", "sixes": "0"}
}

const (
	homeXI = "Home CC 1st XI"
	awayXI = "Rival CC 1st XI"
)

func seasonSample() []M {
	return []M{
		match(
			M{"team_batting_name": homeXI, "bat": []any{
				bat("Peter Smith", 30, ""), bat("Karl Jones", 50, "not out"), bat("A N Other", "", "did not bat")}},
			M{"team_batting_name": awayXI, "bat": []any{bat("Bob Rival", 10, "")},
				"bowl": []any{M{"bowler_name": "Peter Smith", "wickets": "3", "runs": "20", "overs": "8", "maidens": "1"}}},
		),
		match(
			M{"team_batting_name": homeXI, "bat": []any{bat("Peter Smith", 60, "b Bowler"), bat("Karl Jones", 10, "")}},
			M{"team_batting_name": awayXI, "bat": []any{bat("Bob Rival", 99, "")},
				"bowl": []any{M{"bowler_name": "Peter Smith", "wickets": "1", "runs": "35", "overs": "8", "maidens": "0"}}},
		),
	}
}

func TestSeasonAveragesAndNotOuts(t *testing.T) {
	lk := aggregateSeasonBat(seasonSample(), []string{"home"})
	smith := gm(lk, "petersmith")
	eqv(t, []any{smith["inn"], smith["runs"], smith["avg"], smith["hs"]}, []any{"2", 90, "45.00", "60"})
	jones := gm(lk, "karljones")
	eqv(t, jones["avg"], "60.00") // 60 runs, 2 innings, 1 not out
	eqv(t, jones["hs"], "50*")    // a not-out high score keeps the star
	if _, ok := lk["another"]; ok {
		t.Fatal(`"did not bat" counted as an innings`)
	}
}

func TestSeasonKeepClubsFiltersOpposition(t *testing.T) {
	if _, ok := aggregateSeasonBat(seasonSample(), []string{"home"})["bobrival"]; ok {
		t.Fatal("opposition kept")
	}
	if _, ok := aggregateSeasonBat(seasonSample(), nil)["bobrival"]; !ok {
		t.Fatal("no filter dropped a player")
	}
}

func TestSeasonSurnameKeyOnlyWhenUnique(t *testing.T) {
	if _, ok := aggregateSeasonBat(seasonSample(), []string{"home"})["smith"]; !ok {
		t.Fatal("one Smith → the surname key should exist")
	}
	// A second Smith → the surname-only key must be suppressed (brother safety)
	data := append(seasonSample(), match(M{"team_batting_name": homeXI, "bat": []any{bat("James Smith", 5, "")}}))
	lk := aggregateSeasonBat(data, []string{"home"})
	if _, ok := lk["smith"]; ok {
		t.Fatal("ambiguous surname key kept")
	}
	for _, k := range []string{"psmith", "jsmith"} { // initial+surname tiers stay distinct
		if _, ok := lk[k]; !ok {
			t.Fatal(k)
		}
	}
}

func TestSeasonInitialSurnameKeyMatchesScorebarShortNames(t *testing.T) {
	lk := aggregateSeasonBat(seasonSample(), []string{"home"})
	if mapIdentity(lk["psmith"]) != mapIdentity(lk["petersmith"]) {
		t.Fatal("not the same record under both spellings")
	}
}

func TestSeasonDuplicateAccountKeepsMostInnings(t *testing.T) {
	// The same person under two PlayCricket accounts: the shared 'psmith' key must keep
	// the regular account.
	data := append(seasonSample(), match(M{"team_batting_name": homeXI, "bat": []any{bat("P Smith", 7, "")}}))
	lk := aggregateSeasonBat(data, []string{"home"})
	eqv(t, gm(lk, "psmith")["inn"], "2")
	eqv(t, gm(lk, "psmith")["name"], "Peter Smith")
}

func TestSeasonTopBowlerCreditedToFieldingSide(t *testing.T) {
	top := seasonTopBowler(seasonSample(), "home")
	// Peter Smith's figures live on the AWAY batting innings' bowl card
	eqv(t, []any{top["name"], top["wkts"], top["runs"]}, []any{"Peter Smith", 4, 55})
}

func TestSeasonTopBowlerNoneWithoutClub(t *testing.T) {
	if seasonTopBowler(nil, "") != nil {
		t.Fatal("a top bowler with no club")
	}
}

func TestSeasonTopScorerDedupesSharedRecords(t *testing.T) {
	top := seasonTopScorer(aggregateSeasonBat(seasonSample(), []string{"home"}))
	eqv(t, []any{top["name"], top["runs"]}, []any{"Peter Smith", 90})
}

func withSeasonLookup(t *testing.T) {
	t.Helper()
	lk := aggregateSeasonBat(seasonSample(), []string{"home"})
	seasonStatsMu.Lock()
	saved := seasonStats
	seasonStats = M{"lookup": lk, "built": true}
	seasonStatsMu.Unlock()
	t.Cleanup(func() {
		seasonStatsMu.Lock()
		seasonStats = saved
		seasonStatsMu.Unlock()
	})
}

func TestSeasonLookupTiers(t *testing.T) {
	withSeasonLookup(t)
	// Full name, scorebar short form, and unique bare surname all resolve
	for _, q := range []string{"Peter Smith", "P SMITH", "p.smith", "SMITH"} {
		rec := lookupSeasonStats(q)
		if rec == nil {
			t.Fatal(q)
		}
		eqv(t, rec["avg"], "45.00", q)
	}
}

func TestSeasonLookupUnknownAndEmpty(t *testing.T) {
	withSeasonLookup(t)
	for _, q := range []string{"ZZZ NOBODY", ""} {
		if lookupSeasonStats(q) != nil {
			t.Fatal(q)
		}
	}
}

// ── League table ──

func leagueTbl(rows ...[4]any) M {
	out := []any{}
	for _, r := range rows {
		out = append(out, M{"position": r[0], "team": r[1], "points": r[2], "played": r[3]})
	}
	return M{"date": "2026-09-24", "competition_id": "123", "built": true, "error": nil, "name": "Division 1", "rows": out}
}

func TestLeagueFindsHomeRowAndRowAbove(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"home_team": "Bridestowe and Belstone CC"})
	r := leagueTableHomeRow(leagueTbl([4]any{4, "Hatherleigh CC", "40", "10"},
		[4]any{5, "Bridestowe and Belstone CC", "36", "10"}, [4]any{6, "Rival CC", "30", "10"}))
	if r == nil {
		t.Fatal("not found")
	}
	eqv(t, r["index"], 1)
	eqv(t, gm(r, "row")["team"], "Bridestowe and Belstone CC")
	eqv(t, gm(r, "row_above")["team"], "Hatherleigh CC")
}

func TestLeagueTopOfTableHasNoRowAbove(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"home_team": "Bridestowe and Belstone CC"})
	r := leagueTableHomeRow(leagueTbl([4]any{1, "Bridestowe and Belstone CC", "50", "10"}, [4]any{2, "Rival CC", "40", "10"}))
	eqv(t, r["index"], 0)
	if r["row_above"] != nil {
		t.Fatal(r["row_above"])
	}
}

func TestLeagueMatchesOnFirstSignificantWordOnly(t *testing.T) {
	// PlayCricket's spelling in the table needn't match state's home_team exactly.
	sandbox(t, "")
	setState(t, M{"home_team": "Bridestowe CC"})
	if leagueTableHomeRow(leagueTbl([4]any{3, "Bridestowe & District", "20", "8"})) == nil {
		t.Fatal("not found")
	}
}

func TestLeagueWrongTableReturnsNoneNotFalseMatch(t *testing.T) {
	// A bad competition_id returned an unrelated real league's table rather than erroring.
	sandbox(t, "")
	setState(t, M{"home_team": "Bridestowe and Belstone CC"})
	if leagueTableHomeRow(leagueTbl([4]any{1, "Some Other CC", "50", "10"}, [4]any{2, "Unrelated CC", "40", "10"})) != nil {
		t.Fatal("false match")
	}
}

func TestLeagueEmptyTableAndBlankHomeName(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"home_team": ""})
	if leagueTableHomeRow(leagueTbl([4]any{1, "Anyone CC", "10", "5"})) != nil {
		t.Fatal("matched a blank home name")
	}
	setState(t, M{"home_team": "Bridestowe CC"})
	if leagueTableHomeRow(leagueTbl()) != nil {
		t.Fatal("matched an empty table")
	}
}

func withLeague(t *testing.T, key, comp string) {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"playcricket_api_key": key, "competition_id": comp})
	leagueTableMu.Lock()
	saved := leagueTable
	leagueTableMu.Unlock()
	t.Cleanup(func() {
		leagueTableMu.Lock()
		leagueTable = saved
		leagueTableMu.Unlock()
	})
}

func TestLeagueNoKeyOrCompetitionIsCleanNotBuiltError(t *testing.T) {
	// Cup/friendly day, or before /match/fetch: no network call, no crash, just why.
	withLeague(t, "", "")
	withPCGet(t, func(string, time.Duration) (M, error) { t.Fatal("network called"); return nil, nil })
	r := FetchLeagueTable(true)
	mustEq(t, r["built"], any(true))
	if r["error"] == nil {
		t.Fatal("no reason given")
	}
	mustEq(t, len(gl(r, "rows")), 0)
}

func TestLeaguePicksColumnsByHeadingText(t *testing.T) {
	// A non-league table had no points column at all — columns must be found by heading
	// text, never assumed to be a fixed column_N.
	withLeague(t, "KEY", "123")
	withPCGet(t, func(string, time.Duration) (M, error) {
		return M{"league_table": []any{M{"name": "Division 1",
			"headings": M{"column_1": "Team", "column_5": "Pts", "column_2": "P"},
			"values":   []any{M{"column_1": "Bridestowe and Belstone CC", "column_5": "36", "column_2": "10"}}}}}, nil
	})
	r := FetchLeagueTable(true)
	if r["error"] != nil {
		t.Fatal(r["error"])
	}
	row := gl(r, "rows")[0].(M)
	eqv(t, []any{row["team"], row["points"], row["played"]}, []any{"Bridestowe and Belstone CC", "36", "10"})
}

func TestLeagueAPIFailureReportsErrorNotException(t *testing.T) {
	withLeague(t, "KEY", "123")
	withPCGet(t, func(string, time.Duration) (M, error) { return nil, errors.New("boom") })
	r := FetchLeagueTable(true)
	if r["error"] == nil {
		t.Fatal("no error")
	}
	mustEq(t, len(gl(r, "rows")), 0)
}

func TestLeagueCachedSameDaySameCompetitionSkipsNetwork(t *testing.T) {
	withLeague(t, "KEY", "123")
	leagueTableMu.Lock()
	leagueTable = M{"date": time.Now().Format("2006-01-02"), "competition_id": "123", "built": true, "error": nil,
		"name": "Division 1", "rows": []any{M{"position": 1, "team": "X", "points": "1", "played": "1"}}}
	leagueTableMu.Unlock()
	withPCGet(t, func(string, time.Duration) (M, error) { t.Fatal("network called"); return nil, nil })
	r := FetchLeagueTable(false)
	eqv(t, gl(r, "rows")[0].(M)["team"], "X")
}
