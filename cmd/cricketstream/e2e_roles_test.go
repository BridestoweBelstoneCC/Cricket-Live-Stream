package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const scoreboardXML = `<?xml version="1.0"?><scoreboard><score>101</score></scoreboard>`

// openSetup waits for the setup page the app opened and returns its base URL.
func openSetup(t *testing.T, p *proc) string {
	t.Helper()
	m := p.waitFor(`Setup is open in your browser:\s+(http://\S+)/setup`, 20*time.Second)
	return m[1]
}

func saveSetup(t *testing.T, base string, values map[string]any) reply {
	t.Helper()
	return do(t, "POST", base+"/setup/save", values, map[string]string{"Origin": base})
}

func streamingAnswers(a *app) map[string]any {
	return map[string]any{"role": "streaming", "name": "Test Town CC", "abbrev": "TTC", "colour": "#123456",
		"pc_id": "4242", "pcs_folder": "", "scorebar_style": "impact", "replay_folder": filepath.Join(a.home, "Replays"),
		"yt_title": "LIVE: {home} vs {away}", "max_overs": "40", "allow_remote": false}
}

// The double-click on a new machine: the setup page, then straight into match day, then
// Ctrl+C — the whole streaming-laptop journey through the real binary.
func TestE2EFirstRunSetupThenMatchDayThenShutdown(t *testing.T) {
	a := newApp(t)
	p := a.start()
	base := openSetup(t, p)

	page := get(t, base+"/setup")
	if page.status != 200 {
		t.Fatalf("setup page: %d", page.status)
	}
	mustContain(t, page.body, "What is this computer for?", `id="colour_pick"`, "Save and start")

	info := get(t, base+"/setup/info").json(t)
	if info["have_config"] != false {
		t.Errorf("have_config = %v on a new machine", info["have_config"])
	}
	if styles, _ := info["styles"].([]any); len(styles) != 4 {
		t.Errorf("styles: %v", info["styles"])
	}

	// The scorebar preview on the page: the real overlay, with the colour being picked.
	if r := get(t, base+"/overlay?preview=1&style=modern"); r.status != 200 || !strings.Contains(r.body, "scorebar") {
		t.Errorf("preview overlay: %d", r.status)
	}
	st := get(t, base+"/state?colour="+url.QueryEscape("#ff0000")).json(t)
	if st["home_colour"] != "#ff0000" {
		t.Errorf("preview state colour %v", st["home_colour"])
	}

	if r := do(t, "POST", base+"/setup/save", streamingAnswers(a), map[string]string{"Origin": "http://evil.example"}); r.status != 403 {
		t.Errorf("cross-site save: %d", r.status)
	}
	bad := streamingAnswers(a)
	bad["name"] = ""
	if r := saveSetup(t, base, bad); r.status != 400 || !strings.Contains(r.body, "club name") {
		t.Errorf("save without a club name: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(a.path("config.ini")); err == nil {
		t.Fatal("a refused form wrote config.ini")
	}

	r := saveSetup(t, base, streamingAnswers(a))
	if r.status != 200 || r.json(t)["ok"] != true {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	cfg := a.read("config.ini")
	for _, kv := range [][3]string{{"App", "role", "streaming"}, {"Club", "name", "Test Town CC"},
		{"Club", "playcricket_id", "4242"}, {"Graphics", "scorebar_style", "impact"}, {"Stream", "max_overs", "40"}} {
		if got := iniValue(cfg, kv[0], kv[1]); got != kv[2] {
			t.Errorf("config.ini [%s] %s = %q, want %q", kv[0], kv[1], got, kv[2])
		}
	}

	// Match day carries straight on; no keyboard, so the questions take their defaults.
	p.closeInput()
	m := p.waitFor(`Control panel: http://localhost:(\d+)/control`, 60*time.Second)
	if m[1] != strconv.Itoa(a.port) {
		t.Errorf("control panel on port %s, want %d", m[1], a.port)
	}
	mustContain(t, p.output(), "Saved. This machine is the streaming laptop", "Local mode",
		"Club: Test Town CC", "match_state.json written")
	if strings.Contains(p.output(), "Opening OBS") {
		t.Error("tried to open an OBS — the test's portable folder has none")
	}

	for _, path := range []string{"/control", "/overlay", "/scoring"} {
		if r := get(t, a.base()+path); r.status != 200 || !strings.Contains(r.header.Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %s", path, r.status, r.header.Get("Content-Type"))
		}
	}
	state := get(t, a.base()+"/state").json(t)
	if state["home_team"] != "Test Town CC" || state["home_abbrev"] != "TTC" {
		t.Errorf("state not seeded from setup: %v / %v", state["home_team"], state["home_abbrev"])
	}
	if h := get(t, a.base()+"/health"); h.status != 200 {
		t.Errorf("/health: %d", h.status)
	}

	// The launcher starts the telemetry logger by itself.
	deadline := time.Now().Add(20 * time.Second)
	var csvs []string
	for time.Now().Before(deadline) && len(csvs) == 0 {
		csvs, _ = filepath.Glob(a.path("diagnostics", "telemetry_*.csv"))
		time.Sleep(200 * time.Millisecond)
	}
	if len(csvs) == 0 {
		t.Error("telemetry didn't start")
	}

	// Ctrl+C: airtime and the report offer while the server is still up, then it stops.
	if err := interrupt(p); err != nil {
		t.Skipf("can't send Ctrl+C here (%v) — the rest needs a console", err)
	}
	p.waitFor(`Server stopped\. Bye!`, 30*time.Second)
	if code := p.wait(15 * time.Second); code != 0 {
		t.Errorf("exit code %d after Ctrl+C", code)
	}
	waitClosed(t, a.port, 10*time.Second)
	mustContain(t, p.output(), "Shutting down server")
}

// The scorer's laptop: setup picks the role, then it serves the scoreboard file; the
// next double-click goes straight to the agent.
func TestE2EScorerLaptopRole(t *testing.T) {
	a := newApp(t)
	if !portClosed(8788) {
		t.Skip("port 8788 is in use on this machine")
	}
	folder := filepath.Join(a.home, "Documents", "Cricket Matches", "_Scoreboards", "Output")
	os.MkdirAll(folder, 0o755)
	os.MkdirAll(filepath.Join(filepath.Dir(folder), "Templates"), 0o755) // NV Play is installed
	os.WriteFile(filepath.Join(folder, "nvplay-scoreboard1.xml"), []byte(scoreboardXML), 0o644)

	p := a.start()
	base := openSetup(t, p)
	r := saveSetup(t, base, map[string]any{"role": "scorer", "pcs_folder": folder})
	if r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if got := iniValue(a.read("config.ini"), "App", "role"); got != "scorer" {
		t.Fatalf("role saved as %q", got)
	}
	// NV Play's Templates folder (beside its Output folder) gets the scoreboard template.
	if _, err := os.Stat(filepath.Join(filepath.Dir(folder), "Templates", "scoreboard.template")); err != nil {
		t.Errorf("template not installed into NV Play's Templates folder: %v", err)
	}
	p.waitFor(`Serving on`, 20*time.Second)
	waitHTTP(t, "http://127.0.0.1:8788/ping", 10*time.Second)
	ping := get(t, "http://127.0.0.1:8788/ping").json(t)
	if !strings.EqualFold(fmt.Sprint(ping["folder"]), folder) {
		t.Errorf("/ping folder %v", ping["folder"])
	}
	if r := get(t, "http://127.0.0.1:8788/pcs"); r.status != 200 || r.json(t)["content"] != scoreboardXML {
		t.Errorf("/pcs: %d %.200s", r.status, r.body)
	}
	p.cmd.Process.Kill()
	p.wait(10 * time.Second)
	waitClosed(t, 8788, 10*time.Second)

	// Second double-click: no setup page, straight to the agent.
	p2 := a.start()
	p2.closeInput()
	p2.waitFor(`Watching folder`, 20*time.Second)
	if strings.Contains(p2.output(), "Setup is open") {
		t.Error("asked the setup questions again")
	}
	waitHTTP(t, "http://127.0.0.1:8788/ping", 10*time.Second)
}

// The bridge machine: setup shows the token; the bridge serves the file only with it.
func TestE2EBridgeRole(t *testing.T) {
	a := newApp(t)
	folder := filepath.Join(a.home, "nvplay-output")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "nvplay-scoreboard1.xml"), []byte(scoreboardXML), 0o644)
	bport := freePort(t)

	p := a.start()
	base := openSetup(t, p)
	r := saveSetup(t, base, map[string]any{"role": "bridge", "pcs_folder": folder, "bridge_port": bport})
	if r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	d := r.json(t)
	token, _ := d["bridge_token"].(string)
	if len(token) != 32 {
		t.Fatalf("token %q", token)
	}
	if !strings.Contains(a.read("bridge_config.ini"), token) {
		t.Error("token not saved in bridge_config.ini")
	}
	bbase := fmt.Sprintf("http://127.0.0.1:%d", bport)
	waitHTTP(t, bbase+"/pcs/ping", 20*time.Second)
	if r := get(t, bbase+"/pcs/ping"); r.status != 200 {
		t.Errorf("ping %d", r.status)
	}
	if r := get(t, bbase+"/pcs/latest"); r.status != 401 {
		t.Errorf("no token: %d", r.status)
	}
	if r := do(t, "GET", bbase+"/pcs/latest", nil, map[string]string{"X-Bridge-Token": "wrong"}); r.status != 401 {
		t.Errorf("wrong token: %d", r.status)
	}
	r = do(t, "GET", bbase+"/pcs/latest", nil, map[string]string{"X-Bridge-Token": token})
	if r.status != 200 || r.json(t)["content"] != scoreboardXML {
		t.Errorf("with token: %d %.200s", r.status, r.body)
	}
	mustContain(t, p.output(), token)
}

