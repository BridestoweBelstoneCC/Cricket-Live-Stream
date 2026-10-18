package app

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"cricketstream/internal/obs"
	"cricketstream/internal/proc"
)

// OBS missing on a new laptop used to surface on match morning ("Couldn't find OBS"). The
// match-day screen now says so beforehand and, on Windows, installs it with Windows' own
// package manager (winget) — OBS's official package, from Microsoft's catalogue — or
// links to obsproject.com where winget isn't there.

const obsWingetID = "OBSProject.OBSStudio"

// Swappable for tests.
var (
	obsInstalled = func() bool { return obs.FindOBS() != "" }
	findWinget   = func() string {
		if runtime.GOOS != "windows" {
			return ""
		}
		p, _ := exec.LookPath("winget")
		return p
	}
	// runInstall runs the install, passing each line it prints to say.
	runInstall = func(winget string, say func(string)) error {
		cmd := exec.Command(winget, "install", "--id", obsWingetID, "--exact", "--source", "winget",
			"--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
		proc.Hide(cmd)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			return err
		}
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			// winget draws progress bars with carriage returns; keep words, not bars.
			line := strings.TrimSpace(sc.Text())
			if i := strings.LastIndex(line, "\r"); i >= 0 {
				line = strings.TrimSpace(line[i+1:])
			}
			if line != "" && strings.IndexFunc(line, func(r rune) bool { return r >= 'A' && r <= 'z' }) >= 0 {
				say(line)
			}
		}
		return cmd.Wait()
	}
)

type obsInstall struct {
	mu      sync.Mutex
	running bool
	result  string // "", "installed", or why it didn't
}

// obsStatus is the match-day screen's OBS line: installed, and if not, how it can be.
func (a *App) obsStatus() map[string]any {
	a.obsInst.mu.Lock()
	defer a.obsInst.mu.Unlock()
	out := map[string]any{"installed": obsInstalled(), "installing": a.obsInst.running, "result": a.obsInst.result,
		"can_install": findWinget() != "", "download": "https://obsproject.com/download"}
	return out
}

// installOBS starts the install in the background (output in the activity log).
func (a *App) installOBS() error {
	if obsInstalled() {
		return errors.New("OBS is already installed on this laptop.")
	}
	winget := findWinget()
	if winget == "" {
		return errors.New("This computer can't install OBS by itself — download it from obsproject.com.")
	}
	a.obsInst.mu.Lock()
	if a.obsInst.running {
		a.obsInst.mu.Unlock()
		return nil
	}
	a.obsInst.running, a.obsInst.result = true, ""
	a.obsInst.mu.Unlock()
	go func() {
		defer crashed()
		fmt.Println("  Installing OBS Studio (Windows may ask you to allow it)...")
		err := runInstall(winget, func(line string) { fmt.Println("    " + line) })
		result := "installed"
		switch {
		case obsInstalled():
			fmt.Println("  ✓ OBS Studio is installed")
		case err != nil:
			result = "OBS didn't install (" + err.Error() + "). If Windows asked for permission, say Yes — or download it from obsproject.com."
			fmt.Println("  ✗ " + result)
		default:
			result = "The installer finished but OBS isn't there yet — try again, or download it from obsproject.com."
			fmt.Println("  ⚠ " + result)
		}
		a.obsInst.mu.Lock()
		a.obsInst.running, a.obsInst.result = false, result
		a.obsInst.mu.Unlock()
	}()
	return nil
}
