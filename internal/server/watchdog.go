package server

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyfmt"
)

// ── Server self-metrics + error flight recorder ────────────────
// The streaming laptop also runs OBS's encoder, so "is the server staying lightweight?"
// should be a number in /health, not a promise. And background errors print to a console
// nobody reads mid-match — a small ring makes "something's failing" visible in /health
// before it becomes "graphics stopped".
var (
	selfStatT, selfStatCPU = now(), 0.0
	selfStatMu             sync.Mutex
	serverErrors           []M
	serverErrorsMu         sync.Mutex
)

func logServerError(where string, err error) {
	serverErrorsMu.Lock()
	defer serverErrorsMu.Unlock()
	serverErrors = append(serverErrors, M{"time": now(), "where": where, "error": truncRunes(fmt.Sprint(err), 300)})
	if len(serverErrors) > 30 {
		serverErrors = serverErrors[len(serverErrors)-30:]
	}
	say("  ✗  Unhandled error in %s: %v", where, err)
}

func recentServerErrors(n int) []M {
	serverErrorsMu.Lock()
	defer serverErrorsMu.Unlock()
	if n > len(serverErrors) {
		n = len(serverErrors)
	}
	out := append([]M(nil), serverErrors[len(serverErrors)-n:]...)
	if out == nil {
		out = []M{}
	}
	return out
}

// selfMetrics is this process's CPU% since the last /health call, peak memory and the
// machine's load.
func selfMetrics() M {
	t, cpu := now(), processCPUSeconds()
	selfStatMu.Lock()
	dt, dcpu := t-selfStatT, cpu-selfStatCPU
	selfStatT, selfStatCPU = t, cpu
	selfStatMu.Unlock()
	var pct any
	if dt > 1 {
		pct = pyfmt.Round(100*dcpu/dt, 1)
	}
	var rss any
	if mb := peakRSSMB(); mb > 0 {
		rss = pyfmt.Round(mb, 1)
	}
	return M{"cpu_pct": pct, "max_rss_mb": rss, "machine_load_1m": loadAvg1()}
}

