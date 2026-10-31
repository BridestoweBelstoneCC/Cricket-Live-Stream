package clubpack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
	"cricketstream/internal/server"
)

// laptop points the app at its own folder, as if it were another machine.
func laptop(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	assets.SetDir(d)
	server.SetAppDir(d)
	return d
}

func restore(t *testing.T) {
	saved := assets.Dir()
	t.Cleanup(func() { assets.SetDir(saved); server.SetAppDir(saved) })
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

var png1 = "\x89PNG\r\n\x1a\nbadge"

func oldLaptop(t *testing.T) string {
	d := laptop(t)
	write(t, filepath.Join(d, "config.ini"), "[App]\nrole = streaming\n\n[Club]\nname = Bridestowe CC\nplaycricket_id = 29434\n\n"+
		"[API]\nplaycricket_key = pc-OLD\nanthropic_key = sk-ant-OLD\n\n[Auth]\nclub_password = pw-OLD\ncontrol_token = tok-OLD\n\n"+
		"[OBS]\nobs_password = obs-OLD\nreplay_folder = C:\\Users\\Old\\Videos\\Replays\n\n"+
		"[Scoring]\npcs_output_folder = C:\\Users\\Old\\NV\\Output\nlogos_folder = D:\\Shared\\Badges\n\n[Network]\nbind_host = 0.0.0.0\n\n"+
		"[Clubhouse]\nenabled = yes\nkey = CHKEYOLD\nview_enabled = yes\nview_key = VWKEYOLD\n")
	server.SaveState(server.M{"home_team": "Bridestowe CC", "sponsor_name": "Acme Builders", "graphics_partnership_display": false,
		"roster": server.M{"7": "John Walker"}, "anthropic_api_key": "sk-ant-OLD", "obs_password": "obs-OLD",
		"logos_folder": `D:\Shared\Badges`, "pcs_output_folder": `C:\Users\Old\NV\Output`, "agent_host": "192.168.1.40:8788"})
	return d
}

func entries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var buf bytes.Buffer
		buf.ReadFrom(rc)
		rc.Close()
		out[f.Name] = buf.String()
	}
	return out
}

