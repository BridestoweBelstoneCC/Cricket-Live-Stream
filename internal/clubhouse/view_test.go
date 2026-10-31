package clubhouse

// Score-only screens: the laptop's second key reaches the score and nothing that starts,
// stops, wakes or switches anything off — refused by the laptop whatever the screen asks.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testViewKey = "V1EWK3YS"

func TestViewAllowlist(t *testing.T) {
	allowed := [][2]string{{"GET", "/live/view"}, {"GET", "/state"}, {"GET", "/checklist"}, {"GET", "/preview.jpg"},
		{"GET", "/youtube/live"}, {"GET", "/match/scorecard"}, {"GET", "/league/table"}, {"GET", "/logo/29434"}}
	for _, c := range allowed {
		if !ViewAllowed(c[0], c[1]) {
			t.Errorf("%s %s should be allowed for a score-only screen", c[0], c[1])
		}
		if !ProxyAllowed(c[0], c[1]) {
			t.Errorf("%s %s: a score-only read must also be on the screen's own allowlist", c[0], c[1])
		}
	}
	// Every button, and the reads only an operator needs.
	refused := [][2]string{{"POST", "/checklist/action"}, {"POST", "/precheck"}, {"POST", "/cameras/use"},
		{"POST", "/obs/add_camera"}, {"POST", "/camera/scene"}, {"POST", "/youtube/update"},
		{"GET", "/precheck/last"}, {"GET", "/cameras/discover"}, {"GET", "/health"}, {"GET", "/stream/monitor"},
		{"GET", "/live"}, {"GET", "/commands"}, {"GET", "/logo/"}, {"GET", "/logo/../state"}, {"POST", "/live/view"}}
	for _, c := range refused {
		if ViewAllowed(c[0], c[1]) {
			t.Errorf("%s %s must be refused to a score-only screen", c[0], c[1])
		}
	}
}

// questionHost is a laptop with a question showing and an activity log: things a
// score-only screen must never see.
type questionHost struct{ fakeHost }

func (h *questionHost) Status() map[string]any {
	return map[string]any{"phase": "running", "club": "Test CC", "server_ready": true, "version": "3.0.0",
		"question": map[string]any{"id": 7, "text": "Generate an AI match report?"},
		"log":      []string{"Signed in to YouTube as the club"}, "manual_session": false}
}

