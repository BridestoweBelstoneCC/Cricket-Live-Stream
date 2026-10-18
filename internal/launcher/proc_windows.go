package launcher

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup    = 0x00000200
	belowNormalPriorityClass = 0x00004000
	createNoWindow           = 0x08000000
)

// detach puts the child in its own process group: the operator's Ctrl+C reaches only
// the launcher, never the server it supervises.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// detachQuiet is detach for a background helper: no window, below-normal priority so it
// never competes with OBS's encoder.
func detachQuiet(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow | belowNormalPriorityClass,
		HideWindow: true}
}

// terminate is Python's Popen.terminate(): TerminateProcess on Windows.
func terminate(p *os.Process) { p.Kill() }
