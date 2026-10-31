package server

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
	"cricketstream/internal/winpredict"
)

// pcParseInt parses PlayCricket's numeric strings; ok is false if it isn't a number.
func pcParseInt(v any) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(pyval.Str(v)))
	return n, err == nil
}

func pcParseIntOr0(v any) int { n, _ := pcParseInt(v); return n }

// firstDetail is (det.get("match_details") or [{}])[0].
func firstDetail(det M) M {
	if l := gl(det, "match_details"); len(l) > 0 {
		if m, ok := l[0].(map[string]any); ok {
			return m
		}
	}
	return M{}
}

// pcGetJSON fetches a PlayCricket API document. Swappable for tests.
var pcGetJSON = func(u string, timeout time.Duration) (M, error) {
	return httpGetJSON(u, timeout, map[string]string{"Accept": "application/json"})
}

// aggregateSeasonBat builds a season batting lookup from PlayCricket match_detail
// payloads. Several name keys map to the same record so the overlay's short name
// ('P SMITH') can match PlayCricket's full name ('Peter Smith'): the full normalised name,
// first-initial + surname, and the surname alone only when it's unique. Each record:
// {name, inn, runs, avg, hs}; avg counts not-outs correctly; hs marks a not-out with *.
//
// keepClubs (lower-case club-name fragments) limits it to innings whose team_batting_name
// contains one: every scorecard lists BOTH teams' batsmen, so without this the pool
// balloons to every opponent ever faced and common surnames stop being unique. An innings
// with no team name is still counted, so it can never silently drop data.
func aggregateSeasonBat(details []M, keepClubs []string) M {
	type acc struct {
		name              string
		inn, no, runs, hs int
		hsNo              bool
	}
	accs := map[string]*acc{}
	var order []string
	for _, det := range details {
		md := firstDetail(det)
		for _, ri := range gl(md, "innings") {
			inn, _ := ri.(map[string]any)
			if len(keepClubs) > 0 {
				tname := strings.ToLower(gsOr(inn, "team_batting_name", ""))
				hit := false
				for _, f := range keepClubs {
					hit = hit || strings.Contains(tname, f)
				}
				if tname != "" && !hit {
					continue // the other club's innings
				}
			}
			for _, rb := range gl(inn, "bat") {
				bat, _ := rb.(map[string]any)
				nm := trim(gsOr(bat, "batsman_name", ""))
				if nm == "" {
					continue
				}
				how := strings.ToLower(trim(gsOr(bat, "how_out", "")))
				switch how {
				case "", "did not bat", "dnb", "tdnb", "did_not_bat":
					continue // didn't bat → not an innings
				}
				runs, ok := pcParseInt(bat["runs"])
				if !ok {
					continue // no score recorded
				}
				key := normNameKey(nm)
				a := accs[key]
				if a == nil {
					a = &acc{name: nm, hs: -1}
					accs[key] = a
					order = append(order, key)
				}
				a.inn++
				a.runs += runs
				notOut := strings.Contains(how, "not out") || how == "no" || strings.Contains(how, "retired hurt") ||
					strings.Contains(how, "retired not")
				if notOut {
					a.no++
				}
				// High score: higher runs wins; a tie goes to a not-out.
				if runs > a.hs || (runs == a.hs && notOut && !a.hsNo) {
					a.hs, a.hsNo = runs, notOut
				}
			}
		}
	}
	finals := map[string]M{}
	for _, k := range order {
		a := accs[k]
		avg := "—"
		if outs := a.inn - a.no; outs > 0 {
			avg = pyfmt.Fixed(pyfmt.Round(float64(a.runs)/float64(outs), 2), 2)
		}
		hs := "—"
		if a.hs >= 0 {
			hs = strconv.Itoa(a.hs)
			if a.hsNo {
				hs += "*"
			}
		}
		finals[k] = M{"name": a.name, "inn": strconv.Itoa(a.inn), "runs": a.runs, "avg": avg, "hs": hs}
	}
	// Some players have more than one PlayCricket account, so one person can show up as
	// two records ("Peter Smith" and "P Smith"). When several records land on one key,
	// keep the one with the most innings — almost always their main account. Distinct
	// full names stay separate, so the roster still tells genuine brothers apart.
	type meta struct{ full, initSur, surname string }
	metas := map[string]meta{}
	keyCands := map[string][]M{}
	var candOrder []string
	surnameCount := map[string]int{}
	add := func(k string, r M) {
		if _, ok := keyCands[k]; !ok {
			candOrder = append(candOrder, k)
		}
		keyCands[k] = append(keyCands[k], r)
	}
	letters := regexp.MustCompile(`[^A-Za-z ]`)
	for _, k := range order {
		r := finals[k]
		toks := strings.Fields(letters.ReplaceAllString(gs(r, "name", ""), " "))
		if len(toks) == 0 {
			continue
		}
		full := normNameKey(gs(r, "name", ""))
		surname := normNameKey(toks[len(toks)-1])
		initSur := surname
		if len(toks) >= 2 {
			initSur = normNameKey(toks[0][:1] + toks[len(toks)-1])
		}
		metas[k] = meta{full, initSur, surname}
		add(full, r)
		if initSur != full {
			add(initSur, r)
		}
		surnameCount[surname]++
	}
	lookup := M{}
	for _, k := range candOrder {
		best := keyCands[k][0]
		for _, r := range keyCands[k][1:] {
			if pyval.IntOr(r["inn"], 0) > pyval.IntOr(best["inn"], 0) {
				best = r
			}
		}
		lookup[k] = best
	}
	// The surname-only key only when exactly one player carries it (avoids brother mix-ups).
	for _, k := range order {
		if s := metas[k].surname; s != "" && surnameCount[s] == 1 {
			if _, ok := lookup[s]; !ok {
				lookup[s] = finals[k]
			}
		}
	}
	return lookup
}

