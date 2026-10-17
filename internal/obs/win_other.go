//go:build !windows

package obs

import (
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func hiddenCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func registryOBSFolders() []string { return nil }

// WindowHung has no equivalent outside Windows: always unknown, so freeze recovery
// never acts there.
func WindowHung(time.Duration, []int) (hung, known bool) { return false, false }

// Close quits OBS the way a person would (never a force-kill); true once it's gone.
func Close(timeout time.Duration) bool {
	var err error
	if runtime.GOOS == "darwin" {
		err = exec.Command("osascript", "-e", `tell application "OBS" to quit`).Run()
	} else {
		err = exec.Command("pkill", "-TERM", "-x", "obs").Run()
	}
	_ = err
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !Running() {
			return true
		}
		sleep(time.Second)
	}
	return false
}
