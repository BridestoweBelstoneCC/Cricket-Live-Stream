package server

// Result-card facts. Facts come from PlayCricket's own result (never re-derive a DLS /
// conceded / pairs result from totals) or, for the streamed match, from the scorer's own
// figures. AI only ever writes the caption. Never claim WIN/DEFEAT when it can't tell
// which side is ours, or before the chase is finished.

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"cricketstream/internal/ai"
	"cricketstream/internal/pyval"
	"cricketstream/internal/resultcard"
)

// clubFragment is the first word of our club name, lower-cased — "us" vs the opponent.
func clubFragment(cfg M) string {
	base := strings.Fields(strings.ToLower(gsOr(cfg, "home_team", gsOr(cfg, "name", "Home"))))
	if len(base) == 0 {
		return "home"
	}
	return base[0]
}

func isOurTeam(name string, cfg M) bool {
	return strings.Contains(strings.ToLower(name), clubFragment(cfg))
}

// pyTitle is Python's str.title().
func pyTitle(s string) string {
	var b strings.Builder
	prevLetter := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if prevLetter {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToUpper(r))
			}
			prevLetter = true
		} else {
			b.WriteRune(r)
			prevLetter = false
		}
	}
	return b.String()
}

// shortName: 'Peter Smith' -> 'P SMITH', broadcast-style. For youth sides, the more
// discreet first name + surname initial ('Jack Smith' -> 'Jack S'), in line with common
// junior safeguarding practice, where a full first name is on record.
func shortName(nm string, youth bool) string {
	toks := strings.Fields(nm)
	if youth && len(toks) >= 2 && len([]rune(toks[0])) > 1 {
		return pyTitle(toks[0] + " " + string([]rune(toks[len(toks)-1])[:1]))
	}
	if len(toks) >= 2 {
		return strings.ToUpper(string([]rune(toks[0])[:1]) + " " + toks[len(toks)-1])
	}
	return strings.ToUpper(nm)
}