// Re-running setup on a configured machine: answers filled in, secrets never sent to the
// page, and blank secret fields keep what was there.
func TestE2ESetupRerunKeepsSecrets(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("\n[API]\nanthropic_key = sk-ant-secret\nplaycricket_key = pc-secret\n\n[Auth]\nclub_password = hunter2\n")
	p := a.start("setup")
	base := openSetup(t, p)
	info := get(t, base+"/setup/info")
	if strings.Contains(info.body, "sk-ant-secret") || strings.Contains(info.body, "hunter2") || strings.Contains(info.body, "pc-secret") {
		t.Fatal("a secret reached the setup page")
	}
	d := info.json(t)
	ex, _ := d["existing"].(map[string]any)
	if d["have_config"] != true || ex["name"] != "Test Town CC" {
		t.Fatalf("existing answers not offered: %v", d["existing"])
	}
	ans := streamingAnswers(a)
	ans["name"] = "Renamed CC"
	if r := saveSetup(t, base, ans); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if code := p.wait(20 * time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
	cfg := a.read("config.ini")
	for _, kv := range [][3]string{{"Club", "name", "Renamed CC"}, {"API", "anthropic_key", "sk-ant-secret"},
		{"API", "playcricket_key", "pc-secret"}, {"Auth", "club_password", "hunter2"}} {
		if got := iniValue(cfg, kv[0], kv[1]); got != kv[2] {
			t.Errorf("[%s] %s = %q, want %q", kv[0], kv[1], got, kv[2])
		}
	}
	mustContain(t, p.output(), "Saved. This machine is set up as: the streaming laptop")
}

// Everything is built in, and a file beside the app wins — picked up on the next request.
func TestE2EServerServesBuiltInPagesAndOverridesBesideTheApp(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	p := a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	builtIn := get(t, a.base()+"/overlay")
	if builtIn.status != 200 || !strings.Contains(builtIn.body, "scorebar") {
		t.Fatalf("built-in overlay: %d", builtIn.status)
	}
	if r := get(t, a.base()+"/favicon.ico"); r.status != 200 || !strings.HasPrefix(r.body, "\x89PNG") {
		t.Errorf("favicon: %d", r.status)
	}
	a.write("overlay.html", "<!doctype html><title>edited</title><p>CLUB-EDITED-OVERLAY</p>")
	if r := get(t, a.base()+"/overlay"); !strings.Contains(r.body, "CLUB-EDITED-OVERLAY") {
		t.Error("an overlay.html beside the app wasn't used")
	}
	os.Remove(a.path("overlay.html"))
	if r := get(t, a.base()+"/overlay"); strings.Contains(r.body, "CLUB-EDITED-OVERLAY") {
		t.Error("removing the edited copy didn't go back to the built-in one")
	}
	if !p.running() {
		t.Fatal("server exited")
	}
}

// Rehearsal: the simulator points the running server at itself and plays a match.
func TestE2ESimulatorDrivesTheRunningServer(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	out, code := a.run(90*time.Second, "simulate", "--configure", "--ball-seconds", "0.01", "--overs", "2", "--seed", "7")
	if code != 0 {
		t.Fatalf("simulate exit %d:\n%s", code, out)
	}
	mustContain(t, out, "Server now reading from the simulator folder")
	v := get(t, a.base()+"/live/view").json(t)
	st, _ := v["state"].(map[string]any)
	if fmt.Sprint(st["score"]) == "0" || st["score"] == nil {
		t.Errorf("/live/view after a simulated match: %v", st)
	}
}

// A folder dropped onto the app starts the scorer agent on it, and it's remembered.
func TestE2EFolderDroppedOnTheAppStartsTheAgent(t *testing.T) {
	a := newApp(t)
	if !portClosed(8788) {
		t.Skip("port 8788 is in use on this machine")
	}
	folder := filepath.Join(a.home, "Output")
	os.MkdirAll(folder, 0o755)
	p := a.start(folder)
	p.closeInput()
	p.waitFor(`Watching folder : `+regexpQuote(folder), 20*time.Second)
	if !strings.Contains(a.read("scorer_agent.json"), filepath.Base(folder)) {
		t.Error("folder not remembered")
	}
}

func regexpQuote(s string) string { return strings.NewReplacer(`\`, `\\`, `.`, `\.`, `(`, `\(`, `)`, `\)`).Replace(s) }
