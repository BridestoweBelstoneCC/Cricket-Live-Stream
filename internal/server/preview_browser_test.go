package server

// The overlay's preview mode in a real browser (headless Chrome/Edge): it must render
// the club's own name and colours and each demo graphic, and — the invariant that
// matters on match day — make no request that could eat the real overlay's events or
// commands, or act on OBS. Skipped when no Chrome or Edge is installed.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func findChrome(t *testing.T) string {
	t.Helper()
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	for _, p := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no Chrome or Edge to run the page in")
	return ""
}

var browserHung atomic.Bool

type reqLog struct {
	mu   sync.Mutex
	reqs []string
}

func (l *reqLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.reqs...)
}

// renderInBrowser loads path from a real server and returns the DOM after the page has
// run for `budget` of (virtual) time, plus every request it made.
func renderInBrowser(t *testing.T, path string, budget time.Duration) (string, []string) {
	t.Helper()
	chrome := findChrome(t)
	if browserHung.Load() {
		// One hang has already failed the run; another 90s per page only pushes the
		// package past go test's timeout, which hides every other result.
		t.Skip("the browser hung on an earlier page")
	}
	sandbox(t, "")
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.mu.Lock()
		log.reqs = append(log.reqs, r.Method+" "+r.URL.Path)
		log.mu.Unlock()
		Handler{}.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	profile := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// --use-mock-keychain / --password-store=basic: headless Chrome on macOS (and some
	// Linux desktops) otherwise waits on the system keychain and never loads the page —
	// seen on GitHub's macOS runners, where every browser test sat out its 90 seconds.
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-first-run",
		"--no-default-browser-check", "--use-mock-keychain", "--password-store=basic", "--user-data-dir="+profile, "--window-size=1920,1080",
		"--virtual-time-budget="+strconv.Itoa(int(budget/time.Millisecond)), "--dump-dom", srv.URL+path)
	// The timeout kills only Chrome's main process; its helpers inherit stdout and keep
	// it open, so without WaitDelay Output() waits on them for ever — on a Mac
	// (2026-11-01) the whole package ran into go test's 10-minute limit instead of
	// failing at 90s.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		browserHung.Store(true)
		t.Fatalf("browser didn't finish in 90s (%s): %v", chrome, err)
	}
	if err != nil && len(out) == 0 {
		t.Fatalf("browser: %v", err)
	}
	return string(out), log.all()
}

// Anything but reading the page, its assets and /state once is a failure.
func checkPreviewRequests(t *testing.T, reqs []string) {
	t.Helper()
	states := 0
	for _, r := range reqs {
		method, path, _ := strings.Cut(r, " ")
		switch {
		case method != "GET":
			t.Errorf("preview made a %s", r)
		case path == "/state":
			states++
		case path == "/overlay", path == "/favicon.ico", strings.HasPrefix(path, "/fonts/"),
			strings.HasPrefix(path, "/logo/"), strings.HasPrefix(path, "/sponsor/"),
			strings.HasPrefix(path, "/graphics/"), strings.HasPrefix(path, "/headshot/"):
		default:
			t.Errorf("preview requested %s", path)
		}
	}
	if states > 1 {
		t.Errorf("preview read /state %d times — it must read it once, then nothing", states)
	}
}

func TestPreviewShowsTheClubAsBeingEdited(t *testing.T) {
	q := url.Values{"preview": {"1"}, "style": {"modern"}, "name": {"Testbury & District CC"},
		"abbrev": {"tbury"}, "colour": {"#aa0011"}}
	dom, reqs := renderInBrowser(t, "/overlay?"+q.Encode(), 3*time.Second)
	checkPreviewRequests(t, reqs)
	if !strings.Contains(dom, `id="bowling-team-name">TBURY`) {
		t.Error("the abbreviation being typed isn't on the scorebar")
	}
	if !strings.Contains(dom, `class="style-modern`) && !strings.Contains(dom, "style-modern") {
		t.Error("style not applied")
	}
	if !strings.Contains(dom, `id="seg-bowling-team" style="background: rgb(170, 0, 17);"`) {
		t.Error("the colour being picked isn't used")
	}
}

func TestPreviewDemoGraphicsMakeNoRequests(t *testing.T) {
	// Element contents, which only the demo graphic fills in. (Whether the panel is
	// mid-animation when the page is captured depends on the browser's frame timing.)
	for g, want := range map[string]string{
		"wicket":      `id="fow-batter">Hatton`,
		"over":        `id="over-runs">13 runs`,
		"fifty":       `id="milestone-sub">50 off 33 balls`,
		"partnership": `id="partner-runs">50`,
		"six":         `id="event-flash" class="">SIX!`,
		"four":        `id="event-flash" class="">FOUR!`,
	} {
		t.Run(g, func(t *testing.T) {
			dom, reqs := renderInBrowser(t, "/overlay?preview=1&graphic="+g, 15*time.Second)
			checkPreviewRequests(t, reqs)
			if !strings.Contains(dom, want) {
				t.Errorf("%s: %q not on the page", g, want)
			}
		})
	}
}
