package launcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/browser"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/proc"
	"cricketstream/internal/server"
)

const banner = `
╔══════════════════════════════════════════════════════╗
║         CricketStream Overlay — Quick Start          ║
╚══════════════════════════════════════════════════════╝
`

// ServerMaxRestarts caps the crash-restart loop: the server never exits on its own in
// normal operation, so an exit is a crash — but a broken config mustn't loop forever.
const ServerMaxRestarts = 3

func log(msg string, status ...string) {
	icon := "   "
	if len(status) > 0 {
		icon = map[string]string{"ok": "  ✓", "warn": "  ⚠", "err": "  ✗", "": "   "}[status[0]]
	}
	fmt.Printf("%s %s\n", icon, msg)
}

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func base() string { return fmt.Sprintf("http://127.0.0.1:%d", server.Port) }

// ask is one prompt with a default; no console means the default.
func ask(prompt, def string) string {
	v, ok := console.Input(prompt)
	if !ok {
		return def
	}
	if v = strings.TrimSpace(v); v == "" {
		return def
	}
	return v
}

// Options for Run.
type Options struct {
	// Remote / Manual pre-answer the two match-day questions (nil = ask).
	Remote, Manual *bool
	// OpenPanel shows the control panel once the server is up (the app's window passes
	// its own; nil = the default browser). token is the launcher's own login, "" with no
	// club password.
	OpenPanel func(url, token string)
	// Interrupt stops match day (the app's window sends on it); nil = Ctrl+C.
	Interrupt <-chan os.Signal
}

