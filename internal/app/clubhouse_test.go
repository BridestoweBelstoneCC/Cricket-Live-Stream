package app

// The window's side of the clubhouse screen: the match-day screen's card turns the link
// on and off, and a screen holding the key drives match day exactly as the window's own
// buttons do — the launcher's questions included.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/clubhouse"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
	"cricketstream/internal/launcher"
	"cricketstream/internal/setup"
)

// screenCall is the screen asking the link something.
func screenCall(t *testing.T, port int, method, path, key string, body any) (int, M) {
	t.Helper()
	var rd *bytes.Reader
	if body == nil {
		body = M{}
	}
	b, _ := json.Marshal(body)
	rd = bytes.NewReader(b)
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), rd)
	req.Header.Set(clubhouse.KeyHeader, key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil // nothing listening
	}
	defer resp.Body.Close()
	var out M
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestClubhouseScreenRunsMatchDay(t *testing.T) {
	ta := newTestApp(t)
	t.Cleanup(ta.stopClubhouse)
	port := freePort(t)
	t.Setenv(clubhouse.PortEnv, fmt.Sprint(port))
	savedOpen := obsOpen
	obsOpen = func() bool { return false }
	t.Cleanup(func() { obsOpen = savedOpen })

	var got launcher.Options
	ran := make(chan struct{})
	runLauncher = func(o launcher.Options) int {
		got = o
		close(ran)
		o.OpenPanel("http://127.0.0.1:5000/control", "tok")
		<-o.Interrupt
		console.Confirm("Generate an AI match report for this game?", false)
		return 0
	}
	if r := ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	// Off by default: no card switched on, no port open.
	st := ta.status()
	if c := st["clubhouse"].(M); c["enabled"] != false || c["key"] != nil {
		t.Fatalf("clubhouse should start off: %v", c)
	}
	if code, _ := screenCall(t, port, "GET", "/clubhouse/hello", "", nil); code != 0 {
		t.Fatalf("the link answered while switched off (%d)", code)
	}

	// Switched on from the card: a key appears, saved in config.ini beside the rest.
	r := ta.api("POST", "clubhouse", M{"enabled": true})
	if r.status != 200 {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}
	key := r.json(t)["clubhouse"].(M)["key"].(string)
	if len(clubhouse.NormaliseKey(key)) != clubhouse.KeyLen {
		t.Fatalf("key %q", key)
	}
	f, _ := ini.Load(setup.ConfigFile())
	if !f.GetBool("Clubhouse", "enabled", false) || clubhouse.FormatKey(f.Get("Clubhouse", "key", "")) != key ||
		f.Get("Club", "name", "") != "Test Town CC" {
		t.Fatal("config.ini should hold [Clubhouse] and keep the club's own settings")
	}

	// The screen: status, then start (by hand today).
	code, s := screenCall(t, port, "GET", "/clubhouse/status", key, nil)
	if code != 200 || s["phase"] != phHome || s["club"] != "Test Town CC" {
		t.Fatalf("status: %d %v", code, s)
	}
	if code, out := screenCall(t, port, "POST", "/clubhouse/start", key, M{"manual": true}); code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	<-ran
	if got.Manual == nil || !*got.Manual || got.Remote == nil || *got.Remote {
		t.Errorf("start from the screen: manual=%v remote=%v (no club password: never remote)", got.Manual, got.Remote)
	}
	ta.waitFor("running", phaseIs(phRunning))
	if base, tok := (chHost{ta.App}).Server(); base != "http://127.0.0.1:5000" || tok != "tok" {
		t.Fatalf("server for the proxy: %q %q", base, tok)
	}
	if ok, why := (chHost{ta.App}).ReadyToPowerOff(); ok || !strings.Contains(why, "still running") {
		t.Fatalf("switching off mid-match: %v %q", ok, why)
	}
	// Switching off isn't allowed by setup: refused before anything else is looked at.
	if code, _ := screenCall(t, port, "POST", "/clubhouse/poweroff", key, M{"delay": 30}); code != 403 {
		t.Fatalf("power-off not allowed: %d", code)
	}

	// Stop from the screen: the launcher's report question shows on the screen too, and
	// the screen's answer is the one taken.
	if code, out := screenCall(t, port, "POST", "/clubhouse/stop", key, M{"close_obs": false}); code != 200 {
		t.Fatalf("stop: %d %v", code, out)
	}
	var q M
	for i := 0; i < 500 && q == nil; i++ {
		_, s = screenCall(t, port, "GET", "/clubhouse/status", key, nil)
		q, _ = s["question"].(M)
	}
	if q == nil || !strings.Contains(fmt.Sprint(q["text"]), "match report") {
		t.Fatalf("the report question didn't reach the screen: %v", s)
	}
	if code, _ := screenCall(t, port, "POST", "/clubhouse/answer", key, M{"id": q["id"], "yes": false}); code != 200 {
		t.Fatalf("answer: %d", code)
	}
	ta.waitFor("stopped", phaseIs(phStopped))
	if ok, why := (chHost{ta.App}).ReadyToPowerOff(); !ok {
		t.Fatalf("after match day with OBS closed it should be ready: %q", why)
	}
	if base, _ := (chHost{ta.App}).Server(); base != "" {
		t.Fatal("no server to proxy to once match day is over")
	}

	// A new key locks the old one out.
	r = ta.api("POST", "clubhouse", M{"new_key": true})
	newKey := r.json(t)["clubhouse"].(M)["key"].(string)
	if newKey == key {
		t.Fatal("new key is the same")
	}
	if code, _ := screenCall(t, port, "GET", "/clubhouse/status", key, nil); code != 403 {
		t.Fatalf("old key after a new one: %d", code)
	}
	if code, _ := screenCall(t, port, "GET", "/clubhouse/status", newKey, nil); code != 200 {
		t.Fatalf("new key: %d", code)
	}

	// Switched off: the port closes.
	ta.api("POST", "clubhouse", M{"enabled": false})
	if code, _ := screenCall(t, port, "GET", "/clubhouse/hello", "", nil); code == 200 {
		t.Fatal("the link still answers after being switched off")
	}
}

