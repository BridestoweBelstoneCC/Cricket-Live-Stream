package server

// Adaptive stream quality: two tiers of defence against a struggling upload.
//  1. OBS's own Dynamic Bitrate (enabled by OBS setup) nudges the encoder bitrate up and
//     down seamlessly — no disconnect, no operator action.
//  2. This sentinel watches the stream's congestion + dropped-frame counters while live.
//     On sustained trouble it can step the configured bitrate DOWN a ladder — from the
//     panel, or automatically when the operator enables auto mode. A step means stop →
//     reconfigure → start (~5-10s ingest gap), so it's rate-limited and never shifts UP
//     on its own.

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

const (
	StreamPollSec        = 15
	StreamWindowSec      = 60   // judge over the last minute, not one bad sample
	StreamCongestionTrip = 0.15 // avg congestion (0..1) that counts as "struggling"
	StreamDroppedPctTrip = 4.0  // % frames dropped across the window
	StreamShiftCooldown  = 150  // seconds between automatic downshifts
)

// StreamLadder is fractions of the baseline bitrate.
var StreamLadder = []float64{1.0, 0.7, 0.5, 0.35}

// StreamSample is one reading of OBS's stream output.
type StreamSample struct {
	T              float64
	Congestion     float64
	Dropped, Total int
}

var (
	streamMon = M{
		"enabled_thread": false, "streaming": false, "reachable": nil,
		"baseline_kbps": nil, // VBitrate observed at step 0 while live
		"step":          0,   // index into StreamLadder
		"current_kbps":  nil, "last_shift_at": 0.0, "shifts": []any{}, "last_reason": "",
		"dynamic_bitrate": nil, // OBS's own Dynamic Bitrate setting (nil = not yet read)
		"simple_mode":     nil, // the ladder needs Simple output mode
		// VBitrate as OBS is CURRENTLY configured, read every tick whatever the live/step
		// state — unlike baseline_kbps, which only latches while live. The pre-flight
		// bitrate check needs it to catch a leftover downshift BEFORE going live.
		"configured_kbps": nil,
	}
	streamSamples []StreamSample
	streamMonMu   sync.Mutex
	qualityShift  sync.Mutex
)

func pct0(x float64) string { return pyfmt.Fixed(x*100, 0) + "%" }

// EvaluateStreamSamples is the pure decision: "downshift" or "hold", and why. It needs
// sustained evidence across the window, and respects the cooldown so a shift's own
// restart blip can't trigger the next.
func EvaluateStreamSamples(samples []StreamSample, now, lastShift float64) (string, string) {
	var recent []StreamSample
	for _, s := range samples {
		if now-s.T <= StreamWindowSec {
			recent = append(recent, s)
		}
	}
	if len(recent) < 3 {
		return "hold", "not enough samples yet"
	}
	if now-lastShift < StreamShiftCooldown {
		return "hold", "cooling down after last quality change"
	}
	sum := 0.0
	for _, s := range recent {
		sum += s.Congestion
	}
	avg := sum / float64(len(recent))
	dDropped := recent[len(recent)-1].Dropped - recent[0].Dropped
	dTotal := recent[len(recent)-1].Total - recent[0].Total
	dropped := 0.0
	if dTotal > 0 {
		dropped = 100 * float64(dDropped) / float64(dTotal)
	}
	if avg >= StreamCongestionTrip {
		return "downshift", fmt.Sprintf("sustained congestion (avg %s over %ds)", pct0(avg), StreamWindowSec)
	}
	if dropped >= StreamDroppedPctTrip {
		return "downshift", fmt.Sprintf("dropping frames (%s%% over %ds)", pyfmt.Fixed(dropped, 1), StreamWindowSec)
	}
	return "hold", fmt.Sprintf("healthy (congestion %s, drops %s%%)", pct0(avg), pyfmt.Fixed(dropped, 1))
}

func streamLadderKbps(baseline, step int) int {
	step = max(0, min(step, len(StreamLadder)-1))
	return max(500, int(float64(baseline)*StreamLadder[step]))
}

var shiftSleep = time.Sleep

// ApplyStreamQualityStep: stop → wait → set the SimpleOutput bitrate → start, verified
// at each step. Serialised: a panel press and the auto path must never interleave two
// stop/start cycles.
func ApplyStreamQualityStep(newStep int, reason, mode string) (bool, string) {
	if !qualityShift.TryLock() {
		return false, "A quality change is already in progress — wait for it to finish"
	}
	defer qualityShift.Unlock()
	return applyQualityStepLocked(newStep, reason, mode)
}

