package clubhouse

// The link: the streaming laptop's side. It answers the screen on the club network and
// does what it asks through the window's own match-day code (the Host), so a start from
// the clubhouse is exactly a press of the window's Start button — same launcher, same
// questions (shown on the screen AND in the window; whichever answers first wins).
//
// Everything but /clubhouse/hello needs a pairing key: the screen's key, or the
// score-only key, which reaches only the reads a score needs (viewGET) and nothing that
// starts, stops, wakes or switches off. The control panel's routes are
// reached through /clubhouse/server/..., on an allowlist, with the window's own login
// added here: the screen never has the club password, and can't reach Settings, the
// club's keys, the export, or anything else the allowlist doesn't name. Plain HTTP on
// the club network, like the control panel itself — the key is no more exposed than the
// panel's own login on the same wifi.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Host is what the window gives the link: its match-day controls.
type Host interface {
	// Status is the window's state for the screen: phase, the question showing, recent
	// activity lines, the club.
	Status() map[string]any
	// StartMatchDay is the window's Start button.
	StartMatchDay(manual bool) error
	// Answer answers the question showing; false if it's already been answered.
	Answer(id int, yes bool, text string, dismissed bool) bool
	// StopMatchDay is the window's Stop (closing OBS too when asked).
	StopMatchDay(closeOBS bool) error
	// ReadyToPowerOff: nothing running that switching off would lose — match day
	// stopped, OBS closed (closing it if it's idle). The reason when not.
	ReadyToPowerOff() (bool, string)
	// Server is the running server's address ("http://127.0.0.1:5000") and the window's
	// login for it; "" when match day isn't running.
	Server() (base, token string)
}

// Config is setup's [Clubhouse] section.
type Config struct {
	Key           string // the screen that runs match day; "" when only score-only screens may connect
	ViewKey       string // score-only screens; "" when there are none
	Port          int
	AllowPowerOff bool
	Name          string // the club, in discovery replies (not secret)
}

// The control panel's routes the screen may use. Reads that show the match and the
// readiness; the buttons a match-day operator presses. Nothing that changes settings or
// reaches the club's secrets — /state is the redacted read the overlay itself uses.
var proxyGET = map[string]bool{
	"/live/view": true, "/state": true, "/checklist": true, "/precheck/last": true,
	"/stream/monitor": true, "/health": true, "/preview.jpg": true, "/youtube/live": true,
	"/cameras/discover": true, "/match/scorecard": true, "/league/table": true,
}

var proxyPOST = map[string]bool{
	"/checklist/action": true, "/precheck": true, "/cameras/use": true, "/obs/add_camera": true,
	"/camera/scene": true, "/youtube/update": true,
}

// viewGET: what a score-only screen may read — the score, the scorecard and table, the
// club's colours and badge, the stream picture, and whether it's on air (the checklist).
// A subset of proxyGET.
var viewGET = map[string]bool{
	"/live/view": true, "/state": true, "/checklist": true, "/preview.jpg": true, "/youtube/live": true,
	"/match/scorecard": true, "/league/table": true,
}

// proxyGETPrefix: the club badges the scorebar uses (/logo/<club id or name>).
var proxyGETPrefix = []string{"/logo/"}

// ProxyAllowed: may the screen make this request of the server? Exported for the
// screen's own pre-check and the tests.
func ProxyAllowed(method, path string) bool {
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	switch method {
	case "GET":
		if proxyGET[path] {
			return true
		}
		for _, p := range proxyGETPrefix {
			if strings.HasPrefix(path, p) && len(path) > len(p) {
				return true
			}
		}
	case "POST":
		return proxyPOST[path]
	}
	return false
}

