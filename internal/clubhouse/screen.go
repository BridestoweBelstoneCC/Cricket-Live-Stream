package clubhouse

// The screen: `cricketstream clubhouse` on the Raspberry Pi. It serves the touch page on
// the Pi's own loopback port (the laptop is off when the page is first touched, so the
// page can't come from it), keeps the pairing in clubhouse.ini beside the app, wakes
// the laptop, and passes the page's requests on to the laptop's link with the key.
//
// The PIN is the screen's own lock — it's a public room. Locked, the page shows the
// score and the stream and nothing that changes anything; the laptop's allowlist is
// still the real boundary whatever the page does.
//
// Paired with the laptop's score-only key, it's a score-only screen: the score, the
// scorecard, overs and table, and the stream — no buttons at all, and the laptop refuses
// anything else from that key whatever this program asks.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
)

//go:embed clubhouse.html
var screenHTML string

// ScreenOptions for RunScreen.
type ScreenOptions struct {
	Port     int    // the page's port (DefaultScreenPort)
	Kiosk    bool   // open Chromium full screen on the page
	Laptop   string // pair with this address instead of searching (host or host:port)
	Headless bool   // open nothing (tests)
	Version  string
	// Newer compares two versions (server.VersionNewer); with Kiosk, a newer release is
	// installed before the screen opens (update.go). nil: never.
	Newer func(a, b string) bool
}

// ScreenFile is the screen's own settings, beside the app.
func ScreenFile() string { return assets.Join("clubhouse.ini") }

func badgeFile() string { return assets.Join("clubhouse_badge.img") }

type screenConfig struct {
	Host    string
	Port    int
	Key     string
	MACs    []string
	View    bool // paired with the score-only key
	Club    string
	Name    string // the laptop's computer name
	Colour  string
	PinSalt string
	PinHash string
	WakeFor int // seconds to wait for a woken laptop before saying so
}

func (c screenConfig) paired() bool { return c.Host != "" && len(NormaliseKey(c.Key)) == KeyLen }

func loadScreenConfig() screenConfig {
	c := screenConfig{Port: DefaultPort, WakeFor: 240}
	f, err := ini.Load(ScreenFile())
	if err != nil {
		return c
	}
	c.Host = strings.TrimSpace(f.Get("Laptop", "host", ""))
	c.Port = f.GetInt("Laptop", "port", DefaultPort)
	c.Key = NormaliseKey(f.Get("Laptop", "key", ""))
	for _, m := range strings.Split(f.Get("Laptop", "macs", ""), ",") {
		if m = strings.TrimSpace(m); m != "" {
			c.MACs = append(c.MACs, m)
		}
	}
	c.View = f.GetBool("Laptop", "view_only", false)
	c.Club = f.Get("Laptop", "club", "")
	c.Name = f.Get("Laptop", "name", "")
	c.Colour = f.Get("Laptop", "colour", "")
	c.PinSalt = f.Get("Screen", "pin_salt", "")
	c.PinHash = f.Get("Screen", "pin_hash", "")
	c.WakeFor = f.GetInt("Screen", "wake_wait", 240)
	return c
}

func saveScreenConfig(c screenConfig) error {
	f := ini.New()
	f.Set("Laptop", "host", c.Host)
	f.Set("Laptop", "port", strconv.Itoa(c.Port))
	f.Set("Laptop", "key", c.Key)
	f.Set("Laptop", "macs", strings.Join(c.MACs, ","))
	f.Set("Laptop", "view_only", map[bool]string{true: "yes", false: "no"}[c.View])
	f.Set("Laptop", "club", c.Club)
	f.Set("Laptop", "name", c.Name)
	f.Set("Laptop", "colour", c.Colour)
	f.Set("Screen", "pin_salt", c.PinSalt)
	f.Set("Screen", "pin_hash", c.PinHash)
	f.Set("Screen", "wake_wait", strconv.Itoa(c.WakeFor))
	data := append([]byte("# CricketStream clubhouse screen — paired with the streaming laptop.\n"+
		"# Holds the pairing key: keep it private.\n\n"), f.Bytes()...)
	tmp := ScreenFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ScreenFile())
}

func hashPIN(salt, pin string) string {
	h := sha256.Sum256([]byte(salt + ":" + pin))
	return hex.EncodeToString(h[:])
}

