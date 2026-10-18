//go:build !windows

package tools

import (
	"fmt"
	"os"
	"os/exec"

	"cricketstream/internal/console"
)

// readPassword is getpass: prompt, read a line without echoing it. stty is on every
// macOS and Linux; if stdin isn't a terminal it fails and the line is simply read.
func readPassword(prompt string) (string, bool) {
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if off.Run() == nil {
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = os.Stdin
			on.Run()
			fmt.Println()
		}()
	}
	return console.Input(prompt)
}
