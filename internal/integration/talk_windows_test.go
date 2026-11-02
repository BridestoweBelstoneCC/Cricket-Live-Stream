package integration

// Commentary from a phone, end to end with nothing faked but the voice: the built app, the
// clubhouse screen's real page in Chrome (its Commentary tab, pressed like a finger would),
// the link, the phone's real page in a second Chrome whose microphone plays a file, and a
// portable OBS recording to disk. The proof is in the recording: the phone's tone is on it
// only while they're on air (replay scene included), and the ground's tone dips under it
// and comes back.
//
//	set CRICKETSTREAM_OBS_PORTABLE=C:\obs-portable
//	set CRICKETSTREAM_TALK_SHOTS=C:\some\folder   (optional: screenshots, and the recording)
//	go test ./internal/integration/ -run TestPhoneCommentaryOnTheRecording -v -count=1
//
// The phone's Chrome goes through the real certificate warning, as a person would: nothing
// tells it to ignore the certificate, and the microphone has to work after that. What this
// can't show: Safari on an iPhone, a phone's own microphone, or a real ground's wifi. See
// CLUBHOUSE_SCREEN.md.
//
// CRICKETSTREAM_TALK_VOICE=<a wav of someone talking> swaps the tone for that, with pink
// noise for the ground, to make a recording worth listening to. The tone's checks are
// then skipped.

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"cricketstream/internal/assets"
	"cricketstream/internal/clubhouse"
	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

const chromeExe = `C:\Program Files\Google\Chrome\Application\chrome.exe`

// ── A small DevTools client: enough to press buttons and take pictures ──

type browser struct {
	t    *testing.T
	port int
	cmd  *exec.Cmd
}

func startChrome(t *testing.T, extra ...string) *browser {
	t.Helper()
	dir := t.TempDir()
	args := append([]string{"--headless=new", "--disable-gpu", "--no-first-run", "--remote-debugging-port=0",
		"--user-data-dir=" + dir, "--disable-background-timer-throttling", "--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows", "about:blank"}, extra...)
	cmd := exec.Command(chromeExe, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		time.Sleep(300 * time.Millisecond) // Windows: let go of the profile folder before TempDir's cleanup
	})
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort")); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(strings.Split(string(b), "\n")[0])); err == nil && p > 0 {
				return &browser{t: t, port: p, cmd: cmd}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Chrome never opened its debugging port")
	return nil
}

type tab struct {
	t  *testing.T
	mu sync.Mutex
	c  *websocket.Conn
	id int
}

func (b *browser) open(url string, w, h int) *tab {
	b.t.Helper()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("http://127.0.0.1:%d/json/new?about:blank", b.port), nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer r.Body.Close()
	var info struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	json.NewDecoder(r.Body).Decode(&info)
	c, _, err := websocket.DefaultDialer.Dial(info.WS, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { c.Close() })
	tb := &tab{t: b.t, c: c}
	tb.send("Page.enable", nil)
	tb.send("Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false})
	tb.send("Page.navigate", map[string]any{"url": url})
	return tb
}

func (tb *tab) send(method string, params any) map[string]any {
	tb.t.Helper()
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.id++
	id := tb.id
	if err := tb.c.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		tb.t.Fatal(err)
	}
	tb.c.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		var m map[string]any
		if err := tb.c.ReadJSON(&m); err != nil {
			tb.t.Fatalf("%s: %v", method, err)
		}
		if int(asFloat(m["id"])) == id {
			if e, ok := m["error"]; ok {
				tb.t.Fatalf("%s: %v", method, e)
			}
			r, _ := m["result"].(map[string]any)
			return r
		}
	}
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