// ViewAllowed: may a score-only screen make this request of the server? Reads only.
func ViewAllowed(method, path string) bool {
	if method != "GET" || !ProxyAllowed(method, path) {
		return false
	}
	if viewGET[path] {
		return true
	}
	for _, p := range proxyGETPrefix {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// viewStatus: the parts of the window's status a score-only screen sees — never the
// question showing or the activity log.
var viewStatus = []string{"phase", "club", "version", "server_ready"}

// Link is the running link.
type Link struct {
	cfg  Config
	host Host
	ln   net.Listener
	udp  net.PacketConn
	srv  *http.Server

	mu           sync.Mutex
	seen         time.Time
	seenFrom     string
	seenView     time.Time
	seenViewFrom string
	fails        map[string]*failCount
	lockAll      time.Time
	allFails     []time.Time
}

type failCount struct {
	n     int
	first time.Time
	until time.Time
}

// Wrong keys allowed per address before it's refused for a while, and across all
// addresses (a guesser hopping addresses) before everyone is.
const (
	maxFailsPerIP  = 5
	failWindow     = time.Minute
	ipLockout      = 2 * time.Minute
	maxFailsGlobal = 30
	globalLockout  = 5 * time.Minute
)

// StartLink opens the link on the club network.
func StartLink(cfg Config, host Host) (*Link, error) {
	if len(NormaliseKey(cfg.Key)) != KeyLen && len(NormaliseKey(cfg.ViewKey)) != KeyLen {
		return nil, errors.New("no pairing key — turn the clubhouse screen off and on again on the match-day screen")
	}
	if cfg.Port == 0 {
		cfg.Port = Port()
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, fmt.Errorf("port %d is in use (another copy of CricketStream?): %v", cfg.Port, err)
	}
	l := &Link{cfg: cfg, host: host, ln: ln, fails: map[string]*failCount{}}
	l.srv = &http.Server{Handler: l, ReadHeaderTimeout: 10 * time.Second}
	go l.srv.Serve(ln)
	// Discovery is a convenience (the screen can be given the address instead): a
	// clash on the UDP port doesn't stop the link.
	if pc, err := net.ListenPacket("udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.Port))); err == nil {
		l.udp = pc
		go l.answerProbes(pc)
	}
	return l, nil
}

// Addr is where the link listens.
func (l *Link) Addr() net.Addr { return l.ln.Addr() }

// Stop closes the link.
func (l *Link) Stop() {
	if l.udp != nil {
		l.udp.Close()
	}
	l.srv.Close()
}

// Seen is when a screen last asked something with the right key, and from where.
func (l *Link) Seen() (time.Time, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen, l.seenFrom
}

// SeenView is Seen for the score-only key.
func (l *Link) SeenView() (time.Time, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seenView, l.seenViewFrom
}

func (l *Link) answerProbes(pc net.PacketConn) {
	buf := make([]byte, 512)
	name, _ := os.Hostname()
	reply, _ := json.Marshal(map[string]any{"service": Service, "port": l.cfg.Port, "host": name, "club": l.cfg.Name})
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if n >= len(ProbeMagic) && bytes.Equal(buf[:len(ProbeMagic)], ProbeMagic) {
			pc.WriteTo(reply, addr)
		}
	}
}

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// keyOK checks the request's key, counting wrong ones; view is true for the score-only
// key. False means the response has been written.
func (l *Link) keyOK(w http.ResponseWriter, r *http.Request) (ok, view bool) {
	ip, now := remoteIP(r), time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.lockAll) {
		writeJSON(w, map[string]any{"ok": false, "error": "Too many wrong pairing keys — try again in a few minutes."}, 429)
		return false, false
	}
	if f := l.fails[ip]; f != nil && now.Before(f.until) {
		writeJSON(w, map[string]any{"ok": false, "error": "Too many wrong pairing keys — try again in a couple of minutes."}, 429)
		return false, false
	}
	typed := r.Header.Get(KeyHeader)
	if KeyMatches(l.cfg.Key, typed) {
		l.seen, l.seenFrom = now, ip
		delete(l.fails, ip)
		return true, false
	}
	if KeyMatches(l.cfg.ViewKey, typed) {
		l.seenView, l.seenViewFrom = now, ip
		delete(l.fails, ip)
		return true, true
	}
	f := l.fails[ip]
	if f == nil || now.Sub(f.first) > failWindow {
		f = &failCount{first: now}
		l.fails[ip] = f
	}
	f.n++
	if f.n >= maxFailsPerIP {
		f.until = now.Add(ipLockout)
		fmt.Printf("  ⚠ Clubhouse screen: %d wrong pairing keys from %s — refusing it for a while\n", f.n, ip)
	}
	keep := l.allFails[:0]
	for _, t := range l.allFails {
		if now.Sub(t) < failWindow {
			keep = append(keep, t)
		}
	}
	l.allFails = append(keep, now)
	if len(l.allFails) >= maxFailsGlobal {
		l.lockAll, l.allFails = now.Add(globalLockout), nil
		fmt.Println("  ⚠ Clubhouse screen: many wrong pairing keys — refusing every screen for 5 minutes")
	}
	writeJSON(w, map[string]any{"ok": false, "error": "That isn't this laptop's pairing key."}, 403)
	return false, false
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(b)
}

