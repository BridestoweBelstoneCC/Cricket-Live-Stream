package server

import (
	"strings"
	"testing"
	"time"

	"cricketstream/internal/obsws"
)

// overlayWatchFake: OBS answering (or not), every request recorded, the watch's clock
// state reset — set back in time by moving the timestamps, not by waiting.
type overlayWatchFake struct {
	reloads int
	other   []string
	reach   bool
}

func newOverlayWatchFake(t *testing.T) *overlayWatchFake {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"obs_host": "localhost", "obs_port": 4455})
	f := &overlayWatchFake{reach: true}
	savedCall, savedStart := obsCall, serverStart
	t.Cleanup(func() {
		obsCall, serverStart = savedCall, savedStart
		overlayLastPoll, overlayReloads, overlayReloadAt, overlayReloaded, overlayGaveUp = 0, 0, 0, 0, false
	})
	overlayLastPoll, overlayReloads, overlayReloadAt, overlayReloaded, overlayGaveUp = 0, 0, 0, 0, false
	watchdogMu.Lock()
	watchdogStatus["fixes_applied"] = []any{}
	watchdogMu.Unlock()
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		if !f.reach {
			return nil
		}
		out := []map[string]any{}
		for _, r := range reqs {
			d, _ := r.Data.(M)
			if r.Type == "PressInputPropertiesButton" && d["inputName"] == "Overlay" && d["propertyName"] == "refreshnocache" {
				f.reloads++
			} else {
				f.other = append(f.other, r.Type)
			}
			out = append(out, map[string]any{})
		}
		return out
	}
	return f
}

func ago(sec float64) float64 { return now() - sec }

// The match-day order: OBS loaded the overlay before the server listened, so it never
// polled. The watch reloads it — once, then waits to see if that worked.
func TestOverlayWatchReloadsAnOverlayThatNeverPolled(t *testing.T) {
	f := newOverlayWatchFake(t)
	serverStart = time.Now().Add(-10 * time.Second)
	overlayWatchTick()
	mustEq(t, f.reloads, 0, "the overlay has had no time to load yet")

	serverStart = time.Now().Add(-40 * time.Second)
	overlayWatchTick()
	mustEq(t, f.reloads, 1, "silent 40s since the server started")
	overlayWatchTick()
	mustEq(t, f.reloads, 1, "a reload gets time to take before another")
	if len(f.other) != 0 {
		t.Fatalf("asked OBS for more than the reload: %v", f.other)
	}

	// It worked: the overlay polls, and the watch stands down.
	overlayPolled()
	overlayReloadAt = ago(overlayReloadGapSec + 1)
	overlayWatchTick()
	mustEq(t, f.reloads, 1, "polling again")
	st := overlayWatchStatus()
	mustEq(t, st["reloads_that_worked"], any(1))
	mustEq(t, st["reloads_pending"], any(0))
}

// A page that's polling is never touched — however long the server's been up.
func TestOverlayWatchLeavesAPollingOverlayAlone(t *testing.T) {
	f := newOverlayWatchFake(t)
	serverStart = time.Now().Add(-time.Hour)
	overlayLastPoll = ago(5)
	overlayWatchTick()
	mustEq(t, f.reloads, 0)
}

// Not a load race after three tries (a wrong URL, a deleted source): it says so once and
// stops, rather than reloading the overlay every two minutes all match.
func TestOverlayWatchGivesUpAfterThreeReloads(t *testing.T) {
	f := newOverlayWatchFake(t)
	serverStart = time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		overlayReloadAt = ago(overlayReloadGapSec + 1)
		overlayWatchTick()
	}
	mustEq(t, f.reloads, overlayMaxReloads)
	saidSo := 0
	for _, x := range gl(watchdogStatusCopy(), "fixes_applied") {
		if what, _ := x.(M)["what"].(string); strings.Contains(what, "after 3 reloads") {
			saidSo++
		}
	}
	mustEq(t, saidSo, 1, "the give-up message, once")
	// A poll at last (someone fixed the URL): back to watching.
	overlayPolled()
	mustEq(t, overlayGaveUp, false)
}

// OBS closed, or OBS on another computer: nothing to reload, and nothing counted against
// the three tries.
func TestOverlayWatchNeedsALocalOBS(t *testing.T) {
	f := newOverlayWatchFake(t)
	serverStart = time.Now().Add(-time.Hour)
	f.reach = false
	overlayWatchTick()
	mustEq(t, overlayReloads, 0, "OBS unreachable")

	f.reach = true
	setState(t, M{"obs_host": "192.168.1.20", "obs_port": 4455})
	overlayWatchTick()
	mustEq(t, f.reloads, 0, "an OBS on another computer")
}

// Only the overlay's own poll counts: a panel reading /live/view says nothing about
// whether OBS's overlay is running.
func TestOverlayWatchCountsOnlyTheOverlaysPoll(t *testing.T) {
	newOverlayWatchFake(t)
	ts := serve(t)
	ts.get("/live/view")
	if overlayLastPoll != 0 {
		t.Fatal("/live/view counted as the overlay")
	}
	ts.get("/live")
	if overlayLastPoll == 0 {
		t.Fatal("/live didn't count")
	}
	_, h := ts.getJSON("/health")
	if _, ok := h["overlay"].(map[string]any); !ok {
		t.Fatalf("/health has no overlay block: %v", h["overlay"])
	}
}
