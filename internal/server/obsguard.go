package server

// OBS crash recovery. If OBS crashes mid-match, nobody may be at the laptop to reopen it,
// and reopening it from the desktop icon stops on OBS's "Run in Safe Mode?" prompt
// anyway. This reopens it the way the launcher does (clear the crash marker, launch, wait
// for the WebSocket), puts the Main scene back, and restarts the stream if it was live.
//
// Crash vs. closed on purpose is OBS's own call, not a guess: a clean exit deletes its
// .sentinel/run_* marker, a crash or force-close leaves it — so closing OBS at the end of
// the match never makes it pop back up. Polled every 5s rather than on the 90s watchdog:
// every second OBS is down, the stream is down.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

const (
	obsGuardPollSec     = 5
	obsGuardMaxRestarts = 3 // within obsGuardWindowSec; a crash loop needs a human
	obsGuardWindowSec   = 1800
	obsGuardStartupSec  = 45
	// Frozen ("Not Responding") this long before it's treated as dead: long enough that a
	// slow start or heavy scene switch never qualifies; short against a match.
	obsFreezeSec        = 60
	obsFreezeRecheckSec = 30 // frozen window, stream still flowing: look again this often
)

type obsGuardT struct {
	armed, streaming, gaveUp bool
	// A reopen whose Main scene / stream restart hasn't been sent yet: OBS was slower
	// than obsGuardStartupSec to answer, or didn't start at all. Its crash marker is
	// already cleared, so without this the next check took OBS being down for a normal
	// close and stopped watching — and an OBS that answered late was taken as "not live",
	// so a match that was on air stayed off.
	pending                  bool
	pendingStream            bool
	pendingTries             int
	restarts                 []float64
	lastEvent                M
	enabled                  any
	hungSince, freezeChecked float64 // 0 = not set
	pids                     []int
}

var (
	obsGuard   obsGuardT
	obsGuardMu sync.Mutex
)

// Swappable for tests: everything that touches the real OBS process.
var (
	guardPortOpen   = func(port int) bool { return obs.PortOpen(port, "", time.Second) }
	guardWindowHung = func(pids []int) (bool, bool) { return obs.WindowHung(5*time.Second, pids) }
	guardKill       = obs.Kill
	guardPIDs       = obs.PIDs
	guardSleep      = time.Sleep
	guardLaunch     = func(exe string, args []string) error { return obs.Launch(exe, args) }
	guardWaitWS     = func(port int, t time.Duration) bool { return obs.WaitForWebsocket(port, t) }
	guardStreaming  = obsIsStreaming
)

func obsGuardEnabled() bool {
	if !obs.ManageOBSEnabled(ConfigFile) {
		return false
	}
	// Only an OBS on this machine: a process check can't see one on another computer.
	return obsIsLocal(LoadState())
}

func obsGuardEvent(what string) {
	obsGuardMu.Lock()
	obsGuard.lastEvent = M{"time": now(), "what": what}
	obsGuardMu.Unlock()
}

// obsIsStreaming asks OBS itself: (live, answered).
func obsIsStreaming(st M) (bool, bool) {
	r := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 3*time.Second)
	if r == nil || r[0] == nil {
		return false, false
	}
	return gb(r[0], "outputActive", false), true
}

// obsStreamFlowing: is OBS still sending the stream? Two readings a few seconds apart;
// true only if live AND the byte count moved. Anything else is "can't confirm", which is
// all a frozen OBS gets the benefit of the doubt for.
func obsStreamFlowing(st M) bool {
	first := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
	if first == nil || first[0] == nil || !gb(first[0], "outputActive", false) {
		return false
	}
	guardSleep(5 * time.Second)
	second := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}}, 5*time.Second)
	if second == nil || second[0] == nil || !gb(second[0], "outputActive", false) {
		return false
	}
	return gf(second[0], "outputBytes", 0) > gf(first[0], "outputBytes", 0)
}

