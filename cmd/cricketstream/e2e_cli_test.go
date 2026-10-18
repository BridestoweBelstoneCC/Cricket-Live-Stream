package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EHelpListsEveryCommand(t *testing.T) {
	a := newApp(t)
	out, code := a.run(10*time.Second, "help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, c := range []string{"setup", "start", "server", "agent", "bridge", "simulate", "obs-setup",
		"telemetry", "camera-encoder", "refresh-cam", "quality-test", "version"} {
		if !strings.Contains(out, "cricketstream "+c) {
			t.Errorf("help doesn't mention %q", c)
		}
	}
}

func TestE2EUnknownCommandExits2WithUsage(t *testing.T) {
	a := newApp(t)
	out, code := a.run(10*time.Second, "frobnicate")
	if code != 2 || !strings.Contains(out, `unknown command "frobnicate"`) || !strings.Contains(out, "Usage:") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestE2EStartWithoutSetupExplainsAndPoints(t *testing.T) {
	a := newApp(t)
	out, code := a.run(15*time.Second, "start", "--local", "--nvplay")
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	mustContain(t, out, "No config.ini yet", "cricketstream setup")
}

// A double-clicked window must not close on an error nobody has read: fatal paths wait
// for Enter (CRICKETSTREAM_NO_PAUSE is for automation only).
func TestE2EFatalPathsWaitForEnter(t *testing.T) {
	a := newApp(t)
	var env []string
	for _, kv := range a.env {
		if !strings.HasPrefix(kv, "CRICKETSTREAM_NO_PAUSE=") {
			env = append(env, kv)
		}
	}
	a.env = env
	p := a.start("start", "--local", "--nvplay")
	p.waitFor(`Press Enter to close this window`, 15*time.Second)
	time.Sleep(time.Second)
	if !p.running() {
		t.Fatal("closed without waiting for Enter")
	}
	p.say("")
	if code := p.wait(10 * time.Second); code != 1 {
		t.Errorf("exit %d", code)
	}
}

func TestE2EAgentHasTheTemplateBuiltIn(t *testing.T) {
	a := newApp(t)
	out, code := a.run(10*time.Second, "agent", "--check-template")
	if code != 0 || !strings.Contains(out, "bundled: yes") {
		t.Errorf("exit %d: %s", code, out)
	}
}

// Nothing listens on OBS's port in a test (it'd be a real OBS): obs-setup explains.
func TestE2EObsSetupWithoutOBSExplains(t *testing.T) {
	a := newApp(t)
	if !portClosed(4455) {
		t.Skip("something is listening on OBS's port — never touch a real OBS from a test")
	}
	a.streamingConfig("")
	out, code := a.run(30*time.Second, "obs-setup")
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	mustContain(t, out, "Setup incomplete")
}

func TestE2ETelemetrySummaryMatchesTheOriginal(t *testing.T) {
	a := newApp(t)
	src, _ := filepath.Abs(filepath.Join("..", "..", "internal", "tools", "testdata"))
	csv := filepath.Join(src, "telemetry_sample.csv")
	want, err := os.ReadFile(filepath.Join(src, "telemetry_sample.summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out, code := a.run(15*time.Second, "telemetry", "--summary", csv)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if norm(out) != norm(string(want)) {
		t.Errorf("summary differs from the Python original's:\n%s", out)
	}
}

func TestE2EServerRefusesAPortInUse(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	first := a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	out, code := a.run(30*time.Second, "server")
	if code == 0 {
		t.Errorf("second server on the same port exited 0:\n%s", out)
	}
	mustContain(t, out, "already running")
	if !first.running() {
		t.Error("the first server died")
	}
}

// Without CRICKETSTREAM_HOME the app keeps its files beside itself: one folder is the
// whole install.
func TestE2EDataLivesBesideTheApp(t *testing.T) {
	a := newApp(t)
	copyDir := filepath.Join(a.home, "Documents", "CricketStream")
	os.MkdirAll(copyDir, 0o755)
	exe := filepath.Join(copyDir, filepath.Base(exePath))
	b, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range a.env {
		if !strings.HasPrefix(kv, "CRICKETSTREAM_HOME=") {
			env = append(env, kv)
		}
	}
	a.env = env
	saved := exePath
	exePath = exe
	defer func() { exePath = saved }()
	p := a.start("setup")
	base := openSetup(t, p)
	if r := saveSetup(t, base, map[string]any{"role": "scorer", "pcs_folder": ""}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	p.wait(20 * time.Second)
	if _, err := os.Stat(filepath.Join(copyDir, "config.ini")); err != nil {
		t.Errorf("config.ini not beside the app: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.dir, "config.ini")); err == nil {
		t.Error("config.ini written somewhere else")
	}
}

func TestE2EVersion(t *testing.T) {
	a := newApp(t)
	out, code := a.run(10*time.Second, "version")
	if code != 0 || !strings.Contains(out, "CricketStream (development build)") {
		t.Errorf("exit %d: %q", code, out)
	}
}
