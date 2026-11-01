package server

// Manual scoring (/scoring): the session glue. The engine and the event-sourced session
// live in internal/scoring; this keeps the one live session, restores a saved one after a
// restart, and renders its frames through ParsePCSJSON — so the overlay, ball DB,
// highlights tagging and every graphic work exactly as if a scorer's feed were present.
// While a session is live it OUTRANKS the scorer's file in /live, and every other feed
// consumer (over commentary, /health, the watchdog) must apply the same precedence.

import (
	"fmt"
	"os"
	"sync"

	"cricketstream/internal/scoring"
)

var (
	manualMu          sync.Mutex
	manualSession     *scoring.Session
	manualLoadAttempt bool
)

// ResetManual drops the in-memory session (tests). loadAttempted true means a saved file
// won't be restored.
func ResetManual(loadAttempted bool) {
	manualMu.Lock()
	manualSession, manualLoadAttempt = nil, loadAttempted
	manualMu.Unlock()
}

// manualSessionLocked is the current session, lazily restoring a saved one after a
// restart. Caller holds manualMu.
func manualSessionLocked() *scoring.Session {
	if manualSession == nil && !manualLoadAttempt {
		manualLoadAttempt = true
		if fileExists(ManualScoringFile) {
			s, err := scoring.LoadSession(ManualScoringFile)
			if err != nil {
				say("  ⚠  Manual scoring: saved session unreadable (%v) — ignoring it", err)
			} else {
				manualSession = s
				c := s.Current()
				say("  ✓  Manual scoring: resumed saved session — %s %d-%d (%s ov), innings %d",
					c.BattingName, c.Total, c.Wkts, c.OversStr(), s.InningsNo())
			}
		}
	}
	return manualSession
}

// ManualTodayEnv: the launcher sets it on the server it starts when match day was told
// the match is scored by hand, so /live is the scorer's feed before the match is set up.
const ManualTodayEnv = "CRICKETSTREAM_MANUAL_TODAY"

func manualToday() bool { return os.Getenv(ManualTodayEnv) == "1" }

// ManualLiveState is the overlay state from the manual session, or nil when not active.
// Rendered through ParsePCSJSON so it is EXACTLY what the scorer's-file path would give.
func ManualLiveState() M {
	manualMu.Lock()
	s := manualSessionLocked()
	if s == nil {
		manualMu.Unlock()
		return nil
	}
	frame := s.Current().Frame(s.InningsNo())
	manualMu.Unlock()
	return ParsePCSJSON(FrameToM(frame))
}

// ManualSessionActive: a live manual session outranks the scorer's file in /live, so
// feed-freshness reporting must consult it too — a stale or absent file is not a problem
// while the overlay is fed manually.
func ManualSessionActive() bool {
	manualMu.Lock()
	defer manualMu.Unlock()
	return manualSessionLocked() != nil
}

// ManualUIState is everything the /scoring page needs to draw itself.
func ManualUIState() M {
	manualMu.Lock()
	defer manualMu.Unlock()
	s := manualSessionLocked()
	if s == nil {
		return M{"active": false}
	}
	inn := s.Current()
	bowler := inn.BowlerForOver(inn.CurrentOverNo())
	var arrived any // the auto-arrived new batter who hasn't faced a ball yet
	for _, b := range []*scoring.Batter{inn.Striker, inn.NonStriker} {
		idx := -1
		for i, x := range inn.Batters {
			if x == b {
				idx = i
			}
		}
		if b.HowOut == "" && b.Balls == 0 && b.Runs == 0 && inn.NextBat > 2 && idx == inn.NextBat-1 {
			arrived = b.Name
		}
	}
	var target, need any
	if inn.Target != nil {
		target = *inn.Target
		if *inn.Target != 0 {
			need = max(0, *inn.Target-inn.Total)
		}
	}
	var lastOver any
	if n := len(inn.TokenHistory); n > 0 {
		lastOver = M{"num": len(inn.OverHistory), "runs": inn.OverHistory[len(inn.OverHistory)-1],
			"balls": append([]string{}, inn.TokenHistory[n-1].Tokens...)}
	}
	yet := []string{}
	if inn.NextBat < len(inn.Batters) {
		for _, b := range inn.Batters[inn.NextBat:] {
			if b.HowOut == "" {
				yet = append(yet, b.Name)
			}
		}
	}
	return M{
		"active": true, "innings": s.InningsNo(),
		"batting": inn.BattingName, "bowling": inn.BowlingName,
		"score": inn.Total, "wickets": inn.Wkts, "overs": inn.OversStr(), "max_overs": inn.MaxOvers,
		"striker":     M{"name": inn.Striker.Name, "runs": inn.Striker.Runs, "balls": inn.Striker.Balls},
		"non_striker": M{"name": inn.NonStriker.Name, "runs": inn.NonStriker.Runs, "balls": inn.NonStriker.Balls},
		"bowler": M{"name": bowler.Name,
			"figs": fmt.Sprintf("%s-%d-%d-%d", bowler.OversStr(), bowler.Maidens, bowler.Runs, bowler.Wkts)},
		"this_over":        append([]string{}, inn.OverTokensNow...),
		"target":           target,
		"need":             need,
		"innings_complete": inn.Complete(),
		"match_over":       s.MatchOver(),
		"awaiting_bowler":  inn.AwaitingNewOver(),
		// Recap of the just-completed over for the scorer's end-of-over banner: confirm
		// and pick the next bowler, or undo back into the over to fix a ball.
		"last_over":   lastOver,
		"new_batter":  arrived,
		"yet_to_bat":  yet,
		"bowling_xi":  append([]string{}, inn.Fielders...),
		"last_wicket": inn.LastWicket.HowOut,
		"events":      len(s.Events),
	}
}
