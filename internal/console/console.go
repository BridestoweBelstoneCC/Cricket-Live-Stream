// Package console is how every part of the app talks to a person at its console window.
//
// No path may exit without pausing first: a double-clicked exe owns its console window,
// so exiting closes it with the reason inside — on a match morning, nobody ever finds out
// why. Fatal paths go through Die() (framed problem + what to do + pause).
// CRICKETSTREAM_NO_PAUSE=1 turns the pauses off for automation and tests.
package console

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

var stdin = bufio.NewReader(os.Stdin)
var stdinMu sync.Mutex

// NoPause reports whether pauses are switched off.
func NoPause() bool { return os.Getenv("CRICKETSTREAM_NO_PAUSE") == "1" }

// Pause waits for Enter unless pauses are switched off.
func Pause(msg string) {
	if NoPause() {
		return
	}
	if msg == "" {
		msg = "Press Enter to close this window..."
	}
	fmt.Printf("\n  %s", msg)
	stdinMu.Lock()
	defer stdinMu.Unlock()
	stdin.ReadString('\n')
}

// Input prompts for a line of text; ok is false at end of input (stdin closed).
func Input(prompt string) (string, bool) {
	fmt.Print(prompt)
	stdinMu.Lock()
	defer stdinMu.Unlock()
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// Problem prints the framed explanation Die() uses, without exiting.
func Problem(title string, lines ...string) {
	fmt.Println()
	fmt.Println("  " + strings.Repeat("=", 60))
	fmt.Printf("   PROBLEM: %s\n", title)
	fmt.Println("  " + strings.Repeat("=", 60))
	for _, l := range lines {
		if l == "" {
			fmt.Println()
		} else {
			fmt.Printf("   %s\n", l)
		}
	}
}

// Die explains a fatal problem, pauses, and exits 1.
func Die(title string, lines ...string) {
	Problem(title, lines...)
	Pause("")
	os.Exit(1)
}

// Init makes the console show UTF-8 (em-dashes, ✓/✗) instead of mojibake.
func Init() { initPlatform() }
