// Package integration drives the built app against a real OBS. Opt-in: it only runs with
// CRICKETSTREAM_OBS_PORTABLE pointing at a PORTABLE OBS copy (a folder holding
// obs_portable_mode.txt), so it can never touch anyone's own scenes:
//
//	xcopy /E /I "C:\Program Files\obs-studio" C:\obs-portable
//	type nul > C:\obs-portable\obs_portable_mode.txt
//	set CRICKETSTREAM_OBS_PORTABLE=C:\obs-portable
//	go test ./internal/integration/ -v -count=1
//
// It refuses to start while any OBS is already open. What it covers, end to end: OBS prep
// (WebSocket switched on, password into config.ini, replay buffer), opening OBS, scene
// setup (and that a second run changes nothing), the server reaching OBS (stream check,
// adding a camera, a replay landing on disk), and the diagnostic tools (telemetry,
// refresh-cam, camera-encoder's OBS lookup, quality-test's not-live refusal).
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func call(t *testing.T, target obsws.Target, typ string, data any) map[string]any {
	t.Helper()
	c, err := obsws.Dial(target, 10*time.Second)
	if err != nil {
		t.Fatalf("OBS: %v", err)
	}
	defer c.Close()
	r, err := c.Request(typ, data)
	if err != nil || !r.OK {
		t.Fatalf("%s: %v %+v", typ, err, r)
	}
	return r.Data
}

func names(list any, key string) []string {
	var out []string
	l, _ := list.([]any)
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, fmt.Sprint(m[key]))
		}
	}
	return out
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ownOBSFiles maps every file in the installed OBS's own settings folder to its mtime.
func ownOBSFiles(t *testing.T) map[string]time.Time {
	out := map[string]time.Time{}
	root := filepath.Join(os.Getenv("APPDATA"), "obs-studio")
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out[p] = info.ModTime()
		}
		return nil
	})
	return out
}

