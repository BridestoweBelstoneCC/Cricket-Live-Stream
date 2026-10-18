package obs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/obsws/obswstest"
)

// fakeOBS answers like a fresh OBS: no scenes or inputs yet, replay buffer not running.
type fakeState struct {
	live           bool
	mode           string
	rbStarts       bool
	sceneItems     map[string][]any
	startReplayErr string
}

func newFake(t *testing.T, st *fakeState) *obswstest.Server {
	if st.mode == "" {
		st.mode = "Simple"
	}
	f := obswstest.New("pw", func(rt string, d map[string]any) (bool, map[string]any) {
		switch rt {
		case "GetSceneList":
			return true, map[string]any{"scenes": []any{}}
		case "GetInputList":
			return true, map[string]any{"inputs": []any{}}
		case "GetSceneItemList":
			scene, _ := d["sceneName"].(string)
			return true, map[string]any{"sceneItems": st.sceneItems[scene]}
		case "GetSceneItemTransform":
			return true, map[string]any{"sceneItemTransform": map[string]any{"positionX": 0, "positionY": 0, "scaleX": 1, "scaleY": 1}}
		case "GetVideoSettings":
			return true, map[string]any{"baseWidth": 1920, "baseHeight": 1080}
		case "SetSceneItemIndex":
			// Move the item and renumber, as OBS does.
			for scene, list := range st.sceneItems {
				var moving map[string]any
				var rest []any
				for _, it := range list {
					m := it.(map[string]any)
					if m["sceneItemId"] == d["sceneItemId"] {
						moving = m
					} else {
						rest = append(rest, m)
					}
				}
				if moving == nil {
					continue
				}
				at := int(d["sceneItemIndex"].(float64))
				rest = append(rest[:at], append([]any{moving}, rest[at:]...)...)
				for i, it := range rest {
					it.(map[string]any)["sceneItemIndex"] = i
				}
				st.sceneItems[scene] = rest
			}
			return true, nil
		case "GetStreamStatus":
			return true, map[string]any{"outputActive": st.live}
		case "GetProfileParameter":
			return true, map[string]any{"parameterValue": st.mode}
		case "GetReplayBufferStatus":
			return true, map[string]any{"outputActive": false}
		case "StartReplayBuffer":
			return st.rbStarts, nil
		}
		return true, nil
	})
	t.Cleanup(f.Close)
	return f
}

func runFakeSetup(t *testing.T, st *fakeState, o SetupOptions) (*obswstest.Server, []string) {
	f := newFake(t, st)
	o.Target = f.Target()
	ok, msgs := Setup(o)
	if !ok {
		t.Fatal(msgs)
	}
	return f, msgs
}

func indexOf(types []string, want string, from int) int {
	for i := from; i < len(types); i++ {
		if types[i] == want {
			return i
		}
	}
	return -1
}

func paramSets(f *obswstest.Server) map[string]string {
	out := map[string]string{}
	for _, r := range f.Requests {
		if r.Type == "SetProfileParameter" {
			out[r.Data["parameterCategory"].(string)+"/"+r.Data["parameterName"].(string)] = r.Data["parameterValue"].(string)
		}
	}
	return out
}

func joined(msgs []string) string { return strings.Join(msgs, "\n") }

// The replay buffer must be ENABLED, not just started: StartReplayBuffer fails outright
// on a fresh OBS, and nothing announced it again until a wicket fell with no replay.
func TestEnablesTheBufferInBothModesBeforeStartingIt(t *testing.T) {
	f, msgs := runFakeSetup(t, &fakeState{rbStarts: true}, SetupOptions{})
	p := paramSets(f)
	for _, k := range []string{"SimpleOutput/RecRB", "AdvOut/RecRB"} {
		if p[k] != "true" {
			t.Fatalf("%s not enabled: %v", k, p)
		}
	}
	if p["SimpleOutput/RecRBTime"] != "25" || p["AdvOut/RecRBTime"] != "25" {
		t.Fatal(p)
	}
	types := f.Types()
	enable := -1
	for i, r := range f.Requests {
		if r.Type == "SetProfileParameter" && r.Data["parameterName"] == "RecRB" && enable < 0 {
			enable = i
		}
	}
	if start := indexOf(types, "StartReplayBuffer", 0); enable < 0 || start < enable {
		t.Fatal("must enable the buffer before trying to start it")
	}
	if !strings.Contains(joined(msgs), "Replay buffer started") {
		t.Fatal(msgs)
	}
}

