package server

// Adaptive stream quality: the pure downshift decision (hysteresis, cooldown), the
// bitrate ladder, and the HTTP surface — everything except talking to a real OBS
// (tests/test_stream_quality.py).

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

const sqNow = 1_000_000.0

// mkSamples is one sample per entry, oldest first.
func mkSamples(agos []float64, congestion float64, droppedStep, totalStep int) []StreamSample {
	var out []StreamSample
	dropped, total := 0, 0
	// sorted oldest first: largest "ago" first
	for i := range agos {
		for j := i + 1; j < len(agos); j++ {
			if agos[j] > agos[i] {
				agos[i], agos[j] = agos[j], agos[i]
			}
		}
	}
	for _, ago := range agos {
		dropped += droppedStep
		total += totalStep
		out = append(out, StreamSample{T: sqNow - ago, Congestion: congestion, Dropped: dropped, Total: total})
	}
	return out
}

func decide(t *testing.T, s []StreamSample, lastShift float64, wantAction, wantIn string) {
	t.Helper()
	a, r := EvaluateStreamSamples(s, sqNow, lastShift)
	if a != wantAction || !strings.Contains(r, wantIn) {
		t.Fatalf("got %s (%s), want %s containing %q", a, r, wantAction, wantIn)
	}
}

func TestDownshiftNeedsEnoughEvidence(t *testing.T) {
	decide(t, mkSamples([]float64{30, 15}, 0.9, 0, 375), 0, "hold", "not enough")
}

func TestDownshiftHealthyStreamHolds(t *testing.T) {
	decide(t, mkSamples([]float64{45, 30, 15, 0}, 0.02, 0, 375), 0, "hold", "healthy")
}

func TestDownshiftSustainedCongestionTrips(t *testing.T) {
	decide(t, mkSamples([]float64{45, 30, 15, 0}, 0.4, 0, 375), 0, "downshift", "congestion")
}

func TestDownshiftDroppedFramesTrip(t *testing.T) {
	// ~6.7% of frames dropped across the window, congestion metric quiet
	decide(t, mkSamples([]float64{45, 30, 15, 0}, 0, 25, 375), 0, "downshift", "frames")
}

func TestDownshiftCooldownBlocksFlapping(t *testing.T) {
	// The same terrible stream, but we shifted 30s ago — the shift's own restart blip
	// must not immediately trigger the next step down
	decide(t, mkSamples([]float64{45, 30, 15, 0}, 0.9, 0, 375), sqNow-30, "hold", "cooling down")
}

func TestDownshiftOldSamplesOutsideWindowIgnored(t *testing.T) {
	old := mkSamples([]float64{300, 290}, 0.9, 0, 375)
	fresh := mkSamples([]float64{40, 25, 10}, 0.01, 0, 375)
	decide(t, append(old, fresh...), 0, "hold", "")
}

func TestLadderSteps(t *testing.T) {
	for step, want := range []int{4000, 2800, 2000, 1400} {
		mustEq(t, streamLadderKbps(4000, step), want)
	}
}

func TestLadderFloorAndClamping(t *testing.T) {
	mustEq(t, streamLadderKbps(1000, 3), 500) // never below 500
	mustEq(t, streamLadderKbps(4000, 99), streamLadderKbps(4000, len(StreamLadder)-1))
}

func TestDefaultStateHasAutoDownshiftToggle(t *testing.T) {
	v, ok := DefaultState()["stream_auto_downshift"]
	if !ok || v != false {
		t.Fatal(v, ok)
	}
}

func resetStreamMon(t *testing.T, kv M) {
	t.Helper()
	streamMonMu.Lock()
	saved := shallow(streamMon)
	for k, v := range (M{"streaming": false, "reachable": nil, "baseline_kbps": nil, "step": 0,
		"current_kbps": nil, "last_shift_at": 0.0, "shifts": []any{}, "last_reason": "",
		"dynamic_bitrate": nil, "simple_mode": nil, "clean_since": 0.0,
		"upshift_wait": StreamUpshiftAfter, "last_dir": "", "upshift_note": "",
		"base_res": nil, "foreign_ticks": 0, "configured_kbps": nil}) {
		streamMon[k] = v
	}
	for k, v := range kv {
		streamMon[k] = v
	}
	streamSamples = nil
	streamMonMu.Unlock()
	t.Cleanup(func() {
		streamMonMu.Lock()
		streamMon = saved
		streamMonMu.Unlock()
	})
}

