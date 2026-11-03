package server

// A replay's end is one request, from the goroutine that started it. If the server dies or
// is killed in between, or OBS doesn't answer at that moment, nothing else ever cuts back:
// viewers sat on a finished clip until the next replay happened to end. So this watches for
// the Replay scene live on air with no replay meant to be on it. Measured on a real OBS: the
// launcher had a killed server back in 3s, and OBS was still on Replay 75s later.
//
// Only while streaming: off air, someone may be looking at that scene on purpose, and
// nobody's watching. Two checks in a row, so a replay starting between the two reads is left
// alone (noteReplay covers it from then on).

import (
	"time"

	"cricketstream/internal/obsws"
)

const replayWatchPollSec = 10

var replayWatchStrikes int // only touched by the watch's one goroutine (and tests)

// replayMeantToBeOn: a replay or the break show has said it's on air until after t.
func replayMeantToBeOn(t float64) bool {
	playMu.Lock()
	defer playMu.Unlock()
	return t < playReplayUntil
}

func replayWatchTick() {
	st := LoadState()
	replay, main := gs(st, "obs_replay_scene", "Replay"), gs(st, "obs_main_scene", "Main")
	r := obsCall(st, []obsws.Req{{Type: "GetCurrentProgramScene"}, {Type: "GetStreamStatus"}}, 5*time.Second)
	if len(r) != 2 || r[0] == nil || r[1] == nil {
		return // OBS closed or crashed: the crash recovery puts the scene right
	}
	if gs(r[0], "currentProgramSceneName", "") != replay || !gb(r[1], "outputActive", false) || replayMeantToBeOn(now()) {
		replayWatchStrikes = 0
		return
	}
	if replayWatchStrikes++; replayWatchStrikes < 2 {
		return
	}
	if cutToScene(st, main) {
		replayWatchStrikes = 0
		watchdogLogFix("OBS was left on the replay scene with no replay running — cut back to " + main)
	}
}

func StartReplayWatch() { loop("Replay watch", replayWatchPollSec*time.Second, replayWatchTick) }
