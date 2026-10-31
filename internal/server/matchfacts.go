package server

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ai"
	"cricketstream/internal/winpredict"
)

// NoMatchDataError is the refusal when there's nothing to write about: the report and
// card come from the scorer's feed, never from a model's imagination.
const NoMatchDataError = "No match data recorded yet — the report is written from the scorer's " +
	"feed, so it needs at least one ball logged for today's match."

// oversToBalls: 6.0 / "6" -> 36, 22.3 -> 135 (the digit after the point is balls).
func oversToBalls(overs any) int { return winpredict.Balls(overs) }

func ballsToOvers(balls int) string {
	if balls%6 != 0 {
		return fmt.Sprintf("%d.%d", balls/6, balls%6)
	}
	return strconv.Itoa(balls / 6)
}

// What the previous poll looked like, so logLiveFigures can tell who was dismissed (the
// batter who left the pair) and finish the over of a bowler who has just been rotated off.
type livePrevT struct {
	mid         string
	innings     int
	set         bool
	pair        []string
	wkts, score int
	balls       int
	bowler      string
	bowlerOvers int // balls
}

var (
	livePrev   livePrevT
	livePrevMu sync.Mutex
)

// matchTables is every table keyed by match_id, for FollowMatchID. A new match-keyed
// table must be added here or it gets left behind when the id changes.
var matchTables = []string{"balls", "live_innings", "live_batting", "live_bowling", "live_fow",
	"innings_totals", "batting", "bowling", "clips", "moments"}

var (
	loggedMid   string
	loggedMidMu sync.Mutex
)

const matchIDFollowSec = 600

// ResetLiveFigures forgets the in-memory side of the figure logger (tests).
func ResetLiveFigures() {
	livePrevMu.Lock()
	livePrev = livePrevT{}
	livePrevMu.Unlock()
	loggedMidMu.Lock()
	loggedMid = ""
	loggedMidMu.Unlock()
}

// FollowMatchID keeps one match's logged data under one id when the id changes
// mid-innings. CurrentMatchID is the PlayCricket id once "Fetch today's match" has run,
// else the date plus both team names — so fetching after the first ball, or correcting
// the opposition name mid-match, used to split the match. Rows move only when it's
// genuinely the same match: the innings is under way, the old id logged in the last 10
// minutes, and the new id has nothing yet — so a morning rehearsal is never pulled into
// the afternoon's match. Never raises.
func FollowMatchID(state M) {
	defer func() { recover() }()
	mid := CurrentMatchID()
	loggedMidMu.Lock()
	old := loggedMid
	loggedMid = mid
	loggedMidMu.Unlock()
	if old == "" || old == mid || oversToBalls(state["overs"]) == 0 {
		return
	}
	moved := false
	withDB(func(tx *sql.Tx) error {
		var one int
		if tx.QueryRow("SELECT 1 FROM balls WHERE match_id=? UNION ALL SELECT 1 FROM live_innings WHERE match_id=? LIMIT 1", mid, mid).Scan(&one) == nil {
			return nil
		}
		var last sql.NullString
		tx.QueryRow("SELECT MAX(ts) FROM (SELECT ts FROM balls WHERE match_id=? UNION ALL SELECT ts FROM live_innings WHERE match_id=?)", old, old).Scan(&last)
		t, err := time.ParseInLocation("2006-01-02T15:04:05", last.String, time.Local)
		if !last.Valid || err != nil || time.Since(t).Seconds() > matchIDFollowSec {
			return nil
		}
		for _, tbl := range matchTables {
			if _, err := tx.Exec("UPDATE OR IGNORE "+tbl+" SET match_id=? WHERE match_id=?", mid, old); err != nil {
				return err
			}
		}
		_, err = tx.Exec("UPDATE OR IGNORE matches SET match_id=? WHERE match_id=?", mid, old)
		moved = err == nil
		return err
	})
	if !moved {
		return
	}
	ballLogMu.Lock()
	if ballLog.mid == old {
		ballLog.mid = mid
	}
	ballLogMu.Unlock()
	livePrevMu.Lock()
	if livePrev.set && livePrev.mid == old {
		livePrev.mid = mid
	}
	livePrevMu.Unlock()
	say("  ✓  Match id changed mid-innings (%s → %s) — logged data moved with it", old, mid)
}

