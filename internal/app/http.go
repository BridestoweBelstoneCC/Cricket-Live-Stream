package app

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"cricketstream/internal/agent"
	"cricketstream/internal/assets"
	"cricketstream/internal/browser"
	"cricketstream/internal/clubpack"
	"cricketstream/internal/setup"
)

//go:embed app.html
var pageHTML string

func goos() string { return runtime.GOOS }

func writeJSON(w http.ResponseWriter, v any, status int) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(b)
}

// ownHost: the request is addressed to this window's own address. Anything else is a web
// page elsewhere trying its luck (DNS rebinding), never the app's page.
func (a *App) ownHost(r *http.Request) bool {
	u, _ := url.Parse(a.base)
	if r.Host == u.Host {
		return true
	}
	_, port, _ := strings.Cut(u.Host, ":")
	return r.Host == "localhost:"+port
}

// sameOrigin: a POST from the app's own page (the same check as the panel's).
func sameOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			// "null" (a sandboxed iframe, a file:// page) names no host: refused too.
			if err != nil || u.Host != r.Host {
				return false
			}
		}
	}
	return true
}

func readBody(r *http.Request, v any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return false
	}
	if len(b) == 0 {
		return true
	}
	return json.Unmarshal(b, v) == nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.ownHost(r) {
		http.Error(w, "Not this address", http.StatusMisdirectedRequest)
		return
	}
	p := r.URL.Path
	// A second double-click asks the first copy to come to the front; nothing else is
	// reachable without the page's key.
	if p == "/hello" {
		if r.Method == "POST" {
			a.focus()
		}
		writeJSON(w, map[string]any{"service": "cricketstream-app"}, 200)
		return
	}
	switch {
	case r.Method == "GET" && p == "/":
		a.servePage(w)
		return
	case strings.HasPrefix(p, "/api/"):
		if r.Header.Get("X-App-Key") != a.key {
			writeJSON(w, map[string]any{"ok": false, "error": "Not the app's page"}, 403)
			return
		}
		if r.Method == "POST" && !sameOrigin(r) {
			writeJSON(w, map[string]any{"ok": false, "error": "Cross-origin request rejected"}, 403)
			return
		}
		a.api(w, r)
		return
	case r.Method == "POST" && p == "/setup/save":
		a.saveSetup(w, r)
		return
	case r.Method == "POST" && (p == "/setup/export" || p == "/setup/import"):
		a.clubFile(w, r)
		return
	case r.Method == "GET" && strings.HasPrefix(p, "/brand/") && strings.HasSuffix(p, ".png") && !strings.Contains(p, ".."):
		if b, err := assets.Read(strings.TrimPrefix(p, "/")); err == nil {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "max-age=3600")
			w.Write(b)
			return
		}
	}
	// The setup page — its checks, the stream preview — is the setup package's own.
	setupHandler.ServeHTTP(w, r)
}

var setupHandler, _ = setup.Handler()

// The page's Content-Security-Policy: its own script only, nothing external.
const pageCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'"

func (a *App) servePage(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", pageCSP)
	page := pageHTML
	if b, err := os.ReadFile(assets.Join("app.html")); err == nil {
		page = string(b) // edited beside the app: shown without a rebuild, like the other pages
	}
	w.Write([]byte(strings.Replace(page, "{{APP_KEY}}", html.EscapeString(a.key), 1)))
}

