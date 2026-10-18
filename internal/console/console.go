// Package console is how every part of the app talks to a person at its console window.
//
// No path may exit without pausing first: a double-clicked exe owns its console window,
// so exiting closes it with the reason inside — on a match morning, nobody ever finds out
// why. Fatal paths go through Die() (framed problem + what to do + pause).
// CRICKETSTREAM_NO_PAUSE=1 turns the pauses off for automation and tests.
//
// When the app has its own window there is no console to type into: SetUI routes the
// prompts there instead (the app shows them as questions in the window), and the text
// printed around them reaches the window's activity log.
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

// UI answers the prompts when there's a window instead of a console.
type UI interface {
	// Pause shows msg (with the last Problem, if one is waiting) until it's dismissed.
	Pause(msg string)
	// Input asks for a line of text; ok is false if the question was dismissed.
	Input(prompt string) (string, bool)
	// Confirm is a yes/no question; ok is false if it was dismissed.
	Confirm(question string, def bool) (yes, ok bool)
	// Problem shows a fatal problem (Die pauses after it).
	Problem(title string, lines []string)
}

var (
	uiMu sync.Mutex
	ui   UI
)

// SetUI routes the prompts to a window (nil = back to the console).
func SetUI(u UI) {
	uiMu.Lock()
	ui = u
	uiMu.Unlock()
}

func currentUI() UI {
	uiMu.Lock()
	defer uiMu.Unlock()
	return ui
}

// NoPause reports whether pauses are switched off.
func NoPause() bool { return os.Getenv("CRICKETSTREAM_NO_PAUSE") == "1" }

// Pause waits for Enter unless pauses are switched off.
func Pause(msg string) {
	if NoPause() {
		return
	}
	if u := currentUI(); u != nil {
		u.Pause(msg)
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
	if u := currentUI(); u != nil {
		fmt.Println(strings.TrimSpace(prompt))
		return u.Input(prompt)
	}
	fmt.Print(prompt)
	stdinMu.Lock()
	defer stdinMu.Unlock()
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// Confirm asks a yes/no question ("  question [Y/n]: " at the console, buttons in the
// window). A blank answer is def; ok is false at end of input.
func Confirm(question string, def bool) (yes, ok bool) {
	if u := currentUI(); u != nil {
		fmt.Println("  " + question)
		return u.Confirm(question, def)
	}
	hint := " [y/N]: "
	if def {
		hint = " [Y/n]: "
	}
	ans, ok := Input("  " + question + hint)
	if !ok {
		return def, false
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "":
		return def, true
	case "y", "yes":
		return true, true
	}
	return false, true
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
	if u := currentUI(); u != nil {
		u.Problem(title, lines)
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
