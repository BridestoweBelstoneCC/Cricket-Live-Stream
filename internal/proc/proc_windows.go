package proc

import (
	"os/exec"
	"syscall"
)

const (
	belowNormalPriorityClass = 0x00004000
	createNoWindow           = 0x08000000
)

func configure(cmd *exec.Cmd, o Options) {
	flags := uint32(createNoWindow)
	if o.BelowNormal {
		flags |= belowNormalPriorityClass
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}
