//go:build windows

package app

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The icon by the clock: CricketStream is still there when its window is minimised or
// behind OBS. Click it to bring the window up; right-click for Open, Activity, the control
// panel and Close. It's also where the alerts appear, as Windows notifications.

var (
	shell32Tray          = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW = shell32Tray.NewProc("Shell_NotifyIconW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procPostMessageW     = user32.NewProc("PostMessageW")
)

// notifyIconData is NOTIFYICONDATAW (976 bytes on 64-bit Windows).
type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State, StateMask uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUIDItem         windows.GUID
	BalloonIcon      uintptr
}

const (
	nimAdd, nimModify, nimDelete = 0, 1, 2
	nifMessage, nifIcon, nifTip  = 0x1, 0x2, 0x4
	nifInfo                      = 0x10
	niifInfo, niifWarning        = 0x1, 0x2
	niifRespectQuietTime         = 0x80 // no pop-up during Windows' own "do not disturb"
	trayMsg                      = 0x8000 + 0x51
	wmLButtonUp, wmRButtonUp     = 0x202, 0x205
	wmContextMenu                = 0x7B
	wmNull                       = 0x0000
	mfString, mfSeparator        = 0x0, 0x800
	tpmReturnCmd, tpmRightButton = 0x100, 0x2
	tpmNoNotify                  = 0x80
)

const (
	menuOpen = 1 + iota
	menuActivity
	menuPanel
	menuClose
)

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = append(u[:len(dst)-1], 0)
	}
	copy(dst, u)
}

func (nw *nativeWindow) trayData() notifyIconData {
	d := notifyIconData{Wnd: nw.hwnd, ID: 1}
	d.Size = uint32(unsafe.Sizeof(d))
	return d
}

// addTray puts the icon by the clock (on the window's thread).
func (nw *nativeWindow) addTray(tip string) {
	d := nw.trayData()
	d.Flags = nifMessage | nifIcon | nifTip
	d.CallbackMessage = trayMsg
	d.Icon = nw.smallIcon
	copyUTF16(d.Tip[:], tip)
	if ok, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&d))); ok == 0 {
		fmt.Printf("  (No icon by the clock: %v — notifications go to this log only.)\n", err)
	}
}

func (nw *nativeWindow) removeTray() {
	d := nw.trayData()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&d)))
}

// SetTip is the icon's hover text.
func (nw *nativeWindow) setTip(tip string) {
	d := nw.trayData()
	d.Flags = nifTip
	copyUTF16(d.Tip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d)))
}

// Notify shows a Windows notification from the icon.
func (nw *nativeWindow) Notify(title, text string, warn bool) {
	nw.wv.Dispatch(func() {
		d := nw.trayData()
		d.Flags = nifInfo
		d.InfoFlags = niifInfo | niifRespectQuietTime
		if warn {
			d.InfoFlags = niifWarning | niifRespectQuietTime
		}
		copyUTF16(d.InfoTitle[:], title)
		copyUTF16(d.Info[:], text)
		if ok, _, err := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d))); ok == 0 {
			fmt.Printf("  (Windows didn't show that notification: %v)\n", err)
		}
	})
}

// trayEvent handles the icon's clicks (on the window's thread).
func (nw *nativeWindow) trayEvent(lp uintptr) {
	switch uint32(lp) & 0xFFFF {
	case wmLButtonUp:
		nw.Focus()
	case wmRButtonUp, wmContextMenu:
		nw.trayMenu()
	}
}

func (nw *nativeWindow) trayMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	item := func(id uintptr, text string) {
		p, _ := windows.UTF16PtrFromString(text)
		procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(p)))
	}
	item(menuOpen, "Open CricketStream")
	item(menuActivity, "Activity")
	if nw.a.currentPhase() == phRunning {
		item(menuPanel, "Control panel")
	}
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	item(menuClose, "Close CricketStream")
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(nw.hwnd) // or the menu won't close when clicked away from
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton|tpmNoNotify,
		uintptr(pt.X), uintptr(pt.Y), 0, nw.hwnd, 0)
	procPostMessageW.Call(nw.hwnd, wmNull, 0, 0)
	switch cmd {
	case menuOpen:
		nw.Focus()
	case menuActivity:
		nw.Focus()
		go nw.a.navigate(nw.a.base + "/#log")
	case menuPanel:
		nw.Focus()
		go nw.a.showPanel()
	case menuClose:
		procPostMessageW.Call(nw.hwnd, wmClose, 0, 0) // the close button's path: asks first if something's running
	}
}
