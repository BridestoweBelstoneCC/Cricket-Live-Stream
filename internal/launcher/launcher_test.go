package launcher

// tests/test_quickstart.py: the state merge that stops match-day runs wiping panel
// state, the fixture lookup, and the crash-restart loop driven with fake processes.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

func stubShutdown(t *testing.T) *[]string {
	t.Helper()
	var reports []string
	sr, so, sa, sd, sl := offerMatchReport, resumeOBSUpdates, serverAnswers, serverDown, leaveReplay
	t.Cleanup(func() {
		offerMatchReport, resumeOBSUpdates, serverAnswers, serverDown, leaveReplay = sr, so, sa, sd, sl
	})
	serverAnswers = func() bool { return true } // never the real port 5000
	offerMatchReport = func(tok string) { reports = append(reports, tok) }
	// Shutdown puts OBS's update check back — in the REAL OBS folder. Never from a test.
	resumeOBSUpdates = func() {}
	// The same for this: left alone, it would talk to whatever OBS is on this machine.
	leaveReplay = func() (string, bool) { return "Main", false }
	return &reports
}

// retrySoon shortens the wait between tries; capture collects what's printed.
func retrySoon(t *testing.T, every time.Duration) {
	t.Helper()
	old := serverRetryEvery
	t.Cleanup(func() { serverRetryEvery = old })
	serverRetryEvery = every
}

func capture(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() { b, _ := io.ReadAll(r); out <- string(b) }()
	done := false
	finish := func() string {
		if done {
			return ""
		}
		done = true
		os.Stdout = old
		w.Close()
		return <-out
	}
	t.Cleanup(func() { finish() })
	return finish
}

func TestKeepsTryingAfterTheQuickRestartsRunOut(t *testing.T) {
	reports := stubShutdown(t)
	retrySoon(t, time.Millisecond)
	var downs []bool
	serverDown = func(d bool) { downs = append(downs, d) }
	var sleeps []time.Duration
	sig := make(chan os.Signal, 1)
	launched := 0
	launch := func() (Proc, error) {
		if launched++; launched == 20 {
			sig <- os.Interrupt
			return newFake(false), nil
		}
		return newFake(true), nil
	}
	said := capture(t)
	code := RunServerWithRestarts(newFake(true), launch, "", 3, 0, func(d time.Duration) { sleeps = append(sleeps, d) }, sig)
	text := said()
	if code != 0 || launched != 20 {
		t.Fatal("stopped trying", code, launched)
	}
	if len(sleeps) != 3 { // the quick restarts back off; the rest wait a minute instead
		t.Fatal(len(sleeps))
	}
	if n := strings.Count(text, "keeps crashing"); n != 1 { // one post to Discord, not one a minute
		t.Fatalf("said %d times:\n%s", n, text)
	}
	if n := strings.Count(text, "Server stopped unexpectedly"); n != 3 {
		t.Fatalf("restart notices %d:\n%s", n, text)
	}
	if len(downs) != 1 || !downs[0] {
		t.Fatal(downs)
	}
	if len(*reports) != 0 { // that last server never answered: nothing to ask for a report
		t.Fatal(*reports)
	}
}

// A replay on air has nothing to end it while the server's down. It's looked at only when no
// server is running, and again at each try, in case OBS didn't answer the first time.
func TestAReplayIsCutBackFromOnceTheQuickRestartsRunOut(t *testing.T) {
	stubShutdown(t)
	retrySoon(t, time.Millisecond)
	var launched, looks, tooEarly atomic.Int32
	leaveReplay = func() (string, bool) {
		if launched.Load() < 3 {
			tooEarly.Add(1)
		}
		looks.Add(1)
		return "Main", false
	}
	sig := make(chan os.Signal, 1)
	launch := func() (Proc, error) {
		if launched.Add(1) == 8 {
			sig <- os.Interrupt
			return newFake(false), nil
		}
		return newFake(true), nil
	}
	RunServerWithRestarts(newFake(true), launch, "", 3, 0, func(time.Duration) {}, sig)
	for w := 0; w < 200 && looks.Load() < 2; w++ {
		time.Sleep(10 * time.Millisecond) // they run on their own goroutines
	}
	if looks.Load() < 2 {
		t.Fatalf("looked %d times; once per try was the plan", looks.Load())
	}
	if tooEarly.Load() != 0 {
		t.Fatal("looked during the quick restarts: the next server was seconds away, and its own watch does this")
	}
}

