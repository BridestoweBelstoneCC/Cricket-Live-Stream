package server

// Commentary from a phone. A stranger's voice on a public stream: most of this is who can
// reach what. The real thing, phone page to OBS's recording, is
// internal/integration/talk_windows_test.go.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"cricketstream/internal/obsws"
)

// talkSandbox is a test server with commentary's seams pointed away from the machine: an
// ephemeral HTTPS port and no real OBS.
func talkSandbox(t *testing.T, password string) *testServer {
	t.Helper()
	ts := newTestServer(t, password)
	t.Setenv(talkPortEnv, "0")
	savedIP, savedCall := talkLANIP, obsCall
	talkLANIP = func() string { return "127.0.0.1" }
	obsCall = func(M, []obsws.Req, time.Duration) []map[string]any { return nil }
	t.Cleanup(func() {
		TalkOff()
		waitBackground(t)
		talkLANIP, obsCall = savedIP, savedCall
		cfMu.Lock()
		cfURL = ""
		cfMu.Unlock()
	})
	return ts
}

// talkClient is a phone that has clicked through the certificate warning.
func talkClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}}
}

func talkPhoneBase(t *testing.T) string {
	t.Helper()
	talk.Lock()
	defer talk.Unlock()
	if talk.port == 0 {
		t.Fatal("the phone's port isn't open")
	}
	return fmt.Sprintf("https://127.0.0.1:%d", talk.port)
}

func talkKey() string {
	talk.Lock()
	defer talk.Unlock()
	return talk.key
}

func phonePost(t *testing.T, base, path string, body any) (int, M) {
	t.Helper()
	b, _ := json.Marshal(body)
	r, err := talkClient().Post(base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var m M
	json.NewDecoder(r.Body).Decode(&m)
	return r.StatusCode, m
}

func phoneAudio(t *testing.T, base string, id, secret any, seq int, pcm []byte) (int, M) {
	t.Helper()
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/talk/audio?seq=%d", base, seq), bytes.NewReader(pcm))
	req.Header.Set("X-Talk-Id", fmt.Sprint(id))
	req.Header.Set("X-Talk-Secret", fmt.Sprint(secret))
	r, err := talkClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var m M
	json.NewDecoder(r.Body).Decode(&m)
	return r.StatusCode, m
}

// listenOBS is OBS's page on the WebSocket. Returns a channel of what it hears.
func listenOBS(t *testing.T, ts *testServer) (<-chan []byte, *websocket.Conn) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws://"+ts.addr+"/talk/out/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	out := make(chan []byte, 64)
	go func() {
		for {
			_, b, err := c.ReadMessage()
			if err != nil {
				return
			}
			out <- b
		}
	}()
	return out, c
}

func heard(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("OBS's page heard nothing")
		return nil
	}
}

func nothingHeard(t *testing.T, ch <-chan []byte) {
	t.Helper()
	select {
	case b := <-ch:
		t.Fatalf("OBS's page heard %d bytes it shouldn't have", len(b))
	case <-time.After(150 * time.Millisecond):
	}
}

// The whole path, over real HTTPS: nobody's heard until the operator says yes, and then
// their samples reach OBS's page byte for byte.
func TestTalkApprovalThenAudioReachesOBSPage(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	if st, b := ts.postJSON("/talk/control", M{"action": "on"}, auth); st != 200 || b["on"] != true {
		t.Fatalf("on: %d %v", st, b)
	}
	base := talkPhoneBase(t)
	ch, _ := listenOBS(t, ts)

	// The phone's page, over HTTPS with its security headers.
	r, err := talkClient().Get(base + "/talk?k=" + talkKey())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.ProtoMajor != 2 {
		t.Errorf("phone's page came over HTTP/%d: chunks would queue on one connection", r.ProtoMajor)
	}
	if !bytes.Contains(page, []byte("Ask to go on air")) || !strings.Contains(r.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("phone's page: %q csp %q", page[:60], r.Header.Get("Content-Security-Policy"))
	}

	st, j := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "  Sam  Bell‮ "})
	if st != 200 || j["name"] != "Sam Bell" || j["state"] != "waiting" {
		t.Fatalf("join: %d %v", st, j)
	}
	id, secret := j["id"], j["secret"]
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}

	// Asking gets you nothing on air.
	if _, a := phoneAudio(t, base, id, secret, 0, pcm); a["state"] != "waiting" {
		t.Fatalf("audio while waiting: %v", a)
	}
	nothingHeard(t, ch)
	if _, a := ts.getJSON("/talk/onair"); a["on_air"] != false || a["waiting"] != float64(1) {
		t.Fatalf("/talk/onair: %v", a)
	}

	// The screen sees them, and says yes.
	_, status := ts.getJSON("/talk/status", auth)
	w, _ := status["waiting"].([]any)
	if len(w) != 1 || w[0].(M)["name"] != "Sam Bell" {
		t.Fatalf("status.waiting: %v", status)
	}
	if st, b := ts.postJSON("/talk/control", M{"action": "approve", "id": id}, auth); st != 200 || b["on_air"] == nil {
		t.Fatalf("approve: %d %v", st, b)
	}
	if _, p := phonePost(t, base, "/talk/poll", M{"id": id, "secret": secret}); p["state"] != "on_air" {
		t.Fatalf("poll: %v", p)
	}
	// A request that isn't the page's JSON is refused, not read as an empty one (which
	// said "you've been taken off"), and takes nobody off air.
	for _, path := range []string{"/talk/poll", "/talk/level", "/talk/join"} {
		if st, _ := phonePost(t, base, path, "not an object"); st != 400 {
			t.Errorf("%s with a bad body: %d, want 400", path, st)
		}
	}
	if _, p := phonePost(t, base, "/talk/poll", M{"id": id, "secret": secret}); p["state"] != "on_air" {
		t.Fatalf("a bad request took the phone off air: %v", p)
	}
	if _, a := ts.getJSON("/talk/onair"); a["on_air"] != true || a["name"] != "Sam Bell" {
		t.Fatalf("/talk/onair on air: %v", a)
	}
	if _, a := phoneAudio(t, base, id, secret, 0, pcm); a["state"] != "on_air" {
		t.Fatalf("audio on air: %v", a)
	}
	if got := heard(t, ch); !bytes.Equal(got, pcm) {
		t.Fatalf("heard %v, sent %v", got, pcm)
	}

	// Someone else's id with the wrong secret is nobody.
	phoneAudio(t, base, id, "wrong", 1, pcm)
	nothingHeard(t, ch)

	// Taken off air: their audio stops at the server, whatever the phone keeps sending.
	ts.postJSON("/talk/control", M{"action": "end", "id": id}, auth)
	if _, a := phoneAudio(t, base, id, secret, 1, pcm); a["state"] != "ended" {
		t.Fatalf("audio after end: %v", a)
	}
	nothingHeard(t, ch)
}

// The boundary that matters: a phone that has clicked through the warning can reach the
// phone's routes and nothing else on that port, however it asks.
func TestTalkHTTPSPortServesOnlyThePhoneRoutes(t *testing.T) {
	ts := talkSandbox(t, "pw")
	ts.postJSON("/talk/control", M{"action": "on"}, ts.bearer("pw"))
	base := talkPhoneBase(t)
	for _, p := range []string{"/", "/control", "/overlay", "/state", "/live", "/live/view", "/commands", "/settings", "/health",
		"/talk/status", "/talk/qr.png", "/talk/out", "/talk/out/ws", "/talk/control", "/talk/onair", "/talk/../state", "/talk/", "/preview.jpg"} {
		r, err := talkClient().Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		// Go's mux isn't in the way: /talk/../state reaches the handler as written or as
		// cleaned, and neither is a phone route.
		if r.StatusCode != 404 && !(p == "/talk/../state" && r.StatusCode == 404) {
			t.Errorf("GET %s on the phone's port: %d %q", p, r.StatusCode, b)
		}
	}
	st, _ := phonePost(t, base, "/talk/control", M{"action": "off"})
	if st != 404 {
		t.Errorf("POST /talk/control on the phone's port: %d", st)
	}
	if st, _ := phonePost(t, base, "/settings/save", M{}); st != 404 {
		t.Errorf("POST /settings/save on the phone's port: %d", st)
	}
	if !talk.on {
		t.Error("something on the phone's port switched commentary off")
	}
}

