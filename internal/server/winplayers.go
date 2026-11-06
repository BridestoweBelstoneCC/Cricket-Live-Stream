package server

// Today's XIs for the win predictor (winpredict.XIBatting): each player's batting this
// season and each team's XIs, from the season build's scorecards, against today's line-ups
// as the clubs entered them on PlayCricket (lineups.go).

import (
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyval"
	"cricketstream/internal/winpredict"
)

// seasonPlayerData is every player's batting (runs, innings) and every team's listed XIs,
// from this season's scorecards.
func seasonPlayerData(details []M) (M, M) {
	runs, inns := map[string]int{}, map[string]int{}
	xis := map[string][]any{}
	for _, d := range details {
		md := firstDetail(d)
		for _, ri := range gl(md, "innings") {
			inn, _ := ri.(map[string]any)
			for _, rb := range gl(inn, "bat") {
				b, _ := rb.(map[string]any)
				id := anyStr(b, "batsman_id", "")
				how := strings.ToLower(trim(pyval.Str(b["how_out"])))
				if id == "" || how == "did not bat" || how == "absent" {
					continue
				}
				r, balls := trim(anyStr(b, "runs", "")), trim(anyStr(b, "balls", ""))
				if (how == "" || how == "none") && (r == "" || r == "0") && (balls == "" || balls == "0") {
					continue // nothing entered: most likely didn't bat
				}
				runs[id] += gi(b, "runs", 0)
				inns[id]++
			}
		}
		for side, ids := range lineupIDs(md) {
			team := anyStr(md, side+"_team_id", "")
			if team != "" && len(ids) > 0 {
				xis[team] = append(xis[team], ids)
			}
		}
	}
	players := M{}
	for id, n := range inns {
		players[id] = []any{runs[id], n}
	}
	teams := M{}
	for t, l := range xis {
		teams[t] = l
	}
	return players, teams
}

// lineupIDs is the player ids each side listed ("home", "away"); match_detail keeps them as
// [{"home_team": [...]}, {"away_team": [...]}].
func lineupIDs(md M) map[string][]any {
	out := map[string][]any{}
	for _, rg := range gl(md, "players") {
		g, _ := rg.(map[string]any)
		for _, side := range []string{"home", "away"} {
			for _, rp := range gl(g, side+"_team") {
				p, _ := rp.(map[string]any)
				if id := anyStr(p, "player_id", ""); id != "" {
					out[side] = append(out[side], id)
				}
			}
		}
	}
	return out
}

func toPlayerRecords(m M) map[string]winpredict.PlayerRecord {
	out := map[string]winpredict.PlayerRecord{}
	for id, v := range m {
		l, _ := v.([]any)
		if len(l) == 2 {
			r, _ := pyval.Int(l[0])
			n, _ := pyval.Int(l[1])
			out[id] = winpredict.PlayerRecord{Runs: r, Innings: n}
		}
	}
	return out
}

func toXIs(v any) [][]string {
	var out [][]string
	l, _ := v.([]any)
	for _, rx := range l {
		ids, _ := rx.([]any)
		var s []string
		for _, id := range ids {
			s = append(s, pyval.Str(id))
		}
		out = append(out, s)
	}
	return out
}

// todaysXIs is today's two XIs by team id, from line-ups already fetched — never a fetch
// of its own: /live asks every poll. When they're stale (or never fetched) one refresh is
// started in the background, for the next poll.
func todaysXIs(teams M, cfg M) map[string][]string {
	mid := trim(gs(cfg, "pc_match_id", ""))
	home := gs(teams, "home", "")
	if mid == "" || home == "" {
		return nil
	}
	lineupsMu.Lock()
	out, key, fresh := lineupsOut, lineupsKey, time.Since(lineupsAt) < lineupsFor
	lineupsMu.Unlock()
	if key != mid || !fresh {
		refreshLineupsInBackground()
	}
	if key != mid || out == nil {
		return nil
	}
	away := gs(teams, "ours", "")
	if away == home {
		away = gs(teams, "theirs", "")
	}
	xi := map[string][]string{}
	for side, team := range map[string]string{"home": home, "away": away} {
		for _, rp := range gl(gm(out, side), "players") {
			p, _ := rp.(map[string]any)
			if id := anyStr(p, "id", ""); id != "" {
				xi[team] = append(xi[team], id)
			}
		}
	}
	return xi
}

var (
	lineupsRefreshing sync.Mutex
	lineupsTriedAt    time.Time // guarded by lineupsRefreshing
)

// refreshLineupsInBackground fetches today's line-ups unless a fetch is already running,
// and at most once a minute: a failing fetch isn't retried every poll.
var refreshLineupsInBackground = func() {
	if !lineupsRefreshing.TryLock() {
		return
	}
	if time.Since(lineupsTriedAt) < time.Minute {
		lineupsRefreshing.Unlock()
		return
	}
	lineupsTriedAt = time.Now()
	go func() {
		defer lineupsRefreshing.Unlock()
		TodaysLineups()
	}()
}