// A frozen OBS can take seconds to not answer, and Stop has to work meanwhile.
func TestAFrozenOBSDoesntHoldUpStop(t *testing.T) {
	stubShutdown(t)
	retrySoon(t, time.Hour)
	release := make(chan struct{})
	defer close(release)
	sig := make(chan os.Signal, 1)
	serverDown = func(d bool) {
		if d {
			sig <- os.Interrupt
		}
	}
	leaveReplay = func() (string, bool) { <-release; return "Main", false }
	done := make(chan int)
	go func() {
		done <- RunServerWithRestarts(newFake(true), func() (Proc, error) { return newFake(true), nil }, "", 3, 0, func(time.Duration) {}, sig)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop waited for OBS")
	}
}

func TestCuttingBackFromAReplayIsSaidAndCantStopMatchDay(t *testing.T) {
	said := capture(t)
	cutBackFromReplay(func() (string, bool) { return "Main", true })
	cutBackFromReplay(func() (string, bool) { return "Main", false })
	cutBackFromReplay(func() (string, bool) { panic("no") })
	text := said()
	if n := strings.Count(text, "cut back to Main"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, text)
	}
	// These lines become Discord posts, once per crash loop or restart: not this one.
	if strings.Contains(text, "keeps crashing") || strings.Contains(text, "Server stopped unexpectedly") {
		t.Fatal(text)
	}
}

func TestStopWorksWhileWaitingToTryAgain(t *testing.T) {
	reports := stubShutdown(t)
	retrySoon(t, time.Hour)
	resumed := false
	resumeOBSUpdates = func() { resumed = true }
	sig := make(chan os.Signal, 1)
	serverDown = func(d bool) {
		if d {
			sig <- os.Interrupt
		}
	}
	launched := 0
	done := make(chan int)
	go func() {
		done <- RunServerWithRestarts(newFake(true), func() (Proc, error) { launched++; return newFake(true), nil }, "", 3, 0, func(time.Duration) {}, sig)
	}()
	select {
	case code := <-done:
		if code != 0 || launched != 3 || !resumed || len(*reports) != 0 {
			t.Fatal(code, launched, resumed, *reports)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop didn't end the wait")
	}
}

func TestServerComingBackIsNoticedAndTheCountStartsAgain(t *testing.T) {
	reports := stubShutdown(t)
	retrySoon(t, time.Millisecond)
	every, settled := hungCheckEvery, serverSettledAfter
	t.Cleanup(func() { hungCheckEvery, serverSettledAfter = every, settled })
	hungCheckEvery, serverSettledAfter = time.Millisecond, 200*time.Millisecond
	var mu sync.Mutex
	var downs []bool
	sig := make(chan os.Signal, 1)
	serverDown = func(d bool) { mu.Lock(); downs = append(downs, d); mu.Unlock() }
	launched := 0
	good := newFake(false)
	launch := func() (Proc, error) {
		if launched++; launched == 4 { // past the 3 quick restarts: this one stays up
			go func() {
				time.Sleep(500 * time.Millisecond) // past serverSettledAfter
				sig <- os.Interrupt
			}()
			return good, nil
		}
		return newFake(true), nil
	}
	said := capture(t)
	RunServerWithRestarts(newFake(true), launch, "tok", 3, 0, func(time.Duration) {}, sig)
	text := said()
	if len(downs) != 2 || !downs[0] || downs[1] {
		t.Fatal(downs)
	}
	if !strings.Contains(text, "Server's been running again") {
		t.Fatal(text)
	}
	if !good.terminated {
		t.Fatal("left it running")
	}
	eq(t, *reports, []string{"tok"}, "the server's back, so the report's offered")
}

func TestCrashesHoursApartDontUseUpTheCap(t *testing.T) {
	stubShutdown(t)
	retrySoon(t, time.Hour)
	var downs []bool
	serverDown = func(d bool) { downs = append(downs, d) }
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
	if len(downs) != 0 || launched != 6 {
		t.Fatal("slowed down on crashes hours apart", downs, launched)
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

// One that hangs before its first answer is hung, not starting, once it's had long enough.
func TestServerThatNeverAnswersIsRestarted(t *testing.T) {
	stubShutdown(t)
	every, limit := hungCheckEvery, hungStartLimit
	t.Cleanup(func() { hungCheckEvery, hungStartLimit = every, limit })
	hungCheckEvery, hungStartLimit = time.Millisecond, 20*time.Millisecond
	serverAnswers = func() bool { return false }
	hung := newFake(false)
	killed := make(chan struct{})
	sig := make(chan os.Signal, 1)
	go func() { <-killed; close(hung.exit) }() // a kill ends the process
	restarted := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunServerWithRestarts(&killOnce{fakeProc: hung, killed: killed}, func() (Proc, error) {
			restarted++
			hungStartLimit = time.Hour // the new one's fine
			sig <- os.Interrupt
			return newFake(false), nil
		}, "", 3, 0, func(time.Duration) {}, sig)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a server that never answered was left running")
	}
	if !hung.kill || restarted != 1 {
		t.Fatal(hung.kill, restarted)
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
	reports := stubShutdown(t)
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
