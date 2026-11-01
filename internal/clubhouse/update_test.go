package clubhouse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
	sa, sh, so, sv, sd := updReleasesAPI, updHTTP, updOS, updVersionOf, assets.Dir()
	t.Cleanup(func() { updReleasesAPI, updHTTP, updOS, updVersionOf = sa, sh, so, sv; assets.SetDir(sd) })
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
