package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/server"
)

// A newer release: a banner on every screen of the window, and on Windows an Update now
// that replaces CricketStream.exe in place and restarts it. Asked once a run, in the
// background; any failure to ask — no internet, a development build, GitHub's rate
// limit — is silent.
//
// The new file is trusted only after three checks: its SHA-256 matches the one GitHub
// publishes for the release asset; Windows accepts its signature AND it's signed by the
// same publisher as the copy running now (a signed CricketStream never "updates" to an
// unsigned or differently-signed file); and its own version resource says the version
// the release does. Until then it's only a download beside the app; the swap renames
// the running file aside (Windows allows that, not overwriting it) and puts it back if
// anything after fails.

// Swappable for tests.
var (
	releasesAPI    = "https://api.github.com/repos/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest"
	releasesPage   = "https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest"
	currentExe     = os.Executable
	updateHTTP     http.RoundTripper // nil: the default; tests trust their own TLS server
	fileSignatureF = fileSignature
	fileVersionF   = fileVersion
	relaunch       = func(exe string) error {
		cmd := exec.Command(exe)
		cmd.Dir = filepath.Dir(exe)
		cmd.Env = append(os.Environ(), UpdatedEnv+"=1")
		return cmd.Start()
	}
)

// UpdatedEnv tells the restarted copy it's the update: wait for the old one to let go of
// the window's port, and say so in the log.
const UpdatedEnv = "CRICKETSTREAM_UPDATED"

const maxDownload = 300 << 20

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>"
}

type updateInfo struct {
	mu       sync.Mutex
	latest   string
	tag      string // its release page is releases/tag/<tag>
	asset    *releaseAsset
	state    string // "", "downloading", "checking", "restarting", "failed"
	progress float64
	err      string
}

// ReleasesEnv points the update check at another releases API — an end-to-end test's
// fake release on this machine (only there may the download be plain HTTP).
const ReleasesEnv = "CRICKETSTREAM_RELEASES_API"

func loopbackURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(p.Hostname())
	return ip != nil && ip.IsLoopback()
}

// assetName is the release file for this computer.
func assetName() string {
	switch {
	case runtime.GOOS == "windows" && runtime.GOARCH == "arm64":
		return "CricketStream-arm64.exe"
	case runtime.GOOS == "windows":
		return "CricketStream.exe"
	}
	return ""
}

type ghRelease struct {
	Tag    string         `json:"tag_name"`
	Draft  bool           `json:"draft"`
	Assets []releaseAsset `json:"assets"`
}

func getRelease(u string, v any) bool {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "CricketStream-app")
	resp, err := (&http.Client{Timeout: 8 * time.Second, Transport: updateHTTP}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v) == nil
}

func (u *updateInfo) check(have string) {
	have = strings.TrimPrefix(have, "v")
	if have == "" {
		return
	}
	// releases/latest is never a pre-release, so a beta asks the recent list too: someone
	// on beta.1 is offered beta.2 (and then the release). A release is only ever offered
	// releases.
	var rels []ghRelease
	if strings.Contains(have, "-") {
		getRelease(strings.TrimSuffix(releasesAPI, "/latest")+"?per_page=20", &rels)
	}
	var latest ghRelease
	if getRelease(releasesAPI, &latest) {
		rels = append(rels, latest)
	}
	var best *ghRelease
	bestV := have
	for i := range rels {
		v := strings.TrimPrefix(rels[i].Tag, "v")
		if !rels[i].Draft && v != "" && server.VersionNewer(v, bestV) {
			best, bestV = &rels[i], v
		}
	}
	if best == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.latest, u.tag = bestV, best.Tag
	for i := range best.Assets {
		if a := best.Assets[i]; a.Name == assetName() && (strings.HasPrefix(a.URL, "https://") || loopbackURL(a.URL)) {
			u.asset = &a
		}
	}
}

// status is nil, or the newer version, where to get it, and how an update is going.
func (u *updateInfo) status() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest == "" {
		return nil
	}
	page := releasesPage
	if u.tag != "" {
		page = strings.TrimSuffix(releasesPage, "/latest") + "/tag/" + url.PathEscape(u.tag)
	}
	return map[string]any{"latest": u.latest, "url": page, "can_update": u.asset != nil,
		"state": u.state, "progress": u.progress, "error": u.err}
}

func (u *updateInfo) set(state string, progress float64, err string) {
	u.mu.Lock()
	u.state, u.progress, u.err = state, progress, err
	u.mu.Unlock()
}