func TestStreamMonitorEndpoint(t *testing.T) {
	s := newTestServer(t, "")
	resetStreamMon(t, nil)
	st, body := s.getJSON("/stream/monitor")
	mustEq(t, st, 200)
	mustEq(t, body["ok"], any(true))
	mustEq(t, body["streaming"], any(false))
	eqv(t, body["ladder_pct"], []any{100, 70, 50, 35})
	mustEq(t, body["auto"], any(false))
	if body["dynamic_bitrate"] != nil { // not yet read from OBS
		t.Fatal(body["dynamic_bitrate"])
	}
	if _, ok := body["simple_mode"]; !ok {
		t.Fatal("no simple_mode")
	}
}

func TestDynamicBitrateEnableFailsCleanlyWithoutOBS(t *testing.T) {
	s := newTestServer(t, "")
	resetStreamMon(t, nil)
	setState(t, M{"obs_port": 1}) // nothing listens there
	st, body := s.postJSON("/stream/dynamic", M{})
	mustEq(t, st, 200)
	mustEq(t, body["ok"], any(false))
	if !strings.Contains(gs(body, "error", ""), "OBS") {
		t.Fatal(body)
	}
}

func TestQualityBadActionRejected(t *testing.T) {
	s := newTestServer(t, "")
	resetStreamMon(t, nil)
	st, _ := s.postJSON("/stream/quality", M{"action": "sideways"})
	mustEq(t, st, 400)
}

func TestQualityNoopWhenAlreadyAtStep(t *testing.T) {
	s := newTestServer(t, "")
	resetStreamMon(t, nil)
	st, body := s.postJSON("/stream/quality", M{"action": "restore"})
	mustEq(t, st, 200)
	mustEq(t, body["ok"], any(false))
	if !strings.Contains(gs(body, "error", ""), "already") {
		t.Fatal(body)
	}
}

func TestQualityDownWithoutBaselineFailsCleanly(t *testing.T) {
	// No stream observed → no baseline → a clean refusal, no OBS contact
	s := newTestServer(t, "")
	resetStreamMon(t, nil)
	st, body := s.postJSON("/stream/quality", M{"action": "down"})
	mustEq(t, st, 200)
	mustEq(t, body["ok"], any(false))
	if !strings.Contains(strings.ToLower(gs(body, "error", "")), "baseline") {
		t.Fatal(body)
	}
}

// ── The shift procedure itself, against a scripted OBS ──

type sentReq struct {
	typ  string
	data any
}

func runShift(t *testing.T, responses [][]map[string]any, step int, reason, mode string) (bool, string, [][]sentReq) {
	t.Helper()
	sandbox(t, "")
	resetStreamMon(t, M{"streaming": true, "reachable": true, "baseline_kbps": 4000, "step": 0,
		"current_kbps": 4000, "dynamic_bitrate": true, "simple_mode": true})
	var calls [][]sentReq
	savedCall, savedSleep := obsCall, shiftSleep
	t.Cleanup(func() { obsCall, shiftSleep = savedCall, savedSleep })
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		var c []sentReq
		for _, r := range reqs {
			c = append(c, sentReq{r.Type, r.Data})
		}
		calls = append(calls, c)
		if len(responses) == 0 {
			return nil
		}
		r := responses[0]
		responses = responses[1:]
		return r
	}
	shiftSleep = func(time.Duration) {}
	ok, msg := ApplyStreamQualityStep(step, reason, mode)
	return ok, msg, calls
}

func types(calls [][]sentReq) [][]string {
	var out [][]string
	for _, c := range calls {
		var names []string
		for _, r := range c {
			names = append(names, r.typ)
		}
		out = append(out, names)
	}
	return out
}

func monGet(k string) any {
	streamMonMu.Lock()
	defer streamMonMu.Unlock()
	return streamMon[k]
}

