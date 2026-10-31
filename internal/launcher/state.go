// Package launcher is match day on the streaming laptop (quickstart.py): today's fixture
// from PlayCricket, match_state.json merged so nothing the panel owns is lost, OBS made
// ready and set up, the scorer's feed checked, then the server started — restarted if it
// crashes — and, on Ctrl+C, the sponsor airtime and match report while it's still up.
package launcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricketstream/internal/ini"
	"cricketstream/internal/pyval"
)

type M = map[string]any

// Match is today's fixture as quickstart reads it.
type Match struct {
	MatchID, AwayTeam, AwayAbbrev, AwayClubID        string
	Competition, CompetitionID, Ground, GroundLat    string
	GroundLon, Umpire1, Umpire2, Scorer1, Date, Time string
}

// matchesURL is swappable for tests.
var matchesURL = func(key, club string, year int) string {
	return fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d", key, club, year)
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func str(m M, k string) string {
	if v := m[k]; pyval.Truthy(v) {
		return pyval.Str(v)
	}
	return ""
}

// FetchTodaysMatch finds today's fixture for the club: (match, "") or (nil, why not).
func FetchTodaysMatch(apiKey, clubID string) (*Match, string) {
	if apiKey == "" || apiKey == "YOUR_KEY_HERE" {
		return nil, "No API key configured in config.ini"
	}
	today := time.Now().Format("02/01/2006")
	req, _ := http.NewRequest("GET", matchesURL(apiKey, clubID, time.Now().Year()), nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Sprintf("HTTP Error %d: %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	var data M
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err.Error()
	}
	all, _ := data["matches"].([]any)
	var todays []M
	for _, x := range all {
		if m, ok := x.(M); ok && str(m, "match_date") == today {
			todays = append(todays, m)
		}
	}
	if len(todays) == 0 {
		return nil, fmt.Sprintf("No matches found for today (%s) — %d total in season", today, len(all))
	}
	m := todays[0]
	for _, t := range todays {
		if pyval.Str(t["home_club_id"]) == clubID {
			m = t
			break
		}
	}
	// The CLUB name ('Heathcoat CC'), not the team name ('1st XI'), for both the display
	// name and the abbreviation — otherwise the abbreviation comes out as "1ST".
	var opp, oppID string
	if pyval.Str(m["home_club_id"]) == clubID {
		opp, oppID = orStr(str(m, "away_club_name"), str(m, "away_team_name")), str(m, "away_club_id")
	} else {
		opp, oppID = orStr(str(m, "home_club_name"), str(m, "home_team_name")), str(m, "home_club_id")
	}
	// Auto-abbreviate from the club name: strip CC/Cricket Club, first word, up to 5 chars
	// ('Heathcoat CC' -> 'HEATH'). Still overridable in the control panel.
	words := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(opp, " CC", ""), " Cricket Club", ""))
	abbrev := ""
	if len(words) > 0 {
		r := []rune(words[0])
		if len(r) > 5 {
			r = r[:5]
		}
		abbrev = strings.ToUpper(string(r))
	}
	return &Match{MatchID: pyval.Str(valOr(m["id"], "")), AwayTeam: opp, AwayAbbrev: abbrev, AwayClubID: oppID,
		Competition: str(m, "competition_name"), CompetitionID: pyval.Str(valOr(m["competition_id"], "")),
		Ground: str(m, "ground_name"), GroundLat: str(m, "ground_latitude"), GroundLon: str(m, "ground_longitude"),
		Umpire1: str(m, "umpire_1_name"), Umpire2: str(m, "umpire_2_name"), Scorer1: str(m, "scorer_1_name"),
		Date: str(m, "match_date"), Time: str(m, "match_time")}, ""
}

