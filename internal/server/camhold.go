package server

// A held camera cut: to camera 2 for a while (the over change, a batter walking out
// after a wicket, the teams walking off), then back — but only if OBS is still on that
// scene: a replay or the operator may have moved it since, and they win. While a hold
// runs, the break show's replays wait for it (breakReel).

import (
	"sync"
	"time"
)

var (
	camHoldMu    sync.Mutex
	camHoldGen   int
	camHoldUntil time.Time
)

// camHolding: a held cut is running now.
func camHolding() bool {
	camHoldMu.Lock()
	defer camHoldMu.Unlock()
	return time.Now().Before(camHoldUntil)
}

// endCamHold forgets any running hold (another cut has happened).
func endCamHold() {
	camHoldMu.Lock()
	camHoldGen++
	camHoldUntil = time.Time{}
	camHoldMu.Unlock()
}

// holdCut cuts to scene, and after hold cuts to back if OBS is still on scene and no
// other cut has been asked for since.
func holdCut(st M, scene, back string, hold time.Duration) bool {
	if !cutToScene(st, scene) {
		return false
	}
	camHoldMu.Lock()
	camHoldGen++
	gen := camHoldGen
	camHoldUntil = time.Now().Add(hold)
	camHoldMu.Unlock()
	time.AfterFunc(hold, func() {
		defer func() { recover() }()
		camHoldMu.Lock()
		mine := camHoldGen == gen
		if mine {
			camHoldUntil = time.Time{}
		}
		camHoldMu.Unlock()
		if mine && programScene(st) == scene {
			cutToScene(st, back)
		}
	})
	return true
}

// replayBusyUntil is when the wicket/boundary replay running now ends (ObsTriggerReplay
// sets it), so the break show doesn't start a clip over it.
var replayBusyUntil time.Time

// breakClear: no held cut and no replay running — the break show may play a clip.
func breakClear() bool {
	camHoldMu.Lock()
	defer camHoldMu.Unlock()
	n := time.Now()
	return !n.Before(camHoldUntil) && !n.Before(replayBusyUntil)
}

func setReplayBusy(d time.Duration) {
	camHoldMu.Lock()
	replayBusyUntil = time.Now().Add(d)
	camHoldMu.Unlock()
}

// cutIfOn cuts to scene only while OBS is on from (cutting back early, when play
// restarts before a hold ends). (ok, cut).
func cutIfOn(st M, scene, from string) (bool, bool) {
	endCamHold()
	if programScene(st) != from {
		return true, false
	}
	return cutToScene(st, scene), true
}
