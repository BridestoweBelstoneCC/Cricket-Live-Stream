//go:build !windows

package server

import (
	"runtime"
	"syscall"
)

func processCPUSeconds() float64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

// peakRSSMB: macOS reports bytes, Linux kilobytes.
func peakRSSMB() float64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / 1e6
	}
	return float64(ru.Maxrss) / 1024.0
}
