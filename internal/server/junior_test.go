package server

// Junior match mode (junior.go): no child's surname reaches the stream, the database or
// anything made from it, and the graphics about one named child are off.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func juniorFixture(t *testing.T, extra M) *pcsFixture {
	t.Helper()
	f := newPCSFixture(t)
	st := M{"junior_mode": true, "roster": M{"7": "Patrick Edwards", "9": "Jack Stone", "11": "Jack Smith"}}
	for k, v := range extra {
		st[k] = v
	}
	setState(t, st)
	juniorMu.Lock()
	jBook = juniorBook{}
	juniorMu.Unlock()
	t.Cleanup(func() {
		juniorMu.Lock()
		jBook = juniorBook{}
		juniorMu.Unlock()
	})
	return f
}

// liveState is the "state" of a /live/view read (no side effects) or the overlay's /live.
func liveState(t *testing.T, f *pcsFixture, path string) M {
	t.Helper()
	st, d := f.getJSON(path)
	mustEq(t, st, 200)
	s, _ := d["state"].(M)
	if s == nil {
		t.Fatalf("%s: no state: %v", path, d)
	}
	return s
}

func TestJuniorNamesOnTheFeed(t *testing.T) {
	f := juniorFixture(t, nil)
	f.write(0, "batter1_name", "EDWARDS", "batter1_number", "7", // roster by shirt number
		"batter2_name", "T Hatton", // no first name anywhere: initials
		"bowler_name", "Oliver Marsh", // the feed has the first name
		"last_wicket_batter", "STONE 12", "last_wicket_bowler", "Marsh", "last_wicket_fielder", "Dunn",
		"last_wicket_howout", "c Dunn b O Marsh",
		"card_b1_name", "STONE", "card_b1_runs", "12", "card_b1_balls", "20", "card_b1_out", "c Dunn b Marsh",
		"card_b2_name", "EDWARDS", "card_b2_runs", "8", "card_b2_balls", "10", "card_b2_out", "not out",
		"card_w1_name", "Oliver Marsh", "card_w1_o", "2", "card_w1_r", "9", "card_w1_w", "1")
	s := liveState(t, f, "/live/view")
	name := func(k string) string { return gs(gm(s, k), "name", "") }
	mustEq(t, name("batter1"), "Patrick E")
	mustEq(t, name("batter2"), "T H")
	mustEq(t, name("bowler"), "Oliver M")
	mustEq(t, gs(s, "lastWicketBowler", ""), "Oliver M") // the bowler on this frame
	mustEq(t, gs(s, "lastWicketFielder", ""), "D")       // nobody knows his first name
	mustEq(t, gs(s, "lastWicketHowOut", ""), "c D b Oliver M")
	// One Stone on the roster: his first name; and the score after it is kept.
	mustEq(t, gs(s, "lastWicketBatter", ""), "Jack S 12")
	card := gm(s, "card")
	b1, b2 := gl(card, "batters")[0].(M), gl(card, "batters")[1].(M)
	mustEq(t, gs(b1, "name", ""), "Jack S")
	mustEq(t, gs(b1, "out", ""), "c D b Oliver M")
	mustEq(t, gs(b2, "name", ""), "Patrick E")
	mustEq(t, gs(b2, "out", ""), "not out")
	mustEq(t, gs(gl(card, "bowlers")[0].(M), "name", ""), "Oliver M")

	// Nothing in the whole answer holds a surname.
	raw := string(f.get("/live/view").body)
	for _, surname := range []string{"EDWARDS", "Edwards", "Hatton", "Marsh", "STONE", "Stone", "Dunn"} {
		if strings.Contains(raw, surname) {
			t.Errorf("/live/view still holds %q", surname)
		}
	}
	// The team names, the score and the ticker are untouched.
	mustEq(t, gs(s, "battingTeamName", ""), "Home CC")
	mustEq(t, gs(s, "last_ball", ""), "1 4 .")
	mustEq(t, gi(s, "score", 0), 12)
}

