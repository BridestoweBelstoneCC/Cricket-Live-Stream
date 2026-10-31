package server

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/pyval"
)

// fakePC is PlayCricket: season lists by year, scorecards by match id; it counts calls.
type fakePC struct {
	mu      sync.Mutex
	seasons map[int][]any
	details map[string]M
	calls   []string
}

func (f *fakePC) get(u string, _ time.Duration) (M, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := url.Parse(u)
	q := p.Query()
	switch {
	case strings.HasSuffix(p.Path, "/matches.json"):
		f.calls = append(f.calls, "season "+q.Get("season"))
		var y int
		fmt.Sscan(q.Get("season"), &y)
		return M{"matches": f.seasons[y]}, nil
	case strings.HasSuffix(p.Path, "/match_detail.json"):
		f.calls = append(f.calls, "detail "+q.Get("match_id"))
		if d, ok := f.details[q.Get("match_id")]; ok {
			return M{"match_details": []any{d}}, nil
		}
		return M{"match_details": []any{}}, nil
	}
	return nil, fmt.Errorf("unexpected %s", u)
}

func (f *fakePC) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Our club 100 (1st XI = 11, 2nd XI = 12); theirs 200 (1st XI = 21, 2nd XI = 22).
func fixture(id, date string, homeClub, homeTeam, awayClub, awayTeam int) M {
	return M{"id": id, "match_date": date, "home_club_id": fmt.Sprint(homeClub), "home_team_id": fmt.Sprint(homeTeam),
		"away_club_id": fmt.Sprint(awayClub), "away_team_id": fmt.Sprint(awayTeam), "ground_name": "The Rec"}
}

func innings(teamID int, team string, runs, wkts int, overs string, bat []any, bowl []any) M {
	return M{"team_batting_id": fmt.Sprint(teamID), "team_batting_name": team, "runs": fmt.Sprint(runs),
		"wickets": fmt.Sprint(wkts), "overs": overs, "bat": bat, "bowl": bowl}
}

func h2hFake(t *testing.T, password ...string) *fakePC {
	t.Helper()
	sandbox(t, strings.Join(password, ""))
	UpdateState(func(s M) {
		s["playcricket_api_key"], s["home_club_id"], s["away_club_id"], s["pc_match_id"] = "k", "100", "200", "T"
		s["home_team"], s["away_team"] = "Home CC", "Rivals CC"
	})
	now := time.Date(2026, 6, 6, 10, 0, 0, 0, time.Local)
	savedNow, savedGet, savedSleep := h2hNow, pcGetJSON, pcSleep
	t.Cleanup(func() {
		h2hNow, pcGetJSON, pcSleep = savedNow, savedGet, savedSleep
		h2hMu.Lock()
		h2hResult, h2hKey = nil, ""
		h2hMu.Unlock()
	})
	h2hNow, pcSleep = func() time.Time { return now }, func(time.Duration) {}
	f := &fakePC{seasons: map[int][]any{
		2026: {fixture("T", "06/06/2026", 100, 11, 200, 21), // today: not a past meeting
			fixture("A", "09/05/2026", 200, 21, 100, 11),  // 1st XIs, away: we lost
			fixture("X", "10/05/2026", 100, 12, 200, 22),  // the 2nd XIs: not today's teams
			fixture("O", "16/05/2026", 100, 11, 300, 31)}, // someone else
		2025: {fixture("B", "14/06/2025", 100, 11, 200, 21), // we won
			fixture("C", "02/08/2025", 200, 21, 100, 11)}, // drawn
		2021: {fixture("D", "01/07/2021", 100, 11, 200, 21)}, // abandoned
		2019: {fixture("E", "01/07/2019", 100, 11, 200, 21)}, // too long ago
	}, details: map[string]M{
		"A": {"result": "W", "result_applied_to": "21", "result_description": "Rivals CC won by 4 wickets",
			"innings": []any{
				innings(11, "Home CC", 187, 9, "40", []any{M{"batsman_name": "Jo Smith", "runs": "64", "how_out": "ct"}, M{"batsman_name": "Al Brown", "runs": "12", "how_out": "not out"}},
					[]any{M{"bowler_name": "Raj Khan", "wickets": "3", "runs": "28"}, M{"bowler_name": "Tom Lee", "wickets": "3", "runs": "40"}}),
				innings(21, "Rivals CC", 188, 6, "38.2", []any{M{"batsman_name": "Sam Cole", "runs": "71", "how_out": "not out"}},
					[]any{M{"bowler_name": "Ali Patel", "wickets": "2", "runs": "35"}})}},
		"B": {"result": "W", "result_applied_to": "11", "result_description": "Home CC won by 30 runs", "innings": []any{}},
		"C": {"result": "D", "result_description": "Match drawn", "innings": []any{}},
		"D": {"result": "A", "result_description": "Abandoned", "innings": []any{}},
	}}
	pcGetJSON = f.get
	return f
}

