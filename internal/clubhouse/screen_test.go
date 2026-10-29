package clubhouse

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/assets"
)

type testScreen struct {
	*Screen
	t   *testing.T
	srv *httptest.Server
}

func newTestScreen(t *testing.T) *testScreen {
	t.Helper()
	saved := assets.Dir()
	assets.SetDir(t.TempDir())
	t.Cleanup(func() { assets.SetDir(saved) })
	s := NewScreen(ScreenOptions{Headless: true})
	srv := httptest.NewServer(s)
	s.base = srv.URL
	t.Cleanup(srv.Close)
	t.Cleanup(s.bg.Wait) // runs first: nothing of this test's lands in the next one's folder
	return &testScreen{Screen: s, t: t, srv: srv}
}

func (ts *testScreen) do(method, path string, body any, headers map[string]string) (int, map[string]any, []byte) {
	ts.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.srv.URL+path, rd)
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out, raw
}

// api is the page's own call.
func (ts *testScreen) api(method, path string, body any) (int, map[string]any) {
	ts.t.Helper()
	code, out, _ := ts.do(method, "/api/"+path, body, map[string]string{"X-Screen-Key": ts.key, "Origin": ts.srv.URL})
	if path == "pair" {
		// Pairing polls the laptop in the background; a test's own poll mustn't race it.
		ts.bg.Wait()
	}
	return code, out
}

func TestScreenPageCarriesItsKeyAndNothingElseDrivesIt(t *testing.T) {
	ts := newTestScreen(t)
	code, _, page := ts.do("GET", "/", nil, nil)
	if code != 200 || !bytes.Contains(page, []byte(`content="`+ts.key+`"`)) {
		t.Fatalf("page: %d, key not in it", code)
	}
	if code, _, _ := ts.do("GET", "/api/status", nil, nil); code != 403 {
		t.Errorf("no key: %d", code)
	}
	// Another web page in the kiosk browser (or a rebinding trick) can't drive it.
	if code, _, _ := ts.do("POST", "/api/wake", M{}, map[string]string{"X-Screen-Key": ts.key, "Origin": "http://evil.example"}); code != 403 {
		t.Errorf("cross-origin: %d", code)
	}
	if code, _, _ := ts.do("GET", "/api/status", nil, map[string]string{"X-Screen-Key": ts.key, "Host": "evil.example"}); code != http.StatusMisdirectedRequest {
		t.Errorf("wrong host: %d", code)
	}
	if code, _, font := ts.do("GET", "/fonts/BarlowCondensed-ExtraBold.ttf", nil, nil); code != 200 || len(font) < 1000 {
		t.Errorf("the scoreboard font: %d (%d bytes)", code, len(font))
	}
	if code, _, _ := ts.do("GET", "/fonts/../config.ini.ttf", nil, nil); code == 200 {
		t.Error("font route escaped its folder")
	}
}

type M = map[string]any

func TestScreenPairsAndRemembers(t *testing.T) {
	h := &fakeHost{}
	l := startTestLink(t, h, true)
	addr := l.Addr().String()
	defer swap(&discover, func(time.Duration) []string { return []string{addr} })()
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "pair", M{"key": "WRONGKEY"}); code != 400 || !strings.Contains(out["error"].(string), "didn't accept") {
		t.Fatalf("wrong key: %d %v", code, out)
	}
	code, out := ts.api("POST", "pair", M{"key": strings.ToLower(FormatKey(testKey))})
	if code != 200 || out["club"] != "Test CC" {
		t.Fatalf("pair: %d %v", code, out)
	}
	c := loadScreenConfig()
	if !c.paired() || c.Key != testKey || c.Club != "Test CC" {
		t.Fatalf("saved: %+v", c)
	}
	// Windows has no group/other bits to check (the screen runs on a Pi anyway).
	if st, _ := os.Stat(ScreenFile()); runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		t.Errorf("clubhouse.ini holds the key: must be owner-only, is %v", st.Mode().Perm())
	}
	ts.pollOnce()
	_, st := ts.api("GET", "status", nil)
	if st["paired"] != true || st["reachable"] != true || st["laptop"].(M)["phase"] != "home" {
		t.Fatalf("status after pairing: %v", st)
	}
	// Start, through the link.
	if code, out := ts.api("POST", "laptop/start", M{"manual": false}); code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	if len(h.started) != 1 {
		t.Fatal("start didn't reach the laptop")
	}
}

func TestScreenPairNeedsAProperKeyAndSomeoneToAnswer(t *testing.T) {
	defer swap(&discover, func(time.Duration) []string { return nil })()
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "pair", M{"key": "AB"}); code != 400 || !strings.Contains(out["error"].(string), "8 letters") {
		t.Fatalf("short key: %d %v", code, out)
	}
	if code, out := ts.api("POST", "pair", M{"key": testKey}); code != 400 || !strings.Contains(out["error"].(string), "No streaming laptop answered") {
		t.Fatalf("nobody there: %d %v", code, out)
	}
}

