package server

// PlayCricket live widget / ResultsVault fallback feed. PlayCricket's widget calls
// api.resultsvault.co.uk for live data; this replicates that call directly (no API key).
// It's the fallback when the scorer's own file isn't connected: score only, from the
// widget, with batter/bowler names from ResultsVault on a slower cycle.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

const rvAPIID = "1003"

type rvCacheT struct {
	rvID, pcID     int
	clubID         string
	lastRVPoll     float64
	lastState      M
	pinned         bool
	lastMapAttempt float64
	initialised    bool
}

var (
	rvCache        rvCacheT
	widgetLastPoll float64
	widgetLastData string
	rvMu           sync.Mutex
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

func httpGetText(u string, headers map[string]string, timeout time.Duration) (string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", &httpError{Code: resp.StatusCode, Body: string(b)}
	}
	return strings.ToValidUTF8(string(b), "�"), nil
}

func widgetURL(clubID string) string {
	return fmt.Sprintf("https://www.play-cricket.com/embed_widget/live_scorer_widgets?club_id=%s&days=0", clubID)
}

var widgetHeaders = map[string]string{"User-Agent": browserUA, "Accept": "application/json, text/html, */*",
	"Referer": "https://www.play-cricket.com/"}

// fetchRVMapping would map a PlayCricket match id to a ResultsVault one, but
// api.resultsvault.co.uk needs a browser session cookie to authorise, so it returns 0
// unless a manual rv_match_id is set. Kept for if that auth can be resolved.
func fetchRVMapping(pcMatchID int) int { return 0 }

func fetchRVLive(rvMatchID int) M {
	u := fmt.Sprintf("https://api.resultsvault.co.uk/rv/130000/matches/%d/?apiid=%s&strmflg=3", rvMatchID, rvAPIID)
	data, err := httpGetJSON(u, 10*time.Second, map[string]string{"User-Agent": browserUA,
		"Accept": "application/json, text/plain, */*", "Referer": "https://www.play-cricket.com/",
		"Origin": "https://www.play-cricket.com", "x-ias-api-request": "true"})
	if err != nil {
		say("  ✗  RV live error: %v", err)
		return nil
	}
	return data
}

var (
	pcIDInPath = regexp.MustCompile(`/(\d{6,8})(?:[/?#]|$)`)
	pcIDAny    = regexp.MustCompile(`\b(\d{6,8})\b`)
)

// extractPCMatchID pulls a PlayCricket match id out of a URL or raw id ("" if none).
func extractPCMatchID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if isDigits(s) {
		n, _ := strconv.Atoi(s)
		return strconv.Itoa(n)
	}
	for _, re := range []*regexp.Regexp{pcIDInPath, pcIDAny} {
		if m := re.FindAllStringSubmatch(s, -1); len(m) > 0 {
			n, _ := strconv.Atoi(m[len(m)-1][1])
			return strconv.Itoa(n)
		}
	}
	return ""
}

var (
	reTenWickets = regexp.MustCompile(`[-/]10[^0-9]`)
	reScore      = regexp.MustCompile(`^(\d+)[-/](\d+)\s*\(?([\.\d]+)?\)?`)
)

func dash() M { return M{"name": "—", "runs": 0, "balls": 0, "onStrike": true} }

