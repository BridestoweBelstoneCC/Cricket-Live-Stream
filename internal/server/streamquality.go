package server

// Adaptive stream quality: two tiers of defence against a struggling upload.
//  1. OBS's own Dynamic Bitrate (enabled by OBS setup) nudges the encoder bitrate up and
//     down seamlessly â€” no disconnect, no operator action.
//  2. This sentinel watches the stream's congestion + dropped-frame counters while live.
//     On sustained trouble it can step the configured bitrate DOWN a ladder â€” from the
//     panel, or automatically when the operator enables auto mode. A step means stop â†’
//     reconfigure â†’ start (~5-10s ingest gap), so it's rate-limited.
//  3. In auto mode it also steps back UP, one rung at a time: after a long clean spell
//     at the current rung, only while the line is really carrying that bitrate (Dynamic
//     Bitrate can be quietly holding it lower), and only at a break in play. A step up
//     that the line can't take is undone by the next downshift, and each one that fails
//     doubles the wait before the next try. It used to stay down for good: one bad patch
//     early on and a club with a weak line streamed at a third of its quality all day.

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
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

	StreamUpshiftAfter    = 600  // clean for this long at a rung before trying the next one up
	StreamUpshiftMaxWait  = 7200 // the wait doubles per failed step up, to this
	StreamUpshiftFailSec  = 600  // a downshift this soon after a step up means it failed
	StreamCleanCongestion = 0.03 // "clean" is much stricter than "not struggling"
	StreamCleanDroppedPct = 0.5
	StreamCleanCarried    = 0.6 // sent at least this share of the current bitrate
)

// StreamLadder is fractions of the baseline bitrate.
var StreamLadder = []float64{1.0, 0.7, 0.5, 0.35}

// StreamLadderHeight is the output height at each rung (0 = what the club streams at).
// At 35% (1400 kbps from 4000) 720p is blocks; 480p at that bitrate is a clean picture.
// Only ever lowers: a club already at 480p or below is left alone.
var StreamLadderHeight = []int{0, 0, 0, 480}

const (
	// OBS sending this much more than the setting, for a minute: the setting isn't what
	// the encoder runs at. (Audio and container overhead are a couple of hundred kbps.)
	streamOverSendFactor = 1.3
	streamOverSendSlack  = 300
	streamOverSendTicks  = 4
)

// StreamSample is one reading of OBS's stream output.
type StreamSample struct {
	T              float64
	Congestion     float64
	Dropped, Total int
	Bytes          int // outputBytes: what actually went up the line
}

var (
	streamMon     = newStreamMon()
	streamSamples []StreamSample
	streamMonMu   sync.Mutex
	qualityShift  sync.Mutex
)

// newStreamMon is the monitor's state as a server starts.
func newStreamMon() M {
	return M{
		"enabled_thread": false, "streaming": false, "reachable": nil,
		"baseline_kbps": nil, // VBitrate observed at step 0 while live
		"step":          0,   // index into StreamLadder
		"current_kbps":  nil, "last_shift_at": 0.0, "shifts": []any{}, "last_reason": "",
		"dynamic_bitrate": nil, // OBS's own Dynamic Bitrate setting (nil = not yet read)
		"simple_mode":     nil, // the ladder needs Simple output mode
		// VBitrate as OBS is CURRENTLY configured, read every tick whatever the live/step
		// state â€” unlike baseline_kbps, which only latches while live. The pre-flight
		// bitrate check needs it to catch a leftover downshift BEFORE going live.
		"configured_kbps": nil,
		"clean_since":     0.0, // start of the current clean spell; 0 = not clean now
		"upshift_wait":    StreamUpshiftAfter,
		"last_dir":        "", // "up"/"down": the last shift, for the failed-step-up rule
		"upshift_note":    "", // why it isn't stepping up yet, for the panel
		// What OBS really sends (outputBytes over the last minute), and a warning when it
		// isn't what the ladder set: the replay-buffer bug hid for want of exactly this.
		"sent_kbps": nil, "over_ticks": 0, "bitrate_warning": "",
		"base_res":      nil, // the output size before the ladder first changed it
		"foreign_ticks": 0,   // ticks OBS's bitrate has disagreed with the ladder's
	}
}

