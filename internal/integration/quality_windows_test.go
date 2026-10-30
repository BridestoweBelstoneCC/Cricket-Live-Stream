package integration

// Stream quality against a real OBS on a throttled line. Portable OBS streams to a local
// ffmpeg RTMP receiver through a proxy here that caps how fast it passes the stream on, so
// OBS sees what a club's weak upload looks like: its send buffer backs up, congestion
// rises, frames drop, Dynamic Bitrate backs off.
//
// Opt-in and slow (CRICKETSTREAM_QUALITY_TEST=1, ~20 min):
//   - Calibration: what OBS really sends and reports at a bitrate, for a still and a
//     moving picture, a full and a throttled line, Dynamic Bitrate on and off, with the
//     hardware encoder and x264. The stress test's model of OBS
//     (internal/server/streamquality_stress_test.go) rests on these numbers.
//   - Ladder: the built server's quality step, with the replay buffer running as on
//     match day, measured by what OBS actually sends afterwards. (Before the buffer was
//     stopped for a step, OBS kept sending its old bitrate: found here.)
//   - Soak (also CRICKETSTREAM_QUALITY_SOAK=1, ~45 min more): auto quality through a bad
//     patch and out of it, at the real timings.
//   - Clip: what each encoder sends for a looped video at two bitrates: a grainy test
//     card, or CRICKETSTREAM_QUALITY_CLIP=a video file (a minute of a club's own camera).
//
//	set CRICKETSTREAM_OBS_PORTABLE=C:\obs-portable
//	set CRICKETSTREAM_QUALITY_TEST=1
//	go test ./internal/integration/ -run Quality -v -count=1 -timeout 120m

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

// throttle passes OBS's stream on to the receiver at no more than kbps (0 = as fast as
// it comes). Reading slowly from a small socket buffer is what makes OBS's own buffer
// back up, as it does in front of a slow upload.
type throttle struct{ kbps atomic.Int64 }

func startThrottle(t *testing.T, listen, sink int, stop <-chan struct{}) *throttle {
	th := &throttle{}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
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
			go th.relay(c.(*net.TCPConn), sink)
		}
	}()
	return th
}

func (th *throttle) relay(in *net.TCPConn, sink int) {
	defer in.Close()
	in.SetReadBuffer(32 << 10)
	var out net.Conn
	for i := 0; i < 30; i++ { // the receiver restarts between streams
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sink)); err == nil {
			out = c
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out == nil {
		return
	}
	defer out.Close()
	go io.Copy(in, out) // the receiver's side of the handshake, unthrottled
	buf := make([]byte, 4096)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return
			}
			if k := th.kbps.Load(); k > 0 {
				time.Sleep(time.Duration(float64(n*8) / float64(k*1000) * float64(time.Second)))
			}
		}
		if err != nil {
			return
		}
	}
}

// receive runs an ffmpeg RTMP receiver on port, again after each stream ends.
func receive(ffmpeg string, port int, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-listen", "1",
			"-i", fmt.Sprintf("rtmp://127.0.0.1:%d/live/test", port), "-c", "copy", "-f", "null", "-")
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
}