func waitH2H(t *testing.T, force bool) M {
	t.Helper()
	HeadToHead(force)
	for i := 0; i < 200; i++ {
		if r := HeadToHead(false); r["ready"] == true {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("never built")
	return nil
}

func TestHeadToHead(t *testing.T) {
	f := h2hFake(t)
	r := waitH2H(t, false)
	if r["usable"] != true {
		t.Fatalf("%v", r)
	}
	rec := r["record"].(M)
	if rec["played"] != 3 || rec["won"] != 1 || rec["lost"] != 1 || rec["drawn"] != 1 || rec["no_result"] != 0 {
		// D (abandoned, no scorecard) isn't counted: nothing was played.
		t.Fatalf("record %v", rec)
	}
	last := r["last"].(M)
	if last["date"] != "09/05/2026" || last["outcome"] != "L" || last["result"] != "Rivals CC won by 4 wickets" {
		t.Fatalf("last %v", last)
	}
	our, their := last["our"].(M), last["their"].(M)
	// Our bowler's figures are in THEIR innings; theirs in ours. Ties on wickets go to
	// fewer runs. All out isn't "/10".
	if our["total"] != "187/9" || our["top_bat"] != "Jo Smith 64" || our["best_bowl"] != "Ali Patel 2-35" {
		t.Fatalf("our %v", our)
	}
	if their["total"] != "188/6" || their["top_bat"] != "Sam Cole 71*" || their["best_bowl"] != "Raj Khan 3-28" {
		t.Fatalf("their %v", their)
	}
	if f.count("season 2019") != 0 {
		t.Error("looked back further than six seasons")
	}
	if f.count("detail X") != 0 || f.count("detail O") != 0 || f.count("detail T") != 0 {
		t.Error("read a game that isn't a past meeting of today's teams")
	}

	// Built again (a new day, or forced): past seasons and scorecards come from the cache;
	// only this season's list is asked for again.
	seasons, details := f.count("season"), f.count("detail")
	waitH2H(t, true)
	if f.count("season") != seasons+1 || f.count("season 2026") != 2 || f.count("detail") != details {
		t.Fatalf("calls after a rebuild: %v", f.calls)
	}
}

func TestHeadToHeadWithoutAnOpposition(t *testing.T) {
	h2hFake(t)
	UpdateState(func(s M) { s["away_club_id"] = "" })
	if r := waitH2H(t, false); r["usable"] != false || !strings.Contains(fmt.Sprint(r["reason"]), "opposition") {
		t.Fatalf("%v", r)
	}
}

func TestHeadToHeadFirstMeeting(t *testing.T) {
	h2hFake(t)
	UpdateState(func(s M) { s["away_club_id"] = "300" })
	r := waitH2H(t, false)
	if r["usable"] != false || !strings.Contains(fmt.Sprint(r["reason"]), "No meetings") {
		t.Fatalf("%v", r)
	}
}

func TestH2HRoutes(t *testing.T) {
	h2hFake(t, "pw")
	ts := serve(t)
	waitH2H(t, false)
	// Open, like /league/table: the overlay reads it, and nothing in it is secret.
	if r := ts.get("/h2h"); r.status != 200 || !strings.Contains(string(r.body), `"usable":true`) {
		t.Fatalf("GET /h2h: %d %s", r.status, r.body)
	}
	if r := ts.get("/h2h?force=1"); r.status != 401 && r.status != 403 {
		t.Fatalf("a forced rebuild needs the login: %d", r.status)
	}
	if r := ts.post("/h2h/show", M{}); r.status != 401 && r.status != 403 {
		t.Fatalf("showing it needs the login: %d", r.status)
	}
	if code, _ := ts.postJSON("/h2h/show", M{}, ts.bearer("pw")); code != 200 {
		t.Fatalf("show: %d", code)
	}
	if !pyval.Truthy(popCommands()["show_h2h"]) {
		t.Fatal("the overlay wasn't told")
	}
}
