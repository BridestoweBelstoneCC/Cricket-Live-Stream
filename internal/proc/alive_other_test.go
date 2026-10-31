//go:build !windows

package proc

import (
	"os"
	"syscall"
)

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
