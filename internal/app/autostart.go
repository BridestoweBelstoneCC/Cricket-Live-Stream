package app

// Start match day by itself (the code review's F9). With "Start by itself before a match"
// ticked, the window looks up today's fixture on PlayCricket and, so many minutes before
// its start time, does what pressing Start match day does: OBS opens, its scenes are
// checked, the server starts and the pre-match show goes up (the overlay's own holding
// graphics, until the scorer's feed arrives).
//
// It never goes live on YouTube: that stays a person's press, in the checklist or on the
// clubhouse screen. And it starts once a day at most: match day started by anyone today,
// by hand or from the clubhouse, means it has nothing to do, so stopping match day isn't
// undone by it a minute later.
//
// The laptop has to be on with CricketStream open ("Open CricketStream when this laptop
// starts" on the Clubhouse card does that at sign-in). Waking a laptop that's off is the
// clubhouse screen's job, by a press there.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ini"
	"cricketstream/internal/launcher"
	"cricketstream/internal/setup"
)

const (
	autoStartDefaultMinutes = 20
	// A match that's already this far in isn't "about to start": the laptop was switched
	// on late, and whoever switched it on is standing at it.
	autoStartLate = 30 * time.Minute
	// How often PlayCricket is asked for today's fixture while waiting (a fixture can be
	// entered, or its time changed, on the morning).
	autoStartRefetch = 20 * time.Minute
)

type autoStartSettings struct {
	On      bool
	Minutes int
}

func clampAutoMinutes(n int) int {
	switch {
	case n <= 0:
		return autoStartDefaultMinutes
	case n < 5:
		return 5
	case n > 120:
		return 120
	}
	return n
}

// loadAutoStart is config.ini's [App] auto_start and auto_start_minutes.
func loadAutoStart() autoStartSettings {
	f, err := ini.Load(setup.ConfigFile())
	if err != nil {
		return autoStartSettings{Minutes: autoStartDefaultMinutes}
	}
	n, _ := strconv.Atoi(strings.TrimSpace(f.Get("App", "auto_start_minutes", "")))
	return autoStartSettings{On: f.GetBool("App", "auto_start", false), Minutes: clampAutoMinutes(n)}
}

func saveAutoStart(s autoStartSettings) error {
	path := setup.ConfigFile()
	f, err := ini.Load(path)
	if err != nil {
		return fmt.Errorf("config.ini couldn't be read (%v) — run setup first", err)
	}
	f.Set("App", "auto_start", map[bool]string{true: "yes", false: "no"}[s.On])
	f.Set("App", "auto_start_minutes", strconv.Itoa(clampAutoMinutes(s.Minutes)))
	return writeConfig(path, f)
}

// autoFixture is today's match as the scheduled start needs it.
type autoFixture struct {
	Time     string // "13:00", as PlayCricket has it
	Opponent string
	Why      string // why there's none
}

// fetchAutoFixture asks PlayCricket for today's fixture with config.ini's key. Swappable.
var fetchAutoFixture = func() autoFixture {
	f, err := ini.Load(setup.ConfigFile())
	if err != nil {
		return autoFixture{Why: "config.ini couldn't be read"}
	}
	m, why := launcher.FetchTodaysMatch(strings.TrimSpace(f.Get("API", "playcricket_key", "")),
		strings.TrimSpace(f.Get("Club", "playcricket_id", "")))
	if m == nil {
		return autoFixture{Why: why}
	}
	return autoFixture{Time: strings.TrimSpace(m.Time), Opponent: m.AwayTeam}
}

// autoNow is the clock, swappable.
var autoNow = time.Now

// matchStart is today's date at a fixture's "13:00" (or "1:00 PM"), in local time.
func matchStart(now time.Time, hhmm string) (time.Time, bool) {
	hhmm = strings.ToUpper(strings.TrimSpace(hhmm))
	for _, layout := range []string{"15:04", "15.04", "3:04 PM", "3:04PM", "3.04PM", "15:04:05"} {
		if t, err := time.Parse(layout, hhmm); err == nil {
			return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location()), true
		}
	}
	return time.Time{}, false
}

// autoStartDue: it's time. From `minutes` before the start until autoStartLate after it.
func autoStartDue(now, start time.Time, minutes int) bool {
	return !now.Before(start.Add(-time.Duration(minutes)*time.Minute)) && now.Before(start.Add(autoStartLate))
}