func TestScreenPINLocksControlsButNotTheScore(t *testing.T) {
	var proxied []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	h := &fakeHost{server: srv.URL}
	l := startTestLink(t, h, true)
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()}); code != 200 {
		t.Fatalf("pair by address: %d %v", code, out)
	}
	if code, out := ts.api("POST", "pin", M{"pin": "12a4"}); code != 400 {
		t.Fatalf("a PIN with a letter: %d %v", code, out)
	}
	if code, _ := ts.api("POST", "pin", M{"pin": "2468"}); code != 200 {
		t.Fatal("set PIN")
	}
	ts.api("POST", "lock", M{})

	// Locked: the score and the picture still show...
	if code, _ := ts.api("GET", "server/live/view", nil); code != 200 {
		t.Errorf("a locked screen must still show the score: %d", code)
	}
	// ...but nothing that changes anything reaches the laptop.
	before := len(proxied)
	for _, p := range []string{"laptop/start", "laptop/stop", "laptop/poweroff", "wake", "server/checklist/action", "pin", "forget", "pair"} {
		if code, _ := ts.api("POST", p, M{}); code != 401 {
			t.Errorf("locked %s: %d, want 401", p, code)
		}
	}
	if code, _ := ts.api("GET", "server/cameras/discover", nil); code != 401 {
		t.Errorf("locked camera search: %d", code)
	}
	if len(proxied) != before || len(h.started) != 0 || len(h.stopped) != 0 {
		t.Fatal("a locked screen reached the laptop")
	}

	// Wrong PINs: five, then a pause.
	for i := 0; i < 4; i++ {
		if code, out := ts.api("POST", "unlock", M{"pin": "0000"}); code != 401 || out["error"] != "That isn't the PIN." {
			t.Fatalf("wrong PIN %d: %d %v", i, code, out)
		}
	}
	ts.api("POST", "unlock", M{"pin": "0000"})
	if code, out := ts.api("POST", "unlock", M{"pin": "2468"}); code != 401 || !strings.Contains(out["error"].(string), "Too many") {
		t.Fatalf("after five wrong PINs the right one should wait: %d %v", code, out)
	}
	ts.mu.Lock()
	ts.pinLockTil = time.Time{}
	ts.mu.Unlock()
	if code, _ := ts.api("POST", "unlock", M{"pin": "2468"}); code != 200 {
		t.Fatal("unlock")
	}
	if code, _ := ts.api("POST", "server/checklist/action", M{"action": "go_live"}); code != 200 || proxied[len(proxied)-1] != "POST /checklist/action" {
		t.Fatalf("unlocked go live: %d %v", code, proxied)
	}
	// The screen's own check mirrors the laptop's allowlist.
	if code, _ := ts.api("POST", "server/settings/save", M{}); code != 403 {
		t.Errorf("off the allowlist: %d", code)
	}
	// An unlock wears off.
	defer swap(&unlockFor, time.Millisecond)()
	ts.api("POST", "unlock", M{"pin": "2468"})
	time.Sleep(5 * time.Millisecond)
	if code, _ := ts.api("POST", "laptop/start", M{}); code != 401 {
		t.Fatalf("expired unlock: %d", code)
	}
}

func TestScreenWakes(t *testing.T) {
	var woke []string
	defer swap(&wake, func(macs []string) (int, error) { woke = macs; return 6, nil })()
	h := &fakeHost{}
	l := startTestLink(t, h, true)
	ts := newTestScreen(t)
	ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()})
	// Pairing asks the laptop how it is in the background; let that land first, or it
	// (rightly) ends the wait below as soon as it does.
	for i := 0; i < 200; i++ {
		if _, st := ts.api("GET", "status", nil); st["reachable"] == true {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ts.mu.Lock()
	ts.cfg.MACs = []string{"aa:bb:cc:dd:ee:ff"}
	ts.reachable = false // switched off since
	ts.mu.Unlock()
	if code, out := ts.api("POST", "wake", M{}); code != 200 {
		t.Fatalf("wake: %d %v", code, out)
	}
	if len(woke) != 1 || woke[0] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("woke %v", woke)
	}
	_, st := ts.api("GET", "status", nil)
	if st["waking_for"] == nil {
		t.Fatal("status should say it's waking")
	}
	// The laptop answering ends the wait.
	ts.pollOnce()
	if _, st = ts.api("GET", "status", nil); st["waking_for"] != nil {
		t.Fatal("still waking after the laptop answered")
	}
	ts.api("POST", "wake", M{})
	ts.api("POST", "wake/cancel", M{})
	if _, st = ts.api("GET", "status", nil); st["waking_for"] != nil {
		t.Fatal("stop waiting didn't")
	}
}

func TestScreenNoticesARevokedKey(t *testing.T) {
	l := startTestLink(t, &fakeHost{}, false)
	ts := newTestScreen(t)
	ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()})
	ts.mu.Lock()
	ts.cfg.Key = "ZZZZZZZZ" // the laptop made a new key
	ts.mu.Unlock()
	ts.pollOnce()
	_, st := ts.api("GET", "status", nil)
	if st["reachable"] != false || !strings.Contains(st["error"].(string), "pair again") {
		t.Fatalf("status: %v", st)
	}
}