// unlockFor is how long an unlock lasts without a touch; every action renews it.
var unlockFor = 10 * time.Minute

// Screen is the running screen.
type Screen struct {
	o      ScreenOptions
	key    string // the page's key: only the page itself drives the API
	base   string
	client *http.Client // status polls: short
	slow   *http.Client // everything else: a pre-match check takes ~25s

	bg sync.WaitGroup // background(): tests wait for these so one can't land in the next

	mu          sync.Mutex
	cfg         screenConfig
	laptop      map[string]any
	reachable   bool
	lastOK      time.Time
	lastErr     string
	wakingSince time.Time
	unlockedTil time.Time
	pinFails    int
	pinLockTil  time.Time
	badgeAt     time.Time
	asks        []askingLaptop // asking to pair, waiting for a yes on the laptop
	askUntil    time.Time
	askErr      string
	askRunning  bool
	done        chan struct{}
}

// NewScreen makes a screen (RunScreen's guts, and the tests').
func NewScreen(o ScreenOptions) *Screen {
	if o.Port == 0 {
		o.Port = DefaultScreenPort
	}
	b := make([]byte, 16)
	rand.Read(b)
	return &Screen{o: o, key: hex.EncodeToString(b), cfg: loadScreenConfig(),
		client: &http.Client{Timeout: 3 * time.Second}, slow: &http.Client{Timeout: 45 * time.Second},
		done: make(chan struct{})}
}

// RunScreen is `cricketstream clubhouse`. It returns the exit code.
func RunScreen(o ScreenOptions) int {
	// Started at boot (the start script's loop runs it again): update first, then open.
	if o.Kiosk && !o.Headless && SelfUpdate(o.Version, o.Newer) {
		return 0
	}
	s := NewScreen(o)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.o.Port)))
	if err != nil {
		console.Die(fmt.Sprintf("The clubhouse screen couldn't open port %d", s.o.Port), err.Error(), "",
			"Is it already running? (pkill -f 'cricketstream clubhouse' stops it)")
	}
	s.base = "http://" + ln.Addr().String()
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()
	go s.pollLaptop(2 * time.Second)
	fmt.Printf("  CricketStream clubhouse screen — %s/\n", s.base)
	if s.cfg.paired() {
		fmt.Printf("  Paired with %s (%s)\n", orDefault(s.cfg.Club, s.cfg.Name), s.cfg.Host)
	} else {
		fmt.Println("  Not paired yet: type the pairing key from the streaming laptop on the screen.")
	}
	var kiosk *kioskBrowser
	if o.Kiosk && !o.Headless {
		kiosk = startKiosk(s.base + "/?kiosk=1")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	close(s.done)
	if kiosk != nil {
		kiosk.Stop()
	}
	return 0
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func (s *Screen) linkURL(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("http://%s%s", net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)), path)
}

// toLaptop sends a request to the link. The response body is the link's (or the
// server's, through it), passed back as it came.
func (s *Screen) toLaptop(c *http.Client, method, path string, body []byte) (int, http.Header, []byte, error) {
	s.mu.Lock()
	key := s.cfg.Key
	s.mu.Unlock()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, s.linkURL(path), rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set(KeyHeader, key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, resp.Header, b, err
}

// pollLaptop asks the link how it is every interval, so the page's own (frequent)
// status calls never wait on the network.
func (s *Screen) pollLaptop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.pollOnce()
		select {
		case <-s.done:
			return
		case <-t.C:
		}
	}
}

func (s *Screen) background(f func()) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		f()
	}()
}

