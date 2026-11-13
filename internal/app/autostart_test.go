package app

// Start match day by itself (autostart.go): at the right time, once a day, never when
// someone has already started (or stopped) match day, and only when switched on.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/launcher"
)

func at(h, m int) time.Time { return time.Date(2026, 6, 13, h, m, 0, 0, time.Local) }

type autoRig struct {
	*testApp
	fetches int
	clockMu sync.Mutex // the page's status reads the clock from another goroutine
	now     time.Time
	started chan launcher.Options
	block   chan struct{}
}

func newAutoRig(t *testing.T, fix autoFixture, now time.Time) *autoRig {
	t.Helper()
	r := &autoRig{testApp: newTestApp(t), started: make(chan launcher.Options, 4), block: make(chan struct{})}
	// At the end: let any match day still "running" finish, and wait until it has. Its
	// goroutine reads the app folder, which the next test moves.
	t.Cleanup(func() {
		close(r.block)
		r.mu.Lock()
		end := r.launcherEnd
		r.mu.Unlock()
		if end != nil {
			select {
			case <-end:
			case <-time.After(5 * time.Second):
				t.Error("match day didn't finish")
			}
		}
	})
	runLauncher = func(o launcher.Options) int {
		r.started <- o
		select {
		case <-o.Interrupt:
		case <-r.block:
		}
		return 0
	}
	savedFetch, savedNow := fetchAutoFixture, autoNow
	fetchAutoFixture = func() autoFixture { r.fetches++; return fix }
	r.now = now
	autoNow = func() time.Time {
		r.clockMu.Lock()
		defer r.clockMu.Unlock()
		return r.now
	}
	t.Cleanup(func() { fetchAutoFixture, autoNow = savedFetch, savedNow })
	if rep := r.saveSetup(M{"role": "streaming", "name": "Test Town CC", "pc_id": "4242"}); rep.status != 200 {
		t.Fatalf("save: %d %s", rep.status, rep.body)
	}
	return r
}

func (r *autoRig) setNow(now time.Time) {
	r.clockMu.Lock()
	r.now = now
	r.clockMu.Unlock()
}

// stopMatchDay is someone pressing Stop: the launcher's interrupt, read as the app does.
func (r *autoRig) stopMatchDay() {
	r.mu.Lock()
	ch := r.interrupt
	r.mu.Unlock()
	ch <- nil
	r.waitFor("stopped", phaseIs(phStopped))
}

func (r *autoRig) didStart() bool {
	select {
	case <-r.started:
		return true
	case <-time.After(150 * time.Millisecond):
		return false
	}
}