// motionPage: a textured green field panning slowly, players running, a ball in the air,
// a scorebar. Meant to look like cricket, it turned out to be a picture H.264 can't fit in
// 4 Mbps at all: at its coarsest quantizer (QP 51) it still needs 11 Mbps from NVENC and
// 13 from x264. Past that floor NVENC sends what it must (10-15 Mbps whatever it's set
// to) and x264 holds the rate by throwing the picture away. Camera footage, game footage
// and heavy grain all held their bitrate on NVENC (QClip; measured 2026-10-30), so the
// ladder isn't judged on this picture: it's kept to show the server's over-sending warning.
const motionPage = `<!doctype html><html><body style="margin:0;background:#000">
<canvas id="c" width="1920" height="1080"></canvas>
<script>
const c = document.getElementById('c'), x = c.getContext('2d');
const grass = document.createElement('canvas'); grass.width = 2400; grass.height = 1080;
const g = grass.getContext('2d');
for (let i = 0; i < 24; i++) { g.fillStyle = i % 2 ? '#3f7d3a' : '#46883f'; g.fillRect(i * 100, 0, 100, 1080); }
for (let i = 0; i < 60000; i++) { g.fillStyle = 'rgba(20,60,20,' + Math.random() * 0.4 + ')'; g.fillRect(Math.random() * 2400, Math.random() * 1080, 2, 3); }
g.fillStyle = '#c9b27c'; g.fillRect(1100, 380, 200, 320);
const players = Array.from({length: 11}, () => ({x: Math.random() * 1920, y: Math.random() * 1080, dx: 0, dy: 0}));
let f = 0;
(function draw() {
  x.drawImage(grass, (f * 0.6) % 480, 0, 1920, 1080, 0, 0, 1920, 1080);
  for (const p of players) {
    if (f % 90 === 0) { p.dx = (Math.random() - 0.5) * 6; p.dy = (Math.random() - 0.5) * 6; }
    p.x = (p.x + p.dx + 1920) % 1920; p.y = (p.y + p.dy + 1080) % 1080;
    x.fillStyle = '#f4f1e8'; x.fillRect(p.x, p.y, 18, 46); x.fillStyle = '#e0b090'; x.fillRect(p.x + 4, p.y - 12, 10, 12);
  }
  x.fillStyle = '#b01010'; x.beginPath(); x.arc((f * 9) % 1920, 540 - 300 * Math.abs(Math.sin(f / 40)), 7, 0, 7); x.fill();
  x.fillStyle = 'rgba(0,0,0,.6)'; x.fillRect(60, 960, 900, 70); x.fillStyle = '#fff'; x.font = '40px sans-serif';
  x.fillText('HOME CC 143/4  (23.' + (Math.floor(f / 150) % 6) + ' ov)', 80, 1010);
  f++; requestAnimationFrame(draw);
})();
</script></body></html>`

type qualityRig struct {
	t         *testing.T
	c         *obsws.Conn
	th        *throttle
	target    obsws.Target
	proxyPort string
}

func (r *qualityRig) req(typ string, d any) map[string]any {
	r.t.Helper()
	resp, err := r.c.Request(typ, d)
	if err != nil {
		r.t.Fatalf("%s: %v", typ, err)
	}
	if !resp.OK {
		r.t.Logf("%s refused: %s", typ, resp.Comment)
		return nil
	}
	return resp.Data
}