func (s *Screen) pollOnce() {
	s.mu.Lock()
	paired := s.cfg.paired()
	s.mu.Unlock()
	if !paired {
		return
	}
	code, _, b, err := s.toLaptop(s.client, "GET", "/clubhouse/status", nil)
	var st map[string]any
	if err == nil && code == 200 {
		err = json.Unmarshal(b, &st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err != nil:
		s.reachable, s.laptop, s.lastErr = false, nil, "" // off, asleep, or not on the network: the usual
	case code != 200:
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(b, &e) // not JSON (a 404 page, say): no error text, and the fallback below
		s.reachable, s.laptop = false, nil
		s.lastErr = orDefault(e.Error, fmt.Sprintf("The laptop said %d", code))
		if code == 403 {
			s.lastErr = "The laptop doesn't accept this screen's pairing key any more — pair again with the key on its match-day screen."
		}
	default:
		if !s.wakingSince.IsZero() {
			// Woken by this screen, and now answering: tell the laptop, so its pre-match
			// check can say a wake has worked (and how long it takes).
			after, _ := json.Marshal(map[string]int{"after": int(time.Since(s.wakingSince).Seconds())})
			s.background(func() { s.toLaptop(s.client, "POST", "/clubhouse/woke", after) })
		}
		s.reachable, s.laptop, s.lastOK, s.lastErr = true, st, time.Now(), ""
		s.wakingSince = time.Time{}
		if club, _ := st["club"].(string); club != "" && club != s.cfg.Club {
			s.cfg.Club = club
			saveScreenConfig(s.cfg)
		}
		if ready, _ := st["server_ready"].(bool); ready && time.Since(s.badgeAt) > 10*time.Minute {
			s.badgeAt = time.Now()
			go s.cacheClub()
		}
	}
}

// cacheClub keeps the club's badge and colour on the Pi, so the screen looks like the
// club's even while the laptop is off.
func (s *Screen) cacheClub() {
	code, _, b, err := s.toLaptop(s.client, "GET", "/clubhouse/server/state", nil)
	if err != nil || code != 200 {
		return
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil {
		return
	}
	colour, _ := st["home_colour"].(string)
	logoKey := fmt.Sprint(orDefault(fmt.Sprint(nz(st["home_club_id"])), fmt.Sprint(nz(st["home_team"]))))
	s.mu.Lock()
	if colour != "" && colour != s.cfg.Colour && validColour(colour) {
		s.cfg.Colour = colour
		saveScreenConfig(s.cfg)
	}
	s.mu.Unlock()
	if logoKey == "" || logoKey == "Home CC" {
		return
	}
	code, h, img, err := s.toLaptop(s.client, "GET", "/clubhouse/server/logo/"+url.PathEscape(logoKey), nil)
	if err != nil || code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "image/") || len(img) > 4<<20 {
		return
	}
	os.WriteFile(badgeFile(), img, 0o644)
}

func nz(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func validColour(c string) bool {
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(c[1:])
	return err == nil
}

// ── The page and its API ──

func (s *Screen) ownHost(r *http.Request) bool {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(s.base, "http://"))
	return r.Host == "127.0.0.1:"+port || r.Host == "localhost:"+port
}

func sameOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Host != r.Host {
				return false
			}
		}
	}
	return true
}

// The page's CSP: its own script; the only thing from outside is YouTube's player, for
// the full-screen stream.
const screenCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; connect-src 'self'; frame-src https://www.youtube-nocookie.com; frame-ancestors 'none'"

func (s *Screen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.ownHost(r) {
		http.Error(w, "Not this address", http.StatusMisdirectedRequest)
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/":
		page := screenHTML
		if b, err := os.ReadFile(assets.Join("clubhouse.html")); err == nil {
			page = string(b) // edited beside the app: no rebuild, like the other pages
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", screenCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		w.Write([]byte(strings.Replace(page, "{{SCREEN_KEY}}", html.EscapeString(s.key), 1)))
		return
	case r.Method == "GET" && strings.HasPrefix(p, "/fonts/") && strings.HasSuffix(p, ".ttf") && !strings.Contains(p[1:], "/.."):
		// The scoreboard's numerals: Barlow Condensed, built into the app (OFL).
		if b, err := assets.Read("fonts/" + strings.TrimPrefix(p, "/fonts/")); err == nil && !strings.Contains(p[len("/fonts/"):], "/") {
			w.Header().Set("Content-Type", "font/ttf")
			w.Header().Set("Cache-Control", "max-age=86400")
			w.Write(b)
			return
		}
	case r.Method == "GET" && (p == "/favicon.ico" || p == "/mark.png"):
		if b, err := assets.Read("brand/mark-transparent.png"); err == nil {
			w.Header().Set("Content-Type", "image/png")
			w.Write(b)
			return
		}
	case strings.HasPrefix(p, "/api/"):
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Screen-Key")), []byte(s.key)) != 1 {
			writeJSON(w, map[string]any{"ok": false, "error": "Not the screen's page"}, 403)
			return
		}
		if r.Method == "POST" && !sameOrigin(r) {
			writeJSON(w, map[string]any{"ok": false, "error": "Cross-origin request rejected"}, 403)
			return
		}
		s.api(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Screen) unlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.PinHash == "" || time.Now().Before(s.unlockedTil)
}

// touch renews an unlock (an operator at work isn't asked for the PIN mid-task).
func (s *Screen) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.PinHash != "" && time.Now().Before(s.unlockedTil) {
		s.unlockedTil = time.Now().Add(unlockFor)
	}
}