func (a *App) api(w http.ResponseWriter, r *http.Request) {
	ok := func(extra map[string]any) {
		out := map[string]any{"ok": true}
		for k, v := range extra {
			out[k] = v
		}
		writeJSON(w, out, 200)
	}
	fail := func(status int, msg string) { writeJSON(w, map[string]any{"ok": false, "error": msg}, status) }
	switch r.Method + " " + r.URL.Path {
	case "GET /api/status":
		since, _ := strconv.Atoi(r.URL.Query().Get("since"))
		writeJSON(w, a.status(since), 200)
	case "POST /api/start":
		var body struct{ Remote, Manual bool }
		if !readBody(r, &body) {
			fail(400, "Bad request")
			return
		}
		if err := a.startMatchDay(body.Remote, body.Manual); err != nil {
			fail(409, err.Error())
			return
		}
		ok(nil)
	case "POST /api/answer":
		var body struct {
			ID        int    `json:"id"`
			Text      string `json:"text"`
			Yes       bool   `json:"yes"`
			Dismissed bool   `json:"dismissed"`
		}
		if !readBody(r, &body) {
			fail(400, "Bad request")
			return
		}
		if !a.ask.Answer(body.ID, answer{Text: body.Text, Yes: body.Yes, Dismissed: body.Dismissed}) {
			fail(409, "That question has already been answered.")
			return
		}
		ok(nil)
	case "POST /api/update":
		if err := a.startUpdate(); err != nil {
			fail(409, err.Error())
			return
		}
		ok(nil)
	case "POST /api/install_obs":
		if err := a.installOBS(); err != nil {
			fail(409, err.Error())
			return
		}
		ok(nil)
	case "POST /api/precheck":
		if err := a.startPrecheck(); err != nil {
			fail(409, err.Error())
			return
		}
		ok(nil)
	case "POST /api/clubhouse":
		// The match-day screen's Clubhouse card: on/off, switching off allowed, a new
		// pairing key, open at sign-in. Only before or after match day, like Settings.
		switch a.currentPhase() {
		case phHome, phStopped:
		default:
			fail(409, "Change the clubhouse screen before or after match day.")
			return
		}
		var body struct {
			Enabled       *bool `json:"enabled"`
			AllowPowerOff *bool `json:"allow_power_off"`
			NewKey        *bool `json:"new_key"`
			StartAtLogin  *bool `json:"start_at_login"`
		}
		if !readBody(r, &body) {
			fail(400, "Bad request")
			return
		}
		if err := a.setClubhouse(body.Enabled, body.AllowPowerOff, body.NewKey, body.StartAtLogin); err != nil {
			fail(500, err.Error())
			return
		}
		ok(map[string]any{"clubhouse": a.clubhouseInfo()})
	case "POST /api/panel":
		a.showPanel()
		ok(nil)
	case "POST /api/stop":
		var body struct {
			Close    bool `json:"close"`
			CloseOBS bool `json:"close_obs"`
		}
		readBody(r, &body)
		go a.stopThenClose(body.Close, body.CloseOBS)
		ok(nil)
	case "POST /api/close":
		// The page's Close: only once nothing is running (the page asks first).
		switch a.currentPhase() {
		case phRunning, phStarting:
			fail(409, "Stop match day first.")
			return
		}
		ok(nil)
		go func() { a.stop(); a.quit() }()
	case "POST /api/agent/folder":
		var body struct{ Folder string }
		if !readBody(r, &body) {
			fail(400, "Bad request")
			return
		}
		folder := strings.Trim(strings.TrimSpace(body.Folder), `"`)
		if folder == "" || !isDir(folder) {
			fail(400, "That folder doesn't exist on this laptop.")
			return
		}
		if setup.IsAppFolder(folder) {
			fail(400, "That's CricketStream's own folder, not NV Play's — choose the folder NV Play writes its scoreboard to.")
			return
		}
		if a.currentPhase() != phAgent {
			fail(409, "This laptop isn't set up as the scorer's laptop.")
			return
		}
		folder, _ = filepath.Abs(folder)
		agent.SaveFolder(folder)
		a.stopAgent()
		a.startAgent(folder, "chosen")
		ok(nil)
	case "POST /api/open":
		var body struct{ What string }
		readBody(r, &body)
		dir := map[string]string{"app": assets.Dir(), "logs": filepath.Dir(LogFile())}[body.What]
		if dir == "" {
			fail(400, "Unknown folder")
			return
		}
		os.MkdirAll(dir, 0o755)
		if err := browser.OpenFolder(dir); err != nil {
			fail(500, err.Error())
			return
		}
		ok(map[string]any{"folder": dir})
	default:
		fail(404, "Not found")
	}
}

// saveSetup is the setup page's Save, in the window: saved, the app moves straight on to
// this computer's job — no restart.
func (a *App) saveSetup(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, map[string]any{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	switch a.currentPhase() {
	case phStarting, phRunning:
		// The running server's own Settings page is the place for that (it applies live).
		writeJSON(w, map[string]any{"ok": false, "error": "Match day is running — use Settings in the control panel."}, 409)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 400)
		return
	}
	code, out, res := setup.Save(body)
	if res != nil {
		out["next"] = "/"
		a.enterRole(res.Values.Role)
	}
	writeJSON(w, out, code)
}

func (a *App) focus() {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	switch {
	case w != nil:
		w.Focus()
	case !a.opts.Headless:
		browser.Open(a.base + "/")
	}
}

// clubFile is the settings page's "Move to another laptop": POST /setup/export gives the
// club file (a ZIP; passwords and keys only with {"secrets": true}), POST /setup/import
// applies one. In the app's window only, and not while match day is running — the server
// would be writing the same settings.
func (a *App) clubFile(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, map[string]any{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	switch a.currentPhase() {
	case phStarting, phRunning:
		writeJSON(w, map[string]any{"ok": false, "error": "Stop match day first."}, 409)
		return
	}
	if r.URL.Path == "/setup/export" {
		var body struct{ Secrets bool }
		readBody(r, &body)
		var buf bytes.Buffer
		sum, err := clubpack.Export(&buf, body.Secrets, a.opts.Version)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 500)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Club", sum.Club)
		w.Write(buf.Bytes())
		fmt.Printf("  ✓ Exported the club's settings (%d pictures%s)\n", total(sum.Pictures),
			map[bool]string{true: ", with passwords and keys", false: ""}[body.Secrets])
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, clubpack.MaxFileBytes+1))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 400)
		return
	}
	sum, err := clubpack.Import(data)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 400)
		return
	}
	kept := "" // a new laptop has no old settings to keep
	if sum.Backup != "" {
		kept = " (the old settings are in " + sum.Backup + ")"
	}
	fmt.Printf("  ✓ Imported %s's settings and %d pictures%s\n", orStr(sum.Club, "the club"), total(sum.Pictures), kept)
	a.enterRole(setup.Role())
	writeJSON(w, map[string]any{"ok": true, "summary": sum, "next": "/"}, 200)
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func orStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
