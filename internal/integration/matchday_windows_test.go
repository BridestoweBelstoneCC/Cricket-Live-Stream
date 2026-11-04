package integration

// A whole match day with everything on, and things going wrong during it. The real app (the
// window's own code, with no window) runs match day against a portable OBS that streams to a
// local receiver, with a simulated match, two cameras, the clubhouse screen and a phone on
// air. Then the server is killed in the middle of a replay, hung, OBS crashes, OBS freezes,
// the line drops, the line goes slow, the scorer's file goes bad. What's measured is what
// viewers get: bytes reaching the receiver, second by second, and what's on air afterwards.
//
// Opt-in, about 30 minutes:
//
//	set CRICKETSTREAM_OBS_PORTABLE=C:\obs-portable
//	set CRICKETSTREAM_MATCHDAY=1
//	set CRICKETSTREAM_MATCHDAY_OUT=C:\somewhere   (screenshots and logs; optional)
//	go test ./internal/integration/ -run MatchDayWithEverythingOn -v -count=1 -timeout 70m

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

// mdLine is the proxy in front of the receiver: it counts what OBS gets through, and can be
// cut or slowed.
type mdLine struct {
	bytes atomic.Int64
	conns atomic.Int64
	kbps  atomic.Int64
	down  atomic.Bool
	mu    sync.Mutex
	open  map[net.Conn]bool
	sink  int
}

func (l *mdLine) listen(t *testing.T, port int, stop <-chan struct{}) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	go func() { <-stop; ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if l.down.Load() {
				c.Close()
				continue
			}
			go l.relay(c.(*net.TCPConn))
		}
	}()
}

func (l *mdLine) relay(in *net.TCPConn) {
	defer in.Close()
	in.SetReadBuffer(64 << 10)
	var out net.Conn
	for i := 0; i < 50 && out == nil; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.sink)); err == nil {
			out = c
		} else {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if out == nil {
		return
	}
	defer out.Close()
	l.conns.Add(1)
	l.mu.Lock()
	l.open[in] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.open, in); l.mu.Unlock() }()
	go io.Copy(in, out)
	buf := make([]byte, 8192)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return
			}
			l.bytes.Add(int64(n))
			if k := l.kbps.Load(); k > 0 {
				time.Sleep(time.Duration(float64(n*8) / float64(k*1000) * float64(time.Second)))
			}
		}
		if err != nil {
			return
		}
	}
}

// cut drops what's connected and refuses what comes next, as a dead line would.
func (l *mdLine) cut() {
	l.down.Store(true)
	l.mu.Lock()
	for c := range l.open {
		c.Close()
	}
	l.mu.Unlock()
}

// mdPhone is someone on air: the page's requests, over the real HTTPS port.
type mdPhone struct {
	base       string
	id, secret string
	client     *http.Client
	stop       chan struct{}
	done       chan struct{}
	sent, fail atomic.Int64
	state      atomic.Value
	seq        int64
	phase      float64
}

func (p *mdPhone) post(path string, body any) (map[string]any, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var d map[string]any
	json.NewDecoder(r.Body).Decode(&d)
	if s, ok := d["state"].(string); ok {
		p.state.Store(s)
	}
	return d, nil
}

func (p *mdPhone) chunk() []byte {
	const n = 3600 // 0.15s at 24 kHz
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		p.phase += 2 * math.Pi * 1000 / 24000
		binary.LittleEndian.PutUint16(b[i*2:], uint16(int16(math.Sin(p.phase)*14000)))
	}
	return b
}

func (p *mdPhone) talk() {
	defer close(p.done)
	tick, poll := time.NewTicker(150*time.Millisecond), time.NewTicker(2*time.Second)
	defer tick.Stop()
	defer poll.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-poll.C:
			p.post("/talk/poll", map[string]any{"id": p.id, "secret": p.secret})
		case <-tick.C:
			req, _ := http.NewRequest("POST", fmt.Sprintf("%s/talk/audio?seq=%d", p.base, p.seq), bytes.NewReader(p.chunk()))
			p.seq++
			req.Header.Set("X-Talk-Id", p.id)
			req.Header.Set("X-Talk-Secret", p.secret)
			if r, err := p.client.Do(req); err != nil {
				p.fail.Add(1)
			} else {
				var d map[string]any
				json.NewDecoder(r.Body).Decode(&d)
				r.Body.Close()
				if s, ok := d["state"].(string); ok {
					p.state.Store(s)
				}
				p.sent.Add(1)
			}
		}
	}
}

// mdListener is one server port's owner, found the way an operator would.
func mdPortOwner(port int) int {
	want := fmt.Sprintf(":%d", port)
	// netstat sometimes comes back without the listening ports, several times running, while
	// the table's changing: this test took that for "nothing there", and "killed" pid 0. So
	// Windows' own cmdlet has the last word.
	for try := 0; try < 3; try++ {
		out, err := exec.Command("netstat", "-ano", "-p", "TCP").Output()
		if err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				f := strings.Fields(l)
				if len(f) >= 5 && f[3] == "LISTENING" && strings.HasSuffix(f[1], want) {
					pid, _ := strconv.Atoi(f[4])
					return pid
				}
			}
		}
		out, err = exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf("(Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1).OwningProcess", port)).Output()
		if err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(out))); pid != 0 {
				return pid
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0
}

// mdAlive: the process is still running.
func mdAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}

// mdSuspend freezes a process, which is what a hang looks like from outside.
func mdSuspend(pid int) error {
	h, err := windows.OpenProcess(0x0800, false, uint32(pid)) // PROCESS_SUSPEND_RESUME
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	r, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSuspendProcess").Call(uintptr(h))
	if r != 0 {
		return fmt.Errorf("NtSuspendProcess: %#x", r)
	}
	return nil
}

