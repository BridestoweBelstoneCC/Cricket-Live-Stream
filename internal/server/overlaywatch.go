package server

// The overlay's own browser source in OBS loads http://127.0.0.1:5000/overlay once. If
// that load happens while the server isn't listening yet, OBS shows an error page and
// never tries again — no scorebar on air, and nothing reaches the ball database, the
// scorecard or the match report. That's the normal match-day order: the launcher opens
// OBS and builds its scenes before it starts the server. Found on a fresh VM, end to end
// (2026-10-29). A page that did load survives a server restart on its own (its poll just
// fails until the server is back), so this only ever has to kick a page that never ran:
// no /live poll for a while, with OBS reachable, reloads the Overlay source.

import (
	"sync"
	"time"

	"cricketstream/internal/obsws"
)

const (
	overlayWatchPollSec = 10
	overlaySilentSec    = 30  // the overlay polls every ~2.5s; this long without one is not a hiccup
	overlayReloadGapSec = 120 // give a reload time to take before trying again
	overlayMaxReloads   = 3   // without a poll in between: past that it's not a load race
)

var (
	overlayWatchMu  sync.Mutex
	overlayLastPoll float64 // the overlay's last /live; 0 = not since this server started
	overlayReloads  int     // reloads since the last poll
	overlayReloadAt float64
	overlayReloaded int // reloads that brought it back, for /health
	overlayGaveUp   bool
)

// overlayPolled is the overlay's /live: whatever was wrong, it's running now.
func overlayPolled() {
	overlayWatchMu.Lock()
	defer overlayWatchMu.Unlock()
	if overlayReloads > 0 {
		overlayReloaded++
	}
	overlayLastPoll, overlayReloads, overlayGaveUp = now(), 0, false
}

func overlayWatchTick() {
	t := now()
	overlayWatchMu.Lock()
	last := overlayLastPoll
	if last == 0 {
		last = float64(serverStart.UnixNano()) / 1e9
	}
	due := t-last >= overlaySilentSec && t-overlayReloadAt >= overlayReloadGapSec
	if due && overlayReloads >= overlayMaxReloads {
		due = false
		if !overlayGaveUp {
			overlayGaveUp = true
			defer watchdogLogFix("OBS's Overlay still isn't asking for the score after 3 reloads — check its URL in OBS (Sources → Overlay)")
		}
	}
	overlayWatchMu.Unlock()
	if !due {
		return
	}
	s := LoadState()
	if !obsIsLocal(s) {
		return // the Overlay source loads 127.0.0.1: only OBS on this machine has one to reload
	}
	r := obsCall(s, []obsws.Req{{Type: "PressInputPropertiesButton",
		Data: M{"inputName": "Overlay", "propertyName": "refreshnocache"}}}, 5*time.Second)
	if len(r) == 0 || r[0] == nil {
		return // OBS closed, or no Overlay source: nothing to reload, and not counted
	}
	overlayWatchMu.Lock()
	overlayReloads++
	overlayReloadAt = t
	overlayWatchMu.Unlock()
	watchdogLogFix("OBS's Overlay wasn't asking for the score — reloaded it")
}

func overlayWatchStatus() M {
	overlayWatchMu.Lock()
	defer overlayWatchMu.Unlock()
	var ago any
	if overlayLastPoll != 0 {
		ago = int(now() - overlayLastPoll)
	}
	return M{"last_poll_sec_ago": ago, "reloads_pending": overlayReloads, "reloads_that_worked": overlayReloaded}
}

func StartOverlayWatch() { loop("Overlay watch", overlayWatchPollSec*time.Second, overlayWatchTick) }
