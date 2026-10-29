package clubhouse

import "testing"

// Windows' real readings on this machine: taken without administrator rights, and no
// virtual adapter counted as one of the laptop's own wired ports.
func TestCheckWakeOnThisMachine(t *testing.T) {
	c := CheckWake()
	if !c.Platform || !c.LoginKnown {
		t.Fatalf("Windows readings not taken: %+v", c)
	}
	if c.FastStartup == nil || c.AutoSignIn == nil {
		t.Errorf("registry readings missing: fast startup %v, auto sign-in %v", c.FastStartup, c.AutoSignIn)
	}
	for _, p := range c.WiredPorts {
		if isVirtual(p) {
			t.Errorf("virtual adapter counted as a wired port: %q", p)
		}
	}
	for _, a := range c.Armed {
		found := false
		for _, p := range c.WiredPorts {
			found = found || a == p
		}
		if !found {
			t.Errorf("armed %q isn't one of the wired ports %v", a, c.WiredPorts)
		}
	}
	t.Logf("%+v", c)
}
