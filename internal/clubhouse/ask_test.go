package clubhouse

// Pairing without typing: a screen asks, someone at the laptop says yes, and only that
// screen (the holder of its secret) is handed the key — once.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func askCall(t *testing.T, l *Link, path string, body any, origin string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d%s", l.Addr().(*net.TCPAddr).Port, path), bytes.NewReader(b))
	if origin != "" {
		req.Header.Set("Origin", origin)
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

func TestAskYesHandsTheKeyOverOnce(t *testing.T) {
	l := startTestLinkBoth(t, &fakeHost{}, testKey, testViewKey)
	code, out := askCall(t, l, "/clubhouse/ask", M{"secret": testSecret, "name": "pi\x1b[31m-bar" + strings.Repeat("x", 60)}, "")
	shown, _ := out["code"].(string)
	if code != 200 || len(shown) != 3 || out["club"] != "Test CC" || out["key"] != nil {
		t.Fatalf("ask: %d %v", code, out)
	}
	reqs := l.Requests()
	if len(reqs) != 1 || reqs[0].Code != shown || reqs[0].From != "127.0.0.1" {
		t.Fatalf("requests: %+v", reqs)
	}
	if n := reqs[0].Name; strings.ContainsRune(n, '\x1b') || len([]rune(n)) != maxAskName || !strings.HasPrefix(n, "pi[31m-bar") {
		t.Errorf("a screen's name is shown on the laptop: kept short and printable, got %q", n)
	}
	if _, out := askCall(t, l, "/clubhouse/ask/poll", M{"secret": testSecret}, ""); out["status"] != "waiting" || out["key"] != nil {
		t.Fatalf("before the yes: %v", out)
	}
	if err := l.Answer(reqs[0].ID, "view"); err != nil {
		t.Fatal(err)
	}
	if len(l.Requests()) != 0 {
		t.Error("answered, still listed")
	}
	if _, out := askCall(t, l, "/clubhouse/ask/poll", M{"secret": "ffffffffffffffffffffffffffffffff"}, ""); out["key"] != nil {
		t.Fatal("handed the key to the wrong secret")
	}
	_, out = askCall(t, l, "/clubhouse/ask/poll", M{"secret": testSecret}, "")
	if out["status"] != "view" || out["key"] != testViewKey {
		t.Fatalf("after the yes: %v", out)
	}
	if _, out := askCall(t, l, "/clubhouse/ask/poll", M{"secret": testSecret}, ""); out["status"] != "unknown" || out["key"] != nil {
		t.Fatalf("the key went twice: %v", out)
	}
}

func TestAskNoAndTheRules(t *testing.T) {
	l := startTestLinkBoth(t, &fakeHost{}, "", testViewKey) // score-only screens only
	if code, _ := askCall(t, l, "/clubhouse/ask", M{"secret": "short", "name": "pi"}, ""); code != 400 {
		t.Errorf("a short secret: %d", code)
	}
	if code, _ := askCall(t, l, "/clubhouse/ask", M{"secret": testSecret}, "http://evil.example"); code != 403 || len(l.Requests()) != 0 {
		t.Errorf("from a web page: %d", code)
	}
	askCall(t, l, "/clubhouse/ask", M{"secret": testSecret, "name": "pi"}, "")
	// The same address asking again replaces its request (one each).
	other := strings.Repeat("a", askSecretLen)
	askCall(t, l, "/clubhouse/ask", M{"secret": other, "name": "pi"}, "")
	reqs := l.Requests()
	if len(reqs) != 1 {
		t.Fatalf("one request per address: %+v", reqs)
	}
	if err := l.Answer(reqs[0].ID, "control"); err == nil || !strings.Contains(err.Error(), "run match day") {
		t.Fatalf("yes as a match-day screen with that switched off: %v", err)
	}
	if err := l.Answer("nope", "view"); err == nil {
		t.Fatal("answered a request that isn't there")
	}
	if err := l.Answer(reqs[0].ID, "no"); err != nil {
		t.Fatal(err)
	}
	if err := l.Answer(reqs[0].ID, "view"); err == nil {
		t.Fatal("said yes after no")
	}
	if _, out := askCall(t, l, "/clubhouse/ask/poll", M{"secret": other}, ""); out["status"] != "no" || out["key"] != nil {
		t.Fatalf("no: %v", out)
	}
}

func TestAskLimitAndExpiry(t *testing.T) {
	l := startTestLinkBoth(t, &fakeHost{}, testKey, "")
	// Many addresses (a flood): only a few at once.
	l.mu.Lock()
	l.asks = map[string]*askEntry{}
	for i := 0; i < maxAsks; i++ {
		id := fmt.Sprint(i)
		l.asks[id] = &askEntry{PairRequest: PairRequest{ID: id, Code: "00" + id, From: "10.0.0." + id}, at: time.Now()}
	}
	l.mu.Unlock()
	if code, _ := askCall(t, l, "/clubhouse/ask", M{"secret": testSecret}, ""); code != 429 {
		t.Fatalf("over the limit: %d", code)
	}
	defer swap(&askFor, time.Millisecond)()
	time.Sleep(5 * time.Millisecond)
	if len(l.Requests()) != 0 {
		t.Fatal("waited too long, still listed")
	}
	if code, _ := askCall(t, l, "/clubhouse/ask", M{"secret": testSecret}, ""); code != 200 {
		t.Fatalf("after they expired: %d", code)
	}
}

// End to end: the screen asks, shows the laptop's code, and pairs when the laptop says yes.
func TestScreenPairsFromTheLaptop(t *testing.T) {
	defer swap(&askEvery, 10*time.Millisecond)()
	l := startTestLinkBoth(t, &fakeHost{}, testKey, testViewKey)
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "ask", M{"address": l.Addr().String()}); code != 200 {
		t.Fatalf("ask: %d %v", code, out)
	}
	_, st := ts.api("GET", "status", nil)
	asking, _ := st["asking"].([]any)
	if len(asking) != 1 || st["paired"] != false {
		t.Fatalf("status while asking: %v", st)
	}
	reqs := l.Requests()
	if len(reqs) != 1 || asking[0].(M)["code"] != reqs[0].Code || asking[0].(M)["club"] != "Test CC" {
		t.Fatalf("the screen must show the laptop's code: %v vs %+v", asking, reqs)
	}
	if err := l.Answer(reqs[0].ID, "control"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return loadScreenConfig().paired() })
	c := loadScreenConfig()
	if c.Key != testKey || c.View {
		t.Fatalf("paired: %+v", c)
	}
	ts.bg.Wait()
	if _, st := ts.api("GET", "status", nil); len(st["asking"].([]any)) != 0 || st["ask_error"] != "" {
		t.Fatalf("after pairing: %v", st)
	}
}

