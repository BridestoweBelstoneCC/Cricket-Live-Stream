package main

// End-to-end tests of the built app: the binary is compiled once and every test runs it
// for real, in its own app folder, the way a club does — double-click (no arguments),
// the setup page in a "browser", each role, the subcommands. They cover what the unit
// tests can't: role dispatch, the launcher supervising the server as a child process,
// the built-in assets, flags, exit codes, and shutdown.
//
// Safety: every run gets its own app folder, home folder and port, listens on loopback
// only, and points CRICKETSTREAM_OBS_PORTABLE at an empty portable-OBS folder — so no run
// can find, open or reconfigure an OBS installed on the machine, or write into a real
// NV Play folder.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// exeName is unusual on purpose: TestMain stops every process of that name on the way
// out, which catches a server the launcher started and a test then abandoned.
var exeName = "cricketstream-e2e-test"

var exePath string

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "cricketstream-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	exePath = filepath.Join(dir, exeName)
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.Command(gobin, "build", "-o", exePath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the app: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	killStragglers()
	os.RemoveAll(dir)
	os.Exit(code)
}

func killStragglers() {
	if runtime.GOOS == "windows" {
		exec.Command("taskkill", "/F", "/IM", exeName).Run()
	} else {
		exec.Command("pkill", "-f", exeName).Run()
	}
}

func needBinary(t *testing.T) {
	t.Helper()
	if testing.Short() || exePath == "" {
		t.Skip("end-to-end tests build the app; skipped with -short")
	}
}

// ── An app folder ──

type app struct {
	t    *testing.T
	dir  string // the app folder (CRICKETSTREAM_HOME)
	home string // the user's home folder, as far as the app can tell
	port int    // the stream server's port
	env  []string
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newApp(t *testing.T) *app {
	t.Helper()
	needBinary(t)
	root := t.TempDir()
	a := &app{t: t, dir: filepath.Join(root, "CricketStream"), home: filepath.Join(root, "home"), port: freePort(t)}
	obsp := filepath.Join(root, "obs-portable") // a portable OBS with no OBS in it
	for _, d := range []string{a.dir, a.home, obsp} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(obsp, "obs_portable_mode.txt"), nil, 0o644)
	keep := []string{}
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if strings.HasPrefix(k, "CRICKETSTREAM_") || k == "HOME" || k == "USERPROFILE" || k == "APPDATA" ||
			k == "LOCALAPPDATA" || k == "XDG_CONFIG_HOME" || k == "BBCC_BIND_HOST" {
			continue
		}
		keep = append(keep, kv)
	}
	a.env = append(keep,
		"CRICKETSTREAM_HOME="+a.dir,
		"CRICKETSTREAM_NO_PAUSE=1",
		"CRICKETSTREAM_NO_BROWSER=1",
		"CRICKETSTREAM_LOOPBACK_ONLY=1",
		fmt.Sprintf("CRICKETSTREAM_PORT=%d", a.port),
		"CRICKETSTREAM_OBS_PORTABLE="+obsp,
		// OBS's port moved to one nothing listens on: the machine's own OBS (on 4455
		// whenever it's open) is out of reach even before the portable guard.
		fmt.Sprintf("CRICKETSTREAM_OBS_WS_PORT=%d", freePort(t)),
		"CRICKETSTREAM_APP_PORT=0",
		"CRICKETSTREAM_NO_WINDOW=1", // the app's page, driven over HTTP as its window would
		"HOME="+a.home, "USERPROFILE="+a.home,
		"APPDATA="+filepath.Join(a.home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(a.home, "AppData", "Local"),
		"XDG_CONFIG_HOME="+filepath.Join(a.home, ".config"),
	)
	return a
}

func (a *app) path(elem ...string) string { return filepath.Join(append([]string{a.dir}, elem...)...) }

func (a *app) write(name, content string) {
	a.t.Helper()
	p := a.path(name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		a.t.Fatal(err)
	}
}

