package server

// OBS crash recovery (obsGuardTick) — reopening OBS mid-match with nobody there
// (tests/test_obs_guard.py). The process, launch and WebSocket are faked; the crash
// marker is a real file in a temp folder, because "crashed vs. closed on purpose" is
// decided by whether OBS left it behind.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

type guardFake struct {
	t        *testing.T
	cfg      string
	running  bool
	hung     *bool // nil = can't tell
	calls    [][]obsws.Req
	launches []string
	kills    int
	pidCalls int
	runCalls int
	waitWS   bool
	obsFn    func(reqs []obsws.Req) []map[string]any
	isLive   func() (bool, bool)
}

func bp(b bool) *bool { return &b }

func newGuardFake(t *testing.T) *guardFake {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"obs_host": "localhost", "obs_port": 4455, "obs_main_scene": "Main"})
	f := &guardFake{t: t, cfg: t.TempDir(), running: true, hung: bp(false), waitWS: true}
	saved := struct {
		call      func(M, []obsws.Req, time.Duration) []map[string]any
		port      func(int) bool
		hung      func([]int) (bool, bool)
		kill      func()
		pids      func() []int
		sleep     func(time.Duration)
		launch    func(string, []string) error
		wait      func(int, time.Duration) bool
		streaming func(M) (bool, bool)
		running   func() bool
		cfgDir    func() string
		find      func() string
	}{obsCall, guardPortOpen, guardWindowHung, guardKill, guardPIDs, guardSleep, guardLaunch, guardWaitWS,
		guardStreaming, obsRunning, obs.ConfigDir, obs.FindOBS}
	t.Cleanup(func() {
		obsCall, guardPortOpen, guardWindowHung, guardKill, guardPIDs, guardSleep, guardLaunch, guardWaitWS,
			guardStreaming, obsRunning, obs.ConfigDir, obs.FindOBS = saved.call, saved.port, saved.hung, saved.kill,
			saved.pids, saved.sleep, saved.launch, saved.wait, saved.streaming, saved.running, saved.cfgDir, saved.find
	})
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		f.calls = append(f.calls, reqs)
		if f.obsFn != nil {
			return f.obsFn(reqs)
		}
		out := make([]map[string]any, len(reqs)) // one answer per request, as OBS gives
		for i := range out {
			out[i] = map[string]any{}
		}
		return out
	}
	guardPortOpen = func(int) bool { return f.running }
	obsRunning = func() bool { f.runCalls++; return f.running }
	guardWindowHung = func([]int) (bool, bool) {
		if f.hung == nil {
			return false, false
		}
		return *f.hung, true
	}
	guardKill = func() { f.kills++; f.crash() }
	guardPIDs = func() []int { f.pidCalls++; return []int{4242} }
	guardSleep = func(time.Duration) {}
	guardLaunch = func(exe string, _ []string) error {
		if len(f.markers()) != 0 {
			t.Error("launched with the Safe Mode marker still there")
		}
		f.launches = append(f.launches, exe)
		return nil
	}
	guardWaitWS = func(int, time.Duration) bool { return f.waitWS }
	// The guard asks OBS "are you live?" on every check: answer from the monitor flag
	// the test sets, and no answer once OBS has gone.
	f.isLive = func() (bool, bool) {
		if !f.running {
			return false, false
		}
		streamMonMu.Lock()
		defer streamMonMu.Unlock()
		return gb(streamMon, "streaming", false), true
	}
	guardStreaming = func(M) (bool, bool) { return f.isLive() }
	obs.ConfigDir = func() string { return f.cfg }
	obs.FindOBS = func() string { return "/fake/obs64.exe" }
	ResetOBSGuard()
	t.Cleanup(ResetOBSGuard)
	streamMonMu.Lock()
	savedMon := shallow(streamMon)
	streamMonMu.Unlock()
	t.Cleanup(func() {
		streamMonMu.Lock()
		streamMon = savedMon
		streamMonMu.Unlock()
	})
	f.mon(true, false)
	return f
}

func (f *guardFake) mon(reachable, streaming bool) {
	streamMonMu.Lock()
	streamMon["reachable"], streamMon["streaming"] = reachable, streaming
	streamMonMu.Unlock()
}

// crash: OBS dies leaving its marker behind, as a crash or force-close does.
func (f *guardFake) crash() {
	dir := filepath.Join(f.cfg, ".sentinel")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "run_c887aaf3-ecd5-4b72-a795-6f49aaefc6f1"), nil, 0o644)
	f.running = false
	f.mon(false, false) // the stream monitor notices first
}

