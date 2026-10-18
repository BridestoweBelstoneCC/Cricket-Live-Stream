package server

// The shared test sandbox: every path pointed at a temp dir (the real match_state.json /
// match_data.db are never touched), every pipeline cache reset, and the real Handler on
// an ephemeral loopback port. Tests in this package share module state the way the
// Python suite patched server.py's globals, so none of them run in parallel.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/browser"
	"cricketstream/internal/obs"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		sayOut = io.Discard
	}
	os.Exit(m.Run())
}

type testServer struct {
	t    *testing.T
	srv  *httptest.Server
	dir  string
	addr string
}

// sandbox points the package at a fresh temp dir and resets every cache. password
// sets ClubPassword explicitly: never inherit whatever a real config.ini says.
func sandbox(t *testing.T, password string) string {
	t.Helper()
	// A job the last test started (a season-stats build, a replay) may still be running
	// against the last test's files: let it finish before moving everything.
	waitBackground(t)
	closeDB()
	dir := t.TempDir()
	SetAppDir(dir)
	t.Cleanup(func() { waitBackground(t) })
	// Nothing a test does may reach this machine's own OBS settings or home folder: the
	// OBS guard writes OBS's global.ini on a normal close, and defaults (the replay
	// folder) come from the home folder.
	savedAssets := assets.Dir()
	assets.SetDir(dir) // the setup package (settings page) finds config.ini through it
	t.Cleanup(func() { assets.SetDir(savedAssets) })
	savedCfg, savedFind := obs.ConfigDir, obs.FindOBS
	obs.ConfigDir = func() string { return filepath.Join(dir, "obs-config") }
	obs.FindOBS = func() string { return "" }
	t.Cleanup(func() { obs.ConfigDir, obs.FindOBS = savedCfg, savedFind })
	savedOpen, savedFolder := browser.Open, browser.OpenFolder // no test opens a window
	browser.Open = func(string) error { return nil }
	browser.OpenFolder = func(string) error { return nil }
	t.Cleanup(func() { browser.Open, browser.OpenFolder = savedOpen, savedFolder })
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	ClubPassword, ControlToken = password, strings.Repeat("cd", 32)
	stateCacheMu.Lock()
	stateCache, lastGoodState = nil, nil
	stateCacheMu.Unlock()
	ResetManual(true) // never pick up a real leftover scoring session
	ResetPCSCache()
	SetInningsLatch(1)
	ResetEventDetection()
	ResetBallLog()
	ResetLiveFigures()
	MatchLogReset()
	ResetAgentClient()
	rvMu.Lock()
	rvCache = rvCacheT{}
	rvMu.Unlock()
	loginAttemptsMu.Lock()
	loginAttempts = map[string]*loginRecord{}
	loginAttemptsMu.Unlock()
	rateLimitMu.Lock()
	rateLimitTS = map[string]float64{}
	rateLimitMu.Unlock()
	if err := SaveState(DefaultState()); err != nil {
		t.Fatal(err)
	}
	DBInit()
	t.Cleanup(closeDB)
	return dir
}

func waitBackground(t *testing.T) {
	done := make(chan struct{})
	go func() { background.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Log("a background job is still running after 30s")
	}
}

func newTestServer(t *testing.T, password string) *testServer {
	t.Helper()
	sandbox(t, password)
	return serve(t)
}

// serve runs the Handler on the current sandbox.
func serve(t *testing.T) *testServer {
	t.Helper()
	srv := httptest.NewServer(Handler{})
	t.Cleanup(srv.Close)
	return &testServer{t: t, srv: srv, dir: appDir, addr: strings.TrimPrefix(srv.URL, "http://")}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T) M {
	t.Helper()
	var m M
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("not JSON (%d): %q", r.status, r.body)
	}
	return m
}

// raw sends the request over a plain socket so traversal paths reach the server exactly
// as written — Go's client would tidy some of them.
func (s *testServer) raw(method, path string, body any, headers map[string]string) resp {
	s.t.Helper()
	var payload []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		payload = b
	case string:
		payload = []byte(b)
	default:
		payload, _ = json.Marshal(b)
	}
	conn, err := net.DialTimeout("tcp", s.addr, 5*time.Second)
	if err != nil {
		s.t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, path, s.addr)
	for k, v := range headers {
		fmt.Fprintf(&req, "%s: %s\r\n", k, v)
	}
	if body != nil {
		if _, ok := headers["Content-Type"]; !ok {
			req.WriteString("Content-Type: application/json\r\n")
		}
		fmt.Fprintf(&req, "Content-Length: %d\r\n", len(payload))
	}
	req.WriteString("\r\n")
	req.Write(payload)
	conn.Write(req.Bytes()) // may fail part-way if the server rejects early (413)
	r, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return resp{status: -1} // the server dropped the connection
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, data}
}

func (s *testServer) get(path string, headers ...map[string]string) resp {
	s.t.Helper()
	return s.raw("GET", path, nil, first(headers))
}

func (s *testServer) post(path string, body any, headers ...map[string]string) resp {
	s.t.Helper()
	if body == nil {
		body = []byte{}
	}
	return s.raw("POST", path, body, first(headers))
}

func (s *testServer) getJSON(path string, headers ...map[string]string) (int, M) {
	s.t.Helper()
	r := s.get(path, headers...)
	return r.status, r.json(s.t)
}

func (s *testServer) postJSON(path string, body any, headers ...map[string]string) (int, M) {
	s.t.Helper()
	r := s.post(path, body, headers...)
	var m M
	json.Unmarshal(r.body, &m)
	return r.status, m
}

// Listen0 is Listen on a free loopback port.
func Listen0() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func first(h []map[string]string) map[string]string {
	if len(h) > 0 {
		return h[0]
	}
	return nil
}

// bearer logs in and returns the Authorization header.
func (s *testServer) bearer(password string) map[string]string {
	s.t.Helper()
	status, body := s.postJSON("/login", M{"password": password})
	if status != 200 || body["ok"] != true {
		s.t.Fatalf("login failed: %d %v", status, body)
	}
	return map[string]string{"Authorization": "Bearer " + body["session_token"].(string)}
}

func setState(t *testing.T, kv M) {
	t.Helper()
	UpdateState(func(s M) {
		for k, v := range kv {
			s[k] = v
		}
	})
}

func mustEq[T comparable](t *testing.T, got, want T, what ...any) {
	t.Helper()
	if got != want {
		t.Fatalf("%v: got %v, want %v", fmt.Sprint(what...), got, want)
	}
}
