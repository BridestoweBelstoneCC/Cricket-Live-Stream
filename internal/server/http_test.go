package server

// Integration tests against the real Handler (tests/test_http.py): routes, auth, secret
// redaction, path traversal, the origin (CSRF) check, the overlay loopback carve-out,
// and the /live pipeline including event buffering and DB logging — everything short of
// OBS/PlayCricket/AI, which need the real external services.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Auth disabled (no club_password) — the localhost default ──

func TestCorePagesServe(t *testing.T) {
	s := newTestServer(t, "")
	for _, p := range []string{"/", "/overlay", "/control"} {
		r := s.get(p)
		mustEq(t, r.status, 200, p)
		if !bytes.HasPrefix(r.body, []byte("<!DOCTYPE html")) {
			t.Fatalf("%s: %q", p, r.body[:40])
		}
	}
}

func TestControlPanelPresetsInjected(t *testing.T) {
	s := newTestServer(t, "")
	r := s.get("/control")
	mustEq(t, r.status, 200)
	for _, bad := range []string{"__KIT_PRESETS__"} {
		if bytes.Contains(r.body, []byte(bad)) {
			t.Fatal("placeholder not replaced at serve time")
		}
	}
	for _, want := range []string{`"Navy"`, "apiFetch"} {
		if !bytes.Contains(r.body, []byte(want)) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestFaviconServed(t *testing.T) {
	s := newTestServer(t, "")
	for _, p := range []string{"/favicon.ico", "/favicon.png"} {
		r := s.get(p)
		mustEq(t, r.status, 200, p)
		mustEq(t, r.header.Get("Content-Type"), "image/png", p)
		if !bytes.HasPrefix(r.body, []byte("\x89PNG")) {
			t.Fatal(p)
		}
	}
}

func TestHTMLPagesNotCacheable(t *testing.T) {
	// A cached panel/overlay runs OLD JS against a NEW server (e.g. a control it doesn't
	// know to save, so a toggle looks 'stuck'). All three HTML pages must say no-store.
	s := newTestServer(t, "")
	for _, p := range []string{"/control", "/scoring", "/overlay"} {
		if !strings.Contains(s.get(p).header.Get("Cache-Control"), "no-store") {
			t.Fatal(p)
		}
	}
}

func TestUnknownRoutes404(t *testing.T) {
	s := newTestServer(t, "")
	for _, p := range []string{"/nope", "/commentary/test", "/commentary/over/generate"} {
		mustEq(t, s.get(p).status, 404, "GET ", p, " should 404 (POST-only or removed)")
	}
}

func TestDiagnosticsRespond(t *testing.T) {
	s := newTestServer(t, "")
	for _, p := range []string{"/health", "/status", "/data/status", "/pcs/debug", "/logos/debug", "/highlights/status"} {
		st, _ := s.getJSON(p)
		mustEq(t, st, 200, p)
	}
	_, body := s.getJSON("/health")
	mustEq(t, body["ok"], any(true))
	if _, ok := body["pcs"]; !ok {
		t.Fatal("no pcs block")
	}
	// self-metrics: the "is the server staying lightweight?" number, plus the error
	// flight recorder (empty on a healthy server)
	if _, ok := gm(body, "server")["max_rss_mb"]; !ok {
		t.Fatal("no max_rss_mb")
	}
	if _, ok := body["errors"].([]any); !ok {
		t.Fatalf("errors is %T", body["errors"])
	}
}

func TestOptionsPreflight(t *testing.T) {
	s := newTestServer(t, "")
	r := s.raw("OPTIONS", "/state", nil, nil)
	mustEq(t, r.status, 200)
	if !strings.Contains(r.header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal(r.header)
	}
}

func TestStateSecretRedactionAndSentinelRoundtrip(t *testing.T) {
	s := newTestServer(t, "")
	setState(t, M{"anthropic_api_key": "sk-ant-SUPERSECRET", "youtube_stream_key": "abcd-STREAMKEY-1234"})
	st, body := s.getJSON("/state")
	mustEq(t, st, 200)
	mustEq(t, body["anthropic_api_key"], any(SecretSentinel))
	mustEq(t, body["youtube_stream_key"], any(SecretSentinel))
	mustEq(t, body["anthropic_key_set"], any(true))
	dump, _ := json.Marshal(body)
	if bytes.Contains(dump, []byte("SUPERSECRET")) || bytes.Contains(dump, []byte("STREAMKEY")) {
		t.Fatal("secret leaked")
	}
	// Posting the sentinel back must leave the stored key untouched
	st, _ = s.postJSON("/state", M{"anthropic_api_key": SecretSentinel, "home_team": "Test CC"})
	mustEq(t, st, 200)
	disk := LoadState()
	mustEq(t, disk["anthropic_api_key"], any("sk-ant-SUPERSECRET"))
	mustEq(t, disk["home_team"], any("Test CC"))
	// Posting empty string clears it
	st, _ = s.postJSON("/state", M{"anthropic_api_key": ""})
	mustEq(t, st, 200)
	mustEq(t, LoadState()["anthropic_api_key"], any(""))
}

func TestCamera2ConfiguredFlagNeverLeaksURL(t *testing.T) {
	// The overlay needs to know a second camera is configured (to gate the auto-cut)
	// without ever seeing camera2_rtsp_url itself.
	s := newTestServer(t, "")
	_, body := s.getJSON("/state")
	mustEq(t, body["camera2_configured"], any(false))
	setState(t, M{"camera2_rtsp_url": "rtsp://admin:hunter2@192.168.1.51:554/stream"})
	r := s.get("/state")
	body = r.json(t)
	mustEq(t, body["camera2_configured"], any(true))
	mustEq(t, body["camera2_rtsp_url"], any(SecretSentinel))
	if bytes.Contains(r.body, []byte("hunter2")) {
		t.Fatal("camera password leaked")
	}
}

func TestStatePostMerges(t *testing.T) {
	s := newTestServer(t, "")
	st, _ := s.postJSON("/state", M{"roster": M{"21": "Peter Smith"}, "sponsor_name": "Acme"})
	mustEq(t, st, 200)
	st, _ = s.postJSON("/state", M{"away_team": "Rival CC"})
	mustEq(t, st, 200)
	disk := LoadState()
	mustEq(t, gm(disk, "roster")["21"], any("Peter Smith"))
	mustEq(t, disk["sponsor_name"], any("Acme"))
	mustEq(t, disk["away_team"], any("Rival CC"))
}

func TestStatePostRejectsNonDictAndBadJSON(t *testing.T) {
	s := newTestServer(t, "")
	st, _ := s.postJSON("/state", []int{1, 2, 3})
	mustEq(t, st, 400)
	st, _ = s.postJSON("/state", "not json {{{")
	mustEq(t, st, 400)
	mustEq(t, LoadState()["home_team"], any("Home CC")) // the state file is unharmed
}

func TestOriginCheckRejectsCrossSitePosts(t *testing.T) {
	s := newTestServer(t, "")
	st, _ := s.postJSON("/state", M{"home_team": "evil"}, map[string]string{"Origin": "http://evil.example"})
	mustEq(t, st, 403)
	st, _ = s.postJSON("/state", M{"home_team": "evil"}, map[string]string{"Referer": "http://evil.example/page"})
	mustEq(t, st, 403)
	if LoadState()["home_team"] == "evil" {
		t.Fatal("cross-site post applied")
	}
	st, _ = s.postJSON("/state", M{"home_team": "Fine CC"}, map[string]string{"Origin": "http://" + s.addr})
	mustEq(t, st, 200)
}

func TestOversizedBodyRejected(t *testing.T) {
	// The server may 413 and close without draining the body; what matters is that the
	// giant body is never processed into state, and the server keeps serving.
	s := newTestServer(t, "")
	big := append(append([]byte(`{"home_team": "`), bytes.Repeat([]byte("x"), MaxBodyBytes)...), `"}`...)
	if r := s.post("/state", big); r.status != 413 && r.status != -1 { // -1: dropped, also rejected
		t.Fatalf("status %d", r.status)
	}
	mustEq(t, LoadState()["home_team"], any("Home CC"))
	st, _ := s.getJSON("/health")
	mustEq(t, st, 200)
}

func TestLogoServingAndPathTraversal(t *testing.T) {
	s := newTestServer(t, "")
	logos := filepath.Join(s.dir, "logos")
	os.MkdirAll(logos, 0o755)
	os.WriteFile(filepath.Join(logos, "5.png"), []byte("\x89PNG fake"), 0o644)
	setState(t, M{"logos_folder": logos})
	r := s.get("/logo/5")
	mustEq(t, r.status, 200)
	mustEq(t, r.header.Get("Content-Type"), "image/png")
	mustEq(t, string(r.body), "\x89PNG fake")
	// Traversal attempts must never escape the folder
	for _, evil := range []string{"/logo/../match_state", "/logo/..%2f..%2fconfig",
		"/logo/....//....//etc//passwd", `/logo/..\..\config`} {
		r := s.get(evil)
		mustEq(t, r.status, 404, evil)
		if bytes.Contains(r.body, []byte("match_state")) {
			t.Fatal(evil)
		}
	}
}

func TestHeadshotTieredMatching(t *testing.T) {
	s := newTestServer(t, "")
	heads := filepath.Join(s.dir, "headshots")
	os.MkdirAll(heads, 0o755)
	os.WriteFile(filepath.Join(heads, "p.smith.png"), []byte("peter-photo"), 0o644)
	os.WriteFile(filepath.Join(heads, "21.png"), []byte("number-photo"), 0o644)
	setState(t, M{"headshots_folder": heads, "roster": M{"21": "Peter Smith"}})
	mustEq(t, s.get("/headshot/P%20SMITH").status, 200) // initial+surname form
	mustEq(t, s.get("/headshot/SMITH").status, 200)     // unique bare surname
	// a confirmed shirt number (roster surname agrees) prefers the numbered file
	r := s.get("/headshot/SMITH?num=21")
	mustEq(t, r.status, 200)
	mustEq(t, string(r.body), "number-photo")
	mustEq(t, s.get("/headshot/NOBODY").status, 404)
	// a second Smith makes the bare surname ambiguous → suppressed
	os.WriteFile(filepath.Join(heads, "j.smith.png"), []byte("james-photo"), 0o644)
	mustEq(t, s.get("/headshot/SMITH").status, 404)
}

func TestSponsorUnknownID404s(t *testing.T) {
	s := newTestServer(t, "")
	mustEq(t, s.get("/sponsor/zzz-does-not-exist").status, 404)
}

// ── /live with a fake NV Play output file: the match-day critical path ──

type pcsFixture struct {
	*testServer
	pcsDir string
}

func newPCSFixture(t *testing.T) *pcsFixture {
	s := newTestServer(t, "")
	dir := filepath.Join(s.dir, "pcs")
	os.MkdirAll(dir, 0o755)
	setState(t, M{"pcs_output_folder": dir, "use_widget": false, "demo_mode": false})
	return &pcsFixture{s, dir}
}

func (f *pcsFixture) write(age float64, fields ...string) {
	d := map[string]string{"batting_team": "Home CC", "bowling_team": "Rival CC",
		"runs": "12", "wickets": "0", "overs": "2.3",
		"batter1_name": "SMITH", "batter1_runs": "8", "batter1_balls": "10", "batter1_strike": "True",
		"batter2_name": "JONES", "batter2_runs": "3", "batter2_balls": "5",
		"bowler_name": "HARRISON", "bowler_overs": "1.3", "bowler_runs": "12",
		"bowler_wickets": "0", "last_ball": "1 4 ."}
	for i := 0; i+1 < len(fields); i += 2 {
		d[fields[i]] = fields[i+1]
	}
	b, _ := json.Marshal(d)
	p := filepath.Join(f.pcsDir, "scoreboard-output.json")
	os.WriteFile(p, b, 0o644)
	mt := time.Now().Add(-time.Duration(age * float64(time.Second)))
	os.Chtimes(p, mt, mt)
}

func (f *pcsFixture) live() M {
	f.t.Helper()
	st, body := f.getJSON("/live")
	mustEq(f.t, st, 200)
	return body
}

func eventTypes(body M) []string {
	var out []string
	for _, e := range gl(body, "events") {
		out = append(out, gs(e.(M), "type", ""))
	}
	return out
}

func queryRows(t *testing.T, q string, args ...any) [][]any {
	t.Helper()
	dbMu.Lock()
	defer dbMu.Unlock()
	h, err := db()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := h.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	return out
}

func rowsStr(rows [][]any) string { return fmt.Sprint(rows) }

func count(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	return queryRows(t, q, args...)[0][0].(int64)
}

func TestPCSStateParsedNoEventsOnFirstPoll(t *testing.T) {
	f := newPCSFixture(t)
	f.write(10)
	body := f.live()
	mustEq(t, body["source"], any("pcs"))
	mustEq(t, gm(body, "state")["score"], any(12.0))
	mustEq(t, gm(body, "state")["battingTeamName"], any("Home CC"))
	mustEq(t, len(gl(body, "events")), 0) // the first poll seeds, never fires
}

func TestWicketReachesEventBufferAndMatchLog(t *testing.T) {
	// The seeding bug: with graphics_commentary off (default), the baseline was never
	// seeded, so wickets never buffered and the fall-of-wickets list stayed empty.
	f := newPCSFixture(t)
	f.write(10)
	f.live()
	f.write(5, "runs", "16", "wickets", "1", "overs", "2.4", "last_ball", "1 4 . W")
	body := f.live()
	mustEq(t, fmt.Sprint(eventTypes(body)), "[wicket]")
	mustEq(t, gl(body, "events")[0].(M)["wickets"], any(1.0))
	matchLogMu.Lock()
	fow := matchLog.FallOfWickets
	matchLogMu.Unlock()
	mustEq(t, len(fow), 1)
	mustEq(t, fow[0]["batter"], any("SMITH"))
	// no change → no re-fire
	f.write(1, "runs", "16", "wickets", "1", "overs", "2.4", "last_ball", "1 4 . W")
	mustEq(t, len(gl(f.live(), "events")), 0)
}

func TestBallDBRewritesCurrentOver(t *testing.T) {
	f := newPCSFixture(t)
	f.write(10)
	f.live()
	// the scorer adds a ball (and a wicket) to the same over → rewritten, not appended
	f.write(5, "runs", "16", "wickets", "1", "overs", "2.4", "last_ball", "1 4 . W")
	f.live()
	rows := queryRows(t, "SELECT ball, outcome, is_wicket FROM balls WHERE over=2 ORDER BY ball")
	mustEq(t, len(rows), 4)
	mustEq(t, rows[3][1], any("W"))
	mustEq(t, rows[3][2], any(int64(1)))
}

func TestOverCompletingBallRecoveredIntoDB(t *testing.T) {
	// When the ticker is cleared on the write that completes an over, ball 6 never
	// appears in any ticker — without delta recovery every over's final ball is lost.
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "wickets", "0", "overs", "2.5", "last_ball", "1 4 . . 2")
	f.live()
	f.write(8, "runs", "16", "wickets", "0", "overs", "3.0", "last_ball", "")
	f.live()
	rows := queryRows(t, "SELECT ball, outcome, runs FROM balls WHERE over=2 ORDER BY ball")
	mustEq(t, len(rows), 6)
	mustEq(t, fmt.Sprint(rows[5]), "[6 4 4]")
	// ...and a WICKET on the over-completing ball is recovered too
	f.write(6, "runs", "17", "wickets", "0", "overs", "3.5", "last_ball", "1 . . . .")
	f.live()
	f.write(4, "runs", "17", "wickets", "1", "overs", "4.0", "last_ball", "")
	f.live()
	rows = queryRows(t, "SELECT ball, outcome, is_wicket FROM balls WHERE over=3 ORDER BY ball")
	mustEq(t, len(rows), 6)
	mustEq(t, fmt.Sprint(rows[5]), "[6 W 1]")
}

func TestStaleTickerThroughTheOverBoundary(t *testing.T) {
	// What NV Play usually does: the finished over stays in the ticker on the
	// over-completing write AND after it until the next ball. The final ball must still
	// be recovered, and the stale balls must not be logged again under the next over.
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "wickets", "0", "overs", "2.5", "last_ball", "1 4 . . 2")
	f.live()
	for _, age := range []float64{8, 6} {
		f.write(age, "runs", "16", "wickets", "0", "overs", "3.0", "last_ball", "1 4 . . 2")
		f.live()
	}
	f.write(4, "runs", "17", "wickets", "0", "overs", "3.1", "last_ball", "1")
	f.live()
	over2 := queryRows(t, "SELECT ball, outcome, runs FROM balls WHERE over=2 ORDER BY ball")
	over3 := queryRows(t, "SELECT ball, outcome, runs FROM balls WHERE over=3 ORDER BY ball")
	mustEq(t, len(over2), 6)
	mustEq(t, fmt.Sprint(over2[5]), "[6 4 4]")
	mustEq(t, rowsStr(over3), "[[1 1 1]]")
}

