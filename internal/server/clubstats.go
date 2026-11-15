package server

// The club stats table: every player's figures for the club across all of PlayCricket's
// history, for the clubhouse screen's Stats tab. It's the club record the milestones use
// (career.go), kept apart by club (a merged club's former clubs, [Club] former_ids) and by
// kind of side (men's, ladies', junior, girls'), so the screen can filter by either and add
// up whatever's chosen. Names are joined exactly as the milestones join them: one row per
// person the record knows, under the name it goes by.
//
// Full names, juniors too: they're on PlayCricket's public scorecards already (the club's
// choice, 2026-11-15). Only our sides' rows are counted, never the opposition's.

import (
	"regexp"
	"sort"
	"strings"
)

// The kinds of side. A girls' side is junior and ladies' both.
const (
	sideMen    = "m"
	sideLadies = "l"
	sideJunior = "j"
	sideGirls  = "g"
)

var reLadies = regexp.MustCompile(`(?i)\b(women'?s?|ladies|ladies'|girls'?|female)\b`)

// clubStatsFields are the figures in each of a player's entries, in order.
var clubStatsFields = []string{"apps", "inn", "no", "runs", "hs", "hs_no", "fifties", "hundreds",
	"balls", "b_runs", "wkts", "fivefors", "best_w", "best_r", "ct", "st", "ro"}

type statsTally struct {
	site    string                           // the club itself; any other club is a former one
	accs    map[string]map[string]*careerAcc // side bucket (club:kind) → name key → figures
	sheets  [][]string                       // each match's team sheet...
	buckets []string                         // ...and its bucket
	games   map[string]int                   // matches in each bucket
	clubs   map[string]string                // club id → its name on the fixtures
	order   []string                         // club ids as first seen
}

func newStatsTally(site string) *statsTally {
	return &statsTally{site: site, accs: map[string]map[string]*careerAcc{}, games: map[string]int{}, clubs: map[string]string{}}
}

// sideKind: which kind of side ours was in this fixture, from our team's name and the
// competition's.
func sideKind(fixture M, club string) string {
	var names []string
	for _, side := range []string{"home", "away"} {
		if anyStr(fixture, side+"_club_id", "") == club {
			names = append(names, gs(fixture, side+"_team_name", ""))
		}
	}
	names = append(names, gs(fixture, "competition_name", ""), gs(fixture, "league_name", ""))
	junior := LooksJunior(names...)
	ladies := reLadies.MatchString(strings.Join(names, " | "))
	switch {
	case junior && ladies:
		return sideGirls
	case junior:
		return sideJunior
	case ladies:
		return sideLadies
	}
	return sideMen
}

// match adds one played match's figures to its bucket.
func (t *statsTally) match(md, fixture M, club string) {
	if t == nil {
		return
	}
	defer func() { recover() }() // a scorecard the table can't read never costs the milestones
	if _, ok := t.clubs[club]; !ok {
		t.order = append(t.order, club)
		t.clubs[club] = ""
	}
	for _, side := range []string{"home", "away"} {
		if anyStr(fixture, side+"_club_id", "") == club && t.clubs[club] == "" {
			t.clubs[club] = trim(gs(fixture, side+"_club_name", ""))
		}
	}
	b := club + ":" + sideKind(fixture, club)
	raw := t.accs[b]
	if raw == nil {
		raw = map[string]*careerAcc{}
		t.accs[b] = raw
	}
	careerMatch(md, fixture, club, func(name string) *careerAcc {
		k := normNameKey(name)
		if raw[k] == nil {
			raw[k] = &careerAcc{name: name}
		}
		return raw[k]
	})
	var sheet []string
	for _, list := range careerTeamLists(md, fixture, club) {
		sheet = append(sheet, list...)
	}
	t.sheets = append(t.sheets, sheet)
	t.buckets = append(t.buckets, b)
	t.games[b]++
}

// table is what /club/stats serves: one row per player the record knows (after
// careerMerge has joined their names), each with their figures in every bucket they
// played in. accs and sheets are the whole record's, so a game's sheet finds players the
// way the appearances on the player card do.
func (t *statsTally) table(accs map[string]*careerAcc, sheets [][]string) (out M) {
	out = M{"fields": clubStatsFields, "clubs": []M{}, "players": []M{}, "games": M{}}
	if t == nil {
		return out
	}
	defer func() {
		if r := recover(); r != nil {
			out = M{"fields": clubStatsFields, "clubs": []M{}, "players": []M{}, "games": M{}}
		}
	}()
	per := map[*careerAcc]map[string]*careerAcc{}
	get := func(person *careerAcc, b string) *careerAcc {
		if per[person] == nil {
			per[person] = map[string]*careerAcc{}
		}
		if per[person][b] == nil {
			per[person][b] = &careerAcc{name: person.name}
		}
		return per[person][b]
	}
	for b, raw := range t.accs {
		for k, a := range raw {
			if person := accs[k]; person != nil {
				get(person, b).add(a)
			}
		}
	}
	find := careerAppFinder(accs, sheets)
	for i, sheet := range t.sheets {
		seen := map[*careerAcc]bool{}
		for _, name := range sheet {
			if person := find(trim(name)); person != nil && !seen[person] {
				seen[person] = true
				get(person, t.buckets[i]).apps++
			}
		}
	}
	var players []M
	for person, buckets := range per {
		row := M{}
		for b, a := range buckets {
			if a.apps == 0 && a.inn == 0 && a.balls == 0 && a.wkts == 0 && a.ct+a.st+a.ro == 0 {
				continue
			}
			hsNO, bestW, bestR := 0, 0, 0
			if a.hsNotOut {
				hsNO = 1
			}
			if a.hasBest {
				bestW, bestR = a.bestW, a.bestR
			} else {
				bestW = -1
			}
			row[b] = []int{a.apps, a.inn, a.no, a.runs, a.hs, hsNO, a.fifties, a.hundreds,
				a.balls, a.bRuns, a.wkts, a.fivefors, bestW, bestR, a.ct, a.st, a.ro}
		}
		if len(row) > 0 {
			players = append(players, M{"name": person.name, "b": row})
		}
	}
	sort.Slice(players, func(i, j int) bool { return gs(players[i], "name", "") < gs(players[j], "name", "") })
	clubs := []M{}
	for _, id := range t.order {
		clubs = append(clubs, M{"id": id, "name": t.clubs[id], "former": id != t.site})
	}
	games := M{}
	for b, n := range t.games {
		games[b] = n
	}
	out["clubs"], out["players"], out["games"] = clubs, players, games
	return out
}

// ClubStats is /club/stats: the table, or {"ready": false} while the club record is being
// built (the first build of a day reads the latest week's scorecards, the first ever reads
// them all and takes a few minutes).
func ClubStats() M {
	career := ClubCareer()
	if career == nil {
		return M{"ready": false}
	}
	if e := gs(career, "error", ""); e != "" {
		return M{"ready": true, "error": e}
	}
	st := gm(career, "stats")
	out := M{"ready": true, "as_of": career["date"], "since": career["since"], "matches": career["matches"]}
	for _, k := range []string{"fields", "clubs", "players", "games"} {
		out[k] = st[k]
	}
	return out
}