func (f *guardFake) markers() []string {
	entries, _ := os.ReadDir(filepath.Join(f.cfg, ".sentinel"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func (f *guardFake) lastSent() []string {
	if len(f.calls) == 0 {
		return nil
	}
	var out []string
	for _, r := range f.calls[len(f.calls)-1] {
		out = append(out, r.Type)
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestGuardCrashReopensWithoutSafeModeMarker(t *testing.T) {
	f := newGuardFake(t)
	mustEq(t, obsGuardTick(0), "armed")
	f.crash()
	mustEq(t, obsGuardTick(0), "restarted")
	eqv(t, f.launches, []string{"/fake/obs64.exe"})
}

func TestGuardStreamRestartedIfItWasLive(t *testing.T) {
	f := newGuardFake(t)
	f.mon(true, true)
	obsGuardTick(0)
	f.crash() // the monitor now says streaming=false — must not erase "it was live"
	obsGuardTick(0)
	eqv(t, f.lastSent(), []string{"SetCurrentProgramScene", "StartStream"})
	eqv(t, f.calls[len(f.calls)-1][0].Data, M{"sceneName": "Main"})
}

func TestGuardStreamNotStartedIfItWasntLive(t *testing.T) {
	// A crash before kick-off must not put the club on air.
	f := newGuardFake(t)
	obsGuardTick(0)
	f.crash()
	obsGuardTick(0)
	if has(f.lastSent(), "StartStream") {
		t.Fatal(f.lastSent())
	}
}

func TestGuardNormalCloseLeftClosed(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(0)
	f.running = false // clean exit: no marker left
	mustEq(t, obsGuardTick(0), "closed")
	mustEq(t, obsGuardTick(0), "") // and stays disarmed
	mustEq(t, len(f.launches), 0)
}

func TestGuardNormalClosePutsUpdateCheckBack(t *testing.T) {
	f := newGuardFake(t)
	obs.PauseUpdateCheck(f.cfg)
	obsGuardTick(0)
	f.running = false
	obsGuardTick(0)
	if fileExists(filepath.Join(f.cfg, obs.UpdatesMarker)) {
		t.Fatal("marker left")
	}
	v, _ := obs.ReadINIValue(filepath.Join(f.cfg, "global.ini"), "General", "EnableAutoUpdates")
	mustEq(t, v, "true")
}

func TestGuardCrashLeavesUpdateCheckPaused(t *testing.T) {
	// Reopened mid-match: the pop-up must stay away.
	f := newGuardFake(t)
	obs.PauseUpdateCheck(f.cfg)
	obsGuardTick(0)
	f.crash()
	obsGuardTick(0)
	if !fileExists(filepath.Join(f.cfg, obs.UpdatesMarker)) {
		t.Fatal("update check resumed after a crash")
	}
}

func TestGuardReopenedByHandWatchedAgain(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(0)
	f.running = false
	obsGuardTick(0) // closed normally
	f.running = true
	mustEq(t, obsGuardTick(0), "armed")
}

func TestGuardOBSNeverSeenNotStarted(t *testing.T) {
	// Server up but OBS never opened this session (or it's on another computer).
	f := newGuardFake(t)
	f.running = false
	f.crash()
	mustEq(t, obsGuardTick(0), "")
	mustEq(t, len(f.launches), 0)
}

func TestGuardCrashLoopCapped(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(0)
	var results []string
	for i := 0; i <= obsGuardMaxRestarts; i++ {
		f.crash()
		results = append(results, obsGuardTick(float64(1000+i)))
		f.running = true
		obsGuardTick(float64(1000 + i))
	}
	for _, r := range results[:len(results)-1] {
		mustEq(t, r, "restarted")
	}
	mustEq(t, results[len(results)-1], "gave_up")
	mustEq(t, len(f.launches), obsGuardMaxRestarts)
	mustEq(t, obsGuardStatus()["gave_up"], any(true))
}

func TestGuardOldCrashesAgeOutOfCap(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(0)
	for i := 0; i < obsGuardMaxRestarts; i++ {
		f.crash()
		obsGuardTick(float64(1000 + i))
		f.running = true
		obsGuardTick(float64(1000 + i))
	}
	f.crash()
	mustEq(t, obsGuardTick(float64(1000+obsGuardWindowSec+10)), "restarted")
}

func TestGuardOBSThatDoesntComeBackIsReported(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(0)
	f.crash()
	f.waitWS = false
	mustEq(t, obsGuardTick(0), "restart_failed")
	mustEq(t, len(f.calls), 0)
}

func TestGuardSlowReopenStillGetsItsStreamBack(t *testing.T) {
	// Reopened OBS took longer than the wait to answer. When it does, the match that was
	// on air goes back on air — not read as "OBS says it isn't live".
	f := newGuardFake(t)
	f.mon(true, true)
	obsGuardTick(0)
	f.crash()
	f.waitWS = false
	mustEq(t, obsGuardTick(0), "restart_failed")
	f.running = true // it answers at last, not streaming
	mustEq(t, obsGuardTick(0), "restarted")
	eqv(t, f.lastSent(), []string{"SetCurrentProgramScene", "StartStream"})
	mustEq(t, len(f.launches), 1)
}

func TestGuardFailedReopenIsRetriedNotTakenForAClose(t *testing.T) {
	// The crash marker is cleared before the reopen, so an OBS that never came up looked
	// like one closed on purpose, and the guard stopped watching.
	f := newGuardFake(t)
	obsGuardTick(0)
	f.crash()
	f.waitWS = false
	mustEq(t, obsGuardTick(0), "restart_failed")
	f.waitWS = true
	mustEq(t, obsGuardTick(0), "restarted") // OBS still down, no marker: reopened again
	mustEq(t, len(f.launches), 2)
}

func TestGuardRefusedStreamRestartAskedAgainThenLeft(t *testing.T) {
	f := newGuardFake(t)
	f.mon(true, true)
	obsGuardTick(0)
	f.crash()
	f.obsFn = func(reqs []obsws.Req) []map[string]any {
		out := make([]map[string]any, len(reqs))
		for i, r := range reqs {
			if r.Type != "StartStream" {
				out[i] = map[string]any{}
			}
		}
		return out
	}
	mustEq(t, obsGuardTick(0), "restore_pending")
	f.running = true
	for i := 2; i < obsGuardRestorePasses; i++ {
		mustEq(t, obsGuardTick(0), "restore_pending")
	}
	mustEq(t, obsGuardTick(0), "restart_failed")
	f.obsFn = nil
	if r := obsGuardTick(0); r == "restarted" || r == "restore_pending" {
		t.Fatal("still retrying after giving up:", r)
	}
}

func TestGuardDisabledByConfigOrRemoteOBS(t *testing.T) {
	newGuardFake(t)
	if !obsGuardEnabled() {
		t.Fatal("should be enabled by default")
	}
	os.WriteFile(ConfigFile, []byte("[OBS]\nmanage_obs = no\n"), 0o644)
	if obsGuardEnabled() {
		t.Fatal("manage_obs = no ignored")
	}
	os.Remove(ConfigFile)
	setState(t, M{"obs_host": "192.168.1.50"})
	if obsGuardEnabled() {
		t.Fatal("guarding an OBS on another computer")
	}
}

// ── Frozen ("Not Responding") ──

// streamStatus makes GetStreamStatus return these (outputActive, outputBytes) in turn;
// a nil reading is no answer.
func (f *guardFake) streamStatus(readings ...[]any) {
	i := 0
	f.obsFn = func(reqs []obsws.Req) []map[string]any {
		if reqs[0].Type == "GetStreamStatus" {
			if i >= len(readings) || readings[i] == nil {
				i++
				return nil
			}
			r := readings[i]
			i++
			return []map[string]any{{"outputActive": r[0], "outputBytes": r[1]}}
		}
		return []map[string]any{{}}
	}
}

func TestGuardBriefFreezeWaitedOut(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(1000)
	f.hung = bp(true)
	mustEq(t, obsGuardTick(1005), "")
	mustEq(t, obsGuardTick(1005+obsFreezeSec-1), "")
	f.hung = bp(false)
	mustEq(t, obsGuardTick(1100), "")
	f.hung = bp(true) // a new freeze starts the clock again
	obsGuardTick(1105)
	mustEq(t, obsGuardTick(1105+obsFreezeSec-1), "")
	mustEq(t, f.kills, 0)
}

func TestGuardFrozenNotStreamingClosedThenReopened(t *testing.T) {
	f := newGuardFake(t)
	f.streamStatus([]any{false, 0})
	obsGuardTick(1000)
	f.hung = bp(true)
	obsGuardTick(1005)
	mustEq(t, obsGuardTick(1005+obsFreezeSec), "frozen_killed")
	mustEq(t, f.kills, 1)
	// The force-close left the crash marker, so the next check reopens it.
	f.hung = bp(false)
	mustEq(t, obsGuardTick(1070), "restarted")
	mustEq(t, len(f.launches), 1)
}

func TestGuardFrozenWindowOverFlowingStreamLeftAlone(t *testing.T) {
	// Encoding runs off the UI thread: viewers may see nothing wrong. Killing OBS would
	// cause the outage this exists to prevent.
	f := newGuardFake(t)
	f.streamStatus([]any{true, 1000}, []any{true, 5000}, []any{true, 9000}, []any{true, 13000})
	obsGuardTick(1000)
	f.hung = bp(true)
	obsGuardTick(1005)
	tm := float64(1005 + obsFreezeSec)
	mustEq(t, obsGuardTick(tm), "frozen_streaming")
	n := len(f.calls) // not re-measured every tick...
	mustEq(t, obsGuardTick(tm+5), "frozen_streaming")
	mustEq(t, len(f.calls), n)
	// ...but it is periodically, and still left alone while bytes keep moving.
	mustEq(t, obsGuardTick(tm+obsFreezeRecheckSec), "frozen_streaming")
	mustEq(t, f.kills, 0)
}

func TestGuardFrozenStalledStreamClosedAndRestarted(t *testing.T) {
	f := newGuardFake(t)
	f.mon(true, true)
	obsGuardTick(1000)
	f.streamStatus([]any{true, 5000}, []any{true, 5000}) // live, but nothing moving
	f.hung = bp(true)
	obsGuardTick(1005)
	mustEq(t, obsGuardTick(1005+obsFreezeSec), "frozen_killed")
	f.hung = bp(false)
	obsGuardTick(1070)
	if !has(f.lastSent(), "StartStream") {
		t.Fatal(f.lastSent())
	}
}

func TestGuardFrozenAndNotAnsweringClosed(t *testing.T) {
	f := newGuardFake(t)
	f.streamStatus(nil) // the WebSocket doesn't answer either
	obsGuardTick(1000)
	f.hung = bp(true)
	obsGuardTick(1005)
	mustEq(t, obsGuardTick(1005+obsFreezeSec), "frozen_killed")
}

func TestGuardCantTellIsNotAFreeze(t *testing.T) {
	// Mac/Linux, or no OBS window found: never acted on.
	f := newGuardFake(t)
	obsGuardTick(1000)
	f.hung = nil
	for tm := 1005; tm < 1300; tm += 5 {
		mustEq(t, obsGuardTick(float64(tm)), "")
	}
	mustEq(t, f.kills, 0)
}

// ── From the code review ──

func TestGuardStandsAsideWhileStartOBSRuns(t *testing.T) {
	// Start OBS closes and reopens OBS on purpose on a first run; the guard must not read
	// that as "closed normally" (disarming, and switching updates back on).
	f := newGuardFake(t)
	obsGuardTick(0)
	checklistJobMu.Lock()
	saved := checklistJob
	checklistJob = M{"running": true}
	checklistJobMu.Unlock()
	t.Cleanup(func() {
		checklistJobMu.Lock()
		checklistJob = saved
		checklistJobMu.Unlock()
	})
	f.running = false
	mustEq(t, obsGuardTick(0), "")
	obsGuardMu.Lock()
	armed := obsGuard.armed
	obsGuardMu.Unlock()
	mustEq(t, armed, true)
}

func TestGuardStreamStoppedJustBeforeCrashStaysStopped(t *testing.T) {
	// The monitor's flag lags up to 15s; OBS itself is asked on every check.
	f := newGuardFake(t)
	f.mon(true, true)
	obsGuardTick(0)
	live := f.isLive
	f.isLive = func() (bool, bool) { return false, true } // the operator pressed Stop
	obsGuardTick(0)
	f.isLive = live
	f.crash() // the monitor has nothing fresher
	f.mon(false, true)
	obsGuardTick(0)
	if has(f.lastSent(), "StartStream") {
		t.Fatal("put the club back on air")
	}
}

func TestGuardNoProcessCheckForOBSNeverSeen(t *testing.T) {
	f := newGuardFake(t)
	f.running = false
	for i := 0; i < 5; i++ {
		obsGuardTick(0)
	}
	mustEq(t, f.runCalls, 0)
}

func TestGuardFreezeCheckReusesProcessIDs(t *testing.T) {
	f := newGuardFake(t)
	obsGuardTick(1000)
	for tm := 1005; tm < 1050; tm += 5 {
		obsGuardTick(float64(tm))
	}
	mustEq(t, f.pidCalls, 1)
}

func TestGuardHealthReportsIt(t *testing.T) {
	newTestServer(t, "")
	s := serve(t)
	_, body := s.getJSON("/health")
	rec := gm(body, "obs_recovery")
	for _, k := range []string{"enabled", "watching", "gave_up", "last_event"} {
		if _, ok := rec[k]; !ok {
			t.Fatal(k)
		}
	}
}
