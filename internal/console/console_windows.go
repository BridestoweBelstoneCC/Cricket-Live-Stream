package console

import "syscall"

// A Windows console defaults to a legacy codepage, which shows the app's em-dashes and
// arrows as "â€”". Best-effort: never allowed to stop the app starting.
func initPlatform() {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	for _, name := range []string{"SetConsoleOutputCP", "SetConsoleCP"} {
		if p := k32.NewProc(name); p.Find() == nil {
			p.Call(65001)
		}
	}
}
