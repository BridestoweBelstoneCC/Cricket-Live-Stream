package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/agent"
	"cricketstream/internal/assets"
	"cricketstream/internal/bridge"
	"cricketstream/internal/browser"
	"cricketstream/internal/console"
	"cricketstream/internal/launcher"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

type M = map[string]any

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// testApp is the app's page and API on a test server, in a folder of its own: nothing
// opens, nothing reaches this machine's home folder, NV Play or OBS.
type testApp struct {
	*App
	t   *testing.T
	srv *httptest.Server
	dir string
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	dir := t.TempDir()
	saved := assets.Dir()
	assets.SetDir(dir)
	t.Cleanup(func() { assets.SetDir(saved) })
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o755)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("CRICKETSTREAM_NO_BROWSER", "1")
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "")
	portable := filepath.Join(dir, "obs-portable")
	os.MkdirAll(portable, 0o755)
	os.WriteFile(filepath.Join(portable, "obs_portable_mode.txt"), nil, 0o644)
	t.Setenv(obs.PortableEnv, portable)
	t.Setenv(obsws.WSPortEnv, fmt.Sprint(freePort(t)))
	savedOpen, savedFolder := browser.Open, browser.OpenFolder
	browser.Open = func(string) error { return nil }
	browser.OpenFolder = func(string) error { return nil }
	savedLaunch, savedAlready, savedAgent := runLauncher, alreadyRunning, agentOptions
	alreadyRunning = func() bool { return false }
	agentOptions = agent.Options{Port: freePort(t), DiscoveryPort: freePort(t)}
	t.Cleanup(func() {
		browser.Open, browser.OpenFolder = savedOpen, savedFolder
		runLauncher, alreadyRunning, agentOptions = savedLaunch, savedAlready, savedAgent
	})

	a := &App{opts: Options{Headless: true}, key: randomKey(), log: &Log{}, ask: newAsker(), done: make(chan struct{})}
	srv := httptest.NewServer(a)
	a.base = srv.URL
	ta := &testApp{App: a, t: t, srv: srv, dir: dir}
	console.SetUI(a.ask)
	t.Cleanup(func() {
		console.SetUI(nil)
		a.ask.close()
		a.stopAgent()
		a.stopBridge()
		srv.Close()
	})
	return ta
}

type reply struct {
	status int
	body   string
}

func (r reply) json(t *testing.T) M {
	t.Helper()
	var d M
	if err := json.Unmarshal([]byte(r.body), &d); err != nil {
		t.Fatalf("not JSON (%d): %s", r.status, r.body)
	}
	return d
}

func (ta *testApp) do(method, path string, body any, headers map[string]string) reply {
	ta.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ta.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ta.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, string(b)}
}

// api is the page's own call: the key and its own origin.
func (ta *testApp) api(method, path string, body any) reply {
	ta.t.Helper()
	return ta.do(method, "/api/"+path, body, map[string]string{"X-App-Key": ta.key, "Origin": ta.srv.URL})
}

func (ta *testApp) status() M {
	ta.t.Helper()
	return ta.api("GET", "status", nil).json(ta.t)
}

func (ta *testApp) saveSetup(v M) reply {
	ta.t.Helper()
	return ta.do("POST", "/setup/save", v, map[string]string{"Origin": ta.srv.URL})
}