// Nobody gets the buttons without the laptop's login, and the phone's key isn't one.
func TestTalkOperatorRoutesNeedALogin(t *testing.T) {
	ts := talkSandbox(t, "pw")
	if st, _ := ts.postJSON("/talk/control", M{"action": "on"}); st != 401 {
		t.Fatalf("on with no login: %d", st)
	}
	if talk.on {
		t.Fatal("switched on with no login")
	}
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	key := talkKey()
	for _, p := range []string{"/talk/status", "/talk/qr.png"} {
		if r := ts.get(p); r.status != 401 {
			t.Errorf("GET %s with no login: %d", p, r.status)
		}
		if r := ts.get(p + "?k=" + key); r.status != 401 {
			t.Errorf("GET %s with the phone's key: %d", p, r.status)
		}
	}
	if r := ts.get("/talk/qr.png", auth); r.status != 200 || !bytes.HasPrefix(r.body, []byte("\x89PNG")) {
		t.Errorf("QR code: %d", r.status)
	}
	// The public read never carries the key.
	if r := ts.get("/talk/onair"); bytes.Contains(r.body, []byte(key)) || bytes.Contains(r.body, []byte("url")) {
		t.Errorf("/talk/onair leaks: %s", r.body)
	}
	// A cross-site page can't press the button, even with a token.
	h := map[string]string{"Authorization": auth["Authorization"], "Origin": "https://evil.example"}
	if st, _ := ts.postJSON("/talk/control", M{"action": "off"}, h); st != 403 {
		t.Errorf("cross-origin off: %d", st)
	}
}

// OBS's feed: this machine only, never through anything that proxies (the clubhouse link
// and the tunnel both forward from loopback).
func TestTalkOBSFeedIsLoopbackOnly(t *testing.T) {
	ts := talkSandbox(t, "pw")
	xff := map[string]string{"X-Forwarded-For": "192.0.2.9"}
	for _, p := range []string{"/talk/out", "/talk/out/ws"} {
		if r := ts.get(p, xff); r.status != 404 {
			t.Errorf("GET %s forwarded: %d", p, r.status)
		}
	}
	// A web page in the laptop's own browser can't listen in: the WebSocket's origin check.
	hdr := http.Header{"Origin": []string{"https://evil.example"}}
	if c, _, err := websocket.DefaultDialer.Dial("ws://"+ts.addr+"/talk/out/ws", hdr); err == nil {
		c.Close()
		t.Error("a page elsewhere connected to OBS's feed")
	}
	if r := ts.get("/talk/out"); r.status != 200 || !bytes.Contains(r.body, []byte("AudioContext")) {
		t.Errorf("OBS's page on loopback: %d", r.status)
	}
}

// Off means off: the port closes, and the code in last week's photo does nothing.
func TestTalkOffForgetsEverything(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	oldKey := talkKey()
	_, j := phonePost(t, base, "/talk/join", M{"k": oldKey, "name": "Sam"})
	id, secret := j["id"], j["secret"]
	ts.postJSON("/talk/control", M{"action": "approve", "id": id}, auth)
	ts.postJSON("/talk/control", M{"action": "off"}, auth)
	if a := TalkOnAir(); a["on_air"] != false || a["on"] != false {
		t.Fatalf("after off: %v", a)
	}
	if c, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "https://"), time.Second); err == nil {
		c.Close()
		t.Error("the phone's port is still open")
	}
	if u, _ := talkURL(); u != "" {
		t.Errorf("a URL while off: %s", u)
	}
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base = talkPhoneBase(t)
	if talkKey() == oldKey {
		t.Fatal("the key didn't change")
	}
	if st, b := phonePost(t, base, "/talk/join", M{"k": oldKey, "name": "Sam"}); st != 403 || b["off"] != true {
		t.Errorf("old key: %d %v", st, b)
	}
	if _, p := phonePost(t, base, "/talk/poll", M{"id": id, "secret": secret}); p["state"] != "ended" {
		t.Errorf("old phone: %v", p)
	}
	// Pressed again while on: the QR code on the screen stays good.
	k := talkKey()
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	if talkKey() != k {
		t.Error("pressing on again changed the key")
	}
}

func TestTalkOneVoiceAndTimeouts(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	saved := talkGoneFor
	t.Cleanup(func() { talkGoneFor = saved })
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	_, a := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Sam"})
	_, b := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Jo"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": a["id"]}, auth)
	ts.postJSON("/talk/control", M{"action": "approve", "id": b["id"]}, auth)
	if _, p := phonePost(t, base, "/talk/poll", M{"id": a["id"], "secret": a["secret"]}); p["state"] != "ended" {
		t.Fatalf("handed over: %v", p)
	}
	if on := TalkOnAir(); on["name"] != "Jo" {
		t.Fatalf("on air: %v", on)
	}
	// A locked phone stops answering: off air, and the strap comes down with it.
	talkGoneFor = 20 * time.Millisecond
	time.Sleep(50 * time.Millisecond)
	talkTick()
	if on := TalkOnAir(); on["on_air"] != false {
		t.Fatalf("still on air: %v", on)
	}
	// The queue is capped: a code on a public screen mustn't flood the operator.
	talkGoneFor = time.Minute
	for i := 0; i < talkMaxWaiting; i++ {
		if st, _ := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": fmt.Sprint("P", i)}); st != 200 {
			t.Fatalf("join %d: %d", i, st)
		}
	}
	if st, _ := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "One too many"}); st != 429 {
		t.Errorf("past the cap: %d", st)
	}
	if st, b := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "   "}); st == 200 {
		t.Errorf("no name: %v", b)
	}
	if st, _ := ts.postJSON("/talk/control", M{"action": "approve", "id": "nobody"}, auth); st != 409 {
		t.Errorf("approving nobody: %d", st)
	}
}

// Bad chunks are refused, not played as noise.
func TestTalkBadChunks(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	ch, _ := listenOBS(t, ts)
	_, j := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Sam"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": j["id"]}, auth)
	if st, _ := phoneAudio(t, base, j["id"], j["secret"], 0, []byte{1, 2, 3}); st != 400 {
		t.Errorf("odd length: %d", st)
	}
	if st, _ := phoneAudio(t, base, j["id"], j["secret"], 0, nil); st != 400 {
		t.Errorf("empty: %d", st)
	}
	if st, _ := phoneAudio(t, base, j["id"], j["secret"], 0, make([]byte, talkChunkMax+2)); st != 413 {
		t.Errorf("huge: %d", st)
	}
	nothingHeard(t, ch)
}

// A couple of chunks are in flight at once, so they can land out of order; one that's lost
// is skipped rather than waited for.
func TestTalkChunksPlayInOrder(t *testing.T) {
	sandbox(t, "pw")
	var got []byte
	o := &talkOut{send: make(chan []byte, 32)}
	talk.Lock()
	defer func() {
		delete(talk.outs, o)
		talk.Unlock()
	}()
	talk.outs[o] = true
	take := func() {
		for {
			select {
			case b := <-o.send:
				got = append(got, b[0])
			default:
				return
			}
		}
	}
	p := &talkPhone{held: map[int64][]byte{}}
	send := func(seq int64) { p.takeChunk(seq, []byte{byte(seq), 0}); take() }
	send(0)
	send(2) // 1 is late
	if !bytes.Equal(got, []byte{0}) {
		t.Fatalf("played past a gap too soon: %v", got)
	}
	send(1)
	if !bytes.Equal(got, []byte{0, 1, 2}) {
		t.Fatalf("reordered: %v", got)
	}
	send(1) // a repeat
	if !bytes.Equal(got, []byte{0, 1, 2}) {
		t.Fatalf("repeat played: %v", got)
	}
	// 3 is lost: once more than a couple have come after it, carry on without it.
	send(4)
	send(5)
	send(6)
	if !bytes.Equal(got, []byte{0, 1, 2, 4, 5, 6}) {
		t.Fatalf("lost chunk: %v", got)
	}
	send(3) // and when it turns up after all, it's too late
	send(7)
	if !bytes.Equal(got, []byte{0, 1, 2, 4, 5, 6, 7}) {
		t.Fatalf("late chunk: %v", got)
	}
	// A phone that starts from a number other than 0 isn't left waiting for 0.
	q := &talkPhone{held: map[int64][]byte{}}
	got = nil
	q.takeChunk(40, []byte{40, 0})
	take()
	if !bytes.Equal(got, []byte{40}) {
		t.Fatalf("first chunk: %v", got)
	}
}

func TestTalkCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Sam":                         "Sam",
		"  Sam   Bell ":               "Sam Bell",
		"Sam\nBell\t\r":               "Sam Bell",
		"Sa\x00m\x1b[31m":             "Sam[31m",
		"‮Sam​":                       "Sam",
		"Zoë O'Brien":                 "Zoë O'Brien",
		"   ":                         "",
		"‮":                           "",
		strings.Repeat("x", 99):       strings.Repeat("x", talkNameMax),
		"abcdefghijklmnopqrstuvw xyz": "abcdefghijklmnopqrstuvw",
	} {
		if got := talkCleanName(in); got != want {
			t.Errorf("talkCleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The tunnel first: a real certificate means no warning, and works on mobile data.
func TestTalkURLPrefersTheTunnel(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	_, s := ts.getJSON("/talk/status", auth)
	if s["via"] != "lan" || s["cert_warning"] != true || !strings.HasPrefix(fmt.Sprint(s["url"]), "https://127.0.0.1:") {
		t.Fatalf("lan: %v", s)
	}
	cfMu.Lock()
	cfURL = "https://x.trycloudflare.com"
	cfMu.Unlock()
	_, s = ts.getJSON("/talk/status", auth)
	if s["via"] != "cloudflare" || s["cert_warning"] != false || s["url"] != "https://x.trycloudflare.com/talk?k="+talkKey() {
		t.Fatalf("tunnel: %v", s)
	}
	// Through the tunnel it's the main port: the phone's routes need the key, not a login.
	if st, b := ts.postJSON("/talk/join", M{"k": talkKey(), "name": "Sam"}); st != 200 || b["ok"] != true {
		t.Fatalf("join via the main port: %d %v", st, b)
	}
	if st, _ := ts.postJSON("/talk/join", M{"k": "wrong", "name": "Sam"}); st != 403 {
		t.Fatalf("wrong key via the main port: %d", st)
	}
	// And cross-site joins are refused here as well.
	if st, _ := ts.postJSON("/talk/join", M{"k": talkKey(), "name": "Sam"}, map[string]string{"Origin": "https://evil.example"}); st != 403 {
		t.Fatalf("cross-origin join: %d", st)
	}
}

// Apple refuses a certificate outright, with no warning to click through, over 825 days or
// without serverAuth or the address in its SANs.
func TestTalkCertificateSuitsAPhone(t *testing.T) {
	sandbox(t, "pw")
	saved := talkLANIP
	t.Cleanup(func() { talkLANIP = saved })
	talkLANIP = func() string { return "192.0.2.50" }
	served := func(cfg *tls.Config) []byte {
		t.Helper()
		c, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatal(err)
		}
		return c.Certificate[0]
	}
	cfg, err := talkTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Go only asks GetCertificate about a connection to a bare address when this is empty.
	if len(cfg.Certificates) != 0 {
		t.Error("a fixed certificate: the address couldn't move while it's on")
	}
	der := served(cfg)
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 825*24*time.Hour {
		t.Errorf("valid for %v", d)
	}
	if err := leaf.VerifyHostname("192.0.2.50"); err != nil {
		t.Error(err)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("key usage: %v", leaf.ExtKeyUsage)
	}
	// The private key isn't left readable by anyone else (where the OS can say).
	if fi, err := os.Stat(talkCertFile()); err != nil {
		t.Fatal(err)
	} else if fi.Size() == 0 {
		t.Error("empty certificate file")
	}
	// Kept for next time, so a phone that accepted it isn't asked again...
	cfg2, err := talkTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(served(cfg2), der) {
		t.Error("made a new certificate for no reason")
	}
	// ...until the laptop's address changes.
	talkLANIP = func() string { return "192.0.2.77" }
	cfg3, err := talkTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(served(cfg3), der) {
		t.Error("kept a certificate that doesn't name the new address")
	}
}

// A connection to an address, for the certificate to be chosen by.
type talkConnOn struct {
	net.Conn
	ip string
}

func (c talkConnOn) LocalAddr() net.Addr { return &net.TCPAddr{IP: net.ParseIP(c.ip), Port: 5095} }

// The laptop's address moves mid-match. The QR code follows it, and an iPhone refuses a
// certificate without the address in it, so a new address gets one that has it. A phone on
// the old address trusts only the certificate it accepted, so it keeps getting that one.
func TestTalkCertificateFollowsTheAddress(t *testing.T) {
	sandbox(t, "pw")
	saved := talkLANIP
	t.Cleanup(func() { talkLANIP = saved })
	talkLANIP = func() string { return "192.0.2.50" }
	cfg, err := talkTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	on := func(ip string) (*x509.Certificate, []byte) {
		t.Helper()
		c, err := cfg.GetCertificate(&tls.ClientHelloInfo{Conn: talkConnOn{ip: ip}})
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf, c.Certificate[0]
	}
	_, first := on("192.0.2.50")

	leaf, moved := on("192.0.2.77")
	if err := leaf.VerifyHostname("192.0.2.77"); err != nil {
		t.Fatalf("a phone on the new address: %v", err)
	}
	if _, again := on("192.0.2.77"); !bytes.Equal(again, moved) {
		t.Error("a new certificate on every connection")
	}
	if _, old := on("192.0.2.50"); !bytes.Equal(old, first) {
		t.Error("the phone already on air on the old address got a certificate it hasn't accepted")
	}
}

// The Pi may be on the cameras' switch with phones on the wifi: the operator's choice beats
// the screen's card, and it's made from a real list.
func TestTalkOperatorChoosesTheAddress(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	saved := talkCards
	t.Cleanup(func() {
		talkCards = saved
		talkForgetCards()
		talk.Lock()
		talk.chosenIP = ""
		talk.Unlock()
	})
	talkCards = func() []talkCard {
		return []talkCard{{"192.0.2.10", "Ethernet"}, {"198.51.100.7", "Wi-Fi"}}
	}
	talkForgetCards()
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	_, s := ts.getJSON("/talk/status", auth)
	if l, _ := s["addresses"].([]any); len(l) != 2 {
		t.Fatalf("the screen wasn't offered the cards: %v", s["addresses"])
	}
	if r, _ := ts.postJSON("/talk/control", M{"action": "address", "id": "203.0.113.9"}, auth); r != 409 {
		t.Errorf("chose an address that isn't this laptop's: %d", r)
	}
	_, j := phonePost(t, talkPhoneBase(t), "/talk/join", M{"k": talkKey(), "name": "Sam"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": j["id"]}, auth)
	key := talkKey()

	_, s = ts.postJSON("/talk/control", M{"action": "address", "id": "198.51.100.7"}, auth)
	if !strings.Contains(fmt.Sprint(s["url"]), "https://198.51.100.7:") || s["address"] != "198.51.100.7" {
		t.Errorf("the code didn't move: %v", s)
	}
	if key != talkKey() {
		t.Error("choosing an address changed the key")
	}
	if a := TalkOnAir(); a["on_air"] != true {
		t.Errorf("choosing an address took them off air: %v", a)
	}
	// Believed over the screen's card...
	talk.Lock()
	talk.localIP = "127.0.0.1"
	talk.Unlock()
	if ip := talkWantIP(); ip != "198.51.100.7" {
		t.Errorf("the screen's card beat the operator: %s", ip)
	}
	// ...until that card has gone.
	talkCards = func() []talkCard { return []talkCard{{"192.0.2.10", "Ethernet"}} }
	talkForgetCards()
	if ip := talkWantIP(); ip == "198.51.100.7" {
		t.Error("the code still points at a card that's gone")
	}
	talk.Lock()
	talk.localIP = ""
	talk.Unlock()

	// The tunnel's address works from anywhere: nothing to choose.
	cfMu.Lock()
	cfURL = "https://example.trycloudflare.com"
	cfMu.Unlock()
	_, s = ts.getJSON("/talk/status", auth)
	if l, _ := s["addresses"].([]any); len(l) != 0 {
		t.Errorf("offered a choice with the tunnel running: %v", l)
	}
}

func TestTalkCardsLeaveOutVirtualAdapters(t *testing.T) {
	for name, virtual := range map[string]bool{
		"Wi-Fi": false, "Ethernet": false, "Ethernet 2": false, "en0": false, "eth0": false, "wlan0": false,
		"Local Area Connection": false, "Network Bridge": false,
		"vEthernet (WSL (Hyper-V firewall))": true, "VirtualBox Host-Only Network": true, "VMware Network Adapter VMnet8": true,
		"Tailscale": true, "ZeroTier One": true, "OpenVPN TAP-Windows6": true, "Bluetooth Network Connection": true,
		"docker0": true, "tun0": true, "wg0": true, "utun3": true, "br-1a2b": true, "vboxnet0": true,
	} {
		if talkCardIsVirtual(name, nil) != virtual {
			t.Errorf("%q: virtual should be %v", name, virtual)
		}
	}
	// Windows calls VirtualBox's adapter "Ethernet 3": only its hardware address gives it away.
	for mac, virtual := range map[string]bool{
		"0a:00:27:00:00:04": true, "08:00:27:12:34:56": true, "00:15:5d:26:5e:6e": true, "00:50:56:c0:00:08": true, "00:ff:4f:51:e5:88": true,
		"a8:a1:59:8d:87:ed": false, "14:18:c3:3f:8f:36": false,
	} {
		m, err := net.ParseMAC(mac)
		if err != nil {
			t.Fatal(err)
		}
		if talkCardIsVirtual("Ethernet 3", m) != virtual {
			t.Errorf("%s: virtual should be %v", mac, virtual)
		}
	}
}

// Still polling, but no audio: a dead mic, so viewers aren't told someone's commentating. A
// mute is different, from either end: the strap stays.
func TestTalkStrapForADeadMicButNotAMute(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	savedGone := talkMicGone
	t.Cleanup(func() { talkMicGone = savedGone })
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	_, j := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Sam"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": j["id"]}, auth)
	poll := func(muted bool) {
		t.Helper()
		phonePost(t, base, "/talk/poll", M{"id": j["id"], "secret": j["secret"], "muted": muted})
	}
	strap := func() bool { return TalkOnAir()["on_air"] == true }
	if !strap() {
		t.Fatal("no strap in the moment before the first audio arrives")
	}

	talkMicGone = 10 * time.Millisecond
	time.Sleep(30 * time.Millisecond)
	poll(false)
	if strap() {
		t.Error("the strap stayed up for a phone polling with a dead mic")
	}
	if _, s := ts.getJSON("/talk/status", auth); s["on_air"] == nil {
		t.Error("a dead mic took them off the screen: they're still on air")
	}
	// Muted on the phone: no audio is what's meant to happen.
	poll(true)
	if !strap() {
		t.Error("the strap came down for a mute on the phone")
	}
	_, s := ts.getJSON("/talk/status", auth)
	if air, _ := s["on_air"].(M); air == nil || air["phone_muted"] != true {
		t.Errorf("the screen wasn't told it's a mute: %v", s["on_air"])
	}
	poll(false)
	if strap() {
		t.Error("unmuted with no audio: that's a dead mic again")
	}
	// Muted from the screen.
	ts.postJSON("/talk/control", M{"action": "mute", "id": j["id"], "muted": true}, auth)
	if !strap() {
		t.Error("the strap came down for a mute from the screen")
	}
	ts.postJSON("/talk/control", M{"action": "mute", "id": j["id"], "muted": false}, auth)
	// Audio's back: so is the strap.
	talkMicGone = time.Minute
	phoneAudio(t, base, j["id"], j["secret"], 0, []byte{1, 0})
	if !strap() {
		t.Error("the strap didn't come back with the audio")
	}
	// Audio coming means it isn't muted, whatever it last said.
	poll(true)
	phoneAudio(t, base, j["id"], j["secret"], 1, []byte{1, 0})
	if _, s := ts.getJSON("/talk/status", auth); s["on_air"].(M)["phone_muted"] != false {
		t.Error("sending audio but still marked muted")
	}
}

// A phone that's out of signal and then finished by the operator must still hear why when it
// gets back. Forgotten too soon, it's told commentary is off, with no way to ask again.
func TestTalkFinishedPhoneHearsWhy(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	finish := map[string]func(id any){
		"clubhouse": func(id any) { ts.postJSON("/talk/control", M{"action": "end", "id": id}, auth) },
		"handover": func(id any) {
			_, n := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Jo"})
			ts.postJSON("/talk/control", M{"action": "approve", "id": n["id"]}, auth)
		},
	}
	for why, do := range finish {
		_, j := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Sam"})
		ts.postJSON("/talk/control", M{"action": "approve", "id": j["id"]}, auth)
		// Out of signal for longer than a finished phone's record is kept, though not so long
		// it would have lost its place.
		talk.Lock()
		talk.phones[fmt.Sprint(j["id"])].seen = time.Now().Add(-talkForgetFor - 10*time.Second)
		talk.Unlock()
		do(j["id"])
		talkTick()
		_, p := phonePost(t, base, "/talk/poll", M{"id": j["id"], "secret": j["secret"]})
		if p["state"] != "ended" || p["why"] != why {
			t.Errorf("%s: told %v", why, p)
		}
		ts.postJSON("/talk/control", M{"action": "off"}, auth)
		ts.postJSON("/talk/control", M{"action": "on"}, auth)
		base = talkPhoneBase(t)
	}
}

// A flat battery leaves the session behind just as a crash does. The file stays fresh while
// commentary's on, so a restart can tell seconds from hours, and only seconds pick it up.
func TestTalkSessionIsOnlyForAQuickRestart(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	if talkSessionFor > 10*time.Minute {
		t.Errorf("picks up a session %v old: long enough to be back on air after a flat battery", talkSessionFor)
	}
	savedEvery := talkSaveEvery
	t.Cleanup(func() { talkSaveEvery = savedEvery })
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	at := func() time.Time {
		t.Helper()
		b, err := os.ReadFile(talkSessionFile())
		if err != nil {
			t.Fatal(err)
		}
		var s talkSession
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		return s.At
	}
	before := at()
	talkSaveEvery = time.Hour
	time.Sleep(5 * time.Millisecond)
	talkTick()
	if !at().Equal(before) {
		t.Error("saved on every tick")
	}
	// Nothing's changed for a long time, and it still says how recently it was running.
	talkSaveEvery = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	talkTick()
	if !at().After(before) {
		t.Error("the session's time wasn't kept fresh, so a restart can't tell how long it's been")
	}
}

// OBS refusing one read isn't OBS saying there's nothing there: taking it for that added a
// second Commentary to the scene, saved in the club's collection for good.
func TestTalkDoesntAddASecondSourceOnAFailedRead(t *testing.T) {
	talkSandbox(t, "pw")
	f := newTalkFakeOBS("Main", "Replay")
	f.inputs["Cricket Camera"] = M{}
	f.filters["Cricket Camera"] = []M{{"filterName": talkDuckFilter}}
	f.inputs[TalkSource] = M{"url": fmt.Sprintf("http://127.0.0.1:%d/talk/out", Port), "reroute_audio": true}
	f.items["Main"] = []string{TalkSource}
	f.items["Replay"] = []string{TalkSource}
	refuse := ""
	obsCall = func(st M, reqs []obsws.Req, d time.Duration) []map[string]any {
		out := f.call(st, reqs, d)
		for i, r := range reqs {
			if r.Type == refuse {
				out[i] = nil
			}
		}
		return out
	}
	for _, read := range []string{"GetSceneItemList", "GetSourceFilterList"} {
		refuse, f.writes = read, nil
		talkEnsureOBS()
		if len(f.writes) != 0 {
			t.Errorf("%s refused: changed OBS anyway: %v", read, f.writes)
		}
	}
	// The connection dropping part way: nothing's known, so nothing's changed.
	refuse, f.writes = "", nil
	n := 0
	obsCall = func(st M, reqs []obsws.Req, d time.Duration) []map[string]any {
		if n++; n > 1 {
			return nil
		}
		return f.call(st, reqs, d)
	}
	if ok, _ := talkEnsureOBS(); ok {
		t.Error("ready, with half the reads missing")
	}
	if len(f.writes) != 0 {
		t.Errorf("a dropped connection changed OBS: %v", f.writes)
	}
}

// Every obsCall is a new connection, on the laptop that's encoding the stream, every 30s.
func TestTalkHealthyPassIsFewConnections(t *testing.T) {
	talkSandbox(t, "pw")
	f := newTalkFakeOBS("Main", "Main-Bowler", "Replay")
	f.inputs["Cricket Camera"] = M{}
	f.inputs["Bowler End Camera"] = M{}
	obsCall = f.call
	if ok, note := talkEnsureOBS(); !ok {
		t.Fatalf("setting up: %s", note)
	}
	n := 0
	obsCall = func(st M, reqs []obsws.Req, d time.Duration) []map[string]any {
		n++
		return f.call(st, reqs, d)
	}
	if ok, note := talkEnsureOBS(); !ok {
		t.Fatalf("second pass: %s", note)
	}
	if n > 2 {
		t.Errorf("%d connections to OBS on a pass with nothing to do", n)
	}
}

// A fake OBS: remembers what it's told.
type talkFakeOBS struct {
	mu      sync.Mutex
	scenes  []string
	inputs  map[string]M
	items   map[string][]string
	filters map[string][]M
	writes  []string
	index   map[string]int
}

func newTalkFakeOBS(scenes ...string) *talkFakeOBS {
	f := &talkFakeOBS{scenes: scenes, inputs: map[string]M{}, items: map[string][]string{}, filters: map[string][]M{}, index: map[string]int{}}
	return f
}

func (f *talkFakeOBS) call(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, r := range reqs {
		d, _ := r.Data.(M)
		switch r.Type {
		case "GetSceneList":
			var l []any
			for _, s := range f.scenes {
				l = append(l, M{"sceneName": s})
			}
			out = append(out, M{"scenes": l})
		case "GetInputList":
			var l []any
			for n := range f.inputs {
				l = append(l, M{"inputName": n})
			}
			out = append(out, M{"inputs": l})
		case "GetInputSettings":
			out = append(out, M{"inputSettings": f.inputs[gs(d, "inputName", "")]})
		case "SetInputSettings":
			f.writes = append(f.writes, r.Type)
			f.inputs[gs(d, "inputName", "")] = d["inputSettings"].(M)
			out = append(out, M{})
		case "GetSceneItemList":
			var l []any
			for i, n := range f.items[gs(d, "sceneName", "")] {
				l = append(l, M{"sourceName": n, "sceneItemId": i + 1})
			}
			out = append(out, M{"sceneItems": l})
		case "CreateInput":
			f.writes = append(f.writes, r.Type)
			f.inputs[gs(d, "inputName", "")] = d["inputSettings"].(M)
			sc := gs(d, "sceneName", "")
			f.items[sc] = append(f.items[sc], gs(d, "inputName", ""))
			out = append(out, M{"sceneItemId": len(f.items[sc])})
		case "CreateSceneItem":
			f.writes = append(f.writes, r.Type)
			sc := gs(d, "sceneName", "")
			f.items[sc] = append(f.items[sc], gs(d, "sourceName", ""))
			out = append(out, M{"sceneItemId": len(f.items[sc])})
		case "SetSceneItemIndex":
			f.writes = append(f.writes, r.Type)
			f.index[gs(d, "sceneName", "")] = gi(d, "sceneItemIndex", -1)
			out = append(out, M{})
		case "GetSourceFilterList":
			var l []any
			for _, x := range f.filters[gs(d, "sourceName", "")] {
				l = append(l, x)
			}
			out = append(out, M{"filters": l})
		case "CreateSourceFilter":
			f.writes = append(f.writes, r.Type)
			s := gs(d, "sourceName", "")
			f.filters[s] = append(f.filters[s], d)
			out = append(out, M{})
		case "PressInputPropertiesButton":
			f.writes = append(f.writes, r.Type)
			out = append(out, M{})
		default:
			out = append(out, nil)
		}
	}
	return out
}

func TestTalkMakesTheOBSSourceInEveryScene(t *testing.T) {
	talkSandbox(t, "pw")
	f := newTalkFakeOBS("Main", "Main-Bowler", "Replay", "Starting soon")
	f.inputs["Cricket Camera"] = M{}
	f.inputs["Bowler End Camera"] = M{}
	f.inputs["Overlay"] = M{}
	f.items["Main"] = []string{"Cricket Camera", "Overlay"}
	obsCall = f.call

	ok, note := talkEnsureOBS()
	if !ok {
		t.Fatalf("not ready: %s", note)
	}
	src := f.inputs[TalkSource]
	if src == nil {
		t.Fatal("no source made")
	}
	if src["reroute_audio"] != true || src["shutdown"] != false || !strings.HasSuffix(gs(src, "url", ""), "/talk/out") ||
		!strings.Contains(gs(src, "url", ""), "127.0.0.1") {
		t.Errorf("source settings: %v", src)
	}
	for _, sc := range []string{"Main", "Main-Bowler", "Replay"} {
		has := false
		for _, n := range f.items[sc] {
			has = has || n == TalkSource
		}
		if !has {
			t.Errorf("not in %s: a source missing from the live scene falls silent", sc)
		}
		if f.index[sc] != 0 {
			t.Errorf("%s: left where OBS put it", sc)
		}
	}
	for _, n := range f.items["Starting soon"] {
		if n == TalkSource {
			t.Error("added to a scene that isn't the match's")
		}
	}
	for _, cam := range []string{"Cricket Camera", "Bowler End Camera"} {
		if len(f.filters[cam]) != 1 {
			t.Fatalf("%s filters: %v", cam, f.filters[cam])
		}
		fl := f.filters[cam][0]
		st, _ := fl["filterSettings"].(M)
		if fl["filterKind"] != "compressor_filter" || st["sidechain_source"] != TalkSource {
			t.Errorf("%s filter: %v", cam, fl)
		}
	}

	// Again and again, as it runs: nothing more changes.
	f.writes = nil
	if ok, _ := talkEnsureOBS(); !ok {
		t.Fatal("second run")
	}
	if len(f.writes) != 0 {
		t.Errorf("second run changed OBS: %v", f.writes)
	}

	// A port change (a new server) moves the page.
	f.inputs[TalkSource] = M{"url": "http://127.0.0.1:1/talk/out", "reroute_audio": true}
	talkEnsureOBS()
	if !strings.HasSuffix(gs(f.inputs[TalkSource], "url", ""), fmt.Sprintf(":%d/talk/out", Port)) {
		t.Errorf("url not corrected: %v", f.inputs[TalkSource])
	}
}

// Opening OBS before the server is match day's order: the source's first load is an error
// page that never retries, so a page that hasn't connected gets a reload. Limited, and
// never while one is connected.
func TestTalkReloadsAPageThatNeverConnected(t *testing.T) {
	ts := talkSandbox(t, "pw")
	saved := talkReloadWait
	t.Cleanup(func() { talkReloadWait = saved })
	talkReloadWait = 0
	f := newTalkFakeOBS("Main")
	f.inputs[TalkSource] = M{"url": fmt.Sprintf("http://127.0.0.1:%d/talk/out", Port), "reroute_audio": true}
	f.items["Main"] = []string{TalkSource}
	obsCall = f.call
	talk.Lock()
	talk.on, talk.onAt, talk.reloads, talk.reloadAt = true, time.Now().Add(-time.Hour), 0, time.Time{}
	talk.Unlock()
	presses := func() int {
		n := 0
		for _, w := range f.writes {
			if w == "PressInputPropertiesButton" {
				n++
			}
		}
		return n
	}
	for i := 0; i < 5; i++ {
		talkEnsureOBS()
	}
	if presses() != 3 {
		t.Errorf("%d reloads, want 3", presses())
	}
	// Connected: the count resets, and no more presses.
	listenOBS(t, ts)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		talk.Lock()
		n := len(talk.outs)
		talk.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.writes = nil
	talkEnsureOBS()
	if presses() != 0 {
		t.Errorf("reloaded a page that's connected")
	}
}

func TestTalkSaysWhyOBSIsntReady(t *testing.T) {
	talkSandbox(t, "pw")
	if ok, note := talkEnsureOBS(); ok || !strings.Contains(note, "isn't open") {
		t.Errorf("OBS closed: %v %q", ok, note)
	}
	obsCall = newTalkFakeOBS().call
	if ok, note := talkEnsureOBS(); ok || !strings.Contains(note, "no match scenes") {
		t.Errorf("no scenes: %v %q", ok, note)
	}
	setState(t, M{"obs_host": "192.0.2.4"})
	if ok, note := talkEnsureOBS(); ok || !strings.Contains(note, "another computer") {
		t.Errorf("OBS elsewhere: %v %q", ok, note)
	}
}

// ── The pages ──

func talkAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// What the microphone sounds like on the way through: a tone in at the phone's rate, the
// same tone out at 24 kHz, the same length and level.
func TestTalkResamplerKeepsTheTone(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	test := talkAsset(t, "talk_capture.js") + `
var freq = function (s, rate) { // zero crossings
  var c = 0; for (var i = 1; i < s.length; i++) if ((s[i-1] < 0) !== (s[i] < 0)) c++;
  return c / 2 / (s.length / rate);
};
var rms = function (s) { var t = 0; for (var i = 0; i < s.length; i++) t += s[i]*s[i]; return Math.sqrt(t / s.length); };
[48000, 44100, 96000, 16000, 24000].forEach(function (rate) {
  var r = new TalkResampler(rate, TALK_RATE), out = [];
  var total = rate * 4, block = 128, t = 0;
  while (t < total) {
    var b = new Float32Array(block);
    for (var i = 0; i < block; i++) b[i] = 0.5 * Math.sin(2 * Math.PI * 1000 * (t + i) / rate);
    r.push(b, out); t += block;
  }
  var want = t * TALK_RATE / rate;
  if (Math.abs(out.length - want) > 2) throw new Error(rate + ': ' + out.length + ' samples, want ' + want + ' (drift)');
  var f = freq(out, TALK_RATE);
  if (Math.abs(f - 1000) > 3) throw new Error(rate + ': tone came out at ' + f + ' Hz');
  var level = rms(out), want2 = 0.5 / Math.SQRT2;
  if (Math.abs(level - want2) > 0.05) throw new Error(rate + ': level ' + level + ' want ' + want2);
});
// A tone above what 24 kHz can carry is reduced, not folded back in as a loud whistle.
var r = new TalkResampler(48000, TALK_RATE), out = [], b = new Float32Array(48000);
for (var i = 0; i < b.length; i++) b[i] = 0.5 * Math.sin(2 * Math.PI * 20000 * i / 48000);
r.push(b, out);
if (rms(out) > 0.15) throw new Error('20 kHz folded back at level ' + rms(out));
var pcm = talkToPCM([1.5, -1.5, 0, 1, -1]);
if (pcm[0] !== 32767 || pcm[1] !== -32768 || pcm[2] !== 0 || pcm[3] !== 32767 || pcm[4] !== -32768) throw new Error('clip: ' + pcm);
`
	cmd := exec.Command(bin, "-e", test)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Not a sample leaves the phone until it's on air, or while it's muted.
func TestTalkPageSendsNothingUntilOnAir(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := talkAsset(t, "talk.html")
	m := regexp.MustCompile(`(?s)function onChunk\(d\) \{.*?\n\}\n`).FindString(page)
	if m == "" {
		t.Fatal("no onChunk in talk.html")
	}
	test := `
var state = '', muted = false, queue = [], MOST_BEHIND = 3, pumped = 0;
var lastChunk = 0, lastSound = 0, lastReply = Date.now(), micDown = false, autoTries = 0, NO_CONTACT = 3000;
function drawAir() {}
var els = {};
function $(id) { return els[id] || (els[id] = {style: {}}); }
function pump() { pumped++; }
` + m + `
var d = {pcm: new ArrayBuffer(8), peak: 0.25};
['', 'waiting', 'refused', 'ended'].forEach(function (s) {
  state = s; onChunk(d);
  if (queue.length || pumped) throw new Error('queued in state ' + JSON.stringify(s));
});
if (!$('w_meter').style.width) throw new Error('the waiting meter should still move');
state = 'on_air'; muted = true; onChunk(d);
if (queue.length || pumped) throw new Error('queued while muted');
muted = false; onChunk(d);
if (queue.length !== 1 || pumped !== 1) throw new Error('not sent on air');
for (var i = 0; i < 10; i++) onChunk({pcm: new ArrayBuffer(8), peak: 0});
if (queue.length > MOST_BEHIND) throw new Error('fell behind without limit: ' + queue.length);
`
	cmd := exec.Command(bin, "-e", test)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestTalkPageElementsExist(t *testing.T) {
	page := talkAsset(t, "talk.html")
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		ids[m[1]] = true
	}
	used := regexp.MustCompile(`\$\('([\w-]+)'\)`).FindAllStringSubmatch(page, -1)
	if len(used) < 10 {
		t.Fatalf("only %d lookups found", len(used))
	}
	for _, m := range used {
		if !ids[m[1]] {
			t.Errorf("the script looks up #%s, which isn't on the page", m[1])
		}
	}
	// What someone's told before they speak on a public stream.
	for _, want := range []string{"Everyone watching can hear", "Nothing is sent until the clubhouse says yes"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page no longer says %q", want)
		}
	}
	if strings.Contains(page, "innerHTML") {
		t.Error("innerHTML on a page handling someone's typed name")
	}
}

// The overlay: same rule as its other timers (a preview polls nothing), and a name that's
// someone's own typing goes in as text.
func TestTalkStrapOnTheOverlay(t *testing.T) {
	overlay := talkAsset(t, "overlay.html")
	if !regexp.MustCompile(`if \(new URLSearchParams\(location\.search\)\.get\('preview'\) !== '1'\) \{\s*\n\s*setInterval\(pollTalkStrap`).MatchString(overlay) {
		t.Fatal("pollTalkStrap is no longer gated on preview mode")
	}
	fn := regexp.MustCompile(`(?s)async function pollTalkStrap\(\) \{.*?\n\}`).FindString(overlay)
	if fn == "" || !strings.Contains(fn, "textContent = d.name") || strings.Contains(fn, "innerHTML") {
		t.Fatalf("strap: %q", fn)
	}
	if strings.Contains(fn, "'/live'") || strings.Contains(fn, "/commands") {
		t.Fatal("the strap must read /talk/onair, nothing with side effects")
	}
}

// Volume and mute, from the screen and the phone: what reaches OBS's page is what they say.
func TestTalkVolumeAndMute(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	ch, _ := listenOBS(t, ts)
	_, j := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Sam"})
	id, secret := j["id"], j["secret"]
	// Before they're on air there's nothing to turn up.
	if st, _ := ts.postJSON("/talk/control", M{"action": "volume", "id": id, "step": 1}, auth); st != 409 {
		t.Errorf("volume while waiting: %d", st)
	}
	if _, l := phonePost(t, base, "/talk/level", M{"id": id, "secret": secret, "step": 1}); l["gain_db"] != float64(0) {
		t.Errorf("a waiting phone moved its volume: %v", l)
	}
	ts.postJSON("/talk/control", M{"action": "approve", "id": id}, auth)
	seq := 0
	sample := func() int16 {
		t.Helper()
		pcm := make([]byte, 2)
		binary.LittleEndian.PutUint16(pcm, uint16(int16(8000)))
		phoneAudio(t, base, id, secret, seq, pcm)
		seq++
		return int16(binary.LittleEndian.Uint16(heard(t, ch)))
	}
	if v := sample(); v != 8000 {
		t.Fatalf("at normal: %d", v)
	}
	// Two presses down on the screen: 6 dB is half.
	for i := 0; i < 2; i++ {
		if st, _ := ts.postJSON("/talk/control", M{"action": "volume", "id": id, "step": -1}, auth); st != 200 {
			t.Fatalf("quieter: %d", st)
		}
	}
	if v := sample(); v < 3950 || v > 4050 {
		t.Errorf("at -6 dB: %d", v)
	}
	// And the phone sees it, and can move it itself.
	_, l := phonePost(t, base, "/talk/level", M{"id": id, "secret": secret, "step": 1})
	if l["gain_db"] != float64(-3) {
		t.Errorf("phone's step: %v", l)
	}
	if _, l := phonePost(t, base, "/talk/level", M{"id": id, "secret": "wrong", "step": 12}); l["state"] != "ended" {
		t.Errorf("wrong secret: %v", l)
	}
	// It stops at each end, however many presses or however big a step is asked for.
	for i := 0; i < 30; i++ {
		phonePost(t, base, "/talk/level", M{"id": id, "secret": secret, "step": 99})
	}
	_, s := ts.getJSON("/talk/status", auth)
	air := s["on_air"].(M)
	if air["gain_db"] != float64(talkGainMax) || air["can_louder"] != false {
		t.Errorf("top end: %v", air)
	}
	// Past full scale it clips, it doesn't wrap round into noise.
	if v := sample(); v != 32767 && v < 31000 {
		t.Errorf("at the top: %d", v)
	}
	for i := 0; i < 30; i++ {
		ts.postJSON("/talk/control", M{"action": "volume", "id": id, "step": -1}, auth)
	}
	_, s = ts.getJSON("/talk/status", auth)
	if air = s["on_air"].(M); air["gain_db"] != float64(talkGainMin) || air["can_quieter"] != false {
		t.Errorf("bottom end: %v", air)
	}

	// Muted from the screen: nothing reaches OBS (so the ground comes back), the meter still
	// moves, the phone's told, and the phone can't undo it.
	ts.postJSON("/talk/control", M{"action": "mute", "id": id, "muted": true}, auth)
	pcm := make([]byte, 2)
	binary.LittleEndian.PutUint16(pcm, 30000)
	if _, a := phoneAudio(t, base, id, secret, seq, pcm); a["clubhouse_muted"] != true || a["state"] != "on_air" {
		t.Errorf("phone not told: %v", a)
	}
	seq++
	nothingHeard(t, ch)
	_, s = ts.getJSON("/talk/status", auth)
	if air = s["on_air"].(M); air["muted"] != true || air["hearing"] != true {
		t.Errorf("screen while muted: %v", air)
	}
	if _, l := phonePost(t, base, "/talk/level", M{"id": id, "secret": secret, "step": 1, "muted": false}); l["clubhouse_muted"] != true {
		t.Errorf("the phone undid the clubhouse's mute: %v", l)
	}
	ts.postJSON("/talk/control", M{"action": "mute", "id": id, "muted": false}, auth)
	if v := sample(); v == 0 {
		t.Errorf("unmuted, silent: %d", v)
	}

	// The next person starts as themselves.
	ts.postJSON("/talk/control", M{"action": "mute", "id": id, "muted": true}, auth)
	_, k := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Jo"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": k["id"]}, auth)
	_, s = ts.getJSON("/talk/status", auth)
	if air = s["on_air"].(M); air["name"] != "Jo" || air["gain_db"] != float64(0) || air["muted"] != false {
		t.Errorf("next commentator inherited: %v", air)
	}
}

