package server

// YouTube broadcast manager (tests/test_youtube.py): the pure payload builder, the
// no-creds guardrails, the login refresh paths, the update logic against a fake YouTube
// Data API, and the endpoint wiring. Everything goes through the real OAuth client.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func sp(s string) *string { return &s }

var curSnip = M{"title": "Old title", "description": "old desc", "scheduledStartTime": "2026-07-11T13:00:00Z"}

func TestSnippetPreservesCurrentWhenNothingChanges(t *testing.T) {
	s := ytSnippetPayload(curSnip, nil, nil)
	eqv(t, []any{s["title"], s["description"], s["scheduledStartTime"]}, []any{"Old title", "old desc", "2026-07-11T13:00:00Z"})
}

func TestSnippetAppliesChangesCarriesScheduledStart(t *testing.T) {
	s := ytSnippetPayload(curSnip, sp("New"), sp("ND"))
	eqv(t, []any{s["title"], s["description"], s["scheduledStartTime"]}, []any{"New", "ND", "2026-07-11T13:00:00Z"})
}

func TestSnippetNeverIncludesMadeForKids(t *testing.T) {
	// selfDeclaredMadeForKids must NEVER be in the payload — YouTube 403s any attempt to
	// modify it via update.
	s := ytSnippetPayload(curSnip, sp("X"), nil)
	for _, k := range []string{"selfDeclaredMadeForKids", "status"} {
		if _, ok := s[k]; ok {
			t.Fatal(k)
		}
	}
}

func TestSnippetDefaultsWhenBroadcastBare(t *testing.T) {
	s := ytSnippetPayload(M{}, nil, nil)
	eqv(t, []any{s["title"], s["description"]}, []any{"", ""})
	if _, ok := s["scheduledStartTime"]; ok {
		t.Fatal("nothing to carry")
	}
}

func TestSnippetEmptyStringsApply(t *testing.T) {
	// "" is a real value (clear the field), distinct from nil (leave unchanged)
	s := ytSnippetPayload(curSnip, sp(""), sp(""))
	eqv(t, []any{s["title"], s["description"]}, []any{"", ""})
}

func TestYouTubeMissingCredentialsCleanError(t *testing.T) {
	sandbox(t, "")
	ok, msg := UpdateYouTubeBroadcast(sp("X"), nil, nil, nil, false)
	if ok || !strings.Contains(msg, "yt_credentials.json") {
		t.Fatal(ok, msg)
	}
}

func TestYouTubeInvalidPrivacyShortCircuits(t *testing.T) {
	sandbox(t, "")
	ok, msg := UpdateYouTubeBroadcast(sp("X"), nil, sp("nope"), nil, false)
	if ok || !strings.Contains(msg, "privacy") {
		t.Fatal(ok, msg)
	}
}

// writeCreds writes a client file whose token endpoint is tokenURL.
func writeCreds(t *testing.T, tokenURL string) {
	t.Helper()
	b, _ := json.Marshal(M{"installed": M{"client_id": "x", "client_secret": "y", "token_uri": tokenURL}})
	os.WriteFile(YTCredsFile, b, 0o644)
}

func TestYouTubeNonInteractiveFirstRunRefusesCleanly(t *testing.T) {
	// A remote (tunnelled) first run must NOT open a browser on the host.
	sandbox(t, "")
	writeCreds(t, "http://127.0.0.1:1/token")
	called := withInteractiveLogin(t)
	yt, msg := ytService(false)
	if yt != nil || !strings.Contains(msg, "STREAMING LAPTOP") {
		t.Fatal(msg)
	}
	ok, msg := UpdateYouTubeBroadcast(sp("X"), nil, nil, nil, false)
	if ok || !strings.Contains(strings.ToLower(msg), "authoris") {
		t.Fatal(msg)
	}
	if *called {
		t.Fatal("opened a login")
	}
}

func withInteractiveLogin(t *testing.T) *bool {
	t.Helper()
	called := false
	saved := interactiveYTLogin
	interactiveYTLogin = func(*oauth2.Config) (*oauth2.Token, error) {
		called = true
		return nil, errors.New("test: no browser")
	}
	t.Cleanup(func() { interactiveYTLogin = saved })
	return &called
}