// autoState is what the scheduled start remembers (under its own lock).
type autoState struct {
	mu        sync.Mutex
	doneDay   string // the day match day was last started on, by anyone
	fixDay    string
	fixAt     time.Time
	fix       autoFixture
	lastError string
}

func day(t time.Time) string { return t.Format("2006-01-02") }

// autoStartTick is one look: start match day if it's time. True if it did.
func (a *App) autoStartTick() bool {
	now := autoNow()
	today := day(now)
	phase := a.currentPhase()
	st := &a.auto
	st.mu.Lock()
	if phase == phStarting || phase == phRunning {
		st.doneDay = today // started today, however: nothing to do until tomorrow
	}
	done := st.doneDay == today
	st.mu.Unlock()
	set := loadAutoStart()
	if !set.On || done || setup.Role() != setup.RoleStreaming || (phase != phHome && phase != phStopped) {
		return false
	}
	if a.ask.Current() != nil {
		return false // someone's being asked something: they're at the laptop
	}
	st.mu.Lock()
	if st.fixDay != today || now.Sub(st.fixAt) >= autoStartRefetch {
		st.mu.Unlock()
		fix := fetchAutoFixture() // the network: not under the lock
		st.mu.Lock()
		st.fixDay, st.fixAt, st.fix = today, now, fix
	}
	fix := st.fix
	st.mu.Unlock()
	start, ok := matchStart(now, fix.Time)
	if !ok || !autoStartDue(now, start, set.Minutes) {
		return false
	}
	remote, _ := homeInfo()["remote"].(bool) // the club's saved choice, as the clubhouse screen uses
	if err := a.startMatchDay(remote, false, false); err != nil {
		st.mu.Lock()
		st.lastError = err.Error()
		st.mu.Unlock()
		return false
	}
	st.mu.Lock()
	st.doneDay, st.lastError = today, ""
	st.mu.Unlock()
	who := ""
	if fix.Opponent != "" {
		who = " v " + fix.Opponent
	}
	a.notify(notice{Title: "Match day has started by itself",
		Text: fmt.Sprintf("Today's match%s is at %s. OBS is opening and the pre-match show is going up. Nothing goes live on YouTube until someone presses Go live.",
			who, start.Format("15:04"))})
	return true
}

// autoStartLoop looks every so often for as long as the app runs.
func (a *App) autoStartLoop(every time.Duration) {
	for {
		select {
		case <-a.done:
			return
		case <-time.After(every):
		}
		func() {
			defer func() { recover() }()
			a.autoStartTick()
		}()
	}
}

// autoStartInfo is the home page's card: the setting, and what it's waiting for.
func (a *App) autoStartInfo() map[string]any {
	set := loadAutoStart()
	out := map[string]any{"on": set.On, "minutes": set.Minutes, "note": ""}
	if !set.On {
		return out
	}
	now := autoNow()
	st := &a.auto
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.doneDay == day(now):
		out["note"] = "Match day has already been started today."
	case st.fixDay != day(now):
		out["note"] = "Looking up today's fixture..."
	case st.fix.Time == "":
		why := st.fix.Why
		if why == "" {
			why = "today's fixture has no start time on PlayCricket"
		}
		out["note"] = "Nothing to start for: " + why + "."
	default:
		start, ok := matchStart(now, st.fix.Time)
		if !ok {
			out["note"] = "Today's fixture's time (" + st.fix.Time + ") isn't one CricketStream can read."
			break
		}
		at := start.Add(-time.Duration(set.Minutes) * time.Minute)
		who := ""
		if st.fix.Opponent != "" {
			who = " v " + st.fix.Opponent
		}
		switch {
		case now.Before(at):
			out["note"] = fmt.Sprintf("Today's match%s is at %s: match day starts by itself at %s.", who, start.Format("15:04"), at.Format("15:04"))
		case !now.Before(start.Add(autoStartLate)):
			out["note"] = fmt.Sprintf("Today's match%s started at %s: too late to start by itself. Press Start match day.", who, start.Format("15:04"))
		default:
			out["note"] = fmt.Sprintf("Today's match%s is at %s: starting.", who, start.Format("15:04"))
		}
	}
	if st.lastError != "" {
		out["note"] = "It couldn't start by itself: " + st.lastError
	}
	return out
}
