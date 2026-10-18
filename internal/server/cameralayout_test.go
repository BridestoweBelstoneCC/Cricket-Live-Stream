package server

// Camera size and stacking in OBS scenes (tests/test_camera_layout.py). OBS drops a newly
// added source at its native size in the top-left corner, ON TOP of everything already
// in the scene: verified on OBS 32.1.1, a 720p camera filled a quarter of the canvas,
// covered the Overlay in Main and covered ReplayClip in Replay. fakeScenes follows OBS's
// own rules — index 0 is the bottom, CreateSceneItem adds on top, SetSceneItemIndex
// moves an item to that position — behind the fake OBS WebSocket server.

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
	"cricketstream/internal/obsws/obswstest"
)

const canvasW, canvasH = 1920, 1080

func defaultTR() M {
	return M{"boundsType": "OBS_BOUNDS_NONE", "positionX": 0.0, "positionY": 0.0, "scaleX": 1.0, "scaleY": 1.0}
}

// kindOf: cameras and the replay clip are video sources; the Overlay is a browser
// source; anything called Logo is an image (a sponsor's, say).
func kindOf(src string) string {
	switch {
	case src == "Overlay":
		return "browser_source"
	case strings.HasPrefix(src, "Logo"):
		return "image_source"
	}
	return "ffmpeg_source"
}

type sceneItem struct {
	src string
	id  int
	tr  M
}

type fakeScenes struct {
	mu     sync.Mutex
	scenes map[string][]*sceneItem // bottom first
	nextID int
}

func newScenes(scenes map[string][]string) *fakeScenes {
	f := &fakeScenes{scenes: map[string][]*sceneItem{}, nextID: 100}
	for s, items := range scenes {
		f.scenes[s] = []*sceneItem{}
		for n, src := range items {
			f.scenes[s] = append(f.scenes[s], &sceneItem{src, n + 1, defaultTR()})
		}
	}
	return f
}

func (f *fakeScenes) add(scene, src string, id int) {
	f.mu.Lock()
	f.scenes[scene] = append(f.scenes[scene], &sceneItem{src, id, defaultTR()})
	f.mu.Unlock()
}

// order is top first, like OBS's sources list.
func (f *fakeScenes) order(scene string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	sc := f.scenes[scene]
	for i := len(sc) - 1; i >= 0; i-- {
		out = append(out, sc[i].src)
	}
	return out
}

func (f *fakeScenes) tr(scene, src string) M {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.scenes[scene] {
		if it.src == src {
			return it.tr
		}
	}
	return nil
}

func (f *fakeScenes) handle(rt string, rd map[string]any) (bool, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	scene := gs(rd, "sceneName", "")
	sc := f.scenes[scene]
	find := func() (int, *sceneItem) {
		id := gi(rd, "sceneItemId", -1)
		for i, it := range sc {
			if it.id == id {
				return i, it
			}
		}
		return -1, nil
	}
	switch rt {
	case "GetSceneItemList":
		items := []any{}
		for k, it := range sc {
			items = append(items, M{"sourceName": it.src, "sceneItemId": it.id, "sceneItemIndex": k, "inputKind": kindOf(it.src)})
		}
		return true, M{"sceneItems": items}
	case "CreateSceneItem":
		f.nextID++
		f.scenes[scene] = append(sc, &sceneItem{gs(rd, "sourceName", ""), f.nextID, defaultTR()})
		return true, M{}
	case "GetSceneItemTransform":
		_, it := find()
		return it != nil, M{"sceneItemTransform": shallow(it.tr)}
	case "SetSceneItemTransform":
		_, it := find()
		for k, v := range gm(rd, "sceneItemTransform") {
			it.tr[k] = v
		}
		return true, M{}
	case "SetSceneItemIndex":
		i, it := find()
		sc = append(sc[:i], sc[i+1:]...)
		to := gi(rd, "sceneItemIndex", 0)
		sc = append(sc[:to], append([]*sceneItem{it}, sc[to:]...)...)
		f.scenes[scene] = sc
		return true, M{}
	}
	return false, nil
}