func valOr(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func get(m M, k string, def any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

// BuildState is match_state.json from config.ini + today's fixture, MERGED over the
// existing state. The merge matters: the panel stores plenty this launcher knows nothing
// about — the squad roster, the weekend sponsor, the cached network test, badge ids... A
// full rewrite used to wipe them on every match day. Fields the launcher owns (today's
// opposition, config.ini values, match-day safety defaults) still override.
func BuildState(cfg *ini.File, match *Match, exist M) M {
	if exist == nil {
		exist = M{}
	}
	c := func(sec, key, def string) string { return cfg.Get(sec, key, def) }
	// No fixture found today → keep whatever the operator already entered in the panel
	// rather than stomping it back to placeholders.
	pick := func(fromMatch string, key string, def any) any {
		if match != nil {
			return fromMatch
		}
		return get(exist, key, def)
	}
	var awayTeam, awayAbbrev, comp, compID, ump1, ump2, scorer, pcID, awayClub, start string
	if match != nil {
		awayTeam, awayAbbrev, comp, compID = match.AwayTeam, match.AwayAbbrev, match.Competition, match.CompetitionID
		ump1, ump2, scorer, pcID, awayClub = match.Umpire1, match.Umpire2, match.Scorer1, match.MatchID, match.AwayClubID
		start = match.Time
	}
	maxOvers, err := strconv.Atoi(strings.TrimSpace(c("Stream", "max_overs", "50")))
	if err != nil {
		maxOvers = 50
	}
	drinks, err := strconv.Atoi(strings.TrimSpace(c("Scoring", "drinks_over", "25")))
	if err != nil || drinks == 0 {
		drinks = 25
	}
	src := strings.ToLower(strings.TrimSpace(c("Scoring", "pcs_source", "local")))
	if src == "" {
		src = "local"
	}
	fresh := M{
		// Club / match identity
		"home_team":        c("Club", "name", "My Club CC"),
		"home_abbrev":      c("Club", "abbreviation", "CC"),
		"away_team":        pick(awayTeam, "away_team", "Opposition CC"),
		"away_abbrev":      pick(awayAbbrev, "away_abbrev", ""),
		"home_colour":      c("Club", "home_colour", "#1a3a5c"),
		"away_colour":      get(exist, "away_colour", "#7b2d2d"),
		"competition_name": pick(comp, "competition_name", ""),
		"competition_id":   pick(compID, "competition_id", ""),
		"replay_motto":     c("Club", "motto", ""),
		"umpire1_name":     pick(ump1, "umpire1_name", ""),
		"umpire2_name":     pick(ump2, "umpire2_name", ""),
		"scorer1_name":     pick(scorer, "scorer1_name", ""),
		"pc_match_id":      pick(pcID, "pc_match_id", ""),
		"match_time":       pick(start, "match_time", ""), // "13:00": the pre-match countdown
		"match_notes":      "",
		// API keys
		"playcricket_api_key": c("API", "playcricket_key", ""),
		"anthropic_api_key":   c("API", "anthropic_key", ""),
		"home_club_id":        c("Club", "playcricket_id", ""),
		"ground_filter":       c("Scoring", "ground_filter", ""),
		"away_club_id":        pick(awayClub, "away_club_id", ""),
		// Operational defaults: always ready to stream
		"demo_mode":  false,
		"use_widget": false, // the scorer's file only
		"max_overs":  maxOvers,
		// Scoring
		"pcs_source":        src,
		"agent_host":        strings.TrimSpace(c("Scoring", "agent_host", "")),
		"pcs_output_folder": expandUser(c("Scoring", "pcs_output_folder", "")),
		"logos_folder":      expandUser(c("Scoring", "logos_folder", "")),
		"headshots_folder":  expandUser(c("Scoring", "headshots_folder", "")),
		"drinks_over":       drinks,
		// Graphics — the user's settings survive, else sensible defaults
		"graphics_fow":                 get(exist, "graphics_fow", true),
		"graphics_partnership":         get(exist, "graphics_partnership", true),
		"graphics_lineup":              get(exist, "graphics_lineup", true),
		"graphics_boundary_flash":      get(exist, "graphics_boundary_flash", true),
		"graphics_milestones":          get(exist, "graphics_milestones", true),
		"graphics_innings_summary":     get(exist, "graphics_innings_summary", true),
		"graphics_over_summary":        get(exist, "graphics_over_summary", true),
		"graphics_partnership_display": get(exist, "graphics_partnership_display", true),
		"graphics_runrate_trend":       get(exist, "graphics_runrate_trend", true),
		"graphics_commentary":          get(exist, "graphics_commentary", false),
		"graphics_commentary_over":     get(exist, "graphics_commentary_over", false),
		"graphics_player_card":         get(exist, "graphics_player_card", false),
		// Replay — on by default (including 50s), but panel edits survive re-runs
		"replay_enabled":  get(exist, "replay_enabled", true),
		"replay_on_fifty": get(exist, "replay_on_fifty", true),
		"replay_folder":   expandUser(c("OBS", "replay_folder", "")),
		"max_clips":       get(exist, "max_clips", 500),
		"replay_duration": get(exist, "replay_duration", 18),
		// OBS
		"obs_host": "localhost", "obs_port": 4455, "obs_password": c("OBS", "obs_password", ""),
		"obs_main_scene": "Main", "obs_replay_scene": "Replay",
		// YouTube
		"youtube_title_template": c("Stream", "youtube_title", "LIVE: {home} vs {away}"),
		// Weather — a panel-owned secret; a hard "" here silently wiped it every match day
		"weather_api_key": get(exist, "weather_api_key", ""),
		// Misc
		"poll_interval": get(exist, "poll_interval", 20),
		"match_url":     "", // deliberately cleared — a pinned match is per-day
	}
	out := M{}
	for k, v := range exist {
		out[k] = v
	}
	for k, v := range fresh {
		out[k] = v
	}
	return out
}