// obsFreezeCheck runs while OBS is running: "frozen_killed", "frozen_streaming" or "".
// Kills only when BOTH OBS's window has been Not Responding for obsFreezeSec AND the
// stream isn't visibly sending. A frozen window over a stream still going out is left
// alone: encoding runs on its own threads, so viewers may see nothing wrong, and killing
// OBS would turn that into a real outage.
func obsFreezeCheck(st M, t float64) string {
	obsGuardMu.Lock()
	pids := obsGuard.pids
	obsGuardMu.Unlock()
	if len(pids) == 0 {
		pids = guardPIDs()
	}
	hung, known := guardWindowHung(pids)
	obsGuardMu.Lock()
	// Keep the ids while they still find OBS's windows; drop them when they don't (OBS
	// restarted with new ids), so the next check looks them up again.
	if known {
		obsGuard.pids = pids
	} else {
		obsGuard.pids = nil
	}
	if !hung {
		if obsGuard.hungSince != 0 {
			say("  ✓  OBS is responding again")
		}
		obsGuard.hungSince, obsGuard.freezeChecked = 0, 0
		obsGuardMu.Unlock()
		return ""
	}
	if obsGuard.hungSince == 0 {
		obsGuard.hungSince = t
		obsGuardMu.Unlock()
		say("  ⚠  OBS is not responding — watching it")
		return ""
	}
	frozenFor := t - obsGuard.hungSince
	checked := obsGuard.freezeChecked
	obsGuardMu.Unlock()
	if frozenFor < obsFreezeSec {
		return ""
	}
	if checked != 0 && t-checked < obsFreezeRecheckSec {
		return "frozen_streaming"
	}
	if obsStreamFlowing(st) {
		obsGuardMu.Lock()
		first := obsGuard.freezeChecked == 0
		obsGuard.freezeChecked = t
		obsGuardMu.Unlock()
		if first {
			say("  ⚠  OBS's window is frozen, but the stream is still going out — leaving it alone. Closing OBS would stop the stream.")
			obsGuardEvent("OBS window frozen; stream still flowing, left alone")
		}
		return "frozen_streaming"
	}
	say("  ⚠  OBS has been frozen for %ds and isn't streaming — closing it so it can be reopened", int(frozenFor))
	guardKill()
	obsGuardMu.Lock()
	obsGuard.hungSince, obsGuard.freezeChecked = 0, 0
	obsGuardMu.Unlock()
	obsGuardEvent("OBS frozen — force-closed for reopening")
	return "frozen_killed"
}

