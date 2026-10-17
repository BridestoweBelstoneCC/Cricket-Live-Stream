package server

// The settings page: the first-run setup page, served by the running app at /settings
// behind the control panel's login, so everything setup asked can be changed later
// without a terminal. Saving writes config.ini exactly as setup does, then applies what
// changed to the running server straight away — the panel and the overlay show the new
// club name, colours or style on their next poll. Only the listening address and this
// machine's role wait for a restart.

import (
	"strconv"
	"strings"

	"cricketstream/internal/ini"
	"cricketstream/internal/setup"
)

func (c *ctx) settingsPage() {
	h := c.w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store, must-revalidate")
	c.securityHeaders()
	h.Set("Content-Security-Policy", setup.PageCSP)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	page := setup.Page()
	h.Set("Content-Length", strconv.Itoa(len(page)))
	c.w.WriteHeader(200)
	c.w.Write(page)
}

func (c *ctx) settingsInfo() {
	if !c.checkToken() {
		return
	}
	out := setup.Info()
	out["mode"] = "settings"
	out["port"] = Port
	c.json(out)
}

func (c *ctx) settingsCheck(body []byte) {
	code, out := setup.RunCheck(body)
	c.json(out, code)
}

// settingsSave applies a submitted settings form (login and origin already checked).
func (c *ctx) settingsSave(body []byte) {
	before, _ := ini.Load(ConfigFile)
	if before == nil {
		before = ini.New()
	}
	code, out, res := setup.Save(body)
	if res == nil {
		c.json(out, code)
		return
	}
	after, err := ini.Load(ConfigFile)
	if err != nil {
		c.json(M{"ok": false, "error": "Saved, but couldn't read config.ini back: " + err.Error()}, 500)
		return
	}
	applied := applyConfigChanges(before, after)

	var restart []string
	if before.Get("App", "role", "streaming") != after.Get("App", "role", "streaming") {
		restart = append(restart, "what this computer is for")
	}
	// The password is read live; the address the server listens on can't change under it.
	bound, oldPassword := BindHost, ClubPassword
	LoadAuthConfig()
	BindHost = bound
	if strings.TrimSpace(after.Get("Network", "bind_host", "127.0.0.1")) != strings.TrimSpace(before.Get("Network", "bind_host", "127.0.0.1")) {
		restart = append(restart, "phone and tablet access")
	}
	// Removing the password while phones on the wifi can reach the panel would open it to
	// everyone there. That waits for a restart, which falls back to this laptop only.
	if ClubPassword == "" && oldPassword != "" && bound != "127.0.0.1" && bound != "localhost" {
		ClubPassword = oldPassword
		restart = append(restart, "removing the club password (until then the panel stays locked)")
	}
	if ClubPassword != oldPassword {
		say("  ✓  Settings: club password changed")
	}
	say("  ✓  Settings saved: %s", strings.Join(append(append([]string{}, applied...), restart...), ", "))
	out["applied"], out["restart_needed"] = applied, restart
	c.json(out)
}

// applyConfigChanges copies every config.ini value that changed into the running state,
// overwriting what's there (unlike startup seeding, which only fills in blanks: here the
// operator has just said what they want). Returns the panel fields it set, secrets named
// generically.
func applyConfigChanges(before, after *ini.File) []string {
	var changed []configMapping
	for _, m := range configStateMap {
		if strings.TrimSpace(before.Get(m.section, m.key, "")) != strings.TrimSpace(after.Get(m.section, m.key, "")) {
			changed = append(changed, m)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var names []string
	UpdateState(func(s M) {
		for _, m := range changed {
			raw := strings.TrimSpace(after.Get(m.section, m.key, ""))
			if m.isInt {
				n, err := strconv.Atoi(raw)
				if err != nil {
					continue
				}
				s[m.field] = n
			} else {
				s[m.field] = raw
			}
			if isSecret(m.field) {
				names = append(names, "a saved key")
			} else {
				names = append(names, m.field)
			}
		}
	})
	return names
}