// LogLiveFigures keeps the live_* tables in step with the scorer's running figures:
// innings total, each batter's runs/balls, each bowler's figures, and who fell at what
// score. These are the scorer's OWN totals, so they're exact — unlike the balls table,
// whose batter column is whoever was batter1 at the last write and which can't see an
// over's final ball. They're what the report and result card read after a restart.
// Never raises.
func LogLiveFigures(state M) {
	defer func() { recover() }()
	innings := gi(state, "innings", 1)
	if innings == 0 {
		innings = 1
	}
	score, wkts := gi(state, "score", 0), gi(state, "wickets", 0)
	balls := oversToBalls(state["overs"])
	if score == 0 && wkts == 0 && balls == 0 {
		return // a pre-match frame (or a blank file): not an innings yet
	}
	mid := CurrentMatchID()
	t := nowISO()
	var limit any
	if rem := gi(state, "ballsRemaining", 0); rem > 0 {
		limit = balls + rem
	}
	type batter struct {
		name        string
		runs, balls int
	}
	var pair []batter
	for _, k := range []string{"batter1", "batter2"} {
		b := gm(state, k)
		if name := trim(gs(b, "name", "")); name != "" && name != "—" && name != "-" {
			pair = append(pair, batter{name, gi(b, "runs", 0), gi(b, "balls", 0)})
		}
	}
	bw := gm(state, "bowler")
	bowler := trim(gs(bw, "name", ""))
	if bowler == "—" || bowler == "-" {
		bowler = ""
	}
	livePrevMu.Lock()
	defer livePrevMu.Unlock()
	var prev *livePrevT
	if livePrev.set && livePrev.mid == mid && livePrev.innings == innings {
		prev = &livePrev
	}
	err := withDB(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO live_innings(match_id,innings,batting_team,score,wickets,overs,"+
			"limit_balls,ts) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(match_id,innings) DO UPDATE SET "+
			"batting_team=excluded.batting_team, score=excluded.score, wickets=excluded.wickets,"+
			" overs=excluded.overs, limit_balls=COALESCE(excluded.limit_balls, limit_balls),"+
			" ts=excluded.ts", mid, innings, gs(state, "battingTeamName", ""), score, wkts, ballsToOvers(balls), limit, t); err != nil {
			return err
		}
		for _, b := range pair {
			if _, err := tx.Exec("INSERT INTO live_batting(match_id,innings,name,runs,balls,out) "+
				"VALUES(?,?,?,?,?,0) ON CONFLICT(match_id,innings,name) DO UPDATE SET "+
				"runs=excluded.runs, balls=excluded.balls, out=0", mid, innings, b.name, b.runs, b.balls); err != nil {
				return err
			}
		}
		if prev != nil && wkts > prev.wkts {
			// Whoever left the pair is out. Two gone at once (a missed poll) still pairs
			// them up in order; a wicket we can't name keeps its score.
			still := map[string]bool{}
			for _, b := range pair {
				still[b.name] = true
			}
			var gone []string
			for _, n := range prev.pair {
				if !still[n] {
					gone = append(gone, n)
				}
			}
			for i, w := 0, prev.wkts+1; w <= wkts; i, w = i+1, w+1 {
				who := ""
				if i < len(gone) {
					who = gone[i]
				}
				if _, err := tx.Exec("INSERT OR REPLACE INTO live_fow(match_id,innings,wicket,batter,score) VALUES(?,?,?,?,?)",
					mid, innings, w, who, score); err != nil {
					return err
				}
				if who != "" {
					if _, err := tx.Exec("UPDATE live_batting SET out=1 WHERE match_id=? AND innings=? AND name=?", mid, innings, who); err != nil {
						return err
					}
				}
			}
		}
		if bowler != "" {
			if _, err := tx.Exec("INSERT INTO live_bowling(match_id,innings,name,overs,runs,wickets) "+
				"VALUES(?,?,?,?,?,?) ON CONFLICT(match_id,innings,name) DO UPDATE SET "+
				"overs=excluded.overs, runs=excluded.runs, wickets=excluded.wickets",
				mid, innings, bowler, gsOr(bw, "overs", "0"), gi(bw, "runs", 0), gi(bw, "wickets", 0)); err != nil {
				return err
			}
		}
		if prev != nil && prev.bowler != "" && prev.bowler != bowler && balls%6 == 0 && balls > prev.balls {
			// On the over-completing write the bowler has ALREADY rotated, so the finished
			// over's last ball never reaches the old bowler's figures. Finish them from the
			// score movement; if they bowl again, the scorer's own cumulative figures
			// overwrite this.
			if _, err := tx.Exec("UPDATE live_bowling SET overs=?, runs=runs+?, wickets=wickets+? "+
				"WHERE match_id=? AND innings=? AND name=?", ballsToOvers((prev.bowlerOvers/6+1)*6),
				max(score-prev.score, 0), max(wkts-prev.wkts, 0), mid, innings, prev.bowler); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	names := make([]string, len(pair))
	for i, b := range pair {
		names[i] = b.name
	}
	livePrev = livePrevT{mid: mid, innings: innings, set: true, pair: names, wkts: wkts, score: score,
		balls: balls, bowler: bowler, bowlerOvers: oversToBalls(valOr(bw, "overs", 0))}
}

// MatchFactsFromDB is the innings totals, fall of wickets, batters, bowlers and
// milestones for one match, from the live_* tables. Never raises: empty collections.
func MatchFactsFromDB(matchID string) M {
	out := M{"innings": M{}, "fall_of_wickets": []any{}, "milestones": []any{}, "top_scorers": M{},
		"batters": M{}, "bowlers": M{}}
	type inn struct {
		n              int64
		team, overs    sql.NullString
		score, wk, lim sql.NullInt64
	}
	var inns []inn
	type bat struct {
		n           int64
		name        string
		runs, balls sql.NullInt64
		out         sql.NullInt64
	}
	var bats []bat
	type bowl struct {
		n        int64
		name     string
		overs    sql.NullString
		runs, wk sql.NullInt64
	}
	var bowls []bowl
	type fowT struct {
		n, wicket int64
		batter    sql.NullString
		score     sql.NullInt64
	}
	var fows []fowT
	err := withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT innings,batting_team,score,wickets,overs,limit_balls FROM live_innings WHERE match_id=? ORDER BY innings", matchID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r inn
			rows.Scan(&r.n, &r.team, &r.score, &r.wk, &r.overs, &r.lim)
			inns = append(inns, r)
		}
		rows.Close()
		// rowid order = the order they first appeared: the batting order, near enough.
		if rows, err = tx.Query("SELECT innings,name,runs,balls,out FROM live_batting WHERE match_id=? ORDER BY rowid", matchID); err != nil {
			return err
		}
		for rows.Next() {
			var r bat
			rows.Scan(&r.n, &r.name, &r.runs, &r.balls, &r.out)
			bats = append(bats, r)
		}
		rows.Close()
		if rows, err = tx.Query("SELECT innings,name,overs,runs,wickets FROM live_bowling WHERE match_id=? ORDER BY rowid", matchID); err != nil {
			return err
		}
		for rows.Next() {
			var r bowl
			rows.Scan(&r.n, &r.name, &r.overs, &r.runs, &r.wk)
			bowls = append(bowls, r)
		}
		rows.Close()
		if rows, err = tx.Query("SELECT innings,wicket,batter,score FROM live_fow WHERE match_id=? ORDER BY innings,wicket", matchID); err != nil {
			return err
		}
		for rows.Next() {
			var r fowT
			rows.Scan(&r.n, &r.wicket, &r.batter, &r.score)
			fows = append(fows, r)
		}
		rows.Close()
		return nil
	})
	if err != nil {
		return out
	}
	innM, batM, bowlM := out["innings"].(M), out["batters"].(M), out["bowlers"].(M)
	for _, r := range inns {
		var lim any
		if r.lim.Valid {
			lim = int(r.lim.Int64)
		}
		innM[strconv.FormatInt(r.n, 10)] = M{"batting_team": r.team.String, "score": int(r.score.Int64),
			"wickets": int(r.wk.Int64), "overs": orStr(r.overs.String, "0"), "limit_balls": lim}
	}
	var batOrder []string
	for _, r := range bats {
		k := strconv.FormatInt(r.n, 10)
		if _, ok := batM[k]; !ok {
			batOrder = append(batOrder, k)
			batM[k] = []any{}
		}
		batM[k] = append(batM[k].([]any), M{"name": r.name, "runs": int(r.runs.Int64), "balls": int(r.balls.Int64), "out": r.out.Int64 != 0})
	}
	for _, r := range bowls {
		k := strconv.FormatInt(r.n, 10)
		l, _ := bowlM[k].([]any)
		bowlM[k] = append(l, M{"name": r.name, "o": orStr(r.overs.String, "0"), "r": int(r.runs.Int64), "w": int(r.wk.Int64)})
	}
	fowL := []any{}
	for _, r := range fows {
		score := "None"
		if r.score.Valid {
			score = strconv.FormatInt(r.score.Int64, 10)
		}
		fowL = append(fowL, M{"batter": orStr(r.batter.String, "?"), "score": fmt.Sprintf("%s-%d", score, r.wicket),
			"howout": "", "innings": strconv.FormatInt(r.n, 10)})
	}
	out["fall_of_wickets"] = fowL
	tops, miles := out["top_scorers"].(M), []any{}
	for _, k := range batOrder {
		rows := append([]any(nil), batM[k].([]any)...) // a copy: keep the batting order intact
		sort.SliceStable(rows, func(i, j int) bool { return gi(rows[i].(M), "runs", 0) > gi(rows[j].(M), "runs", 0) })
		var ts []any
		for i, r := range rows {
			b := r.(M)
			if i < 3 && gi(b, "runs", 0) > 0 {
				star := "*"
				if gb(b, "out", false) {
					star = ""
				}
				ts = append(ts, fmt.Sprintf("%s %d%s (%d balls)", gs(b, "name", ""), gi(b, "runs", 0), star, gi(b, "balls", 0)))
			}
		}
		if ts == nil {
			ts = []any{}
		}
		tops[k] = ts
		for _, r := range rows {
			b := r.(M)
			if runs := gi(b, "runs", 0); runs >= 50 {
				miles = append(miles, M{"batter": gs(b, "name", ""), "milestone": strconv.Itoa(runs / 50 * 50)})
			}
		}
	}
	out["milestones"] = miles
	return out
}

