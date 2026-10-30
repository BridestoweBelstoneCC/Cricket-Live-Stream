package server

// The control panel's pre-match check (tests/test_precheck.py): every credential and
// connection tried for real, in parallel, each answering ok / warn / bad with what to do.
// Built after the YouTube login on the maintainer's laptop turned out to have expired
// silently.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"cricketstream/internal/ai"
	"cricketstream/internal/assets"
	"cricketstream/internal/clubhouse"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
)

type precheckFn = struct {
	ID, Name string
	Fn       func(M) CheckResult
}

func all(status string) func(M) CheckResult {
	return func(M) CheckResult { return chk(status, status+" detail", "") }
}

func withPrechecks(t *testing.T, checks []precheckFn, timeout time.Duration) {
	t.Helper()
	saved, savedT := Prechecks, precheckTimeout
	Prechecks, precheckTimeout = checks, timeout
	t.Cleanup(func() { Prechecks, precheckTimeout = saved, savedT })
}

func statuses(r M) []string {
	var out []string
	for _, c := range gl(r, "checks") {
		out = append(out, gs(c.(M), "status", ""))
	}
	return out
}

func TestPrecheckAllGood(t *testing.T) {
	sandbox(t, "")
	withPrechecks(t, []precheckFn{{"a", "A", all("ok")}, {"b", "B", all("warn")}}, 20*time.Second)
	r := RunPrecheck()
	mustEq(t, r["ok"], any(true)) // a warning isn't a failure
	eqv(t, statuses(r), []string{"ok", "warn"})
}

func TestPrecheckOneFailureFailsOverall(t *testing.T) {
	sandbox(t, "")
	withPrechecks(t, []precheckFn{{"a", "A", all("ok")}, {"b", "B", all("bad")}}, 20*time.Second)
	mustEq(t, RunPrecheck()["ok"], any(false))
}

func TestPrecheckCrashingCheckReportedNotRaised(t *testing.T) {
	sandbox(t, "")
	boom := func(M) CheckResult { panic(errors.New("socket exploded")) }
	withPrechecks(t, []precheckFn{{"a", "A", boom}, {"b", "B", all("ok")}}, 20*time.Second)
	c := gl(RunPrecheck(), "checks")
	eqv(t, c[0].(M)["status"], "bad")
	if !strings.Contains(gs(c[0].(M), "detail", ""), "socket exploded") {
		t.Fatal(c[0])
	}
	eqv(t, c[1].(M)["status"], "ok") // the others still answer
}

func TestPrecheckHungCheckTimesOutRestAnswer(t *testing.T) {
	sandbox(t, "")
	slow := func(M) CheckResult { time.Sleep(3 * time.Second); return chk("ok", "", "") }
	withPrechecks(t, []precheckFn{{"a", "A", slow}, {"b", "B", all("ok")}}, 500*time.Millisecond)
	start := time.Now()
	c := gl(RunPrecheck(), "checks")
	if time.Since(start) > 2500*time.Millisecond {
		t.Fatal("waited for the hung check")
	}
	eqv(t, c[0].(M)["status"], "bad")
	if !strings.Contains(gs(c[0].(M), "detail", ""), "No answer") {
		t.Fatal(c[0])
	}
	eqv(t, c[1].(M)["status"], "ok")
}

var clubSt = M{"home_team": "Home and District CC", "home_club_id": "111",
	"playcricket_api_key": "k", "anthropic_api_key": "a"}

// fakeChannel is a YouTube that answers channels.list with one channel title.
func fakeChannel(t *testing.T, title string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(M{"items": []any{M{"snippet": M{"title": title}}}})
	}))
	t.Cleanup(srv.Close)
	savedAPI, savedYT := ytAPI, precheckYT
	ytAPI = srv.URL + "/"
	precheckYT = func(bool) (*YTClient, string) { return &YTClient{http: srv.Client()}, "" }
	t.Cleanup(func() { ytAPI, precheckYT = savedAPI, savedYT })
}

