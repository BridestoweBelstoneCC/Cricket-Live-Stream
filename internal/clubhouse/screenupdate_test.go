package clubhouse

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubUpdate stands in for the real update (GitHub and a download), counting its calls.
func stubUpdate(t *testing.T, ok bool, state string) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	restore := swap(&screenUpdateTo, func(string, func(a, b string) bool) (bool, string) {
		mu.Lock()
		n++
		mu.Unlock()
		return ok, state
	})
	t.Cleanup(restore)
	t.Cleanup(swap(&updRestartDelay, 10*time.Millisecond))
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func pairedScreen(t *testing.T, version string) (*testScreen, *Link) {
	t.Helper()
	l := startTestLink(t, &fakeHost{}, false)
	addr := l.Addr().String()
	t.Cleanup(swap(&discover, func(time.Duration) []string { return []string{addr} }))
	ts := newTestScreen(t) // also points assets (clubhouse_screen.json) at a temp folder
	ts.o.Version = version
	if code, out := ts.api("POST", "pair", M{"key": testKey}); code != 200 {
		t.Fatalf("pair: %d %v", code, out)
	}
	return ts, l
}

// The laptop learns the screen's version from its requests, and keeps it for the
// pre-match check. Found 2026-11-16: the club's Pi was on beta 9 and nobody could tell.
func TestLinkLearnsTheScreensVersion(t *testing.T) {
	ts, l := pairedScreen(t, "3.0.0-beta.9")
	ts.pollOnce()
	if c, v := l.Versions(); c != "3.0.0-beta.9" || v != "" {
		t.Fatalf("versions: %q %q", c, v)
	}
	if rec := LastScreenVersions(); rec == nil || rec.Control != "3.0.0-beta.9" {
		t.Fatalf("not kept for the pre-match check: %+v", rec)
	}
	// Anything that isn't a version is never shown or saved.
	req, _ := http.NewRequest("GET", "http://"+l.Addr().String()+"/clubhouse/status", nil)
	req.Header.Set(KeyHeader, testKey)
	req.Header.Set(VersionHeader, "<b>3.0</b>")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if c, _ := l.Versions(); c != "3.0.0-beta.9" {
		t.Errorf("took %q", c)
	}
}

// "Update the screen" on the laptop reaches the screen in its next status poll, once.
func TestLaptopAsksTheScreenToUpdate(t *testing.T) {
	calls := stubUpdate(t, false, updUpToDate)
	ts, l := pairedScreen(t, "3.0.0-beta.9")
	ts.pollOnce()
	ts.bg.Wait()
	if calls() != 0 {
		t.Fatal("updated before anyone asked")
	}
	l.AskUpdate()
	ts.pollOnce()
	waitFor(t, func() bool { return calls() == 1 })
	ts.pollOnce()
	ts.pollOnce()
	ts.bg.Wait()
	if calls() != 1 {
		t.Errorf("one ask, %d checks", calls())
	}
	ts.mu.Lock()
	state := ts.upd.state
	ts.mu.Unlock()
	if state != updUpToDate {
		t.Errorf("state %q", state)
	}
	// The ask lapses: a screen switched on next week checks at its own start instead.
	t.Cleanup(swap(&updateAskFor, time.Millisecond))
	time.Sleep(5 * time.Millisecond)
	if _, st := call(t, l, "GET", "/clubhouse/status", testKey, nil); st["update_screen"] != nil {
		t.Errorf("still asking: %v", st["update_screen"])
	}
}

// An ask from before this copy started is the one that brought it here: not again.
func TestScreenIgnoresAnAskFromBeforeItStarted(t *testing.T) {
	ts := newTestScreen(t)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.laptopAsked(M{"update_screen": float64(ts.started.Unix() - 60)}) {
		t.Error("acted on an old ask")
	}
	if !ts.laptopAsked(M{"update_screen": float64(ts.started.Unix() + 1)}) {
		t.Error("ignored a new ask")
	}
	if ts.laptopAsked(M{"update_screen": float64(ts.started.Unix() + 1)}) {
		t.Error("acted twice on one ask")
	}
}

// Found an update while running: under the start script it exits for the new copy; run
// by hand it says so and carries on.
func TestScreenRestartsAfterUpdating(t *testing.T) {
	stubUpdate(t, true, updDone)
	ts := newTestScreen(t)
	ts.o.Kiosk = true
	ts.updateNow("test")
	select {
	case <-ts.upd.restart:
	case <-time.After(5 * time.Second):
		t.Fatal("didn't restart onto the new copy")
	}

	hand := newTestScreen(t)
	hand.updateNow("test")
	hand.bg.Wait()
	select {
	case <-hand.upd.restart:
		t.Fatal("a screen run by hand quit")
	case <-time.After(100 * time.Millisecond):
	}
}

// "Check for an update" on the screen is behind the PIN, and one at a time.
func TestScreenUpdateButton(t *testing.T) {
	stubUpdate(t, false, updCantCheck)
	ts := newTestScreen(t)
	if err := ts.setPIN("2468"); err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	ts.unlockedTil = time.Time{}
	ts.mu.Unlock()
	if code, _ := ts.api("POST", "update", M{}); code != 401 {
		t.Errorf("locked: %d", code)
	}
	ts.mu.Lock()
	ts.unlockedTil = time.Now().Add(time.Minute)
	ts.upd.running = true
	ts.mu.Unlock()
	if code, _ := ts.api("POST", "update", M{}); code != 409 {
		t.Errorf("while checking: %d", code)
	}
	ts.mu.Lock()
	ts.upd.running = false
	ts.mu.Unlock()
	if code, out := ts.api("POST", "update", M{}); code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	ts.bg.Wait()
	_, st := ts.api("GET", "status", nil)
	if u, _ := st["update"].(M); u["state"] != updCantCheck || u["running"] != false {
		t.Errorf("status: %v", st["update"])
	}
}

