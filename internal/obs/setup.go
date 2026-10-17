package obs

// OBS auto-setup over the WebSocket (OBS open): Main and Replay scenes, the Overlay
// browser source, the ReplayClip media source, stacking, replay folder, stream key,
// bitrate reset, dynamic bitrate, and the replay buffer — enabled AND started.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"cricketstream/internal/ini"
	"cricketstream/internal/obsws"
	"cricketstream/internal/port"
	"cricketstream/internal/pyval"
)

// SetupOptions are what setup needs to know.
type SetupOptions struct {
	Target       obsws.Target
	ReplayFolder string
	ServerPort   int
	Verbose      bool
	StreamKey    string
	BitrateKbps  int
}

type setupLog struct {
	lines   []string
	verbose bool
}

func (l *setupLog) add(msg, status string) {
	icons := map[string]string{"ok": "  ✓", "warn": "  ⚠", "err": "  ✗", "": "   "}
	line := icons[status] + " " + msg
	l.lines = append(l.lines, line)
	if l.verbose {
		fmt.Println(line)
	}
}

func items(resp *obsws.Response) []map[string]any {
	if resp == nil || !resp.OK {
		return nil
	}
	list, _ := resp.Data["sceneItems"].([]any)
	var out []map[string]any
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func names(resp *obsws.Response, listKey, nameKey string) []string {
	if resp == nil || !resp.OK {
		return nil
	}
	list, _ := resp.Data[listKey].([]any)
	var out []string
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			if s, ok := m[nameKey].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func comment(r *obsws.Response, err error) string {
	if err != nil || r == nil {
		return "no response"
	}
	return r.Comment
}

func ok(r *obsws.Response, err error) bool { return err == nil && r != nil && r.OK }

// Setup is the full OBS setup over the WebSocket. Returns success and its messages.
func Setup(o SetupOptions) (bool, []string) {
	log := &setupLog{verbose: o.Verbose}
	if o.ServerPort == 0 {
		o.ServerPort = port.Server()
	}
	c, err := obsws.Dial(o.Target, 6*time.Second)
	if err != nil {
		port := o.Target.Port
		if port == 0 {
			port = WSPort
		}
		host := o.Target.Host
		if host == "" {
			host = "localhost"
		}
		log.add(fmt.Sprintf("Cannot connect to OBS at ws://%s:%d — is OBS open?", host, port), "err")
		log.add(fmt.Sprintf("Error: %v", err), "err")
		log.add("Enable WebSocket: OBS → Tools → WebSocket Server Settings → Enable", "warn")
		return false, log.lines
	}
	defer c.Close()
	req := func(t string, d any) (*obsws.Response, error) { return c.Request(t, d) }
	log.add("Connected to OBS", "ok")

	// ── Scenes ──
	existingScenes := names(must(req("GetSceneList", nil)), "scenes", "sceneName")
	for _, scene := range []string{"Main", "Replay"} {
		if has(existingScenes, scene) {
			log.add(fmt.Sprintf("Scene '%s' already exists", scene), "ok")
		} else if ok(req("CreateScene", map[string]any{"sceneName": scene})) {
			log.add(fmt.Sprintf("Scene '%s' created", scene), "ok")
		} else {
			log.add(fmt.Sprintf("Failed to create scene '%s'", scene), "err")
		}
	}
	existingInputs := names(must(req("GetInputList", nil)), "inputs", "inputName")

	// ── Overlay browser source in Main ──
	overlay := map[string]any{
		"url": fmt.Sprintf("http://localhost:%d/overlay", o.ServerPort), "width": 1920, "height": 1080,
		"fps": 24, "fps_custom": true, "shutdown": false, "restart_when_active": false,
		// Overlay audio (spoken commentary) goes through OBS's mixer and into the stream,
		// not just out of the laptop's speakers.
		"reroute_audio": true,
	}
	if has(existingInputs, "Overlay") {
		req("SetInputSettings", map[string]any{"inputName": "Overlay", "inputSettings": overlay})
		log.add("Overlay browser source settings updated", "ok")
	} else {
		overlay["css"] = ""
		r, err := req("CreateInput", map[string]any{"sceneName": "Main", "inputName": "Overlay",
			"inputKind": "browser_source", "inputSettings": overlay})
		if ok(r, err) {
			log.add("Overlay browser source created in Main", "ok")
		} else {
			log.add("Overlay source issue: "+comment(r, err), "warn")
		}
	}

	// ── ReplayClip media source in Replay ──
	if has(existingInputs, "ReplayClip") {
		req("SetInputSettings", map[string]any{"inputName": "ReplayClip", "inputSettings": map[string]any{
			"restart_on_activate": true, "looping": false, "local_file": ""}})
		log.add("ReplayClip media source settings updated", "ok")
	} else {
		r, err := req("CreateInput", map[string]any{"sceneName": "Replay", "inputName": "ReplayClip",
			"inputKind": "ffmpeg_source", "inputSettings": map[string]any{
				"restart_on_activate": true, "looping": false, "local_file": "", "is_local_file": true}})
		if ok(r, err) {
			log.add("ReplayClip media source created in Replay", "ok")
		} else {
			log.add("ReplayClip source issue: "+comment(r, err), "warn")
		}
	}

	// ── Graphics on top, replay clip full-screen ──
	// OBS puts every newly added source on TOP of its scene, so a camera added after this
	// setup covered the Overlay in Main and the ReplayClip in Replay (verified on OBS
	// 32.1.1). Re-asserting the order every run also repairs an install where that
	// already happened. The ReplayClip is fitted to the canvas because replays are
	// recorded at the OUTPUT resolution (720p by default).
	for _, pair := range [][2]string{{"Main", "Overlay"}, {"Replay", "ReplayClip"}} {
		scene, source := pair[0], pair[1]
		lst := items(must(req("GetSceneItemList", map[string]any{"sceneName": scene})))
		var item map[string]any
		for _, it := range lst {
			if it["sourceName"] == source {
				item = it
			}
		}
		if item == nil {
			continue
		}
		id := item["sceneItemId"]
		if source == "ReplayClip" {
			tr := map[string]any{}
			if r, err := req("GetSceneItemTransform", map[string]any{"sceneName": scene, "sceneItemId": id}); ok(r, err) {
				if m, isMap := r.Data["sceneItemTransform"].(map[string]any); isMap {
					tr = m
				}
			}
			if UntouchedPlacement(tr) {
				w, h := 1920, 1080
				if r, err := req("GetVideoSettings", nil); ok(r, err) {
					w = pyval.IntOr(r.Data["baseWidth"], 0)
					h = pyval.IntOr(r.Data["baseHeight"], 0)
					if w == 0 {
						w = 1920
					}
					if h == 0 {
						h = 1080
					}
				}
				req("SetSceneItemTransform", map[string]any{"sceneName": scene, "sceneItemId": id,
					"sceneItemTransform": FillCanvasTransform(w, h)})
			}
		}
		// Any camera/video covering it is moved DOWN to just beneath it; nothing else
		// moves, so a sponsor logo someone put above the scorebar stays there.
		var moved []string
		for range lst { // one camera per pass; re-read after each move
			over := CoveringPictures(lst, source)
			if len(over) == 0 {
				break
			}
			mine := 0
			for _, it := range lst {
				if it["sourceName"] == source {
					mine = itemIndex(it)
				}
			}
			req("SetSceneItemIndex", map[string]any{"sceneName": scene,
				"sceneItemId": over[0]["sceneItemId"], "sceneItemIndex": mine})
			n, _ := over[0]["sourceName"].(string)
			moved = append(moved, n)
			lst = items(must(req("GetSceneItemList", map[string]any{"sceneName": scene})))
		}
		if len(moved) > 0 {
			log.add(fmt.Sprintf("%s moved back under the %s in %s (it had covered it)", strings.Join(moved, ", "), source, scene), "ok")
		}
	}

	// ── Point OBS's recording/replay output at the configured folder ──
	// config.ini's replay_folder is where the server LOOKS for saved clips; without this,
	// OBS could be SAVING them somewhere else and replays would silently never find one.
	if o.ReplayFolder != "" {
		os.MkdirAll(o.ReplayFolder, 0o755)
		if isDir(o.ReplayFolder) {
			if ok(req("SetProfileParameter", map[string]any{"parameterCategory": "SimpleOutput",
				"parameterName": "FilePath", "parameterValue": o.ReplayFolder})) {
				log.add("Replay/recording folder set to "+o.ReplayFolder, "ok")
			} else {
				log.add("Could not set the replay folder — set it in OBS Settings → Output", "warn")
			}
		} else {
			log.add("Replay folder doesn't exist and couldn't be created: "+o.ReplayFolder, "warn")
		}
	}

	live := func() bool {
		r, err := req("GetStreamStatus", nil)
		return ok(r, err) && pyval.Truthy(r.Data["outputActive"])
	}
	outputMode := func() string {
		if r, err := req("GetProfileParameter", map[string]any{"parameterCategory": "Output", "parameterName": "Mode"}); ok(r, err) {
			if s, _ := r.Data["parameterValue"].(string); s != "" {
				return s
			}
		}
		return "Simple"
	}

	// ── The YouTube stream key (key-based streaming) ──
	// Key-based streaming survives restarts and quality changes; OBS's "connect account"
	// mode with auto-stop ENDS the broadcast whenever the stream stops. Never touched
	// while a stream is actually live.
	if o.StreamKey != "" {
		if live() {
			log.add("Stream is LIVE — leaving OBS's stream settings alone", "warn")
		} else if ok(req("SetStreamServiceSettings", map[string]any{"streamServiceType": "rtmp_common",
			"streamServiceSettings": map[string]any{"service": "YouTube - RTMPS",
				"server": "rtmps://a.rtmps.youtube.com:443/live2", "key": o.StreamKey}})) {
			log.add("YouTube stream key applied — key-based streaming (survives restarts and quality changes)", "ok")
		} else {
			log.add("Could not apply the stream key — set it in OBS Settings → Stream", "warn")
		}
	}

	// ── Reset the video bitrate to the configured baseline ──
	// The stream-quality ladder writes SimpleOutput/VBitrate when it downshifts for
	// congestion, and that value PERSISTS after the server exits — its step counter does
	// not. Nothing put the bitrate back, so a downshifted match handed the next match day
	// a silently-crippled stream from the first ball (bit twice for real: a leftover 875
	// kbps against a >2700 kbps recommendation). Applied on every run, never while live,
	// and only in Simple output mode (Advanced ignores SimpleOutput/VBitrate).
	if o.BitrateKbps != 0 {
		switch {
		case live():
			log.add(fmt.Sprintf("Stream is LIVE — leaving the bitrate alone (would reset to %d kbps)", o.BitrateKbps), "warn")
		case outputMode() != "Simple":
			log.add("OBS is in Advanced output mode — bitrate reset only applies to Simple output, skipped", "warn")
		case ok(req("SetProfileParameter", map[string]any{"parameterCategory": "SimpleOutput",
			"parameterName": "VBitrate", "parameterValue": strconv.Itoa(o.BitrateKbps)})):
			log.add(fmt.Sprintf("Video bitrate reset to %d kbps (undoes any leftover downshift from a previous match)", o.BitrateKbps), "ok")
		default:
			log.add(fmt.Sprintf("Could not reset the bitrate — check it manually in OBS Settings → Output (should be %d kbps)", o.BitrateKbps), "warn")
		}
	}

	// ── OBS's Dynamic Bitrate (congestion handled without disconnects) ──
	// Off by default in OBS. With it on, the encoder bitrate flexes when the connection
	// struggles — the seamless first line of defence. (The server's stream sentinel is
	// the second line.)
	if ok(req("SetProfileParameter", map[string]any{"parameterCategory": "Output",
		"parameterName": "DynamicBitrate", "parameterValue": "true"})) {
		log.add("Dynamic bitrate enabled — congestion managed without disconnects", "ok")
	} else {
		log.add("Could not enable dynamic bitrate — turn it on in OBS Settings → Advanced → Network", "warn")
	}

	req("SetCurrentProgramScene", map[string]any{"sceneName": "Main"})
	log.add("Active scene set to Main", "ok")

	// ── Enable AND start the replay buffer ──
	// Starting alone fails when the buffer isn't ENABLED in OBS's output settings — the
	// default for a fresh OBS. There's no WebSocket request to enable it, but the profile
	// parameter behind the checkbox is settable. Set it in BOTH sections: OBS honours only
	// the one matching the current mode, so enabling just the active one leaves replays
	// silently off after a later switch to Advanced.
	mode := outputMode()
	enabledOK := false
	for _, cat := range []string{"SimpleOutput", "AdvOut"} {
		set := ok(req("SetProfileParameter", map[string]any{"parameterCategory": cat,
			"parameterName": "RecRB", "parameterValue": "true"}))
		if set {
			// Long enough to cover the run-up and the shot.
			req("SetProfileParameter", map[string]any{"parameterCategory": cat,
				"parameterName": "RecRBTime", "parameterValue": "25"})
		}
		active := "SimpleOutput"
		if mode != "Simple" {
			active = "AdvOut"
		}
		if cat == active {
			enabledOK = set
		}
	}
	if enabledOK {
		log.add(fmt.Sprintf("Replay buffer enabled, 25s (%s output mode — and pre-set for the other mode, so switching later won't turn replays off)", mode), "ok")
	} else {
		log.add("Could not enable the replay buffer automatically — tick it in OBS Settings → Output → Replay Buffer", "warn")
	}

	rbActive := false
	if r, err := req("GetReplayBufferStatus", nil); ok(r, err) {
		rbActive = pyval.Truthy(r.Data["outputActive"])
	}
	if rbActive {
		log.add("Replay buffer already running", "ok")
	} else if r, err := req("StartReplayBuffer", nil); ok(r, err) {
		log.add("Replay buffer started", "ok")
	} else {
		e := comment(r, err)
		if r == nil {
			e = "no response"
		}
		low := strings.ToLower(e)
		switch {
		case strings.Contains(low, "not active") || strings.Contains(low, "already"):
			log.add("Replay buffer started", "ok")
		case enabledOK:
			// We just ticked the box, but an output that was disabled when OBS started may
			// only be creatable after a restart. Say so specifically.
			log.add("Replay buffer enabled but wouldn't start yet — restart OBS once and re-run setup; it starts automatically from then on", "warn")
		default:
			if e == "" {
				e = "enable in OBS Settings → Output → Replay Buffer"
			}
			log.add("Replay buffer: "+e, "warn")
		}
	}
	return true, log.lines
}

func must(r *obsws.Response, err error) *obsws.Response {
	if err != nil {
		return nil
	}
	return r
}

// Swappable for tests: the setup run, closing OBS, and preparing it.
var (
	runSetup    = Setup
	closeOBS    = Close
	prepareOBS  = Prepare
	manageOBSOn = ManageOBSEnabled
)

// SetupFromConfig runs Setup with everything read from config.ini — the one place that
// does it, for the launcher, the control panel's "Start OBS" checklist button, and
// `cricketstream obs-setup`.
//
// A blank bitrate_kbps uses the last upload-speed test's recommendation (from
// match_state.json at statePath), which also undoes a leftover quality downshift;
// "manual" leaves OBS's bitrate alone; no test yet leaves it alone too.
//
// allowRestart: on a brand-new OBS the replay buffer has just been switched on, but OBS
// only builds outputs when it starts, so it can't run until OBS restarts. When we manage
// OBS, do it for them — close OBS cleanly (it saves the new scenes on the way out) and
// reopen it. Never with anything on air: setup refuses to touch a live OBS, and Close
// gives up if OBS asks to confirm.
func SetupFromConfig(configPath, statePath string, verbose, allowRestart bool) (bool, []string) {
	cfg, _ := ini.Load(configPath)
	raw := strings.TrimSpace(cfg.Get("Stream", "bitrate_kbps", ""))
	bitrate, err := strconv.Atoi(raw)
	if err != nil {
		bitrate = 0
	}
	switch strings.ToLower(raw) {
	case "manual", "off", "no":
	default:
		if bitrate == 0 && statePath != "" {
			if mbps, ok := LastUploadMbps(statePath); ok {
				bitrate = RecommendStreamSettings(mbps).BitrateKbps
			}
		}
	}
	run := func() (bool, []string) {
		// Re-read: on a first run Prepare may have just written the password.
		cfg, _ := ini.Load(configPath)
		return runSetup(SetupOptions{
			Target:       obsws.Target{Host: "localhost", Port: WSPort, Password: cfg.Get("OBS", "obs_password", "")},
			ReplayFolder: expandUser(cfg.Get("OBS", "replay_folder", "")),
			StreamKey:    strings.TrimSpace(cfg.Get("Stream", "youtube_stream_key", "")),
			BitrateKbps:  bitrate,
			Verbose:      verbose,
		})
	}
	okRun, messages := run()
	needsRestart := false
	for _, m := range messages {
		if strings.Contains(m, "wouldn't start yet") {
			needsRestart = true
		}
	}
	if okRun && allowRestart && needsRestart && manageOBSOn(configPath) && closeOBS(30*time.Second) {
		prepareOBS(configPath, nil, nil)
		var again []string
		okRun, again = run()
		messages = append(append(messages, "  ✓ Restarted OBS once so the replay buffer could start (first run only)"), again...)
	}
	return okRun, messages
}