// seasonBowlers is one club's bowling this season, one record per bowler in the order
// first seen. PlayCricket's per-innings bowling card belongs to the FIELDING side, not the
// innings' own team, so a club's bowling figures live on the OTHER innings of the same
// match.
type seasonBowler struct {
	name       string
	wkts, runs int
}

func seasonBowlers(details []M, clubFrag string) []*seasonBowler {
	if clubFrag == "" {
		return nil
	}
	accs := map[string]*seasonBowler{}
	var order []*seasonBowler
	for _, det := range details {
		innings := gl(firstDetail(det), "innings")
		if len(innings) < 2 {
			continue
		}
		for i, ri := range innings {
			inn, _ := ri.(map[string]any)
			if !strings.Contains(strings.ToLower(gsOr(inn, "team_batting_name", "")), clubFrag) {
				continue
			}
			for j, ro := range innings {
				if j == i {
					continue
				}
				other, _ := ro.(map[string]any)
				for _, rw := range gl(other, "bowl") {
					bw, _ := rw.(map[string]any)
					nm := trim(gsOr(bw, "bowler_name", ""))
					if nm == "" {
						continue
					}
					w, ok1 := pcParseInt(bw["wickets"])
					r, ok2 := pcParseInt(bw["runs"])
					if !ok1 || !ok2 {
						continue
					}
					k := normNameKey(nm)
					a := accs[k]
					if a == nil {
						a = &seasonBowler{name: nm}
						accs[k] = a
						order = append(order, a)
					}
					a.wkts += w
					a.runs += r
				}
			}
		}
	}
	return order
}

func (b *seasonBowler) record() M {
	avg := pyfmt.Float(pyfmt.Round(float64(b.runs)/float64(b.wkts), 1))
	return M{"name": b.name, "wkts": b.wkts, "runs": b.runs, "avg": avg}
}