func TestTickerThatDoesCarryTheFinalBall(t *testing.T) {
	// If NV Play ever DOES put the final ball in the ticker, it's logged once, as that
	// over's sixth ball — not again under the next over.
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "wickets", "0", "overs", "2.5", "last_ball", "1 4 . . 2")
	f.live()
	for _, age := range []float64{8, 6} {
		f.write(age, "runs", "12", "wickets", "1", "overs", "3.0", "last_ball", "1 4 . . 2 W")
		f.live()
	}
	f.write(4, "runs", "12", "wickets", "1", "overs", "3.1", "last_ball", ".")
	f.live()
	over2 := queryRows(t, "SELECT ball, outcome, is_wicket FROM balls WHERE over=2 ORDER BY ball")
	mustEq(t, len(over2), 6)
	mustEq(t, fmt.Sprint(over2[5]), "[6 W 1]")
	mustEq(t, count(t, "SELECT COUNT(*) FROM balls WHERE over=3"), int64(1))
}

func TestNewOversOpeningWideNotMistakenForStaleTicker(t *testing.T) {
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "wickets", "0", "overs", "2.5", "last_ball", "w . . . . 1")
	f.live()
	f.write(8, "runs", "13", "wickets", "0", "overs", "3.0", "last_ball", "")
	f.live()
	f.write(6, "runs", "14", "wickets", "0", "overs", "3.0", "last_ball", "w")
	f.live()
	mustEq(t, rowsStr(queryRows(t, "SELECT outcome FROM balls WHERE over=3")), "[[wide]]")
}

