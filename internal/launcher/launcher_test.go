package launcher

// tests/test_quickstart.py: the state merge that stops match-day runs wiping panel
// state, the fixture lookup, and the crash-restart loop driven with fake processes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/ini"
)

const config = `
[Club]
name = Test CC
abbreviation = TEST
home_colour = #1a3a5c
playcricket_id = 111
motto = Up the Test

[API]
playcricket_key = key
anthropic_key =

[Scoring]
pcs_output_folder =

[OBS]
obs_password =
replay_folder =

[Stream]
youtube_title = LIVE: {home} vs {away}
max_overs = 40
`

var fixture = &Match{AwayTeam: "Fixture CC", AwayAbbrev: "FIX", Competition: "League Div 1",
	CompetitionID: "12345", Umpire1: "U One", Umpire2: "U Two", Scorer1: "S One", MatchID: "999", AwayClubID: "222",
	Time: "13:00"}

func cfg(t *testing.T) *ini.File {
	t.Helper()
	f, err := ini.Parse([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// panelState is state as the control panel might have left it after a season, as it
// comes back from the JSON file.
func panelState() M {
	var m M
	json.Unmarshal([]byte(`{"roster": {"21": "Peter Smith", "7": "James Smith"},
		"sponsor_name": "Acme Builders", "sponsor_id": "3", "away_colour": "#123456",
		"away_team": "Manually Entered CC", "replay_on_fifty": false, "max_clips": 200,
		"replay_duration": 25, "graphics_player_card": true, "network_test_mbps": 18.4,
		"network_test_at": 1751000000, "home_club_id": "29434", "weather_api_key": "wk-secret-123"}`), &m)
	return m
}

func eq(t *testing.T, got, want any, what string) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("%s: got %s, want %s", what, g, w)
	}
}

func TestPanelStateSurvivesRerunWithoutFixture(t *testing.T) {
	st := BuildState(cfg(t), nil, panelState())
	// Everything the panel owns must ride through
	eq(t, st["roster"], M{"21": "Peter Smith", "7": "James Smith"}, "roster")
	eq(t, st["sponsor_name"], "Acme Builders", "sponsor_name")
	eq(t, st["sponsor_id"], "3", "sponsor_id")
	eq(t, st["away_colour"], "#123456", "away_colour")
	eq(t, st["network_test_mbps"], 18.4, "network test")
	eq(t, st["home_club_id"], "111", "config.ini owns the club's own id")
	eq(t, st["replay_on_fifty"], false, "replay_on_fifty")
	eq(t, st["max_clips"], 200, "max_clips")
	eq(t, st["replay_duration"], 25, "replay_duration")
	eq(t, st["graphics_player_card"], true, "graphics_player_card")
	// No fixture found → the manually entered opposition is kept
	eq(t, st["away_team"], "Manually Entered CC", "away_team")
	// A panel-set secret survives (a hard "" used to wipe it every match day)
	eq(t, st["weather_api_key"], "wk-secret-123", "weather key")
}

func TestFixtureDataOverridesManualEntry(t *testing.T) {
	st := BuildState(cfg(t), fixture, panelState())
	eq(t, st["away_team"], "Fixture CC", "away_team")
	eq(t, st["away_abbrev"], "FIX", "away_abbrev")
	eq(t, st["away_club_id"], "222", "away_club_id")
	eq(t, st["competition_name"], "League Div 1", "competition")
	eq(t, st["pc_match_id"], "999", "pc_match_id")
	eq(t, st["match_time"], "13:00", "match_time (the pre-match countdown)")
	eq(t, st["roster"], M{"21": "Peter Smith", "7": "James Smith"}, "roster")
	eq(t, st["sponsor_name"], "Acme Builders", "sponsor")
}

func TestSafetyDefaultsAlwaysForced(t *testing.T) {
	// However the last session left these, a fresh launch must be stream-safe
	st := BuildState(cfg(t), nil, M{"demo_mode": true, "use_widget": true, "match_url": "http://x/1"})
	eq(t, st["demo_mode"], false, "demo_mode")
	eq(t, st["use_widget"], false, "use_widget")
	eq(t, st["match_url"], "", "a pinned match is per-day")
	eq(t, st["max_overs"], 40, "config.ini wins for its own fields")
	eq(t, st["home_team"], "Test CC", "home_team")
}

func TestMissingStateStartsClean(t *testing.T) {
	eq(t, BuildState(cfg(t), nil, nil)["away_team"], "Opposition CC", "away_team")
	eq(t, BuildState(cfg(t), nil, M{})["home_team"], "Test CC", "home_team")
}

func TestStateIsJSONSerialisable(t *testing.T) {
	if _, err := json.Marshal(BuildState(cfg(t), fixture, panelState())); err != nil {
		t.Fatal(err)
	}
}

func TestFetchTodaysMatchNoAPIKey(t *testing.T) {
	m, why := FetchTodaysMatch("", "111")
	if m != nil || !strings.Contains(why, "API key") {
		t.Fatal(m, why)
	}
}

func fakePlayCricket(t *testing.T, matches []any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(M{"matches": matches})
	}))
	t.Cleanup(srv.Close)
	saved := matchesURL
	matchesURL = func(string, string, int) string { return srv.URL }
	t.Cleanup(func() { matchesURL = saved })
}