// eval runs an expression in the page and returns its value.
func (tb *tab) eval(expr string) any {
	tb.t.Helper()
	r := tb.send("Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true})
	if ex, ok := r["exceptionDetails"]; ok {
		tb.t.Fatalf("%s: %v", expr, ex)
	}
	res, _ := r["result"].(map[string]any)
	return res["value"]
}

func (tb *tab) waitFor(what, expr string, within time.Duration) {
	tb.t.Helper()
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		if v, _ := tb.eval(expr).(bool); v {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	shot := tb.eval("document.body ? document.body.innerText.slice(0, 600) : ''")
	tb.t.Fatalf("never happened: %s\nthe page says: %v", what, shot)
}

func (tb *tab) visible(id string) string {
	return fmt.Sprintf(`(function(){var e=document.getElementById(%q);return !!e && e.offsetParent!==null;})()`, id)
}

func (tb *tab) click(id string) {
	tb.t.Helper()
	tb.eval(fmt.Sprintf(`document.getElementById(%q).click()`, id))
}

func (tb *tab) picture(path string) {
	tb.t.Helper()
	if path == "" {
		return
	}
	r := tb.send("Page.captureScreenshot", map[string]any{"format": "png"})
	b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(r["data"]))
	if err := os.WriteFile(path, b, 0o644); err != nil {
		tb.t.Error(err)
	}
}

// ── What's on the recording ──

// bandLevel is the mean level, in dB, of one frequency in a stretch of the recording.
func bandLevel(t *testing.T, ff, file string, from, to float64, hz int) float64 {
	t.Helper()
	cmd := exec.Command(ff, "-hide_banner", "-nostats", "-ss", fmt.Sprintf("%.2f", from), "-to", fmt.Sprintf("%.2f", to), "-i", file,
		"-vn", "-af", fmt.Sprintf("bandpass=f=%d:width_type=h:w=60,bandpass=f=%d:width_type=h:w=60,volumedetect", hz, hz), "-f", "null", "-")
	out, _ := cmd.CombinedOutput()
	m := regexp.MustCompile(`mean_volume: (-?[\d.]+|-inf) dB`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("ffmpeg gave no level for %.1f-%.1fs: %s", from, to, out)
	}
	if string(m[1]) == "-inf" {
		return -120
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return v
}

// ── The laptop as the screen's link sees it ──

type span struct {
	name     string
	from, to float64
}

// gaps is how many times the voice dropped out for over a quarter of a second, and for how
// long altogether. The test's tone never gets that quiet on its own.
func gaps(t *testing.T, ff, file string, from, to float64) (int, float64) {
	t.Helper()
	out, _ := exec.Command(ff, "-hide_banner", "-nostats", "-ss", fmt.Sprintf("%.2f", from), "-to", fmt.Sprintf("%.2f", to), "-i", file, "-vn",
		"-af", "bandpass=f=1000:width_type=h:w=60,bandpass=f=1000:width_type=h:w=60,silencedetect=n=-55dB:d=0.25", "-f", "null", "-").CombinedOutput()
	n, total := 0, 0.0
	for _, m := range regexp.MustCompile(`silence_duration: ([\d.]+)`).FindAllSubmatch(out, -1) {
		v, _ := strconv.ParseFloat(string(m[1]), 64)
		n++
		total += v
	}
	return n, total
}

func phoneClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func get(u string) any {
	r, err := http.Get(u)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	return m["overlay"]
}

type talkHost struct{ base string }

func (h talkHost) Status() map[string]any {
	return map[string]any{"phase": "running", "server_ready": true, "club": "Test CC"}
}
func (talkHost) StartMatchDay(bool) error            { return nil }
func (talkHost) Answer(int, bool, string, bool) bool { return false }
func (talkHost) StopMatchDay(bool) error             { return nil }
func (talkHost) ReadyToPowerOff() (bool, string)     { return false, "test" }
func (h talkHost) Server() (string, string)          { return h.base, "" }

func TestPhoneCommentaryOnTheRecording(t *testing.T) {
	root := os.Getenv(obs.PortableEnv)
	if root == "" {
		t.Skip("set " + obs.PortableEnv + " to a portable OBS copy to run this")
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		t.Fatalf("%s has no obs_portable_mode.txt — refusing", root)
	}
	if _, err := os.Stat(chromeExe); err != nil {
		t.Skip("needs Chrome")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("needs ffmpeg")
	}
	if obs.Running() {
		t.Skip("an OBS is already open; close it first (this must only ever drive the portable copy)")
	}
	shots := os.Getenv("CRICKETSTREAM_TALK_SHOTS")
	shot := func(name string) string {
		if shots == "" {
			return ""
		}
		return filepath.Join(shots, name)
	}
	own := ownOBSFiles(t)
	t.Cleanup(func() {
		for p, m := range ownOBSFiles(t) {
			if before, ok := own[p]; !ok || !before.Equal(m) {
				t.Errorf("this machine's own OBS settings changed: %s", p)
			}
		}
	})

	// ── The laptop ──
	app := t.TempDir()
	t.Setenv("CRICKETSTREAM_HOME", app)
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
	t.Setenv("CRICKETSTREAM_NO_BROWSER", "1")
	assets.SetDir(app)
	example, err := os.ReadFile("../../config.example.ini")
	if err != nil {
		t.Fatal(err)
	}
	replays := filepath.Join(app, "replays")
	os.MkdirAll(replays, 0o755)
	cfgPath := filepath.Join(app, "config.ini")
	os.WriteFile(cfgPath, []byte(strings.Replace(string(example), "replay_folder =", "replay_folder = "+replays, 1)), 0o644)
	statePath := filepath.Join(app, "match_state.json")
	if !obs.Prepare(cfgPath, nil, func(s string) { t.Log(s) }) {
		t.Fatal("OBS prep/launch failed")
	}
	t.Cleanup(func() {
		if !obs.Close(30 * time.Second) {
			obs.Kill()
		}
	})
	cfg, _ := ini.Load(cfgPath)
	target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: cfg.Get("OBS", "obs_password", "")}
	if ok, msgs := obs.SetupFromConfig(cfgPath, statePath, true, true); !ok {
		t.Fatalf("OBS setup failed: %s", strings.Join(msgs, "\n"))
	}

	// A portable copy keeps its scenes: what a last run left would hide the first-time setup.
	// Item by item: RemoveInput on a source in several scenes leaves it broken (obsops.go).
	for _, sc := range names(call(t, target, "GetSceneList", nil)["scenes"], "sceneName") {
		l, _ := call(t, target, "GetSceneItemList", map[string]any{"sceneName": sc})["sceneItems"].([]any)
		for _, it := range l {
			m := it.(map[string]any)
			if m["sourceName"] == "Commentary" || m["sourceName"] == "Cricket Camera" {
				call(t, target, "RemoveSceneItem", map[string]any{"sceneName": sc, "sceneItemId": m["sceneItemId"]})
			}
		}
	}
	for i := 0; i < 40; i++ {
		in := names(call(t, target, "GetInputList", nil)["inputs"], "inputName")
		if !has(in, "Commentary") && !has(in, "Cricket Camera") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	// The ground: a looping file standing in for the camera's own sound, a 400 Hz tone.
	ownVoice := os.Getenv("CRICKETSTREAM_TALK_VOICE")
	groundSound := "sine=f=400:d=20,volume=0.3"
	if ownVoice != "" {
		groundSound = "anoisesrc=c=pink:r=48000:a=0.08:d=20"
	}
	ground := filepath.Join(app, "ground.mkv")
	if out, err := exec.Command(ff, "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=s=640x360:r=25:d=20",
		"-f", "lavfi", "-i", groundSound, "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-b:a", "128k", ground).CombinedOutput(); err != nil {
		t.Fatalf("making the ground's tone: %v\n%s", err, out)
	}
	made := call(t, target, "CreateInput", map[string]any{"sceneName": "Main", "inputName": "Cricket Camera", "inputKind": "ffmpeg_source",
		"inputSettings": map[string]any{"local_file": ground, "is_local_file": true, "looping": true}, "sceneItemEnabled": true})
	// OBS puts a new source on top, and a real camera is moved under the graphics when it's
	// added (arrangeCameraItem): this one too, or it hides that corner of the overlay.
	call(t, target, "SetSceneItemIndex", map[string]any{"sceneName": "Main", "sceneItemId": made["sceneItemId"], "sceneItemIndex": 0})

	// The voice: a 1 kHz tone that comes and goes, as speech does. A steady one is what a
	// phone's noise suppression is for, and it fades it out.
	voice := filepath.Join(app, "voice.wav")
	if out, err := exec.Command(ff, "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"sine=f=1000:d=120,tremolo=f=3:d=0.9,volume=0.5", "-ac", "1", "-ar", "48000", voice).CombinedOutput(); err != nil {
		t.Fatalf("making the voice: %v\n%s", err, out)
	}
	if ownVoice != "" {
		voice = ownVoice
	}

	exe := filepath.Join(app, "CricketStream.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "../../cmd/cricketstream").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	port, talkPort := freePort(t), freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var srv *exec.Cmd
	var srvLog bytes.Buffer
	startServer := func() {
		srv = exec.Command(exe, "server")
		srv.Dir = app
		srv.Env = append(os.Environ(), fmt.Sprintf("CRICKETSTREAM_PORT=%d", port), fmt.Sprintf("CRICKETSTREAM_TALK_PORT=%d", talkPort))
		srv.Stdout, srv.Stderr = &srvLog, &srvLog
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
	}
	startServer()
	t.Cleanup(func() {
		srv.Process.Kill()
		srv.Wait()
		t.Logf("server log:\n%s", srvLog.String())
	})
	serverUp := false
	for i := 0; i < 60 && !serverUp; i++ {
		if r, err := http.Get(base + "/health"); err == nil {
			r.Body.Close()
			serverUp = r.StatusCode == 200
		} else {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !serverUp {
		t.Fatal("server never answered")
	}
	// OBS was set up before this server's port was known: as the launcher's restart would,
	// point the Overlay at it, so the strap has a page to be on.
	call(t, target, "SetInputSettings", map[string]any{"inputName": "Overlay", "inputSettings": map[string]any{"url": base + "/overlay"}})
	// The real link, on the real port, as the window runs it.
	const key = "K8Q27WMX"
	link, err := clubhouse.StartLink(clubhouse.Config{Key: key, Port: freePort(t), Name: "Test CC"}, talkHost{base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(link.Stop)

	// ── The clubhouse screen: the real program and page, in its own folder ──
	piDir := t.TempDir()
	piPort := freePort(t)
	pi := exec.Command(exe, "clubhouse", fmt.Sprintf("--port=%d", piPort))
	pi.Dir = piDir
	pi.Env = append(os.Environ(), "CRICKETSTREAM_HOME="+piDir)
	if err := pi.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pi.Process.Kill(); pi.Wait(); time.Sleep(200 * time.Millisecond) })
	piURL := fmt.Sprintf("http://127.0.0.1:%d/", piPort)
	for i := 0; i < 60; i++ {
		if r, err := http.Get(piURL); err == nil {
			r.Body.Close()
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	piChrome := startChrome(t)
	screen := piChrome.open(piURL, 1280, 720)
	screen.waitFor("the page to load", `document.readyState==='complete' && !!window.fetch`, 15*time.Second)
	paired := screen.eval(fmt.Sprintf(`fetch('/api/pair',{method:'POST',headers:{'X-Screen-Key':document.querySelector('meta[name=screen-key]').content,'Content-Type':'application/json'},
		body:JSON.stringify({key:%q,address:%q})}).then(function(r){return r.json()}).then(function(d){return JSON.stringify(d)})`, key, link.Addr().String()))
	if !strings.Contains(fmt.Sprint(paired), `"ok":true`) {
		t.Fatalf("pairing: %v", paired)
	}
	// Pairing leaves the page on the PIN step: no PIN, on to the tabs.
	screen.send("Page.reload", nil)
	screen.waitFor("the tabs", screen.visible("tab_talk"), 20*time.Second)
	screen.click("tab_talk")
	screen.waitFor("the Commentary tab", screen.visible("talk_switch"), 5*time.Second)
	screen.waitFor("the tab to say it's off", screen.visible("talk_off"), 5*time.Second)
	screen.picture(shot("pi-1-off.png"))
	// The older 7-inch display is 800x480: the switch must still be reachable.
	screen.send("Emulation.setDeviceMetricsOverride", map[string]any{"width": 800, "height": 480, "deviceScaleFactor": 1, "mobile": false})
	screen.waitFor("the switch on a small screen", `(function(){var r=document.getElementById('talk_switch').getBoundingClientRect();return r.right<=800&&r.bottom<=480;})()`, 3*time.Second)
	screen.send("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 720, "deviceScaleFactor": 1, "mobile": false})

	// ── Turn it on: the QR code comes up ──
	screen.click("talk_switch")
	screen.waitFor("the QR code", `(function(){var i=document.getElementById('talk_qr');return i.complete&&i.naturalWidth>100;})()`, 10*time.Second)
	screen.waitFor("OBS ready (the source made, its page playing)", `document.getElementById('talk_say').textContent===''`, 40*time.Second)
	screen.picture(shot("pi-2-on.png"))
	screen.send("Emulation.setDeviceMetricsOverride", map[string]any{"width": 800, "height": 480, "deviceScaleFactor": 1, "mobile": false})
	time.Sleep(300 * time.Millisecond)
	screen.waitFor("the whole code on a small screen", `(function(){var r=document.getElementById('talk_qr').getBoundingClientRect(),
		tabs=document.getElementById('tabs').getBoundingClientRect();return r.width>=160&&r.bottom<=tabs.top&&r.right<=800&&document.getElementById('pv').offsetParent===null;})()`, 3*time.Second)
	screen.picture(shot("pi-2-on-800x480.png"))
	screen.send("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 720, "deviceScaleFactor": 1, "mobile": false})
	status := screen.eval(`JSON.stringify(talkData)`)
	var st struct {
		URL string `json:"url"`
		Via string `json:"via"`
	}
	json.Unmarshal([]byte(fmt.Sprint(status)), &st)
	if st.Via != "lan" || !strings.HasPrefix(st.URL, "https://") || !strings.Contains(st.URL, fmt.Sprintf(":%d/talk?k=", talkPort)) {
		t.Fatalf("the QR code's address: %+v", st)
	}
	t.Logf("the QR code opens %s", regexp.MustCompile(`k=.*`).ReplaceAllString(st.URL, "k=..."))
	if shots != "" {
		os.WriteFile(shot("qr-url.txt"), []byte(st.URL), 0o644)
		if r, err := http.Get(base + "/talk/qr.png"); err == nil {
			var b bytes.Buffer
			b.ReadFrom(r.Body)
			r.Body.Close()
			os.WriteFile(shot("qr.png"), b.Bytes(), 0o644)
		}
	}

	// OBS got its source: in every match scene, sending to OBS's mixer.
	inputs := names(call(t, target, "GetInputList", nil)["inputs"], "inputName")
	if !has(inputs, "Commentary") {
		t.Fatalf("no Commentary source: %v", inputs)
	}
	for _, sc := range []string{"Main", "Replay"} {
		if !has(names(call(t, target, "GetSceneItemList", map[string]any{"sceneName": sc})["sceneItems"], "sourceName"), "Commentary") {
			t.Errorf("Commentary isn't in %s", sc)
		}
	}
	filters := names(call(t, target, "GetSourceFilterList", map[string]any{"sourceName": "Cricket Camera"})["filters"], "filterName")
	if !has(filters, "Commentary duck") {
		t.Errorf("no duck on the camera: %v", filters)
	}

	// ── The phone: the address in the code, its microphone playing the file ──
	phoneChrome := startChrome(t, "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream",
		"--use-file-for-fake-audio-capture="+voice)
	phone := phoneChrome.open(st.URL, 390, 844)
	// The warning a phone shows, and the way through it. The real page, the real links.
	phone.waitFor("the certificate warning", `!!document.getElementById('proceed-link') || !!document.getElementById('join')`, 15*time.Second)
	if v, _ := phone.eval(`!!document.getElementById('proceed-link')`).(bool); !v {
		t.Fatal("no certificate warning: is the certificate trusted on this machine?")
	}
	phone.picture(shot("phone-0-warning.png"))
	phone.eval(`document.getElementById('details-button').click(); document.getElementById('proceed-link').click()`)
	phone.waitFor("the phone's page", phone.visible("join"), 15*time.Second)
	if v, _ := phone.eval(`window.isSecureContext`).(bool); !v {
		t.Fatal("the phone's page isn't a secure context: no phone would give it the microphone")
	}
	phone.picture(shot("phone-1-name.png"))
	phone.eval(`document.getElementById('name').value='Sam Bell'`)

	// Past the warning, a second page load keeps the exception (a scan of the code again).
	// ── On the recording from here ──
	call(t, target, "SetCurrentProgramScene", map[string]any{"sceneName": "Main"})
	call(t, target, "SetRecordDirectory", map[string]any{"recordDirectory": app})
	call(t, target, "StartRecord", nil)
	recAt := func() float64 {
		return asFloat(call(t, target, "GetRecordStatus", nil)["outputDuration"]) / 1000
	}
	stopped := false
	stopRec := func() string {
		if stopped {
			return ""
		}
		stopped = true
		return fmt.Sprint(call(t, target, "StopRecord", nil)["outputPath"])
	}
	t.Cleanup(func() { stopRec() })
	time.Sleep(6 * time.Second)

	phone.click("join")
	phone.waitFor("asking the clubhouse", phone.visible("w_cancel"), 15*time.Second)
	phone.picture(shot("phone-2-waiting.png"))
	screen.waitFor("their name on the screen", `document.getElementById('talk_list').textContent.indexOf('Sam Bell')>=0`, 10*time.Second)
	screen.picture(shot("pi-3-asking.png"))
	// Waiting isn't on air: 5 seconds of the phone's microphone running, and not a sound of it.
	waitFrom := recAt()
	time.Sleep(5 * time.Second)
	waitTo := recAt()

	screen.eval(`Array.from(document.querySelectorAll('#talk_list button')).filter(function(b){return b.textContent==='Put on air'})[0].click()`)
	phone.waitFor("on air on the phone", phone.visible("a_leave"), 10*time.Second)
	screen.waitFor("on air on the screen", screen.visible("talk_air"), 10*time.Second)
	phone.waitFor("chunks reaching the laptop", `window.talkDebug().sent>10`, 10*time.Second)
	time.Sleep(2 * time.Second) // past the cushion and the duck's attack
	airFrom := recAt()
	time.Sleep(10 * time.Second)
	airTo := recAt()
	screen.waitFor("the meter to move", `parseInt(document.getElementById('talk_air_meter').style.width)>0`, 5*time.Second)
	phone.picture(shot("phone-3-on-air.png"))
	screen.picture(shot("pi-4-on-air.png"))
	// The overlay's strap, as OBS composes it.
	// It's anchored by its right edge beside the weather, so its left edge moves with the
	// name. At 1280 wide its row is y=63 and it starts somewhere from 843. Red alone would do
	// nothing here: the test picture has red bars. Red with the strap's dark fill just right
	// of it is the strap.
	strapRed := func(name string) bool {
		s := call(t, target, "GetSourceScreenshot", map[string]any{"sourceName": "Main", "imageFormat": "png", "imageWidth": 1280})
		data := strings.SplitN(fmt.Sprint(s["imageData"]), ",", 2)
		if len(data) != 2 {
			t.Fatalf("no screenshot from OBS: %v", s)
		}
		b, _ := base64.StdEncoding.DecodeString(data[1])
		if name != "" && shots != "" {
			os.WriteFile(shot(name), b, 0o644)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		for x := 843; x < 1090; x++ {
			r, g, _, _ := img.At(x, 63).RGBA()
			fr, fg, fb, _ := img.At(x+5, 63).RGBA()
			if r>>8 > 150 && g>>8 < 120 && fr>>8 < 70 && fg>>8 < 70 && fb>>8 < 70 {
				return true
			}
		}
		return false
	}
	onAir := false
	for i := 0; i < 40 && !onAir; i++ {
		onAir = strapRed("obs-main-on-air.png")
		if !onAir {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !onAir {
		t.Errorf("no on-air strap on OBS's picture; overlay health: %v", get(base+"/health"))
	}

	// Mid-replay they're still heard: the source is in that scene too.
	call(t, target, "SetCurrentProgramScene", map[string]any{"sceneName": "Replay"})
	time.Sleep(time.Second)
	replayFrom := recAt()
	time.Sleep(5 * time.Second)
	replayTo := recAt()
	call(t, target, "SetCurrentProgramScene", map[string]any{"sceneName": "Main"})
	time.Sleep(2 * time.Second)

	// Quieter from the screen: four presses is 12 dB. Then back up from the phone.
	for n := 0; n < 4; n++ {
		screen.click("talk_quieter")
		time.Sleep(250 * time.Millisecond)
	}
	screen.waitFor("the screen to show the volume", `document.getElementById('talk_vol').textContent==='\u221212 dB'`, 5*time.Second)
	phone.waitFor("the phone to show the volume", `document.getElementById('a_vol').textContent==='\u221212 dB'`, 5*time.Second)
	time.Sleep(time.Second)
	quietFrom := recAt()
	time.Sleep(6 * time.Second)
	quietTo := recAt()
	screen.picture(shot("pi-5-volume.png"))
	phone.picture(shot("phone-5-volume.png"))
	for n := 0; n < 4; n++ {
		phone.click("a_louder")
		time.Sleep(250 * time.Millisecond)
	}
	screen.waitFor("the screen to see the phone's change", `document.getElementById('talk_vol').textContent==='Normal'`, 5*time.Second)
	time.Sleep(time.Second)

	// Muted from the screen: not heard, the ground comes back, and the phone's told.
	screen.click("talk_mute")
	phone.waitFor("the phone told", `document.getElementById('a_tag').textContent==='MUTED'`, 5*time.Second)
	time.Sleep(2 * time.Second)
	opMuteFrom := recAt()
	time.Sleep(5 * time.Second)
	opMuteTo := recAt()
	screen.picture(shot("pi-6-muted.png"))
	phone.picture(shot("phone-6-muted.png"))
	screen.click("talk_mute")
	phone.waitFor("the phone told it's back", `document.getElementById('a_tag').textContent==='ON AIR'`, 5*time.Second)
	time.Sleep(2 * time.Second)

	// Mute on the phone: nothing heard, the ground comes back, still on air.
	phone.click("a_mute")
	time.Sleep(2 * time.Second)
	muteFrom := recAt()
	time.Sleep(5 * time.Second)
	muteTo := recAt()
	phone.click("a_mute")
	time.Sleep(2 * time.Second)

	// ── What goes wrong in a match ──
	var hurdles []span
	heardAgain := func(name, what string) {
		t.Helper()
		phone.waitFor(name+": back on air on the phone", `(function(){var d=talkDebug();return document.getElementById('a_tag').textContent==='ON AIR'&&d.track==='live'&&d.ctx==='running'&&!d.micDown&&d.contact;})()`, 30*time.Second)
		phone.waitFor(name+": "+what, `window.talkDebug().sent>0`, 5*time.Second)
		time.Sleep(2 * time.Second)
		from := recAt()
		time.Sleep(4 * time.Second)
		hurdles = append(hurdles, span{name, from, recAt()})
	}
	// The mic stops (a call, the phone taking it back): started again without anyone asked.
	phone.eval(`stream.getTracks()[0].stop()`)
	phone.waitFor("the phone to notice the mic has stopped", `window.talkDebug().micDown||window.talkDebug().track==='live'`, 6*time.Second)
	heardAgain("mic stopped", "audio flowing again")
	// The page reloads (an iPhone throwing away a background tab): back where they were.
	phone.send("Page.reload", nil)
	phone.waitFor("the page back, still on air", phone.visible("a_leave"), 20*time.Second)
	heardAgain("page reload", "audio flowing again")
	// The wifi drops, for longer than the strap waits. No yes needed when it's back.
	phone.send("Network.enable", nil)
	phone.send("Network.emulateNetworkConditions", map[string]any{"offline": true, "latency": 0, "downloadThroughput": 0, "uploadThroughput": 0})
	phone.waitFor("the phone to say it's reconnecting", `document.getElementById('a_tag').textContent==='RECONNECTING'`, 15*time.Second)
	phone.picture(shot("phone-7-reconnecting.png"))
	screen.waitFor("the screen to say it's lost them", `document.getElementById('talk_air_line').textContent.indexOf('Lost touch')>=0`, 30*time.Second)
	screen.picture(shot("pi-7-lost.png"))
	var onAirNow struct {
		OnAir bool `json:"on_air"`
	}
	if r, err := http.Get(base + "/talk/onair"); err == nil {
		json.NewDecoder(r.Body).Decode(&onAirNow)
		r.Body.Close()
	}
	if onAirNow.OnAir {
		t.Error("the strap stayed up for a phone with no signal")
	}
	phone.send("Network.emulateNetworkConditions", map[string]any{"offline": false, "latency": 0, "downloadThroughput": -1, "uploadThroughput": -1})
	heardAgain("wifi back", "audio flowing again")
	// The server dies mid-match, and the launcher starts another.
	srv.Process.Kill()
	srv.Wait()
	phone.waitFor("the phone to say it's reconnecting", `document.getElementById('a_tag').textContent==='RECONNECTING'`, 15*time.Second)
	startServer()
	heardAgain("server restart", "audio flowing again")
	screen.waitFor("the screen still showing them", `(function(){var e=document.getElementById('talk_air_name');return e.offsetParent!==null&&e.textContent==='Sam Bell';})()`, 15*time.Second)

	// A long match, if asked for: CRICKETSTREAM_TALK_SOAK=<minutes>.
	soakFrom := recAt()
	if m, _ := strconv.Atoi(os.Getenv("CRICKETSTREAM_TALK_SOAK")); m > 0 {
		t.Logf("on air for %d minutes...", m)
		time.Sleep(time.Duration(m) * time.Minute)
	}
	soakTo := recAt()

	// Off air from the screen: the phone's told, and the sound comes back.
	screen.click("talk_air_end")
	phone.waitFor("the phone told it's off air", phone.visible("e_title"), 10*time.Second)
	phone.picture(shot("phone-4-off.png"))
	time.Sleep(2 * time.Second)
	afterFrom := recAt()
	time.Sleep(6 * time.Second)
	afterTo := recAt()
	file := stopRec()
	time.Sleep(2 * time.Second)
	if shots != "" {
		if b, err := os.ReadFile(file); err == nil {
			os.WriteFile(shot("recording"+filepath.Ext(file)), b, 0o644)
		}
	}

	if shots != "" {
		exec.Command(ff, "-y", "-hide_banner", "-loglevel", "error", "-i", file, "-vn", "-ac", "2", "-b:a", "128k", shot("recording.m4a")).Run()
	}

	// ── What's on the recording ──
	levels := map[string][2]float64{}
	for _, s := range []span{{"waiting", waitFrom, waitTo}, {"on air", airFrom, airTo}, {"replay", replayFrom, replayTo},
		{"quieter", quietFrom, quietTo}, {"op-muted", opMuteFrom, opMuteTo}, {"muted", muteFrom, muteTo}, {"after", afterFrom, afterTo}} {
		v, g := bandLevel(t, ff, file, s.from, s.to, 1000), bandLevel(t, ff, file, s.from, s.to, 400)
		levels[s.name] = [2]float64{v, g}
		t.Logf("%-8s %5.1f-%5.1fs  voice (1 kHz) %6.1f dB   ground (400 Hz) %6.1f dB", s.name, s.from, s.to, v, g)
	}
	if ownVoice != "" {
		return
	}
	for _, s := range hurdles {
		v := bandLevel(t, ff, file, s.from, s.to, 1000)
		t.Logf("after %-15s %5.1f-%5.1fs  voice %6.1f dB", s.name, s.from, s.to, v)
		if v < -40 {
			t.Errorf("not heard again after %s: %.1f dB", s.name, v)
		}
	}
	if soakTo-soakFrom > 60 {
		n, total := gaps(t, ff, file, soakFrom, soakTo)
		t.Logf("%.0f minutes on air: %d dropouts, %.1fs altogether", (soakTo-soakFrom)/60, n, total)
		for f := soakFrom; f+30 < soakTo; f += (soakTo - soakFrom) / 6 {
			t.Logf("  at %5.1f min: voice %6.1f dB, ground %6.1f dB", f/60, bandLevel(t, ff, file, f, f+30, 1000), bandLevel(t, ff, file, f, f+30, 400))
		}
		if total > (soakTo-soakFrom)*0.005 {
			t.Errorf("the voice dropped out for %.1fs of %.0fs", total, soakTo-soakFrom)
		}
	}
	voiceAt := func(n string) float64 { return levels[n][0] }
	groundAt := func(n string) float64 { return levels[n][1] }
	if voiceAt("waiting") > -60 {
		t.Errorf("heard while still waiting for a yes: %.1f dB", voiceAt("waiting"))
	}
	if voiceAt("on air") < -40 {
		t.Errorf("not on the recording while on air: %.1f dB", voiceAt("on air"))
	}
	if voiceAt("replay") < -40 {
		t.Errorf("not heard during a replay: %.1f dB", voiceAt("replay"))
	}
	if drop := voiceAt("on air") - voiceAt("quieter"); drop < 9 || drop > 15 {
		t.Errorf("12 dB quieter came out %.1f dB quieter", drop)
	}
	if voiceAt("op-muted") > -60 {
		t.Errorf("heard while muted from the screen: %.1f dB", voiceAt("op-muted"))
	}
	if back := groundAt("op-muted") - groundAt("waiting"); back < -2 || back > 2 {
		t.Errorf("the ground didn't come back under a mute from the screen: %.1f dB", back)
	}
	if voiceAt("muted") > -60 {
		t.Errorf("heard while muted: %.1f dB", voiceAt("muted"))
	}
	if voiceAt("after") > -60 {
		t.Errorf("heard after going off air: %.1f dB", voiceAt("after"))
	}
	if dip := groundAt("waiting") - groundAt("on air"); dip < 5 {
		t.Errorf("the ground dipped only %.1f dB under the voice", dip)
	}
	if back := groundAt("after") - groundAt("waiting"); back < -2 || back > 2 {
		t.Errorf("the ground came back %.1f dB from where it was", back)
	}
	if back := groundAt("muted") - groundAt("waiting"); back < -2 || back > 2 {
		t.Errorf("the ground didn't come back while muted: %.1f dB", back)
	}

	// ── The strap comes down with them ──
	gone := false
	for i := 0; i < 40 && !gone; i++ {
		gone = !strapRed("")
		if !gone {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !gone {
		t.Error("the strap stayed up after they went off air")
	}

	// ── Off ──
	screen.waitFor("the switch still on", `document.getElementById('talk_switch').getAttribute('aria-checked')==='true'`, 5*time.Second)
	screen.click("talk_switch")
	screen.waitFor("the code gone", screen.visible("talk_off"), 10*time.Second)
	// The port has closed behind it: the code in anyone's camera roll goes nowhere.
	if r, err := phoneClient().Get(st.URL); err == nil {
		r.Body.Close()
		t.Errorf("the phone's port still answers with commentary off: %d", r.StatusCode)
	}
}
