package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// serveAs sends one request straight to the handler, from a chosen socket address and
// with a chosen Host — neither of which a real connection from the test can fake.
func serveAs(method, target, remote, host string, headers map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Host = host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Handler{}.ServeHTTP(w, r)
	return w
}

// The review's S1/S2: X-Forwarded-For is believed only from a proxy on this machine, and
// is only ever an address.
func TestClientIPTrustsForwardingOnlyFromLoopback(t *testing.T) {
	// No tunnel running: CF-Connecting-IP is the sender's own text even through a proxy
	// on this machine (Tailscale doesn't strip it), so the proxy's own hop is used.
	cfMu.Lock()
	oldCF := cfURL
	cfURL = ""
	cfMu.Unlock()
	t.Cleanup(func() { cfMu.Lock(); cfURL = oldCF; cfMu.Unlock() })
	r0 := httptest.NewRequest("GET", "/", nil)
	r0.RemoteAddr = "127.0.0.1:5555"
	r0.Header.Set("CF-Connecting-IP", "10.9.9.9")
	r0.Header.Set("X-Forwarded-For", "100.64.0.7")
	mustEq(t, (&ctx{r: r0}).clientIP(), "100.64.0.7")
	cfMu.Lock()
	cfURL = "https://brave-otter-ride.trycloudflare.com"
	cfMu.Unlock()
	cases := []struct {
		remote  string
		headers map[string]string
		want    string
	}{
		// from the network: the header is the sender's own text
		{"192.168.1.50:5555", map[string]string{"X-Forwarded-For": "10.0.0.1"}, "192.168.1.50"},
		{"192.168.1.50:5555", map[string]string{"X-Forwarded-For": "<img src=x onerror=alert(1)>"}, "192.168.1.50"},
		{"192.168.1.50:5555", map[string]string{"CF-Connecting-IP": "10.0.0.1"}, "192.168.1.50"},
		// through cloudflared: Cloudflare's own header, or the hop it appended
		{"127.0.0.1:5555", map[string]string{"CF-Connecting-IP": "203.0.113.7", "X-Forwarded-For": "1.2.3.4, 203.0.113.7"}, "203.0.113.7"},
		{"127.0.0.1:5555", map[string]string{"X-Forwarded-For": "1.2.3.4, 203.0.113.7"}, "203.0.113.7"},
		// the clubhouse link: one address
		{"127.0.0.1:5555", map[string]string{"X-Forwarded-For": "192.168.1.80"}, "192.168.1.80"},
		// a proxy passing on rubbish never puts it in the log
		{"127.0.0.1:5555", map[string]string{"X-Forwarded-For": "<b>hi</b>"}, "proxied"},
		{"127.0.0.1:5555", nil, "127.0.0.1"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		c := &ctx{r: r}
		mustEq(t, c.clientIP(), tc.want, tc.remote, tc.headers)
	}
}

// A new X-Forwarded-For per guess no longer walks past the lockout.
func TestLockoutIgnoresSpoofedForwardedFor(t *testing.T) {
	newTestServer(t, testPW)
	loginSleep = func(time.Duration) {}
	t.Cleanup(func() { loginSleep = time.Sleep })
	last := 0
	for i := 0; i <= LoginMaxFailures; i++ {
		w := serveAs("POST", "/login", "192.168.1.50:5555", "192.168.1.10:5000",
			map[string]string{"X-Forwarded-For": "10.0.0." + strconv.Itoa(i+1), "Content-Type": "application/json"},
			`{"password":"wrong"}`)
		last = w.Code
	}
	mustEq(t, last, 429)
}

// Many addresses guessing at once lock everyone out but this laptop.
func TestGlobalLoginCap(t *testing.T) {
	s := newTestServer(t, testPW)
	loginSleep = func(time.Duration) {}
	t.Cleanup(func() { loginSleep = time.Sleep })
	for i := 0; i < LoginMaxFailuresGlobal; i++ {
		remote := "192.168.2." + strconv.Itoa(i+1) + ":5555"
		serveAs("POST", "/login", remote, "192.168.1.10:5000",
			map[string]string{"Content-Type": "application/json"}, `{"password":"wrong"}`)
	}
	w := serveAs("POST", "/login", "192.168.3.1:5555", "192.168.1.10:5000",
		map[string]string{"Content-Type": "application/json"}, `{"password":"`+testPW+`"}`)
	mustEq(t, w.Code, 429)
	// the laptop itself still signs in
	st, body := s.postJSON("/login", M{"password": testPW})
	mustEq(t, st, 200)
	mustEq(t, body["ok"], any(true))
}

// The review's S4: DNS rebinding. A page whose domain points at 127.0.0.1 sends its own
// name as Host; the server answers only to its own.
func TestHostCheckRefusesForeignNames(t *testing.T) {
	newTestServer(t, "")
	for _, host := range []string{"evil.example.com", "evil.example.com:5000", "localhost.evil.example:5000"} {
		w := serveAs("GET", "/state", "127.0.0.1:5555", host, nil, "")
		mustEq(t, w.Code, http.StatusMisdirectedRequest, host)
	}
	me := ownHostname()
	for _, host := range []string{"127.0.0.1:5000", "localhost:5000", "[::1]:5000", "192.168.1.10:5000",
		"100.101.102.103:5000", "laptop.tail1234.ts.net", me + ":5000", me + ".local:5000", ""} {
		if host == ":5000" || host == ".local:5000" {
			continue // no hostname on this machine
		}
		w := serveAs("GET", "/state", "127.0.0.1:5555", host, nil, "")
		if w.Code == http.StatusMisdirectedRequest {
			t.Fatalf("%q refused", host)
		}
	}
	cfMu.Lock()
	old := cfURL
	cfURL = "https://brave-otter-ride.trycloudflare.com"
	cfMu.Unlock()
	t.Cleanup(func() { cfMu.Lock(); cfURL = old; cfMu.Unlock() })
	w := serveAs("GET", "/state", "127.0.0.1:5555", "brave-otter-ride.trycloudflare.com", nil, "")
	if w.Code == http.StatusMisdirectedRequest {
		t.Fatal("the tunnel's own name was refused")
	}
	w = serveAs("GET", "/state", "127.0.0.1:5555", "other.trycloudflare.com", nil, "")
	mustEq(t, w.Code, http.StatusMisdirectedRequest)
}

// The review's S5: Go's HTTP errors quote the URL, and PlayCricket's holds the key.
func TestURLErrorsLoseTheKey(t *testing.T) {
	err := scrubURLError(&url.Error{Op: "Get", URL: "https://play-cricket.com/api/v2/matches.json?api_token=SECRETKEY",
		Err: errors.New("dial tcp: lookup play-cricket.com: no such host")})
	if strings.Contains(err.Error(), "SECRETKEY") || !strings.Contains(err.Error(), "no such host") {
		t.Fatal(err)
	}
	_, err = httpGetJSON("http://127.0.0.1:1/x?api_token=SECRETKEY", time.Second, nil)
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatal(err)
	}
}

