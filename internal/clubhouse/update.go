package clubhouse

// The screen keeps itself up to date: each time it starts at boot (--kiosk), before the
// page opens, it asks GitHub for a newer release, and if there is one it downloads this
// Pi's file, checks it, puts it in ~/cricketstream (which both start scripts prefer —
// clubhouse-start.sh and install-pi.sh's) and exits; the start script's loop runs the new
// one three seconds later. So a club never copies a new binary onto the Pi by hand: a
// restart after a release is enough.
//
// The same rules as the app's own update: releases only, never a branch; a beta follows
// betas, a release only releases; the download must match the SHA-256 GitHub publishes for
// it, and the new file must run and say the version the release does. With no answer from
// GitHub it asks again for up to updNetWait (the wifi joining after a power-on). Any failure
// — no internet, GitHub's rate limit, a damaged download — leaves the copy it has, says so
// in clubhouse.log, and opens the screen as usual. [Screen] auto_update = no in
// clubhouse.ini turns it off.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
)

// Swappable for tests.
var (
	updReleasesAPI = "https://api.github.com/repos/BridestoweBelstoneCC/Cricket-Live-Stream/releases?per_page=20"
	updHTTP        http.RoundTripper // nil: the default
	updOS          = runtime.GOOS
	// updVersionOf runs a downloaded file and returns what `version` prints.
	updVersionOf = func(path string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, path, "version").Output()
		return strings.TrimSpace(string(out))
	}
	// How long a check at boot keeps asking while GitHub can't be reached (the wifi may
	// still be joining), and how often. The screen opens after it either way.
	updNetWait  = 90 * time.Second
	updNetRetry = 5 * time.Second
)

const updMaxDownload = 300 << 20

// errNoAnswer: GitHub couldn't be reached at all, the one failure worth asking again about
// (an answer, even a refusal like its rate limit, isn't going to change in a minute).
var errNoAnswer = errors.New("no answer from GitHub — is the internet up?")

type updRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"` // "sha256:<hex>"
	} `json:"assets"`
}

// updAsset is this computer's release file.
func updAsset() string {
	if runtime.GOARCH == "arm64" {
		return "CricketStream-linux-arm64"
	}
	return "CricketStream-linux"
}

// autoUpdateOn: [Screen] auto_update in clubhouse.ini, on unless "no".
func autoUpdateOn() bool {
	f, err := ini.Load(ScreenFile())
	if err != nil {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(f.Get("Screen", "auto_update", "yes")))
	return v != "no" && v != "false" && v != "off" && v != "0"
}

// SelfUpdate installs a newer release, if there is one. True when it did: the caller
// exits so the start script runs the new copy. have is this copy's version ("" for a
// development build, which never updates); newer compares two versions.
func SelfUpdate(have string, newer func(a, b string) bool) bool {
	have = strings.TrimPrefix(strings.TrimSpace(have), "v")
	if updOS != "linux" || have == "" || newer == nil || !autoUpdateOn() {
		return false
	}
	// A Pi switched on at the plug starts the screen before its wifi has joined: the one
	// try this used to make found no network, said nothing, and the screen stayed on the
	// version it had (seen on the club's Pi, 2026-11-16). So until GitHub answers, ask
	// again for a while; once it has answered, its answer stands.
	fmt.Printf("  Checking for a newer CricketStream (this is v%s)...\n", have)
	var rel, url, digest string
	var err error
	for give := time.Now().Add(updNetWait); ; {
		if rel, url, digest, err = pickUpdate(have, newer); err != errNoAnswer || time.Now().After(give) {
			break
		}
		time.Sleep(updNetRetry)
	}
	switch {
	case err != nil:
		fmt.Printf("  ⚠ Couldn't check for an update (%v) — carrying on with v%s\n", err, have)
		return false
	case rel == "":
		fmt.Printf("  ✓ Up to date\n")
		return false
	}
	fmt.Printf("  A newer CricketStream is out (v%s) — updating the screen...\n", rel)
	if err := installUpdate(url, digest, rel); err != nil {
		fmt.Printf("  ⚠ The update didn't go ahead (%v) — carrying on with v%s\n", err, have)
		return false
	}
	fmt.Printf("  ✓ Updated to v%s — restarting\n", rel)
	return true
}

// pickUpdate is the newest release after have with this computer's file: its version,
// download address and SHA-256 ("" when there's none). err is set when GitHub didn't give
// a usable answer (no network yet, its rate limit, a damaged reply).
func pickUpdate(have string, newer func(a, b string) bool) (version, url, digest string, err error) {
	req, _ := http.NewRequest("GET", updReleasesAPI, nil)
	req.Header.Set("User-Agent", "CricketStream-clubhouse")
	resp, err := (&http.Client{Timeout: 8 * time.Second, Transport: updHTTP}).Do(req)
	if err != nil {
		return "", "", "", errNoAnswer
	}
	defer resp.Body.Close()
	var rels []updRelease
	if resp.StatusCode != 200 {
		return "", "", "", fmt.Errorf("GitHub said HTTP %d", resp.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels) != nil {
		return "", "", "", errors.New("GitHub's reply couldn't be read")
	}
	beta := strings.Contains(have, "-")
	best := have
	for _, r := range rels {
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || v == "" || (r.Prerelease && !beta) || !newer(v, best) {
			continue
		}
		for _, a := range r.Assets {
			sum, ok := strings.CutPrefix(a.Digest, "sha256:")
			if a.Name == updAsset() && strings.HasPrefix(a.URL, "https://") && ok && sum != "" {
				best, version, url, digest = v, v, a.URL, sum
			}
		}
	}
	return version, url, digest, nil
}

// installUpdate downloads, checks and puts the new copy in place.
func installUpdate(url, digest, version string) error {
	target := assets.Join("cricketstream")
	tmp := target + ".new"
	defer os.Remove(tmp)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "CricketStream-clubhouse")
	resp, err := (&http.Client{Timeout: 10 * time.Minute, Transport: updHTTP}).Do(req)
	if err != nil {
		return fmt.Errorf("the download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("the download failed: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, updMaxDownload+1))
	f.Close()
	switch {
	case err != nil:
		return fmt.Errorf("the download failed: %w", err)
	case n > updMaxDownload:
		return errors.New("the download is far bigger than CricketStream")
	case !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), digest):
		return errors.New("the download doesn't match the release's checksum")
	}
	os.Chmod(tmp, 0o755)
	if got := updVersionOf(tmp); got != "CricketStream v"+version {
		return fmt.Errorf("the download says %q, not v%s", got, version)
	}
	return os.Rename(tmp, target)
}
