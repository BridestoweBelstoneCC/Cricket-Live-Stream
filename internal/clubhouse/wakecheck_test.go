package clubhouse

// Can the screen switch the laptop on: which network cards count as the laptop's own
// cable, the pre-match check's advice for every way a wake can be set up wrong, and the
// proof a wake has worked (the screen reports it, the link keeps it).

import (
	"net"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/assets"
)

func mac(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }

// Real adapters from a maintainer's Windows laptop (Get-NetAdapter), and the usual
// Linux/Mac names. The first was the bug: VirtualBox's host-only adapter is called
// "Ethernet 3", and passed as the laptop's network cable.
func TestClassifyNIC(t *testing.T) {
	for _, c := range []struct {
		name, desc, mac string
		virtual, wired  bool
	}{
		{"Ethernet 3", "VirtualBox Host-Only Ethernet Adapter", "0a:00:27:00:00:04", true, false},
		{"Ethernet", "Realtek PCIe 2.5GbE Family Controller", "d8:bb:c1:12:34:56", false, true},
		{"WiFi", "Intel(R) Wi-Fi 6E AX210 160MHz", "14:18:c3:3f:8f:36", false, false},
		{"OpenVPN TAP-Windows6", "TAP-Windows Adapter V9", "00:ff:12:34:56:78", true, false},
		{"Bluetooth Network Connection", "Bluetooth Device (Personal Area Network)", "14:18:c3:3f:8f:3a", true, false},
		{"vEthernet (Default Switch)", "Hyper-V Virtual Ethernet Adapter", "00:15:5d:01:02:03", true, false},
		// Named like a port, but its address is made up: a VM's or a tether's, never the
		// laptop's own card (whose address the maker burned in).
		{"Ethernet 2", "Remote NDIS based Internet Sharing Device", "02:4a:11:22:33:44", false, false},
		{"eth0", "", "dc:a6:32:01:02:03", false, true},
		{"enp3s0", "", "00:d8:61:aa:bb:cc", false, true},
		{"vboxnet0", "", "0a:00:27:00:00:00", true, false},
		{"wlan0", "", "dc:a6:32:01:02:04", false, false},
	} {
		v, w := classifyNIC(c.name, c.desc, mac(c.mac))
		if v != c.virtual || w != c.wired {
			t.Errorf("%s (%s, %s): virtual=%v wired=%v, want %v %v", c.name, c.desc, c.mac, v, w, c.virtual, c.wired)
		}
	}
}

// This machine's own cards: nothing virtual listed, nothing with a made-up address
// called wired.
func TestLocalNICsLeaveOutVirtualCards(t *testing.T) {
	for _, n := range LocalNICs("") {
		if n.Wired && locallyAdministered(mac(n.MAC)) {
			t.Errorf("%s (%s) is wired with a made-up address", n.Name, n.MAC)
		}
		if isVirtual(adapterDescription(n.Name)) {
			t.Errorf("%s (%s) is virtual", n.Name, adapterDescription(n.Name))
		}
	}
}

