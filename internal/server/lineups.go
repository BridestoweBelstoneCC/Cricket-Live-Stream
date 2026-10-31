package server

// Today's line-ups for the pre-match show: the two XIs as the clubs entered them on
// PlayCricket (match_detail's "players"), which most do the evening before. Nothing is
// shown when they haven't: the overlay just leaves the line-ups out of the rotation.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyval"
)

// lineupsFor is how long an answer is kept: teams change up to the toss.
const lineupsFor = 5 * time.Minute

var (
	lineupsMu  sync.Mutex
	lineupsAt  time.Time
	lineupsKey string
	lineupsOut M
)

// TodaysLineups is {"home": {"team", "players": [...]}, "away": ...}; a side with no
// players entered has an empty list. Never raises.
func TodaysLineups() (out M) {
	defer func() {
		if recover() != nil {
			out = M{"ok": false}
		}
	}()
	cfg := LoadState()
	key, mid := trim(gs(cfg, "playcricket_api_key", "")), trim(gs(cfg, "pc_match_id", ""))
	if key == "" || mid == "" {
		return M{"ok": false, "reason": "No PlayCricket match for today."}
	}
	lineupsMu.Lock()
	defer lineupsMu.Unlock()
	if lineupsKey == mid && time.Since(lineupsAt) < lineupsFor {
		return lineupsOut
	}
	det, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?api_token=%s&match_id=%s", key, mid), 15*time.Second)
	if err != nil {
		return M{"ok": false, "reason": "PlayCricket: " + err.Error()}
	}
	md := firstDetail(det)
	side := func(which, team string) M {
		var players []any
		for _, rp := range gl(md, "players") {
			group, _ := rp.(map[string]any)
			for _, rx := range gl(group, which+"_team") {
				p, _ := rx.(map[string]any)
				name := trim(gs(p, "player_name", ""))
				if name == "" || strings.EqualFold(name, "unsure") {
					continue
				}
				players = append(players, M{"name": name, "captain": pyval.Truthy(p["captain"]),
					"keeper": pyval.Truthy(p["wicket_keeper"])})
			}
		}
		if players == nil {
			players = []any{}
		}
		return M{"team": team, "players": players}
	}
	out = M{"ok": true, "home": side("home", gsOr(cfg, "home_team", gs(md, "home_club_name", ""))),
		"away": side("away", gsOr(cfg, "away_team", gs(md, "away_club_name", "")))}
	lineupsOut, lineupsKey, lineupsAt = out, mid, time.Now()
	return out
}