func pct0(x float64) string { return pyfmt.Fixed(x*100, 0) + "%" }

// EvaluateStreamSamples is the pure decision: "downshift" or "hold", and why. It needs
// sustained evidence across the window, and respects the cooldown so a shift's own
// restart blip can't trigger the next.
func EvaluateStreamSamples(samples []StreamSample, now, lastShift float64) (string, string) {
	recent, avg, dropped := streamWindow(samples, now)
	if len(recent) < 3 {
		return "hold", "not enough samples yet"
	}
	if now-lastShift < StreamShiftCooldown {
		return "hold", "cooling down after last quality change"
	}
	if avg >= StreamCongestionTrip {
		return "downshift", fmt.Sprintf("sustained congestion (avg %s over %ds)", pct0(avg), StreamWindowSec)
	}
	if dropped >= StreamDroppedPctTrip {
		return "downshift", fmt.Sprintf("dropping frames (%s%% over %ds)", pyfmt.Fixed(dropped, 1), StreamWindowSec)
	}
	return "hold", fmt.Sprintf("healthy (congestion %s, drops %s%%)", pct0(avg), pyfmt.Fixed(dropped, 1))
}

// streamWindow is the samples in the last StreamWindowSec, their average congestion and
// the % of frames dropped across them.
func streamWindow(samples []StreamSample, now float64) ([]StreamSample, float64, float64) {
	var recent []StreamSample
	for _, s := range samples {
		if now-s.T <= StreamWindowSec {
			recent = append(recent, s)
		}
	}
	if len(recent) == 0 {
		return nil, 0, 0
	}
	sum := 0.0
	for _, s := range recent {
		sum += s.Congestion
	}
	dDropped := recent[len(recent)-1].Dropped - recent[0].Dropped
	dTotal := recent[len(recent)-1].Total - recent[0].Total
	dropped := 0.0
	if dTotal > 0 {
		dropped = 100 * float64(dDropped) / float64(dTotal)
	}
	return recent, sum / float64(len(recent)), dropped
}

// StreamClean: the last minute was clean enough to count towards a step up â€” next to no
// congestion or drops, and the line really carried the current bitrate (OBS's Dynamic
// Bitrate lowers what it sends without telling the ladder, so a quiet congestion figure
// alone could just mean it has already backed off). No byte count = not proven clean.
func StreamClean(samples []StreamSample, now float64, currentKbps int) (bool, string) {
	recent, avg, dropped := streamWindow(samples, now)
	if len(recent) < 3 {
		return false, "not enough samples yet"
	}
	if avg >= StreamCleanCongestion || dropped >= StreamCleanDroppedPct {
		return false, fmt.Sprintf("not clean enough yet (congestion %s, drops %s%%)", pct0(avg), pyfmt.Fixed(dropped, 1))
	}
	first, last := recent[0], recent[len(recent)-1]
	span, bytes := last.T-first.T, last.Bytes-first.Bytes
	if span <= 0 || bytes <= 0 || currentKbps <= 0 {
		return false, "no bitrate reading yet"
	}
	sent := float64(bytes) * 8 / 1000 / span
	if sent < StreamCleanCarried*float64(currentKbps) {
		return false, fmt.Sprintf("only %d of %d kbps getting through", int(sent), currentKbps)
	}
	return true, ""
}

// EvaluateUpshift is the pure step-up decision, given when the current clean spell
// started (0 = not clean now), how long it must last, and whether play is paused.
func EvaluateUpshift(cleanSince, now, wait float64, atBreak bool, breakWhy string) (bool, string) {
	if cleanSince == 0 {
		return false, ""
	}
	if left := wait - (now - cleanSince); left > 0 {
		return false, fmt.Sprintf("stepping up after %d more clean minutes", int(left+59)/60)
	}
	if !atBreak {
		return false, "line is clean â€” stepping up at the next break in play"
	}
	return true, fmt.Sprintf("clean for %d minutes (%s)", int(now-cleanSince)/60, breakWhy)
}

// What play is doing, from the overlay's /live frames: a step up restarts the stream for
// a few seconds, so it waits for the end of an over, or a pause (drinks, innings break).
var (
	playMu       sync.Mutex
	playBalls    = -1
	playSig      string  // balls/score/wickets: a wide or a no-ball moves the score only
	playChangeAt float64 // when anything in playSig last changed
	playFrameAt  float64 // the last frame with a score in it
	// A replay on air until then. A boundary off the last ball replays straight into the
	// gap between overs, and a step's restart in it cut the replay off for viewers.
	playReplayUntil float64
)

