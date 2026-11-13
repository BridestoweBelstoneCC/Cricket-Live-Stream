package server

// Sound and picture while on air (the code review's F4). The camera spotter looks at the
// picture every few minutes, and only with an AI key; nothing listened at all. A dead
// ground microphone or a camera that had dropped off the network went out to YouTube
// until a viewer said so.
//
// Every avWatchEvery, while OBS is streaming on this laptop:
//   - sound: OBS's own level meters for a second (obsws.Meters). Silence only counts once
//     there HAS been sound this match day: a club that streams with no microphone is never
//     told about it, twice a minute, all afternoon. A muted input is silent, whatever its
//     meter says: the meter is before the mute.
//   - picture: a small screenshot of each camera source. A live camera's picture is never
//     the same twice (sensor noise alone changes the JPEG); the same bytes for
//     avFrozenAfter means OBS is showing the last frame of a camera that's gone.
//
// It reports and does nothing else: /health's "av" block, which the panel's strip, the
// clubhouse screen's Health tab and the window's alerts (and so Discord) read. Reloading a
// camera is the operator's call (and refresh-cam's).

import (
	"crypto/sha256"
	"sync"
	"time"

	"cricketstream/internal/obsws"
)

const (
	avWatchEvery   = 15 * time.Second
	avSilentAfter  = 45.0  // seconds with no sound before it's said
	avFrozenAfter  = 45.0  // seconds with the same picture before it's said
	avSilenceLevel = 0.001 // -60 dB: below a quiet ground's own hiss
)

type avCamera struct {
	last    [32]byte // the last screenshot's hash
	have    bool
	since   float64 // when the picture last changed
	moved   bool    // it has been seen changing (a source that never has isn't a camera)
	checked float64
}

var (
	avMu         sync.Mutex
	avWatching   bool
	avHeard      bool    // there has been sound this match day
	avQuietSince float64 // when it went quiet (0 while there's sound)
	avSoundKnown bool    // the last look at the meters worked
	avCams       = map[string]*avCamera{}
	avLastRun    float64
	avSaidSilent bool
	avSaidFrozen = map[string]bool{}
)

// avNow is the clock, swappable so a test needn't wait 45 seconds.
var avNow = now

// obsSoundPeak is the loudest unmuted audio input's peak over about a second, and whether
// OBS could be asked. Swappable.
var obsSoundPeak = func(st M) (float64, bool) {
	peaks, err := obsws.Meters(obsTarget(st), time.Second, 4*time.Second)
	if err != nil {
		return 0, false
	}
	var names []string
	var reqs []obsws.Req
	for name := range peaks {
		names = append(names, name)
		reqs = append(reqs, obsws.Req{Type: "GetInputMute", Data: M{"inputName": name}})
	}
	var muted []map[string]any
	if len(reqs) > 0 {
		muted = obsCall(st, reqs, 4*time.Second)
	}
	loudest := 0.0
	for i, name := range names {
		if i < len(muted) && muted[i] != nil && muted[i]["inputMuted"] == true {
			continue
		}
		if p := peaks[name]; p > loudest {
			loudest = p
		}
	}
	return loudest, true
}

// obsSmallFrame is a camera source's picture, small (it's only compared with the last
// one), or nil. Swappable.
var obsSmallFrame = func(st M, source string) []byte {
	r := obsCall(st, []obsws.Req{{Type: "GetSourceScreenshot", Data: M{"sourceName": source, "imageFormat": "jpg",
		"imageWidth": 320, "imageCompressionQuality": 60}}}, 6*time.Second)
	if len(r) == 0 || r[0] == nil {
		return nil
	}
	return []byte(gs(r[0], "imageData", ""))
}

// avStreaming: OBS is on air, as the stream monitor last saw it. Swappable.
var avStreaming = func() bool {
	streamMonMu.Lock()
	defer streamMonMu.Unlock()
	return streamMon["streaming"] == true
}

func avWatchTick() {
	defer func() { recover() }() // the match-day loop never dies of this
	st := LoadState()
	t := avNow()
	if !gb(st, "stream_av_watch", true) || !obsIsLocal(st) || !avStreaming() {
		avMu.Lock()
		// Off air: nothing's wrong, and the next stream starts with a clean slate (but it
		// has still heard sound today).
		avWatching, avQuietSince, avSoundKnown = false, 0, false
		avCams = map[string]*avCamera{}
		avSaidSilent, avSaidFrozen = false, map[string]bool{}
		avMu.Unlock()
		return
	}
	peak, soundOK := obsSoundPeak(st)
	frames := map[string][]byte{}
	cams := spotterCameras(st)
	for _, c := range cams {
		frames[c[0]] = obsSmallFrame(st, c[1])
	}

	avMu.Lock()
	defer avMu.Unlock()
	avWatching, avLastRun, avSoundKnown = true, t, soundOK
	if soundOK {
		switch {
		case peak >= avSilenceLevel:
			if avSaidSilent {
				say("  ✓  Sound: there's sound on the stream again")
			}
			avHeard, avQuietSince, avSaidSilent = true, 0, false
		case avQuietSince == 0:
			avQuietSince = t
		case avHeard && !avSaidSilent && t-avQuietSince >= avSilentAfter:
			avSaidSilent = true
			say("  ⚠  Sound: nothing on the stream for %ds — check the ground microphone, and that nothing in OBS is muted", int(t-avQuietSince))
		}
	}
	for _, c := range cams {
		name, frame := c[0], frames[c[0]]
		cam := avCams[name]
		if cam == nil {
			cam = &avCamera{since: t}
			avCams[name] = cam
		}
		if len(frame) == 0 {
			continue // OBS wouldn't give one (a source not in the scene): not known, not frozen
		}
		cam.checked = t
		sum := sha256.Sum256(frame)
		switch {
		case !cam.have:
			cam.have, cam.last, cam.since = true, sum, t
		case sum != cam.last:
			if avSaidFrozen[name] {
				say("  ✓  Picture: %s is moving again", name)
			}
			cam.last, cam.since, cam.moved = sum, t, true
			avSaidFrozen[name] = false
		case cam.moved && !avSaidFrozen[name] && t-cam.since >= avFrozenAfter:
			avSaidFrozen[name] = true
			say("  ⚠  Picture: %s hasn't changed for %ds — the camera may have dropped out (Cameras → reload it)", name, int(t-cam.since))
		}
	}
}

// avWatchStatus is /health's "av" block.
func avWatchStatus() M {
	avMu.Lock()
	defer avMu.Unlock()
	t := avNow()
	out := M{"watching": avWatching, "sound": "unknown", "silent_for_sec": 0, "cameras": []any{}}
	if !avWatching {
		return out
	}
	switch {
	case !avSoundKnown:
	case avQuietSince == 0:
		out["sound"] = "ok"
	case !avHeard:
		out["sound"] = "none yet"
	case t-avQuietSince >= avSilentAfter:
		out["sound"], out["silent_for_sec"] = "silent", int(t-avQuietSince)
	default:
		out["sound"] = "ok" // a pause, not yet a silence
	}
	cams := []any{}
	for _, c := range spotterCameras(LoadState()) {
		cam := avCams[c[0]]
		if cam == nil || !cam.have {
			continue
		}
		frozen := cam.moved && t-cam.since >= avFrozenAfter
		rec := M{"name": c[0], "frozen": frozen, "frozen_for_sec": 0}
		if frozen {
			rec["frozen_for_sec"] = int(t - cam.since)
		}
		cams = append(cams, rec)
	}
	out["cameras"] = cams
	return out
}

func StartAVWatch() { loop("Sound and picture watch", avWatchEvery, avWatchTick) }
