package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const scoreboardXML = `<?xml version="1.0"?><scoreboard><score>101</score></scoreboard>`

// openSetup waits for the setup page the app opened and returns its base URL.
func openSetup(t *testing.T, p *proc) string {
	t.Helper()
	m := p.waitFor(`Setup is open in your browser:\s+(http://\S+)/setup`, 20*time.Second)
	return m[1]
}

func saveSetup(t *testing.T, base string, values map[string]any) reply {
	t.Helper()
	return do(t, "POST", base+"/setup/save", values, map[string]string{"Origin": base})
}

func streamingAnswers(a *app) map[string]any {
	return map[string]any{"role": "streaming", "name": "Test Town CC", "abbrev": "TTC", "colour": "#123456",
		"pc_id": "4242", "pcs_folder": "", "scorebar_style": "impact", "replay_folder": filepath.Join(a.home, "Replays"),
		"yt_title": "LIVE: {home} vs {away}", "max_overs": "40", "allow_remote": false}
}

// Re-running setup on a configured machine: answers filled in, secrets never sent to the
// page, and blank secret fields keep what was there.
func TestE2ESetupRerunKeepsSecrets(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("\n[API]\nanthropic_key = sk-ant-secret\nplaycricket_key = pc-secret\n\n[Auth]\nclub_password = hunter2\n")
	p := a.start("setup")
	base := openSetup(t, p)
	info := get(t, base+"/setup/info")
	if strings.Contains(info.body, "sk-ant-secret") || strings.Contains(info.body, "hunter2") || strings.Contains(info.body, "pc-secret") {
		t.Fatal("a secret reached the setup page")
	}
	d := info.json(t)
	ex, _ := d["existing"].(map[string]any)
	if d["have_config"] != true || ex["name"] != "Test Town CC" {
		t.Fatalf("existing answers not offered: %v", d["existing"])
	}
	ans := streamingAnswers(a)
	ans["name"] = "Renamed CC"
	if r := saveSetup(t, base, ans); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if code := p.wait(20 * time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
	cfg := a.read("config.ini")
	for _, kv := range [][3]string{{"Club", "name", "Renamed CC"}, {"API", "anthropic_key", "sk-ant-secret"},
		{"API", "playcricket_key", "pc-secret"}, {"Auth", "club_password", "hunter2"}} {
		if got := iniValue(cfg, kv[0], kv[1]); got != kv[2] {
			t.Errorf("[%s] %s = %q, want %q", kv[0], kv[1], got, kv[2])
		}
	}
	mustContain(t, p.output(), "Saved. This machine is set up as: the streaming laptop")
}

// Everything is built in, and a file beside the app wins — picked up on the next request.
func TestE2EServerServesBuiltInPagesAndOverridesBesideTheApp(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	p := a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	builtIn := get(t, a.base()+"/overlay")
	if builtIn.status != 200 || !strings.Contains(builtIn.body, "scorebar") {
		t.Fatalf("built-in overlay: %d", builtIn.status)
	}
	if r := get(t, a.base()+"/favicon.ico"); r.status != 200 || !strings.HasPrefix(r.body, "\x89PNG") {
		t.Errorf("favicon: %d", r.status)
	}
	a.write("overlay.html", "<!doctype html><title>edited</title><p>CLUB-EDITED-OVERLAY</p>")
	if r := get(t, a.base()+"/overlay"); !strings.Contains(r.body, "CLUB-EDITED-OVERLAY") {
		t.Error("an overlay.html beside the app wasn't used")
	}
	os.Remove(a.path("overlay.html"))
	if r := get(t, a.base()+"/overlay"); strings.Contains(r.body, "CLUB-EDITED-OVERLAY") {
		t.Error("removing the edited copy didn't go back to the built-in one")
	}
	if !p.running() {
		t.Fatal("server exited")
	}
}

// Rehearsal: the simulator points the running server at itself and plays a match.
func TestE2ESimulatorDrivesTheRunningServer(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	out, code := a.run(90*time.Second, "simulate", "--configure", "--ball-seconds", "0.01", "--overs", "2", "--seed", "7")
	if code != 0 {
		t.Fatalf("simulate exit %d:\n%s", code, out)
	}
	mustContain(t, out, "Server now reading from the simulator folder")
	v := get(t, a.base()+"/live/view").json(t)
	st, _ := v["state"].(map[string]any)
	if fmt.Sprint(st["score"]) == "0" || st["score"] == nil {
		t.Errorf("/live/view after a simulated match: %v", st)
	}
}

func regexpQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `.`, `\.`, `(`, `\(`, `)`, `\)`).Replace(s)
}

// Started a second time on match day (the first window got minimised): it says so and
// points at the running one instead of fighting it for the port.
func TestE2ESecondCopyPointsAtTheRunningOne(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	first := a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	out, code := a.run(20*time.Second, "start", "--local", "--nvplay")
	if code != 0 {
		t.Errorf("exit %d", code)
	}
	mustContain(t, out, "already running on this laptop")
	if strings.Contains(out, "restarting") || strings.Contains(out, "keeps crashing") {
		t.Error("tried to start a second server")
	}
	if !first.running() {
		t.Error("the running copy was disturbed")
	}
}

// Settings in the running app: change the club and the overlay's state follows at once.
func TestE2ESettingsPageChangesTheRunningApp(t *testing.T) {
	a := newApp(t)
	a.streamingConfig("")
	a.start("server")
	waitHTTP(t, a.base()+"/health", 20*time.Second)
	if r := get(t, a.base()+"/settings"); r.status != 200 || !strings.Contains(r.body, "What is this computer for?") {
		t.Fatalf("/settings: %d", r.status)
	}
	info := get(t, a.base()+"/settings/info").json(t)
	if ex, _ := info["existing"].(map[string]any); ex["name"] != "Test Town CC" {
		t.Fatalf("existing %v", info["existing"])
	}
	r := do(t, "POST", a.base()+"/settings/save", map[string]any{"role": "streaming", "name": "Renamed CC",
		"abbrev": "REN", "colour": "#22aa44", "pc_id": "4242", "scorebar_style": "minimal", "max_overs": "40"},
		map[string]string{"Origin": a.base()})
	if r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	st := get(t, a.base()+"/state").json(t)
	if st["home_team"] != "Renamed CC" || st["home_colour"] != "#22aa44" || st["scorebar_style"] != "minimal" {
		t.Errorf("state after saving settings: %v %v %v", st["home_team"], st["home_colour"], st["scorebar_style"])
	}
	if got := iniValue(a.read("config.ini"), "Club", "name"); got != "Renamed CC" {
		t.Errorf("config.ini name %q", got)
	}
}