// Run is match day. It returns the process exit code.
func Run(o Options) int {
	fmt.Print(banner)
	cfgPath := assets.Join("config.ini")
	if _, err := os.Stat(cfgPath); err != nil {
		console.Die("No config.ini yet — setup hasn't been run", "",
			"Expected it at:  "+cfgPath, "",
			"Run CricketStream again with no arguments and the setup page opens,",
			"or run:  cricketstream setup")
	}

	// Double-clicked again while it's already running (the window got minimised, or it
	// was started earlier): a second copy can't have the port, and would only fail and
	// restart until it gave up. Bring up the control panel instead.
	if AlreadyRunning() {
		log("CricketStream is already running on this laptop — opening the control panel.", "ok")
		openPanel(o, base()+"/control", "")
		fmt.Println("  (Its own window is the one to keep open; this one can close.)")
		console.Pause("")
		return 0
	}

	checkForUpdates()
	fmt.Println()

	// ── Access mode ──
	remote := false
	if o.Remote != nil {
		remote = *o.Remote
	} else {
		fmt.Println("  How do you want to run today?")
		fmt.Println()
		fmt.Println("    [1] Local only    — control panel on this laptop only (recommended)")
		fmt.Println("    [2] Remote access — allow Tailscale / LAN (share with volunteer operators)")
		fmt.Println()
		remote = ask("  Enter 1 or 2 [1]: ", "1") == "2"
	}
	if remote && noClubPassword(cfgPath) {
		// The server would refuse anyway (no login for the panel = anyone on the wifi can
		// run the stream); say so here rather than claim remote access.
		log("Phones and tablets need a club password — running on this laptop only today. "+
			"Set one in Settings to allow them.", "warn")
		remote = false
	}
	if remote {
		log("Remote access mode — server will listen on all network interfaces", "ok")
	} else {
		log("Local mode — control panel accessible on this machine only", "ok")
	}
	fmt.Println()

	// ── Data source ──
	manual := false
	if o.Manual != nil {
		manual = *o.Manual
	} else {
		fmt.Println("  How is today's match being scored?")
		fmt.Println()
		fmt.Println("    [1] NV Play / PCS Pro — the scorer's laptop writes the live feed (recommended)")
		fmt.Println("    [2] Manual scoring — score ball-by-ball from a phone/tablet at /scoring")
		fmt.Println()
		manual = ask("  Enter 1 or 2 [1]: ", "1") == "2"
	}
	if manual {
		log("Manual scoring — set the match up at /scoring once the server starts", "ok")
		log("Tip: remote access mode + Tailscale lets a volunteer score from the boundary", "")
	} else {
		// Foot-gun guard: a saved manual-scoring session OUTRANKS the scorer's feed in
		// /live — a leftover test session would silently replace NV Play on the overlay.
		mf := assets.Join("manual_scoring.json")
		if _, err := os.Stat(mf); err == nil {
			log("A saved manual-scoring session exists — it would OVERRIDE the scorer's feed!", "warn")
			if yes, _ := console.Confirm("Clear it so NV Play drives the overlay today?", true); yes {
				if err := os.Remove(mf); err != nil {
					log(fmt.Sprintf("Could not remove it (%v) — delete manual_scoring.json by hand", err), "err")
				} else {
					log("Manual session cleared", "ok")
				}
			} else {
				log("Keeping it — the overlay will show the MANUAL session, not the scorer", "warn")
			}
		}
	}
	fmt.Println()

	// ── Get OBS ready (before config.ini is loaded: it can write the WebSocket password
	// into it). [OBS] manage_obs = no turns it off. ──
	func() {
		defer func() {
			if r := recover(); r != nil {
				log(fmt.Sprintf("Skipped getting OBS ready (%v) — open OBS yourself", r), "warn")
			}
		}()
		if obs.ManageOBSEnabled(cfgPath) {
			log("Getting OBS ready...")
			var wait func(string)
			if !console.NoPause() {
				wait = console.Pause
			}
			obs.Prepare(cfgPath, wait, func(s string) { fmt.Println(s) })
			fmt.Println()
		}
	}()

	// ── Load config ──
	log("Loading config.ini...")
	cfg, err := ini.Load(cfgPath)
	if err != nil {
		console.Die("config.ini couldn't be read", err.Error())
	}
	clubName := cfg.Get("Club", "name", "My Club CC")
	clubID := cfg.Get("Club", "playcricket_id", "")
	apiKey := cfg.Get("API", "playcricket_key", "")
	log("Club: "+clubName, "ok")

	// ── Today's match ──
	log("Fetching today's match from PlayCricket API...")
	match, why := FetchTodaysMatch(apiKey, clubID)
	if match != nil {
		log(fmt.Sprintf("Match found: %s vs %s", clubName, match.AwayTeam), "ok")
		log("Competition: "+match.Competition, "ok")
		if match.Umpire1 != "" {
			log(fmt.Sprintf("Umpires: %s / %s", match.Umpire1, match.Umpire2), "ok")
		}
		if match.Ground != "" {
			log("Ground: "+match.Ground, "ok")
		}
	} else {
		log("API: "+why, "warn")
		log("Continuing without match data — enter opposition manually in control panel", "warn")
	}

	// ── match_state.json ──
	exist := server.LoadExistingState()
	state := BuildState(cfg, match, exist)
	if err := server.SaveState(state); err != nil {
		console.Die("Couldn't write match_state.json", err.Error())
	}
	log("match_state.json written", "ok")

	// ── Configure OBS ──
	log("Configuring OBS...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				log(fmt.Sprintf("OBS setup skipped: %v", r), "warn")
			}
		}()
		ok, msgs := obs.SetupFromConfig(cfgPath, server.StateFile, false, true)
		if ok {
			log("OBS configured — scenes and sources ready", "ok")
			return
		}
		last := ""
		for i := len(msgs) - 1; i >= 0; i-- {
			if strings.Contains(msgs[i], "✗") {
				last = msgs[i]
				break
			}
		}
		if last == "" && len(msgs) > 0 {
			last = msgs[len(msgs)-1]
		}
		log(fmt.Sprintf("OBS: %s — configure manually if needed", strings.TrimSpace(last)), "warn")
	}()

	// ── The score source ──
	checkScoreSource(state, manual)

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────────")
	opp := "???"
	if match != nil {
		opp = match.AwayTeam
	}
	fmt.Printf("  Ready: %s vs %s\n", clubName, opp)
	if match != nil && match.Competition != "" {
		fmt.Printf("  %s\n", match.Competition)
	}
	fmt.Println()

	// ── The server, as its own process: a Ctrl+C here reaches only the launcher, so the
	// server stays up long enough to write the match report (its match log is in memory),
	// and a crash is restarted instead of ending match day. ──
	bind := "127.0.0.1"
	if remote {
		bind = "0.0.0.0"
	}
	launch := func() (Proc, error) { return startServer(bind) }
	p, err := launch()
	if err != nil {
		console.Die("The stream server wouldn't start", err.Error())
	}

	token := ""
	if WaitForServer(20 * time.Second) {
		token = SessionToken(cfg)
		PullSeasonStats(apiKey, token)
		SelfTest()
		openPanel(o, base()+"/control", token) // the match-day checklist is the next thing to look at
	} else {
		log("Server didn't respond in time — season stats will load on the first player card", "warn")
	}
	if tel := startTelemetry(); tel != nil {
		// Stopped with the launcher: it has no window, so a logger left behind would run
		// unseen until the laptop is switched off, and the next match day would start a
		// second one. It flushes every row, so stopping it loses nothing.
		defer tel.Kill()
	}

	fmt.Println()
	fmt.Printf("  Control panel: http://localhost:%d/control\n", server.Port)
	fmt.Printf("  Overlay:       http://localhost:%d/overlay\n", server.Port)
	if manual {
		fmt.Printf("  Manual scoring: http://localhost:%d/scoring  ← set the match up here\n", server.Port)
		log("(Any PCS feed warnings in the pre-flight check above are expected today)", "")
	}
	if remote {
		ts := tailscaleIP()
		host := ts
		if host == "" {
			host = "<tailscale-ip>"
		}
		suffix := ""
		if ts != "" {
			suffix = "  (Tailscale)"
		}
		fmt.Printf("  Remote panel:  http://%s:%d/control%s\n", host, server.Port, suffix)
		if manual {
			fmt.Printf("  Remote scoring: http://%s:%d/scoring%s\n", host, server.Port, suffix)
		}
	}
	fmt.Println()
	fmt.Println("  Keep this window open while streaming.")
	fmt.Println("  Press Ctrl+C to stop the server.")
	fmt.Println("  ─────────────────────────────────────────────")
	fmt.Println()

	interrupt := o.Interrupt
	if interrupt == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		defer signal.Stop(ch)
		interrupt = ch
	}
	return RunServerWithRestarts(p, launch, token, ServerMaxRestarts, 2*time.Second, time.Sleep, interrupt)
}

