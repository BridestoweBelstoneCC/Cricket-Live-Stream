//go:build !windows

package server

// readPerfLimit is Windows' CPU performance limit; elsewhere there's none (the Mac has
// pmset, in thermalState). Swappable for tests.
var readPerfLimit = func() (float64, bool) { return 0, false }