// noteReplay: a replay is on air for about secs from now (the save, then the clip).
func noteReplay(secs float64) {
	t := now()
	playMu.Lock()
	playReplayUntil = max(playReplayUntil, t+secs)
	playMu.Unlock()
}

func notePlay(state M) {
	b, t := oversToBalls(state["overs"]), now()
	sig := fmt.Sprint(b, "/", state["score"], "/", state["wickets"])
	playMu.Lock()
	defer playMu.Unlock()
	playFrameAt, playBalls = t, b
	if sig != playSig {
		playSig, playChangeAt = sig, t
	}
}

const (
	// Early in the gap between overs: one can be as short as 45s at a club match, and a
	// restart takes up to ~10s, so a step decided late in the gap ran into the next ball.
	playOverGapSec = 30
	playPauseSec   = 180 // nothing for this long: drinks, an innings break, rain...
	// ...but only early in it: a pause can end at any moment, and a step decided at the
	// end of drinks went out on the first ball after it (stress test).
	playPauseEndSec = 240
	playNoScoreSec  = 60 // no score on screen at all (before the match, after it)
)

func playBreak(t float64) (bool, string) {
	playMu.Lock()
	defer playMu.Unlock()
	switch {
	case t < playReplayUntil:
		return false, ""
	case t-playFrameAt > playNoScoreSec:
		return true, "no match in play"
	case playBalls > 0 && playBalls%6 == 0 && t-playChangeAt <= playOverGapSec:
		return true, "end of the over"
	case t-playChangeAt >= playPauseSec && t-playChangeAt <= playPauseEndSec:
		return true, "a pause in play"
	}
	return false, ""
}

func streamLadderKbps(baseline, step int) int {
	step = max(0, min(step, len(StreamLadder)-1))
	return max(500, int(float64(baseline)*StreamLadder[step]))
}

var shiftSleep = time.Sleep

// waitStopped polls OBS up to polls times, half a second apart, for the stream output to
// be fully stopped. known is false when OBS never answered.
func waitStopped(st M, polls int) (stopped, known bool) {
	for i := 0; i < polls; i++ {
		shiftSleep(500 * time.Millisecond)
		s := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
		if s == nil || s[0] == nil {
			continue
		}
		known = true
		if !gb(s[0], "outputActive", false) && !gb(s[0], "outputReconnecting", false) {
			return true, true
		}
	}
	return false, known
}

// backOnAir starts a stream the shift stopped. OBS refuses StartStream while an output
// is still settling, so it retries; and OBS accepting StartStream is not the stream
// surviving: with the connected-YouTube-account integration + auto-stop, StopStream ENDS
// the broadcast, and the restart goes out into a dead broadcast. So it checks it stayed up.
func backOnAir(st M) (started, stayed bool) {
	for i := 0; i < 6; i++ {
		if r := obsCall(st, []obsws.Req{{Type: "StartStream"}}, 8*time.Second); r != nil && r[0] != nil {
			started = true
			break
		}
		shiftSleep(1500 * time.Millisecond)
	}
	if !started {
		return false, false
	}
	shiftSleep(3 * time.Second)
	s := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
	return true, s != nil && s[0] != nil && gb(s[0], "outputActive", false)
}

func waitReplayStopped(st M) bool {
	for i := 0; i < 20; i++ {
		s := obsCall(st, []obsws.Req{{Type: "GetReplayBufferStatus"}}, 5*time.Second)
		if s != nil && s[0] != nil && !gb(s[0], "outputActive", false) {
			return true
		}
		shiftSleep(500 * time.Millisecond)
	}
	return false
}

