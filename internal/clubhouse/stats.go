package clubhouse

// Two things the screen keeps for itself, so they're there with the laptop off:
//
// The club stats (the Stats tab). The laptop adds up every player's record for the club
// from PlayCricket (the server's /club/stats); the screen keeps its own copy in
// club_stats.json and asks for a new one when it finds the laptop's server running and its
// copy is a week old. It never asks PlayCricket itself: that would need the club's API key
// on a screen on a clubhouse wall.
//
// Extra tabs: [Screen] extra_tabs in clubhouse.ini adds tabs that show a page of the club's
// own, kept beside the app on the Pi ("Darts: darts.html, Quiz: quiz.html"). Nothing of
// the club's goes into CricketStream for it, and an update never touches the page.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cricketstream/internal/assets"
)

// statsEvery is how old the screen's copy may get before it asks for a new one; statsRetry
// is how soon it asks again when the laptop's still adding them up (the first build ever
// reads every scorecard the club has, and takes a few minutes).
var (
	statsEvery = 7 * 24 * time.Hour
	statsRetry = 2 * time.Minute
)

func statsFile() string { return assets.Join("club_stats.json") }

// statsDueLocked: time to ask the laptop for the stats. s.mu held.
func (s *Screen) statsDueLocked() bool {
	if s.statsBusy || time.Since(s.statsTried) < statsRetry {
		return false
	}
	fi, err := os.Stat(statsFile())
	return err != nil || time.Since(fi.ModTime()) > statsEvery
}

// fetchStats asks the laptop for the club stats and keeps them if they're whole.
func (s *Screen) fetchStats() {
	defer func() {
		s.mu.Lock()
		s.statsBusy, s.statsTried = false, time.Now()
		s.mu.Unlock()
	}()
	code, _, b, err := s.toLaptop(s.slow, "GET", "/clubhouse/server/club/stats", nil)
	if err != nil || code != 200 {
		return
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil || st["ready"] != true || st["error"] != nil && st["error"] != "" {
		return // still being added up, or PlayCricket wasn't reachable: keep what there is
	}
	if ps, _ := st["players"].([]any); len(ps) == 0 {
		return
	}
	st["fetched"] = time.Now().Format(time.RFC3339)
	out, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := statsFile() + ".tmp"
	if os.WriteFile(tmp, out, 0o644) == nil {
		os.Rename(tmp, statsFile())
	}
}

// serveStats is GET /api/stats: the screen's copy, whether or not the laptop's on.
func (s *Screen) serveStats(w http.ResponseWriter) {
	b, err := os.ReadFile(statsFile())
	if err != nil {
		s.mu.Lock()
		busy := s.statsBusy || !s.statsTried.IsZero()
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ok": false, "none": true, "asked": busy}, 200)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// ── Extra tabs ──

type extraTab struct {
	Label string `json:"label"`
	File  string `json:"-"`
	URL   string `json:"url"`
}

var extraFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*\.html$`)

// parseExtraTabs reads "Darts: darts.html, Quiz: quiz.html". A file must be a plain .html
// name beside the app: no folders, nothing else on the Pi.
func parseExtraTabs(v string) []extraTab {
	var out []extraTab
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
		label, file, ok := strings.Cut(part, ":")
		label, file = strings.TrimSpace(label), strings.TrimSpace(file)
		if !ok || label == "" || len(label) > 20 || !extraFileName.MatchString(file) || filepath.Base(file) != file {
			continue
		}
		out = append(out, extraTab{Label: label, File: file, URL: "/extra/" + file})
		if len(out) == 3 {
			break
		}
	}
	return out
}

// extraCSP: the club's page runs its own inline script and nothing from outside, and only
// the screen's own page may show it.
const extraCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'self'"

// serveExtra is GET /extra/<file>: only a file named in extra_tabs.
func (s *Screen) serveExtra(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/extra/")
	s.mu.Lock()
	tabs := parseExtraTabs(s.cfg.Extra)
	s.mu.Unlock()
	for _, t := range tabs {
		if t.File != name {
			continue
		}
		b, err := os.ReadFile(assets.Join(t.File))
		if err != nil {
			http.Error(w, t.File+" isn't beside the clubhouse screen's app", http.StatusNotFound)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", extraCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		w.Write(b)
		return
	}
	http.NotFound(w, r)
}