// publicReads: what a locked screen may show — the match and the stream.
var publicReads = map[string]bool{"/live/view": true, "/state": true, "/preview.jpg": true,
	"/youtube/live": true, "/checklist": true, "/stream/monitor": true, "/health": true,
	"/match/scorecard": true, "/league/table": true}

func (s *Screen) api(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api")
	fail := func(status int, msg string) { writeJSON(w, map[string]any{"ok": false, "error": msg}, status) }
	locked := func() bool {
		if !s.unlocked() {
			fail(401, "Unlock the screen first.")
			return true
		}
		s.touch()
		return false
	}
	s.mu.Lock()
	view := s.cfg.View
	s.mu.Unlock()
	viewFail := func() { fail(403, "This screen only shows the score.") }
	if rest, ok := strings.CutPrefix(p, "/server/"); ok {
		path := "/" + rest
		if view {
			// Nothing to unlock: a score-only screen reads, and only what the laptop lets it.
			if !ViewAllowed(r.Method, path) {
				viewFail()
				return
			}
			s.forward(w, r, "/clubhouse/server"+strings.TrimPrefix(r.URL.EscapedPath(), "/api/server"))
			return
		}
		if r.Method == "POST" || !(publicReads[path] || strings.HasPrefix(path, "/logo/")) {
			if locked() {
				return
			}
		}
		if !ProxyAllowed(r.Method, path) {
			fail(403, "The clubhouse screen can't do that.")
			return
		}
		s.forward(w, r, "/clubhouse/server"+strings.TrimPrefix(r.URL.EscapedPath(), "/api/server"))
		return
	}
	if rest, ok := strings.CutPrefix(p, "/laptop/"); ok {
		switch rest {
		case "start", "answer", "stop", "poweroff", "poweroff/cancel":
		default:
			fail(404, "Not found")
			return
		}
		if view {
			viewFail()
			return
		}
		if r.Method != "POST" || locked() {
			if r.Method != "POST" {
				fail(405, "POST only")
			}
			return
		}
		s.forward(w, r, "/clubhouse/"+rest)
		return
	}
	switch r.Method + " " + p {
	case "GET /status":
		writeJSON(w, s.status(), 200)
	case "GET /badge":
		b, err := os.ReadFile(badgeFile())
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	case "POST /unlock":
		var body struct {
			PIN string `json:"pin"`
		}
		readJSON(r, &body)
		ok, msg := s.unlock(body.PIN)
		if !ok {
			fail(401, msg)
			return
		}
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /lock":
		s.mu.Lock()
		s.unlockedTil = time.Time{}
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /pin":
		var body struct {
			PIN string `json:"pin"`
		}
		readJSON(r, &body)
		if locked() {
			return
		}
		if err := s.setPIN(body.PIN); err != nil {
			fail(400, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /pair":
		var body struct {
			Key     string `json:"key"`
			Address string `json:"address"`
		}
		readJSON(r, &body)
		if locked() {
			return
		}
		out, err := s.pair(body.Key, orDefault(body.Address, s.o.Laptop))
		if err != nil {
			fail(400, err.Error())
			return
		}
		writeJSON(w, out, 200)
	case "POST /forget":
		if locked() {
			return
		}
		s.mu.Lock()
		s.cfg.Host, s.cfg.Key, s.cfg.MACs, s.cfg.View, s.cfg.Club, s.cfg.Name = "", "", nil, false, "", ""
		s.reachable, s.laptop = false, nil
		err := saveScreenConfig(s.cfg)
		s.mu.Unlock()
		os.Remove(badgeFile())
		if err != nil {
			fail(500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /ask":
		var body struct {
			Address string `json:"address"`
		}
		readJSON(r, &body)
		if locked() {
			return
		}
		if err := s.startAsking(orDefault(body.Address, s.o.Laptop)); err != nil {
			fail(400, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /ask/cancel":
		s.stopAsking("")
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /wake/cancel":
		// "Stop waiting": nothing to undo on the laptop, just the screen giving up.
		s.mu.Lock()
		s.wakingSince = time.Time{}
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true}, 200)
	case "POST /wake":
		if view {
			viewFail()
			return
		}
		if locked() {
			return
		}
		s.mu.Lock()
		macs := append([]string(nil), s.cfg.MACs...)
		s.mu.Unlock()
		n, err := wake(macs)
		if err != nil {
			fail(500, capitalise(err.Error()))
			return
		}
		s.mu.Lock()
		s.wakingSince = time.Now()
		s.mu.Unlock()
		fmt.Printf("  Woke the streaming laptop (%d packets)\n", n)
		writeJSON(w, map[string]any{"ok": true}, 200)
	default:
		fail(404, "Not found")
	}
}

// wake is Wake (a test seam: tests send nothing on the network).
var wake = Wake

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Screen) forward(w http.ResponseWriter, r *http.Request, path string) {
	if !s.isPaired() {
		writeJSON(w, map[string]any{"ok": false, "error": "Not paired with the streaming laptop yet."}, 409)
		return
	}
	var body []byte
	if r.Method == "POST" {
		var err error
		if body, err = io.ReadAll(io.LimitReader(r.Body, 64<<10)); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "Bad request"}, 400)
			return
		}
		if body == nil {
			body = []byte{}
		}
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	code, h, b, err := s.toLaptop(s.slow, r.Method, path, body)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "The streaming laptop isn't answering.", "offline": true}, 502)
		return
	}
	if ct := h.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	w.Write(b)
}

