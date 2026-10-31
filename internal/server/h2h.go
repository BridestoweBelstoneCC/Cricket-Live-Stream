package server

// Head to head: "the last time we played them", from PlayCricket — something PlayCricket's
// own match page doesn't put in front of a viewer. Today's two TEAMS (our 1st XI v their
// 1st XI, not any two sides of the clubs), looked for in the last few seasons' fixture
// lists; the most recent meetings' scorecards give the record and the last game's story.
//
// Gentle on the API: a past season's fixture list and a finished match's scorecard never
// change, so both are cached on disk for good; the built answer is kept for the day.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// h2hSeasons is how far back meetings are looked for; h2hMeetings how many of the most
// recent are read in full.
const (
	h2hSeasons  = 6
	h2hMeetings = 5
)

// h2hCacheDir holds the cached lists and scorecards, beside the app (tests: their sandbox).
func h2hCacheDir() string { return filepath.Join(appDir, ".h2h_cache") }

var (
	h2hMu       sync.Mutex
	h2hResult   M
	h2hKey      string
	h2hBuilding bool
)

// h2hNow is time.Now (a test seam).
var h2hNow = time.Now

// HeadToHead is today's head to head: {"ready": false} while it's being built (in the
// background; ask again in a few seconds), then {"ready": true, "usable": ...}.
func HeadToHead(force bool) M {
	cfg := LoadState()
	key := strings.Join([]string{trim(gs(cfg, "home_club_id", "")), trim(gs(cfg, "away_club_id", "")),
		trim(gs(cfg, "pc_match_id", "")), h2hNow().Format("2006-01-02")}, "|")
	h2hMu.Lock()
	defer h2hMu.Unlock()
	if force {
		h2hResult = nil // asked for afresh: the old answer isn't given out meanwhile
	}
	if h2hKey == key && h2hResult != nil {
		return h2hResult
	}
	if !h2hBuilding {
		h2hBuilding = true
		go func() {
			var out M
			func() {
				defer func() {
					if r := recover(); r != nil {
						out = M{"ready": true, "usable": false, "reason": fmt.Sprint(r)}
					}
				}()
				out = buildH2H(cfg)
			}()
			h2hMu.Lock()
			h2hResult, h2hKey, h2hBuilding = out, key, false
			h2hMu.Unlock()
		}()
	}
	return M{"ready": false}
}

func h2hCached(name string, keep bool, fetch func() (M, error)) (M, error) {
	p := filepath.Join(h2hCacheDir(), name)
	if keep {
		if b, err := os.ReadFile(p); err == nil {
			var m M
			if json.Unmarshal(b, &m) == nil {
				return m, nil
			}
		}
	}
	m, err := fetch()
	if err != nil {
		return nil, err
	}
	if keep {
		os.MkdirAll(h2hCacheDir(), 0o755)
		if b, err := json.Marshal(m); err == nil {
			os.WriteFile(p, b, 0o644)
		}
	}
	return m, nil
}

func pcDate(s string) time.Time {
	t, _ := time.ParseInLocation("02/01/2006", s, time.Local)
	return t
}

func buildH2H(cfg M) M {
	key := trim(gs(cfg, "playcricket_api_key", ""))
	site := trim(gs(cfg, "home_club_id", ""))
	opp := trim(gs(cfg, "away_club_id", ""))
	if key == "" || site == "" {
		return M{"ready": true, "usable": false, "reason": "No PlayCricket key or club ID."}
	}
	if !isDigits(opp) {
		return M{"ready": true, "usable": false, "reason": "Today's opposition isn't known — fetch today's match first."}
	}
	today := h2hNow()
	year := today.Year()
	list := func(season int) ([]M, error) {
		// This season's list changes (results, new fixtures): fetched fresh, once a build.
		m, err := h2hCached(fmt.Sprintf("matches-%s-%d.json", site, season), season < year, func() (M, error) {
			return pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d",
				key, site, season), 15*time.Second)
		})
		if err != nil {
			return nil, err
		}
		return toMs(gl(m, "matches")), nil
	}
	cur, err := list(year)
	if err != nil {
		return M{"ready": true, "usable": false, "reason": "PlayCricket: " + err.Error()}
	}
	// Today's teams, from today's fixture; without it, any of the two clubs' sides.
	ours, theirs := "", ""
	if pcid := trim(gs(cfg, "pc_match_id", "")); pcid != "" {
		for _, m := range cur {
			if anyStr(m, "id", "") != pcid {
				continue
			}
			if anyStr(m, "home_club_id", "") == site {
				ours, theirs = anyStr(m, "home_team_id", ""), anyStr(m, "away_team_id", "")
			} else {
				ours, theirs = anyStr(m, "away_team_id", ""), anyStr(m, "home_team_id", "")
			}
		}
	}
	isMeeting := func(m M) bool {
		hc, ac := anyStr(m, "home_club_id", ""), anyStr(m, "away_club_id", "")
		if !((hc == site && ac == opp) || (hc == opp && ac == site)) {
			return false
		}
		if ours != "" && theirs != "" {
			ht, at := anyStr(m, "home_team_id", ""), anyStr(m, "away_team_id", "")
			return (ht == ours && at == theirs) || (ht == theirs && at == ours)
		}
		return true
	}
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	var meetings []M
	for season := year; season > year-h2hSeasons; season-- {
		ms := cur
		if season != year {
			if ms, err = list(season); err != nil {
				continue // one missing season doesn't sink the rest
			}
			pcSleep(200 * time.Millisecond)
		}
		for _, m := range ms {
			if d := pcDate(gs(m, "match_date", "")); !d.IsZero() && d.Before(start) && isMeeting(m) {
				meetings = append(meetings, m)
			}
		}
	}
	sort.SliceStable(meetings, func(i, j int) bool {
		return pcDate(gs(meetings[i], "match_date", "")).After(pcDate(gs(meetings[j], "match_date", "")))
	})
	since := year - h2hSeasons + 1
	base := M{"ready": true, "since": since, "teams": ours != "", "them": gsOr(cfg, "away_team", "the opposition"),
		"us": gsOr(cfg, "home_team", "Home CC")}
	if len(meetings) == 0 {
		base["usable"], base["reason"] = false, fmt.Sprintf("No meetings on PlayCricket since %d.", since)
		return base
	}
	if len(meetings) > h2hMeetings {
		meetings = meetings[:h2hMeetings]
	}
	record := M{"played": 0, "won": 0, "lost": 0, "drawn": 0, "tied": 0, "no_result": 0}
	var games []M
	var last M
	for _, m := range meetings {
		id := anyStr(m, "id", "")
		det, err := h2hCached("detail-"+id+".json", true, func() (M, error) {
			return pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?api_token=%s&match_id=%s", key, id), 15*time.Second)
		})
		if err != nil {
			continue
		}
		g := h2hGame(firstDetail(det), m, site)
		if g == nil {
			continue
		}
		record["played"] = record["played"].(int) + 1
		k := map[string]string{"W": "won", "L": "lost", "D": "drawn", "T": "tied"}[gs(g, "outcome", "")]
		if k == "" {
			k = "no_result"
		}
		record[k] = record[k].(int) + 1
		games = append(games, M{"date": g["date"], "outcome": g["outcome"], "result": g["result"]})
		if last == nil {
			last = g
		}
	}
	if last == nil {
		base["usable"], base["reason"] = false, "PlayCricket didn't return the scorecards of the last meetings."
		return base
	}
	base["usable"], base["record"], base["meetings"], base["last"] = true, record, games, last
	return base
}