func readJSON(r *http.Request, v any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return false
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	return json.Unmarshal(b, v) == nil
}

func (l *Link) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/clubhouse/hello" {
		writeJSON(w, map[string]any{"service": Service}, 200)
		return
	}
	if !strings.HasPrefix(p, "/clubhouse/") {
		http.NotFound(w, r)
		return
	}
	// A browser page elsewhere has no business here (the screen is a program, which
	// sends no Origin): refused before the key is even looked at.
	if r.Header.Get("Origin") != "" {
		writeJSON(w, map[string]any{"ok": false, "error": "Not from a browser"}, 403)
		return
	}
	ok, view := l.keyOK(w, r)
	if !ok {
		return
	}
	if rest, ok := strings.CutPrefix(p, "/clubhouse/server/"); ok {
		// Checked as decoded, sent as it came: a badge named "Club #2" must stay one path.
		raw := strings.TrimPrefix(r.URL.EscapedPath(), "/clubhouse/server")
		if view && !ViewAllowed(r.Method, "/"+rest) {
			writeJSON(w, map[string]any{"ok": false, "error": "This screen only shows the score."}, 403)
			return
		}
		l.proxy(w, r, "/"+rest, raw)
		return
	}
	fail := func(status int, msg string) { writeJSON(w, map[string]any{"ok": false, "error": msg}, status) }
	if view {
		l.serveView(w, r)
		return
	}
	switch r.Method + " " + p {
	case "GET /clubhouse/pair":
		name, _ := os.Hostname()
		writeJSON(w, map[string]any{"ok": true, "service": Service, "host": name, "club": l.cfg.Name,
			"nics": LocalNICs(localIPFor(r)), "allow_power_off": l.cfg.AllowPowerOff, "role": "control"}, 200)
	case "GET /clubhouse/status":
		out := map[string]any{"ok": true, "allow_power_off": l.cfg.AllowPowerOff}
		for k, v := range l.host.Status() {
			out[k] = v
		}
		if at := PowerOffAt(); !at.IsZero() {
			out["power_off_in"] = int(time.Until(at).Seconds())
		}
		writeJSON(w, out, 200)
	case "POST /clubhouse/start":
		var body struct {
			Manual bool `json:"manual"`
		}
		if !readJSON(r, &body) {
			fail(400, "Bad request")
			return
		}
		CancelPowerOff() // starting again inside the countdown: stay on
		if err := l.host.StartMatchDay(body.Manual); err != nil {
			fail(409, err.Error())
			return
		}
		fmt.Printf("  Clubhouse screen (%s): started match day\n", remoteIP(r))
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /clubhouse/answer":
		var body struct {
			ID        int    `json:"id"`
			Yes       bool   `json:"yes"`
			Text      string `json:"text"`
			Dismissed bool   `json:"dismissed"`
		}
		if !readJSON(r, &body) {
			fail(400, "Bad request")
			return
		}
		if !l.host.Answer(body.ID, body.Yes, body.Text, body.Dismissed) {
			fail(409, "That question has already been answered.")
			return
		}
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /clubhouse/stop":
		var body struct {
			CloseOBS bool `json:"close_obs"`
		}
		readJSON(r, &body)
		if err := l.host.StopMatchDay(body.CloseOBS); err != nil {
			fail(409, err.Error())
			return
		}
		fmt.Printf("  Clubhouse screen (%s): stopping match day\n", remoteIP(r))
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /clubhouse/poweroff":
		var body struct {
			Delay int `json:"delay"`
		}
		readJSON(r, &body)
		if !l.cfg.AllowPowerOff {
			fail(403, "Switching this laptop off from the clubhouse is turned off on the laptop "+
				"(match-day screen → Clubhouse screen).")
			return
		}
		if ok, why := l.host.ReadyToPowerOff(); !ok {
			fail(409, why)
			return
		}
		delay := time.Duration(body.Delay) * time.Second
		if delay <= 0 {
			delay = 60 * time.Second
		}
		if err := PowerOff(delay, "The clubhouse screen is switching this computer off. "+
			"To keep it on, press Cancel on the clubhouse screen."); err != nil {
			fail(500, "Couldn't switch off: "+err.Error())
			return
		}
		fmt.Printf("  Clubhouse screen (%s): switching this computer off in %ds\n", remoteIP(r), int(delay.Seconds()))
		writeJSON(w, map[string]any{"ok": true, "power_off_in": int(time.Until(PowerOffAt()).Seconds())}, 200)
	case "POST /clubhouse/woke":
		// The screen woke this laptop and it has now answered: the pre-match check's proof
		// that a wake works (clubhouse.LastWake).
		var body struct {
			After int `json:"after"`
		}
		if !readJSON(r, &body) || body.After < 0 || body.After > 24*3600 {
			fail(400, "Bad request")
			return
		}
		if err := saveWake(WakeRecord{At: time.Now(), AfterSec: body.After, From: remoteIP(r)}); err != nil {
			fail(500, err.Error())
			return
		}
		fmt.Printf("  Clubhouse screen (%s): switched this laptop on — it answered after %s\n", remoteIP(r), plainDuration(body.After))
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /clubhouse/poweroff/cancel":
		if err := CancelPowerOff(); err != nil {
			fail(500, err.Error())
			return
		}
		fmt.Printf("  Clubhouse screen (%s): kept this computer on\n", remoteIP(r))
		writeJSON(w, map[string]any{"ok": true}, 200)
	default:
		fail(404, "Not found")
	}
}