func TestShiftAdvancedOutputModeRefusedBeforeRestart(t *testing.T) {
	// In Advanced mode SimpleOutput/VBitrate is ignored — a shift would restart the
	// stream for NO quality change. Must refuse without ever sending StopStream.
	ok, msg, calls := runShift(t, [][]map[string]any{{{"outputActive": true}, {"parameterValue": "Advanced"}}}, 1, "test", "manual")
	if ok || !strings.Contains(msg, "Advanced") {
		t.Fatal(ok, msg)
	}
	mustEq(t, len(calls), 1) // the status check only
	eqv(t, monGet("step"), 0)
}

func TestShiftSimpleModeStopsWaitsReconfiguresRestarts(t *testing.T) {
	// OBS stops outputs asynchronously — the shift must WAIT for the output to go
	// inactive before reconfiguring, and verify the restart actually happened.
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}}, // status, mode, no buffer, no recording
		{{}},                      // StopStream
		{{"outputActive": false}}, // wind-down confirmed
		{{}},                      // SetProfileParameter
		{{"outputWidth": 1280, "outputHeight": 720}}, // GetVideoSettings: 720p, no change
		{{}},                     // StartStream
		{{"outputActive": true}}, // ...and it STAYED up
	}, 1, "sustained congestion", "auto")
	if !ok || !strings.Contains(msg, "2800") { // 70% of the 4000 baseline
		t.Fatal(ok, msg)
	}
	eqv(t, types(calls), [][]string{{"GetStreamStatus", "GetProfileParameter", "GetReplayBufferStatus", "GetRecordStatus"}, {"StopStream"},
		{"GetStreamStatus"}, {"SetProfileParameter"}, {"GetVideoSettings"}, {"StartStream"}, {"GetStreamStatus"}})
	eqv(t, calls[3][0].data.(M)["parameterValue"], "2800")
	eqv(t, monGet("step"), 1)
	eqv(t, monGet("current_kbps"), 2800)
	shifts := monGet("shifts").([]any)
	last := shifts[len(shifts)-1].(M)
	eqv(t, last["kbps_to"], 2800)
	eqv(t, last["mode"], "auto") // the guard's output-mode local must not shadow this
	streamMonMu.Lock()
	n := len(streamSamples)
	streamMonMu.Unlock()
	mustEq(t, n, 0) // a fresh evidence window
}

func TestShiftStopFailureMakesNoChange(t *testing.T) {
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}},
		{nil}, // StopStream refused
	}, 1, "test", "manual")
	if ok || !strings.Contains(msg, "no quality change") {
		t.Fatal(ok, msg)
	}
	for _, c := range types(calls) {
		for _, n := range c {
			if n == "SetProfileParameter" {
				t.Fatal("bitrate touched")
			}
		}
	}
	eqv(t, monGet("step"), 0)
}

func TestShiftFailedRestartReportsHonestly(t *testing.T) {
	// The bitrate changed but StartStream keeps failing: the operator must be TOLD the
	// stream is down, and the ladder must record the new bitrate so a restore is truthful.
	responses := [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}}, {{}}, {{"outputActive": false}}, {{}}, {{"outputWidth": 1280, "outputHeight": 720}},
		{nil}, {nil}, {nil}, {nil}, // every StartStream attempt fails
	}
	ok, msg, _ := runShift(t, responses, 1, "test", "manual")
	if ok || !strings.Contains(msg, "DID NOT restart") {
		t.Fatal(ok, msg)
	}
	eqv(t, monGet("current_kbps"), 2800)
}

func TestShiftRestartIntoDeadBroadcastReported(t *testing.T) {
	// OBS accepts StartStream but the stream doesn't STAY up — the connected-account
	// auto-stop having ENDED the broadcast on StopStream.
	ok, msg, _ := runShift(t, [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}}, {{}}, {{"outputActive": false}}, {{}}, {{"outputWidth": 1280, "outputHeight": 720}},
		{{}},                      // StartStream accepted...
		{{"outputActive": false}}, // ...but the stream died
	}, 1, "test", "manual")
	if ok || !strings.Contains(msg, "persistent stream key") {
		t.Fatal(ok, msg)
	}
	eqv(t, monGet("current_kbps"), 2800)
}

func TestShiftWhileNotLiveSkipsStopStart(t *testing.T) {
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": false}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}},
		{{}}, // just SetProfileParameter
	}, 2, "test", "manual")
	if !ok {
		t.Fatal(msg)
	}
	eqv(t, types(calls)[1], []string{"SetProfileParameter"})
	eqv(t, monGet("current_kbps"), 2000)
}