// hasInnings: an innings that has actually started — not a pre-match/blank frame's 0-0.
func hasInnings(rec M) bool {
	return gi(rec, "score", 0) != 0 || gi(rec, "wickets", 0) != 0 || oversToBalls(valOr(rec, "overs", 0)) != 0
}

// mergedMatchFacts is (db facts, innings) for the current match — the one merge both the
// AI write-ups and the result card use, so they can't disagree. The live_* tables first
// (exact, and they survive restarts); the in-memory log only fills an innings the DB
// doesn't have. Empty 0-0 innings are dropped either way.
func mergedMatchFacts() (M, M) {
	db := MatchFactsFromDB(CurrentMatchID())
	innings := M{}
	for k, v := range db["innings"].(M) {
		if r := v.(M); hasInnings(r) {
			innings[k] = shallow(r)
		}
	}
	for k, v := range gm(matchLogCopy(), "innings") {
		if _, ok := innings[k]; !ok {
			if r, isM := v.(map[string]any); isM && hasInnings(r) {
				innings[k] = shallow(r)
			}
		}
	}
	return db, innings
}

// buildMatchSummary is the facts handed to the report / social generators, and whether
// there are any. hasData is false until an innings has actually started — callers must
// refuse rather than let the model invent a match.
func buildMatchSummary(cfg M) (string, bool) {
	snap := matchLogCopy()
	db, innings := mergedMatchFacts()
	// The DB names every wicket; the memory list starts afresh at a restart.
	fow := gl(db, "fall_of_wickets")
	if len(fow) < len(gl(snap, "fall_of_wickets")) {
		fow = gl(snap, "fall_of_wickets")
	}
	seen := map[string]bool{}
	milestones := append([]any(nil), gl(snap, "milestones")...)
	for _, m := range milestones {
		seen[gs(m.(map[string]any), "batter", "")] = true
	}
	for _, m := range gl(db, "milestones") {
		if !seen[gs(m.(M), "batter", "")] {
			milestones = append(milestones, m)
		}
	}
	home := gsOr(cfg, "home_team", gs(cfg, "name", "Home"))
	away := gs(cfg, "away_team", "Opposition")
	comp := gs(cfg, "competition", "")
	head := fmt.Sprintf("Match: %s v %s", home, away)
	if comp != "" {
		head += " (" + comp + ")"
	}
	lines := []string{head}
	tops := gm(db, "top_scorers")
	for _, k := range sortedKeys(innings) {
		r := innings[k].(M)
		lines = append(lines, fmt.Sprintf("Innings %s: %s %s-%s (%s overs)", k, gs(r, "batting_team", "?"),
			anyStr(r, "score", "0"), anyStr(r, "wickets", "0"), oversStr(valOr(r, "overs", 0))))
		if ts := gl(tops, k); len(ts) > 0 {
			var s []string
			for _, x := range ts {
				s = append(s, fmt.Sprint(x))
			}
			lines = append(lines, "  Top scorers: "+strings.Join(s, "; "))
		}
	}
	if len(fow) > 0 {
		lines = append(lines, "Wickets:")
		for i, w := range fow {
			if i >= 12 {
				break
			}
			wm, _ := w.(map[string]any)
			lines = append(lines, fmt.Sprintf("  %s %s at %s", gs(wm, "batter", "?"), gs(wm, "howout", ""), gs(wm, "score", "?")))
		}
	}
	if len(milestones) > 0 {
		var s []string
		for i, m := range milestones {
			if i >= 8 {
				break
			}
			mm, _ := m.(map[string]any)
			s = append(s, fmt.Sprintf("%s %s", gs(mm, "batter", "?"), gs(mm, "milestone", "")))
		}
		lines = append(lines, "Milestones: "+strings.Join(s, "; "))
	}
	if ev := gl(snap, "events"); len(ev) > 0 {
		if len(ev) > 25 {
			ev = ev[len(ev)-25:]
		}
		var s []string
		for _, e := range ev {
			s = append(s, gs(e.(map[string]any), "detail", ""))
		}
		lines = append(lines, "Key moments: "+strings.Join(s, " | "))
	}
	return strings.Join(lines, "\n"), len(innings) > 0
}