// parseWidgetJSONScore reads the score straight from the widget's JSON — fast, no extra
// calls. Batter and bowler names are filled in by ResultsVault on a slower cycle. With a
// pcMatchID, that match is preferred.
func parseWidgetJSONScore(data M, pcMatchID int) (M, int) {
	matches := gl(data, "matches")
	if len(matches) == 0 {
		return nil, 0
	}
	if pcMatchID != 0 {
		var filtered []any
		for _, m := range matches {
			if pyval.Str(pyval.Get(m, "match_id")) == strconv.Itoa(pcMatchID) {
				filtered = append(filtered, m)
			}
		}
		// Not found: the match may not be in this club's widget (an away match listed
		// under the other club).
		if len(filtered) > 0 {
			matches = filtered
		}
	}
	m, _ := matches[0].(map[string]any)
	pcID := pyval.IntOr(m["match_id"], 0)
	homeTeam := strings.TrimSpace(gs(m, "home_club_name", "") + " " + gs(m, "home_team_name", ""))
	awayTeam := strings.TrimSpace(gs(m, "away_club_name", "") + " " + gs(m, "away_team_name", ""))
	homeScore, awayScore := gsOr(m, "home_team_score", ""), gsOr(m, "away_team_score", "")
	battedFirst, homeTeamID := anyStr(m, "batted_first", ""), anyStr(m, "home_team_id", "")
	homeYet := strings.Contains(strings.ToLower(homeScore), "yet to bat")
	awayYet := strings.Contains(strings.ToLower(awayScore), "yet to bat")
	var battingScore, batting, bowling string
	innings := 1
	switch {
	case awayYet && !homeYet:
		battingScore, batting, bowling = homeScore, homeTeam, awayTeam
	case homeYet && !awayYet:
		battingScore, batting, bowling = awayScore, awayTeam, homeTeam
	default:
		// Both teams have a score — the 2nd innings, or the widget showing the completed
		// 1st innings total alongside the live one. Only call it innings 2 when the side
		// batting first is all out; default to 1 to avoid false positives mid-game.
		first := awayScore
		if homeTeamID == battedFirst {
			battingScore, batting, bowling = awayScore, awayTeam, homeTeam
			first = homeScore
		} else {
			battingScore, batting, bowling = homeScore, homeTeam, awayTeam
		}
		if strings.Contains(strings.ToLower(first), "all out") || reTenWickets.MatchString(first) {
			innings = 2
		}
	}
	score, wickets, overs := 0, 0, 0.0
	if sm := reScore.FindStringSubmatch(strings.TrimSpace(battingScore)); sm != nil {
		score, _ = strconv.Atoi(sm[1])
		wickets, _ = strconv.Atoi(sm[2])
		if sm[3] != "" {
			overs, _ = strconv.ParseFloat(sm[3], 64)
		}
	}
	rr := 0.0
	if overs > 0 {
		rr = pyfmt.Round(float64(score)/overs, 2)
	}
	// Keep batter/bowler details from the last ResultsVault fetch if there is one.
	cached := rvCache.lastState
	pick := func(k string, def M) any {
		if cached != nil {
			if v, ok := cached[k]; ok {
				return v
			}
		}
		return def
	}
	b2 := dash()
	b2["onStrike"] = false
	state := M{
		"battingTeamName": batting, "bowlingTeamName": bowling, "innings": innings,
		"score": score, "wickets": wickets, "overs": overs,
		"batter1": pick("batter1", dash()), "batter2": pick("batter2", b2),
		"bowler":     pick("bowler", M{"name": "—", "overs": "0", "runs": 0, "wickets": 0}),
		"statusText": "RR: " + pyfmt.Float(rr), "targetRuns": 0, "runsRequired": 0, "ballsRemaining": 0,
	}
	say("  Widget: %s %d-%d (%s ov)", batting, score, wickets, pyfmt.Float(overs))
	return state, pcID
}

// GetLiveState is the widget/ResultsVault fallback's main entry: the overlay state, or
// nil. Pinned to a match (match_url) it polls ResultsVault every 20s for full data;
// otherwise it polls the widget JSON for the score and ResultsVault every 60s for names.
func GetLiveState(clubID, homeName, awayName string) M {
	rvMu.Lock()
	defer rvMu.Unlock()
	if !rvCache.pinned && (!rvCache.initialised || rvCache.clubID != clubID) {
		rvCache = rvCacheT{clubID: clubID, initialised: true}
	}
	if rvCache.rvID != 0 && rvCache.pinned {
		t := now()
		if t-rvCache.lastRVPoll > 20 {
			if data := fetchRVLive(rvCache.rvID); data != nil {
				if st := parseRVToState(data, homeName, awayName); st != nil {
					rvCache.lastRVPoll, rvCache.lastState = t, st
					return st
				}
			}
		} else if rvCache.lastState != nil {
			return rvCache.lastState
		}
		return nil
	}
	// The widget JSON — at most once every 25s.
	raw := ""
	if t := now(); t-widgetLastPoll > 25 {
		r, err := httpGetText(widgetURL(clubID), widgetHeaders, 10*time.Second)
		if err != nil {
			say("  ✗  Widget error: %v", err)
			if widgetLastData == "" {
				return nil
			}
			raw = widgetLastData // the cached copy on error
		} else {
			widgetLastPoll, widgetLastData, raw = t, r, r
		}
	} else {
		if widgetLastData == "" {
			return nil
		}
		raw = widgetLastData
	}
	var widget M
	if json.Unmarshal([]byte(raw), &widget) == nil && len(widget) > 0 {
		pinnedPC := 0
		if rvCache.pinned {
			pinnedPC = rvCache.pcID
		}
		state, pcID := parseWidgetJSONScore(widget, pinnedPC)
		if state == nil {
			return nil
		}
		if pcID != 0 && rvCache.pcID != pcID {
			if rv := fetchRVMapping(pcID); rv != 0 {
				rvCache.rvID, rvCache.pcID, rvCache.lastRVPoll = rv, pcID, 0
			}
		}
		if t := now(); rvCache.rvID != 0 && t-rvCache.lastRVPoll > 60 {
			if data := fetchRVLive(rvCache.rvID); data != nil {
				if rvs := parseRVToState(data, homeName, awayName); rvs != nil {
					// The widget's score is more current; ResultsVault has the names.
					state["batter1"], state["batter2"], state["bowler"] = rvs["batter1"], rvs["batter2"], rvs["bowler"]
					rvCache.lastState, rvCache.lastRVPoll = rvs, t
				}
			}
		}
		return state
	}
	// Fallback: match ids in the widget's HTML.
	if m := regexp.MustCompile(`/rv/mappings/4/12/(\d+)/`).FindStringSubmatch(raw); m != nil {
		pcID, _ := strconv.Atoi(m[1])
		if rvCache.pcID != pcID {
			if rv := fetchRVMapping(pcID); rv != 0 {
				rvCache.rvID, rvCache.pcID, rvCache.lastRVPoll = rv, pcID, 0
			}
		}
		if rvCache.rvID != 0 {
			if data := fetchRVLive(rvCache.rvID); data != nil {
				return parseRVToState(data, homeName, awayName)
			}
		}
	}
	return nil
}