// expiredLogin: a stored login whose refresh Google rejects (revoked, or a Testing-mode
// app's 7-day expiry) — found on the maintainer's laptop, where it raised a traceback
// out of every YouTube call.
func expiredLogin(t *testing.T, tokenURL string) {
	t.Helper()
	sandbox(t, "")
	writeCreds(t, tokenURL)
	b, _ := json.Marshal(M{"token": "old", "refresh_token": "r", "expiry": "2020-01-01T00:00:00.000000Z"})
	os.WriteFile(YTTokenFile, b, 0o600)
}

func rejectingTokenEndpoint(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestYouTubeExpiredLoginSaysReconnectOpensNoBrowser(t *testing.T) {
	expiredLogin(t, rejectingTokenEndpoint(t))
	called := withInteractiveLogin(t)
	yt, msg := ytService(false)
	if yt != nil || !strings.Contains(msg, "expired") || !strings.Contains(msg, "Update YouTube broadcast now") {
		t.Fatal(msg)
	}
	if *called {
		t.Fatal("opened a login remotely")
	}
}

func TestYouTubeExpiredLoginOnTheLaptopOpensLogin(t *testing.T) {
	expiredLogin(t, rejectingTokenEndpoint(t))
	called := withInteractiveLogin(t)
	ytService(true)
	if !*called {
		t.Fatal("no login opened on the laptop itself")
	}
}

func TestYouTubeNetworkTroubleIsNotExpiredLogin(t *testing.T) {
	expiredLogin(t, "http://127.0.0.1:1/token") // nothing listens: a network failure
	called := withInteractiveLogin(t)
	yt, msg := ytService(true)
	if yt != nil || !strings.Contains(msg, "Couldn't reach Google") {
		t.Fatal(msg)
	}
	if *called {
		t.Fatal("popped a login because the wifi blipped")
	}
}

// ── The update logic against a fake YouTube Data API ──

type ytCall struct {
	method, kind, part string
	query              string
	body               M
}

type fakeYouTube struct {
	mu                     sync.Mutex
	active, upcoming, mine []any
	mineSet                bool
	video                  []any
	fail                   map[[2]string]bool // (kind, part) → 403
	calls                  []ytCall
}

func newFakeYouTube(t *testing.T, f *fakeYouTube) *fakeYouTube {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		kind := strings.TrimPrefix(r.URL.Path, "/")
		q := r.URL.Query()
		c := ytCall{method: r.Method, kind: kind, part: q.Get("part"), query: r.URL.RawQuery}
		if r.Method == "PUT" {
			json.NewDecoder(r.Body).Decode(&c.body)
		}
		f.calls = append(f.calls, c)
		if r.Method == "PUT" {
			if f.fail[[2]string{kind, c.part}] {
				w.WriteHeader(403)
				io.WriteString(w, `{"error":{"message":"some API error"}}`)
				return
			}
			io.WriteString(w, "{}")
			return
		}
		var items []any
		switch {
		case kind == "videos":
			items = f.video
		case q.Get("mine") == "true":
			if f.mineSet {
				items = f.mine
			} else {
				items = append(append([]any{}, f.active...), f.upcoming...)
			}
		case q.Get("broadcastStatus") == "active":
			items = f.active
		case q.Get("broadcastStatus") == "upcoming":
			items = f.upcoming
		}
		if items == nil {
			items = []any{}
		}
		json.NewEncoder(w).Encode(M{"items": items})
	}))
	t.Cleanup(srv.Close)
	saved := ytAPI
	ytAPI = srv.URL + "/"
	t.Cleanup(func() { ytAPI = saved })
	// A signed-in laptop: a valid stored token, so no refresh and no login.
	writeCreds(t, "http://127.0.0.1:1/token")
	b, _ := json.Marshal(M{"token": "good", "refresh_token": "r",
		"expiry": time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000000Z")})
	os.WriteFile(YTTokenFile, b, 0o600)
	return f
}

func (f *fakeYouTube) updates(kind, part string) []ytCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ytCall
	for _, c := range f.calls {
		if c.method == "PUT" && c.kind == kind && (part == "" || c.part == part) {
			out = append(out, c)
		}
	}
	return out
}