// seasonTopBowler is one club's leading wicket-taker.
func seasonTopBowler(details []M, clubFrag string) M {
	order := seasonBowlers(details, clubFrag)
	if len(order) == 0 {
		return nil
	}
	best := order[0]
	for _, a := range order[1:] {
		if a.wkts > best.wkts || (a.wkts == best.wkts && -a.runs > -best.runs) {
			best = a
		}
	}
	if best.wkts <= 0 {
		return nil
	}
	return best.record()
}

// seasonLeaders is the home club's top three run-scorers and wicket-takers this season,
// for the overlay's season leaders panel. Ties: more runs (fewer runs conceded) first,
// then the order the players first appeared, so a rebuild never reshuffles them.
func seasonLeaders(details []M, clubFrag string) M {
	const n = 3
	runs := []any{}
	var bats []M
	// The lookup holds one record under several keys (full name, initial, surname).
	lk := aggregateSeasonBat(details, []string{clubFrag})
	for _, k := range sortedKeys(lk) {
		r, _ := lk[k].(map[string]any)
		if r == nil {
			continue
		}
		dup := false
		for _, b := range bats {
			dup = dup || mapIdentity(b) == mapIdentity(r)
		}
		if !dup && gi(r, "runs", 0) > 0 {
			bats = append(bats, r)
		}
	}
	sort.SliceStable(bats, func(i, j int) bool {
		if gi(bats[i], "runs", 0) != gi(bats[j], "runs", 0) {
			return gi(bats[i], "runs", 0) > gi(bats[j], "runs", 0)
		}
		return gs(bats[i], "name", "") < gs(bats[j], "name", "")
	})
	for i := 0; i < len(bats) && i < n; i++ {
		b := bats[i]
		runs = append(runs, M{"name": b["name"], "runs": b["runs"], "inn": b["inn"], "avg": b["avg"]})
	}
	bowl := seasonBowlers(details, clubFrag)
	sort.SliceStable(bowl, func(i, j int) bool {
		if bowl[i].wkts != bowl[j].wkts {
			return bowl[i].wkts > bowl[j].wkts
		}
		return bowl[i].runs < bowl[j].runs
	})
	wkts := []any{}
	for i := 0; i < len(bowl) && i < n && bowl[i].wkts > 0; i++ {
		wkts = append(wkts, bowl[i].record())
	}
	return M{"runs": runs, "wkts": wkts}
}

// seasonTopScorer picks the highest season-runs record from a filtered batting lookup.
func seasonTopScorer(lookup M) M {
	var best M
	// Keys share records, and Python picked the first maximum in key order; sorting the
	// keys keeps the choice deterministic.
	for _, k := range sortedKeys(lookup) {
		r, _ := lookup[k].(map[string]any)
		if best == nil || gi(r, "runs", 0) > gi(best, "runs", 0) {
			best = r
		}
	}
	if best == nil || gi(best, "runs", 0) <= 0 {
		return nil
	}
	return M{"name": best["name"], "runs": best["runs"], "inn": best["inn"], "avg": best["avg"]}
}

// ── Season stats cache ──
// PlayCricket has no per-player endpoint, so match scorecards are aggregated ONCE per day
// and every batsman is served from the cached build.
var (
	seasonStats = M{"date": nil, "lookup": M{}, "built": false, "building": false,
		"matches_used": 0, "calls": 0, "error": nil, "build_started": nil}
	seasonStatsMu         sync.Mutex
	seasonStatsLastAction string // "cache" | "fresh"
)

func seasonStatsCopy() M {
	seasonStatsMu.Lock()
	defer seasonStatsMu.Unlock()
	return shallow(seasonStats)
}

func pcIsPastMatch(m M) bool {
	d, err := time.ParseInLocation("02/01/2006", gs(m, "match_date", ""), time.Local)
	if err != nil {
		return false
	}
	y, mo, da := time.Now().Date()
	return d.Before(time.Date(y, mo, da, 0, 0, 0, 0, time.Local))
}