func TestTalkGain(t *testing.T) {
	pcm := make([]byte, 4)
	binary.LittleEndian.PutUint16(pcm, uint16(int16(30000)))
	v := int16(-30000)
	binary.LittleEndian.PutUint16(pcm[2:], uint16(v))
	if &talkGain(pcm, 0)[0] != &pcm[0] {
		t.Error("normal volume should leave the samples alone")
	}
	up := talkGain(pcm, 12)
	if int16(binary.LittleEndian.Uint16(up)) != 32767 || int16(binary.LittleEndian.Uint16(up[2:])) != -32768 {
		t.Errorf("clipping: %v", up)
	}
	if int16(binary.LittleEndian.Uint16(pcm)) != 30000 {
		t.Error("changed the samples it was given")
	}
}

// A phone that goes quiet comes off the strap at once, keeps its place, and carries on by
// itself when it's back. Only much later is it off air.
func TestTalkPhoneKeepsItsPlaceThroughABlip(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	savedGone, savedLost, savedWait := talkGoneFor, talkLostFor, talkWaitGone
	t.Cleanup(func() { talkGoneFor, talkLostFor, talkWaitGone = savedGone, savedLost, savedWait })
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	ch, _ := listenOBS(t, ts)
	_, j := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Sam"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": j["id"]}, auth)
	phoneAudio(t, base, j["id"], j["secret"], 0, []byte{1, 0})
	heard(t, ch)

	talkGoneFor = 10 * time.Millisecond
	time.Sleep(30 * time.Millisecond)
	talkTick()
	if a := TalkOnAir(); a["on_air"] != false {
		t.Errorf("the strap stayed up for a phone that's gone quiet: %v", a)
	}
	_, s := ts.getJSON("/talk/status", auth)
	if air, _ := s["on_air"].(M); air == nil || air["lost"] != true {
		t.Errorf("the screen should still show them, lost: %v", s["on_air"])
	}
	// Back: no yes needed, and they're heard, strap and all.
	if _, a := phoneAudio(t, base, j["id"], j["secret"], 1, []byte{2, 0}); a["state"] != "on_air" {
		t.Fatalf("back: %v", a)
	}
	if got := heard(t, ch); got[0] != 2 {
		t.Errorf("back: heard %v", got)
	}
	talkGoneFor = time.Minute
	if a := TalkOnAir(); a["on_air"] != true {
		t.Errorf("the strap didn't come back: %v", a)
	}
	// Gone for good: off air, and the phone's told why.
	talkLostFor = time.Millisecond
	time.Sleep(10 * time.Millisecond)
	talkTick()
	talkLostFor = time.Hour
	if _, p := phonePost(t, base, "/talk/poll", M{"id": j["id"], "secret": j["secret"]}); p["state"] != "ended" || p["why"] != "lost" {
		t.Errorf("after the long wait: %v", p)
	}
	// Asking, and gone: taken off the screen's list.
	_, w := phonePost(t, base, "/talk/join", M{"k": talkKey(), "name": "Jo"})
	talkWaitGone = time.Millisecond
	time.Sleep(10 * time.Millisecond)
	talkTick()
	talkWaitGone = time.Hour
	if a := TalkOnAir(); a["waiting"] != 0 {
		t.Errorf("a phone that stopped asking is still waiting: %v (%v)", a, w)
	}
}

