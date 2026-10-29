package server

// GET /match/scorecard: the match so far as a scorecard, for the clubhouse screen's
// Scorecard and Overs views. Read-only, like /live/view — it never runs the loggers or
// touches the overlay's event detection, so any number of screens may poll it.
//
// The innings, batters, bowlers and fall of wickets are the scorer's own figures (the
// live_* tables, via MatchFactsFromDB) — the same source as the result card, so the two
// can't disagree. The last few overs come from the balls table, which is the only
// ball-by-ball record; they're shown as balls and runs only, never credited to a bowler
// or batter (that table's personnel isn't reliable enough — see CLAUDE.md).

import (
	"database/sql"
	"sort"
	"strconv"
)

// recentOversShown: what fits on the screen at a size readable across a room.
const recentOversShown = 6

// MatchScorecard is GET /match/scorecard.
func MatchScorecard() M {
	mid := CurrentMatchID()
	facts := MatchFactsFromDB(mid)
	inns := gm(facts, "innings")
	keys := make([]int, 0, len(inns))
	for k, v := range inns {
		n, err := strconv.Atoi(k)
		if r, ok := v.(M); err == nil && ok && hasInnings(r) {
			keys = append(keys, n)
		}
	}
	sort.Ints(keys)
	fowBy := map[string][]any{}
	for _, f := range gl(facts, "fall_of_wickets") {
		m := f.(M)
		fowBy[gs(m, "innings", "")] = append(fowBy[gs(m, "innings", "")], M{"batter": m["batter"], "score": m["score"]})
	}
	list := []any{}
	for _, n := range keys {
		k := strconv.Itoa(n)
		r := inns[k].(M)
		bat, _ := gm(facts, "batters")[k].([]any)
		bowl, _ := gm(facts, "bowlers")[k].([]any)
		list = append(list, M{"innings": n, "batting_team": r["batting_team"], "score": r["score"], "wickets": r["wickets"],
			"overs": r["overs"], "batters": orEmpty(bat), "bowlers": orEmpty(bowl), "fall_of_wickets": orEmpty(fowBy[k])})
	}
	out := M{"ok": true, "innings": list, "recent_overs": []any{}}
	if len(keys) > 0 {
		out["recent_overs"] = recentOvers(mid, keys[len(keys)-1], recentOversShown)
	}
	return out
}

func orEmpty(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// recentOvers is the last n overs of an innings, newest first: each ball as the scorebar's
// ticker would show it, the over's runs and wickets.
func recentOvers(mid string, innings, n int) []any {
	type ball struct {
		over    int
		outcome string
		runs    int
		wicket  bool
	}
	var balls []ball
	withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT over, outcome, runs, is_wicket FROM balls WHERE match_id=? AND innings=?
			AND over > (SELECT COALESCE(MAX(over), 0) FROM balls WHERE match_id=? AND innings=?) - ?
			ORDER BY over DESC, ball`, mid, innings, mid, innings, n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b ball
			var w sql.NullInt64
			var outcome sql.NullString
			var runs sql.NullInt64
			if rows.Scan(&b.over, &outcome, &runs, &w) == nil {
				b.outcome, b.runs, b.wicket = outcome.String, int(runs.Int64), w.Int64 != 0
				balls = append(balls, b)
			}
		}
		return nil
	})
	out := []any{}
	var cur M
	for _, b := range balls {
		if cur == nil || gi(cur, "over", 0) != b.over+1 {
			cur = M{"over": b.over + 1, "balls": []any{}, "runs": 0, "wickets": 0}
			out = append(out, cur)
		}
		cur["balls"] = append(gl(cur, "balls"), ballLabel(b.outcome, b.runs))
		cur["runs"] = gi(cur, "runs", 0) + b.runs
		if b.wicket {
			cur["wickets"] = gi(cur, "wickets", 0) + 1
		}
	}
	return out
}

// ballLabel is a ball as a scorer writes it: "•" for a dot, "W", "4", "wd", "2nb", "1lb".
func ballLabel(outcome string, runs int) string {
	extra := func(suffix string, base int) string {
		if runs <= base {
			return suffix
		}
		return strconv.Itoa(runs-base) + suffix
	}
	switch outcome {
	case "dot", "":
		return "•"
	case "W":
		return "W"
	case "wide":
		return extra("wd", 1)
	case "noball":
		return extra("nb", 1)
	case "legbye":
		return extra("lb", 0)
	case "bye":
		return extra("b", 0)
	}
	return outcome
}