// Routes that were open and shouldn't be, with a password set.
func TestReviewGatedRoutes(t *testing.T) {
	s := newTestServer(t, testPW)
	for _, p := range []string{"/agent/discover", "/agent/status", "/match/fixtures"} {
		st, _ := s.getJSON(p)
		mustEq(t, st, 401, p)
	}
	// The diagnostics the setup guides send people to in a browser tab (which can't carry
	// the login): this laptop reads them, the network needs the login.
	for _, p := range []string{"/pcs/debug", "/logos/debug", "/status"} {
		st, _ := s.getJSON(p)
		mustEq(t, st, 200, p)
		w := serveAs("GET", p, "192.168.1.50:5555", "192.168.1.10:5000", nil, "")
		mustEq(t, w.Code, 401, p)
		w = serveAs("GET", p, "127.0.0.1:5555", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, "")
		mustEq(t, w.Code, 401, p, "through the tunnel")
	}
}

// A club's own domain in front of the server: [Network] extra_hosts.
func TestExtraHosts(t *testing.T) {
	newTestServer(t, "")
	got := parseExtraHosts("Stream.OurClub.example, https://panel.ourclub.example:8443/control  other.example.")
	mustEq(t, strings.Join(got, "|"), "stream.ourclub.example|panel.ourclub.example|other.example")
	old := ExtraHosts
	ExtraHosts = got
	t.Cleanup(func() { ExtraHosts = old })
	w := serveAs("GET", "/state", "127.0.0.1:5555", "stream.ourclub.example", nil, "")
	if w.Code == http.StatusMisdirectedRequest {
		t.Fatal("an extra host was refused")
	}
	w = serveAs("GET", "/state", "127.0.0.1:5555", "evil.example", nil, "")
	mustEq(t, w.Code, http.StatusMisdirectedRequest)
}

