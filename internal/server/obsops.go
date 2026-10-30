package server

// OBS operations over its WebSocket: the instant replay, adding cameras, and the
// replay-clip tagging that feeds the highlights reel.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
	"cricketstream/internal/proc"
	"cricketstream/internal/pyval"
)

// obsTarget is where OBS is, from the state.
func obsTarget(state M) obsws.Target {
	return obsws.Target{Host: gs(state, "obs_host", "localhost"), Port: gi(state, "obs_port", 4455),
		Password: gs(state, "obs_password", "")}
}

// obsCall connects, runs the requests in order, disconnects (connection-per-call: callers
// are low-frequency, and never holding a socket means a mid-match OBS restart can't wedge
// anything). nil if OBS couldn't be reached; a nil entry per REFUSED request — check each
// result, not just the slice, or a refusal reads as success. Swappable for tests.
var obsCall = func(state M, reqs []obsws.Req, timeout time.Duration) []map[string]any {
	return obsws.Call(obsTarget(state), reqs, timeout)
}

// obsFast keeps one socket open for camera cuts only (measured 4x quicker).
var obsFast = &obsws.Fast{}

// cutToScene is a camera cut: /camera/scene (the panel's buttons, the overlay's auto-cut)
// and the cut test both go through it, so the test cuts exactly as a match does. Fast path
// first: a held-open socket makes repeat cuts ~4x quicker, the difference between
// flicking between angles and waiting for each one. Falls back to the connection-per-call
// path if the socket can't be established at all.
func cutToScene(st M, scene string) bool {
	data := M{"sceneName": scene}
	ok, reached := obsFastCall(st, "SetCurrentProgramScene", data, 3*time.Second)
	if !reached {
		r := obsCall(st, []obsws.Req{{Type: "SetCurrentProgramScene", Data: data}}, 6*time.Second)
		ok = len(r) > 0 && r[0] != nil
	}
	return ok
}

// obsFastCall runs one request over the held socket: (ok, reached). Swappable.
var obsFastCall = func(state M, requestType string, data any, timeout time.Duration) (bool, bool) {
	return obsFast.Call(obsTarget(state), requestType, data, timeout)
}

// ObsTriggerReplay is the instant replay: save the replay buffer, find the new clip, tag
// it, cut to the Replay scene, hold, cut back. Runs on its own goroutine.
// replayFeed is the frame a replay is captioned from. Same source precedence as /live:
// a live manual session outranks the scorer's file. Agent mode never sets the local
// file's frame, so every clip used to be captioned "SIX" with no batter or score.
func replayFeed(state M) M {
	if m := ManualLiveState(); m != nil {
		return m
	}
	if gs(state, "pcs_source", "local") == "agent" {
		agentMu.Lock()
		defer agentMu.Unlock()
		return agentLastState
	}
	pcsMu.Lock()
	defer pcsMu.Unlock()
	return pcsLastState
}