func withYTCreds(t *testing.T) {
	t.Helper()
	sandbox(t, "")
	os.WriteFile(YTCredsFile, []byte("{}"), 0o644)
}

func TestPrecheckYouTubeExpiredLoginIsBadWithReason(t *testing.T) {
	withYTCreds(t)
	saved := precheckYT
	precheckYT = func(bool) (*YTClient, string) { return nil, "YouTube's login has expired — …" }
	t.Cleanup(func() { precheckYT = saved })
	r := pcYouTube(clubSt)
	if r.Status != "bad" || !strings.Contains(r.Detail, "expired") {
		t.Fatal(r)
	}
}

func TestPrecheckYouTubeWrongChannelIsWarning(t *testing.T) {
	withYTCreds(t)
	fakeChannel(t, "Somebody's Holiday Videos")
	r := pcYouTube(clubSt)
	if r.Status != "warn" || !strings.Contains(r.Detail, "Holiday") {
		t.Fatal(r)
	}
}

func TestPrecheckYouTubeClubChannelIsOK(t *testing.T) {
	withYTCreds(t)
	fakeChannel(t, "Home and District Cricket Club")
	mustEq(t, pcYouTube(clubSt).Status, "ok")
}

func withPCGet(t *testing.T, fn func(string, time.Duration) (M, error)) {
	t.Helper()
	saved := pcGetJSON
	pcGetJSON = fn
	t.Cleanup(func() { pcGetJSON = saved })
}

func TestPrecheckPlayCricketRejectedKey(t *testing.T) {
	withPCGet(t, func(string, time.Duration) (M, error) { return nil, &httpError{Code: 401} })
	r := pcPlayCricket(clubSt)
	if r.Status != "bad" || !strings.Contains(r.Detail, "rejected") {
		t.Fatal(r)
	}
}

func TestPrecheckPlayCricketWorks(t *testing.T) {
	withPCGet(t, func(string, time.Duration) (M, error) { return M{"matches": []any{1, 2, 3}}, nil })
	mustEq(t, pcPlayCricket(clubSt), chk("ok", "Key works — 3 fixtures this season.", ""))
}

func TestPrecheckMissingOptionalKeysAreWarnings(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	st := M{"home_team": "X CC"}
	mustEq(t, pcAnthropic(st).Status, "warn")
	mustEq(t, pcPlayCricket(st).Status, "warn")
}

func TestPrecheckAnthropicRejectedKey(t *testing.T) {
	saved := ai.CheckKey
	ai.CheckKey = func(context.Context, string) error { return &anthropic.Error{StatusCode: 401} }
	t.Cleanup(func() { ai.CheckKey = saved })
	mustEq(t, pcAnthropic(clubSt).Status, "bad")
}

func withPortOpen(t *testing.T, open bool) {
	t.Helper()
	saved := precheckPortOpen
	precheckPortOpen = func(int) bool { return open }
	t.Cleanup(func() { precheckPortOpen = saved })
}

func TestPrecheckOBSClosedIsFineDaysBefore(t *testing.T) {
	withPortOpen(t, false)
	mustEq(t, pcOBS(M{"obs_host": "localhost"}).Status, "warn")
}

func TestPrecheckOBSOpenButPasswordWrong(t *testing.T) {
	withPortOpen(t, true)
	saved := obsCall
	obsCall = func(M, []obsws.Req, time.Duration) []map[string]any { return nil }
	t.Cleanup(func() { obsCall = saved })
	r := pcOBS(M{"obs_host": "localhost"})
	if r.Status != "bad" || !strings.Contains(r.Fix, "password") {
		t.Fatal(r)
	}
}

