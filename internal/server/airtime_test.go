package server

// Sponsor airtime: how long the weekend sponsor's strap was on screen while live
// (tests/test_sponsor_airtime.py).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/assets"
)

const airT0 = 1791000000.0 // a fixed clock, so every interval lands on one known day

var airDay = time.Unix(int64(airT0), 0).Format("2006-01-02")

func airSandbox(t *testing.T) {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"sponsor_name": "Acme Builders"})
	sponsorAirMu.Lock()
	sponsorAirOpen = nil
	sponsorAirMu.Unlock()
	viewersMu.Lock()
	saved := viewers
	viewers.count, viewers.has, viewers.at, viewers.video, viewers.yt, viewers.err = 0, false, 0, "", nil, ""
	viewersMu.Unlock()
	t.Cleanup(func() {
		viewersMu.Lock()
		viewers = saved
		viewersMu.Unlock()
		setLive(false)
	})
	setLive(true)
}

func setLive(on bool) {
	streamMonMu.Lock()
	streamMon["streaming"] = on
	streamMonMu.Unlock()
}

func setViewers(count int, at float64) {
	viewersMu.Lock()
	viewers.count, viewers.has, viewers.at = count, true, at
	viewersMu.Unlock()
}

func showFor(start, secs float64) {
	SponsorAirtimeEvent("show", airT0+start)
	SponsorAirtimeEvent("hide", airT0+start+secs)
}

func airToday() M {
	s := gl(SponsorAirtimeSummary(airDay), "sponsors")
	if len(s) == 0 {
		return nil
	}
	return s[0].(M)
}

func TestAirtimeLiveAppearancesAddUp(t *testing.T) {
	airSandbox(t)
	showFor(0, 7)
	showFor(60, 5)
	showFor(120, 9)
	rec := airToday()
	eqv(t, rec["on_air_sec"], 21)
	eqv(t, rec["on_air_shows"], 3)
	if !strings.Contains(gs(rec, "line", ""), "Acme Builders was on screen for 21 s of the live stream, across 3 appearances.") {
		t.Fatal(rec["line"])
	}
}

func TestAirtimeMinutesInTheLine(t *testing.T) {
	airSandbox(t)
	for i := 0; i < 20; i++ {
		showFor(float64(i*60), 9)
	}
	if !strings.Contains(gs(airToday(), "line", ""), "3 min 0 s") {
		t.Fatal(airToday()["line"])
	}
}

func TestAirtimeBeforeGoingLiveCountedSeparately(t *testing.T) {
	// A rehearsal: on screen, but nobody was watching. Never presented as airtime.
	airSandbox(t)
	setLive(false)
	showFor(0, 7)
	setLive(true)
	showFor(60, 5)
	rec := airToday()
	eqv(t, []any{rec["on_air_sec"], rec["on_air_shows"]}, []any{5, 1})
	eqv(t, []any{rec["off_air_sec"], rec["off_air_shows"]}, []any{7, 1})
}

func TestAirtimeGoingLiveMidAppearanceCounts(t *testing.T) {
	airSandbox(t)
	setLive(false)
	SponsorAirtimeEvent("show", airT0)
	setLive(true)
	SponsorAirtimeEvent("hide", airT0+6)
	eqv(t, airToday()["on_air_shows"], 1)
}

func TestAirtimeMissedHideIsCapped(t *testing.T) {
	// Overlay reloaded mid-panel: the next "show" closes the old one, capped.
	airSandbox(t)
	SponsorAirtimeEvent("show", airT0)
	SponsorAirtimeEvent("show", airT0+3600)
	SponsorAirtimeEvent("hide", airT0+3607)
	rec := airToday()
	eqv(t, rec["on_air_sec"], SponsorShowMaxSec+7)
	eqv(t, rec["on_air_shows"], 2)
}

func TestAirtimeStrayHideCountsNothing(t *testing.T) {
	airSandbox(t)
	SponsorAirtimeEvent("hide", airT0)
	if airToday() != nil {
		t.Fatal(airToday())
	}
}

