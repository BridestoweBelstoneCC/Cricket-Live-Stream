package server

import (
	"strings"
	"testing"
	"time"
)

func quickRehearsal(t *testing.T, ballSec float64) {
	t.Helper()
	saved := rehearsalBallSec
	rehearsalBallSec = ballSec
	t.Cleanup(func() {
		StopRehearsal()
		rehearsalBallSec = saved
	})
}

func rehearsalRunning() bool { r, _ := RehearsalStatus()["running"].(bool); return r }

func countRows(t *testing.T, mid string) int {
	t.Helper()
	n := 0
	for _, tbl := range append(append([]string{}, matchTables...), "matches") {
		n += gi(M{"n": queryRows(t, "SELECT COUNT(*) FROM "+tbl+" WHERE match_id=?", mid)[0][0]}, "n", 0)
	}
	return n
}

// The practice plays through the real pipeline (the overlay's /live), under its own
// match id, then puts the score source back and leaves no trace in the ball database.
func TestPracticeMatchPlaysThenCleansUp(t *testing.T) {
	s := newTestServer(t, "")
	quickRehearsal(t, 0.01)
	setState(t, M{"pcs_output_folder": `C:\real\scorer\Output`, "pcs_source": "agent", "pcs_bridge_url": "http://100.1.2.3:5050",
		"demo_mode": true})
	h := map[string]string{"Origin": "http://" + s.addr}
	st, d := s.postJSON("/rehearsal", M{"action": "start", "scenario": "full", "overs": 1}, h)
	mustEq(t, st, 200)
	if d["running"] != true {
		t.Fatal(d)
	}
	mid := CurrentMatchID()
	if !strings.HasPrefix(mid, "practice_") {
		t.Fatalf("practice logged under %q", mid)
	}
	if st := LoadState(); st["pcs_source"] != "local" || st["pcs_bridge_url"] != "" || st["demo_mode"] != false {
		t.Errorf("feed not pointed at the simulator: %v %v", st["pcs_source"], st["pcs_bridge_url"])
	}
	maxScore, rowsSeen := 0, 0
	deadline := time.Now().Add(60 * time.Second)
	for rehearsalRunning() && time.Now().Before(deadline) {
		body := s.get("/live").json(t) // the overlay's poll drives the loggers
		if st, _ := body["state"].(M); st != nil && gi(st, "score", 0) > maxScore {
			maxScore = gi(st, "score", 0)
		}
		if n := countRows(t, mid); n > rowsSeen {
			rowsSeen = n
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rehearsalRunning() {
		t.Fatal("practice never finished")
	}
	if maxScore == 0 || rowsSeen == 0 {
		t.Errorf("the overlay never saw it: max score %d, rows %d", maxScore, rowsSeen)
	}
	st2 := LoadState()
	if st2["pcs_output_folder"] != `C:\real\scorer\Output` || st2["pcs_source"] != "agent" || st2["pcs_bridge_url"] != "http://100.1.2.3:5050" ||
		st2["demo_mode"] != true {
		t.Errorf("score source not put back: %v %v %v", st2["pcs_output_folder"], st2["pcs_source"], st2["pcs_bridge_url"])
	}
	if _, left := st2["rehearsal_restore"]; left {
		t.Error("restore note left in state")
	}
	if n := countRows(t, mid); n != 0 {
		t.Errorf("%d practice rows left in the database", n)
	}
	if strings.HasPrefix(CurrentMatchID(), "practice_") {
		t.Error("still logging under the practice id")
	}
}

func TestPracticeMatchStopsEarlyAndPutsThingsBack(t *testing.T) {
	s := newTestServer(t, "")
	quickRehearsal(t, 5) // slow: it's stopped long before the end
	setState(t, M{"pcs_output_folder": "/real"})
	h := map[string]string{"Origin": "http://" + s.addr}
	if st, _ := s.postJSON("/rehearsal", M{"action": "start"}, h); st != 200 {
		t.Fatal(st)
	}
	if st, _ := s.postJSON("/rehearsal", M{"action": "start"}, h); st != 409 {
		t.Errorf("second practice: %d", st)
	}
	st, d := s.postJSON("/rehearsal", M{"action": "stop"}, h)
	mustEq(t, st, 200)
	mustEq(t, d["running"], false)
	mustEq(t, LoadState()["pcs_output_folder"], "/real")
}

func TestPracticeMatchRefusedWhileLiveOrManual(t *testing.T) {
	s := newTestServer(t, "")
	quickRehearsal(t, 5)
	streamMonMu.Lock()
	saved := streamMon["streaming"]
	streamMon["streaming"] = true
	streamMonMu.Unlock()
	t.Cleanup(func() { streamMonMu.Lock(); streamMon["streaming"] = saved; streamMonMu.Unlock() })
	st, d := s.postJSON("/rehearsal", M{"action": "start"}, map[string]string{"Origin": "http://" + s.addr})
	mustEq(t, st, 409)
	if !strings.Contains(str(d, "error", ""), "live") {
		t.Errorf("error %v", d["error"])
	}
	if rehearsalRunning() {
		t.Fatal("started while live")
	}
}

func TestPracticeMatchNeedsLoginAndAKnownScenario(t *testing.T) {
	s := newTestServer(t, "pw")
	quickRehearsal(t, 5)
	if st, _ := s.getJSON("/rehearsal"); st != 401 {
		t.Errorf("status without login: %d", st)
	}
	h := s.bearer("pw")
	h["Origin"] = "http://" + s.addr
	if st, _ := s.postJSON("/rehearsal", M{"action": "start", "scenario": "nonsense"}, h); st != 409 {
		t.Errorf("unknown scenario: %d", st)
	}
	if st, _ := s.postJSON("/rehearsal", M{"action": "dance"}, h); st != 400 {
		t.Errorf("unknown action: %d", st)
	}
	_, d := s.getJSON("/rehearsal", s.bearer("pw"))
	if sc, _ := d["scenarios"].([]any); len(sc) < 4 {
		t.Errorf("scenarios %v", d["scenarios"])
	}
}

// Cut short by a crash: the next start puts the score source back.
func TestInterruptedPracticeIsUndoneAtStart(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"pcs_output_folder": "/sim", "pcs_source": "local",
		"rehearsal_restore": M{"pcs_output_folder": "/real", "pcs_source": "agent", "pcs_bridge_url": nil}})
	RecoverInterruptedRehearsal()
	st := LoadState()
	mustEq(t, st["pcs_output_folder"], "/real")
	mustEq(t, st["pcs_source"], "agent")
	if _, left := st["rehearsal_restore"]; left {
		t.Error("restore note left")
	}
}