func TestPrecheckDiskSpaceThresholds(t *testing.T) {
	saved := precheckDiskFree
	t.Cleanup(func() { precheckDiskFree = saved })
	for _, c := range []struct {
		gb   float64
		want string
	}{{50, "ok"}, {5, "warn"}, {1, "bad"}} {
		precheckDiskFree = func(string) (uint64, error) { return uint64(c.gb * 1e9), nil }
		mustEq(t, pcDisk(M{"replay_folder": t.TempDir()}).Status, c.want, c.gb)
	}
}

func TestPrecheckDiskCopesWithFolderNotThereYet(t *testing.T) {
	r := pcDisk(M{"replay_folder": filepath.Join(t.TempDir(), "no", "such", "replays")})
	if !strings.Contains(r.Detail, "doesn't exist yet") {
		t.Fatal(r)
	}
}

func TestPrecheckYouTubeLoginAboutToExpireWarnsWithDate(t *testing.T) {
	withYTCreds(t)
	fakeChannel(t, "Home and District Cricket Club")
	st := M{"home_team": "Home and District CC", "youtube_signed_in_at": now() - 6*86400}
	r := pcYouTube(st)
	if r.Status != "warn" || !strings.Contains(r.Detail, "6 days ago") || !strings.Contains(r.Fix, "Testing mode") {
		t.Fatal(r)
	}
	st["youtube_signed_in_at"] = now() - 86400
	mustEq(t, pcYouTube(st).Status, "ok")
}

// bitrate runs pcOBSBitrate with OBS's profile at iniKbps, the target from config.ini,
// and (if live > 0) the live monitor reading OBS's value.
func bitrate(t *testing.T, iniKbps int, target string, st M, live int) CheckResult {
	t.Helper()
	sandbox(t, "")
	cfg := ""
	if target != "" {
		cfg = "[Stream]\nbitrate_kbps = " + target + "\n"
	}
	os.WriteFile(ConfigFile, []byte(cfg), 0o644)
	prof := filepath.Join(t.TempDir(), "basic.ini")
	os.WriteFile(prof, []byte("[Output]\nMode=Simple\n\n[SimpleOutput]\nVBitrate="+strconv.Itoa(iniKbps)+"\n"), 0o644)
	saved := precheckProfileINI
	precheckProfileINI = func() string { return prof }
	t.Cleanup(func() { precheckProfileINI = saved })
	streamMonMu.Lock()
	savedMon := shallow(streamMon)
	streamMon["reachable"] = live > 0
	if live > 0 {
		streamMon["configured_kbps"] = live
	} else {
		streamMon["configured_kbps"] = nil
	}
	streamMonMu.Unlock()
	t.Cleanup(func() {
		streamMonMu.Lock()
		streamMon = savedMon
		streamMonMu.Unlock()
	})
	return pcOBSBitrate(st)
}

func TestPrecheckLeftoverDownshiftCaughtWithOBSClosed(t *testing.T) {
	r := bitrate(t, 875, "2500", M{}, 0)
	if r.Status != "bad" || !strings.Contains(r.Detail, "875") {
		t.Fatal(r)
	}
}

func TestPrecheckNormalBitrateOK(t *testing.T) {
	mustEq(t, bitrate(t, 2500, "2500", M{}, 0).Status, "ok")
}

func TestPrecheckBitrateAboveUploadCapacityWarns(t *testing.T) {
	mustEq(t, bitrate(t, 6000, "2500", M{"network_test_mbps": 3.7}, 0).Status, "warn")
}

func TestPrecheckManualBitrateLeftAlone(t *testing.T) {
	mustEq(t, bitrate(t, 875, "manual", M{}, 0).Status, "skip")
}

func TestPrecheckTargetFromConfigINI(t *testing.T) {
	sandbox(t, "")
	for _, c := range []struct {
		v    string
		kbps int
		why  string
	}{{"2500", 2500, "config"}, {"manual", 0, "manual"}, {"", 0, "none"}} {
		os.WriteFile(ConfigFile, []byte("[Stream]\nbitrate_kbps = "+c.v+"\n"), 0o644)
		k, why := targetBitrate(M{})
		if k != c.kbps || why != c.why {
			t.Fatalf("%q: %d %s", c.v, k, why)
		}
	}
	_, why := targetBitrate(M{"network_test_mbps": 10})
	mustEq(t, why, "test")
}

