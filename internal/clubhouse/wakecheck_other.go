//go:build !windows

package clubhouse

// readPlatform: a Mac or Linux streaming machine has no readings here (its wake settings
// live in places a program can't read without root); the cable and a past wake still count.
func readPlatform(*WakeCheck) {}
