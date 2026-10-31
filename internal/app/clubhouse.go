package app

// The clubhouse screen's link (internal/clubhouse), run by the window on the streaming
// laptop when setup has [Clubhouse] enabled = yes, or view_enabled = yes for score-only
// screens (which can't start, stop or switch anything). It lives here, not in the server,
// because it has to outlive match day: it answers before match day starts (to start it)
// and after it ends (to switch the laptop off).

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"cricketstream/internal/agent"
	"cricketstream/internal/clubhouse"
	"cricketstream/internal/ini"
	"cricketstream/internal/setup"
)

// clubhouseSettings is setup's [Clubhouse] section.
type clubhouseSettings struct {
	Enabled       bool
	Key           string
	AllowPowerOff bool
	ViewEnabled   bool   // score-only screens may connect
	ViewKey       string // their key
}

func loadClubhouseSettings() (clubhouseSettings, string) {
	f, err := ini.Load(setup.ConfigFile())
	if err != nil {
		return clubhouseSettings{}, ""
	}
	return clubhouseSettings{
		Enabled:       f.GetBool("Clubhouse", "enabled", false),
		Key:           clubhouse.NormaliseKey(f.Get("Clubhouse", "key", "")),
		AllowPowerOff: f.GetBool("Clubhouse", "allow_power_off", false),
		ViewEnabled:   f.GetBool("Clubhouse", "view_enabled", false),
		ViewKey:       clubhouse.NormaliseKey(f.Get("Clubhouse", "view_key", "")),
	}, f.Get("Club", "name", "")
}

// saveClubhouseSettings merges [Clubhouse] into config.ini, as setup's own save does
// (everything else in the file is kept).
func saveClubhouseSettings(s clubhouseSettings) error {
	path := setup.ConfigFile()
	f, err := ini.Load(path)
	if err != nil {
		return fmt.Errorf("config.ini couldn't be read (%v) — run setup first", err)
	}
	yes := map[bool]string{true: "yes", false: "no"}
	f.Set("Clubhouse", "enabled", yes[s.Enabled])
	f.Set("Clubhouse", "key", s.Key)
	f.Set("Clubhouse", "allow_power_off", yes[s.AllowPowerOff])
	f.Set("Clubhouse", "view_enabled", yes[s.ViewEnabled])
	f.Set("Clubhouse", "view_key", s.ViewKey)
	return writeConfig(path, f)
}

