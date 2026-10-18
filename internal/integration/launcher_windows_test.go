package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// Match day exactly as a club runs it — `cricketstream start` — against a portable OBS:
// OBS prepared and opened, scenes built, the server up as the launcher's child, telemetry
// logging OBS, then Ctrl+C: the server stops, telemetry stops with it, OBS stays open.
func TestMatchDayLauncherAgainstPortableOBS(t *testing.T) {
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
	})

	app := t.TempDir()
	replays := filepath.Join(app, "replays")
	cfgText := "[App]\nrole = streaming\n\n[Club]\nname = Portable CC\nabbreviation = PORT\nhome_colour = #224466\n" +
		"playcricket_id = 1\n\n[OBS]\nobs_password =\nmanage_obs = yes\nreplay_folder = " + replays + "\n\n" +
		"[Graphics]\nscorebar_style = modern\n\n[Stream]\nmax_overs = 20\n"
	os.WriteFile(filepath.Join(app, "config.ini"), []byte(cfgText), 0o644)

	exe := filepath.Join(app, "CricketStream-itest.exe") // its own name: cleanup stops only this
	if out, err := exec.Command("go", "build", "-o", exe, "../../cmd/cricketstream").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	port := freePort(t)
	cmd := exec.Command(exe, "start", "--local", "--nvplay")
	cmd.Dir = app
	cmd.Env = append(os.Environ(), "CRICKETSTREAM_HOME="+app, fmt.Sprintf("CRICKETSTREAM_PORT=%d", port),
		"CRICKETSTREAM_NO_PAUSE=1", "CRICKETSTREAM_NO_BROWSER=1")
	out := &syncBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			cmd.Process.Kill()
		}
		exec.Command("taskkill", "/F", "/IM", "CricketStream-itest.exe").Run() // a server child left behind
		if t.Failed() {
			t.Logf("launcher output:\n%s", out.String())
		}
	})

	waitOut := func(re string, d time.Duration) []string {
		t.Helper()
		rx := regexp.MustCompile(re)
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if m := rx.FindStringSubmatch(out.String()); m != nil {
				return m
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("no %q after %v", re, d)
		return nil
	}
	waitOut(`Control panel: http://localhost:\d+/control`, 3*time.Minute)

	cfg, _ := ini.Load(filepath.Join(app, "config.ini"))
	target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: cfg.Get("OBS", "obs_password", "")}
	scenes := names(call(t, target, "GetSceneList", nil)["scenes"], "sceneName")
	if !has(scenes, "Main") || !has(scenes, "Replay") {
		t.Errorf("scenes %v", scenes)
	}
	ov := call(t, target, "GetInputSettings", map[string]any{"inputName": "Overlay"})
	if s, _ := ov["inputSettings"].(map[string]any); !strings.Contains(fmt.Sprint(s["url"]), fmt.Sprintf(":%d/overlay", port)) {
		t.Errorf("overlay source points at %v, server is on %d", s["url"], port)
	}
	if rb := call(t, target, "GetReplayBufferStatus", nil); rb["outputActive"] != true {
		t.Errorf("replay buffer not running: %v", rb)
	}

	// Telemetry was started by the launcher and is logging OBS.
	var csv string
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		m, _ := filepath.Glob(filepath.Join(app, "diagnostics", "telemetry_*.csv"))
		if len(m) == 1 {
			b, _ := os.ReadFile(m[0])
			if strings.Count(string(b), "\n") >= 3 {
				csv = m[0]
				break
			}
		}
		time.Sleep(time.Second)
	}
	if csv == "" {
		t.Fatal("telemetry didn't log")
	}

	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
		t.Skipf("can't send Ctrl+Break here: %v", err)
	}
	waitOut(`Server stopped\. Bye!`, time.Minute)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("launcher didn't exit")
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if !obs.Running() {
		t.Error("OBS was closed — match-day shutdown must leave it to the operator")
	}
	// Telemetry stopped with the launcher: the log no longer grows.
	st1, _ := os.Stat(csv)
	time.Sleep(13 * time.Second)
	st2, _ := os.Stat(csv)
	if st2.Size() != st1.Size() {
		t.Error("telemetry kept logging after the launcher stopped")
	}
}