// A reloaded page starts counting from 0 again: without this, silent for the rest of the
// time they're on.
func TestTalkReloadedPageIsHeard(t *testing.T) {
	sandbox(t, "pw")
	o := &talkOut{send: make(chan []byte, 64)}
	talk.Lock()
	defer func() {
		delete(talk.outs, o)
		talk.Unlock()
	}()
	talk.outs[o] = true
	p := &talkPhone{held: map[int64][]byte{}}
	for s := int64(500); s < 505; s++ {
		p.takeChunk(s, []byte{1, 0})
	}
	for len(o.send) > 0 {
		<-o.send
	}
	p.takeChunk(0, []byte{9, 0})
	if len(o.send) != 1 || (<-o.send)[0] != 9 {
		t.Fatal("a page counting from 0 again wasn't heard")
	}
	// A gap left over from before a pause doesn't hold them up, or play old audio.
	p = &talkPhone{held: map[int64][]byte{}}
	p.takeChunk(0, []byte{1, 0})
	<-o.send
	p.takeChunk(2, []byte{2, 0}) // 1 never comes
	p.heldAt = time.Now().Add(-2 * time.Second)
	p.takeChunk(50, []byte{3, 0})
	if len(o.send) != 1 || (<-o.send)[0] != 3 {
		t.Fatal("old audio played, or the new audio waited")
	}
}