func TestStaleTickerNotRelinkedAtInningsEnd(t *testing.T) {
	// No "next ball" ever replaces the last over's stale copy, so it used to stay in the
	// DB under an over that was never bowled.
	f := newPCSFixture(t)
	f.write(10, "runs", "40", "wickets", "2", "overs", "4.5", "last_ball", ". 1 . 4 .")
	f.live()
	f.write(8, "runs", "46", "wickets", "2", "overs", "5.0", "last_ball", ". 1 . 4 .")
	f.live()
	mustEq(t, count(t, "SELECT COUNT(*) FROM balls WHERE over=5"), int64(0))
	mustEq(t, rowsStr(queryRows(t, "SELECT runs FROM balls WHERE over=4 AND ball=6")), "[[6]]")
}

func idsWithBalls(t *testing.T) map[string]bool {
	out := map[string]bool{}
	for _, r := range queryRows(t, "SELECT DISTINCT match_id FROM balls") {
		out[r[0].(string)] = true
	}
	return out
}

func TestRenamingOppositionMidInningsKeepsMatchTogether(t *testing.T) {
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "overs", "2.3", "last_ball", "1 4 .")
	f.live()
	setState(t, M{"away_team": "Corrected Name CC"})
	f.write(8, "runs", "13", "overs", "2.4", "last_ball", "1 4 . 1")
	f.live()
	ids := idsWithBalls(t)
	if len(ids) != 1 || !ids[CurrentMatchID()] {
		t.Fatalf("ids %v, current %s", ids, CurrentMatchID())
	}
	mustEq(t, count(t, "SELECT COUNT(*) FROM balls WHERE over=2"), int64(4))
}

