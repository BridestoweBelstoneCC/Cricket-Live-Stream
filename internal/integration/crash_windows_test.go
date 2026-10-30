package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

// OBS crashing mid-match, for real: the server's crash recovery sees the force-close,
// reopens OBS past its Safe Mode question, and puts the Main scene back.
func TestOBSCrashIsRecoveredAgainstPortableOBS(t *testing.T) {
	root := os.Getenv(obs.PortableEnv)
	if root == "" {
		t.Skip("set " + obs.PortableEnv + " to a portable OBS copy to run this")
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		t.Fatalf("%s has no obs_portable_mode.txt — refusing", root)
	}
	if obs.Running() {
		t.Skip("an OBS is already open; close it first")
	}
	own := ownOBSFiles(t)
	t.Cleanup(func() {
		for p, m := range ownOBSFiles(t) {
			if before, ok := own[p]; !ok || !before.Equal(m) {
				t.Errorf("this machine's own OBS settings changed: %s", p)
			}
		}
	})
	t.Cleanup(func() {
		if obs.Running() && !obs.Close(30*time.Second) {
			obs.Kill()
		}
		// Gone, not just closing: the next test refuses to start while any OBS is open.
		for end := time.Now().Add(30 * time.Second); obs.Running() && time.Now().Before(end); {
			time.Sleep(500 * time.Millisecond)
		}
	})

	app := t.TempDir()
	cfgPath := filepath.Join(app, "config.ini")
	os.WriteFile(cfgPath, []byte("[App]\nrole = streaming\n\n[Club]\nname = Crash CC\nplaycricket_id = 1\n\n"+
		"[OBS]\nobs_password =\nmanage_obs = yes\nreplay_folder = "+filepath.Join(app, "replays")+"\n"), 0o644)
	t.Setenv("CRICKETSTREAM_HOME", app)
	if !obs.Prepare(cfgPath, nil, func(s string) { t.Log(s) }) {
		t.Fatal("OBS prep/launch failed")
	}
	if ok, msgs := obs.SetupFromConfig(cfgPath, filepath.Join(app, "match_state.json"), false, true); !ok {
		t.Fatalf("setup: %v", msgs)
	}
	cfg, _ := ini.Load(cfgPath)
	target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: cfg.Get("OBS", "obs_password", "")}
	call(t, target, "SetCurrentProgramScene", map[string]any{"sceneName": "Replay"}) // so "back to Main" is visible

	exe := filepath.Join(app, "CricketStream-itest.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "../../cmd/cricketstream").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	srv := exec.Command(exe, "server")
	srv.Dir = app
	srv.Env = append(os.Environ(), fmt.Sprintf("CRICKETSTREAM_PORT=%d", freePort(t)), "CRICKETSTREAM_NO_PAUSE=1")
	log := &syncBuf{}
	srv.Stdout, srv.Stderr = log, log
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Process.Kill()
		srv.Wait()
		if t.Failed() {
			t.Logf("server log:\n%s", log.String())
		}
	})
	wait := func(cond func() bool, d time.Duration, what string) {
		t.Helper()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("%s: not after %v", what, d)
	}
	wait(func() bool { return strings.Contains(log.String(), "OBS crash recovery: watching OBS") }, 60*time.Second, "guard armed")

	obs.Kill() // a crash: force-closed, so OBS leaves its crash marker
	wait(func() bool { return strings.Contains(log.String(), "reopening it") }, 60*time.Second, "crash noticed")
	wait(func() bool { return strings.Contains(log.String(), "OBS crashed — reopened") }, 90*time.Second, "OBS reopened")
	wait(func() bool { return obs.PortOpen(obs.WSPort, "", time.Second) }, 30*time.Second, "WebSocket back")
	scene, _ := call(t, target, "GetCurrentProgramScene", nil)["currentProgramSceneName"].(string)
	if scene != "Main" {
		t.Errorf("program scene after recovery: %q", scene)
	}
	// Reopened past the Safe Mode question: Safe Mode switches the WebSocket off, so it
	// answering again (above) is the proof — and it was this test, not a person, answering.
}