// Jack Stone and Jack Smith are both "Jack S": each gets as much surname as tells them
// apart, and keeps it for the match whatever order they turn up in afterwards.
func TestJuniorNamesThatWouldClash(t *testing.T) {
	f := juniorFixture(t, nil)
	f.write(0, "batter1_name", "STONE", "batter1_number", "9", "batter2_name", "SMITH", "batter2_number", "11")
	s := liveState(t, f, "/live/view")
	a, b := gs(gm(s, "batter1"), "name", ""), gs(gm(s, "batter2"), "name", "")
	if a == b || !strings.HasPrefix(a, "Jack S") || !strings.HasPrefix(b, "Jack S") {
		t.Fatalf("two players share a label or lost their first name: %q and %q", a, b)
	}
	if strings.Contains(a, "Stone") || strings.Contains(b, "Smith") {
		t.Fatalf("a whole surname was used to tell them apart: %q, %q", a, b)
	}
	// Swapped ends, and after a restart (the labels are on disk): the same labels.
	juniorMu.Lock()
	jBook = juniorBook{}
	juniorMu.Unlock()
	f.write(0, "batter1_name", "SMITH", "batter1_number", "11", "batter2_name", "STONE", "batter2_number", "9", "runs", "13")
	s = liveState(t, f, "/live/view")
	mustEq(t, gs(gm(s, "batter1"), "name", ""), b)
	mustEq(t, gs(gm(s, "batter2"), "name", ""), a)
	// The file holds the labels, for this match only.
	raw, err := os.ReadFile(juniorNamesFile())
	if err != nil {
		t.Fatal(err)
	}
	var saved juniorBook
	json.Unmarshal(raw, &saved)
	if saved.Match != CurrentMatchID() || saved.Labels["full:jackstone"] != a || saved.Labels["full:jacksmith"] != b {
		t.Errorf("junior_names.json: %s", raw)
	}
}

// Two brothers with no roster entry: a bare surname in a dismissal is neither of them.
func TestJuniorSurnameThatCouldBeTwoPeople(t *testing.T) {
	f := juniorFixture(t, M{"roster": M{}})
	f.write(0, "batter1_name", "A Reed", "batter2_name", "B Reed", "last_wicket_fielder", "Reed")
	s := liveState(t, f, "/live/view")
	mustEq(t, gs(gm(s, "batter1"), "name", ""), "A R")
	mustEq(t, gs(gm(s, "batter2"), "name", ""), "B R")
	mustEq(t, gs(s, "lastWicketFielder", ""), "R")
}

// The swap is made before anything is logged: the database never holds a surname either.
func TestJuniorNamesAreWhatGetsLogged(t *testing.T) {
	f := juniorFixture(t, nil)
	f.write(0, "batter1_name", "EDWARDS", "batter1_number", "7", "batter2_name", "T Hatton", "bowler_name", "Oliver Marsh")
	liveState(t, f, "/live") // the overlay's poll: this one logs
	f.write(0, "batter1_name", "EDWARDS", "batter1_number", "7", "batter2_name", "T Hatton", "bowler_name", "Oliver Marsh",
		"runs", "16", "overs", "2.4", "batter1_runs", "12", "batter1_balls", "11", "last_ball", "1 4 . 4")
	liveState(t, f, "/live")
	facts, _ := json.Marshal(MatchFactsFromDB(CurrentMatchID()))
	card, _ := json.Marshal(MatchScorecard())
	for what, text := range map[string]string{"the match facts": string(facts), "the scorecard": string(card)} {
		if !strings.Contains(text, "Patrick E") {
			t.Errorf("%s don't have the batter at all: %s", what, text)
		}
		for _, surname := range []string{"EDWARDS", "Edwards", "Hatton", "Marsh"} {
			if strings.Contains(text, surname) {
				t.Errorf("%s hold %q", what, surname)
			}
		}
	}
}

func TestJuniorModeOffChangesNothing(t *testing.T) {
	f := juniorFixture(t, M{"junior_mode": false})
	f.write(0, "batter1_name", "EDWARDS", "batter1_number", "7", "last_wicket_howout", "c Dunn b Marsh")
	s := liveState(t, f, "/live/view")
	mustEq(t, gs(gm(s, "batter1"), "name", ""), "EDWARDS")
	mustEq(t, gs(s, "lastWicketHowOut", ""), "c Dunn b Marsh")
	if _, err := os.Stat(juniorNamesFile()); err == nil {
		t.Error("a names file was written with junior mode off")
	}
}