// replayBackNote starts the replay buffer a step stopped (after the stream, so the stream
// builds the encoder), and says so only if it wouldn't. The replay trigger also starts a
// buffer it finds stopped, so a refusal here costs one replay, not the afternoon's.
func replayBackNote(st M) string {
	for i := 0; i < 6; i++ {
		obsCall(st, []obsws.Req{{Type: "StartReplayBuffer"}}, 8*time.Second)
		shiftSleep(time.Second)
		if s := obsCall(st, []obsws.Req{{Type: "GetReplayBufferStatus"}}, 5*time.Second); s != nil && s[0] != nil && gb(s[0], "outputActive", false) {
			return ""
		}
	}
	watchdogLogFix("The replay buffer didn't restart after a quality change â€” the next replay will try again")
	return " (the replay buffer didn't restart; the next replay will try again)"
}

// backOnAirNote restarts the stream at the bitrate it had, and says how that went.
func backOnAirNote(st M) string {
	if started, stayed := backOnAir(st); !started {
		return ", and the stream DID NOT restart â€” press Start Streaming in OBS now"
	} else if !stayed {
		return ", and the stream did not STAY up after restarting â€” check OBS and YouTube now"
	}
	return " â€” stream restarted at the old quality"
}

var streamEnded = func() { // swappable for tests
	time.AfterFunc(60*time.Second, matchPageAfterStreamEnd)
	time.AfterFunc(90*time.Second, chaptersAfterStreamEnd)
}

// ApplyStreamQualityStep: stop â†’ wait â†’ set the SimpleOutput bitrate â†’ start, verified
// at each step. Serialised: a panel press and the auto path must never interleave two
// stop/start cycles.
func ApplyStreamQualityStep(newStep int, reason, mode string) (bool, string) {
	if !qualityShift.TryLock() {
		return false, "A quality change is already in progress â€” wait for it to finish"
	}
	defer qualityShift.Unlock()
	return applyQualityStepLocked(newStep, reason, mode)
}

func recordStep(step, target int) {
	streamMonMu.Lock()
	noteDirLocked(step)
	streamMon["step"], streamMon["current_kbps"], streamMon["last_shift_at"] = step, target, now()
	streamMon["over_ticks"], streamMon["bitrate_warning"] = 0, ""
	streamSamples = nil
	baseline, baseRes := gi(streamMon, "baseline_kbps", 0), streamMon["base_res"]
	streamMonMu.Unlock()
	saveLadder(step, baseline, target, baseRes)
}

// The ladder's place is kept in the match state as well as in memory. Mid-match the
// server is restarted after a crash or a freeze, and a fresh one starts at step 0 and
// takes OBS's bitrate as the full one: stepped down before the restart, it then stayed
// down all day (nothing to step up to), and Restore "restored" the reduced bitrate.
const streamLadderKeepSec = 12 * 3600

func saveLadder(step, baseline, kbps int, baseRes any) {
	UpdateState(func(s M) {
		if step == 0 {
			delete(s, "stream_ladder")
			return
		}
		s["stream_ladder"] = M{"step": step, "baseline_kbps": baseline, "kbps": kbps, "base_res": baseRes, "at": now()}
	})
}

// resumeLadderLocked picks the ladder up where a previous server left it, when OBS is
// still at the bitrate it set (anything else, and the record is from another match or
// someone has changed OBS since). Under streamMonMu.
func resumeLadderLocked(st M, vbitrate int, t float64) bool {
	lad, _ := st["stream_ladder"].(map[string]any)
	if lad == nil {
		return false
	}
	step, baseline, kbps := gi(lad, "step", 0), gi(lad, "baseline_kbps", 0), gi(lad, "kbps", 0)
	if at := gf(lad, "at", 0); t-at > streamLadderKeepSec || t < at ||
		step <= 0 || step >= len(StreamLadder) || baseline <= 0 || kbps != vbitrate {
		return false
	}
	switch res := lad["base_res"].(type) {
	case []int: // still in memory as saved
		if len(res) == 2 {
			streamMon["base_res"] = res
		}
	case []any: // read back from the file
		if len(res) == 2 {
			if w, h := pyval.IntOr(res[0], 0), pyval.IntOr(res[1], 0); w > 0 && h > 0 {
				streamMon["base_res"] = []int{w, h}
			}
		}
	}
	streamMon["step"], streamMon["baseline_kbps"], streamMon["current_kbps"] = step, baseline, kbps
	// As if it had just stepped: the restart's own blip mustn't count against the line.
	streamMon["last_shift_at"], streamMon["last_dir"], streamMon["clean_since"] = t, "down", 0.0
	say("  ðŸ“¶  Quality ladder picked up where it was before the restart: %d kbps (%d%% of %d)",
		kbps, int(StreamLadder[step]*100), baseline)
	return true
}

