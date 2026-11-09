package server

// Club milestones: "Smith passes 1,000 runs for the club", "Jones: first fifty for the
// club". PlayCricket has no career endpoint, so each home player's record FOR THE CLUB
// is added up from every past scorecard of the club's sides (all of them: 1st XI,
// Sunday, youth), as far back as PlayCricket has them. The first build reads hundreds of
// scorecards, gently, in the background; they're cached on disk for good (shared with
// the head to head's cache), so later builds only fetch the latest week's. A merged club
// lists the clubs it was formed from in [Club] former_ids, and their scorecards count too.
//
// Live, LogClubMilestones checks each frame's batters and bowler against that record and
// logs a milestone once (a moment — so it's a chapter too) and tells the overlay.
//
// A claim on air has to be right, so: today's match never counts towards the record (it
// isn't in it), the strap says since when PlayCricket has the club's records, and names are
// only joined up when they can only be one person (careerMerge). PlayCricket's player ids
// don't help: on real scorecards one man had four, because old cards were entered with
// throwaway ones. A bare initial or surname that could be two people matches nobody.
//
// The same scorecards give the player card its appearances. A game counts for everyone on
// our team list as well as everyone in our batting and bowling rows (a fielder who neither
// batted nor bowled still played), but the team list only ever adds to a record: it can't
// start one, so a different spelling there can't make a name look ambiguous and take a
// player's milestones away.

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
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
	ct, st, ro                   int // catches, stumpings, run outs in the field
	apps                         int
}

func (a *careerAcc) record() M {
	return M{"name": a.name, "inn": a.inn, "runs": a.runs, "fifties": a.fifties, "hundreds": a.hundreds,
		"wkts": a.wkts, "fivefors": a.fivefors, "ct": a.ct, "st": a.st, "ro": a.ro, "apps": a.apps}
}

// fieldingHow is a dismissal's kind in the field: "ct", "st", "ro" or "" (nobody in the field
// took it). PlayCricket says "ct", "st" and "run out"; NV Play's feed spells them out. A
// caught and bowled is the bowler's catch.
func fieldingHow(how string) string {
	h := strings.ToLower(trim(how))
	switch {
	case h == "ct" || h == "c" || h == "c&b" || h == "c & b" || strings.HasPrefix(h, "caught"):
		return "ct"
	case h == "st" || strings.HasPrefix(h, "stumped"):
		return "st"
	case h == "ro" || h == "run out" || h == "runout" || strings.HasPrefix(h, "run out"):
		return "ro"
	}
	return ""
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
	matches, since, failed := 0, 0, 0
	var sheets, lists [][]string
	for i, club := range careerClubs(cfg, site) {
		// A former club has nothing since it stopped, so its gap only counts once its
		// matches have started. Ours starts at today: two empty seasons is its records' start.
		empty, counting := 0, i == 0
		for season := today.Year(); season >= careerFirstSeason; season-- {
			list, err := h2hCached(fmt.Sprintf("matches-%s-%d.json", club, season), season < today.Year(), func() (M, error) {
				pcSleep(200 * time.Millisecond)
				return pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d",
					key, club, season), 15*time.Second)
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
				if md := firstDetail(det); careerMatch(md, m, club, acc) {
					played++
					var sheet []string
					for _, list := range careerTeamLists(md, m, club) {
						sheet = append(sheet, list...)
						lists = append(lists, list)
					}
					sheets = append(sheets, sheet)
				}
			}
			matches += played
			if played == 0 {
				if empty++; counting && empty >= careerGapSeasons && season < today.Year() {
					break
				}
				continue
			}
			empty, counting = 0, true
			if since == 0 || season < since {
				since = season
			}
		}
	}
	careerMerge(accs, sheets, lists, careerSamePlayers(cfg))
	careerCountApps(accs, sheets)
	recs := make([]M, 0, len(order))
	recOf := map[*careerAcc]M{}
	for _, k := range order {
		if a := accs[k]; recOf[a] == nil {
			recOf[a] = a.record()
			recs = append(recs, recOf[a])
		}
	}
	lookup := careerLookup(recs)
	// A joined spelling finds its player too: the scorebar or the roster may use it.
	for k, a := range accs {
		if rec := recOf[a]; rec != nil && lookup[k] == nil && k != normNameKey(a.name) {
			lookup[k] = rec
		}
	}
	out["lookup"], out["matches"], out["since"] = lookup, matches, since
	return out
}

