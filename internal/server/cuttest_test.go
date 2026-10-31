package server

// The camera cut test, against a fake OBS: it refuses what it must (no second camera, on
// air, already running), finds a camera missing from a scene before cutting anything,
// cuts and comes back exactly as a match does, and notices a picture that drops out.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/obsws"
)

type cutFake struct {
	mu        sync.Mutex
	live      bool
	program   string
	scenes    map[string]map[string]bool // scene -> sources in it
	media     map[string]string          // input -> mediaState
	onBowler  map[string]string          // media states once the bowler's end is on air
	refuseCut bool
	cuts      []string
}

func newCutFake(t *testing.T) *cutFake {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"camera_rtsp_url": "rtsp://wide", "camera2_rtsp_url": "rtsp://bowler"})
	all := func() map[string]bool { return map[string]bool{"Cricket Camera": true, "Bowler End Camera": true} }
	f := &cutFake{program: "Main", scenes: map[string]map[string]bool{"Main": all(), "Main-Bowler": all(), "Replay": all()},
		media: map[string]string{"Cricket Camera": "OBS_MEDIA_STATE_PLAYING", "Bowler End Camera": "OBS_MEDIA_STATE_PLAYING"}}
	savedCall, savedFast, savedHold := obsCall, obsFastCall, cutTestHold
	cutTestHold = 20 * time.Millisecond
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := make([]map[string]any, len(reqs))
		for i, r := range reqs {
			d, _ := r.Data.(M)
			switch r.Type {
			case "GetStreamStatus":
				out[i] = M{"outputActive": f.live}
			case "GetSceneItemId":
				if f.scenes[str(d, "sceneName", "")][str(d, "sourceName", "")] {
					out[i] = M{"sceneItemId": 1}
				}
			case "GetMediaInputStatus":
				name := str(d, "inputName", "")
				if s, ok := f.onBowler[name]; ok && f.program == "Main-Bowler" {
					out[i] = M{"mediaState": s}
				} else if s, ok := f.media[name]; ok {
					out[i] = M{"mediaState": s}
				}
			case "GetCurrentProgramScene":
				out[i] = M{"currentProgramSceneName": f.program}
			case "SetCurrentProgramScene":
				out[i] = f.cut(str(d, "sceneName", ""))
			}
		}
		return out
	}
	// The held socket, as /camera/scene uses it: the test must cut through the same path.
	obsFastCall = func(_ M, typ string, data any, _ time.Duration) (bool, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if typ != "SetCurrentProgramScene" {
			t.Errorf("fast path used for %s", typ)
		}
		return f.cut(str(data.(M), "sceneName", "")) != nil, true
	}
	cutTestMu.Lock()
	saved := cutTestJob
	cutTestJob = M{"running": false, "ok": nil, "steps": []any{}, "finished": nil}
	cutTestMu.Unlock()
	t.Cleanup(func() {
		obsCall, obsFastCall, cutTestHold = savedCall, savedFast, savedHold
		cutTestMu.Lock()
		cutTestJob = saved
		cutTestMu.Unlock()
	})
	return f
}

// cut: held under f.mu.
func (f *cutFake) cut(scene string) M {
	f.cuts = append(f.cuts, scene)
	if f.refuseCut || f.scenes[scene] == nil {
		return nil
	}
	f.program = scene
	return M{}
}