// GenerateMatchReport writes an AI match report ("report") or short social post
// ("social") from the scorer's figures. Swappable for tests.
var GenerateMatchReport = func(reportType string) M {
	cfg := LoadState()
	key := anthropicKey(cfg)
	if key == "" {
		return M{"ok": false, "error": "No Anthropic API key set"}
	}
	summary, hasData := buildMatchSummary(cfg)
	if !hasData {
		return M{"ok": false, "error": NoMatchDataError}
	}
	home := gsOr(cfg, "home_team", gs(cfg, "name", "Home"))
	var prompt string
	var maxTok int64
	if reportType == "social" {
		prompt = fmt.Sprintf("You are running the social media for a village cricket club, %s. ", home) +
			"Write a short, upbeat match-result post for Twitter/Instagram " +
			"based on the facts below. Max 60 words. Use the club spirit — warm, proud, a little wit. " +
			"You may use up to 3 tasteful hashtags and at most 1 emoji. Do NOT invent facts not present below.\n\n" +
			summary + "\n\nPost:"
		maxTok = 200
	} else {
		prompt = fmt.Sprintf("You are a cricket correspondent writing a match report for %s's club website. ", home) +
			"Write an engaging, accurate report of 150-250 words " +
			"based ONLY on the facts below. Cover the key passages of play, standout performers, and the result. " +
			"Warm local-paper tone, not hyperbole. Do NOT invent statistics or names not present.\n\n" +
			summary + "\n\nMatch report:"
		maxTok = 600
	}
	text, err := ai.Ask(context.Background(), ai.Request{Key: key, Prompt: prompt, MaxTokens: maxTok, Timeout: 60 * time.Second})
	if err != nil {
		return M{"ok": false, "error": err.Error()}
	}
	return M{"ok": true, "text": strings.TrimSpace(text), "summary": summary}
}

