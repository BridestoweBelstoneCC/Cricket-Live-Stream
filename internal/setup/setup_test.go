package setup

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cricketstream/internal/assets"
	"cricketstream/internal/bridge"
	"cricketstream/internal/ini"
)

func appDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := assets.Dir()
	assets.SetDir(dir)
	t.Cleanup(func() { assets.SetDir(saved) })
	return dir
}

func TestAbbrevFromName(t *testing.T) {
	for name, want := range map[string]string{"Bridestowe and Belstone CC": "BRIDES", "The Heathcoat Cricket Club": "HEATHC",
		"Home & District CC": "HOME", "CC": "", "Up": "UP"} {
		if got := AbbrevFromName(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func TestNormaliseAppliesTheWizardsTidying(t *testing.T) {
	v := Values{Name: " Home and District CC ", Colour: "1a3a5c", Abbrev: "toolongname", ScorebarStyle: "nope"}
	v.Normalise()
	if v.Role != RoleStreaming || v.Abbrev != "TOOLON" || v.Colour != "#1a3a5c" || v.ScorebarStyle != "classic" ||
		v.YTTitle != "LIVE: {home} vs {away}" || v.MaxOvers != "50" || v.ReplayFolder != DefaultReplayFolder() {
		t.Fatalf("%+v", v)
	}
	w := Values{Name: "Home and District CC"}
	w.Normalise()
	if w.Abbrev != "HOME" {
		t.Fatal(w.Abbrev)
	}
}

func TestRequiredQuestions(t *testing.T) {
	v := Values{Role: RoleStreaming}
	if len(v.Problems()) != 2 {
		t.Fatal(v.Problems())
	}
	if p := (Values{Role: RoleScorer}).Problems(); len(p) != 0 {
		t.Fatal("a scorer's laptop needs no club details:", p)
	}
}

func TestConfigHasEverySectionTheServerReads(t *testing.T) {
	v := Values{Name: "Test CC", PCID: "111", PCKey: "k", PCSFolder: `C:\NV`, ScorebarStyle: "modern",
		ClubPassword: "pw", AllowRemote: true, YTKey: "abcd"}
	v.Normalise()
	f := v.Config()
	for _, c := range [][3]string{{"App", "role", "streaming"}, {"Club", "name", "Test CC"}, {"Club", "abbreviation", "TEST"},
		{"Club", "playcricket_id", "111"}, {"API", "playcricket_key", "k"}, {"Scoring", "pcs_output_folder", `C:\NV`},
		{"Graphics", "scorebar_style", "modern"}, {"Stream", "youtube_stream_key", "abcd"}, {"Stream", "max_overs", "50"},
		{"Auth", "club_password", "pw"}, {"Network", "bind_host", "0.0.0.0"}, {"OBS", "obs_password", ""}} {
		if got := f.Get(c[0], c[1], "<missing>"); got != c[2] {
			t.Errorf("[%s] %s = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	if tok := f.Get("Auth", "control_token", ""); len(tok) != 64 {
		t.Fatal("no random control_token:", tok)
	}
}

func TestScorerConfigIsJustTheRole(t *testing.T) {
	v := Values{Role: RoleScorer, PCSFolder: "/nv"}
	v.Normalise()
	f := v.Config()
	if f.HasSection("Club") || f.Get("App", "role", "") != RoleScorer || f.Get("Scoring", "pcs_output_folder", "") != "/nv" {
		t.Fatal(string(f.Bytes()))
	}
}

func TestRoleOfAnOldConfigIsTheStreamingLaptop(t *testing.T) {
	dir := appDir(t)
	if Role() != "" {
		t.Fatal("a role with no config.ini")
	}
	os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[Club]\nname = X\n"), 0o644)
	if Role() != RoleStreaming {
		t.Fatal(Role())
	}
	os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[App]\nrole = Bridge\n"), 0o644)
	if Role() != RoleBridge {
		t.Fatal(Role())
	}
}

func TestRerunKeepsSecretsTokenAndKeysItDoesntOwn(t *testing.T) {
	old, _ := ini.Parse([]byte("[API]\nplaycricket_key = oldkey\nanthropic_key = sk-old\n[Auth]\ncontrol_token = tok\n" +
		"club_password = oldpw\n[OBS]\nobs_password = fromobs\nmanage_obs = no\n[Stream]\nbitrate_kbps = 2500\n"))
	v := Values{Name: "New CC", PCID: "222", ClubPassword: ""}
	v.Normalise()
	f := v.Merge(old)
	for _, c := range [][3]string{{"API", "playcricket_key", "oldkey"}, {"API", "anthropic_key", "sk-old"},
		{"Auth", "control_token", "tok"}, {"Auth", "club_password", "oldpw"}, {"OBS", "obs_password", "fromobs"},
		{"OBS", "manage_obs", "no"}, {"Stream", "bitrate_kbps", "2500"}, {"Club", "name", "New CC"}} {
		if got := f.Get(c[0], c[1], "<missing>"); got != c[2] {
			t.Errorf("[%s] %s = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	// a newly typed secret replaces the old one
	v.PCKey = "newkey"
	if got := v.Merge(old).Get("API", "playcricket_key", ""); got != "newkey" {
		t.Fatal(got)
	}
}

func TestPercentSignsSurviveTheRoundTrip(t *testing.T) {
	dir := appDir(t)
	v := Values{Name: "100% Cricket CC", PCID: "1", ClubPassword: "50%off"}
	v.Normalise()
	if err := v.Save(filepath.Join(dir, "config.ini")); err != nil {
		t.Fatal(err)
	}
	f, err := ini.Load(filepath.Join(dir, "config.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Get("Club", "name", "") != "100% Cricket CC" || f.Get("Auth", "club_password", "") != "50%off" {
		t.Fatal(string(f.Bytes()))
	}
}

func post(t *testing.T, h http.Handler, body any, origin string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "http://127.0.0.1:5000/setup/save", bytes.NewReader(b))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestPageSavesStreamingConfig(t *testing.T) {
	dir := appDir(t)
	h, done := Handler()
	code, out := post(t, h, map[string]any{"role": "streaming", "name": "Test CC", "pc_id": "111", "allow_remote": false}, "http://127.0.0.1:5000")
	if code != 200 || out["ok"] != true {
		t.Fatal(code, out)
	}
	select {
	case <-done:
	default:
		t.Fatal("save didn't finish setup")
	}
	f, err := ini.Load(filepath.Join(dir, "config.ini"))
	if err != nil || f.Get("Club", "name", "") != "Test CC" || f.Get("Network", "bind_host", "") != "127.0.0.1" {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.ini"))
	if !strings.HasPrefix(string(b), "# CricketStream Overlay — Club Configuration") {
		t.Fatal("no header")
	}
}

func TestPageRefusesMissingClubAndCrossSite(t *testing.T) {
	appDir(t)
	h, _ := Handler()
	if code, out := post(t, h, map[string]any{"role": "streaming"}, ""); code != 400 || !strings.Contains(out["error"].(string), "club name") {
		t.Fatal(code, out)
	}
	if code, _ := post(t, h, map[string]any{"role": "streaming", "name": "X", "pc_id": "1"}, "http://evil.example"); code != 403 {
		t.Fatal(code)
	}
}

func TestPageSavesBridgeWithAToken(t *testing.T) {
	appDir(t)
	h, _ := Handler()
	code, out := post(t, h, map[string]any{"role": "bridge", "pcs_folder": "/nv/out", "bridge_port": 5051}, "")
	if code != 200 || len(out["bridge_token"].(string)) != 32 {
		t.Fatal(code, out)
	}
	c, ok := bridge.Load()
	if !ok || c.Folder != "/nv/out" || c.Port != 5051 || c.Token != out["bridge_token"] {
		t.Fatal(c)
	}
	// a re-run keeps the token the streaming laptop already has
	_, again := post(t, h, map[string]any{"role": "bridge", "pcs_folder": "/nv/out"}, "")
	if again["bridge_token"] != out["bridge_token"] {
		t.Fatal("token changed on a re-run")
	}
	if Role() != RoleBridge {
		t.Fatal(Role())
	}
}

func TestInfoNeverShowsSecrets(t *testing.T) {
	dir := appDir(t)
	os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[Club]\nname = Test CC\n[API]\nplaycricket_key = SECRETKEY\n"+
		"[Auth]\nclub_password = SECRETPW\n"), 0o600)
	h, _ := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/setup/info", nil))
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(string(body), "SECRET") {
		t.Fatal("a secret reached the page")
	}
	if !strings.Contains(string(body), `"name":"Test CC"`) {
		t.Fatal(string(body))
	}
}

func TestPageOffersEveryScorebarStyleTheOverlayHas(t *testing.T) {
	// setup is where a first-time club learns these exist at all.
	b, _ := assets.Embedded("overlay.html")
	for _, s := range ScorebarStyles {
		if s.ID != "classic" && !strings.Contains(string(b), "body.style-"+s.ID+" #scorebar") {
			t.Errorf("setup offers %q but the overlay has no such style", s.ID)
		}
	}
	if len(ScorebarStyles) != 4 {
		t.Fatal(len(ScorebarStyles))
	}
}
