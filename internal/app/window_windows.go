//go:build windows

package app

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"

	"cricketstream/internal/browser"
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW     = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW       = user32.NewProc("CallWindowProcW")
	procLoadImageW            = user32.NewProc("LoadImageW")
	procSendMessageW          = user32.NewProc("SendMessageW")
	procShowWindow            = user32.NewProc("ShowWindow")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procAllowSetForegroundWnd = user32.NewProc("AllowSetForegroundWindow")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procGetWindowPlacement    = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement    = user32.NewProc("SetWindowPlacement")
	procMonitorFromRect       = user32.NewProc("MonitorFromRect")
)

const (
	wmClose     = 0x0010
	wmSetIcon   = 0x0080
	swRestore   = 9
	imageIcon   = 1
	lrShared    = 0x8000
	smCxScreen  = 0
	smCyScreen  = 1
	smCxIcon    = 11
	smCxSmIcon  = 49
	gwlpWndProc = ^uintptr(3) // -4
)

type nativeWindow struct {
	wv      webview2.WebView
	hwnd    uintptr
	orig    uintptr
	closing atomic.Bool
	a       *App
}

// openWindow opens CricketStream's window on url. It must run on the main goroutine (the
// web view's thread), and so must Run.
func openWindow(a *App, start string) (window, error) {
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		return nil, errors.New("Microsoft Edge WebView2 isn't installed on this computer")
	}
	w, h := windowSize()
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("CRICKETSTREAM_DEVTOOLS") == "1",
		DataPath:  filepath.Join(os.Getenv("LOCALAPPDATA"), "CricketStream", "WebView2"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{Title: "CricketStream", Width: uint(w), Height: uint(h),
			Center: true},
	})
	if wv == nil {
		return nil, errors.New("the window's web view wouldn't start")
	}
	nw := &nativeWindow{wv: wv, hwnd: uintptr(wv.Window()), a: a}
	nw.setIcon()
	nw.restorePlacement()
	// The close button asks first when something's running: catch WM_CLOSE before the
	// web view's own window procedure destroys the window.
	nw.orig, _, _ = procSetWindowLongPtrW.Call(nw.hwnd, gwlpWndProc, windows.NewCallback(nw.wndproc))
	wv.SetSize(scale(860), scale(600), webview2.HintMin)

	// What the pages can ask of the window. Harmless to any page: a folder the person
	// chooses, a link opened in their browser, a screen of the app.
	wv.Bind("csApp_pickFolder", func(title, startDir string) string {
		p, _ := pickFolder(nw.hwnd, title, startDir)
		return p
	})
	wv.Bind("csApp_open", func(link string) bool {
		if u, err := url.Parse(link); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			browser.Open(u.String())
			return true
		}
		return false
	})
	wv.Bind("csApp_go", func(view string) {
		go a.navigate(a.base + "/#" + strings.TrimPrefix(view, "#"))
	})
	wv.Init(windowScript(a.base))
	wv.Navigate(start)
	return nw, nil
}

// windowScript runs at the start of every page in the window. It makes the app's
// functions one object, and sends links that would leave CricketStream (a new tab, or
// another website) to the person's own browser, where they belong.
func windowScript(appBase string) string {
	return `(function(){
  window.csApp = {pickFolder: window.csApp_pickFolder, open: window.csApp_open, go: window.csApp_go,
                  base: ` + jsQuote(appBase) + `};
  function ours(u) { return (u.hostname === '127.0.0.1' || u.hostname === 'localhost') && /^https?:$/.test(u.protocol); }
  var nativeOpen = window.open;
  window.open = function (u) {
    try { var a = new URL(u, location.href); if (/^https?:$/.test(a.protocol)) { window.csApp_open(a.href); return null; } } catch (e) {}
    return nativeOpen.apply(window, arguments);
  };
  document.addEventListener('click', function (e) {
    var a = e.target && e.target.closest ? e.target.closest('a[href]') : null;
    if (!a || a.hasAttribute('download')) return;
    var u; try { u = new URL(a.href, location.href); } catch (x) { return; }
    if (!/^https?:$/.test(u.protocol)) return;
    if (a.target === '_blank' || !ours(u)) { e.preventDefault(); window.csApp_open(u.href); }
  }, true);
})();`
}

func jsQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "<", `\x3c`)
	return "'" + r.Replace(s) + "'"
}

