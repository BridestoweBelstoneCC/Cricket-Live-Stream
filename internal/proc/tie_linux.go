package proc

import (
	"os/exec"
	"syscall"
)

func beforeStart(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func afterStart(*exec.Cmd) {}
