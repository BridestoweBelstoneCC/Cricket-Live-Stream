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
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyval"
	"cricketstream/internal/simulate"
)

// rehearsalBallSec is the pace: seconds per delivery (tests make it quick). Everything
// else — a replay's time, the end of an over — scales with it.
var rehearsalBallSec = realBallSec

// realBallSec: an ordinary delivery's gap. Real cricket is nearer 30-40s; 15 keeps a
// practice short while leaving each graphic its time on screen.
const realBallSec = 15.0

// practiceGap is how long a practice frame stays before the next, in real-pace seconds:
// a ball's own gap, plus — as at a real match — the time its replay takes (the overlay's
// wicket/boundary replay: ~6s to save it, the clip, ~5s back; the side-on one and the
// walk-out when camera 2 does them) and the end of an over's graphics. Without that, the
// 2.5s-a-ball practice bowled the next ball while the replay was still on screen.
func practiceGap(prev, fr simulate.Frame, s M) float64 {
	switch fr.Label {
	// Several frames each (3 pre-match, 3 at the break, 3 at each innings' end).
	case "prematch", "prematch2":
		return 10
	case "break":
		return 30
	case "end":
		return 20
	}
	gap := realBallSec
	if prev.Data == nil || prev.Data["innings"] != fr.Data["innings"] {
		return gap
	}
	clip := float64(gi(s, "replay_duration", 18))
	cam2 := strings.TrimSpace(gs(s, "camera2_rtsp_url", "")) != ""
	runs := atoiOr(fr.Data["runs"]) - atoiOr(prev.Data["runs"])
	wkts := atoiOr(fr.Data["wickets"]) - atoiOr(prev.Data["wickets"])
	replays := pyTruthy(s["replay_enabled"], true)
	switch {
	case wkts > 0:
		if replays {
			gap += 6 + clip + 5
			if cam2 && pyTruthy(s["replay_side_on"], false) {
				gap += clip + 5
			}
		}
		if cam2 && pyTruthy(s["camera_walkout"], false) {
			gap += float64(gi(s, "camera_walkout_hold", 35))
		} else {
			gap += 20 // the new batter walking out
		}
	case runs >= 4 && replays && !extrasNotBoundary(prev, fr):
		gap += 6 + clip + 5
	}
	// "3" or "3.0": an over just finished (not "0" — nothing bowled yet).
	if whole, part, _ := strings.Cut(fr.Data["overs"], "."); atoiOr(whole) > 0 && atoiOr(part) == 0 &&
		fr.Data["overs"] != prev.Data["overs"] {
		gap += 15 // the over's summary, the bowler change
	}
	return gap
}

// extrasNotBoundary: the ball's own ticker entry shows it wasn't a plain four or six (a
// wide that ran away for four, "w+4", or a four off a no-ball, "4nb") — the overlay only
// replays a plain 4 or 6 (parseBall), so there's no replay to wait for. When
// the ticker didn't move (the over-completing write often still shows the over before the
// final ball), runs alone decide, as they did.
func extrasNotBoundary(prev, fr simulate.Frame) bool {
	if fr.Data["last_ball"] == prev.Data["last_ball"] {
		return false
	}
	toks := strings.Fields(fr.Data["last_ball"])
	if len(toks) == 0 {
		return false
	}
	last := toks[len(toks)-1]
	return last != "4" && last != "6"
}

func atoiOr(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func pyTruthy(v any, def bool) bool {
	if v == nil {
		return def
	}
	return pyval.Truthy(v)
}

var (
	rehearsalMu sync.Mutex
	// dbLogMu: /live's loggers hold it shared; the end of a practice holds it alone.
	dbLogMu   sync.RWMutex
	rehearsal struct {
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
	settings := LoadState()
	scale := rehearsalBallSec / realBallSec
	safeGo("practice match", func() {
		defer close(done)
		defer endRehearsal()
		var prev simulate.Frame
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
			gap := practiceGap(prev, fr, settings)
			prev = fr
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(gap * scale * float64(time.Second))):
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
	restoreAfterRehearsal()
	// The practice id off and its rows gone in one step that no overlay poll straddles: a
	// poll that read the id just before would otherwise write rows back after the delete.
	dbLogMu.Lock()
	rehearsalMu.Lock()
	mid := rehearsal.matchID
	rehearsal.running, rehearsal.matchID = false, ""
	rehearsalMu.Unlock()
	if mid != "" {
		deleteMatchRows(mid)
	}
	dbLogMu.Unlock()
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
