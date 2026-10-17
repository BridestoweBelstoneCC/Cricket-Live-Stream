package server

// The AI camera spotter (tests/test_camera_spotter.py): Claude looks at each camera while
// live and flags what a viewer would notice; a frozen picture is caught without AI. OBS
// and the API are faked. Plus the source-level rules from tests/test_obs_fast.py.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cricketstream/internal/ai"
)

var spotSt = M{"obs_camera_name": "Wide Camera", "obs_camera2_name": "Bowler End Camera",
	"camera2_rtsp_url": "rtsp://cam2", "anthropic_api_key": "k"}

func resetSpotter(t *testing.T) {
	t.Helper()
	spotterMu.Lock()
	spotterCams, spotterLastRun, spotterRunning = map[string]M{}, 0, false
	spotterMu.Unlock()
	t.Cleanup(func() {
		spotterMu.Lock()
		spotterCams, spotterLastRun, spotterRunning = map[string]M{}, 0, false
		spotterMu.Unlock()
	})
}

type lookResult struct {
	ok      bool
	problem string
	err     error
}

// runSpot feeds these frames in order and answers every AI look with res.
func runSpot(t *testing.T, frames [][]byte, res lookResult, useAI bool) (M, int) {
	t.Helper()
	savedFrame, savedLook := obsFrame, aiLook
	t.Cleanup(func() { obsFrame, aiLook = savedFrame, savedLook })
	i, looks := 0, 0
	obsFrame = func(M, string) []byte {
		f := frames[i%len(frames)]
		i++
		return f
	}
	aiLook = func(M, []byte) (bool, string, error) { looks++; return res.ok, res.problem, res.err }
	return SpotCameras(spotSt, useAI), looks
}

func TestSpotterBothCamerasChecked(t *testing.T) {
	var names []string
	for _, c := range spotterCameras(spotSt) {
		names = append(names, c[1])
	}
	eqv(t, names, []string{"Wide Camera", "Bowler End Camera"})
	one := shallow(spotSt)
	one["camera2_rtsp_url"] = ""
	mustEq(t, len(spotterCameras(one)), 1)
}

func TestSpotterProblemReported(t *testing.T) {
	resetSpotter(t)
	cams, _ := runSpot(t, [][]byte{[]byte("a"), []byte("b")}, lookResult{false, "Lens fogged", nil}, true)
	eqv(t, gm(cams, "Wide Camera")["problem"], "Lens fogged")
	eqv(t, spotterStatus()["problems"], 2)
}

func TestSpotterFrozenCaughtWithoutAI(t *testing.T) {
	resetSpotter(t)
	runSpot(t, [][]byte{[]byte("same"), []byte("x")}, lookResult{true, "", nil}, true)
	cams, looks := runSpot(t, [][]byte{[]byte("same"), []byte("y")}, lookResult{true, "", nil}, true)
	wide := gm(cams, "Wide Camera")
	if !strings.Contains(gs(wide, "problem", ""), "frozen") {
		t.Fatal(wide)
	}
	eqv(t, wide["how"], "frozen")
	mustEq(t, looks, 1) // only the camera that wasn't frozen
}

func TestSpotterNoPictureFromOBS(t *testing.T) {
	resetSpotter(t)
	cams, looks := runSpot(t, [][]byte{nil, nil}, lookResult{true, "", nil}, true)
	eqv(t, gm(cams, "Wide Camera")["problem"], "No picture from OBS")
	mustEq(t, looks, 0)
}

func TestSpotterAIFailureIsNotACameraProblem(t *testing.T) {
	resetSpotter(t)
	cams, _ := runSpot(t, [][]byte{[]byte("f")}, lookResult{err: errors.New("rate limited")}, true)
	wide := gm(cams, "Wide Camera")
	mustEq(t, wide["ok"], any(true))
	if !strings.Contains(gs(wide, "problem", ""), "Couldn't check") {
		t.Fatal(wide)
	}
}

