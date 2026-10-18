package obs

// OBS preparation — the part of OBS setup that has to happen while OBS is CLOSED.
//
// Setup over the WebSocket leaves three things it can't do, because each needs OBS not
// to be running yet:
//
//  1. Turn the WebSocket server on in the first place (there's no API for that).
//  2. Enable the replay buffer so it exists at startup. Setup can tick the box over the
//     WebSocket, but OBS only creates outputs when it starts, so the first run needed an
//     unexpected "restart OBS and run setup again".
//  3. Open OBS itself, so match day is one double-click, not "open OBS first".
//
// This does all three by editing OBS's own config files before launching it. Both are
// written only while OBS is not running, because OBS writes its in-memory copy back over
// them when it exits. Every step is best-effort.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricketstream/internal/pyval"
)

// ── Replay buffer: the active profile's basic.ini ────────────────────────────

// ActiveProfileINI is basic.ini of the profile OBS will open, or "" if OBS has never
// been run. OBS 31 moved the profile choice from global.ini to user.ini; the new one is
// read first.
func ActiveProfileINI(cfgDir string) string {
	for _, name := range []string{"user.ini", "global.ini"} {
		if folder, ok := ReadINIValue(filepath.Join(cfgDir, name), "Basic", "ProfileDir"); ok && folder != "" {
			p := filepath.Join(cfgDir, "basic", "profiles", folder, "basic.ini")
			if isFile(p) {
				return p
			}
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	st, _ := os.Stat(src)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	if st != nil {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}

// EnsureReplayBuffer enables the replay buffer (and points it at replayFolder) in the
// active profile: RecRB in BOTH output-mode sections, so switching Simple <-> Advanced
// later doesn't silently turn replays off. The folder matters too: launched with
// --startreplaybuffer, the buffer starts with whatever path basic.ini holds, and the
// server only looks for clips in replayFolder.
//
// Returns the basic.ini path, or "" if there's no profile yet (OBS never opened; setup's
// own fallback covers that run). Keeps one backup of the original file. OBS closed only.
func EnsureReplayBuffer(cfgDir, replayFolder string) (string, error) {
	path := ActiveProfileINI(cfgDir)
	if path == "" {
		return "", nil
	}
	simple := [][2]string{{"RecRB", "true"}, {"RecRBTime", ReplaySeconds}}
	adv := [][2]string{{"RecRB", "true"}, {"RecRBTime", ReplaySeconds}}
	if replayFolder != "" {
		os.MkdirAll(replayFolder, 0o755)
		if isDir(replayFolder) {
			// OBS stores paths with forward slashes on every platform.
			p := strings.ReplaceAll(replayFolder, `\`, "/")
			simple = append(simple, [2]string{"FilePath", p})
			adv = append(adv, [2]string{"RecFilePath", p})
		}
	}
	backup := path + ".cricketstream-backup"
	if _, err := os.Stat(backup); err != nil {
		copyFile(path, backup)
	}
	_, err := SetINIValues(path, []Update{{"SimpleOutput", simple}, {"AdvOut", adv}})
	return path, err
}

// ── Safe Mode prompt: the .sentinel folder ───────────────────────────────────

// ClearCrashSentinel removes the marker that makes OBS ask about Safe Mode on its next
// start.
//
// OBS 30+ drops an empty .sentinel/run_<uuid> file while it runs and deletes it on a
// clean exit, so one left behind means the last run crashed (or the laptop lost power,
// or something force-closed it). Next start, OBS stops on a modal "Run in Safe Mode?"
// prompt. Nobody is at the laptop to answer it on a match day, and Safe Mode skips the
// WebSocket plugin. Clearing the marker skips the question — verified on OBS 32.1.1.
// Must only be called while OBS is closed. Returns how many markers were removed.
func ClearCrashSentinel(cfgDir string) int {
	folder := filepath.Join(cfgDir, ".sentinel")
	entries, err := os.ReadDir(folder)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run_") && os.Remove(filepath.Join(folder, e.Name())) == nil {
			n++
		}
	}
	return n
}

// ── Video: canvas, output resolution, frame rate ─────────────────────────────

// overlay.html is a fixed 1920x1080 browser source placed at 0,0, so the canvas MUST be
// this size. A fresh OBS sizes its canvas to the laptop's screen instead.
var (
	Canvas      = [2]int{1920, 1080}
	Resolutions = map[string][2]int{"720p": {1280, 720}, "1080p": {1920, 1080}}
	FPSChoices  = []string{"25", "30"}
)

// Recommendation is the stream settings for an upload speed.
type Recommendation struct {
	BitrateKbps int
	Resolution  string
	FPS         int
	Note        string
}

// RecommendStreamSettings keeps the bitrate comfortably under the measured speed (25%
// headroom) so real-world jitter doesn't cause buffering, then picks the resolution that
// bitrate can carry well. The one recommendation table: quickstart applies it and the
// server's /obs/stream_check shows it.
func RecommendStreamSettings(uploadMbps float64) Recommendation {
	safe := int(uploadMbps * 1000 * 0.75)
	switch {
	case safe < 1500:
		return Recommendation{max(safe, 800), "720p", 30, "Upload speed is limited — 720p30 keeps quality watchable without buffering."}
	case safe < 2800:
		return Recommendation{safe, "720p", 30, "Enough headroom for a clean 720p30 stream."}
	case safe < 4500:
		return Recommendation{min(safe, 4000), "1080p", 30, "Good enough for 1080p30 — the standard for this project."}
	}
	return Recommendation{min(safe, 6000), "1080p", 30, "Plenty of headroom for a strong 1080p30 stream (60fps rarely helps for cricket — the action is slower-moving than most sports)."}
}

// LastUploadMbps is the server's cached upload-speed test from match_state.json.
func LastUploadMbps(stateFile string) (float64, bool) {
	b, err := os.ReadFile(stateFile)
	if err != nil {
		return 0, false
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil || !pyval.Truthy(st["network_test_mbps"]) {
		return 0, false
	}
	f, err := pyval.Float(st["network_test_mbps"])
	return f, err == nil
}

// ChooseVideo is config.ini's [Stream] output_resolution / fps if set to something
// valid, otherwise the last upload test's recommendation, otherwise 720p30.
func ChooseVideo(configFile, stateFile string) (res, fps, why string) {
	resCfg := strings.ToLower(ReadConfigValue(configFile, "Stream", "output_resolution"))
	fpsCfg := ReadConfigValue(configFile, "Stream", "fps")
	if mbps, ok := LastUploadMbps(stateFile); ok {
		r := RecommendStreamSettings(mbps)
		res, fps, why = r.Resolution, strconv.Itoa(r.FPS), fmt.Sprintf("from your last upload test (%.1f Mbps)", mbps)
	} else {
		res, fps, why = "720p", "30", "no upload test yet"
	}
	if _, ok := Resolutions[resCfg]; ok {
		res, why = resCfg, "set in config.ini"
	}
	for _, f := range FPSChoices {
		if fpsCfg == f {
			fps = fpsCfg
		}
	}
	return res, fps, why
}

// EnsureVideoSettings writes canvas/output/frame rate into the active profile.
// (changed, hadProfile). OBS closed only: OBS refuses to change video settings while any
// output runs, and the replay buffer always does.
func EnsureVideoSettings(cfgDir, resolution, fps string) (bool, bool, error) {
	path := ActiveProfileINI(cfgDir)
	if path == "" {
		return false, false, nil
	}
	out := Resolutions[resolution]
	changed, err := SetINIValues(path, []Update{{"Video", [][2]string{
		{"BaseCX", strconv.Itoa(Canvas[0])}, {"BaseCY", strconv.Itoa(Canvas[1])},
		{"OutputCX", strconv.Itoa(out[0])}, {"OutputCY", strconv.Itoa(out[1])},
		// FPSType 1 = "Integer FPS Value", which reads FPSInt as a plain number. Not the
		// default type 0 ("Common"): that matches FPSCommon against OBS's own menu labels
		// ("25 PAL", not "25") and silently runs at 30 on anything else (OBS 32.1.1).
		{"FPSType", "1"}, {"FPSInt", fps},
	}}})
	return changed, true, err
}

// ── OBS's update check, paused for the match ─────────────────────────────────

// OBS checks for a new version at startup and, when there is one, opens a "New update
// available" box over itself — including after the crash recovery reopens it mid-match.
// Paused for match day, put back when OBS is closed afterwards. Measured on OBS 32.1.1:
// the box on 4 of 6 default launches; 0 of 4 with EnableAutoUpdates=false.
const UpdatesMarker = ".cricketstream-updates-paused"

func ensureFile(path string) error {
	if isFile(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

// PauseUpdateCheck turns OBS's update check off, remembering what it was. True if it
// was turned off just now. A club that already had it off is left exactly as it was (no
// marker, so resume never turns it on). OBS closed only.
func PauseUpdateCheck(cfgDir string) (bool, error) {
	path := filepath.Join(cfgDir, "global.ini")
	marker := filepath.Join(cfgDir, UpdatesMarker)
	if err := ensureFile(path); err != nil {
		return false, err
	}
	current, _ := ReadINIValue(path, "General", "EnableAutoUpdates")
	off := []Update{{"General", [][2]string{{"EnableAutoUpdates", "false"}}}}
	if _, err := os.Stat(marker); err == nil {
		_, err := SetINIValues(path, off)
		return false, err // still paused from an earlier run
	}
	if current == "false" {
		return false, nil // the club's own choice
	}
	if err := os.WriteFile(marker, []byte(current), 0o644); err != nil { // "" = never set (on)
		return false, err
	}
	_, err := SetINIValues(path, off)
	return err == nil, err
}

// ResumeUpdateCheck puts OBS's update check back how the club had it. True if it was
// restored. OBS closed only (OBS writes its settings back over this on exit).
func ResumeUpdateCheck(cfgDir string) bool {
	marker := filepath.Join(cfgDir, UpdatesMarker)
	b, err := os.ReadFile(marker)
	if err != nil {
		return false
	}
	original := strings.TrimSpace(string(b))
	if original == "" {
		original = "true"
	}
	path := filepath.Join(cfgDir, "global.ini")
	if _, err := SetINIValues(path, []Update{{"General", [][2]string{{"EnableAutoUpdates", original}}}}); err != nil {
		return false
	}
	return os.Remove(marker) == nil
}

// ── OBS's first-run wizard ───────────────────────────────────────────────────

// SkipFirstRunWizard marks OBS's first run as done, so a brand-new OBS doesn't open its
// Auto-Configuration Wizard: a modal window nobody is there to answer, which also blocks
// OBS from closing. Everything that wizard sets is set here instead. OBS keeps the flag
// in user.ini from OBS 31, global.ini before that; set in both. OBS closed only.
func SkipFirstRunWizard(cfgDir string) (bool, error) {
	changed := false
	for _, name := range []string{"user.ini", "global.ini"} {
		path := filepath.Join(cfgDir, name)
		if v, _ := ReadINIValue(path, "General", "FirstRun"); v == "true" {
			continue
		}
		if err := ensureFile(path); err != nil {
			return changed, err
		}
		c, err := SetINIValues(path, []Update{{"General", [][2]string{{"FirstRun", "true"}}}})
		if err != nil {
			return changed, err
		}
		changed = changed || c
	}
	return changed, nil
}

// ── Video encoder ────────────────────────────────────────────────────────────

// Hardware H.264 encoders, best first: (test on the encoder id in OBS's log, the name
// Simple output mode stores in basic.ini). H.264 only: it's what every YouTube ingest
// takes.
var hardwareEncoders = []struct {
	test  func(string) bool
	name  string
	label string
}{
	{func(e string) bool {
		return strings.Contains(e, "nvenc") && !strings.Contains(e, "hevc") && !strings.Contains(e, "av1")
	}, "nvenc", "NVIDIA NVENC"},
	{func(e string) bool {
		return strings.Contains(e, "videotoolbox") && (strings.Contains(e, "ave.avc") || strings.Contains(e, "h264.gva"))
	}, "apple_h264", "Apple hardware (VideoToolbox)"},
	{func(e string) bool { return strings.Contains(e, "amf") && strings.Contains(e, "h264") }, "amd", "AMD hardware (AMF)"},
	{func(e string) bool {
		return strings.Contains(e, "qsv11") && !strings.Contains(e, "av1") && !strings.Contains(e, "hevc")
	}, "qsv", "Intel QuickSync"},
}

func isSoftwareEncoder(e string) bool { return e == "x264" || e == "x264_lowcpu" || e == "" }

// AvailableEncoders lists the video encoder ids OBS reported as usable in its most
// recent log (nil if there's no log yet). OBS writes "Available Encoders:" at every
// start, from the encoders that actually loaded on THIS machine — a better answer than
// any guess from the hardware.
func AvailableEncoders(cfgDir string) []string {
	folder := filepath.Join(cfgDir, "logs")
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var logs []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".txt") {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs)
	for i := len(logs) - 1; i >= 0; i-- {
		b, err := os.ReadFile(filepath.Join(folder, logs[i]))
		if err != nil {
			continue
		}
		text := string(b)
		_, after, ok := strings.Cut(text, "Video Encoders:")
		if !ok {
			continue // a log cut short before OBS got that far; try an older one
		}
		block, _, _ := strings.Cut(after, "Audio Encoders:")
		var found []string
		for _, line := range splitLines(block) {
			if _, rest, ok := strings.Cut(line, ": "); ok {
				line = rest // drop the timestamp
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "- ") {
				id, _, _ := strings.Cut(line[2:], " ")
				found = append(found, id)
			}
		}
		return found
	}
	return nil
}

// PickHardwareEncoder returns the Simple-mode name and label of the best hardware H.264
// encoder listed, or ok=false.
func PickHardwareEncoder(ids []string) (name, label string, ok bool) {
	for _, h := range hardwareEncoders {
		for _, e := range ids {
			if h.test(strings.ToLower(e)) {
				return h.name, h.label, true
			}
		}
	}
	return "", "", false
}

// EnsureEncoder moves OBS off CPU encoding (x264) onto a hardware encoder when this
// laptop has one. Returns whether it changed anything and a message. Simple output mode
// only (OBS's default): someone in Advanced mode has set their encoder up deliberately,
// and a hardware encoder already chosen is never second-guessed.
//
// This replaced an old "hardware vs x264" test-recording comparison over the WebSocket,
// which measured the same encoder twice: OBS only builds encoders when it starts. Never
// test or switch the encoder over the WebSocket. OBS closed only.
func EnsureEncoder(cfgDir string) (bool, string, error) {
	path := ActiveProfileINI(cfgDir)
	if path == "" {
		return false, "", nil
	}
	if mode, _ := ReadINIValue(path, "Output", "Mode"); mode != "" && mode != "Simple" {
		return false, "", nil
	}
	stream, _ := ReadINIValue(path, "SimpleOutput", "StreamEncoder")
	rec, _ := ReadINIValue(path, "SimpleOutput", "RecEncoder")
	stream, rec = strings.ToLower(stream), strings.ToLower(rec)
	if !isSoftwareEncoder(stream) && !isSoftwareEncoder(rec) {
		return false, "", nil
	}
	name, label, ok := PickHardwareEncoder(AvailableEncoders(cfgDir))
	if !ok {
		return false, "", nil
	}
	var vals [][2]string
	if isSoftwareEncoder(stream) {
		vals = append(vals, [2]string{"StreamEncoder", name})
	}
	// The recording encoder too: unless recording quality is "Same as stream", the
	// replay buffer runs its own encode with this one, a second full x264 load.
	if isSoftwareEncoder(rec) {
		vals = append(vals, [2]string{"RecEncoder", name})
	}
	if _, err := SetINIValues(path, []Update{{"SimpleOutput", vals}}); err != nil {
		return false, "", err
	}
	return true, fmt.Sprintf("Video encoder: %s (was the CPU, which drops frames under load).", label), nil
}

// ── Launching ────────────────────────────────────────────────────────────────

// ScenesReady says whether setup has already built the scenes (an "Overlay" source in
// the scene collection OBS will open). False on a fresh OBS or if it can't tell.
func ScenesReady(cfgDir string) bool {
	for _, name := range []string{"user.ini", "global.ini"} {
		coll, ok := ReadINIValue(filepath.Join(cfgDir, name), "Basic", "SceneCollectionFile")
		if !ok || coll == "" {
			continue
		}
		if !strings.HasSuffix(coll, ".json") {
			coll += ".json"
		}
		b, err := os.ReadFile(filepath.Join(cfgDir, "basic", "scenes", coll))
		if err != nil {
			return false
		}
		var d struct {
			Sources []struct {
				Name string `json:"name"`
			} `json:"sources"`
		}
		if json.Unmarshal(b, &d) != nil {
			return false
		}
		for _, s := range d.Sources {
			if s.Name == "Overlay" {
				return true
			}
		}
		return false
	}
	return false
}

// LaunchArgsFor gives --startreplaybuffer only once the scenes exist. On an empty
// scene collection OBS won't start the buffer: it stops on a modal "No Sources"
// question instead, which nobody is there to answer. The first time through, setup
// builds the scenes and starts the buffer over the WebSocket itself.
func LaunchArgsFor(cfgDir string) []string {
	if ScenesReady(cfgDir) {
		return append([]string(nil), LaunchArgs...)
	}
	var out []string
	for _, a := range LaunchArgs {
		if a != "--startreplaybuffer" {
			out = append(out, a)
		}
	}
	return out
}

// ── The whole thing ──────────────────────────────────────────────────────────

// Prepare gets OBS ready for match day: WebSocket on, password in config.ini, replay
// buffer enabled, OBS open. Returns true if OBS is running with its WebSocket reachable.
//
// waitEnter is the launcher's "press Enter" prompt; without it, Prepare never blocks and
// simply skips whatever needs OBS closed.
func Prepare(configFile string, waitEnter func(string), say func(string)) bool {
	if say == nil {
		say = func(string) {}
	}
	exe := FindOBS()
	if exe == "" {
		say("  [!!] OBS Studio isn't installed. Get it from https://obsproject.com,")
		say("       then run this again. (Everything else will still start.)")
		return false
	}
	cfgDir := ConfigDir()
	port := WSPort

	if Running() {
		conf := ReadWSConfig(cfgDir)
		// The settings file saying "enabled" isn't enough: an OBS sitting on its Safe Mode
		// question hasn't loaded the WebSocket plugin yet, and one running IN Safe Mode
		// never will. Only an answering port means OBS is actually reachable.
		if WSUsable(conf) && WaitForWebsocket(port, 10*time.Second) {
			if changed, _ := SyncConfigPassword(configFile, WSPassword(conf)); changed {
				say("  [OK] Copied OBS's WebSocket password into config.ini.")
			}
			say("  [OK] OBS is already open.")
			return true
		}
		if WSUsable(conf) {
			say("  [!!] OBS is open but not answering. It's probably asking about Safe Mode,")
			say("       or running in it (Safe Mode switches off what this uses to talk to OBS).")
			say("       Close OBS and this will reopen it properly.")
		} else {
			// Anything we write now, OBS overwrites when it exits.
			say("  [!!] OBS is open, but its WebSocket server (how this talks to OBS) is off.")
			say("       Close OBS and this will switch it on and reopen it for you.")
		}
		if waitEnter == nil {
			return false
		}
		// A few asks, not forever: an operator who wants to carry on with OBS as it is
		// (mid-stream, say) must not be stuck before the server ever starts.
		closed := false
		for i := 0; i < 3; i++ {
			if !Running() {
				closed = true
				break
			}
			waitEnter("Close OBS, then press Enter here (or just press Enter to carry on without it)...")
		}
		if !closed && Running() {
			say("  Carrying on with OBS as it is. Replays and camera cuts won't work until OBS's WebSocket is on.")
			return false
		}
		say("")
	}

	if password, changed, err := EnsureWebsocket(cfgDir, port); err != nil {
		say(fmt.Sprintf("  [!!] Couldn't switch on OBS's WebSocket server (%v).", err))
		say("       Do it by hand: OBS -> Tools -> WebSocket Server Settings.")
	} else {
		if changed {
			say("  [OK] Switched on OBS's WebSocket server.")
		}
		if c, _ := SyncConfigPassword(configFile, password); c {
			say("  [OK] Saved the OBS password in config.ini.")
		}
	}

	replayFolder := expandUser(ReadConfigValue(configFile, "OBS", "replay_folder"))
	if p, err := EnsureReplayBuffer(cfgDir, replayFolder); err != nil {
		say(fmt.Sprintf("  [!!] Couldn't pre-set the replay buffer (%v) — OBS setup will try again.", err))
	} else if p != "" {
		say("  [OK] Replay buffer on.")
	}

	stateFile := filepath.Join(filepath.Dir(absPath(configFile)), "match_state.json")
	switch strings.ToLower(ReadConfigValue(configFile, "Stream", "output_resolution")) {
	case "manual", "off", "no":
	default:
		res, fps, why := ChooseVideo(configFile, stateFile)
		if _, had, err := EnsureVideoSettings(cfgDir, res, fps); err != nil {
			say(fmt.Sprintf("  [!!] Couldn't set OBS's video settings (%v) — check them in OBS → Settings → Video.", err))
		} else if had {
			say(fmt.Sprintf("  [OK] Video: %s at %sfps (%s).", res, fps, why))
		}
	}

	switch strings.ToLower(ReadConfigValue(configFile, "Stream", "encoder")) {
	case "manual", "off", "no":
	default:
		if changed, msg, err := EnsureEncoder(cfgDir); err != nil {
			say(fmt.Sprintf("  [!!] Couldn't check OBS's video encoder (%v).", err))
		} else if changed {
			say("  [OK] " + msg)
		}
	}

	SkipFirstRunWizard(cfgDir) // a failure just means the wizard may appear on a new OBS
	if paused, _ := PauseUpdateCheck(cfgDir); paused {
		say("  [OK] OBS's update check is paused for the match (back on when OBS is closed).")
	}
	if ClearCrashSentinel(cfgDir) > 0 {
		say("  [OK] OBS didn't close properly last time; skipping its Safe Mode question.")
	}

	say("  Opening OBS...")
	if err := Launch(exe, LaunchArgsFor(cfgDir)); err != nil {
		say(fmt.Sprintf("  [!!] Couldn't open OBS (%v). Open it yourself, then carry on.", err))
		return false
	}
	if WaitForWebsocket(port, 45*time.Second) {
		say("  [OK] OBS is open and ready.")
		// Give OBS a moment past "port open" to finish loading the scene collection, so
		// setup's first requests don't race the UI coming up.
		sleep(3 * time.Second)
		return true
	}
	say("  [!!] OBS opened but isn't answering yet. If it's showing a first-run")
	say("       wizard, close that; OBS setup will carry on once it can connect.")
	return false
}

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func absPath(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