// pcMatchIDs is a club's completed match ids this season. One API call.
func pcMatchIDs(key, siteID, season string) (map[string]bool, error) {
	out := map[string]bool{}
	list, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%s", key, siteID, season), 15*time.Second)
	if err != nil {
		return nil, err
	}
	for _, rm := range gl(list, "matches") {
		m, _ := rm.(map[string]any)
		if pcIsPastMatch(m) && pyval.Truthy(m["id"]) {
			out[pyval.Str(m["id"])] = true
		}
	}
	return out, nil
}

var reLowerWords = regexp.MustCompile(`[^a-z ]`)

func firstWord(s string) string {
	ws := strings.Fields(reLowerWords.ReplaceAllString(strings.ToLower(s), " "))
	if len(ws) > 0 && len(ws[0]) > 2 {
		return ws[0]
	}
	return ""
}

var pcSleep = time.Sleep

// BuildSeasonStats aggregates season batting stats for BOTH the home club and today's
// opposition: each club's completed-match ids, DEDUPED (the home-vs-away fixture is in
// both lists), each scorecard fetched once, cached to disk for the day. One build per
// day, reused on restarts — gentle on the API.
func BuildSeasonStats(force bool) M {
	cfg := LoadState()
	key := trim(gs(cfg, "playcricket_api_key", ""))
	siteID := trim(gsOr(cfg, "home_club_id", ""))
	awayID := trim(gsOr(cfg, "away_club_id", ""))
	if !isDigits(awayID) {
		awayID = "" // only query the API with a numeric club id
	}
	season := strconv.Itoa(time.Now().Year())
	today := time.Now().Format("2006-01-02")
	if key == "" {
		seasonStatsMu.Lock()
		seasonStats["built"], seasonStats["building"], seasonStats["error"] = true, false, "No PlayCricket API key set"
		out := shallow(seasonStats)
		seasonStatsMu.Unlock()
		return out
	}
	seasonStatsMu.Lock()
	if !force && gb(seasonStats, "built", false) && gs(seasonStats, "date", "") == today && gs(seasonStats, "away_id", "") == awayID {
		seasonStatsLastAction = "cache"
		out := shallow(seasonStats)
		seasonStatsMu.Unlock()
		return out
	}
	if gb(seasonStats, "building", false) {
		out := shallow(seasonStats) // another thread is already building
		seasonStatsMu.Unlock()
		return out
	}
	if !force {
		if b, err := os.ReadFile(SeasonStatsCache); err == nil {
			var disk M
			if json.Unmarshal(b, &disk) == nil && gs(disk, "date", "") == today && len(gm(disk, "lookup")) > 0 &&
				gs(disk, "away_id", "") == awayID {
				disk["built"], disk["building"] = true, false
				seasonStats = disk
				seasonStatsLastAction = "cache"
				out := shallow(disk)
				seasonStatsMu.Unlock()
				return out
			}
		}
	}
	seasonStats["building"], seasonStats["build_started"], seasonStats["error"] = true, now(), nil
	seasonStatsMu.Unlock()

	// Everything below runs with "building" set and no lock held (network calls take a
	// while). The deferred recover is the fix for a real bug: an unexpected failure here
	// once left "building" stuck forever, wedging the feature until a restart.
	var result M
	func() {
		defer func() {
			if r := recover(); r != nil {
				seasonStatsMu.Lock()
				seasonStats["building"], seasonStats["build_started"] = false, nil
				seasonStats["error"] = fmt.Sprintf("build crashed: %v", r)
				result = shallow(seasonStats)
				seasonStatsMu.Unlock()
				say("  ✗  Season stats: build crashed — %v", r)
			}
		}()
		var errs []string
		calls := 0
		ids := map[string]bool{}
		if h, err := pcMatchIDs(key, siteID, season); err != nil {
			errs = append(errs, fmt.Sprintf("home list failed: %v", err))
		} else {
			calls++
			for id := range h {
				ids[id] = true
			}
		}
		// The opposition's matches too (the token isn't club-scoped, so this is allowed).
		awayOK := false
		if awayID != "" {
			if a, err := pcMatchIDs(key, awayID, season); err != nil {
				errs = append(errs, fmt.Sprintf("opposition list failed: %v", err))
			} else {
				calls++
				awayOK = true
				for id := range a {
					ids[id] = true
				}
			}
		}
		sorted := make([]string, 0, len(ids))
		for id := range ids {
			sorted = append(sorted, id)
		}
		sort.Strings(sorted)
		if len(sorted) > 160 {
			sorted = sorted[:160] // overall safety cap
		}
		var details []M
		for _, mid := range sorted {
			det, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?api_token=%s&match_id=%s", key, mid), 15*time.Second)
			if err != nil {
				continue
			}
			calls++
			details = append(details, det)
			pcSleep(200 * time.Millisecond) // be gentle on the API
		}
		// Keep only the two clubs on show (home + today's opposition), so the pool stays
		// small and surnames remain unique.
		homeFrag := firstWord(gs(cfg, "home_team", ""))
		if homeFrag == "" {
			homeFrag = "home"
		}
		awayFrag := firstWord(gs(cfg, "away_team", ""))
		var keep []string
		for _, w := range []string{homeFrag, awayFrag} {
			if w != "" {
				keep = append(keep, w)
			}
		}
		lookup := aggregateSeasonBat(details, keep)
		// The pre-game "season form" panel: each team's own top scorer/wicket-taker.
		var awayTop, awayBowl any
		if awayFrag != "" {
			if t := seasonTopScorer(aggregateSeasonBat(details, []string{awayFrag})); t != nil {
				awayTop = t
			}
			if t := seasonTopBowler(details, awayFrag); t != nil {
				awayBowl = t
			}
		}
		var homeTop, homeBowl any
		if t := seasonTopScorer(aggregateSeasonBat(details, []string{homeFrag})); t != nil {
			homeTop = t
		}
		if t := seasonTopBowler(details, homeFrag); t != nil {
			homeBowl = t
		}
		// The win predictor's league data: one compact summary per completed scorecard.
		history := []any{}
		for _, d := range details {
			if h := winpredict.InningsSummary(d); h != nil {
				history = append(history, normalise(h))
			}
		}
		var errv any
		if len(errs) > 0 {
			errv = strings.Join(errs, "; ")
		}
		result = M{"date": today, "away_id": awayID, "away_ok": awayOK, "lookup": lookup,
			"leaders":         seasonLeaders(details, homeFrag),
			"innings_history": history,
			"top_scorers":     M{"home": homeTop, "away": awayTop},
			"top_bowlers":     M{"home": homeBowl, "away": awayBowl}, "build_started": nil,
			"built": true, "building": false, "matches_used": len(details), "calls": calls, "error": errv}
		result = normalise(result).(map[string]any)
		seasonStatsMu.Lock()
		seasonStats = result
		seasonStatsLastAction = "fresh"
		seasonStatsMu.Unlock()
		if b, err := json.Marshal(result); err == nil {
			os.WriteFile(SeasonStatsCache, b, 0o644)
		}
		extra := ""
		if awayOK {
			extra = ", incl. opposition"
		}
		tail := ""
		if errv != nil {
			tail = " — " + errv.(string)
		}
		say("  📊  Season stats: %d player keys from %d matches (%d API calls%s)%s", len(lookup), len(details), calls, extra, tail)
	}()
	return result
}