// Killed mid-match and restarted by the launcher: the same code still works, and whoever was
// on air is still on, with their volume. A clean stop leaves nothing to pick up.
func TestTalkPicksUpAfterACrash(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	base := talkPhoneBase(t)
	key := talkKey()
	_, j := phonePost(t, base, "/talk/join", M{"k": key, "name": "Sam"})
	ts.postJSON("/talk/control", M{"action": "approve", "id": j["id"]}, auth)
	ts.postJSON("/talk/control", M{"action": "volume", "id": j["id"], "step": 1}, auth)
	b, err := os.ReadFile(talkSessionFile())
	if err != nil {
		t.Fatal(err)
	}

	// The crash: memory gone, the file left behind.
	TalkOff()
	if _, err := os.Stat(talkSessionFile()); err == nil {
		t.Fatal("a clean stop left the session behind")
	}
	TalkResume()
	if talk.on {
		t.Fatal("picked up with no session: commentary must start off")
	}
	os.WriteFile(talkSessionFile(), b, 0o600)
	TalkResume()
	if talkKey() != key {
		t.Fatal("a new key: the code on the wall stopped working")
	}
	base = talkPhoneBase(t)
	if a := TalkOnAir(); a["on_air"] != true || a["name"] != "Sam" {
		t.Fatalf("not back on air: %v", a)
	}
	_, p := phonePost(t, base, "/talk/poll", M{"id": j["id"], "secret": j["secret"]})
	if p["state"] != "on_air" || p["gain_db"] != float64(talkGainStep) {
		t.Fatalf("their phone: %v", p)
	}
	if _, q := phonePost(t, base, "/talk/poll", M{"id": j["id"], "secret": "wrong"}); q["state"] != "ended" {
		t.Errorf("the secret wasn't kept: %v", q)
	}

	// Yesterday's leftovers, or a corrupt file, are not picked up, and are cleared away.
	TalkOff()
	var old talkSession
	json.Unmarshal(b, &old)
	old.At = time.Now().Add(-talkSessionFor - time.Hour)
	stale, _ := json.Marshal(old)
	for _, content := range [][]byte{stale, []byte("{not json")} {
		os.WriteFile(talkSessionFile(), content, 0o600)
		TalkResume()
		if talk.on {
			t.Fatalf("picked up from %q", content)
		}
		if _, err := os.Stat(talkSessionFile()); err == nil {
			t.Errorf("left %q behind", content)
		}
	}
}

