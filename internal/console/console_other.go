//go:build !windows

package console

func initPlatform() {}

// HideOwnConsole: only Windows gives a double-clicked program a console window of its own.
func HideOwnConsole() {}
