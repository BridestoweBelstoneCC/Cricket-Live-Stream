package server

// The settings page (/settings): the setup page served by the running server behind the
// control panel's login, applying saved changes to the running state at once.

import (
	"os"
	"strings"
	"testing"

	"cricketstream/internal/ini"
)

const settingsConfig = "[App]\nrole = streaming\n\n[Club]\nname = Old Town CC\nabbreviation = OLD\nhome_colour = #111111\n" +
	"playcricket_id = 42\n\n[API]\nplaycricket_key = pc-secret\n\n[Graphics]\nscorebar_style = classic\n\n" +
	"[Stream]\nmax_overs = 40\n\n[Auth]\ncontrol_token = %s\nclub_password = %s\n\n[Network]\nbind_host = 127.0.0.1\n"

func settingsServer(t *testing.T, password string) *testServer {
	t.Helper()
	s := newTestServer(t, password)
	cfg := strings.Replace(strings.Replace(settingsConfig, "%s", ControlToken, 1), "%s", password, 1)
	if err := os.WriteFile(ConfigFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	SeedStateFromConfig()
	return s
}

func settingsAnswers(kv M) M {
	v := M{"role": "streaming", "name": "Old Town CC", "abbrev": "OLD", "colour": "#111111", "pc_id": "42",
		"scorebar_style": "classic", "max_overs": "40", "allow_remote": false}
	for k, x := range kv {
		v[k] = x
	}
	return v
}

func TestSettingsPageIsServedWithItsOwnPolicy(t *testing.T) {
	s := settingsServer(t, "")
	r := s.get("/settings")
	mustEq(t, r.status, 200)
	if !strings.Contains(string(r.body), "What is this computer for?") {
		t.Fatal("not the setup page")
	}
	csp := r.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src 'self'") || !strings.Contains(csp, "img-src 'self' data:") {
		t.Errorf("CSP %q", csp)
	}
	if r.header.Get("Cache-Control") == "" {
		t.Error("cacheable")
	}
}

func TestSettingsInfoNeedsTheLoginAndHidesSecrets(t *testing.T) {
	s := settingsServer(t, "hunter2")
	if st, _ := s.getJSON("/settings/info"); st != 401 {
		t.Fatalf("no login: %d", st)
	}
	st, d := s.getJSON("/settings/info", s.bearer("hunter2"))
	mustEq(t, st, 200)
	mustEq(t, d["mode"], "settings")
	ex, _ := d["existing"].(M)
	mustEq(t, ex["name"], "Old Town CC")
	b := string(s.get("/settings/info", s.bearer("hunter2")).body)
	if strings.Contains(b, "pc-secret") || strings.Contains(b, "hunter2") {
		t.Fatal("a secret reached the page")
	}
	if set, _ := d["secrets_set"].(M); set["pc_key"] != true || set["club_password"] != true {
		t.Errorf("secrets_set %v", d["secrets_set"])
	}
}

// What the operator saves is live at once: /state (what the overlay and panel read)
// shows it with no restart, even where the panel had its own value.
func TestSettingsSaveAppliesStraightAway(t *testing.T) {
	s := settingsServer(t, "")
	setState(t, M{"home_team": "Panel Edit CC"}) // the panel had changed it
	st, d := s.postJSON("/settings/save", settingsAnswers(M{"name": "New Town CC", "abbrev": "NEW",
		"colour": "#aa0011", "scorebar_style": "impact", "max_overs": "45"}), map[string]string{"Origin": "http://" + s.addr})
	mustEq(t, st, 200)
	if d["ok"] != true {
		t.Fatal(d)
	}
	state := LoadState()
	for k, want := range map[string]any{"home_team": "New Town CC", "home_abbrev": "NEW", "home_colour": "#aa0011",
		"scorebar_style": "impact"} {
		if state[k] != want {
			t.Errorf("state %s = %v, want %v", k, state[k], want)
		}
	}
	mustEq(t, gi(state, "max_overs", 0), 45)
	if state["playcricket_api_key"] != "pc-secret" {
		t.Error("a saved key was lost")
	}
	if rs, _ := d["restart_needed"].([]any); len(rs) != 0 {
		t.Errorf("nothing here needs a restart: %v", rs)
	}
	f, _ := ini.Load(ConfigFile)
	mustEq(t, f.Get("Club", "name", ""), "New Town CC")
	mustEq(t, f.Get("Auth", "control_token", ""), ControlToken)
}

