package server

// The control panel's match-day checklist: every item DETECTED here, never ticked by
// hand, each with the button that does that step where one can (start OBS, add the
// camera, start the replay buffer, go live). Token-gated: these start OBS and go live,
// so NEVER the overlay's loopback carve-out.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

var (
	checklistJob   = M{"action": nil, "running": false, "ok": nil, "messages": []any{}, "finished": nil}
	checklistJobMu sync.Mutex
)

var mediaState = map[string]string{"OBS_MEDIA_STATE_PLAYING": "playing", "OBS_MEDIA_STATE_OPENING": "connecting",
	"OBS_MEDIA_STATE_BUFFERING": "buffering", "OBS_MEDIA_STATE_PAUSED": "paused",
	"OBS_MEDIA_STATE_STOPPED": "stopped", "OBS_MEDIA_STATE_ENDED": "ended",
	"OBS_MEDIA_STATE_ERROR": "error", "OBS_MEDIA_STATE_NONE": "no picture"}

// Swappable for tests: the process check shells out to tasklist, and the port check and
// feed status touch the real machine.
var (
	obsRunning        = func() bool { return obs.Running() }
	checklistPortOpen = func(port int) bool { return obs.PortOpen(port, "", time.Second) }
	checklistFeed     = scoreFeedStatus
	checklistStartJob = func() { go checklistRunStartOBS() }
)

