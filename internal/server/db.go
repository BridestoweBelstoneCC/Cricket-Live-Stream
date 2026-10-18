package server

// Ball-by-ball logger — the club's own resilient match database (SQLite). Two tiers:
//   - LIVE  : the current over is rewritten from the ticker every poll, so a scorer's
//             within-over edits/insertions/deletions are always captured. Completed overs
//             freeze as last seen.
//   - TRUTH : reconcileMatch pulls PlayCricket's published scorecard afterwards and writes
//             authoritative per-innings batting/bowling aggregates, correcting live drift.

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"cricketstream/internal/pyval"
)

var (
	dbMu     sync.Mutex // the Python _db_lock: one writer/reader at a time
	dbHandle *sql.DB
	dbOpenAt string
)

// db returns the handle for DBFile (reopened if a test moved DBFile). Caller holds dbMu.
func db() (*sql.DB, error) {
	if dbHandle != nil && dbOpenAt == DBFile {
		return dbHandle, nil
	}
	if dbHandle != nil {
		dbHandle.Close()
		dbHandle = nil
	}
	h, err := sql.Open("sqlite", "file:"+filepath.ToSlash(DBFile)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(1)
	dbHandle, dbOpenAt = h, DBFile
	return h, nil
}

// closeDB releases the handle (tests that delete the file, shutdown).
func closeDB() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbHandle != nil {
		dbHandle.Close()
		dbHandle = nil
	}
}

// withDB runs fn in one transaction under the DB lock: committed if fn returns nil,
// rolled back otherwise.
func withDB(fn func(tx *sql.Tx) error) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	h, err := db()
	if err != nil {
		return err
	}
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const schema = `
CREATE TABLE IF NOT EXISTS matches (
    match_id TEXT PRIMARY KEY, date TEXT, home TEXT, away TEXT,
    competition TEXT, team_key TEXT, reconciled INTEGER DEFAULT 0,
    result TEXT, updated TEXT);
CREATE TABLE IF NOT EXISTS balls (
    match_id TEXT, innings INTEGER, over INTEGER, ball INTEGER,
    batting_team TEXT, batter TEXT, non_striker TEXT, bowler TEXT,
    outcome TEXT, runs INTEGER, extra TEXT, is_wicket INTEGER,
    legal INTEGER, cum_runs INTEGER, cum_wkts INTEGER, ts TEXT,
    PRIMARY KEY (match_id, innings, over, ball));
CREATE TABLE IF NOT EXISTS innings_totals (
    match_id TEXT, innings INTEGER, batting_team TEXT,
    runs INTEGER, wickets INTEGER, overs TEXT,
    PRIMARY KEY (match_id, innings));
CREATE TABLE IF NOT EXISTS batting (
    match_id TEXT, innings INTEGER, position INTEGER, name TEXT,
    how_out TEXT, runs INTEGER, balls INTEGER, fours INTEGER, sixes INTEGER,
    PRIMARY KEY (match_id, innings, name));
CREATE TABLE IF NOT EXISTS bowling (
    match_id TEXT, innings INTEGER, name TEXT, overs TEXT,
    maidens INTEGER, runs INTEGER, wickets INTEGER,
    PRIMARY KEY (match_id, innings, name));
CREATE TABLE IF NOT EXISTS clips (
    match_id TEXT, file TEXT, ts TEXT, reason TEXT,
    caption TEXT,
    PRIMARY KEY (match_id, file));
-- The scorer's own running figures, kept current by logLiveFigures(): exact where the
-- balls table is a reconstruction, and they survive a restart.
CREATE TABLE IF NOT EXISTS live_innings (
    match_id TEXT, innings INTEGER, batting_team TEXT, score INTEGER,
    wickets INTEGER, overs TEXT, limit_balls INTEGER, ts TEXT,
    PRIMARY KEY (match_id, innings));
CREATE TABLE IF NOT EXISTS live_batting (
    match_id TEXT, innings INTEGER, name TEXT, runs INTEGER, balls INTEGER,
    out INTEGER DEFAULT 0,
    PRIMARY KEY (match_id, innings, name));
CREATE TABLE IF NOT EXISTS live_bowling (
    match_id TEXT, innings INTEGER, name TEXT, overs TEXT, runs INTEGER,
    wickets INTEGER,
    PRIMARY KEY (match_id, innings, name));
CREATE TABLE IF NOT EXISTS live_fow (
    match_id TEXT, innings INTEGER, wicket INTEGER, batter TEXT, score INTEGER,
    PRIMARY KEY (match_id, innings, wicket));
`

