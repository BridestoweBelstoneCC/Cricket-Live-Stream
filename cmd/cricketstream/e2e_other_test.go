//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt is the operator pressing Ctrl+C in the app's window.
func interrupt(p *proc) error { return p.cmd.Process.Signal(syscall.SIGINT) }