func TestPrecheckLiveOBSValueWinsOverFile(t *testing.T) {
	mustEq(t, bitrate(t, 875, "2500", M{}, 2500).Status, "ok")
}

func TestPrecheckCameras(t *testing.T) {
	mustEq(t, pcCameras(M{}).Status, "skip")
	st := M{"camera_rtsp_url": "rtsp://user:secret@192.0.2.10:554/stream"}
	saved := precheckDial
	t.Cleanup(func() { precheckDial = saved })
	precheckDial = func(string) (net.Conn, error) { return nil, errors.New("refused") }
	r := pcCameras(st)
	mustEq(t, r.Status, "warn") // not ✗: days before it's expected
	if strings.Contains(r.Detail+r.Fix, "secret") || strings.Contains(r.Detail, "192.0.2.10") {
		t.Fatal("camera URL leaked into the report")
	}
	precheckDial = func(string) (net.Conn, error) { a, b := net.Pipe(); b.Close(); return a, nil }
	mustEq(t, pcCameras(st).Status, "ok")
}

func surnames(t *testing.T, lookup M, roster M) CheckResult {
	t.Helper()
	seasonStatsMu.Lock()
	saved := seasonStats
	seasonStats = shallow(seasonStats)
	seasonStats["lookup"] = lookup
	seasonStatsMu.Unlock()
	t.Cleanup(func() {
		seasonStatsMu.Lock()
		seasonStats = saved
		seasonStatsMu.Unlock()
	})
	if roster == nil {
		roster = M{}
	}
	return pcSurnames(M{"roster": roster})
}

func TestPrecheckSurnameClashWarnsUntilRosterResolves(t *testing.T) {
	lookup := M{"a": M{"name": "Peter Smith"}, "b": M{"name": "James Smith"}, "c": M{"name": "Kian Burns"}}
	r := surnames(t, lookup, nil)
	if r.Status != "warn" || !strings.Contains(r.Detail, "James Smith, Peter Smith") {
		t.Fatal(r)
	}
	mustEq(t, surnames(t, lookup, M{"7": "James Smith", "21": "Peter Smith"}).Status, "ok")
	mustEq(t, surnames(t, M{}, nil).Status, "skip")
}

func TestPrecheckBadge(t *testing.T) {
	sandbox(t, "")
	mustEq(t, pcBadge(M{}).Status, "skip")
	logos := t.TempDir()
	mustEq(t, pcBadge(M{"away_club_id": "999999999", "away_team": "X CC", "logos_folder": logos}).Status, "warn")
	os.WriteFile(filepath.Join(logos, "123.png"), []byte("x"), 0o644)
	mustEq(t, pcBadge(M{"away_club_id": "123", "logos_folder": logos}).Status, "ok")
}

func TestPrecheckFixture(t *testing.T) {
	st := M{"playcricket_api_key": "k", "home_club_id": "111"}
	saved := FetchTodaysMatch
	t.Cleanup(func() { FetchTodaysMatch = saved })
	FetchTodaysMatch = func(string, string) M { return M{"error": "No matches found for today"} }
	mustEq(t, pcFixture(st).Status, "skip")
	FetchTodaysMatch = func(string, string) M { return M{"away_club": "Rivals CC", "competition": "A Division"} }
	mustEq(t, pcFixture(st), chk("ok", "Today: v Rivals CC (A Division).", ""))
}

