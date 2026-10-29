//go:build !windows

package clubhouse

import "errors"

// StartAtLoginSupported: on a Mac or Linux streaming machine, add CricketStream to the
// desktop's own login items by hand (CLUBHOUSE_SCREEN.md).
const StartAtLoginSupported = false

func StartAtLogin() (on, thisCopy bool) { return false, false }

func SetStartAtLogin(on bool) error {
	return errors.New("add CricketStream to this computer's login items by hand")
}
