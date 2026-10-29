package clubhouse

import (
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"cricketstream/internal/proc"
)

// readPlatform takes Windows' readings, none needing administrator rights.
func readPlatform(c *WakeCheck) {
	c.Platform = true
	for _, a := range adapters() {
		if a.ethernet && !isVirtual(a.name) && !isVirtual(a.desc) {
			c.WiredPorts = append(c.WiredPorts, a.desc)
		}
	}
	if armed, ok := wakeArmed(); ok {
		c.Armed = []string{}
		for _, p := range c.WiredPorts {
			for _, d := range armed {
				if armedMatches(d, p) {
					c.Armed = append(c.Armed, p)
					break
				}
			}
		}
	}
	if v, ok := regDWORD(`SYSTEM\CurrentControlSet\Control\Session Manager\Power`, "HiberbootEnabled"); ok {
		on := v != 0
		c.FastStartup = &on
	}
	auto := false
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`,
		registry.QUERY_VALUE); err == nil {
		v, _, _ := k.GetStringValue("AutoAdminLogon") // a string "1", as Autologon writes it
		auto = strings.TrimSpace(v) == "1"
		k.Close()
		c.AutoSignIn = &auto
	}
}

// wakeArmed is `powercfg /devicequery wake_armed`: every device allowed to wake this PC,
// by its description. Swappable for tests.
var wakeArmed = func() ([]string, bool) {
	r, err := proc.Run("powercfg", []string{"/devicequery", "wake_armed"}, proc.Options{Timeout: 10 * time.Second})
	if err != nil || r.Code != 0 {
		return nil, false
	}
	var out []string
	for _, l := range strings.Split(r.Stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.EqualFold(l, "NONE") {
			out = append(out, l)
		}
	}
	return out, true
}

func regDWORD(path, name string) (uint64, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return v, err == nil
}
