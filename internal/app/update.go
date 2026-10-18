package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/server"
)

// A newer release, shown as a banner on every screen of the window (the console only said
// so when match day started, and only in passing). Asked once a run, in the background;
// any failure — no internet, a development build, GitHub's rate limit — is silent.

// Swappable for tests.
var (
	releasesAPI  = "https://api.github.com/repos/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest"
	releasesPage = "https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest"
)

type updateInfo struct {
	mu     sync.Mutex
	latest string
}

func (u *updateInfo) check(have string) {
	have = strings.TrimPrefix(have, "v")
	if have == "" {
		return
	}
	req, _ := http.NewRequest("GET", releasesAPI, nil)
	req.Header.Set("User-Agent", "CricketStream-app")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var d struct {
		Tag string `json:"tag_name"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&d) != nil {
		return
	}
	latest := strings.TrimPrefix(d.Tag, "v")
	if latest != "" && server.VersionNewer(latest, have) {
		u.mu.Lock()
		u.latest = latest
		u.mu.Unlock()
	}
}

// status is nil, or the newer version and where to get it.
func (u *updateInfo) status() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest == "" {
		return nil
	}
	return map[string]any{"latest": u.latest, "url": releasesPage}
}