// Something on the laptop's port that isn't the link (or a link answering without an
// error field): the screen says what it got, never "<nil>".
func TestScreenSaysWhatAStrangeAnswerWas(t *testing.T) {
	other := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(other.Close)
	ts := newTestScreen(t)
	host, port, _ := strings.Cut(strings.TrimPrefix(other.URL, "http://"), ":")
	ts.mu.Lock()
	ts.cfg.Host, ts.cfg.Key = host, testKey
	ts.cfg.Port, _ = strconv.Atoi(port)
	ts.mu.Unlock()
	ts.pollOnce()
	_, st := ts.api("GET", "status", nil)
	if st["reachable"] != false || st["error"] != "The laptop said 404" {
		t.Fatalf("status: %v", st)
	}
}

// The club's badge and colour are kept on the Pi, so the screen looks like the club's
// with the laptop off. Only a real colour and a real image are kept; the default club
// name has no badge to fetch.
func TestScreenKeepsTheClubsBadgeAndColour(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	var state M
	var logoAsked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/state":
			json.NewEncoder(w).Encode(state)
		case strings.HasPrefix(r.URL.Path, "/logo/"):
			logoAsked = append(logoAsked, r.URL.Path)
			if r.URL.Path == "/logo/29434" {
				w.Header().Set("Content-Type", "image/png")
				w.Write(png)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>not a badge</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	l := startTestLink(t, &fakeHost{server: server.URL, token: "tok"}, false)
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()}); code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}

	state = M{"home_colour": "#0a2240", "home_club_id": 29434.0, "home_team": "Test CC"}
	ts.cacheClub()
	if c := loadScreenConfig(); c.Colour != "#0a2240" {
		t.Fatalf("colour not kept: %q", c.Colour)
	}
	code, _, b := ts.do("GET", "/api/badge", nil, map[string]string{"X-Screen-Key": ts.key})
	if code != 200 || !bytes.Equal(b, png) {
		t.Fatalf("badge: %d (%d bytes)", code, len(b))
	}

	// Not a colour, and a "logo" that's a web page: neither replaces what's kept.
	state = M{"home_colour": "red; background:url(x)", "home_club_id": "", "home_team": "Other CC"}
	ts.cacheClub()
	if c := loadScreenConfig(); c.Colour != "#0a2240" {
		t.Fatalf("a bad colour was kept: %q", c.Colour)
	}
	if _, _, b := ts.do("GET", "/api/badge", nil, map[string]string{"X-Screen-Key": ts.key}); !bytes.Equal(b, png) {
		t.Fatal("a non-image replaced the badge")
	}
	if last := logoAsked[len(logoAsked)-1]; last != "/logo/Other%20CC" && last != "/logo/Other CC" {
		t.Fatalf("logo looked up by name as %q", last)
	}

	// The default club name has no badge: not even asked for.
	n := len(logoAsked)
	state = M{"home_team": "Home CC"}
	ts.cacheClub()
	if len(logoAsked) != n {
		t.Fatalf("asked for the default club's badge: %v", logoAsked[n:])
	}
}

func TestScreenKioskArgs(t *testing.T) {
	saved := assets.Dir()
	dir := t.TempDir()
	assets.SetDir(dir)
	t.Cleanup(func() { assets.SetDir(saved) })
	args := kioskArgs("http://127.0.0.1:5097/?kiosk=1")
	if args[len(args)-1] != "http://127.0.0.1:5097/?kiosk=1" {
		t.Fatalf("the page isn't what it opens: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--kiosk", "--user-data-dir=" + filepath.Join(dir, "clubhouse-browser"),
		"--autoplay-policy=no-user-gesture-required", "--disable-session-crashed-bubble"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s (a profile of its own, video without a tap, no restore bar): %v", want, args)
		}
	}
}

func TestLinkPort(t *testing.T) {
	t.Setenv(PortEnv, "")
	if Port() != DefaultPort {
		t.Fatalf("default: %d", Port())
	}
	t.Setenv(PortEnv, "6123")
	if Port() != 6123 {
		t.Fatalf("moved: %d", Port())
	}
	for _, bad := range []string{"0", "70000", "abc", "-1"} {
		t.Setenv(PortEnv, bad)
		if Port() != DefaultPort {
			t.Errorf("%q should fall back to the default, got %d", bad, Port())
		}
	}
}

func TestScreenPageScriptOnlyUsesItsOwnAPI(t *testing.T) {
	// Every request the page makes goes through api()/blob() to its own /api/: nothing
	// talks to the laptop directly, and nothing external but YouTube's player.
	if strings.Contains(screenHTML, "fetch('http") || strings.Contains(screenHTML, "innerHTML") {
		t.Fatal("the page fetches elsewhere or builds HTML from strings")
	}
	if !strings.Contains(screenCSP, "frame-src https://www.youtube-nocookie.com;") {
		t.Fatal("CSP should allow only YouTube's player as a frame")
	}
}
