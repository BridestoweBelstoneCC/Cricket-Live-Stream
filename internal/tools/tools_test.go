package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/obsws"
	"cricketstream/internal/obsws/obswstest"
)

// capture runs f with stdout and stderr redirected, returning what was printed.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	f()
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-done
}

// sandbox points the app folder, OBS and the server at throwaway fakes.
func sandbox(t *testing.T, obs *obswstest.Server, srv http.Handler) {
	t.Helper()
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
	dir := t.TempDir()
	oldDir := assets.Dir()
	assets.SetDir(dir)
	t.Cleanup(func() { assets.SetDir(oldDir) })
	oldTarget, oldURL := obsTarget, ServerURL
	t.Cleanup(func() { obsTarget, ServerURL = oldTarget, oldURL })
	if obs != nil {
		obsTarget = obs.Target
		t.Cleanup(obs.Close)
	} else {
		obsTarget = func() obsws.Target { return obsws.Target{Host: "127.0.0.1", Port: closedPort(t)} }
	}
	if srv != nil {
		s := httptest.NewServer(srv)
		t.Cleanup(s.Close)
		ServerURL = s.URL
	} else {
		ServerURL = "http://127.0.0.1:" + itoa(closedPort(t))
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func jsonHandler(routes map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(v.(string)))
	}
}

// ── telemetry ──

func TestSummaryMatchesPython(t *testing.T) {
	// The expected text is upstream stream_telemetry.summarise() run on the same CSV,
	// including its quirk of pairing a ladder change with rows[i] (13:55:40 has no step).
	want, err := os.ReadFile("testdata/telemetry_sample.summary.txt")
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := Summarise(&got, "testdata/telemetry_sample.csv"); err != nil {
		t.Fatal(err)
	}
	w := strings.ReplaceAll(string(want), "\r\n", "\n")
	if got.String() != w {
		t.Errorf("summary differs from Python's.\n--- got ---\n%s\n--- want ---\n%s", got.String(), w)
	}
}

func TestSummaryOfEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.csv")
	os.WriteFile(p, []byte(strings.Join(Fields, ",")+"\r\n"), 0o644)
	var got bytes.Buffer
	Summarise(&got, p)
	if got.String() != "no rows\n" {
		t.Errorf("got %q", got.String())
	}
}

func telemetryServer(t *testing.T, hits *[]string, mu *sync.Mutex) http.Handler {
	routes := jsonHandler(map[string]any{
		"/stream/monitor": `{"ok": true, "step": 1, "current_kbps": 2000, "baseline_kbps": 2500, "verdict": "congested"}`,
		"/health":         `{"pcs": {"fresh": false, "age_sec": 12.0}, "server": {"cpu_pct": 2.5, "max_rss_mb": 80.1}, "errors": [{"a": 1}, {"b": 2}]}`,
		"/live/view":      `{"state": {"battingTeamName": "Bridestowe", "score": 101, "wickets": 3, "overs": "17.2", "last_ball": " 4 "}}`,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*hits = append(*hits, r.URL.Path)
		mu.Unlock()
		routes(w, r)
	})
}

func streamingOBS(bytesOut, skipped float64) *obswstest.Server {
	return obswstest.New("pw", func(rt string, _ map[string]any) (bool, map[string]any) {
		switch rt {
		case "GetStreamStatus":
			return true, map[string]any{"outputActive": true, "outputReconnecting": false, "outputBytes": bytesOut,
				"outputSkippedFrames": skipped, "outputTotalFrames": 9000, "outputCongestion": 0.012345}
		case "GetStats":
			return true, map[string]any{"cpuUsage": 3.14159, "activeFps": 29.97, "memoryUsage": 0.0,
				"renderSkippedFrames": 2, "renderTotalFrames": 9100}
		}
		return false, nil
	})
}

