package scoring

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Differential test against the Python original: random deliveries go through both
// engines and every frame must match key-for-key. Skipped unless the upstream clone
// (.upstream/, see PORTING.md) and a Python 3 interpreter are present.

const pyDriver = `
import json, sys
sys.path.insert(0, sys.argv[1])
import scoring_engine as se
XI_A = [(str(i), f"Alan Alpha{i}") for i in range(1, 12)]
XI_B = [(str(i), f"Bob Beta{i}") for i in range(1, 12)]
out = []
for script in json.load(sys.stdin):
    e = se.InningsEngine(XI_A, XI_B, "Alpha CC", "Beta CC", script["overs"],
                         target=script["target"], openers_selected=script["openers"])
    frames = [e.frame(1)]
    for o in script["balls"]:
        if o.get("bowler"):
            e.set_bowler(o["bowler"])
        if e.complete:
            break
        e.apply_outcome(o["kind"], runs=o["runs"], wicket_type=o["wt"],
                        fielder=o["fielder"], out_non_striker=o["ons"])
        frames.append(e.frame(1))
    out.append(frames)
json.dump(out, sys.stdout)
`

type diffBall struct {
	Kind    string `json:"kind"`
	Runs    int    `json:"runs"`
	WT      string `json:"wt"`
	Fielder string `json:"fielder"`
	ONS     bool   `json:"ons"`
	Bowler  string `json:"bowler,omitempty"`
}

type diffScript struct {
	Overs   int        `json:"overs"`
	Target  *int       `json:"target"`
	Openers bool       `json:"openers"`
	Balls   []diffBall `json:"balls"`
}

func findPython() string {
	for _, c := range []string{"python3", "python", "py"} {
		if p, err := exec.LookPath(c); err == nil {
			if exec.Command(p, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
				return p
			}
		}
	}
	return ""
}

func TestDifferentialAgainstPython(t *testing.T) {
	upstream, _ := filepath.Abs("../../.upstream")
	if _, err := os.Stat(filepath.Join(upstream, "scoring_engine.py")); err != nil {
		t.Skip("no upstream clone")
	}
	py := findPython()
	if py == "" {
		t.Skip("no python")
	}
	rng := rand.New(rand.NewSource(7))
	kinds := []string{"dot", "dot", "dot", "1", "1", "2", "3", "4", "6", "W", "wide", "noball", "bye", "legbye"}
	var scripts []diffScript
	for n := 0; n < 60; n++ {
		s := diffScript{Overs: 1 + rng.Intn(20), Openers: rng.Intn(5) != 0}
		if rng.Intn(2) == 0 {
			t := 20 + rng.Intn(150)
			s.Target = &t
		}
		for i := 0; i < 160; i++ {
			b := diffBall{Kind: kinds[rng.Intn(len(kinds))], WT: WicketTypes[rng.Intn(len(WicketTypes))]}
			switch b.Kind {
			case "wide", "noball":
				b.Runs = []int{0, 0, 1, 2, 4, 6}[rng.Intn(6)]
			case "bye", "legbye":
				b.Runs = []int{0, 1, 2, 4}[rng.Intn(4)]
			}
			if rng.Intn(2) == 0 {
				b.Fielder = xiB[rng.Intn(11)].Name
			}
			b.ONS = b.WT == "run out" && rng.Intn(2) == 0
			if rng.Intn(25) == 0 {
				b.Bowler = xiB[rng.Intn(11)].Name
			}
			s.Balls = append(s.Balls, b)
		}
		scripts = append(scripts, s)
	}
	in, _ := json.Marshal(scripts)
	cmd := exec.Command(py, "-I", "-c", pyDriver, upstream)
	cmd.Stdin = bytesReader(in)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("python driver: %v", err)
	}
	var want [][]map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for si, s := range scripts {
		e := NewInnings(xiA, xiB, "Alpha CC", "Beta CC", s.Overs, s.Target, s.Openers)
		got := []map[string]string{e.Frame(1)}
		for _, b := range s.Balls {
			if b.Bowler != "" {
				e.SetBowler(b.Bowler)
			}
			if e.Complete() {
				break
			}
			if _, err := e.ApplyOutcome(Outcome{Kind: b.Kind, Runs: b.Runs, WicketType: b.WT,
				Fielder: b.Fielder, OutNonStriker: b.ONS}); err != nil {
				t.Fatalf("script %d: %v", si, err)
			}
			got = append(got, e.Frame(1))
		}
		if len(got) != len(want[si]) {
			t.Fatalf("script %d: %d frames, python %d", si, len(got), len(want[si]))
		}
		for fi := range got {
			if !reflect.DeepEqual(got[fi], want[si][fi]) {
				for k, v := range want[si][fi] {
					if got[fi][k] != v {
						t.Errorf("script %d frame %d: %s = %q, python %q", si, fi, k, got[fi][k], v)
					}
				}
				t.FailNow()
			}
		}
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
