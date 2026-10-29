package clubhouse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKeyRoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		k := NewKey()
		if len(k) != KeyLen || NormaliseKey(k) != k {
			t.Fatalf("NewKey() = %q: not a normal %d-character key", k, KeyLen)
		}
		shown := FormatKey(k)
		if len(shown) != KeyLen+1 || shown[4] != '-' {
			t.Fatalf("FormatKey(%q) = %q", k, shown)
		}
		if !KeyMatches(k, strings.ToLower(shown)) || !KeyMatches(k, " "+shown[:4]+" "+shown[5:]+" ") {
			t.Fatalf("%q should match its own shown form typed loosely", k)
		}
	}
}

func TestKeyMatchesReadsAlikes(t *testing.T) {
	// O and 0, I/L and 1 type alike on a touch keyboard: the agent's pairing code allows
	// it, and so does the key.
	if !KeyMatches("A0B1C1DE", "aob-icl-de") {
		t.Fatal("O/0 and I/L/1 should read alike")
	}
}

func TestKeyMatchesRefusesEmptyAndShort(t *testing.T) {
	cases := [][2]string{{"", ""}, {"ABCD", "ABCD"}, {"ABCDEFGH", ""}, {"ABCDEFGH", "ABCDEFG"},
		{"ABCDEFGH", "ABCDEFGHJ"}, {"ABCDEFGH", "ABCD#FGH"}, {"ABCDEFGH", "ABCDEFGJ"}}
	for _, c := range cases {
		if KeyMatches(c[0], c[1]) {
			t.Errorf("KeyMatches(%q, %q) = true", c[0], c[1])
		}
	}
}