// careerClubs is the club and, after it, any former clubs it was formed from ([Club]
// former_ids): a merged club's scorecards before the merger are under the old clubs' ids.
func careerClubs(cfg M, site string) []string {
	clubs := []string{site}
	seen := map[string]bool{site: true}
	for _, id := range strings.FieldsFunc(gs(cfg, "former_club_ids", ""), func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if isDigits(id) && !seen[id] {
			seen[id] = true
			clubs = append(clubs, id)
		}
	}
	return clubs
}

// careerMatch adds one scorecard's home batting, bowling and fielding; false if nobody batted.
// The club's sides are the fixture's teams whose club is ours; the fielding side's
// bowling and catches are on the OTHER side's innings. A dismissal with no fielder named
// (or "Unsure") isn't anyone's.
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
				if careerNobody(name) {
					batted = true
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
			if careerNobody(name) {
				batted = true
				continue
			}
			a := acc(name)
			a.wkts += w
			if w >= 5 {
				a.fivefors++
			}
			batted = true
		}
		for _, rb := range gl(inn, "bat") {
			b, _ := rb.(map[string]any)
			name := trim(gs(b, "fielder_name", ""))
			how := fieldingHow(gs(b, "how_out", ""))
			if name == "" || how == "" || careerNobody(name) {
				continue
			}
			switch a := acc(name); how {
			case "ct":
				a.ct++
			case "st":
				a.st++
			case "ro":
				a.ro++
			}
		}
	}
	return batted
}

// careerNobody is PlayCricket's stand-in for a player the scorer couldn't name.
func careerNobody(name string) bool { return strings.EqualFold(trim(name), "unsure") }

// careerNicknames are the everyday first names that don't start with the formal one (Dan
// and Daniel need no entry).
var careerNicknames = map[string]string{
	"tom": "thomas", "tommy": "thomas", "jim": "james", "jimmy": "james", "bill": "william", "billy": "william",
	"bob": "robert", "bobby": "robert", "mike": "michael", "andy": "andrew", "tony": "anthony", "rick": "richard",
	"dick": "richard", "steve": "stephen", "joe": "joseph", "joey": "joseph", "nick": "nicholas", "nicky": "nicholas",
}

// careerOwnNames start like a shorter name but are someone else's: a family club can have a
// Sam and a Samantha. "jo" and "al" are too short to say whose they are.
var careerOwnNames = map[string]bool{
	"danielle": true, "daniella": true, "samantha": true, "christine": true, "christina": true, "alexandra": true,
	"alexa": true, "philippa": true, "josephine": true, "joanna": true, "joanne": true, "charlotte": true,
	"patricia": true, "andrea": true, "georgina": true, "georgia": true, "stephanie": true, "nicola": true,
	"michelle": true, "roberta": true, "louise": true, "louisa": true, "jo": true, "al": true,
}