func standardYT(t *testing.T, fail map[[2]string]bool) *fakeYouTube {
	sandbox(t, "")
	return newFakeYouTube(t, &fakeYouTube{
		active: []any{M{"id": "B1", "snippet": M{"title": "Old", "scheduledStartTime": "2026-07-11T13:00:00Z"}}},
		video:  []any{M{"snippet": M{"title": "Old", "categoryId": "1"}}},
		fail:   fail})
}

func TestYouTubeNeverSendsMadeForKidsAndSplitsCalls(t *testing.T) {
	f := standardYT(t, nil)
	ok, msg := UpdateYouTubeBroadcast(sp("New"), sp("D"), sp("unlisted"), sp("17"), false)
	if !ok {
		t.Fatal(msg)
	}
	for _, c := range f.calls {
		b, _ := json.Marshal(c.body)
		if strings.Contains(string(b), "selfDeclaredMadeForKids") {
			t.Fatal("made-for-kids sent")
		}
	}
	// title/description → a snippet-only broadcast update, scheduledStartTime carried
	snip := f.updates("liveBroadcasts", "snippet")
	mustEq(t, len(snip), 1)
	if _, ok := snip[0].body["status"]; ok {
		t.Fatal("status in the snippet update")
	}
	eqv(t, gm(snip[0].body, "snippet")["title"], "New")
	eqv(t, gm(snip[0].body, "snippet")["scheduledStartTime"], "2026-07-11T13:00:00Z")
	// privacy → a status update carrying ONLY privacyStatus
	stat := f.updates("liveBroadcasts", "status")
	mustEq(t, len(stat), 1)
	eqv(t, stat[0].body["status"], map[string]any{"privacyStatus": "unlisted"})
	// category → the video resource
	vid := f.updates("videos", "")
	mustEq(t, len(vid), 1)
	eqv(t, gm(vid[0].body, "snippet")["categoryId"], "17")
}

func TestYouTubePartialFailureReportedNotSwallowed(t *testing.T) {
	// privacy fails (as the real 403 would); title/description + category still apply
	standardYT(t, map[[2]string]bool{{"liveBroadcasts", "status"}: true})
	ok, msg := UpdateYouTubeBroadcast(sp("New"), nil, sp("private"), sp("17"), false)
	if ok {
		t.Fatal("a part failed, so overall not ok")
	}
	for _, want := range []string{"FAILED", "privacy", "title/description"} {
		if !strings.Contains(msg, want) {
			t.Fatal(want, msg)
		}
	}
}

func TestYouTubeNoBroadcastFound(t *testing.T) {
	sandbox(t, "")
	newFakeYouTube(t, &fakeYouTube{})
	ok, msg := UpdateYouTubeBroadcast(sp("X"), nil, nil, nil, false)
	if ok || !strings.Contains(msg, "No broadcast found") || !strings.Contains(msg, "Go Live") {
		t.Fatal(ok, msg)
	}
}

func TestYouTubeFallbackFindsTransitionalBroadcast(t *testing.T) {
	// active + upcoming both empty, but the channel has a 'testing' broadcast — the
	// mine=true fallback must find it (the 'no active stream' timing case).
	sandbox(t, "")
	f := newFakeYouTube(t, &fakeYouTube{mineSet: true,
		mine: []any{M{"id": "B9", "status": M{"lifeCycleStatus": "testing"},
			"snippet": M{"title": "T", "scheduledStartTime": "2026-07-11T13:00:00Z"}}},
		video: []any{M{"snippet": M{"title": "T", "categoryId": "1"}}}})
	ok, msg := UpdateYouTubeBroadcast(sp("New"), nil, sp("unlisted"), nil, false)
	if !ok {
		t.Fatal(msg)
	}
	found := false
	for _, c := range f.calls {
		found = found || (c.method == "GET" && strings.Contains(c.query, "mine=true"))
	}
	if !found {
		t.Fatal("no mine=true fallback")
	}
}