func TestMagicPacket(t *testing.T) {
	mac, err := ParseMAC("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	p := MagicPacket(mac)
	if len(p) != 102 {
		t.Fatalf("len = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) {
		t.Fatal("must start with six 0xFF bytes")
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(p[6+6*i:12+6*i], mac) {
			t.Fatalf("repetition %d isn't the address", i)
		}
	}
}

func TestParseMACForms(t *testing.T) {
	for _, s := range []string{"AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff", "AABBCCDDEEFF", " aa:bb:cc:dd:ee:ff "} {
		m, err := ParseMAC(s)
		if err != nil || m.String() != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("ParseMAC(%q) = %v, %v", s, m, err)
		}
	}
	for _, s := range []string{"", "zz:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee", "00:00:00:00:fe:80:00:00:00:00:00:00:00:00:00:01"} {
		if _, err := ParseMAC(s); err == nil {
			t.Errorf("ParseMAC(%q) should fail", s)
		}
	}
}

func TestWakeSendsEveryTargetPortAndRound(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	defer swap(&sendUDP, func(addr string, b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if len(b) != 102 {
			t.Errorf("packet length %d", len(b))
		}
		got[addr]++
		return nil
	})()
	defer swap(&broadcastTargets, func() []string { return []string{"255.255.255.255", "192.168.1.255"} })()
	defer swap(&wakeGap, time.Millisecond)()
	n, err := Wake([]string{"aa:bb:cc:dd:ee:ff", "not a mac", "11:22:33:44:55:66"})
	if err != nil {
		t.Fatal(err)
	}
	// 2 good addresses × 2 targets × 2 ports × 3 rounds.
	if n != 24 {
		t.Fatalf("sent %d, want 24", n)
	}
	for _, a := range []string{"255.255.255.255:9", "255.255.255.255:7", "192.168.1.255:9", "192.168.1.255:7"} {
		if got[a] != 6 {
			t.Errorf("%s got %d packets, want 6", a, got[a])
		}
	}
}

func TestWakeWithNothingUsable(t *testing.T) {
	defer swap(&sendUDP, func(string, []byte) error { t.Fatal("nothing should be sent"); return nil })()
	if _, err := Wake(nil); err == nil {
		t.Fatal("no address must be an error")
	}
	if _, err := Wake([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("a bad address should be named, got %v", err)
	}
}

func TestWakeReportsTotalFailure(t *testing.T) {
	defer swap(&sendUDP, func(string, []byte) error { return errors.New("network unreachable") })()
	defer swap(&wakeGap, time.Millisecond)()
	if _, err := Wake([]string{"aa:bb:cc:dd:ee:ff"}); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("got %v", err)
	}
}

func TestDirectedBroadcastsAreBroadcasts(t *testing.T) {
	for _, b := range directedBroadcasts() {
		ip := net.ParseIP(b).To4()
		if ip == nil || ip[3] == 0 {
			t.Errorf("%q doesn't look like a broadcast address", b)
		}
	}
}

func TestLooksWired(t *testing.T) {
	for name, want := range map[string]bool{"Ethernet": true, "Ethernet 2": true, "eth0": true, "enp3s0": true,
		"Wi-Fi": false, "wlan0": false, "wlp2s0": false, "Wireless Network Connection": false} {
		if looksWired(name) != want {
			t.Errorf("looksWired(%q) = %v", name, !want)
		}
	}
	for _, v := range []string{"Tailscale", "vEthernet (WSL)", "VirtualBox Host-Only Network", "docker0"} {
		if !isVirtual(v) {
			t.Errorf("%q should be skipped as virtual", v)
		}
	}
}

func TestProxyAllowlist(t *testing.T) {
	allowed := [][2]string{{"GET", "/live/view"}, {"GET", "/state"}, {"GET", "/preview.jpg"},
		{"GET", "/logo/29434"}, {"POST", "/checklist/action"}, {"POST", "/youtube/update"},
		{"GET", "/match/scorecard"}, {"GET", "/league/table"}}
	for _, c := range allowed {
		if !ProxyAllowed(c[0], c[1]) {
			t.Errorf("%s %s should be allowed", c[0], c[1])
		}
	}
	// The overlay's mutating poll and command queue, settings, the club's secrets, the
	// export, and route tricks: never.
	refused := [][2]string{{"GET", "/live"}, {"GET", "/commands"}, {"POST", "/state"}, {"POST", "/settings/save"},
		{"GET", "/settings/info"}, {"POST", "/login"}, {"GET", "/logo/"}, {"GET", "/logo/../state"},
		{"POST", "/live/view"}, {"GET", "/checklist/action"}, {"DELETE", "/state"}, {"GET", "//live/view"},
		{"POST", "/rehearsal"}, {"GET", "/control"}, {"POST", "/match/scorecard"}, {"GET", "/match/page"}}
	for _, c := range refused {
		if ProxyAllowed(c[0], c[1]) {
			t.Errorf("%s %s must be refused", c[0], c[1])
		}
	}
}

// ── The link ──

type fakeHost struct {
	mu        sync.Mutex
	started   []bool
	answered  []int
	stopped   []bool
	ready     bool
	readyWhy  string
	server    string
	token     string
	startErr  error
	answerHit bool
}

func (h *fakeHost) Status() map[string]any { return map[string]any{"phase": "home", "club": "Test CC"} }
func (h *fakeHost) StartMatchDay(manual bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = append(h.started, manual)
	return h.startErr
}
func (h *fakeHost) Answer(id int, yes bool, text string, dismissed bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answered = append(h.answered, id)
	return h.answerHit
}
func (h *fakeHost) StopMatchDay(closeOBS bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, closeOBS)
	return nil
}
func (h *fakeHost) ReadyToPowerOff() (bool, string) { return h.ready, h.readyWhy }
func (h *fakeHost) Server() (string, string)        { return h.server, h.token }

const testKey = "K8Q27WMX"