func TestScreenAskTurnedDownOrForgotten(t *testing.T) {
	defer swap(&askEvery, 10*time.Millisecond)()
	l := startTestLinkBoth(t, &fakeHost{}, testKey, "")
	ts := newTestScreen(t)
	ts.api("POST", "ask", M{"address": l.Addr().String()})
	first := l.Requests()[0]
	// The laptop's link restarted (new settings): the request is gone, so the screen asks
	// again, with a new code to show.
	l.mu.Lock()
	l.asks = nil
	l.mu.Unlock()
	waitFor(t, func() bool { r := l.Requests(); return len(r) == 1 && r[0].ID != first.ID })
	again := l.Requests()[0]
	waitFor(t, func() bool {
		_, st := ts.api("GET", "status", nil)
		a, _ := st["asking"].([]any)
		return len(a) == 1 && a[0].(M)["code"] == again.Code
	})
	l.Answer(again.ID, "no")
	waitFor(t, func() bool {
		_, st := ts.api("GET", "status", nil)
		return st["ask_error"] == "The laptop said no."
	})
	if loadScreenConfig().paired() {
		t.Fatal("paired after a no")
	}
	// Cancelled from the screen: nothing more asked.
	ts.api("POST", "ask", M{"address": l.Addr().String()})
	ts.api("POST", "ask/cancel", M{})
	ts.bg.Wait()
	if _, st := ts.api("GET", "status", nil); len(st["asking"].([]any)) != 0 {
		t.Fatalf("after cancel: %v", st)
	}
}

func TestScreenAskNobodyThere(t *testing.T) {
	defer swap(&discover, func(time.Duration) []string { return nil })()
	ts := newTestScreen(t)
	if code, out := ts.api("POST", "ask", M{}); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "No streaming laptop answered") {
		t.Fatalf("nobody there: %d %v", code, out)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
