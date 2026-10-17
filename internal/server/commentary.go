package server

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ai"
	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

// anthropicKey is the configured key, else the environment's.
func anthropicKey(cfg M) string {
	if k := trim(gs(cfg, "anthropic_api_key", "")); k != "" {
		return k
	}
	return trim(os.Getenv("ANTHROPIC_API_KEY"))
}

// firstSentence trims a model reply to its first sentence, as the original did when the
// model returned more than asked.
func firstSentence(t string) string {
	t = strings.Trim(strings.Trim(strings.TrimSpace(t), `"`), "'")
	if i := strings.Index(t, "."); i >= 0 {
		return strings.TrimSpace(t[:i]) + "."
	}
	return t
}

// pyNum renders a JSON number the way Python's f-string would show the value it held.
func pyNum(v any) string {
	switch x := v.(type) {
	case float64:
		if x == float64(int64(x)) {
			// The state carries overs as a float (5.0) but scores as ints; callers pass
			// the right one. A float that happens to be whole renders like Python's float.
			return pyfmt.Float(x)
		}
		return pyfmt.Float(x)
	case int:
		return strconv.Itoa(x)
	}
	return pyval.Str(v)
}

// ── AI commentary: the panel's test button ────────────────────
// The live AI commentary is the end-of-over panel (/commentary/over/generate, fired by
// the overlay). This one-line generator only backs "Generate test commentary".

// GenerateCommentary writes one broadcast-style line for a match situation, or "" (the
// reason is printed).
func GenerateCommentary(state M, history []string) string {
	cfg := LoadState()
	key := anthropicKey(cfg)
	if key == "" {
		say("  ✗  Commentary: no Anthropic API key set")
		return ""
	}
	score, wickets := gi(state, "score", 0), gi(state, "wickets", 0)
	overs := gf(state, "overs", 0)
	rr := 0.0
	if overs > 0 {
		rr = pyfmt.Round(float64(score)/overs, 2)
	}
	b1, b2, bw := gm(state, "batter1"), gm(state, "batter2"), gm(state, "bowler")
	ctx := fmt.Sprintf("Match: %s vs %s\nScore: %d-%d off %s overs (max %s)\nRun rate: %s\n"+
		"Batters: %s %s (%sb), %s %s (%sb)\nBowler: %s %s-%s (%s ov)\n",
		gs(state, "battingTeamName", "Batting team"), gs(state, "bowlingTeamName", "Bowling team"),
		score, wickets, pyfmt.Float(overs), pyval.Str(cfg["max_overs"]), rrStr(rr, overs),
		gs(b1, "name", "?"), anyStr(b1, "runs", "0"), anyStr(b1, "balls", "0"),
		gs(b2, "name", "?"), anyStr(b2, "runs", "0"), anyStr(b2, "balls", "0"),
		gs(bw, "name", "?"), anyStr(bw, "wickets", "0"), anyStr(bw, "runs", "0"), anyStr(bw, "overs", "0"))
	if len(history) > 0 {
		h := history
		if len(h) > 3 {
			h = h[len(h)-3:]
		}
		ctx += "Recent events: " + strings.Join(h, "; ") + "\n"
	}
	prompt := "You are a cricket commentator for a club match in Devon, England. " +
		"Based on the following match situation, write exactly ONE short, " +
		"broadcast-style commentary observation (maximum 15 words). " +
		"Be specific to the numbers. No filler. No emojis. " +
		"Sound like Sky Sports, not a school report.\n\n" + ctx + "\nCommentary:"
	text, err := ai.Ask(context.Background(), ai.Request{Key: key, Prompt: prompt, MaxTokens: 60, Timeout: 30 * time.Second})
	if err != nil {
		say("  ✗  Commentary API error: %v", err)
		return ""
	}
	return firstSentence(text)
}

func rrStr(rr, overs float64) string {
	if overs > 0 {
		return pyfmt.Float(rr)
	}
	return "0"
}

// anyStr is str(d.get(k, def)).
func anyStr(m M, k, def string) string {
	v, ok := m[k]
	if !ok {
		return def
	}
	return pyval.Str(v)
}

// ── Latest end-of-over commentary (the overlay's 4th panel; the panel previews it) ──
var (
	overCommentary   = M{"text": "", "over": -1}
	overCommentaryMu sync.Mutex
)