func runCutTestToEnd(t *testing.T) M {
	t.Helper()
	if status, out := StartCutTest(); status != 202 {
		t.Fatalf("start: %d %v", status, out)
	}
	for i := 0; i < 500; i++ {
		if s := CutTestStatus(); s["running"] == false {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the test never finished")
	return nil
}

func stepTexts(s M) string {
	var out []string
	for _, x := range gl(s, "steps") {
		m := x.(M)
		mark := "✗"
		if m["ok"] == true {
			mark = "✓"
		}
		out = append(out, mark+" "+str(m, "text", ""))
	}
	return strings.Join(out, "\n")
}

func TestCutTestPasses(t *testing.T) {
	f := newCutFake(t)
	s := runCutTestToEnd(t)
	if s["ok"] != true {
		t.Fatalf("should pass:\n%s", stepTexts(s))
	}
	if got := strings.Join(f.cuts, ","); got != "Main-Bowler,Main" {
		t.Fatalf("cuts: %s", got)
	}
	txt := stepTexts(s)
	for _, want := range []string{"in Main, Main-Bowler and Replay", "Cut to the bowler's end in", "Cut to the wide camera in",
		"showing a picture on the bowler's end", "The camera cut works"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "✗") {
		t.Errorf("a failed step in a passing test:\n%s", txt)
	}
}

// The known cause of drift, found before anything is cut on screen.
func TestCutTestFindsACameraMissingFromAScene(t *testing.T) {
	f := newCutFake(t)
	delete(f.scenes["Replay"], "Bowler End Camera")
	s := runCutTestToEnd(t)
	if s["ok"] != false || !strings.Contains(stepTexts(s), "Camera 2 isn't in the Replay scene") {
		t.Fatalf("missing camera:\n%s", stepTexts(s))
	}
	if len(f.cuts) != 0 {
		t.Fatalf("cut anyway: %v", f.cuts)
	}
}

func TestCutTestNoticesAPictureDroppingOut(t *testing.T) {
	f := newCutFake(t)
	f.onBowler = map[string]string{"Cricket Camera": "OBS_MEDIA_STATE_OPENING"} // the wide camera reconnecting
	s := runCutTestToEnd(t)
	txt := stepTexts(s)
	if s["ok"] != false || !strings.Contains(txt, "No picture on the bowler's end (Wide camera: connecting)") {
		t.Fatalf("dropout:\n%s", txt)
	}
	if f.program != "Main" {
		t.Fatalf("left on %q", f.program)
	}
}

func TestCutTestOBSRefusesTheCut(t *testing.T) {
	f := newCutFake(t)
	f.refuseCut = true
	s := runCutTestToEnd(t)
	if s["ok"] != false || !strings.Contains(stepTexts(s), "OBS wouldn't cut to the bowler's end (Main-Bowler)") {
		t.Fatalf("refused:\n%s", stepTexts(s))
	}
}

// An operator mid-setup on another scene is put back there afterwards.
func TestCutTestPutsTheSceneBack(t *testing.T) {
	f := newCutFake(t)
	f.scenes["Starting Soon"] = map[string]bool{}
	f.program = "Starting Soon"
	if s := runCutTestToEnd(t); s["ok"] != true {
		t.Fatalf("should pass:\n%s", stepTexts(s))
	}
	if f.program != "Starting Soon" {
		t.Fatalf("left on %q", f.program)
	}
}

func TestCutTestRefuses(t *testing.T) {
	f := newCutFake(t)
	f.live = true
	if _, out := StartCutTest(); out["ok"] != false || !strings.Contains(str(out, "error", ""), "live") {
		t.Fatalf("on air: %v", out)
	}
	f.live = false
	setState(t, M{"camera2_rtsp_url": ""})
	if _, out := StartCutTest(); out["ok"] != false || !strings.Contains(str(out, "error", ""), "no bowler's-end camera") {
		t.Fatalf("one camera: %v", out)
	}
	if len(f.cuts) != 0 {
		t.Fatalf("cut while refusing: %v", f.cuts)
	}
	setState(t, M{"camera2_rtsp_url": "rtsp://bowler"})
	cutTestMu.Lock()
	cutTestJob["running"] = true
	cutTestMu.Unlock()
	if status, _ := StartCutTest(); status != 409 {
		t.Fatalf("already running: %d", status)
	}
}

// Login-gated both ways: it cuts the picture, so never the overlay's loopback carve-out.
func TestCutTestRoutesNeedALogin(t *testing.T) {
	newCutFake(t)
	ts := newTestServer(t, "pw")
	if code, _ := ts.getJSON("/camera/cut_test"); code != 401 {
		t.Fatalf("GET without a login: %d", code)
	}
	if r := ts.post("/camera/cut_test", M{}); r.status != 401 {
		t.Fatalf("POST without a login: %d", r.status)
	}
}
