package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setProcessGroup gives the app its own process group, so a Ctrl+Break sent to it
// reaches the app alone (and not this test).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// interrupt is the operator pressing Ctrl+C in the app's window. Go delivers Ctrl+Break
// as os.Interrupt too, and unlike Ctrl+C it can be aimed at one process group. It needs
// a console to share; without one it fails and the caller skips.
func interrupt(p *proc) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.cmd.Process.Pid))
}