func dialScenes(t *testing.T, f *fakeScenes) *obsws.Conn {
	t.Helper()
	srv := obswstest.New("", f.handle)
	t.Cleanup(srv.Close)
	c, err := obsws.Dial(srv.Target(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func arrange(t *testing.T, f *fakeScenes, scene, cam string, home, fresh bool, overlay ...bool) {
	t.Helper()
	ov := len(overlay) == 0 || overlay[0]
	arrangeCameraItem(dialScenes(t, f), scene, cam, canvasW, canvasH, home, fresh, ov)
}

func TestLayoutNewCameraGoesUnderGraphicsInMain(t *testing.T) {
	f := newScenes(map[string][]string{"Main": {"Overlay", "Cam"}}) // OBS put Cam on top
	arrange(t, f, "Main", "Cam", true, true)
	eqv(t, f.order("Main"), []string{"Overlay", "Cam"})
}

func TestLayoutNewCameraGoesUnderReplayClip(t *testing.T) {
	f := newScenes(map[string][]string{"Replay": {"ReplayClip", "Cam"}})
	arrange(t, f, "Replay", "Cam", false, true)
	eqv(t, f.order("Replay"), []string{"ReplayClip", "Cam"})
}

func TestLayoutNewCameraFillsCanvas(t *testing.T) {
	f := newScenes(map[string][]string{"Main": {"Overlay", "Cam"}})
	arrange(t, f, "Main", "Cam", true, true)
	tr := f.tr("Main", "Cam")
	eqv(t, tr["boundsType"], "OBS_BOUNDS_SCALE_INNER")
	eqv(t, []any{tr["boundsWidth"], tr["boundsHeight"]}, []any{canvasW, canvasH})
}

func TestLayoutUntouchedExistingCameraIsFitted(t *testing.T) {
	// Added by the old code: still at native size in the corner.
	f := newScenes(map[string][]string{"Main": {"Cam", "Overlay"}})
	arrange(t, f, "Main", "Cam", true, false)
	eqv(t, f.tr("Main", "Cam")["boundsType"], "OBS_BOUNDS_SCALE_INNER")
}

func TestLayoutHandPlacedCameraLeftWhereItIs(t *testing.T) {
	// A deliberate picture-in-picture, say. Not ours to undo.
	f := newScenes(map[string][]string{"Main": {"Cam", "Overlay"}})
	tr := f.tr("Main", "Cam")
	tr["positionX"], tr["positionY"], tr["scaleX"], tr["scaleY"] = 1400.0, 50.0, 0.25, 0.25
	arrange(t, f, "Main", "Cam", true, false)
	tr = f.tr("Main", "Cam")
	eqv(t, []any{tr["positionX"], tr["scaleX"], tr["boundsType"]}, []any{1400, 0.25, "OBS_BOUNDS_NONE"})
}

func TestLayoutExistingCameraCoveringGraphicsMovedUnder(t *testing.T) {
	f := newScenes(map[string][]string{"Main": {"Overlay", "Cam"}})
	arrange(t, f, "Main", "Cam", true, false)
	eqv(t, f.order("Main"), []string{"Overlay", "Cam"})
}

func TestLayoutCorrectExistingStackNotTouched(t *testing.T) {
	f := newScenes(map[string][]string{"Main": {"Other", "Cam", "Overlay"}})
	arrange(t, f, "Main", "Cam", true, false)
	eqv(t, f.order("Main"), []string{"Overlay", "Cam", "Other"})
}

func TestLayoutSponsorLogoAboveScorebarStaysThere(t *testing.T) {
	// The maintainer's real install: logos above the Overlay on purpose. Adding a camera
	// must slot it under the Overlay, not lift the Overlay over them.
	f := newScenes(map[string][]string{"Main": {"Display", "Overlay", "Logo1", "Logo2", "Cam"}})
	arrange(t, f, "Main", "Cam", true, true)
	eqv(t, f.order("Main"), []string{"Logo2", "Logo1", "Overlay", "Cam", "Display"})
}

func TestLayoutBowlerSceneGetsTheOverlay(t *testing.T) {
	// Created with cameras only, so the over-boundary auto-cut hid the scorebar.
	f := newScenes(map[string][]string{"Main-Bowler": {"Cam2"}})
	arrange(t, f, "Main-Bowler", "Cam2", true, true)
	eqv(t, f.order("Main-Bowler"), []string{"Overlay", "Cam2"})
}

func TestLayoutNoOverlaySourceYetAddsNothing(t *testing.T) {
	f := newScenes(map[string][]string{"Main-Bowler": {"Cam2"}})
	arrange(t, f, "Main-Bowler", "Cam2", true, true, false)
	eqv(t, f.order("Main-Bowler"), []string{"Cam2"})
}

func TestLayoutTwoCamerasEachSceneShowsItsOwn(t *testing.T) {
	// The real-OBS sequence: camera 2 added (home Main-Bowler, also in Main), then camera
	// 1 re-added (home Main, also in Main-Bowler). Matches what OBS showed.
	f := newScenes(map[string][]string{"Main": {"Cam1", "Overlay"}, "Main-Bowler": {}, "Replay": {"Cam1", "ReplayClip"}})
	for _, s := range []struct {
		scene string
		home  bool
	}{{"Main-Bowler", true}, {"Replay", false}, {"Main", false}} {
		f.add(s.scene, "Cam2", 50+len(s.scene))
		arrange(t, f, s.scene, "Cam2", s.home, true)
	}
	f.add("Main-Bowler", "Cam1", 90)
	arrange(t, f, "Main-Bowler", "Cam1", false, true)
	arrange(t, f, "Main", "Cam1", true, false)
	arrange(t, f, "Replay", "Cam1", false, false)
	eqv(t, f.order("Main"), []string{"Overlay", "Cam1", "Cam2"})
	eqv(t, f.order("Main-Bowler"), []string{"Overlay", "Cam2", "Cam1"})
	mustEq(t, f.order("Replay")[0], "ReplayClip")
}

// ── obs.CoveringPictures + the move obs-setup makes every match day: each covering
// camera is moved DOWN to just beneath the graphic ──

func repair(t *testing.T, names []string, source string) []string {
	t.Helper()
	f := newScenes(map[string][]string{"S": names})
	for range names {
		_, d := f.handle("GetSceneItemList", M{"sceneName": "S"})
		var items []map[string]any
		for _, it := range gl(d, "sceneItems") {
			items = append(items, it.(M))
		}
		over := obs.CoveringPictures(items, source)
		if len(over) == 0 {
			break
		}
		mine := 0
		for _, it := range items {
			if it["sourceName"] == source {
				mine = gi(it, "sceneItemIndex", 0)
			}
		}
		f.handle("SetSceneItemIndex", M{"sceneName": "S", "sceneItemId": over[0]["sceneItemId"], "sceneItemIndex": mine})
	}
	return f.order("S")
}

func TestCoveringCameraOverTheOverlay(t *testing.T) {
	eqv(t, repair(t, []string{"Overlay", "Cam"}, "Overlay"), []string{"Overlay", "Cam"})
}

func TestCoveringCameraDraggedOverEverythingLeavesLogosAlone(t *testing.T) {
	// The real-OBS case: lifting the Overlay instead put it above the logo too.
	eqv(t, repair(t, []string{"Display", "Overlay", "Logo1", "Cam"}, "Overlay"), []string{"Logo1", "Overlay", "Cam", "Display"})
}

func TestCoveringAlreadyRightIsUntouched(t *testing.T) {
	eqv(t, repair(t, []string{"Display", "Cam", "Overlay", "Logo1", "Logo2"}, "Overlay"),
		[]string{"Logo2", "Logo1", "Overlay", "Cam", "Display"})
}

func TestCoveringTwoCamerasOverTheReplayClip(t *testing.T) {
	eqv(t, repair(t, []string{"ReplayClip", "Cam1", "Logo1", "Cam2"}, "ReplayClip"), []string{"Logo1", "ReplayClip", "Cam2", "Cam1"})
}

func TestScreenCaptureCountsAsAPicture(t *testing.T) {
	for kind, want := range map[string]bool{"monitor_capture": true, "av_capture_input_v2": true,
		"image_source": false, "text_gdiplus_v3": false} {
		mustEq(t, obs.IsPictureKind(kind), want, kind)
	}
}

// obs-setup talks to a real WebSocket, so these are source checks; the behaviour was
// confirmed on OBS 32.1.1 (a camera put back on top was moved under the Overlay).
func obsSetupSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../obs/setup.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOBSSetupPutsGraphicsBackAboveCameraEveryRun(t *testing.T) {
	src := obsSetupSource(t)
	for _, want := range []string{`{"Main", "Overlay"}, {"Replay", "ReplayClip"}`, "CoveringPictures(lst, source)"} {
		if !strings.Contains(src, want) {
			t.Fatal(want)
		}
	}
}

func TestOBSSetupFitsReplayClipToCanvas(t *testing.T) {
	// Replays are recorded at the OUTPUT resolution (720p by default).
	src := obsSetupSource(t)
	i := strings.Index(src, `if source == "ReplayClip" {`)
	if i < 0 {
		t.Fatal("ReplayClip fit not found")
	}
	block := src[i : i+1000]
	if !strings.Contains(block, "UntouchedPlacement(tr)") || !strings.Contains(block, "FillCanvasTransform(") {
		t.Fatal("ReplayClip not fitted to the canvas")
	}
}
