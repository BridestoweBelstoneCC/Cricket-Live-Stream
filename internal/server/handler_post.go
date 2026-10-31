package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cricketstream/internal/agent"
	"cricketstream/internal/logobg"
	"cricketstream/internal/obsws"
	"cricketstream/internal/pyval"
	"cricketstream/internal/scoring"
)

// overlayEndpoints are fired automatically by the overlay itself — a loopback OBS
// browser source with no login flow of its own. /commentary/over/generate additionally
// spends Anthropic credit, so all of them trust loopback callers with no forwarding proxy
// in front of them (see trustedLoopback), or a valid session either way, rather than
// requiring a session the overlay can never obtain. The checklist never belongs here: it
// starts OBS and goes live.
var overlayEndpoints = map[string]bool{"/commentary/over/generate": true, "/replay": true,
	"/weather/show": true, "/weather/hide": true, "/camera/scene": true, "/sponsor/airtime": true,
	"/break/start": true, "/break/end": true}

// jsonObject is json.loads(body or "{}") required to be an object; anything else is
// an empty object, as the Python routes that tolerated bad bodies treated it.
func jsonObject(body []byte) M {
	if len(bytes.TrimSpace(body)) == 0 {
		return M{}
	}
	var d M
	if json.Unmarshal(body, &d) != nil || d == nil {
		return M{}
	}
	return d
}

// strictObject is json.loads(body) for the routes where a bad body is an error.
func strictObject(body []byte) (M, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	d, ok := v.(M)
	if !ok {
		return nil, errNotObject
	}
	return d, nil
}

// str is Python's str(d.get(k) or def).
func str(d M, k, def string) string {
	if v := d[k]; pyval.Truthy(v) {
		return pyval.Str(v)
	}
	return def
}

