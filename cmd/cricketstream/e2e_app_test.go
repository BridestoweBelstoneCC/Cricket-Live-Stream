package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The double-click: CricketStream's own window. The harness runs it with
// CRICKETSTREAM_NO_WINDOW=1, which serves the window's page and opens nothing, and these
// tests press its buttons over HTTP exactly as the page does.

type appPage struct {
	t         *testing.T
	base, key string
}

var keyRx = regexp.MustCompile(`name="app-key" content="([0-9a-f]{32})"`)

// openApp starts the app and finds its page and the page's key.
func (a *app) openApp(args ...string) (*proc, *appPage) {
	a.t.Helper()
	p := a.start(args...)
	m := p.waitFor(`app page (http://127\.0\.0\.1:\d+)/`, 20*time.Second)
	pg := &appPage{t: a.t, base: m[1]}
	page := get(a.t, pg.base+"/")
	k := keyRx.FindStringSubmatch(page.body)
	if page.status != 200 || k == nil {
		a.t.Fatalf("app page %d without its key", page.status)
	}
	pg.key = k[1]
	return p, pg
}

func (pg *appPage) api(method, path string, body any) reply {
	pg.t.Helper()
	return do(pg.t, method, pg.base+"/api/"+path, body, map[string]string{"X-App-Key": pg.key, "Origin": pg.base})
}

func (pg *appPage) status() map[string]any {
	pg.t.Helper()
	return pg.api("GET", "status", nil).json(pg.t)
}

func (pg *appPage) saveSetup(values map[string]any) reply {
	pg.t.Helper()
	return saveSetup(pg.t, pg.base, values)
}

