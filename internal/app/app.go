// Package app is CricketStream's own window: what a double-click opens. It asks which
// computer this is on first run, runs the setup page, and then does this computer's job
// in the same window — match day on the streaming laptop (the control panel opens in
// the window, already logged in), or the scorer's agent or bridge with their status —
// with the prompts the console used to ask shown as questions in the window, and what it
// used to print in an activity log (also written to logs/ beside the app).
//
// The pages are HTML served from this process on a loopback port, shown by the system's
// web view (WebView2 on Windows): no browser, no address bar. Without one — a Mac or
// Linux build, or a Windows machine missing WebView2 — the same pages open in the
// default browser instead. The control panel stays a web page either way, because phones
// and tablets use it too.
package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/agent"
	"cricketstream/internal/assets"
	"cricketstream/internal/bridge"
	"cricketstream/internal/browser"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
	"cricketstream/internal/launcher"
	"cricketstream/internal/port"
	"cricketstream/internal/proc"
	"cricketstream/internal/scorerfile"
	"cricketstream/internal/setup"
)

// The screens. The page shows the one for the current phase.
const (
	phWelcome  = "welcome"  // no config.ini yet: which computer is this?
	phHome     = "home"     // the streaming laptop, before match day starts
	phStarting = "starting" // the launcher getting OBS, the match and the server ready
	phRunning  = "running"  // the server is up; the window shows the control panel
	phStopped  = "stopped"  // match day is over
	phAlready  = "already"  // another copy is already running match day on this laptop
	phAgent    = "agent"    // the scorer's laptop: sharing NV Play's file on the wifi
	phBridge   = "bridge"   // the scorer's machine elsewhere: the bridge
)

// AppPortEnv picks the window's own port (default DefaultAppPort; 0 = any free one, for
// tests). Holding it is also how a second copy finds the first.
const AppPortEnv = "CRICKETSTREAM_APP_PORT"

// DefaultAppPort is the window's port: the server's 5000 is the control panel's.
const DefaultAppPort = 5098

// Options for Run.
type Options struct {
	Version string
	// Headless serves the pages and opens nothing (CRICKETSTREAM_NO_WINDOW=1): automation
	// and the end-to-end tests drive them over HTTP, as the window would.
	Headless bool
}

// App is the running window and whatever this computer is doing.
type App struct {
	opts Options
	key  string // every /api call carries it: only the app's own page can drive it
	base string
	log  *Log
	ask  *asker
	win  window
	upd  updateInfo

	mu          sync.Mutex
	phase       string
	panelURL    string
	panelToken  string
	interrupt   chan os.Signal
	launcherOn  bool
	launcherEnd chan struct{}
	startLine   int  // the log line match day started after: the page lists what it did
	abandoned   bool // closed while still getting ready: nothing to wait for
	exitCode    int
	lastPoll    time.Time

	agentRun    *agent.Running
	agentFolder string
	agentHow    string
	agentErr    string

	bridgeRun *bridge.Running
	bridgeErr string

	done     chan struct{}
	doneOnce sync.Once
}

// Run is the double-click. It returns the exit code.
func Run(o Options) int {
	a := &App{opts: o, key: randomKey(), log: &Log{}, ask: newAsker(), done: make(chan struct{})}
	if o.Headless {
		// Nobody to answer: the console's NO_PAUSE, for the window.
		os.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
	}

	ln, err := listen()
	if err != nil {
		if errors.Is(err, errAlreadyOpen) {
			// The window is already open (minimised, behind something): bring it up.
			focusRunningCopy()
			fmt.Println("  CricketStream is already open — brought its window to the front.")
			return 0
		}
		console.Die("CricketStream couldn't open its window", err.Error())
	}
	a.base = "http://" + ln.Addr().String()
	undo, _ := a.log.capture()
	defer undo()
	console.SetUI(a.ask)
	defer console.SetUI(nil)
	a.ask.show = a.showOwnPage

	srv := &http.Server{Handler: a, ReadHeaderTimeout: 20 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()
	fmt.Printf("  CricketStream %s — app page %s/\n", versionText(o.Version), a.base)

	a.enterRole(setup.Role())
	go a.upd.check(o.Version)

	// Ctrl+C at a terminal (a Mac's, where the pages are in the browser): the same clean
	// stop as the window's Stop — the launcher's own shutdown, then close. A second Ctrl+C
	// doesn't wait.
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		<-sig
		go func() { <-sig; os.Exit(1) }()
		a.stop()
		a.mu.Lock()
		end := a.launcherEnd
		a.mu.Unlock()
		if end != nil {
			<-end
		}
		a.quit()
	}()

	switch {
	case o.Headless:
		<-a.done
	default:
		w, err := window(nil), errBrowserChosen
		if os.Getenv(NoWebViewEnv) != "1" {
			w, err = openWindow(a, a.base+"/")
		}
		if err == nil {
			a.mu.Lock()
			a.win = w
			a.mu.Unlock()
			a.updateTitle(setup.Role())
			console.HideOwnConsole() // a development build's console window: the window replaces it
			w.Run()                  // until the window closes
		} else {
			// No web view here: the same pages in the default browser, and the page's
			// own Close button ends the app.
			fmt.Printf("  (%v — opening it in the browser instead)\n", err)
			browser.Open(a.base + "/")
			<-a.done
		}
	}
	a.finish()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitCode
}