func startTestLinkBoth(t *testing.T, h Host, key, viewKey string) *Link {
	t.Helper()
	l, err := StartLink(Config{Key: key, ViewKey: viewKey, Port: freePort(t), AllowPowerOff: true, Name: "Test CC"}, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	return l
}

func TestLinkScoreOnlyKeyReadsAndNothingElse(t *testing.T) {
	var proxied []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	h := &questionHost{}
	h.server, h.token, h.ready, h.answerHit = srv.URL, "tok", true, true
	l := startTestLinkBoth(t, h, testKey, testViewKey)

	code, pair := call(t, l, "GET", "/clubhouse/pair", testViewKey, nil)
	if code != 200 || pair["role"] != "view" || pair["club"] != "Test CC" {
		t.Fatalf("pair: %d %v", code, pair)
	}
	if pair["nics"] != nil || pair["allow_power_off"] != nil {
		t.Errorf("a score-only screen can't wake or switch off, so isn't told how: %v", pair)
	}
	code, st := call(t, l, "GET", "/clubhouse/status", testViewKey, nil)
	if code != 200 || st["view"] != true || st["phase"] != "running" || st["server_ready"] != true {
		t.Fatalf("status: %d %v", code, st)
	}
	for _, k := range []string{"question", "log", "allow_power_off", "power_off_in", "manual_session"} {
		if _, ok := st[k]; ok {
			t.Errorf("a score-only screen was shown %q", k)
		}
	}
	for _, p := range []string{"/clubhouse/start", "/clubhouse/answer", "/clubhouse/stop", "/clubhouse/poweroff",
		"/clubhouse/poweroff/cancel", "/clubhouse/woke"} {
		if code, _ := call(t, l, "POST", p, testViewKey, M{"id": 7, "yes": true, "after": 30}); code != 403 {
			t.Errorf("%s with the score-only key: %d, want 403", p, code)
		}
	}
	if len(h.started) != 0 || len(h.answered) != 0 || len(h.stopped) != 0 {
		t.Fatalf("the score-only key reached the window: start %v answer %v stop %v", h.started, h.answered, h.stopped)
	}
	if code, _ := call(t, l, "GET", "/clubhouse/server/live/view", testViewKey, nil); code != 200 {
		t.Fatalf("the score: %d", code)
	}
	for _, c := range [][2]string{{"POST", "/clubhouse/server/checklist/action"}, {"GET", "/clubhouse/server/cameras/discover"},
		{"GET", "/clubhouse/server/health"}, {"POST", "/clubhouse/server/youtube/update"}} {
		if code, _ := call(t, l, c[0], c[1], testViewKey, M{"action": "stop_stream"}); code != 403 {
			t.Errorf("%s %s with the score-only key: %d, want 403", c[0], c[1], code)
		}
	}
	if strings.Join(proxied, ",") != "GET /live/view" {
		t.Fatalf("reached the server: %v", proxied)
	}
	if seen, _ := l.Seen(); !seen.IsZero() {
		t.Error("a score-only screen counted as the match-day screen being in touch")
	}
	if seen, _ := l.SeenView(); seen.IsZero() {
		t.Error("a score-only screen in touch should be seen")
	}

	// The match-day screen's key still does everything.
	if code, st := call(t, l, "GET", "/clubhouse/status", testKey, nil); code != 200 || st["question"] == nil {
		t.Fatalf("the match-day key: %d %v", code, st)
	}
	if code, pair := call(t, l, "GET", "/clubhouse/pair", testKey, nil); code != 200 || pair["role"] != "control" {
		t.Fatalf("the match-day key's pairing: %d %v", code, pair)
	}
}

func TestLinkScoreOnlyAlone(t *testing.T) {
	// Score-only screens without a match-day screen: the link opens on the one key, and an
	// empty or made-up key never matches the missing one.
	h := &fakeHost{}
	l := startTestLinkBoth(t, h, "", testViewKey)
	if code, _ := call(t, l, "GET", "/clubhouse/status", testViewKey, nil); code != 200 {
		t.Fatalf("score-only key: %d", code)
	}
	for _, k := range []string{"", "00000000", testKey} {
		if code, _ := call(t, l, "POST", "/clubhouse/start", k, M{}); code != 403 {
			t.Errorf("key %q with no match-day screen: %d", k, code)
		}
	}
	if len(h.started) != 0 {
		t.Fatal("started with no match-day key")
	}
}

func TestScreenScoreOnly(t *testing.T) {
	var mu sync.Mutex // the screen's background reads land here too
	var proxied []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		proxied = append(proxied, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	h := &questionHost{}
	h.server, h.ready = srv.URL, true
	l := startTestLinkBoth(t, h, testKey, testViewKey)
	sentWake := false
	defer swap(&wake, func([]string) (int, error) { sentWake = true; return 1, nil })()
	ts := newTestScreen(t)
	code, out := ts.api("POST", "pair", M{"key": testViewKey, "address": l.Addr().String()})
	if code != 200 || out["view_only"] != true || out["warning"] != nil {
		t.Fatalf("pair: %d %v", code, out)
	}
	if c := loadScreenConfig(); !c.View || len(c.MACs) != 0 {
		t.Fatalf("saved: %+v", c)
	}
	// A PIN guards the screen's settings, but the score is never behind it.
	if code, _ := ts.api("POST", "pin", M{"pin": "1234"}); code != 200 {
		t.Fatal("set PIN")
	}
	ts.api("POST", "lock", M{})
	ts.pollOnce()
	_, st := ts.api("GET", "status", nil)
	if st["view_only"] != true || st["can_wake"] != false || st["reachable"] != true {
		t.Fatalf("status: %v", st)
	}
	if code, _ := ts.api("GET", "server/live/view", nil); code != 200 {
		t.Fatalf("the score, locked: %d", code)
	}
	if code, _ := ts.api("GET", "server/match/scorecard", nil); code != 200 {
		t.Fatalf("the scorecard, locked: %d", code)
	}
	// Unlocked or not, nothing that changes anything leaves the screen.
	ts.api("POST", "unlock", M{"pin": "1234"})
	for _, p := range []string{"laptop/start", "laptop/stop", "laptop/answer", "laptop/poweroff", "wake",
		"server/checklist/action", "server/youtube/update"} {
		if code, _ := ts.api("POST", p, M{}); code != 403 {
			t.Errorf("%s on a score-only screen: %d, want 403", p, code)
		}
	}
	if code, _ := ts.api("GET", "server/cameras/discover", nil); code != 403 {
		t.Errorf("an operator's read on a score-only screen: %d", code)
	}
	if sentWake || len(h.started) != 0 || len(h.stopped) != 0 {
		t.Fatal("a score-only screen woke or drove the laptop")
	}
	// (The screen also reads /state and the badge in the background, for the club's look.)
	mu.Lock()
	defer mu.Unlock()
	for _, p := range proxied {
		m, path, _ := strings.Cut(p, " ")
		if !ViewAllowed(m, path) {
			t.Errorf("reached the server: %s", p)
		}
	}
	// Forgetting it forgets that it was score-only too.
	if code, _ := ts.api("POST", "forget", M{}); code != 200 {
		t.Fatal("forget")
	}
	if c := loadScreenConfig(); c.View || c.paired() {
		t.Fatalf("after forget: %+v", c)
	}
}