func (s *Screen) isPaired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.paired()
}

func (s *Screen) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	_, badgeErr := os.Stat(badgeFile())
	out := map[string]any{"ok": true, "paired": c.paired(), "club": c.Club, "laptop_name": c.Name,
		"laptop_host": c.Host, "colour": c.Colour, "has_badge": badgeErr == nil, "reachable": s.reachable,
		"error": s.lastErr, "laptop": s.laptop, "pin_set": c.PinHash != "", "can_wake": len(c.MACs) > 0 && !c.View,
		"view_only": c.View,
		"wake_wait": c.WakeFor, "version": s.o.Version}
	unlocked := c.PinHash == "" || time.Now().Before(s.unlockedTil)
	out["unlocked"] = unlocked
	if c.PinHash != "" && unlocked {
		out["unlock_left"] = int(time.Until(s.unlockedTil).Seconds())
	}
	if !s.wakingSince.IsZero() {
		out["waking_for"] = int(time.Since(s.wakingSince).Seconds())
	}
	if !s.lastOK.IsZero() {
		out["seen_ago"] = int(time.Since(s.lastOK).Seconds())
	}
	asking := []map[string]any{}
	for _, a := range s.asks {
		asking = append(asking, map[string]any{"code": a.code, "club": a.club})
	}
	out["asking"], out["ask_error"] = asking, s.askErr
	return out
}

func (s *Screen) unlock(pin string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.cfg.PinHash == "" {
		return true, ""
	}
	if now.Before(s.pinLockTil) {
		return false, fmt.Sprintf("Too many wrong PINs — try again in %d seconds.", int(time.Until(s.pinLockTil).Seconds())+1)
	}
	if subtle.ConstantTimeCompare([]byte(hashPIN(s.cfg.PinSalt, pin)), []byte(s.cfg.PinHash)) == 1 {
		s.pinFails, s.unlockedTil = 0, now.Add(unlockFor)
		return true, ""
	}
	s.pinFails++
	if s.pinFails >= 5 {
		s.pinFails, s.pinLockTil = 0, now.Add(time.Minute)
		return false, "Too many wrong PINs — try again in a minute."
	}
	return false, "That isn't the PIN."
}