func ObsTriggerReplay(state M, reason string) {
	// The caption is built from the match state NOW, at trigger time — by the time OBS
	// has saved the clip a few seconds from here, the strike may have rotated.
	caption := makeClipCaption(reason, replayFeed(state))
	main := gs(state, "obs_main_scene", "Main")
	replay := gs(state, "obs_replay_scene", "Replay")
	duration := gi(state, "replay_duration", 18)
	folder := gs(state, "replay_folder", "")
	if folder == "" {
		folder = defaultReplayFolder()
	}
	noteReplay(float64(duration) + 15) // the clip, and up to ten seconds' saving: no quality step in it
	c, err := obsws.Dial(obsTarget(state), 8*time.Second)
	if err != nil {
		say("  ✗  OBS WebSocket: %v — is OBS running? (check the password in the control panel)", err)
		return
	}
	defer c.Close()
	say("  ✓  OBS WebSocket authenticated")
	send := func(t string, d any) bool {
		r, err := c.Request(t, d)
		if err != nil {
			say("  ✗  No response from OBS for %s", t)
		} else if !r.OK {
			say("  ✗  OBS request %s failed: %s", t, r.Comment)
		}
		return err == nil && r.OK
	}
	asked := now()
	if !send("SaveReplayBuffer", nil) {
		// Usually the buffer isn't running (OBS reopened without it, or it was stopped).
		// Start it, so the next replay works — this one is gone either way. Cutting to the
		// Replay scene now would show the PREVIOUS clip, or nothing, on air. Not during a
		// quality step, which stops it on purpose and starts it again itself.
		if shifting.Load() {
			say("  ✗  Replay skipped — a stream quality change is under way")
			return
		}
		if send("StartReplayBuffer", nil) {
			say("  ✓  Replay buffer wasn't running — started it for the next replay")
			watchdogLogFix("OBS's replay buffer wasn't running — started it (that replay was skipped)")
		}
		return
	}
	// OBS writes the clip after answering; a slow disk can take longer than the 3s that
	// used to be allowed. Only a clip written since the request will do.
	clip := ""
	replaySleep(3 * time.Second) // for OBS to write the file
	for waited := 0; ; waited++ {
		if c := newestClip(folder); c != "" && mtime(c) >= asked-1 {
			clip = c
			break
		}
		if waited == 7 {
			break
		}
		replaySleep(time.Second)
	}
	if clip == "" {
		say("  ✗  No new replay clip in: %s — replay skipped", folder)
		say("     Make sure OBS Replay Buffer is enabled and saving to: %s", folder)
		return
	}
	enforceClipLimit(folder, gi(LoadState(), "max_clips", MaxClips))
	say("  ✓  Clip found: %s", filepath.Base(clip))
	// Tag the clip with why it was captured — feeds the highlights compiler.
	logReplayClip(clip, reason, caption)
	if caption != "" {
		say("  🏷  Tagged: %s", caption)
	}
	send("SetInputSettings", M{"inputName": "ReplayClip", "inputSettings": M{"local_file": clip}})
	send("SetCurrentProgramScene", M{"sceneName": replay})
	say("  ▶  Switched to %s scene", replay)
	replaySleep(time.Duration(duration) * time.Second)
	send("SetCurrentProgramScene", M{"sceneName": main})
	say("  ◀  Returned to %s scene", main)
}

var replaySleep = time.Sleep // swappable for tests

// graphicsSources must always sit above any camera.
var graphicsSources = []string{"Overlay", "ReplayClip"}

func reqData(r *obsws.Response, err error) M {
	if err != nil || r == nil || r.Data == nil {
		return M{}
	}
	return r.Data
}

func reqOK(r *obsws.Response, err error) bool { return err == nil && r != nil && r.OK }

// arrangeCameraItem fits a camera's scene item to the canvas and puts it in the right
// place in the stack. In its HOME scene (Main, or the bowler-end scene) it goes directly
// under the graphics, so it's the camera you see — and that scene gets the Overlay if it
// lacks it (the bowler-end scene was once created with cameras only, so the auto-cut hid
// the scorebar). Elsewhere (Replay, the other camera's scene) it's only there to stay
// active, so it goes to the bottom.
func arrangeCameraItem(c *obsws.Conn, scene, name string, canvasW, canvasH int, home, fresh, overlayExists bool) {
	items := func() map[string]M {
		out := map[string]M{}
		for _, it := range toMs(gl(reqData(c.Request("GetSceneItemList", M{"sceneName": scene})), "sceneItems")) {
			out[gs(it, "sourceName", "")] = it
		}
		return out
	}
	if home && overlayExists {
		if _, ok := items()["Overlay"]; !ok {
			c.Request("CreateSceneItem", M{"sceneName": scene, "sourceName": "Overlay", "sceneItemEnabled": true})
		}
	}
	cur := items()
	cam, ok := cur[name]
	if !ok {
		return
	}
	id := cam["sceneItemId"]
	tr := gm(reqData(c.Request("GetSceneItemTransform", M{"sceneName": scene, "sceneItemId": id})), "sceneItemTransform")
	if fresh || obs.UntouchedPlacement(tr) {
		c.Request("SetSceneItemTransform", M{"sceneName": scene, "sceneItemId": id,
			"sceneItemTransform": obs.FillCanvasTransform(canvasW, canvasH)})
	}
	camIdx := gi(cam, "sceneItemIndex", 0)
	lowest, covers := -1, false
	for _, g := range graphicsSources {
		if it, ok := cur[g]; ok {
			idx := gi(it, "sceneItemIndex", 0)
			if idx < camIdx {
				covers = true
			}
			if lowest < 0 || idx < lowest {
				lowest = idx
			}
		}
	}
	if !fresh && !covers {
		return
	}
	if home {
		// Directly beneath the lowest graphic: above any other camera in the scene, and
		// without touching anything above the graphics. (Lifting the graphics to the top
		// instead once buried a real club's sponsor logos under the scorebar.)
		if lowest >= 0 && camIdx > lowest {
			c.Request("SetSceneItemIndex", M{"sceneName": scene, "sceneItemId": id, "sceneItemIndex": lowest})
		}
	} else {
		c.Request("SetSceneItemIndex", M{"sceneName": scene, "sceneItemId": id, "sceneItemIndex": 0})
	}
}