// waitFor polls the page's status until ok, as the page does.
func (ta *testApp) waitFor(what string, ok func(M) bool) M {
	ta.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := ta.status()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			ta.t.Fatalf("waiting for %s; last status: %v", what, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func phaseIs(p string) func(M) bool { return func(st M) bool { return st["phase"] == p } }

// ── The page and who may drive it ──

func TestPageCarriesItsKeyAndOnlyThePageCanDriveTheApp(t *testing.T) {
	ta := newTestApp(t)
	page := ta.do("GET", "/", nil, nil)
	if page.status != 200 || !strings.Contains(page.body, `content="`+ta.key+`"`) {
		t.Fatalf("page %d without its key", page.status)
	}
	if !strings.Contains(page.body, "Which computer is this?") && !strings.Contains(page.body, "which computer is this?") {
		t.Error("no welcome question on the page")
	}
	if r := ta.do("GET", "/api/status", nil, nil); r.status != 403 {
		t.Errorf("status without the key: %d", r.status)
	}
	if r := ta.do("GET", "/api/status", nil, map[string]string{"X-App-Key": "0123"}); r.status != 403 {
		t.Errorf("status with a wrong key: %d", r.status)
	}
	// Another website can't post to it, even holding the key...
	if r := ta.do("POST", "/api/close", M{}, map[string]string{"X-App-Key": ta.key, "Origin": "http://evil.example"}); r.status != 403 {
		t.Errorf("cross-origin close: %d", r.status)
	}
	// ...or reach it under another name (DNS rebinding).
	if r := ta.do("GET", "/", nil, map[string]string{"Host": "evil.example"}); r.status != http.StatusMisdirectedRequest {
		t.Errorf("rebound host: %d", r.status)
	}
	if r := ta.do("POST", "/setup/save", M{"role": "scorer"}, map[string]string{"Origin": "http://evil.example"}); r.status != 403 {
		t.Errorf("cross-origin setup save: %d", r.status)
	}
	if _, err := os.Stat(filepath.Join(ta.dir, "config.ini")); err == nil {
		t.Error("a refused save wrote config.ini")
	}
	// The brand mark and the setup page's preview are served; nothing outside brand/ is.
	if r := ta.do("GET", "/brand/mark-transparent.png", nil, nil); r.status != 200 {
		t.Errorf("brand mark: %d", r.status)
	}
	if r := ta.do("GET", "/brand/../config.example.ini.png", nil, nil); r.status == 200 {
		t.Error("served a path outside brand/")
	}
	if r := ta.do("GET", "/setup?app=1&role=scorer", nil, nil); r.status != 200 || !strings.Contains(r.body, "csApp") {
		t.Errorf("setup page in the window: %d", r.status)
	}
	if r := ta.do("GET", "/overlay?preview=1", nil, nil); r.status != 200 {
		t.Errorf("stream preview: %d", r.status)
	}
	if st := ta.status(); st["phase"] != "" && st["phase"] != phWelcome {
		t.Errorf("phase %v before anything started", st["phase"])
	}
}

// ── First run: which computer is this? ──

func TestFirstRunAsTheScorersLaptopSharesTheScoreInTheWindow(t *testing.T) {
	ta := newTestApp(t)
	ta.enterRole("")
	if st := ta.status(); st["phase"] != phWelcome {
		t.Fatalf("first run phase %v", st["phase"])
	}
	nv := filepath.Join(ta.dir, "nv", "Output")
	os.MkdirAll(nv, 0o755)
	os.WriteFile(filepath.Join(nv, "nvplay-scoreboard1.xml"), []byte("<x/>"), 0o644)

	r := ta.saveSetup(M{"role": "scorer", "pcs_folder": nv})
	d := r.json(t)
	if r.status != 200 || d["ok"] != true || d["next"] != "/" {
		t.Fatalf("save: %d %v", r.status, d)
	}
	st := ta.waitFor("the agent", func(st M) bool { a, _ := st["agent"].(M); return a != nil && a["running"] == true })
	a := st["agent"].(M)
	if a["folder"] != nv || a["health"] != "live" || a["file"] != "nvplay-scoreboard1.xml" {
		t.Errorf("agent status %v", a)
	}
	// It's really serving: the streaming laptop collects the file, and the window says so.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/pcs", agentOptions.Port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	st = ta.waitFor("the collection", func(st M) bool { a, _ := st["agent"].(M); return a["collected"] != nil && a["collected"].(float64) > 0 })
	if st["agent"].(M)["client"] != "127.0.0.1" {
		t.Errorf("client %v", st["agent"].(M)["client"])
	}

	// Choosing another folder: a bad one is refused, a good one restarts the sharing on it.
	if r := ta.api("POST", "agent/folder", M{"folder": filepath.Join(ta.dir, "nope")}); r.status != 400 {
		t.Errorf("missing folder: %d", r.status)
	}
	other := filepath.Join(ta.dir, "other")
	os.MkdirAll(other, 0o755)
	if r := ta.api("POST", "agent/folder", M{"folder": `"` + other + `"`}); r.status != 200 {
		t.Fatalf("other folder: %d %s", r.status, r.body)
	}
	if got := agent.LoadSavedFolder(); got != other {
		t.Errorf("remembered %q", got)
	}
	a = ta.status()["agent"].(M)
	if a["folder"] != other || a["running"] != true || a["file"] != "" {
		t.Errorf("after the change: %v", a)
	}

	// Closing asks first (the scorer would stop sharing), then stops and closes.
	if ta.closeRequested() {
		t.Fatal("closed without asking while sharing")
	}
	ta.api("POST", "stop", M{})
	ta.waitFor("sharing stopped", func(st M) bool { return st["agent"].(M)["running"] == false })
	if r := ta.api("POST", "close", M{}); r.status != 200 {
		t.Errorf("close: %d", r.status)
	}
	select {
	case <-ta.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the app didn't close")
	}
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ping", agentOptions.Port)); err == nil {
		t.Error("still sharing after close")
	}
}

// No folder found: the window asks for one (never a console prompt), and the setup's
// folder is used when it exists.
func TestScorersLaptopWithoutAFolderAsksForOne(t *testing.T) {
	ta := newTestApp(t)
	if r := ta.saveSetup(M{"role": "scorer", "pcs_folder": ""}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	a := ta.status()["agent"].(M)
	if a["need_folder"] != true || a["running"] != false {
		t.Fatalf("expected to ask for a folder: %v", a)
	}
	if ta.ask.Current() != nil {
		t.Error("asked a console-style question instead of showing the folder box")
	}
	nv := filepath.Join(ta.dir, "Output")
	os.MkdirAll(nv, 0o755)
	if r := ta.api("POST", "agent/folder", M{"folder": nv}); r.status != 200 {
		t.Fatalf("choose: %d", r.status)
	}
	if a := ta.status()["agent"].(M); a["running"] != true || a["need_folder"] != false {
		t.Errorf("after choosing: %v", a)
	}
}

func TestBridgeMachineShowsItsAddressAndToken(t *testing.T) {
	ta := newTestApp(t)
	nv := filepath.Join(ta.dir, "Output")
	os.MkdirAll(nv, 0o755)
	os.WriteFile(filepath.Join(nv, "nvplay-scoreboard1.xml"), []byte("<x/>"), 0o644)
	port := freePort(t)
	r := ta.saveSetup(M{"role": "bridge", "pcs_folder": nv, "bridge_port": port})
	if r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	b := ta.waitFor("the bridge", func(st M) bool { b, _ := st["bridge"].(M); return b != nil && b["running"] == true })["bridge"].(M)
	token, _ := b["token"].(string)
	if len(token) != 32 || b["folder_ok"] != true || !strings.HasSuffix(b["url"].(string), fmt.Sprintf(":%d", port)) {
		t.Fatalf("bridge status %v", b)
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/pcs/latest", port), nil)
	req.Header.Set("X-Bridge-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// A wrong token isn't counted as the streaming laptop collecting it.
	req.Header.Set("X-Bridge-Token", "wrong")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	b = ta.status()["bridge"].(M)
	if b["collected"] != float64(1) {
		t.Errorf("collected %v", b["collected"])
	}
	if c, ok := bridge.Load(); !ok || c.Token != token {
		t.Error("token not saved in bridge_config.ini")
	}
}

// ── The streaming laptop ──

// Match day in the window: the two questions answered by the page, the launcher's own
// questions shown in the window, the panel shown once the server is up, and stopping.
func TestMatchDayInTheWindow(t *testing.T) {
	ta := newTestApp(t)
	undo, _ := ta.log.capture() // what Run does: everything printed reaches the window's log
	defer undo()
	var got launcher.Options
	ran := make(chan struct{})
	runLauncher = func(o launcher.Options) int {
		got = o
		close(ran)
		fmt.Println("  ✓ Club: Test Town CC")
		o.OpenPanel("http://127.0.0.1:5000/control", "tok")
		<-o.Interrupt // the operator stops match day
		if yes, ok := console.Confirm("Generate an AI match report for this game?", false); ok && yes {
			fmt.Println("  Saved to: match_report_today.txt")
		}
		return 0
	}
	if r := ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	st := ta.status()
	if st["phase"] != phHome || st["home"].(M)["club"] != "Test Town CC" {
		t.Fatalf("home: %v %v", st["phase"], st["home"])
	}
	if r := ta.api("POST", "start", M{"remote": true, "manual": false}); r.status != 200 {
		t.Fatalf("start: %d %s", r.status, r.body)
	}
	<-ran
	if got.Remote == nil || !*got.Remote || got.Manual == nil || *got.Manual {
		t.Errorf("the page's answers didn't reach the launcher: %v %v", got.Remote, got.Manual)
	}
	st = ta.waitFor("the panel", phaseIs(phRunning))
	if st["panel_url"] != "http://127.0.0.1:5000/control" {
		t.Errorf("panel %v", st["panel_url"])
	}
	if r := ta.api("POST", "start", M{}); r.status != 409 {
		t.Errorf("second start: %d", r.status)
	}
	// Settings while running belong to the running server (applied live), not setup.
	if r := ta.saveSetup(M{"role": "streaming", "name": "X", "pc_id": "1"}); r.status != 409 {
		t.Errorf("setup save while running: %d", r.status)
	}
	if r := ta.api("POST", "close", M{}); r.status != 409 {
		t.Errorf("close while running: %d", r.status)
	}
	if ta.closeRequested() {
		t.Error("the close button closed a running match day without asking")
	}

	// Stop: the launcher's report question comes up in the window.
	ta.api("POST", "stop", M{})
	st = ta.waitFor("the report question", func(st M) bool { return st["question"] != nil })
	q := st["question"].(M)
	if q["kind"] != "confirm" || !strings.Contains(q["text"].(string), "match report") || q["default"] != nil && q["default"] != false {
		t.Fatalf("question %v", q)
	}
	if r := ta.api("POST", "answer", M{"id": q["id"], "yes": true}); r.status != 200 {
		t.Fatalf("answer: %d %s", r.status, r.body)
	}
	if r := ta.api("POST", "answer", M{"id": q["id"], "yes": true}); r.status != 409 {
		t.Errorf("answered twice: %d", r.status)
	}
	st = ta.waitFor("match day over", phaseIs(phStopped))
	var lines []string
	for _, l := range st["log"].([]any) {
		lines = append(lines, l.(M)["t"].(string))
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"Club: Test Town CC", "Saved to: match_report_today.txt", "Match day is over"} {
		if !strings.Contains(all, want) {
			t.Errorf("log missing %q:\n%s", want, all)
		}
	}
	if !ta.closeRequested() {
		t.Error("couldn't close once match day was over")
	}
}

// Closed while still getting ready (the launcher isn't listening for a stop yet): the
// window closes at once rather than waiting on it.
func TestClosingWhileGettingReadyDoesntWait(t *testing.T) {
	ta := newTestApp(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	runLauncher = func(o launcher.Options) int { <-block; return 0 }
	ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"})
	ta.api("POST", "start", M{})
	ta.waitFor("getting ready", phaseIs(phStarting))
	ta.api("POST", "stop", M{})
	select {
	case <-ta.done:
	case <-time.After(5 * time.Second):
		t.Fatal("didn't close")
	}
	start := time.Now()
	ta.finish()
	if time.Since(start) > 3*time.Second {
		t.Errorf("finish waited %v on a launcher that was abandoned", time.Since(start))
	}
}

// A copy already running match day on this laptop: the window offers its control panel.
func TestSecondCopyOnALaptopAlreadyRunningMatchDay(t *testing.T) {
	ta := newTestApp(t)
	alreadyRunning = func() bool { return true }
	ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"})
	st := ta.status()
	if st["phase"] != phAlready || !strings.HasSuffix(st["panel_url"].(string), "/control") {
		t.Fatalf("%v %v", st["phase"], st["panel_url"])
	}
	if r := ta.api("POST", "start", M{}); r.status != 409 {
		t.Errorf("started a second match day: %d", r.status)
	}
	if !ta.closeRequested() {
		t.Error("couldn't close the second window")
	}
}

// ── Questions, the log, the window's own port ──

func TestQuestionsWaitForTheWindowAndTakeTheirDefaultsWhenItsGone(t *testing.T) {
	a := newAsker()
	shown := 0
	a.show = func() { shown++ }

	a.Problem("Port 5000 is in use", []string{"Another copy is running."})
	done := make(chan struct{})
	go func() { a.Pause(""); close(done) }()
	var q *question
	for q == nil {
		q = a.Current()
		time.Sleep(time.Millisecond)
	}
	if q.Kind != "pause" || q.Title != "Port 5000 is in use" || q.Button != "Close CricketStream" || len(q.Lines) != 1 {
		t.Errorf("fatal pause %+v", q)
	}
	if a.Answer(q.ID+1, answer{}) {
		t.Error("answered a question that isn't showing")
	}
	if !a.Answer(q.ID, answer{}) {
		t.Fatal("couldn't answer")
	}
	<-done
	if shown != 1 {
		t.Errorf("page brought up %d times", shown)
	}
	// The next pause has no problem attached.
	go a.Pause("Close OBS, then press Enter here (or just press Enter to carry on without it)...")
	for q = a.Current(); q == nil || q.Button == "Close CricketStream"; q = a.Current() {
		time.Sleep(time.Millisecond)
	}
	if q.Title != "" || q.Text != "Close OBS, then press Continue (or just press Continue to carry on without it)" {
		t.Errorf("pause %+v", q)
	}
	a.Answer(q.ID, answer{})

	// Input: the typed answer; Confirm: yes/no, or the default when dismissed.
	go func() {
		for a.Current() == nil || a.Current().Kind != "input" {
			time.Sleep(time.Millisecond)
		}
		a.Answer(a.Current().ID, answer{Text: `C:\NV`})
	}()
	if v, ok := a.Input("  Scoreboard output folder: "); v != `C:\NV` || !ok {
		t.Errorf("input %q %v", v, ok)
	}

	// The window closes: a question waiting gets its default, and so does every later one.
	res := make(chan [2]bool)
	go func() { y, ok := a.Confirm("Clear it?", true); res <- [2]bool{y, ok} }()
	for a.Current() == nil {
		time.Sleep(time.Millisecond)
	}
	a.close()
	if r := <-res; r != [2]bool{true, false} {
		t.Errorf("confirm after close: %v", r)
	}
	if y, ok := a.Confirm("Generate a report?", false); y || ok {
		t.Errorf("confirm with no window: %v %v", y, ok)
	}
}

func TestWindowWording(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "",
		"Press Enter to close this window...": "",
		"Close OBS, then press Enter here (or just press Enter to carry on without it)...": "Close OBS, then press Continue (or just press Continue to carry on without it)",
		"Plug the camera in.": "Plug the camera in.",
	} {
		if got := windowWording(in); got != want {
			t.Errorf("windowWording(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogSplitsLinesAsTheyArrive(t *testing.T) {
	var echo bytes.Buffer
	l := &Log{echo: &echo}
	l.Write([]byte("  ✓ one\r\n  two"))
	l.Write([]byte(" halves\nprogress 10%\rprogress 100%\n"))
	got, next := l.Since(0)
	want := []string{"  ✓ one", "  two halves", "progress 100%"}
	if len(got) != len(want) || next != 3 {
		t.Fatalf("lines %v next %d", got, next)
	}
	for i, w := range want {
		if got[i].Text != w || got[i].N != i+1 {
			t.Errorf("line %d: %+v", i, got[i])
		}
	}
	if later, _ := l.Since(2); len(later) != 1 || later[0].Text != "progress 100%" {
		t.Errorf("since 2: %v", later)
	}
	if !strings.Contains(echo.String(), "two halves") {
		t.Error("the terminal copy lost text")
	}
	// It keeps the last logKeep lines, numbered on.
	for i := 0; i < logKeep+10; i++ {
		fmt.Fprintf(l, "line %d\n", i)
	}
	all, next := l.Since(0)
	if len(all) != logKeep || all[len(all)-1].N != next || all[0].N != next-logKeep+1 {
		t.Errorf("kept %d, first %d, last %d, next %d", len(all), all[0].N, all[len(all)-1].N, next)
	}
}

// Everything printed while the app runs — this process and the processes it starts —
// reaches the window's log and today's log file beside the app.
func TestCaptureReachesTheLogFile(t *testing.T) {
	dir := t.TempDir()
	saved := assets.Dir()
	assets.SetDir(dir)
	t.Cleanup(func() { assets.SetDir(saved) })
	l := &Log{}
	undo, err := l.capture()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("  ✓ hello from the app")
	fmt.Fprintln(os.Stderr, "  ✗ and its errors")
	fmt.Print("no newline at the end")
	undo()
	lines, _ := l.Since(0)
	var texts []string
	for _, ln := range lines {
		texts = append(texts, ln.Text)
	}
	joined := strings.Join(texts, "|")
	for _, want := range []string{"hello from the app", "and its errors", "no newline at the end"} {
		if !strings.Contains(joined, want) {
			t.Errorf("window log missing %q: %s", want, joined)
		}
	}
	b, err := os.ReadFile(LogFile())
	if err != nil || !strings.Contains(string(b), "hello from the app") || !strings.Contains(string(b), "and its errors") {
		t.Errorf("log file: %v %q", err, b)
	}
	if !strings.HasPrefix(LogFile(), filepath.Join(dir, "logs")) {
		t.Errorf("log file outside the app folder: %s", LogFile())
	}
}

// The window logs the control panel in: on the panel's own origin only, never over a
// login the panel already has, and with a token that can't break out of the script.
func TestPanelLoginScript(t *testing.T) {
	s := panelLoginScript("http://127.0.0.1:5000/control", `a'b"</script>`)
	for _, want := range []string{`location.origin==="http://127.0.0.1:5000"`, `!sessionStorage.getItem('bbcc_token')`,
		`"a'b\"\u003c/script\u003e"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

// A second double-click finds the first window by its port and brings it forward; a port
// held by something else just means another port.
func TestSecondDoubleClickFindsTheFirstWindow(t *testing.T) {
	p := freePort(t)
	t.Setenv(AppPortEnv, fmt.Sprint(p))
	ln, err := listen()
	if err != nil {
		t.Fatal(err)
	}
	focused := make(chan struct{}, 1)
	a := &App{opts: Options{Headless: true}}
	a.base = "http://" + ln.Addr().String()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hello" && r.Method == "POST" {
			focused <- struct{}{}
		}
		a.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	if _, err := listen(); err != errAlreadyOpen {
		t.Fatalf("second listen: %v", err)
	}
	focusRunningCopy()
	select {
	case <-focused:
	case <-time.After(3 * time.Second):
		t.Error("the first window wasn't asked to come forward")
	}

	// Something else on the port: any free one instead.
	other, _ := net.Listen("tcp", "127.0.0.1:0")
	defer other.Close()
	go http.Serve(other, http.NotFoundHandler())
	t.Setenv(AppPortEnv, fmt.Sprint(other.Addr().(*net.TCPAddr).Port))
	ln2, err := listen()
	if err != nil {
		t.Fatalf("with the port taken by something else: %v", err)
	}
	ln2.Close()
}

var keyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestKeysAreRandom(t *testing.T) {
	a, b := randomKey(), randomKey()
	if !keyPattern.MatchString(a) || a == b {
		t.Errorf("%q %q", a, b)
	}
}

// A newer release shows in the window; the same or an older one, a development build or
// no answer shows nothing.
func TestUpdateNotice(t *testing.T) {
	tag := "v3.1"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag == "" {
			http.Error(w, "rate limited", 403)
			return
		}
		fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	}))
	defer gh.Close()
	saved := releasesAPI
	releasesAPI = gh.URL
	t.Cleanup(func() { releasesAPI = saved })

	for _, c := range []struct {
		tag, have string
		want      any
	}{
		{"v3.1", "3.0.2", "3.1"},
		{"v3.1", "v3.1", nil},
		{"v3.0", "3.1", nil},
		{"v3.1", "", nil}, // development build
		{"", "3.0", nil},  // no answer
	} {
		tag = c.tag
		var u updateInfo
		u.check(c.have)
		st := u.status()
		var got any
		if st != nil {
			got = st["latest"]
			if st["url"] != releasesPage {
				t.Errorf("url %v", st["url"])
			}
		}
		if got != c.want {
			t.Errorf("tag %q have %q: %v, want %v", c.tag, c.have, got, c.want)
		}
	}
}

func TestWindowTitleSaysWhatThisComputerIs(t *testing.T) {
	ta := newTestApp(t)
	if got := windowTitle(""); got != "CricketStream" {
		t.Error(got)
	}
	if got := windowTitle("scorer"); got != "CricketStream — Scorer's laptop" {
		t.Error(got)
	}
	ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"})
	if got := windowTitle("streaming"); got != "CricketStream — Test Town CC" {
		t.Error(got)
	}
}

// The match-day screen only offers phones and tablets with a club password.
func TestHomeOffersPhonesOnlyWithAPassword(t *testing.T) {
	ta := newTestApp(t)
	ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242", "allow_remote": true})
	if h := ta.status()["home"].(M); h["password"] != false || h["remote"] != false {
		t.Errorf("without a password: %v", h)
	}
	ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242", "allow_remote": true, "club_password": "pw"})
	if h := ta.status()["home"].(M); h["password"] != true || h["remote"] != true {
		t.Errorf("with a password: %v", h)
	}
}

func TestScorersLaptopRefusesTheAppsOwnFolder(t *testing.T) {
	ta := newTestApp(t)
	ta.saveSetup(M{"role": "scorer", "pcs_folder": ""})
	r := ta.api("POST", "agent/folder", M{"folder": ta.dir})
	if r.status != 400 || !strings.Contains(r.body, "CricketStream's own folder") {
		t.Errorf("%d %s", r.status, r.body)
	}
}
