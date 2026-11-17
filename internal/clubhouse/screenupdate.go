package clubhouse

// Updating the screen while it's running. At boot it checks by itself (update.go), but a
// screen left on for weeks, or one whose wifi took longer than updNetWait to join, would
// sit on an old version, and nobody could see which (the club's Pi was found on beta 9 the
// morning after beta 10, 2026-11-16). So:
//   - it tells the laptop its version on every request (VersionHeader), and the laptop's
//     card and pre-match check say when it's behind;
//   - "Check for an update" in its settings (behind the PIN), or the laptop's "Update the
//     screen" (a time in the link's status), checks now;
//   - it checks again by itself every day, only while match day isn't on and nobody has
//     touched it for a while;
//   - a check that couldn't reach GitHub (the boot one, with the wifi still joining) is
//     tried again every 15 minutes, not left for the next day;
//   - it follows the laptop: seeing the laptop on a newer version, it checks at the next
//     quiet moment, so updating the laptop updates the screen with nobody pressing anything.
// An update found while running is installed, then the screen exits the way a stop does
// (Chromium closed) and the start script's loop runs the new copy.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VersionHeader carries the screen's version to the laptop.
const VersionHeader = "X-Clubhouse-Version"

// Swappable for tests.
var (
	screenUpdateTo  = updateTo
	updRecheckFirst = 4 * time.Hour    // the first quiet check after the one at boot
	updRecheckEvery = 24 * time.Hour   // and from then on
	updRetryNoNet   = 15 * time.Minute // after a check that couldn't reach GitHub
	updQuietFor     = 10 * time.Minute
	updQuietTick    = 5 * time.Minute
	updRestartDelay = 3 * time.Second // long enough for the page to say so
)

type screenUpdate struct {
	running    bool
	state      string // the last check's outcome (updDone, updUpToDate, ...)
	at         time.Time
	lastCheck  time.Time
	handledAsk int64 // the laptop's last "update the screen", once dealt with
	// The laptop's version when it's newer than this copy, and the one already checked
	// for: one check per laptop version, so a release the Pi can't take isn't asked for
	// every few minutes.
	followWant, followDone string
	restart                chan struct{}
	once                   sync.Once
}

// updateNow checks for an update in the background (false if one is already running).
func (s *Screen) updateNow(why string) bool {
	s.mu.Lock()
	if s.upd.running {
		s.mu.Unlock()
		return false
	}
	s.upd.running, s.upd.lastCheck, s.upd.followDone = true, time.Now(), s.upd.followWant
	s.mu.Unlock()
	s.background(func() {
		fmt.Printf("  Checking for an update (%s)\n", why)
		ok, state := screenUpdateTo(s.o.Version, s.o.Newer)
		s.mu.Lock()
		s.upd.running, s.upd.state, s.upd.at = false, state, time.Now()
		s.mu.Unlock()
		if ok {
			s.restartForUpdate()
		}
	})
	return true
}

// restartForUpdate ends RunScreen so the start script runs the new copy. Only under the
// start script (--kiosk): run by hand, nothing would start it again.
func (s *Screen) restartForUpdate() {
	if !s.o.Kiosk {
		fmt.Println("  Restart the screen to use the new version.")
		return
	}
	time.AfterFunc(updRestartDelay, func() { s.upd.once.Do(func() { close(s.upd.restart) }) })
}

// laptopAsked is the link's status asking for an update (unix seconds), seen by pollOnce
// with s.mu held. An ask from before this copy started is the one that brought it here.
func (s *Screen) laptopAsked(st map[string]any) bool {
	v, _ := st["update_screen"].(float64)
	at := int64(v)
	if at <= 0 || at == s.upd.handledAsk || at < s.started.Unix() {
		return false
	}
	s.upd.handledAsk = at
	return true
}

// noteIdle: the page's status poll says how long since the screen was touched.
func (s *Screen) noteIdle(q string) {
	n, err := strconv.Atoi(q)
	if err != nil || n < 0 {
		return
	}
	s.mu.Lock()
	s.lastTouch = time.Now().Add(-time.Duration(n) * time.Second)
	s.mu.Unlock()
}

// noteLaptopVersion: the laptop's status says its version; newer than this copy, the screen
// follows it at the next quiet moment. s.mu held.
func (s *Screen) noteLaptopVersion(st map[string]any) {
	lv, _ := st["version"].(string)
	lv = strings.TrimPrefix(strings.TrimSpace(lv), "v")
	have := strings.TrimPrefix(strings.TrimSpace(s.o.Version), "v")
	if lv == "" || have == "" || s.o.Newer == nil || lv == s.upd.followWant || !s.o.Newer(lv, have) {
		return
	}
	s.upd.followWant = lv
	fmt.Printf("  The streaming laptop is on %s (this screen: %s) — updating when the screen's quiet\n", lv, have)
}

// quietForUpdate: a check is due, match day isn't on, and nobody's using the screen.
func (s *Screen) quietForUpdate(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upd.running {
		return false
	}
	due := now.Sub(s.started) >= updRecheckFirst
	if !s.upd.lastCheck.IsZero() {
		due = now.Sub(s.upd.lastCheck) >= updRecheckEvery ||
			s.upd.state == updCantCheck && now.Sub(s.upd.lastCheck) >= updRetryNoNet
	}
	if s.upd.followWant != "" && s.upd.followWant != s.upd.followDone {
		due = true
	}
	phase, _ := s.laptop["phase"].(string)
	return due && now.Sub(s.lastTouch) >= updQuietFor && phase != "running" && phase != "starting"
}

// updateWhenQuiet is the daily check (kiosk only, and only with auto_update on).
func (s *Screen) updateWhenQuiet() {
	t := time.NewTicker(updQuietTick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if autoUpdateOn() && s.quietForUpdate(time.Now()) {
			s.updateNow("the daily check, while the screen's quiet")
		}
	}
}

// updateStatus is the settings dialog's view of it.
func (s *Screen) updateStatus() map[string]any {
	out := map[string]any{"running": s.upd.running, "state": s.upd.state}
	if !s.upd.at.IsZero() {
		out["ago"] = int(time.Since(s.upd.at).Seconds())
	}
	return out
}
