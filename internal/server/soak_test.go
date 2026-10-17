package server

// Full-match soak (tests/test_soak.py): drive a COMPLETE simulated match through the
// real HTTP server — every frame written to a fake scorer's folder and polled via /live
// like the overlay would, with panel-style /live/view and /state noise mixed in — then
// reconcile the ball-by-ball database against the engine's own book, ball for ball,
// over for over, both innings. The integration-drift catcher: it fails on the old ball
// logger that dropped every over's final delivery.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cricketstream/internal/simulate"
)

func TestWholeMatchReconcilesAgainstTheEngine(t *testing.T) {
	t.Run("stale ticker", func(t *testing.T) { soak(t, true) }) // what NV Play really does
	t.Run("clearing ticker", func(t *testing.T) { soak(t, false) })
}

func soak(t *testing.T, staleTicker bool) {
	f := newPCSFixture(t)
	match, err := simulate.NewMatch("full", 5, 21, staleTicker)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.pcsDir, "nvplay-scoreboard1.xml")
	base := time.Now().Add(-4000 * time.Second)
	n := 0
	for _, fr := range match.Frames() {
		n++
		b, _ := json.Marshal(fr.Data)
		os.WriteFile(path, b, 0o644)
		mt := base.Add(time.Duration(n) * time.Second)
		os.Chtimes(path, mt, mt)
		if body := f.live(); body["source"] != "pcs" { // the overlay's poll
			t.Fatal(body["source"])
		}
		if n%7 == 0 { // panel noise, like match day
			f.getJSON("/live/view")
			f.getJSON("/state")
		}
	}
	if n <= 40 {
		t.Fatalf("only %d frames — a whole match should be many", n)
	}
	inn1, inn2 := match.Innings[0], match.Innings[1]
	if !inn1.Complete() || !inn2.Complete() {
		t.Fatal("match didn't finish")
	}
	for no, inn := range map[int]*simulate.Innings{1: inn1, 2: inn2} {
		r := queryRows(t, "SELECT COALESCE(SUM(runs),0), COALESCE(SUM(is_wicket),0), COALESCE(SUM(legal),0) FROM balls WHERE innings=?", no)[0]
		eqv(t, r[0], inn.Total, "innings ", no, ": DB runs != engine total")
		eqv(t, r[1], inn.Wkts, "innings ", no, ": DB wickets != engine wickets")
		eqv(t, r[2], inn.LegalBalls, "innings ", no, ": DB legal balls != engine")
		// every COMPLETED over must reconcile individually
		for over, runs := range inn.OverHistory {
			got := queryRows(t, "SELECT COALESCE(SUM(runs),0) FROM balls WHERE innings=? AND over=?", no, over)[0][0]
			eqv(t, got, runs, "innings ", no, " over ", over+1, " runs")
		}
	}
	// The match log's fall-of-wickets must have seen every wicket in both innings
	matchLogMu.Lock()
	fow := len(matchLog.FallOfWickets)
	matchLogMu.Unlock()
	mustEq(t, fow, inn1.Wkts+inn2.Wkts)
	// ...and the innings latch delivered the right innings numbers throughout
	mustEq(t, rowsStr(queryRows(t, "SELECT DISTINCT innings FROM balls ORDER BY innings")), "[[1] [2]]")
}
