package server

// Adaptive stream quality: the pure downshift decision (hysteresis, cooldown), the
// bitrate ladder, and the HTTP surface — everything except talking to a real OBS
// (tests/test_stream_quality.py).

import (
	"strings"
	"testing"
	"time"

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
		"dynamic_bitrate": nil, "simple_mode": nil}) {
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
		{{"outputActive": true}, {"parameterValue": "Simple"}}, // status + mode
		{{}},                      // StopStream
		{{"outputActive": false}}, // wind-down confirmed
		{{}},                      // SetProfileParameter
		{{}},                      // StartStream
		{{"outputActive": true}},  // ...and it STAYED up
	}, 1, "sustained congestion", "auto")
	if !ok || !strings.Contains(msg, "2800") { // 70% of the 4000 baseline
		t.Fatal(ok, msg)
	}
	eqv(t, types(calls), [][]string{{"GetStreamStatus", "GetProfileParameter"}, {"StopStream"},
		{"GetStreamStatus"}, {"SetProfileParameter"}, {"StartStream"}, {"GetStreamStatus"}})
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
		{{"outputActive": true}, {"parameterValue": "Simple"}},
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
		{{"outputActive": true}, {"parameterValue": "Simple"}}, {{}}, {{"outputActive": false}}, {{}},
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
		{{"outputActive": true}, {"parameterValue": "Simple"}}, {{}}, {{"outputActive": false}}, {{}},
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
		{{"outputActive": false}, {"parameterValue": "Simple"}},
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