// ChecklistStatus is the checklist as the panel shows it. One OBS connection per call,
// and none at all if OBS's port is shut (so a closed OBS answers instantly).
func ChecklistStatus() M {
	st, t := LoadState(), now()
	host := trim(gs(st, "obs_host", "localhost"))
	local := obsIsLocal(st)
	port := gi(st, "obs_port", 4455)
	if port == 0 {
		port = 4455
	}
	camURL := trim(gs(st, "camera_rtsp_url", ""))
	camName := trim(gsOr(st, "obs_camera_name", "Cricket Camera"))
	checklistJobMu.Lock()
	job := shallow(checklistJob)
	job["messages"] = append([]any{}, gl(checklistJob, "messages")...)
	checklistJobMu.Unlock()
	starting := gb(job, "running", false) && gs(job, "action", "") == "start_obs"

	var res []map[string]any
	if !local || checklistPortOpen(port) {
		reqs := []obsws.Req{{Type: "GetReplayBufferStatus"}, {Type: "GetStreamStatus"}}
		if camURL != "" {
			reqs = append(reqs, obsws.Req{Type: "GetMediaInputStatus", Data: M{"inputName": camName}})
		}
		res = obsCall(st, reqs, 4*time.Second)
	}
	obsOK := res != nil
	items := []any{}
	item := func(id, label, state, detail, action, actionLabel string) {
		var a, al any
		if action != "" {
			a, al = action, actionLabel
		}
		items = append(items, M{"id": id, "label": label, "state": state, "detail": detail, "action": a, "action_label": al})
	}
	switch {
	case obsOK:
		item("obs", "OBS", "done", "Open and connected.", "", "")
	case starting:
		msgs := gl(job, "messages")
		last := "Starting OBS…"
		if len(msgs) > 0 {
			last = fmt.Sprint(msgs[len(msgs)-1])
		}
		item("obs", "OBS", "busy", last, "", "")
	case !local:
		item("obs", "OBS", "todo", fmt.Sprintf("Can't reach OBS on %s:%d. It's on another computer, so it has to be started there.", host, port), "", "")
	case obsRunning():
		item("obs", "OBS", "todo", "Open but not answering — it may be asking about Safe Mode. Close it on the laptop, then press Start OBS.", "start_obs", "Start OBS")
	default:
		item("obs", "OBS", "todo", "Not open.", "start_obs", "Start OBS")
	}
	switch {
	case camURL == "":
		item("camera", "Camera", "na", "No camera address set (Setup → Cameras). Fine if you've added your camera in OBS yourself.", "", "")
	case !obsOK:
		item("camera", "Camera", "todo", "Waiting for OBS.", "", "")
	default:
		var media M
		if len(res) > 2 {
			media = res[2]
		}
		if media == nil {
			item("camera", "Camera", "todo", "Not in OBS yet.", "add_camera", "Add to OBS")
		} else {
			raw := gs(media, "mediaState", "")
			state, ok := mediaState[raw]
			if !ok {
				state = orStr(raw, "unknown")
			}
			if state == "playing" {
				item("camera", "Camera", "done", "Picture coming in.", "", "")
			} else {
				item("camera", "Camera", "todo", fmt.Sprintf("In OBS but %s — check the camera is on and on the network.", state), "add_camera", "Reconnect")
			}
		}
	}
	switch {
	case !obsOK:
		item("replay", "Replay buffer", "todo", "Waiting for OBS.", "", "")
	case gb(res[0], "outputActive", false):
		item("replay", "Replay buffer", "done", "Running — replays ready.", "", "")
	default:
		item("replay", "Replay buffer", "todo", "Not running, so no replays.", "start_replay", "Start replay buffer")
	}
	away := trim(gs(st, "away_team", ""))
	switch {
	case away != "" && away != "Opposition CC":
		item("match", "Today's match", "done", "vs "+away+".", "", "")
	case trim(gs(st, "playcricket_api_key", "")) != "":
		item("match", "Today's match", "todo", "Opposition not set yet.", "fetch_match", "Fetch today's match")
	default:
		item("match", "Today's match", "todo", "Opposition not set — type it in Match day → Match.", "", "")
	}
	fresh, detail := false, ""
	func() {
		defer func() {
			if r := recover(); r != nil {
				fresh, detail = false, fmt.Sprintf("Couldn't check the score feed: %v", r)
			}
		}()
		fresh, detail = checklistFeed(st, t)
	}()
	switch {
	case fresh:
		item("scorer", "Scorer feed", "done", detail, "", "")
	case gs(st, "pcs_source", "") == "agent":
		item("scorer", "Scorer feed", "todo", detail, "find_agent", "Find scorer laptop")
	default:
		item("scorer", "Scorer feed", "todo", detail, "", "")
	}
	switch {
	case obsOK && gb(res[1], "outputActive", false):
		item("live", "Live on YouTube", "done", "Streaming.", "", "")
	case obsOK:
		item("live", "Live on YouTube", "todo", "Not live yet.", "go_live", "Go live")
	default:
		item("live", "Live on YouTube", "todo", "Waiting for OBS.", "", "")
	}
	return M{"items": items, "job": job}
}

// Swappable for tests: the OBS preparation and setup the checklist's Start OBS runs.
var (
	prepareOBSForChecklist = obs.Prepare
	setupOBSForChecklist   = obs.SetupFromConfig
)

// checklistRunStartOBS is the background job: get OBS ready exactly the way the launcher
// does — prepare (WebSocket on, replay buffer, video, encoder, open OBS) then setup
// (scenes, overlay, stream key) — reporting each step into the job for the panel.
func checklistRunStartOBS() {
	var msgs []any
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" {
			return
		}
		msgs = append(msgs, strings.ReplaceAll(strings.ReplaceAll(m, "[OK] ", ""), "[!!] ", ""))
		checklistJobMu.Lock()
		if len(msgs) > 12 {
			checklistJob["messages"] = append([]any{}, msgs[len(msgs)-12:]...)
		} else {
			checklistJob["messages"] = append([]any{}, msgs...)
		}
		checklistJobMu.Unlock()
	}
	ok := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				add(fmt.Sprintf("Couldn't start OBS: %v", r))
			}
		}()
		managed := obs.ManageOBSEnabled(ConfigFile)
		if managed {
			prepareOBSForChecklist(ConfigFile, nil, add)
		}
		// Prepare may have just written a NEW WebSocket password into config.ini; the
		// server's copy in state has to follow, or every later OBS call fails auth. Only
		// when we manage OBS and only a non-blank one: otherwise the password typed into
		// the panel is the working one, and copying "" over it broke every OBS call.
		if pw := obs.ReadConfigValue(ConfigFile, "OBS", "obs_password"); managed && pw != "" {
			UpdateState(func(s M) { s["obs_password"] = pw })
		}
		add("Setting up scenes and the overlay…")
		var log []string
		ok, log = setupOBSForChecklist(ConfigFile, StateFile, false, true)
		r := strings.NewReplacer("✓", "", "⚠", "", "✗", "")
		for _, l := range log {
			add(r.Replace(l))
		}
		if ok {
			add("OBS is ready.")
		} else {
			add("OBS setup didn't finish — see above.")
		}
	}()
	checklistJobMu.Lock()
	checklistJob["running"], checklistJob["ok"], checklistJob["finished"] = false, ok, now()
	checklistJobMu.Unlock()
}