// ── Team keys, social photos, roster and player names ──

var (
	reYouth1  = regexp.MustCompile(`\bu\s?-?\d{1,2}\b`)
	reYouth2  = regexp.MustCompile(`\bunder[\s-]?\d{1,2}\b`)
	reYouth3  = regexp.MustCompile(`\b(colts?|juniors?|junior|youth|jnr|girls|boys|kwik)\b`)
	reOrdinal = regexp.MustCompile(`\b([1-9])\s*(?:st|nd|rd|th)\b`)
)

// teamKey maps a team name to a photo-folder key. Youth/age-group sides (U11, Under-15,
// Colts, Juniors…) ALL map to one "youth" key so their posts only ever use the club's
// stock photos in socials/youth — never images of the children. Senior sides map to
// 1st/2nd/3rd.
func teamKey(name string) string {
	s := strings.ToLower(name)
	if reYouth1.MatchString(s) || reYouth2.MatchString(s) || reYouth3.MatchString(s) {
		return "youth"
	}
	if m := reOrdinal.FindStringSubmatch(s); m != nil {
		suffix := map[string]string{"1": "st", "2": "nd", "3": "rd"}[m[1]]
		if suffix == "" {
			suffix = "th"
		}
		return m[1] + suffix
	}
	for _, p := range [][2]string{{"first", "1st"}, {"second", "2nd"}, {"third", "3rd"}, {"fourth", "4th"}, {"fifth", "5th"}} {
		if regexp.MustCompile(`\b` + p[0] + `\b`).MatchString(s) {
			return p[1]
		}
	}
	return ""
}
