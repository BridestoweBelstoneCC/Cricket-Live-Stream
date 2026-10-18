package setup

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/agent"
	"cricketstream/internal/assets"
	"cricketstream/internal/bridge"
	"cricketstream/internal/browser"
	"cricketstream/internal/ini"
	"cricketstream/internal/port"
)

//go:embed setup.html
var pageHTML []byte

// Result is what the page saved.
type Result struct {
	Values      Values
	BridgeToken string
}

// secretKeys are kept from the existing config.ini when the form leaves them blank, so
// re-running setup never needs the keys typed in again (and never shows them).
var secretKeys = [][2]string{{"API", "playcricket_key"}, {"API", "anthropic_key"},
	{"Stream", "youtube_stream_key"}, {"Auth", "club_password"}}

// secretFields are the form's names for them (a "Remove" on the page names these).
var secretFields = map[string][2]string{"pc_key": {"API", "playcricket_key"},
	"anthropic_key": {"API", "anthropic_key"}, "yt_key": {"Stream", "youtube_stream_key"},
	"club_password": {"Auth", "club_password"}}

// Merge applies the answers to an existing config.ini (or a new one): keys the
// interview doesn't own — the OBS password match day writes, manage_obs, a hand-set
// bitrate — survive a re-run.
func (v Values) Merge(f *ini.File) *ini.File {
	if f == nil {
		f = ini.New()
	}
	keep := map[[2]string]string{}
	for _, k := range secretKeys {
		if old, ok := f.Lookup(k[0], k[1]); ok {
			keep[k] = old
		}
	}
	token, _ := f.Lookup("Auth", "control_token")
	obsPW, _ := f.Lookup("OBS", "obs_password")
	fresh := v.Config()
	for _, s := range fresh.Sections() {
		for _, k := range fresh.Keys(s) {
			val, _ := fresh.Lookup(s, k)
			f.Set(s, k, val)
		}
	}
	if v.Role == RoleStreaming {
		cleared := map[[2]string]bool{}
		for _, field := range v.Clear {
			if k, ok := secretFields[field]; ok {
				cleared[k] = true
			}
		}
		for k, old := range keep {
			if cur, _ := f.Lookup(k[0], k[1]); cur == "" && !cleared[k] {
				f.Set(k[0], k[1], old)
			}
		}
		if strings.TrimSpace(token) != "" {
			f.Set("Auth", "control_token", token)
		}
		if obsPW != "" {
			f.Set("OBS", "obs_password", obsPW)
		}
	}
	return f
}

// existingValues is the current config.ini as form defaults — secrets left out.
func existingValues() (Values, bool) {
	f, err := ini.Load(ConfigFile())
	if err != nil {
		return Values{}, false
	}
	v := Values{Role: Role(), Name: f.Get("Club", "name", ""), Abbrev: f.Get("Club", "abbreviation", ""),
		Colour: f.Get("Club", "home_colour", ""), Motto: f.Get("Club", "motto", ""),
		PCID: f.Get("Club", "playcricket_id", ""), PCSFolder: f.Get("Scoring", "pcs_output_folder", ""),
		ScorebarStyle: f.Get("Graphics", "scorebar_style", ""), ReplayFolder: f.Get("OBS", "replay_folder", ""),
		YTTitle: f.Get("Stream", "youtube_title", ""), MaxOvers: f.Get("Stream", "max_overs", ""),
		AllowRemote: f.Get("Network", "bind_host", "127.0.0.1") == "0.0.0.0"}
	return v, true
}

type server struct {
	mu     sync.Mutex
	result *Result
	done   chan struct{}
	once   sync.Once
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(b)
}

