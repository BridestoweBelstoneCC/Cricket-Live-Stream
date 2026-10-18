//go:build !windows

package proc

import "os/exec"

func configure(cmd *exec.Cmd, o Options) {
	if o.BelowNormal {
		// Lower the priority the portable way: run it under nice.
		if p, err := exec.LookPath("nice"); err == nil {
			cmd.Args = append([]string{p, "-n", "10", cmd.Path}, cmd.Args[1:]...)
			cmd.Path = p
		}
	}
}
