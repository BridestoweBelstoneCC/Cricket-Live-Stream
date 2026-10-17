// Package assets finds the app's own files: the HTML pages, fonts, brand images and the
// NV Play template. They are built into the executable, so the one download is the whole
// install; a file of the same name in the app folder wins over the built-in copy. That
// keeps the original workflow of editing control.html/overlay.html and refreshing,
// with no rebuild or restart.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"cricketstream"
)

var (
	dirOnce sync.Once
	dir     string
)

// Dir is the app folder: where config.ini, match_state.json and the database live and
// where overriding asset files are looked for. CRICKETSTREAM_HOME overrides it (tests,
// and running from a source checkout with `go run`).
func Dir() string {
	dirOnce.Do(func() {
		if d := os.Getenv("CRICKETSTREAM_HOME"); d != "" {
			dir, _ = filepath.Abs(d)
			return
		}
		exe, err := os.Executable()
		if err == nil {
			if r, err2 := filepath.EvalSymlinks(exe); err2 == nil {
				exe = r
			}
			dir = filepath.Dir(exe)
			return
		}
		dir, _ = os.Getwd()
	})
	return dir
}

// SetDir points the app folder somewhere else (tests).
func SetDir(d string) {
	dirOnce.Do(func() {})
	dir = d
}

// Join is a path inside the app folder.
func Join(elem ...string) string { return filepath.Join(append([]string{Dir()}, elem...)...) }

// Read returns an asset, preferring a copy on disk in the app folder.
func Read(name string) ([]byte, error) {
	if b, err := os.ReadFile(Join(filepath.FromSlash(name))); err == nil {
		return b, nil
	}
	return fs.ReadFile(cricketstream.Files, name)
}

// Embedded returns the built-in copy only (the scorer agent's bundled template check).
func Embedded(name string) ([]byte, error) { return fs.ReadFile(cricketstream.Files, name) }

var extractMu sync.Mutex

// Path returns a real file path for an asset, for external tools that need one on disk
// (ffmpeg's drawtext fonts). The app-folder copy
// is used when present; otherwise the built-in copy is written once to a cache folder
// keyed by its content, so an upgraded app never uses a stale extract.
func Path(name string) (string, error) {
	p := Join(filepath.FromSlash(name))
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	b, err := fs.ReadFile(cricketstream.Files, name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	out := filepath.Join(cache, "CricketStream", "assets", hex.EncodeToString(sum[:8]), filepath.FromSlash(name))
	extractMu.Lock()
	defer extractMu.Unlock()
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	return out, os.Rename(tmp, out)
}