func recordStep(step, target int) {
	streamMonMu.Lock()
	streamMon["step"], streamMon["current_kbps"], streamMon["last_shift_at"] = step, target, now()
	streamSamples = nil
	streamMonMu.Unlock()
}

func applyQualityStepLocked(newStep int, reason, mode string) (bool, string) {
	st := LoadState()
	streamMonMu.Lock()
	baseline := gi(streamMon, "baseline_kbps", 0)
	streamMonMu.Unlock()
	if baseline == 0 {
		return false, "No baseline bitrate recorded yet — is the stream live?"
	}
	newStep = max(0, min(newStep, len(StreamLadder)-1))
	target := streamLadderKbps(baseline, newStep)
	status := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"},
		{Type: "GetProfileParameter", Data: M{"parameterCategory": "Output", "parameterName": "Mode"}}}, 6*time.Second)
	if status == nil || status[0] == nil {
		return false, "Could not reach OBS"
	}
	// The ladder writes SimpleOutput/VBitrate — ignored in Advanced output mode, so a
	// shift would restart the stream for NO quality change. Refuse instead.
	if m := gsOr(status[1], "parameterValue", "Simple"); m != "Simple" {
		return false, "OBS is in Advanced output mode — the quality ladder only supports " +
			"the default Simple mode (OBS's own dynamic bitrate still works). " +
			"Change the bitrate manually in OBS Settings → Output."
	}
	wasLive := gb(status[0], "outputActive", false)
	// OBS stops outputs ASYNCHRONOUSLY: StartStream straight after StopStream is rejected
	// ("output still stopping"), which would leave the stream DOWN while reporting
	// success. So: stop, wait for the output to go inactive, reconfigure, start.
	if wasLive {
		if r := obsCall(st, []obsws.Req{{Type: "StopStream"}}, 8*time.Second); r == nil || r[0] == nil {
			return false, "Could not stop the stream — no quality change made"
		}
		stopped := false
		for i := 0; i < 20; i++ { // up to ~10s for the output to wind down
			shiftSleep(500 * time.Millisecond)
			s2 := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
			if s2 != nil && s2[0] != nil && !gb(s2[0], "outputActive", false) && !gb(s2[0], "outputReconnecting", false) {
				stopped = true
				break
			}
		}
		if !stopped {
			return false, "Stream did not stop cleanly — no quality change made; check OBS"
		}
	}
	if r := obsCall(st, []obsws.Req{{Type: "SetProfileParameter", Data: M{"parameterCategory": "SimpleOutput",
		"parameterName": "VBitrate", "parameterValue": strconv.Itoa(target)}}}, 8*time.Second); r == nil || r[0] == nil {
		if wasLive {
			obsCall(st, []obsws.Req{{Type: "StartStream"}}, 8*time.Second) // best effort: back on air
		}
		return false, "Could not set the new bitrate — stream restarted at the old quality"
	}
	if wasLive {
		started := false
		for i := 0; i < 4; i++ { // OBS can still be settling — retry briefly
			if r := obsCall(st, []obsws.Req{{Type: "StartStream"}}, 8*time.Second); r != nil && r[0] != nil {
				started = true
				break
			}
			shiftSleep(1500 * time.Millisecond)
		}
		if !started {
			// The bitrate DID change — record it so the ladder/restore stay truthful even
			// though the operator has to press Start Streaming themselves.
			recordStep(newStep, target)
			return false, fmt.Sprintf("Bitrate set to %d kbps but the stream DID NOT restart — press Start Streaming in OBS now", target)
		}
		// OBS accepting StartStream is not the stream surviving: with the connected-
		// YouTube-account integration + auto-stop, StopStream ENDS the broadcast, and the
		// restart goes out into a dead broadcast. Verify it stayed up.
		shiftSleep(3 * time.Second)
		if s3 := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second); s3 == nil || s3[0] == nil || !gb(s3[0], "outputActive", false) {
			recordStep(newStep, target)
			return false, fmt.Sprintf("Bitrate set to %d kbps and OBS restarted, but the stream did not STAY up. "+
				"If YouTube shows the broadcast as ENDED, OBS's connected-account auto-stop ended it on "+
				"StopStream — stream with a persistent stream key instead (YouTube Studio → copy key → OBS "+
				"Settings → Stream), which survives quality changes.", target)
		}
	}
	streamMonMu.Lock()
	old := gi(streamMon, "current_kbps", 0)
	if old == 0 {
		old = baseline
	}
	streamMon["step"], streamMon["current_kbps"], streamMon["last_shift_at"] = newStep, target, now()
	streamSamples = nil // judge the new setting on fresh evidence
	shifts := append(gl(streamMon, "shifts"), M{"t": now(), "kbps_from": old, "kbps_to": target, "reason": reason, "mode": mode})
	if len(shifts) > 20 {
		shifts = shifts[len(shifts)-20:]
	}
	streamMon["shifts"] = shifts
	streamMonMu.Unlock()
	verb, tail := "reduced", ""
	if newStep == 0 {
		verb = "restored"
	}
	if wasLive {
		tail = " — stream restarted"
	}
	msg := fmt.Sprintf("Stream bitrate %s to %d kbps (%d%% of baseline)%s", verb, target, int(StreamLadder[newStep]*100), tail)
	say("  📶  %s [%s: %s]", msg, mode, reason)
	return true, msg
}