func TestClubhouseStopWhileGettingReadyIsRefused(t *testing.T) {
	ta := newTestApp(t)
	ta.setPhase(phStarting)
	if err := (chHost{ta.App}).StopMatchDay(true); err == nil {
		t.Fatal("stopping while getting ready closes the window — never from the clubhouse")
	}
	ta.setPhase(phHome)
	if err := (chHost{ta.App}).StopMatchDay(true); err == nil {
		t.Fatal("nothing to stop")
	}
}

// Switching off is held back by anything it would lose: a question waiting on the
// laptop, and OBS still streaming or recording (finishOBS refuses). OBS left open but
// idle is closed first, the way the close screen does it.
func TestClubhouseReadyToPowerOff(t *testing.T) {
	ta := newTestApp(t)
	ta.setPhase(phStopped)
	open, closed := true, 0
	refuse := "OBS is still streaming — stop the stream in OBS, then close it"
	savedOpen, savedFinish := obsOpen, finishOBS
	obsOpen = func() bool { return open }
	finishOBS = func() (bool, string) {
		if refuse != "" {
			return false, refuse
		}
		closed++
		open = false
		return true, ""
	}
	t.Cleanup(func() { obsOpen, finishOBS = savedOpen, savedFinish })
	h := chHost{ta.App}

	if ok, why := h.ReadyToPowerOff(); ok || why != refuse {
		t.Fatalf("OBS still streaming: %v %q", ok, why)
	}
	refuse = ""
	if ok, why := h.ReadyToPowerOff(); !ok || closed != 1 {
		t.Fatalf("OBS open but idle should be closed, then ready: %v %q closed=%d", ok, why, closed)
	}

	// A question waiting: never switched off under it.
	asked := make(chan struct{})
	go func() {
		close(asked)
		console.Confirm("Generate an AI match report for this game?", false)
	}()
	<-asked
	for i := 0; i < 500 && ta.ask.Current() == nil; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if ok, why := h.ReadyToPowerOff(); ok || !strings.Contains(why, "waiting for an answer") {
		t.Fatalf("question waiting: %v %q", ok, why)
	}
	if q := ta.ask.Current(); q != nil {
		ta.ask.Answer(q.ID, answer{Yes: false})
	}
}

func TestClubhouseCardOnlyBeforeOrAfterMatchDay(t *testing.T) {
	ta := newTestApp(t)
	ta.setPhase(phRunning)
	if r := ta.api("POST", "clubhouse", M{"enabled": true}); r.status != 409 {
		t.Fatalf("changing it mid-match: %d", r.status)
	}
}

