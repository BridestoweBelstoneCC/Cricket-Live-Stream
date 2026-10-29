// Package clubpack exports a club's CricketStream set-up to one file and imports it on
// another laptop: the setup answers, the control panel's settings (graphics, sponsors, the
// squad roster, colours), and every badge, player photo, sponsor logo and match photo.
// For a new or second laptop, a volunteer's laptop, or a backup.
//
// What never travels, because it belongs to the machine and not the club: OBS's WebSocket
// password and the panel's login token (each laptop has its own), and paths to folders on
// the old laptop (NV Play's output, replays, custom picture folders) — the new laptop
// finds or defaults its own. Passwords and API keys travel only when asked for; importing
// a file without them keeps the keys this laptop already has.
package clubpack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
	"cricketstream/internal/server"
)

// ManifestName is the file that marks a ZIP as a CricketStream club file.
const ManifestName = "cricketstream-club.json"

// Format is this file layout's version.
const Format = 1

// Limits against a damaged or hostile file: whole file, each picture, number of entries.
const (
	MaxFileBytes    = 300 << 20
	maxEntryBytes   = 25 << 20
	maxEntries      = 5000
	maxSettingsSize = 4 << 20
)

// Manifest describes a club file.
type Manifest struct {
	App     string `json:"app"`
	Format  int    `json:"format"`
	Version string `json:"version"`
	Made    string `json:"made"`
	Club    string `json:"club"`
	Secrets bool   `json:"secrets"`
}

// Summary says what an export or import carried.
type Summary struct {
	Club     string         `json:"club"`
	Secrets  bool           `json:"secrets"`
	Settings bool           `json:"settings"`
	Panel    bool           `json:"panel"`
	Pictures map[string]int `json:"pictures"`
	Backup   string         `json:"backup,omitempty"`
}

// configSecrets are config.ini's passwords and keys (travel only when asked for).
var configSecrets = [][2]string{{"API", "playcricket_key"}, {"API", "anthropic_key"},
	{"Stream", "youtube_stream_key"}, {"Auth", "club_password"}}

// configMachine is config.ini's per-laptop part (never travels).
var configMachine = [][2]string{{"OBS", "obs_password"}, {"Auth", "control_token"}, {"Clubhouse", "key"},
	{"Scoring", "pcs_output_folder"}, {"OBS", "replay_folder"}, {"Scoring", "logos_folder"},
	{"Network", "bind_host"}}

// machineDefault is what a machine-specific key becomes on a laptop that has none of its
// own. bind_host must never be blank: blank means every network interface.
var machineDefault = map[[2]string]string{{"Network", "bind_host"}: "127.0.0.1"}

// stateMachine are the panel settings that belong to the laptop.
var stateMachine = []string{"obs_password", "control_token", "obs_host", "obs_port", "pcs_output_folder",
	"replay_folder", "logos_folder", "headshots_folder", "socials_folder", "sponsors_folder",
	"pcs_bridge_url", "agent_host"}

// stateCredentialFiles are YouTube's sign-in files: secrets, beside the app.
var stateCredentialFiles = []string{"yt_token.json", "yt_credentials.json"}

// pictureKinds are the picture folders carried, by the Pictures tab's kinds.
var pictureKinds = []string{"badges", "headshots", "sponsors", "photos"}

func isAny(k string, list []string) bool {
	for _, x := range list {
		if x == k {
			return true
		}
	}
	return false
}