// ObsAddCamera creates (or updates) an RTSP media source in OBS — one-click camera setup
// — and makes sure it's present in every scene that needs it: main + replay, plus
// extraScenes (the other camera's scene). A source left out of even one scene that goes
// live deactivates outright, so switching back re-activates the RTSP feed from scratch
// and it drifts out of sync with the overlay for the rest of the session.
func ObsAddCamera(rtspURL, inputName, sceneName string, state M, extraScenes []string) (bool, string) {
	if state == nil {
		state = LoadState()
	}
	rtspURL = trim(rtspURL)
	if rtspURL == "" {
		return false, "No camera URL given"
	}
	name := trim(orStr(inputName, gsOr(state, "obs_camera_name", "Cricket Camera")))
	scene := trim(orStr(sceneName, gsOr(state, "obs_main_scene", "Main")))
	replayScene := trim(gsOr(state, "obs_replay_scene", "Replay"))
	c, err := obsws.Dial(obsTarget(state), 10*time.Second)
	if err != nil {
		if strings.Contains(err.Error(), "identify") {
			return false, "OBS authentication failed — check the WebSocket password"
		}
		return false, "No response from OBS — is it running with the WebSocket server enabled?"
	}
	defer c.Close()
	// A valid scene: the requested one if it exists or can be created (a second camera's
	// scene often doesn't exist yet), else the current program scene.
	scenesResp := reqData(c.Request("GetSceneList", nil))
	var names []string
	for _, s := range toMs(gl(scenesResp, "scenes")) {
		names = append(names, gs(s, "sceneName", ""))
	}
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	if !has(scene) {
		if reqOK(c.Request("CreateScene", M{"sceneName": scene})) {
			names = append(names, scene)
		} else if cur := gs(scenesResp, "currentProgramSceneName", ""); cur != "" {
			scene = cur
		} else if len(names) > 0 {
			scene = names[0]
		}
	}
	settings := M{"is_local_file": false, "input": rtspURL,
		"reconnect_delay_sec": 2, // auto-reconnect if the stream drops
		"restart_on_activate": true,
		"hw_decode":           true, // GPU decode where available
	}
	// Idempotent WITHOUT destroying and recreating: RemoveInput on a source still used by
	// another scene only detaches one reference and leaves the input broken (refusing new
	// scene items), so an existing camera's settings are updated in place.
	inputs := reqData(c.Request("GetInputList", nil))
	exists, overlayExists := false, false
	for _, i := range toMs(gl(inputs, "inputs")) {
		exists = exists || gs(i, "inputName", "") == name
		overlayExists = overlayExists || gs(i, "inputName", "") == "Overlay"
	}
	var r *obsws.Response
	if exists {
		r, err = c.Request("SetInputSettings", M{"inputName": name, "inputSettings": settings, "overlay": true})
	} else {
		r, err = c.Request("CreateInput", M{"sceneName": scene, "inputName": name, "inputKind": "ffmpeg_source",
			"inputSettings": settings, "sceneItemEnabled": true})
	}
	if !reqOK(r, err) {
		msg := "unknown error"
		if r != nil && r.Comment != "" {
			msg = r.Comment
		}
		return false, "OBS rejected the request: " + msg
	}
	var addedTo []string
	fresh := map[string]bool{}
	if !exists {
		fresh[scene] = true // CreateInput placed it in scene
	}
	added := func(t string) bool {
		for _, a := range addedTo {
			if a == t {
				return true
			}
		}
		return false
	}
	for _, target := range append([]string{scene, replayScene}, extraScenes...) {
		if target == "" || added(target) {
			continue
		}
		if !has(target) {
			if !reqOK(c.Request("CreateScene", M{"sceneName": target})) {
				continue // couldn't create it and it doesn't exist — not fatal
			}
			names = append(names, target)
		}
		present := false
		for _, it := range toMs(gl(reqData(c.Request("GetSceneItemList", M{"sceneName": target})), "sceneItems")) {
			present = present || gs(it, "sourceName", "") == name
		}
		if present {
			addedTo = append(addedTo, target)
			continue
		}
		if reqOK(c.Request("CreateSceneItem", M{"sceneName": target, "sourceName": name, "sceneItemEnabled": true})) {
			addedTo = append(addedTo, target)
			fresh[target] = true
		}
	}
	// Size and stacking: OBS adds a new source at native size, top-left, on TOP of
	// everything — verified to hide every graphic in Main and to show the live camera in
	// Replay. Placement an operator has adjusted by hand is left alone.
	vs := reqData(c.Request("GetVideoSettings", nil))
	w, h := gi(vs, "baseWidth", 0), gi(vs, "baseHeight", 0)
	if w == 0 {
		w = 1920
	}
	if h == 0 {
		h = 1080
	}
	for _, t := range addedTo {
		arrangeCameraItem(c, t, name, w, h, t == scene, fresh[t], overlayExists)
	}
	if len(addedTo) == 0 {
		return false, fmt.Sprintf("Updated '%s' but could not place it in any scene", name)
	}
	return true, fmt.Sprintf("'%s' is set up in %s", name, strings.Join(addedTo, ", "))
}