func openPanel(o Options, url, token string) {
	if o.OpenPanel != nil {
		o.OpenPanel(url, token)
		return
	}
	openBrowser(url)
}

func checkScoreSource(state M, manual bool) {
	switch {
	case manual:
		log("PCS folder checks skipped — scoring manually at /scoring today", "ok")
	case str(state, "pcs_source") == "agent":
		// Two-laptop mode: check we can see the scoring laptop before the toss rather
		// than at the start of play.
		host := str(state, "agent_host")
		if host != "" {
			if info, err := server.AgentPing(host, 3*time.Second); info != nil {
				log(fmt.Sprintf("Scorer laptop reachable: %s (%s)", orStr(str(info, "hostname"), "?"), host), "ok")
			} else {
				log(fmt.Sprintf("Scorer laptop not reachable at %s: %s", host, err), "warn")
			}
			return
		}
		if agents := server.DiscoverAgents(2 * time.Second); len(agents) > 0 {
			a := agents[0]
			log(fmt.Sprintf("Scorer laptop found: %s (%s)", orStr(str(a, "hostname"), "?"), str(a, "address")), "ok")
			if str(a, "file") == "" {
				log("Connected, but no scoreboard file yet — has the scorer started PCS Pro scoreboard output?", "warn")
			}
		} else {
			log("No scorer laptop found on the network — is CricketStream running in scorer mode "+
				"on the scoring laptop? See TWO_LAPTOP_SETUP.md", "warn")
		}
	default:
		folder := str(state, "pcs_output_folder")
		switch {
		case folder == "":
			log("No PCS folder set — enter it in the control panel", "warn")
		case isDir(folder):
			log("PCS folder found: "+folder, "ok")
		default:
			log("PCS folder not found: "+folder, "warn")
			log("Check pcs_output_folder in config.ini", "warn")
		}
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ── The server process ──

// Proc is the running server (a fake one in tests).
type Proc interface {
	Wait() error
	Terminate()
	Kill()
}

type osProc struct {
	cmd   *exec.Cmd
	done  chan error
	stdin io.Closer
}

func (p *osProc) Wait() error { return <-p.done }
func (p *osProc) Kill()       { p.cmd.Process.Kill() }

// Terminate asks the server to stop by closing its stdin: it then stops cleanly (a
// practice match puts the score source back) on every platform, where a Windows
// TerminateProcess can't be caught. The signal is the fallback.
func (p *osProc) Terminate() {
	if p.stdin != nil && p.stdin.Close() == nil {
		return
	}
	terminate(p.cmd.Process)
}

// StopOnEOFEnv tells `cricketstream server` its supervisor will close stdin to stop it.
const StopOnEOFEnv = "CRICKETSTREAM_STOP_ON_EOF"

// startServer runs this same program as `cricketstream server`, in its own process
// group so the operator's Ctrl+C doesn't reach it.
func startServer(bind string) (Proc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "server")
	// Its stdin is how it's told to stop; a fatal problem in it must exit (and be
	// restarted), not wait for an Enter nobody can type.
	cmd.Env = append(os.Environ(), "BBCC_BIND_HOST="+bind, "CRICKETSTREAM_HOME="+assets.Dir(), StopOnEOFEnv+"=1",
		"CRICKETSTREAM_NO_PAUSE=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &osProc{cmd: cmd, done: make(chan error, 1), stdin: stdin}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}

// RunServerWithRestarts waits on the server, relaunching it (capped) if it exits on its
// own, and handles Ctrl+C: the sponsor airtime and match report are offered while the
// server is STILL RUNNING, then it's stopped. Everything it touches is injected, so a test
// can drive it with fake processes and no real waiting.
func RunServerWithRestarts(p Proc, launch func() (Proc, error), token string, maxRestarts int,
	backoff time.Duration, sleep func(time.Duration), interrupt <-chan os.Signal) int {
	restarts := 0
	for {
		exited := make(chan struct{})
		go func(p Proc) { p.Wait(); close(exited) }(p)
		select {
		case <-interrupt:
			offerMatchReport(token)
			fmt.Println("\n  Shutting down server...")
			p.Terminate()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				p.Kill()
			}
			fmt.Println("  Server stopped. Bye!")
			resumeOBSUpdates()
			return 0
		case <-exited:
		}
		// The server exited on its own — a crash, not a shutdown.
		if restarts >= maxRestarts {
			dieFn(fmt.Sprintf("The server keeps crashing (restarted it %d times)", maxRestarts), "",
				"Whatever killed it is printed above this box — scroll up; the",
				"last few lines before each restart are the useful ones.", "",
				"Common causes:",
				fmt.Sprintf("  - Port %d already in use (another copy still running? on a Mac,", server.Port),
				"    AirPlay Receiver: System Settings → General → AirDrop & Handoff)",
				"  - A setting in config.ini the server can't read", "",
				"If it isn't obvious, please open an issue with those lines:",
				"  https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues")
			return 1
		}
		restarts++
		log(fmt.Sprintf("Server stopped unexpectedly — restarting (attempt %d/%d)", restarts, maxRestarts), "warn")
		sleep(backoff) // so a genuine crash loop doesn't hammer the machine
		np, err := launch()
		if err != nil {
			log("Couldn't restart the server: "+err.Error(), "err")
			continue
		}
		p = np
	}
}

// openBrowser opens a page in the default browser (swappable for tests).
var openBrowser = func(url string) { browser.Open(url) }

// AlreadyRunning reports whether a CricketStream server already answers on this
// laptop's port.
func AlreadyRunning() bool {
	resp, err := (&http.Client{Timeout: 1500 * time.Millisecond}).Get(base() + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var d M
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&d) != nil {
		return false
	}
	_, ok := d["pcs"]
	return ok
}

// Swappable for tests: the shutdown steps that talk to the real server and OBS.
var (
	dieFn            = console.Die
	offerMatchReport = OfferMatchReport
	resumeOBSUpdates = ResumeOBSUpdates
)

// ResumeOBSUpdates puts OBS's update check back after the match (OBS prep paused it).
// Usually the server's OBS guard already did, when OBS was closed; this catches OBS closed
// AFTER the server stopped watching. Still open: say how it'll come back.
func ResumeOBSUpdates() {
	defer func() { recover() }()
	dir := obs.ConfigDir()
	if _, err := os.Stat(filepath.Join(dir, obs.UpdatesMarker)); err != nil {
		return
	}
	if obs.Running() {
		fmt.Println("  (OBS's update check stays paused until OBS is closed while CricketStream")
		fmt.Println("   is running, or until the next match. Close OBS first next time to have")
		fmt.Println("   it switched straight back on.)")
	} else if obs.ResumeUpdateCheck(dir) {
		fmt.Println("  OBS's update check is back on.")
	}
}

// ── Talking to the running server ──

func getJSON(path, token string, timeout time.Duration) (M, error) {
	req, _ := http.NewRequest("GET", base()+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var d M
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return d, nil
}

// WaitForServer polls /health until the server answers (or gives up).
func WaitForServer(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(base() + "/health"); err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// SessionToken logs in with the club password from config.ini, so the launcher's own
// calls to token-gated endpoints don't 401 against its own server. "" without a password.
func SessionToken(cfg *ini.File) string {
	pw := strings.TrimSpace(cfg.Get("Auth", "club_password", ""))
	if pw == "" {
		return ""
	}
	b, _ := json.Marshal(M{"password": pw})
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(base()+"/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d M
	json.NewDecoder(resp.Body).Decode(&d)
	if d["ok"] != true {
		return ""
	}
	return str(d, "session_token")
}

// PullSeasonStats builds (or reuses today's cached) season stats, so they're ready before
// the first player card.
func PullSeasonStats(apiKey, token string) {
	if apiKey == "" || apiKey == "YOUR_KEY_HERE" {
		log("Season stats skipped — no PlayCricket API key in config.ini", "warn")
		return
	}
	log("Pulling season batting stats from PlayCricket (both teams, one-off, cached locally)...")
	d, err := getJSON("/player/stats/refresh", token, 180*time.Second)
	if err != nil {
		log(fmt.Sprintf("Season stats: couldn't pull (%v) — will load on first player card", err), "warn")
		return
	}
	if e := str(d, "error"); e != "" {
		log(fmt.Sprintf("Season stats: %s (loaded %s players)", e, numStr(d["players"])), "warn")
		return
	}
	src := numStr(d["api_calls"]) + " API calls"
	if d["from_cache"] == true {
		src = "from today's saved cache"
	}
	opp := " (home only — no opposition club ID)"
	if d["opposition"] == true {
		opp = " incl. opposition"
	}
	log(fmt.Sprintf("Season stats ready: %s players from %s matches (%s)%s", numStr(d["players"]),
		numStr(d["matches_used"]), src, opp), "ok")
}

func numStr(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	if v == nil {
		return "0"
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// SelfTest is the pre-flight checklist: /health as a go/no-go summary, run once the server
// is up so problems show in the warm-up, not at the first ball. Warnings don't block.
func SelfTest() {
	fmt.Println()
	fmt.Println("  ─── Pre-flight check ────────────────────────")
	h, err := getJSON("/health", "", 6*time.Second)
	if err != nil {
		log(fmt.Sprintf("Could not read /health (%v) — skipping pre-flight", err), "warn")
		return
	}
	issues := 0
	ok := func(m string) { fmt.Printf("   ✓  %s\n", m) }
	warn := func(m string) { issues++; fmt.Printf("   ⚠  %s\n", m) }
	sub := func(k string) M { m, _ := h[k].(M); return m }
	pcs := sub("pcs")
	if h["demo_mode"] == true {
		warn("Demo mode is ON — turn it off in the control panel before going live")
	}
	switch {
	case pcs["folder_set"] != true:
		warn("PCS output folder not set (control panel → PCS Pro section)")
	case pcs["file_found"] != true:
		warn("PCS scoreboard file not found — is PCS Pro running and outputting?")
	case pcs["fresh"] != true:
		warn(fmt.Sprintf("PCS file found but stale (%dm old) — scorer not scoring yet?", int(num(pcs["age_sec"]))/60))
	default:
		ok(fmt.Sprintf("Scorer feed live (%s, updated %ss ago)", str(pcs, "file"), numStr(pcs["age_sec"])))
	}
	st := sub("stats")
	switch {
	case st["built"] == true:
		ok(fmt.Sprintf("Season stats ready (%s players)", numStr(st["players"])))
	case st["building"] == true:
		ok("Season stats building in the background")
	default:
		warn("Season stats not built — player cards will show photos only")
	}
	as := sub("assets")
	if num(as["headshots"]) > 0 {
		ok(fmt.Sprintf("%s player photo(s) found", numStr(as["headshots"])))
	} else {
		warn("No player photos found (headshots folder)")
	}
	if sub("match")["badges"] == true {
		ok("Both club badges set")
	} else {
		warn("Club badge(s) missing — set them in the control panel")
	}
	if sub("keys")["anthropic"] == true {
		ok("Anthropic key set — AI features available")
	} else {
		warn("No Anthropic key — commentary/reports/social posts disabled")
	}
	fmt.Println("  ─────────────────────────────────────────────")
	if issues == 0 {
		fmt.Println("   ✓  ALL SYSTEMS GO")
	} else {
		fmt.Printf("   ⚠  %d warning(s) above — fixable in the control panel\n", issues)
	}
	fmt.Println()
}

// startTelemetry runs the match-day telemetry logger in the background. Started here
// rather than left to the operator, because it only has to be forgotten once to lose a
// whole match's data. Best-effort: never holds up match day. Returns the process (nil
// if it didn't start) so the launcher can stop it on the way out.
func startTelemetry() *os.Process {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	cmd := exec.Command(exe, "telemetry")
	cmd.Env = append(os.Environ(), "CRICKETSTREAM_HOME="+assets.Dir(), "CRICKETSTREAM_NO_PAUSE=1")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	detachQuiet(cmd)
	if err := cmd.Start(); err != nil {
		log(fmt.Sprintf("Telemetry didn't start (%v) — not fatal, run `cricketstream telemetry` by hand", err), "warn")
		return nil
	}
	go cmd.Wait()
	log("Telemetry logging to diagnostics/ (bandwidth, drops, match context)", "ok")
	return cmd.Process
}

func tailscaleIP() string {
	r, err := proc.Run("tailscale", []string{"ip", "-4"}, proc.Options{Timeout: 2 * time.Second})
	if err != nil || r.Code != 0 {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(r.Stdout), "\n", 2)[0])
}

// ── After the match ──

// ReportSponsorAirtime prints today's sponsor airtime and saves it to a file the club
// can forward. Returns the text ("" if none). Needs no API key.
func ReportSponsorAirtime() string {
	d, err := getJSON("/sponsor/airtime", "", 5*time.Second)
	if err != nil {
		return ""
	}
	var lines []string
	sponsors, _ := d["sponsors"].([]any)
	for _, s := range sponsors {
		if m, ok := s.(M); ok && num(m["on_air_shows"]) > 0 {
			lines = append(lines, str(m, "line"))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	text := strings.Join(lines, "\n")
	fmt.Println()
	fmt.Println("  ─── SPONSOR AIRTIME ──────────────")
	for _, l := range lines {
		fmt.Println("  " + l)
	}
	fn := assets.Join(fmt.Sprintf("sponsor_airtime_%s.txt", orStr(str(d, "date"), "today")))
	if os.WriteFile(fn, []byte(text+"\n"), 0o644) == nil {
		fmt.Println("  Saved to: " + fn)
	}
	return text
}

// OfferMatchReport offers an AI match report from the still-running server.
func OfferMatchReport(token string) {
	fmt.Println()
	airtime := ReportSponsorAirtime()
	yes, ok := console.Confirm("Generate an AI match report for this game?", false)
	if !ok {
		return
	}
	if !yes {
		fmt.Println("  No report generated.")
		return
	}
	fmt.Println("  Generating report (needs your Anthropic API key)...")
	d, err := getJSON("/report/generate", token, 60*time.Second)
	if err != nil {
		fmt.Printf("  Report generation failed: %v\n", err)
		fmt.Println("  Tip: you can still generate it from the control panel (After the match tab) while the server is running.")
		return
	}
	if d["ok"] != true {
		fmt.Printf("  Could not generate report: %s\n", orStr(str(d, "error"), "unknown"))
		return
	}
	text := str(d, "text")
	fmt.Println()
	fmt.Println("  ─── MATCH REPORT ─────────────────")
	for _, l := range strings.Split(text, "\n") {
		fmt.Println("  " + l)
	}
	fmt.Println("  ────────────────────────────────")
	// Below the report, not in it: the report is written for the club's website, and an
	// airtime figure in the middle would read like an advert.
	if airtime != "" {
		text += "\n\n---\nSponsor airtime\n" + airtime + "\n"
	}
	fn := assets.Join(fmt.Sprintf("match_report_%s.txt", time.Now().Format("2006-01-02")))
	if os.WriteFile(fn, []byte(text), 0o644) == nil {
		fmt.Printf("\n  Saved to: %s\n", fn)
	}
}

// ── Update check ──

var releasesURL = "https://api.github.com/repos/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest"

// checkForUpdates compares this build with the newest release. Purely informational:
// any failure (no internet, rate limit, a development build) is silent.
func checkForUpdates() {
	have := strings.TrimPrefix(server.Version, "v")
	if have == "" {
		return
	}
	req, _ := http.NewRequest("GET", releasesURL, nil)
	req.Header.Set("User-Agent", "CricketStreamOverlay-quickstart")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var d M
	if json.NewDecoder(resp.Body).Decode(&d) != nil {
		return
	}
	latest := strings.TrimPrefix(str(d, "tag_name"), "v")
	if latest == "" {
		return
	}
	if server.VersionNewer(latest, have) {
		log(fmt.Sprintf("Update available: v%s (you're on v%s)", latest, have), "warn")
		log("Download it from the GitHub Releases page", "warn")
	} else {
		log(fmt.Sprintf("Up to date (v%s)", have), "ok")
	}
}

// noClubPassword: config.ini has no [Auth] club_password (or can't be read).
func noClubPassword(cfgPath string) bool {
	f, err := ini.Load(cfgPath)
	return err != nil || strings.TrimSpace(f.Get("Auth", "club_password", "")) == ""
}