// Export writes the club file to w. version is the app's own (for the manifest).
func Export(w io.Writer, secrets bool, version string) (Summary, error) {
	sum := Summary{Secrets: secrets, Pictures: map[string]int{}}
	zw := zip.NewWriter(w)
	add := func(name string, b []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	}

	// config.ini: everything but the machine's part (and the secrets unless asked).
	cfgPath := assets.Join("config.ini")
	if f, err := ini.Load(cfgPath); err == nil {
		for _, k := range configMachine {
			blank(f, k)
		}
		if !secrets {
			for _, k := range configSecrets {
				blank(f, k)
			}
		}
		sum.Club = f.Get("Club", "name", "")
		if err := add("config.ini", f.Bytes()); err != nil {
			return sum, err
		}
		sum.Settings = true
	}

	// The control panel's settings.
	if _, err := os.Stat(server.StateFile); err == nil {
		st := server.LoadState()
		for k := range st {
			if isAny(k, stateMachine) || (!secrets && isAny(k, server.SecretKeys)) {
				delete(st, k)
			}
		}
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return sum, err
		}
		if err := add("match_state.json", b); err != nil {
			return sum, err
		}
		sum.Panel = true
		if sum.Club == "" {
			sum.Club, _ = st["home_team"].(string)
		}
	}
	if secrets {
		for _, n := range stateCredentialFiles {
			if b, err := os.ReadFile(assets.Join(n)); err == nil {
				if err := add(n, b); err != nil {
					return sum, err
				}
			}
		}
	}

	// The pictures, by kind (match photos with their per-team folders).
	kinds := append([]string{}, pictureKinds...)
	for _, t := range server.LibraryTeams() {
		kinds = append(kinds, "photos:"+t)
	}
	for _, kind := range kinds {
		dir := server.LibraryDir(kind)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !hasExt(e.Name(), server.LibraryExts(kind)) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || len(b) > maxEntryBytes {
				continue
			}
			if err := add(path.Join("pictures", strings.Replace(kind, ":", "/", 1), e.Name()), b); err != nil {
				return sum, err
			}
			sum.Pictures[strings.SplitN(kind, ":", 2)[0]]++
		}
	}

	man, _ := json.MarshalIndent(Manifest{App: "CricketStream", Format: Format, Version: version,
		Made: time.Now().Format(time.RFC3339), Club: sum.Club, Secrets: secrets}, "", "  ")
	if err := add(ManifestName, man); err != nil {
		return sum, err
	}
	return sum, zw.Close()
}

func blank(f *ini.File, k [2]string) {
	if _, ok := f.Lookup(k[0], k[1]); ok {
		f.Set(k[0], k[1], "")
	}
}

func hasExt(name string, exts []string) bool {
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return false
	}
	ext := strings.ToLower(name[i+1:])
	for _, e := range exts {
		if e == ext {
			return true
		}
	}
	return false
}

// ErrNotAClubFile is a file that isn't a CricketStream club file.
var ErrNotAClubFile = errors.New("that isn't a CricketStream club file (made with Settings → Export)")

