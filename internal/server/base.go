// Package server is the streaming laptop's backend: one HTTP server on port 5000 that
// reads the scorer's match data, serves the live state to the OBS overlay, runs the
// control panel and the manual scoring page, drives OBS, and makes the post-match
// outputs.
//
// Data flow: the scorer's software writes a frame on every ball; ReadScoreSource reads
// it and ParsePCSJSON turns it into the live state, which /live serves to overlay.html
// (an OBS browser source) every ~2.5s.
//
// Like the Python module it replaces, the package keeps its state in package-level
// variables guarded by locks, and the state dicts are plain map[string]any — the JSON
// the overlay and control panel exchange.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/pyval"
)

// M is a JSON object.
type M = map[string]any

var serverStart = time.Now()

// Paths inside the app folder. Package variables so tests can point them at a temp dir
// (SetAppDir), as the Python tests patched STATE_FILE and _db_path.
var (
	appDir             string
	StateFile          string
	ConfigFile         string
	DBFile             string
	ManualScoringFile  string
	SeasonStatsCache   string
	SponsorAirtimeFile string
	SponsorDir         string
	MatchPagesDir      string
	VoiceDir           string
	PCSBridgeCacheDir  string
	YTTokenFile        string
	YTCredsFile        string
	ControlHTMLFile    string
)

// SetAppDir points every app file at dir.
func SetAppDir(dir string) {
	appDir = dir
	j := func(n string) string { return filepath.Join(dir, n) }
	StateFile = j("match_state.json")
	ConfigFile = j("config.ini")
	DBFile = j("match_data.db")
	ManualScoringFile = j("manual_scoring.json")
	SeasonStatsCache = j("season_stats_cache.json")
	SponsorAirtimeFile = j("sponsor_airtime.json")
	SponsorDir = j("sponsors")
	MatchPagesDir = j("match_pages")
	VoiceDir = j(".voice")
	PCSBridgeCacheDir = j(".pcs_bridge_cache")
	YTTokenFile = j("yt_token.json")
	YTCredsFile = j("yt_credentials.json")
	ControlHTMLFile = j("control.html")
}

func init() { SetAppDir(assets.Dir()) }

// AppDir is the folder the app's files live in.
func AppDir() string { return appDir }

// ── Logging ──────────────────────────────────────────────────

// say prints a console line, as the Python server's print() did. Never panics.
func say(format string, args ...any) {
	if len(args) == 0 {
		fmt.Fprintln(sayOut, format)
		return
	}
	fmt.Fprintf(sayOut, format+"\n", args...)
}

// sayOut is the console; tests quieten it.
var sayOut io.Writer = os.Stdout

func init() { log.SetFlags(0) }

// ── map helpers (Python dict semantics) ───────────────────────

// gs is d.get(k, def) for a string: def when the key is missing, str() otherwise ("" for
// None, matching how the original treated JSON nulls in practice).
func gs(m M, k, def string) string {
	v, ok := m[k]
	if !ok {
		return def
	}
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return pyval.Str(v)
}

// gsOr is str(d.get(k) or def).
func gsOr(m M, k, def string) string {
	v := m[k]
	if !pyval.Truthy(v) {
		return def
	}
	return pyval.Str(v)
}

// gi is int(d.get(k) or 0) with a fallback for junk.
func gi(m M, k string, def int) int {
	v, ok := m[k]
	if !ok || v == nil {
		return def
	}
	return pyval.IntOr(v, def)
}

// gf is float(d.get(k) or 0).
func gf(m M, k string, def float64) float64 {
	v, ok := m[k]
	if !ok || v == nil {
		return def
	}
	if !pyval.Truthy(v) {
		return 0
	}
	f, err := pyval.Float(v)
	if err != nil {
		return def
	}
	return f
}

// gb is bool(d.get(k, def)).
func gb(m M, k string, def bool) bool {
	v, ok := m[k]
	if !ok {
		return def
	}
	return pyval.Truthy(v)
}

// gm returns a nested object (an empty one if missing or not an object).
func gm(m M, k string) M {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return M{}
}

// gl returns a nested list (nil if missing or not a list).
func gl(m M, k string) []any {
	if v, ok := m[k].([]any); ok {
		return v
	}
	return nil
}

// copyM is a deep copy through JSON (the state dicts are JSON anyway).
func copyM(m M) M {
	b, _ := json.Marshal(m)
	var out M
	json.Unmarshal(b, &out)
	if out == nil {
		out = M{}
	}
	return out
}

// normalise turns any JSON-able value into plain JSON types (float64, []any, M).
func normalise(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	json.Unmarshal(b, &out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func trim(s string) string { return strings.TrimSpace(s) }

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func mtime(p string) float64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return float64(st.ModTime().UnixNano()) / 1e9
}

// truncRunes is s[:n].
func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// background counts the safeGo jobs still running, so tests can let one finish before
// pointing the package at the next test's files.
var background sync.WaitGroup

// safeGo runs fn on its own goroutine; a panic is recorded, never fatal (nothing in the
// match-day loop may take the stream down).
func safeGo(where string, fn func()) {
	background.Add(1)
	go func() {
		defer background.Done()
		defer func() {
			if r := recover(); r != nil {
				logServerError(where, fmt.Errorf("%v", r))
			}
		}()
		fn()
	}()
}
