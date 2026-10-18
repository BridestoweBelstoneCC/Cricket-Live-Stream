// Package browser opens a URL in the person's default web browser.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

// Open is swappable for tests (so a test never pops a browser up).
// CRICKETSTREAM_NO_BROWSER=1 turns it off everywhere (automation, a headless machine).
var Open = func(url string) error {
	if os.Getenv("CRICKETSTREAM_NO_BROWSER") == "1" {
		return nil
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}

// OpenFolder shows a folder in Explorer / Finder / the file manager (swappable for tests).
var OpenFolder = func(dir string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Start()
	}
	return exec.Command("xdg-open", dir).Start()
}