func TestPrecheckUpload(t *testing.T) {
	// The target comes from config.ini — pin it, or this passes or fails depending on the
	// machine's real config.ini.
	sandbox(t, "")
	os.WriteFile(ConfigFile, []byte("[Stream]\nbitrate_kbps = 2500\n"), 0o644)
	mustEq(t, pcUpload(M{}).Status, "skip")
	ok := M{"network_test_mbps": 10, "network_test_at": now()}
	mustEq(t, pcUpload(ok).Status, "ok")
	old := shallow(ok)
	old["network_test_at"] = now() - 30*86400
	mustEq(t, pcUpload(old).Status, "warn")
	slow := shallow(ok)
	slow["network_test_mbps"] = 2
	mustEq(t, pcUpload(slow).Status, "bad")
}

func TestPrecheckUpdateCheck(t *testing.T) {
	tag := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(M{"tag_name": tag})
	}))
	t.Cleanup(srv.Close)
	savedURL, savedV := releasesURL, Version
	releasesURL, Version = srv.URL, "2.10"
	t.Cleanup(func() { releasesURL, Version = savedURL, savedV })
	for _, c := range [][2]string{{"v2.11", "warn"}, {"v2.10", "ok"}, {"v2.9", "ok"}} { // 2.10 > 2.9
		tag = c[0]
		mustEq(t, pcUpdate(M{}).Status, c[1], c[0])
	}
}

// A beta hears about the next beta (releases/latest never names one); drafts don't count.
func TestPrecheckUpdateCheckBeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/latest") {
			json.NewEncoder(w).Encode(M{"tag_name": "v2.12"})
			return
		}
		w.Write([]byte(`[{"tag_name":"v3.0.0-beta.3","draft":true},{"tag_name":"v3.0.0-beta.2"},{"tag_name":"v2.12"}]`))
	}))
	t.Cleanup(srv.Close)
	savedURL, savedV := releasesURL, Version
	t.Cleanup(func() { releasesURL, Version = savedURL, savedV })
	releasesURL, Version = srv.URL+"/releases/latest", "3.0.0-beta.1"
	r := pcUpdate(M{})
	mustEq(t, r.Status, "warn")
	if !strings.Contains(r.Detail, "3.0.0-beta.2 is out") {
		t.Errorf("%+v", r)
	}
	Version = "3.0.0-beta.2"
	mustEq(t, pcUpdate(M{}).Status, "ok")
}

func TestPrecheckScorerLink(t *testing.T) {
	sandbox(t, "")
	mustEq(t, pcScorerLink(M{}).Status, "skip")
	// agent mode with nothing answering (port 1 never does)
	mustEq(t, pcScorerLink(M{"pcs_source": "agent", "agent_host": "127.0.0.1:1"}).Status, "bad")
	mustEq(t, pcScorerLink(M{"pcs_bridge_url": "http://127.0.0.1:1"}).Status, "bad")
}

func TestPrecheckLastResultForPanelBadge(t *testing.T) {
	s := newTestServer(t, "")
	precheckLastMu.Lock()
	precheckLast = nil
	precheckLastMu.Unlock()
	st, d := s.getJSON("/precheck/last")
	mustEq(t, st, 200)
	mustEq(t, len(gl(d, "checks")), 0)
	withPrechecks(t, []precheckFn{{"a", "A", all("bad")}}, 20*time.Second)
	s.postJSON("/precheck", M{})
	_, d = s.getJSON("/precheck/last")
	mustEq(t, d["ok"], any(false))
	eqv(t, gl(d, "checks")[0].(M)["status"], "bad")
}

func TestPrecheckRunsAndIsRateLimited(t *testing.T) {
	s := newTestServer(t, "")
	withPrechecks(t, []precheckFn{{"a", "A", all("ok")}}, 20*time.Second)
	st, d := s.postJSON("/precheck", M{})
	mustEq(t, st, 200)
	eqv(t, gl(d, "checks")[0].(M)["name"], "A")
	st, _ = s.postJSON("/precheck", M{})
	mustEq(t, st, 429)
}