func TestSettingsSaveNeedsLoginAndSameOrigin(t *testing.T) {
	s := settingsServer(t, "hunter2")
	if st, _ := s.postJSON("/settings/save", settingsAnswers(M{"name": "Hijack CC"}), map[string]string{"Origin": "http://" + s.addr}); st != 401 {
		t.Errorf("no login: %d", st)
	}
	h := s.bearer("hunter2")
	h["Origin"] = "http://evil.example"
	if st, _ := s.postJSON("/settings/save", settingsAnswers(M{"name": "Hijack CC"}), h); st != 403 {
		t.Errorf("cross-site: %d", st)
	}
	f, _ := ini.Load(ConfigFile)
	mustEq(t, f.Get("Club", "name", ""), "Old Town CC")
}

func TestSettingsPasswordChangeIsLive(t *testing.T) {
	s := settingsServer(t, "old-pw")
	h := s.bearer("old-pw")
	h["Origin"] = "http://" + s.addr
	st, _ := s.postJSON("/settings/save", settingsAnswers(M{"club_password": "new-pw"}), h)
	mustEq(t, st, 200)
	mustEq(t, ClubPassword, "new-pw")
	if st, d := s.postJSON("/login", M{"password": "new-pw"}); st != 200 || d["ok"] != true {
		t.Errorf("new password refused: %d %v", st, d)
	}
}

// With phones on the wifi able to reach the panel, removing the password must not open
// it to all of them: that waits for a restart (which falls back to this laptop only).
func TestSettingsRemovingThePasswordWhileRemoteWaitsForARestart(t *testing.T) {
	s := settingsServer(t, "pw")
	saved := BindHost
	BindHost = "0.0.0.0"
	t.Cleanup(func() { BindHost = saved })
	h := s.bearer("pw")
	h["Origin"] = "http://" + s.addr
	st, d := s.postJSON("/settings/save", settingsAnswers(M{"clear": []string{"club_password"}}), h)
	mustEq(t, st, 200)
	mustEq(t, ClubPassword, "pw")
	mustEq(t, BindHost, "0.0.0.0")
	rs, _ := d["restart_needed"].([]any)
	if len(rs) == 0 || !strings.Contains(rs[len(rs)-1].(string), "club password") {
		t.Errorf("restart_needed %v", rs)
	}
	if st, _ := s.getJSON("/settings/info"); st != 401 {
		t.Error("the panel opened up without a password")
	}
}

func TestSettingsRoleAndRemoteAccessWaitForARestart(t *testing.T) {
	s := settingsServer(t, "pw")
	h := s.bearer("pw")
	h["Origin"] = "http://" + s.addr
	st, d := s.postJSON("/settings/save", settingsAnswers(M{"allow_remote": true}), h)
	mustEq(t, st, 200)
	rs, _ := d["restart_needed"].([]any)
	if len(rs) != 1 || !strings.Contains(rs[0].(string), "phone and tablet") {
		t.Errorf("restart_needed %v", rs)
	}
	mustEq(t, BindHost, "127.0.0.1") // still where it's listening
	st, d = s.postJSON("/settings/save", M{"role": "scorer", "pcs_folder": ""}, h)
	mustEq(t, st, 200)
	rs, _ = d["restart_needed"].([]any)
	if len(rs) == 0 || !strings.Contains(rs[0].(string), "what this computer is for") {
		t.Errorf("restart_needed %v", rs)
	}
}

func TestSettingsCheckFolder(t *testing.T) {
	s := settingsServer(t, "")
	st, d := s.postJSON("/settings/check", M{"what": "folder", "folder": t.TempDir()}, map[string]string{"Origin": "http://" + s.addr})
	mustEq(t, st, 200)
	mustEq(t, d["status"], "warn")
}

func TestControlPanelLinksToSettings(t *testing.T) {
	contains(t, page(t, "control.html"), `href="/settings"`)
}
