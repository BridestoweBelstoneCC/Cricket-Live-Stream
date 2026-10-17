package server

// The scorer's scoreboard file. NV Play / PCS Pro writes it on every ball (JSON, even
// when the file is called .xml); this turns it into the overlay's live state. Far more
// reliable than any API — instant, no auth needed.

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
	"cricketstream/internal/scorerfile"
)

var (
	pcsMu        sync.Mutex
	pcsLastMtime float64
	pcsLastState M
	// inningsLatch is the latched innings number (see ParsePCSJSON); it survives the
	// winning runs.
	inningsLatch   = 1
	inningsLatchMu sync.Mutex
)

// SetInningsLatch resets the latch (tests, a new match).
func SetInningsLatch(n int) {
	inningsLatchMu.Lock()
	inningsLatch = n
	inningsLatchMu.Unlock()
}

// ResetPCSCache forgets the last frame read from disk.
func ResetPCSCache() {
	pcsMu.Lock()
	pcsLastMtime, pcsLastState = 0, nil
	pcsMu.Unlock()
}

// EffectivePCSFolder is which local folder to scan for the scorer's file. Every
// /live-adjacent call site must go through this (never read pcs_output_folder directly)
// so bridge mode and local mode are interchangeable everywhere at once: with a bridge
// URL set, the file to read is the local mirror.
func EffectivePCSFolder(state M) string {
	if trim(gs(state, "pcs_bridge_url", "")) != "" {
		return PCSBridgeCacheDir
	}
	return expandUser(trim(gs(state, "pcs_output_folder", "")))
}

// FindPCSOutputFile is the scorer's file in folder, or "".
func FindPCSOutputFile(folder string) string { return scorerfile.Find(folder, 10*time.Minute) }

// parseXMLFields reads a flat XML document as {child tag (lower-case): text}.
func parseXMLFields(raw string) (M, bool) {
	d := xml.NewDecoder(strings.NewReader(raw))
	out := M{}
	depth := 0
	var cur string
	var text strings.Builder
	rootSeen := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				rootSeen = true
			} else if depth == 2 {
				cur = strings.ToLower(t.Name.Local)
				text.Reset()
			}
		case xml.CharData:
			if depth == 2 {
				text.Write(t)
			}
		case xml.EndElement:
			if depth == 2 {
				out[cur] = strings.TrimSpace(text.String())
			}
			depth--
		}
	}
	return out, rootSeen
}

// parseScoreboardRaw turns a scoreboard file's text into the overlay state, or nil when
// it's neither JSON nor XML or the parse produced nothing usable. Shared by the local
// file and the scorer agent so the two sources can never drift in what they accept.
func parseScoreboardRaw(raw, name string) M {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		x, ok := parseXMLFields(raw)
		if !ok {
			return nil
		}
		data = x
	}
	obj, ok := data.(map[string]any)
	if !ok {
		say("  ✗  PCS parse error in %s: not a JSON object", orStr(name, "scoreboard file"))
		return nil
	}
	return ParsePCSJSON(obj)
}

func logScore(state M, source string) {
	b1, b2 := gm(state, "batter1"), gm(state, "batter2")
	say("  %s: %s %s-%s (%s ov) | %s %s / %s %s", source, gs(state, "battingTeamName", ""),
		anyStr(state, "score", ""), anyStr(state, "wickets", ""), oversStr(state["overs"]),
		gs(b1, "name", ""), anyStr(b1, "runs", ""), gs(b2, "name", ""), anyStr(b2, "runs", ""))
}

// oversStr renders the state's overs (a float) as Python would ("5.0", "4.3").
func oversStr(v any) string {
	if f, ok := v.(float64); ok {
		return pyfmt.Float(f)
	}
	return pyval.Str(v)
}