func TestYouTubeCompletedBroadcastStillTargeted(t *testing.T) {
	// 'OBS is streaming' but the persistent broadcast reads as complete after a restart
	// — the finder must still target it rather than refusing.
	sandbox(t, "")
	f := newFakeYouTube(t, &fakeYouTube{mineSet: true,
		mine: []any{M{"id": "persistent", "status": M{"lifeCycleStatus": "complete"},
			"snippet": M{"title": "My Match", "scheduledStartTime": "2026-07-11T13:00:00Z"}}},
		video: []any{M{"snippet": M{"title": "My Match", "categoryId": "1"}}}})
	ok, msg := UpdateYouTubeBroadcast(sp("New"), nil, sp("unlisted"), nil, false)
	if !ok || !strings.Contains(msg, "My Match") { // names the broadcast it targeted
		t.Fatal(ok, msg)
	}
	eqv(t, f.updates("liveBroadcasts", "")[0].body["id"], "persistent")
}

func TestYouTubeRevokedBroadcastsSkipped(t *testing.T) {
	sandbox(t, "")
	f := newFakeYouTube(t, &fakeYouTube{mineSet: true, video: []any{M{"snippet": M{"categoryId": "1"}}},
		mine: []any{
			M{"id": "gone", "status": M{"lifeCycleStatus": "revoked"}, "snippet": M{"scheduledStartTime": "2026-07-12T10:00:00Z"}},
			M{"id": "real", "status": M{"lifeCycleStatus": "ready"}, "snippet": M{"scheduledStartTime": "2026-07-11T13:00:00Z"}},
		}})
	UpdateYouTubeBroadcast(sp("New"), nil, nil, nil, false)
	eqv(t, f.updates("liveBroadcasts", "")[0].body["id"], "real") // revoked skipped despite newer
}

// ── The endpoint ──

func youtubeEndpoint(t *testing.T) (*testServer, *fakeYouTube) {
	t.Helper()
	s := newTestServer(t, "")
	setState(t, M{"home_team": "Alpha CC", "away_team": "Beta CC",
		"youtube_title_template": "LIVE: {home} vs {away}", "youtube_description": "Watch {home} v {away}.",
		"youtube_privacy": "public", "youtube_category": "17"})
	f := newFakeYouTube(t, &fakeYouTube{
		active: []any{M{"id": "B1", "snippet": M{"title": "Old"}}},
		video:  []any{M{"snippet": M{"title": "Old", "categoryId": "1"}}}})
	return s, f
}

func TestYouTubeEndpointFillsTemplatesUsesStateDefaults(t *testing.T) {
	s, f := youtubeEndpoint(t)
	st, d := s.postJSON("/youtube/update", M{})
	mustEq(t, st, 200)
	mustEq(t, d["ok"], any(true), d)
	snip := gm(f.updates("liveBroadcasts", "snippet")[0].body, "snippet")
	eqv(t, snip["title"], "LIVE: Alpha CC vs Beta CC")
	eqv(t, snip["description"], "Watch Alpha CC v Beta CC.")
	eqv(t, gm(f.updates("liveBroadcasts", "status")[0].body, "status")["privacyStatus"], "public")
	eqv(t, gm(f.updates("videos", "")[0].body, "snippet")["categoryId"], "17")
}

func TestYouTubeEndpointExplicitPayloadOverridesState(t *testing.T) {
	s, f := youtubeEndpoint(t)
	s.postJSON("/youtube/update", M{"title": "Cup Final", "description": "Big one", "privacy": "private", "category": "24"})
	snip := gm(f.updates("liveBroadcasts", "snippet")[0].body, "snippet")
	eqv(t, []any{snip["title"], snip["description"]}, []any{"Cup Final", "Big one"})
	eqv(t, gm(f.updates("liveBroadcasts", "status")[0].body, "status")["privacyStatus"], "private")
	eqv(t, gm(f.updates("videos", "")[0].body, "snippet")["categoryId"], "24")
}

func TestYouTubeEndpointFailureReportedAsOKFalse(t *testing.T) {
	s := newTestServer(t, "")
	newFakeYouTube(t, &fakeYouTube{mineSet: true})
	st, d := s.postJSON("/youtube/update", M{})
	mustEq(t, st, 200)
	mustEq(t, d["ok"], any(false))
	if !strings.Contains(gs(d, "error", ""), "broadcast") {
		t.Fatal(d)
	}
}

func TestYouTubeUpdateIsTokenGated(t *testing.T) {
	s := newTestServer(t, testPW)
	st, _ := s.postJSON("/youtube/update", M{"title": "x"})
	mustEq(t, st, 401)
}