func getOverCommentary() M {
	overCommentaryMu.Lock()
	defer overCommentaryMu.Unlock()
	return copyM(overCommentary)
}

func setOverCommentary(m M) {
	overCommentaryMu.Lock()
	overCommentary = m
	overCommentaryMu.Unlock()
}

// ── Match log: key events for the end-of-match report ──
// It dies with any restart, so it only ever fills gaps in the scorer's own figures.
type matchLogT struct {
	Events        []M
	Innings       map[string]M
	FallOfWickets []M
	Milestones    []M
	Started       string
}

var (
	matchLog   = matchLogT{Innings: map[string]M{}}
	matchLogMu sync.Mutex
)

// MatchLogEvent records a notable match event for the report generator.
func MatchLogEvent(kind, detail string) {
	matchLogMu.Lock()
	defer matchLogMu.Unlock()
	if matchLog.Started == "" {
		matchLog.Started = time.Now().Format("2006-01-02T15:04:05.000000")
	}
	matchLog.Events = append(matchLog.Events, M{"kind": kind, "detail": detail})
	if len(matchLog.Events) > 400 {
		matchLog.Events = matchLog.Events[len(matchLog.Events)-400:]
	}
}

// MatchLogSnapshot updates per-innings running totals from the current state.
func MatchLogSnapshot(state M) {
	matchLogMu.Lock()
	defer matchLogMu.Unlock()
	inn := anyStr(state, "innings", "1")
	rec := matchLog.Innings[inn]
	if rec == nil {
		rec = M{}
		matchLog.Innings[inn] = rec
	}
	rec["batting_team"] = gs(state, "battingTeamName", "")
	rec["bowling_team"] = gs(state, "bowlingTeamName", "")
	rec["score"] = valOr(state, "score", 0)
	rec["wickets"] = valOr(state, "wickets", 0)
	rec["overs"] = valOr(state, "overs", 0)
}