func listOfMaps(v any) []M {
	var out []M
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// parseRVToState converts ResultsVault match JSON into the overlay's state.
func parseRVToState(data M, homeName, awayName string) M {
	teams := listOfMaps(data["MatchTeams"])
	if len(teams) == 0 {
		return nil
	}
	maxOvers := gi(gm(data, "MatchConfig"), "max_overs", 50)
	if maxOvers == 0 {
		maxOvers = 50
	}
	bat := -1
	var cur M
	for i, t := range teams {
		for _, inn := range listOfMaps(t["Innings"]) {
			if inn["status"] != nil && pyval.IntOr(inn["status"], -1) == 0 {
				bat, cur = i, inn
			}
		}
	}
	if bat < 0 {
		for i, t := range teams {
			if inns := listOfMaps(t["Innings"]); len(inns) > 0 {
				bat, cur = i, inns[len(inns)-1]
			}
		}
	}
	if bat < 0 || cur == nil {
		return nil
	}
	battingTeam, bowlingTeam := teams[bat], teams[len(teams)-1]
	for i, t := range teams {
		if i != bat {
			bowlingTeam = t
			break
		}
	}
	runs, wickets := gi(cur, "runs", 0), gi(cur, "wickets", 0)
	overs := gf(cur, "overs_bowled", 0)
	innNum := gi(cur, "innings_number", 1)
	if innNum == 0 {
		innNum = 1
	}
	live := cur["status"] != nil && pyval.IntOr(cur["status"], -1) == 0
	var batting, bowling []M
	for _, p := range listOfMaps(cur["PlayerPerfs"]) {
		t := gs(p, "__type", "")
		if strings.Contains(t, "Batting") {
			batting = append(batting, p)
		}
		if strings.Contains(t, "Bowling") {
			bowling = append(bowling, p)
		}
	}
	var atCrease []M
	for _, b := range batting {
		if pyval.IntOr(b["dismissal_id"], -1) == 1 {
			atCrease = append(atCrease, b)
		}
	}
	sort.SliceStable(atCrease, func(i, j int) bool { return gi(atCrease[i], "number", 99) < gi(atCrease[j], "number", 99) })
	b1, b2 := M{}, M{}
	if len(atCrease) > 0 {
		b1 = atCrease[0]
	}
	if len(atCrease) > 1 {
		b2 = atCrease[1]
	}
	// Current bowler — the highest over number, but only while the innings is in progress.
	cb := M{}
	if len(bowling) > 0 && live {
		cb = bowling[0]
		for _, b := range bowling[1:] {
			if gi(b, "number", 0) > gi(cb, "number", 0) {
				cb = b
			}
		}
	}
	rr := 0.0
	if overs > 0 {
		rr = pyfmt.Round(float64(runs)/overs, 2)
	}
	status := "complete"
	if live {
		status = "live"
	}
	say("  RV [%s] inn%d: %d-%d (%sov) | %s %s / %s %s | bowl: %s", status, innNum, runs, wickets, pyfmt.Float(overs),
		gs(b1, "player_name", "—"), anyStr(b1, "runs", "0"), gs(b2, "player_name", "—"), anyStr(b2, "runs", "0"), gs(cb, "player_name", "—"))
	state := M{
		"battingTeamName": gs(battingTeam, "team_name", homeName),
		"bowlingTeamName": gs(bowlingTeam, "team_name", awayName),
		"innings":         innNum, "score": runs, "wickets": wickets, "overs": overs,
		"batter1": M{"name": gs(b1, "player_name", "—"), "runs": gi(b1, "runs", 0), "balls": gi(b1, "balls", 0), "onStrike": true},
		"batter2": M{"name": gs(b2, "player_name", "—"), "runs": gi(b2, "runs", 0), "balls": gi(b2, "balls", 0), "onStrike": false},
		"bowler": M{"name": gs(cb, "player_name", "—"), "overs": anyStr(cb, "overs", "0"),
			"runs": gi(cb, "runs", 0), "wickets": gi(cb, "wickets", 0)},
		"statusText": "RR: " + pyfmt.Float(rr), "targetRuns": 0, "runsRequired": 0, "ballsRemaining": 0,
	}
	if innNum == 2 {
		for _, t := range teams {
			for _, inn := range listOfMaps(t["Innings"]) {
				if gi(inn, "innings_number", 0) == 1 && pyval.IntOr(inn["status"], -1) == 1 {
					target := gi(inn, "runs", 0) + 1
					state["targetRuns"] = target
					state["runsRequired"] = max(0, target-runs)
					state["ballsRemaining"] = int(math.RoundToEven(math.Max(0, float64(maxOvers)-overs) * 6))
					state["statusText"] = fmt.Sprintf("Need %d", max(0, target-runs))
				}
			}
		}
	}
	return state
}

// ── Event buffer: wickets queued for the overlay (and only the overlay) ──
var (
	prevState     = M{"score": nil, "wickets": nil, "overs": nil}
	prevStateMu   sync.Mutex
	eventBuffer   []M
	eventBufferMu sync.Mutex // append (live thread) vs pop (overlay poll)
)

// ResetEventDetection clears the buffer and the baseline (tests, a new match).
func ResetEventDetection() {
	prevStateMu.Lock()
	prevState = M{"score": nil, "wickets": nil, "overs": nil}
	prevStateMu.Unlock()
	eventBufferMu.Lock()
	eventBuffer = nil
	eventBufferMu.Unlock()
}

// drainEvents pops every buffered event (the overlay's /live poll).
func drainEvents() []M {
	eventBufferMu.Lock()
	defer eventBufferMu.Unlock()
	out := eventBuffer
	eventBuffer = nil
	if out == nil {
		out = []M{}
	}
	return out
}

// BufferPCSEvents detects wickets from the feed and buffers them for the overlay.
// Boundaries are detected client-side in the overlay from the ticker, which can tell a
// real four from 4 byes / leg-byes / wide+4 (a score delta alone can't), so no four/six
// replay events are emitted here; a probable six is only noted for the match report.
func BufferPCSEvents(state M) {
	prevStateMu.Lock()
	defer prevStateMu.Unlock()
	score, wickets := gi(state, "score", 0), gi(state, "wickets", 0)
	if prevState["score"] == nil {
		// First poll: seed the baseline HERE, unconditionally — once it only happened
		// inside an optional trigger, so no wicket ever reached the buffer.
		prevState = M{"score": score, "wickets": wickets, "overs": valOr(state, "overs", 0.0)}
		return
	}
	delta := score - gi(prevState, "score", 0)
	dw := wickets - gi(prevState, "wickets", 0)
	if dw > 0 {
		eventBufferMu.Lock()
		eventBuffer = append(eventBuffer, M{"type": "wicket", "score": score, "wickets": wickets})
		eventBufferMu.Unlock()
		b1, b2 := gm(state, "batter1"), gm(state, "batter2")
		out := gs(b2, "name", "")
		if gb(b1, "onStrike", false) {
			out = gs(b1, "name", "")
		}
		ov := pyval.Str(valOr(state, "overs", 0))
		if f, ok := state["overs"].(float64); ok {
			ov = pyfmt.Float(f)
		}
		MatchLogEvent("wicket", fmt.Sprintf("Wicket: %d-%d (%s ov)", score, wickets, ov))
		matchLogMu.Lock()
		matchLog.FallOfWickets = append(matchLog.FallOfWickets, M{"batter": out,
			"score": fmt.Sprintf("%d-%d", score, wickets), "over": valOr(state, "overs", 0), "howout": ""})
		matchLogMu.Unlock()
	}
	if delta == 6 && dw == 0 {
		MatchLogEvent("six", fmt.Sprintf("Six — %d-%d", score, wickets))
	}
	prevState = M{"score": score, "wickets": wickets, "overs": valOr(state, "overs", 0.0)}
	eventBufferMu.Lock()
	if len(eventBuffer) > 20 {
		eventBuffer = eventBuffer[len(eventBuffer)-20:]
	}
	eventBufferMu.Unlock()
}