func TestArmedMatches(t *testing.T) {
	for _, c := range []struct {
		armed, desc string
		want        bool
	}{
		{"Realtek PCIe 2.5GbE Family Controller", "Realtek PCIe 2.5GbE Family Controller", true},
		{"Intel(R) Ethernet Connection (2)", "Intel(R) Ethernet Connection", true},
		{"realtek pcie 2.5gbe family controller", "Realtek PCIe 2.5GbE Family Controller", true},
		{"HID Keyboard Device", "Realtek PCIe 2.5GbE Family Controller", false},
		{"Intel(R) Ethernet Connection (I219-V)", "Intel(R) Ethernet Connection", false}, // a model, not a count
	} {
		if got := armedMatches(c.armed, c.desc); got != c.want {
			t.Errorf("armedMatches(%q, %q) = %v", c.armed, c.desc, got)
		}
	}
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestWakeAdvice(t *testing.T) {
	woke := &WakeRecord{At: time.Date(2026, 10, 24, 11, 5, 0, 0, time.Local), AfterSec: 74}
	ready := WakeCheck{Platform: true, WiredPorts: []string{"Realtek PCIe"}, CableIn: true, Armed: []string{"Realtek PCIe"},
		FastStartup: no(), AutoSignIn: yes(), LoginKnown: true, StartAtLogin: true, LastWake: woke}

	if s, d, f := WakeAdvice(ready); s != "ok" || f != "" || !strings.Contains(d, "Sat 24 Oct 11:05") || !strings.Contains(d, "74s") {
		t.Fatalf("all set and woken before: %s %q %q", s, d, f)
	}
	never := ready
	never.LastWake = nil
	if s, d, f := WakeAdvice(never); s != "warn" || !strings.Contains(d, "hasn't switched this laptop on yet") || !strings.Contains(f, "Try it once") {
		t.Fatalf("never tried: %s %q %q", s, d, f)
	}

	// Each thing a wake needs, missing on its own: a warning naming that fix — never ✗,
	// since none of it is wrong days before the match.
	for _, c := range []struct {
		name   string
		change func(*WakeCheck)
		fix    string
	}{
		{"no cable", func(c *WakeCheck) { c.CableIn = false }, "by cable"},
		{"not armed", func(c *WakeCheck) { c.Armed = []string{} }, "Allow this device to wake the computer"},
		{"fast startup", func(c *WakeCheck) { c.FastStartup = yes() }, "Fast Startup off"},
		{"no auto sign-in", func(c *WakeCheck) { c.AutoSignIn = no() }, "sign in automatically"},
		{"not at login", func(c *WakeCheck) { c.StartAtLogin = false }, "Open CricketStream when Windows starts"},
		{"no port at all", func(c *WakeCheck) { c.WiredPorts, c.CableIn, c.Armed = nil, false, []string{} }, "no network port of its own"},
	} {
		w := ready
		c.change(&w)
		s, d, f := WakeAdvice(w)
		if s != "warn" || !strings.Contains(f, c.fix) || !strings.Contains(d, "may not be able to") {
			t.Errorf("%s: %s %q %q", c.name, s, d, f)
		}
		if strings.Count(f, ". ") > 3 && c.name != "not armed" && c.name != "fast startup" {
			t.Errorf("%s: only its own fix expected, got %q", c.name, f)
		}
	}
	// No port at all: the cable and arming advice would be noise.
	w := ready
	w.WiredPorts, w.CableIn, w.Armed = nil, false, []string{}
	if _, _, f := WakeAdvice(w); strings.Contains(f, "by cable") || strings.Contains(f, "Power Management") {
		t.Errorf("no port: %q", f)
	}
	// Windows wouldn't say what's armed: not a warning on its own.
	w = ready
	w.Armed = nil
	if s, _, _ := WakeAdvice(w); s != "ok" {
		t.Errorf("armed unknown: %s", s)
	}
	// A Mac: no Windows readings and no login switch to tick — only the cable and a past
	// wake count, and it's never told to tick a Windows box.
	mac := WakeCheck{CableIn: true, LastWake: woke}
	if s, d, f := WakeAdvice(mac); s != "ok" || f != "" {
		t.Errorf("mac, cabled and woken: %s %q %q", s, d, f)
	}
	mac.CableIn = false
	if _, _, f := WakeAdvice(mac); !strings.Contains(f, "by cable") || strings.Contains(f, "Windows") {
		t.Errorf("mac without cable: %q", f)
	}
}

// The screen woke the laptop; when it answers, the screen says so and the laptop keeps it.
func TestScreenReportsAWakeThatWorked(t *testing.T) {
	l := startTestLink(t, &fakeHost{}, false)
	ts := newTestScreen(t) // its assets folder is the link's too: one machine in the test
	defer swap(&wake, func([]string) (int, error) { return 6, nil })()
	if code, out := ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()}); code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	if LastWake() != nil {
		t.Fatal("a wake recorded before any was sent")
	}
	ts.mu.Lock()
	ts.cfg.MACs = []string{"d8:bb:c1:12:34:56"}
	ts.mu.Unlock()
	if code, out := ts.api("POST", "wake", M{}); code != 200 {
		t.Fatalf("wake: %d %v", code, out)
	}
	ts.mu.Lock()
	ts.wakingSince = time.Now().Add(-74 * time.Second)
	ts.mu.Unlock()
	ts.pollOnce()
	var w *WakeRecord
	for i := 0; i < 200 && w == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		w = LastWake()
	}
	if w == nil || w.AfterSec < 74 || w.AfterSec > 80 || time.Since(w.At) > time.Minute {
		t.Fatalf("recorded: %+v", w)
	}
	// Only once: the next poll isn't another wake.
	at := w.At
	ts.pollOnce()
	time.Sleep(100 * time.Millisecond)
	if w2 := LastWake(); w2 == nil || !w2.At.Equal(at) {
		t.Fatalf("a second poll recorded another wake: %+v", w2)
	}
}

func TestLinkWokeNeedsTheKeyAndASaneBody(t *testing.T) {
	saved := assets.Dir()
	assets.SetDir(t.TempDir())
	t.Cleanup(func() { assets.SetDir(saved) })
	l := startTestLink(t, &fakeHost{}, false)
	if code, _ := call(t, l, "POST", "/clubhouse/woke", "", M{"after": 30}); code != 403 {
		t.Fatalf("no key: %d", code)
	}
	for _, bad := range []any{M{"after": -1}, M{"after": 999999}, "nonsense"} {
		if code, _ := call(t, l, "POST", "/clubhouse/woke", testKey, bad); code != 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if LastWake() != nil {
		t.Fatal("a refused report was kept")
	}
	if code, _ := call(t, l, "POST", "/clubhouse/woke", testKey, M{"after": 95}); code != 200 {
		t.Fatalf("report: %d", code)
	}
	if w := LastWake(); w == nil || w.AfterSec != 95 || plainDuration(w.AfterSec) != "1m 35s" {
		t.Fatalf("kept: %+v", w)
	}
}