// AddCameraFromState adds/updates camera 1 or 2 from the saved settings (or posted
// overrides). Shared by Setup → Cameras and the checklist's "Add to OBS".
func AddCameraFromState(which, postedURL, name, scene string) (bool, string) {
	cfg := LoadState()
	urlKey, nameKey, sceneKey, defScene, defName := "camera_rtsp_url", "obs_camera_name", "obs_main_scene", "Main", "Cricket Camera"
	if which == "2" {
		urlKey, nameKey, sceneKey, defScene, defName = "camera2_rtsp_url", "obs_camera2_name", "obs_bowler_scene", "Main-Bowler", "Bowler End Camera"
	}
	// A field still showing the redacted sentinel (the operator loaded the panel without
	// retyping the URL) means the stored value — never "••••••••" as the camera source.
	postedURL = trim(postedURL)
	u := trim(orStr(postedURL, gsOr(cfg, urlKey, "")))
	if postedURL == SecretSentinel {
		u = trim(gsOr(cfg, urlKey, ""))
	}
	name = orStr(name, gsOr(cfg, nameKey, defName))
	scene = orStr(scene, gsOr(cfg, sceneKey, defScene))
	// The OTHER camera's scene too, if that camera is configured, so both cameras sit in
	// both cameras' scenes (plus Replay).
	otherScene, otherURL := gsOr(cfg, "obs_bowler_scene", "Main-Bowler"), gs(cfg, "camera2_rtsp_url", "")
	if which == "2" {
		otherScene, otherURL = gsOr(cfg, "obs_main_scene", "Main"), gs(cfg, "camera_rtsp_url", "")
	}
	var extra []string
	if trim(otherURL) != "" {
		extra = []string{otherScene}
	}
	return ObsAddCamera(u, name, scene, cfg, extra)
}

// defaultReplayFolder is the best-guess replay folder.
func defaultReplayFolder() string {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	for _, c := range []string{filepath.Join(home, "Videos", "Replays"), filepath.Join(home, "Videos")} {
		if isDir(c) {
			return c
		}
	}
	return filepath.Join(home, "Videos", "Replays")
}

var clipPatterns = []string{"*.mkv", "*.mp4", "*.flv", "*.mov"}

func listClips(folder string) []string {
	if folder == "" || !isDir(folder) {
		return nil
	}
	var clips []string
	for _, p := range clipPatterns {
		m, _ := filepath.Glob(filepath.Join(folder, p))
		clips = append(clips, m...)
	}
	return clips
}

