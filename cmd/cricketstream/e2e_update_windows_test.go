//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildVersion builds the app as `version`, with that version stamped into its Windows
// resources the way the release workflow does (the update checks them).
func buildVersion(t *testing.T, version, out string) {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	build := exec.Command(gobin, "build", "-ldflags", "-X main.version="+version, "-o", out, ".")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
	patch := exec.Command(gobin, "run", "github.com/tc-hib/go-winres@v0.3.3", "patch", "--in", "winres/winres.json",
		"--no-backup", "--product-version", version, "--file-version", strings.SplitN(version, "-", 2)[0]+".0", out)
	if b, err := patch.CombinedOutput(); err != nil {
		t.Skipf("couldn't stamp the version (go-winres): %v\n%s", err, b)
	}
}

// The whole Update now, with real programs: v3.0.0 running, v3.0.1 published (on this
// machine, unsigned but with its checksum), the update downloads, checks, swaps the file,
// and the new copy comes up on the same window port while the old one closes.
func TestE2EUpdateNowReplacesTheRunningApp(t *testing.T) {
	needBinary(t)
	a := newApp(t)
	v1 := filepath.Join(a.dir, "CricketStream.exe")
	v2 := filepath.Join(t.TempDir(), "CricketStream-3.0.1.exe")
	buildVersion(t, "3.0.0", v1)
	buildVersion(t, "3.0.1", v2)
	newBytes, _ := os.ReadFile(v2)
	sum := sha256.Sum256(newBytes)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rel := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			w.Write(newBytes)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v3.0.1","assets":[{"name":"CricketStream.exe","browser_download_url":%q,"size":%d,"digest":"sha256:%s"}]}`,
			rel+"/download", len(newBytes), hex.EncodeToString(sum[:]))
	})}
	go srv.Serve(ln)
	defer srv.Close()

	port := freePort(t)
	env := append([]string{}, a.env...)
	for i, kv := range env {
		if strings.HasPrefix(kv, "CRICKETSTREAM_APP_PORT=") {
			env[i] = fmt.Sprintf("CRICKETSTREAM_APP_PORT=%d", port)
		}
	}
	env = append(env, "CRICKETSTREAM_RELEASES_API="+rel+"/latest")
	cmd := exec.Command(v1)
	cmd.Dir, cmd.Env = a.dir, env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan error, 1)
	go func() { oldDone <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })

	pg := &appPage{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	waitHTTP(t, pg.base+"/", 20*time.Second)
	pg.key = keyRx.FindStringSubmatch(get(t, pg.base+"/").body)[1]
	pg.until("the update offered", 20*time.Second, func(st map[string]any) bool {
		u, _ := st["update"].(map[string]any)
		return u != nil && u["can_update"] == true
	})
	if r := pg.api("POST", "update", map[string]any{}); r.status != 200 {
		t.Fatalf("update: %d %s", r.status, r.body)
	}
	select {
	case <-oldDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the old copy didn't close")
	}

	// The new copy on the same port, as v3.0.1.
	deadline := time.Now().Add(30 * time.Second)
	var version string
	for time.Now().Before(deadline) {
		if r, err := (&http.Client{Timeout: time.Second}).Get(pg.base + "/"); err == nil {
			r.Body.Close()
			pg.key = ""
			page := get(t, pg.base+"/")
			if k := keyRx.FindStringSubmatch(page.body); k != nil {
				pg.key = k[1]
				version, _ = pg.status()["version"].(string)
				if version == "3.0.1" {
					break
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if version != "3.0.1" {
		t.Fatalf("after the update the app reports %q", version)
	}
	t.Cleanup(func() { // if the test stops early, don't leave the updated copy running
		req, _ := http.NewRequest("POST", pg.base+"/api/close", strings.NewReader("{}"))
		req.Header.Set("X-App-Key", pg.key)
		req.Header.Set("Origin", pg.base)
		if r, err := (&http.Client{Timeout: 3 * time.Second}).Do(req); err == nil {
			r.Body.Close()
		}
	})
	if got, _ := os.ReadFile(v1); string(got) != string(newBytes) {
		t.Error("CricketStream.exe wasn't replaced")
	}
	st := pg.status()
	var lines []string
	for _, l := range st["log"].([]any) {
		lines = append(lines, l.(map[string]any)["t"].(string))
	}
	mustContain(t, strings.Join(lines, "\n"), "Updated to v3.0.1")
	// The copy left aside is tidied away once it has closed.
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(v1 + ".old"); os.IsNotExist(err) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if _, err := os.Stat(v1 + ".old"); err == nil {
		t.Error("CricketStream.exe.old was never removed")
	}
	pg.api("POST", "close", map[string]any{})
	waitClosed(t, port, 15*time.Second)
}
