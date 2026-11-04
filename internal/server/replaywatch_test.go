package server

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/obsws/obswstest"
)

// replayWatchRig is OBS on a scene, live or not, and a record of the cuts.
func replayWatchRig(t *testing.T, scene string, live bool) *[]string {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"obs_host": "localhost", "obs_port": 4455, "obs_main_scene": "Main", "obs_replay_scene": "Replay"})
	savedCall, savedFast := obsCall, obsFastCall
	t.Cleanup(func() {
		obsCall, obsFastCall = savedCall, savedFast
		replayWatchStrikes = 0
		playMu.Lock()
		playReplayUntil = 0
		playMu.Unlock()
	})
	replayWatchStrikes = 0
	playMu.Lock()
	playReplayUntil = 0
	playMu.Unlock()
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		return []map[string]any{{"currentProgramSceneName": scene}, {"outputActive": live}}
	}
	var cuts []string
	obsFastCall = func(_ M, typ string, data any, _ time.Duration) (bool, bool) {
		cuts = append(cuts, data.(M)["sceneName"].(string))
		return true, true
	}
	return &cuts
}

// The server died in a replay: nothing's left to cut back but this.
func TestReplayLeftOnAirIsCutBack(t *testing.T) {
	cuts := replayWatchRig(t, "Replay", true)
	replayWatchTick()
	if len(*cuts) != 0 {
		t.Fatalf("cut on the first look, which could be a replay just starting: %v", *cuts)
	}
	replayWatchTick()
	if len(*cuts) != 1 || (*cuts)[0] != "Main" {
		t.Fatalf("cuts: %v", *cuts)
	}
}

// With no server, there's no replay it could be, so the first look is enough.
func TestLeaveReplayCutsBackAtTheFirstLook(t *testing.T) {
	cuts := replayWatchRig(t, "Replay", true)
	if main, cut := LeaveReplay(); !cut || main != "Main" {
		t.Fatal(main, cut)
	}
	if len(*cuts) != 1 || (*cuts)[0] != "Main" {
		t.Fatalf("cuts: %v", *cuts)
	}
}

func TestLeaveReplayLeavesEverythingElseAlone(t *testing.T) {
	for _, c := range []struct {
		scene string
		live  bool
	}{{"Replay", false}, {"Main", true}, {"Main-Bowler", true}} {
		cuts := replayWatchRig(t, c.scene, c.live)
		if _, cut := LeaveReplay(); cut || len(*cuts) != 0 {
			t.Fatalf("%v: cut %v, cuts %v", c, cut, *cuts)
		}
	}
}

func TestLeaveReplayWithOBSNotAnswering(t *testing.T) {
	cuts := replayWatchRig(t, "Replay", true)
	obsCall = func(M, []obsws.Req, time.Duration) []map[string]any { return nil }
	if _, cut := LeaveReplay(); cut || len(*cuts) != 0 {
		t.Fatal(cut, *cuts)
	}
}

func TestReplayRunningIsLeftAlone(t *testing.T) {
	cuts := replayWatchRig(t, "Replay", true)
	noteReplay(60)
	for i := 0; i < 3; i++ {
		replayWatchTick()
	}
	if len(*cuts) != 0 {
		t.Fatalf("cut away from a replay on air: %v", *cuts)
	}
}

// Off air, someone may be looking at that scene on purpose.
func TestReplaySceneOffAirIsLeftAlone(t *testing.T) {
	cuts := replayWatchRig(t, "Replay", false)
	for i := 0; i < 3; i++ {
		replayWatchTick()
	}
	if len(*cuts) != 0 {
		t.Fatalf("cuts: %v", *cuts)
	}
}

func TestOtherScenesAreLeftAlone(t *testing.T) {
	cuts := replayWatchRig(t, "Main-Bowler", true)
	for i := 0; i < 3; i++ {
		replayWatchTick()
	}
	if len(*cuts) != 0 {
		t.Fatalf("cuts: %v", *cuts)
	}
}

// A look at the Replay scene, then a normal scene, then Replay again is two different replays,
// not one that's stuck.
func TestReplayWatchNeedsTwoLooksInARow(t *testing.T) {
	sandbox(t, "")
	cuts := replayWatchRig(t, "Replay", true)
	replayWatchTick()
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		return []map[string]any{{"currentProgramSceneName": "Main"}, {"outputActive": true}}
	}
	replayWatchTick()
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		return []map[string]any{{"currentProgramSceneName": "Replay"}, {"outputActive": true}}
	}
	replayWatchTick()
	if len(*cuts) != 0 {
		t.Fatalf("cuts: %v", *cuts)
	}
}

// Its answer about the way back: OBS refusing once mustn't leave viewers on the clip, and the
// log mustn't say it went back when it didn't.
func TestReplayReturnIsRetriedWhenOBSRefuses(t *testing.T) {
	sandbox(t, "")
	folder := t.TempDir()
	var mainAsks atomic.Int32
	srv := obswstest.New("", func(typ string, d map[string]any) (bool, map[string]any) {
		if typ == "SaveReplayBuffer" {
			os.WriteFile(filepath.Join(folder, "Replay new.mp4"), []byte("y"), 0o644)
		}
		if typ == "SetCurrentProgramScene" && d["sceneName"] == "Main" {
			return mainAsks.Add(1) > 1, nil // refused the first time
		}
		return true, nil
	})
	t.Cleanup(srv.Close)
	tg := srv.Target()
	setState(t, M{"obs_host": tg.Host, "obs_port": tg.Port, "replay_folder": folder, "replay_duration": 1, "obs_main_scene": "Main"})
	saved := replaySleep
	t.Cleanup(func() { replaySleep = saved })
	replaySleep = func(time.Duration) {}
	t.Cleanup(func() { obsFast.Close() })
	ObsTriggerReplay(LoadState(), "Boundary")
	if mainAsks.Load() < 2 {
		t.Fatalf("asked to go back to Main %d time(s)", mainAsks.Load())
	}
}

// Two replays close together: the first one's end mustn't cut away from the second.
func TestAnOlderReplayLeavesTheSceneToANewerOne(t *testing.T) {
	srv, _ := replayRig(t, true, true)
	first := true
	replaySleep = func(d time.Duration) {
		if d == time.Second && first { // the clip's on air now (replay_duration is 1)
			first = false
			replayLatest.Add(1) // a newer replay has cut to the scene
		}
	}
	ObsTriggerReplay(LoadState(), "Boundary")
	if n := countSceneCuts(srv.Types()); n != 1 {
		t.Fatalf("%d cuts, want only the one to Replay: %v", n, strings.Join(srv.Types(), " "))
	}
}

func countSceneCuts(types []string) int {
	n := 0
	for _, ty := range types {
		if ty == "SetCurrentProgramScene" {
			n++
		}
	}
	return n
}
