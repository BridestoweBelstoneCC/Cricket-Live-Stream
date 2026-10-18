package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"

	"cricketstream/internal/console"
)

// readPassword is getpass: prompt, read a line without echoing it.
func readPassword(prompt string) (string, bool) {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT)
		defer func() {
			windows.SetConsoleMode(h, mode)
			fmt.Println()
		}()
	}
	return console.Input(prompt)
}
