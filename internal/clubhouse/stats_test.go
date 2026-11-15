package clubhouse

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cricketstream/internal/assets"
)

// runningHost is a laptop with match day running and its server up.
type runningHost struct{ fakeHost }

func (h *runningHost) Status() map[string]any {
	return map[string]any{"phase": "running", "club": "Test CC", "server_ready": true}
}

func TestScreenKeepsItsOwnCopyOfTheClubStats(t *testing.T) {
	var ready atomic.Bool
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/club/stats" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		asked.Add(1)
		if !ready.Load() {
			w.Write([]byte(`{"ready":false}`)) // still adding them up
			return
		}
		w.Write([]byte(`{"ready":true,"as_of":"2026-11-15","fields":["apps"],"players":[{"name":"Karl Jones","b":{"100:m":[12]}}]}`))
	}))
	defer srv.Close()
	h := &runningHost{}
	h.server, h.token, h.ready = srv.URL, "tok", true
	l := startTestLink(t, h, true)
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "pair", M{"key": testKey, "address": l.Addr().String()}); code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	// Nothing yet: the screen says so.
	if code, out := ts.api("GET", "stats", nil); code != 200 || out["none"] != true {
		t.Fatalf("before: %d %v", code, out)
	}
	// The laptop's still building them: nothing kept, and it doesn't ask again at once.
	ts.pollOnce()
	ts.bg.Wait()
	if _, err := os.Stat(statsFile()); err == nil || asked.Load() != 1 {
		t.Fatalf("kept an unready table, or asked %d times", asked.Load())
	}
	ts.pollOnce()
	ts.bg.Wait()
	if asked.Load() != 1 {
		t.Fatalf("asked again straight away (%d)", asked.Load())
	}
	// A couple of minutes later they're ready, and kept.
	ready.Store(true)
	ts.mu.Lock()
	ts.statsTried = time.Now().Add(-statsRetry)
	ts.mu.Unlock()
	ts.pollOnce()
	ts.bg.Wait()
	code, out := ts.api("GET", "stats", nil)
	if code != 200 || out["fetched"] == nil || len(out["players"].([]any)) != 1 {
		t.Fatalf("after: %d %v", code, out)
	}
	// Fresh: not asked for again this week, however often the laptop's seen.
	ts.mu.Lock()
	ts.statsTried = time.Time{}
	ts.mu.Unlock()
	ts.pollOnce()
	ts.bg.Wait()
	if asked.Load() != 2 {
		t.Fatalf("asked for a fresh table again (%d)", asked.Load())
	}
	// A week old: asked for again.
	old := time.Now().Add(-statsEvery - time.Hour)
	os.Chtimes(statsFile(), old, old)
	ts.pollOnce()
	ts.bg.Wait()
	if asked.Load() != 3 {
		t.Fatalf("a week-old table wasn't refreshed (%d)", asked.Load())
	}
	// The laptop off: the screen still has them, without the PIN.
	l.Stop()
	ts.api("POST", "pin", M{"pin": "2468"})
	ts.api("POST", "lock", M{})
	if code, out := ts.api("GET", "stats", nil); code != 200 || out["players"] == nil {
		t.Fatalf("with the laptop off and the screen locked: %d %v", code, out)
	}
}

func TestLinkLetsScreensReadTheClubStats(t *testing.T) {
	if !ProxyAllowed("GET", "/club/stats") || !ViewAllowed("GET", "/club/stats") {
		t.Fatal("both kinds of screen read the club stats")
	}
	if ProxyAllowed("POST", "/club/stats") {
		t.Fatal("and only read them")
	}
}

func TestParseExtraTabs(t *testing.T) {
	got := parseExtraTabs("Darts: darts.html, Quiz:quiz.html | Bad: ../config.ini, Worse: sub/x.html, Nope: x.js, : y.html, Four: a.html, Five: b.html")
	if len(got) != 3 || got[0].Label != "Darts" || got[0].URL != "/extra/darts.html" || got[1].File != "quiz.html" || got[2].File != "a.html" {
		t.Fatalf("%+v", got)
	}
}

func TestScreenServesOnlyItsOwnExtraTabs(t *testing.T) {
	ts := newTestScreen(t)
	ts.mu.Lock()
	ts.cfg.Extra = "Darts: darts.html"
	ts.mu.Unlock()
	if code, _, _ := ts.do("GET", "/extra/darts.html", nil, nil); code != 404 {
		t.Fatalf("missing file: %d", code)
	}
	os.WriteFile(assets.Join("darts.html"), []byte("<p>180!</p>"), 0o644)
	os.WriteFile(assets.Join("other.html"), []byte("secret"), 0o644)
	code, _, body := ts.do("GET", "/extra/darts.html", nil, nil)
	if code != 200 || string(body) != "<p>180!</p>" {
		t.Fatalf("darts: %d %s", code, body)
	}
	for _, p := range []string{"/extra/other.html", "/extra/../clubhouse.ini", "/extra/clubhouse.ini", "/extra/"} {
		if code, _, body := ts.do("GET", p, nil, nil); code == 200 {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
	// Only the screen's own address, like everything else it serves.
	if code, _, _ := ts.do("GET", "/extra/darts.html", nil, map[string]string{"Host": "evil.example"}); code != http.StatusMisdirectedRequest {
		t.Errorf("wrong host: %d", code)
	}
	_, st := ts.api("GET", "status", nil)
	tabs, _ := st["extra_tabs"].([]any)
	if len(tabs) != 1 || !strings.Contains(tabs[0].(map[string]any)["url"].(string), "darts.html") {
		t.Fatalf("status: %v", st["extra_tabs"])
	}
	// Kept when the screen rewrites its settings (a new PIN, say).
	ts.api("POST", "pin", M{"pin": "1357"})
	if loadScreenConfig().Extra != "Darts: darts.html" {
		t.Fatal("a new PIN lost the extra tabs")
	}
}