func (c *ctx) readBody() ([]byte, bool) {
	if c.r.ContentLength > MaxBodyBytes {
		c.w.Header().Set("Content-Length", "0")
		c.w.WriteHeader(413)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(c.r.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, false
	}
	if len(body) > MaxBodyBytes {
		c.w.Header().Set("Content-Length", "0")
		c.w.WriteHeader(413)
		return nil, false
	}
	return body, true
}

func (c *ctx) sponsorUpload() {
	// Before the generic body read: an image is bigger than MaxBodyBytes, and whoever
	// sends one has to be logged in BEFORE we read megabytes of it.
	if !c.originOK() {
		c.json(M{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	if !c.checkToken() {
		return
	}
	n := c.r.ContentLength
	if n <= 0 || n > SponsorUploadMaxBytes {
		c.json(M{"ok": false, "error": fmt.Sprintf("Logo must be under %d MB.", SponsorUploadMaxBytes/(1024*1024))}, 413)
		return
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(c.r.Body, data); err != nil {
		c.json(M{"ok": false, "error": "Upload interrupted: " + err.Error()}, 400)
		return
	}
	ok, result := saveSponsorLogo(data)
	if !ok {
		c.json(M{"ok": false, "error": result}, 400)
		return
	}
	plain := false
	if im, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		plain = logobg.HasPlainBackground(im)
	}
	c.json(M{"ok": true, "sponsor_id": result, "plain_background": plain})
}

// libraryUpload is POST /library/upload?kind=&name=: a picture for the Pictures tab, as
// the raw image (like /sponsor/upload: logged in before megabytes are read).
func (c *ctx) libraryUpload() {
	if !c.originOK() {
		c.json(M{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	if !c.checkToken() {
		return
	}
	n := c.r.ContentLength
	if n <= 0 || n > SponsorUploadMaxBytes {
		c.json(M{"ok": false, "error": fmt.Sprintf("Pictures must be under %d MB.", SponsorUploadMaxBytes/(1024*1024))}, 413)
		return
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(c.r.Body, data); err != nil {
		c.json(M{"ok": false, "error": "Upload interrupted: " + err.Error()}, 400)
		return
	}
	file, err := LibrarySave(c.q("kind"), c.q("name"), data)
	if err != nil {
		c.json(M{"ok": false, "error": capitalise(err.Error())}, 400)
		return
	}
	c.json(M{"ok": true, "file": file})
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (c *ctx) handleLogin(body []byte) {
	if ClubPassword == "" {
		// Auth disabled — issue a token so the browser can proceed normally
		tok := ""
		if ControlToken != "" {
			tok, _ = makeSessionToken()
		}
		c.json(M{"ok": true, "session_token": tok})
		return
	}
	ip := c.clientIP()
	t := now()
	loginAttemptsMu.Lock()
	rec := loginAttempts[ip]
	if rec == nil {
		rec = &loginRecord{}
		loginAttempts[ip] = rec
	}
	lockedUntil := rec.lockedUntil
	loginAttemptsMu.Unlock()
	if lockedUntil > t {
		wait := int(lockedUntil-t) + 1
		say("  ✗  Login: %s is locked out (%ds remaining)", ip, wait)
		authLogAdd("locked_out", ip)
		c.json(M{"ok": false, "error": fmt.Sprintf("Too many attempts — try again in %ds", wait)}, 429)
		return
	}
	pw := ""
	if d, err := strictObject(body); err == nil {
		pw, _ = d["password"].(string)
	}
	if pw != "" && constantTimeEqual(pw, ClubPassword) {
		loginAttemptsMu.Lock()
		delete(loginAttempts, ip)
		loginAttemptsMu.Unlock()
		tok, err := makeSessionToken()
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()}, 500)
			return
		}
		say("  ✓  Login: session issued [%s]", ip)
		authLogAdd("login_ok", ip)
		c.json(M{"ok": true, "session_token": tok})
		return
	}
	loginSleep(time.Second) // slow down brute-force attempts — never log the password itself
	loginAttemptsMu.Lock()
	rec = loginAttempts[ip]
	if rec == nil {
		rec = &loginRecord{}
		loginAttempts[ip] = rec
	}
	kept := rec.failures[:0]
	for _, f := range rec.failures {
		if t-f < LoginWindowSec {
			kept = append(kept, f)
		}
	}
	rec.failures = append(kept, t)
	count := len(rec.failures)
	if count >= LoginMaxFailures {
		rec.lockedUntil = t + LoginLockoutSec
	}
	loginAttemptsMu.Unlock()
	say("  ✗  Login: bad password, %d failure(s) in window [%s]", count, ip)
	authLogAdd("login_fail", ip)
	c.json(M{"ok": false, "error": "Wrong password"}, 401)
}

func (c *ctx) doPOST() {
	path := c.path
	if path == "/sponsor/upload" {
		c.sponsorUpload()
		return
	}
	if path == "/library/upload" {
		c.libraryUpload()
		return
	}
	body, ok := c.readBody()
	if !ok {
		return
	}
	if !c.originOK() {
		c.json(M{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	if path == "/login" {
		c.handleLogin(body)
		return
	}
	if overlayEndpoints[path] {
		if !c.trustedLoopback() && !c.checkToken() {
			return
		}
	} else if !c.checkToken() {
		return
	}

	switch {
	case path == "/settings/save":
		c.settingsSave(body)

	case path == "/settings/check":
		c.settingsCheck(body)

	case path == "/settings/open_folder":
		c.settingsOpenFolder(body)

	case path == "/rehearsal":
		d := jsonObject(body)
		switch str(d, "action", "") {
		case "start":
			if err := StartRehearsal(str(d, "scenario", "full"), gi(d, "overs", 5)); err != nil {
				c.json(M{"ok": false, "error": err.Error()}, 409)
				return
			}
		case "stop":
			StopRehearsal()
		default:
			c.json(M{"ok": false, "error": "action must be start or stop"}, 400)
			return
		}
		out := RehearsalStatus()
		out["ok"] = true
		c.json(out)

	case path == "/cameras/use":
		out, status := CamerasUse(jsonObject(body))
		c.json(out, status)

	case path == "/agent/pair":
		// The scorer's laptop's pairing code (shown in its window), for wifi where "Find
		// scorer laptop" finds nothing. Saved only once the laptop answers.
		addr, err := agent.ParsePairingCode(str(jsonObject(body), "code", ""))
		if err != nil {
			c.json(M{"ok": false, "error": capitalise(err.Error())}, 400)
			return
		}
		info, why := AgentPing(addr, 4*time.Second)
		if info == nil {
			c.json(M{"ok": false, "error": why + " — is CricketStream open on the scorer's laptop, on the same network?"}, 200)
			return
		}
		UpdateState(func(st M) { st["agent_host"], st["pcs_source"] = addr, "agent" })
		ResetAgentClient()
		c.json(M{"ok": true, "address": addr, "hostname": valOr(info, "hostname", ""), "file": info["file"]})

	case path == "/library/delete":
		d := jsonObject(body)
		if err := LibraryDelete(str(d, "kind", ""), str(d, "file", "")); err != nil {
			c.json(M{"ok": false, "error": capitalise(err.Error())}, 400)
			return
		}
		c.json(M{"ok": true})

	case path == "/library/rename":
		d := jsonObject(body)
		file, err := LibraryRename(str(d, "kind", ""), str(d, "file", ""), str(d, "name", ""))
		if err != nil {
			c.json(M{"ok": false, "error": capitalise(err.Error())}, 400)
			return
		}
		c.json(M{"ok": true, "file": file})

	case path == "/sponsor/remove_background":
		d := jsonObject(body)
		ok, out := saveTransparentSponsor(str(d, "id", ""), str(d, "mode", ""))
		c.json(out, statusFor(ok))

	case path == "/camera/cut_test":
		// The panel's "Test the camera cut". Never the overlay's loopback carve-out: it
		// cuts the picture.
		status, out := StartCutTest()
		c.json(out, status)

	case path == "/checklist/action":
		status, out := ChecklistAction(str(jsonObject(body), "action", ""))
		c.json(out, status)

	case path == "/sponsor/airtime":
		// The overlay's show/hide beacon. Only the event is taken from the request: the
		// time comes from this clock and the sponsor's name from state.
		event, _ := jsonObject(body)["event"].(string)
		if event != "show" && event != "hide" {
			c.json(M{"ok": false, "error": "event must be show or hide"}, 400)
			return
		}
		SponsorAirtimeEvent(event, now())
		c.json(M{"ok": true})

	case path == "/state":
		incoming, err := strictObject(body)
		if err != nil {
			// A non-object body must be rejected outright: writing it to the state file
			// would break every later LoadState until the file is hand-fixed.
			msg := err.Error()
			if errors.Is(err, errNotObject) {
				msg = "state must be a JSON object"
			}
			c.json(M{"ok": false, "error": msg}, 400)
			return
		}
		// Drop sentinel-valued secrets: that's the control panel round-tripping the
		// redacted value from GET /state, meaning "leave the stored key unchanged".
		for _, k := range SecretKeys {
			if incoming[k] == SecretSentinel {
				delete(incoming, k)
			}
		}
		// MERGE into existing state rather than replacing the whole file. The panel form
		// only posts its own fields, so a plain replace would wipe keys that have no form
		// input (home_club_id / away_club_id badges, drinks_over...). One locked
		// load→merge→save, so a panel save can't write back a stale copy over a change
		// another thread made meanwhile (a logo upload setting sponsor_id, say).
		UpdateState(func(cur M) {
			// If the user retyped the scorer laptop's address or switched source, forget
			// where we thought the agent was so the next poll honours what they asked for.
			if !jsonEqual(incoming["agent_host"], cur["agent_host"]) || !jsonEqual(incoming["pcs_source"], cur["pcs_source"]) {
				agentMu.Lock()
				agentResolved, agentLastError, agentNextDiscover = "", "", 0
				agentMu.Unlock()
			}
			for k, v := range incoming {
				cur[k] = v
			}
		})
		c.json(M{"ok": true})
		s := LoadState()
		replay := "off"
		if pyval.Truthy(s["replay_enabled"]) {
			replay = "on"
		}
		say("  ✓  %s  home=%v  away=%v  replay=%s", gsOr(s, "away_team", "?"), pyStr(s["home_colour"]),
			pyStr(s["away_colour"]), replay)

	case path == "/replay":
		state := LoadState()
		if !pyval.Truthy(state["replay_enabled"]) {
			c.json(M{"ok": false, "error": "Replay disabled in control panel"})
			return
		}
		data, err := strictObject(body)
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()}, 500)
			return
		}
		reason := "Unknown"
		if v, ok := data["reason"]; ok {
			reason = pyval.Str(v)
		}
		// In the background — don't block the overlay
		safeGo("replay", func() { ObsTriggerReplay(state, reason) })
		c.json(M{"ok": true, "reason": reason})
		say("  ▶  Replay queued — reason: %s", reason)

	case path == "/weather/show":
		setCommand("show_weather", true)
		c.json(M{"ok": true})

	case path == "/obs/add_camera":
		d := jsonObject(body)
		ok, msg := AddCameraFromState(trim(str(d, "which", "1")), str(d, "url", ""), str(d, "name", ""), str(d, "scene", ""))
		c.json(M{"ok": ok, "message": msg})

	case path == "/camera/spotter":
		// "Check cameras now": works off-air too, for trying it out before a match.
		if !c.rateLimit(path) {
			return
		}
		st := LoadState()
		hasKey := trim(str(st, "anthropic_api_key", "")) != ""
		SpotCameras(st, hasKey)
		out := spotterStatus()
		out["used_ai"] = hasKey
		c.json(out)

	case path == "/precheck":
		if !c.rateLimit(path) {
			return
		}
		c.json(RunPrecheck())

	case path == "/camera/scene":
		// Hard-cut between camera scenes (bowler-end <-> wide). Called from the panel's
		// manual cut buttons (operator, needs a session) AND automatically by the overlay
		// at the over boundary when graphics_camera_auto_cut is on (loopback carve-out).
		scene := trim(str(jsonObject(body), "scene", ""))
		if scene == "" {
			c.json(M{"ok": false, "error": "No scene given"}, 400)
			return
		}
		ok := cutToScene(LoadState(), scene)
		msg := "Switched to " + scene
		if !ok {
			msg = fmt.Sprintf("Could not switch OBS to '%s' — check it exists and OBS is reachable", scene)
		}
		c.json(M{"ok": ok, "scene": scene, "message": msg})

	case path == "/data/reconcile":
		// Reconcile a match's aggregates against PlayCricket's published scorecard.
		mid := trim(str(jsonObject(body), "match_id", ""))
		if mid == "" {
			mid = CurrentMatchID()
		}
		c.json(ReconcileMatch(mid))

	case path == "/replay/test":
		// The panel's "Test replay now": through the overlay, so it's what a real one looks
		// like — the REPLAY screen with the club's motto, then the cut. POSTing /replay
		// straight from the panel cut to the replay with no intro.
		if !pyval.Truthy(LoadState()["replay_enabled"]) {
			c.json(M{"ok": false, "error": "Replay disabled in control panel"})
			return
		}
		setCommand("test_replay", true)
		c.json(M{"ok": true})

	case path == "/scorecard/show":
		setCommand("show_scorecard", true)
		c.json(M{"ok": true})

	case path == "/break/start":
		// Drinks and the innings break come from the overlay; a rain delay or a break in
		// play from the panel. Either way the overlay is told (it shows the break graphics),
		// and a break already running just takes the new name.
		kind := trim(str(jsonObject(body), "kind", "break"))
		StartBreak(kind)
		setCommand("break", gs(BreakStatus(), "kind", "break"))
		c.json(BreakStatus())

	case path == "/break/end":
		EndBreak()
		setCommand("break_end", true)
		c.json(M{"ok": true, "active": false})

	case path == "/h2h/show":
		setCommand("show_h2h", true)
		c.json(M{"ok": true})

	case path == "/cards/show":
		// Manually (re)show player cards for the current batters — handy for testing the
		// pipeline and for match-day "show them again" moments.
		setCommand("show_player_cards", true)
		c.json(M{"ok": true})

	case path == "/weather/hide":
		setCommand("hide_weather", true)
		c.json(M{"ok": true})

	case path == "/youtube/update":
		c.youtubeUpdate(body)

	case path == "/youtube/chapters":
		ok, msg, text := AddChaptersToYouTube(CurrentMatchID(), false)
		c.json(M{"ok": ok, "message": msg, "chapters": text})

	case path == "/match/page":
		started := StartMatchPageBuild()
		if started {
			c.json(M{"ok": true, "error": nil})
		} else {
			c.json(M{"ok": false, "error": "Already building"}, 409)
		}

	case path == "/social/clips":
		socialClipsMu.Lock()
		busy := pyval.Truthy(socialClipsStatus["running"])
		if !busy {
			for k, v := range (M{"running": true, "ok": nil, "message": "", "done": 0, "total": 0}) {
				socialClipsStatus[k] = v
			}
		}
		socialClipsMu.Unlock()
		if busy {
			c.json(M{"ok": false, "error": "Already making clips"}, 409)
			return
		}
		go func() {
			ok, msg := false, ""
			func() {
				defer func() {
					if p := recover(); p != nil {
						ok, msg = false, fmt.Sprint(p)
					}
				}()
				ok, msg, _ = MakeSocialClips("")
			}()
			socialClipsMu.Lock()
			socialClipsStatus["running"], socialClipsStatus["ok"], socialClipsStatus["message"] = false, ok, msg
			socialClipsMu.Unlock()
			say("  %s  Social clips: %s", tick(ok), msg)
		}()
		c.json(M{"ok": true, "started": true})

	case path == "/highlights":
		var d M
		if len(body) > 0 {
			var err error
			if d, err = strictObject(body); err != nil {
				c.json(M{"ok": false, "error": err.Error()}, 500)
				return
			}
		}
		st := LoadState()
		folder := replayFolder(st)
		output := filepath.Join(folder, "highlights.mp4")
		if v, ok := d["output"]; ok {
			output = pyval.Str(v)
		}
		highlightsMu.Lock()
		highlightsStatus["running"], highlightsStatus["ok"], highlightsStatus["message"] = true, nil, ""
		highlightsMu.Unlock()
		go func() {
			ok, msg := false, ""
			func() {
				defer func() { // belt and braces — the status must resolve
					if p := recover(); p != nil {
						ok, msg = false, fmt.Sprint(p)
					}
				}()
				ok, msg = CompileHighlights(folder, output)
			}()
			highlightsMu.Lock()
			highlightsStatus["running"], highlightsStatus["ok"], highlightsStatus["message"] = false, ok, msg
			highlightsMu.Unlock()
			say("  %s  Highlights: %s", tick(ok), msg)
		}()
		clips := replayClips(folder)
		c.json(M{"ok": true, "output": output, "clip_count": len(clips), "tagged": countTagged(clips)})

	case path == "/commentary/over/generate":
		d, err := strictObject(body)
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()})
			return
		}
		st := LoadState()
		// Same precedence as /live: a live manual session outranks the scorer's file,
		// otherwise the AI narrates a stale (or absent) NV Play match.
		ps := ManualLiveState()
		if ps == nil {
			ps = ReadScoreSource(st)
		}
		if ps == nil {
			ps = M{}
		}
		GenerateOverCommentary(pyval.IntOr(d["over_num"], 0), pyval.IntOr(d["over_runs"], 0),
			str(d, "bowler_name", ""), str(d, "bowler_figs", ""), str(d, "balls", ""), ps)
		c.json(M{"ok": true})

	case path == "/commentary/test":
		if !c.rateLimit(path) {
			return
		}
		st := LoadState()
		demo := M{
			"battingTeamName": gsOr(st, "home_team", "Home CC"),
			"bowlingTeamName": gsOr(st, "away_team", "Opposition CC"),
			"score":           87, "wickets": 3, "overs": 18.2,
			"batter1": M{"name": "A. Richards", "runs": 34, "balls": 52},
			"batter2": M{"name": "T. Blake", "runs": 12, "balls": 21},
			"bowler":  M{"name": "J. Harrison", "wickets": 2, "runs": 28, "overs": "7"},
		}
		// Waits for the answer: returning whatever had arrived after a fixed few seconds
		// showed "Generating..." for good on a slow reply.
		text := GenerateCommentary(demo, []string{"FOUR — 87-3", "Wicket — 75-3"})
		var errMsg any
		if text == "" {
			errMsg = "No commentary came back — check the Anthropic API key (the server window says why)"
		}
		c.json(M{"ok": text != "", "text": text, "error": errMsg})

	case path == "/auth/logout_all":
		// Rotates the signing epoch mixed into every session's HMAC — every session issued
		// before this instant (including the caller's own) stops verifying, without
		// restarting the stream.
		rotateSessionEpoch()
		ip := c.clientIP()
		say("  ⚠  All sessions revoked by [%s]", ip)
		authLogAdd("logout_all", ip)
		c.json(M{"ok": true})

	case path == "/stream/dynamic":
		// One-click enable of OBS's Dynamic Bitrate — the same WebSocket write obs-setup
		// does. OBS reads it at stream START, so if already live it applies next stream.
		st := LoadState()
		r := obsCall(st, []obsws.Req{
			{Type: "SetProfileParameter", Data: M{"parameterCategory": "Output",
				"parameterName": "DynamicBitrate", "parameterValue": "true"}},
			{Type: "GetStreamStatus"}}, 4*time.Second)
		if len(r) == 0 || r[0] == nil {
			c.json(M{"ok": false, "error": "Could not reach OBS — is it running with the WebSocket server enabled?"})
			return
		}
		live := len(r) > 1 && r[1] != nil && pyval.Truthy(r[1]["outputActive"])
		streamMonMu.Lock()
		streamMon["dynamic_bitrate"] = true
		streamMonMu.Unlock()
		msg := "Dynamic bitrate enabled"
		if live {
			msg += " — takes effect when the stream next starts"
		}
		c.json(M{"ok": true, "message": msg})

	case path == "/stream/quality":
		// Manual quality control: step down, step back up, or restore. A
		// stop→reconfigure→start (~5-10s ingest gap; the broadcast survives).
		act, _ := jsonObject(body)["action"].(string)
		streamMonMu.Lock()
		step := gi(streamMon, "step", 0)
		streamMonMu.Unlock()
		var target int
		switch act {
		case "down":
			target = min(step+1, len(StreamLadder)-1)
		case "up":
			target = max(step-1, 0)
		case "restore":
			target = 0
		default:
			c.json(M{"ok": false, "error": "action must be down/up/restore"}, 400)
			return
		}
		if target == step {
			c.json(M{"ok": false, "error": "already at that quality step"})
			return
		}
		ok, msg := ApplyStreamQualityStep(target, "operator request", "manual")
		key := "message"
		if !ok {
			key = "error"
		}
		c.json(M{"ok": ok, key: msg, "state": streamMonitorStatus()})

	case path == "/scoring/setup":
		// Create (or replace) the manual scoring session. Exact match BEFORE the /scoring/
		// prefix below — see the route-ordering gotcha.
		c.scoringSetup(body)

	case strings.HasPrefix(path, "/scoring/"):
		c.scoringAction(path[len("/scoring/"):], body)

	default:
		c.notFound()
	}
}

var errNotObject = errors.New("expected a JSON object")

// loginSleep is swappable so tests needn't wait out the anti-brute-force delay.
var loginSleep = time.Sleep

func statusFor(ok bool) int {
	if ok {
		return 200
	}
	return 400
}

func tick(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// pyStr is str(v) for a console line (None for a missing value).
func pyStr(v any) string {
	if v == nil {
		return "None"
	}
	return pyval.Str(v)
}

func (c *ctx) youtubeUpdate(body []byte) {
	var data M
	if len(bytes.TrimSpace(body)) == 0 {
		data = M{}
	} else {
		var err error
		if data, err = strictObject(body); err != nil {
			c.json(M{"ok": false, "error": err.Error()}, 500)
			return
		}
	}
	s := LoadState()
	home, away := gsOr(s, "home_team", "Home CC"), gsOr(s, "away_team", "Opposition")
	fill := func(t string) string {
		return strings.ReplaceAll(strings.ReplaceAll(t, "{home}", home), "{away}", away)
	}
	title := fill(str(data, "title", gsOr(s, "youtube_title_template", "LIVE: {home} vs {away}")))
	var desc string
	if v, ok := data["description"]; ok && v != nil {
		desc = fill(pyval.Str(v))
	} else {
		desc = fill(gsOr(s, "youtube_description", ""))
	}
	privacy := str(data, "privacy", gsOr(s, "youtube_privacy", "unlisted"))
	category := str(data, "category", pyval.Str(valOr(s, "youtube_category", "17")))
	// First-run browser auth only for a real same-machine operator — a remote
	// (tunnelled) request must not pop a browser on the host.
	ok, msg := UpdateYouTubeBroadcast(&title, &desc, &privacy, &category, c.trustedLoopback())
	say("  %s  YouTube: %s", tick(ok), msg)
	out := M{"ok": ok, "title": title, "message": "", "error": ""}
	if ok {
		out["message"] = msg
	} else {
		out["error"] = msg
	}
	c.json(out)
}

// splitLines is Python's str.splitlines().
func splitLines(s string) []any {
	out := []any{}
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', ' ', ' ':
			out = append(out, string(rs[start:i]))
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

func lines(v any) any {
	if l, ok := v.([]any); ok {
		return l
	}
	if !pyval.Truthy(v) {
		return []any{}
	}
	return splitLines(pyval.Str(v))
}

func (c *ctx) scoringSetup(body []byte) {
	var d M
	if len(bytes.TrimSpace(body)) == 0 {
		d = M{}
	} else {
		var err error
		if d, err = strictObject(body); err != nil {
			msg := err.Error()
			if errors.Is(err, errNotObject) {
				msg = "setup must be a JSON object"
			}
			c.json(M{"ok": false, "error": msg}, 400)
			return
		}
	}
	manualMu.Lock()
	sess := manualSessionLocked()
	if sess != nil && len(sess.Events) > 0 && !sess.MatchOver() && !pyval.Truthy(d["force"]) {
		manualMu.Unlock()
		c.json(M{"ok": false, "error": "a scoring session is already in progress — reset it (or pass force) first"}, 400)
		return
	}
	newSess, err := newManualSession(scoring.Config{
		"home": d["home"], "away": d["away"],
		"home_xi": lines(d["home_xi"]), "away_xi": lines(d["away_xi"]),
		"max_overs": d["max_overs"], "batting_first": d["batting_first"],
	})
	if err != nil {
		manualMu.Unlock()
		c.json(M{"ok": false, "error": err.Error()}, 400)
		return
	}
	manualSession = newSess
	newSess.Save()
	cfg := newSess.Config
	manualMu.Unlock()
	// Manual scoring implies: no demo data, no widget fallback, and the overlay's team
	// names/colour mapping should match what's being scored.
	UpdateState(func(st M) {
		st["demo_mode"], st["use_widget"] = false, false
		st["home_team"], st["away_team"], st["max_overs"] = cfg["home"], cfg["away"], cfg["max_overs"]
	})
	say("  ✓  Manual scoring: %s v %s, %s overs", pyStr(cfg["home"]), pyStr(cfg["away"]), pyStr(cfg["max_overs"]))
	c.json(M{"ok": true, "state": ManualUIState()})
}

// newManualSession builds a session, turning the engine's validation panics (Python's
// ValueError from the constructor) into an error.
func newManualSession(cfg scoring.Config) (s *scoring.Session, err error) {
	defer func() {
		if p := recover(); p != nil {
			s, err = nil, fmt.Errorf("%v", p)
		}
	}()
	return scoring.NewSession(cfg, ManualScoringFile), nil
}

func ballEvent(d M) scoring.Event {
	runs := d["runs"]
	if _, ok := d["runs"]; !ok {
		runs = 0
	}
	return scoring.Event{"event": "ball", "kind": valOr(d, "kind", ""), "runs": runs,
		"wicket_type": valOr(d, "wicket_type", "bowled"), "fielder": valOr(d, "fielder", ""),
		"out_non_striker": pyval.Truthy(d["out_non_striker"])}
}

func (c *ctx) scoringAction(action string, body []byte) {
	d := jsonObject(body)
	manualMu.Lock()
	sess := manualSessionLocked()
	var err error
	switch {
	case action == "reset":
		manualSession, manualLoadAttempt = nil, true // don't resurrect from the file
		os.Remove(ManualScoringFile)
	case sess == nil:
		err = errors.New("no scoring session — set the match up first")
	case action == "ball":
		err = sess.Apply(ballEvent(d))
	case action == "undo":
		_, err = sess.Undo()
	case action == "edit":
		idx := -1
		if v, ok := d["index"]; ok {
			if idx, err = pyval.Int(v); err != nil {
				break
			}
		}
		err = sess.EditBall(idx, ballEvent(d))
	case action == "bowler":
		err = sess.Apply(scoring.Event{"event": "bowler", "name": valOr(d, "name", "")})
	case action == "batter":
		err = sess.Apply(scoring.Event{"event": "batter", "name": valOr(d, "name", "")})
	case action == "strike":
		err = sess.Apply(scoring.Event{"event": "swap_strike"})
	case action == "innings":
		ev := "end_innings"
		if d["action"] == "start_next" {
			ev = "start_innings"
		}
		err = sess.Apply(scoring.Event{"event": ev})
	default:
		manualMu.Unlock()
		c.notFound()
		return
	}
	manualMu.Unlock()
	if err != nil {
		c.json(M{"ok": false, "error": err.Error()}, 400)
		return
	}
	c.json(M{"ok": true, "state": ManualUIState()})
}