// OBS creates outputs at startup, so a buffer enabled in a running instance can't start
// until OBS restarts: that needs its own message, not "go and enable it".
func TestFailureToStartAfterEnablingSaysRestartOBS(t *testing.T) {
	_, msgs := runFakeSetup(t, &fakeState{rbStarts: false}, SetupOptions{})
	if !strings.Contains(joined(msgs), "restart OBS once") || !strings.Contains(joined(msgs), "wouldn't start yet") {
		t.Fatal(msgs)
	}
}

func TestOverlayRoutesItsAudioIntoTheStream(t *testing.T) {
	f, _ := runFakeSetup(t, &fakeState{rbStarts: true}, SetupOptions{ServerPort: 5000})
	for _, r := range f.Requests {
		if r.Type == "CreateInput" && r.Data["inputName"] == "Overlay" {
			s := r.Data["inputSettings"].(map[string]any)
			if s["reroute_audio"] != true || s["url"] != "http://localhost:5000/overlay" || s["width"].(float64) != 1920 {
				t.Fatal(s)
			}
			return
		}
	}
	t.Fatal("no Overlay created")
}

// Move cameras DOWN, never lift graphics UP: a sponsor logo above the scorebar stays put.
func TestCameraCoveringTheOverlayIsMovedBeneathIt(t *testing.T) {
	item := func(id int, name, kind string, idx int) map[string]any {
		return map[string]any{"sceneItemId": float64(id), "sourceName": name, "inputKind": kind, "sceneItemIndex": float64(idx)}
	}
	st := &fakeState{rbStarts: true, sceneItems: map[string][]any{"Main": {
		item(1, "Overlay", "browser_source", 0),
		item(2, "Sponsor", "image_source", 1),
		item(3, "Camera", "ffmpeg_source", 2),
	}}}
	_, msgs := runFakeSetup(t, st, SetupOptions{})
	order := []string{}
	for _, it := range st.sceneItems["Main"] {
		order = append(order, it.(map[string]any)["sourceName"].(string))
	}
	if strings.Join(order, ",") != "Camera,Overlay,Sponsor" {
		t.Fatal(order, msgs)
	}
	if !strings.Contains(joined(msgs), "Camera moved back under the Overlay in Main") {
		t.Fatal(msgs)
	}
}

func TestLiveStreamSettingsAreNeverTouched(t *testing.T) {
	f, msgs := runFakeSetup(t, &fakeState{rbStarts: true, live: true}, SetupOptions{StreamKey: "k", BitrateKbps: 3000})
	for _, r := range f.Requests {
		if r.Type == "SetStreamServiceSettings" {
			t.Fatal("stream key changed while live")
		}
	}
	if _, set := paramSets(f)["SimpleOutput/VBitrate"]; set {
		t.Fatal("bitrate changed while live")
	}
	if !strings.Contains(joined(msgs), "Stream is LIVE") {
		t.Fatal(msgs)
	}
}

func TestBitrateResetOnlyInSimpleMode(t *testing.T) {
	f, _ := runFakeSetup(t, &fakeState{rbStarts: true}, SetupOptions{BitrateKbps: 3000})
	if paramSets(f)["SimpleOutput/VBitrate"] != "3000" {
		t.Fatal("bitrate not reset")
	}
	f, msgs := runFakeSetup(t, &fakeState{rbStarts: true, mode: "Advanced"}, SetupOptions{BitrateKbps: 3000})
	if _, set := paramSets(f)["SimpleOutput/VBitrate"]; set || !strings.Contains(joined(msgs), "Advanced output mode") {
		t.Fatal(msgs)
	}
}

func TestUnreachableOBSIsExplained(t *testing.T) {
	ok, msgs := Setup(SetupOptions{Target: targetNowhere()})
	if ok || !strings.Contains(joined(msgs), "is OBS open?") {
		t.Fatal(ok, msgs)
	}
}