func TestSpotterSchedule(t *testing.T) {
	resetSpotter(t)
	on := shallow(spotSt)
	on["camera_spotter"], on["camera_spotter_minutes"] = true, 5
	if !spotterDue(on, true, 1000) {
		t.Fatal("due")
	}
	if spotterDue(on, false, 1000) {
		t.Fatal("never off-air")
	}
	off := shallow(on)
	off["camera_spotter"] = false
	if spotterDue(off, true, 1000) {
		t.Fatal("switched off")
	}
	noKey := shallow(on)
	noKey["anthropic_api_key"] = ""
	if spotterDue(noKey, true, 1000) {
		t.Fatal("no key")
	}
	spotterMu.Lock()
	spotterLastRun = 1000
	spotterMu.Unlock()
	if spotterDue(on, true, 1000+299) || !spotterDue(on, true, 1000+300) {
		t.Fatal("interval")
	}
}

func TestSpotterAILookSendsFrameAndParsesJSON(t *testing.T) {
	saved := ai.Ask
	t.Cleanup(func() { ai.Ask = saved })
	var got ai.Request
	ai.Ask = func(_ context.Context, r ai.Request) (string, error) {
		got = r
		return "```json\n{\"ok\": false, \"problem\": \"Camera knocked\"}\n```", nil
	}
	ok, problem, err := aiLook(spotSt, []byte("\xff\xd8jpeg"))
	if err != nil || ok || problem != "Camera knocked" {
		t.Fatal(ok, problem, err)
	}
	mustEq(t, string(got.ImageJPEG), "\xff\xd8jpeg")
	mustEq(t, ai.Model, "claude-haiku-4-5")
}

func TestSpotterCheckNowAndStatus(t *testing.T) {
	s := newTestServer(t, "")
	resetSpotter(t)
	setState(t, M{"obs_camera_name": "Cam", "camera2_rtsp_url": "", "anthropic_api_key": ""})
	savedFrame, savedLook := obsFrame, aiLook
	t.Cleanup(func() { obsFrame, aiLook = savedFrame, savedLook })
	obsFrame = func(M, string) []byte { return []byte("f") }
	looks := 0
	aiLook = func(M, []byte) (bool, string, error) { looks++; return true, "", nil }
	st, d := s.postJSON("/camera/spotter", M{})
	mustEq(t, st, 200)
	mustEq(t, d["used_ai"], any(false)) // no key: the frozen check only, no AI call
	mustEq(t, looks, 0)
	cam := gl(d, "cameras")[0].(M)
	eqv(t, cam["label"], "Wide camera")
	if _, ok := cam["digest"]; ok {
		t.Fatal("digest leaked")
	}
	st, _ = s.getJSON("/camera/spotter")
	mustEq(t, st, 200)
}

// ── The held-open OBS socket is for camera cuts only (tests/test_obs_fast.py) ──

func packageSource(t *testing.T) map[string]string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		out[f] = string(b)
	}
	return out
}

func TestCameraEndpointPrefersFastPathKeepsFallback(t *testing.T) {
	src := packageSource(t)["handler_post.go"]
	i := strings.Index(src, `case path == "/camera/scene":`)
	if i < 0 {
		t.Fatal("route not found")
	}
	endpoint := src[i : i+1600]
	if !strings.Contains(endpoint, "obsFastCall(") {
		t.Fatal("camera cuts should use the fast path")
	}
	if !strings.Contains(endpoint, "obsCall(") {
		t.Fatal("the connection-per-call fallback must remain")
	}
}

func TestOtherCallersStillUsePerCallConnection(t *testing.T) {
	// The stream sentinel and health checks are seconds-to-minutes apart; a held socket
	// buys them nothing and would only add a failure mode.
	n := 0
	re := regexp.MustCompile(`obsFastCall\(`)
	for _, s := range packageSource(t) {
		n += len(re.FindAllString(s, -1))
	}
	if n > 2 { // the definition and the camera cut
		t.Fatalf("obsFastCall used %d times — the fast path is for camera cuts only", n)
	}
}