// sameOrigin: the page's own POST, never another site's (the same check as the panel).
func sameOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			if err != nil || (u.Host != "" && u.Host != r.Host) {
				return false
			}
		}
	}
	return true
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/setup"):
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store, must-revalidate")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", PageCSP)
		w.Write(pageHTML)
	case r.Method == "GET" && r.URL.Path == "/setup/info":
		writeJSON(w, Info(), 200)
	case r.Method == "POST" && r.URL.Path == "/setup/save":
		s.save(w, r)
	case r.Method == "POST" && r.URL.Path == "/setup/check":
		if !sameOrigin(r) {
			writeJSON(w, map[string]any{"ok": false, "error": "Cross-origin request rejected"}, 403)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		code, out := RunCheck(body)
		writeJSON(w, out, code)
	// The scorebar preview: the real overlay in preview mode, which renders one frame of
	// demo data and reads /state once for the club's colours.
	case r.Method == "GET" && r.URL.Path == "/overlay":
		b, err := assets.Read("overlay.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	case r.Method == "GET" && r.URL.Path == "/state":
		// The preview iframe's own address carries the colour being chosen; the overlay
		// itself asks for /state with no query.
		colour := r.URL.Query().Get("colour")
		if ref, err := url.Parse(r.Header.Get("Referer")); colour == "" && err == nil {
			colour = ref.Query().Get("colour")
		}
		if colour == "" {
			colour = "#1a3a5c"
		}
		writeJSON(w, map[string]any{"home_team": "Home CC", "away_team": "Opposition CC",
			"home_colour": colour, "away_colour": "#7b2d2d", "demo_mode": true}, 200)
	case r.Method == "GET" && r.URL.Path == "/favicon.ico":
		if b, err := assets.Read("brand/favicon-64.png"); err == nil {
			w.Header().Set("Content-Type", "image/png")
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Page is the setup page itself. The running server also serves it at /settings, where
// it becomes the settings page.
func Page() []byte { return pageHTML }

// PageCSP is the page's Content-Security-Policy: everything inline, nothing external, and
// a same-origin frame for the stream preview. A ground photo for the preview background
// stays in the browser (a data: URL), never uploaded.
const PageCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; frame-src 'self'; frame-ancestors 'self'"

// Info is what the page needs to fill itself in: defaults, the NV Play folder found on
// this machine, the scorebar styles, and the current answers (secrets left out).
func Info() map[string]any {
	folder, how := DetectPCSFolder()
	why := map[string]string{"live": "NV Play is writing to it right now",
		"stale": "it has a scoreboard file from an earlier match",
		"empty": "it's NV Play's usual output folder (nothing in it yet)"}[how]
	existing, have := existingValues()
	host, _ := os.Hostname()
	var styles []map[string]string
	for _, st := range ScorebarStyles {
		styles = append(styles, map[string]string{"id": st.ID, "name": st.Name, "about": st.About})
	}
	out := map[string]any{"detected": map[string]any{"folder": folder, "how": how, "why": why},
		"defaults": map[string]any{"replay_folder": DefaultReplayFolder(), "colour": "#1a3a5c",
			"yt_title": "LIVE: {home} vs {away}", "max_overs": "50", "bridge_port": bridge.DefaultPort},
		"styles": styles, "hostname": host, "lan_ip": agent.LocalIP(), "config_file": ConfigFile(),
		"have_config": have, "port": port.Server(), "os": runtime.GOOS}
	if have {
		out["existing"] = existing
		out["secrets_set"] = secretsSet()
	}
	return out
}

// secretsSet says which secrets config.ini already holds (never what they are), so the
// page can say "saved" beside a blank field.
func secretsSet() map[string]bool {
	out := map[string]bool{}
	f, err := ini.Load(ConfigFile())
	if err != nil {
		return out
	}
	for field, k := range secretFields {
		out[field] = strings.TrimSpace(f.Get(k[0], k[1], "")) != ""
	}
	return out
}

// Save validates and applies a submitted form: the status code, the reply, and the
// result when it was saved.
func Save(body []byte) (int, map[string]any, *Result) {
	var v Values
	if err := json.Unmarshal(body, &v); err != nil {
		return 400, map[string]any{"ok": false, "error": "That wasn't a setup form."}, nil
	}
	v.Normalise()
	if p := v.Problems(); len(p) > 0 {
		return 400, map[string]any{"ok": false, "error": strings.Join(p, " ")}, nil
	}
	res, out, err := Apply(v)
	if err != nil {
		return 500, map[string]any{"ok": false, "error": err.Error()}, nil
	}
	if v.DesktopShortcut {
		// Best-effort: a shortcut that couldn't be made never fails the save.
		if where, err := DesktopShortcut(); err != nil {
			out["shortcut_error"] = err.Error()
		} else {
			out["shortcut"] = where
		}
	}
	return 200, out, &res
}

func (s *server) save(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, map[string]any{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 400)
		return
	}
	code, out, res := Save(body)
	writeJSON(w, out, code)
	if res == nil {
		return
	}
	s.mu.Lock()
	s.result = res
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

// Apply saves the answers: config.ini (merged over any existing one), the scorer
// agent's remembered folder or the bridge's own config, and NV Play's template.
func Apply(v Values) (Result, map[string]any, error) {
	path := ConfigFile()
	existing, err := ini.Load(path)
	if err != nil {
		existing = nil
	}
	f := v.Merge(existing)
	data := append([]byte(Header), f.Bytes()...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return Result{}, nil, fmt.Errorf("couldn't save %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return Result{}, nil, fmt.Errorf("couldn't save %s: %w", path, err)
	}
	res := Result{Values: v}
	out := map[string]any{"ok": true, "role": v.Role, "config_file": path}
	switch v.Role {
	case RoleScorer:
		if v.PCSFolder != "" {
			agent.SaveFolder(v.PCSFolder)
		}
	case RoleBridge:
		c, ok := bridge.Load()
		if !ok || c.Token == "" {
			c.Token = bridge.NewToken()
		}
		c.Folder, c.Port = v.PCSFolder, v.BridgePort
		if err := bridge.Save(c); err != nil {
			return Result{}, nil, fmt.Errorf("couldn't save the bridge settings: %w", err)
		}
		res.BridgeToken = c.Token
		out["bridge_token"], out["bridge_port"] = c.Token, c.Port
	}
	if result, folder := InstallNVPlayTemplate(v.PCSFolder); result == "installed" || result == "updated" {
		out["template"] = map[string]string{"result": result, "folder": folder}
	}
	return res, out, nil
}

// Options for Run.
type Options struct {
	Port        int  // preferred port (the server's own 5000, so the address is familiar)
	OpenBrowser bool // open the page in the default browser
	Say         func(format string, args ...any)
}

// Run serves the setup page until it's saved (or Ctrl+C), and returns what was saved.
func Run(o Options) (Result, error) {
	if o.Say == nil {
		o.Say = func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	}
	if o.Port == 0 {
		o.Port = port.Server()
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)))
	if err != nil {
		// Something else has the port (a CricketStream already running?): any free one.
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return Result{}, err
		}
	}
	s := &server{done: make(chan struct{})}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 20 * time.Second}
	go srv.Serve(ln)
	u := "http://" + ln.Addr().String() + "/setup"
	o.Say("  Setup is open in your browser:  %s", u)
	o.Say("  (If it didn't open, copy that address into a browser on this laptop.)")
	if o.OpenBrowser && os.Getenv("CRICKETSTREAM_NO_BROWSER") != "1" {
		browser.Open(u)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	select {
	case <-s.done:
	case <-sig:
		srv.Close()
		return Result{}, errors.New("setup cancelled — nothing saved")
	}
	// Let the page finish reading its answer (and show what comes next) before closing.
	time.Sleep(1500 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.result, nil
}

// Handler is the setup page's handler (tests).
func Handler() (http.Handler, <-chan struct{}) {
	s := &server{done: make(chan struct{})}
	return s, s.done
}