// ── SetupFromConfig: the first-run restart and the bitrate choice ──

func swapSetup(t *testing.T, results [][]string, closes bool) (calls, closeCalls, prepCalls *int, got *[]SetupOptions) {
	a, b, c := 0, 0, 0
	var opts []SetupOptions
	oldRun, oldClose, oldPrep := runSetup, closeOBS, prepareOBS
	runSetup = func(o SetupOptions) (bool, []string) {
		opts = append(opts, o)
		r := results[min(a, len(results)-1)]
		a++
		return true, r
	}
	closeOBS = func(time.Duration) bool { b++; return closes }
	prepareOBS = func(string, func(string), func(string)) bool { c++; return true }
	t.Cleanup(func() { runSetup, closeOBS, prepareOBS = oldRun, oldClose, oldPrep })
	return &a, &b, &c, &opts
}

var (
	needsRestart = []string{"  ✓ Replay buffer enabled, 25s", "  ⚠ Replay buffer enabled but wouldn't start yet — restart OBS once"}
	fine         = []string{"  ✓ Replay buffer already running"}
)

func writeConfig(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "config.ini")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestRestartsOnceAndFinishes(t *testing.T) {
	setups, closes, preps, _ := swapSetup(t, [][]string{needsRestart, fine}, true)
	ok, msgs := SetupFromConfig(writeConfig(t, "[OBS]\nobs_password = pw\nmanage_obs = \n"), "", false, true)
	if !ok || *setups != 2 || *closes != 1 || *preps != 1 || !strings.Contains(joined(msgs), "Restarted OBS once") {
		t.Fatal(ok, *setups, *closes, *preps, msgs)
	}
}

func TestNoRestartWhenNotNeeded(t *testing.T) {
	setups, closes, _, _ := swapSetup(t, [][]string{fine}, true)
	SetupFromConfig(writeConfig(t, "[OBS]\nobs_password = pw\n"), "", false, true)
	if *setups != 1 || *closes != 0 {
		t.Fatal(*setups, *closes)
	}
}

// Someone running setup by hand is managing OBS themselves.
func TestStandaloneRunNeverRestarts(t *testing.T) {
	setups, closes, _, _ := swapSetup(t, [][]string{needsRestart}, true)
	_, msgs := SetupFromConfig(writeConfig(t, "[OBS]\nobs_password = pw\n"), "", false, false)
	if *setups != 1 || *closes != 0 || !strings.Contains(joined(msgs), "restart OBS once") {
		t.Fatal(*setups, *closes, msgs)
	}
}

func TestManageOBSNoNeverRestarts(t *testing.T) {
	_, closes, _, _ := swapSetup(t, [][]string{needsRestart}, true)
	SetupFromConfig(writeConfig(t, "[OBS]\nobs_password = pw\nmanage_obs = no\n"), "", false, true)
	if *closes != 0 {
		t.Fatal(*closes)
	}
}

// e.g. OBS asking "exit with outputs active?": never answered for anyone.
func TestOBSThatWontCloseIsLeftRunning(t *testing.T) {
	setups, closes, preps, _ := swapSetup(t, [][]string{needsRestart}, false)
	SetupFromConfig(writeConfig(t, "[OBS]\nobs_password = pw\n"), "", false, true)
	if *setups != 1 || *closes != 1 || *preps != 0 {
		t.Fatal(*setups, *closes, *preps)
	}
}

func TestBlankBitrateUsesTheRecommendationButManualOptsOut(t *testing.T) {
	dir := t.TempDir()
	st := filepath.Join(dir, "match_state.json")
	b, _ := json.Marshal(map[string]any{"network_test_mbps": 4.0})
	os.WriteFile(st, b, 0o644)
	for raw, want := range map[string]int{"": 3000, "2500": 2500, "manual": 0} {
		_, _, _, opts := swapSetup(t, [][]string{fine}, true)
		SetupFromConfig(writeConfig(t, "[OBS]\nobs_password = pw\n\n[Stream]\nbitrate_kbps = "+raw+"\n"), st, false, false)
		if got := (*opts)[0]; got.BitrateKbps != want || got.Target.Password != "pw" {
			t.Fatalf("%q: %+v", raw, got)
		}
	}
}

