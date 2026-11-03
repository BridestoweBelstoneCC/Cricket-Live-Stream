package clubhouse

// The Commentary tab: what the screen and the link let through to the laptop's
// commentary routes (internal/server/talk.go has the rest).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/ini"
)

func TestTalkOnTheAllowlist(t *testing.T) {
	for _, c := range [][2]string{{"GET", "/talk/status"}, {"GET", "/talk/qr.png"}, {"GET", "/talk/onair"}, {"POST", "/talk/control"}} {
		if !ProxyAllowed(c[0], c[1]) {
			t.Errorf("%s %s should be allowed", c[0], c[1])
		}
		// A score-only screen has no business with any of it.
		if ViewAllowed(c[0], c[1]) {
			t.Errorf("%s %s must be refused to a score-only screen", c[0], c[1])
		}
	}
	// OBS's own feed, and the phone's routes: never through the link.
	for _, c := range [][2]string{{"GET", "/talk/out"}, {"GET", "/talk/out/ws"}, {"GET", "/talk"}, {"GET", "/talk/info"},
		{"POST", "/talk/join"}, {"POST", "/talk/audio"}, {"POST", "/talk/poll"}, {"POST", "/talk/leave"},
		{"GET", "/talk/control"}, {"POST", "/talk/status"}} {
		if ProxyAllowed(c[0], c[1]) {
			t.Errorf("%s %s must be refused", c[0], c[1])
		}
	}
}

// Locked, the screen can say someone's asking, but not show the code or answer them. And
// the tab's poll is no touch: an unlock it renewed would never run out on a public screen
// left showing the tab.
func TestTalkTabNeedsTheUnlockAndDoesntRenewIt(t *testing.T) {
	var proxied []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	l := startTestLink(t, &fakeHost{server: srv.URL}, true)
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()}); code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	if code, _ := ts.api("POST", "pin", M{"pin": "1234"}); code != 200 {
		t.Fatal("set PIN")
	}
	ts.api("POST", "lock", M{})

	if code, _ := ts.api("GET", "server/talk/onair", nil); code != 200 {
		t.Errorf("locked: the public read: %d", code)
	}
	for _, p := range []string{"server/talk/status", "server/talk/qr.png"} {
		if code, _ := ts.api("GET", p, nil); code != 401 {
			t.Errorf("locked: GET %s: %d", p, code)
		}
	}
	if code, _ := ts.api("POST", "server/talk/control", M{"action": "on"}); code != 401 {
		t.Errorf("locked: on: %d", code)
	}
	if len(proxied) != 1 || proxied[0] != "GET /talk/onair" {
		t.Fatalf("reached the laptop while locked: %v", proxied)
	}

	if code, _ := ts.api("POST", "unlock", M{"pin": "1234"}); code != 200 {
		t.Fatalf("unlock: %d", code)
	}
	// Move the unlock's end to a known point, and watch it.
	soon := time.Now().Add(time.Minute)
	ts.mu.Lock()
	ts.unlockedTil = soon
	ts.mu.Unlock()
	if code, _ := ts.api("GET", "server/talk/status", nil); code != 200 {
		t.Fatalf("unlocked: status: %d", code)
	}
	ts.mu.Lock()
	got := ts.unlockedTil
	ts.mu.Unlock()
	if !got.Equal(soon) {
		t.Errorf("the tab's poll renewed the unlock: %v -> %v", soon, got)
	}
	// A real press does renew it.
	if code, _ := ts.api("POST", "server/talk/control", M{"action": "on"}); code != 200 {
		t.Fatalf("unlocked: on: %d", code)
	}
	ts.mu.Lock()
	got = ts.unlockedTil
	ts.mu.Unlock()
	if !got.After(soon) {
		t.Errorf("pressing the switch didn't renew the unlock")
	}
}

// "auto_update = no" is set by hand: the screen rewriting its settings must keep it.
func TestSavingSettingsKeepsAutoUpdateOff(t *testing.T) {
	ts := newTestScreen(t)
	if err := saveScreenConfig(ts.cfg); err != nil {
		t.Fatal(err)
	}
	f, err := ini.Load(ScreenFile())
	if err != nil {
		t.Fatal(err)
	}
	f.Set("Screen", "auto_update", "no")
	if err := os.WriteFile(ScreenFile(), f.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ts.setPIN("1234"); err != nil { // one of the things that saves
		t.Fatal(err)
	}
	if autoUpdateOn() {
		t.Error("setting a PIN switched the updates back on")
	}
}

// The server picks the QR code's address from the card the screen came in on.
func TestTalkLinkSaysWhichCard(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+"="+r.Header.Get("X-Talk-Local-Ip"))
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	l := startTestLink(t, &fakeHost{server: srv.URL}, true)
	call(t, l, "GET", "/clubhouse/server/talk/status", testKey, nil)
	call(t, l, "GET", "/clubhouse/server/state", testKey, nil)
	if len(got) != 2 || got[0] != "/talk/status=127.0.0.1" || got[1] != "/state=" {
		t.Errorf("%v", got)
	}
}

// Rebuilt every second, a button can be replaced between a finger going down and coming up,
// and the press is lost: on a touch screen, that's "the button doesn't work".
func TestTalkListOnlyRedrawsWhenItChanges(t *testing.T) {
	fn := grabFunc(t, pageScript(t), "renderTalk")
	if !strings.Contains(fn, "listKey !== talkListKey") {
		t.Error("the waiting list is rebuilt on every poll")
	}
}

// The button under the steps goes round the laptop's cards. It's for a laptop on more than
// one network, and for a code that's on something that isn't a real card, like a VPN.
func TestTalkAddressButton(t *testing.T) {
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	test := grabFunc(t, pageScript(t), "talkNextCard") + `
var eth = {ip: '10.0.0.5', card: 'Ethernet'}, wifi = {ip: '192.168.1.73', card: 'WiFi'};
function next(address, list) { var n = talkNextCard({address: address, addresses: list}); return n && n.card; }
var cases = [
  ['no choice with one card', next('192.168.1.73', [wifi]), null],
  ['round from the first', next('10.0.0.5', [eth, wifi]), 'WiFi'],
  ['round from the last', next('192.168.1.73', [eth, wifi]), 'Ethernet'],
  ['the code on a VPN, one real card', next('100.64.0.1', [wifi]), 'WiFi'],
  ['the tunnel: nothing listed', next('', []), null],
  ['an older server: no list', talkNextCard({address: 'x'}), null]
];
cases.forEach(function (c) { if (c[1] !== c[2]) throw new Error(c[0] + ': got ' + c[1]); });
`
	if out, err := exec.Command(bin, "-e", test).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The tab's script: the text it needs is on the page, and what's typed on a phone goes in
// as text, never as HTML, on a screen in a public room.
func TestTalkTabOnThePage(t *testing.T) {
	script := pageScript(t)
	fn := grabFunc(t, script, "renderTalk")
	if strings.Contains(fn, "innerHTML") || !strings.Contains(fn, "b.textContent = w.name") {
		t.Errorf("a phone's name must go in as text: %s", fn)
	}
	if !regexp.MustCompile(`data-tab="talk" class="op"`).MatchString(screenHTML) {
		t.Error("the Commentary tab must be an operator's tab: hidden while locked")
	}
	// The address under the code leaves the key off: it's a public room's screen, and the
	// QR code alone is enough to scan.
	if !strings.Contains(fn, "d.url.split('?')[0]") {
		t.Error("the key is shown as text under the QR code")
	}
}
