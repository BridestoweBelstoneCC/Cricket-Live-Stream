package server

// The CPU hold-back warning: /health's thermal block and the watchdog's once-per-change
// line. Windows' reading is faked; the real counter is read in thermal_windows_test.go.

import (
	"bytes"
	"strings"
	"testing"
)

func fakePerfLimit(t *testing.T, pct float64, ok bool) *float64 {
	t.Helper()
	saved := readPerfLimit
	v := pct
	readPerfLimit = func() (float64, bool) { return v, ok }
	t.Cleanup(func() { readPerfLimit = saved })
	return &v
}

func TestWindowsThermal(t *testing.T) {
	for _, c := range []struct {
		pct       float64
		ok        bool
		throttled any
		limit     any
	}{
		{100, true, false, 100},
		{96, true, false, 96},   // ordinary power management: not worth a red badge
		{94.6, true, false, 95}, // rounds to the threshold
		{94.4, true, true, 94},
		{38, true, true, 38}, // off the charger, or far too hot
	} {
		fakePerfLimit(t, c.pct, c.ok)
		got := windowsThermal()
		if got["available"] != true || got["platform"] != "windows" || got["throttled"] != c.throttled || got["speed_limit_pct"] != c.limit {
			t.Errorf("%v%%: %v", c.pct, got)
		}
	}
	fakePerfLimit(t, 0, false)
	if got := windowsThermal(); got["available"] != false || len(got) != 1 {
		t.Errorf("no counter: %v", got)
	}
}

// The watchdog says so once when the laptop starts slowing down and once when it stops —
// not every 90 seconds in between — and on Windows it names the charger first.
func TestWatchdogWarnsOnceWhenTheCPUIsHeldBack(t *testing.T) {
	sandbox(t, "")
	var out bytes.Buffer
	savedOut, savedTherm := sayOut, thermalState
	sayOut = &out
	t.Cleanup(func() { sayOut, thermalState = savedOut, savedTherm })
	watchdogMu.Lock()
	watchdogStatus["was_throttled"] = nil
	watchdogMu.Unlock()
	state := M{"available": true, "platform": "windows", "speed_limit_pct": 40, "throttled": true}
	thermalState = func() M { return state }

	watchdogTick()
	watchdogTick()
	if n := strings.Count(out.String(), "slowing its CPU down"); n != 1 {
		t.Fatalf("warned %d times, want once:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "performance limit 40%") || !strings.Contains(out.String(), "charger") {
		t.Fatalf("warning: %s", out.String())
	}
	state = M{"available": true, "platform": "windows", "speed_limit_pct": 100, "throttled": false}
	watchdogTick()
	watchdogTick()
	if n := strings.Count(out.String(), "throttling has cleared"); n != 1 {
		t.Fatalf("cleared %d times, want once:\n%s", n, out.String())
	}
	// A Mac keeps its own words.
	state = M{"available": true, "platform": "mac", "speed_limit_pct": 70, "throttled": true}
	watchdogTick()
	if !strings.Contains(out.String(), "Mac is thermally throttled (CPU speed limit 70%)") {
		t.Fatalf("mac warning: %s", out.String())
	}
}

func TestHealthCarriesTheThermalBlock(t *testing.T) {
	ts := newTestServer(t, "")
	savedTherm := thermalState
	thermalState = func() M { return M{"available": true, "platform": "windows", "speed_limit_pct": 60, "throttled": true} }
	t.Cleanup(func() { thermalState = savedTherm })
	_, h := ts.getJSON("/health")
	if th, _ := h["thermal"].(M); th["throttled"] != true || th["platform"] != "windows" {
		t.Fatalf("/health thermal: %v", h["thermal"])
	}
}
