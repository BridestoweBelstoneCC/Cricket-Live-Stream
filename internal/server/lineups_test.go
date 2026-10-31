package server

import (
	"strings"
	"testing"
	"time"
)

func TestTodaysLineups(t *testing.T) {
	sandbox(t, "")
	UpdateState(func(s M) {
		s["playcricket_api_key"], s["pc_match_id"], s["home_team"], s["away_team"] = "k", "T", "Home CC", "Rivals CC"
	})
	calls := 0
	saved := pcGetJSON
	t.Cleanup(func() {
		pcGetJSON = saved
		lineupsMu.Lock()
		lineupsKey, lineupsOut = "", nil
		lineupsMu.Unlock()
	})
	pcGetJSON = func(u string, _ time.Duration) (M, error) {
		calls++
		if !strings.Contains(u, "match_id=T") {
			t.Errorf("asked for %s", u)
		}
		return M{"match_details": []any{M{"players": []any{
			M{"home_team": []any{M{"player_name": "Jo Smith", "captain": true}, M{"player_name": "Sam Hatton", "wicket_keeper": true},
				M{"player_name": " "}}},
			M{"away_team": []any{}},
		}}}}, nil
	}
	r := TodaysLineups()
	home := r["home"].(M)
	ps := home["players"].([]any)
	if r["ok"] != true || home["team"] != "Home CC" || len(ps) != 2 ||
		ps[0].(M)["captain"] != true || ps[1].(M)["keeper"] != true || ps[0].(M)["keeper"] != false {
		t.Fatalf("%v", r)
	}
	if away := r["away"].(M); away["team"] != "Rivals CC" || len(away["players"].([]any)) != 0 {
		t.Fatalf("a side not entered yet: %v", away)
	}
	TodaysLineups()
	if calls != 1 {
		t.Fatalf("asked PlayCricket %d times in five minutes", calls)
	}
}

func TestTodaysLineupsWithoutAMatch(t *testing.T) {
	sandbox(t, "")
	if r := TodaysLineups(); r["ok"] != false {
		t.Fatalf("%v", r)
	}
}
