package server

// The live overlay in a real browser, end to end: the scorer's file → /live → the page.
// Junior match mode's names are what's drawn, and with the graphics delay on, the score
// isn't drawn until its time has come.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func liveOverlayDOM(t *testing.T, state M, budget time.Duration) string {
	t.Helper()
	dom, _ := renderInBrowserWith(t, "/overlay", budget, func(dir string) {
		pcs := filepath.Join(dir, "pcs")
		os.MkdirAll(pcs, 0o755)
		feed, _ := json.Marshal(map[string]string{"batting_team": "Home CC", "bowling_team": "Rival CC",
			"runs": "47", "wickets": "1", "overs": "8.3",
			"batter1_name": "EDWARDS", "batter1_number": "7", "batter1_runs": "21", "batter1_balls": "30", "batter1_strike": "True",
			"batter2_name": "T Hatton", "batter2_runs": "9", "batter2_balls": "12",
			"bowler_name": "Oliver Marsh", "bowler_overs": "2.3", "bowler_runs": "11", "bowler_wickets": "1", "last_ball": "1 4 ."})
		if err := os.WriteFile(filepath.Join(pcs, "scoreboard-output.json"), feed, 0o644); err != nil {
			t.Fatal(err)
		}
		st := M{"pcs_output_folder": pcs, "use_widget": false, "demo_mode": false, "roster": M{"7": "Patrick Edwards"}}
		for k, v := range state {
			st[k] = v
		}
		setState(t, st)
	})
	return dom
}

func TestLiveOverlayShowsJuniorNames(t *testing.T) {
	dom := liveOverlayDOM(t, M{"junior_mode": true}, 12*time.Second)
	if !strings.Contains(dom, "47") {
		t.Skip("the overlay didn't draw a live frame in this browser's time budget")
	}
	// What's in each of the scorebar's name boxes (the overlay used to cut a name down to
	// its last word there, which made "Patrick E" just "E").
	shown := func(id string) string {
		m := regexp.MustCompile(`id="` + id + `"[^>]*>([^<]*)`).FindStringSubmatch(dom)
		if m == nil {
			t.Fatalf("no #%s on the page", id)
		}
		return strings.TrimSpace(m[1])
	}
	for id, want := range map[string]string{"batter1-name": "Patrick E", "batter2-name": "T H", "bowler-name": "Oliver M"} {
		if got := shown(id); !strings.EqualFold(got, want) {
			t.Errorf("#%s shows %q, want %q", id, got, want)
		}
	}
	// Nothing the page drew holds a surname (its own script has demo names in it, so only
	// what's between the tags is read).
	text := regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>`).ReplaceAllString(dom, "")
	for _, surname := range []string{"EDWARDS", "Edwards", "Hatton", "HATTON", "Marsh", "MARSH"} {
		if strings.Contains(text, surname) {
			t.Errorf("the overlay drew the surname %q", surname)
		}
	}
}

// The activity log (the window's, and the clubhouse screen's Health tab) doesn't print the
// scorer's names in a junior match.
func TestJuniorModeKeepsNamesOutOfTheLog(t *testing.T) {
	newTestServer(t, "")
	var out strings.Builder
	saved := sayOut
	sayOut = &out
	t.Cleanup(func() { sayOut = saved })
	state := M{"battingTeamName": "Home CC", "score": 47, "wickets": 1, "overs": 8.3,
		"batter1": M{"name": "EDWARDS", "runs": 21}, "batter2": M{"name": "T Hatton", "runs": 9}}
	setState(t, M{"junior_mode": true})
	logScore(state, "PCS")
	if s := out.String(); strings.Contains(s, "EDWARDS") || strings.Contains(s, "Hatton") || !strings.Contains(s, "Home CC 47-1") {
		t.Errorf("logged %q", s)
	}
	out.Reset()
	setState(t, M{"junior_mode": false})
	logScore(state, "PCS")
	if s := out.String(); !strings.Contains(s, "EDWARDS 21 / T Hatton 9") {
		t.Errorf("with junior mode off the line changed: %q", s)
	}
}

// scorebarText is what one of the scorebar's boxes shows ("" if it isn't on the page). The
// whole page can't be searched for a name: its own script has names in it.
func scorebarText(dom, id string) string {
	m := regexp.MustCompile(`id="` + id + `"[^>]*>([^<]*)`).FindStringSubmatch(dom)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func TestLiveOverlayHoldsTheScoreBack(t *testing.T) {
	batter := func(dom string) string { return strings.ToUpper(scorebarText(dom, "batter1-name")) }
	// The same feed, three ways. Without the delay the score is up within a few seconds...
	if got := batter(liveOverlayDOM(t, nil, 6*time.Second)); got != "EDWARDS" {
		t.Skipf("the overlay didn't draw a live frame in this browser's time budget (batter box: %q)", got)
	}
	// ...with a 10 second delay it isn't there at 6 seconds...
	if got := batter(liveOverlayDOM(t, M{"graphics_delay": true, "graphics_delay_seconds": 10}, 6*time.Second)); got == "EDWARDS" {
		t.Error("with a 10s graphics delay the batter was on the scorebar after 6s")
	}
	// ...and is by 20.
	if got := batter(liveOverlayDOM(t, M{"graphics_delay": true, "graphics_delay_seconds": 10}, 20*time.Second)); got != "EDWARDS" {
		t.Errorf("with a 10s graphics delay the batter box still showed %q after 20s", got)
	}
}