// A check that couldn't reach GitHub (the boot one, the wifi still joining) is tried again
// within the quarter hour, not the next day; still only when quiet, never in match day.
func TestScreenRetriesSoonAfterNoInternet(t *testing.T) {
	ts := newTestScreen(t)
	start := ts.started
	ts.mu.Lock()
	ts.upd.state, ts.upd.lastCheck = updCantCheck, start
	ts.mu.Unlock()
	at := func(d, idleFor time.Duration, phase string) bool {
		ts.mu.Lock()
		now := start.Add(d)
		ts.lastTouch = now.Add(-idleFor)
		ts.laptop = M{"phase": phase}
		ts.mu.Unlock()
		return ts.quietForUpdate(now)
	}
	if at(10*time.Minute, time.Hour, "home") {
		t.Error("tried again straight away")
	}
	if !at(16*time.Minute, time.Hour, "home") {
		t.Error("not tried again after a quarter of an hour")
	}
	if at(16*time.Minute, time.Minute, "home") || at(16*time.Minute, time.Hour, "running") {
		t.Error("tried while in use or during match day")
	}
	// Reached GitHub this time: back to once a day.
	ts.mu.Lock()
	ts.upd.state = updUpToDate
	ts.mu.Unlock()
	if at(30*time.Minute, time.Hour, "home") {
		t.Error("kept checking every quarter hour once GitHub had answered")
	}
}

// The laptop on a newer version: the screen follows it at the next quiet moment, once per
// laptop version, and not for one that's the same or older.
func TestScreenFollowsTheLaptopsVersion(t *testing.T) {
	ts := newTestScreen(t)
	ts.o.Version, ts.o.Newer = "3.0.0-beta.3", newerForTest
	start := ts.started
	quiet := func(d time.Duration, phase string) bool {
		ts.mu.Lock()
		now := start.Add(d)
		ts.lastTouch = now.Add(-time.Hour)
		ts.laptop = M{"phase": phase}
		ts.mu.Unlock()
		return ts.quietForUpdate(now)
	}
	see := func(v string) {
		ts.mu.Lock()
		ts.noteLaptopVersion(M{"version": v})
		ts.mu.Unlock()
	}
	for _, v := range []string{"3.0.0-beta.3", "v3.0.0-beta.3", "", "dev"} {
		see(v)
		if quiet(time.Minute, "home") {
			t.Fatalf("checked for laptop version %q", v)
		}
	}
	see("v3.0.0-beta.4")
	if quiet(time.Minute, "running") {
		t.Error("followed the laptop during match day")
	}
	if !quiet(time.Minute, "home") {
		t.Fatal("didn't follow the laptop's newer version")
	}
	var checks atomic.Int32
	saved := screenUpdateTo
	t.Cleanup(func() { screenUpdateTo = saved })
	screenUpdateTo = func(string, func(a, b string) bool) (bool, string) { checks.Add(1); return false, updUpToDate }
	ts.updateNow("following the laptop")
	ts.bg.Wait()
	see("3.0.0-beta.4") // seen again on every poll
	if quiet(2*time.Minute, "home") || checks.Load() != 1 {
		t.Errorf("asked again for the same laptop version (%d checks)", checks.Load())
	}
	see("3.0.0") // the laptop moves on again
	if !quiet(3*time.Minute, "home") {
		t.Error("didn't follow the laptop's next version")
	}
}

// The daily check waits for a quiet screen and never runs during match day.
func TestScreenUpdatesOnlyWhenQuiet(t *testing.T) {
	ts := newTestScreen(t)
	start := ts.started
	at := func(d time.Duration, idleFor time.Duration, phase string) bool {
		ts.mu.Lock()
		now := start.Add(d)
		ts.lastTouch = now.Add(-idleFor)
		ts.laptop = M{"phase": phase}
		ts.mu.Unlock()
		return ts.quietForUpdate(now)
	}
	if at(time.Hour, time.Hour, "home") {
		t.Error("checked again an hour after the one at boot")
	}
	if !at(5*time.Hour, time.Hour, "home") {
		t.Error("never checked again")
	}
	if at(5*time.Hour, time.Minute, "home") {
		t.Error("checked while someone was using it")
	}
	for _, p := range []string{"running", "starting"} {
		if at(5*time.Hour, time.Hour, p) {
			t.Errorf("checked during match day (%s)", p)
		}
	}
	ts.mu.Lock()
	ts.upd.lastCheck = start.Add(5 * time.Hour)
	ts.mu.Unlock()
	if at(6*time.Hour, time.Hour, "home") {
		t.Error("checked again within the day")
	}
	if !at(30*time.Hour, time.Hour, "home") {
		t.Error("no check the next day")
	}
	// The page's polls say how long it's been since a touch.
	ts.noteIdle("42")
	ts.mu.Lock()
	idle := time.Since(ts.lastTouch)
	ts.mu.Unlock()
	if idle < 41*time.Second || idle > 50*time.Second {
		t.Errorf("idle %v", idle)
	}
}