func TestMatchDayWithEverythingOn(t *testing.T) {
	if os.Getenv("CRICKETSTREAM_MATCHDAY") != "1" {
		t.Skip("set CRICKETSTREAM_MATCHDAY=1 (and " + obs.PortableEnv + ") to run this; it takes about 30 minutes")
	}
	root := os.Getenv(obs.PortableEnv)
	if root == "" {
		t.Skip("set " + obs.PortableEnv + " to a portable OBS copy")
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		t.Fatalf("%s has no obs_portable_mode.txt — refusing", root)
	}
	if obs.Running() {
		t.Skip("an OBS is already open; close it first (this must only ever drive the portable copy)")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("needs ffmpeg")
	}
	own := ownOBSFiles(t)
	t.Cleanup(func() {
		for p, m := range ownOBSFiles(t) {
			if before, ok := own[p]; !ok || !before.Equal(m) {
				t.Errorf("this machine's own OBS settings changed: %s", p)
			}
		}
	})
	// Not t.TempDir: OBS has the camera's file open until the cleanup below has closed it.
	app, err := os.MkdirTemp("", "cricketstream-matchday")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(app) })
	t.Cleanup(func() {
		if obs.Running() && !obs.Close(30*time.Second) {
			obs.Kill()
		}
		for end := time.Now().Add(30 * time.Second); obs.Running() && time.Now().Before(end); {
			time.Sleep(500 * time.Millisecond)
		}
	})
	outDir := os.Getenv("CRICKETSTREAM_MATCHDAY_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	os.MkdirAll(outDir, 0o755)

	const password, linkKey = "smoke-test-pass", "K8Q27WMX"
	port, talkPort, linkPort, piPort, sink, proxy := freePort(t), freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	// The server's process. Its side-on recorder's folder is named after it (camera 2 is switched
	// on below); asking Windows who owns the port came back empty too often under this load, and
	// a step that's meant to kill the server can't start without knowing which it is.
	serverPID := func() int {
		ents, _ := os.ReadDir(filepath.Join(app, ".sidecam"))
		newest, at := 0, time.Time{}
		for _, e := range ents {
			if id, err := strconv.Atoi(e.Name()); err == nil && mdAlive(id) {
				if info, err := e.Info(); err == nil && info.ModTime().After(at) {
					newest, at = id, info.ModTime()
				}
			}
		}
		if newest != 0 {
			return newest
		}
		return mdPortOwner(port)
	}
	cfgText := "[App]\nrole = streaming\n\n[Club]\nname = Smoke CC\nabbreviation = SMK\nhome_colour = #224466\nplaycricket_id = 1\n\n" +
		"[OBS]\nobs_password =\nmanage_obs = yes\nreplay_folder = " + filepath.Join(app, "replays") + "\n\n" +
		"[Graphics]\nscorebar_style = modern\n\n[Stream]\nmax_overs = 50\n\n[Auth]\nclub_password = " + password + "\n\n" +
		"[Clubhouse]\nenabled = yes\nname = Smoke CC\nkey = " + linkKey + "\n"
	os.MkdirAll(filepath.Join(app, "replays"), 0o755)
	cfgPath := filepath.Join(app, "config.ini")
	os.WriteFile(cfgPath, []byte(cfgText), 0o644)

	exe := filepath.Join(app, "CricketStream-matchday.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "../../cmd/cricketstream").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := append(os.Environ(), "CRICKETSTREAM_HOME="+app, fmt.Sprintf("CRICKETSTREAM_PORT=%d", port), fmt.Sprintf("CRICKETSTREAM_TALK_PORT=%d", talkPort),
		fmt.Sprintf("CRICKETSTREAM_CLUBHOUSE_PORT=%d", linkPort), "CRICKETSTREAM_NO_PAUSE=1", "CRICKETSTREAM_NO_BROWSER=1",
		"CRICKETSTREAM_NO_WINDOW=1", "CRICKETSTREAM_APP_PORT=0")

	// ── The receiver, and the line to it ──
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	partsDir := filepath.Join(app, "received")
	os.MkdirAll(partsDir, 0o755)
	line := &mdLine{open: map[net.Conn]bool{}, sink: sink}
	line.listen(t, proxy, stop)
	go func() {
		for n := 1; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y", "-listen", "1", "-i", fmt.Sprintf("rtmp://127.0.0.1:%d/live/test", sink),
				"-c", "copy", "-f", "flv", filepath.Join(partsDir, fmt.Sprintf("part%02d.flv", n)))
			cmd.Start()
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			select {
			case <-stop:
				cmd.Process.Kill()
				<-done
				return
			case <-done:
			}
		}
	}()

	// ── The app, and match day started from its page ──
	log := &syncBuf{}
	appCmd := exec.Command(exe)
	appCmd.Dir, appCmd.Env = app, env
	appCmd.Stdout, appCmd.Stderr = log, log
	if err := appCmd.Start(); err != nil {
		t.Fatal(err)
	}
	appDone := make(chan struct{})
	go func() { appCmd.Wait(); close(appDone) }()
	t.Cleanup(func() {
		select {
		case <-appDone:
		default:
			appCmd.Process.Kill()
		}
		exec.Command("taskkill", "/F", "/IM", "CricketStream-matchday.exe").Run()
		os.WriteFile(filepath.Join(outDir, "app-output.log"), []byte(log.String()), 0o644)
	})
	waitLog := func(re string, d time.Duration) []string {
		t.Helper()
		rx := regexp.MustCompile(re)
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			if m := rx.FindStringSubmatch(log.String()); m != nil {
				return m
			}
		}
		t.Fatalf("no %q after %v\n%s", re, d, log.String())
		return nil
	}
	pageURL := waitLog(`app page (http://127\.0\.0\.1:\d+)/`, 30*time.Second)[1]
	client := &http.Client{Timeout: 15 * time.Second}
	do := func(method, url string, body any, hdr map[string]string) (int, []byte) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, url, rd)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := client.Do(req)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, b
	}
	_, page := do("GET", pageURL+"/", nil, nil)
	km := regexp.MustCompile(`name="app-key" content="([0-9a-f]{32})"`).FindSubmatch(page)
	if km == nil {
		t.Fatal("no app key on the page")
	}
	appHdr := map[string]string{"X-App-Key": string(km[1]), "Origin": pageURL}
	appStatus := func() map[string]any {
		_, b := do("GET", pageURL+"/api/status", nil, appHdr)
		var d map[string]any
		json.Unmarshal(b, &d)
		return d
	}
	var answering atomic.Bool
	answering.Store(true)
	go func() { // a question nobody's there for: noted, and answered no
		seen := map[string]bool{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
			if !answering.Load() {
				continue
			}
			if q, _ := appStatus()["question"].(map[string]any); q != nil {
				id := fmt.Sprint(q["id"])
				if !seen[id] {
					seen[id] = true
					t.Logf("QUESTION in the window: %v", q["text"])
				}
				do("POST", pageURL+"/api/answer", map[string]any{"id": q["id"], "yes": false, "text": ""}, appHdr)
			}
		}
	}()
	if st, b := do("POST", pageURL+"/api/start", map[string]any{"remote": false, "manual": false}, appHdr); st != 200 {
		t.Fatalf("start: %d %s", st, b)
	}
	waitLog(`Control panel: http://localhost:\d+/control`, 4*time.Minute)
	t.Log("match day is running")

	_, b := do("POST", base+"/login", map[string]any{"password": password}, nil)
	var login map[string]any
	json.Unmarshal(b, &login)
	token, _ := login["session_token"].(string)
	if token == "" {
		t.Fatalf("login: %s", b)
	}
	auth := map[string]string{"Authorization": "Bearer " + token}
	reauth := func() {
		_, b := do("POST", base+"/login", map[string]any{"password": password}, nil)
		var l map[string]any
		json.Unmarshal(b, &l)
		if tk, _ := l["session_token"].(string); tk != "" {
			auth["Authorization"] = "Bearer " + tk
		}
	}
	cfg, _ := ini.Load(cfgPath)
	target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: cfg.Get("OBS", "obs_password", "")}
	tryOBS := func(typ string, data any) (map[string]any, bool) {
		c, err := obsws.Dial(target, 5*time.Second)
		if err != nil {
			return nil, false
		}
		defer c.Close()
		r, err := c.Request(typ, data)
		if err != nil || !r.OK {
			return nil, false
		}
		return r.Data, true
	}
	mustOBS := func(typ string, data any) map[string]any {
		t.Helper()
		d, ok := tryOBS(typ, data)
		if !ok {
			t.Fatalf("OBS refused %s", typ)
		}
		return d
	}

	// ── Two cameras, in every scene (a looping file, since nothing here can serve RTSP) ──
	ground := filepath.Join(app, "ground.mkv")
	if out, err := exec.Command(ff, "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=30:d=30",
		"-f", "lavfi", "-i", "sine=f=400:d=30,volume=0.3", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", ground).CombinedOutput(); err != nil {
		t.Fatalf("camera file: %v\n%s", err, out)
	}
	scenes := names(mustOBS("GetSceneList", nil)["scenes"], "sceneName")
	if !has(scenes, "Main-Bowler") {
		mustOBS("CreateScene", map[string]any{"sceneName": "Main-Bowler"})
		mustOBS("CreateSceneItem", map[string]any{"sceneName": "Main-Bowler", "sourceName": "Overlay", "sceneItemEnabled": true})
	}
	for _, cam := range []string{"Cricket Camera", "Bowler End Camera"} {
		for _, sc := range []string{"Main", "Main-Bowler", "Replay"} {
			l, _ := mustOBS("GetSceneItemList", map[string]any{"sceneName": sc})["sceneItems"].([]any)
			for _, it := range l {
				if m := it.(map[string]any); m["sourceName"] == cam || m["sourceName"] == "Commentary" {
					mustOBS("RemoveSceneItem", map[string]any{"sceneName": sc, "sceneItemId": m["sceneItemId"]})
				}
			}
		}
	}
	for i := 0; i < 40; i++ {
		in := names(mustOBS("GetInputList", nil)["inputs"], "inputName")
		if !has(in, "Cricket Camera") && !has(in, "Bowler End Camera") && !has(in, "Commentary") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	for _, cam := range []struct{ name, home string }{{"Cricket Camera", "Main"}, {"Bowler End Camera", "Main-Bowler"}} {
		made := mustOBS("CreateInput", map[string]any{"sceneName": cam.home, "inputName": cam.name, "inputKind": "ffmpeg_source",
			"inputSettings": map[string]any{"local_file": ground, "is_local_file": true, "looping": true}, "sceneItemEnabled": true})
		mustOBS("SetSceneItemIndex", map[string]any{"sceneName": cam.home, "sceneItemId": made["sceneItemId"], "sceneItemIndex": 0})
		for _, sc := range []string{"Main", "Main-Bowler", "Replay"} {
			if sc != cam.home {
				it := mustOBS("CreateSceneItem", map[string]any{"sceneName": sc, "sourceName": cam.name, "sceneItemEnabled": true})
				mustOBS("SetSceneItemIndex", map[string]any{"sceneName": sc, "sceneItemId": it["sceneItemId"], "sceneItemIndex": 0})
			}
		}
	}
	mustOBS("SetStreamServiceSettings", map[string]any{"streamServiceType": "rtmp_custom",
		"streamServiceSettings": map[string]any{"server": fmt.Sprintf("rtmp://127.0.0.1:%d/live", proxy), "key": "test"}})

	// ── Everything switched on ──
	everything := map[string]any{"replay_enabled": true, "graphics_camera_auto_cut": true, "camera_walkout": true, "camera_over_hold": 8,
		"camera_walkout_hold": 12, "replay_side_on": true, "camera2_rtsp_url": "rtsp://127.0.0.1:9/nothing-there", "stream_auto_downshift": true,
		"break_reel": true, "graphics_h2h": true, "graphics_lineup": true, "graphics_win_predictor": true, "graphics_club_milestones": true,
		"graphics_season_leaders": true, "graphics_rain_radar": true, "graphics_voice_commentary": true}
	if st, b := do("POST", base+"/state", everything, auth); st != 200 {
		t.Fatalf("state: %d %s", st, b)
	}
	sim := func() *exec.Cmd {
		c := exec.Command(exe, "simulate", "--scenario", "full", "--overs", "50", "--ball-seconds", "8", "--configure", "--seed", "7")
		c.Dir, c.Env = app, env
		c.Stdout, c.Stderr = log, log
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	simCmd := sim()
	t.Cleanup(func() { simCmd.Process.Kill(); simCmd.Wait() })

	// ── The clubhouse screen, paired ──
	piDir := t.TempDir()
	pi := func() *exec.Cmd {
		c := exec.Command(exe, "clubhouse", fmt.Sprintf("--port=%d", piPort))
		c.Dir = piDir
		c.Env = append(env, "CRICKETSTREAM_HOME="+piDir)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	piCmd := pi()
	t.Cleanup(func() { piCmd.Process.Kill(); piCmd.Wait() })
	piURL := fmt.Sprintf("http://127.0.0.1:%d", piPort)
	var piHdr map[string]string
	piOpen := func() {
		for i := 0; i < 80; i++ {
			if st, b := do("GET", piURL+"/", nil, nil); st == 200 {
				if m := regexp.MustCompile(`name="?screen-key"?\s+content="([^"]+)"`).FindSubmatch(b); m != nil {
					piHdr = map[string]string{"X-Screen-Key": string(m[1]), "Origin": piURL}
					return
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatal("the screen's page never came up")
	}
	piOpen()
	if st, b := do("POST", piURL+"/api/pair", map[string]any{"key": linkKey, "address": fmt.Sprintf("127.0.0.1:%d", linkPort)}, piHdr); st != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("pairing: %d %s", st, b)
	}
	talkControl := func(body map[string]any) map[string]any {
		_, b := do("POST", piURL+"/api/server/talk/control", body, piHdr)
		var d map[string]any
		json.Unmarshal(b, &d)
		return d
	}
	talkStatus := func() map[string]any {
		_, b := do("GET", piURL+"/api/server/talk/status", nil, piHdr)
		var d map[string]any
		json.Unmarshal(b, &d)
		return d
	}
	onAir := func() map[string]any {
		_, b := do("GET", base+"/talk/onair", nil, nil)
		var d map[string]any
		json.Unmarshal(b, &d)
		return d
	}

	// ── Go live, from the panel's own button ──
	if st, b := do("POST", base+"/checklist/action", map[string]any{"action": "go_live"}, auth); st != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("go live: %d %s", st, b)
	}

	// ── Watching: the stream by the second, OBS and the overlay every few ──
	type sample struct {
		at     time.Time
		bytes  int64
		active bool
		scene  string
		skip   float64
		poll   float64
	}
	var (
		smu     sync.Mutex
		samples []sample
	)
	go func() {
		last := int64(0)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		n := 0
		var active bool
		var scene string
		var skip, poll float64
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			n++
			if n%3 == 0 {
				if d, ok := tryOBS("GetStreamStatus", nil); ok {
					active, _ = d["outputActive"].(bool)
					skip, _ = d["outputSkippedFrames"].(float64)
				} else {
					active = false
				}
				if d, ok := tryOBS("GetCurrentProgramScene", nil); ok {
					scene, _ = d["currentProgramSceneName"].(string)
				} else {
					scene = "?"
				}
				if st, b := do("GET", base+"/health", nil, nil); st == 200 {
					var h map[string]any
					json.Unmarshal(b, &h)
					ov, _ := h["overlay"].(map[string]any)
					if v, ok := ov["last_poll_sec_ago"].(float64); ok {
						poll = v
					} else {
						poll = -1
					}
				} else {
					poll = -2
				}
			}
			now := line.bytes.Load()
			smu.Lock()
			samples = append(samples, sample{time.Now(), now - last, active, scene, skip, poll})
			smu.Unlock()
			last = now
		}
	}()
	// Reading through the screen as its page does, all match: the score, the status, the picture.
	go func() {
		tick := time.NewTicker(1500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				hdr := piHdr
				do("GET", piURL+"/api/server/live/view", nil, hdr)
				do("GET", piURL+"/api/server/talk/status", nil, hdr)
				do("GET", piURL+"/api/status", nil, hdr)
				do("GET", piURL+"/api/preview.jpg", nil, hdr)
			}
		}
	}()

	phaseStart := time.Now()
	report := func(name string) {
		smu.Lock()
		var total, seconds, gap, worst int64
		var bad []string
		maxPoll, scenesSeen := 0.0, map[string]int{}
		for _, s := range samples {
			if s.at.Before(phaseStart) {
				continue
			}
			seconds++
			total += s.bytes
			if s.bytes == 0 {
				gap++
				if gap > worst {
					worst = gap
				}
			} else {
				gap = 0
			}
			if s.poll > maxPoll {
				maxPoll = s.poll
			}
			if s.poll < 0 {
				bad = append(bad, fmt.Sprint(s.poll))
			}
			scenesSeen[s.scene]++
		}
		smu.Unlock()
		kbps := int64(0)
		if seconds > 0 {
			kbps = total * 8 / seconds / 1000
		}
		t.Logf("RESULT %-26s %4ds, %5d kbps, longest silence on the line %3ds, overlay's slowest poll %3.0fs, health unanswered %ds, scenes %v, connections so far %d",
			name, seconds, kbps, worst, maxPoll, len(bad), scenesSeen, line.conns.Load())
		if st, b := do("GET", base+"/stream/monitor", nil, nil); st == 200 {
			var m map[string]any
			json.Unmarshal(b, &m)
			t.Logf("       monitor: streaming=%v current=%v baseline=%v step=%v verdict=%v", m["streaming"], m["current_kbps"], m["baseline_kbps"], m["step"], m["verdict"])
		}
		if d, ok := tryOBS("GetSourceScreenshot", map[string]any{"sourceName": "Main", "imageFormat": "png", "imageWidth": 1280}); ok {
			if s, _ := d["imageData"].(string); s != "" {
				if i := strings.Index(s, ","); i > 0 {
					if b, err := base64.StdEncoding.DecodeString(s[i+1:]); err == nil {
						safe := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(name), "-")
						os.WriteFile(filepath.Join(outDir, safe+".png"), b, 0o644)
					}
				}
			}
		}
		for _, n := range []string{"obs64"} {
			if o, err := exec.Command("powershell", "-NoProfile", "-Command", "(Get-Process "+n+" -ErrorAction SilentlyContinue | Measure-Object WorkingSet64 -Sum).Sum/1MB").Output(); err == nil {
				t.Logf("       %s memory: %s MB", n, strings.TrimSpace(string(o)))
			}
		}
		if pid := serverPID(); pid != 0 {
			if o, err := exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf("$p=Get-Process -Id %d; '{0:N0} MB, {1} handles, {2} threads' -f ($p.WorkingSet64/1MB),$p.HandleCount,$p.Threads.Count", pid)).Output(); err == nil {
				t.Logf("       server: pid %d, %s", pid, strings.TrimSpace(string(o)))
			}
		}
		phaseStart = time.Now()
	}
	until := func(what string, d time.Duration, ok func() bool) bool {
		t.Helper()
		start := time.Now()
		for end := start.Add(d); time.Now().Before(end); time.Sleep(time.Second) {
			if ok() {
				t.Logf("  %s: after %.0fs", what, time.Since(start).Seconds())
				return true
			}
		}
		t.Errorf("FAILED: %s: not after %v", what, d)
		return false
	}
	lineMoving := func() bool {
		a := line.bytes.Load()
		time.Sleep(2 * time.Second)
		return line.bytes.Load() > a
	}
	streaming := func() bool {
		d, ok := tryOBS("GetStreamStatus", nil)
		return ok && d["outputActive"] == true
	}
	scene := func() string {
		d, _ := tryOBS("GetCurrentProgramScene", nil)
		s, _ := d["currentProgramSceneName"].(string)
		return s
	}
	overlayPolling := func() bool {
		st, b := do("GET", base+"/health", nil, nil)
		if st != 200 {
			return false
		}
		var h map[string]any
		json.Unmarshal(b, &h)
		ov, _ := h["overlay"].(map[string]any)
		v, ok := ov["last_poll_sec_ago"].(float64)
		return ok && v < 8
	}

	// ── 1. A healthy start ──
	if !until("the stream reaches the receiver", 60*time.Second, lineMoving) {
		t.Log(log.String())
		t.FailNow()
	}
	until("the overlay polls", 90*time.Second, overlayPolling)
	time.Sleep(2 * time.Minute)
	report("1 healthy, 2 minutes")

	// ── 2. Commentary on, a phone on air ──
	if d := talkControl(map[string]any{"action": "on"}); d["ok"] != true {
		t.Errorf("FAILED: switching commentary on: %v", d)
	}
	until("OBS ready for commentary", 60*time.Second, func() bool {
		st := talkStatus()
		o, _ := st["obs"].(map[string]any)
		return o != nil && o["ready"] == true
	})
	st := talkStatus()
	urlKey := regexp.MustCompile(`k=(.*)$`).FindStringSubmatch(fmt.Sprint(st["url"]))
	if urlKey == nil {
		t.Fatalf("the QR code's address: %v", st)
	}
	phone := &mdPhone{base: fmt.Sprintf("https://127.0.0.1:%d", talkPort), stop: make(chan struct{}), done: make(chan struct{}),
		client: &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	j, err := phone.post("/talk/join", map[string]any{"k": urlKey[1], "name": "Smoke Tester"})
	if err != nil || j["ok"] != true {
		t.Fatalf("join: %v %v", j, err)
	}
	phone.id, phone.secret = fmt.Sprint(j["id"]), fmt.Sprint(j["secret"])
	go phone.talk()
	if d := talkControl(map[string]any{"action": "approve", "id": phone.id}); d["ok"] != true {
		t.Errorf("FAILED: approving the phone: %v", d)
	}
	until("the strap's up", 20*time.Second, func() bool { return onAir()["on_air"] == true })
	time.Sleep(90 * time.Second)
	report("2 commentary on air")

	// ── 3. The server killed in the middle of a replay ──
	// The match pauses first: another wicket's replay would cut back to Main on its way out,
	// and hide what this is looking for.
	simCmd.Process.Kill()
	simCmd.Wait()
	for i := 0; i < 40 && scene() != "Main"; i++ {
		time.Sleep(time.Second)
	}
	time.Sleep(5 * time.Second)
	if st, b := do("POST", base+"/replay", map[string]any{"reason": "Smoke"}, auth); st != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Errorf("FAILED: replay: %d %s", st, b)
	}
	if until("the replay's on air", 40*time.Second, func() bool { return scene() == "Replay" }) {
		time.Sleep(2 * time.Second)
		pid := serverPID()
		t.Logf("  killing the server (pid %d) mid-replay", pid)
		if pid == 0 {
			t.Fatal("couldn't find the server's process")
		}
		if p, err := os.FindProcess(pid); err == nil {
			if err := p.Kill(); err != nil {
				t.Errorf("kill: %v", err)
			}
		}
		until("the server's back", 40*time.Second, func() bool {
			st, _ := do("GET", base+"/health", nil, nil)
			return st == 200 && serverPID() != pid
		})
		reauth()
		until("the overlay polls again", 60*time.Second, overlayPolling)
		until("the phone's still on air", 40*time.Second, func() bool { return onAir()["on_air"] == true })
		// A replay's clip has finished long before this. Viewers must be back on the cameras.
		if !until("OBS back on the Main scene after the replay the server died in", 75*time.Second, func() bool {
			s := scene()
			return s == "Main" || s == "Main-Bowler"
		}) {
			t.Logf("  STUCK on scene %q: putting it right by hand, so the test can carry on", scene())
			tryOBS("SetCurrentProgramScene", map[string]any{"sceneName": "Main"})
		}
	}
	if !lineMoving() {
		t.Errorf("FAILED: the stream stopped when the server died")
	}
	simCmd = sim()
	report("3 server killed mid-replay")

	// ── 4. The server hung ──
	pid := serverPID()
	t.Logf("  hanging the server (pid %d)", pid)
	if pid == 0 {
		t.Fatal("couldn't find the server's process")
	}
	if err := mdSuspend(pid); err != nil {
		t.Errorf("suspend: %v", err)
	}
	until("the hung server's replaced", 3*time.Minute, func() bool {
		p := serverPID()
		return p != 0 && p != pid && func() bool { st, _ := do("GET", base+"/health", nil, nil); return st == 200 }()
	})
	reauth()
	until("the overlay polls again", 60*time.Second, overlayPolling)
	until("the phone's on air again", 40*time.Second, func() bool { return onAir()["on_air"] == true })
	// Two restarts in: the screen's own login must still be good, or nobody can be taken off air.
	until("the clubhouse screen's buttons still work", 20*time.Second, func() bool { return talkStatus()["ok"] == true })
	if d := talkControl(map[string]any{"action": "volume", "id": phone.id, "step": -1}); d["ok"] != true {
		t.Errorf("FAILED: the screen can't reach the commentary controls after a restart: %v", d)
	}
	talkControl(map[string]any{"action": "volume", "id": phone.id, "step": 1})
	if !lineMoving() {
		t.Errorf("FAILED: the stream stopped when the server hung")
	}
	report("4 server hung")

	// ── 5. OBS crashes mid-stream ──
	crash := time.Now()
	obs.Kill()
	until("OBS back and streaming", 3*time.Minute, streaming)
	until("the stream's reaching the receiver", 60*time.Second, lineMoving)
	t.Logf("  viewers' gap: about %.0fs", time.Since(crash).Seconds())
	until("the overlay polls", 90*time.Second, overlayPolling)
	if s := scene(); s != "Main" && s != "Main-Bowler" {
		t.Errorf("FAILED: scene after the crash: %q", s)
	}
	if d, ok := tryOBS("GetReplayBufferStatus", nil); !ok || d["outputActive"] != true {
		t.Errorf("FAILED: replay buffer after the crash: %v", d)
	}
	in := names(mustOBS("GetInputList", nil)["inputs"], "inputName")
	t.Logf("  inputs after the crash: %v", in)
	until("commentary heard again", 90*time.Second, func() bool {
		o, _ := talkStatus()["obs"].(map[string]any)
		return o != nil && o["ready"] == true
	})
	crashPart := int(line.conns.Load())
	time.Sleep(60 * time.Second)
	report("5 OBS crashed mid-stream")

	// ── 6. The line drops for 30 seconds, and comes back ──
	line.cut()
	time.Sleep(30 * time.Second)
	line.down.Store(false)
	cutAt := time.Now()
	until("the stream's back on the line", 3*time.Minute, lineMoving)
	t.Logf("  back %.0fs after the line was", time.Since(cutAt).Seconds())
	time.Sleep(45 * time.Second)
	if !streaming() {
		t.Errorf("FAILED: not streaming after the line came back")
	}
	report("6 line down 30s")

	// ── 7. A slow line (auto quality on), then a good one ──
	line.kbps.Store(1500)
	time.Sleep(150 * time.Second)
	report("7a slow line 1.5 Mbps")
	line.kbps.Store(0)
	time.Sleep(90 * time.Second)
	until("still streaming after the slow line", 90*time.Second, streaming)
	until("moving", 60*time.Second, lineMoving)
	report("7b line good again")

	// ── 8. The scorer's file goes bad, and the scorer's laptop goes quiet ──
	simCmd.Process.Kill()
	simCmd.Wait()
	feed := filepath.Join(app, "sim_pcs_output", "nvplay-scoreboard1.xml")
	for _, junk := range []string{"", "{\"half\":", "\x00\x00\x00", "<html>not json</html>", `{"overs":"NaN","wickets":null,"batter1":{}}`} {
		os.WriteFile(feed, []byte(junk), 0o644)
		time.Sleep(6 * time.Second)
	}
	if st, _ := do("GET", base+"/health", nil, nil); st != 200 {
		t.Errorf("FAILED: the server stopped answering over a bad scorer's file")
	}
	until("the overlay's still polling", 30*time.Second, overlayPolling)
	simCmd = sim()
	time.Sleep(45 * time.Second)
	if !lineMoving() {
		t.Errorf("FAILED: the stream stopped over a bad scorer's file")
	}
	report("8 scorer's file bad")

	// ── 9. OBS freezes ──
	frozenAt := time.Now()
	for _, p := range obs.PIDs() {
		if err := mdSuspend(p); err != nil {
			t.Errorf("suspending OBS: %v", err)
		}
	}
	t.Log("  OBS frozen")
	time.Sleep(30 * time.Second)
	until("a frozen OBS replaced, and streaming", 6*time.Minute, streaming)
	until("the stream's reaching the receiver", 60*time.Second, lineMoving)
	t.Logf("  viewers' gap: about %.0fs", time.Since(frozenAt).Seconds())
	until("the overlay polls", 90*time.Second, overlayPolling)
	report("9 OBS frozen")

	// ── 10. The clubhouse screen dies and comes back; the phone walks off ──
	piCmd.Process.Kill()
	piCmd.Wait()
	time.Sleep(10 * time.Second)
	piCmd = pi()
	piOpen()
	until("the screen's paired after its restart", 30*time.Second, func() bool { return talkStatus()["ok"] == true })
	close(phone.stop)
	<-phone.done
	t.Logf("  phone: %d chunks sent, %d failed, last told %v", phone.sent.Load(), phone.fail.Load(), phone.state.Load())
	until("the strap's down without the phone", 45*time.Second, func() bool { return onAir()["on_air"] == false })
	if d := talkControl(map[string]any{"action": "off"}); d["ok"] != true {
		t.Errorf("FAILED: switching commentary off: %v", d)
	}
	time.Sleep(30 * time.Second)
	if !lineMoving() {
		t.Errorf("FAILED: stream stopped at the end of the commentary")
	}
	report("10 screen and phone gone")

	// ── 11. A server that keeps crashing: the quick restarts run out, and it keeps trying ──
	for i := 0; i < 4; i++ {
		var p int
		for w := 0; w < 40 && p == 0; w++ {
			if p = serverPID(); p == 0 {
				time.Sleep(500 * time.Millisecond)
			}
		}
		if p == 0 {
			t.Logf("  no server to kill on round %d", i+1)
			break
		}
		if i == 3 { // the last straw, with a replay left on air and nothing to end it
			tryOBS("SetCurrentProgramScene", map[string]any{"sceneName": "Replay"})
		}
		t.Logf("  crash loop: killing pid %d", p)
		if pr, err := os.FindProcess(p); err == nil {
			pr.Kill()
		}
		time.Sleep(3 * time.Second)
	}
	if !until("the window says the server's gone", 60*time.Second, func() bool { return appStatus()["server_gone"] == true }) {
		t.Errorf("FAILED: the window didn't say the server keeps stopping")
	}
	if !until("OBS is cut back from the replay with the server down", 20*time.Second, func() bool {
		s := scene()
		return s == "Main" || s == "Main-Bowler"
	}) {
		t.Errorf("FAILED: stuck on scene %q with the server down", scene())
	}
	// A fault that can't fix itself: something else holds the port, so a try fails.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Logf("  couldn't hold the server's port: %v", err)
	} else {
		failed := func() bool { return strings.Contains(log.String(), "(every minute, 1 so far)") }
		if !until("a try with the port taken", 90*time.Second, failed) {
			t.Errorf("FAILED: no try a minute after the restarts ran out")
		}
		time.Sleep(15 * time.Second) // it fails to bind and exits
		if appStatus()["server_gone"] != true {
			t.Errorf("FAILED: the window stopped saying the server's gone while it couldn't start")
		}
		ln.Close()
	}
	if !until("a later try brings the server back", 100*time.Second, func() bool { st, _ := do("GET", base+"/health", nil, nil); return st == 200 }) {
		t.Errorf("FAILED: the server never came back after the crash loop")
	}
	if !until("the window notices it's back", 60*time.Second, func() bool { return appStatus()["server_gone"] == false }) {
		t.Errorf("FAILED: the window still says the server's gone")
	}
	if !until("the overlay polls again", 90*time.Second, overlayPolling) {
		t.Errorf("FAILED: the overlay didn't come back after the crash loop")
	}
	select {
	case <-appDone:
		t.Errorf("FAILED: the app ended during a crash loop")
	default:
	}
	if n := strings.Count(log.String(), "The server keeps crashing"); n != 1 {
		t.Errorf("FAILED: said it keeps crashing %d times; that's a Discord post each", n)
	}
	if !lineMoving() || !streaming() {
		t.Errorf("FAILED: the stream stopped over a server crash loop")
	}
	report("11 server crash loop")

	// reopen is the operator opening the app again and pressing Start match day, live.
	var runningCmd, runningDone = appCmd, appDone
	reopen := func(when string) {
		t.Helper()
		select {
		case <-runningDone:
		case <-time.After(30 * time.Second):
			runningCmd.Process.Kill()
			<-runningDone
		}
		lg := &syncBuf{}
		c := exec.Command(exe)
		c.Dir, c.Env = app, env
		c.Stdout, c.Stderr = lg, lg
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { c.Wait(); close(done) }()
		runningCmd, runningDone = c, done
		safe := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(when, "-")
		t.Cleanup(func() {
			select {
			case <-done:
			default:
				c.Process.Kill()
			}
			os.WriteFile(filepath.Join(outDir, "app-"+safe+".log"), []byte(lg.String()), 0o644)
		})
		var page string
		if !until("the app opens "+when, 30*time.Second, func() bool {
			if m := regexp.MustCompile(`app page (http://127\.0\.0\.1:\d+)/`).FindStringSubmatch(lg.String()); m != nil {
				page = m[1]
				return true
			}
			return false
		}) {
			return
		}
		_, pg := do("GET", page+"/", nil, nil)
		km := regexp.MustCompile(`name="app-key" content="([0-9a-f]{32})"`).FindSubmatch(pg)
		if km == nil {
			t.Fatal("no app key on the reopened page")
		}
		pageURL, appHdr = page, map[string]string{"X-App-Key": string(km[1]), "Origin": page}
		conns := line.conns.Load()
		if st, b := do("POST", pageURL+"/api/start", map[string]any{"remote": false, "manual": false}, appHdr); st != 200 {
			t.Errorf("FAILED: starting match day %s: %d %s", when, st, b)
		}
		until("the server's back", 3*time.Minute, func() bool { st, _ := do("GET", base+"/health", nil, nil); return st == 200 })
		reauth()
		until("the overlay polls", 90*time.Second, overlayPolling)
		until("the screen works", 60*time.Second, func() bool { return talkStatus()["ok"] == true })
		time.Sleep(30 * time.Second)
		if line.conns.Load() != conns {
			t.Errorf("FAILED: starting match day %s reconnected the stream (%d connections, was %d)", when, line.conns.Load(), conns)
		}
		if !streaming() || !lineMoving() {
			t.Errorf("FAILED: starting match day %s stopped the stream", when)
		}
		if s := scene(); s == "Replay" {
			t.Errorf("FAILED: on the Replay scene after starting match day %s", when)
		}
	}

	// ── 12. Open the app again after the crash loop, live ──
	reopen("after the crash loop")
	report("12 app reopened, live")

	// ── 13. The app itself dies while live, with its server running ──
	if serverPID() == 0 {
		t.Errorf("FAILED: no server running before the app's killed")
	}
	runningCmd.Process.Kill()
	<-runningDone
	t.Log("  the app was killed")
	until("its server stops with it", 30*time.Second, func() bool { st, _ := do("GET", base+"/health", nil, nil); return st != 200 })
	time.Sleep(10 * time.Second)
	if !streaming() || !lineMoving() {
		t.Errorf("FAILED: the stream stopped when the app died")
	}
	reopen("after the app died")
	report("13 app died, opened again")

	// ── What reached the receiver ──
	parts, _ := filepath.Glob(filepath.Join(partsDir, "part*.flv"))
	for i, p := range parts {
		info, _ := os.Stat(p)
		o, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "csv=p=0", p).Output()
		vol, _ := exec.Command(ff, "-hide_banner", "-i", p, "-t", "120", "-af", "bandpass=f=1000:w=120,volumedetect", "-vn", "-f", "null", "-").CombinedOutput()
		m := regexp.MustCompile(`mean_volume: (-?[\d.]+) dB`).FindSubmatch(vol)
		mean := "?"
		if m != nil {
			mean = string(m[1])
		}
		t.Logf("  received %s: %d MB, %s, voice band %s dB", filepath.Base(p), info.Size()>>20, strings.Join(strings.Fields(string(o)), " "), mean)
		if i+1 == crashPart && m != nil {
			if v, _ := strconv.ParseFloat(mean, 64); v < -60 {
				t.Errorf("FAILED: no commentary on the stream after OBS's crash (%s dB)", mean)
			}
		}
	}

	// ── Finish, as the operator would ──
	do("POST", base+"/checklist/action", map[string]any{"action": "stop_stream"}, auth)
	answering.Store(false)
	do("POST", pageURL+"/api/stop", map[string]any{}, appHdr)
	stopped := until("match day's stop asks about the report, or ends", 60*time.Second, func() bool {
		s := appStatus()
		if q, _ := s["question"].(map[string]any); q != nil {
			do("POST", pageURL+"/api/answer", map[string]any{"id": q["id"], "yes": false, "text": ""}, appHdr)
		}
		return s["phase"] == "stopped"
	})
	if stopped {
		time.Sleep(5 * time.Second)
		if serverPID() != 0 {
			t.Errorf("FAILED: the server's still listening after match day ended")
		}
		if mdPortOwner(talkPort) != 0 {
			t.Errorf("FAILED: the phone's port is still open after match day ended")
		}
		if _, err := os.Stat(filepath.Join(app, "talk_session.json")); err == nil {
			t.Errorf("FAILED: talk_session.json left behind by a clean stop")
		}
	}

	// ── Anything the app said was wrong ──
	for _, l := range strings.Split(log.String(), "\n") {
		if strings.Contains(l, "✗") || strings.Contains(l, "panic") || strings.Contains(l, "tick failed") || strings.Contains(l, "goroutine ") {
			t.Logf("  APP SAID: %s", strings.TrimSpace(l))
		}
	}
}
