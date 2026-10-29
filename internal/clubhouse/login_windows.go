package clubhouse

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// The laptop the clubhouse screen wakes has to come up running CricketStream, or the
// screen waits for a link that never answers. The usual per-user Run key does it: no
// administrator rights, and it's the same entry Windows' own Settings → Apps → Startup
// lists (and lets anyone switch off).
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

const runValue = "CricketStream"

// StartAtLoginSupported: only Windows has it built in here.
const StartAtLoginSupported = true

// StartAtLogin reports whether CricketStream opens when this user signs in, and whether
// the entry points at THIS copy (an old copy's path is no good after a move).
func StartAtLogin() (on, thisCopy bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	if err != nil || strings.TrimSpace(v) == "" {
		return false, false
	}
	exe, _ := os.Executable()
	return true, exe != "" && strings.EqualFold(strings.Trim(v, `" `), exe)
}

// SetStartAtLogin adds or removes the entry for this copy.
func SetStartAtLogin(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`"`)
}
