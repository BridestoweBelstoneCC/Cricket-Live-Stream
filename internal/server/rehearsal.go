package server

// Practice match: the control panel's "Practice match" plays a simulated match through
// the real overlay — scorebar, wicket cards, boundaries, replays, the end-of-over
// sequence — so a club can rehearse in OBS without a scorer. It's the simulator
// (`cricketstream simulate --configure`) built in, with the clean-up the command line
// left to the operator:
//
//   - the score source is pointed at the simulator's folder and put back afterwards
//     (saved in state first, so a crash mid-practice is undone at the next start);
//   - the practice is logged under its own match id and its rows are deleted at the end,
//     so it can never be merged into, or mistaken for, a real match;
//   - it refuses while the stream is live, or while a manual scoring session is on.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cricketstream/internal/simulate"
)

// rehearsalBallSec is the pace: seconds per delivery (tests make it quick).
var rehearsalBallSec = 2.5

var (
	rehearsalMu sync.Mutex
	rehearsal   struct {
		running  bool
		stop     chan struct{}
		done     chan struct{}
		matchID  string
		scenario string
		frame    int
		total    int
		score    string
		started  float64
	}
)

// rehearsalMatchID is the practice's own match id while one runs ("" otherwise).
func rehearsalMatchID() string {
	rehearsalMu.Lock()
	defer rehearsalMu.Unlock()
	if rehearsal.running {
		return rehearsal.matchID
	}
	return ""
}

// RehearsalStatus is GET /rehearsal.
func RehearsalStatus() M {
	rehearsalMu.Lock()
	defer rehearsalMu.Unlock()
	out := M{"running": rehearsal.running, "scenarios": simulate.ScenarioNames()}
	if rehearsal.running {
		out["scenario"], out["frame"], out["total"] = rehearsal.scenario, rehearsal.frame, rehearsal.total
		out["score"], out["elapsed"] = rehearsal.score, int(now()-rehearsal.started)
	}
	return out
}

// StartRehearsal begins a practice match.
func StartRehearsal(scenario string, overs int) error {
	if scenario == "" {
		scenario = "full"
	}
	if overs <= 0 || overs > 50 {
		overs = 5
	}
	if _, ok := simulate.Scenarios[scenario]; !ok {
		return fmt.Errorf("unknown practice %q", scenario)
	}
	streamMonMu.Lock()
	live := gb(streamMon, "streaming", false)
	streamMonMu.Unlock()
	if live {
		return errors.New("Not while the stream is live — a practice match would go out to viewers.")
	}
	if ManualSessionActive() {
		return errors.New("A manual scoring session is on — it would outrank the practice feed. Finish or reset it first.")
	}
	m, err := simulate.NewMatch(scenario, overs, time.Now().UnixNano(), true)
	if err != nil {
		return err
	}
	frames := m.Frames()
	folder := filepath.Join(appDir, "sim_pcs_output")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}

	rehearsalMu.Lock()
	if rehearsal.running {
		rehearsalMu.Unlock()
		return errors.New("A practice match is already running.")
	}
	rehearsal.running = true
	rehearsal.stop, rehearsal.done = make(chan struct{}), make(chan struct{})
	rehearsal.matchID = "practice_" + time.Now().Format("20060102_150405")
	rehearsal.scenario, rehearsal.frame, rehearsal.total, rehearsal.score = scenario, 0, len(frames), ""
	rehearsal.started = now()
	stop, done := rehearsal.stop, rehearsal.done
	rehearsalMu.Unlock()

	// Point the feed at the simulator, keeping what to put back (in state, so a crash
	// mid-practice is undone at the next start).
	UpdateState(func(s M) {
		s["rehearsal_restore"] = M{"pcs_output_folder": s["pcs_output_folder"], "pcs_source": s["pcs_source"],
			"pcs_bridge_url": s["pcs_bridge_url"], "demo_mode": s["demo_mode"], "max_overs": s["max_overs"]}
		// Demo mode makes the overlay play its own made-up data and never read the feed.
		s["pcs_output_folder"], s["pcs_source"], s["pcs_bridge_url"], s["demo_mode"] = folder, "local", "", false
		s["max_overs"] = overs
	})
	resetFeedPipeline()
	say("  ▶  Practice match started (%s, %d overs)", scenario, overs)

	path := filepath.Join(folder, simulate.OutputFilename)
	// Seconds per frame in deliveries: a short look at the pre-match graphics, a pause at
	// the innings break and the result.
	pace := map[string]float64{"prematch": 1, "prematch2": 1, "break": 3, "end": 2, "ball": 1}
	safeGo("practice match", func() {
		defer close(done)
		defer endRehearsal()
		for i, fr := range frames {
			b, _ := json.Marshal(fr.Data)
			tmp := path + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil {
				os.Rename(tmp, path)
			}
			rehearsalMu.Lock()
			rehearsal.frame = i + 1
			rehearsal.score = fmt.Sprintf("%s-%s (%s ov)", fr.Data["runs"], fr.Data["wickets"], fr.Data["overs"])
			rehearsalMu.Unlock()
			p, ok := pace[fr.Label]
			if !ok {
				p = 1
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(p * rehearsalBallSec * float64(time.Second))):
			}
		}
		// Leave the result on screen a little before handing back.
		select {
		case <-stop:
		case <-time.After(time.Duration(4 * rehearsalBallSec * float64(time.Second))):
		}
	})
	return nil
}

