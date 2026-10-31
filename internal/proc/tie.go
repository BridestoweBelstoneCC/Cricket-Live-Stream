package proc

import "os/exec"

// StartTied starts a long-running program (a recording) that must not outlive this one:
// on Windows it goes in a job object that closes — killing it — when this process ends,
// however it ends; on Linux it gets SIGKILL when this process dies. Elsewhere (macOS)
// there's no such hook, so callers also bound the program's own run time.
func StartTied(cmd *exec.Cmd) error {
	configure(cmd, Options{})
	beforeStart(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	afterStart(cmd)
	return nil
}
