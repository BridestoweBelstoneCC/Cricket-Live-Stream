package integration

// obsws.Meters against a real OBS: the sound and picture watch (internal/server/avwatch.go)
// depends on OBS sending its level meters in the shape the fake does. Opt-in like the rest
// of this package: CRICKETSTREAM_OBS_PORTABLE set to a portable copy, no OBS open.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

func TestSoundLevelsAgainstPortableOBS(t *testing.T) {
	root := os.Getenv(obs.PortableEnv)
	if root == "" {
		t.Skip("set " + obs.PortableEnv + " to a portable OBS copy to run this")
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		t.Fatalf("%s has no obs_portable_mode.txt — refusing", root)
	}
	if obs.Running() {
		t.Skip("an OBS is already open; close it first (this must only ever drive the portable copy)")
	}
	own := ownOBSFiles(t)
	t.Cleanup(func() {
		for p, m := range ownOBSFiles(t) {
			if before, ok := own[p]; !ok || !before.Equal(m) {
				t.Errorf("this machine's own OBS settings changed: %s", p)
			}
		}
	})
	app := t.TempDir()
	t.Setenv("CRICKETSTREAM_HOME", app)
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
	assets.SetDir(app)
	example, err := os.ReadFile("../../config.example.ini")
	if err != nil {
		t.Fatal(err)
	}
	replays := filepath.Join(app, "replays")
	os.MkdirAll(replays, 0o755)
	cfgPath := filepath.Join(app, "config.ini")
	os.WriteFile(cfgPath, []byte(strings.Replace(string(example), "replay_folder =", "replay_folder = "+replays, 1)), 0o644)
	if !obs.Prepare(cfgPath, nil, func(s string) { t.Log(s) }) {
		t.Fatal("OBS prep/launch failed")
	}
	t.Cleanup(func() {
		if !obs.Close(30 * time.Second) {
			obs.Kill()
		}
	})
	cfg, _ := ini.Load(cfgPath)
	target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: cfg.Get("OBS", "obs_password", "")}

	// OBS answers the WebSocket a little after its window opens.
	var peaks map[string]float64
	deadline := time.Now().Add(40 * time.Second)
	for {
		if peaks, err = obsws.Meters(target, 1500*time.Millisecond, 4*time.Second); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("no sound levels from a real OBS: %v", err)
	}
	t.Logf("real OBS sent levels for %d input(s): %v", len(peaks), peaks)
	for name, p := range peaks {
		if p < 0 || p > 4 {
			t.Errorf("%s: a peak of %v isn't a level", name, p)
		}
	}
	// An ordinary connection alongside still works and isn't sent the meters.
	c, err := obsws.Dial(target, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if r, err := c.Request("GetVersion", nil); err != nil || !r.OK {
		t.Errorf("GetVersion beside the listener: %v %+v", err, r)
	}
}
