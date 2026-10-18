package setup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cricketstream/internal/proc"
)

// shortcutScript makes a desktop shortcut with Windows' own scripting host. The paths
// arrive in environment variables, never in the script text, so no folder name can
// break out of it. The Desktop folder is asked of Windows (it's often in OneDrive).
const shortcutScript = `$d = $env:CS_DESKTOP
if (-not $d) { $d = [Environment]::GetFolderPath('Desktop') }
$s = (New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $d 'CricketStream.lnk'))
$s.TargetPath = $env:CS_TARGET
$s.WorkingDirectory = $env:CS_DIR
$s.IconLocation = $env:CS_TARGET + ',0'
$s.Description = 'CricketStream - live-stream graphics for grassroots cricket'
$s.Save()
Write-Output (Join-Path $d 'CricketStream.lnk')`

// runShortcutScript is swappable for tests.
var runShortcutScript = func(target string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("desktop shortcuts are made on Windows only")
	}
	env := append(os.Environ(), "CS_TARGET="+target, "CS_DIR="+filepath.Dir(target))
	ps, err := exec.LookPath("powershell")
	if err != nil {
		return "", err
	}
	r, err := proc.Run(ps, []string{"-NoProfile", "-NonInteractive", "-Command", shortcutScript},
		proc.Options{Timeout: 30 * time.Second, Env: env})
	if err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", errors.New(lastLine(r.Stderr))
	}
	return lastLine(r.Stdout), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// DesktopShortcut puts a CricketStream shortcut on the desktop pointing at this app.
// Returns where it went.
func DesktopShortcut() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return runShortcutScript(exe)
}
