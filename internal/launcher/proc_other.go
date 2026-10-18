//go:build !windows

package launcher

import (
	"os"
	"os/exec"
	"syscall"
)

// detach puts the child in its own session: the operator's Ctrl+C reaches only the
// launcher, never the server it supervises.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// detachQuiet is detach for a background helper (niced once started).
func detachQuiet(cmd *exec.Cmd) { detach(cmd) }

func terminate(p *os.Process) { p.Signal(syscall.SIGTERM) }