func TestPanelCameraButtonsInPinnedBar(t *testing.T) {
	b, _ := assets.Embedded("control.html")
	html := string(b)
	i, j := strings.Index(html, `<div id="topbar">`), strings.Index(html, `<div class="status" id="status">`)
	if i < 0 || j < i {
		t.Fatal("top bar not found")
	}
	bar := html[i:j]
	for _, want := range []string{`data-cam="main"`, `data-cam="bowler"`} {
		if !strings.Contains(bar, want) {
			t.Fatal(want)
		}
	}
	if !strings.Contains(html, "querySelectorAll('.cam-btn[data-cam=") { // both buttons light up
		t.Fatal("camera buttons not lit together")
	}
}

func TestPanelHasPrecheckButton(t *testing.T) {
	b, _ := assets.Embedded("control.html")
	for _, want := range []string{`onclick="runPrecheck()"`, "apiFetch('/precheck'"} {
		if !strings.Contains(string(b), want) {
			t.Fatal(want)
		}
	}
}

// The clubhouse screen's line: skipped unless the screen is switched on; then the
// clubhouse package's advice, never ✗ (it runs every six hours, days before the match).
func TestPrecheckClubhouse(t *testing.T) {
	sandbox(t, "")
	if r := pcClubhouse(M{}); r.Status != "skip" {
		t.Fatalf("no config.ini: %+v", r)
	}
	os.WriteFile(ConfigFile, []byte("[Clubhouse]\nenabled = no\n"), 0o600)
	if r := pcClubhouse(M{}); r.Status != "skip" {
		t.Fatalf("switched off: %+v", r)
	}
	os.WriteFile(ConfigFile, []byte("[Clubhouse]\nenabled = yes\nkey = K8Q27WMX\n"), 0o600)
	saved := clubhouse.CheckWake
	t.Cleanup(func() { clubhouse.CheckWake = saved })
	clubhouse.CheckWake = func() clubhouse.WakeCheck { return clubhouse.WakeCheck{Platform: true} }
	r := pcClubhouse(M{})
	if r.Status != "warn" || !strings.Contains(r.Fix, "no network port") {
		t.Fatalf("no port: %+v", r)
	}
	found := false
	for _, c := range Prechecks {
		found = found || c.ID == "clubhouse"
	}
	if !found {
		t.Fatal("not in the pre-match check's list")
	}
}

// obsProfile writes an OBS profile into the sandbox's OBS folder; signedIn adds the
// "Connect account" sign-in OBS records there.
func obsProfile(t *testing.T, signedIn bool) {
	t.Helper()
	dir := obs.ConfigDir()
	prof := filepath.Join(dir, "basic", "profiles", "Untitled")
	os.MkdirAll(prof, 0o755)
	os.WriteFile(filepath.Join(dir, "user.ini"), []byte("[Basic]\nProfileDir=Untitled\n"), 0o644)
	ini := "[Output]\nMode=Simple\n"
	if signedIn {
		ini += "[Auth]\nType=YouTube - RTMPS\n"
	}
	os.WriteFile(filepath.Join(prof, "basic.ini"), []byte(ini), 0o644)
}

func TestAutoQualityCheck(t *testing.T) {
	sandbox(t, "")
	for _, c := range []struct {
		name     string
		auto     bool
		signedIn bool
		mbps     float64
		want     string
	}{
		{"on with a stream key", true, false, 10, "ok"},
		{"on while signed in: a step would end the broadcast", true, true, 10, "warn"},
		{"off on a tight line: suggest it", false, false, 3, "warn"},
		{"off on a tight line, signed in: don't suggest it", false, true, 3, "skip"},
		{"off with room to spare", false, false, 20, "skip"},
		{"off, no upload test", false, false, 0, "skip"},
	} {
		obsProfile(t, c.signedIn)
		st := M{"stream_auto_downshift": c.auto, "network_test_mbps": c.mbps, "network_test_at": now()}
		if r := pcAutoQuality(st); r.Status != c.want {
			t.Errorf("%s: %s (%s)", c.name, r.Status, r.Detail)
		}
	}
}