func (a *app) read(name string) string {
	b, _ := os.ReadFile(a.path(name))
	return string(b)
}

func (a *app) base() string { return fmt.Sprintf("http://127.0.0.1:%d", a.port) }

// streamingConfig is a configured streaming laptop (no PlayCricket key: no network).
func (a *app) streamingConfig(extra string) {
	a.write("config.ini", "[App]\nrole = streaming\n\n[Club]\nname = Test Town CC\nabbreviation = TTC\n"+
		"home_colour = #123456\nplaycricket_id = 4242\n\n[OBS]\nmanage_obs = yes\n\n"+
		"[Graphics]\nscorebar_style = modern\n\n[Stream]\nmax_overs = 40\n"+extra)
}

// ── A running copy ──

type proc struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	out   bytes.Buffer
	done  chan struct{}
	code  int
}

func (p *proc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.Write(b)
}

func (p *proc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

// start runs the app. Its stdin stays open (an operator who hasn't typed anything yet)
// until closeInput; the questions then take their defaults.
func (a *app) start(args ...string) *proc {
	a.t.Helper()
	p := &proc{t: a.t, done: make(chan struct{})}
	p.cmd = exec.Command(exePath, args...)
	p.cmd.Dir = a.dir
	p.cmd.Env = a.env
	p.cmd.Stdout, p.cmd.Stderr = p, p
	setProcessGroup(p.cmd)
	in, err := p.cmd.StdinPipe()
	if err != nil {
		a.t.Fatal(err)
	}
	p.stdin = in
	if err := p.cmd.Start(); err != nil {
		a.t.Fatal(err)
	}
	go func() {
		p.cmd.Wait()
		p.code = p.cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	a.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			p.cmd.Process.Kill()
			<-p.done
		}
		if a.t.Failed() {
			a.t.Logf("output of %v:\n%s", args, p.output())
		}
	})
	return p
}

// run runs the app to completion with no input.
func (a *app) run(timeout time.Duration, args ...string) (string, int) {
	a.t.Helper()
	p := a.start(args...)
	p.closeInput()
	code := p.wait(timeout)
	return p.output(), code
}

func (p *proc) closeInput() { p.stdin.Close() }

func (p *proc) say(line string) { io.WriteString(p.stdin, line+"\n") }

func (p *proc) wait(timeout time.Duration) int {
	p.t.Helper()
	select {
	case <-p.done:
		return p.code
	case <-time.After(timeout):
		p.t.Fatalf("still running after %v:\n%s", timeout, p.output())
	}
	return -1
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// waitFor waits for the output to match re and returns the submatches.
func (p *proc) waitFor(re string, timeout time.Duration) []string {
	p.t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := rx.FindStringSubmatch(p.output()); m != nil {
			return m
		}
		if !p.running() {
			if m := rx.FindStringSubmatch(p.output()); m != nil {
				return m
			}
			p.t.Fatalf("exited (code %d) before printing %q:\n%s", p.code, re, p.output())
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("no %q after %v:\n%s", re, timeout, p.output())
	return nil
}

// ── HTTP ──

var client = &http.Client{Timeout: 15 * time.Second}

type reply struct {
	status int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not JSON (%d): %.300s", r.status, r.body)
	}
	return m
}

func do(t *testing.T, method, url string, body any, headers map[string]string) reply {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, string(b)}
}

func get(t *testing.T, url string) reply { t.Helper(); return do(t, "GET", url, nil, nil) }

// waitHTTP waits until url answers at all.
func waitHTTP(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	c := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if resp, err := c.Get(url); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never answered", url)
}

func portClosed(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return true
	}
	c.Close()
	return false
}

func waitClosed(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portClosed(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("port %d still open after %v", port, timeout)
}

func iniValue(text, section, key string) string {
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			cur = l[1 : len(l)-1]
			continue
		}
		if cur == section {
			if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == key {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func mustContain(t *testing.T, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%.2000s", w, s)
		}
	}
}
