package server

// The code review's smaller security points (2026-11): POST /state takes only settings of
// the right type, a request body has a time limit, the match page has a CSP, and OBS's
// password has no made-up default.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestStateRefusesUnknownAndMistypedSettings(t *testing.T) {
	s := newTestServer(t, "")
	st, d := s.postJSON("/state", M{
		"home_team":         "Fine CC",          // kept
		"max_overs":         40,                 // kept
		"replay_enabled":    false,              // kept
		"roster":            M{"7": "J Walker"}, // kept
		"replay_folder":     12345,              // a number where a folder's name goes
		"obs_port":          "4455; rm -rf",     // text where a number goes
		"demo_mode":         "yes",              // text where a switch goes
		"home_colour":       nil,                // nothing where text goes
		"stream_ladder":     M{"step": 3},       // the server's own
		"not_a_setting":     "x",                // nobody's
		"network_test_mbps": 900,                // measured by the server, never posted
	})
	mustEq(t, st, 200)
	mustEq(t, d["ok"], any(true))
	got := fmt.Sprint(d["ignored"])
	mustEq(t, got, "[demo_mode home_colour network_test_mbps not_a_setting obs_port replay_folder stream_ladder]")

	now := LoadState()
	mustEq(t, now["home_team"], any("Fine CC"))
	mustEq(t, gi(now, "max_overs", 0), 40)
	mustEq(t, now["replay_enabled"], any(false))
	mustEq(t, fmt.Sprint(now["roster"]), "map[7:J Walker]")
	def := DefaultState()
	for _, k := range []string{"replay_folder", "obs_port", "demo_mode", "home_colour", "network_test_mbps"} {
		if !jsonEqual(now[k], def[k]) {
			t.Errorf("%s was changed to %v", k, now[k])
		}
	}
	for _, k := range []string{"stream_ladder", "not_a_setting"} {
		if _, has := now[k]; has {
			t.Errorf("%s was written to the state", k)
		}
	}
	// A clean save says nothing about ignoring.
	_, d = s.postJSON("/state", M{"away_team": "Rival CC"})
	if _, has := d["ignored"]; has {
		t.Errorf("a clean save reported %v", d["ignored"])
	}
}

// Every field the control panel's own form posts is a setting POST /state knows, of the
// type the form sends: a field added to the page and not to the server would be dropped
// without a word to the operator.
func TestEveryPanelFieldIsAPostableSetting(t *testing.T) {
	panel := page(t, "control.html")
	list := func(name string) []string {
		m := regexp.MustCompile(`(?s)const ` + name + ` = \[(.*?)\];`).FindStringSubmatch(panel)
		if m == nil {
			t.Fatalf("%s not found in control.html", name)
		}
		var out []string
		for _, q := range regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
		if len(out) < 5 {
			t.Fatalf("%s: only %d fields read", name, len(out))
		}
		return out
	}
	for kind, fields := range map[string][]string{"string": list("FIELDS"), "number": list("NUM_FIELDS"), "bool": list("BOOL_FIELDS")} {
		for _, f := range fields {
			if got, ok := postedStateKind(f); !ok || got != kind {
				t.Errorf("the panel posts %s as a %s; POST /state takes it as %q (known: %v) — add it to DefaultState or postedStateExtras", f, kind, got, ok)
			}
		}
	}
	// And the panel's one-field saves.
	for f, kind := range map[string]string{"sponsor_id": "string", "roster": "object", "home_club_id": "string", "away_club_id": "string"} {
		if got, _ := postedStateKind(f); got != kind {
			t.Errorf("%s: %q, want %s", f, got, kind)
		}
	}
	// Every secret the panel round-trips is postable too (or it could never be set).
	for _, k := range []string{"anthropic_api_key", "playcricket_api_key", "obs_password", "camera_rtsp_url", "camera2_rtsp_url", "pcs_bridge_token"} {
		if got, _ := postedStateKind(k); got != "string" {
			t.Errorf("secret %s isn't postable", k)
		}
	}
}

func TestABodyThatNeverArrivesIsGivenUpOn(t *testing.T) {
	s := newTestServer(t, "")
	saved := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = saved })

	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Headers promising a body, then two bytes of it and nothing more.
	fmt.Fprintf(conn, "POST /state HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 50\r\n\r\n{\"", s.addr)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	r, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer within 5s: the connection was held open (%v)", err)
	}
	r.Body.Close()
	if r.StatusCode != 408 || time.Since(start) > 3*time.Second {
		t.Errorf("status %d after %v, want 408 soon after the limit", r.StatusCode, time.Since(start))
	}
	// A whole body sent promptly is unaffected, and so is the request after it on the
	// same connection (the limit is lifted again).
	c2, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	br := bufio.NewReader(c2)
	for i := 0; i < 2; i++ {
		if i == 1 {
			time.Sleep(2 * bodyReadTimeout) // idle for longer than the limit
		}
		body := `{"away_team":"Rival CC"}`
		fmt.Fprintf(c2, "POST /state HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", s.addr, len(body), body)
		c2.SetReadDeadline(time.Now().Add(5 * time.Second))
		r, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("request %d on a kept connection: %v", i+1, err)
		}
		var d M
		json.NewDecoder(r.Body).Decode(&d)
		r.Body.Close()
		if r.StatusCode != 200 || d["ok"] != true {
			t.Errorf("request %d: %d %v", i+1, r.StatusCode, d)
		}
	}
}

func TestMatchPageIsServedSandboxed(t *testing.T) {
	s := matchPageSandbox(t)
	buildPage(t)
	r := s.get("/match/page/latest")
	mustEq(t, r.status, 200)
	csp := r.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "sandbox", "img-src data:", "style-src 'unsafe-inline'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	for _, never := range []string{"script-src", "allow-scripts", "allow-same-origin"} {
		if strings.Contains(csp, never) {
			t.Errorf("CSP %q has %q: the page needs no script and must not share the panel's origin", csp, never)
		}
	}
	mustEq(t, r.header.Get("X-Content-Type-Options"), "nosniff")
	// The policy has to fit the page: nothing in it is loaded from anywhere.
	body := string(r.body)
	for _, loaded := range []string{"<script", "<link", "src=\"http", "src=\"/", "url(http", "<iframe", "<form"} {
		if strings.Contains(body, loaded) {
			t.Errorf("the match page has %q, which its CSP would block", loaded)
		}
	}
}

func TestOBSPasswordHasNoMadeUpDefault(t *testing.T) {
	s := newTestServer(t, "")
	mustEq(t, DefaultState()["obs_password"], any(""))
	// A state file saved when the default was CHANGE_ME reads as no password...
	if err := os.WriteFile(StateFile, []byte(`{"obs_password":"CHANGE_ME","home_team":"Old CC"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustEq(t, LoadState()["obs_password"], any(""))
	_, d := s.getJSON("/state")
	mustEq(t, d["obs_password"], any("")) // not "a password is saved"
	// ...so the real one in config.ini is seeded over it.
	if err := os.WriteFile(ConfigFile, []byte("[OBS]\nobs_password = the-real-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	SeedStateFromConfig()
	mustEq(t, LoadState()["obs_password"], any("the-real-one"))
	// A real password that someone chose is left alone.
	UpdateState(func(st M) { st["obs_password"] = "chosen" })
	SeedStateFromConfig()
	mustEq(t, LoadState()["obs_password"], any("chosen"))
}
