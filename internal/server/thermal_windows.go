package server

// Windows' version of the Mac's CPU_Speed_Limit: the "% Performance Limit" counter,
// which drops below 100 when the firmware holds the CPU back — for heat, or for power on
// a laptop running off its battery. A real temperature needs administrator rights (the
// ACPI thermal zone), and this is the signal that matters anyway: it moves the moment
// the laptop starts slowing down, well before it shuts itself off.
//
// Read through pdh.dll, not PowerShell: /health is polled every 10s, and starting a
// process that often is load the overheating laptop doesn't need. The English counter
// name works on every Windows language.

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	pdh                             = syscall.NewLazyDLL("pdh.dll")
	procPdhOpenQueryW               = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW       = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = pdh.NewProc("PdhGetFormattedCounterValue")
)

const pdhFmtDouble = 0x00000200

// pdhFmtCounterValue is PDH_FMT_COUNTERVALUE: a status, then a union whose double is
// 8-byte aligned on every Windows architecture.
type pdhFmtCounterValue struct {
	cStatus uint32
	_       uint32
	double  float64
}

var perfLimit struct {
	sync.Mutex
	query, counter uintptr
	failed         bool // the counter isn't there (older Windows): stop asking
	at             time.Time
	pct            float64
	ok             bool
}

// perfLimitTTL: /health and the watchdog may both ask; once every few seconds is plenty.
const perfLimitTTL = 5 * time.Second

// readPerfLimit is the CPU's "% Performance Limit" right now (100 = not held back).
// Swappable for tests.
var readPerfLimit = func() (float64, bool) {
	perfLimit.Lock()
	defer perfLimit.Unlock()
	if perfLimit.failed {
		return 0, false
	}
	if time.Since(perfLimit.at) < perfLimitTTL {
		return perfLimit.pct, perfLimit.ok
	}
	if perfLimit.query == 0 {
		path, _ := syscall.UTF16PtrFromString(`\Processor Information(_Total)\% Performance Limit`)
		if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&perfLimit.query))); r != 0 {
			perfLimit.failed = true
			return 0, false
		}
		if r, _, _ := procPdhAddEnglishCounterW.Call(perfLimit.query, uintptr(unsafe.Pointer(path)), 0,
			uintptr(unsafe.Pointer(&perfLimit.counter))); r != 0 {
			perfLimit.failed = true
			return 0, false
		}
		// A rate counter needs two samples; the first only primes it.
		procPdhCollectQueryData.Call(perfLimit.query)
		time.Sleep(100 * time.Millisecond)
	}
	perfLimit.at, perfLimit.ok = time.Now(), false
	if r, _, _ := procPdhCollectQueryData.Call(perfLimit.query); r != 0 {
		return 0, false
	}
	var v pdhFmtCounterValue
	if r, _, _ := procPdhGetFormattedCounterValue.Call(perfLimit.counter, pdhFmtDouble, 0,
		uintptr(unsafe.Pointer(&v))); r != 0 || v.cStatus > 1 { // 0 = valid, 1 = new data
		return 0, false
	}
	perfLimit.pct, perfLimit.ok = v.double, true
	return v.double, true
}