// ChecklistAction does one checklist step: (http status, body).
func ChecklistAction(action string) (int, M) {
	st := LoadState()
	switch action {
	case "start_obs":
		checklistJobMu.Lock()
		if gb(checklistJob, "running", false) {
			checklistJobMu.Unlock()
			return 409, M{"ok": false, "error": "Already starting OBS."}
		}
		checklistJob = M{"action": action, "running": true, "ok": nil, "messages": []any{"Starting OBS…"}, "finished": nil}
		checklistJobMu.Unlock()
		checklistStartJob()
		return 202, M{"ok": true, "started": true}
	case "start_replay":
		r := obsCall(st, []obsws.Req{{Type: "StartReplayBuffer"}}, 8*time.Second)
		if r == nil {
			return 200, M{"ok": false, "error": "Couldn't reach OBS."}
		}
		if r[0] == nil { // OBS answered, and said no
			return 200, M{"ok": false, "error": "OBS wouldn't start the replay buffer. On a brand-new OBS it needs one restart first: press Start OBS."}
		}
		return 200, M{"ok": true}
	case "add_camera":
		ok, msg := AddCameraFromState("1", "", "", "")
		return 200, M{"ok": ok, "message": msg}
	case "go_live":
		r := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
		if r == nil {
			return 200, M{"ok": false, "error": "Couldn't reach OBS."}
		}
		if gb(r[0], "outputActive", false) {
			return 200, M{"ok": true, "message": "Already live."}
		}
		if r = obsCall(st, []obsws.Req{{Type: "StartStream"}}, 8*time.Second); r == nil || r[0] == nil {
			return 200, M{"ok": false, "error": "OBS wouldn't start the stream — check the stream key (OBS → Settings → Stream)."}
		}
		return 200, M{"ok": true, "message": "Going live…"}
	case "stop_stream":
		// The clubhouse screen's "End the stream". Only the stream: the replay buffer and
		// recording stay as they are (closing OBS at the end of match day deals with them).
		r := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
		if r == nil {
			return 200, M{"ok": false, "error": "Couldn't reach OBS."}
		}
		if r[0] == nil { // refused: "not live" would be a guess, and the screen ends match day on it
			return 200, M{"ok": false, "error": "OBS didn't say whether it's streaming — stop the stream in OBS on the laptop."}
		}
		if !gb(r[0], "outputActive", false) {
			return 200, M{"ok": true, "message": "Not live."}
		}
		if r = obsCall(st, []obsws.Req{{Type: "StopStream"}}, 8*time.Second); r == nil || r[0] == nil {
			return 200, M{"ok": false, "error": "OBS wouldn't stop the stream — stop it in OBS on the laptop."}
		}
		say("  ■  Stream stopped (clubhouse screen)")
		return 200, M{"ok": true, "message": "Stream stopped."}
	}
	return 400, M{"ok": false, "error": "Unknown action: " + action}
}
