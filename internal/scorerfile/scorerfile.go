// Package scorerfile finds the scorer's live scoreboard file — the one file NV Play /
// PCS Pro rewrites on every ball. The streaming server, the scorer agent and the NV Play
// bridge all use it. (The Python original kept three hand-synchronised copies because
// the agent and bridge had to run as standalone scripts; in the single app they share
// this one.)
package scorerfile

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// DecodeText is Python's text-mode read with encoding="utf-8", errors="replace": each
// invalid byte becomes U+FFFD and \r\n / \r become \n (universal newlines).
func DecodeText(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			sb.WriteRune('�')
		} else {
			sb.Write(b[:n])
		}
		b = b[n:]
	}
	s := strings.ReplaceAll(sb.String(), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// ReadText reads a file as DecodeText does.
func ReadText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return DecodeText(b), nil
}

// OutputFilenames are the names the scorer's software writes, in priority order.
var OutputFilenames = []string{
	"nvplay-scoreboard1.xml",
	"nvplay-scoreboard.xml",
	"scoreboard-output.json",
	"scoreboard-output.xml",
	"scoreboard.json",
	"scoreboard.xml",
	"pcs-output.json",
	"output.json",
	"live.json",
}

// CommonFolderPatterns are folders PCS Pro / NV Play commonly write to, relative to the
// user's home. Windows 11 commonly redirects Documents (and Desktop) into OneDrive, so
// the same places also live under OneDrive/ — or "OneDrive - <organisation>/" on a work
// or school account.
var CommonFolderPatterns = func() []string {
	base := []string{
		"Documents/Cricket Matches/_Scoreboards/Output",
		"Documents/Cricket Matches/*/Output",
		"Documents/Cricket Matches/_Scoreboards/*",
		"Documents/NV Play/Scoreboards/Output",
		"Documents/NV Play/*/Output",
		"Documents/PCS Pro/Output",
		"Documents/PCS Pro/Scoreboard*",
		"Documents/Scoreboards/Output",
		"Documents/Scoreboard*",
		"Desktop/Scoreboard*",
		"Desktop/Output",
	}
	out := append([]string(nil), base...)
	for _, p := range base {
		out = append(out, "OneDrive*/"+p)
	}
	return out
}()

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// MTime is a file's modification time as float seconds since the epoch (Python's
// os.path.getmtime), or 0 if it can't be read.
func MTime(p string) float64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return float64(st.ModTime().UnixNano()) / 1e9
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Find returns the scoreboard output file in folder, or "".
//
// Known filenames win. Otherwise fall back to the most recently modified .json/.xml in
// the folder, provided it was touched within maxAge (maxAge < 0 accepts any age — used
// when hunting for the folder itself, before a match has started).
func Find(folder string, maxAge time.Duration) string {
	if folder == "" || !isDir(folder) {
		return ""
	}
	for _, name := range OutputFilenames {
		if p := filepath.Join(folder, name); exists(p) {
			return p
		}
	}
	var candidates []string
	for _, ext := range []string{"*.json", "*.xml"} {
		m, _ := filepath.Glob(filepath.Join(folder, ext))
		for _, c := range m {
			if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
				candidates = append(candidates, c)
			}
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	newest, best := "", -1.0
	for _, c := range candidates {
		if t := MTime(c); t > best {
			newest, best = c, t
		}
	}
	if maxAge < 0 || now()-best < maxAge.Seconds() {
		return newest
	}
	return ""
}

// HasScoreboard says whether folder holds a scoreboard file, written within maxAge if
// maxAge >= 0.
//
// Checks the age itself rather than leaving it to Find(), which returns a known
// filename (nvplay-scoreboard1.xml...) whatever its age — right for serving the file,
// wrong here: it made a folder holding last season's scoreboard count as "live", so
// Autodetect() could pick an old PCS Pro folder over the one NV Play was writing to at
// that moment.
func HasScoreboard(folder string, maxAge time.Duration) bool {
	p := Find(folder, -1)
	if p == "" {
		return false
	}
	if maxAge < 0 {
		return true
	}
	t := MTime(p)
	return t != 0 && now()-t < maxAge.Seconds()
}

// CandidateFolders are the existing folders matching CommonFolderPatterns under home.
func CandidateFolders(home string) []string {
	var seen []string
	in := map[string]bool{}
	for _, pattern := range CommonFolderPatterns {
		m, err := filepath.Glob(filepath.Join(home, filepath.FromSlash(pattern)))
		if err != nil {
			continue
		}
		sort.Strings(m)
		for _, p := range m {
			p = filepath.Clean(p)
			if isDir(p) && !in[p] {
				in[p] = true
				seen = append(seen, p)
			}
		}
	}
	return seen
}

// nvplayDefaultOutput is where NV Play writes unless told otherwise.
var nvplayDefaultOutput = []string{
	"Documents/Cricket Matches/_Scoreboards/Output",
	"OneDrive*/Documents/Cricket Matches/_Scoreboards/Output",
}

// Detect is setup's search: Autodetect(), then NV Play's standard output folder even
// before any match has written to it ("empty"). Returns ("", "") when nothing is found.
func Detect() (folder, how string) {
	if f, h := Autodetect(); f != "" {
		return f, h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	for _, pattern := range nvplayDefaultOutput {
		m, _ := filepath.Glob(filepath.Join(home, filepath.FromSlash(pattern)))
		sort.Strings(m)
		for _, p := range m {
			if isDir(p) {
				return filepath.Clean(p), "empty"
			}
		}
	}
	return "", ""
}

// Autodetect hunts the usual places for the PCS Pro / NV Play output folder. Prefers a
// folder whose file was written recently (a live match, freshness "live"), then any
// folder that has ever held a scoreboard file ("stale").
func Autodetect() (folder, freshness string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	seen := CandidateFolders(home)
	for _, p := range seen {
		if HasScoreboard(p, 10*time.Minute) {
			return p, "live"
		}
	}
	for _, p := range seen {
		if HasScoreboard(p, -1) {
			return p, "stale"
		}
	}
	return "", ""
}
