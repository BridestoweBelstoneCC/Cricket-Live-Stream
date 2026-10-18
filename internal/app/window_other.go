//go:build !windows

package app

import "errors"

// Only Windows has a built-in window so far; elsewhere the pages open in the browser.
func openWindow(a *App, url string) (window, error) {
	return nil, errors.New("no built-in window on this system yet")
}

func allowForeground() {}