func newestClip(folder string) string {
	best, bestT := "", -1.0
	for _, c := range listClips(folder) {
		if t := mtime(c); t > bestT {
			best, bestT = c, t
		}
	}
	return best
}

// enforceClipLimit deletes the oldest clips beyond limit (a rolling buffer).
func enforceClipLimit(folder string, limit int) {
	clips := listClips(folder)
	if len(clips) <= limit {
		return
	}
	sort.SliceStable(clips, func(i, j int) bool { return mtime(clips[i]) < mtime(clips[j]) })
	for _, f := range clips[:len(clips)-limit] {
		if err := os.Remove(f); err != nil {
			say("  ✗  Could not delete %s: %v", f, err)
		} else {
			say("  🗑  Deleted old clip: %s", filepath.Base(f))
		}
	}
}

// ── Replay clip tagging (auto-tagged highlights) ──
// Every replay the overlay triggers already knows WHY it fired. Recording that reason
// against the saved clip — plus the live match context — is what lets the highlights
// compiler caption each clip and write a chaptered description, with no operator effort.

// makeClipCaption is one broadcast-style caption line for a clip, or "" for reasons that
// shouldn't be tagged (tests).
func makeClipCaption(reason string, st M) string {
	r := trim(reason)
	if r == "" || strings.ToLower(r) == "test" {
		return ""
	}
	if st == nil {
		st = M{}
	}
	scoreBit := ""
	if pyval.Truthy(st["battingTeamName"]) {
		scoreBit = fmt.Sprintf("%s-%s (%s ov)", anyStr(st, "score", "0"), anyStr(st, "wickets", "0"), oversStr(valOr(st, "overs", 0)))
	}
	striker := func() string {
		b1, b2 := gm(st, "batter1"), gm(st, "batter2")
		b := b2
		if gb(b1, "onStrike", false) {
			b = b1
		}
		if n := gs(b, "name", ""); n == "" || n == "—" {
			return ""
		}
		return fmt.Sprintf("%s %s*", gs(b, "name", ""), anyStr(b, "runs", "0"))
	}
	after := func() string {
		if _, rest, ok := strings.Cut(r, "-"); ok {
			return strings.TrimSpace(rest)
		}
		return ""
	}
	low := strings.ToLower(r)
	var parts []string
	switch {
	case low == "wicket":
		parts = []string{"WICKET", gs(st, "lastWicketBatter", ""), gs(st, "lastWicketHowOut", "")}
	case low == "boundary":
		parts = []string{"FOUR", striker()}
	case low == "six":
		parts = []string{"SIX", striker()}
	case strings.HasPrefix(low, "century"):
		parts = []string{"CENTURY", after()}
	case strings.HasPrefix(low, "fifty"):
		parts = []string{"FIFTY", after()}
	default:
		parts = []string{strings.ToUpper(r)}
	}
	parts = append(parts, scoreBit)
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "  ·  ")
}

// logReplayClip tags a saved clip in the DB. Never raises — same rule as the ball logger.
func logReplayClip(clip, reason, caption string) {
	defer func() { recover() }()
	if clip == "" || caption == "" {
		return
	}
	withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT OR REPLACE INTO clips(match_id,file,ts,reason,caption) VALUES(?,?,?,?,?)",
			CurrentMatchID(), filepath.Base(clip), nowISO(), reason, caption)
		return err
	})
}

// ClipTag is why a clip was saved.
type ClipTag struct{ Reason, Caption string }

// clipTags maps clip basename → its tag, for whichever files exist. Swappable for tests.
var clipTags = func(files []string) map[string]ClipTag {
	out := map[string]ClipTag{}
	want := map[string]bool{}
	for _, f := range files {
		want[filepath.Base(f)] = true
	}
	withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT file, reason, caption FROM clips")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f, r, c sql.NullString
			rows.Scan(&f, &r, &c)
			if want[f.String] {
				out[f.String] = ClipTag{r.String, c.String}
			}
		}
		return nil
	})
	return out
}