// careerSameFirst says two first names can be one person's. Steven's spelling is the one
// formal name that varies enough to need it.
func careerSameFirst(a, b string) bool {
	if a != b && len(a) > 1 && len(b) > 1 && (careerOwnNames[a] || careerOwnNames[b]) {
		return false
	}
	norm := func(s string) string {
		if f := careerNicknames[s]; f != "" {
			return f
		}
		if s == "steven" {
			return "stephen"
		}
		return s
	}
	a, b = norm(a), norm(b)
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// careerMerge joins the names that can only be one person, so a record holds their whole
// career: old scorecards say "J Hale" or "Dan Reed" where later ones say "James Hale" and
// "Daniel Reed". Two names on one list (lists: a team list, an innings' batting or bowling)
// are two people, whatever they look like. The same card's different lists don't say that: real
// cards have "Thomas Marsh" in the team and "T Marsh" batting. Full names join when their
// first names fit (careerSameFirst). A bare initial joins a full name only if
// that's the one it could be, and the full name has played at least as often (otherwise
// "A Nash" might be someone else who only ever appears that way). Anything else stays
// apart, which at worst reads a little low. same is the club's own word ([Club] same_players)
// and overrides all of that. Afterwards accs maps each joined name's key to the one record.
func careerMerge(accs map[string]*careerAcc, sheets, lists, same [][]string) {
	careerJoinNamed(accs, same)
	listed := careerFirstNames(lists)
	games := map[string]int{}
	apart := map[[2]string]bool{}
	pair := func(a, b string) [2]string {
		if a > b {
			a, b = b, a
		}
		return [2]string{a, b}
	}
	for _, sheet := range sheets {
		seen := map[string]bool{}
		for _, name := range sheet {
			if k := normNameKey(trim(name)); accs[k] != nil && !seen[k] {
				seen[k] = true
				games[k]++
			}
		}
	}
	for _, list := range lists {
		var keys []string
		seen := map[string]bool{}
		for _, name := range list {
			if k := normNameKey(trim(name)); accs[k] != nil && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		for i, a := range keys {
			for _, b := range keys[i+1:] {
				apart[pair(a, b)] = true
			}
		}
	}
	type member struct{ key, first string }
	groups := map[string][]member{}
	for k, a := range accs {
		if init, first := careerInitSur(a.name); first != "" && k == normNameKey(a.name) {
			groups[init] = append(groups[init], member{k, first})
		}
	}
	for _, group := range groups {
		// Most games first, so a joined name becomes the player's usual one.
		sort.Slice(group, func(i, j int) bool {
			if games[group[i].key] != games[group[j].key] {
				return games[group[i].key] > games[group[j].key]
			}
			return group[i].key < group[j].key
		})
		type person struct {
			first string
			keys  []string
		}
		var people []*person
		canJoin := func(p *person, k string) bool {
			for _, m := range p.keys {
				if apart[pair(m, k)] {
					return false
				}
			}
			return true
		}
		join := func(p *person, k string) {
			accs[p.keys[0]].add(accs[k])
			accs[k] = accs[p.keys[0]]
			p.keys = append(p.keys, k)
		}
		for _, m := range group {
			if len(m.first) == 1 {
				continue
			}
			var fits *person
			for _, p := range people {
				if careerSameFirst(m.first, p.first) && canJoin(p, m.key) {
					fits = p
					break
				}
			}
			if fits != nil {
				join(fits, m.key)
			} else {
				people = append(people, &person{m.first, []string{m.key}})
			}
		}
		for _, m := range group {
			if len(m.first) != 1 {
				continue
			}
			var fits []*person
			for _, p := range people {
				if canJoin(p, m.key) {
					fits = append(fits, p)
				}
			}
			if len(people) == 1 && len(fits) == 1 && games[fits[0].keys[0]] >= games[m.key] &&
				careerOnlyFirst(listed, accs[m.key].name, fits[0].first) {
				join(fits[0], m.key)
			}
		}
	}
}

// add puts another record's figures into this one.
func (a *careerAcc) add(from *careerAcc) {
	a.inn += from.inn
	a.runs += from.runs
	a.fifties += from.fifties
	a.hundreds += from.hundreds
	a.wkts += from.wkts
	a.fivefors += from.fivefors
	a.ct += from.ct
	a.st += from.st
	a.ro += from.ro
}

// careerJoinNamed applies [Club] same_players. The first name of each group is the one the
// record goes by. A name with no record of its own (only ever on a team list) still counts.
func careerJoinNamed(accs map[string]*careerAcc, same [][]string) {
	for _, names := range same {
		var into *careerAcc
		for _, n := range names {
			if a := accs[normNameKey(n)]; a != nil {
				into = a
				break
			}
		}
		if into == nil {
			continue
		}
		into.name = names[0]
		for _, n := range names {
			k := normNameKey(n)
			from := accs[k]
			if from != nil && from != into {
				into.add(from)
				for other, a := range accs {
					if a == from {
						accs[other] = into
					}
				}
			}
			accs[k] = into
		}
	}
}

// careerSamePlayers reads [Club] same_players: "Ian Ward: I Ward, Iam Ward | ...".
func careerSamePlayers(cfg M) [][]string {
	var out [][]string
	for _, group := range strings.Split(gs(cfg, "same_players", ""), "|") {
		head, rest, _ := strings.Cut(group, ":")
		names := []string{}
		for _, n := range append([]string{head}, strings.Split(rest, ",")...) {
			if n = trim(n); n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 1 {
			out = append(out, names)
		}
	}
	return out
}

// careerFirstNames is every first name seen for each initial + surname, including people on
// a team list who never batted or bowled, so they have no record of their own.
func careerFirstNames(lists [][]string) map[string]map[string]bool {
	seen := map[string]map[string]bool{}
	for _, list := range lists {
		for _, n := range list {
			init, first := careerInitSur(trim(n))
			if len(first) > 1 && !careerNobody(n) {
				if seen[init] == nil {
					seen[init] = map[string]bool{}
				}
				seen[init][first] = true
			}
		}
	}
	return seen
}

// careerOnlyFirst says a bare initial can only mean this first name: with a Charles and a
// Chris Hale on the club's team lists, "C Hale" could be either, even if Chris never batted.
func careerOnlyFirst(seen map[string]map[string]bool, initialName, first string) bool {
	init, _ := careerInitSur(initialName)
	for other := range seen[init] {
		if !careerSameFirst(other, first) {
			return false
		}
	}
	return true
}

// careerTeamLists is the lists of names a played match has for our side: each side's team
// list, then each innings' batting rows (a "did not bat" still played) or the bowling on the
// other side's. Old scorecards often have no team list, which is why it can't be that alone.
func careerTeamLists(md, fixture M, site string) [][]string {
	ours := map[string]bool{}
	var lists [][]string
	for _, side := range []string{"home", "away"} {
		if anyStr(fixture, side+"_club_id", "") != site {
			continue
		}
		ours[anyStr(fixture, side+"_team_id", "")] = true
		var names []string
		for _, rg := range gl(md, "players") {
			group, _ := rg.(map[string]any)
			for _, rp := range gl(group, side+"_team") {
				p, _ := rp.(map[string]any)
				names = append(names, gs(p, "player_name", ""))
			}
		}
		lists = append(lists, names)
	}
	for _, ri := range gl(md, "innings") {
		inn, _ := ri.(map[string]any)
		rows, field := gl(inn, "bowl"), "bowler_name"
		if ours[anyStr(inn, "team_batting_id", "")] {
			rows, field = gl(inn, "bat"), "batsman_name"
		}
		var names []string
		for _, rr := range rows {
			r, _ := rr.(map[string]any)
			names = append(names, gs(r, field, ""))
		}
		lists = append(lists, names)
	}
	return lists
}

// careerInitSur is a name's first initial + surname, and its first name, in careerLookup's
// form.
func careerInitSur(name string) (initSur, first string) {
	toks := strings.Fields(careerLetters.ReplaceAllString(name, " "))
	if len(toks) == 0 {
		return "", ""
	}
	if len(toks) == 1 {
		return normNameKey(toks[0]), ""
	}
	return normNameKey(toks[0][:1] + toks[len(toks)-1]), normNameKey(toks[0])
}

// careerCountApps gives each record one appearance per match its name is on. A name with no
// record of its own joins one only when it can only be that player: one record with that
// initial and surname, whose first name fits ("P Smith" or "Pete Smith" is Peter Smith; "Tom
// Jones" isn't Tim Jones). A name that can't be placed adds nothing: reading low beats
// crediting the wrong player.
func careerCountApps(accs map[string]*careerAcc, sheets [][]string) {
	byInit := map[string][]*careerAcc{}
	counted := map[*careerAcc]bool{}
	for _, a := range accs {
		if !counted[a] {
			counted[a] = true
			k, _ := careerInitSur(a.name)
			byInit[k] = append(byInit[k], a)
		}
	}
	listed := careerFirstNames(sheets)
	find := func(name string) *careerAcc {
		if a := accs[normNameKey(name)]; a != nil {
			return a
		}
		k, first := careerInitSur(name)
		c := byInit[k]
		if k == "" || first == "" || len(c) != 1 {
			return nil
		}
		_, other := careerInitSur(c[0].name)
		if !careerSameFirst(first, other) || (len(first) == 1 && !careerOnlyFirst(listed, name, other)) {
			return nil
		}
		return c[0]
	}
	for _, sheet := range sheets {
		seen := map[*careerAcc]bool{}
		for _, name := range sheet {
			if a := find(trim(name)); a != nil && !seen[a] {
				seen[a] = true
				a.apps++
			}
		}
	}
}

// cardAppearances is what the player card shows, or 0 for no figure at all. Today's match
// counts (the record never holds it, and they're out there playing it). Only ever for our
// side: an opposition batter who shares a surname with one of ours must not get their
// record. No record means no figure, not "1": a name that didn't match isn't a debut.
func cardAppearances(career, cfg M, name, num, team string) int {
	if career == nil || trim(team) == "" || !isOurTeam(team, cfg) {
		return 0
	}
	if lookupBy, _ := resolvePlayer(name, num); careerBareName(lookupBy) {
		TodaysLineups() // the card's own request, so it can wait; refreshes what the check reads
	}
	rec := careerPlayerRecord(career, name, num)
	if rec == nil || gi(rec, "apps", 0) == 0 {
		return 0
	}
	return gi(rec, "apps", 0) + 1
}

// careerLookup files each record under its full name, its first initial + surname, and its
// surname — the season stats' keys, so the scorebar's names resolve the same way — but only
// the keys that can mean one person. With a Jack and a John Lowe, each is found by his full
// name (the squad roster's shirt numbers give it) and "J Lowe" and "Lowe" find nobody. A
// record that's only ever "J Lowe" has no full name to be found by, so it isn't filed.
func careerLookup(recs []M) M {
	type keyed struct {
		rec                    M
		full, initSur, surname string
		initialOnly            bool
	}
	var ks []keyed
	initCount, surCount := map[string]int{}, map[string]int{}
	for _, r := range recs {
		toks := strings.Fields(careerLetters.ReplaceAllString(gs(r, "name", ""), " "))
		if len(toks) == 0 {
			continue
		}
		k := keyed{rec: r, full: normNameKey(gs(r, "name", "")), surname: normNameKey(toks[len(toks)-1])}
		var first string
		k.initSur, first = careerInitSur(gs(r, "name", ""))
		k.initialOnly = len(first) == 1
		ks = append(ks, k)
		initCount[k.initSur]++
		surCount[k.surname]++
	}
	lookup := M{}
	for _, k := range ks {
		alone := initCount[k.initSur] == 1
		if !alone && k.initialOnly {
			continue
		}
		lookup[k.full] = k.rec
		if alone {
			lookup[k.initSur] = k.rec
		}
		if surCount[k.surname] == 1 {
			lookup[k.surname] = k.rec
		}
	}
	return lookup
}

// careerRecord is the scorebar name's club record, or nil (not found, or ambiguous). A name
// with a first name or initial never falls back on the surname, and its first name has to
// fit: Callum Vane's record isn't his brother Harry's, whose first game this may be.
func careerRecord(career M, name string) M {
	lk := gm(career, "lookup")
	k := nameKeys(name)
	if k.surname == "" {
		return nil
	}
	if careerBareName(name) {
		r, _ := lk[k.surname].(M)
		return r
	}
	if r, ok := lk[k.full].(M); ok {
		return r
	}
	r, ok := lk[k.initsur].(M)
	if !ok {
		return nil
	}
	_, first := careerInitSur(name)
	_, other := careerInitSur(gs(r, "name", ""))
	if other != "" && !careerSameFirst(first, other) {
		return nil
	}
	return r
}

// careerBareName is a name that's only a surname, which is all PCS Pro reports.
func careerBareName(name string) bool {
	return len(strings.Fields(careerLetters.ReplaceAllString(name, " "))) < 2
}

// careerPlayerRecord is careerRecord for a player on the scorebar, roster first. A bare surname
// is checked against today's XIs when they're known: if a relative with a different first name
// is playing, the surname alone can't say whose record this is. It only reads what's already
// fetched, because this runs on every /live poll. While a fetch is under way it says nothing
// rather than guess; the next poll asks again.
func careerPlayerRecord(career M, name, num string) M {
	lookupBy, _ := resolvePlayer(name, num)
	rec := careerRecord(career, lookupBy)
	if rec == nil || !careerBareName(lookupBy) {
		return rec
	}
	if !lineupsMu.TryLock() {
		return nil
	}
	defer lineupsMu.Unlock()
	surname := nameKeys(lookupBy).surname
	_, recFirst := careerInitSur(gs(rec, "name", ""))
	for _, side := range []string{"home", "away"} {
		for _, rp := range gl(gm(lineupsOut, side), "players") {
			player, _ := rp.(map[string]any)
			pn := gs(player, "name", "")
			if nameKeys(pn).surname != surname {
				continue
			}
			if _, first := careerInitSur(pn); first != "" && recFirst != "" && !careerSameFirst(first, recFirst) {
				return nil
			}
		}
	}
	return rec
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

// catchMilestones is what today's catches add to a fielder's club record.
func catchMilestones(rec M, catchesToday int) []string {
	if m := crossed(gi(rec, "ct", 0), gi(rec, "ct", 0)+catchesToday, wicketMarks); m > 0 {
		return []string{fmt.Sprintf("%s: %d catches for the club", gs(rec, "name", ""), m)}
	}
	return nil
}

// catchesToday counts the catches this fielder's name has in this innings so far: the innings
// we're fielding in. The other innings' catches are the other side's.
func catchesToday(mid string, innings int, fielder string) int {
	n := 0
	withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT fielder FROM live_fielding WHERE match_id=? AND innings=? AND how='ct'", mid, innings)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f string
			if rows.Scan(&f) == nil && normNameKey(f) == normNameKey(fielder) {
				n++
			}
		}
		return rows.Err()
	})
	return n
}

// LogClubMilestones checks this /live frame's home batters (or, while the club is in the
// field, the bowler and whoever took the last catch) against their club records. Each milestone is logged once per
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
			if rec := careerPlayerRecord(career, name, anyStr(b, "number", "")); rec != nil {
				for _, t := range clubMilestones(rec, gi(b, "runs", 0), 0, true) {
					hits = append(hits, hit{"club-" + t, t})
				}
			}
		}
	} else {
		bw := gm(state, "bowler")
		if name := cleanName(gs(bw, "name", "")); name != "" {
			if rec := careerPlayerRecord(career, name, anyStr(bw, "number", "")); rec != nil {
				for _, t := range clubMilestones(rec, 0, gi(bw, "wickets", 0), false) {
					hits = append(hits, hit{"club-" + t, t})
				}
			}
		}
		// The last wicket's catch: the feed names the fielder, maybe only by surname, so the
		// record goes through the same family checks as a batter's.
		if f := cleanName(gs(state, "lastWicketFielder", "")); f != "" && fieldingHow(gs(state, "lastWicketType", "")) == "ct" {
			if rec := careerPlayerRecord(career, f, ""); rec != nil {
				inn := gi(state, "innings", 1)
				if inn == 0 {
					inn = 1
				}
				for _, t := range catchMilestones(rec, catchesToday(CurrentMatchID(), inn, f)) {
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
