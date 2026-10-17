// Package browser opens a URL in the person's default web browser.
package browser

import (
	"os/exec"
	"runtime"
)

// Open is swappable for tests (so a test never pops a browser up).
var Open = func(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
