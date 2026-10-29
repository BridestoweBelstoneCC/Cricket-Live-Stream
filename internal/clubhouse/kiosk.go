package clubhouse

// Chromium, full screen on the screen's page: what the Pi shows. Restarted if it exits
// (a crash, or someone finding a way to close it), with a pause if it keeps doing so.

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"cricketstream/internal/assets"
)

// browserNames: Raspberry Pi OS Bookworm ships chromium-browser; Trixie and most other
// distributions call it chromium.
var browserNames = []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"}

func findBrowser() string {
	for _, n := range browserNames {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// kioskArgs: full screen with nothing to tap out of; no "restore pages?" bar after a
// power cut; no translate prompt; video plays without a tap first; Wayland or X as the
// desktop has it; a profile of its own beside the app (never the Pi user's browser).
func kioskArgs(url string) []string {
	return []string{"--kiosk", "--noerrdialogs", "--disable-infobars", "--no-first-run",
		"--disable-session-crashed-bubble", "--disable-features=Translate,TranslateUI",
		"--autoplay-policy=no-user-gesture-required", "--check-for-update-interval=31536000",
		"--overscroll-history-navigation=0", "--disable-pinch", "--ozone-platform-hint=auto",
		"--user-data-dir=" + assets.Join("clubhouse-browser"), url}
}

type kioskBrowser struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	stop bool
}

func startKiosk(url string) *kioskBrowser {
	k := &kioskBrowser{}
	path := findBrowser()
	if path == "" {
		fmt.Println("  ⚠ Chromium isn't installed — install it (sudo apt install chromium-browser) or open " + url + " yourself.")
		return k
	}
	go func() {
		fails := 0
		for {
			k.mu.Lock()
			if k.stop {
				k.mu.Unlock()
				return
			}
			cmd := exec.Command(path, kioskArgs(url)...)
			cmd.Env = os.Environ()
			k.cmd = cmd
			k.mu.Unlock()
			started := time.Now()
			if err := cmd.Start(); err != nil {
				fmt.Println("  ✗ Couldn't open Chromium: " + err.Error())
				return
			}
			cmd.Wait()
			k.mu.Lock()
			stopped := k.stop
			k.mu.Unlock()
			if stopped {
				return
			}
			if time.Since(started) < 10*time.Second {
				fails++
			} else {
				fails = 0
			}
			wait := time.Duration(fails*fails) * time.Second // 0s, 1s, 4s, 9s... a desktop not ready yet, say
			if wait > time.Minute {
				wait = time.Minute
			}
			fmt.Printf("  Chromium closed — opening it again%s\n", map[bool]string{true: fmt.Sprintf(" in %s", wait), false: ""}[wait > 0])
			time.Sleep(wait)
		}
	}()
	return k
}

func (k *kioskBrowser) Stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stop = true
	if k.cmd != nil && k.cmd.Process != nil {
		k.cmd.Process.Kill()
	}
}