func (r *qualityRig) stopStream() {
	if st := r.req("GetStreamStatus", nil); st != nil && st["outputActive"] == true {
		r.req("StopStream", nil)
	}
	t0 := time.Now()
	for i := 0; i < 180; i++ {
		if st := r.req("GetStreamStatus", nil); st != nil && st["outputActive"] != true && st["outputReconnecting"] != true {
			if d := time.Since(t0); d > 3*time.Second {
				r.t.Logf("STOP took %.0fs", d.Seconds())
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	r.t.Fatal("stream wouldn't stop in 90s")
}

func (r *qualityRig) startStream() {
	for i := 0; i < 10; i++ {
		if r.req("StartStream", nil) != nil {
			return
		}
		time.Sleep(time.Second)
	}
	r.t.Fatal("stream wouldn't start")
}

func (r *qualityRig) replay(on bool) {
	typ := "StopReplayBuffer"
	if on {
		typ = "StartReplayBuffer"
	}
	if st := r.req("GetReplayBufferStatus", nil); st != nil && (st["outputActive"] == true) != on {
		r.req(typ, nil)
	}
	for i := 0; i < 40; i++ {
		if st := r.req("GetReplayBufferStatus", nil); st != nil && (st["outputActive"] == true) == on {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	r.t.Fatalf("replay buffer didn't go %v", on)
}

func num(m map[string]any, k string) float64 {
	f, _ := m[k].(float64)
	return f
}

// sentKbps is what OBS sends over the next secs seconds.
func (r *qualityRig) sentKbps(secs int) float64 {
	a := r.req("GetStreamStatus", nil)
	t0 := time.Now()
	time.Sleep(time.Duration(secs) * time.Second)
	b := r.req("GetStreamStatus", nil)
	if a == nil || b == nil {
		return 0
	}
	return (num(b, "outputBytes") - num(a, "outputBytes")) * 8 / 1000 / time.Since(t0).Seconds()
}

// measure streams scene at kbps over a line of limit kbps (0 = unlimited) for ~45s and
// reports what OBS sent and said.
func (r *qualityRig) measure(enc, scene string, kbps, limit int, dynamic bool) {
	r.stopStream()
	r.req("SetProfileParameter", map[string]any{"parameterCategory": "SimpleOutput", "parameterName": "VBitrate", "parameterValue": fmt.Sprint(kbps)})
	r.req("SetProfileParameter", map[string]any{"parameterCategory": "Output", "parameterName": "DynamicBitrate", "parameterValue": fmt.Sprint(dynamic)})
	r.req("SetCurrentProgramScene", map[string]any{"sceneName": scene})
	r.th.kbps.Store(int64(limit))
	r.startStream()
	time.Sleep(12 * time.Second) // past the start-up burst
	var first, last map[string]any
	var t0, t1 time.Time
	sum, n, congMax := 0.0, 0, 0.0
	for i := 0; i < 8; i++ {
		st := r.req("GetStreamStatus", nil)
		if st == nil {
			continue
		}
		if first == nil {
			first, t0 = st, time.Now()
		}
		last, t1 = st, time.Now()
		cg := num(st, "outputCongestion")
		sum, n, congMax = sum+cg, n+1, math.Max(congMax, cg)
		time.Sleep(5 * time.Second)
	}
	r.stopStream()
	if n < 2 {
		r.t.Fatal("no stream status")
	}
	sent := (num(last, "outputBytes") - num(first, "outputBytes")) * 8 / 1000 / t1.Sub(t0).Seconds()
	drops := 0.0
	if fr := num(last, "outputTotalFrames") - num(first, "outputTotalFrames"); fr > 0 {
		drops = 100 * (num(last, "outputSkippedFrames") - num(first, "outputSkippedFrames")) / fr
	}
	line := "full"
	if limit > 0 {
		line = fmt.Sprint(limit)
	}
	r.t.Logf("CAL %-6s %-7s line %-5s DBR %-5v set %4d → sent %5.0f kbps (%3.0f%%)  congestion avg %.2f max %.2f  drops %4.1f%%",
		enc, scene, line, dynamic, kbps, sent, 100*sent/float64(kbps), sum/float64(n), congMax, drops)
}

func (r *qualityRig) calibrate(enc string) {
	// Does a bitrate set over the WebSocket reach the encoder while the replay buffer
	// (which shares it in Simple mode, and runs all match day) is running?
	r.replay(true)
	r.measure(enc+"+RB", "QStill", 2000, 0, false)
	r.replay(false)
	for _, scene := range []string{"QStill", "QMotion"} {
		for _, kbps := range []int{4000, 2000} {
			r.measure(enc, scene, kbps, 0, false)
		}
	}
	for _, dyn := range []bool{false, true} {
		r.measure(enc, "QMotion", 4000, 2400, dyn)
	}
	r.replay(true)
}

func TestStreamQualityOnAThrottledLine(t *testing.T) {
	if os.Getenv("CRICKETSTREAM_QUALITY_TEST") != "1" {
		t.Skip("set CRICKETSTREAM_QUALITY_TEST=1 (and " + obs.PortableEnv + ") to run this; it takes 20+ minutes")
	}
	root := os.Getenv(obs.PortableEnv)
	if root == "" {
		t.Skip("set " + obs.PortableEnv + " to a portable OBS copy to run this")
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		t.Fatalf("%s has no obs_portable_mode.txt — refusing", root)
	}
	if obs.Running() {
		t.Skip("an OBS is already open; close it first (this must only ever drive the portable copy)")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("needs ffmpeg on PATH for the RTMP receiver")
	}
	own := ownOBSFiles(t)
	t.Cleanup(func() {
		for p, m := range ownOBSFiles(t) {
			if before, ok := own[p]; !ok || !before.Equal(m) {
				t.Errorf("this machine's own OBS settings changed: %s", p)
			}
		}
	})

	app := t.TempDir()
	t.Setenv("CRICKETSTREAM_HOME", app)
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
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
	page := filepath.Join(app, "motion.html")
	os.WriteFile(page, []byte(motionPage), 0o644)
	// A real club's camera footage, looped, says what an encoder does with that picture.
	// Without one, a grainy moving test card: at 720p it wants ~15 Mbps to look right, so
	// the encoder's rate control has to hold it down, as with a detailed camera picture.
	clip := os.Getenv("CRICKETSTREAM_QUALITY_CLIP")
	if clip != "" {
		if abs, err := filepath.Abs(clip); err == nil {
			clip = abs
		}
		if _, err := os.Stat(clip); err != nil {
			t.Fatalf("CRICKETSTREAM_QUALITY_CLIP: %v", err)
		}
	} else {
		clip = filepath.Join(app, "grain.mp4")
		if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
			"-i", "testsrc2=s=1920x1080:r=30,noise=alls=40:allf=t+u", "-t", "10",
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p", clip).CombinedOutput(); err != nil {
			t.Fatalf("making the test clip: %v\n%s", err, out)
		}
	}

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	sink, proxy := freePort(t), freePort(t)
	go receive(ffmpeg, sink, stop)
	th := startThrottle(t, proxy, sink, stop)

	closeOBS := func() {
		if !obs.Running() {
			return
		}
		if !obs.Close(30 * time.Second) {
			obs.Kill()
		}
		for end := time.Now().Add(30 * time.Second); obs.Running() && time.Now().Before(end); {
			time.Sleep(500 * time.Millisecond)
		}
	}
	t.Cleanup(closeOBS) // whatever happens (only the portable copy is visible: checked above)
	open := func() *qualityRig {
		if !obs.Prepare(cfgPath, nil, func(s string) { t.Log(s) }) {
			t.Fatal("OBS prep/launch failed")
		}
		cfg, _ := ini.Load(cfgPath)
		target := obsws.Target{Host: "localhost", Port: obs.WSPort, Password: cfg.Get("OBS", "obs_password", "")}
		if ok, msgs := obs.SetupFromConfig(cfgPath, statePath, true, true); !ok {
			t.Fatal("OBS setup failed:\n" + strings.Join(msgs, "\n"))
		}
		c, err := obsws.Dial(target, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		r := &qualityRig{t: t, c: c, th: th, target: target, proxyPort: fmt.Sprint(proxy)}
		scenes := names(r.req("GetSceneList", nil)["scenes"], "sceneName")
		pictures := map[string]map[string]any{
			"QStill": {"inputName": "QStillPic", "inputKind": "color_source_v3",
				"inputSettings": map[string]any{"color": 4283215696, "width": 1920, "height": 1080}},
			"QMotion": {"inputName": "QMotionPic", "inputKind": "browser_source",
				"inputSettings": map[string]any{"is_local_file": true, "local_file": page, "width": 1920, "height": 1080, "fps": 30}},
			"QClip": {"inputName": "QClipPic", "inputKind": "ffmpeg_source",
				"inputSettings": map[string]any{"is_local_file": true, "local_file": clip, "looping": true}},
		}
		for name, input := range pictures {
			if !has(scenes, name) {
				r.req("CreateScene", map[string]any{"sceneName": name})
				input["sceneName"] = name
				r.req("CreateInput", input)
			}
		}
		r.req("SetStreamServiceSettings", map[string]any{"streamServiceType": "rtmp_custom",
			"streamServiceSettings": map[string]any{"server": fmt.Sprintf("rtmp://127.0.0.1:%d/live", proxy), "key": "test"}})
		return r
	}
	encoder := func(r *qualityRig) string {
		e, _ := r.req("GetProfileParameter", map[string]any{"parameterCategory": "SimpleOutput", "parameterName": "StreamEncoder"})["parameterValue"].(string)
		t.Logf("encoder: %s", e)
		return e
	}

	// CRICKETSTREAM_QUALITY_PARTS picks parts (cal, clip, probe, ladder, x264); default all
	// but probe.
	parts := os.Getenv("CRICKETSTREAM_QUALITY_PARTS")
	part := func(name string) bool {
		if parts == "" {
			return name != "probe"
		}
		return strings.Contains(","+parts+",", ","+name+",")
	}
	clipCheck := func(r *qualityRig, enc string) {
		if part("clip") {
			r.replay(false)
			for _, kbps := range []int{4000, 2000} {
				r.measure(enc, "QClip", kbps, 0, false)
			}
			r.replay(true)
		}
	}

	// ── the encoder match-day prep picks (hardware where there is one) ──
	r := open()
	enc := encoder(r)
	if part("cal") {
		r.calibrate(enc)
	}
	clipCheck(r, enc)
	if part("probe") {
		probe(t, r, ffmpeg, app, enc)
	}
	if part("ladder") || os.Getenv("CRICKETSTREAM_QUALITY_SOAK") == "1" {
		base, stopServer := startServer(t, app)
		if part("ladder") {
			ladderCheck(t, r, base, "QStill")
			ladderCheck(t, r, base, "QClip")
			ladderCheck(t, r, base, "QMotion")
		}
		if os.Getenv("CRICKETSTREAM_QUALITY_SOAK") == "1" {
			soak(t, r, base)
		}
		stopServer() // before x264: a server left running stepped its stream down mid-measurement
	}
	r.stopStream()
	r.c.Close()
	closeOBS()

	// ── x264, as on a laptop without a hardware encoder ──
	if enc != "x264" && part("x264") {
		cfg, _ := ini.Load(cfgPath)
		cfg.Set("Stream", "encoder", "manual")
		cfg.Save(cfgPath)
		if p := obs.ActiveProfileINI(obs.ConfigDir()); p != "" {
			obs.SetINIValues(p, []obs.Update{{Section: "SimpleOutput", Values: [][2]string{{"StreamEncoder", "x264"}}}})
		}
		r = open()
		x := encoder(r)
		if part("cal") || !part("clip") {
			r.calibrate(x)
		}
		clipCheck(r, x)
		r.c.Close()
		closeOBS()
	}
}

// startServer runs the built app's server against the rig's OBS. stop is safe to call twice.
func startServer(t *testing.T, app string) (base string, stop func()) {
	exe := filepath.Join(app, "CricketStream.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "../../cmd/cricketstream").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	port := freePort(t)
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	srv := exec.Command(exe, "server")
	srv.Dir = app
	srv.Env = append(os.Environ(), fmt.Sprintf("CRICKETSTREAM_PORT=%d", port))
	var srvLog bytes.Buffer
	srv.Stdout, srv.Stderr = &srvLog, &srvLog
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			srv.Process.Kill()
			srv.Wait()
			for _, l := range strings.Split(srvLog.String(), "\n") {
				if strings.Contains(l, "📶") || strings.Contains(l, "eplay") || strings.Contains(l, "⚠") {
					t.Log("SERVER " + strings.TrimSpace(l))
				}
			}
		})
	}
	t.Cleanup(stop)
	for i := 0; i < 60; i++ {
		if resp, err := http.Get(base + "/health"); err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return base, stop
}

func postJSON(t *testing.T, url string, body any) map[string]any {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func monitor(base string) map[string]any {
	resp, err := http.Get(base + "/stream/monitor")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

// probe settles whether OBS's outputBytes is what really leaves it: OBS streams straight
// to an ffmpeg that records to a file, and ffprobe says what arrived. (QMotion showed
// outputBytes at 3.6x the encoder's CBR setting; the over-sending warning reads
// outputBytes, so it must be right. It is: see motionPage for why NVENC sends that much.)
func probe(t *testing.T, r *qualityRig, ffmpeg, app, enc string) {
	ffprobe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe.exe")
	if _, err := os.Stat(ffprobe); err != nil {
		if p, err := exec.LookPath("ffprobe"); err == nil {
			ffprobe = p
		}
	}
	r.replay(false)
	defer r.replay(true)
	for _, scene := range []string{"QStill", "QMotion"} {
		port := freePort(t)
		file := filepath.Join(app, "probe-"+scene+".flv")
		rec := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-listen", "1",
			"-i", fmt.Sprintf("rtmp://127.0.0.1:%d/live/test", port), "-c", "copy", "-f", "flv", "-y", file)
		rec.Start()
		time.Sleep(time.Second)
		r.stopStream()
		r.req("SetProfileParameter", map[string]any{"parameterCategory": "SimpleOutput", "parameterName": "VBitrate", "parameterValue": "4000"})
		r.req("SetProfileParameter", map[string]any{"parameterCategory": "Output", "parameterName": "DynamicBitrate", "parameterValue": "false"})
		r.req("SetCurrentProgramScene", map[string]any{"sceneName": scene})
		r.req("SetStreamServiceSettings", map[string]any{"streamServiceType": "rtmp_custom",
			"streamServiceSettings": map[string]any{"server": fmt.Sprintf("rtmp://127.0.0.1:%d/live", port), "key": "test"}})
		r.startStream()
		time.Sleep(8 * time.Second)
		obsKbps := r.sentKbps(30)
		r.stopStream()
		done := make(chan struct{})
		go func() { rec.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			rec.Process.Kill()
			<-done
		}
		out, _ := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-count_packets",
			"-show_entries", "stream=width,height,nb_read_packets:format=duration,size,bit_rate", "-of", "compact", file).CombinedOutput()
		// The file holds the whole stream: its size over its length (30 fps) is what arrived.
		var size int64
		if fi, err := os.Stat(file); err == nil {
			size = fi.Size()
		}
		packets := 0.0
		fmt.Sscanf(strings.TrimSpace(string(out))[strings.LastIndex(string(out), "nb_read_packets=")+len("nb_read_packets="):], "%g", &packets)
		arrived := 0.0
		if packets > 0 {
			arrived = float64(size) * 8 / 1000 / (packets / 30)
		}
		t.Logf("PROBE %s %s: OBS outputBytes says %.0f kbps; what arrived: %d bytes over %.0fs = %.0f kbps (%s)",
			enc, scene, obsKbps, size, packets/30, arrived, strings.TrimSpace(string(out)))
	}
	r.req("SetStreamServiceSettings", map[string]any{"streamServiceType": "rtmp_custom",
		"streamServiceSettings": map[string]any{"server": fmt.Sprintf("rtmp://127.0.0.1:%s/live", r.proxyPort), "key": "test"}})
}

// goLive streams scene at 4000 kbps over a full line, with the replay buffer running (as
// on match day), and waits for the server to have seen it.
func goLive(t *testing.T, r *qualityRig, base string, dynamic bool, scene string) {
	r.stopStream()
	r.req("SetProfileParameter", map[string]any{"parameterCategory": "SimpleOutput", "parameterName": "VBitrate", "parameterValue": "4000"})
	r.req("SetProfileParameter", map[string]any{"parameterCategory": "Output", "parameterName": "DynamicBitrate", "parameterValue": fmt.Sprint(dynamic)})
	r.req("SetCurrentProgramScene", map[string]any{"sceneName": scene})
	r.replay(false) // so the stream builds the encoder at 4000, whatever ran before
	r.th.kbps.Store(0)
	r.startStream()
	r.replay(true)
	for i := 0; i < 20; i++ {
		if m := monitor(base); m != nil && m["streaming"] == true && num(m, "baseline_kbps") > 0 {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatal("the server never saw the stream live")
}

// ladderCheck: does a quality step change what OBS really sends, with the replay buffer
// running? (It didn't, before the buffer was stopped for the step.)
func ladderCheck(t *testing.T, r *qualityRig, base, scene string) {
	goLive(t, r, base, false, scene)
	t.Logf("LADDER %s before: sending %.0f kbps", scene, r.sentKbps(15))
	res := postJSON(t, base+"/stream/quality", map[string]any{"action": "down"})
	t.Logf("LADDER %s down: %v %v", scene, res["message"], res["error"])
	time.Sleep(5 * time.Second)
	got := r.sentKbps(20)
	t.Logf("LADDER %s after a step down to 2800: sending %.0f kbps", scene, got)
	if scene != "QMotion" && (got > 2800*1.25 || got < 2800*0.6) {
		t.Errorf("LADDER: a step down to 2800 kbps left OBS sending %.0f kbps", got)
	}
	if rb := r.req("GetReplayBufferStatus", nil); rb == nil || rb["outputActive"] != true {
		t.Error("LADDER: the replay buffer wasn't running again after the step")
	}
	if m := monitor(base); m != nil && m["bitrate_warning"] != "" && m["bitrate_warning"] != nil {
		t.Logf("LADDER %s: the server warned: %v", scene, m["bitrate_warning"])
	}
	res = postJSON(t, base+"/stream/quality", map[string]any{"action": "restore"})
	t.Logf("LADDER %s restore: %v %v", scene, res["message"], res["error"])
	time.Sleep(5 * time.Second)
	got = r.sentKbps(20)
	t.Logf("LADDER %s after restoring 4000: sending %.0f kbps", scene, got)
	if scene != "QMotion" && got < 4000*0.75 {
		t.Errorf("LADDER: restoring 4000 kbps left OBS sending %.0f kbps", got)
	}
}

// soak runs the server's auto quality through a bad patch and out of it, at the real
// timings, with Dynamic Bitrate off so the ladder has to do the work (with it on, OBS
// rides out a slow line by itself: measured).
func soak(t *testing.T, r *qualityRig, base string) {
	postJSON(t, base+"/state", map[string]any{"stream_auto_downshift": true})
	if m := monitor(base); m == nil || m["auto"] != true {
		t.Fatalf("auto quality didn't switch on: %v", m)
	}
	goLive(t, r, base, false, "QStill") // a picture that fills its bitrate, as CBR does
	defer r.stopStream()
	start := time.Now()
	line := func(el time.Duration) int64 {
		if el >= 3*time.Minute && el < 9*time.Minute {
			return 1500
		}
		return 0
	}
	maxStep, lastStep := 0, -1
	for el := time.Duration(0); el < 55*time.Minute; el = time.Since(start) {
		r.th.kbps.Store(line(el))
		if m := monitor(base); m != nil {
			step := int(num(m, "step"))
			maxStep = max(maxStep, step)
			if step != lastStep || int(el.Seconds())%120 < 15 {
				t.Logf("SOAK %5.1f min line %-5d step %d %v kbps (sending %.0f)  %v  %v", el.Minutes(), line(el), step,
					m["current_kbps"], r.sentKbps(5), m["verdict"], m["upshift"])
			}
			if step == 0 && maxStep > 0 && el > 12*time.Minute {
				t.Logf("SOAK back at full quality after %.1f min", el.Minutes())
				return
			}
			lastStep = step
		}
		time.Sleep(15 * time.Second)
	}
	if maxStep == 0 {
		t.Error("SOAK: never stepped down for a 1500 kbps line")
	} else {
		t.Error("SOAK: never got back to full quality")
	}
}
