package server

import "testing"

// The real counter on this machine: the PDH call works and gives a sane percentage, and
// a second read inside the cache window is served without asking Windows again.
func TestReadPerfLimitOnThisMachine(t *testing.T) {
	pct, ok := readPerfLimit()
	if !ok {
		t.Skip("this Windows has no % Performance Limit counter (a VM, or an older Windows)")
	}
	if pct <= 0 || pct > 200 {
		t.Fatalf("%% Performance Limit = %v", pct)
	}
	perfLimit.Lock()
	at := perfLimit.at
	perfLimit.Unlock()
	if again, ok := readPerfLimit(); !ok || again != pct {
		t.Fatalf("cached read: %v %v, want %v", again, ok, pct)
	}
	perfLimit.Lock()
	defer perfLimit.Unlock()
	if !perfLimit.at.Equal(at) {
		t.Fatal("a read inside the cache window asked Windows again")
	}
}