// DBInit creates the tables.
func DBInit() {
	if err := withDB(func(tx *sql.Tx) error { _, err := tx.Exec(schema); return err }); err != nil {
		say("  ⚠  match DB init failed: %v", err)
	}
}

// Ball is one classified delivery.
type Ball struct {
	Outcome string
	Runs    int
	Extra   string // "" = none
	Wicket  bool
	Legal   bool
}

func (b Ball) extraOrNil() any {
	if b.Extra == "" {
		return nil
	}
	return b.Extra
}

func (b Ball) M() M {
	return M{"outcome": b.Outcome, "runs": b.Runs, "extra": b.extraOrNil(), "wicket": b.Wicket, "legal": b.Legal}
}

func atoiOr0(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// ClassifyBall is the overlay's classifyBall: one ticker token -> structured outcome.
func ClassifyBall(token string) Ball {
	t := strings.TrimSpace(token)
	if t == "" || t == "." || t == "0" {
		return Ball{Outcome: "dot", Legal: true}
	}
	if t == "W" {
		return Ball{Outcome: "W", Wicket: true, Legal: true}
	}
	if t == "w" {
		return Ball{Outcome: "wide", Runs: 1, Extra: "wide"}
	}
	if len(t) > 2 && t[0] == 'w' && t[1] == '+' {
		return Ball{Outcome: "wide", Runs: 1 + atoiOr0(t[2:]), Extra: "wide"}
	}
	low := strings.ToLower(t)
	if nb := strings.Index(low, "nb"); nb >= 0 {
		pre := atoiOr0(t[:nb])
		after, post := t[nb+2:], 0
		if len(after) > 1 && after[0] == '+' {
			post = atoiOr0(after[1:])
		}
		return Ball{Outcome: "noball", Runs: 1 + pre + post, Extra: "noball"}
	}
	if strings.HasSuffix(low, "lb") {
		return Ball{Outcome: "legbye", Runs: atoiOr0(t[:len(t)-2]), Extra: "legbye", Legal: true}
	}
	if strings.HasSuffix(low, "b") {
		return Ball{Outcome: "bye", Runs: atoiOr0(t[:len(t)-1]), Extra: "bye", Legal: true}
	}
	if n, err := strconv.Atoi(t); err == nil {
		return Ball{Outcome: strconv.Itoa(n), Runs: n, Legal: true}
	}
	return Ball{Outcome: "dot", Legal: true}
}

var tickerSplit = regexp.MustCompile(`[\s\x{00b7}]+`)

func parseTicker(s string) []Ball {
	if s == "" || strings.HasPrefix(s, "{{") {
		return nil
	}
	var out []Ball
	for _, t := range tickerSplit.Split(strings.TrimSpace(s), -1) {
		if t != "" {
			out = append(out, ClassifyBall(t))
		}
	}
	return out
}

// CurrentMatchID is the stable id for the match being logged: the PlayCricket match id
// if we have one, else a date+teams fallback so a non-pinned match still gets a
// consistent id all day.
func CurrentMatchID() string {
	if id := rehearsalMatchID(); id != "" {
		return id // a practice match never shares a real match's id
	}
	cfg := LoadState()
	mid := ""
	if mu := trim(gs(cfg, "match_url", "")); mu != "" {
		mid = extractPCMatchID(mu)
	} else {
		mid = trim(gsOr(cfg, "pc_match_id", ""))
	}
	if mid != "" {
		return mid
	}
	team := func(k, def string) string {
		s := strings.ReplaceAll(gsOr(cfg, k, def), " ", "")
		return truncRunes(s, 14)
	}
	return fmt.Sprintf("%s_%s_v_%s", time.Now().Format("2006-01-02"), team("home_team", "home"), team("away_team", "away"))
}

func nowISO() string { return time.Now().Format("2006-01-02T15:04:05") }

// ballLogPrev tracks the last over whose ticker we saw, so the over-completing delivery
// can be recovered. On the write that completes an over NV Play usually keeps showing
// the over as it was BEFORE its final ball, so that ball rarely appears in any ticker —
// the overlay recovers it from the score delta for the over-summary graphic, and the
// logger must do the same or the ball DB (and every CSV export) silently loses it.
type ballLogPrev struct {
	mid            string
	innings        int
	hasInnings     bool
	over           int
	hasOver        bool
	score, wickets int
	count          int
	// Personnel as of the last write we saw: on the over-completing write the feed has
	// already swapped the batters and rotated the bowler for the NEXT over, so the
	// recovered final delivery must be attributed from here, never from the current poll.
	striker, nonstriker, bowler string
	sig                         []string
	// done: (mid, innings, over, sig) of the last over that rolled on.
	doneMid     string
	doneInnings int
	doneOver    int
	doneSig     []string
	hasDone     bool
}

var (
	ballLog   ballLogPrev
	ballLogMu sync.Mutex
)

// ResetBallLog forgets the logger's memory (tests, a new match).
func ResetBallLog() {
	ballLogMu.Lock()
	ballLog = ballLogPrev{}
	ballLogMu.Unlock()
}

func sigOf(balls []Ball) []string {
	out := make([]string, len(balls))
	for i, b := range balls {
		out[i] = b.Outcome
	}
	return out
}

func hasPrefix(sig, ref []string) bool {
	if len(sig) < len(ref) {
		return false
	}
	for i := range ref {
		if sig[i] != ref[i] {
			return false
		}
	}
	return true
}

const insertBall = "INSERT OR REPLACE INTO balls(match_id,innings,over,ball,batting_team," +
	"batter,non_striker,bowler,outcome,runs,extra,is_wicket,legal," +
	"cum_runs,cum_wkts,ts) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// LogBallData captures the current over from the live ticker. Rewrites the whole
// current over each call (delete + reinsert — that's how scorer edits within an over stay
// correct; don't switch it to append); once the over rolls on it freezes — after
// recovering the over-completing ball the cleared ticker can never show. Never raises:
// logging must not affect the stream.
func LogBallData(state M) {
	defer func() { recover() }()
	ballLogMu.Lock()
	defer ballLogMu.Unlock()
	prev := &ballLog

	balls := parseTicker(gs(state, "last_ball", ""))
	innings := gi(state, "innings", 1)
	if innings == 0 {
		innings = 1
	}
	over := int(gf(state, "overs", 0))
	score := gi(state, "score", 0)
	wkts := gi(state, "wickets", 0)
	bteam := gs(state, "battingTeamName", "")
	b1, b2 := gm(state, "batter1"), gm(state, "batter2")
	bowler := gs(gm(state, "bowler"), "name", "")
	striker, nonstriker := gs(b1, "name", ""), gs(b2, "name", "")
	mid := CurrentMatchID()

	// NV Play usually does NOT update the ticker when an over completes: it keeps showing
	// the finished over as it was before the final ball, on every write, until the next
	// ball. Those balls are already logged, so read the write as a cleared ticker —
	// otherwise the final delivery reads as negative runs and is never recovered, and the
	// stale balls are logged again under the NEXT over against the next bowler. Matched
	// as a prefix so a ticker that DOES carry the final ball still counts as stale: that
	// extra ball (tail) goes into the finished over where it belongs. Only while no legal
	// ball of the new over has been bowled, and only for a ticker at least as long as the
	// logged one, so a new over's opening wide can't be mistaken for it.
	sig := sigOf(balls)
	var tail []Ball
	rolled := prev.mid == mid && prev.hasInnings && prev.innings == innings && prev.hasOver && over > prev.over
	var ref []string
	if rolled {
		ref = prev.sig
	} else if prev.hasDone && prev.doneMid == mid && prev.doneInnings == innings && prev.doneOver == over-1 {
		ref = prev.doneSig
	}
	if len(balls) > 0 && len(ref) > 0 && oversToBalls(state["overs"])%6 == 0 && hasPrefix(sig, ref) {
		if rolled {
			tail = balls[len(ref):]
		}
		balls = nil
	}

	// ── Recover the invisible over-completing delivery ──
	// The over we were logging has rolled on: whatever score/wickets moved beyond the
	// balls we HAVE seen is the final delivery (or, across a missed poll, final deliveries
	// — logged as one aggregate ball; totals stay exact even when the per-ball split is
	// unknowable).
	if rolled {
		if len(tail) > 0 {
			// The final ball(s) are in the ticker after all: log them as bowled.
			nowS := nowISO()
			run, wk := prev.score, prev.wickets
			err := withDB(func(tx *sql.Tx) error {
				for i, b := range tail {
					run += b.Runs
					wk += b2i(b.Wicket)
					if _, err := tx.Exec(insertBall, mid, innings, prev.over, prev.count+1+i, bteam,
						orStr(prev.striker, striker), orStr(prev.nonstriker, nonstriker), orStr(prev.bowler, bowler),
						b.Outcome, b.Runs, b.extraOrNil(), b2i(b.Wicket), b2i(b.Legal), run, wk, nowS); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return
			}
			prev.count += len(tail)
			prev.score, prev.wickets = run, wk
		}
		seenRuns, seenWkts := 0, 0
		for _, b := range balls {
			seenRuns += b.Runs
			seenWkts += b2i(b.Wicket)
		}
		missRuns := score - prev.score - seenRuns
		missWkts := wkts - prev.wickets - seenWkts
		if missRuns >= 0 && (missRuns > 0 || missWkts > 0 || (prev.count > 0 && len(tail) == 0)) {
			outcome := "dot"
			if missWkts > 0 {
				outcome = "W"
			} else if missRuns != 0 {
				outcome = strconv.Itoa(missRuns)
			}
			if err := withDB(func(tx *sql.Tx) error {
				_, err := tx.Exec(insertBall, mid, innings, prev.over, prev.count+1, bteam,
					orStr(prev.striker, striker), orStr(prev.nonstriker, nonstriker), orStr(prev.bowler, bowler),
					outcome, missRuns, nil, b2i(missWkts > 0), 1, prev.score+missRuns, prev.wickets+missWkts, nowISO())
				return err
			}); err != nil {
				return
			}
		}
		prev.hasDone, prev.doneMid, prev.doneInnings, prev.doneOver, prev.doneSig = true, mid, innings, prev.over, prev.sig
		prev.hasOver, prev.count, prev.score, prev.wickets = false, 0, score, wkts
	}

	if len(balls) == 0 {
		// Between overs (or pre-match): remember the baseline so the next over's recovery
		// is computed against the right score. The names on this write already belong to
		// the new over, which is what a whole-over-in-one-poll recovery would need.
		prev.mid, prev.innings, prev.hasInnings = mid, innings, true
		prev.score, prev.wickets = score, wkts
		prev.striker, prev.nonstriker, prev.bowler = striker, nonstriker, bowler
		return
	}
	overRuns := 0
	for _, b := range balls {
		overRuns += b.Runs
	}
	overStart := score - overRuns
	cfg := LoadState()
	t := nowISO()
	err := withDB(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT OR IGNORE INTO matches(match_id,date,home,away,competition,team_key,updated) VALUES(?,?,?,?,?,?,?)",
			mid, time.Now().Format("2006-01-02"), gs(cfg, "home_team", ""), gs(cfg, "away_team", ""),
			gs(cfg, "competition", ""), teamKey(bteam), t); err != nil {
			return err
		}
		// New ball data means the published aggregates may now be stale → mark for reconcile.
		if _, err := tx.Exec("UPDATE matches SET updated=?, reconciled=0 WHERE match_id=?", t, mid); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM balls WHERE match_id=? AND innings=? AND over=?", mid, innings, over); err != nil {
			return err
		}
		run := overStart
		for i, b := range balls {
			run += b.Runs
			if _, err := tx.Exec(insertBall, mid, innings, over, i+1, bteam, striker, nonstriker, bowler,
				b.Outcome, b.Runs, b.extraOrNil(), b2i(b.Wicket), b2i(b.Legal), run, wkts, t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	prev.mid, prev.innings, prev.hasInnings = mid, innings, true
	prev.over, prev.hasOver = over, true
	prev.score, prev.wickets, prev.count = score, wkts, len(balls)
	prev.striker, prev.nonstriker, prev.bowler = striker, nonstriker, bowler
	prev.sig = sigOf(balls)
}

// ReconcileMatch writes per-innings batting/bowling aggregates from PlayCricket's
// published scorecard: the trusted layer for analysis, immune to live-scoring drift.
func ReconcileMatch(matchID string) M {
	cfg := LoadState()
	key := trim(gsOr(cfg, "playcricket_api_key", gsOr(cfg, "api_token", "")))
	if key == "" {
		return M{"ok": false, "error": "No PlayCricket API key set"}
	}
	if !isDigits(matchID) {
		// CurrentMatchID's date+teams fallback: PlayCricket only answers "404".
		return M{"ok": false, "error": "This match isn't linked to a PlayCricket fixture " +
			"(use \"Fetch today's match\" first), so there's no scorecard to check against."}
	}
	det, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?api_token=%s&match_id=%s", key, matchID), 15*time.Second)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Code == 404 {
			return M{"ok": false, "error": fmt.Sprintf("PlayCricket has no match %s — check the "+
				"fixture, or try again once the scorecard is published.", matchID)}
		}
		return M{"ok": false, "error": fmt.Sprintf("PlayCricket fetch failed: %v", err)}
	}
	md := firstDetail(det)
	innings := gl(md, "innings")
	if len(innings) == 0 {
		return M{"ok": false, "error": "No published scorecard for that match yet"}
	}
	t := nowISO()
	facts := BuildMatchFactsFromPC(matchID, "")
	result := ""
	if gb(facts, "ok", false) {
		result = gs(facts, "result", "")
	}
	err = withDB(func(tx *sql.Tx) error {
		for idx, raw := range innings {
			inn, _ := raw.(map[string]any)
			if _, err := tx.Exec("INSERT OR REPLACE INTO innings_totals VALUES(?,?,?,?,?,?)",
				matchID, idx+1, gs(inn, "team_batting_name", ""), pcParseIntOr0(inn["runs"]),
				pcParseIntOr0(inn["wickets"]), gsOr(inn, "overs", "")); err != nil {
				return err
			}
			for pos, rb := range gl(inn, "bat") {
				b, _ := rb.(map[string]any)
				if _, err := tx.Exec("INSERT OR REPLACE INTO batting VALUES(?,?,?,?,?,?,?,?,?)",
					matchID, idx+1, pos+1, gs(b, "batsman_name", ""), gs(b, "how_out", ""),
					pcParseIntOr0(b["runs"]), pcParseIntOr0(b["balls"]), pcParseIntOr0(b["fours"]), pcParseIntOr0(b["sixes"])); err != nil {
					return err
				}
			}
			for _, rw := range gl(inn, "bowl") {
				w, _ := rw.(map[string]any)
				if _, err := tx.Exec("INSERT OR REPLACE INTO bowling VALUES(?,?,?,?,?,?,?)",
					matchID, idx+1, gs(w, "bowler_name", ""), gsOr(w, "overs", ""),
					pcParseIntOr0(w["maidens"]), pcParseIntOr0(w["runs"]), pcParseIntOr0(w["wickets"])); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec("UPDATE matches SET reconciled=1, result=?, updated=? WHERE match_id=?", result, t, matchID)
		return err
	})
	if err != nil {
		return M{"ok": false, "error": err.Error()}
	}
	return M{"ok": true, "innings": len(innings), "result": gs(facts, "result", "")}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// DBStatus summarises the ball database.
func DBStatus() M {
	var balls, matches int
	var recent []M
	err := withDB(func(tx *sql.Tx) error {
		if err := tx.QueryRow("SELECT COUNT(*) FROM balls").Scan(&balls); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT COUNT(*) FROM matches").Scan(&matches); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT match_id,date,home,away,reconciled FROM matches ORDER BY updated DESC LIMIT 10")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, date, home, away sql.NullString
			var rec sql.NullInt64
			rows.Scan(&id, &date, &home, &away, &rec)
			recent = append(recent, M{"match_id": id.String, "date": date.String, "home": home.String,
				"away": away.String, "reconciled": rec.Int64 != 0})
		}
		return rows.Err()
	})
	if err != nil {
		return M{"ok": false, "error": err.Error(), "balls": 0, "matches": 0, "recent": []M{}}
	}
	if recent == nil {
		recent = []M{}
	}
	return M{"ok": true, "balls": balls, "matches": matches, "recent": recent}
}

// ExportMatchCSV is the match's ball-by-ball log as CSV.
func ExportMatchCSV(matchID string) (string, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true // Python's csv module writes \r\n
	w.Write([]string{"innings", "over", "ball", "batting_team", "batter", "non_striker",
		"bowler", "outcome", "runs", "extra", "is_wicket", "legal", "cum_runs", "cum_wkts"})
	err := withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT innings,over,ball,batting_team,batter,non_striker,bowler,"+
			"outcome,runs,extra,is_wicket,legal,cum_runs,cum_wkts FROM balls "+
			"WHERE match_id=? ORDER BY innings,over,ball", matchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			vals := make([]any, 14)
			ptrs := make([]any, 14)
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			rec := make([]string, 14)
			for i, v := range vals {
				switch x := v.(type) {
				case nil:
					rec[i] = ""
				case []byte:
					rec[i] = string(x)
				default:
					rec[i] = pyval.Str(x)
				}
			}
			w.Write(rec)
		}
		return rows.Err()
	})
	w.Flush()
	return buf.String(), err
}