// noteDirLocked records which way a shift to step goes, before streamMon's step and
// last_shift_at move to it. A step down soon after a step up means the line couldn't take
// it: wait twice as long before trying again. One long after means the line was fine for
// a while and then got worse, so the wait starts again from the beginning.
func noteDirLocked(step int) {
	old := gi(streamMon, "step", 0)
	if step == old {
		return
	}
	streamMon["clean_since"] = 0.0
	if step < old {
		streamMon["last_dir"] = "up"
		return
	}
	wait := StreamUpshiftAfter
	if gs(streamMon, "last_dir", "") == "up" && now()-gf(streamMon, "last_shift_at", 0) < StreamUpshiftFailSec {
		wait = min(2*gi(streamMon, "upshift_wait", StreamUpshiftAfter), StreamUpshiftMaxWait)
	}
	streamMon["last_dir"], streamMon["upshift_wait"] = "down", wait
}

// noteSentLocked works out what OBS really sends over the last minute and warns, once per
// setting, when it stays well above what the ladder (or OBS's own setting) says. Only
// over-sending: sending less is just what a congested line, a quiet picture or Dynamic
// Bitrate looks like. Under streamMonMu.
func noteSentLocked(t float64, vbitrate int) {
	recent, _, _ := streamWindow(streamSamples, t)
	streamMon["sent_kbps"] = nil
	if len(recent) < 3 {
		return
	}
	first, last := recent[0], recent[len(recent)-1]
	span, bytes := last.T-first.T, last.Bytes-first.Bytes
	if span <= 0 || bytes <= 0 {
		return
	}
	sent := int(float64(bytes) * 8 / 1000 / span)
	streamMon["sent_kbps"] = sent
	set := gi(streamMon, "current_kbps", 0)
	if set == 0 {
		set = vbitrate
	}
	if set == 0 || float64(sent) <= streamOverSendFactor*float64(set)+streamOverSendSlack {
		streamMon["over_ticks"] = 0
		return
	}
	n := gi(streamMon, "over_ticks", 0) + 1
	streamMon["over_ticks"] = n
	if n == streamOverSendTicks {
		// Two causes, both measured on a real OBS: a setting that never reached the
		// encoder (the replay buffer keeping the old one running), and a picture H.264
		// can't fit in the bitrate even at its coarsest, where NVENC sends what it must
		// (3x, on an animated test page; camera footage and heavy grain held their rate).
		w := fmt.Sprintf("OBS is sending %d kbps, far more than the %d kbps it's set to. Either the setting hasn't "+
			"reached the encoder (stop the stream and the replay buffer, then start them again), or the picture is "+
			"too detailed for this bitrate and the hardware encoder is sending what it must: a busy video or "+
			"animated source can do this, a camera picture hasn't in testing (OBS Settings â†’ Output â†’ Encoder: "+
			"x264 holds its bitrate, at the cost of a blockier picture).", sent, set)
		streamMon["bitrate_warning"] = w
		say("  âš   %s", w)
		safeGo("bitrate warning", func() { watchdogLogFix(w) })
	}
}

// applyLadderResolution sets the output size for step (StreamLadderHeight), with every
// output stopped. Returns a note for the step's message ("" when nothing changed).
func applyLadderResolution(st M, step int) string {
	v := obsCall(st, []obsws.Req{{Type: "GetVideoSettings"}}, 5*time.Second)
	if v == nil || v[0] == nil {
		return ""
	}
	w, h := gi(v[0], "outputWidth", 0), gi(v[0], "outputHeight", 0)
	if w == 0 || h == 0 {
		return ""
	}
	streamMonMu.Lock()
	base, _ := streamMon["base_res"].([]int)
	if base == nil {
		base = []int{w, h}
		streamMon["base_res"] = base
	}
	streamMonMu.Unlock()
	tw, th := base[0], base[1]
	if lh := StreamLadderHeight[max(0, min(step, len(StreamLadderHeight)-1))]; lh > 0 && base[1] > lh {
		tw, th = (base[0]*lh/base[1]+1)&^1, lh
	}
	if tw == w && th == h {
		return ""
	}
	if r := obsCall(st, []obsws.Req{{Type: "SetVideoSettings", Data: M{"outputWidth": tw, "outputHeight": th}}}, 8*time.Second); r == nil || r[0] == nil {
		return " (the resolution couldn't be changed)"
	}
	return fmt.Sprintf(", %dp", th)
}

