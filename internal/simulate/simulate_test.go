package simulate

// The simulator doubles as a regression harness: the engine's books must balance like a
// real scorer's, and the frames must behave like NV Play's feed in the ways that have
// caused real bugs. (The parser-consistency half of the original tests lives with the
// feed parser, which consumes these frames.)

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cricketstream/internal/scoring"
)

func runScenario(t *testing.T, name string, seed int64, overs int, stale bool) (*Match, []Frame) {
	t.Helper()
	m, err := NewMatch(name, overs, seed, stale)
	if err != nil {
		t.Fatal(err)
	}
	return m, m.Frames()
}

func checkBooks(t *testing.T, m *Match, label string) {
	t.Helper()
	for _, inn := range m.Innings {
		bat, wk := 0, 0
		for _, b := range inn.Batters {
			bat += b.Runs
			if b.HowOut != "" {
				wk++
			}
		}
		if inn.Total != bat+inn.Extras {
			t.Fatalf("%s: team total %d != batter runs %d + extras %d", label, inn.Total, bat, inn.Extras)
		}
		if inn.Wkts != wk {
			t.Fatalf("%s: wickets %d != dismissed batters %d", label, inn.Wkts, wk)
		}
		legal := 0
		for _, w := range inn.AllBowlers() {
			legal += w.Legal
		}
		if legal != inn.LegalBalls {
			t.Fatalf("%s: bowlers' legal balls %d != innings' %d", label, legal, inn.LegalBalls)
		}
	}
}

func TestMatchReachesAResultAndBooksBalance(t *testing.T) {
	m, _ := runScenario(t, "full", 7, 10, true)
	inn1, inn2 := m.Innings[0], m.Innings[1]
	if !inn1.Complete() || !inn2.Complete() {
		t.Fatal("match not finished")
	}
	checkBooks(t, m, "seed 7")
}

func TestBookkeepingHoldsAcrossSeeds(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		m, frames := runScenario(t, "full", seed, 4, true)
		if len(frames) == 0 {
			t.Fatal(seed)
		}
		checkBooks(t, m, "seed "+strconv.FormatInt(seed, 10))
	}
}

// (previous ball's frame, over-completing frame) for every X.5 -> (X+1).0.
func rollovers(frames []Frame) [][2]map[string]string {
	var out [][2]map[string]string
	var prev map[string]string
	for _, f := range frames {
		if f.Label != "ball" {
			continue
		}
		if prev != nil && f.Data["overs"] != prev["overs"] && strings.HasSuffix(f.Data["overs"], ".0") && f.Data["overs"] != "0.0" {
			out = append(out, [2]map[string]string{prev, f.Data})
		}
		prev = f.Data
	}
	return out
}

// What NV Play really does: the over-completing write still shows the ticker from before
// the final ball — the final delivery never appears in any ticker.
func TestTickerIsUnchangedOnTheOverCompletingWrite(t *testing.T) {
	_, frames := runScenario(t, "full", 11, 8, true)
	pairs := rollovers(frames)
	if len(pairs) == 0 {
		t.Fatal("no over boundaries")
	}
	for _, p := range pairs {
		if p[1]["last_ball"] != p[0]["last_ball"] || p[1]["last_ball"] == "" {
			t.Fatalf("before %q, at %q", p[0]["last_ball"], p[1]["last_ball"])
		}
	}
}

func TestClearingTickerModeStillAvailable(t *testing.T) {
	_, frames := runScenario(t, "full", 11, 8, false)
	pairs := rollovers(frames)
	if len(pairs) == 0 {
		t.Fatal("no over boundaries")
	}
	for _, p := range pairs {
		if p[1]["last_ball"] != "" {
			t.Fatal(p[1]["last_ball"])
		}
	}
}

func TestPrematchFramesHaveBlankNamesNotPlaceholders(t *testing.T) {
	_, frames := runScenario(t, "full", 11, 8, true)
	n := 0
	for _, f := range frames {
		if f.Label == "prematch" {
			n++
			if f.Data["batter1_name"] != "" {
				t.Fatal(f.Data["batter1_name"])
			}
		}
	}
	if n == 0 {
		t.Fatal("no prematch frames")
	}
}

