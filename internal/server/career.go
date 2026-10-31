package server

// Club milestones: "Smith passes 1,000 runs for the club", "Jones: first fifty for the
// club". PlayCricket has no career endpoint, so each home player's record FOR THE CLUB
// is added up from every past scorecard of the club's sides (all of them: 1st XI,
// Sunday, youth), as far back as PlayCricket has them. The first build reads hundreds of
// scorecards, gently, in the background; they're cached on disk for good (shared with
// the head to head's cache), so later builds only fetch the latest week's.
//
// Live, LogClubMilestones checks each frame's batters and bowler against that record and
// logs a milestone once (a moment — so it's a chapter too) and tells the overlay.
//
// A claim on air has to be right, so: today's match never counts towards the record (it
// isn't in it), a player is only matched when the name is unambiguous (two records that
// could be the same person — "P Smith" and "Peter Smith", or two P Smiths — and neither
// gets a milestone; one person's runs split across two accounts would read low and
// "pass 1,000" a second time), and the strap says since when PlayCricket has the club's
// records.

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	careerFirstSeason = 2005 // PlayCricket's records don't go back further
	careerGapSeasons  = 2    // this many empty seasons in a row: the start of the club's records
	careerFreshDays   = 7    // a scorecard younger than this may still be corrected: fetched fresh
)

var (
	careerMu       sync.Mutex
	careerData     M // {"date", "since", "matches", "lookup", "error"}
	careerBuilding bool
)

// careerNow is time.Now (a test seam).
var careerNow = time.Now

type careerAcc struct {
	name                         string
	inn, runs, fifties, hundreds int
	wkts, fivefors               int
}

func (a *careerAcc) record() M {
	return M{"name": a.name, "inn": a.inn, "runs": a.runs, "fifties": a.fifties, "hundreds": a.hundreds,
		"wkts": a.wkts, "fivefors": a.fivefors}
}

// ClubCareer is the built record, or nil while there isn't one for today yet (a build
// is started in the background the first time it's asked for).
func ClubCareer() M {
	today := careerNow().Format("2006-01-02")
	careerMu.Lock()
	defer careerMu.Unlock()
	if careerData != nil && gs(careerData, "date", "") == today {
		return careerData
	}
	if !careerBuilding {
		careerBuilding = true
		safeGo("club career", func() {
			out := buildCareer(LoadState())
			careerMu.Lock()
			careerData, careerBuilding = out, false
			careerMu.Unlock()
			if e := gs(out, "error", ""); e != "" {
				say("  ⚠  Club milestones: %s", e)
			} else {
				say("  📊  Club milestones: %d players' records from %d matches since %d", len(uniqueRecords(gm(out, "lookup"))),
					gi(out, "matches", 0), gi(out, "since", 0))
			}
		})
	}
	return nil
}

var careerLetters = regexp.MustCompile(`[^A-Za-z ]`)

// buildCareer adds up every home player's batting and bowling for the club.
func buildCareer(cfg M) (out M) {
	today := careerNow()
	out = M{"date": today.Format("2006-01-02"), "lookup": M{}, "matches": 0, "since": 0}
	defer func() {
		if r := recover(); r != nil {
			out["error"] = fmt.Sprint("build crashed: ", r)
		}
	}()
	key := trim(gs(cfg, "playcricket_api_key", ""))
	site := trim(gs(cfg, "home_club_id", ""))
	if key == "" || !isDigits(site) {
		out["error"] = "no PlayCricket key or home club id"
		return out
	}
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	accs := map[string]*careerAcc{}
	var order []string
	acc := func(name string) *careerAcc {
		k := normNameKey(name)
		a := accs[k]
		if a == nil {
			a = &careerAcc{name: name}
			accs[k] = a
			order = append(order, k)
		}
		return a
	}
	matches, since, empty, failed := 0, 0, 0, 0
	for season := today.Year(); season >= careerFirstSeason; season-- {
		list, err := h2hCached(fmt.Sprintf("matches-%s-%d.json", site, season), season < today.Year(), func() (M, error) {
			pcSleep(200 * time.Millisecond)
			return pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d",
				key, site, season), 15*time.Second)
		})
		if err != nil {
			if failed++; failed > 3 {
				out["error"] = "PlayCricket: " + err.Error()
				return out
			}
			continue
		}
		played := 0
		for _, m := range toMs(gl(list, "matches")) {
			d := pcDate(gs(m, "match_date", ""))
			if d.IsZero() || !d.Before(start) {
				continue // today's match isn't part of the record it's measured against
			}
			id := anyStr(m, "id", "")
			fresh := start.Sub(d) < careerFreshDays*24*time.Hour
			det, err := h2hCached("detail-"+id+".json", !fresh, func() (M, error) {
				pcSleep(200 * time.Millisecond)
				return pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?api_token=%s&match_id=%s", key, id), 15*time.Second)
			})
			if err != nil {
				continue
			}
			if careerMatch(firstDetail(det), m, site, acc) {
				played++
			}
		}
		matches += played
		if played == 0 {
			if empty++; empty >= careerGapSeasons && season < today.Year() {
				break
			}
			continue
		}
		empty, since = 0, season
	}
	recs := make([]M, 0, len(order))
	for _, k := range order {
		recs = append(recs, accs[k].record())
	}
	out["lookup"], out["matches"], out["since"] = careerLookup(recs), matches, since
	return out
}