func TestFetchTodaysMatchPrefersOurHomeFixtureAndClubName(t *testing.T) {
	today := time.Now().Format("02/01/2006")
	fakePlayCricket(t, []any{
		M{"id": 1, "match_date": "01/01/2000", "home_club_id": "111", "away_club_name": "Old CC"},
		M{"id": 2, "match_date": today, "home_club_id": "999", "away_club_id": "111", "home_club_name": "Away Day CC"},
		M{"id": 3, "match_date": today, "home_club_id": "111", "away_club_id": "222",
			"away_club_name": "Heathcoat CC", "away_team_name": "1st XI", "competition_name": "Div 1",
			"competition_id": 77, "umpire_1_name": "U1", "ground_name": "The Rec"}})
	m, why := FetchTodaysMatch("k", "111")
	if m == nil {
		t.Fatal(why)
	}
	eq(t, []string{m.MatchID, m.AwayTeam, m.AwayAbbrev, m.AwayClubID, m.CompetitionID, m.Ground},
		[]string{"3", "Heathcoat CC", "HEATH", "222", "77", "The Rec"}, "match")
}

func TestFetchTodaysMatchAwayDayUsesTheHomeClub(t *testing.T) {
	today := time.Now().Format("02/01/2006")
	fakePlayCricket(t, []any{M{"id": 2, "match_date": today, "home_club_id": "999",
		"home_club_name": "Away Day Cricket Club", "away_club_id": "111"}})
	m, _ := FetchTodaysMatch("k", "111")
	eq(t, []string{m.AwayTeam, m.AwayAbbrev, m.AwayClubID}, []string{"Away Day Cricket Club", "AWAY", "999"}, "away day")
}

func TestFetchTodaysMatchNoneToday(t *testing.T) {
	fakePlayCricket(t, []any{M{"match_date": "01/01/2000"}})
	m, why := FetchTodaysMatch("k", "111")
	if m != nil || !strings.Contains(why, "No matches found for today") || !strings.Contains(why, "1 total in season") {
		t.Fatal(m, why)
	}
}

// fakeProc stands in for the server process: Wait returns when exit is closed.
type fakeProc struct {
	exit             chan struct{}
	terminated, kill bool
}

func newFake(exited bool) *fakeProc {
	p := &fakeProc{exit: make(chan struct{})}
	if exited {
		close(p.exit)
	}
	return p
}

func (p *fakeProc) Wait() error { <-p.exit; return nil }
func (p *fakeProc) Terminate() {
	p.terminated = true
	select {
	case <-p.exit:
	default:
		close(p.exit)
	}
}
func (p *fakeProc) Kill() { p.kill = true }

func stubShutdown(t *testing.T) (*[]string, *bool) {
	t.Helper()
	var reports []string
	died := false
	sd, sr, so, sa := dieFn, offerMatchReport, resumeOBSUpdates, serverAnswers
	t.Cleanup(func() { dieFn, offerMatchReport, resumeOBSUpdates, serverAnswers = sd, sr, so, sa })
	dieFn = func(string, ...string) { died = true }
	serverAnswers = func() bool { return true } // never the real port 5000
	offerMatchReport = func(tok string) { reports = append(reports, tok) }
	// Shutdown puts OBS's update check back — in the REAL OBS folder. Never from a test.
	resumeOBSUpdates = func() {}
	return &reports, &died
}