// The whole move: the club's set-up, panel settings and pictures arrive; the new laptop's
// own OBS password, login token and folders stay its own; keys stay home unless asked for.
func TestMoveToANewLaptop(t *testing.T) {
	restore(t)
	a := oldLaptop(t)
	// Pictures, in the folders the Pictures tab uses — the badges in a custom folder of
	// the club's (a shared drive): read from there, they land in the new laptop's own.
	custom := filepath.Join(a, "shared-drive", "Badges")
	server.UpdateState(func(st server.M) { st["logos_folder"] = custom })
	write(t, filepath.Join(custom, "29434.png"), png1)
	write(t, filepath.Join(a, "headshots", "John Walker.png"), png1)
	write(t, filepath.Join(a, "sponsors", "1.png"), png1)
	write(t, filepath.Join(a, "socials", "2nd", "Tea.jpg"), "\xff\xd8\xff photo")
	write(t, filepath.Join(a, "headshots", "notes.txt"), "not a picture")

	var buf bytes.Buffer
	sum, err := Export(&buf, false, "3.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Club != "Bridestowe CC" || sum.Pictures["headshots"] != 1 || sum.Pictures["photos"] != 1 {
		t.Errorf("export summary %+v", sum)
	}
	got := entries(t, buf.Bytes())
	for _, leak := range []string{"pc-OLD", "sk-ant-OLD", "pw-OLD", "tok-OLD", "obs-OLD", `C:\Users\Old`, "0.0.0.0", "192.168.1.40", "shared-drive", `D:\Shared`} {
		for name, body := range got {
			if strings.Contains(body, leak) {
				t.Errorf("%q went into the club file (%s)", leak, name)
			}
		}
	}
	for _, want := range []string{ManifestName, "config.ini", "match_state.json", "pictures/headshots/John Walker.png",
		"pictures/sponsors/1.png", "pictures/photos/2nd/Tea.jpg", "pictures/badges/29434.png"} {
		if _, ok := got[want]; !ok {
			t.Errorf("club file missing %s", want)
		}
	}
	if _, ok := got["pictures/headshots/notes.txt"]; ok {
		t.Error("a non-picture went in")
	}

	// The new laptop already has its own OBS password, login token, NV Play folder and a
	// PlayCricket key of its own.
	b := laptop(t)
	write(t, filepath.Join(b, "config.ini"), "[App]\nrole = streaming\n[API]\nplaycricket_key = pc-NEW\n[Auth]\ncontrol_token = tok-NEW\n"+
		"[OBS]\nobs_password = obs-NEW\n[Scoring]\npcs_output_folder = C:\\NV\\Output\n")
	server.SaveState(server.M{"obs_password": "obs-NEW", "pcs_output_folder": `C:\NV\Output`, "playcricket_api_key": "pc-NEW"})
	isum, err := Import(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !isum.Settings || !isum.Panel || isum.Pictures["badges"] != 1 || isum.Pictures["photos"] != 1 || isum.Backup == "" {
		t.Errorf("import summary %+v", isum)
	}
	cfg, _ := ini.Load(filepath.Join(b, "config.ini"))
	for _, c := range [][3]string{{"Club", "name", "Bridestowe CC"}, {"API", "playcricket_key", "pc-NEW"},
		{"Auth", "control_token", "tok-NEW"}, {"OBS", "obs_password", "obs-NEW"}, {"Scoring", "pcs_output_folder", `C:\NV\Output`},
		{"Network", "bind_host", "127.0.0.1"}, {"Scoring", "logos_folder", ""}} {
		if v := cfg.Get(c[0], c[1], "<none>"); v != c[2] && !(c[2] == "" && v == "<none>") {
			t.Errorf("config [%s] %s = %q, want %q", c[0], c[1], v, c[2])
		}
	}
	st := server.LoadState()
	if st["sponsor_name"] != "Acme Builders" || st["graphics_partnership_display"] != false {
		t.Errorf("panel settings not carried: %v %v", st["sponsor_name"], st["graphics_partnership_display"])
	}
	if r, _ := st["roster"].(map[string]any); r["7"] != "John Walker" {
		t.Errorf("roster %v", st["roster"])
	}
	if st["obs_password"] != "obs-NEW" || st["pcs_output_folder"] != `C:\NV\Output` || st["playcricket_api_key"] != "pc-NEW" {
		t.Errorf("this laptop's own settings were overwritten: %v %v %v", st["obs_password"], st["pcs_output_folder"], st["playcricket_api_key"])
	}
	if st["logos_folder"] != "" || st["agent_host"] != nil {
		t.Errorf("old laptop's paths carried: %v %v", st["logos_folder"], st["agent_host"])
	}
	for _, p := range []string{"logos/29434.png", "headshots/John Walker.png", "sponsors/1.png", "socials/2nd/Tea.jpg"} {
		if _, err := os.Stat(filepath.Join(b, filepath.FromSlash(p))); err != nil {
			t.Errorf("picture %s not imported", p)
		}
	}
	if old, err := os.ReadFile(filepath.Join(isum.Backup, "config.ini")); err != nil || !strings.Contains(string(old), "pc-NEW") {
		t.Errorf("no backup of the settings it replaced: %v", err)
	}
}

// Asked for, the keys travel — but never the machine's OBS password or login token.
func TestExportWithKeys(t *testing.T) {
	restore(t)
	oldLaptop(t)
	var buf bytes.Buffer
	if _, err := Export(&buf, true, "3.0.0"); err != nil {
		t.Fatal(err)
	}
	got := entries(t, buf.Bytes())
	for _, want := range []string{"pc-OLD", "sk-ant-OLD", "pw-OLD"} {
		if !strings.Contains(got["config.ini"], want) {
			t.Errorf("%s not in the club file", want)
		}
	}
	// Nor the clubhouse screens' keys: each laptop makes its own, and its screens pair again.
	for _, never := range []string{"tok-OLD", "obs-OLD", "CHKEYOLD", "VWKEYOLD"} {
		if strings.Contains(got["config.ini"]+got["match_state.json"], never) {
			t.Errorf("%s went into the club file", never)
		}
	}
	var man Manifest
	json.Unmarshal([]byte(got[ManifestName]), &man)
	if !man.Secrets || man.App != "CricketStream" || man.Club != "Bridestowe CC" {
		t.Errorf("manifest %+v", man)
	}
	// Onto a fresh laptop: the keys arrive, and remote access is never left wide open.
	b := laptop(t)
	if _, err := Import(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := ini.Load(filepath.Join(b, "config.ini"))
	if cfg.Get("API", "anthropic_key", "") != "sk-ant-OLD" || cfg.Get("App", "role", "") != "streaming" ||
		cfg.Get("Network", "bind_host", "") != "127.0.0.1" {
		t.Errorf("fresh laptop config: key %q role %q bind %q", cfg.Get("API", "anthropic_key", ""), cfg.Get("App", "role", ""),
			cfg.Get("Network", "bind_host", ""))
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, body := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

// Not a club file, a newer format, and entries trying to land outside their folders.
func TestImportRefuses(t *testing.T) {
	restore(t)
	d := laptop(t)
	if _, err := Import([]byte("not a zip")); err != ErrNotAClubFile {
		t.Errorf("not a zip: %v", err)
	}
	if _, err := Import(zipOf(t, map[string]string{"config.ini": "[App]\nrole = scorer\n"})); err != ErrNotAClubFile {
		t.Errorf("no manifest: %v", err)
	}
	newer, _ := json.Marshal(Manifest{App: "CricketStream", Format: Format + 1, Version: "9.0"})
	if _, err := Import(zipOf(t, map[string]string{ManifestName: string(newer)})); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("newer format: %v", err)
	}
	man, _ := json.Marshal(Manifest{App: "CricketStream", Format: Format})
	sum, err := Import(zipOf(t, map[string]string{ManifestName: string(man),
		"pictures/badges/../../config.ini.png": png1, "pictures/badges/..\\evil.png": png1, "../outside.png": png1,
		"pictures/fonts/x.png": png1, "pictures/photos/../x.png": png1, "pictures/photos/2nd/../../y.png": png1,
		"pictures/badges/.hidden.png": png1, "pictures/badges/ok.png": png1}))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Pictures["badges"] != 1 {
		t.Errorf("pictures %v", sum.Pictures)
	}
	var stray []string
	filepath.WalkDir(filepath.Dir(d), func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.HasSuffix(p, ".png") && filepath.Base(p) != "ok.png" {
			stray = append(stray, p)
		}
		return nil
	})
	if len(stray) > 0 {
		t.Errorf("files landed outside the picture folders: %v", stray)
	}
	if _, err := os.Stat(filepath.Join(d, "logos", "ok.png")); err != nil {
		t.Error("the good badge wasn't imported")
	}
}