func targetNowhere() obsws.Target { return obsws.Target{Host: "127.0.0.1", Port: 1} }

func TestInsidePortable(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "obs_portable_mode.txt"), nil, 0o644)
	t.Setenv(PortableEnv, root)
	for exe, want := range map[string]bool{
		filepath.Join(root, "bin", "64bit", "obs64.exe"):                true,
		filepath.Join(root+"-other", "bin", "obs64.exe"):                false, // a sibling with the same prefix
		filepath.Join(filepath.Dir(root), "obs64.exe"):                  false,
		filepath.Join(root, "..", filepath.Base(root)+"x", "obs64.exe"): false,
		`C:\Program Files\obs-studio\bin\64bit\obs64.exe`:               false,
		"": false, // unreadable
	} {
		if got := insidePortable(exe); got != want {
			t.Errorf("insidePortable(%q) = %v, want %v", exe, got, want)
		}
	}
}

// While sandboxed, a connection to OBS's own port is refused whenever an OBS other than
// the portable copy is running — before anything is sent. A test's fake OBS on a port of
// its own, or a run that isn't sandboxed, is untouched.
func TestSandboxRefusesTheInstalledOBS(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "obs_portable_mode.txt"), nil, 0o644)
	saved := foreignOBS
	t.Cleanup(func() { foreignOBS = saved })
	foreignOBS = func() []string { return []string{`C:\Program Files\obs-studio\bin\64bit\obs64.exe`} }

	t.Setenv(PortableEnv, root)
	t.Setenv(obsws.WSPortEnv, "")
	for _, target := range []obsws.Target{{}, {Host: "127.0.0.1", Port: 4455}, {Host: "localhost"}} {
		err := sandboxGuard(target.Resolved())
		if err == nil || !strings.Contains(err.Error(), "Program Files") {
			t.Errorf("%+v: %v", target, err)
		}
		if _, err := obsws.Dial(target, time.Second); err == nil || !strings.Contains(err.Error(), "refusing to connect") {
			t.Errorf("Dial %+v: %v", target, err)
		}
	}
	// A fake OBS on its own port still answers.
	f := newFake(t, &fakeState{})
	if r := obsws.Call(f.Target(), []obsws.Req{{Type: "GetSceneList"}}, 3*time.Second); r == nil {
		t.Error("fake OBS refused")
	}
	// Moved off 4455 (the sandbox's redirect): not OBS's port any more.
	t.Setenv(obsws.WSPortEnv, "45999")
	if err := sandboxGuard((obsws.Target{}).Resolved()); err != nil {
		t.Errorf("redirected: %v", err)
	}
	// Not sandboxed: nothing is refused.
	t.Setenv(PortableEnv, "")
	t.Setenv(obsws.WSPortEnv, "")
	if err := sandboxGuard((obsws.Target{}).Resolved()); err != nil {
		t.Errorf("not sandboxed: %v", err)
	}
}

// With an OBS installed and open on this machine (only reads the process list): a
// sandbox doesn't see it, so it can't close, kill, wait on or connect to it.
func TestSandboxDoesntSeeTheOBSThatsOpen(t *testing.T) {
	if len(allPIDs()) == 0 {
		t.Skip("no OBS open on this machine")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "obs_portable_mode.txt"), nil, 0o644)
	t.Setenv(PortableEnv, root)
	t.Setenv(obsws.WSPortEnv, "")
	if p := PIDs(); len(p) != 0 {
		t.Errorf("sandbox sees OBS processes %v", p)
	}
	if Running() {
		t.Error("sandbox thinks OBS is running")
	}
	f := foreignOBS()
	if len(f) == 0 {
		t.Fatal("the open OBS isn't reported as foreign")
	}
	t.Logf("foreign: %v", f)
	if err := sandboxGuard((obsws.Target{}).Resolved()); err == nil {
		t.Error("guard let a sandbox connect to the open OBS")
	}
}