func versionText(v string) string {
	if v == "" {
		return "(development build)"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

func randomKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var errAlreadyOpen = errors.New("already open")

// NoWebViewEnv=1 uses the browser instead of a window of its own: the fallback for a PC
// without WebView2, by choice (and how that fallback is tested).
const NoWebViewEnv = "CRICKETSTREAM_NO_WEBVIEW"

var errBrowserChosen = errors.New(NoWebViewEnv + " is set")

func appPort() int {
	if v := os.Getenv(AppPortEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return DefaultAppPort
}

// listen takes the window's port. Taken by another CricketStream window: errAlreadyOpen.
// Taken by something else: any free port (the window doesn't need a known address).
func listen() (net.Listener, error) {
	p := appPort()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
	if err == nil || p == 0 {
		return ln, err
	}
	if pingRunningCopy(p) {
		return nil, errAlreadyOpen
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func pingRunningCopy(p int) bool {
	resp, err := (&http.Client{Timeout: 1500 * time.Millisecond}).Get(fmt.Sprintf("http://127.0.0.1:%d/hello", p))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return strings.Contains(string(b), "cricketstream-app")
}

func focusRunningCopy() {
	allowForeground()
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/hello", appPort()), nil)
	if resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req); err == nil {
		resp.Body.Close()
	}
}

// Test seams: match day itself, the "already running" check, and the agent's ports.
var (
	runLauncher    = launcher.Run
	alreadyRunning = launcher.AlreadyRunning
	agentOptions   = agent.Options{}
)

// ── What this computer does ──

// enterRole starts this computer's job (after setup, or at start).
func (a *App) enterRole(role string) {
	defer a.updateTitle(role)
	a.stopAgent()
	a.stopBridge()
	switch role {
	case "":
		a.setPhase(phWelcome)
	case setup.RoleScorer:
		a.setPhase(phAgent)
		folder, how := agent.FindFolder()
		if folder == "" {
			if f, err := ini.Load(setup.ConfigFile()); err == nil {
				if p := strings.TrimSpace(f.Get("Scoring", "pcs_output_folder", "")); p != "" && isDir(p) {
					folder, how = p, "setup"
				}
			}
		}
		if folder != "" {
			a.startAgent(folder, how)
		} else {
			fmt.Println("  NV Play's scoreboard folder wasn't found on this laptop — choose it in the window.")
		}
	case setup.RoleBridge:
		a.setPhase(phBridge)
		a.startBridge()
	default:
		if alreadyRunning() {
			a.mu.Lock()
			a.phase, a.panelURL, a.panelToken = phAlready, fmt.Sprintf("http://127.0.0.1:%d/control", port.Server()), ""
			a.mu.Unlock()
			return
		}
		a.setPhase(phHome)
	}
}

// updateTitle names the window after what it's doing — on the taskbar, two CricketStream
// windows (a test on the scorer's laptop, say) are told apart at a glance.
func (a *App) updateTitle(role string) {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.SetTitle(windowTitle(role))
	}
}

func windowTitle(role string) string {
	switch role {
	case setup.RoleScorer:
		return "CricketStream — Scorer's laptop"
	case setup.RoleBridge:
		return "CricketStream — NV Play bridge"
	case "":
		return "CricketStream"
	}
	if club := homeInfo()["club"].(string); club != "" {
		return "CricketStream — " + club
	}
	return "CricketStream"
}

func (a *App) setPhase(p string) {
	a.mu.Lock()
	a.phase = p
	a.mu.Unlock()
}

func (a *App) currentPhase() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (a *App) startAgent(folder, how string) {
	r, err := agent.Start(folder, agentOptions)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.agentFolder, a.agentHow = folder, how
	if err != nil {
		a.agentErr = fmt.Sprintf("Couldn't share the score: port %d is in use — almost always another copy of "+
			"CricketStream already sharing it on this laptop. Close that one first.", agentPort())
		fmt.Println("  ✗  " + err.Error())
		return
	}
	a.agentRun, a.agentErr = r, ""
	fmt.Printf("  ✓  Sharing NV Play's scoreboard from %s on %s:%d\n", folder, agent.LocalIP(), agentPort())
	if r.Template == "installed" || r.Template == "updated" {
		fmt.Printf("  ✓  Put the scoreboard template in NV Play's Templates folder (%s)\n", r.TemplatesDir)
	}
}

func agentPort() int {
	if agentOptions.Port != 0 {
		return agentOptions.Port
	}
	return agent.DefaultHTTPPort
}

func (a *App) stopAgent() {
	a.mu.Lock()
	r := a.agentRun
	a.agentRun = nil
	a.mu.Unlock()
	if r != nil {
		r.Stop()
		fmt.Println("  Stopped sharing the scoreboard.")
	}
}

func (a *App) startBridge() {
	c, ok := bridge.Load()
	if !ok {
		a.mu.Lock()
		a.bridgeErr = "No bridge settings yet — press Change setup and save."
		a.mu.Unlock()
		return
	}
	r, err := bridge.Start(c)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.bridgeErr = err.Error()
		fmt.Println("  ✗  " + err.Error())
		return
	}
	a.bridgeRun, a.bridgeErr = r, ""
	fmt.Printf("  ✓  NV Play bridge serving %s on port %d\n", c.Folder, c.Port)
}

func (a *App) stopBridge() {
	a.mu.Lock()
	r := a.bridgeRun
	a.bridgeRun = nil
	a.mu.Unlock()
	if r != nil {
		r.Stop()
		fmt.Println("  Stopped the bridge.")
	}
}

// startMatchDay runs the launcher with the window's answers to its two questions.
func (a *App) startMatchDay(remote, manual bool) error {
	a.mu.Lock()
	if a.launcherOn {
		a.mu.Unlock()
		return errors.New("Match day has already started.")
	}
	if a.phase != phHome && a.phase != phStopped {
		a.mu.Unlock()
		return errors.New("Not now — this computer isn't the streaming laptop, or it's busy.")
	}
	a.launcherOn, a.phase = true, phStarting
	_, a.startLine = a.log.Since(1 << 30)
	a.interrupt = make(chan os.Signal, 1)
	a.launcherEnd = make(chan struct{})
	interrupt, end := a.interrupt, a.launcherEnd
	a.mu.Unlock()
	go func() {
		defer close(end)
		defer crashed()
		code := runLauncher(launcher.Options{Remote: &remote, Manual: &manual, Interrupt: interrupt,
			OpenPanel: a.panelReady})
		fmt.Println()
		fmt.Println("  Match day is over.")
		a.mu.Lock()
		a.launcherOn, a.exitCode = false, code
		a.phase, a.panelURL = phStopped, ""
		a.mu.Unlock()
		a.showOwnPage()
	}()
	return nil
}

// crashed turns a panic in match day into the window's explanation, as main's guard does
// for its own goroutine (a goroutine's panic would otherwise end the app unexplained).
func crashed() {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n%s", r, debug.Stack())
		console.Problem("CricketStream hit an unexpected error", fmt.Sprint(r), "",
			"The details are in the activity log and today's log file. Please report them:",
			"  https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues")
		console.Pause("")
		os.Exit(1)
	}
}

// panelReady is the launcher's OpenPanel: the server is up.
func (a *App) panelReady(url, token string) {
	a.mu.Lock()
	a.phase, a.panelURL, a.panelToken = phRunning, url, token
	a.mu.Unlock()
	a.showPanel()
}

// stop ends whatever this computer is doing: match day (the launcher's own shutdown —
// the airtime and report questions come up in the window) or the sharing.
func (a *App) stop() {
	a.mu.Lock()
	interrupt, on := a.interrupt, a.launcherOn
	if on && a.phase == phStarting {
		// Still getting ready: the launcher only listens once the server is up, so there's
		// nothing to stop cleanly yet. Closing ends the process; a server it had already
		// started sees its stdin close and stops itself.
		a.abandoned = true
		a.mu.Unlock()
		a.quit()
		return
	}
	a.mu.Unlock()
	if on && interrupt != nil {
		select {
		case interrupt <- os.Interrupt:
		default:
		}
	}
	a.stopAgent()
	a.stopBridge()
}

// quit closes the window (and with it the app).
func (a *App) quit() {
	a.doneOnce.Do(func() { close(a.done) })
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// finish runs once the window has gone: nothing may be left running unseen.
func (a *App) finish() {
	a.ask.close()
	a.stop()
	a.mu.Lock()
	end := a.launcherEnd
	if a.abandoned {
		end = nil
	}
	a.mu.Unlock()
	if end != nil {
		select {
		case <-end:
		case <-time.After(20 * time.Second):
		}
	}
}

// closeRequested is the window's close button. True closes it now; false means the page
// is asking first (something is running that closing would stop).
func (a *App) closeRequested() bool {
	switch a.currentPhase() {
	case phWelcome, phHome, phStopped, phAlready:
		if a.ask.Current() == nil {
			a.doneOnce.Do(func() { close(a.done) })
			return true
		}
	}
	a.navigate(a.base + "/#close")
	return false
}

// ── The window ──

// window is the native window (window_windows.go) — or nothing, in a browser.
type window interface {
	Navigate(url string) // from any goroutine
	AddScript(js string) // run at the start of every page loaded after this
	SetTitle(title string)
	Focus()
	Close()
	Run() // on the main goroutine, until the window closes
}

func (a *App) navigate(url string) {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Navigate(url)
	}
}

// showOwnPage brings the app's page back (from the control panel) when it has something
// to show — a question, the end of match day.
func (a *App) showOwnPage() {
	a.mu.Lock()
	recent := time.Since(a.lastPoll) < 2*time.Second
	a.mu.Unlock()
	if !recent {
		a.navigate(a.base + "/")
	}
}

// showPanel shows the control panel, logged in as the launcher is. In a browser, it opens
// a tab (and the panel asks for the password there, as before).
func (a *App) showPanel() {
	a.mu.Lock()
	url, token, w := a.panelURL, a.panelToken, a.win
	a.mu.Unlock()
	if url == "" {
		return
	}
	if w == nil {
		if !a.opts.Headless {
			browser.Open(url)
		}
		return
	}
	w.AddScript(panelLoginScript(url, token))
	w.Navigate(url)
}

// panelLoginScript logs the window's control panel in: the panel keeps its login in
// sessionStorage, so it's set before the panel's own script reads it — only on the
// panel's own origin, and only if it isn't logged in already.
func panelLoginScript(panelURL, token string) string {
	origin := panelURL
	if i := strings.Index(origin[len("http://"):], "/"); i >= 0 {
		origin = origin[:len("http://")+i]
	}
	o, _ := json.Marshal(origin)
	t, _ := json.Marshal(token)
	return `(function(){try{if(location.origin===` + string(o) + `&&!sessionStorage.getItem('bbcc_token'))` +
		`sessionStorage.setItem('bbcc_token',` + string(t) + `);}catch(e){}})();`
}

// ── Status for the page ──

func (a *App) status(since int) map[string]any {
	lines, next := a.log.Since(since)
	a.mu.Lock()
	a.lastPoll = time.Now()
	out := map[string]any{"phase": a.phase, "role": setup.Role(), "version": a.opts.Version,
		"os": goos(), "window": a.win != nil, "log": lines, "next": next, "panel_url": a.panelURL,
		"log_file": LogFile(), "app_folder": assets.Dir(),
		"start_line": a.startLine, "update": a.upd.status()}
	phase := a.phase
	agentRun, folder, how, agentErr := a.agentRun, a.agentFolder, a.agentHow, a.agentErr
	bridgeRun, bridgeErr := a.bridgeRun, a.bridgeErr
	a.mu.Unlock()
	if q := a.ask.Current(); q != nil {
		out["question"] = q
	} else {
		out["question"] = nil
	}
	switch phase {
	case phHome, phStarting, phStopped, phRunning, phAlready, phWelcome:
		out["home"] = homeInfo()
	case phAgent:
		out["agent"] = agentInfo(agentRun, folder, how, agentErr)
	case phBridge:
		out["bridge"] = bridgeInfo(bridgeRun, bridgeErr)
	}
	return out
}

func homeInfo() map[string]any {
	out := map[string]any{"club": "", "remote": false}
	if f, err := ini.Load(setup.ConfigFile()); err == nil {
		out["club"] = f.Get("Club", "name", "")
		out["remote"] = f.Get("Network", "bind_host", "127.0.0.1") == "0.0.0.0"
	}
	_, err := os.Stat(assets.Join("manual_scoring.json"))
	out["manual_session"] = err == nil
	return out
}

// feedHealth is how fresh NV Play's file is, in the words the agent's console used.
func feedHealth(folder string) map[string]any {
	out := map[string]any{"file": "", "age": nil, "health": "waiting"}
	p := scorerfile.Find(folder, 10*time.Minute)
	if p == "" {
		return out
	}
	age := float64(time.Now().UnixNano())/1e9 - scorerfile.MTime(p)
	out["file"], out["age"] = baseName(p), int(age)
	switch {
	case age < 30:
		out["health"] = "live"
	case age < 300:
		out["health"] = "recent"
	default:
		out["health"] = "stale"
	}
	return out
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func agentInfo(r *agent.Running, folder, how, errText string) map[string]any {
	out := map[string]any{"folder": folder, "how": how, "error": errText, "running": r != nil,
		"ip": agent.LocalIP(), "port": agentPort(), "need_folder": folder == ""}
	if folder != "" {
		for k, v := range feedHealth(folder) {
			out[k] = v
		}
	}
	if r != nil {
		n, last, client := r.State.Collected()
		out["collected"] = n
		out["client"] = client
		if n > 0 {
			out["collected_ago"] = int(time.Since(last).Seconds())
		}
		out["template"], out["templates_dir"] = r.Template, r.TemplatesDir
		if r.DiscoveryErr != nil {
			out["discovery_error"] = r.DiscoveryErr.Error()
		}
	}
	return out
}

func bridgeInfo(r *bridge.Running, errText string) map[string]any {
	out := map[string]any{"error": errText, "running": r != nil}
	c, ok := bridge.Load()
	if r != nil {
		c, ok = r.Config, true
	}
	if !ok {
		return out
	}
	host := tailscaleIP()
	out["tailscale"] = host != ""
	if host == "" {
		host = agent.LocalIP()
	}
	out["folder"], out["folder_ok"], out["port"], out["token"] = c.Folder, isDir(c.Folder), c.Port, c.Token
	out["url"] = fmt.Sprintf("http://%s:%d", host, c.Port)
	if c.Folder != "" {
		for k, v := range feedHealth(c.Folder) {
			out[k] = v
		}
	}
	if r != nil {
		n, last, from := r.Fetches()
		out["collected"], out["client"] = n, from
		if n > 0 {
			out["collected_ago"] = int(time.Since(last).Seconds())
		}
	}
	return out
}

var tailscaleCache struct {
	sync.Mutex
	ip string
	at time.Time
}

// tailscaleIP is this machine's Tailscale address ("" without Tailscale), asked at most
// once a minute: the page polls often.
func tailscaleIP() string {
	tailscaleCache.Lock()
	defer tailscaleCache.Unlock()
	if time.Since(tailscaleCache.at) < time.Minute {
		return tailscaleCache.ip
	}
	tailscaleCache.ip, tailscaleCache.at = "", time.Now()
	if r, err := proc.Run("tailscale", []string{"ip", "-4"}, proc.Options{Timeout: 2 * time.Second}); err == nil && r.Code == 0 {
		tailscaleCache.ip = strings.TrimSpace(strings.SplitN(strings.TrimSpace(r.Stdout), "\n", 2)[0])
	}
	return tailscaleCache.ip
}