// startUpdate begins Update now (not during match day).
func (a *App) startUpdate() error {
	switch a.currentPhase() {
	case phStarting, phRunning:
		return errors.New("Stop match day first — the update restarts CricketStream.")
	}
	a.upd.mu.Lock()
	asset, latest, busy := a.upd.asset, a.upd.latest, a.upd.state == "downloading" || a.upd.state == "checking" || a.upd.state == "restarting"
	a.upd.mu.Unlock()
	if asset == nil {
		return errors.New("There's no update to install here — download it from the releases page.")
	}
	if busy {
		return nil
	}
	go func() {
		defer crashed()
		if err := a.performUpdate(*asset, latest); err != nil {
			a.upd.set("failed", 0, err.Error())
			fmt.Println("  ✗ Update didn't go ahead: " + err.Error())
		}
	}()
	return nil
}

func (a *App) performUpdate(asset releaseAsset, latest string) error {
	exe, err := currentExe()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	dl := exe + ".download"
	defer os.Remove(dl)
	fmt.Printf("  Downloading CricketStream v%s...\n", latest)
	a.upd.set("downloading", 0, "")
	sum, err := download(asset, dl, func(p float64) { a.upd.set("downloading", p, "") })
	if err != nil {
		return fmt.Errorf("the download failed (%w) — try again, or download it from the releases page", err)
	}

	a.upd.set("checking", 1, "")
	if want, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok && !strings.EqualFold(want, sum) {
		return errors.New("the download doesn't match the release's checksum — it was damaged or isn't the real file")
	}
	curSigner, curErr := fileSignatureF(exe)
	newSigner, newErr := fileSignatureF(dl)
	switch {
	case curErr == nil && newErr != nil:
		return fmt.Errorf("the download isn't validly signed (%v) — this copy is, so it won't be replaced by it", newErr)
	case curErr == nil && newSigner != curSigner:
		return fmt.Errorf("the download is signed by %q, not %q like this copy", newSigner, curSigner)
	case curErr != nil && newErr != nil && asset.Digest == "":
		return errors.New("the download has neither a signature nor a published checksum to check it against")
	}
	if v := strings.TrimPrefix(fileVersionF(dl), "v"); v != latest {
		return fmt.Errorf("the download says it's version %q, not %s", v, latest)
	}

	// The swap: the running file aside, the new one in its place, then start it.
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("couldn't move this copy aside (%w) — is the folder read-only?", err)
	}
	if err := os.Rename(dl, exe); err != nil {
		os.Rename(old, exe)
		return fmt.Errorf("couldn't put the new copy in place: %w", err)
	}
	a.upd.set("restarting", 1, "")
	fmt.Printf("  ✓ Checked (%s) — restarting as v%s\n", map[bool]string{true: "signed by " + newSigner, false: "checksum matches"}[newErr == nil], latest)
	if err := relaunch(exe); err != nil {
		os.Remove(exe)
		os.Rename(old, exe)
		return fmt.Errorf("the new copy wouldn't start (%w) — this one is back as it was", err)
	}
	a.quit()
	return nil
}

// download fetches the asset to path, reporting progress 0..1; its SHA-256 in hex.
func download(asset releaseAsset, path string, progress func(float64)) (string, error) {
	req, _ := http.NewRequest("GET", asset.URL, nil)
	req.Header.Set("User-Agent", "CricketStream-app")
	resp, err := (&http.Client{Timeout: 10 * time.Minute, Transport: updateHTTP}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	total := asset.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	var got int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			got += int64(n)
			if got > maxDownload {
				return "", errors.New("the download is far bigger than CricketStream")
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return "", err
			}
			h.Write(buf[:n])
			if total > 0 {
				progress(float64(got) / float64(total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	if asset.Size > 0 && got != asset.Size {
		return "", fmt.Errorf("got %d bytes of %d", got, asset.Size)
	}
	return hex.EncodeToString(h.Sum(nil)), f.Close()
}

// cleanUpAfterUpdate removes the previous copy an update left aside (it may still be
// closing, so a few tries) and any half-finished download.
func cleanUpAfterUpdate() {
	exe, err := currentExe()
	if err != nil {
		return
	}
	os.Remove(exe + ".download")
	go func() {
		for i := 0; i < 20; i++ {
			if err := os.Remove(exe + ".old"); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}
