package server

// The camera cut test: the control panel's "Test the camera cut". The automatic cut at
// the over boundary has never been tried with two real cameras (CLAUDE.md), and the way
// it goes wrong is quiet: a camera missing from a scene deactivates when that scene is
// on air, reconnects when it comes back, and drifts behind the graphics for the rest of
// the day. So before a match this checks every camera is in every scene, then cuts to
// the bowler's end and back exactly as a match does (cutToScene), holding as long as the
// auto-cut does, and watches both pictures the whole way: a camera that stops "playing"
// is one that reconnected.

import (
	"fmt"
	"sync"
	"time"

	"cricketstream/internal/obsws"
)

// cutTestHold is how long the test stays on the bowler's end: the overlay's auto-cut
// holds 8s. Tests shorten it.
var cutTestHold = 8 * time.Second

var (
	cutTestMu  sync.Mutex
	cutTestJob = M{"running": false, "ok": nil, "steps": []any{}, "finished": nil}
)

// CutTestStatus is GET /camera/cut_test.
func CutTestStatus() M {
	cutTestMu.Lock()
	defer cutTestMu.Unlock()
	out := shallow(cutTestJob)
	out["steps"] = append([]any{}, gl(cutTestJob, "steps")...)
	return out
}

// StartCutTest is POST /camera/cut_test: refused while one runs, without a second camera,
// or while the stream is live (the cut is on air). The test itself runs in the background.
func StartCutTest() (int, M) {
	st := LoadState()
	if trim(gs(st, "camera2_rtsp_url", "")) == "" {
		return 200, M{"ok": false, "error": "There's no bowler's-end camera to cut to — add one first (Setup → Cameras)."}
	}
	r := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
	if r == nil {
		return 200, M{"ok": false, "error": "Couldn't reach OBS — is it open?"}
	}
	if r[0] == nil || gb(r[0], "outputActive", false) {
		return 200, M{"ok": false, "error": "The stream is live — the test cuts the picture, so run it before going live."}
	}
	cutTestMu.Lock()
	if gb(cutTestJob, "running", false) {
		cutTestMu.Unlock()
		return 409, M{"ok": false, "error": "The camera cut test is already running."}
	}
	cutTestJob = M{"running": true, "ok": nil, "steps": []any{}, "finished": nil}
	cutTestMu.Unlock()
	go runCutTest(st)
	return 202, M{"ok": true, "started": true}
}

func cutTestStep(ok bool, text string) {
	cutTestMu.Lock()
	defer cutTestMu.Unlock()
	cutTestJob["steps"] = append(gl(cutTestJob, "steps"), M{"ok": ok, "text": text})
}