// careerMatch adds one scorecard's home batting and bowling; false if nobody batted.
// The club's sides are the fixture's teams whose club is ours; the fielding side's
// bowling is on the OTHER side's innings.
func careerMatch(md, fixture M, site string, acc func(string) *careerAcc) bool {
	ours := map[string]bool{}
	for _, side := range []string{"home", "away"} {
		if anyStr(fixture, side+"_club_id", "") == site {
			ours[anyStr(fixture, side+"_team_id", "")] = true
		}
	}
	batted := false
	for _, ri := range gl(md, "innings") {
		inn, _ := ri.(map[string]any)
		if ours[anyStr(inn, "team_batting_id", "")] {
			for _, rb := range gl(inn, "bat") {
				b, _ := rb.(map[string]any)
				name := trim(gs(b, "batsman_name", ""))
				switch strings.ToLower(trim(gs(b, "how_out", ""))) {
				case "", "did not bat", "dnb", "tdnb", "did_not_bat":
					continue
				}
				r, ok := pcParseInt(b["runs"])
				if name == "" || !ok {
					continue
				}
				a := acc(name)
				a.inn++
				a.runs += r
				if r >= 100 {
					a.hundreds++
				} else if r >= 50 {
					a.fifties++
				}
				batted = true
			}
			continue
		}
		for _, rw := range gl(inn, "bowl") {
			b, _ := rw.(map[string]any)
			name := trim(gs(b, "bowler_name", ""))
			w, ok := pcParseInt(b["wickets"])
			if name == "" || !ok {
				continue
			}
			a := acc(name)
			a.wkts += w
			if w >= 5 {
				a.fivefors++
			}
			batted = true
		}
	}
	return batted
}

// careerLookup files each record under its full name, its first initial + surname, and
// (only when one record has it) its surname — the season stats' keys, so the scorebar's
// names resolve the same way. A record whose initial + surname another record shares is
// left out entirely: it might be the same person under a second account (whose career
// would read low), or a namesake the scorebar can't be told apart from.
func careerLookup(recs []M) M {
	type keyed struct {
		rec                    M
		full, initSur, surname string
	}
	var ks []keyed
	initCount, surCount := map[string]int{}, map[string]int{}
	for _, r := range recs {
		toks := strings.Fields(careerLetters.ReplaceAllString(gs(r, "name", ""), " "))
		if len(toks) == 0 {
			continue
		}
		k := keyed{rec: r, full: normNameKey(gs(r, "name", "")), surname: normNameKey(toks[len(toks)-1])}
		k.initSur = k.surname
		if len(toks) >= 2 {
			k.initSur = normNameKey(toks[0][:1] + toks[len(toks)-1])
		}
		ks = append(ks, k)
		initCount[k.initSur]++
		surCount[k.surname]++
	}
	lookup := M{}
	for _, k := range ks {
		if initCount[k.initSur] > 1 {
			continue
		}
		lookup[k.full], lookup[k.initSur] = k.rec, k.rec
		if surCount[k.surname] == 1 {
			lookup[k.surname] = k.rec
		}
	}
	return lookup
}

// careerRecord is the scorebar name's club record, or nil (not found, or ambiguous).
func careerRecord(career M, name string) M {
	lk := gm(career, "lookup")
	k := nameKeys(name)
	for _, key := range []string{k.full, k.initsur, k.surname} {
		if r, ok := lk[key].(M); ok && key != "" {
			return r
		}
	}
	return nil
}