// OBS on another computer, named not numbered: asked of DNS once, not on every poll.
func TestOBSHostByName(t *testing.T) {
	newTestServer(t, "")
	asked := 0
	old := lookupHost
	lookupHost = func(h string) ([]string, error) {
		asked++
		if h == "obs-pc" {
			return []string{"192.168.1.60"}, nil
		}
		return nil, errors.New("no such host")
	}
	obsHostMu.Lock()
	obsHostName = ""
	obsHostMu.Unlock()
	t.Cleanup(func() {
		lookupHost = old
		obsHostMu.Lock()
		obsHostName = ""
		obsHostMu.Unlock()
	})
	setState(t, M{"obs_host": "obs-pc"})
	for i := 0; i < 3; i++ {
		w := serveAs("GET", "/commands", "192.168.1.60:5555", "192.168.1.10:5000", nil, "")
		mustEq(t, w.Code, 200)
	}
	w := serveAs("GET", "/commands", "192.168.1.61:5555", "192.168.1.10:5000", nil, "")
	mustEq(t, w.Code, 403)
	mustEq(t, asked, 1)
}

// The review's S6: /live and /commands change state on a GET, so only the overlay — on
// this laptop, or on the computer OBS runs on — may call them.
func TestOverlayRoutesRefuseStrangers(t *testing.T) {
	newTestServer(t, "")
	refused := []struct {
		remote  string
		headers map[string]string
	}{
		{"127.0.0.1:5555", map[string]string{"Sec-Fetch-Site": "cross-site"}},   // a web page's <img>
		{"127.0.0.1:5555", map[string]string{"Sec-Fetch-Site": "same-site"}},    // another port on localhost
		{"127.0.0.1:5555", map[string]string{"Origin": "http://evil.example"}},  // a foreign page's fetch
		{"127.0.0.1:5555", map[string]string{"X-Forwarded-For": "203.0.113.9"}}, // through the tunnel
		{"192.168.1.50:5555", nil}, // a device on the wifi
		{"192.168.1.50:5555", map[string]string{"Sec-Fetch-Site": "same-origin"}}, // ...whatever it claims
	}
	for _, path := range []string{"/live", "/commands"} {
		for _, tc := range refused {
			w := serveAs("GET", path, tc.remote, "127.0.0.1:5000", tc.headers, "")
			mustEq(t, w.Code, 403, path, tc.remote, tc.headers)
		}
		// the overlay in OBS on this laptop
		w := serveAs("GET", path, "127.0.0.1:5555", "127.0.0.1:5000", map[string]string{"Sec-Fetch-Site": "same-origin"}, "")
		mustEq(t, w.Code, 200, path)
	}
	// /live/view stays open to every reader: it changes nothing
	w := serveAs("GET", "/live/view", "192.168.1.50:5555", "192.168.1.10:5000", nil, "")
	mustEq(t, w.Code, 200)
	// OBS on another computer (manage_obs = no): its overlay polls over the network
	setState(t, M{"obs_host": "192.168.1.60"})
	w = serveAs("GET", "/commands", "192.168.1.60:5555", "192.168.1.10:5000", map[string]string{"Sec-Fetch-Site": "same-origin"}, "")
	mustEq(t, w.Code, 200)
	w = serveAs("GET", "/commands", "192.168.1.50:5555", "192.168.1.10:5000", nil, "")
	mustEq(t, w.Code, 403)
}