func TestConcurrentShiftsSerialised(t *testing.T) {
	qualityShift.Lock() // a shift is "in progress"
	ok, msg := ApplyStreamQualityStep(1, "test", "manual")
	qualityShift.Unlock()
	if ok || !strings.Contains(msg, "already in progress") {
		t.Fatal(ok, msg)
	}
}

func TestQualityTokenGatedMonitorOpen(t *testing.T) {
	s := newTestServer(t, testPW)
	resetStreamMon(t, nil)
	st, _ := s.postJSON("/stream/quality", M{"action": "down"})
	mustEq(t, st, 401)
	st, _ = s.postJSON("/stream/dynamic", M{})
	mustEq(t, st, 401)
	mustEq(t, s.get("/stream/monitor").status, 200)
}

// ── Stepping back up ──

// cleanSamples is a minute of a clean stream sending kbps, ending at end.
func cleanSamples(end float64, kbps int, congestion float64) []StreamSample {
	var out []StreamSample
	for i := 0; i < 4; i++ {
		t := end - float64(45-15*i)
		out = append(out, StreamSample{T: t, Congestion: congestion, Total: 375 * (i + 1),
			Bytes: int(t-end+45) * kbps * 1000 / 8})
	}
	return out
}

func TestCleanNeedsTheLineToCarryTheBitrate(t *testing.T) {
	if ok, why := StreamClean(cleanSamples(sqNow, 2000, 0), sqNow, 2000); !ok {
		t.Fatal(why)
	}
	// Quiet congestion, but Dynamic Bitrate is only getting half of it through
	if ok, why := StreamClean(cleanSamples(sqNow, 1000, 0), sqNow, 2000); ok || !strings.Contains(why, "getting through") {
		t.Fatal(ok, why)
	}
	// "Not struggling" (under the 15% trip) is not clean
	if ok, _ := StreamClean(cleanSamples(sqNow, 2000, 0.08), sqNow, 2000); ok {
		t.Fatal("8% congestion counted as clean")
	}
	// No byte count from OBS: not proven
	if ok, _ := StreamClean(mkSamples([]float64{45, 30, 15, 0}, 0, 0, 375), sqNow, 2000); ok {
		t.Fatal("clean without a bitrate reading")
	}
}

func TestUpshiftWaitsForTheCleanSpellAndABreak(t *testing.T) {
	if up, _ := EvaluateUpshift(0, sqNow, 600, true, "x"); up {
		t.Fatal("stepped up while not clean")
	}
	if up, why := EvaluateUpshift(sqNow-300, sqNow, 600, true, "x"); up || !strings.Contains(why, "5 more") {
		t.Fatal(up, why)
	}
	if up, why := EvaluateUpshift(sqNow-700, sqNow, 600, false, ""); up || !strings.Contains(why, "break in play") {
		t.Fatal(up, why)
	}
	if up, _ := EvaluateUpshift(sqNow-700, sqNow, 600, true, "end of the over"); !up {
		t.Fatal("didn't step up")
	}
}

func setPlay(t *testing.T, balls int, ballAgo, frameAgo float64) {
	t.Helper()
	playMu.Lock()
	sb, ss, sa, sf, sr := playBalls, playSig, playChangeAt, playFrameAt, playReplayUntil
	playBalls, playSig, playChangeAt, playFrameAt, playReplayUntil = balls, "", now()-ballAgo, now()-frameAgo, 0
	playMu.Unlock()
	t.Cleanup(func() {
		playMu.Lock()
		playBalls, playSig, playChangeAt, playFrameAt, playReplayUntil = sb, ss, sa, sf, sr
		playMu.Unlock()
	})
}