func TestAirtimeNoSponsorSetCountsNothing(t *testing.T) {
	airSandbox(t)
	setState(t, M{"sponsor_name": ""})
	showFor(0, 7)
	if airToday() != nil {
		t.Fatal(airToday())
	}
}

func TestAirtimeViewerMinutes(t *testing.T) {
	airSandbox(t)
	setViewers(40, airT0)
	showFor(0, 9)
	showFor(60, 6)
	rec := airToday()
	eqv(t, rec["viewer_minutes"], 10)
	eqv(t, rec["viewer_covered_sec"], 15)
	if !strings.Contains(gs(rec, "line", ""), "an estimated 10 viewer-minutes (YouTube's live viewer count × time on screen).") {
		t.Fatal(rec["line"])
	}
}

func TestAirtimeStaleViewerCountNotUsed(t *testing.T) {
	airSandbox(t)
	setViewers(40, airT0-1000)
	showFor(0, 9)
	rec := airToday()
	eqv(t, rec["viewer_minutes"], 0)
	if !strings.HasSuffix(gs(rec, "line", ""), "across 1 appearance.") {
		t.Fatal(rec["line"])
	}
}

func TestAirtimePartialCoverageSaidNotScaled(t *testing.T) {
	airSandbox(t)
	setViewers(60, airT0)
	showFor(0, 9)    // the count is fresh
	showFor(1000, 6) // the count is 16 minutes old by now
	rec := airToday()
	eqv(t, rec["viewer_minutes"], 9)
	if !strings.Contains(gs(rec, "line", ""), "an estimated 9 viewer-minutes over the 9 s of it that had a YouTube viewer count.") {
		t.Fatal(rec["line"])
	}
}

func TestAirtimeViewersNeverCountedBeforeLive(t *testing.T) {
	airSandbox(t)
	setLive(false)
	setViewers(40, airT0)
	showFor(0, 9)
	eqv(t, airToday()["viewer_minutes"], 0)
}

func TestAirtimeSurvivesRestart(t *testing.T) {
	// Totals are on disk, not just in memory: the launcher restarts a crashed server.
	airSandbox(t)
	showFor(0, 7)
	sponsorAirMu.Lock()
	sponsorAirOpen = nil
	sponsorAirMu.Unlock()
	showFor(60, 5)
	eqv(t, airToday()["on_air_shows"], 2)
}

func TestAirtimeNeverRaises(t *testing.T) {
	airSandbox(t)
	SponsorAirtimeFile = filepath.Join(t.TempDir(), "missing", "dir", "x.json") // unwritable
	showFor(0, 5)                                                               // must not panic into the handler
	StateFile = t.TempDir()                                                     // unreadable state: a directory
	SponsorAirtimeEvent("show", 0)
}

func TestAirtimeHTTPOverlayBeaconNeedsNoLogin(t *testing.T) {
	// The overlay is a loopback OBS source with no login flow.
	airSandbox(t)
	s := &testServer{t: t}
	srv := httptest.NewServer(Handler{})
	t.Cleanup(srv.Close)
	s.srv, s.addr = srv, strings.TrimPrefix(srv.URL, "http://")
	ClubPassword = "secret"
	st, _ := s.postJSON("/sponsor/airtime", M{"event": "show"}, map[string]string{"Origin": srv.URL})
	mustEq(t, st, 200)
	ClubPassword = ""
	st, _ = s.postJSON("/sponsor/airtime", M{"event": "credit-me-an-hour"}, map[string]string{"Origin": srv.URL})
	mustEq(t, st, 400)
}

func TestAirtimeSummaryNotSwallowedByLogoRoute(t *testing.T) {
	airSandbox(t)
	srv := httptest.NewServer(Handler{})
	t.Cleanup(srv.Close)
	showFor(0, 7)
	r, err := http.Get(srv.URL + "/sponsor/airtime?date=" + airDay)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var d M
	json.NewDecoder(r.Body).Decode(&d)
	eqv(t, gl(d, "sponsors")[0].(M)["on_air_shows"], 1)
}