func TestEarlierSessionNotPulledIntoMatch(t *testing.T) {
	// A morning rehearsal under one id, then the real match under another: the old rows
	// are hours old, so they stay where they are.
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "overs", "2.3", "last_ball", "1 4 .")
	f.live()
	rehearsal := CurrentMatchID()
	queryRows(t, "UPDATE balls SET ts='2000-01-01T09:00:00'")
	queryRows(t, "UPDATE live_innings SET ts='2000-01-01T09:00:00'")
	setState(t, M{"away_team": "Real Opponents CC"})
	f.write(8, "runs", "5", "overs", "0.4", "last_ball", "1 4 . .")
	f.live()
	ids := idsWithBalls(t)
	if len(ids) != 2 || !ids[rehearsal] || !ids[CurrentMatchID()] {
		t.Fatalf("ids %v", ids)
	}
}

func TestRecoveredBallKeepsOldOversPersonnel(t *testing.T) {
	// On the over-completing write the feed has ALREADY rotated the bowler and swapped
	// the batters — the recovered final delivery belongs to the over that ended.
	f := newPCSFixture(t)
	f.write(10, "runs", "12", "wickets", "0", "overs", "2.5", "last_ball", "1 4 . . 2")
	f.live()
	f.write(8, "runs", "16", "wickets", "1", "overs", "3.0", "last_ball", "",
		"bowler_name", "PATEL", "bowler_overs", "0.0", "bowler_runs", "0",
		"batter1_name", "JONES", "batter2_name", "BROWN")
	f.live()
	mustEq(t, rowsStr(queryRows(t, "SELECT bowler, batter, non_striker FROM balls WHERE over=2 AND ball=6")),
		"[[HARRISON SMITH JONES]]")
}

