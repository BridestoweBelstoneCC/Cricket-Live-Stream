package clubhouse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/assets"
)

// A fake GitHub: a list of releases, each with this computer's file.
type fakeRelease struct {
	tag        string
	prerelease bool
	body       []byte
	digest     string // "" = the real one
}

func fakeGitHub(t *testing.T, rels ...fakeRelease) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases" {
			var out []map[string]any
			for _, rl := range rels {
				sum := sha256.Sum256(rl.body)
				d := "sha256:" + hex.EncodeToString(sum[:])
				if rl.digest != "" {
					d = rl.digest
				}
				out = append(out, map[string]any{"tag_name": rl.tag, "prerelease": rl.prerelease,
					"assets": []map[string]any{{"name": updAsset(), "browser_download_url": srv.URL + "/dl/" + rl.tag, "digest": d}}})
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		for _, rl := range rels {
			if r.URL.Path == "/dl/"+rl.tag {
				w.Write(rl.body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	sa, sh, so, sv, sd, sw, sr := updReleasesAPI, updHTTP, updOS, updVersionOf, assets.Dir(), updNetWait, updNetRetry
	t.Cleanup(func() {
		updReleasesAPI, updHTTP, updOS, updVersionOf, updNetWait, updNetRetry = sa, sh, so, sv, sw, sr
		assets.SetDir(sd)
	})
	updNetWait, updNetRetry = 2*time.Second, 10*time.Millisecond
	assets.SetDir(dir)
	updReleasesAPI, updHTTP, updOS = srv.URL+"/releases", srv.Client().Transport, "linux"
	// The "program" is its own version line, so running it is reading it.
	updVersionOf = func(path string) string { b, _ := os.ReadFile(path); return string(b) }
}

// Good enough for these tags: "3.0.0-beta.10" isn't among them.
func newerForTest(a, b string) bool {
	rank := map[string]int{"3.0.0-beta.3": 3, "3.0.0-beta.4": 4, "3.0.0": 10, "3.0.1": 11}
	return rank[a] > rank[b]
}

func installed(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join(assets.Dir(), "cricketstream"))
	if err != nil {
		return ""
	}
	return string(b)
}

func TestScreenUpdatesToANewerRelease(t *testing.T) {
	fakeGitHub(t, fakeRelease{tag: "v3.0.0-beta.4", prerelease: true, body: []byte("CricketStream v3.0.0-beta.4")},
		fakeRelease{tag: "v3.0.0-beta.3", prerelease: true, body: []byte("CricketStream v3.0.0-beta.3")})
	if !SelfUpdate("3.0.0-beta.3", newerForTest) {
		t.Fatal("no update")
	}
	if got := installed(t); got != "CricketStream v3.0.0-beta.4" {
		t.Errorf("installed %q", got)
	}
	if fi, _ := os.Stat(filepath.Join(assets.Dir(), "cricketstream")); fi != nil && fi.Mode().Perm()&0o100 == 0 && updOS == "linux" && os.PathSeparator == '/' {
		t.Error("not executable")
	}
	if _, err := os.Stat(filepath.Join(assets.Dir(), "cricketstream.new")); !os.IsNotExist(err) {
		t.Error("the download was left behind")
	}
}

// offline: a network that isn't there yet for the first n requests (the wifi still
// joining after the Pi was switched on at the plug).
type offline struct {
	n    int
	next http.RoundTripper
	mu   sync.Mutex
	seen int
}

func (o *offline) RoundTrip(r *http.Request) (*http.Response, error) {
	o.mu.Lock()
	o.seen++
	down := o.seen <= o.n
	o.mu.Unlock()
	if down {
		return nil, errors.New("dial tcp: lookup api.github.com: no such host")
	}
	return o.next.RoundTrip(r)
}

// Found on the club's Pi: switched on at the plug, it checked once before the wifi was up
// and stayed on the old version without a word.
func TestScreenUpdateWaitsForTheNetwork(t *testing.T) {
	fakeGitHub(t, fakeRelease{tag: "v3.0.0-beta.4", prerelease: true, body: []byte("CricketStream v3.0.0-beta.4")})
	off := &offline{n: 3, next: updHTTP}
	updHTTP = off
	if !SelfUpdate("3.0.0-beta.3", newerForTest) {
		t.Fatal("gave up before the network came up")
	}
	if got := installed(t); got != "CricketStream v3.0.0-beta.4" {
		t.Errorf("installed %q", got)
	}

	// No network at all: it gives up in the end, and the screen opens as it was.
	fakeGitHub(t, fakeRelease{tag: "v3.0.0-beta.4", prerelease: true, body: []byte("CricketStream v3.0.0-beta.4")})
	updNetWait = 100 * time.Millisecond
	updHTTP = &offline{n: 1 << 30, next: updHTTP}
	start := time.Now()
	if SelfUpdate("3.0.0-beta.3", newerForTest) {
		t.Error("updated with no network")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("waited %v", time.Since(start))
	}
}

// An answer isn't asked again: GitHub's rate limit won't lift in a minute.
func TestScreenUpdateTakesARefusalAsTheAnswer(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	fakeGitHub(t)
	updReleasesAPI, updHTTP = srv.URL+"/releases", srv.Client().Transport
	if SelfUpdate("3.0.0-beta.3", newerForTest) {
		t.Error("updated")
	}
	if calls != 1 {
		t.Errorf("asked %d times", calls)
	}
}

func TestScreenRefusesABadUpdate(t *testing.T) {
	for name, rel := range map[string]fakeRelease{
		"damaged":       {tag: "v3.0.0-beta.4", prerelease: true, body: []byte("CricketStream v3.0.0-beta.4"), digest: "sha256:00ff"},
		"wrong version": {tag: "v3.0.0-beta.4", prerelease: true, body: []byte("CricketStream v2.0")},
	} {
		t.Run(name, func(t *testing.T) {
			fakeGitHub(t, rel)
			if SelfUpdate("3.0.0-beta.3", newerForTest) {
				t.Error("installed it")
			}
			if installed(t) != "" {
				t.Error("replaced the program")
			}
		})
	}
}

func TestScreenUpdateRules(t *testing.T) {
	beta := fakeRelease{tag: "v3.0.0-beta.4", prerelease: true, body: []byte("CricketStream v3.0.0-beta.4")}
	fakeGitHub(t, beta)
	if SelfUpdate("3.0.0", newerForTest) {
		t.Error("a release moved onto a beta")
	}
	if SelfUpdate("", newerForTest) {
		t.Error("a development build updated")
	}
	if SelfUpdate("3.0.0-beta.4", newerForTest) {
		t.Error("updated to the same version")
	}
	os.WriteFile(ScreenFile(), []byte("[Screen]\nauto_update = no\n"), 0o644)
	if SelfUpdate("3.0.0-beta.3", newerForTest) {
		t.Error("updated with auto_update = no")
	}
	os.Remove(ScreenFile())
	updOS = "windows"
	if SelfUpdate("3.0.0-beta.3", newerForTest) {
		t.Error("updated off Linux")
	}
}