// pcRecentMatches is our club's recent COMPLETED matches this season, newest first.
func pcRecentMatches(key, siteID, season string, cfg M, limit int) ([]M, error) {
	list, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%s", key, siteID, season), 15*time.Second)
	if err != nil {
		return nil, err
	}
	rows := []M{}
	for _, rm := range gl(list, "matches") {
		m, _ := rm.(map[string]any)
		if !pcIsPastMatch(m) || !pyval.Truthy(m["id"]) {
			continue
		}
		home := gsOr(m, "home_club_name", gsOr(m, "home_team_name", ""))
		away := gsOr(m, "away_club_name", gsOr(m, "away_team_name", ""))
		ours := isOurTeam(home, cfg)
		opp, team := home, gsOr(m, "away_team_name", "")
		if ours {
			opp, team = away, gsOr(m, "home_team_name", "")
		}
		date := gs(m, "match_date", "")
		sortKey := "0001-01-01"
		if d, err := time.Parse("02/01/2006", date); err == nil {
			sortKey = d.Format("2006-01-02")
		}
		rows = append(rows, M{"id": pyval.Str(m["id"]), "date": date, "sort": sortKey, "opponent": opp,
			"team": team, "team_key": teamKey(team), "label": strings.Trim(fmt.Sprintf("%s — v %s", date, opp), " —")})
	}
	sort.SliceStable(rows, func(i, j int) bool { return gs(rows[i], "sort", "") > gs(rows[j], "sort", "") })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

var reAnd = regexp.MustCompile(`(?i)\s+and\s+`)

// cardName: 'Bridestowe and Belstone CC' -> 'Bridestowe & Belstone CC' — shorter on the card.
func cardName(name string) string { return reAnd.ReplaceAllString(strings.TrimSpace(name), " & ") }

// cardSide is one team's row. Pairs (softball) cricket: a wicket costs runs and the
// batters carry on, so 10+ wickets is normal and never "all out". wkts < 0 = unknown.
func cardSide(club, team string, runs, wkts int, overs, clubID string, ours, pairs bool) M {
	allOut := !pairs && wkts >= 10
	ov := ""
	if overs != "" {
		ov = overs + " overs"
	}
	score := fmt.Sprintf("%d-%d", runs, max(wkts, 0))
	ovText := ov
	if allOut {
		score = fmt.Sprint(runs)
		ovText = "All out"
		if ov != "" {
			ovText = "All out · " + ov
		}
	}
	return M{"club": cardName(club), "team": team, "club_id": clubID, "ours": ours, "won": false,
		"runs": runs, "wkts": wkts, "overs_raw": overs, "score": score, "overs": ovText}
}

// marginFromScores is (winner index, margin) from the two innings, or (-1, "") for a tie.
func marginFromScores(first, second M) (int, string) {
	s, f := gi(second, "runs", 0), gi(first, "runs", 0)
	switch {
	case s > f:
		left := max(0, 10-gi(second, "wkts", 0))
		return 1, fmt.Sprintf("by %d %s", left, plural(left, "wicket"))
	case f > s:
		return 0, fmt.Sprintf("by %d %s", f-s, plural(f-s, "run"))
	}
	return -1, ""
}

func plural(n int, w string) string {
	if n != 1 {
		return w + "s"
	}
	return w
}

// finishCardFacts adds the flat legacy fields the caption writer reads.
func finishCardFacts(sides []M, outcome, margin string, stars []M, cfg M, extra M) M {
	// The panel's own abbreviation, unless it's still the "HOME" placeholder for a club
	// with a real name. (Reading config.ini's "abbreviation" key here — never a state key
	// — once made every result line say the first five letters of the club name.)
	def := DefaultState()
	abbr := trim(gs(cfg, "home_abbrev", ""))
	if abbr == "" || (abbr == def["home_abbrev"] && gs(cfg, "home_team", "") != def["home_team"]) {
		abbr = truncRunes(clubFragment(cfg), 5)
	}
	abbr = strings.ToUpper(abbr)
	word := map[string]string{"win": "WIN", "loss": "LOSE", "abandoned": "ABANDONED", "cancelled": "CANCELLED"}[outcome]
	var result string
	switch outcome {
	case "win", "loss":
		result = strings.TrimSpace(fmt.Sprintf("%s %s %s", abbr, word, strings.ToUpper(margin)))
	case "tie":
		result = "MATCH TIED"
	case "draw":
		result = "MATCH DRAWN"
	default:
		result = orStr(word, "MATCH UPDATE")
	}
	kicker := "Match result"
	if outcome == "live" {
		kicker = "Match update"
	}
	sidesAny := make([]any, len(sides))
	for i, s := range sides {
		sidesAny[i] = s
	}
	starsAny := make([]any, len(stars))
	for i, s := range stars {
		starsAny[i] = s
	}
	facts := M{"ok": true, "outcome": outcome, "margin": margin, "sides": sidesAny, "stars": starsAny,
		"kicker": kicker, "caption": "", "result": result}
	for i, s := range sides {
		if i >= 2 {
			break
		}
		n := i + 1
		facts[fmt.Sprintf("team%d_name", n)] = strings.ToUpper(strings.TrimSpace(gs(s, "club", "") + " " + gs(s, "team", "")))
		sc := gs(s, "score", "")
		if ov := gs(s, "overs", ""); ov != "" {
			sc = fmt.Sprintf("%s (%s)", sc, ov)
		}
		facts[fmt.Sprintf("team%d_score", n)] = sc
	}
	for i, st := range stars {
		if i >= 2 {
			break
		}
		facts[fmt.Sprintf("performer%d", i+1)] = strings.TrimSpace(fmt.Sprintf("%s %s %s", gs(st, "name", ""), gs(st, "big", ""), gs(st, "small", "")))
	}
	for k, v := range extra {
		facts[k] = v
	}
	return facts
}

// BuildMatchFactsFromPC pulls one match's scorecard from PlayCricket and distils it into
// result-card facts — outcome, margin, both sides (with club ids for badges), and our top
// batter and bowler. Works for ANY match, streamed or not. Deterministic; no AI. The
// outcome is PlayCricket's own result: a DLS win can come from the lower score, and a
// tie/draw/abandonment can't be read off totals. teamKey "youth" switches performer names
// to the discreet junior form.
var BuildMatchFactsFromPC = func(matchID, teamKeyArg string) M {
	youth := teamKeyArg == "youth"
	cfg := LoadState()
	key := trim(gsOr(cfg, "playcricket_api_key", gsOr(cfg, "api_token", "")))
	if key == "" {
		return M{"ok": false, "error": "No PlayCricket API key set"}
	}
	det, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?api_token=%s&match_id=%s", key, matchID), 15*time.Second)
	if err != nil {
		return M{"ok": false, "error": fmt.Sprintf("PlayCricket fetch failed: %v", err)}
	}
	md := firstDetail(det)
	innings := gl(md, "innings")
	res := strings.ToUpper(trim(gsOr(md, "result", "")))
	desc := gsOr(md, "result_description", "")
	if len(innings) < 2 && res != "A" && res != "C" && res != "CON" {
		return M{"ok": false, "error": "That match has no completed scorecard yet"}
	}
	ourID := trim(gsOr(cfg, "home_club_id", ""))
	type club struct{ tid, club, team, cid string }
	var clubs []club
	for _, k := range []string{"home", "away"} {
		clubs = append(clubs, club{anyStr(md, k+"_team_id", ""), gs(md, k+"_club_name", ""), gs(md, k+"_team_name", ""), gsOr(md, k+"_club_id", "")})
	}
	isOurs := func(name, cid string) bool { return (ourID != "" && cid == ourID) || isOurTeam(name, cfg) }
	pairs := strings.Contains(strings.ToLower(anyStr(md, "game_type", "")), "pairs")
	var sides []M
	var inns []M
	for i, ri := range innings {
		if i >= 2 {
			break
		}
		inn, _ := ri.(map[string]any)
		tid := anyStr(inn, "team_batting_id", "")
		c := club{tid, gs(inn, "team_batting_name", ""), "", ""}
		for _, cl := range clubs {
			if cl.tid == tid {
				c = cl
			}
		}
		side := cardSide(c.club, c.team, pcParseIntOr0(inn["runs"]), pcParseIntOr0(inn["wickets"]),
			trim(gsOr(inn, "overs", "")), c.cid, isOurs(c.club, c.cid), pairs)
		if _, ok := pcParseInt(inn["runs"]); !ok { // conceded / abandoned: never batted
			side["score"], side["overs"] = "", ""
		}
		side["team_id"] = tid
		sides = append(sides, side)
		inns = append(inns, inn)
	}
	if len(sides) < 2 && (res == "A" || res == "C" || res == "CON") {
		// Conceded / abandoned before (or without) a full scorecard: both clubs, no scores.
		sides, inns = nil, nil
		for _, c := range clubs {
			side := cardSide(c.club, c.team, 0, 0, "", c.cid, isOurs(c.club, c.cid), false)
			side["score"], side["overs"], side["team_id"] = "", "", c.tid
			sides = append(sides, side)
			inns = append(inns, M{})
		}
	}
	// Can't tell which side is ours (no home_club_id and a name that matches neither, or
	// both): show the result without claiming a WIN or DEFEAT for the wrong club.
	unsure := false
	if len(sides) == 2 {
		n := 0
		for _, s := range sides {
			if gb(s, "ours", false) {
				n++
			}
		}
		if unsure = n != 1; unsure {
			for _, s := range sides {
				s["ours"] = false
			}
		}
	}
	applied := gsOr(md, "result_applied_to", "")
	winner, outcome, margin := -1, "", ""
	sideIndex := func(tid string) int {
		for i, s := range sides {
			if tid != "" && gs(s, "team_id", "") == tid {
				return i
			}
		}
		return -1
	}
	switch {
	case res == "T":
		outcome = "tie"
	case res == "D":
		outcome = "draw"
	case res == "A":
		outcome = "abandoned"
	case res == "C":
		outcome = "cancelled"
	case res == "CON":
		// "<Team> - Conceded": that team gave the match away, so the other side won.
		named := strings.ToLower(cardName(strings.SplitN(desc, " - ", 2)[0]))
		loser := ""
		for _, s := range sides {
			if strings.ToLower(gs(s, "club", "")) == named {
				loser = gs(s, "team_id", "")
				break
			}
		}
		winTid := ""
		for _, s := range sides {
			if loser != "" && gs(s, "team_id", "") != loser {
				winTid = gs(s, "team_id", "")
				break
			}
		}
		winner = sideIndex(winTid)
		desc += " conceded"
	case (res == "W" || res == "L") && applied != "":
		winTid := applied
		if res == "L" {
			winTid = ""
			for _, s := range sides {
				if gs(s, "team_id", "") != applied {
					winTid = gs(s, "team_id", "")
					break
				}
			}
		}
		winner = sideIndex(winTid)
	case len(sides) == 2:
		// No result recorded yet: scorers upload live, so this may be mid-chase. Only a
		// finished chase has a winner; otherwise it's a match update.
		var limit any
		if l := pcParseIntOr0(md["no_of_overs"]) * 6; l != 0 {
			limit = l
		}
		if chaseFinished(sides[0], sides[1], M{"overs": sides[1]["overs_raw"], "limit_balls": limit}, M{"overs": sides[0]["overs_raw"]}, cfg) {
			winner, _ = marginFromScores(sides[0], sides[1])
			if winner < 0 {
				outcome = "tie"
			}
		} else {
			outcome = "live"
		}
	}
	if winner >= 0 && len(sides) == 2 {
		sides[winner]["won"] = true
		outcome = "loss"
		if gb(sides[winner], "ours", false) {
			outcome = "win"
		}
		if unsure {
			outcome = ""
		}
		low := strings.ToLower(desc)
		switch {
		case strings.Contains(low, "conced"):
			margin = "by concession"
		case strings.Contains(low, "dls") || strings.Contains(low, "run rate"):
			margin = "on DLS"
		case pairs:
			// Pairs cricket: both sides bat all their overs (a wicket costs runs), so the
			// side batting second can win by plenty — the margin is always runs.
			diff := gi(sides[0], "runs", 0) - gi(sides[1], "runs", 0)
			if diff < 0 {
				diff = -diff
			}
			if diff != 0 {
				margin = fmt.Sprintf("by %d %s", diff, plural(diff, "run"))
			}
		default:
			if by, m := marginFromScores(sides[0], sides[1]); by == winner {
				margin = m
			}
		}
		if unsure {
			margin = ""
		}
	}
	// Our top batter (our batting innings) and top bowler (theirs).
	var topBat, topBowl M
	for i, side := range sides {
		inn := inns[i]
		if gb(side, "ours", false) {
			for _, rb := range gl(inn, "bat") {
				b, _ := rb.(map[string]any)
				runs, ok := pcParseInt(b["runs"])
				how := strings.ToLower(trim(gsOr(b, "how_out", "")))
				// A blank how_out is a batter who batted (pairs cricket never fills it);
				// did-not-bat entries have no runs.
				if !ok || how == "did not bat" || how == "dnb" || how == "tdnb" || how == "did_not_bat" {
					continue
				}
				no := strings.Contains(how, "not out") || how == "no" || strings.Contains(how, "retired not")
				if topBat == nil || runs > gi(topBat, "runs", 0) {
					var balls any
					if n, ok := pcParseInt(b["balls"]); ok {
						balls = n
					}
					topBat = M{"name": gs(b, "batsman_name", ""), "runs": runs, "balls": balls, "no": no}
				}
			}
		} else {
			for _, rw := range gl(inn, "bowl") {
				bw, _ := rw.(map[string]any)
				wk := pcParseIntOr0(bw["wickets"])
				rn, ok := pcParseInt(bw["runs"])
				if !ok {
					rn = 999
				}
				if topBowl == nil || wk > gi(topBowl, "w", 0) || (wk == gi(topBowl, "w", 0) && rn < gi(topBowl, "r", 0)) {
					topBowl = M{"name": gs(bw, "bowler_name", ""), "w": wk, "r": rn, "o": trim(gsOr(bw, "overs", ""))}
				}
			}
		}
	}
	var stars []M
	if topBat != nil {
		star, small := "", ""
		if gb(topBat, "no", false) {
			star = "*"
		}
		if pyval.Truthy(topBat["balls"]) {
			small = fmt.Sprintf("(%d)", gi(topBat, "balls", 0))
		}
		stars = append(stars, M{"label": "Top score", "name": shortName(gs(topBat, "name", ""), youth),
			"big": fmt.Sprintf("%d%s", gi(topBat, "runs", 0), star), "small": small})
	}
	if topBowl != nil && gi(topBowl, "w", 0) > 0 {
		small := ""
		if o := gs(topBowl, "o", ""); o != "" {
			small = o + " ov"
		}
		stars = append(stars, M{"label": "Best bowling", "name": shortName(gs(topBowl, "name", ""), youth),
			"big": fmt.Sprintf("%d-%d", gi(topBowl, "w", 0), gi(topBowl, "r", 0)), "small": small})
	}
	league := trim(strings.ReplaceAll(gsOr(md, "league_name", ""), " Cricket League", " League"))
	comp := trim(gsOr(md, "competition_name", ""))
	if league != "" && strings.Contains(strings.ToLower(comp), strings.ToLower(league)) {
		league = "" // "Devon Women Softball League · DEVON WOMEN SOFTBALL…"
	}
	date := ""
	if d, err := time.Parse("02/01/2006", gs(md, "match_date", "")); err == nil {
		date = d.Format("02 Jan 2006")
	}
	var parts []string
	for _, x := range []string{league, comp} {
		if x != "" {
			parts = append(parts, x)
		}
	}
	competition := strings.Join(parts, " · ")
	if competition == "" {
		competition = gs(cfg, "competition", "")
	}
	return finishCardFacts(sides, outcome, margin, stars, cfg, M{"competition": competition, "date": date,
		"ground": gsOr(md, "ground_name", "")})
}

// aiCaptionForFacts turns the facts into an upbeat Instagram caption with Claude, falling
// back to a simple template without a key, so away-game posts still work offline.
func aiCaptionForFacts(facts M) string {
	cfg := LoadState()
	key := trim(gs(cfg, "anthropic_api_key", ""))
	fallback := strings.TrimSpace(fmt.Sprintf("%s! %s %s, %s %s. #cricket #villagecricket",
		pyTitle(gs(facts, "result", "")), pyTitle(gs(facts, "team1_name", "")), gs(facts, "team1_score", ""),
		pyTitle(gs(facts, "team2_name", "")), gs(facts, "team2_score", "")))
	if key == "" {
		return fallback
	}
	prompt := "Write a short upbeat Instagram caption (max 45 words, up to 3 hashtags, " +
		"at most 1 emoji) for this cricket result. Return only the caption.\n\n" +
		fmt.Sprintf("%s\n%s %s\n%s %s\nTop batter: %s\nTop bowler: %s", gs(facts, "result", ""),
			gs(facts, "team1_name", ""), gs(facts, "team1_score", ""), gs(facts, "team2_name", ""), gs(facts, "team2_score", ""),
			gs(facts, "performer1", ""), gs(facts, "performer2", ""))
	text, err := ai.Ask(context.Background(), ai.Request{Key: key, Prompt: prompt, MaxTokens: 200, Timeout: 30 * time.Second})
	if err != nil || strings.TrimSpace(text) == "" {
		return fallback
	}
	return strings.TrimSpace(text)
}

var reCardAbbr = regexp.MustCompile(`\b(Cc|Xi|Ii|Iii)\b`)

func capitalizeWord(w string) string {
	r := []rune(strings.ToLower(w))
	if len(r) == 0 {
		return w
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// GenerateSocialGraphicFacts is the result-card facts for the streamed match.
// Deterministic — no AI: the model used to distil the match log into the card's numbers,
// exactly where a made-up score does the most damage. The published PlayCricket
// scorecard wins when the match is linked and has a result; otherwise the scorer's own
// live figures. Swappable for tests.
var GenerateSocialGraphicFacts = func() M {
	cfg := LoadState()
	if mid := CurrentMatchID(); isDigits(mid) {
		if pc := BuildMatchFactsFromPC(mid, ""); gb(pc, "ok", false) && gs(pc, "outcome", "") != "live" {
			return pc
		}
	}
	db, innings := mergedMatchFacts()
	if len(innings) == 0 {
		return M{"ok": false, "error": NoMatchDataError}
	}
	var sides []M
	for i, k := range sortedKeys(innings) {
		if i >= 2 {
			break
		}
		r := innings[k].(M)
		team := gs(r, "batting_team", "")
		ours := isOurTeam(team, cfg)
		club, cid := gsOr(cfg, "away_team", team), gs(cfg, "away_club_id", "")
		if ours {
			club, cid = gsOr(cfg, "home_team", team), gs(cfg, "home_club_id", "")
		}
		side := cardSide(club, "", gi(r, "score", 0), gi(r, "wickets", 0), anyStrOr(r, "overs"), cid, ours, false)
		side["key"], side["raw"] = k, team
		sides = append(sides, side)
	}
	// Can't tell which side is ours (the scorer's team name doesn't contain the club
	// name, or both do): use the scorer's names and never claim a WIN or DEFEAT.
	unsure := false
	if len(sides) == 2 {
		n := 0
		for _, s := range sides {
			if gb(s, "ours", false) {
				n++
			}
		}
		if unsure = n != 1; unsure {
			for _, s := range sides {
				var ws []string
				for _, w := range strings.Fields(cardName(gs(s, "raw", ""))) {
					ws = append(ws, capitalizeWord(w))
				}
				name := reCardAbbr.ReplaceAllStringFunc(strings.Join(ws, " "), strings.ToUpper)
				s["club"], s["club_id"], s["ours"] = name, "", false
			}
		}
	}
	outcome, margin := "live", ""
	if len(sides) == 2 {
		first, second := sides[0], sides[1]
		if chaseFinished(first, second, innings[gs(second, "key", "")].(M), innings[gs(first, "key", "")].(M), cfg) {
			w, m := marginFromScores(first, second)
			margin = m
			if w < 0 {
				outcome = "tie"
			} else {
				sides[w]["won"] = true
				outcome = "loss"
				if gb(sides[w], "ours", false) {
					outcome = "win"
				}
				if unsure {
					outcome, margin = "", ""
				}
			}
		}
	}
	var stars []M
	batKey, bowlKey := "", ""
	for _, s := range sides {
		if gb(s, "ours", false) {
			batKey = gs(s, "key", "")
			break
		}
	}
	if batKey != "" {
		for _, s := range sides {
			if !gb(s, "ours", false) {
				bowlKey = gs(s, "key", "")
				break
			}
		}
	}
	youth := false
	if r, ok := innings[batKey].(M); ok {
		youth = teamKey(gs(r, "batting_team", "")) == "youth"
	}
	batters := toMs(gl(gm(db, "batters"), batKey))
	sort.SliceStable(batters, func(i, j int) bool { return gi(batters[i], "runs", 0) > gi(batters[j], "runs", 0) })
	if len(batters) > 0 && gi(batters[0], "runs", 0) > 0 {
		b := batters[0]
		star := "*"
		if gb(b, "out", false) {
			star = ""
		}
		stars = append(stars, M{"label": "Top score", "name": shortName(gs(b, "name", ""), youth),
			"big": fmt.Sprintf("%d%s", gi(b, "runs", 0), star), "small": fmt.Sprintf("(%d)", gi(b, "balls", 0))})
	}
	bowlers := toMs(gl(gm(db, "bowlers"), bowlKey))
	sort.SliceStable(bowlers, func(i, j int) bool {
		wi, wj := gi(bowlers[i], "w", 0), gi(bowlers[j], "w", 0)
		if wi != wj {
			return wi > wj
		}
		return gi(bowlers[i], "r", 0) < gi(bowlers[j], "r", 0)
	})
	if len(bowlers) > 0 && gi(bowlers[0], "w", 0) > 0 {
		b := bowlers[0]
		stars = append(stars, M{"label": "Best bowling", "name": shortName(gs(b, "name", ""), youth),
			"big": fmt.Sprintf("%d-%d", gi(b, "w", 0), gi(b, "r", 0)), "small": gs(b, "o", "") + " ov"})
	}
	return finishCardFacts(sides, outcome, margin, stars, cfg, M{
		"competition": gsOr(cfg, "competition_name", gs(cfg, "competition", "")),
		"date":        time.Now().Format("02 Jan 2006"), "ground": ""})
}

func anyStrOr(m M, k string) string {
	if !pyval.Truthy(m[k]) {
		return ""
	}
	return oversStr(m[k])
}

func toMs(l []any) []M {
	out := make([]M, 0, len(l))
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// chaseFinished: is the second innings over? Passed the target, all out, or out of
// balls. The overs limit is the scorer's own (overs bowled + ballsRemaining, kept in
// live_innings), not the panel's max_overs, which defaults to 50 and is often never
// changed for a 40-over game. Without it: the first innings' overs if it wasn't all out
// (it batted the full quota), else the panel setting.
func chaseFinished(first, second, secondRec, firstRec, cfg M) bool {
	if gi(second, "runs", 0) > gi(first, "runs", 0) || gi(second, "wkts", 0) >= 10 {
		return true
	}
	bowled := oversToBalls(valOr(secondRec, "overs", 0))
	limit := gi(secondRec, "limit_balls", 0)
	if limit == 0 {
		if gi(first, "wkts", 0) < 10 {
			limit = oversToBalls(valOr(firstRec, "overs", 0))
		} else {
			limit = gi(cfg, "max_overs", 0) * 6
		}
	}
	return limit != 0 && bowled >= limit
}

// logosFolder is the badges folder: the configured one, else logos/ in the app folder.
func logosFolder(cfg M) string {
	if l := trim(gs(cfg, "logos_folder", "")); l != "" {
		return expandUser(l)
	}
	return filepath.Join(appDir, "logos")
}

func badgeFile(folder, clubID string) string {
	if clubID == "" {
		return ""
	}
	for _, e := range []string{".png", ".webp", ".jpg", ".jpeg"} {
		if p := filepath.Join(folder, clubID+e); fileExists(p) {
			return p
		}
	}
	return ""
}

func sponsorsFolder(cfg M) string {
	if s := trim(gs(cfg, "sponsors_folder", "")); s != "" {
		return expandUser(s)
	}
	return SponsorDir
}

// factsToCard converts the facts dict into the renderer's types.
func factsToCard(facts M) resultcard.Facts {
	f := resultcard.Facts{Outcome: gs(facts, "outcome", ""), Margin: gs(facts, "margin", ""), Kicker: gs(facts, "kicker", ""),
		Competition: gs(facts, "competition", ""), Date: gs(facts, "date", ""), Ground: gs(facts, "ground", "")}
	for _, s := range toMs(gl(facts, "sides")) {
		f.Sides = append(f.Sides, resultcard.Side{Club: gs(s, "club", ""), Team: gs(s, "team", ""), Score: gs(s, "score", ""),
			Overs: gs(s, "overs", ""), ClubID: gs(s, "club_id", ""), Won: gb(s, "won", false), Ours: gb(s, "ours", false)})
	}
	for _, s := range toMs(gl(facts, "stars")) {
		f.Stars = append(f.Stars, resultcard.Star{Label: gs(s, "label", ""), Big: gs(s, "big", ""), Small: gs(s, "small", ""), Name: gs(s, "name", "")})
	}
	return f
}

// BuildInstagramImage renders the post-match result card for these facts, in the club's
// colour with its crest, the clubs' badges from logos/, and every sponsor logo.
func BuildInstagramImage(facts M, photoPath, outPath string) (string, error) {
	cfg := LoadState()
	logos := logosFolder(cfg)
	if outPath == "" {
		outPath = filepath.Join(appDir, fmt.Sprintf("instagram_result_%s.png", time.Now().Format("2006-01-02")))
	}
	return resultcard.Render(factsToCard(facts), resultcard.Look{
		ClubColour: gs(cfg, "home_colour", "#1a3a5c"), Crest: badgeFile(logos, trim(gsOr(cfg, "home_club_id", ""))),
		LogosDir: logos, ClubName: gsOr(cfg, "home_team", gsOr(cfg, "name", "Home CC")),
		Sponsors: sponsorLogosForCard(sponsorsFolder(cfg), gs(cfg, "sponsor_id", "")), Photo: photoPath}, outPath)
}