func TestClubhouseScoreOnlyScreens(t *testing.T) {
	ta := newTestApp(t)
	t.Cleanup(ta.stopClubhouse)
	port := freePort(t)
	t.Setenv(clubhouse.PortEnv, fmt.Sprint(port))
	if r := ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	// Score-only screens on their own: the link opens with that key alone.
	r := ta.api("POST", "clubhouse", M{"view_enabled": true})
	if r.status != 200 {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}
	c := r.json(t)["clubhouse"].(M)
	viewKey, _ := c["view_key"].(string)
	if c["enabled"] != false || c["key"] != nil || len(clubhouse.NormaliseKey(viewKey)) != clubhouse.KeyLen {
		t.Fatalf("card: %v", c)
	}
	f, _ := ini.Load(setup.ConfigFile())
	if !f.GetBool("Clubhouse", "view_enabled", false) || clubhouse.FormatKey(f.Get("Clubhouse", "view_key", "")) != viewKey ||
		f.GetBool("Clubhouse", "enabled", true) {
		t.Fatal("config.ini should hold the score-only key, with the match-day screen still off")
	}
	if code, s := screenCall(t, port, "GET", "/clubhouse/status", viewKey, nil); code != 200 || s["view"] != true || s["club"] != "Test Town CC" {
		t.Fatalf("score-only status: %d %v", code, s)
	}
	if code, _ := screenCall(t, port, "POST", "/clubhouse/start", viewKey, M{"manual": true}); code != 403 {
		t.Fatalf("score-only key started match day: %d", code)
	}
	if ta.currentPhase() != phHome {
		t.Fatalf("phase %v", ta.currentPhase())
	}

	// Both on: two different keys, each its own; a new score-only key leaves the other be.
	c = ta.api("POST", "clubhouse", M{"enabled": true}).json(t)["clubhouse"].(M)
	key := c["key"].(string)
	if key == viewKey || c["view_key"] != viewKey {
		t.Fatalf("keys: %v", c)
	}
	c = ta.api("POST", "clubhouse", M{"new_view_key": true}).json(t)["clubhouse"].(M)
	if c["view_key"] == viewKey || c["key"] != key {
		t.Fatalf("new score-only key: %v", c)
	}
	if code, _ := screenCall(t, port, "GET", "/clubhouse/status", viewKey, nil); code != 403 {
		t.Fatalf("old score-only key: %d", code)
	}
	if code, _ := screenCall(t, port, "GET", "/clubhouse/status", key, nil); code != 200 {
		t.Fatalf("match-day key after a new score-only key: %d", code)
	}

	// Both off: the port closes.
	ta.api("POST", "clubhouse", M{"enabled": false})
	if code, _ := screenCall(t, port, "GET", "/clubhouse/hello", "", nil); code != 200 {
		t.Fatal("the score-only screens lost the link when the match-day screen was switched off")
	}
	ta.api("POST", "clubhouse", M{"view_enabled": false})
	if code, _ := screenCall(t, port, "GET", "/clubhouse/hello", "", nil); code == 200 {
		t.Fatal("the link still answers with both switched off")
	}
}

// A screen asking to pair shows on the card, and the card's answer reaches it.
func TestClubhouseCardAnswersAScreenAsking(t *testing.T) {
	ta := newTestApp(t)
	t.Cleanup(ta.stopClubhouse)
	port := freePort(t)
	t.Setenv(clubhouse.PortEnv, fmt.Sprint(port))
	if r := ta.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"}); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	c := ta.api("POST", "clubhouse", M{"view_enabled": true}).json(t)["clubhouse"].(M)
	viewKey := clubhouse.NormaliseKey(c["view_key"].(string))
	secret := strings.Repeat("ab", 16)
	if code, out := screenCall(t, port, "POST", "/clubhouse/ask", "", M{"secret": secret, "name": "clubhouse-pi"}); code != 200 {
		t.Fatalf("ask: %d %v", code, out)
	}
	reqs, _ := ta.status()["clubhouse"].(M)["requests"].([]any)
	if len(reqs) != 1 || reqs[0].(M)["name"] != "clubhouse-pi" {
		t.Fatalf("card: %v", reqs)
	}
	id := reqs[0].(M)["id"]
	if r := ta.api("POST", "clubhouse", M{"answer": id, "as": "control"}); r.status == 200 {
		t.Fatal("said yes as a match-day screen, which isn't switched on")
	}
	if r := ta.api("POST", "clubhouse", M{"answer": id, "as": "view"}); r.status != 200 {
		t.Fatalf("yes: %d %s", r.status, r.body)
	}
	_, out := screenCall(t, port, "POST", "/clubhouse/ask/poll", "", M{"secret": secret})
	if out["status"] != "view" || out["key"] != viewKey {
		t.Fatalf("poll: %v", out)
	}
	if f, _ := ini.Load(setup.ConfigFile()); f.GetBool("Clubhouse", "enabled", false) {
		t.Fatal("answering must not change the settings")
	}
}
