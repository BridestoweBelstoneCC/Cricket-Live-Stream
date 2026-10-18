package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fake release: the latest tag, and the build whose bytes say who signed it and what
// version it is (the stand-ins for Windows' checks read them).
type fakeRelease struct {
	srv          *httptest.Server
	tag, body    string
	publishedSum string
}

func newFakeRelease(t *testing.T, tag, body string) *fakeRelease {
	t.Helper()
	f := &fakeRelease{tag: tag, body: body}
	sum := sha256.Sum256([]byte(body))
	f.publishedSum = "sha256:" + hex.EncodeToString(sum[:])
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			fmt.Fprint(w, f.body)
			return
		}
		digest := ""
		if f.publishedSum != "" {
			digest = `,"digest":"` + f.publishedSum + `"`
		}
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q,"size":%d%s}]}`,
			f.tag, assetName(), f.srv.URL+"/download", len(f.body), digest)
	}))
	t.Cleanup(f.srv.Close)
	saved, savedT := releasesAPI, updateHTTP
	releasesAPI, updateHTTP = f.srv.URL+"/latest", f.srv.Client().Transport
	t.Cleanup(func() { releasesAPI, updateHTTP = saved, savedT })
	return f
}

// A build's bytes: "SIGNED:<who>|v=<version>" or "UNSIGNED|v=<version>".
func stubChecks(t *testing.T) {
	t.Helper()
	sf, sv := fileSignatureF, fileVersionF
	t.Cleanup(func() { fileSignatureF, fileVersionF = sf, sv })
	fileSignatureF = func(p string) (string, error) {
		b, _ := os.ReadFile(p)
		if who, ok := strings.CutPrefix(strings.SplitN(string(b), "|", 2)[0], "SIGNED:"); ok {
			return who, nil
		}
		return "", errors.New("not signed")
	}
	fileVersionF = func(p string) string {
		b, _ := os.ReadFile(p)
		if i := strings.Index(string(b), "|v="); i >= 0 {
			return string(b)[i+3:]
		}
		return ""
	}
}

// installed is this copy, as a file in a folder of its own.
func installed(t *testing.T, ta *testApp, body string) (string, *[]string) {
	t.Helper()
	exe := filepath.Join(ta.dir, "CricketStream.exe")
	os.WriteFile(exe, []byte(body), 0o755)
	se, sr := currentExe, relaunch
	t.Cleanup(func() { currentExe, relaunch = se, sr })
	currentExe = func() (string, error) { return exe, nil }
	calls := &[]string{}
	relaunch = func(p string) error { *calls = append(*calls, p); return nil }
	return exe, calls
}

func (ta *testApp) updateTo(t *testing.T, version string) map[string]any {
	t.Helper()
	ta.upd = updateInfo{}
	ta.upd.check(version)
	if st := ta.upd.status(); st == nil || st["can_update"] != true {
		t.Fatalf("no update offered: %v", st)
	}
	if r := ta.api("POST", "update", M{}); r.status != 200 {
		t.Fatalf("update: %d %s", r.status, r.body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := ta.upd.status()
		if st["state"] == "restarting" || st["state"] == "failed" || time.Now().After(deadline) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdateReplacesTheAppAndRestarts(t *testing.T) {
	if assetName() == "" {
		t.Skip("updating in place is Windows-only")
	}
	ta := newTestApp(t)
	stubChecks(t)
	newFakeRelease(t, "v3.1.0", "SIGNED:SignPath Foundation|v=3.1.0")
	exe, relaunched := installed(t, ta, "SIGNED:SignPath Foundation|v=3.0.0")
	st := ta.updateTo(t, "3.0.0")
	if st["state"] != "restarting" {
		t.Fatalf("state %v: %v", st["state"], st["error"])
	}
	if b, _ := os.ReadFile(exe); string(b) != "SIGNED:SignPath Foundation|v=3.1.0" {
		t.Errorf("not replaced: %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "SIGNED:SignPath Foundation|v=3.0.0" {
		t.Errorf("old copy not kept aside: %q", b)
	}
	if len(*relaunched) != 1 || (*relaunched)[0] != exe {
		t.Errorf("relaunched %v", *relaunched)
	}
	select {
	case <-ta.done:
	case <-time.After(3 * time.Second):
		t.Error("the old copy didn't close")
	}
	if _, err := os.Stat(exe + ".download"); err == nil {
		t.Error("download left behind")
	}
}

// Every way a download can be wrong leaves this copy exactly as it was.
func TestUpdateRefusesABadDownload(t *testing.T) {
	if assetName() == "" {
		t.Skip("updating in place is Windows-only")
	}
	for _, c := range []struct {
		name, current, download, why string
		noSum, badSum                bool
	}{
		{name: "another signer", current: "SIGNED:SignPath Foundation|v=3.0.0", download: "SIGNED:Someone Else|v=3.1.0", why: `not "SignPath Foundation"`},
		{name: "unsigned replacing signed", current: "SIGNED:SignPath Foundation|v=3.0.0", download: "UNSIGNED|v=3.1.0", why: "isn't validly signed"},
		{name: "checksum mismatch", current: "UNSIGNED|v=3.0.0", download: "UNSIGNED|v=3.1.0", badSum: true, why: "checksum"},
		{name: "no signature, no checksum", current: "UNSIGNED|v=3.0.0", download: "UNSIGNED|v=3.1.0", noSum: true, why: "neither a signature nor"},
		{name: "wrong version inside", current: "SIGNED:SignPath Foundation|v=3.0.0", download: "SIGNED:SignPath Foundation|v=2.9.0", why: "says it's version"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ta := newTestApp(t)
			stubChecks(t)
			f := newFakeRelease(t, "v3.1.0", c.download)
			if c.noSum {
				f.publishedSum = ""
			}
			if c.badSum {
				f.publishedSum = "sha256:" + strings.Repeat("0", 64)
			}
			exe, relaunched := installed(t, ta, c.current)
			st := ta.updateTo(t, "3.0.0")
			if st["state"] != "failed" || !strings.Contains(fmt.Sprint(st["error"]), c.why) {
				t.Errorf("state %v, error %q", st["state"], st["error"])
			}
			if b, _ := os.ReadFile(exe); string(b) != c.current {
				t.Errorf("this copy changed: %q", b)
			}
			for _, leftover := range []string{exe + ".old", exe + ".download"} {
				if _, err := os.Stat(leftover); err == nil {
					t.Errorf("left %s", filepath.Base(leftover))
				}
			}
			if len(*relaunched) != 0 {
				t.Error("restarted after a refused download")
			}
		})
	}
}

// An unsigned beta updates to an unsigned one when the published checksum matches; the
// new copy failing to start puts the old one back.
func TestUpdateUnsignedBetaAndRollback(t *testing.T) {
	if assetName() == "" {
		t.Skip("updating in place is Windows-only")
	}
	ta := newTestApp(t)
	stubChecks(t)
	newFakeRelease(t, "v3.0.0-beta.2", "UNSIGNED|v=3.0.0-beta.2")
	exe, _ := installed(t, ta, "UNSIGNED|v=3.0.0-beta.1")
	relaunch = func(string) error { return errors.New("blocked") }
	st := ta.updateTo(t, "3.0.0-beta.1")
	if st["state"] != "failed" || !strings.Contains(fmt.Sprint(st["error"]), "back as it was") {
		t.Fatalf("state %v %v", st["state"], st["error"])
	}
	if b, _ := os.ReadFile(exe); string(b) != "UNSIGNED|v=3.0.0-beta.1" {
		t.Errorf("not rolled back: %q", b)
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error(".old left after the rollback")
	}

	// With the restart working, it goes through on the checksum alone.
	relaunch = func(string) error { return nil }
	if st := ta.updateTo(t, "3.0.0-beta.1"); st["state"] != "restarting" {
		t.Errorf("unsigned beta update: %v %v", st["state"], st["error"])
	}
}

func TestNoUpdateDuringMatchDay(t *testing.T) {
	ta := newTestApp(t)
	ta.mu.Lock()
	ta.phase = phRunning
	ta.mu.Unlock()
	if r := ta.api("POST", "update", M{}); r.status != 409 || !strings.Contains(r.body, "Stop match day") {
		t.Errorf("%d %s", r.status, r.body)
	}
}

// releases/latest is never a pre-release: a beta finds the next beta in the list, a
// release never offers one, and drafts are never offered.
func TestBetaIsOfferedTheNextBeta(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/latest") {
			fmt.Fprint(w, `{"tag_name":"v2.12","assets":[]}`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v3.0.0-beta.3","draft":true,"assets":[]},`+
			`{"tag_name":"v3.0.0-beta.2","assets":[{"name":"CricketStream.exe","browser_download_url":"https://example.invalid/x"}]},`+
			`{"tag_name":"v3.0.0-beta.1","assets":[]},{"tag_name":"v2.12","assets":[]}]`)
	}))
	t.Cleanup(srv.Close)
	saved, savedT := releasesAPI, updateHTTP
	releasesAPI, updateHTTP = srv.URL+"/repos/x/y/releases/latest", srv.Client().Transport
	t.Cleanup(func() { releasesAPI, updateHTTP = saved, savedT })

	var u updateInfo
	u.check("3.0.0-beta.1")
	st := u.status()
	if st == nil || st["latest"] != "3.0.0-beta.2" {
		t.Fatalf("beta.1 was offered %v", st)
	}
	if !strings.HasSuffix(st["url"].(string), "/releases/tag/v3.0.0-beta.2") {
		t.Errorf("page %v", st["url"])
	}
	var rel updateInfo
	rel.check("2.11.0")
	if st := rel.status(); st == nil || st["latest"] != "2.12" {
		t.Errorf("a release was offered %v", st)
	}
	var cur updateInfo
	cur.check("3.0.0-beta.2")
	if st := cur.status(); st != nil {
		t.Errorf("the newest beta was offered %v", st)
	}
}