func TestLiveViewNeverRebindsPinnedMatchCache(t *testing.T) {
	// Clearing match_url must only take effect on the overlay's own /live poll — a
	// panel /live/view poll landing first used to drop the overlay's cached widget state.
	f := newPCSFixture(t)
	f.write(10)
	rvMu.Lock()
	rvCache = rvCacheT{rvID: 1, pcID: 42, lastState: M{"score": 1}, pinned: true, initialised: true}
	rvMu.Unlock()
	f.getJSON("/live/view")
	rvMu.Lock()
	pinned, rv := rvCache.pinned, rvCache.rvID
	rvMu.Unlock()
	if !pinned || rv != 1 {
		t.Fatal("/live/view touched the pinned cache")
	}
	f.live() // the overlay's poll does the unpin
	rvMu.Lock()
	pinned = rvCache.pinned
	rvMu.Unlock()
	if pinned {
		t.Fatal("/live didn't unpin")
	}
}

func TestLiveViewReadOnlyNeverEatsEvents(t *testing.T) {
	// The panel polls /live/view; same picture as the overlay, NO side effects.
	f := newPCSFixture(t)
	f.write(10)
	f.live() // overlay poll 1: seeds + logs the over
	f.write(5, "runs", "16", "wickets", "1", "overs", "2.4", "last_ball", "1 4 . W")
	for i := 0; i < 3; i++ { // the panel hammers /live/view first
		st, body := f.getJSON("/live/view")
		mustEq(t, st, 200)
		mustEq(t, body["source"], any("pcs"))
		mustEq(t, gm(body, "state")["score"], any(16.0))
		mustEq(t, len(gl(body, "events")), 0)
	}
	prevStateMu.Lock()
	w := prevState["wickets"]
	prevStateMu.Unlock()
	mustEq(t, fmt.Sprint(w), "0") // baseline not advanced
	mustEq(t, count(t, "SELECT COUNT(*) FROM balls WHERE over=2"), int64(3))
	mustEq(t, fmt.Sprint(eventTypes(f.live())), "[wicket]")
}