// setPIN sets the screen's PIN (4 to 8 digits); "" removes it.
func (s *Screen) setPIN(pin string) error {
	pin = strings.TrimSpace(pin)
	if pin != "" {
		if len(pin) < 4 || len(pin) > 8 {
			return errors.New("A PIN is 4 to 8 digits.")
		}
		for _, c := range pin {
			if c < '0' || c > '9' {
				return errors.New("A PIN is digits only.")
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pin == "" {
		s.cfg.PinSalt, s.cfg.PinHash = "", ""
	} else {
		b := make([]byte, 16)
		rand.Read(b)
		s.cfg.PinSalt = hex.EncodeToString(b)
		s.cfg.PinHash = hashPIN(s.cfg.PinSalt, pin)
		s.unlockedTil = time.Now().Add(unlockFor) // whoever set it carries on
	}
	return saveScreenConfig(s.cfg)
}

// discover is "where are you?" on the club network: every link that answers, as
// host:port. Swappable for tests.
var discover = func(wait time.Duration) []string {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil
	}
	defer pc.Close()
	for _, t := range broadcastTargets() {
		if a, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(t, strconv.Itoa(Port()))); err == nil {
			pc.WriteTo(ProbeMagic, a)
		}
	}
	pc.SetReadDeadline(time.Now().Add(wait))
	seen := map[string]bool{}
	var out []string
	buf := make([]byte, 1024)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return out
		}
		var d struct {
			Service string `json:"service"`
			Port    int    `json:"port"`
		}
		ua, ok := addr.(*net.UDPAddr)
		if !ok || json.Unmarshal(buf[:n], &d) != nil || d.Service != Service || d.Port <= 0 {
			continue
		}
		hp := net.JoinHostPort(ua.IP.String(), strconv.Itoa(d.Port))
		if !seen[hp] {
			seen[hp] = true
			out = append(out, hp)
		}
	}
}

// candidates are the laptops to try: the one at address, if given; else every laptop that
// answers discovery, and the one this screen was paired with (discovery may be blocked).
func (s *Screen) candidates(address string) []string {
	if address = strings.TrimSpace(address); address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			address = net.JoinHostPort(address, strconv.Itoa(Port()))
		}
		return []string{address}
	}
	candidates := discover(2 * time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Host != "" {
		hp := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
		for _, c := range candidates {
			if c == hp {
				return candidates
			}
		}
		candidates = append(candidates, hp)
	}
	return candidates
}

