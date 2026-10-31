package server

// The second camera: held cuts (the over change, the walk-out), side-on wicket replays.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/obsws"
)

// fakeProgram is OBS's program scene, for cutToScene and programScene.
type fakeProgram struct {
	mu    sync.Mutex
	scene string
	cuts  []string
}

func (f *fakeProgram) on() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scene
}

func (f *fakeProgram) set(s string) {
	f.mu.Lock()
	f.scene = s
	f.mu.Unlock()
}

func (f *fakeProgram) cutList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cuts...)
}

func fakeOBSProgram(t *testing.T) *fakeProgram {
	t.Helper()
	f := &fakeProgram{scene: "Main"}
	camHoldMu.Lock()
	replayBusyUntil = time.Time{} // an earlier test's replay
	camHoldMu.Unlock()
	savedFast, savedCall := obsFastCall, obsCall
	obsFastCall = func(_ M, typ string, data any, _ time.Duration) (bool, bool) {
		if typ == "SetCurrentProgramScene" {
			s := gs(data.(M), "sceneName", "")
			f.mu.Lock()
			f.scene, f.cuts = s, append(f.cuts, s)
			f.mu.Unlock()
		}
		return true, true
	}
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		out := make([]map[string]any, len(reqs))
		for i, r := range reqs {
			if r.Type == "GetCurrentProgramScene" {
				out[i] = M{"currentProgramSceneName": f.on()}
			} else {
				out[i] = M{}
			}
		}
		return out
	}
	t.Cleanup(func() { obsFastCall, obsCall = savedFast, savedCall; endCamHold() })
	return f
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func TestHoldCutComesBackOnlyIfStillOnCameraTwo(t *testing.T) {
	sandbox(t, "")
	f := fakeOBSProgram(t)
	st := LoadState()

	// Still on camera 2 when the hold ends: back to Main; the break show waited for it.
	holdCut(st, "Main-Bowler", "Main", 80*time.Millisecond)
	if breakClear() {
		t.Fatal("the break show would play over a held cut")
	}
	waitFor(t, "the cut back", func() bool { return f.on() == "Main" })
	eqv(t, f.cutList(), []string{"Main-Bowler", "Main"})
	if !breakClear() {
		t.Fatal("the hold didn't end")
	}

	// A replay took the picture meanwhile: left alone.
	holdCut(st, "Main-Bowler", "Main", 60*time.Millisecond)
	f.set("Replay")
	time.Sleep(150 * time.Millisecond)
	if f.on() != "Replay" {
		t.Fatal("cut away from a replay")
	}

	// Another cut was asked for (the operator's): the old hold's way back is forgotten.
	f.set("Main")
	holdCut(st, "Main-Bowler", "Main", 60*time.Millisecond)
	endCamHold()
	time.Sleep(150 * time.Millisecond)
	if f.on() != "Main-Bowler" {
		t.Fatal("a stale hold cut back")
	}
}

func TestCutIfOn(t *testing.T) {
	sandbox(t, "")
	f := fakeOBSProgram(t)
	st := LoadState()
	if ok, cut := cutIfOn(st, "Main", "Main-Bowler"); !ok || cut {
		t.Fatal("cut while not on camera 2")
	}
	f.set("Main-Bowler")
	if ok, cut := cutIfOn(st, "Main", "Main-Bowler"); !ok || !cut || f.on() != "Main" {
		t.Fatal("didn't cut back")
	}
}

func TestCameraSceneEndpointHoldAndIfOn(t *testing.T) {
	ts := newTestServer(t, "")
	f := fakeOBSProgram(t)
	post := func(body string) M {
		_, r := ts.postJSON("/camera/scene", []byte(body))
		return r
	}
	if r := post(`{"scene":"Main-Bowler","hold":0.05,"back":"Main"}`); !gb(r, "ok", false) {
		t.Fatal(r)
	}
	waitFor(t, "the cut back", func() bool { return f.on() == "Main" })
	if r := post(`{"scene":"Main","if_on":"Main-Bowler"}`); !gb(r, "ok", false) || !strings.Contains(gs(r, "message", ""), "left as it is") {
		t.Fatal(r)
	}
}

// ── Side-on replays ──

func TestSideCamArgsCopyCameraTwoVideoIntoTheRing(t *testing.T) {
	a := strings.Join(sideCamArgs("rtsp://u:p@cam/1", "D"), " ")
	for _, want := range []string{"-rtsp_transport tcp", "-i rtsp://u:p@cam/1", "-map 0:v:0", "-c copy", "-f segment",
		"-segment_wrap 20", "-t 1800", filepath.Join("D", "s%02d.ts")} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
}

func TestSideOnOnlyWithCameraTwo(t *testing.T) {
	if sideOnEnabled(M{"replay_side_on": true}) || sideOnEnabled(M{"camera2_rtsp_url": "rtsp://x"}) ||
		!sideOnEnabled(M{"replay_side_on": true, "camera2_rtsp_url": "rtsp://x"}) {
		t.Fatal("sideOnEnabled")
	}
}