func TestSampleRow(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	fake := streamingOBS(2_000_000, 7)
	sandbox(t, fake, telemetryServer(t, &hits, &mu))
	c, err := obsws.Dial(obsTarget(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	prev := &prevSample{t: time.Now().Add(-10 * time.Second), bytes: 750_000, skipped: 2}
	r, next, err := sample(c, prev)
	if err != nil {
		t.Fatal(err)
	}
	rec := map[string]string{}
	for i, v := range r.record() {
		rec[Fields[i]] = v
	}
	for k, want := range map[string]string{
		"congestion": "0.0123", "delta_skipped": "5", "skipped_total": "7", "frames_total": "9000",
		"stream_active": "True", "reconnecting": "False",
		"obs_cpu_pct": "3.14", "obs_fps": "30.0", "obs_mem_mb": "0", "render_skipped": "2",
		"ladder_step": "1", "ladder_current_kbps": "2000", "ladder_verdict": "congested",
		"pcs_fresh": "False", "pcs_age_s": "12.0", "srv_cpu_pct": "2.5", "srv_errors": "2",
		"batting_team": "Bridestowe", "score": "101", "wickets": "3", "overs": "17.2", "last_ball": "4",
		"probe_mbps": "", "elapsed_s": "",
	} {
		if rec[k] != want {
			t.Errorf("%s = %q, want %q", k, rec[k], want)
		}
	}
	if m := rec["inst_mbps"]; !strings.HasPrefix(m, "0.99") && !strings.HasPrefix(m, "1.0") {
		t.Errorf("inst_mbps = %q, want ~1.0 (1.25 MB over 10 s)", m)
	}
	if next.bytes != 2_000_000 || next.skipped != 7 {
		t.Errorf("next = %+v", next)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range hits {
		if h == "/live" {
			t.Error("telemetry called /live, which is a mutating GET only the overlay may call")
		}
	}
	if !strings.Contains(strings.Join(hits, " "), "/live/view") {
		t.Errorf("never read /live/view: %v", hits)
	}
}

func TestSampleBlanksRatesAfterAStreamRestart(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	sandbox(t, streamingOBS(1000, 0), telemetryServer(t, &hits, &mu))
	c, err := obsws.Dial(obsTarget(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, _, err := sample(c, &prevSample{t: time.Now(), bytes: 5_000_000, skipped: 40})
	if err != nil {
		t.Fatal(err)
	}
	if r["inst_mbps"] != "" || r["delta_skipped"] != "" {
		t.Errorf("rates after a counter reset should be blank, got %v / %v", r["inst_mbps"], r["delta_skipped"])
	}
}

func TestSampleWithoutOBSStillLogsTheServer(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	sandbox(t, nil, telemetryServer(t, &hits, &mu))
	r, _, err := sample(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := r.record()
	got := map[string]string{}
	for i, v := range rec {
		got[Fields[i]] = v
	}
	if got["score"] != "101" || got["ladder_step"] != "1" {
		t.Errorf("server columns missing without OBS: %v", got)
	}
	if got["congestion"] != "0" || got["stream_active"] != "" || got["skipped_total"] != "0" {
		t.Errorf("OBS columns without OBS: congestion=%q active=%q skipped=%q", got["congestion"], got["stream_active"], got["skipped_total"])
	}
}

func TestProbeGuard(t *testing.T) {
	var gotUA string
	var gotLen int
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		gotLen = len(b)
	}))
	defer probe.Close()
	old := ProbeURL
	ProbeURL = probe.URL
	defer func() { ProbeURL = old }()

	cases := []struct {
		r   row
		why string
	}{
		{row{"stream_active": nil, "congestion": 0, "delta_skipped": ""}, "not streaming"},
		{row{"stream_active": true, "congestion": 0.0123, "delta_skipped": int64(0)}, "congested (0.0123)"},
		{row{"stream_active": true, "congestion": 0, "delta_skipped": int64(12)}, "dropping frames (12)"},
	}
	for _, c := range cases {
		capture(t, func() { probeGuarded(c.r) })
		if c.r["probe_skipped_why"] != c.why {
			t.Errorf("why = %v, want %q", c.r["probe_skipped_why"], c.why)
		}
	}
	if gotLen != 0 {
		t.Fatal("probe ran while the stream was struggling")
	}
	r := row{"stream_active": true, "congestion": 0, "delta_skipped": int64(0), "iso_time": "t"}
	out := capture(t, func() { probeGuarded(r) })
	if gotLen != ProbeBytes || !strings.Contains(gotUA, "CricketStreamOverlay") {
		t.Errorf("probe upload: %d bytes, UA %q", gotLen, gotUA)
	}
	if _, ok := r["probe_mbps"].(float64); !ok || !strings.Contains(out, "probe:") {
		t.Errorf("probe_mbps = %v, out %q", r["probe_mbps"], out)
	}
}

func TestTelemetryWritesACSVAndSurvivesNoOBS(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	sandbox(t, nil, telemetryServer(t, &hits, &mu))
	stop := make(chan struct{})
	time.AfterFunc(700*time.Millisecond, func() { close(stop) })
	out := capture(t, func() {
		if code := Telemetry(TelemetryOptions{Interval: 0.2, Stop: stop}); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	if !strings.Contains(out, "OBS not reachable") || !strings.Contains(out, "samples written to") {
		t.Errorf("output:\n%s", out)
	}
	files, _ := filepath.Glob(filepath.Join(assets.Dir(), "diagnostics", "telemetry_*.csv"))
	if len(files) != 1 {
		t.Fatalf("csv files: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n")
	if lines[0] != strings.Join(Fields, ",") {
		t.Errorf("header %q", lines[0])
	}
	if len(lines) < 3 {
		t.Errorf("want at least 2 samples, got %d lines", len(lines))
	}
	if !strings.Contains(out, "=== "+filepath.Base(files[0])+" ===") {
		t.Error("no summary printed on stop")
	}
}

// ── refresh-cam ──

func TestReloadSourceWritesTheSettingsBackUnchanged(t *testing.T) {
	settings := map[string]any{"input": "rtsp://u:p@192.0.2.10:554/h264Preview_01_main", "hw_decode": true}
	fake := obswstest.New("", func(rt string, d map[string]any) (bool, map[string]any) {
		if d["inputName"] != "Cam" {
			return false, nil
		}
		if rt == "GetInputSettings" {
			return true, map[string]any{"inputSettings": settings}
		}
		return true, nil
	})
	sandbox(t, fake, nil)
	c, err := obsws.Dial(obsTarget(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := reloadSource(c, "Cam"); err != nil {
		t.Fatal(err)
	}
	set := fake.Requests[len(fake.Requests)-1]
	if set.Type != "SetInputSettings" || set.Data["overlay"] != true {
		t.Fatalf("last request %+v", set)
	}
	b1, _ := json.Marshal(set.Data["inputSettings"])
	b2, _ := json.Marshal(settings)
	if string(b1) != string(b2) {
		t.Errorf("settings changed: %s vs %s", b1, b2)
	}
	if err := reloadSource(c, "Nope"); err == nil || !strings.Contains(err.Error(), "refused by fake") {
		t.Errorf("missing source: %v", err)
	}
}

func TestRefreshCamOnceAndLoop(t *testing.T) {
	fake := obswstest.New("", func(rt string, d map[string]any) (bool, map[string]any) {
		return true, map[string]any{"inputSettings": map[string]any{}}
	})
	sandbox(t, fake, nil)
	tg := obsTarget()
	out := capture(t, func() {
		if code := RefreshCam(RefreshOptions{Once: true, Host: tg.Host, Port: tg.Port}); code != 0 {
			t.Errorf("once: exit %d", code)
		}
	})
	if !strings.Contains(out, "reloaded 'Cam'") {
		t.Errorf("once output: %s", out)
	}
	stop := make(chan struct{})
	time.AfterFunc(450*time.Millisecond, func() { close(stop) })
	out = capture(t, func() { RefreshCam(RefreshOptions{Interval: 0.1, Host: tg.Host, Port: tg.Port, Stop: stop}) })
	if !strings.Contains(out, "(#2)") || !strings.Contains(out, "stopped after") {
		t.Errorf("loop output: %s", out)
	}
}

func TestRefreshCamReconnectsWhenOBSRestarts(t *testing.T) {
	fake := obswstest.New("", func(string, map[string]any) (bool, map[string]any) {
		return true, map[string]any{"inputSettings": map[string]any{}}
	})
	sandbox(t, fake, nil)
	tg := obsTarget()
	stop := make(chan struct{})
	// Drop every connection once the first reload is done: the refresher must log the
	// failure and reconnect, never exit.
	time.AfterFunc(150*time.Millisecond, fake.DropConnections)
	time.AfterFunc(700*time.Millisecond, func() { close(stop) })
	out := capture(t, func() { RefreshCam(RefreshOptions{Interval: 0.1, Host: tg.Host, Port: tg.Port, Stop: stop}) })
	if !strings.Contains(out, "reload failed") || !strings.Contains(out, "reconnected") {
		t.Errorf("output:\n%s", out)
	}
}

// ── camera-encoder ──

func TestCameraEncoderReadsCredentialsFromOBSAndKeepsThemOutOfTheURL(t *testing.T) {
	var queries []string
	var loginBody string
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Query().Get("cmd") {
		case "Login":
			loginBody = string(b)
			w.Write([]byte(`[{"cmd":"Login","code":0,"value":{"Token":{"leaseTime":3600,"name":"tok123"}}}]`))
		case "GetEnc":
			w.Write([]byte(`[{"cmd":"GetEnc","code":0,"value":{"Enc":{"audio":0,"channel":0,
				"mainStream":{"bitRate":1024,"frameRate":25,"gop":2,"height":1440,"profile":"High","size":"2560*1440","width":2560},
				"subStream":{"bitRate":256,"frameRate":15,"profile":"High","size":"640*360"}}},
				"range":[{"Enc":{"mainStream":{"bitRate":[1024,1536,2048,3072,4096,5120,6144,8192],
				"frameRate":[25,22,20],"size":"2560*1440"}}}]}]`))
		}
	}))
	defer cam.Close()
	host, port, _ := net.SplitHostPort(cam.Listener.Addr().String())
	old := cameraPort
	cameraPort = port
	defer func() { cameraPort = old }()

	fake := obswstest.New("", func(rt string, d map[string]any) (bool, map[string]any) {
		return true, map[string]any{"inputSettings": map[string]any{"input": "rtsp://admin:p%40ss@" + host + ":554/h264Preview_01_main"}}
	})
	sandbox(t, fake, nil)
	out := capture(t, func() {
		if code := CameraEncoder(CameraOptions{}); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	for _, q := range queries {
		if strings.Contains(q, "admin") || strings.Contains(q, "ss") {
			t.Errorf("credentials in the query string: %q", q)
		}
	}
	if !strings.Contains(loginBody, `"userName":"admin"`) || !strings.Contains(loginBody, `"password":"p@ss"`) {
		t.Errorf("login body %s", loginBody)
	}
	if queries[1] != "cmd=GetEnc&token=tok123" {
		t.Errorf("GetEnc query %q", queries[1])
	}
	for _, want := range []string{
		"camera " + host + " (from OBS source 'Cam', user 'admin')",
		"resolution : 2560x1440",
		"size       : 640*360",
		"gop        : 2",
		"audio enabled: 0",
		"bitrates (kbps)   : [1024, 1536, 2048, 3072, 4096, 5120, 6144, 8192]",
		"resolutions       : 2560*1440",
		"Main stream is 1024 kbps. That is low",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCameraEncoderLoginRejected(t *testing.T) {
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"cmd":"Login","code":1,"error":{"detail":"login failed","rspCode":-7}}]`))
	}))
	defer cam.Close()
	host, port, _ := net.SplitHostPort(cam.Listener.Addr().String())
	old := cameraPort
	cameraPort = port
	defer func() { cameraPort = old }()
	fake := obswstest.New("", func(string, map[string]any) (bool, map[string]any) {
		return true, map[string]any{"inputSettings": map[string]any{"input": "rtsp://admin:x@" + host + "/"}}
	})
	sandbox(t, fake, nil)
	out := capture(t, func() {
		if code := CameraEncoder(CameraOptions{}); code != 1 {
			t.Errorf("exit %d", code)
		}
	})
	if !strings.Contains(out, "login failed: login rejected: login failed") {
		t.Errorf("output: %s", out)
	}
}

func TestCameraEncoderWithoutOBS(t *testing.T) {
	sandbox(t, nil, nil)
	out := capture(t, func() { CameraEncoder(CameraOptions{}) })
	if !strings.Contains(out, "Could not read an RTSP URL from OBS source 'Cam'") {
		t.Errorf("output: %s", out)
	}
}

// ── quality-test ──

type ladderServer struct {
	mu      sync.Mutex
	step    int
	actions []string
	auth    []string
}

func (l *ladderServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.auth = append(l.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/login":
		w.Write([]byte(`{"ok": true, "session_token": "sess"}`))
	case "/stream/monitor":
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "step": l.step, "current_kbps": 2500 - 500*l.step,
			"baseline_kbps": 2500, "dynamic_bitrate": false, "congestion": 0})
	case "/stream/quality":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		l.actions = append(l.actions, b["action"].(string))
		if b["action"] == "down" {
			l.step = 1
		} else {
			l.step = 0
		}
		w.Write([]byte(`{"ok": true}`))
	default:
		http.NotFound(w, r)
	}
}

func shrinkWaits(t *testing.T) {
	a, b, c := settleWait, shiftWait, pollInterval
	settleWait, shiftWait, pollInterval = 10*time.Millisecond, 120*time.Millisecond, 30*time.Millisecond
	savedSent := sentKbps
	sentKbps = func(*obsws.Conn, time.Duration) float64 { return 0 } // the fake OBS sends nothing
	t.Cleanup(func() { settleWait, shiftWait, pollInterval, sentKbps = a, b, c, savedSent })
}

func TestQualityTestBothShiftsSurvive(t *testing.T) {
	shrinkWaits(t)
	ladder := &ladderServer{}
	sandbox(t, streamingOBS(123456, 0), ladder)
	os.WriteFile(assets.Join("config.ini"), []byte("[OBS]\nobs_password = pw\n\n[Auth]\nclub_password = secret\n"), 0o644)
	out := capture(t, func() {
		if code := QualityTest(); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	if strings.Join(ladder.actions, ",") != "down,restore" {
		t.Errorf("actions %v", ladder.actions)
	}
	if ladder.auth[len(ladder.auth)-1] != "Bearer sess" {
		t.Errorf("not using the session token: %v", ladder.auth)
	}
	for _, want := range []string{"Stream is live — 123456 bytes sent so far", "Now at step 1, 2000 kbps (baseline 2500 kbps)",
		"Back to step 0, 2500 kbps", "ALL SYSTEMS GO"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestQualityTestCatchesAStreamThatDies(t *testing.T) {
	shrinkWaits(t)
	ladder := &ladderServer{}
	var mu sync.Mutex
	calls := 0
	fake := obswstest.New("", func(rt string, _ map[string]any) (bool, map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		// Live for the first check, dead once the down-shift is under way.
		return true, map[string]any{"outputActive": calls == 1, "outputSkippedFrames": 0, "outputBytes": 1}
	})
	sandbox(t, fake, ladder)
	out := capture(t, func() {
		if code := QualityTest(); code != 1 {
			t.Errorf("exit %d", code)
		}
	})
	if !strings.Contains(out, "did NOT survive the down-shift") || !strings.Contains(out, "do not enable auto quality") {
		t.Errorf("output:\n%s", out)
	}
}

func TestQualityTestRefusesWhenNotLive(t *testing.T) {
	ladder := &ladderServer{}
	fake := obswstest.New("", func(string, map[string]any) (bool, map[string]any) {
		return true, map[string]any{"outputActive": false}
	})
	sandbox(t, fake, ladder)
	out := capture(t, func() {
		if code := QualityTest(); code != 1 {
			t.Errorf("exit %d", code)
		}
	})
	if !strings.Contains(out, "OBS is not currently streaming") || len(ladder.actions) != 0 {
		t.Errorf("output:\n%s\nactions %v", out, ladder.actions)
	}
}

func TestPyRepr(t *testing.T) {
	for v, want := range map[string]string{
		`["a", 1, 2.5, null, true]`: `['a', 1, 2.5, None, True]`,
		`{"b": "it's", "a": [1]}`:   `{'a': [1], 'b': "it's"}`,
		`"back\\slash"`:             `'back\\slash'`,
	} {
		var x any
		json.Unmarshal([]byte(v), &x)
		if got := pyRepr(x); got != want {
			t.Errorf("pyRepr(%s) = %s, want %s", v, got, want)
		}
	}
}

func TestQualityStepJudgedByWhatOBSSends(t *testing.T) {
	saved := sentKbps
	t.Cleanup(func() { sentKbps = saved })
	for _, c := range []struct {
		sending float64
		want    bool
	}{{6000, false}, {2700, true}, {1500, true}, {0, true}} {
		sentKbps = func(*obsws.Conn, time.Duration) float64 { return c.sending }
		if got := tookEffect(nil, 2800.0); got != c.want {
			t.Errorf("sending %.0f with 2800 set: %v", c.sending, got)
		}
	}
}