// ── Milestones ──

// runMarks and wicketMarks are the round numbers worth a strap.
func runMarks(n int) bool {
	return n > 0 && ((n <= 3000 && n%500 == 0) || n%1000 == 0)
}

func wicketMarks(n int) bool {
	return n > 0 && ((n <= 200 && n%50 == 0) || n%100 == 0)
}

// crossed is the highest mark in (before, after], or 0.
func crossed(before, after int, mark func(int) bool) int {
	for n := after; n > before; n-- {
		if mark(n) {
			return n
		}
	}
	return 0
}

func ordinal(n int) string {
	if n == 1 {
		return "first"
	}
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// clubMilestones is what this player's figures today add to their club record.
func clubMilestones(rec M, runsToday, wktsToday int, batting bool) []string {
	name := gs(rec, "name", "")
	var out []string
	if batting {
		if m := crossed(gi(rec, "runs", 0), gi(rec, "runs", 0)+runsToday, runMarks); m > 0 {
			out = append(out, fmt.Sprintf("%s passes %s runs for the club", name, thousands(m)))
		}
		if h := gi(rec, "hundreds", 0); runsToday >= 100 {
			out = append(out, fmt.Sprintf("%s: %s hundred for the club", name, ordinal(h+1)))
		} else if runsToday >= 50 && h+gi(rec, "fifties", 0) == 0 {
			out = append(out, fmt.Sprintf("%s: first fifty for the club", name))
		}
		return out
	}
	if m := crossed(gi(rec, "wkts", 0), gi(rec, "wkts", 0)+wktsToday, wicketMarks); m > 0 {
		out = append(out, fmt.Sprintf("%s: %d wickets for the club", name, m))
	}
	if wktsToday >= 5 {
		out = append(out, fmt.Sprintf("%s: %s five-for for the club", name, ordinal(gi(rec, "fivefors", 0)+1)))
	}
	return out
}

// LogClubMilestones checks this /live frame's home batters (or, while the club is in the
// field, the bowler) against their club records. Each milestone is logged once per
// match — a moment, so it's a chapter on the match video too — and handed to the
// overlay. Never raises: it sits in the match-day loop.
func LogClubMilestones(state M) {
	defer func() { recover() }()
	cfg := LoadState()
	if !gb(cfg, "graphics_club_milestones", true) {
		return
	}
	career := ClubCareer()
	if career == nil || gs(career, "error", "") != "" {
		return
	}
	team := trim(gs(state, "battingTeamName", ""))
	if team == "" {
		return
	}
	ourBat := isOurTeam(team, cfg)
	type hit struct{ key, text string }
	var hits []hit
	if ourBat {
		for _, k := range []string{"batter1", "batter2"} {
			b := gm(state, k)
			name := cleanName(gs(b, "name", ""))
			if name == "" {
				continue
			}
			lookupBy, _ := resolvePlayer(name, anyStr(b, "number", ""))
			if rec := careerRecord(career, lookupBy); rec != nil {
				for _, t := range clubMilestones(rec, gi(b, "runs", 0), 0, true) {
					hits = append(hits, hit{"club-" + t, t})
				}
			}
		}
	} else {
		bw := gm(state, "bowler")
		if name := cleanName(gs(bw, "name", "")); name != "" {
			lookupBy, _ := resolvePlayer(name, anyStr(bw, "number", ""))
			if rec := careerRecord(career, lookupBy); rec != nil {
				for _, t := range clubMilestones(rec, 0, gi(bw, "wickets", 0), false) {
					hits = append(hits, hit{"club-" + t, t})
				}
			}
		}
	}
	if len(hits) == 0 {
		return
	}
	mid, now := CurrentMatchID(), float64(momentNow().UnixNano())/1e9
	sub := ""
	if s := gi(career, "since", 0); s > 0 {
		sub = fmt.Sprintf("Club records on PlayCricket since %d", s)
	}
	for _, h := range hits {
		added := false
		withDB(func(tx *sql.Tx) error {
			r, err := tx.Exec("INSERT OR IGNORE INTO moments(match_id,key,ts,kind,text) VALUES(?,?,?,?,?)",
				mid, h.key, now, "club", h.text)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			added = n == 1
			return nil
		})
		if added {
			commandsMu.Lock()
			l, _ := commands["club_milestone"].([]any)
			commands["club_milestone"] = append(l, M{"text": h.text, "sub": sub})
			commandsMu.Unlock()
		}
	}
}