func TestConfiguredButEmptyFolderStillReportsPCS(t *testing.T) {
	// Pre-match: folder set, no file yet — source stays 'pcs' so the overlay keeps
	// fast-polling instead of falling back to widget polling.
	f := newPCSFixture(t)
	body := f.live()
	mustEq(t, body["source"], any("pcs"))
	if body["state"] != nil {
		t.Fatal(body["state"])
	}
}

func TestMidWriteEmptyFileHoldsLastGoodFrame(t *testing.T) {
	f := newPCSFixture(t)
	f.write(10)
	f.live()
	p := filepath.Join(f.pcsDir, "scoreboard-output.json")
	os.WriteFile(p, nil, 0o644)
	mt := time.Now().Add(-time.Second)
	os.Chtimes(p, mt, mt)
	mustEq(t, gm(f.live(), "state")["score"], any(12.0))
}

func TestNonNumericClubIDDoesNotCrashLive(t *testing.T) {
	// The manual badge picker can set home_club_id to a filename stem
	f := newPCSFixture(t)
	setState(t, M{"home_club_id": "brides-logo"})
	f.write(10)
	body := f.live()
	mustEq(t, body["source"], any("pcs"))
	mustEq(t, body["club_id"], any(0.0))
}

// ── With a club password set — the remote-operation config ──

