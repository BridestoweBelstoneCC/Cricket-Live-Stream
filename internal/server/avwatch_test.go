package server

// Sound and picture while on air (avwatch.go): silence is only news after there's been
// sound, a frozen camera only after it's been seen moving, and nothing's said off air.

import (
	"fmt"
	"strings"
	"testing"
)

type avRig struct {
	t         *testing.T
	clock     float64
	peak      float64
	soundOK   bool
	streaming bool
	frame     map[string]int // camera source -> a counter: the same number is the same picture
	said      *strings.Builder
}

func newAVRig(t *testing.T, state M) *avRig {
	t.Helper()
	newTestServer(t, "")
	st := M{"obs_camera_name": "Cricket Camera", "stream_av_watch": true}
	for k, v := range state {
		st[k] = v
	}
	setState(t, st)
	r := &avRig{t: t, clock: 1000, soundOK: true, streaming: true, frame: map[string]int{}, said: &strings.Builder{}}
	sNow, sPeak, sFrame, sStream, sOut := avNow, obsSoundPeak, obsSmallFrame, avStreaming, sayOut
	avNow = func() float64 { return r.clock }
	obsSoundPeak = func(M) (float64, bool) { return r.peak, r.soundOK }
	obsSmallFrame = func(_ M, source string) []byte {
		n, ok := r.frame[source]
		if !ok {
			return nil
		}
		return []byte(fmt.Sprintf("picture %s %d", source, n))
	}
	avStreaming = func() bool { return r.streaming }
	sayOut = r.said
	reset := func() {
		avMu.Lock()
		avWatching, avHeard, avQuietSince, avSoundKnown = false, false, 0, false
		avCams, avSaidSilent, avSaidFrozen = map[string]*avCamera{}, false, map[string]bool{}
		avMu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		avNow, obsSoundPeak, obsSmallFrame, avStreaming, sayOut = sNow, sPeak, sFrame, sStream, sOut
		reset()
	})
	return r
}

// tick moves the clock on 15 seconds and looks again; moving says whether the cameras'
// pictures changed since the last look.
func (r *avRig) tick(moving bool) M {
	r.clock += 15
	if moving {
		for k := range r.frame {
			r.frame[k]++
		}
	}
	avWatchTick()
	return avWatchStatus()
}

func TestSilenceIsOnlyNewsAfterThereHasBeenSound(t *testing.T) {
	r := newAVRig(t, nil)
	r.frame["Cricket Camera"] = 0
	// A club with no microphone: quiet from the start, for minutes. Never an alarm.
	var st M
	for i := 0; i < 10; i++ {
		st = r.tick(true)
	}
	mustEq(t, st["sound"], any("none yet"))
	if strings.Contains(r.said.String(), "Sound") {
		t.Fatalf("a stream that never had sound was warned about: %s", r.said)
	}
	// Sound arrives, then stops.
	r.peak = 0.2
	mustEq(t, r.tick(true)["sound"], any("ok"))
	r.peak = 0.0004                             // hiss: below -60 dB
	mustEq(t, r.tick(true)["sound"], any("ok")) // 0s quiet
	mustEq(t, r.tick(true)["sound"], any("ok")) // 15s: a pause between overs
	mustEq(t, r.tick(true)["sound"], any("ok")) // 30s
	st = r.tick(true)                           // 45s
	mustEq(t, st["sound"], any("silent"))
	mustEq(t, st["silent_for_sec"], any(45))
	if n := strings.Count(r.said.String(), "nothing on the stream"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, r.said)
	}
	r.tick(true)
	r.tick(true)
	if n := strings.Count(r.said.String(), "nothing on the stream"); n != 1 {
		t.Errorf("the same silence was said %d times", n)
	}
	// Back.
	r.peak = 0.05
	mustEq(t, r.tick(true)["sound"], any("ok"))
	if !strings.Contains(r.said.String(), "there's sound on the stream again") {
		t.Error("the sound coming back wasn't said")
	}
}

func TestSoundThatCannotBeReadIsNotSilence(t *testing.T) {
	r := newAVRig(t, nil)
	r.peak = 0.2
	r.tick(true)
	r.soundOK, r.peak = false, 0 // OBS busy, or didn't answer
	var st M
	for i := 0; i < 6; i++ {
		st = r.tick(true)
	}
	mustEq(t, st["sound"], any("unknown"))
	if strings.Contains(r.said.String(), "nothing on the stream") {
		t.Error("not being able to ask OBS was reported as silence")
	}
}