func startTestLink(t *testing.T, h Host, allowOff bool) *Link {
	t.Helper()
	l, err := StartLink(Config{Key: testKey, Port: freePort(t), AllowPowerOff: allowOff, Name: "Test CC"}, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	return l
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func call(t *testing.T, l *Link, method, path, key string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", l.Addr().(*net.TCPAddr).Port, path), rd)
	if key != "" {
		req.Header.Set(KeyHeader, key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestLinkNeedsAKey(t *testing.T) {
	if _, err := StartLink(Config{Key: "", Port: freePort(t)}, &fakeHost{}); err == nil {
		t.Fatal("a link with no key must not open")
	}
}

func TestLinkHelloIsOpenEverythingElseIsKeyed(t *testing.T) {
	h := &fakeHost{}
	l := startTestLink(t, h, false)
	if code, out := call(t, l, "GET", "/clubhouse/hello", "", nil); code != 200 || out["service"] != Service {
		t.Fatalf("hello: %d %v", code, out)
	}
	for _, p := range []string{"/clubhouse/status", "/clubhouse/pair", "/clubhouse/server/live/view"} {
		if code, _ := call(t, l, "GET", p, "", nil); code != 403 {
			t.Errorf("%s without a key: %d, want 403", p, code)
		}
	}
	if code, _ := call(t, l, "POST", "/clubhouse/start", "WRONGKEY", map[string]any{}); code != 403 {
		t.Errorf("start with the wrong key: %d", code)
	}
	if len(h.started) != 0 {
		t.Fatal("a wrong key must not start match day")
	}
	code, out := call(t, l, "GET", "/clubhouse/status", FormatKey(testKey), nil)
	if code != 200 || out["club"] != "Test CC" || out["phase"] != "home" {
		t.Fatalf("status: %d %v", code, out)
	}
	if seen, _ := l.Seen(); time.Since(seen) > 5*time.Second {
		t.Fatal("a keyed request should count as the screen being seen")
	}
}

func TestLinkRefusesBrowserPages(t *testing.T) {
	l := startTestLink(t, &fakeHost{}, false)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://%s/clubhouse/start", l.Addr()), strings.NewReader("{}"))
	req.Header.Set(KeyHeader, testKey)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("a request from a web page: %d, want 403", resp.StatusCode)
	}
}

func TestLinkLocksOutAGuesser(t *testing.T) {
	l := startTestLink(t, &fakeHost{}, false)
	for i := 0; i < maxFailsPerIP; i++ {
		call(t, l, "GET", "/clubhouse/status", "AAAAAAA"+string(crockford[i]), nil)
	}
	// Now even the right key is refused from that address for a while.
	if code, _ := call(t, l, "GET", "/clubhouse/status", testKey, nil); code != 429 {
		t.Fatalf("after %d wrong keys the right one got %d, want 429", maxFailsPerIP, code)
	}
}

func TestLinkStartAnswerStop(t *testing.T) {
	h := &fakeHost{answerHit: true}
	l := startTestLink(t, h, false)
	if code, _ := call(t, l, "POST", "/clubhouse/start", testKey, map[string]any{"manual": true}); code != 200 {
		t.Fatalf("start: %d", code)
	}
	if code, _ := call(t, l, "POST", "/clubhouse/answer", testKey, map[string]any{"id": 7, "yes": true}); code != 200 {
		t.Fatalf("answer: %d", code)
	}
	if code, _ := call(t, l, "POST", "/clubhouse/stop", testKey, map[string]any{"close_obs": true}); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	if len(h.started) != 1 || !h.started[0] || len(h.answered) != 1 || h.answered[0] != 7 || len(h.stopped) != 1 || !h.stopped[0] {
		t.Fatalf("host saw started=%v answered=%v stopped=%v", h.started, h.answered, h.stopped)
	}
	h.startErr = errors.New("Match day has already started.")
	if code, out := call(t, l, "POST", "/clubhouse/start", testKey, map[string]any{}); code != 409 || out["error"] != "Match day has already started." {
		t.Fatalf("a refused start should say why: %d %v", code, out)
	}
	h.answerHit = false
	if code, _ := call(t, l, "POST", "/clubhouse/answer", testKey, map[string]any{"id": 8}); code != 409 {
		t.Fatalf("an already-answered question: %d", code)
	}
}

func TestLinkPowerOffRules(t *testing.T) {
	var ran [][]string
	defer swap(&runCmd, func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	})()
	defer swap(&goos, "windows")()
	h := &fakeHost{ready: false, readyWhy: "OBS is still streaming."}
	off := startTestLink(t, h, false)
	if code, _ := call(t, off, "POST", "/clubhouse/poweroff", testKey, map[string]any{"delay": 30}); code != 403 {
		t.Fatalf("power-off not allowed by setup: %d, want 403", code)
	}
	on := startTestLink(t, h, true)
	if code, out := call(t, on, "POST", "/clubhouse/poweroff", testKey, map[string]any{"delay": 30}); code != 409 || out["error"] != "OBS is still streaming." {
		t.Fatalf("not ready: %d %v", code, out)
	}
	if len(ran) != 0 {
		t.Fatalf("nothing should have run yet: %v", ran)
	}
	h.ready = true
	code, out := call(t, on, "POST", "/clubhouse/poweroff", testKey, map[string]any{"delay": 30})
	if code != 200 || len(ran) != 1 || strings.Join(ran[0][:4], " ") != "shutdown /s /t 30" {
		t.Fatalf("power-off: %d %v ran=%v", code, out, ran)
	}
	if code, out := call(t, on, "GET", "/clubhouse/status", testKey, nil); code != 200 || out["power_off_in"] == nil {
		t.Fatalf("status should show the countdown: %v", out)
	}
	if code, _ := call(t, on, "POST", "/clubhouse/poweroff/cancel", testKey, nil); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	if strings.Join(ran[len(ran)-1], " ") != "shutdown /a" {
		t.Fatalf("cancel ran %v", ran[len(ran)-1])
	}
	if !PowerOffAt().IsZero() {
		t.Fatal("cancelled: nothing pending")
	}
}

func TestPowerOffClampsDelay(t *testing.T) {
	var ran []string
	defer swap(&runCmd, func(name string, args ...string) error { ran = append(ran, strings.Join(args, " ")); return nil })()
	defer swap(&goos, "windows")()
	PowerOff(0, "x")
	PowerOff(5*time.Hour, "x")
	CancelPowerOff()
	if !strings.HasPrefix(ran[0], "/s /t 10 ") || !strings.HasPrefix(ran[1], "/s /t 3600 ") {
		t.Fatalf("delays not clamped: %v", ran)
	}
}

func TestPowerOffElsewhereIsATimerThatCancels(t *testing.T) {
	ranCh := make(chan string, 1)
	defer swap(&runCmd, func(name string, args ...string) error { ranCh <- name; return nil })()
	defer swap(&goos, "linux")()
	if err := PowerOff(10*time.Second, "x"); err != nil {
		t.Fatal(err)
	}
	if PowerOffAt().IsZero() {
		t.Fatal("should be pending")
	}
	CancelPowerOff()
	select {
	case n := <-ranCh:
		t.Fatalf("cancelled, but %s ran", n)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLinkProxy(t *testing.T) {
	var seen *http.Request
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"path":"` + r.URL.Path + `"}`))
	}))
	defer srv.Close()
	h := &fakeHost{}
	l := startTestLink(t, h, false)

	// Not running yet: says so, distinctly.
	if code, out := call(t, l, "GET", "/clubhouse/server/live/view", testKey, nil); code != 503 || out["not_running"] != true {
		t.Fatalf("not running: %d %v", code, out)
	}
	h.server, h.token = srv.URL, "tok123"
	code, out := call(t, l, "POST", "/clubhouse/server/checklist/action", testKey, map[string]any{"action": "go_live"})
	if code != 200 || out["path"] != "/checklist/action" {
		t.Fatalf("proxy: %d %v", code, out)
	}
	if seen.Header.Get("Authorization") != "Bearer tok123" {
		t.Error("the window's login must be added")
	}
	if seen.Header.Get("X-Forwarded-For") == "" {
		t.Error("must be marked as from the screen, so the server's loopback carve-outs don't apply")
	}
	if seen.Header.Get(KeyHeader) != "" {
		t.Error("the pairing key must not be passed on to the server")
	}
	if !strings.Contains(seenBody, "go_live") {
		t.Errorf("body not passed: %q", seenBody)
	}
	// A badge named with a "#" or "?" stays one path (built from the decoded path, it was
	// cut at the "#" and the rest sent as a fragment).
	if code, _ := call(t, l, "GET", "/clubhouse/server/logo/Club%20%232%3F", testKey, nil); code != 200 || seen.URL.Path != "/logo/Club #2?" {
		t.Fatalf("badge path: %d %q", code, seen.URL.Path)
	}
	seen = nil
	if code, _ := call(t, l, "POST", "/clubhouse/server/settings/save", testKey, map[string]any{}); code != 403 || seen != nil {
		t.Fatalf("a route off the allowlist reached the server (%d)", code)
	}
	if code, _ := call(t, l, "GET", "/clubhouse/server/live", testKey, nil); code != 403 || seen != nil {
		t.Fatal("the overlay's mutating /live must never be reachable")
	}
}

func TestDiscoveryReplyHasNoSecrets(t *testing.T) {
	l := startTestLink(t, &fakeHost{}, false)
	if l.udp == nil {
		t.Skip("UDP port unavailable here")
	}
	port := l.Addr().(*net.TCPAddr).Port
	c, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write(ProbeMagic)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	reply := string(buf[:n])
	if !strings.Contains(reply, Service) || strings.Contains(reply, testKey) {
		t.Fatalf("reply %s", reply)
	}
}

func swap[T any](p *T, v T) func() {
	old := *p
	*p = v
	return func() { *p = old }
}