// until polls the status, as the page does, until ok.
func (pg *appPage) until(what string, timeout time.Duration, ok func(map[string]any) bool) map[string]any {
	pg.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := pg.status()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			pg.t.Fatalf("waiting for %s; status: %v", what, st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (pg *appPage) phase(p string, timeout time.Duration) map[string]any {
	pg.t.Helper()
	return pg.until("phase "+p, timeout, func(st map[string]any) bool { return st["phase"] == p })
}

func sub(st map[string]any, k string) map[string]any { m, _ := st[k].(map[string]any); return m }

// The whole streaming-laptop journey through the real binary, in the window: which
// computer is this, the setup page, match day with the page's two answers, the control
// panel, the launcher's own question shown in the window, and stopping.
func TestE2EFirstRunInTheAppThenMatchDayThenStop(t *testing.T) {
	a := newApp(t)
	p, pg := a.openApp()
	if st := pg.status(); st["phase"] != "welcome" {
		t.Fatalf("first run phase %v", st["phase"])
	}
	mustContain(t, get(t, pg.base+"/").body, "which computer is this?", "The streaming laptop", "The scorer")

	// The welcome screen's choice opens setup with the role picked.
	page := get(t, pg.base+"/setup?app=1&role=streaming")
	if page.status != 200 {
		t.Fatalf("setup in the app: %d", page.status)
	}
	mustContain(t, page.body, `id="colour_pick"`, "PRESET", "data-browse")
	if st := get(t, pg.base+"/state?colour=%23ff0000").json(t); st["home_colour"] != "#ff0000" {
		t.Errorf("stream preview colour %v", st["home_colour"])
	}
	if r := do(t, "POST", pg.base+"/setup/save", streamingAnswers(a), map[string]string{"Origin": "http://evil.example"}); r.status != 403 {
		t.Errorf("cross-site save: %d", r.status)
	}
	r := pg.saveSetup(streamingAnswers(a))
	if d := r.json(t); r.status != 200 || d["ok"] != true || d["next"] != "/" {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if got := iniValue(a.read("config.ini"), "App", "role"); got != "streaming" {
		t.Errorf("role %q", got)
	}
	st := pg.phase("home", 5*time.Second)
	if sub(st, "home")["club"] != "Test Town CC" {
		t.Errorf("home %v", st["home"])
	}

	// Start match day: the page answers "just this laptop" and "NV Play".
	if r := pg.api("POST", "start", map[string]any{"remote": false, "manual": false}); r.status != 200 {
		t.Fatalf("start: %d %s", r.status, r.body)
	}
	m := p.waitFor(`Control panel: http://localhost:(\d+)/control`, 60*time.Second)
	if m[1] != strconv.Itoa(a.port) {
		t.Errorf("control panel on port %s, want %d", m[1], a.port)
	}
	st = pg.phase("running", 20*time.Second)
	if st["panel_url"] != fmt.Sprintf("http://127.0.0.1:%d/control", a.port) {
		t.Errorf("panel %v", st["panel_url"])
	}
	mustContain(t, p.output(), "Local mode", "Club: Test Town CC", "match_state.json written")
	if strings.Contains(p.output(), "Opening OBS") {
		t.Error("tried to open an OBS — the test's portable folder has none")
	}
	for _, path := range []string{"/control", "/overlay", "/scoring"} {
		if r := get(t, a.base()+path); r.status != 200 {
			t.Errorf("%s: %d", path, r.status)
		}
	}
	if s := get(t, a.base()+"/state").json(t); s["home_team"] != "Test Town CC" || s["home_abbrev"] != "TTC" {
		t.Errorf("state not seeded from setup: %v / %v", s["home_team"], s["home_abbrev"])
	}
	deadline := time.Now().Add(20 * time.Second)
	var csvs []string
	for time.Now().Before(deadline) && len(csvs) == 0 {
		csvs, _ = filepath.Glob(a.path("diagnostics", "telemetry_*.csv"))
		time.Sleep(200 * time.Millisecond)
	}
	if len(csvs) == 0 {
		t.Error("telemetry didn't start")
	}
	// The activity log has what the console window used to show.
	var lines []string
	for _, l := range st["log"].([]any) {
		lines = append(lines, l.(map[string]any)["t"].(string))
	}
	mustContain(t, strings.Join(lines, "\n"), "Pre-flight check", "Control panel: http://localhost:")
	if b, err := os.ReadFile(filepath.Join(a.dir, "logs", "cricketstream-"+time.Now().Format("2006-01-02")+".log")); err != nil ||
		!strings.Contains(string(b), "Pre-flight check") {
		t.Errorf("log file beside the app: %v", err)
	}
	if r := pg.api("POST", "close", map[string]any{}); r.status != 409 {
		t.Errorf("closed while match day was running: %d", r.status)
	}

	// Stop: the report question comes up in the window; the server stays up until it's
	// answered (the report needs it), then stops.
	pg.api("POST", "stop", map[string]any{})
	st = pg.until("the report question", 30*time.Second, func(st map[string]any) bool { return st["question"] != nil })
	q := sub(st, "question")
	if q["kind"] != "confirm" || !strings.Contains(fmt.Sprint(q["text"]), "match report") {
		t.Fatalf("question %v", q)
	}
	if r := get(t, a.base()+"/health"); r.status != 200 {
		t.Errorf("server gone before the report question was answered: %d", r.status)
	}
	if r := pg.api("POST", "answer", map[string]any{"id": q["id"], "yes": false}); r.status != 200 {
		t.Fatalf("answer: %d", r.status)
	}
	pg.phase("stopped", 30*time.Second)
	mustContain(t, p.output(), "No report generated.", "Server stopped. Bye!", "Match day is over.")
	waitClosed(t, a.port, 10*time.Second)

	if r := pg.api("POST", "close", map[string]any{}); r.status != 200 {
		t.Errorf("close: %d", r.status)
	}
	if code := p.wait(20 * time.Second); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// The scorer's laptop: picked on the welcome screen, then the window shows the sharing;
// the next double-click goes straight to it.
func TestE2EScorerLaptopInTheApp(t *testing.T) {
	a := newApp(t)
	if !portClosed(8788) {
		t.Skip("port 8788 is in use on this machine")
	}
	folder := filepath.Join(a.home, "Documents", "Cricket Matches", "_Scoreboards", "Output")
	os.MkdirAll(folder, 0o755)
	os.MkdirAll(filepath.Join(filepath.Dir(folder), "Templates"), 0o755) // NV Play is installed
	os.WriteFile(filepath.Join(folder, "nvplay-scoreboard1.xml"), []byte(scoreboardXML), 0o644)

	p, pg := a.openApp()
	// The setup page finds NV Play's folder for the scorer.
	info := get(t, pg.base+"/setup/info").json(t)
	if !strings.EqualFold(fmt.Sprint(sub(info, "detected")["folder"]), folder) {
		t.Errorf("detected %v", info["detected"])
	}
	if r := pg.saveSetup(map[string]any{"role": "scorer", "pcs_folder": folder}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if got := iniValue(a.read("config.ini"), "App", "role"); got != "scorer" {
		t.Fatalf("role %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(folder), "Templates", "scoreboard.template")); err != nil {
		t.Errorf("template not installed: %v", err)
	}
	st := pg.until("sharing", 10*time.Second, func(st map[string]any) bool { return sub(st, "agent")["running"] == true })
	ag := sub(st, "agent")
	if !strings.EqualFold(fmt.Sprint(ag["folder"]), folder) || ag["health"] != "live" {
		t.Errorf("agent %v", ag)
	}
	waitHTTP(t, "http://127.0.0.1:8788/ping", 10*time.Second)
	if r := get(t, "http://127.0.0.1:8788/pcs"); r.status != 200 || r.json(t)["content"] != scoreboardXML {
		t.Errorf("/pcs: %d %.200s", r.status, r.body)
	}
	pg.until("the collection shown", 5*time.Second, func(st map[string]any) bool { return sub(st, "agent")["collected"] != nil })

	// Stop sharing and close, as the window's buttons do.
	pg.api("POST", "stop", map[string]any{})
	pg.api("POST", "close", map[string]any{})
	if code := p.wait(20 * time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
	waitClosed(t, 8788, 10*time.Second)

	// Second double-click: straight to sharing, no welcome.
	p2, pg2 := a.openApp()
	if st := pg2.status(); st["phase"] != "agent" {
		t.Errorf("second run phase %v", st["phase"])
	}
	waitHTTP(t, "http://127.0.0.1:8788/ping", 10*time.Second)
	p2.cmd.Process.Kill()
}

// The bridge: its address and token are in the window; it serves the file only with it.
func TestE2EBridgeInTheApp(t *testing.T) {
	a := newApp(t)
	folder := filepath.Join(a.home, "nvplay-output")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "nvplay-scoreboard1.xml"), []byte(scoreboardXML), 0o644)
	bport := freePort(t)

	_, pg := a.openApp()
	r := pg.saveSetup(map[string]any{"role": "bridge", "pcs_folder": folder, "bridge_port": bport})
	if r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	b := sub(pg.until("the bridge", 10*time.Second, func(st map[string]any) bool { return sub(st, "bridge")["running"] == true }), "bridge")
	token, _ := b["token"].(string)
	if len(token) != 32 || !strings.Contains(a.read("bridge_config.ini"), token) {
		t.Fatalf("token %q", token)
	}
	if !strings.HasSuffix(fmt.Sprint(b["url"]), fmt.Sprintf(":%d", bport)) {
		t.Errorf("address %v", b["url"])
	}
	bbase := fmt.Sprintf("http://127.0.0.1:%d", bport)
	if r := get(t, bbase+"/pcs/latest"); r.status != 401 {
		t.Errorf("no token: %d", r.status)
	}
	r = do(t, "GET", bbase+"/pcs/latest", nil, map[string]string{"X-Bridge-Token": token})
	if r.status != 200 || r.json(t)["content"] != scoreboardXML {
		t.Errorf("with token: %d %.200s", r.status, r.body)
	}
	if b := sub(pg.status(), "bridge"); b["collected"] != float64(1) {
		t.Errorf("collected %v", b["collected"])
	}
}

// A folder dropped onto the app is remembered, and the scorer's laptop shares it.
func TestE2EFolderDroppedOnTheApp(t *testing.T) {
	a := newApp(t)
	if !portClosed(8788) {
		t.Skip("port 8788 is in use on this machine")
	}
	a.write("config.ini", "[App]\nrole = scorer\n")
	folder := filepath.Join(a.home, "Output")
	os.MkdirAll(folder, 0o755)
	p, pg := a.openApp(folder)
	if !strings.Contains(a.read("scorer_agent.json"), filepath.Base(folder)) {
		t.Error("folder not remembered")
	}
	ag := sub(pg.until("sharing", 10*time.Second, func(st map[string]any) bool { return sub(st, "agent")["running"] == true }), "agent")
	if !strings.EqualFold(fmt.Sprint(ag["folder"]), folder) {
		t.Errorf("sharing %v", ag["folder"])
	}
	p.cmd.Process.Kill()
}

// Double-clicked again while open: the second copy hands over to the first and exits.
func TestE2ESecondDoubleClickHandsOverToTheOpenWindow(t *testing.T) {
	a := newApp(t)
	port := freePort(t)
	for i, kv := range a.env {
		if strings.HasPrefix(kv, "CRICKETSTREAM_APP_PORT=") {
			a.env[i] = fmt.Sprintf("CRICKETSTREAM_APP_PORT=%d", port)
		}
	}
	first, pg := a.openApp()
	if !strings.HasSuffix(pg.base, fmt.Sprintf(":%d", port)) {
		t.Fatalf("first copy on %s", pg.base)
	}
	out, code := a.run(20 * time.Second)
	if code != 0 || !strings.Contains(out, "already open") {
		t.Errorf("second copy: exit %d\n%s", code, out)
	}
	if !first.running() {
		t.Error("the first copy went away")
	}
}

// At a console (`cricketstream start`): Ctrl+C offers the report while the server is up,
// then stops — the match-day launcher without the window.
func TestE2EStartAtAConsoleStopsOnCtrlC(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	a.write("config.ini", strings.Replace(a.read("config.ini"), "manage_obs = yes", "manage_obs = no", 1))
	p := a.start("start", "--local", "--nvplay")
	p.closeInput()
	p.waitFor(`Control panel: http://localhost:\d+/control`, 60*time.Second)
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

// Ctrl+C at the terminal the app was started from (a Mac's, where its pages are in the
// browser): the same clean stop as the window's Stop — the report question in the page,
// the server stopped, nothing left running.
func TestE2ECtrlCStopsTheAppCleanly(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	a.write("config.ini", strings.Replace(a.read("config.ini"), "manage_obs = yes", "manage_obs = no", 1))
	p, pg := a.openApp()
	pg.phase("home", 5*time.Second)
	pg.api("POST", "start", map[string]any{})
	pg.phase("running", 60*time.Second)
	if err := interrupt(p); err != nil {
		t.Skipf("can't send Ctrl+C here (%v)", err)
	}
	st := pg.until("the report question", 30*time.Second, func(st map[string]any) bool { return st["question"] != nil })
	pg.api("POST", "answer", map[string]any{"id": sub(st, "question")["id"], "yes": false})
	if code := p.wait(30 * time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
	waitClosed(t, a.port, 10*time.Second)
	mustContain(t, p.output(), "Server stopped. Bye!", "Match day is over.")
}

// Without a web view (a PC missing WebView2, macOS for now): the same page in the browser,
// and the page's own Close ends the app.
func TestE2EWithoutAWebViewThePagesOpenInTheBrowser(t *testing.T) {
	a := newApp(t)
	for i, kv := range a.env {
		if kv == "CRICKETSTREAM_NO_WINDOW=1" {
			a.env[i] = "CRICKETSTREAM_NO_WEBVIEW=1" // NO_BROWSER stays: nothing actually opens
		}
	}
	p, pg := a.openApp()
	p.waitFor(`opening it in the browser instead`, 10*time.Second)
	if st := pg.status(); st["phase"] != "welcome" || st["window"] != false {
		t.Errorf("status %v / window %v", st["phase"], st["window"])
	}
	if r := pg.api("POST", "close", map[string]any{}); r.status != 200 {
		t.Fatalf("close: %d", r.status)
	}
	if code := p.wait(15 * time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
}

// "Phones and tablets too" with no club password: match day says it runs on this laptop
// only, and the panel really isn't reachable beyond it.
func TestE2ERemoteWithoutAPasswordStaysOnThisLaptop(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	cfg := strings.Replace(a.read("config.ini"), "manage_obs = yes", "manage_obs = no", 1)
	a.write("config.ini", cfg+"\n[Network]\nbind_host = 0.0.0.0\n") // an older setup wrote this
	for i, kv := range a.env {
		if kv == "CRICKETSTREAM_LOOPBACK_ONLY=1" {
			a.env[i] = "CRICKETSTREAM_LOOPBACK_ONLY=0"
		}
	}
	p, pg := a.openApp()
	if h := sub(pg.status(), "home"); h["remote"] != false || h["password"] != false {
		t.Errorf("home offered phones and tablets without a password: %v", h)
	}
	pg.api("POST", "start", map[string]any{"remote": true})
	pg.phase("running", 60*time.Second)
	mustContain(t, p.output(), "Phones and tablets need a club password", "Local mode")
	if strings.Contains(p.output(), "Remote access mode") {
		t.Error("claimed remote access")
	}
	pg.api("POST", "stop", map[string]any{})
	st := pg.until("the report question", 30*time.Second, func(st map[string]any) bool { return st["question"] != nil })
	pg.api("POST", "answer", map[string]any{"id": sub(st, "question")["id"], "yes": false})
	pg.phase("stopped", 30*time.Second)
}