func loadAvg1() any {
	if runtime.GOOS != "linux" {
		return nil
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	f, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	if err != nil {
		return nil
	}
	return pyfmt.Round(f, 2)
}

// thermalState is whether the CPU is being held back — on a Mac `pmset -g therm` (no
// sudo), on Windows the "% Performance Limit" counter (thermal_windows.go). Neither OS
// gives a real temperature without extra tooling or admin rights, but both limits drop
// below 100 the moment the machine throttles — the signal that matters, well before a
// crash. {"available": false} anywhere else (Linux). Never fails.
var thermalState = func() M {
	if runtime.GOOS == "windows" {
		return windowsThermal()
	}
	if runtime.GOOS != "darwin" {
		return M{"available": false}
	}
	out, err := exec.Command("pmset", "-g", "therm").Output()
	if err != nil {
		return M{"available": false}
	}
	vals := map[string]int{}
	for _, k := range []string{"CPU_Speed_Limit", "CPU_Scheduler_Limit", "CPU_Available_CPUs"} {
		if m := regexp.MustCompile(k + `\s*=\s*(\d+)`).FindStringSubmatch(string(out)); m != nil {
			vals[k], _ = strconv.Atoi(m[1])
		}
	}
	_, s1 := vals["CPU_Speed_Limit"]
	_, s2 := vals["CPU_Scheduler_Limit"]
	if !s1 && !s2 {
		return M{"available": false}
	}
	speed, sched := 100, 100
	if v, ok := vals["CPU_Speed_Limit"]; ok {
		speed = v
	}
	if v, ok := vals["CPU_Scheduler_Limit"]; ok {
		sched = v
	}
	var cpus any
	if v, ok := vals["CPU_Available_CPUs"]; ok {
		cpus = v
	}
	return M{"available": true, "platform": "mac", "speed_limit_pct": speed, "scheduler_limit_pct": sched,
		"available_cpus": cpus, "throttled": speed < 100 || sched < 100}
}

// winThrottledBelow: the performance limit dips a point or two in ordinary power
// management on some laptops; a real hold-back for heat or a battery is far below that.
// (A red badge in the panel all match for nothing would teach people to ignore it.)
const winThrottledBelow = 95

func windowsThermal() M {
	pct, ok := readPerfLimit()
	if !ok {
		return M{"available": false}
	}
	limit := int(pct + 0.5)
	return M{"available": true, "platform": "windows", "speed_limit_pct": limit,
		"throttled": limit < winThrottledBelow}
}

// ── Self-healing watchdog ──────────────────────────────────────
// Runs for the life of the process and checks the few things known to get stuck during
// a long match day, fixing what it safely can and logging what it can't.
const (
	watchdogIntervalSec = 90
	seasonBuildStuckSec = 300 // defence in depth — the build's own recover should prevent this
	rateLimitTSMaxAge   = 3600
)

var (
	watchdogStatus = M{"last_run": nil, "runs": 0, "fixes_applied": []any{}, "pcs_was_fresh": nil, "was_throttled": nil}
	watchdogMu     sync.Mutex
)

func watchdogLogFix(what string) {
	say("  🩹  Watchdog: %s", what)
	watchdogMu.Lock()
	defer watchdogMu.Unlock()
	fixes := append(gl(watchdogStatus, "fixes_applied"), M{"time": now(), "what": what})
	if len(fixes) > 20 {
		fixes = fixes[len(fixes)-20:]
	}
	watchdogStatus["fixes_applied"] = fixes
}

func watchdogStatusCopy() M {
	watchdogMu.Lock()
	defer watchdogMu.Unlock()
	return shallow(watchdogStatus)
}

func watchdogTick() {
	t := now()
	// 1) A season-stats build stuck "building" — reset so the next request can retry.
	seasonStatsMu.Lock()
	stuck := gb(seasonStats, "building", false) && gf(seasonStats, "build_started", 0) != 0 &&
		t-gf(seasonStats, "build_started", 0) > seasonBuildStuckSec
	if stuck {
		seasonStats["building"], seasonStats["build_started"] = false, nil
		seasonStats["error"] = "watchdog: build looked stuck and was reset"
	}
	seasonStatsMu.Unlock()
	if stuck {
		watchdogLogFix("season stats build looked stuck — reset so it can retry")
	}
	// 2) The Cloudflare Tunnel died — restart it, capped so a real problem can't loop.
	if CloudflareTunnel && ClubPassword != "" {
		cfMu.Lock()
		dead := cfProcess != nil && cfProcess.ProcessState != nil
		cfMu.Unlock()
		if dead {
			if cfRestartCount < CFMaxRestarts {
				cfRestartCount++
				watchdogLogFix(fmt.Sprintf("Cloudflare Tunnel had stopped — restarting (attempt %d/%d)", cfRestartCount, CFMaxRestarts))
				startCloudflareTunnel()
			} else if cfRestartCount == CFMaxRestarts {
				cfRestartCount++ // past the cap, so this only prints once
				say("  ✗  Watchdog: Cloudflare Tunnel keeps dying — giving up auto-restart, check cloudflared manually")
			}
		}
	}
	// 3) Trim old rate-limit timestamps (memory hygiene over a long day).
	rateLimitMu.Lock()
	for k, ts := range rateLimitTS {
		if t-ts > rateLimitTSMaxAge {
			delete(rateLimitTS, k)
		}
	}
	rateLimitMu.Unlock()
	// 4) Scorer feed freshness — visibility only: the server can't restart the scorer's
	// laptop. One shared rule for every feed consumer (manual > agent > bridge/local).
	var fresh any
	func() {
		defer func() { recover() }()
		f, _ := scoreFeedStatus(LoadState(), t)
		fresh = f
	}()
	watchdogMu.Lock()
	was := watchdogStatus["pcs_was_fresh"]
	if was == true && fresh == false {
		say("  ⚠  Watchdog: PCS scorer feed has gone stale (2+ min old) — check the scorer's laptop")
	} else if was == false && fresh == true {
		say("  ✓  Watchdog: PCS scorer feed is fresh again")
	}
	watchdogStatus["pcs_was_fresh"] = fresh
	watchdogMu.Unlock()
	// 5) Thermal pressure — visibility only: warned early, an operator can shed load
	// before heat takes the rig out mid-match.
	therm := thermalState()
	var throttled any
	if gb(therm, "available", false) {
		throttled = therm["throttled"]
	}
	watchdogMu.Lock()
	wasT := watchdogStatus["was_throttled"]
	if throttled == true && wasT != true {
		if therm["platform"] == "windows" {
			// Windows' limit counts power too: a laptop that's come off its charger looks the same.
			say("  ⚠  Watchdog: this laptop is slowing its CPU down (performance limit %v%%) — check it's on its charger, then give it air or reduce load", therm["speed_limit_pct"])
		} else {
			say("  ⚠  Watchdog: Mac is thermally throttled (CPU speed limit %v%%) — reduce load or improve cooling now", therm["speed_limit_pct"])
		}
	} else if wasT == true && throttled != true {
		say("  ✓  Watchdog: thermal throttling has cleared")
	}
	watchdogStatus["was_throttled"] = throttled
	watchdogStatus["last_run"] = t
	watchdogStatus["runs"] = gi(watchdogStatus, "runs", 0) + 1
	watchdogMu.Unlock()
}

// loop runs tick every interval for the life of the process. It must never die — a dead
// watchdog is the one thing nobody's watching.
func loop(name string, interval time.Duration, tick func()) {
	go func() {
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						say("  ✗  %s tick failed (will retry): %v", name, r)
					}
				}()
				tick()
			}()
			time.Sleep(interval)
		}
	}()
}

func StartWatchdog() { loop("Watchdog", watchdogIntervalSec*time.Second, watchdogTick) }

// lookupSeasonStats is {avg, hs, inn} for a player from the cached season build, or nil.
// Tries the full name, then first-initial+surname, then surname — so the scorebar's
// 'K J JONES' matches PlayCricket's 'K Jones', and 'WALKER' matches 'J Walker' (when
// unambiguous).
func lookupSeasonStats(name string) M {
	seasonStatsMu.Lock()
	lk := gm(seasonStats, "lookup")
	seasonStatsMu.Unlock()
	if name == "" || len(lk) == 0 {
		return nil
	}
	k := nameKeys(name)
	for _, key := range []string{k.full, k.initsur, k.surname} {
		if rec, ok := lk[key].(map[string]any); ok && len(rec) > 0 {
			return M{"avg": gs(rec, "avg", "—"), "hs": gs(rec, "hs", "—"), "inn": gs(rec, "inn", "—")}
		}
	}
	return nil
}
