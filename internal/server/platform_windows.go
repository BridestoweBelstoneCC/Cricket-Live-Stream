package server

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a helper program flashing a console window on the streaming laptop.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
