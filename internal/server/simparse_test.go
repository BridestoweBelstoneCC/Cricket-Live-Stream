package server

// The simulator's frames through the real feed parser (the parser-consistency half of
// tests/test_simulator.py): every frame parses, the innings latches 1 → 2 exactly once,
// and the winning-runs frames don't unlatch it.

import (
	"testing"

	"cricketstream/internal/simulate"
)

func simFrames(t *testing.T) []simulate.Frame {
	t.Helper()
	m, err := simulate.NewMatch("full", 6, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	return m.Frames()
}

func asM(d map[string]string) M {
	out := M{}
	for k, v := range d {
		out[k] = v
	}
	return out
}

func TestEverySimulatorFrameParses(t *testing.T) {
	withLatch(t)
	for _, f := range simFrames(t) {
		st := ParsePCSJSON(asM(f.Data))
		if st == nil {
			t.Fatalf("%s: didn't parse", f.Label)
		}
		if gi(st, "score", -1) < 0 {
			t.Fatalf("%s: score %v", f.Label, st["score"])
		}
	}
}

func TestSimulatorInningsLatchTransitionsOnceAndHolds(t *testing.T) {
	withLatch(t)
	var seen []int
	var last []M
	for _, f := range simFrames(t) {
		st := ParsePCSJSON(asM(f.Data))
		in := gi(st, "innings", 0)
		if len(seen) == 0 || seen[len(seen)-1] != in {
			seen = append(seen, in)
		}
		last = append(last, st)
	}
	eqv(t, seen, []int{1, 2})
	// The final frames have runs_required == 0: the latch must still say innings 2.
	for _, st := range last[len(last)-3:] {
		mustEq(t, gi(st, "innings", 0), 2)
	}
}

// Every completed over's tokens, summed through the SERVER's classifier, equal the runs
// the engine credited that over — the ball-by-ball DB logs exactly what was scored.
func TestSimulatorTickerGrammarMatchesTheParser(t *testing.T) {
	m, err := simulate.NewMatch("full", 6, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	m.Frames()
	for i, inn := range m.Innings {
		if len(inn.TokenHistory) == 0 {
			t.Fatalf("innings %d: no token history", i+1)
		}
		for _, ov := range inn.TokenHistory {
			runs, legal := 0, 0
			for _, tok := range ov.Tokens {
				b := ClassifyBall(tok)
				runs += b.Runs
				if b.Legal {
					legal++
				}
			}
			if runs != ov.Runs || legal != 6 {
				t.Fatalf("innings %d over %v: parsed %d runs/%d legal, engine %d", i+1, ov.Tokens, runs, legal, ov.Runs)
			}
		}
	}
}