func TestAgainstPortableOBS(t *testing.T) {
	root := os.Getenv(obs.PortableEnv)
	if root == "" {
		t.Skip("set " + obs.PortableEnv + " to a portable OBS copy to run this")
	}
	if runtime.GOOS != "windows" {
		t.Skip("portable OBS is a Windows feature")
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		t.Fatalf("%s has no obs_portable_mode.txt — refusing", root)
	}
	if obs.Running() {
		t.Skip("an OBS is already open; close it first (this must only ever drive the portable copy)")
	}

	// The whole point of the portable copy: the machine's own OBS settings must come out
	// of this exactly as they went in.
	own := ownOBSFiles(t)
	t.Cleanup(func() {
		after := ownOBSFiles(t)
		for p, m := range after {
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
	cfgText := strings.Replace(string(example), "replay_folder =", "replay_folder = "+replays, 1)
	cfgPath := filepath.Join(app, "config.ini")
	os.WriteFile(cfgPath, []byte(cfgText), 0o644)
	statePath := filepath.Join(app, "match_state.json")

	// ── prep + open ──
	if !obs.Prepare(cfgPath, nil, func(s string) { t.Log(s) }) {
		t.Fatal("OBS prep/launch failed")
	}
	t.Cleanup(func() {
		if !obs.Close(30 * time.Second) {
			obs.Kill()
		}
	})
	cfg, _ := ini.Load(cfgPath)
	password := cfg.Get("OBS", "obs_password", "")
	if password == "" {
		t.Fatal("prep didn't copy OBS's WebSocket password into config.ini")
	}
	target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: password}

	// ── scene setup, twice ──
	ok, msgs := obs.SetupFromConfig(cfgPath, statePath, true, true)
	t.Log(strings.Join(msgs, "\n"))
	if !ok {
		t.Fatal("OBS setup failed")
	}
	scenes := names(call(t, target, "GetSceneList", nil)["scenes"], "sceneName")
	for _, s := range []string{"Main", "Replay"} {
		if !has(scenes, s) {
			t.Errorf("scene %s missing: %v", s, scenes)
		}
	}
	inputs := names(call(t, target, "GetInputList", nil)["inputs"], "inputName")
	for _, s := range []string{"Overlay", "ReplayClip"} {
		if !has(inputs, s) {
			t.Errorf("input %s missing: %v", s, inputs)
		}
	}
	countItems := func() int {
		l, _ := call(t, target, "GetSceneItemList", map[string]any{"sceneName": "Main"})["sceneItems"].([]any)
		return len(l)
	}
	before := countItems()
	if ok, _ := obs.SetupFromConfig(cfgPath, statePath, false, false); !ok {
		t.Error("second setup run failed")
	}
	if after := countItems(); after != before {
		t.Errorf("second setup run changed Main: %d items -> %d", before, after)
	}
	if rb := call(t, target, "GetReplayBufferStatus", nil); rb["outputActive"] != true {
		t.Errorf("replay buffer not running: %v", rb)
	}

	// ── the built app ──
	exe := filepath.Join(app, "CricketStream.exe")
	build := exec.Command("go", "build", "-o", exe, "../../cmd/cricketstream")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(timeout time.Duration, args ...string) (string, int) {
		cmd := exec.Command(exe, args...)
		cmd.Dir = app
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		cmd.Start()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(timeout):
			cmd.Process.Kill()
			<-done
		}
		return buf.String(), cmd.ProcessState.ExitCode()
	}

	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	srv := exec.Command(exe, "server")
	srv.Dir = app
	srv.Env = append(os.Environ(), fmt.Sprintf("CRICKETSTREAM_PORT=%d", port))
	var srvLog bytes.Buffer
	srv.Stdout, srv.Stderr = &srvLog, &srvLog
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Process.Kill()
		srv.Wait()
		t.Logf("server log:\n%s", srvLog.String())
	})
	get := func(path string) map[string]any {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		var m map[string]any
		b, _ := io.ReadAll(resp.Body)
		json.Unmarshal(b, &m)
		return m
	}
	post := func(path string, body any) map[string]any {
		b, _ := json.Marshal(body)
		resp, err := http.Post(base+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		var m map[string]any
		raw, _ := io.ReadAll(resp.Body)
		json.Unmarshal(raw, &m)
		return m
	}
	up := false
	for i := 0; i < 40 && !up; i++ {
		if r, err := http.Get(base + "/health"); err == nil {
			r.Body.Close()
			up = r.StatusCode == 200
		} else {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !up {
		t.Fatal("server never answered /health")
	}

	sc := get("/obs/stream_check")
	t.Logf("/obs/stream_check: %v", sc)
	if sc == nil {
		t.Error("/obs/stream_check gave no JSON")
	}

	cam := post("/obs/add_camera", map[string]any{"url": "rtsp://viewer:s3cret@192.0.2.10:554/h264Preview_01_main", "name": "Cam"})
	if cam["ok"] != true {
		t.Errorf("add camera: %v", cam)
	}
	inputs = names(call(t, target, "GetInputList", nil)["inputs"], "inputName")
	if !has(inputs, "Cam") {
		t.Errorf("camera input not created: %v", inputs)
	}

	post("/state", map[string]any{"replay_enabled": true})
	if r := post("/replay", map[string]any{"reason": "integration test"}); r["ok"] != true {
		t.Errorf("replay: %v", r)
	}
	var saved []string
	for i := 0; i < 40 && len(saved) == 0; i++ {
		time.Sleep(500 * time.Millisecond)
		saved, _ = filepath.Glob(filepath.Join(replays, "*"))
	}
	if len(saved) == 0 {
		t.Error("no replay file landed in the replay folder")
	}

	// ── diagnostic tools ──
	out, code := run(30*time.Second, "refresh-cam", "--once")
	if code != 0 || !strings.Contains(out, "reloaded 'Cam'") {
		t.Errorf("refresh-cam --once: exit %d\n%s", code, out)
	}

	out, _ = run(15*time.Second, "camera-encoder")
	if !strings.Contains(out, "camera 192.0.2.10 (from OBS source 'Cam', user 'viewer')") {
		t.Errorf("camera-encoder didn't read the camera from OBS:\n%s", out)
	}

	out, code = run(30*time.Second, "quality-test")
	if code == 0 || !strings.Contains(out, "OBS is not currently streaming") {
		t.Errorf("quality-test should refuse when not live: exit %d\n%s", code, out)
	}

	// Telemetry reads the server on its usual port (not this test's), so only its OBS
	// columns are checked here.
	out, _ = run(5*time.Second, "telemetry", "--interval", "1", "--probe-every", "0")
	if strings.Contains(out, "OBS not reachable") {
		t.Errorf("telemetry couldn't reach OBS:\n%s", out)
	}
	csvs, _ := filepath.Glob(filepath.Join(app, "diagnostics", "telemetry_*.csv"))
	if len(csvs) != 1 {
		t.Fatalf("telemetry csv: %v\n%s", csvs, out)
	}
	b, _ := os.ReadFile(csvs[0])
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 3 {
		t.Fatalf("telemetry wrote %d lines:\n%s", len(lines), b)
	}
	head := strings.Split(strings.TrimSpace(lines[0]), ",")
	last := strings.Split(strings.TrimSpace(lines[len(lines)-1]), ",")
	col := map[string]string{}
	for i, k := range head {
		if i < len(last) {
			col[k] = last[i]
		}
	}
	if col["obs_fps"] == "" || col["obs_fps"] == "0" || col["stream_active"] != "False" {
		t.Errorf("telemetry OBS columns: fps=%q active=%q (row %s)", col["obs_fps"], col["stream_active"], lines[len(lines)-1])
	}
}