// pcsDebugDiagnostics surfaces the NV Play fields most likely to be misconfigured
// (truncated names, wrong wicket-field template mapping). A {{...}} value means NV Play
// did not recognise that field. raw is the RAW field dict, not an overlay state.
func pcsDebugDiagnostics(raw M) M {
	if raw == nil {
		return nil
	}
	if _, ok := raw["error"]; ok {
		return nil
	}
	show := func(k string) any {
		v, ok := raw[k]
		if !ok {
			return "(field absent)"
		}
		if s, ok := v.(string); ok && strings.HasPrefix(s, "{{") {
			return "NOT RECOGNISED BY NV PLAY (" + s + ")"
		}
		return v
	}
	return M{
		"names": M{
			"batter1_name": show("batter1_name"), "batter2_name": show("batter2_name"),
			"bowler_name": show("bowler_name"),
			"note": "If a name here is shorter than reality (e.g. 'Harmiso' for " +
				"'Harmison'), NV Play itself is truncating it — check the player's " +
				"name in the NV Play team list and any scoreboard name-length limit.",
		},
		"wicket_fields": M{
			"batter1_howout (Wicket)":         show("batter1_howout"),
			"batter1_wickettype (WicketType)": show("batter1_wickettype"),
			"batter1_wicketfielder (Fielder)": show("batter1_wicketfielder"),
			"batter2_howout (Wicket)":         show("batter2_howout"),
			"batter2_wickettype (WicketType)": show("batter2_wickettype"),
			"batter2_wicketfielder (Fielder)": show("batter2_wicketfielder"),
			"note": "After a wicket, at least one set should show real values " +
				"(e.g. type='Caught', fielder='Jones', Wicket='Smith'). If they show " +
				"'NOT RECOGNISED', the field names in scoreboard.template don't " +
				"match this NV Play version — read the real names from the keys list " +
				"below and tell Claude so the template can be corrected.",
		},
		"ball_ticker": show("last_ball"),
	}
}

// ReadPCSFile reads the scorer's file from a local folder (or the bridge mirror) and
// returns the overlay state. nil if the file isn't found; on a transient read/parse
// error (mid-write, momentarily corrupt) it holds the last good frame rather than
// blanking the overlay.
func ReadPCSFile(folder string) M {
	path := FindPCSOutputFile(folder)
	if path == "" {
		return nil
	}
	pcsMu.Lock()
	defer pcsMu.Unlock()
	mt := mtime(path)
	if mt == pcsLastMtime && pcsLastState != nil {
		return pcsLastState
	}
	raw, err := scorerfile.ReadText(path)
	if err != nil {
		say("  ✗  PCS file read error: %v", err)
		return pcsLastState
	}
	if state := parseScoreboardRaw(raw, filepath.Base(path)); state != nil {
		pcsLastMtime, pcsLastState = mt, state
		logScore(state, "PCS")
		return state
	}
	return pcsLastState
}