// guessClipTags is the fallback for clips saved by hand (OBS hotkey) with no trigger
// reason on record: correlate the file's mtime with notable balls (W/4/6) in the ball log
// and caption from the closest one inside the window. basename → caption. Swappable.
var guessClipTags = func(files []string, window float64) map[string]string {
	out := map[string]string{}
	type ev struct {
		t                float64
		outcome          string
		wicket           int
		batter           string
		runs, wkts, over int
	}
	var events []ev
	withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT ts, outcome, is_wicket, batter, bowler, cum_runs, cum_wkts, over FROM balls WHERE is_wicket=1 OR outcome IN ('4','6')")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ts, outcome, batter, bowler sql.NullString
			var isW, runs, wkts, over sql.NullInt64
			rows.Scan(&ts, &outcome, &isW, &batter, &bowler, &runs, &wkts, &over)
			t, err := time.ParseInLocation("2006-01-02T15:04:05", ts.String, time.Local)
			if err != nil {
				continue
			}
			events = append(events, ev{float64(t.Unix()), outcome.String, int(isW.Int64), batter.String, int(runs.Int64), int(wkts.Int64), int(over.Int64)})
		}
		return nil
	})
	if len(events) == 0 {
		return out
	}
	abs := func(x float64) float64 {
		if x < 0 {
			return -x
		}
		return x
	}
	for _, f := range files {
		mt := mtime(f)
		if mt == 0 {
			continue
		}
		var near []ev
		for _, e := range events {
			if abs(e.t-mt) <= window {
				near = append(near, e)
			}
		}
		if len(near) == 0 {
			continue
		}
		// Prefer wickets, then sixes, then fours; closest in time within each class.
		rank := func(e ev) int {
			r := e.wicket * 2
			if e.outcome == "6" {
				r++
			}
			return r
		}
		sort.SliceStable(near, func(i, j int) bool {
			if rank(near[i]) != rank(near[j]) {
				return rank(near[i]) > rank(near[j])
			}
			return abs(near[i].t-mt) < abs(near[j].t-mt)
		})
		e := near[0]
		label := "FOUR"
		if e.wicket != 0 {
			label = "WICKET"
		} else if e.outcome == "6" {
			label = "SIX"
		}
		var parts []string
		for _, p := range []string{label, trim(e.batter), fmt.Sprintf("%d-%d (over %d)", e.runs, e.wkts, e.over+1)} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		out[filepath.Base(f)] = strings.Join(parts, "  ·  ")
	}
	return out
}

// PlanEntry is one clip in the highlights reel.
type PlanEntry struct {
	File, Caption string
}

// planHighlights is the pure planning step (testable without ffmpeg): chronological clip
// order with each clip's caption, replay-trigger test clips excluded.
func planHighlights(files []string, tags map[string]ClipTag) []PlanEntry {
	sorted := append([]string(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return mtime(sorted[i]) < mtime(sorted[j]) })
	var plan []PlanEntry
	for _, f := range sorted {
		tag := tags[filepath.Base(f)]
		if strings.ToLower(trim(tag.Reason)) == "test" {
			continue
		}
		plan = append(plan, PlanEntry{f, tag.Caption})
	}
	return plan
}

// chaptersText is a YouTube-ready description with chapter timestamps.
func chaptersText(captions []string, durations []float64, title string) string {
	if title == "" {
		title = "Match highlights"
	}
	lines := []string{title, ""}
	t := 0.0
	for i, c := range captions {
		mm, ss := int(t)/60, int(t)%60
		lines = append(lines, fmt.Sprintf("%02d:%02d %s", mm, ss, orStr(c, "Replay")))
		if i < len(durations) && durations[i] > 0 {
			t += durations[i]
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// fontPath is a real .ttf for ffmpeg's drawtext: the app's own bundled font.
func fontPath() string {
	p, err := assets.Path("fonts/BarlowCondensed-Bold.ttf")
	if err != nil {
		return ""
	}
	return p
}

// clipDuration is a clip's length in seconds via ffprobe, or 0.
func clipDuration(path string) float64 {
	r, err := proc.Run("ffprobe", []string{"-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path},
		proc.Options{Timeout: 20 * time.Second})
	if err != nil || r.Code != 0 {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(r.Stdout), 64)
	if err != nil {
		return 0
	}
	return f
}