func TestChaseStartsLiveInSecondInningsWithTarget(t *testing.T) {
	m, frames := runScenario(t, "chase", 3, 10, true)
	first := frames[0].Data
	target, _ := strconv.Atoi(first["target"])
	need, _ := strconv.Atoi(first["runs_required"])
	left, _ := strconv.Atoi(first["balls_remaining"])
	// Fast-forwarded to the tense part: within 40 runs or 30 balls (one ball on).
	if target != m.Innings[0].Total+1 || need <= 0 || (need > 41 && left > 30) {
		t.Fatal(first["target"], first["runs_required"], first["balls_remaining"])
	}
}

func TestCenturyScenarioHasABatterInTheEighties(t *testing.T) {
	_, frames := runScenario(t, "century", 5, 10, true)
	a, _ := strconv.Atoi(frames[0].Data["batter1_runs"])
	b, _ := strconv.Atoi(frames[0].Data["batter2_runs"])
	if max(a, b) < 85 {
		t.Fatal(a, b)
	}
}

func TestCollapseScenarioLosesWickets(t *testing.T) {
	_, frames := runScenario(t, "collapse", 9, 12, true)
	s, _ := strconv.Atoi(frames[0].Data["wickets"])
	e, _ := strconv.Atoi(frames[len(frames)-1].Data["wickets"])
	if e <= s {
		t.Fatal(s, e)
	}
}

func TestDeterministicForASeed(t *testing.T) {
	_, a := runScenario(t, "full", 42, 6, true)
	_, b := runScenario(t, "full", 42, 6, true)
	_, c := runScenario(t, "full", 43, 6, true)
	if !reflect.DeepEqual(a, b) || reflect.DeepEqual(a, c) {
		t.Fatal("not deterministic per seed")
	}
}

func TestUnknownScenarioRejected(t *testing.T) {
	if _, err := NewMatch("nope", 20, 1, true); err == nil {
		t.Fatal("expected an error")
	}
}

// The frame must carry exactly the fields scoreboard.template produces, so the rehearsal
// exercises the same parse paths as the real scorer's feed.
func TestFieldNamesMatchTheTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../scoreboard.template")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl map[string]any
	if err := json.Unmarshal(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	_, frames := runScenario(t, "full", 1, 6, true)
	last := frames[len(frames)-1].Data
	for k := range tmpl {
		if _, ok := last[k]; !ok {
			t.Errorf("frame lacks template field %q", k)
		}
	}
	for k := range last {
		if _, ok := tmpl[k]; !ok {
			t.Errorf("frame has field %q the template doesn't", k)
		}
	}
}

func TestWicketFieldsPopulatedAfterADismissal(t *testing.T) {
	_, frames := runScenario(t, "collapse", 9, 12, true)
	var f map[string]string
	for _, fr := range frames {
		if fr.Data["last_wicket_howout"] != "" {
			f = fr.Data
		}
	}
	if f == nil || f["last_wicket_batter"] == "" || f["last_wicket_type"] == "" {
		t.Fatal(f)
	}
	if b := f["last_wicket_bowler"]; b != "" {
		found := false
		for _, p := range append(append([]any{}, toAny(HomeXI)...), toAny(AwayXI)...) {
			if p == b {
				found = true
			}
		}
		if !found {
			t.Fatal("bowler not in either XI:", b)
		}
	}
}

func toAny(xi []scoring.Player) []any {
	var out []any
	for _, p := range xi {
		out = append(out, p.Name)
	}
	return out
}

func TestCardFieldsConsistentWithScore(t *testing.T) {
	m, frames := runScenario(t, "full", 7, 8, true)
	var f map[string]string
	for _, fr := range frames {
		if fr.Data["batting_team"] == HomeTeam {
			f = fr.Data
		}
	}
	runs, wk := 0, 0
	for i := 1; i <= 11; i++ {
		r, _ := strconv.Atoi(f["card_b"+strconv.Itoa(i)+"_runs"])
		runs += r
		if f["card_b"+strconv.Itoa(i)+"_out"] != "" {
			wk++
		}
	}
	total, _ := strconv.Atoi(f["runs"])
	wkts, _ := strconv.Atoi(f["wickets"])
	if runs+m.Innings[0].Extras != total || wk != wkts {
		t.Fatal(runs, m.Innings[0].Extras, total, wk, wkts)
	}
}