// FrameToM turns an engine frame (all strings) into a decoded-JSON-style object.
func FrameToM(f map[string]string) M {
	out := make(M, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}

// ParsePCSJSON converts the scorer's frame into the overlay's state, or nil when the
// frame has no batting team yet.
func ParsePCSJSON(data M) M {
	// g handles PCS's {{field}} placeholders for unset values, and falls back to the
	// default on a blank string too — NV Play renders the template as soon as a match
	// starts, so fields like batter1_name are genuinely empty until the scorer selects the
	// openers. Without this, pre-game state looks like a real (blank-named) player.
	g := func(key string, def any) any {
		v, ok := data[key]
		if !ok {
			return def
		}
		if s, isStr := v.(string); isStr && (strings.HasPrefix(s, "{{") || strings.TrimSpace(s) == "") {
			return def
		}
		return v
	}
	gis := func(key string, def int) int {
		v := g(key, def)
		if !pyval.Truthy(v) {
			v = 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(pyval.Str(v)), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return def
		}
		return int(f)
	}
	gfs := func(key string, def float64) float64 {
		v := g(key, def)
		if !pyval.Truthy(v) {
			v = 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(pyval.Str(v)), 64)
		if err != nil {
			return def
		}
		return f
	}
	gstr := func(key, def string) string { return pyval.Str(g(key, def)) }

	runs, wickets := gis("runs", 0), gis("wickets", 0)
	overs := gfs("overs", 0)

	// Innings detection. The reliable live signal is runs_required, positive throughout
	// the 2nd innings — EXCEPT it drops to 0 the instant the target is reached, which
	// would flip back to "1st innings" on the winning runs. So it LATCHES: once in the 2nd
	// innings it stays there until a genuinely fresh innings begins (under an over, 0/0).
	inningsLatchMu.Lock()
	runsReq := gis("runs_required", 0)
	fresh := overs < 1.0 && runs == 0 && wickets == 0
	var inn int
	switch {
	case runsReq > 0:
		inn, inningsLatch = 2, 2
	case inningsLatch == 2 && !fresh:
		inn = 2 // hold the 2nd innings through the winning runs
	default:
		inn, inningsLatch = 1, 1
	}
	inningsLatchMu.Unlock()

	battingTeam := g("batting_team", "Batting")
	bowlingTeam := g("bowling_team", "Bowling")

	b1Name := g("batter1_name", "—")
	b1Number := strings.TrimSpace(gstr("batter1_number", ""))
	b1Runs, b1Balls := gis("batter1_runs", 0), gis("batter1_balls", 0)
	// Striker: NV Play may output True/False/1/0/"" in either field.
	truthy := func(s string) bool {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes", "*":
			return true
		}
		return false
	}
	b1s, b2s := truthy(gstr("batter1_strike", "")), truthy(gstr("batter2_strike", ""))
	b1Strike := true // batter1 on strike unless batter2 explicitly is
	if b2s && !b1s {
		b1Strike = false
	}
	sr := func(key string, r, b int) float64 {
		if v := gfs(key, 0); v != 0 {
			return v
		}
		if b > 0 {
			return pyfmt.Round(float64(r)/float64(b)*100, 1)
		}
		return 0
	}
	b2Name := g("batter2_name", "—")
	b2Number := strings.TrimSpace(gstr("batter2_number", ""))
	b2Runs, b2Balls := gis("batter2_runs", 0), gis("batter2_balls", 0)

	rr := gfs("run_rate", 0)
	target := gis("target", 0)

	if !pyval.Truthy(battingTeam) || battingTeam == "—" {
		return nil
	}
	status := "—"
	if rr != 0 {
		status = "RR: " + pyfmt.Fixed(rr, 2)
	}
	runsRequired := gis("runs_required", 0)
	if runsRequired == 0 && target != 0 {
		runsRequired = max(0, target-runs)
	}
	strip := func(k string) string { return strings.TrimSpace(gstr(k, "")) }
	state := M{
		"battingTeamName": battingTeam,
		"bowlingTeamName": bowlingTeam,
		"innings":         inn,
		"score":           runs,
		"wickets":         wickets,
		"overs":           overs,
		"batter1": M{"name": b1Name, "number": b1Number, "runs": b1Runs, "balls": b1Balls, "onStrike": b1Strike,
			"sr": sr("batter1_sr", b1Runs, b1Balls), "fours": gis("batter1_fours", 0), "sixes": gis("batter1_sixes", 0), "howOut": ""},
		"batter2": M{"name": b2Name, "number": b2Number, "runs": b2Runs, "balls": b2Balls, "onStrike": !b1Strike,
			"sr": sr("batter2_sr", b2Runs, b2Balls), "fours": gis("batter2_fours", 0), "sixes": gis("batter2_sixes", 0), "howOut": ""},
		"bowler": M{"name": g("bowler_name", "—"), "overs": gstr("bowler_overs", "0"), "runs": gis("bowler_runs", 0),
			"wickets": gis("bowler_wickets", 0), "maidens": gis("bowler_maidens", 0)},
		"statusText":        status,
		"run_rate":          rr,
		"targetRuns":        target,
		"partnership_runs":  gis("partnership_runs", 0),
		"partnership_balls": gis("partnership_balls", 0),
		// Most-recent dismissal, straight from PCS Pro's pre-formatted LastWicket fields:
		// "C JONES B HOGGARD", "LBW B MCGRATH", "RUN OUT (GILCHRIST)"... passed verbatim.
		"lastWicketHowOut":  strip("last_wicket_howout"),
		"lastWicketBatter":  strip("last_wicket_batter"), // e.g. "MARTY 12"
		"lastWicketBowler":  strip("last_wicket_bowler"),
		"lastWicketFielder": strip("last_wicket_fielder"),
		"lastWicketType":    strip("last_wicket_type"),
		"last_ball":         g("last_ball", ""),
		"last_over_runs":    gis("last_over_runs", 0),
		"over_history":      g("over_history", ""),
		"pcs_overs":         overs,
		"runsRequired":      runsRequired,
		"ballsRemaining":    gis("balls_remaining", 0),
	}
	// The full scorecard (template v2.1 fields) for the innings-break card. Older
	// templates lack the keys, so the card comes out empty — harmless.
	bat, bwl := []any{}, []any{}
	for i := 1; i <= 11; i++ {
		n := strconv.Itoa(i)
		if nm := strings.TrimSpace(gstr("card_b"+n+"_name", "")); nm != "" && !strings.HasPrefix(nm, "{{") {
			bat = append(bat, M{"name": nm, "runs": gis("card_b"+n+"_runs", 0), "balls": gis("card_b"+n+"_balls", 0),
				"out": strings.TrimSpace(gstr("card_b"+n+"_out", ""))})
		}
		if wn := strings.TrimSpace(gstr("card_w"+n+"_name", "")); wn != "" && !strings.HasPrefix(wn, "{{") {
			bwl = append(bwl, M{"name": wn, "o": strings.TrimSpace(gstr("card_w"+n+"_o", "0")),
				"m": gis("card_w"+n+"_m", 0), "r": gis("card_w"+n+"_r", 0), "w": gis("card_w"+n+"_w", 0)})
		}
	}
	state["card"] = M{"batters": bat, "bowlers": bwl}
	return state
}