// streamMonitorTick reads OBS and returns a downshift decision (next step, reason), or
// step < 0 for none.
func streamMonitorTick() (int, string) {
	st := LoadState()
	pp := func(cat, name string) obsws.Req {
		return obsws.Req{Type: "GetProfileParameter", Data: M{"parameterCategory": cat, "parameterName": name}}
	}
	res := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}, pp("SimpleOutput", "VBitrate"),
		pp("Output", "DynamicBitrate"), pp("Output", "Mode")}, 5*time.Second)
	t := now()
	streamMonMu.Lock()
	defer streamMonMu.Unlock()
	if res == nil || res[0] == nil {
		streamMon["reachable"], streamMon["streaming"] = false, false
		streamSamples = nil
		return -1, ""
	}
	streamMon["reachable"] = true
	// Tier-1 status: is OBS's own Dynamic Bitrate on? (Surfaced in the panel so nobody has
	// to dig through OBS settings to check.)
	streamMon["dynamic_bitrate"] = gsOr(res[2], "parameterValue", "") == "true" ||
		gsOr(res[2], "parameterValue", "") == "True"
	simple := gsOr(res[3], "parameterValue", "Simple") == "Simple" // unset Mode = Simple
	streamMon["simple_mode"] = simple
	vbitrate := 0
	if v := res[1]; v != nil && pyval.Truthy(v["parameterValue"]) {
		if n, err := strconv.Atoi(trim(pyval.Str(v["parameterValue"]))); err == nil {
			vbitrate = n
		}
	}
	if simple && vbitrate != 0 {
		streamMon["configured_kbps"] = vbitrate
	}
	s0 := res[0]
	live := gb(s0, "outputActive", false)
	if gb(streamMon, "streaming", false) && !live {
		// The stream just ended: build the match page a minute later — if it's still off
		// then. (The ladder's own stop/start, or an operator restarting the stream, also
		// reads as an end here for one tick.)
		time.AfterFunc(60*time.Second, matchPageAfterStreamEnd)
	}
	streamMon["streaming"] = live
	if !live {
		streamSamples = nil
		return -1, ""
	}
	// Baseline: the configured bitrate the first time the stream is seen live at step 0.
	step := gi(streamMon, "step", 0)
	if simple && step == 0 && vbitrate != 0 {
		streamMon["baseline_kbps"], streamMon["current_kbps"] = vbitrate, vbitrate
	}
	streamSamples = append(streamSamples, StreamSample{T: t, Congestion: gf(s0, "outputCongestion", 0),
		Dropped: gi(s0, "outputSkippedFrames", 0), Total: gi(s0, "outputTotalFrames", 0)})
	if len(streamSamples) > 40 {
		streamSamples = streamSamples[len(streamSamples)-40:]
	}
	action, reason := EvaluateStreamSamples(streamSamples, t, gf(streamMon, "last_shift_at", 0))
	streamMon["last_reason"] = reason
	if action == "downshift" && gb(st, "stream_auto_downshift", false) && simple && step < len(StreamLadder)-1 {
		return step + 1, reason
	}
	return -1, ""
}

// StartStreamMonitor runs the sentinel every 15s (once).
func StartStreamMonitor() {
	streamMonMu.Lock()
	if gb(streamMon, "enabled_thread", false) {
		streamMonMu.Unlock()
		return
	}
	streamMon["enabled_thread"] = true
	streamMonMu.Unlock()
	loop("Stream monitor", StreamPollSec*time.Second, func() {
		if step, reason := streamMonitorTick(); step >= 0 {
			ApplyStreamQualityStep(step, reason, "auto")
		}
	})
}