func overlaySource(t *testing.T) string {
	t.Helper()
	b, err := assets.Embedded("overlay.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func after(t *testing.T, src, marker string, n int) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("%q not found", marker)
	}
	s := src[i:]
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func TestOverlayBeaconOnlyOnRealChange(t *testing.T) {
	src := overlaySource(t)
	if !strings.Contains(after(t, src, "function showSponsorStripFor(", 600), "if (!s.classList.contains('show')) sponsorAirtime('show');") {
		t.Fatal("show beacon")
	}
	if !strings.Contains(after(t, src, "function fadeSponsorStrap(", 300), "sponsorAirtime('hide')") {
		t.Fatal("hide beacon")
	}
}

func TestOverlayBeaconNeverFromPreview(t *testing.T) {
	if !strings.Contains(after(t, overlaySource(t), "function sponsorAirtime(", 120), "if (SPONSOR_PREVIEW) return;") {
		t.Fatal("preview guard")
	}
}

// ── The once-a-minute YouTube viewer count, against a fake YouTube ──

type fakeYT struct {
	mu         sync.Mutex
	broadcasts []any
	details    M
	calls      []string
	queries    []string
}

func newFakeYT(t *testing.T) *fakeYT {
	t.Helper()
	airSandbox(t)
	for _, f := range []string{YTCredsFile, YTTokenFile} {
		os.WriteFile(f, nil, 0o644)
	}
	f := &fakeYT{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/"))
		f.queries = append(f.queries, r.URL.RawQuery)
		var out M
		if strings.HasSuffix(r.URL.Path, "liveBroadcasts") {
			out = M{"items": f.broadcasts}
		} else {
			out = M{"items": []any{M{"liveStreamingDetails": f.details}}}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	savedAPI := ytAPI
	ytAPI = srv.URL + "/"
	t.Cleanup(func() { ytAPI = savedAPI })
	viewersMu.Lock()
	viewers.yt = &YTClient{http: srv.Client()}
	viewersMu.Unlock()
	return f
}

func (f *fakeYT) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func TestViewerSamplesActiveBroadcast(t *testing.T) {
	f := newFakeYT(t)
	f.broadcasts = []any{M{"id": "vid1"}}
	f.details = M{"concurrentViewers": "57"}
	viewerTick(airT0)
	n, ok := liveViewers(airT0 + 30)
	if !ok || n != 57 {
		t.Fatal(n, ok)
	}
	if !strings.Contains(f.queries[0], "broadcastStatus=active") { // never a guessed past stream
		t.Fatal(f.queries[0])
	}
	if !strings.Contains(f.queries[1], "id=vid1") || !strings.Contains(f.queries[1], "part=liveStreamingDetails") {
		t.Fatal(f.queries[1])
	}
}

func TestViewerNoActiveBroadcastNoCount(t *testing.T) {
	f := newFakeYT(t)
	viewerTick(airT0)
	if _, ok := liveViewers(airT0); ok {
		t.Fatal("count without a broadcast")
	}
	if f.called("videos") {
		t.Fatal("asked for a video without a broadcast")
	}
}

func TestViewerNotLiveMakesNoAPICalls(t *testing.T) {
	f := newFakeYT(t)
	setLive(false)
	viewersMu.Lock()
	viewers.video = "old"
	viewersMu.Unlock()
	viewerTick(airT0)
	if f.called("liveBroadcasts") {
		t.Fatal("API called while not live")
	}
	viewersMu.Lock()
	defer viewersMu.Unlock()
	mustEq(t, viewers.video, "")
}

func TestViewerEndedBroadcastForgotten(t *testing.T) {
	f := newFakeYT(t)
	viewersMu.Lock()
	viewers.video = "vid1"
	viewersMu.Unlock()
	f.details = M{"actualEndTime": "2026-10-02T17:00:00Z"}
	viewerTick(airT0)
	viewersMu.Lock()
	v := viewers.video
	viewersMu.Unlock()
	mustEq(t, v, "")
	if _, ok := liveViewers(airT0); ok {
		t.Fatal("count from an ended broadcast")
	}
}

func TestViewerYouTubeNotSetUpDoesNothing(t *testing.T) {
	f := newFakeYT(t)
	os.Remove(YTTokenFile)
	viewerTick(airT0)
	if f.called("liveBroadcasts") {
		t.Fatal("API called without YouTube set up")
	}
}