// obsGuardTick is one check. It returns what it did ("armed", "closed", "restarted",
// "restart_failed", "gave_up", "frozen_killed", "frozen_streaming") or "", so tests can
// drive it without goroutines.
func obsGuardTick(t float64) string {
	if t == 0 {
		t = now()
	}
	// Stand aside while the checklist's Start OBS job runs: on a first run it closes OBS
	// cleanly and reopens it on purpose.
	checklistJobMu.Lock()
	busy := gb(checklistJob, "running", false)
	checklistJobMu.Unlock()
	if busy {
		return ""
	}
	st := LoadState()
	port := gi(st, "obs_port", 4455)
	if port == 0 {
		port = 4455
	}
	obsGuardMu.Lock()
	armed := obsGuard.armed
	obsGuardMu.Unlock()
	// The port answering is proof enough of a running OBS. The process check (a tasklist
	// spawn) is only needed once armed.
	portUp := guardPortOpen(port)
	if portUp || (armed && obsRunning()) {
		obsGuardMu.Lock()
		first := !obsGuard.armed
		obsGuard.armed = true
		obsGuardMu.Unlock()
		// Whether it's streaming, asked of OBS itself every check: the monitor's flag can
		// be 15s old, so an operator who pressed Stop just before a crash would have been
		// put back on air. Never overwritten by "not reachable", or a crash would erase
		// the "it WAS live" this needs to restore.
		var live, known bool
		if portUp {
			live, known = guardStreaming(st)
		}
		if !known {
			streamMonMu.Lock()
			if gb(streamMon, "reachable", false) {
				live, known = gb(streamMon, "streaming", false), true
			}
			streamMonMu.Unlock()
		}
		if known {
			obsGuardMu.Lock()
			obsGuard.streaming = live
			obsGuardMu.Unlock()
		}
		if first {
			say("  ✓  OBS crash recovery: watching OBS")
			return "armed"
		}
		obsGuardMu.Lock()
		pending, pendingStream := obsGuard.pending, obsGuard.pendingStream
		obsGuardMu.Unlock()
		if pending && portUp {
			return obsGuardRestore(st, pendingStream)
		}
		// A force-close leaves OBS's crash marker, so the next tick takes the crash path.
		return obsFreezeCheck(st, t)
	}
	obsGuardMu.Lock()
	if !obsGuard.armed {
		obsGuardMu.Unlock()
		return "" // never seen OBS this session — not ours to start
	}
	wasStreaming := obsGuard.streaming
	pending := obsGuard.pending
	if pending {
		wasStreaming = obsGuard.pendingStream
	}
	obsGuardMu.Unlock()

	cfgDir := obs.ConfigDir()
	crashed := pending // our own reopen that hasn't come up: its marker is already gone
	if entries, err := os.ReadDir(filepath.Join(cfgDir, ".sentinel")); err == nil {
		for _, e := range entries {
			crashed = crashed || strings.HasPrefix(e.Name(), "run_")
		}
	}
	if !crashed {
		obsGuardMu.Lock()
		obsGuard.armed, obsGuard.streaming = false, false
		obsGuardMu.Unlock()
		say("  ⚠  OBS was closed (normally) — not reopening it. Start CricketStream again, or open OBS yourself, to carry on.")
		obsGuardEvent("OBS closed normally — left closed")
		// The one moment that's both after the match and safe to edit OBS's settings
		// (closed, so it won't write over them): put its update check back.
		if obs.ResumeUpdateCheck(cfgDir) {
			say("  ✓  OBS's update check is back on")
		}
		return "closed"
	}
	obsGuardMu.Lock()
	var recent []float64
	for _, r := range obsGuard.restarts {
		if t-r < obsGuardWindowSec {
			recent = append(recent, r)
		}
	}
	obsGuard.restarts = recent
	gaveUp := len(recent) >= obsGuardMaxRestarts
	attempt := 0
	if gaveUp {
		obsGuard.armed, obsGuard.gaveUp, obsGuard.pending = false, true, false
	} else {
		obsGuard.restarts = append(obsGuard.restarts, t)
		attempt = len(obsGuard.restarts)
	}
	obsGuardMu.Unlock()
	if gaveUp {
		say("  ✗  OBS has crashed %d times in %d minutes — no longer reopening it automatically. "+
			"Something needs a human look (OBS's log: Help → Log Files).", obsGuardMaxRestarts, obsGuardWindowSec/60)
		obsGuardEvent("OBS keeps crashing — gave up reopening it")
		return "gave_up"
	}
	exe := obs.FindOBS()
	if exe == "" {
		obsGuardEvent("OBS crashed but its program couldn't be found to reopen it")
		return "restart_failed"
	}
	mid := ""
	if wasStreaming {
		mid = " mid-stream"
	}
	say("  ⚠  OBS crashed%s — reopening it (attempt %d/%d)", mid, attempt, obsGuardMaxRestarts)
	obs.ClearCrashSentinel(cfgDir)
	obsGuardMu.Lock()
	obsGuard.pending, obsGuard.pendingStream, obsGuard.pendingTries = true, wasStreaming, 0
	obsGuardMu.Unlock()
	if err := guardLaunch(exe, obs.LaunchArgsFor(cfgDir)); err != nil {
		watchdogLogFix(fmt.Sprintf("OBS crashed and couldn't be reopened: %v", err))
		obsGuardEvent(fmt.Sprintf("reopen failed: %v", err))
		return "restart_failed" // still pending: the next check tries again
	}
	if !guardWaitWS(port, obsGuardStartupSec*time.Second) {
		watchdogLogFix("OBS crashed and was reopened, but isn't answering yet")
		obsGuardEvent("reopened, not answering")
		return "restart_failed" // still pending: put right as soon as it answers
	}
	guardSleep(3 * time.Second) // past "port open" to "scene collection loaded"
	return obsGuardRestore(st, wasStreaming)
}