// serveView is the link for a score-only screen: pairing and a trimmed status. It can't
// start, stop, wake or switch anything off, so it isn't told the network cards (no wake)
// or offered power-off.
func (l *Link) serveView(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "GET /clubhouse/pair":
		name, _ := os.Hostname()
		writeJSON(w, map[string]any{"ok": true, "service": Service, "host": name, "club": l.cfg.Name, "role": "view"}, 200)
	case "GET /clubhouse/status":
		st := l.host.Status()
		out := map[string]any{"ok": true, "view": true}
		for _, k := range viewStatus {
			if v, ok := st[k]; ok {
				out[k] = v
			}
		}
		writeJSON(w, out, 200)
	default:
		writeJSON(w, map[string]any{"ok": false, "error": "This screen only shows the score."}, 403)
	}
}

// localIPFor is the address the screen reached this laptop on: the card a wake must
// reach is that one.
func localIPFor(r *http.Request) string {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if h, _, err := net.SplitHostPort(a.String()); err == nil {
			return h
		}
	}
	return ""
}

// proxyClient: a pre-match check takes up to ~25s; nothing else is slow.
var proxyClient = &http.Client{Timeout: 40 * time.Second}

// proxy passes an allowlisted request to the running server with the window's login.
// It's sent as from the screen (X-Forwarded-For), so the server's loopback carve-outs
// — no login for the overlay's own calls, a YouTube sign-in window popping up on the
// laptop — never apply to the clubhouse.
func (l *Link) proxy(w http.ResponseWriter, r *http.Request, path, rawPath string) {
	if !ProxyAllowed(r.Method, path) {
		writeJSON(w, map[string]any{"ok": false, "error": "The clubhouse screen can't do that."}, 403)
		return
	}
	base, token := l.host.Server()
	if base == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "Match day isn't running.", "not_running": true}, 503)
		return
	}
	var body io.Reader
	if r.Method == "POST" {
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "Bad request"}, 400)
			return
		}
		body = bytes.NewReader(b)
	}
	u := strings.TrimRight(base, "/") + rawPath
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequest(r.Method, u, body)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "Bad request"}, 400)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Forwarded-For", remoteIP(r))
	resp, err := proxyClient.Do(req)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "The stream server didn't answer: " + err.Error()}, 502)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}