const testPW = "testpw"

func TestLoginAndGatedPost(t *testing.T) {
	s := newTestServer(t, testPW)
	loginSleep = func(time.Duration) {}
	t.Cleanup(func() { loginSleep = time.Sleep })
	st, _ := s.postJSON("/login", M{"password": "wrong"})
	mustEq(t, st, 401)
	st, body := s.postJSON("/login", M{"password": testPW})
	mustEq(t, st, 200)
	if gs(body, "session_token", "") == "" {
		t.Fatal("no token")
	}
	st, _ = s.postJSON("/state", M{"home_team": "X"})
	mustEq(t, st, 401)
	st, _ = s.postJSON("/state", M{"home_team": "X"}, s.bearer(testPW))
	mustEq(t, st, 200)
}

func TestGatedGets(t *testing.T) {
	s := newTestServer(t, testPW)
	for _, p := range []string{"/report/log", "/auth/log", "/data/export?match_id=x"} {
		mustEq(t, s.get(p).status, 401, p)
	}
	h := s.bearer(testPW)
	mustEq(t, s.get("/report/log", h).status, 200)
	// /data/export with a token → CSV (what the panel's blob download sends)
	r := s.get("/data/export?match_id=x", h)
	mustEq(t, r.status, 200)
	mustEq(t, r.header.Get("Content-Type"), "text/csv")
	if !bytes.HasPrefix(r.body, []byte("innings,over,ball")) {
		t.Fatalf("%q", r.body)
	}
}

func TestOpenEndpointsStayOpen(t *testing.T) {
	// The overlay has no login flow: everything it GETs must work tokenless
	s := newTestServer(t, testPW)
	setState(t, M{"use_widget": false})
	for _, p := range []string{"/state", "/live", "/commands", "/health", "/player/stats?name=X", "/commentary/over"} {
		mustEq(t, s.get(p).status, 200, p)
	}
}