func TestPlayBreaks(t *testing.T) {
	cases := []struct {
		balls             int
		ballAgo, frameAgo float64
		want              bool
	}{
		{27, 10, 2, false},  // mid-over (4.3)
		{30, 20, 2, true},   // the over just ended
		{30, 45, 2, false},  // late in the gap: the next ball could come mid-restart
		{33, 200, 2, true},  // nothing for 3 minutes: drinks, a stoppage
		{33, 600, 2, false}, // ...but not late in it: play could restart any second
		{27, 10, 90, true},  // no score on screen at all
	}
	for _, c := range cases {
		setPlay(t, c.balls, c.ballAgo, c.frameAgo)
		if got, _ := playBreak(now()); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestNotePlayTracksBallChanges(t *testing.T) {
	setPlay(t, -1, 0, 0)
	notePlay(M{"overs": "4.5", "score": 20})
	notePlay(M{"overs": 5.0, "score": 20})
	playMu.Lock()
	b, at := playBalls, playChangeAt
	playChangeAt = 0
	playMu.Unlock()
	mustEq(t, b, 30)
	if now()-at > 5 {
		t.Fatal(at)
	}
	notePlay(M{"overs": 5.0, "score": 21}) // a wide to start the next over: play, not a break
	playMu.Lock()
	at = playChangeAt
	playMu.Unlock()
	if now()-at > 5 {
		t.Fatal("a wide didn't count as play")
	}
}

// tickAt runs one monitor tick against an OBS that's live and clean at kbps.
func tickAt(t *testing.T, kbps int) (int, string) {
	t.Helper()
	saved := obsCall
	t.Cleanup(func() { obsCall = saved })
	streamMonMu.Lock()
	n := len(streamSamples)
	streamMonMu.Unlock()
	bytes := (n + 1) * 15 * kbps * 1000 / 8
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		return []map[string]any{{"outputActive": true, "outputCongestion": 0.0, "outputTotalFrames": 375 * (n + 1),
			"outputSkippedFrames": 0, "outputBytes": bytes}, {"parameterValue": strconv.Itoa(kbps)},
			{"parameterValue": "true"}, {"parameterValue": "Simple"}}
	}
	return streamMonitorTick()
}

func TestAutoStepsUpOneRungAtTheEndOfAnOver(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"stream_auto_downshift": true})
	resetStreamMon(t, M{"streaming": true, "baseline_kbps": 4000, "step": 2, "current_kbps": 2000,
		"last_shift_at": now() - 900, "clean_since": now() - 700})
	streamMonMu.Lock()
	streamSamples = cleanSamples(now()-15, 2000, 0)
	streamMonMu.Unlock()

	setPlay(t, 27, 10, 2) // mid-over: wait
	if step, _ := tickAt(t, 2000); step >= 0 {
		t.Fatal("stepped up mid-over", step)
	}
	if !strings.Contains(gs(streamMonitorStatus(), "upshift", ""), "break in play") {
		t.Fatal(streamMonitorStatus()["upshift"])
	}
	setPlay(t, 30, 5, 2) // over complete
	step, reason := tickAt(t, 2000)
	mustEq(t, step, 1) // one rung, not straight to full
	if !strings.Contains(reason, "end of the over") {
		t.Fatal(reason)
	}
}

func TestAutoNeverStepsUpWithAutoOff(t *testing.T) {
	sandbox(t, "")
	resetStreamMon(t, M{"streaming": true, "baseline_kbps": 4000, "step": 2, "current_kbps": 2000,
		"clean_since": now() - 7000})
	streamMonMu.Lock()
	streamSamples = cleanSamples(now()-15, 2000, 0)
	streamMonMu.Unlock()
	setPlay(t, 30, 5, 2)
	if step, _ := tickAt(t, 2000); step >= 0 {
		t.Fatal("stepped up with auto off", step)
	}
}

func TestFailedStepUpDoublesTheWait(t *testing.T) {
	sandbox(t, "")
	resetStreamMon(t, M{"baseline_kbps": 4000, "step": 2, "current_kbps": 2000})
	saved := obsCall
	t.Cleanup(func() { obsCall = saved })
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		out := make([]map[string]any, len(reqs)) // not live: just the bitrate is set
		for i := range out {
			out[i] = map[string]any{"outputActive": false, "parameterValue": "Simple"}
		}
		return out
	}
	shift := func(step int) string {
		ok, msg := ApplyStreamQualityStep(step, "test", "auto")
		if !ok {
			t.Fatal(msg)
		}
		return msg
	}
	if msg := shift(1); !strings.Contains(msg, "raised") {
		t.Fatal(msg)
	}
	shift(2) // straight back down: the line couldn't take it
	mustEq(t, monGet("upshift_wait"), any(2*StreamUpshiftAfter))
	shift(1)
	shift(2)
	mustEq(t, monGet("upshift_wait"), any(4*StreamUpshiftAfter))
	for i := 0; i < 5; i++ {
		shift(1)
		shift(2)
	}
	mustEq(t, monGet("upshift_wait"), any(StreamUpshiftMaxWait))

	// A step down long after a step up that held: the line got worse later, start again
	shift(1)
	streamMonMu.Lock()
	streamMon["last_shift_at"] = now() - StreamUpshiftFailSec - 1
	streamMonMu.Unlock()
	shift(2)
	mustEq(t, monGet("upshift_wait"), any(StreamUpshiftAfter))
	if msg := shift(0); !strings.Contains(msg, "restored") {
		t.Fatal(msg)
	}
}

