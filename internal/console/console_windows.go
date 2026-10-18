package console

import (
	"bufio"
	"os"
	"syscall"
	"unsafe"
)

var (
	k32                     = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleWindow    = k32.NewProc("GetConsoleWindow")
	procAttachConsole       = k32.NewProc("AttachConsole")
	procGetConsoleProcesses = k32.NewProc("GetConsoleProcessList")
	procShowWindow          = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")
)

const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS (DWORD -1)

// A Windows console defaults to a legacy codepage, which shows the app's em-dashes and
// arrows as "â€”". Best-effort: never allowed to stop the app starting.
//
// The release build is a GUI program (no console window flashing up behind CricketStream's
// own window), so run from a terminal — `CricketStream.exe version` — it has no console
// of its own: it borrows the terminal's, and points the output there unless it was
// already redirected (a pipe or a file is left alone).
func initPlatform() {
	if h, _, _ := procGetConsoleWindow.Call(); h == 0 {
		if r, _, _ := procAttachConsole.Call(attachParentProcess); r != 0 {
			reopenStd()
		}
	}
	for _, name := range []string{"SetConsoleOutputCP", "SetConsoleCP"} {
		if p := k32.NewProc(name); p.Find() == nil {
			p.Call(65001)
		}
	}
}

func usable(f *os.File) bool {
	if f == nil {
		return false
	}
	h := syscall.Handle(f.Fd())
	if h == 0 || h == syscall.InvalidHandle {
		return false
	}
	_, err := syscall.GetFileType(h)
	return err == nil
}

func reopenStd() {
	if !usable(os.Stdout) {
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			os.Stdout = f
		}
	}
	if !usable(os.Stderr) {
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			os.Stderr = f
		}
	}
	if !usable(os.Stdin) {
		if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = f
			stdin = bufio.NewReader(os.Stdin)
		}
	}
}

// HideOwnConsole hides the console window when it's this program's alone (a development
// build double-clicked: the console subsystem gets a window of its own). One shared with
// a terminal the person typed into is left alone.
func HideOwnConsole() {
	h, _, _ := procGetConsoleWindow.Call()
	if h == 0 {
		return
	}
	var pids [2]uint32
	if n, _, _ := procGetConsoleProcesses.Call(uintptr(unsafe.Pointer(&pids[0])), 2); n == 1 {
		procShowWindow.Call(h, 0) // SW_HIDE
	}
}