// pair finds the laptop holding key (at address, if given; else every laptop that
// answers discovery) and remembers it.
func (s *Screen) pair(key, address string) (map[string]any, error) {
	key = NormaliseKey(key)
	if len(key) != KeyLen {
		return nil, errors.New("The pairing key is 8 letters and numbers — it's on the streaming laptop's match-day screen, under Clubhouse screen.")
	}
	candidates := s.candidates(address)
	if len(candidates) == 0 {
		return nil, errors.New("No streaming laptop answered. Is CricketStream open on it, with the clubhouse screen switched on, " +
			"on the same network as this screen? If the network blocks the search, type the laptop's address too.")
	}
	wrongKey := false
	for _, hp := range candidates {
		req, _ := http.NewRequest("GET", "http://"+hp+"/clubhouse/pair", nil)
		req.Header.Set(KeyHeader, key)
		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}
		var d struct {
			OK            bool          `json:"ok"`
			Host          string        `json:"host"`
			Club          string        `json:"club"`
			NICs          []WakeableNIC `json:"nics"`
			AllowPowerOff bool          `json:"allow_power_off"`
			Role          string        `json:"role"`
			Error         string        `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&d)
		resp.Body.Close()
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			wrongKey = true
			continue
		}
		if resp.StatusCode != 200 || !d.OK {
			continue
		}
		host, portS, _ := net.SplitHostPort(hp)
		port, _ := strconv.Atoi(portS)
		view := d.Role == "view"
		var macs []string
		wired := false
		for _, n := range d.NICs {
			macs = append(macs, n.MAC)
			wired = wired || n.Wired
		}
		s.mu.Lock()
		s.cfg.Host, s.cfg.Port, s.cfg.Key, s.cfg.MACs, s.cfg.Club, s.cfg.Name = host, port, key, macs, d.Club, d.Host
		s.cfg.View = view
		err = saveScreenConfig(s.cfg)
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("Paired, but couldn't save it: %v", err)
		}
		fmt.Printf("  ✓ Paired with %s (%s)\n", orDefault(d.Club, d.Host), host)
		s.background(s.pollOnce)
		out := map[string]any{"ok": true, "club": d.Club, "host": d.Host, "address": host, "nics": d.NICs,
			"allow_power_off": d.AllowPowerOff, "view_only": view}
		if !wired && !view {
			out["warning"] = "The laptop isn't plugged into the network by cable, so this screen can't switch it on " +
				"(it can still run match day once the laptop is on). Plug a network cable in and pair again."
		}
		return out, nil
	}
	if wrongKey {
		return nil, errors.New("A streaming laptop answered but didn't accept that key — check it on the laptop's match-day screen.")
	}
	return nil, errors.New("Found a laptop but couldn't pair with it — try again, or type its address.")
}

// ── Asking to pair (the laptop's side is ask.go) ──

// askingLaptop is one laptop this screen has asked.
type askingLaptop struct {
	hp, secret, code, club string
}

// askEvery is how often the screen asks the laptops it's waiting on for their answer.
var askEvery = 2 * time.Second

// postLink sends a keyless request to a link (asking to pair).
func (s *Screen) postLink(hp, path string, body any) (int, map[string]any, error) {
	b, _ := json.Marshal(body)
	resp, err := s.client.Post("http://"+hp+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	return resp.StatusCode, out, nil
}

// askOne asks one laptop; its code to show, or ok false.
func (s *Screen) askOne(hp string) (askingLaptop, bool) {
	b := make([]byte, askSecretLen/2)
	rand.Read(b)
	name, _ := os.Hostname()
	a := askingLaptop{hp: hp, secret: hex.EncodeToString(b)}
	code, out, err := s.postLink(hp, "/clubhouse/ask", map[string]any{"secret": a.secret, "name": name})
	if err != nil || code != 200 || out["ok"] != true {
		return a, false
	}
	a.code, _ = out["code"].(string)
	a.club, _ = out["club"].(string)
	return a, a.code != ""
}

// startAsking asks every laptop it can find (or the one at address) to pair, and waits
// for someone at the laptop to say yes.
func (s *Screen) startAsking(address string) error {
	candidates := s.candidates(address)
	if len(candidates) == 0 {
		return errors.New("No streaming laptop answered. Is CricketStream open on it, with the clubhouse screen switched on, " +
			"on the same network as this screen? If the network blocks the search, type the laptop's address.")
	}
	var asks []askingLaptop
	for _, hp := range candidates {
		if a, ok := s.askOne(hp); ok {
			asks = append(asks, a)
		}
	}
	if len(asks) == 0 {
		return errors.New("A laptop answered but didn't take the request — is CricketStream up to date on it? You can type the pairing key instead.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks, s.askUntil, s.askErr = asks, time.Now().Add(askFor), ""
	if !s.askRunning {
		s.askRunning = true
		s.background(s.askLoop)
	}
	return nil
}

func (s *Screen) stopAsking(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks, s.askErr = nil, why
}

func (s *Screen) askLoop() {
	for {
		select {
		case <-s.done:
			s.mu.Lock()
			s.askRunning = false
			s.mu.Unlock()
			return
		case <-time.After(askEvery):
		}
		more := s.askOnce()
		// Stopped under the lock a new ask takes: one pressed just now is still asked.
		s.mu.Lock()
		if !more && len(s.asks) == 0 {
			s.askRunning = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

// askOnce asks each laptop waited on for its answer; false when there's nothing left
// to wait for.
func (s *Screen) askOnce() bool {
	s.mu.Lock()
	asks, until := append([]askingLaptop(nil), s.asks...), s.askUntil
	s.mu.Unlock()
	if len(asks) == 0 {
		return false
	}
	if time.Now().After(until) {
		s.stopAsking("Nobody answered on the laptop — press Pair from the laptop again.")
		return false
	}
	var keep []askingLaptop
	saidNo := false
	for _, a := range asks {
		code, out, err := s.postLink(a.hp, "/clubhouse/ask/poll", map[string]any{"secret": a.secret})
		status, _ := out["status"].(string)
		switch {
		case err != nil || code != 200:
			keep = append(keep, a) // off for a moment, or busy: ask again next time
		case status == "control" || status == "view":
			key, _ := out["key"].(string)
			s.stopAsking("")
			if _, err := s.pair(key, a.hp); err != nil {
				s.stopAsking(err.Error())
			}
			return false
		case status == "no":
			saidNo = true
		case status == "unknown":
			// The laptop forgot it (its link restarted with new settings): ask again —
			// with a new code, which the screen now shows.
			if again, ok := s.askOne(a.hp); ok {
				keep = append(keep, again)
			}
		default:
			keep = append(keep, a)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.asks == nil { // cancelled meanwhile
		return false
	}
	s.asks = keep
	if len(keep) == 0 {
		s.askErr = "Nobody on the laptop said yes."
		if saidNo {
			s.askErr = "The laptop said no."
		}
		return false
	}
	return true
}