// ── The replay buffer shares the stream's encoder (measured on OBS 33) ──

func TestShiftStopsAndRestartsTheReplayBuffer(t *testing.T) {
	// With the buffer running, OBS keeps the old encoder — and its bitrate — through a
	// stream restart. The buffer has to stop too, and start again AFTER the stream.
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": true}, {"outputActive": false}},
		{{}},                      // StopStream
		{{"outputActive": false}}, // stream wound down
		{{}},                      // StopReplayBuffer
		{{"outputActive": false}}, // buffer stopped
		{{}},                      // SetProfileParameter
		{{"outputWidth": 1280, "outputHeight": 720}}, // GetVideoSettings: 720p, no change
		{{}},                     // StartStream
		{{"outputActive": true}}, // stayed up
		{{}},                     // StartReplayBuffer
		{{"outputActive": true}}, // buffer running again
	}, 1, "test", "auto")
	if !ok || strings.Contains(msg, "replay buffer didn't") {
		t.Fatal(ok, msg)
	}
	eqv(t, types(calls)[3:], [][]string{{"StopReplayBuffer"}, {"GetReplayBufferStatus"}, {"SetProfileParameter"},
		{"GetVideoSettings"}, {"StartStream"}, {"GetStreamStatus"}, {"StartReplayBuffer"}, {"GetReplayBufferStatus"}})
}

func TestShiftRefusedWhileRecording(t *testing.T) {
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": true}, {"outputActive": true}},
	}, 1, "test", "auto")
	if ok || !strings.Contains(msg, "recording") {
		t.Fatal(ok, msg)
	}
	mustEq(t, len(calls), 1) // nothing stopped
}

func TestShiftBufferThatWontStopChangesNothing(t *testing.T) {
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": true}, {"parameterValue": "Simple"}, {"outputActive": true}, {"outputActive": false}},
		{{}}, {{"outputActive": false}}, // stream stopped
		{nil}, // StopReplayBuffer refused, and it stays running:
		{{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}},
		{{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}},
		{{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}},
		{{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}},
		{{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}}, {{"outputActive": true}},
		{{"outputActive": true}},       // and is it running? yes: nothing to start again
		{{}}, {{"outputActive": true}}, // back on air
	}, 1, "test", "auto")
	if ok || !strings.Contains(msg, "no quality change") || !strings.Contains(msg, "restarted at the old quality") {
		t.Fatal(ok, msg)
	}
	for _, c := range types(calls) {
		if c[0] == "SetProfileParameter" {
			t.Fatal("set a bitrate that couldn't take")
		}
	}
	eqv(t, monGet("step"), 0)
}

func TestReplaySkippedNotRestartedDuringAShift(t *testing.T) {
	srv, _ := replayRig(t, false, false)
	shifting.Store(true)
	defer shifting.Store(false)
	ObsTriggerReplay(LoadState(), "Boundary")
	if has(srv.Types(), "StartReplayBuffer") {
		t.Fatal("restarted the buffer in the middle of a quality step")
	}
}

// ── 480p at the bottom rung ──