// h2hGame is one meeting from our side: when and where, the result, and each side's
// total, top scorer and best bowler. nil for a game with no scorecard (abandoned before
// a ball, or not entered).
func h2hGame(md, fixture M, site string) M {
	ourTeams := map[string]bool{}
	for _, side := range []string{"home", "away"} {
		if anyStr(fixture, side+"_club_id", "") == site {
			ourTeams[anyStr(fixture, side+"_team_id", "")] = true
		}
	}
	res := strings.ToUpper(trim(gs(md, "result", "")))
	applied := anyStr(md, "result_applied_to", "")
	outcome := "N"
	switch res {
	case "W":
		outcome = "L"
		if ourTeams[applied] {
			outcome = "W"
		}
	case "L":
		outcome = "W"
		if ourTeams[applied] {
			outcome = "L"
		}
	case "D", "T":
		outcome = res
	}
	// Each side: its first innings' total and top scorer, and its best bowler — whose
	// figures are in the OTHER side's innings (the bowling list belongs to the fielders).
	sides := map[string]M{}
	side := func(who string) M {
		if sides[who] == nil {
			sides[who] = M{}
		}
		return sides[who]
	}
	for _, ri := range gl(md, "innings") {
		inn, _ := ri.(map[string]any)
		bat, field := "their", "our"
		if ourTeams[anyStr(inn, "team_batting_id", "")] {
			bat, field = "our", "their"
		}
		if _, done := side(bat)["total"]; done {
			continue // a second innings (a two-day game): the first tells the story
		}
		total := anyStr(inn, "runs", "0")
		if w := anyStr(inn, "wickets", "0"); w != "10" {
			total += "/" + w
		}
		s := side(bat)
		s["team"], s["total"], s["overs"] = gs(inn, "team_batting_name", ""), total, anyStr(inn, "overs", "")
		topRuns := -1
		for _, rb := range gl(inn, "bat") {
			b, _ := rb.(map[string]any)
			name := trim(gs(b, "batsman_name", ""))
			if r, ok := pcParseInt(b["runs"]); ok && r > topRuns && name != "" {
				topRuns = r
				s["top_bat"] = fmt.Sprintf("%s %d", name, r)
				if strings.Contains(strings.ToLower(gs(b, "how_out", "")), "not out") {
					s["top_bat"] = fmt.Sprintf("%s %d*", name, r)
				}
			}
		}
		bw, br := -1, 0
		for _, rb := range gl(inn, "bowl") {
			b, _ := rb.(map[string]any)
			name := trim(gs(b, "bowler_name", ""))
			w, _ := pcParseInt(b["wickets"])
			r, _ := pcParseInt(b["runs"])
			if name != "" && (w > bw || (w == bw && r < br)) {
				bw, br = w, r
				side(field)["best_bowl"] = fmt.Sprintf("%s %d-%d", name, w, r)
			}
		}
	}
	if len(sides) == 0 && outcome == "N" {
		return nil
	}
	return M{"date": gs(fixture, "match_date", ""), "ground": gs(fixture, "ground_name", ""),
		"competition": gs(fixture, "competition_name", ""), "outcome": outcome,
		"result": trim(gs(md, "result_description", "")), "our": sides["our"], "their": sides["their"]}
}