func TestMatchStartTimes(t *testing.T) {
	now := at(9, 0)
	for in, want := range map[string]string{"13:00": "13:00", "13.30": "13:30", "1:00 PM": "13:00", "11:15 am": "11:15", " 18:00 ": "18:00", "13:00:00": "13:00"} {
		got, ok := matchStart(now, in)
		if !ok || got.Format("15:04") != want || got.Day() != 13 {
			t.Errorf("matchStart(%q) = %v %v, want %s today", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "TBC", "afternoon", "25:00"} {
		if _, ok := matchStart(now, bad); ok {
			t.Errorf("matchStart(%q) was read as a time", bad)
		}
	}
	start, _ := matchStart(now, "13:00")
	for _, c := range []struct {
		h, m int
		due  bool
	}{{12, 39, false}, {12, 40, true}, {13, 0, true}, {13, 29, true}, {13, 30, false}, {18, 0, false}} {
		if got := autoStartDue(at(c.h, c.m), start, 20); got != c.due {
			t.Errorf("at %02d:%02d, 20 minutes before a 13:00 start: due %v, want %v", c.h, c.m, got, c.due)
		}
	}
}

func TestAutoStartIsOffUnlessSwitchedOn(t *testing.T) {
	r := newAutoRig(t, autoFixture{Time: "13:00", Opponent: "Rival CC"}, at(12, 45))
	if r.autoStartTick() || r.didStart() {
		t.Fatal("match day started by itself with the setting off")
	}
	if r.fetches != 0 {
		t.Errorf("PlayCricket was asked %d times with the setting off", r.fetches)
	}
	st := r.status()["auto_start"].(M)
	if st["on"] != false || st["minutes"] != float64(20) {
		t.Errorf("status: %v", st)
	}
}

func TestAutoStartStartsOnceAtTheRightTime(t *testing.T) {
	r := newAutoRig(t, autoFixture{Time: "13:00", Opponent: "Rival CC"}, at(10, 0))
	if rep := r.api("POST", "autostart", M{"on": true, "minutes": 30}); rep.status != 200 || rep.json(t)["auto_start"].(M)["on"] != true {
		t.Fatalf("switching it on: %d %s", rep.status, rep.body)
	}
	// Mid-morning: it knows the fixture and says when it'll start.
	if r.autoStartTick() || r.didStart() {
		t.Fatal("started three hours early")
	}
	note, _ := r.status()["auto_start"].(M)["note"].(string)
	if !strings.Contains(note, "v Rival CC is at 13:00") || !strings.Contains(note, "by itself at 12:30") {
		t.Errorf("the home page says %q", note)
	}
	r.setNow(at(12, 29))
	if r.autoStartTick() {
		t.Fatal("started a minute early")
	}
	if r.fetches != 2 {
		t.Errorf("the fixture was fetched %d times by 12:29, want once at 10:00 and again later (its time can change)", r.fetches)
	}
	// 12:30: it starts, as the club's saved choices say, and not as a practice.
	r.setNow(at(12, 30))
	if !r.autoStartTick() {
		t.Fatal("didn't start at 12:30")
	}
	var o launcher.Options
	select {
	case o = <-r.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the launcher never ran")
	}
	if o.Practice || o.Manual == nil || *o.Manual || o.Remote == nil || *o.Remote {
		t.Errorf("started with %+v", o)
	}
	r.waitFor("getting ready", phaseIs(phStarting))
	// It doesn't start a second one, now or once this one's been stopped.
	if r.autoStartTick() {
		t.Fatal("started again while starting")
	}
	r.stopMatchDay()
	r.setNow(at(12, 45))
	if r.autoStartTick() || r.didStart() {
		t.Fatal("match day was stopped by someone, and it started it again")
	}
	if note, _ := r.status()["auto_start"].(M)["note"].(string); !strings.Contains(note, "already been started today") {
		t.Errorf("after: %q", note)
	}
	// Tomorrow is a new day.
	tomorrow := at(12, 45).AddDate(0, 0, 1)
	r.setNow(tomorrow)
	if !r.autoStartTick() || !r.didStart() {
		t.Fatal("didn't start for the next day's match")
	}
}

func TestAutoStartLeavesAMatchDaySomeoneStartedAlone(t *testing.T) {
	r := newAutoRig(t, autoFixture{Time: "13:00"}, at(11, 0))
	r.api("POST", "autostart", M{"on": true})
	// Started by hand at 11:00, stopped again (a rehearsal, a look at the cameras).
	r.api("POST", "start", M{})
	<-r.started
	r.waitFor("getting ready", phaseIs(phStarting))
	r.autoStartTick() // it sees match day running today
	r.stopMatchDay()
	r.setNow(at(12, 45))
	if r.autoStartTick() || r.didStart() {
		t.Fatal("someone started and stopped match day today; it started it again")
	}
}

func TestAutoStartNeedsAFixtureWithATime(t *testing.T) {
	for name, fix := range map[string]autoFixture{
		"no fixture":           {Why: "No matches found for today"},
		"no time":              {Opponent: "Rival CC"},
		"a time it can't read": {Time: "TBC", Opponent: "Rival CC"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAutoRig(t, fix, at(12, 45))
			r.api("POST", "autostart", M{"on": true})
			if r.autoStartTick() || r.didStart() {
				t.Fatal("started with nothing to start for")
			}
			if note, _ := r.status()["auto_start"].(M)["note"].(string); note == "" || strings.Contains(note, "starts by itself at") {
				t.Errorf("the home page says %q", note)
			}
		})
	}
	// Switched on late: a match half an hour in isn't started for.
	r := newAutoRig(t, autoFixture{Time: "13:00"}, at(13, 40))
	r.api("POST", "autostart", M{"on": true})
	if r.autoStartTick() || r.didStart() {
		t.Fatal("started 40 minutes into the match")
	}
	if note, _ := r.status()["auto_start"].(M)["note"].(string); !strings.Contains(note, "too late") {
		t.Errorf("late: %q", note)
	}
}

func TestAutoStartSettingIsKeptInConfig(t *testing.T) {
	r := newAutoRig(t, autoFixture{}, at(9, 0))
	r.api("POST", "autostart", M{"on": true, "minutes": 45})
	if s := loadAutoStart(); !s.On || s.Minutes != 45 {
		t.Fatalf("saved %+v", s)
	}
	r.api("POST", "autostart", M{"minutes": 999})
	if s := loadAutoStart(); !s.On || s.Minutes != 120 {
		t.Errorf("a silly number of minutes: %+v", s)
	}
	r.api("POST", "autostart", M{"on": false})
	if s := loadAutoStart(); s.On || s.Minutes != 120 {
		t.Errorf("switched off: %+v", s)
	}
	// Saving it loses nothing else in config.ini.
	if h := homeInfo(); h["club"] != "Test Town CC" {
		t.Errorf("config.ini lost the club: %v", h)
	}
	// Only the app's own page may change it.
	if rep := r.do("POST", "/api/autostart", M{"on": true}, nil); rep.status != 403 {
		t.Errorf("without the page's key: %d", rep.status)
	}
}