func runCutTest(st M) {
	passed := true
	defer func() {
		if p := recover(); p != nil {
			cutTestStep(false, fmt.Sprintf("The test stopped: %v", p))
			passed = false
		}
		cutTestMu.Lock()
		cutTestJob["running"], cutTestJob["ok"], cutTestJob["finished"] = false, passed, now()
		cutTestMu.Unlock()
	}()
	step := func(ok bool, text string) bool {
		passed = passed && ok
		cutTestStep(ok, text)
		return ok
	}
	main := gsOr(st, "obs_main_scene", "Main")
	bowler := gsOr(st, "obs_bowler_scene", "Main-Bowler")
	replay := gsOr(st, "obs_replay_scene", "Replay")
	cams := [][2]string{{"Wide camera", gsOr(st, "obs_camera_name", "Cricket Camera")},
		{"Bowler's-end camera", gsOr(st, "obs_camera2_name", "Bowler End Camera")}}

	// 1) Every camera in every scene it's shown in or cut to, Replay included.
	var reqs []obsws.Req
	for _, sc := range []string{main, bowler, replay} {
		for _, c := range cams {
			reqs = append(reqs, obsws.Req{Type: "GetSceneItemId", Data: M{"sceneName": sc, "sourceName": c[1]}})
		}
	}
	r := obsCall(st, reqs, 6*time.Second)
	if r == nil {
		step(false, "Couldn't reach OBS.")
		return
	}
	var missing []string
	for i, sc := range []string{main, bowler, replay} {
		for j, c := range cams {
			if r[i*len(cams)+j] == nil {
				missing = append(missing, fmt.Sprintf("%s isn't in the %s scene", c[0], sc))
			}
		}
	}
	if len(missing) > 0 {
		step(false, joinSentences(missing)+". A camera missing from a scene switches off when that scene is on "+
			"air and reconnects afterwards — the picture then runs behind the score. Get ready → Add to OBS adds "+
			"each camera to every scene.")
		return // cutting now would only show the problem on screen
	}
	step(true, fmt.Sprintf("Both cameras are in %s, %s and %s.", main, bowler, replay))

	// 2) Both pictures coming in before the cut.
	if !picturesPlaying(st, cams, step, "before the cut") {
		return
	}
	before := programScene(st)

	// 3) The cut, the hold and the cut back, as the auto-cut does them.
	for _, leg := range []struct{ to, label string }{{bowler, "the bowler's end"}, {main, "the wide camera"}} {
		t0 := time.Now()
		if !cutToScene(st, leg.to) {
			step(false, fmt.Sprintf("OBS wouldn't cut to %s (%s) — check the scene exists.", leg.label, leg.to))
			break
		}
		took := time.Since(t0)
		if got := programScene(st); got != leg.to {
			step(false, fmt.Sprintf("Asked OBS for %s, but it's showing %q.", leg.to, got))
			break
		}
		step(true, fmt.Sprintf("Cut to %s in %d ms.", leg.label, took.Milliseconds()))
		if leg.to == bowler {
			time.Sleep(cutTestHold)
			if !picturesPlaying(st, cams, step, "on the bowler's end") {
				break
			}
		}
	}
	// Always back where it was — the wide camera, or whatever an operator mid-setup had on
	// — even when a step failed on the bowler's end: a test must never leave the picture
	// on the wrong camera.
	restore := before
	if restore == "" {
		restore = main
	}
	if programScene(st) != restore && !cutToScene(st, restore) {
		step(false, fmt.Sprintf("Couldn't put OBS back on %s — switch it back in OBS.", restore))
	}
	if passed && picturesPlaying(st, cams, step, "after the cut back") {
		step(true, "The camera cut works. Tick \"Automatic cut at over boundary\" to have it happen each over.")
	}
}

// picturesPlaying checks both cameras are showing a picture, as a step.
func picturesPlaying(st M, cams [][2]string, step func(bool, string) bool, when string) bool {
	reqs := make([]obsws.Req, len(cams))
	for i, c := range cams {
		reqs[i] = obsws.Req{Type: "GetMediaInputStatus", Data: M{"inputName": c[1]}}
	}
	r := obsCall(st, reqs, 5*time.Second)
	if r == nil {
		return step(false, "Couldn't reach OBS.")
	}
	var bad []string
	for i, c := range cams {
		raw := gs(r[i], "mediaState", "")
		state, ok := mediaState[raw]
		if !ok {
			state = orStr(raw, "not answering")
		}
		if state != "playing" {
			bad = append(bad, fmt.Sprintf("%s: %s", c[0], state))
		}
	}
	if len(bad) > 0 {
		return step(false, fmt.Sprintf("No picture %s (%s). A camera that drops out at a cut is reconnecting — "+
			"check it's in every scene and on the network.", when, joinSentences(bad)))
	}
	return step(true, "Both cameras showing a picture "+when+".")
}

func programScene(st M) string {
	r := obsCall(st, []obsws.Req{{Type: "GetCurrentProgramScene"}}, 4*time.Second)
	if len(r) == 0 || r[0] == nil {
		return ""
	}
	return gsOr(r[0], "currentProgramSceneName", gs(r[0], "sceneName", ""))
}

func joinSentences(parts []string) string {
	out := ""
	for i, p := range parts {
		switch {
		case i == 0:
			out = p
		case i == len(parts)-1:
			out += " and " + p
		default:
			out += ", " + p
		}
	}
	return out
}
