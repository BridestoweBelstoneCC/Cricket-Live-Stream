//go:build windows

package app

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pickFolder is Windows' own "choose a folder" box (the modern one, IFileOpenDialog with
// FOS_PICKFOLDERS). "" if the person cancels. Runs on the window's thread, which is the
// COM apartment the dialog needs.

var (
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")

	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
		Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768,
		Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE,
		Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	clsctxInprocServer = 0x1
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	sigdnFileSysPath   = 0x80058000
	errCancelled       = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)

	// Method slots: IUnknown, IModalWindow, IFileDialog, IShellItem.
	vRelease        = 2
	vShow           = 3
	vSetOptions     = 9
	vGetOptions     = 10
	vSetFolder      = 12
	vSetTitle       = 17
	vGetResult      = 20
	vGetDisplayName = 5
)

// comCall calls method slot i of a COM object.
func comCall(obj unsafe.Pointer, i int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, i*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func pickFolder(owner uintptr, title, start string) (string, error) {
	var dlg unsafe.Pointer
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg))); hr != 0 || dlg == nil {
		return "", fmt.Errorf("folder box unavailable (%#x)", hr)
	}
	defer comCall(dlg, vRelease)
	var opts uint32
	comCall(dlg, vGetOptions, uintptr(unsafe.Pointer(&opts)))
	comCall(dlg, vSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem|fosPathMustExist))
	if title != "" {
		t, _ := windows.UTF16PtrFromString(title)
		comCall(dlg, vSetTitle, uintptr(unsafe.Pointer(t)))
	}
	if start != "" && isDir(start) {
		p, _ := windows.UTF16PtrFromString(start)
		var item unsafe.Pointer
		if hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(p)), 0,
			uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item))); hr == 0 && item != nil {
			comCall(dlg, vSetFolder, uintptr(item))
			comCall(item, vRelease)
		}
	}
	switch hr := comCall(dlg, vShow, owner); {
	case uint32(hr) == errCancelled:
		return "", nil
	case hr != 0:
		return "", fmt.Errorf("folder box failed (%#x)", hr)
	}
	var item unsafe.Pointer
	if hr := comCall(dlg, vGetResult, uintptr(unsafe.Pointer(&item))); hr != 0 || item == nil {
		return "", fmt.Errorf("no folder chosen (%#x)", hr)
	}
	defer comCall(item, vRelease)
	var name *uint16
	if hr := comCall(item, vGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); hr != 0 || name == nil {
		return "", fmt.Errorf("that isn't a folder on this computer (%#x)", hr)
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	return windows.UTF16PtrToString(name), nil
}
