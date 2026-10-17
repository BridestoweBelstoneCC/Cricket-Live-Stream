package server

// The control panel's match-day checklist (tests/test_checklist.py). Every item is
// detected, never ticked by hand, and carries the button for its step. OBS, the process
// check and the score feed are faked.

import (
	"os"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/obsws"
)

var playing = M{"mediaState": "OBS_MEDIA_STATE_PLAYING"}

type checklistFake struct {
	portOpen, running bool
	obs               map[string]M // nil = OBS unreachable; a missing entry = refused
	sent              [][]string
	feedOK            bool
	feedDetail        string
}

func newChecklistFake(t *testing.T) *checklistFake {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"obs_host": "localhost", "obs_port": 4455, "away_team": "Opposition CC",
		"camera_rtsp_url": "", "playcricket_api_key": ""})
	f := &checklistFake{feedDetail: "No scoreboard file yet."}
	savedCall, savedPort, savedRun, savedFeed, savedJob := obsCall, checklistPortOpen, obsRunning, checklistFeed, checklistStartJob
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		var names []string
		for _, r := range reqs {
			names = append(names, r.Type)
		}
		f.sent = append(f.sent, names)
		if f.obs == nil {
			return nil
		}
		out := make([]map[string]any, len(reqs))
		for i, r := range reqs {
			if v, ok := f.obs[r.Type]; ok {
				out[i] = v
			}
		}
		return out
	}
	checklistPortOpen = func(int) bool { return f.portOpen }
	obsRunning = func() bool { return f.running }
	checklistFeed = func(M, float64) (bool, string) { return f.feedOK, f.feedDetail }
	checklistJobMu.Lock()
	savedState := checklistJob
	checklistJob = M{"action": nil, "running": false, "ok": nil, "messages": []any{}, "finished": nil}
	checklistJobMu.Unlock()
	t.Cleanup(func() {
		obsCall, checklistPortOpen, obsRunning, checklistFeed, checklistStartJob = savedCall, savedPort, savedRun, savedFeed, savedJob
		checklistJobMu.Lock()
		checklistJob = savedState
		checklistJobMu.Unlock()
	})
	return f
}

func (f *checklistFake) up(replay, live bool, media M) {
	f.portOpen = true
	f.obs = map[string]M{"GetReplayBufferStatus": {"outputActive": replay},
		"GetStreamStatus": {"outputActive": live}}
	if media != nil {
		f.obs["GetMediaInputStatus"] = media
	}
}

func (f *checklistFake) sentAny(names ...string) bool {
	want := strings.Join(names, ",")
	for _, s := range f.sent {
		if strings.Join(s, ",") == want {
			return true
		}
	}
	return false
}

func checklistItems() map[string]M {
	out := map[string]M{}
	for _, i := range gl(ChecklistStatus(), "items") {
		m := i.(M)
		out[gs(m, "id", "")] = m
	}
	return out
}

func TestChecklistOBSClosedOffersStart(t *testing.T) {
	newChecklistFake(t)
	o := checklistItems()["obs"]
	eqv(t, []any{o["state"], o["action"]}, []any{"todo", "start_obs"})
}

func TestChecklistClosedOBSIsntConnectedTo(t *testing.T) {
	// Port shut: no WebSocket attempt, so the panel isn't left waiting on a timeout.
	f := newChecklistFake(t)
	checklistItems()
	mustEq(t, len(f.sent), 0)
}

func TestChecklistOBSOpenButNotAnswering(t *testing.T) {
	f := newChecklistFake(t)
	f.running = true
	o := checklistItems()["obs"]
	if !strings.Contains(gs(o, "detail", ""), "Safe Mode") {
		t.Fatal(o)
	}
	eqv(t, o["action"], "start_obs")
}

func TestChecklistOBSOnAnotherComputerNoStartButton(t *testing.T) {
	newChecklistFake(t)
	setState(t, M{"obs_host": "192.168.1.50"})
	o := checklistItems()["obs"]
	eqv(t, o["state"], "todo")
	if o["action"] != nil {
		t.Fatal(o["action"])
	}
}

func TestChecklistOBSStartingShowsProgress(t *testing.T) {
	newChecklistFake(t)
	checklistJobMu.Lock()
	checklistJob = M{"action": "start_obs", "running": true, "messages": []any{"Switched on OBS's WebSocket server."}}
	checklistJobMu.Unlock()
	o := checklistItems()["obs"]
	eqv(t, o["state"], "busy")
	if !strings.Contains(gs(o, "detail", ""), "WebSocket") {
		t.Fatal(o)
	}
}

