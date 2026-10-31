package server

// The instant replay against a fake OBS: what goes on air when saving the clip fails.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/obsws/obswstest"
)

var (
	replayShownMu sync.Mutex
	replayShown   any // the clip ReplayClip was last pointed at
)

func replayRig(t *testing.T, saveOK bool, writeClip bool) (*obswstest.Server, string) {
	t.Helper()
	sandbox(t, "")
	folder := t.TempDir()
	old := filepath.Join(folder, "Replay old.mp4")
	os.WriteFile(old, []byte("x"), 0o644)
	past := time.Now().Add(-10 * time.Minute)
	os.Chtimes(old, past, past)
	replayShownMu.Lock()
	replayShown = nil
	replayShownMu.Unlock()
	srv := obswstest.New("", func(typ string, d map[string]any) (bool, map[string]any) {
		if typ == "SetInputSettings" {
			replayShownMu.Lock()
			replayShown = d["inputSettings"].(map[string]any)["local_file"]
			replayShownMu.Unlock()
		}
		if typ == "SaveReplayBuffer" {
			if saveOK && writeClip {
				os.WriteFile(filepath.Join(folder, "Replay new.mp4"), []byte("y"), 0o644)
			}
			return saveOK, nil
		}
		return true, nil
	})
	t.Cleanup(srv.Close)
	tg := srv.Target()
	setState(t, M{"obs_host": tg.Host, "obs_port": tg.Port, "replay_folder": folder, "replay_duration": 1})
	saved := replaySleep
	t.Cleanup(func() {
		replaySleep = saved
		camHoldMu.Lock()
		replayBusyUntil = time.Time{} // the break show's tests mustn't wait on this one
		camHoldMu.Unlock()
	})
	replaySleep = func(time.Duration) {}
	return srv, folder
}

func TestReplayNotRunningSkipsTheCutAndStartsTheBuffer(t *testing.T) {
	srv, _ := replayRig(t, false, false)
	ObsTriggerReplay(LoadState(), "Boundary")
	types := srv.Types()
	if has(types, "SetCurrentProgramScene") || has(types, "SetInputSettings") {
		t.Fatalf("went on air without a clip: %v", types)
	}
	if !has(types, "StartReplayBuffer") {
		t.Fatalf("buffer not started for next time: %v", types)
	}
}

func TestReplayNeverShowsThePreviousClip(t *testing.T) {
	// Saved "OK" but no new file appeared (wrong folder, full disk): the old clip in the
	// folder must not be replayed as this one.
	srv, _ := replayRig(t, true, false)
	ObsTriggerReplay(LoadState(), "Boundary")
	if types := srv.Types(); has(types, "SetCurrentProgramScene") || has(types, "SetInputSettings") {
		t.Fatalf("replayed a stale clip: %v", types)
	}
}

func TestReplayShowsTheNewClip(t *testing.T) {
	srv, folder := replayRig(t, true, true)
	ObsTriggerReplay(LoadState(), "Boundary")
	replayShownMu.Lock()
	clip := replayShown
	replayShownMu.Unlock()
	mustEq(t, clip, any(filepath.Join(folder, "Replay new.mp4")))
	mustEq(t, srv.Types()[len(srv.Types())-1], "SetCurrentProgramScene")
}
