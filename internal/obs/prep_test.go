package obs

// Everything runs against a temporary copy of OBS's config folder, laid out the way a
// real OBS 32 install has it: plugin_config/obs-websocket/config.json, user.ini naming
// the profile, and basic/profiles/<name>/basic.ini with a UTF-8 BOM and CamelCase keys.
// No real OBS install is read or written.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const basicINI = "\ufeff[General]\r\nName=Untitled\r\n\r\n" +
	"[SimpleOutput]\r\nVBitrate=6000\r\nFilePath=C:\\\\Users\\\\x\\\\Videos\r\n" +
	"RecRB=false\r\nRecRBTime=20\r\n\r\n" +
	"[Output]\r\nMode=Simple\r\n\r\n" +
	"[AdvOut]\r\nEncoder=obs_x264\r\nRecRBSize=512\r\n"

func cfgDir(t *testing.T) string {
	d := filepath.Join(t.TempDir(), "obs-studio")
	os.MkdirAll(d, 0o755)
	return d
}

func makeProfile(t *testing.T, cfg, iniName, profile, body string) string {
	os.WriteFile(filepath.Join(cfg, iniName), []byte("\ufeff[General]\nX=1\n\n[Basic]\nProfile="+profile+"\nProfileDir="+profile+"\n"), 0o644)
	folder := filepath.Join(cfg, "basic", "profiles", profile)
	os.MkdirAll(folder, 0o755)
	p := filepath.Join(folder, "basic.ini")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func writeWS(t *testing.T, cfg string, conf map[string]any) {
	p := WSConfigPath(cfg)
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(conf)
	os.WriteFile(p, b, 0o644)
}

func readFile(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func iniVal(p, section, key string) string { v, _ := ReadINIValue(p, section, key); return v }

// ── WebSocket config ──

func TestFreshInstallGetsAnEnabledPasswordProtectedServer(t *testing.T) {
	cfg := cfgDir(t)
	pw, changed, err := EnsureWebsocket(cfg, WSPort)
	conf := ReadWSConfig(cfg)
	if err != nil || !changed || conf["server_enabled"] != true || conf["auth_required"] != true ||
		conf["server_password"] != pw || len(pw) < 16 || conf["server_port"].(float64) != 4455 {
		t.Fatal(pw, changed, err, conf)
	}
	// first_load=true makes obs-websocket generate its own password on start and save
	// over ours, leaving config.ini holding a password OBS no longer accepts.
	if conf["first_load"] != false {
		t.Fatal(conf["first_load"])
	}
}

func TestSecondRunReusesThePassword(t *testing.T) {
	cfg := cfgDir(t)
	pw1, _, _ := EnsureWebsocket(cfg, WSPort)
	pw2, changed, _ := EnsureWebsocket(cfg, WSPort)
	if pw1 != pw2 || changed {
		t.Fatal(pw1, pw2, changed)
	}
}

func TestWorkingSetupWithoutAuthIsLeftAlone(t *testing.T) {
	cfg := cfgDir(t)
	conf := map[string]any{"server_enabled": true, "auth_required": false, "server_password": "theirs", "server_port": 4455.0, "first_load": false}
	writeWS(t, cfg, conf)
	before := readFile(t, WSConfigPath(cfg))
	if pw, changed, _ := EnsureWebsocket(cfg, WSPort); pw != "" || changed {
		t.Fatal(pw, changed)
	}
	if !bytes.Equal(before, readFile(t, WSConfigPath(cfg))) {
		t.Fatal("rewrote a working config")
	}
}

func TestExistingPasswordIsReusedNotReplaced(t *testing.T) {
	cfg := cfgDir(t)
	writeWS(t, cfg, map[string]any{"server_enabled": true, "auth_required": true, "server_password": "clubpw", "server_port": 4455})
	if pw, changed, _ := EnsureWebsocket(cfg, WSPort); pw != "clubpw" || changed {
		t.Fatal(pw, changed)
	}
}

func TestDisabledServerIsEnabledKeepingOtherSettings(t *testing.T) {
	cfg := cfgDir(t)
	writeWS(t, cfg, map[string]any{"server_enabled": false, "auth_required": true, "server_password": "",
		"server_port": 4460, "alerts_enabled": true, "first_load": true})
	pw, changed, _ := EnsureWebsocket(cfg, WSPort)
	conf := ReadWSConfig(cfg)
	if !changed || conf["server_enabled"] != true || conf["server_port"].(float64) != 4460 ||
		conf["alerts_enabled"] != true || conf["server_password"] != pw {
		t.Fatal(conf)
	}
}

func TestCorruptFileIsReplaced(t *testing.T) {
	cfg := cfgDir(t)
	os.MkdirAll(filepath.Dir(WSConfigPath(cfg)), 0o755)
	os.WriteFile(WSConfigPath(cfg), []byte("{not json"), 0o644)
	pw, changed, _ := EnsureWebsocket(cfg, WSPort)
	if !changed || ReadWSConfig(cfg)["server_password"] != pw {
		t.Fatal("not replaced")
	}
}

// ── INI editing ──

func TestOnlyTheNamedValuesChange(t *testing.T) {
	p := makeProfile(t, cfgDir(t), "user.ini", "Untitled", basicINI)
	before := readFile(t, p)
	SetINIValues(p, []Update{{"SimpleOutput", [][2]string{{"RecRB", "true"}}}})
	if !bytes.Equal(readFile(t, p), bytes.Replace(before, []byte("RecRB=false"), []byte("RecRB=true"), 1)) {
		t.Fatalf("%q", readFile(t, p))
	}
}

// A parser would lowercase OBS's CamelCase keys (which OBS reads case-sensitively) and
// drop the BOM — the reason this is hand-rolled.
func TestBOMCRLFAndKeyCaseSurvive(t *testing.T) {
	p := makeProfile(t, cfgDir(t), "user.ini", "Untitled", basicINI)
	SetINIValues(p, []Update{{"AdvOut", [][2]string{{"RecRB", "true"}}}})
	raw := readFile(t, p)
	if !bytes.HasPrefix(raw, []byte(bom)) || bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte("\n")) ||
		!bytes.Contains(raw, []byte("VBitrate=6000")) || !bytes.Contains(raw, []byte("RecRBSize=512")) {
		t.Fatalf("%q", raw)
	}
}

func TestMissingKeyLandsInsideItsSection(t *testing.T) {
	p := makeProfile(t, cfgDir(t), "user.ini", "Untitled", basicINI)
	SetINIValues(p, []Update{{"SimpleOutput", [][2]string{{"RecRBPrefix", "Replay"}}}})
	text := strings.TrimPrefix(string(readFile(t, p)), bom)
	simple := strings.Split(strings.Split(text, "[SimpleOutput]")[1], "[Output]")[0]
	// Inserted before the blank separator line, not after it.
	if !strings.HasSuffix(strings.TrimRight(simple, "\r\n"), "RecRBPrefix=Replay") {
		t.Fatalf("%q", simple)
	}
}

func TestMissingSectionIsAppended(t *testing.T) {
	p := makeProfile(t, cfgDir(t), "user.ini", "Untitled", basicINI)
	SetINIValues(p, []Update{{"Stream1", [][2]string{{"X", "1"}}}})
	if text := strings.TrimSpace(string(readFile(t, p))); !strings.HasSuffix(text, "[Stream1]\r\nX=1") {
		t.Fatalf("%q", text)
	}
}

func TestUnchangedFileIsNotRewritten(t *testing.T) {
	p := makeProfile(t, cfgDir(t), "user.ini", "Untitled", basicINI)
	if changed, _ := SetINIValues(p, []Update{{"Output", [][2]string{{"Mode", "Simple"}}}}); changed {
		t.Fatal("rewrote an unchanged file")
	}
}

// ── Replay buffer ──

func TestEnabledInBothOutputModes(t *testing.T) {
	cfg := cfgDir(t)
	p := makeProfile(t, cfg, "user.ini", "Untitled", basicINI)
	if got, _ := EnsureReplayBuffer(cfg, ""); got != p {
		t.Fatal(got)
	}
	for _, s := range []string{"SimpleOutput", "AdvOut"} {
		if iniVal(p, s, "RecRB") != "true" || iniVal(p, s, "RecRBTime") != "25" {
			t.Fatal(s)
		}
	}
}

func TestReplayFolderSetForBothModesAndCreated(t *testing.T) {
	cfg := cfgDir(t)
	p := makeProfile(t, cfg, "user.ini", "Untitled", basicINI)
	folder := filepath.Join(filepath.Dir(cfg), "Replays")
	EnsureReplayBuffer(cfg, folder)
	want := strings.ReplaceAll(folder, `\`, "/")
	if !isDir(folder) || iniVal(p, "SimpleOutput", "FilePath") != want || iniVal(p, "AdvOut", "RecFilePath") != want {
		t.Fatal(iniVal(p, "SimpleOutput", "FilePath"))
	}
}

func TestOneBackupOfTheOriginal(t *testing.T) {
	cfg := cfgDir(t)
	p := makeProfile(t, cfg, "user.ini", "Untitled", basicINI)
	original := readFile(t, p)
	EnsureReplayBuffer(cfg, "")
	EnsureReplayBuffer(cfg, filepath.Join(filepath.Dir(cfg), "R"))
	if !bytes.Equal(readFile(t, p+".cricketstream-backup"), original) {
		t.Fatal("backup isn't the original")
	}
}

func TestProfileFoundThroughPre31GlobalINI(t *testing.T) {
	cfg := cfgDir(t)
	if p := makeProfile(t, cfg, "global.ini", "Club", basicINI); ActiveProfileINI(cfg) != p {
		t.Fatal(ActiveProfileINI(cfg))
	}
}

func TestUserINIWinsOverGlobalINI(t *testing.T) {
	cfg := cfgDir(t)
	makeProfile(t, cfg, "global.ini", "Old", basicINI)
	if p := makeProfile(t, cfg, "user.ini", "New", basicINI); ActiveProfileINI(cfg) != p {
		t.Fatal(ActiveProfileINI(cfg))
	}
}

func TestNeverOpenedOBSHasNoProfile(t *testing.T) {
	if p, _ := EnsureReplayBuffer(cfgDir(t), ""); p != "" {
		t.Fatal(p)
	}
}

// ── Crash sentinel (the Safe Mode prompt) ──

func sentinel(t *testing.T, cfg, name string) string {
	folder := filepath.Join(cfg, ".sentinel")
	os.MkdirAll(folder, 0o755)
	p := filepath.Join(folder, name)
	os.WriteFile(p, nil, 0o644)
	return p
}

func TestLeftoverRunMarkersRemovedOthersKept(t *testing.T) {
	cfg := cfgDir(t)
	a := sentinel(t, cfg, "run_c887aaf3-ecd5-4b72-a795-6f49aaefc6f1")
	b := sentinel(t, cfg, "run_11111111-2222-3333-4444-555555555555")
	other := sentinel(t, cfg, "something_else")
	if n := ClearCrashSentinel(cfg); n != 2 {
		t.Fatal(n)
	}
	if isFile(a) || isFile(b) || !isFile(other) {
		t.Fatal("wrong files removed")
	}
	if ClearCrashSentinel(cfgDir(t)) != 0 {
		t.Fatal("no folder should be 0")
	}
}

// ── Video ──

func videoFiles(t *testing.T, config string, mbps float64) (string, string) {
	d := t.TempDir()
	c, s := filepath.Join(d, "config.ini"), filepath.Join(d, "match_state.json")
	os.WriteFile(c, []byte(config), 0o644)
	st := map[string]any{}
	if mbps != 0 {
		st["network_test_mbps"] = mbps
	}
	b, _ := json.Marshal(st)
	os.WriteFile(s, b, 0o644)
	return c, s
}

func TestChooseVideo(t *testing.T) {
	cases := []struct {
		config         string
		mbps           float64
		wantRes, wantF string
	}{
		{"", 0, "720p", "30"},   // no speed test yet
		{"", 2.0, "720p", "30"}, // the speed test drives the resolution
		{"", 8.0, "1080p", "30"},
		{"[Stream]\noutput_resolution = 720p\nfps = 25\n", 8.0, "720p", "25"}, // config wins
		{"[Stream]\noutput_resolution = 4k\nfps = 120\n", 0, "720p", "30"},    // nonsense ignored
	}
	for _, c := range cases {
		cf, sf := videoFiles(t, c.config, c.mbps)
		if res, fps, _ := ChooseVideo(cf, sf); res != c.wantRes || fps != c.wantF {
			t.Errorf("%q %v: got %s %s", c.config, c.mbps, res, fps)
		}
	}
}

// The overlay is a fixed 1920x1080 browser source; a fresh OBS sizes its canvas to the
// laptop's screen, which put most of the overlay off the edge.
func TestCanvasIsAlways1920x1080(t *testing.T) {
	cfg := cfgDir(t)
	p := makeProfile(t, cfg, "user.ini", "Untitled", basicINI+"\r\n[Video]\r\nBaseCX=1366\r\nBaseCY=768\r\n")
	EnsureVideoSettings(cfg, "720p", "30")
	for k, want := range map[string]string{"BaseCX": "1920", "BaseCY": "1080", "OutputCX": "1280", "OutputCY": "720", "FPSType": "1", "FPSInt": "30"} {
		if got := iniVal(p, "Video", k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestRecommendationTable(t *testing.T) {
	for mbps, want := range map[float64]Recommendation{
		0.5: {800, "720p", 30, ""}, 2.0: {1500, "720p", 30, ""}, 4.0: {3000, "1080p", 30, ""}, 10.0: {6000, "1080p", 30, ""},
	} {
		got := RecommendStreamSettings(mbps)
		got.Note = ""
		if got != want {
			t.Errorf("%v Mbps: %+v", mbps, got)
		}
	}
}

// ── Encoder ──

const logWithEncoders = "14:32:37.044: Available Encoders:\n" +
	"14:32:37.044:   Video Encoders:\n" +
	"14:32:37.044: \t- ffmpeg_svt_av1 (SVT-AV1)\n" +
	"14:32:37.044: \t- obs_nvenc_h264_tex (NVIDIA NVENC H.264)\n" +
	"14:32:37.044: \t- obs_nvenc_hevc_tex (NVIDIA NVENC HEVC)\n" +
	"14:32:37.044: \t- obs_x264 (x264)\n" +
	"14:32:37.044:   Audio Encoders:\n" +
	"14:32:37.044: \t- ffmpeg_aac (FFmpeg AAC)\n"

func writeLog(t *testing.T, cfg, text, name string) {
	folder := filepath.Join(cfg, "logs")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, name), []byte(text), 0o644)
}

func encProfile(t *testing.T, cfg, stream, rec, mode string) string {
	p := makeProfile(t, cfg, "user.ini", "Untitled", strings.Replace(basicINI, "[Output]\r\nMode=Simple", "[Output]\r\nMode="+mode, 1))
	SetINIValues(p, []Update{{"SimpleOutput", [][2]string{{"StreamEncoder", stream}, {"RecEncoder", rec}}}})
	return p
}

func TestReadsTheListFromOBSsOwnLog(t *testing.T) {
	cfg := cfgDir(t)
	writeLog(t, cfg, logWithEncoders, "2026-10-01 14-32-35.txt")
	got := strings.Join(AvailableEncoders(cfg), ",")
	if got != "ffmpeg_svt_av1,obs_nvenc_h264_tex,obs_nvenc_hevc_tex,obs_x264" {
		t.Fatal(got)
	}
}

func TestACutShortLogFallsBackToAnOlderOne(t *testing.T) {
	cfg := cfgDir(t)
	writeLog(t, cfg, logWithEncoders, "2026-10-01 10-00-00.txt")
	writeLog(t, cfg, "14:40:00.000: crashed during startup\n", "2026-10-01 14-40-00.txt")
	if !strings.Contains(strings.Join(AvailableEncoders(cfg), ","), "obs_nvenc_h264_tex") {
		t.Fatal(AvailableEncoders(cfg))
	}
}

func TestHardwareChoicePerVendor(t *testing.T) {
	pick := func(ids ...string) string { n, _, _ := PickHardwareEncoder(ids); return n }
	for want, ids := range map[string][]string{
		"nvenc": {"obs_nvenc_h264_tex", "obs_x264"}, "amd": {"h264_texture_amf", "obs_x264"},
		"qsv": {"obs_qsv11_v2", "obs_x264"}, "apple_h264": {"com.apple.videotoolbox.videoencoder.ave.avc"},
	} {
		if got := pick(ids...); got != want {
			t.Errorf("%v: %q", ids, got)
		}
	}
	if pick("jim_nvenc", "obs_x264") != "nvenc" { // older OBS
		t.Error("jim_nvenc")
	}
	if pick("obs_nvenc_hevc_tex", "obs_qsv11_av1", "obs_x264") != "" || pick("obs_x264") != "" {
		t.Error("picked a non-H.264 or software encoder")
	}
}

func TestCPUEncodingMovedToHardware(t *testing.T) {
	cfg := cfgDir(t)
	writeLog(t, cfg, logWithEncoders, "a.txt")
	p := encProfile(t, cfg, "x264", "x264", "Simple")
	changed, msg, _ := EnsureEncoder(cfg)
	if !changed || !strings.Contains(msg, "NVENC") || iniVal(p, "SimpleOutput", "StreamEncoder") != "nvenc" || iniVal(p, "SimpleOutput", "RecEncoder") != "nvenc" {
		t.Fatal(changed, msg)
	}
}

func TestUnsetEncoderCountsAsCPU(t *testing.T) { // a fresh profile has no StreamEncoder line
	cfg := cfgDir(t)
	writeLog(t, cfg, logWithEncoders, "a.txt")
	p := makeProfile(t, cfg, "user.ini", "Untitled", basicINI)
	if changed, _, _ := EnsureEncoder(cfg); !changed || iniVal(p, "SimpleOutput", "StreamEncoder") != "nvenc" {
		t.Fatal("not moved")
	}
}

func TestEncoderLeftAloneCases(t *testing.T) {
	for name, c := range map[string]struct {
		stream, mode, log string
	}{
		"hardware already chosen": {"qsv", "Simple", logWithEncoders},
		"advanced mode":           {"x264", "Advanced", logWithEncoders},
		"no hardware encoder":     {"x264", "Simple", strings.ReplaceAll(logWithEncoders, "obs_nvenc", "obs_other")},
		"no log yet":              {"x264", "Simple", ""},
	} {
		cfg := cfgDir(t)
		if c.log != "" {
			writeLog(t, cfg, c.log, "a.txt")
		}
		p := encProfile(t, cfg, c.stream, c.stream, c.mode)
		if changed, msg, _ := EnsureEncoder(cfg); changed || msg != "" || iniVal(p, "SimpleOutput", "StreamEncoder") != c.stream {
			t.Errorf("%s: changed %v", name, changed)
		}
	}
}

// ── Launch flag ──

func collection(t *testing.T, cfg string, sources ...string) {
	os.WriteFile(filepath.Join(cfg, "user.ini"), []byte("[Basic]\nSceneCollectionFile=Untitled.json\n"), 0o644)
	folder := filepath.Join(cfg, "basic", "scenes")
	os.MkdirAll(folder, 0o755)
	var src []map[string]string
	for _, n := range sources {
		src = append(src, map[string]string{"name": n})
	}
	b, _ := json.Marshal(map[string]any{"sources": src})
	os.WriteFile(filepath.Join(folder, "Untitled.json"), b, 0o644)
}

func TestReplayFlagOnlyOnceScenesExist(t *testing.T) {
	cfg := cfgDir(t)
	collection(t, cfg) // an empty scene makes OBS stop on a modal "No Sources" question
	if strings.Contains(strings.Join(LaunchArgsFor(cfg), " "), "--startreplaybuffer") {
		t.Fatal("fresh OBS launched with the buffer")
	}
	collection(t, cfg, "Overlay", "ReplayClip", "Main", "Replay")
	if !strings.Contains(strings.Join(LaunchArgsFor(cfg), " "), "--startreplaybuffer") {
		t.Fatal("set-up OBS launched without the buffer")
	}
	if ScenesReady(cfgDir(t)) {
		t.Fatal("nothing readable means not ready")
	}
}

// ── Update check ──

func updatesSetting(cfg string) string {
	return iniVal(filepath.Join(cfg, "global.ini"), "General", "EnableAutoUpdates")
}
func hasMarker(cfg string) bool { return isFile(filepath.Join(cfg, UpdatesMarker)) }

func TestDefaultOnIsPausedThenRestored(t *testing.T) {
	cfg := cfgDir(t)
	os.WriteFile(filepath.Join(cfg, "global.ini"), []byte("[General]\nLastUpdateCheck=0\nEnableAutoUpdates=true\n"), 0o644)
	if p, _ := PauseUpdateCheck(cfg); !p || updatesSetting(cfg) != "false" {
		t.Fatal("not paused")
	}
	if !ResumeUpdateCheck(cfg) || updatesSetting(cfg) != "true" || hasMarker(cfg) {
		t.Fatal("not restored")
	}
}

func TestNeverSetIsRestoredToOn(t *testing.T) { // a fresh OBS has no global.ini: default on
	cfg := cfgDir(t)
	PauseUpdateCheck(cfg)
	if updatesSetting(cfg) != "false" {
		t.Fatal("not paused")
	}
	ResumeUpdateCheck(cfg)
	if updatesSetting(cfg) != "true" {
		t.Fatal("not restored")
	}
}

func TestClubThatTurnedItOffIsNeverTurnedOn(t *testing.T) {
	cfg := cfgDir(t)
	os.WriteFile(filepath.Join(cfg, "global.ini"), []byte("[General]\nEnableAutoUpdates=false\n"), 0o644)
	if p, _ := PauseUpdateCheck(cfg); p || hasMarker(cfg) || ResumeUpdateCheck(cfg) || updatesSetting(cfg) != "false" {
		t.Fatal("touched the club's own choice")
	}
}

// Paused, never resumed (server stopped before OBS closed), paused again: the marker
// must still say "on", not the "false" we wrote ourselves.
func TestSecondMatchDayKeepsTheOriginal(t *testing.T) {
	cfg := cfgDir(t)
	os.WriteFile(filepath.Join(cfg, "global.ini"), []byte("[General]\nEnableAutoUpdates=true\n"), 0o644)
	PauseUpdateCheck(cfg)
	if p, _ := PauseUpdateCheck(cfg); p {
		t.Fatal("second pause reported as new")
	}
	ResumeUpdateCheck(cfg)
	if updatesSetting(cfg) != "true" {
		t.Fatal(updatesSetting(cfg))
	}
	if ResumeUpdateCheck(cfgDir(t)) {
		t.Fatal("nothing to resume")
	}
}

// ── First-run wizard ──

func TestFirstRunWizard(t *testing.T) {
	cfg := cfgDir(t)
	if c, _ := SkipFirstRunWizard(cfg); !c {
		t.Fatal("brand-new OBS not marked")
	}
	for _, n := range []string{"user.ini", "global.ini"} {
		if iniVal(filepath.Join(cfg, n), "General", "FirstRun") != "true" {
			t.Fatal(n)
		}
	}
	if c, _ := SkipFirstRunWizard(cfg); c {
		t.Fatal("already-run OBS changed")
	}
	cfg = cfgDir(t)
	p := filepath.Join(cfg, "user.ini")
	os.WriteFile(p, []byte("[General]\nConfirmOnExit=true\n\n[Basic]\nProfile=Club\n"), 0o644)
	SkipFirstRunWizard(cfg)
	if iniVal(p, "General", "ConfirmOnExit") != "true" || iniVal(p, "Basic", "Profile") != "Club" {
		t.Fatal("other settings lost")
	}
}

// ── config.ini password ──

func configFile(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "config.ini")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestConfigPassword(t *testing.T) {
	p := configFile(t, "[Club]\nname = X\n\n[OBS]\nobs_password = \nreplay_folder = R\n")
	if c, _ := SyncConfigPassword(p, "pw1"); !c {
		t.Fatal("blank not filled in")
	}
	text := string(readFile(t, p))
	if !strings.Contains(text, "obs_password = pw1\n") || !strings.Contains(text, "replay_folder = R") || !strings.Contains(text, "name = X") {
		t.Fatal(text)
	}
	if c, _ := SyncConfigPassword(configFile(t, "[OBS]\nobs_password = pw1\n"), "pw1"); c {
		t.Fatal("same password rewrote")
	}
	p = configFile(t, "[OBS]\nreplay_folder = R\n\n[Graphics]\nscorebar_style = classic\n")
	SyncConfigPassword(p, "pw1")
	if ReadConfigValue(p, "OBS", "obs_password") != "pw1" || ReadConfigValue(p, "Graphics", "scorebar_style") != "classic" {
		t.Fatal(string(readFile(t, p)))
	}
	p = configFile(t, "[Club]\nname = X\n")
	SyncConfigPassword(p, "pw1")
	if ReadConfigValue(p, "OBS", "obs_password") != "pw1" {
		t.Fatal("section not added")
	}
	if c, _ := SyncConfigPassword(filepath.Join(t.TempDir(), "nope"), "x"); c {
		t.Fatal("created a config file")
	}
}

// ── Prepare: the orchestration, with OBS's process and launch swapped out ──

type prepHarness struct {
	cfg, config string
	out         []string
	launches    [][]string
	running     []bool
	waits       []bool
	prompts     []string
}

func newPrep(t *testing.T) *prepHarness {
	h := &prepHarness{cfg: cfgDir(t)}
	h.config = filepath.Join(filepath.Dir(h.cfg), "config.ini")
	os.WriteFile(h.config, []byte("[OBS]\nobs_password = \nreplay_folder = "+filepath.Join(filepath.Dir(h.cfg), "Replays")+"\n"), 0o644)
	oc, of, or, ow, ol, os_ := ConfigDir, FindOBS, Running, WaitForWebsocket, Launch, sleep
	t.Cleanup(func() { ConfigDir, FindOBS, Running, WaitForWebsocket, Launch, sleep = oc, of, or, ow, ol, os_ })
	ConfigDir = func() string { return h.cfg }
	FindOBS = func() string { return "/fake/obs64.exe" }
	sleep = func(time.Duration) {}
	WaitForWebsocket = func(int, time.Duration) bool {
		if len(h.waits) == 0 {
			return true
		}
		w := h.waits[0]
		h.waits = h.waits[1:]
		return w
	}
	Running = func() bool {
		r := h.running[0]
		if len(h.running) > 1 {
			h.running = h.running[1:]
		}
		return r
	}
	Launch = func(exe string, args []string) error {
		h.launches = append(h.launches, append([]string{exe}, args...))
		return nil
	}
	return h
}

func (h *prepHarness) run(interactive bool, running ...bool) bool {
	h.running = running
	var wait func(string)
	if interactive {
		wait = func(s string) { h.prompts = append(h.prompts, s) }
	}
	return Prepare(h.config, wait, func(s string) { h.out = append(h.out, s) })
}

func (h *prepHarness) said() string { return strings.Join(h.out, " ") }

func TestClosedOBSIsConfiguredThenOpened(t *testing.T) {
	h := newPrep(t)
	ini := makeProfile(t, h.cfg, "user.ini", "Untitled", basicINI)
	if !h.run(false, false) {
		t.Fatal(h.out)
	}
	conf := ReadWSConfig(h.cfg)
	if conf["server_enabled"] != true || ReadConfigValue(h.config, "OBS", "obs_password") != conf["server_password"] ||
		iniVal(ini, "SimpleOutput", "RecRB") != "true" || len(h.launches) != 1 || h.launches[0][0] != "/fake/obs64.exe" {
		t.Fatal(h.out, h.launches)
	}
	if iniVal(ini, "Video", "BaseCX") != "1920" || !strings.Contains(h.said(), "720p at 30fps") {
		t.Fatal("video not set while closed:", h.said())
	}
}

// Otherwise OBS stops on "Run in Safe Mode?" with nobody there to answer it.
func TestCrashMarkerClearedBeforeLaunch(t *testing.T) {
	h := newPrep(t)
	marker := sentinel(t, h.cfg, "run_c887aaf3-ecd5-4b72-a795-6f49aaefc6f1")
	Launch = func(string, []string) error {
		if isFile(marker) {
			t.Fatal("launched with the crash marker still there")
		}
		h.launches = append(h.launches, nil)
		return nil
	}
	if !h.run(false, false) || len(h.launches) != 1 || !strings.Contains(h.said(), "Safe Mode") {
		t.Fatal(h.out)
	}
}

// The settings file says enabled, but OBS is sat on the Safe Mode prompt (or in Safe
// Mode, where the WebSocket plugin never loads).
func TestOpenOBSThatIsntAnsweringIsNotTrusted(t *testing.T) {
	h := newPrep(t)
	writeWS(t, h.cfg, map[string]any{"server_enabled": true, "auth_required": true, "server_password": "pw", "server_port": 4455})
	marker := sentinel(t, h.cfg, "run_x")
	h.waits = []bool{false}
	if h.run(false, true) || len(h.launches) != 0 || !isFile(marker) || !strings.Contains(h.said(), "Safe Mode") {
		t.Fatal(h.out) // OBS's own live marker — not ours to touch
	}
}

func TestStuckOBSClosedByOperatorIsReopenedProperly(t *testing.T) {
	h := newPrep(t)
	writeWS(t, h.cfg, map[string]any{"server_enabled": true, "auth_required": true, "server_password": "pw", "server_port": 4455})
	marker := sentinel(t, h.cfg, "run_x")
	h.waits = []bool{false, true} // not answering while stuck; answering once relaunched
	if !h.run(true, true, true, false) || len(h.prompts) != 1 || isFile(marker) || len(h.launches) != 1 {
		t.Fatal(h.out, h.prompts)
	}
}

func TestVideoLeftAloneWhenManual(t *testing.T) {
	h := newPrep(t)
	f, _ := os.OpenFile(h.config, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n[Stream]\noutput_resolution = manual\n")
	f.Close()
	ini := makeProfile(t, h.cfg, "user.ini", "Untitled", basicINI)
	h.run(false, false)
	if _, ok := ReadINIValue(ini, "Video", "BaseCX"); ok {
		t.Fatal("video changed in manual mode")
	}
}

func TestOpenOBSWithWorkingWebsocketIsNotTouched(t *testing.T) {
	h := newPrep(t)
	writeWS(t, h.cfg, map[string]any{"server_enabled": true, "auth_required": true, "server_password": "pw", "server_port": 4455})
	ini := makeProfile(t, h.cfg, "user.ini", "Untitled", basicINI)
	before := readFile(t, ini)
	if !h.run(false, true) || len(h.launches) != 0 || !bytes.Equal(readFile(t, ini), before) || ReadConfigValue(h.config, "OBS", "obs_password") != "pw" {
		t.Fatal(h.out)
	}
}

// Non-interactive: never block, never write files a running OBS will overwrite.
func TestOpenOBSWithWebsocketOffWritesNothingWithoutAPrompt(t *testing.T) {
	h := newPrep(t)
	if h.run(false, true) || ReadWSConfig(h.cfg) != nil || len(h.launches) != 0 {
		t.Fatal(h.out)
	}
}

func TestOperatorCanCarryOnWithoutClosingOBS(t *testing.T) {
	h := newPrep(t)
	if h.run(true, true) || len(h.prompts) != 3 || len(h.launches) != 0 || !strings.Contains(h.said(), "Carrying on") {
		t.Fatal(h.prompts, h.out)
	}
}

func TestOpenOBSWithWebsocketOffWaitsForItToClose(t *testing.T) {
	h := newPrep(t)
	if !h.run(true, true, true, false) || len(h.prompts) != 1 || ReadWSConfig(h.cfg)["server_enabled"] != true || len(h.launches) != 1 {
		t.Fatal(h.prompts, h.out)
	}
}

func TestOBSNotInstalled(t *testing.T) {
	h := newPrep(t)
	FindOBS = func() string { return "" }
	if h.run(false, false) || !strings.Contains(h.said(), "obsproject.com") || len(h.launches) != 0 {
		t.Fatal(h.out)
	}
}

// No profile yet: the replay buffer falls back to setup's own path, but the WebSocket
// server — the step that needed a human — is still switched on.
func TestFreshOBSNeverOpenedStillGetsWebsocket(t *testing.T) {
	h := newPrep(t)
	if !h.run(false, false) || ReadWSConfig(h.cfg)["server_enabled"] != true || len(h.launches) != 1 {
		t.Fatal(h.out)
	}
}

func TestPortableModeRefusesANonPortableFolder(t *testing.T) {
	t.Setenv(PortableEnv, t.TempDir())
	defer func() {
		if recover() == nil {
			t.Fatal("ConfigDir accepted a folder without obs_portable_mode.txt")
		}
	}()
	defaultConfigDir()
}