func TestBottomRungDropsTo480pAndRestoreBringsItBack(t *testing.T) {
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": false}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}},
		{{}}, // SetProfileParameter
		{{"outputWidth": 1280, "outputHeight": 720}},
		{{}}, // SetVideoSettings
	}, 3, "test", "manual")
	if !ok || !strings.Contains(msg, "480p") {
		t.Fatal(ok, msg)
	}
	eqv(t, calls[3][0].data, M{"outputWidth": 854, "outputHeight": 480})

	saved := obsCall
	t.Cleanup(func() { obsCall = saved })
	var sent []obsws.Req
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		sent = append(sent, reqs...)
		out := make([]map[string]any, len(reqs))
		for i, r := range reqs {
			out[i] = map[string]any{"outputActive": false, "parameterValue": "Simple"}
			if r.Type == "GetVideoSettings" {
				out[i] = map[string]any{"outputWidth": 854, "outputHeight": 480}
			}
		}
		return out
	}
	if ok, msg := ApplyStreamQualityStep(0, "test", "manual"); !ok || !strings.Contains(msg, "720p") {
		t.Fatal(ok, msg)
	}
	last := sent[len(sent)-1]
	eqv(t, last.Data, M{"outputWidth": 1280, "outputHeight": 720})
}

func TestBottomRungLeavesALowResolutionAlone(t *testing.T) {
	ok, msg, calls := runShift(t, [][]map[string]any{
		{{"outputActive": false}, {"parameterValue": "Simple"}, {"outputActive": false}, {"outputActive": false}},
		{{}},
		{{"outputWidth": 854, "outputHeight": 480}}, // the club streams at 480p already
	}, 3, "test", "manual")
	if !ok || strings.Contains(msg, "p,") {
		t.Fatal(ok, msg)
	}
	for _, c := range types(calls) {
		if c[0] == "SetVideoSettings" {
			t.Fatal("changed a 480p stream's resolution")
		}
	}
}

// ── What OBS really sends ──

func TestOverSendingIsWarnedOnceAndShown(t *testing.T) {
	sandbox(t, "")
	resetStreamMon(t, M{"streaming": true, "baseline_kbps": 4000, "step": 1, "current_kbps": 2800})
	for i := 0; i < 8; i++ {
		streamMonMu.Lock()
		streamSamples = cleanSamples(now(), 6000, 0) // set to 2800, sending 6000
		noteSentLocked(now(), 2800)
		streamMonMu.Unlock()
	}
	m := streamMonitorStatus()
	if n := gi(m, "sent_kbps", 0); n < 5800 || n > 6200 {
		t.Fatal(m["sent_kbps"])
	}
	if !strings.Contains(gs(m, "bitrate_warning", ""), "far more than the 2800 kbps") {
		t.Fatal(m["bitrate_warning"])
	}
	mustEq(t, monGet("over_ticks"), any(8)) // warned at the 4th only: once per setting
}

func TestSendingLessIsNotWarned(t *testing.T) {
	sandbox(t, "")
	resetStreamMon(t, M{"streaming": true, "baseline_kbps": 4000, "step": 0, "current_kbps": 4000})
	for i := 0; i < 8; i++ {
		streamMonMu.Lock()
		streamSamples = cleanSamples(now(), 2300, 0) // Dynamic Bitrate holding it to a slow line
		noteSentLocked(now(), 4000)
		streamMonMu.Unlock()
	}
	if w := gs(streamMonitorStatus(), "bitrate_warning", ""); w != "" {
		t.Fatal(w)
	}
}

// ── The ladder's place survives a server restart ──

func TestLadderPlaceIsSavedAndClearedOnRestore(t *testing.T) {
	sandbox(t, "")
	resetStreamMon(t, M{"baseline_kbps": 4000, "step": 0, "current_kbps": 4000, "base_res": []int{1280, 720}})
	saved := obsCall
	t.Cleanup(func() { obsCall = saved })
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		out := make([]map[string]any, len(reqs)) // not live: just the bitrate is set
		for i := range out {
			out[i] = map[string]any{"outputActive": false, "parameterValue": "Simple"}
		}
		return out
	}
	if ok, msg := ApplyStreamQualityStep(2, "test", "manual"); !ok {
		t.Fatal(msg)
	}
	lad, _ := LoadState()["stream_ladder"].(map[string]any)
	if lad == nil || gi(lad, "step", 0) != 2 || gi(lad, "kbps", 0) != 2000 || gi(lad, "baseline_kbps", 0) != 4000 {
		t.Fatal(lad)
	}
	if ok, msg := ApplyStreamQualityStep(0, "test", "manual"); !ok {
		t.Fatal(msg)
	}
	if lad := LoadState()["stream_ladder"]; lad != nil {
		t.Fatal("still saved after restoring:", lad)
	}
}