// obsGuardRestorePasses: an OBS that answers but refuses StartStream (still loading,
// say) is asked again on the next few checks, then left for a person.
const obsGuardRestorePasses = 6

// obsGuardRestore puts a reopened OBS back how it was. The replay buffer is started by
// the launch flag. Scene: OBS restores whichever was live at the crash, which could be
// mid-replay. Stream: only if it WAS live — a crash before kick-off must not put the club
// on air.
func obsGuardRestore(st M, wasStreaming bool) string {
	reqs := []obsws.Req{{Type: "SetCurrentProgramScene", Data: M{"sceneName": gs(st, "obs_main_scene", "Main")}}}
	if wasStreaming {
		reqs = append(reqs, obsws.Req{Type: "StartStream"})
	}
	r := obsCall(st, reqs, 10*time.Second)
	if wasStreaming && (len(r) < 2 || r[1] == nil) {
		obsGuardMu.Lock()
		obsGuard.pendingTries++
		tries := obsGuard.pendingTries
		if tries >= obsGuardRestorePasses {
			obsGuard.pending = false
		}
		obsGuardMu.Unlock()
		if tries >= obsGuardRestorePasses {
			watchdogLogFix("OBS was reopened, but wouldn't start the stream again — press Start Streaming in OBS")
			obsGuardEvent("reopened; stream restart refused")
			return "restart_failed"
		}
		return "restore_pending"
	}
	obsGuardMu.Lock()
	obsGuard.pending = false
	obsGuardMu.Unlock()
	what := "OBS crashed — reopened"
	if wasStreaming {
		what += " and the stream restarted"
	}
	watchdogLogFix(what)
	obsGuardEvent(what)
	return "restarted"
}

func StartOBSGuard() {
	enabled := obsGuardEnabled()
	obsGuardMu.Lock()
	obsGuard.enabled = enabled
	obsGuardMu.Unlock()
	if enabled {
		loop("OBS crash recovery check", obsGuardPollSec*time.Second, func() { obsGuardTick(0) })
	}
}

// ResetOBSGuard forgets everything (tests).
func ResetOBSGuard() {
	obsGuardMu.Lock()
	obsGuard = obsGuardT{}
	obsGuardMu.Unlock()
}

func obsGuardStatus() M {
	obsGuardMu.Lock()
	defer obsGuardMu.Unlock()
	n := 0
	for _, r := range obsGuard.restarts {
		if now()-r < obsGuardWindowSec {
			n++
		}
	}
	var frozen any
	if obsGuard.hungSince != 0 {
		frozen = int(now() - obsGuard.hungSince)
	}
	var last any
	if obsGuard.lastEvent != nil {
		last = obsGuard.lastEvent
	}
	return M{"enabled": obsGuard.enabled, "watching": obsGuard.armed, "was_streaming": obsGuard.streaming,
		"restarts_recent": n, "gave_up": obsGuard.gaveUp, "frozen_for_sec": frozen, "last_event": last}
}

func streamMonitorStatus() M {
	auto := gb(LoadState(), "stream_auto_downshift", false)
	streamMonMu.Lock()
	defer streamMonMu.Unlock()
	var cong any
	if len(streamSamples) > 0 {
		cong = streamSamples[len(streamSamples)-1].Congestion
	}
	ladder := []any{}
	for _, f := range StreamLadder {
		ladder = append(ladder, int(f*100))
	}
	shifts := gl(streamMon, "shifts")
	if len(shifts) > 5 {
		shifts = shifts[len(shifts)-5:]
	}
	return M{"ok": true, "reachable": streamMon["reachable"], "streaming": streamMon["streaming"],
		"baseline_kbps": streamMon["baseline_kbps"], "current_kbps": streamMon["current_kbps"],
		"step": streamMon["step"], "ladder_pct": ladder, "congestion": cong,
		"verdict": streamMon["last_reason"], "shifts": append([]any{}, shifts...), "auto": auto,
		"dynamic_bitrate": streamMon["dynamic_bitrate"], "simple_mode": streamMon["simple_mode"],
		"upshift": streamMon["upshift_note"], "sent_kbps": streamMon["sent_kbps"],
		"bitrate_warning": streamMon["bitrate_warning"]}
}