func TestJuniorPlainNames(t *testing.T) {
	for in, want := range map[string]string{
		"Patrick Edwards": "Patrick E", "patrick edwards": "Patrick E", "Mary-Jane O'Neil": "Mary-Jane O",
		"J Smith": "J S", "J. Smith": "J S", "SMITH": "S", "Jack van der Berg": "Jack B", "": "",
	} {
		if got := juniorPlain(in); got != want {
			t.Errorf("juniorPlain(%q) = %q, want %q", in, got, want)
		}
	}
	got := juniorNames([]any{M{"name": "Jack Smith", "captain": true}, M{"name": "T Hatton"}})
	mustEq(t, gs(got[0].(M), "name", ""), "Jack S")
	mustEq(t, got[0].(M)["captain"], any(true))
	mustEq(t, gs(got[1].(M), "name", ""), "T H")
	h := juniorH2H(M{"meetings": []any{M{"our": M{"top_bat": "Jack Smith 45*", "best_bowl": "T Hatton 3-12", "total": "150/6"}}}}).(M)
	our := gm(gl(h, "meetings")[0].(M), "our")
	mustEq(t, gs(our, "top_bat", ""), "Jack S 45*")
	mustEq(t, gs(our, "best_bowl", ""), "T H 3-12")
	mustEq(t, gs(our, "total", ""), "150/6")
}