// The real thing, with FFmpeg: the ring written as the recorder writes it (a test
// pattern standing in for camera 2), then the last seconds cut into a clip.
func TestSideOnClipFromARealRing(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	sandbox(t, "")
	UpdateState(func(s M) { s["replay_folder"] = t.TempDir() })
	dir := sideCamDir()
	os.MkdirAll(dir, 0o755)
	args := sideCamArgs("rtsp://camera", dir)
	// The recorder's own output side; a live test pattern in place of the camera.
	i := indexOf(args, "-rtsp_transport")
	j := indexOf(args, "-map")
	args = append(append(append([]string{}, args[:i]...), "-re", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-c:v", "libx264", "-g", "25", "-preset", "ultrafast"), args[j+2:]...)
	args = removePair(args, "-c", "copy")
	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(9 * time.Second)
	clip, why := SideOnClip(LoadState(), 4)
	cmd.Process.Kill()
	cmd.Wait()
	if why != "" {
		t.Fatal(why)
	}
	d := clipDuration(clip)
	if d < 3 || d > 8 {
		t.Fatalf("clip of %.1fs from a 4s window", d)
	}
	if !strings.HasPrefix(clip, filepath.Join(gs(LoadState(), "replay_folder", ""), "side-on")) {
		t.Fatalf("clip landed among the replays: %s", clip)
	}
	if len(listClips(gs(LoadState(), "replay_folder", ""))) != 0 {
		t.Fatal("a side-on clip would count as a replay")
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

func removePair(a []string, k, v string) []string {
	for i := 0; i+1 < len(a); i++ {
		if a[i] == k && a[i+1] == v {
			return append(append([]string{}, a[:i]...), a[i+2:]...)
		}
	}
	return a
}

func TestSideOnClipWithoutARecording(t *testing.T) {
	sandbox(t, "")
	if clip, why := SideOnClip(LoadState(), 25); clip != "" || !strings.Contains(why, "isn't being recorded") {
		t.Fatal(clip, why)
	}
}

// A wicket replays the main clip, then the side-on one in the same Replay scene,
// captioned; a boundary doesn't.
func TestWicketReplayPlaysSideOnToo(t *testing.T) {
	srv, folder := replayRig(t, true, true)
	UpdateState(func(s M) { s["replay_side_on"], s["camera2_rtsp_url"] = true, "rtsp://cam2" })
	side := filepath.Join(folder, "side-on", "side_x.mp4")
	saved := sideOnClipFor
	sideOnClipFor = func(M, float64) (string, string) { return side, "" }
	t.Cleanup(func() { sideOnClipFor = saved })
	popCommands()

	ObsTriggerReplay(LoadState(), "Wicket")
	types := strings.Join(srv.Types(), ",")
	if !strings.Contains(types, "SetInputSettings,SetCurrentProgramScene,SetInputSettings,TriggerMediaInputAction,SetCurrentProgramScene") {
		t.Fatalf("sequence: %s", types)
	}
	replayShownMu.Lock()
	shown := replayShown
	replayShownMu.Unlock()
	mustEq(t, shown, any(side))
	if c := gs(popCommands(), "replay_caption", ""); !strings.HasPrefix(c, "Side-on") {
		t.Fatalf("caption %q", c)
	}

	srv2, _ := replayRig(t, true, true)
	UpdateState(func(s M) { s["replay_side_on"], s["camera2_rtsp_url"] = true, "rtsp://cam2" })
	ObsTriggerReplay(LoadState(), "Six")
	if n := strings.Count(strings.Join(srv2.Types(), ","), "SetInputSettings"); n != 1 {
		t.Fatalf("a boundary replayed %d clips", n)
	}
}

// A stand-in recorder for the loop test: this test binary, sleeping.
func TestSideCamSleeperProcess(t *testing.T) {
	if os.Getenv("SIDECAM_SLEEPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(time.Minute)
}

// The loop records while side-on replays are on, and stops the recorder when they're
// switched off.
func TestSideCamLoopStartsAndStops(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"replay_side_on": true, "camera2_rtsp_url": "rtsp://cam2"})
	savedStart, savedFound, savedStop := startSideCam, ffmpegFound, sideStop
	var mu sync.Mutex
	var got []string
	startSideCam = func(args []string) (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSideCamSleeperProcess$")
		cmd.Env = append(os.Environ(), "SIDECAM_SLEEPER=1")
		mu.Lock()
		got = args
		mu.Unlock()
		return cmd, cmd.Start()
	}
	ffmpegFound = func() bool { return true }
	sideStop = make(chan struct{})
	done := make(chan struct{})
	go func() { sideCamLoop(); close(done) }()
	t.Cleanup(func() {
		StopSideCam()
		<-done
		startSideCam, ffmpegFound, sideStop = savedStart, savedFound, savedStop
	})
	waitFor(t, "recording", func() bool { return gb(SideCamStatus(), "running", false) })
	mu.Lock()
	if !strings.Contains(strings.Join(got, " "), "rtsp://cam2") {
		t.Fatalf("args %v", got)
	}
	mu.Unlock()
	UpdateState(func(s M) { s["replay_side_on"] = false })
	for i := 0; i < 100 && gb(SideCamStatus(), "running", false); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	// "running" goes false only once the loop has killed the recorder and waited for it.
	if gb(SideCamStatus(), "running", false) {
		t.Fatalf("still recording after being switched off: %v", SideCamStatus())
	}
}