func TestRestartedServerPicksTheLadderUp(t *testing.T) {
	sandbox(t, "")
	// What a server that crashed at the bottom rung left in the file (read back as JSON).
	setState(t, M{"stream_ladder": map[string]any{"step": 3.0, "baseline_kbps": 4000.0, "kbps": 1400.0,
		"base_res": []any{1280.0, 720.0}, "at": now() - 60}})
	resetStreamMon(t, M{})
	tickAt(t, 1400) // OBS still at the ladder's 1400
	mustEq(t, monGet("step"), any(3))
	mustEq(t, monGet("baseline_kbps"), any(4000))
	mustEq(t, monGet("current_kbps"), any(1400))
	eqv(t, monGet("base_res"), []int{1280, 720})
	if ok, msg := runShiftKeep(t, 0); !ok || !strings.Contains(msg, "4000") {
		t.Fatal("Restore didn't go back to the full bitrate:", ok, msg)
	}
}

func TestLadderRecordIgnoredWhenOBSMovedOn(t *testing.T) {
	for name, lad := range map[string]map[string]any{
		"OBS at another bitrate": {"step": 2.0, "baseline_kbps": 4000.0, "kbps": 2000.0, "at": now() - 60},
		"yesterday's":            {"step": 2.0, "baseline_kbps": 4000.0, "kbps": 4000.0, "at": now() - 13*3600},
	} {
		t.Run(name, func(t *testing.T) {
			sandbox(t, "")
			setState(t, M{"stream_ladder": lad})
			resetStreamMon(t, M{})
			tickAt(t, 4000)
			mustEq(t, monGet("step"), any(0))
			mustEq(t, monGet("baseline_kbps"), any(4000))
		})
	}
}

func TestBitrateChangedInOBSRestartsTheLadder(t *testing.T) {
	sandbox(t, "")
	resetStreamMon(t, M{"streaming": true, "baseline_kbps": 4000, "step": 2, "current_kbps": 2000,
		"last_shift_at": now() - 900})
	tickAt(t, 2000)
	tickAt(t, 3500) // one disagreeing read could be a step landing mid-tick
	mustEq(t, monGet("step"), any(2))
	tickAt(t, 3500)
	mustEq(t, monGet("step"), any(0))
	tickAt(t, 3500)
	mustEq(t, monGet("baseline_kbps"), any(3500)) // the operator's bitrate is the full one now
}

func TestHealthDoesNotCallALadderStepALeftover(t *testing.T) {
	sandbox(t, "")
	st := M{"network_test_mbps": 10.0}
	rec := obs.RecommendStreamSettings(10).BitrateKbps
	low := rec / 2
	resetStreamMon(t, M{"reachable": true, "simple_mode": true, "configured_kbps": low, "step": 0, "current_kbps": nil})
	if r := obsBitrateSanityCheck(st); r["suspicious"] != true {
		t.Fatal("a low bitrate with the ladder at full wasn't flagged:", r)
	}
	resetStreamMon(t, M{"reachable": true, "simple_mode": true, "configured_kbps": low, "step": 2, "current_kbps": low})
	if r := obsBitrateSanityCheck(st); r["suspicious"] != false || !strings.Contains(gs(r, "note", ""), "quality ladder") {
		t.Fatal("the ladder's own step was flagged:", r)
	}
}

// runShiftKeep applies a step against a not-live OBS without resetting the monitor.
func runShiftKeep(t *testing.T, step int) (bool, string) {
	t.Helper()
	saved := obsCall
	t.Cleanup(func() { obsCall = saved })
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		out := make([]map[string]any, len(reqs))
		for i := range out {
			out[i] = map[string]any{"outputActive": false, "parameterValue": "Simple"}
		}
		return out
	}
	return ApplyStreamQualityStep(step, "test", "manual")
}

func TestNoStepUpWhileAReplayIsOnAir(t *testing.T) {
	sandbox(t, "")
	setPlay(t, 30, 5, 2) // the over just ended: a break...
	if ok, _ := playBreak(now()); !ok {
		t.Fatal("end of the over wasn't a break")
	}
	noteReplay(30) // ...but its last ball went for four, and the replay is on
	if ok, why := playBreak(now()); ok {
		t.Fatal("a step up was allowed during a replay:", why)
	}
}