// ── League table (context graphic: "win today -> move up to Nth") ──
// One API call, rebuilt at most once a day (tables only change after a result is
// entered, never mid-match).
var (
	leagueTable   = M{"date": nil, "competition_id": nil, "built": false, "error": nil, "name": "", "rows": []any{}}
	leagueTableMu sync.Mutex
)

// FetchLeagueTable is today's competition's table, cached once per day per competition.
// Deliberately tolerant of a bad/missing id: a cup or friendly fixture's competition_id
// often has no real table behind it (one returned an unrelated league's table rather
// than an error) — leagueTableHomeRow decides whether the result is USABLE.
func FetchLeagueTable(force bool) M {
	st := LoadState()
	key := trim(gs(st, "playcricket_api_key", ""))
	comp := trim(gsOr(st, "competition_id", ""))
	today := time.Now().Format("2006-01-02")
	if key == "" || comp == "" {
		leagueTableMu.Lock()
		defer leagueTableMu.Unlock()
		leagueTable = M{"date": today, "competition_id": comp, "built": true,
			"error": "No competition_id for today's match yet (set by /match/fetch, or blank on a cup/friendly fixture)",
			"name":  "", "rows": []any{}}
		return leagueTable
	}
	leagueTableMu.Lock()
	if !force && gb(leagueTable, "built", false) && gs(leagueTable, "date", "") == today && gs(leagueTable, "competition_id", "") == comp {
		out := leagueTable
		leagueTableMu.Unlock()
		return out
	}
	leagueTableMu.Unlock()
	result := func() M {
		data, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/league_table.json?division_id=%s&api_token=%s", comp, key), 15*time.Second)
		if err == nil && len(gl(data, "league_table")) == 0 {
			err = fmt.Errorf("empty league_table in response")
		}
		if err != nil {
			return M{"date": today, "competition_id": comp, "built": true,
				"error": fmt.Sprintf("league_table fetch failed: %v", err), "name": "", "rows": []any{}}
		}
		tbl, _ := gl(data, "league_table")[0].(map[string]any)
		headings := gm(tbl, "headings")
		// The "Team"/"Pts"/"P" columns vary by competition (one non-league table had no
		// points column at all) — found by heading text, not a fixed column number.
		find := func(label string) string {
			for _, k := range sortedKeys(headings) {
				if strings.ToLower(trim(pyval.Str(headings[k]))) == label {
					return k
				}
			}
			return ""
		}
		teamCol := find("team")
		if teamCol == "" {
			teamCol = "column_1"
		}
		ptsCol, playedCol := find("pts"), find("p")
		rows := []any{}
		for _, rv := range gl(tbl, "values") {
			row, _ := rv.(map[string]any)
			r := M{"position": valOr(row, "position", ""), "team": valOr(row, teamCol, ""), "points": "", "played": ""}
			if ptsCol != "" {
				r["points"] = valOr(row, ptsCol, "")
			}
			if playedCol != "" {
				r["played"] = valOr(row, playedCol, "")
			}
			rows = append(rows, r)
		}
		return M{"date": today, "competition_id": comp, "built": true, "error": nil, "name": gs(tbl, "name", ""), "rows": rows}
	}()
	leagueTableMu.Lock()
	leagueTable = result
	leagueTableMu.Unlock()
	return result
}

// leagueTableHomeRow says which row (if any) is the home club — and so whether the table
// is USABLE for the "win today" graphic. A table fetched successfully can still be the
// WRONG table; the only reliable check is the home club's own name appearing in it,
// matched on the name's first significant word.
func leagueTableHomeRow(table M) M {
	if table == nil {
		leagueTableMu.Lock()
		table = leagueTable
		leagueTableMu.Unlock()
	}
	rows := gl(table, "rows")
	home := trim(gs(LoadState(), "home_team", ""))
	if len(rows) == 0 || home == "" {
		return nil
	}
	words := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(home, " CC", ""), " Cricket Club", ""))
	if len(words) == 0 {
		return nil
	}
	key := strings.ToLower(words[0])
	for i, rr := range rows {
		row, _ := rr.(map[string]any)
		if strings.Contains(strings.ToLower(pyval.Str(row["team"])), key) {
			var above any
			if i > 0 {
				above = rows[i-1]
			}
			return M{"index": i, "row": row, "row_above": above}
		}
	}
	return nil
}