func TestJuniorDismissals(t *testing.T) {
	newTestServer(t, "")
	b := &juniorBook{Match: "m", Labels: map[string]string{}, loaded: true}
	known := []string{"Jack Stone", "Oliver Marsh"}
	by := map[string]string{"marsh": "Oliver M"}
	for in, want := range map[string]string{
		"c Dunn b Marsh":            "c D b Oliver M",
		"b Marsh":                   "b Oliver M",
		"lbw b Marsh":               "lbw b Oliver M",
		"st Dunn b Stone":           "st D b Jack S",
		"run out (Dunn)":            "run out (D)",
		"run out (T Dunn/Marsh)":    "run out (T D/Oliver M)",
		"c & b Marsh":               "c & b Oliver M",
		"c sub b Marsh":             "c sub b Oliver M",
		"not out":                   "not out",
		"retired hurt":              "retired hurt",
		"did not bat":               "did not bat",
		"hit wicket b O'Neil-Smith": "hit wicket b O",
		"":                          "",
	} {
		if got := b.dismissal(in, known, by); got != want {
			t.Errorf("dismissal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksJunior(t *testing.T) {
	for _, n := range []string{"Under 13", "U15 A", "Bridestowe Colts", "Juniors", "Girls U11", "Kwik Cricket", "U-9"} {
		if !LooksJunior("1st XI", n) {
			t.Errorf("%q isn't seen as a junior side", n)
		}
	}
	for _, n := range []string{"1st XI", "2nd XI", "Sunday XI", "Devon League Division 2", "Midweek T20", ""} {
		if LooksJunior(n) {
			t.Errorf("%q is seen as a junior side", n)
		}
	}
}

// What the mode switches off on the server, whatever each feature's own switch says.
func TestJuniorModeSwitchesOffTheRest(t *testing.T) {
	newTestServer(t, "")
	on := M{"junior_mode": true, "anthropic_api_key": "sk-test", "camera_spotter": true, "camera_spotter_minutes": 1,
		"graphics_club_milestones": true, "youtube_highlights": true}
	setState(t, on)
	calls := noModel(t)
	GenerateOverCommentary(3, 9, "Oliver Marsh", "1-9", "1 4 . W 2 2", M{"score": 30, "wickets": 1, "overs": 3.0})
	waitBackground(t)
	mustEq(t, *calls, 0) // no AI commentary

	// The overlay's half: the same list, in one place.
	overlay := page(t, "overlay.html")
	apply := extractFunction(t, overlay, "applyJuniorMode")
	for _, off := range []string{"graphics_player_card", "graphics_club_milestones", "graphics_season_leaders",
		"graphics_commentary_over", "graphics_voice_commentary"} {
		if !strings.Contains(apply, "c."+off+" = false") {
			t.Errorf("the overlay leaves %s on in junior mode", off)
		}
	}
	if strings.Contains(apply, "replay") {
		t.Error("junior mode touches replays: they stay on")
	}
	if !strings.Contains(extractFunction(t, overlay, "poll"), "cfg=applyJuniorMode(") {
		t.Error("the overlay's settings don't go through applyJuniorMode")
	}
	var got map[string]any
	runJS(t, apply+`
var c = applyJuniorMode({junior_mode:true, graphics_player_card:true, graphics_voice_commentary:true, replay_enabled:true, graphics_fow:true});
var d = applyJuniorMode({junior_mode:false, graphics_player_card:true});
process.stdout.write(JSON.stringify({card:c.graphics_player_card, voice:c.graphics_voice_commentary, replay:c.replay_enabled, fow:c.graphics_fow, off:d.graphics_player_card}));`, &got)
	eqv(t, got, map[string]any{"card": false, "voice": false, "replay": true, "fow": true, "off": true})

	// The panel has the switch and saves it; it's off unless someone (or the fixture) says.
	if !strings.Contains(page(t, "control.html"), `id="junior_mode"`) {
		t.Error("no Junior match switch in the panel")
	}
	mustEq(t, DefaultState()["junior_mode"], any(false))
}

// Line-ups come from PlayCricket with full names: relabelled on the way out.
func TestJuniorLineups(t *testing.T) {
	s := newTestServer(t, "")
	setState(t, M{"junior_mode": true, "playcricket_api_key": "k", "pc_match_id": "99"})
	lineupsMu.Lock()
	saved, savedKey, savedAt := lineupsOut, lineupsKey, lineupsAt
	lineupsOut = M{"ok": true, "home": M{"team": "Home CC", "players": []any{M{"name": "Patrick Edwards", "captain": true}}},
		"away": M{"team": "Rival CC", "players": []any{M{"name": "Tom Hatton"}}}}
	lineupsKey, lineupsAt = "99", time.Now()
	lineupsMu.Unlock()
	t.Cleanup(func() {
		lineupsMu.Lock()
		lineupsOut, lineupsKey, lineupsAt = saved, savedKey, savedAt
		lineupsMu.Unlock()
	})
	r := s.get("/lineups")
	mustEq(t, r.status, 200)
	body := string(r.body)
	if !strings.Contains(body, "Patrick E") || !strings.Contains(body, "Tom H") || strings.Contains(body, "Edwards") || strings.Contains(body, "Hatton") {
		t.Errorf("/lineups in junior mode: %s", body)
	}
	// And they're where the feed's bare surnames get their first names.
	eqv(t, lineupNamesCached(), []string{"Patrick Edwards", "Tom Hatton"})
	setState(t, M{"junior_mode": false})
	if !strings.Contains(string(s.get("/lineups").body), "Patrick Edwards") {
		t.Error("/lineups lost its names with junior mode off")
	}
}

// Today's fixture turns it on, and a later fetch never turns it off.
func TestFixtureSwitchesJuniorModeOn(t *testing.T) {
	s := newTestServer(t, "")
	saved := FetchTodaysMatch
	t.Cleanup(func() { FetchTodaysMatch = saved })
	FetchTodaysMatch = func(key, club string) M {
		return M{"home_team": "Under 13", "away_team": "Under 13 A", "away_club": "Rival CC", "competition": "West Devon Youth League"}
	}
	st, _ := s.getJSON("/match/fetch")
	mustEq(t, st, 200)
	mustEq(t, LoadState()["junior_mode"], any(true))
	FetchTodaysMatch = func(key, club string) M {
		return M{"home_team": "1st XI", "away_team": "1st XI", "away_club": "Rival CC", "competition": "Devon League"}
	}
	s.getJSON("/match/fetch")
	mustEq(t, LoadState()["junior_mode"], any(true)) // someone has to decide it's off
}

// The end-of-over "best score of the season" is one named child's record too.
func TestJuniorModeSkipsSeasonBestAtTheOver(t *testing.T) {
	extras := extractFunction(t, page(t, "overlay.html"), "overExtras")
	if !strings.Contains(extras, "cfg.junior_mode ? [] : ['batter1', 'batter2']") {
		t.Error("overExtras looks up a batter's season best in junior mode")
	}
}