func TestAFrozenCameraIsSaidOnceItHasBeenSeenMoving(t *testing.T) {
	r := newAVRig(t, M{"camera2_rtsp_url": "rtsp://x", "obs_camera2_name": "Bowler End Camera"})
	r.peak = 0.2
	r.frame["Cricket Camera"], r.frame["Bowler End Camera"] = 0, 0
	r.tick(true)
	r.tick(true) // both seen moving
	// Camera 2 drops out: OBS holds its last frame. The wide camera carries on.
	freeze := func() M {
		r.clock += 15
		r.frame["Cricket Camera"]++
		avWatchTick()
		return avWatchStatus()
	}
	var st M
	for i := 0; i < 3; i++ {
		st = freeze()
	}
	cams := st["cameras"].([]any)
	mustEq(t, len(cams), 2)
	wide, two := cams[0].(M), cams[1].(M)
	mustEq(t, wide["frozen"], any(false))
	mustEq(t, two["name"], any("Camera 2"))
	mustEq(t, two["frozen"], any(true))
	mustEq(t, two["frozen_for_sec"], any(45))
	if n := strings.Count(r.said.String(), "Camera 2 hasn't changed"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, r.said)
	}
	if strings.Contains(r.said.String(), "Wide camera hasn't changed") {
		t.Error("the camera that's fine was reported")
	}
	freeze()
	if n := strings.Count(r.said.String(), "Camera 2 hasn't changed"); n != 1 {
		t.Error("the same freeze was said again")
	}
	st = r.tick(true) // it comes back
	mustEq(t, st["cameras"].([]any)[1].(M)["frozen"], any(false))
	if !strings.Contains(r.said.String(), "Camera 2 is moving again") {
		t.Error("the picture coming back wasn't said")
	}
}

// A source that has never changed isn't a live camera (a still, a card): never "frozen".
// And one OBS won't give a picture of isn't known either way.
func TestAPictureThatNeverMovedIsNotAFrozenCamera(t *testing.T) {
	r := newAVRig(t, nil)
	r.frame["Cricket Camera"] = 0
	var st M
	for i := 0; i < 8; i++ {
		st = r.tick(false)
	}
	mustEq(t, st["cameras"].([]any)[0].(M)["frozen"], any(false))
	delete(r.frame, "Cricket Camera") // no screenshot at all
	for i := 0; i < 8; i++ {
		r.tick(false)
	}
	if strings.Contains(r.said.String(), "hasn't changed") {
		t.Errorf("reported: %s", r.said)
	}
}

func TestNothingIsWatchedOffAir(t *testing.T) {
	r := newAVRig(t, nil)
	r.frame["Cricket Camera"] = 0
	r.peak = 0.2
	r.tick(true)
	r.peak = 0
	for i := 0; i < 5; i++ {
		r.tick(false)
	}
	mustEq(t, avWatchStatus()["sound"], any("silent"))
	// The stream ends: nothing's wrong any more, and nothing is asked of OBS.
	r.streaming = false
	asked := 0
	obsSoundPeak = func(M) (float64, bool) { asked++; return 0, true }
	st := r.tick(false)
	mustEq(t, st["watching"], any(false))
	mustEq(t, st["sound"], any("unknown"))
	mustEq(t, len(st["cameras"].([]any)), 0)
	mustEq(t, asked, 0)
	// Switched off in the panel, or OBS on another computer: the same.
	r.streaming = true
	setState(t, M{"stream_av_watch": false})
	mustEq(t, r.tick(false)["watching"], any(false))
	setState(t, M{"stream_av_watch": true, "obs_host": "192.168.1.20"})
	mustEq(t, r.tick(false)["watching"], any(false))
	mustEq(t, asked, 0)
	// It's in /health, where the panel, the clubhouse screen and the window read it.
	w := serveAs("GET", "/health", "127.0.0.1:5555", "127.0.0.1:5000", nil, "")
	if _, ok := jsonObject(w.Body.Bytes())["av"].(M); w.Code != 200 || !ok {
		t.Errorf("/health (%d) has no av block", w.Code)
	}
}
