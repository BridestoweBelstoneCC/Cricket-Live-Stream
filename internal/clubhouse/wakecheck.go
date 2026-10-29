package clubhouse

// Can the clubhouse screen switch this laptop on? The pre-match check asks, days before
// the match, because a wake that doesn't work is found out at the worst moment: in front
// of the clubhouse, with the stream due to start. What Windows will tell a program
// without administrator rights (wakecheck_windows.go), and whether a wake from the
// screen has ever actually worked (the screen reports one when it's answered).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"cricketstream/internal/assets"
)

// WakeCheck is what's known about switching this laptop on from the screen. A nil
// pointer is "couldn't tell" (not Windows, or Windows wouldn't say).
type WakeCheck struct {
	Platform     bool     // Windows: the readings below were taken
	WiredPorts   []string // the laptop's own wired network ports (their descriptions)
	CableIn      bool     // one of them is connected, with an address
	Armed        []string // of WiredPorts, those Windows has armed to wake it; nil = unknown
	FastStartup  *bool
	AutoSignIn   *bool
	LoginKnown   bool // StartAtLogin can be read here (Windows); elsewhere it's set by hand
	StartAtLogin bool // CricketStream (this copy) opens when Windows signs in
	LastWake     *WakeRecord
}

// CheckWake reads it all. Swappable for tests.
var CheckWake = func() WakeCheck {
	c := WakeCheck{LastWake: LastWake()}
	for _, n := range LocalNICs("") {
		c.CableIn = c.CableIn || n.Wired
	}
	c.LoginKnown = StartAtLoginSupported
	if on, this := StartAtLogin(); on && this {
		c.StartAtLogin = true
	}
	readPlatform(&c)
	return c
}

// WakeAdvice is the pre-match check's line: "ok" only when everything a wake needs is in
// place and a wake has worked before; otherwise "warn" — never "bad", because none of it
// is wrong days before the match (the laptop on wifi at home, say), and the check runs
// every six hours.
func WakeAdvice(c WakeCheck) (status, detail, fix string) {
	var fixes []string
	add := func(f string) { fixes = append(fixes, f) }
	if c.Platform && len(c.WiredPorts) == 0 {
		add("This laptop has no network port of its own, so the screen can't switch it on (a USB network adapter almost " +
			"never can). Switch it on by hand on match day; the screen takes over from there.")
	} else {
		if !c.CableIn {
			add("Plug the laptop into the club's network by cable — almost no laptop can be switched on over wifi.")
		}
		if c.Platform && c.Armed != nil && len(c.Armed) == 0 {
			add("Windows isn't letting the network port wake the laptop: Device Manager → Network adapters → " +
				strings.Join(c.WiredPorts, " / ") + " → Power Management → tick both \"Allow this device to wake the computer\" " +
				"and \"Only allow a magic packet\" (and Wake on LAN in the BIOS).")
		}
	}
	if c.FastStartup != nil && *c.FastStartup {
		add("Turn Fast Startup off (Control Panel → Power Options → Choose what the power buttons do → untick " +
			"\"Turn on fast startup\"): with it on, many network ports won't wake the laptop from off.")
	}
	if c.AutoSignIn != nil && !*c.AutoSignIn {
		add("Windows waits at its sign-in screen, so CricketStream can't open by itself — set it to sign in " +
			"automatically (CLUBHOUSE_SCREEN.md: Microsoft's Autologon tool).")
	}
	if c.LoginKnown && !c.StartAtLogin {
		add("Tick \"Open CricketStream when Windows starts\" on the match-day screen's Clubhouse screen card.")
	}
	worked := ""
	if w := c.LastWake; w != nil {
		worked = fmt.Sprintf("Last switched on from the clubhouse screen %s (answered after %s).",
			w.At.Local().Format("Mon 2 Jan 15:04"), plainDuration(w.AfterSec))
	}
	switch {
	case len(fixes) > 0:
		return "warn", strings.TrimSpace(fmt.Sprintf("The clubhouse screen may not be able to switch this laptop on. %s", worked)),
			strings.Join(fixes, " ")
	case worked == "":
		return "warn", "Everything a wake needs is set up, but the screen hasn't switched this laptop on yet.",
			"Try it once before match day: shut the laptop down and press Start the stream on the screen."
	}
	return "ok", worked, ""
}

// armedMatches: powercfg names a device as Device Manager does, which adds " (2)" and so
// on when two share a description; the adapter list doesn't.
func armedMatches(armed, desc string) bool {
	if strings.EqualFold(armed, desc) {
		return true
	}
	i := strings.LastIndex(armed, " (")
	if i <= 0 || !strings.HasSuffix(armed, ")") {
		return false
	}
	n := armed[i+2 : len(armed)-1] // a count, not a model number like "(I219-V)"
	return n != "" && strings.Trim(n, "0123456789") == "" && strings.EqualFold(armed[:i], desc)
}

func plainDuration(sec int) string {
	if sec < 90 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm %02ds", sec/60, sec%60)
}

// ── The last wake that worked ──

// WakeRecord is a wake the screen sent that the laptop then answered.
type WakeRecord struct {
	At       time.Time `json:"at"`
	AfterSec int       `json:"after_seconds"` // from the wake to the first answer
	From     string    `json:"from"`          // the screen's address
}

func wakeFile() string { return assets.Join("clubhouse_wake.json") }

// LastWake is the last wake the screen reported, or nil.
func LastWake() *WakeRecord {
	b, err := os.ReadFile(wakeFile())
	if err != nil {
		return nil
	}
	var w WakeRecord
	if json.Unmarshal(b, &w) != nil || w.At.IsZero() {
		return nil
	}
	return &w
}

func saveWake(w WakeRecord) error {
	b, _ := json.Marshal(w)
	tmp := wakeFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, wakeFile())
}
