package clubhouse

// Switching the streaming laptop off at the end of the day, when the screen asks and
// setup allows it ([Clubhouse] allow_power_off). On Windows it's Windows' own countdown
// (`shutdown /s /t N`), which shows its "you're about to be signed out" notice, carries
// on if CricketStream itself has already closed, and `shutdown /a` cancels. Elsewhere
// there is no unprivileged countdown, so the wait is this process's and the power-off
// at the end goes through the desktop (osascript on a Mac, systemd's logind on Linux).

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/proc"
)

// runCmd runs a command (swappable: tests must never switch the test machine off).
var runCmd = func(name string, args ...string) error {
	r, err := proc.Run(name, args, proc.Options{Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	if r.Code != 0 {
		msg := r.Stderr
		if msg == "" {
			msg = r.Stdout
		}
		return fmt.Errorf("%s exited %d: %s", name, r.Code, msg)
	}
	return nil
}

var goos = runtime.GOOS

var pending struct {
	sync.Mutex
	timer *time.Timer
	at    time.Time
}

// PowerOff switches this computer off after delay (at least 10 seconds — time to read
// the notice and cancel). It returns when the countdown has started.
func PowerOff(delay time.Duration, reason string) error {
	if delay < 10*time.Second {
		delay = 10 * time.Second
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	pending.Lock()
	defer pending.Unlock()
	if goos == "windows" {
		if len(reason) > 500 { // Windows' limit is 512
			reason = reason[:500]
		}
		if err := runCmd("shutdown", "/s", "/t", fmt.Sprint(int(delay.Seconds())), "/c", reason); err != nil {
			return err
		}
		pending.at = time.Now().Add(delay)
		return nil
	}
	cmd, args := "", []string(nil)
	switch goos {
	case "darwin":
		cmd, args = "osascript", []string{"-e", `tell application "System Events" to shut down`}
	case "linux":
		cmd, args = "systemctl", []string{"poweroff"}
	default:
		return errors.New("switching off isn't supported on " + goos)
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	pending.at = time.Now().Add(delay)
	pending.timer = time.AfterFunc(delay, func() {
		if err := runCmd(cmd, args...); err != nil {
			fmt.Println("  ✗ Couldn't switch this computer off: " + err.Error())
		}
	})
	return nil
}

// CancelPowerOff stops a countdown PowerOff started. No countdown is not an error.
func CancelPowerOff() error {
	pending.Lock()
	defer pending.Unlock()
	pending.at = time.Time{}
	if goos == "windows" {
		// 1116: "no shutdown was in progress" — already the state we want.
		if err := runCmd("shutdown", "/a"); err != nil && !containsCode(err, "1116") {
			return err
		}
		return nil
	}
	if pending.timer != nil {
		pending.timer.Stop()
		pending.timer = nil
	}
	return nil
}

// PowerOffAt is when a pending power-off happens (zero if none).
func PowerOffAt() time.Time {
	pending.Lock()
	defer pending.Unlock()
	if !pending.at.IsZero() && time.Now().After(pending.at.Add(time.Minute)) {
		pending.at = time.Time{} // long past: whatever happened, it isn't pending
	}
	return pending.at
}

func containsCode(err error, code string) bool {
	return err != nil && strings.Contains(err.Error(), code)
}