func valOr(m M, k string, def any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func MatchLogReset() {
	matchLogMu.Lock()
	defer matchLogMu.Unlock()
	matchLog = matchLogT{Innings: map[string]M{}}
}

// matchLogCopy is a defensive copy for the report/social generators.
func matchLogCopy() M {
	matchLogMu.Lock()
	defer matchLogMu.Unlock()
	inn := M{}
	for k, v := range matchLog.Innings {
		inn[k] = copyM(v)
	}
	cp := func(l []M) []any {
		out := make([]any, len(l))
		for i, v := range l {
			out[i] = copyM(v)
		}
		return out
	}
	return M{"innings": inn, "fall_of_wickets": cp(matchLog.FallOfWickets),
		"milestones": cp(matchLog.Milestones), "events": cp(matchLog.Events)}
}

// GenerateOverCommentary asks for one punchy end-of-over sentence, in the background;
// the result lands in overCommentary for the overlay's commentary panel.
func GenerateOverCommentary(overNum int, overRuns int, bowler, figs, ballsStr string, state M) {
	cfg := LoadState()
	key := anthropicKey(cfg)
	if key == "" {
		return
	}
	sc, wk := gi(state, "score", 0), gi(state, "wickets", 0)
	ov := gf(state, "overs", float64(overNum))
	if ov == 0 {
		ov = float64(overNum)
		if ov == 0 {
			ov = 1
		}
	}
	rr := pyfmt.Round(float64(sc)/ov, 2)
	pr, pb := anyStr(state, "partnership_runs", "0"), anyStr(state, "partnership_balls", "0")
	bat := gs(state, "battingTeamName", gs(cfg, "home_team", ""))
	mo := anyStr(cfg, "max_overs", "50")
	wkt := "no wickets"
	if strings.Contains(ballsStr, "W") {
		wkt = "including a wicket"
	}
	pl := "s"
	if overRuns == 1 {
		pl = ""
	}
	// Season-stat notables: give the commentator the storylines (season-best scores,
	// past-average innings) so the AI can mention them like a real broadcast would.
	var notable []string
	func() {
		defer func() { recover() }()
		for _, bk := range []string{"batter1", "batter2"} {
			b := gm(state, bk)
			nm, rn := gs(b, "name", ""), gi(b, "runs", 0)
			if nm == "" || nm == "—" {
				continue
			}
			lk, _ := resolvePlayer(nm, gsOr(b, "number", ""))
			rec := lookupSeasonStats(lk)
			if rec == nil {
				continue
			}
			hs, _ := strconv.Atoi(strings.ReplaceAll(gsOr(rec, "hs", "0"), "*", ""))
			av := gf(rec, "avg", 0)
			switch {
			case hs != 0 && rn > hs:
				notable = append(notable, fmt.Sprintf("%s has passed a season-best of %s — a new highest score of the season.", nm, anyStr(rec, "hs", "")))
			case av != 0 && float64(rn) > av*1.5 && rn >= 25:
				notable = append(notable, fmt.Sprintf("%s (%d) is well past a season average of %s.", nm, rn, pyfmt.Fixed(av, 0)))
			}
		}
	}()
	// "Coming up" milestones — the anticipation angle. The overlay fires its own graphic
	// once these are reached; this gives the commentator a reason to build tension first.
	for _, bk := range []string{"batter1", "batter2"} {
		b := gm(state, bk)
		nm, rn := gs(b, "name", ""), gi(b, "runs", 0)
		if nm == "" || nm == "—" {
			continue
		}
		target := 0
		if rn < 50 {
			target = 50
		} else if rn < 100 {
			target = 100
		}
		if target != 0 && target-rn > 0 && target-rn <= 15 {
			label := "half-century"
			if target == 100 {
				label = "century"
			}
			notable = append(notable, fmt.Sprintf("%s is %d runs from a %s.", nm, target-rn, label))
		}
	}
	parts := strings.Split(figs, "-")
	if w, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1])); err == nil && (w == 3 || w == 4) {
		need := 5 - w
		s := "s"
		if need == 1 {
			s = ""
		}
		notable = append(notable, fmt.Sprintf("%s is %d wicket%s from a five-wicket haul.", bowler, need, s))
	}
	if next := (sc/50 + 1) * 50; next-sc > 0 && next-sc <= 15 {
		notable = append(notable, fmt.Sprintf("%s are %d runs from %d.", bat, next-sc, next))
	}
	notableTxt := ""
	if len(notable) > 0 {
		notableTxt = " Notable: " + strings.Join(notable, " ")
	}
	prompt := "You are a cricket commentator for a village match in Devon, England. " +
		"Write ONE punchy broadcast-style sentence (max 20 words) summarising " +
		"the over. Be specific to numbers. Like Sky Sports - direct, vivid. " +
		"If a \"Notable\" fact is given, build the sentence around it.\n\n" +
		fmt.Sprintf("Over %d: %d run%s (%s). ", overNum, overRuns, pl, wkt) +
		fmt.Sprintf("Bowler: %s %s. ", bowler, figs) +
		fmt.Sprintf("Score: %s %d-%d off %d ov (RR %s, max %s). ", bat, sc, wk, overNum, pyfmt.Float(rr), mo) +
		fmt.Sprintf("Partnership: %s off %s balls. Balls: %s.%s\n", pr, pb, ballsStr, notableTxt) +
		"Commentary:"
	safeGo("over commentary", func() {
		text, err := ai.Ask(context.Background(), ai.Request{Key: key, Prompt: prompt, MaxTokens: 80, Timeout: 30 * time.Second})
		if err != nil {
			say("  ✗  Over commentary: %v", err)
			return
		}
		t := firstSentence(text)
		setOverCommentary(M{"text": t, "over": overNum})
		say("  ✓  Over %d commentary: %s", overNum, truncRunes(t, 55))
		if gb(LoadState(), "graphics_voice_commentary", false) {
			SpeakOverCommentary(t, overNum)
		}
	})
}

// ── Pending commands for the overlay ─────────────────────────
// GET /commands POPS the queue: only the OBS overlay may call it, or a second consumer
// silently steals replay/scorecard commands from the real one.
var (
	commands = M{"show_weather": false, "hide_weather": false, "show_scorecard": false,
		"show_player_cards": false}
	commandsMu sync.Mutex
)

func setCommand(key string, val any) {
	commandsMu.Lock()
	commands[key] = val
	commandsMu.Unlock()
}

func popCommands() M {
	commandsMu.Lock()
	defer commandsMu.Unlock()
	s := M{}
	for k, v := range commands {
		s[k] = v
		commands[k] = false
	}
	return s
}