// Import applies a club file to this laptop. The current config.ini and panel settings
// are copied to backups/ first. Pictures are added (one of the same name is replaced);
// none are deleted.
func Import(data []byte) (Summary, error) {
	sum := Summary{Pictures: map[string]int{}}
	if len(data) > MaxFileBytes {
		return sum, fmt.Errorf("that file is over %d MB", MaxFileBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return sum, ErrNotAClubFile
	}
	if len(zr.File) > maxEntries {
		return sum, fmt.Errorf("that file has too many entries")
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(f *zip.File, limit int64) ([]byte, error) {
		if f.UncompressedSize64 > uint64(limit) {
			return nil, fmt.Errorf("%s is too big", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, limit+1))
		if err == nil && int64(len(b)) > limit {
			err = fmt.Errorf("%s is too big", f.Name)
		}
		return b, err
	}
	mf, ok := files[ManifestName]
	if !ok {
		return sum, ErrNotAClubFile
	}
	mb, err := read(mf, 64<<10)
	var man Manifest
	if err != nil || json.Unmarshal(mb, &man) != nil || man.App != "CricketStream" {
		return sum, ErrNotAClubFile
	}
	if man.Format > Format {
		return sum, fmt.Errorf("that club file was made by a newer CricketStream (%s) — update this one first", man.Version)
	}
	sum.Club, sum.Secrets = man.Club, man.Secrets

	backup, err := backupCurrent()
	if err != nil {
		return sum, fmt.Errorf("couldn't back up the current settings first: %w", err)
	}
	sum.Backup = backup

	// config.ini: the file's answers over this laptop's, keeping what the file doesn't
	// carry (this machine's part, and keys it was exported without).
	if f, ok := files["config.ini"]; ok {
		b, err := read(f, maxSettingsSize)
		if err != nil {
			return sum, err
		}
		in, err := ini.Parse(b)
		if err != nil {
			return sum, fmt.Errorf("the club file's settings can't be read: %w", err)
		}
		cfgPath := assets.Join("config.ini")
		cur, _ := ini.Load(cfgPath)
		for _, k := range append(append([][2]string{}, configMachine...), configSecrets...) {
			v, _ := in.Lookup(k[0], k[1])
			if strings.TrimSpace(v) != "" && !isMachine(k) {
				continue // a key the file carries wins
			}
			if cur != nil {
				if old, ok := cur.Lookup(k[0], k[1]); ok {
					in.Set(k[0], k[1], old)
					continue
				}
			}
			if _, had := in.Lookup(k[0], k[1]); had {
				in.Set(k[0], k[1], machineDefault[k])
			}
		}
		// Pictures now live in this laptop's own folders.
		blank(in, [2]string{"Scoring", "logos_folder"})
		if err := writeAtomic(cfgPath, in.Bytes()); err != nil {
			return sum, err
		}
		sum.Settings = true
	}

	// The panel's settings, merged over this laptop's.
	if f, ok := files["match_state.json"]; ok {
		b, err := read(f, maxSettingsSize)
		if err != nil {
			return sum, err
		}
		var in map[string]any
		if json.Unmarshal(b, &in) != nil {
			return sum, fmt.Errorf("the club file's panel settings can't be read")
		}
		server.UpdateState(func(st server.M) {
			for k, v := range in {
				if isAny(k, stateMachine) {
					continue
				}
				if isAny(k, server.SecretKeys) && (v == nil || v == "" || v == server.SecretSentinel) {
					continue // not carried: keep this laptop's
				}
				st[k] = v
			}
			for _, k := range []string{"logos_folder", "headshots_folder", "socials_folder", "sponsors_folder"} {
				st[k] = ""
			}
		})
		sum.Panel = true
	}
	if man.Secrets {
		for _, n := range stateCredentialFiles {
			if f, ok := files[n]; ok {
				if b, err := read(f, 1<<20); err == nil {
					writeAtomic(assets.Join(n), b)
				}
			}
		}
	}

	// Pictures: only plain picture files, only into their own kind's folder.
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rest, ok := strings.CutPrefix(n, "pictures/")
		if !ok || strings.HasSuffix(n, "/") {
			continue
		}
		parts := strings.Split(rest, "/")
		kind := parts[0]
		switch {
		case len(parts) == 2 && isAny(kind, pictureKinds):
		case len(parts) == 3 && kind == "photos":
			kind = "photos:" + parts[1]
		default:
			continue
		}
		file := parts[len(parts)-1]
		dir := server.LibraryDir(kind)
		if dir == "" || file != filepath.Base(file) || strings.HasPrefix(file, ".") || strings.ContainsAny(file, `\:`) ||
			!hasExt(file, server.LibraryExts(kind)) {
			continue
		}
		b, err := read(files[n], maxEntryBytes)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return sum, err
		}
		if err := writeAtomic(filepath.Join(dir, file), b); err != nil {
			return sum, err
		}
		sum.Pictures[strings.SplitN(kind, ":", 2)[0]]++
	}
	return sum, nil
}

func isMachine(k [2]string) bool {
	for _, m := range configMachine {
		if m == k {
			return true
		}
	}
	return false
}

// backupCurrent copies config.ini and the panel settings to backups/before-import-<time>/
// ("" when there was nothing to keep).
func backupCurrent() (string, error) {
	var have []string
	for _, n := range append([]string{"config.ini", "match_state.json"}, stateCredentialFiles...) {
		if _, err := os.Stat(assets.Join(n)); err == nil {
			have = append(have, n)
		}
	}
	if len(have) == 0 {
		return "", nil
	}
	dir := assets.Join("backups", "before-import-"+time.Now().Format("2006-01-02-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, n := range have {
		b, err := os.ReadFile(assets.Join(n))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func writeAtomic(p string, b []byte) error {
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