func TestOverlayLoopbackCarveout(t *testing.T) {
	s := newTestServer(t, testPW)
	setState(t, M{"replay_enabled": false, "obs_port": 1}) // nothing to connect to
	// Loopback, no token: overlay endpoints work...
	st, body := s.postJSON("/replay", M{"reason": "Test"})
	mustEq(t, st, 200) // replay off → ok:false in the body, but never 401
	mustEq(t, body["ok"], any(false))
	st, _ = s.postJSON("/weather/show", M{})
	mustEq(t, st, 200)
	st, _ = s.postJSON("/weather/hide", M{})
	mustEq(t, st, 200)
	// /camera/scene joined the overlay endpoints for the auto-cut; OBS is unreachable
	// here, so ok:false in the body, but never 401
	st, body = s.postJSON("/camera/scene", M{"scene": "Main-Bowler"})
	mustEq(t, st, 200)
	mustEq(t, body["ok"], any(false))
	// ...but a proxied (X-Forwarded-For) caller is NOT trusted loopback
	xff := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	st, _ = s.postJSON("/replay", M{"reason": "Test"}, xff)
	mustEq(t, st, 401)
	st, _ = s.postJSON("/camera/scene", M{"scene": "Main-Bowler"}, xff)
	mustEq(t, st, 401)
	// ...and non-overlay POSTs never get the carve-out even from loopback
	st, _ = s.postJSON("/scorecard/show", M{})
	mustEq(t, st, 401)
	st, _ = s.postJSON("/checklist/action", M{"action": "start_obs"})
	mustEq(t, st, 401)
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	s := newTestServer(t, testPW)
	loginSleep = func(time.Duration) {} // skip the deliberate anti-brute-force delay
	t.Cleanup(func() { loginSleep = time.Sleep })
	for i := 0; i < LoginMaxFailures; i++ {
		st, _ := s.postJSON("/login", M{"password": "wrong"})
		mustEq(t, st, 401)
	}
	st, _ := s.postJSON("/login", M{"password": "wrong"})
	mustEq(t, st, 429)
	// even the CORRECT password is rejected while locked out
	st, _ = s.postJSON("/login", M{"password": testPW})
	mustEq(t, st, 429)
}

func TestLogoutAllInvalidatesExistingSessions(t *testing.T) {
	s := newTestServer(t, testPW)
	h := s.bearer(testPW)
	st, _ := s.postJSON("/auth/logout_all", M{}, h)
	mustEq(t, st, 200)
	st, _ = s.postJSON("/state", M{"home_team": "X"}, h)
	mustEq(t, st, 401)
	st, _ = s.postJSON("/state", M{"home_team": "X"}, s.bearer(testPW))
	mustEq(t, st, 200)
}

func TestAuthLogRecordsEvents(t *testing.T) {
	s := newTestServer(t, testPW)
	loginSleep = func(time.Duration) {}
	t.Cleanup(func() { loginSleep = time.Sleep })
	s.postJSON("/login", M{"password": "wrong"})
	st, body := s.getJSON("/auth/log", s.bearer(testPW))
	mustEq(t, st, 200)
	seen := map[string]bool{}
	for _, e := range gl(body, "entries") {
		seen[gs(e.(M), "event", "")] = true
	}
	if !seen["login_fail"] || !seen["login_ok"] {
		t.Fatal(seen)
	}
}

// A burst of near-simultaneous pollers (overlay + panel + /scoring + devices
// reconnecting after a wifi blip) must all be served. Python had to raise its listen
// backlog from 5 to 64 after load-testing reset connections at concurrency >= 10; this
// pins the behaviour itself rather than the setting.
func TestPollerBurstIsServed(t *testing.T) {
	sandbox(t, "")
	ln, err := Listen0()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewHTTPServer()
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	url := "http://" + ln.Addr().String() + "/health"
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 20 * time.Second}
			r, err := c.Get(url)
			if err != nil {
				errs <- err
				return
			}
			r.Body.Close()
			if r.StatusCode != 200 {
				errs <- fmt.Errorf("status %d", r.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