// A laptop's route to the internet is only a guess at the network phones are on: the card the
// screen reached it on is better, and anything else on that header is ignored.
func TestTalkUsesTheScreensNetworkCard(t *testing.T) {
	ts := talkSandbox(t, "pw")
	auth := ts.bearer("pw")
	var card string
	for _, ip := range talkLocalIPs() {
		if !ip.IsLoopback() {
			card = ip.String()
			break
		}
	}
	if card == "" {
		t.Skip("no network card")
	}
	ts.postJSON("/talk/control", M{"action": "on"}, auth)
	t.Cleanup(func() {
		talk.Lock()
		talk.localIP = ""
		talk.Unlock()
	})
	h := func(ip string) map[string]string {
		return map[string]string{"Authorization": auth["Authorization"], talkLocalIPHeader: ip}
	}
	_, s := ts.getJSON("/talk/status", h(card))
	if !strings.Contains(fmt.Sprint(s["url"]), "https://"+card+":") {
		t.Errorf("not the screen's card: %v", s["url"])
	}
	for _, bad := range []string{"203.0.113.9", "127.0.0.1", "not an address"} {
		talk.Lock()
		talk.localIP = ""
		talk.Unlock()
		_, s = ts.getJSON("/talk/status", h(bad))
		if !strings.Contains(fmt.Sprint(s["url"]), "https://127.0.0.1:") { // the test's own fallback
			t.Errorf("%q: %v", bad, s["url"])
		}
	}
}

func TestTalkTriesThePortAgain(t *testing.T) {
	talkSandbox(t, "pw")
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv(talkPortEnv, fmt.Sprint(port))
	TalkOn()
	if s := TalkStatus(); !strings.Contains(fmt.Sprint(s["error"]), "in use") {
		t.Fatalf("no warning: %v", s)
	}
	ln.Close()
	talkTick()
	s := TalkStatus()
	if s["error"] != "" || !strings.Contains(fmt.Sprint(s["url"]), fmt.Sprint(":", port, "/")) {
		t.Fatalf("didn't recover: %v", s)
	}
}

func TestTalkSaysWhyNobodyCanHear(t *testing.T) {
	talkSandbox(t, "pw")
	f := newTalkFakeOBS("Main", "Replay", "Starting soon")
	base := f.call
	current, mute := "Starting soon", false
	obsCall = func(st M, reqs []obsws.Req, d time.Duration) []map[string]any {
		out := base(st, reqs, d)
		for i, r := range reqs {
			if r.Type == "GetSceneList" {
				out[i]["currentProgramSceneName"] = current
			}
			if r.Type == "GetInputMute" {
				out[i] = M{"inputMuted": mute}
			}
		}
		return out
	}
	if ok, note := talkEnsureOBS(); ok || !strings.Contains(note, "Starting soon") {
		t.Errorf("on another scene: %v %q", ok, note)
	}
	current, mute = "Main", true
	if ok, note := talkEnsureOBS(); ok || !strings.Contains(note, "muted in OBS") {
		t.Errorf("muted in OBS: %v %q", ok, note)
	}
	mute = false
	if ok, note := talkEnsureOBS(); !ok {
		t.Errorf("all's well: %q", note)
	}
}

