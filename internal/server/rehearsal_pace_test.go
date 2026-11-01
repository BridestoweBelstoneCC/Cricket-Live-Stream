package server

import (
	"testing"

	"cricketstream/internal/simulate"
)

// A practice is paced like a match: a wicket or a boundary leaves its replay time to
// play out before the next ball, and the end of an over leaves time for its graphics.
// At 2.5s a ball the next delivery came while the replay was still on screen.
func TestPracticeLeavesTimeForReplays(t *testing.T) {
	m, err := simulate.NewMatch("full", 10, 7, true)
	if err != nil {
		t.Fatal(err)
	}
	s := M{"replay_enabled": true, "replay_duration": 18}
	var prev simulate.Frame
	seen := map[string]bool{}
	for _, fr := range m.Frames() {
		if fr.Label != "ball" || prev.Data == nil || prev.Data["innings"] != fr.Data["innings"] {
			prev = fr
			continue
		}
		gap := practiceGap(prev, fr, s)
		runs := atoiOr(fr.Data["runs"]) - atoiOr(prev.Data["runs"])
		wkts := atoiOr(fr.Data["wickets"]) - atoiOr(prev.Data["wickets"])
		overEnd := gap-realBallSec == 15 || gap-realBallSec-15 == 6+18+5 || gap-realBallSec-15 == 6+18+5+20
		switch {
		case wkts > 0:
			seen["wicket"] = true
			if gap < realBallSec+6+18+5+20 {
				t.Errorf("wicket at %s: %.0fs — no time for its replay and the new batter", fr.Data["overs"], gap)
			}
		case runs >= 4:
			seen["boundary"] = true
			if gap < realBallSec+6+18+5 {
				t.Errorf("boundary at %s: %.0fs — no time for its replay", fr.Data["overs"], gap)
			}
		case overEnd:
			seen["over"] = true
		case gap != realBallSec:
			t.Errorf("ordinary ball at %s (%d runs): %.0fs, want %.0f", fr.Data["overs"], runs, gap, realBallSec)
		}
		prev = fr
	}
	for _, k := range []string{"wicket", "boundary", "over"} {
		if !seen[k] {
			t.Errorf("no %s in the practice — the test proves nothing about it", k)
		}
	}

	// How long each length takes (the control panel quotes it), over a few seeds.
	for _, overs := range []int{2, 5, 10} {
		total := 0.0
		for seed := int64(1); seed <= 5; seed++ {
			m, _ := simulate.NewMatch("full", overs, seed, true)
			var p simulate.Frame
			for _, fr := range m.Frames() {
				total += practiceGap(p, fr, s)
				p = fr
			}
		}
		t.Logf("%d overs a side: about %.0f minutes", overs, total/5/60)
	}

	// With camera 2 doing side-on replays and the walk-out, a wicket waits for both.
	s2 := M{"replay_enabled": true, "replay_duration": 18, "camera2_rtsp_url": "rtsp://x", "replay_side_on": true,
		"camera_walkout": true, "camera_walkout_hold": 35}
	a := simulate.Frame{Label: "ball", Data: map[string]string{"innings": "1", "runs": "10", "wickets": "0", "overs": "1.2"}}
	b := simulate.Frame{Label: "ball", Data: map[string]string{"innings": "1", "runs": "10", "wickets": "1", "overs": "1.3"}}
	if got, want := practiceGap(a, b, s2), realBallSec+(6+18+5)+(18+5)+35; got != want {
		t.Errorf("two-camera wicket: %.0fs, want %.0f", got, want)
	}
}