// shifting is set while a quality step runs: the replay trigger mustn't restart the
// replay buffer in the middle of one (that would rebuild the encoder at the old bitrate).
var shifting atomic.Bool

func applyQualityStepLocked(newStep int, reason, mode string) (ok bool, msg string) {
	shifting.Store(true)
	defer shifting.Store(false)
	st := LoadState()
	streamMonMu.Lock()
	baseline := gi(streamMon, "baseline_kbps", 0)
	streamMonMu.Unlock()
	if baseline == 0 {
		return false, "No baseline bitrate recorded yet â€” is the stream live?"
	}
	newStep = max(0, min(newStep, len(StreamLadder)-1))
	target := streamLadderKbps(baseline, newStep)
	status := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"},
		{Type: "GetProfileParameter", Data: M{"parameterCategory": "Output", "parameterName": "Mode"}},
		{Type: "GetReplayBufferStatus"}, {Type: "GetRecordStatus"}}, 6*time.Second)
	if status == nil || status[0] == nil {
		return false, "Could not reach OBS"
	}
	// The ladder writes SimpleOutput/VBitrate â€” ignored in Advanced output mode, so a
	// shift would restart the stream for NO quality change. Refuse instead.
	if m := gsOr(status[1], "parameterValue", "Simple"); m != "Simple" {
		return false, "OBS is in Advanced output mode â€” the quality ladder only supports " +
			"the default Simple mode (OBS's own dynamic bitrate still works). " +
			"Change the bitrate manually in OBS Settings â†’ Output."
	}
	// OBS only reads the bitrate when it builds the encoder, and it only builds it when no
	// output is using it. The replay buffer shares the stream's encoder and runs all match
	// day, so restarting the stream alone changed nothing â€” measured on a real OBS 33: set
	// to 2000 or 4000, it went on sending the 6000 it started with. Every step, down or
	// up, restarted the stream for no change at all. So the buffer stops for the step too
	// (its last few seconds are lost; a replay in that moment is skipped). A recording
	// would have to be split in two, so that's left to the operator.
	if len(status) > 3 && gb(status[3], "outputActive", false) {
		return false, "OBS is recording â€” it only takes a new bitrate with the recording stopped, " +
			"so no quality change made. Stop the recording to let the quality ladder work."
	}
	// No answer about the buffer is "maybe running": skipping the stop on a guess left the
	// step reporting success with the encoder still at the old bitrate (stress test).
	rbWas := len(status) < 3 || status[2] == nil || gb(status[2], "outputActive", false)
	wasLive := gb(status[0], "outputActive", false)
	rbStopped := false
	defer func() {
		if rbStopped {
			msg += replayBackNote(st)
		}
	}()
	// OBS stops outputs ASYNCHRONOUSLY: StartStream straight after StopStream is rejected
	// ("output still stopping"), which would leave the stream DOWN while reporting
	// success. So: stop, wait for the output to go inactive, reconfigure, start.
	if wasLive {
		// Once a stop is under way the picture is off air whatever happens next, so a stop
		// is waited out and the change made, never abandoned. On a congested line â€” which
		// is when a step down happens â€” OBS flushes its backlog first and a stop can take
		// half a minute (measured: over 30s on a real OBS, throttled). Giving up after 10s
		// and restarting at the old quality cost a restart for nothing, again every check,
		// and a stop that finished later left the stream down (stress test: a dozen in a
		// few minutes).
		polls := 120 // up to a minute
		if r := obsCall(st, []obsws.Req{{Type: "StopStream"}}, 8*time.Second); r == nil || r[0] == nil {
			// Refused, or no answer â€” which isn't proof nothing happened. Still live a few
			// seconds on: nothing changed and nothing was interrupted.
			polls = 6
		}
		if stopped, known := waitStopped(st, polls); !stopped {
			if polls == 6 {
				if known {
					return false, "Could not stop the stream â€” no quality change made"
				}
				return false, "Could not stop the stream â€” no quality change made, but OBS has stopped answering: check OBS that the stream is still live"
			}
			if !known {
				return false, "OBS isn't answering after stopping the stream for a quality change â€” check OBS now: the stream may be down"
			}
			// Still live a minute after an accepted stop: OBS never did it. The stream is
			// up; something in OBS is wrong, so it's left to a person.
			return false, "OBS didn't stop the stream for the quality change â€” no change made; check OBS now"
		}
	}
	if rbWas {
		r := obsCall(st, []obsws.Req{{Type: "StopReplayBuffer"}}, 8*time.Second)
		if !waitReplayStopped(st) {
			// The bitrate wouldn't take with the buffer still running: change nothing.
			if s := obsCall(st, []obsws.Req{{Type: "GetReplayBufferStatus"}}, 5*time.Second); s == nil || s[0] == nil {
				rbStopped = true // can't tell: make sure it's running when this returns
			}
			note := ""
			if wasLive {
				note = backOnAirNote(st)
			}
			return false, "OBS's replay buffer wouldn't stop, and OBS only takes a new bitrate with it stopped â€” no quality change made" + note
		}
		// Stopped now: start it again afterwards if it was running â€” known, or OBS
		// accepting the stop (it refuses one for a buffer that isn't running). Neither, and
		// it was most likely off already; a refused stop of a running buffer is the rare
		// case, and the replay trigger starts a buffer it finds stopped.
		rbStopped = (len(status) > 2 && gb(status[2], "outputActive", false)) || (r != nil && r[0] != nil)
	}
	if r := obsCall(st, []obsws.Req{{Type: "SetProfileParameter", Data: M{"parameterCategory": "SimpleOutput",
		"parameterName": "VBitrate", "parameterValue": strconv.Itoa(target)}}}, 8*time.Second); r == nil || r[0] == nil {
		if wasLive {
			// This used to try StartStream once and report "restarted" whatever OBS said â€”
			// found by the stress test: refused once (OBS still settling after the stop), it
			// left the stream down for the rest of the day while saying it was up.
			return false, "Could not set the new bitrate â€” no quality change made" + backOnAirNote(st)
		}
		return false, "Could not set the new bitrate â€” no quality change made"
	}
	// Every output is still stopped: the only time OBS lets the resolution change.
	resNote := applyLadderResolution(st, newStep)
	if wasLive {
		started, stayed := backOnAir(st)
		if !started {
			// The bitrate DID change â€” record it so the ladder/restore stay truthful even
			// though the operator has to press Start Streaming themselves.
			recordStep(newStep, target)
			return false, fmt.Sprintf("Bitrate set to %d kbps but the stream DID NOT restart â€” press Start Streaming in OBS now", target)
		}
		if !stayed {
			recordStep(newStep, target)
			return false, fmt.Sprintf("Bitrate set to %d kbps and OBS restarted, but the stream did not STAY up. "+
				"If YouTube shows the broadcast as ENDED, OBS's connected-account auto-stop ended it on "+
				"StopStream â€” stream with a persistent stream key instead (YouTube Studio â†’ copy key â†’ OBS "+
				"Settings â†’ Stream), which survives quality changes.", target)
		}
	}
	streamMonMu.Lock()
	old := gi(streamMon, "current_kbps", 0)
	if old == 0 {
		old = baseline
	}
	verb := "reduced"
	if newStep < gi(streamMon, "step", 0) {
		verb = "raised"
	}
	if newStep == 0 {
		verb = "restored"
	}
	noteDirLocked(newStep)
	streamMon["step"], streamMon["current_kbps"], streamMon["last_shift_at"] = newStep, target, now()
	streamMon["over_ticks"], streamMon["bitrate_warning"] = 0, ""
	streamSamples = nil // judge the new setting on fresh evidence
	shifts := append(gl(streamMon, "shifts"), M{"t": now(), "kbps_from": old, "kbps_to": target, "reason": reason, "mode": mode})
	if len(shifts) > 20 {
		shifts = shifts[len(shifts)-20:]
	}
	streamMon["shifts"] = shifts
	baseRes := streamMon["base_res"]
	streamMonMu.Unlock()
	saveLadder(newStep, baseline, target, baseRes)
	tail := ""
	if wasLive {
		tail = " â€” stream restarted"
	}
	msg = fmt.Sprintf("Stream bitrate %s to %d kbps (%d%% of baseline)%s%s", verb, target, int(StreamLadder[newStep]*100), resNote, tail)
	say("  ðŸ“¶  %s [%s: %s]", msg, mode, reason)
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
		// The stream just ended: build the match page a minute later â€” if it's still off
		// then. (The ladder's own stop/start, or an operator restarting the stream, also
		// reads as an end here for one tick.)
		streamEnded()
	}
	streamMon["streaming"] = live
	if !live {
		streamSamples = nil
		return -1, ""
	}
	step := gi(streamMon, "step", 0)
	// Stepped down, and OBS is at some other bitrate: someone changed it in OBS (seen on
	// two ticks, so it's not a step of the ladder's own landing between the read and here).
	// Left alone, the next step "up" could lower it. Start the ladder again from there.
	if simple && step > 0 && vbitrate != 0 && vbitrate != gi(streamMon, "current_kbps", 0) && !shifting.Load() {
		if n := gi(streamMon, "foreign_ticks", 0) + 1; n < 2 {
			streamMon["foreign_ticks"] = n
		} else {
			say("  ðŸ“¶  OBS's bitrate was changed outside the quality ladder (now %d kbps): the ladder starts again from there", vbitrate)
			streamMon["step"], streamMon["foreign_ticks"], streamMon["last_dir"] = 0, 0, ""
			streamMon["upshift_wait"], streamMon["clean_since"] = StreamUpshiftAfter, 0.0
			step = 0
			safeGo("quality ladder", func() { saveLadder(0, 0, 0, nil) })
		}
	} else {
		streamMon["foreign_ticks"] = 0
	}
	// Baseline: the configured bitrate the first time the stream is seen live at step 0
	// (unless this is a restarted server picking up a stepped-down ladder).
	if simple && step == 0 && vbitrate != 0 {
		if streamMon["baseline_kbps"] == nil && resumeLadderLocked(st, vbitrate, t) {
			step = gi(streamMon, "step", 0)
		} else {
			streamMon["baseline_kbps"], streamMon["current_kbps"] = vbitrate, vbitrate
		}
	}
	streamSamples = append(streamSamples, StreamSample{T: t, Congestion: gf(s0, "outputCongestion", 0),
		Dropped: gi(s0, "outputSkippedFrames", 0), Total: gi(s0, "outputTotalFrames", 0),
		Bytes: gi(s0, "outputBytes", 0)})
	if len(streamSamples) > 40 {
		streamSamples = streamSamples[len(streamSamples)-40:]
	}
	noteSentLocked(t, vbitrate)
	auto := gb(st, "stream_auto_downshift", false) && simple
	// A step up the line can't take is undone on the first full window of evidence, not
	// after the cooldown: that wait was 2-3 minutes of dropped frames on air per failed
	// try (stress test, steady weak line). The cooldown is there so a shift's own restart
	// blip can't trigger the next â€” and samples start afresh after every shift anyway.
	lastShift := gf(streamMon, "last_shift_at", 0)
	if gs(streamMon, "last_dir", "") == "up" && t-lastShift < StreamUpshiftFailSec {
		lastShift = 0
	}
	action, reason := EvaluateStreamSamples(streamSamples, t, lastShift)
	streamMon["last_reason"] = reason
	if action == "downshift" && auto && step < len(StreamLadder)-1 {
		return step + 1, reason
	}
	clean, why := StreamClean(streamSamples, t, gi(streamMon, "current_kbps", 0))
	if !clean {
		streamMon["clean_since"] = 0.0
	} else if gf(streamMon, "clean_since", 0) == 0 {
		streamMon["clean_since"] = t
	}
	if step == 0 || !auto {
		streamMon["upshift_note"] = ""
		return -1, ""
	}
	atBreak, breakWhy := playBreak(t)
	up, note := EvaluateUpshift(gf(streamMon, "clean_since", 0), t,
		float64(gi(streamMon, "upshift_wait", StreamUpshiftAfter)), atBreak, breakWhy)
	if !clean {
		note = why
	}
	streamMon["upshift_note"] = note
	if up && action != "downshift" {
		return step - 1, note
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