func TestChecklistEverythingReady(t *testing.T) {
	f := newChecklistFake(t)
	setState(t, M{"camera_rtsp_url": "rtsp://cam", "away_team": "Hatherleigh CC"})
	f.feedOK, f.feedDetail = true, "Scoreboard updated 4s ago."
	f.up(true, true, playing)
	got := map[string]any{}
	for k, v := range checklistItems() {
		got[k] = v["state"]
	}
	eqv(t, got, map[string]any{"obs": "done", "camera": "done", "replay": "done", "match": "done",
		"scorer": "done", "live": "done"})
	eqv(t, f.sent, [][]string{{"GetReplayBufferStatus", "GetStreamStatus", "GetMediaInputStatus"}}) // one connection
}

func TestChecklistNoCameraURLNotCounted(t *testing.T) {
	// A camera added in OBS by hand is a perfectly good setup.
	f := newChecklistFake(t)
	f.up(true, false, playing)
	eqv(t, checklistItems()["camera"]["state"], "na")
}

func TestChecklistCameraNotInOBS(t *testing.T) {
	f := newChecklistFake(t)
	setState(t, M{"camera_rtsp_url": "rtsp://cam"})
	f.up(true, false, nil)
	c := checklistItems()["camera"]
	eqv(t, []any{c["state"], c["action"], c["action_label"]}, []any{"todo", "add_camera", "Add to OBS"})
}

func TestChecklistCameraNotPlaying(t *testing.T) {
	f := newChecklistFake(t)
	setState(t, M{"camera_rtsp_url": "rtsp://cam"})
	f.up(true, false, M{"mediaState": "OBS_MEDIA_STATE_ERROR"})
	c := checklistItems()["camera"]
	eqv(t, c["action_label"], "Reconnect")
	if !strings.Contains(gs(c, "detail", ""), "error") {
		t.Fatal(c)
	}
}

func TestChecklistReplayBufferOff(t *testing.T) {
	f := newChecklistFake(t)
	f.up(false, false, playing)
	eqv(t, checklistItems()["replay"]["action"], "start_replay")
}

func TestChecklistMatchFetchNeedsPlayCricketKey(t *testing.T) {
	newChecklistFake(t)
	if checklistItems()["match"]["action"] != nil {
		t.Fatal("fetch offered without a key")
	}
	setState(t, M{"playcricket_api_key": "k"})
	eqv(t, checklistItems()["match"]["action"], "fetch_match")
}

func TestChecklistTwoLaptopModeOffersFind(t *testing.T) {
	newChecklistFake(t)
	setState(t, M{"pcs_source": "agent"})
	eqv(t, checklistItems()["scorer"]["action"], "find_agent")
}

func TestChecklistGoLiveOnlyOnceOBSIsUp(t *testing.T) {
	f := newChecklistFake(t)
	if checklistItems()["live"]["action"] != nil {
		t.Fatal("go live offered without OBS")
	}
	f.up(true, false, playing)
	eqv(t, checklistItems()["live"]["action"], "go_live")
}

func TestChecklistGoLive(t *testing.T) {
	f := newChecklistFake(t)
	f.up(true, false, playing)
	f.obs["StartStream"] = M{}
	st, _ := ChecklistAction("go_live")
	mustEq(t, st, 200)
	if !f.sentAny("StartStream") {
		t.Fatal(f.sent)
	}
}

func TestChecklistGoLiveWhenAlreadyLiveDoesNothing(t *testing.T) {
	f := newChecklistFake(t)
	f.up(true, true, playing)
	ChecklistAction("go_live")
	if f.sentAny("StartStream") {
		t.Fatal("started an already-live stream")
	}
}

func TestChecklistRefusalsReportedNotHidden(t *testing.T) {
	// OBS answering "no" used to come back as ok: true.
	f := newChecklistFake(t)
	f.up(false, false, playing)
	if _, out := ChecklistAction("start_replay"); out["ok"] != false {
		t.Fatal(out)
	}
	_, out := ChecklistAction("go_live")
	if out["ok"] != false || !strings.Contains(gs(out, "error", ""), "stream key") {
		t.Fatal(out)
	}
}