// The phone must always find out what's happened to it, whatever's stopped the audio.
func TestTalkPagePollsOnceTheAnswersStop(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := talkAsset(t, "talk.html")
	fn := regexp.MustCompile(`(?s)function pollLoop\(\) \{.*?\n\}\n`).FindString(page)
	if fn == "" {
		t.Fatal("no pollLoop")
	}
	test := `
var me = {id: 'a', secret: 's'}, state = 'on_air', muted = false, micDown = false, lastReply = Date.now();
var pollTimer = 0, polls = 0;
function setTimeout() { return 0; } function clearTimeout() {}
function post() { polls++; return Promise.resolve({}); } function onReply() {}
` + fn + `
pollLoop(); if (polls) throw new Error('polled with the audio answering');
lastReply = Date.now() - 5000; pollLoop(); if (polls !== 1) throw new Error('silent mic or no wifi: never polled');
lastReply = Date.now(); muted = true; pollLoop(); if (polls !== 2) throw new Error('muted: never polled');
muted = false; micDown = true; pollLoop(); if (polls !== 3) throw new Error('mic down: never polled');
`
	if out, err := exec.Command(bin, "-e", test).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// An iPhone only starts audio from the tap itself: the context comes before any waiting.
	start := regexp.MustCompile(`(?s)function startMic\(\) \{.*?\n\}\n`).FindString(page)
	if a, b := strings.Index(start, "new AC("), strings.Index(start, "getUserMedia({"); a < 0 || b < 0 || a > b {
		t.Error("the AudioContext must be made before waiting on the microphone")
	}
	if strings.Contains(page, "sendBeacon") {
		t.Error("a reload mustn't take them off air")
	}
}

// A stand-in for what the phone's page needs to start a microphone, for the two tests below.
const talkPageFakes = `
var ctx = null, stream = null, node = null, lock = null, wantMic = false, micDown = false, recovering = false;
var autoTries = 0, queue = [], pollTimer = 0, lastChunk = 0, micTurn = 0, MIC_STOPPED = {};
var clearTimeout = function () {};
var closed = 0, stopped = 0, nodes = 0, nodeThrows = false, grants = [], micsAsked = 0;
function AC() { this.audioWorklet = {addModule: function () { return Promise.resolve(); }}; this.destination = {}; }
AC.prototype.audioWorklet = true;
AC.prototype.resume = function () { return Promise.resolve(); };
AC.prototype.close = function () { closed++; return Promise.resolve(); };
AC.prototype.createGain = function () { return {gain: {}, connect: function () {}}; };
AC.prototype.createMediaStreamSource = function () { return {connect: function () {}}; };
var window = {AudioContext: AC, isSecureContext: true};
var navigator = {mediaDevices: {getUserMedia: function () {
  micsAsked++;
  return new Promise(function (ok) { grants.push(ok); });
}}};
function AudioWorkletNode() { if (nodeThrows) throw new Error('no'); nodes++; this.port = {}; this.connect = function () {}; }
function fakeStream() {
  var t = {stop: function () { stopped++; }, readyState: 'live'};
  return {getAudioTracks: function () { return [t]; }, getTracks: function () { return [t]; }};
}
function keepAwake() {} function watch() {}
`

// Stopped while the phone was still being asked for the microphone: "You're off air", with
// the microphone left open and nothing to close it.
func TestTalkPageDoesntLeaveTheMicOpen(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := talkAsset(t, "talk.html")
	var fns string
	for _, name := range []string{"startMic", "closeMic", "stopMic"} {
		fn := regexp.MustCompile(`(?s)function ` + name + `\(\) \{.*?\n\}\n`).FindString(page)
		if fn == "" {
			t.Fatalf("no %s in talk.html", name)
		}
		fns += fn
	}
	// In a function of its own: node has a global navigator that a fake can't replace.
	test := `(function () {` + talkPageFakes + fns + `
(async function () {
  // Stopped while it waits for the phone to give up the microphone.
  var p = startMic();
  stopMic();
  grants.shift()(fakeStream());
  await p;
  if (stopped !== 1) throw new Error('the microphone was left open after being stopped');
  if (wantMic) throw new Error('still wants the microphone after being stopped');
  if (stream) throw new Error('kept a stream it was told to stop');
  if (nodes) throw new Error('built the audio path after being stopped');

  // A restart under way doesn't count as stopping the new one.
  stopped = 0;
  var a = startMic();
  closeMic();
  var b = startMic();
  grants.shift()(fakeStream());
  grants.shift()(fakeStream());
  await a; await b;
  if (stopped !== 1 || !wantMic || !stream || nodes !== 1) throw new Error('restart: stopped ' + stopped + ', nodes ' + nodes);
  stopMic();

  // The audio path failing to build closes what's open, and says so.
  stopped = 0; nodeThrows = true;
  var c = startMic();
  grants.shift()(fakeStream());
  var failed = false;
  await c.catch(function () { failed = true; });
  if (!failed) throw new Error('a real failure was swallowed');
  if (stopped !== 1) throw new Error('the microphone was left open after the audio path failed');
})().catch(function (e) { console.error(e.message); process.exit(1); });
})();
`
	if out, err := exec.Command(bin, "-e", test).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Enter on the phone's keyboard comes in as well as the button. A second go would start a
// second microphone and send everything twice.
func TestTalkPageAsksOnlyOnce(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := talkAsset(t, "talk.html")
	fn := regexp.MustCompile(`(?s)function join\(\) \{.*?\n\}\n`).FindString(page)
	if fn == "" {
		t.Fatal("no join in talk.html")
	}
	test := `
var els = {name: {value: 'Sam'}, join: {disabled: false}, join_say: {}};
function $(id) { return els[id]; }
var localStorage = {setItem: function () {}};
var started = 0, KEY = 'k';
function startMic() { started++; return new Promise(function () {}); }
` + fn + `
join(); join();
if (started !== 1) throw new Error('started ' + started + ' microphones');
`
	if out, err := exec.Command(bin, "-e", test).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Someone mutes for a private word and the page reloads: they still think they're muted.
// Only a page coming back keeps it: going on air afresh is always unmuted.
func TestTalkPageKeepsItsMuteThroughAReload(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := talkAsset(t, "talk.html")
	var fns string
	for _, name := range []string{"remember", "onState"} {
		fn := regexp.MustCompile(`(?s)function ` + name + `\(.*?\) \{.*?\n\}\n`).FindString(page)
		if fn == "" {
			t.Fatalf("no %s in talk.html", name)
		}
		fns += fn
	}
	if !strings.Contains(page, "muted = keepMute = d.state === 'on_air' && !!saved.muted") {
		t.Error("a reloaded page doesn't take its mute back from what it saved")
	}
	test := `
var saved = {};
var sessionStorage = {setItem: function (k, v) { saved[k] = v; }, removeItem: function (k) { delete saved[k]; }};
var KEY = 'k', me = {id: 'a', secret: 's', name: 'Sam'}, muted = true, keepMute = false, state = '', seq = 5, heldByClubhouse = false;
function $() { return {}; } function view() {} function end() {}
var END_WHY = {};
` + fns + `
remember();
if (JSON.parse(saved.talk_me).muted !== true) throw new Error('the mute was not saved');

// The page coming back with it.
muted = keepMute = true; state = '';
onState('on_air');
if (!muted) throw new Error('a reloaded page came back live');
if (keepMute) throw new Error('kept for the next time as well');

// Going on air afresh, later: unmuted.
state = 'ended';
onState('on_air');
if (muted) throw new Error('someone new went on air muted');
`
	if out, err := exec.Command(bin, "-e", test).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// OBS's page catches up in the gaps between words, and never in the middle of one.
func TestTalkOBSPageCatchesUpInSilence(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := talkAsset(t, "talk_out.html")
	fn := regexp.MustCompile(`(?s)function play\(buf\) \{.*?\n\}\n`).FindString(page)
	if fn == "" {
		t.Fatal("no play()")
	}
	test := `
var RATE = 24000, CUSHION = 0.25, MOST = 1.2, QUIET = 330, next = 0, played = 0;
var ctx = {currentTime: 100, destination: {}, createBuffer: function (c, n) { return {getChannelData: function () { return new Float32Array(n); }}; },
  createBufferSource: function () { return {connect: function () {}, start: function () { played++; }}; }};
var window = {talkStats: {chunks: 0, dropped: 0}};
` + fn + `
function chunk(v) { var a = new Int16Array(3600); a.fill(v); return a.buffer; }
play(chunk(5000)); if (played !== 1 || Math.abs(next - 100.4) > 0.01) throw new Error('first: ' + next);
next = 100.8; // behind
play(chunk(5000)); if (played !== 2) throw new Error('speech dropped while behind');
play(chunk(10)); if (played !== 2 || window.talkStats.dropped !== 1) throw new Error('silence not skipped while behind');
next = 100.3; play(chunk(10)); if (played !== 3) throw new Error('silence skipped when not behind');
next = 102; play(chunk(5000)); if (played !== 3) throw new Error('hopelessly behind: must drop');
`
	if out, err := exec.Command(bin, "-e", test).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The level the screen's meter shows.
func TestTalkPeak(t *testing.T) {
	b := make([]byte, 4)
	v := int16(-16384)
	binary.LittleEndian.PutUint16(b[2:], uint16(v))
	if p := talkPeak(b); p != 50 {
		t.Errorf("peak %d", p)
	}
}