func (nw *nativeWindow) wndproc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmClose && !nw.closing.Load() {
		// Decided off the window's thread: the answer may need the page.
		go func() {
			if nw.a.closeRequested() {
				nw.Close()
			}
		}()
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(nw.orig, hwnd, msg, wp, lp)
	return r
}

func (nw *nativeWindow) Navigate(u string) { nw.wv.Dispatch(func() { nw.wv.Navigate(u) }) }
func (nw *nativeWindow) AddScript(js string) {
	done := make(chan struct{})
	nw.wv.Dispatch(func() { nw.wv.Init(js); close(done) })
	<-done
}
func (nw *nativeWindow) Run() { nw.wv.Run() }
func (nw *nativeWindow) SetTitle(t string) {
	nw.wv.Dispatch(func() { nw.wv.SetTitle(t) })
}

func (nw *nativeWindow) Close() {
	nw.savePlacement()
	nw.closing.Store(true)
	nw.wv.Destroy()
}

func (nw *nativeWindow) Focus() {
	nw.wv.Dispatch(func() {
		if r, _, _ := procIsIconic.Call(nw.hwnd); r != 0 {
			procShowWindow.Call(nw.hwnd, swRestore)
		}
		procSetForegroundWindow.Call(nw.hwnd)
	})
}

// setIcon puts the app's own icon (the "APP" resource) on the window and taskbar.
func (nw *nativeWindow) setIcon() {
	var h windows.Handle
	if windows.GetModuleHandleEx(0, nil, &h) != nil {
		return
	}
	name, _ := windows.UTF16PtrFromString("APP")
	for _, which := range []struct{ metric, kind uintptr }{{smCxIcon, 1}, {smCxSmIcon, 0}} {
		size, _, _ := procGetSystemMetrics.Call(which.metric)
		icon, _, _ := procLoadImageW.Call(uintptr(h), uintptr(unsafe.Pointer(name)), imageIcon, size, size, lrShared)
		if icon != 0 {
			procSendMessageW.Call(nw.hwnd, wmSetIcon, which.kind, icon)
		}
	}
}

func scale(px int) int {
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d != 0 {
			dpi = d
		}
	}
	return px * int(dpi) / 96
}

// windowSize is a comfortable size for the control panel, never bigger than the screen.
func windowSize() (int, int) {
	w, h := scale(1280), scale(860)
	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	if sw > 0 && w > int(sw)*92/100 {
		w = int(sw) * 92 / 100
	}
	if sh > 0 && h > int(sh)*88/100 {
		h = int(sh) * 88 / 100
	}
	return w, h
}

// allowForeground lets the copy already open take the foreground when this one (which
// the person just double-clicked) asks it to come to the front.
func allowForeground() { procAllowSetForegroundWnd.Call(^uintptr(0)) }

// ── Where the window was ──
// The window opens where it was left, the size it was, maximised if it was — unless that
// place is no longer on any screen (a projector unplugged since).

type windowPlacement struct {
	Length, Flags, ShowCmd uint32
	MinPos, MaxPos         [2]int32
	Normal                 [4]int32 // left, top, right, bottom
}

const swShowMaximized, swShowNormal, monitorDefaultToNull = 3, 1, 0

func placementFile() string { return filepath.Join(windowStateDir(), "window.json") }

func windowStateDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "CricketStream")
}

func (nw *nativeWindow) savePlacement() {
	wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
	if r, _, _ := procGetWindowPlacement.Call(nw.hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return
	}
	b, _ := json.Marshal(map[string]any{"normal": wp.Normal, "maximized": wp.ShowCmd == swShowMaximized})
	os.MkdirAll(windowStateDir(), 0o755)
	os.WriteFile(placementFile(), b, 0o644)
}

func (nw *nativeWindow) restorePlacement() {
	b, err := os.ReadFile(placementFile())
	if err != nil {
		return
	}
	var saved struct {
		Normal    [4]int32 `json:"normal"`
		Maximized bool     `json:"maximized"`
	}
	if json.Unmarshal(b, &saved) != nil || saved.Normal[2]-saved.Normal[0] < 200 || saved.Normal[3]-saved.Normal[1] < 150 {
		return
	}
	if m, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&saved.Normal)), monitorDefaultToNull); m == 0 {
		return
	}
	wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{})), ShowCmd: swShowNormal, Normal: saved.Normal}
	if saved.Maximized {
		wp.ShowCmd = swShowMaximized
	}
	procSetWindowPlacement.Call(nw.hwnd, uintptr(unsafe.Pointer(&wp)))
}