func TestChecklistStartReplay(t *testing.T) {
	f := newChecklistFake(t)
	f.up(false, false, playing)
	f.obs["StartReplayBuffer"] = M{}
	if _, out := ChecklistAction("start_replay"); out["ok"] != true {
		t.Fatal(out)
	}
	if !f.sentAny("StartReplayBuffer") {
		t.Fatal(f.sent)
	}
}

func TestChecklistStartOBSRunsOnceAtATime(t *testing.T) {
	newChecklistFake(t)
	checklistStartJob = func() {}
	st, _ := ChecklistAction("start_obs")
	mustEq(t, st, 202)
	st, _ = ChecklistAction("start_obs")
	mustEq(t, st, 409)
}

func TestChecklistUnknownAction(t *testing.T) {
	newChecklistFake(t)
	st, _ := ChecklistAction("format_the_laptop")
	mustEq(t, st, 400)
}

func runStartOBSJob(t *testing.T, config string) (prepared bool) {
	t.Helper()
	os.WriteFile(ConfigFile, []byte(config), 0o644)
	savedPrep, savedSetup := prepareOBSForChecklist, setupOBSForChecklist
	t.Cleanup(func() { prepareOBSForChecklist, setupOBSForChecklist = savedPrep, savedSetup })
	prepareOBSForChecklist = func(string, func(string), func(string)) bool { prepared = true; return true }
	setupOBSForChecklist = func(string, string, bool, bool) (bool, []string) { return true, []string{"✓ Ready"} }
	checklistJobMu.Lock()
	checklistJob["running"] = true
	checklistJobMu.Unlock()
	checklistRunStartOBS()
	return prepared
}

func TestChecklistStartOBSJobAdoptsNewPassword(t *testing.T) {
	// Preparing OBS can write a fresh WebSocket password into config.ini; without
	// following it, every later OBS call from the server fails to authenticate.
	newChecklistFake(t)
	if !runStartOBSJob(t, "[OBS]\nobs_password = fresh-pw\n") {
		t.Fatal("OBS not prepared")
	}
	eqv(t, LoadState()["obs_password"], "fresh-pw")
	checklistJobMu.Lock()
	defer checklistJobMu.Unlock()
	eqv(t, checklistJob["running"], false)
	eqv(t, checklistJob["ok"], true)
}

// Start OBS must never replace the panel's working password.
func TestChecklistBlankConfigPasswordNotCopiedOverWorkingOne(t *testing.T) {
	newChecklistFake(t)
	setState(t, M{"obs_password": "typed-in-the-panel"})
	runStartOBSJob(t, "[OBS]\nobs_password =\n")
	eqv(t, LoadState()["obs_password"], "typed-in-the-panel")
}

func TestChecklistUnmanagedOBSKeepsPanelPassword(t *testing.T) {
	newChecklistFake(t)
	setState(t, M{"obs_password": "typed-in-the-panel"})
	runStartOBSJob(t, "[OBS]\nobs_password = other\nmanage_obs = no\n")
	eqv(t, LoadState()["obs_password"], "typed-in-the-panel")
}

func TestChecklistHTTPNeedsLogin(t *testing.T) {
	f := newChecklistFake(t)
	f.up(true, false, playing)
	s := serve(t)
	ClubPassword = "secret"
	mustEq(t, s.get("/checklist").status, 401)
	// These start OBS and go live. Unlike the overlay's endpoints, no loopback pass.
	st, _ := s.postJSON("/checklist/action", M{"action": "go_live"}, map[string]string{"Origin": "http://" + s.addr})
	if st != 401 && st != 403 {
		t.Fatal(st)
	}
	if f.sentAny("StartStream") {
		t.Fatal("went live without a login")
	}
}

func controlSource(t *testing.T) string {
	t.Helper()
	b, err := assets.Embedded("control.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPanelNoManualTicksLeft(t *testing.T) {
	src := controlSource(t)
	for _, gone := range []string{"bbcc_checklist", "toggleCheck", "resetChecklist", "buildChecklist"} {
		if strings.Contains(src, gone) {
			t.Fatal(gone)
		}
	}
}

func TestPanelGoingLiveAsksFirst(t *testing.T) {
	if !strings.Contains(after(t, controlSource(t), "async function runChecklistAction(", 500), "action === 'go_live' && !confirm(") {
		t.Fatal("go live doesn't confirm")
	}
}

func TestPanelChecklistRefreshedAfterLogin(t *testing.T) {
	mustEq(t, strings.Count(controlSource(t), "refreshChecklist();        // same: /checklist is token-gated"), 2)
}
