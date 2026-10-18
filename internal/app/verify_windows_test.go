//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Windows' own signature check, on real files: a signed program installed on this
// machine names its signer; this unsigned test build is refused.
func TestFileSignatureOnRealFiles(t *testing.T) {
	self, _ := os.Executable()
	if s, err := fileSignature(self); err == nil {
		t.Errorf("the unsigned test build passed as signed by %q", s)
	}
	var signed string
	for _, p := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Git\cmd\git.exe`,
		`C:\Program Files\Oracle\VirtualBox\VBoxManage.exe`,
		`C:\Program Files\Mozilla Firefox\firefox.exe`,
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
	} {
		if _, err := os.Stat(p); err == nil {
			signed = p
			break
		}
	}
	if signed == "" {
		t.Skip("no known signed program installed to check against")
	}
	who, err := fileSignature(signed)
	if err != nil || who == "" {
		t.Fatalf("%s: %q, %v", signed, who, err)
	}
	t.Logf("%s is signed by %s", filepath.Base(signed), who)
	if strings.Contains(strings.ToLower(who), "root") {
		t.Errorf("reported the root certificate, not the signer: %s", who)
	}
}

// The version written into a build's resources is read back.
func TestFileVersionOfABuild(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	out := filepath.Join(t.TempDir(), "cs.exe")
	cmd := exec.Command(gobin, "build", "-o", out, "../../cmd/cricketstream")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	if v := fileVersion(out); v != "dev" {
		t.Errorf("ProductVersion %q, want dev (winres.json's)", v)
	}
	if v := fileVersion(filepath.Join(t.TempDir(), "missing.exe")); v != "" {
		t.Errorf("missing file: %q", v)
	}
}
