package server

import (
	"fmt"
	"strings"
	"time"

	"cricketstream/internal/pyval"
)

// FetchTodaysMatch finds today's home fixture from the PlayCricket API: the match
// details, or {"error": ...}. Swappable for tests.
var FetchTodaysMatch = func(key, siteID string) M {
	if trim(key) == "" {
		return M{"error": "No API key set"}
	}
	if siteID == "" {
		return M{"error": "No PlayCricket club ID set"}
	}
	today := time.Now().Format("02/01/2006")
	cfg := LoadState()
	clubName := gsOr(cfg, "home_abbrev", gsOr(cfg, "home_team", "Your club"))
	data, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d",
		key, siteID, time.Now().Year()), 15*time.Second)
	if err != nil {
		return M{"error": err.Error()}
	}
	matches := toMs(gl(data, "matches"))
	var todays []M
	for _, m := range matches {
		if gs(m, "match_date", "") == today {
			todays = append(todays, m)
		}
	}
	if len(todays) == 0 {
		return M{"error": fmt.Sprintf("No matches found for today (%s)", today), "all_count": len(matches)}
	}
	// Home matches only — the stream is home games only.
	var home []M
	for _, m := range todays {
		if anyStr(m, "home_club_id", "") == siteID {
			home = append(home, m)
		}
	}
	if len(home) == 0 {
		for _, m := range todays {
			if anyStr(m, "away_club_id", "") == siteID {
				return M{"error": clubName + " are away today — no home fixture to stream"}
			}
		}
		return M{"error": fmt.Sprintf("No %s matches found for today (%s)", clubName, today)}
	}
	if filt := strings.ToLower(trim(gs(cfg, "ground_filter", ""))); filt != "" && len(home) > 1 {
		var g []M
		for _, m := range home {
			if strings.Contains(strings.ToLower(gs(m, "ground_name", "")), filt) {
				g = append(g, m)
			}
		}
		if len(g) > 0 {
			home = g
		}
	}
	// Still several (1st XI and 2nd XI both at home): the first.
	match := home[0]
	oppName, oppClub := gs(match, "away_team_name", ""), gs(match, "away_club_name", "")
	if anyStr(match, "home_club_id", "") != siteID {
		oppName, oppClub = gs(match, "home_team_name", ""), gs(match, "home_club_name", "")
	}
	// Abbreviate the CLUB name (e.g. "Ivybridge CC"), not the team label ("Under 15",
	// "1st XI") — PlayCricket gives every team a generic label like that on both sides, so
	// abbreviating the label gave meaningless "UNDER" / "1ST".
	src := oppClub
	if src == "" {
		src = oppName
	}
	words := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(src, " CC", ""), " Cricket Club", ""))
	abbrev := ""
	if len(words) > 0 {
		abbrev = strings.ToUpper(truncRunes(words[0], 5))
	}
	return M{
		"match_id": anyStr(match, "id", ""), "match_date": gs(match, "match_date", ""),
		"match_time": gs(match, "match_time", ""), "competition": gs(match, "competition_name", ""),
		"competition_id": anyStr(match, "competition_id", ""), "competition_type": gs(match, "competition_type", ""),
		"home_team": gs(match, "home_team_name", ""), "away_team": oppName, "away_club": oppClub,
		"away_abbrev": abbrev, "ground": gs(match, "ground_name", ""),
		"ground_lat": valOr(match, "ground_latitude", ""), "ground_lon": valOr(match, "ground_longitude", ""),
		"umpire1": gs(match, "umpire_1_name", ""), "umpire2": gs(match, "umpire_2_name", ""),
		"home_club_id": anyStr(match, "home_club_id", ""), "away_club_id": anyStr(match, "away_club_id", ""),
		"scorer1": gs(match, "scorer_1_name", ""), "scorer2": gs(match, "scorer_2_name", ""),
		"all_today": len(todays),
	}
}

// KitPresets are the kit colours offered in the control panel.
var KitPresets = []M{
	{"name": "Navy", "hex": "#1a3a5c"}, {"name": "Royal Blue", "hex": "#1a4fa8"},
	{"name": "Sky Blue", "hex": "#0099cc"}, {"name": "Dark Green", "hex": "#1a5c2a"},
	{"name": "Bright Green", "hex": "#2e8b2e"}, {"name": "Maroon", "hex": "#7b1a1a"},
	{"name": "Red", "hex": "#c0392b"}, {"name": "Purple", "hex": "#6a1a8c"},
	{"name": "Black", "hex": "#1a1a1a"}, {"name": "Dark Grey", "hex": "#4a4a4a"},
	{"name": "Amber", "hex": "#c87800"}, {"name": "Orange", "hex": "#cc5500"},
	{"name": "White", "hex": "#e8e8e8"}, {"name": "Teal", "hex": "#1a6b6b"},
	{"name": "Pink", "hex": "#b52d6e"},
}

