package obs

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows          = user32.NewProc("EnumWindows")
	procGetWindowThreadPID   = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible      = user32.NewProc("IsWindowVisible")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procSendMessageTimeoutW  = user32.NewProc("SendMessageTimeoutW")
)

const (
	wmNull          = 0x0000
	wmClose         = 0x0010
	smtoAbortIfHung = 0x0002
	errorTimeout    = 1460
)

func hiddenCommand(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

func detach(cmd *exec.Cmd) {
	const detachedProcess, newProcessGroup = 0x00000008, 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup}
}

func registryOBSFolders() []string {
	var out []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, `SOFTWARE\OBS Studio`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if v, _, err := k.GetStringValue(""); err == nil && v != "" {
			out = append(out, v)
		}
		k.Close()
	}
	return out
}

type window struct {
	hwnd  uintptr
	title string
}

// visibleWindows lists the visible top-level windows of the given processes.
func visibleWindows(pids map[uint32]bool) []window {
	var found []window
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if vis, _, _ := procIsWindowVisible.Call(hwnd); pids[pid] && vis != 0 {
			n, _, _ := procGetWindowTextLengthW.Call(hwnd)
			buf := make([]uint16, n+1)
			procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
			found = append(found, window{hwnd, windows.UTF16ToString(buf)})
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

func pidSet(pids []int) map[uint32]bool {
	m := map[uint32]bool{}
	for _, p := range pids {
		m[uint32(p)] = true
	}
	return m
}

// WindowHung reports whether OBS is frozen ("Not Responding"): (hung, known).
//
// Pings OBS's window with a no-op message (WM_NULL) and waits up to timeout for it to be
// handled, which is what a frozen UI thread can't do. Deliberately NOT IsHungAppWindow
// alone, the check behind Windows' own "(Not Responding)": that only fires once messages
// are queued and unhandled, so a frozen OBS that nobody is clicking on can stay
// "responding" indefinitely — a suspended OBS read as responding for two minutes in
// testing. The ping queues the message itself, so it doesn't depend on someone touching
// the laptop. A modal dialog (an update prompt, say) is NOT a freeze; its message loop
// answers for the window behind it.
//
// pids: the caller's cached OBS process ids, so a check every few seconds doesn't spawn
// tasklist every time; none found for them means they're stale: unknown.
func WindowHung(timeout time.Duration, pids []int) (hung, known bool) {
	if len(pids) == 0 {
		pids = PIDs()
	}
	if len(pids) == 0 {
		return false, false
	}
	wins := visibleWindows(pidSet(pids))
	if len(wins) == 0 {
		return false, false
	}
	// Every OBS window shares one UI thread, so one answer speaks for all of them.
	var result uintptr
	ok, _, err := procSendMessageTimeoutW.Call(wins[0].hwnd, wmNull, 0, 0, smtoAbortIfHung,
		uintptr(timeout.Milliseconds()), uintptr(unsafe.Pointer(&result)))
	if ok != 0 {
		return false, true
	}
	// Zero with ERROR_TIMEOUT (or no error: SMTO_ABORTIFHUNG on an already-hung window)
	// is a freeze. Anything else — the window closed mid-check — isn't.
	errno, _ := err.(syscall.Errno)
	return errno == errorTimeout || errno == 0, true
}

// Close closes OBS the way a person would, so it saves its scenes and exits cleanly (no
// crash marker, so no Safe Mode prompt next time). True once it's gone. Only for when
// nothing is on air: with an output running, OBS asks before quitting, and this gives up
// rather than answer for anyone — never a force-kill.
func Close(timeout time.Duration) bool {
	for _, w := range visibleWindows(pidSet(PIDs())) {
		if len(w.title) >= 4 && w.title[:4] == "OBS " { // the main window
			procPostMessageW.Call(w.hwnd, wmClose, 0, 0) // WM_CLOSE: the window's X
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !Running() {
			return true
		}
		sleep(time.Second)
	}
	// Still open: almost always OBS's "Active Outputs — exit anyway?" question, which
	// closing it raised. Cancel it, rather than leave an unexplained "exit?" box on the
	// operator's screen (seen on OBS 32.1.1 with the replay buffer running).
	for _, w := range visibleWindows(pidSet(PIDs())) {
		if !(len(w.title) >= 4 && w.title[:4] == "OBS ") {
			procPostMessageW.Call(w.hwnd, wmClose, 0, 0) // = Cancel / No
		}
	}
	return false
}