// StopRehearsal ends a practice match early and waits for the clean-up.
func StopRehearsal() bool {
	rehearsalMu.Lock()
	if !rehearsal.running {
		rehearsalMu.Unlock()
		return false
	}
	stop, done := rehearsal.stop, rehearsal.done
	rehearsalMu.Unlock()
	select {
	case <-stop:
	default:
		close(stop)
	}
	<-done
	return true
}

// endRehearsal puts the score source back and removes every trace of the practice.
func endRehearsal() {
	rehearsalMu.Lock()
	mid := rehearsal.matchID
	rehearsalMu.Unlock()
	restoreAfterRehearsal()
	if mid != "" {
		deleteMatchRows(mid)
	}
	rehearsalMu.Lock()
	rehearsal.running, rehearsal.matchID = false, ""
	rehearsalMu.Unlock()
	resetFeedPipeline()
	say("  ■  Practice match over — the score source is back as it was")
}

// restoreAfterRehearsal puts back what a practice changed, if anything is waiting to be
// put back (at the end of a practice, or at start-up after one was cut short).
func restoreAfterRehearsal() bool {
	restored := false
	UpdateState(func(s M) {
		r, ok := s["rehearsal_restore"].(M)
		if !ok {
			return
		}
		for _, k := range []string{"pcs_output_folder", "pcs_source", "pcs_bridge_url", "demo_mode", "max_overs"} {
			if v, had := r[k]; had && v != nil {
				s[k] = v
			} else {
				delete(s, k)
			}
		}
		delete(s, "rehearsal_restore")
		restored = true
	})
	return restored
}

// RecoverInterruptedRehearsal runs at start-up.
func RecoverInterruptedRehearsal() {
	if restoreAfterRehearsal() {
		say("  ✓  A practice match was cut short last time — the score source is back as it was")
	}
}

// deleteMatchRows removes one match's rows from every match table.
func deleteMatchRows(mid string) {
	withDB(func(tx *sql.Tx) error {
		for _, tbl := range append(append([]string{}, matchTables...), "matches") {
			if _, err := tx.Exec("DELETE FROM "+tbl+" WHERE match_id=?", mid); err != nil {
				return err
			}
		}
		return nil
	})
}

// resetFeedPipeline forgets everything the per-poll pipeline remembers about the feed,
// so the practice starts (and the real match resumes) from a clean slate.
func resetFeedPipeline() {
	ResetPCSCache()
	SetInningsLatch(1)
	ResetEventDetection()
	ResetBallLog()
	ResetLiveFigures()
	MatchLogReset()
}