func TestRestartsUpToTheCapThenGivesUp(t *testing.T) {
	reports, died := stubShutdown(t)
	var launched []*fakeProc
	var sleeps []time.Duration
	launch := func() (Proc, error) { p := newFake(true); launched = append(launched, p); return p, nil }
	code := RunServerWithRestarts(newFake(true), launch, "", 3, 0, func(d time.Duration) { sleeps = append(sleeps, d) }, make(chan os.Signal))
	if code != 1 || !*died {
		t.Fatal("didn't give up", code)
	}
	if len(launched) != 3 || len(sleeps) != 3 { // relaunched exactly up to the cap, backing off each time
		t.Fatal(len(launched), len(sleeps))
	}
	if len(*reports) != 0 { // no Ctrl+C happened
		t.Fatal(*reports)
	}
}

func TestCrashesHoursApartDontUseUpTheCap(t *testing.T) {
	_, died := stubShutdown(t)
	clock := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	old := timeNow
	t.Cleanup(func() { timeNow = old })
	timeNow = func() time.Time { clock = clock.Add(11 * time.Minute); return clock } // each run lasts 11 minutes
	sig := make(chan os.Signal, 1)
	launched := 0
	launch := func() (Proc, error) {
		launched++
		if launched == 6 {
			sig <- os.Interrupt
			return newFake(false), nil
		}
		return newFake(true), nil
	}
	RunServerWithRestarts(newFake(true), launch, "", 3, 0, func(time.Duration) {}, sig)
	if *died || launched != 6 {
		t.Fatal("gave up on crashes hours apart", *died, launched)
	}
}

func TestHungServerIsKilledAndRestarted(t *testing.T) {
	stubShutdown(t)
	every, misses := hungCheckEvery, hungAfterMisses
	t.Cleanup(func() { hungCheckEvery, hungAfterMisses = every, misses })
	hungCheckEvery, hungAfterMisses = time.Millisecond, 3
	var mu sync.Mutex
	answers := 0
	serverAnswers = func() bool { mu.Lock(); defer mu.Unlock(); answers++; return answers == 1 } // answers once, then hangs
	hung := newFake(false)
	killed := make(chan struct{})
	hungKill := &killOnce{fakeProc: hung, killed: killed}
	sig := make(chan os.Signal, 1)
	restarted := 0
	go func() { <-killed; close(hung.exit) }() // a kill ends the process
	RunServerWithRestarts(hungKill, func() (Proc, error) {
		restarted++
		serverAnswers = func() bool { return true }
		sig <- os.Interrupt
		return newFake(false), nil
	}, "", 3, 0, func(time.Duration) {}, sig)
	if !hung.kill || restarted != 1 {
		t.Fatal("hung server not restarted", hung.kill, restarted)
	}
}

func TestStartingServerIsNotTakenForHung(t *testing.T) {
	stubShutdown(t)
	every := hungCheckEvery
	t.Cleanup(func() { hungCheckEvery = every })
	hungCheckEvery = time.Millisecond
	serverAnswers = func() bool { return false } // never answered yet
	p := newFake(false)
	sig := make(chan os.Signal, 1)
	go func() { time.Sleep(50 * time.Millisecond); sig <- os.Interrupt }()
	RunServerWithRestarts(p, func() (Proc, error) { t.Fatal("restarted"); return nil, nil }, "", 3, 0, func(time.Duration) {}, sig)
	if p.kill {
		t.Fatal("killed a server that hadn't answered yet")
	}
}

// killOnce signals the first Kill.
type killOnce struct {
	*fakeProc
	killed chan struct{}
	once   sync.Once
}

func (k *killOnce) Kill() { k.fakeProc.Kill(); k.once.Do(func() { close(k.killed) }) }

func TestCtrlCOnFirstWaitShutsDownCleanlyNoRestart(t *testing.T) {
	reports, _ := stubShutdown(t)
	launched := 0
	p := newFake(false)
	sig := make(chan os.Signal, 1)
	sig <- os.Interrupt
	RunServerWithRestarts(p, func() (Proc, error) { launched++; return newFake(false), nil }, "tok123", 3, 0, func(time.Duration) {}, sig)
	if launched != 0 || !p.terminated {
		t.Fatal(launched, p.terminated)
	}
	eq(t, *reports, []string{"tok123"}, "report offered with the token")
}

func TestCtrlCAfterRestartTerminatesTheNewProcess(t *testing.T) {
	stubShutdown(t)
	original := newFake(true) // "crashes" once
	restarted := newFake(false)
	sig := make(chan os.Signal, 1)
	RunServerWithRestarts(original, func() (Proc, error) { sig <- os.Interrupt; return restarted, nil }, "", 3, 0, func(time.Duration) {}, sig)
	if original.terminated || !restarted.terminated {
		t.Fatal(original.terminated, restarted.terminated)
	}
}