var writeConfig = func(path string, f *ini.File) error {
	data := append([]byte(setup.Header), f.Bytes()...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// startClubhouse opens the link if setup turns it on (the streaming laptop only).
func (a *App) startClubhouse() {
	a.stopClubhouse()
	s, club := loadClubhouseSettings()
	if !s.Enabled && !s.ViewEnabled {
		return
	}
	if s.missingKey() {
		// Switched on, but no key of its own: settings moved from another laptop (the keys
		// are this laptop's and never travel), or hand-edited. A fresh one; pair again.
		s.fillKeys()
		if err := saveClubhouseSettings(s); err != nil {
			a.mu.Lock()
			a.chErr = "The clubhouse screen's pairing key couldn't be saved: " + err.Error()
			a.mu.Unlock()
			return
		}
	}
	cfg := clubhouse.Config{AllowPowerOff: s.AllowPowerOff, Name: club}
	if s.Enabled {
		cfg.Key = s.Key
	}
	if s.ViewEnabled {
		cfg.ViewKey = s.ViewKey
	}
	l, err := startLink(cfg, chHost{a})
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.chErr = "The clubhouse screen can't connect: " + err.Error()
		fmt.Println("  ✗  " + a.chErr)
		return
	}
	a.chLink, a.chErr = l, ""
	fmt.Printf("  ✓  Clubhouse screen can connect on %s:%d\n", agent.LocalIP(), clubhouse.Port())
}

// missingKey: a kind of screen is switched on without its key.
func (s clubhouseSettings) missingKey() bool {
	return (s.Enabled && len(s.Key) != clubhouse.KeyLen) || (s.ViewEnabled && len(s.ViewKey) != clubhouse.KeyLen)
}

func (s *clubhouseSettings) fillKeys() {
	if s.Enabled && len(s.Key) != clubhouse.KeyLen {
		s.Key = clubhouse.NewKey()
	}
	if s.ViewEnabled && len(s.ViewKey) != clubhouse.KeyLen {
		s.ViewKey = clubhouse.NewKey()
	}
}

func (a *App) stopClubhouse() {
	a.mu.Lock()
	l := a.chLink
	a.chLink, a.chErr = nil, ""
	a.mu.Unlock()
	if l != nil {
		l.Stop()
	}
}

// startLink is clubhouse.StartLink (a test seam: no real port in the window's tests).
var startLink = clubhouse.StartLink

// clubhouseInfo is the match-day screen's Clubhouse card.
func (a *App) clubhouseInfo() map[string]any {
	s, _ := loadClubhouseSettings()
	a.mu.Lock()
	l, errText := a.chLink, a.chErr
	a.mu.Unlock()
	out := map[string]any{"enabled": s.Enabled, "allow_power_off": s.AllowPowerOff, "error": errText,
		"login_supported": clubhouse.StartAtLoginSupported, "port": clubhouse.Port(), "ip": agent.LocalIP(),
		"view_enabled": s.ViewEnabled}
	if s.Enabled {
		out["key"] = clubhouse.FormatKey(s.Key)
	}
	if s.ViewEnabled {
		out["view_key"] = clubhouse.FormatKey(s.ViewKey)
	}
	if clubhouse.StartAtLoginSupported {
		on, this := clubhouse.StartAtLogin()
		out["start_at_login"], out["start_at_login_here"] = on, this
	}
	if l != nil {
		if seen, from := l.Seen(); !seen.IsZero() {
			out["seen_ago"], out["seen_from"] = int(time.Since(seen).Seconds()), from
		}
		if seen, from := l.SeenView(); !seen.IsZero() {
			out["view_seen_ago"], out["view_seen_from"] = int(time.Since(seen).Seconds()), from
		}
	}
	nics := clubhouse.LocalNICs(agent.LocalIP())
	wired := false
	for _, n := range nics {
		wired = wired || n.Wired
	}
	out["nics"], out["wired"] = nics, wired
	return out
}

// clubhouseChange is the card's switches. A nil field is left as it is.
type clubhouseChange struct {
	Enabled       *bool `json:"enabled"`
	AllowPowerOff *bool `json:"allow_power_off"`
	NewKey        *bool `json:"new_key"`
	StartAtLogin  *bool `json:"start_at_login"`
	ViewEnabled   *bool `json:"view_enabled"`
	NewViewKey    *bool `json:"new_view_key"`
}

func (a *App) setClubhouse(c clubhouseChange) error {
	s, _ := loadClubhouseSettings()
	if c.Enabled != nil {
		s.Enabled = *c.Enabled
	}
	if c.AllowPowerOff != nil {
		s.AllowPowerOff = *c.AllowPowerOff
	}
	if c.ViewEnabled != nil {
		s.ViewEnabled = *c.ViewEnabled
	}
	if c.NewKey != nil && *c.NewKey {
		s.Key = clubhouse.NewKey()
		fmt.Println("  New clubhouse pairing key — pair the screen again with it.")
	}
	if c.NewViewKey != nil && *c.NewViewKey {
		s.ViewKey = clubhouse.NewKey()
		fmt.Println("  New score-only key — pair the score-only screens again with it.")
	}
	s.fillKeys()
	login := c.StartAtLogin
	if login != nil {
		if err := clubhouse.SetStartAtLogin(*login); err != nil {
			return err
		}
	}
	if c.Enabled != nil || c.AllowPowerOff != nil || c.NewKey != nil || c.ViewEnabled != nil || c.NewViewKey != nil {
		if err := saveClubhouseSettings(s); err != nil {
			return err
		}
		switch a.currentPhase() {
		case phHome, phStarting, phRunning, phStopped:
			a.startClubhouse() // reopened with the new settings (a new key locks the old one out)
		}
	}
	return nil
}

// ── What the link asks of the window ──

// chHost is the window as the link's Host.
type chHost struct{ a *App }

// chLogTail: the last few activity lines, for "Starting…" on the screen.
const chLogTail = 8

func (h chHost) Status() map[string]any {
	a := h.a
	a.mu.Lock()
	phase, startLine, panel := a.phase, a.startLine, a.panelURL
	a.mu.Unlock()
	info := homeInfo()
	out := map[string]any{"phase": phase, "version": a.opts.Version, "club": info["club"],
		"manual_session": info["manual_session"], "server_ready": phase == phRunning && panel != ""}
	if q := a.ask.Current(); q != nil {
		out["question"] = q
	} else {
		out["question"] = nil
	}
	_, next := a.log.Since(1 << 30)
	from := next - chLogTail
	if (phase == phStarting || phase == phRunning || phase == phStopped) && from < startLine {
		from = startLine
	}
	lines, _ := a.log.Since(from)
	texts := []string{}
	for _, l := range lines {
		if t := strings.TrimSpace(l.Text); t != "" && !strings.HasPrefix(t, "─") {
			texts = append(texts, t)
		}
	}
	out["log"] = texts
	if phase == phHome || phase == phStopped {
		out["obs_open"] = a.obsOpenCached()
	}
	return out
}

func (h chHost) StartMatchDay(manual bool) error {
	remote, _ := homeInfo()["remote"].(bool) // the club's saved choice for phones and tablets
	return h.a.startMatchDay(remote, manual)
}

func (h chHost) Answer(id int, yes bool, text string, dismissed bool) bool {
	return h.a.ask.Answer(id, answer{Yes: yes, Text: text, Dismissed: dismissed})
}

func (h chHost) StopMatchDay(closeOBS bool) error {
	switch h.a.currentPhase() {
	case phStarting:
		// Stopping while getting ready closes the whole window (stop()): never from afar.
		return errors.New("Still getting ready — wait until match day has started, then stop it.")
	case phRunning:
		go h.a.stopThenClose(false, closeOBS)
		return nil
	}
	return errors.New("Match day isn't running.")
}

func (h chHost) ReadyToPowerOff() (bool, string) {
	switch h.a.currentPhase() {
	case phStarting, phRunning:
		return false, "Match day is still running on the laptop — end it first."
	}
	if h.a.ask.Current() != nil {
		return false, "The laptop is waiting for an answer — answer it first."
	}
	if obsOpen() {
		// Closed the way the close screen does: never while streaming or recording.
		if ok, why := finishOBS(); !ok {
			return false, why
		}
	}
	return true, ""
}

func (h chHost) Server() (string, string) {
	a := h.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase != phRunning || a.panelURL == "" {
		return "", ""
	}
	return strings.TrimSuffix(a.panelURL, "/control"), a.panelToken
}
