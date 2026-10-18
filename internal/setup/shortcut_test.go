package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeShortcut(t *testing.T, where string, err error) *[]string {
	t.Helper()
	var calls []string
	saved := runShortcutScript
	runShortcutScript = func(target string) (string, error) { calls = append(calls, target); return where, err }
	t.Cleanup(func() { runShortcutScript = saved })
	return &calls
}

func TestSaveMakesTheDesktopShortcutWhenAsked(t *testing.T) {
	appDir(t)
	calls := fakeShortcut(t, `C:\Users\x\Desktop\CricketStream.lnk`, nil)
	body, _ := json.Marshal(map[string]any{"role": "scorer", "desktop_shortcut": true})
	code, out, _ := Save(body)
	if code != 200 || out["shortcut"] != `C:\Users\x\Desktop\CricketStream.lnk` {
		t.Fatal(code, out)
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if len(*calls) != 1 || (*calls)[0] != exe {
		t.Fatalf("shortcut to %v, want this app %s", *calls, exe)
	}
	body, _ = json.Marshal(map[string]any{"role": "scorer"})
	Save(body)
	if len(*calls) != 1 {
		t.Error("made a shortcut nobody asked for")
	}
}

func TestAShortcutThatFailsNeverFailsTheSave(t *testing.T) {
	appDir(t)
	fakeShortcut(t, "", errors.New("no desktop"))
	body, _ := json.Marshal(map[string]any{"role": "scorer", "desktop_shortcut": true})
	code, out, res := Save(body)
	if code != 200 || res == nil || out["shortcut_error"] != "no desktop" {
		t.Fatal(code, out)
	}
}

func TestLastLine(t *testing.T) {
	for in, want := range map[string]string{"a\r\nb\r\n\r\n": "b", "": "", "only": "only", "x\n  y  \n": "y"} {
		if got := lastLine(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// The real script, on Windows, into a temp folder standing in for the desktop (never the
// real one): the shortcut exists and points at the app.
func TestShortcutScriptForReal(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	desk := t.TempDir()
	t.Setenv("CS_DESKTOP", desk)
	target := filepath.Join(t.TempDir(), "Club Folder (2026)", "CricketStream.exe")
	os.MkdirAll(filepath.Dir(target), 0o755)
	os.WriteFile(target, []byte("MZ"), 0o755)
	where, err := runShortcutScript(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(where, filepath.Join(desk, "CricketStream.lnk")) {
		t.Errorf("made at %q", where)
	}
	b, err := os.ReadFile(filepath.Join(desk, "CricketStream.lnk"))
	if err != nil {
		t.Fatal(err)
	}
	// A .lnk stores its target as UTF-16 or ANSI; the folder name is in there either way.
	if !strings.Contains(string(b), "Club Folder (2026)") && !strings.Contains(utf16le(b), "Club Folder (2026)") {
		t.Error("shortcut doesn't point at the app")
	}
}

func utf16le(b []byte) string {
	var r []rune
	for i := 0; i+1 < len(b); i += 2 {
		r = append(r, rune(b[i])|rune(b[i+1])<<8)
	}
	return string(r)
}
