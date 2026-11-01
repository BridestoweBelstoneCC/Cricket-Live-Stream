package obs

import (
	"fmt"
	"time"

	"cricketstream/internal/ini"
	"cricketstream/internal/obsws"
)

// Finish closes OBS at the end of match day, when the operator asked for that — but never
// on air. OBS asks "exit anyway?" while any output runs, and the replay buffer always does
// during match day, so a plain Close was always refused: the replay buffer is stopped
// first. A stream or a recording still running means someone may be using it, so OBS is
// left open and why is returned. ok is true once OBS has gone (or was never open).
//
// Once OBS has gone, its update check goes back on too. Nothing else would do it: this
// runs after match day has stopped, so the server's OBS guard isn't watching any more,
// and the launcher's own last look found OBS still open (seen from the clubhouse
// screen's Finish, 2026-11-01: the check stayed off until the next match).
func Finish(configPath string) (ok bool, why string) {
	defer func() {
		if ok {
			resumeAfterFinish()
		}
	}()
	if !Running() {
		return true, ""
	}
	cfg, _ := ini.Load(configPath)
	target := obsws.Target{Host: "localhost", Port: WSPort}
	if cfg != nil {
		target.Password = cfg.Get("OBS", "obs_password", "")
	}
	st := obsws.Call(target, []obsws.Req{{Type: "GetStreamStatus"}, {Type: "GetRecordStatus"}}, 5*time.Second)
	if len(st) == 2 {
		if st[0] != nil && st[0]["outputActive"] == true {
			return false, "OBS is still streaming — stop the stream in OBS, then close it"
		}
		if st[1] != nil && st[1]["outputActive"] == true {
			return false, "OBS is still recording — stop the recording in OBS, then close it"
		}
		obsws.Call(target, []obsws.Req{{Type: "StopReplayBuffer"}}, 5*time.Second)
		sleep(time.Second) // the buffer winding down, before OBS is asked to close
	}
	// Unreachable (a password changed by hand, say): try anyway — with an output running
	// Close cancels OBS's question rather than answer it.
	if Close(20 * time.Second) {
		return true, ""
	}
	return false, "OBS didn't close (it may be asking something) — close it yourself"
}

// resumeAfterFinish: a test seam (tests must never touch the real OBS folder).
var resumeAfterFinish = func() {
	defer func() { recover() }()
	if ResumeUpdateCheck(ConfigDir()) {
		fmt.Println("  OBS's update check is back on.")
	}
}
