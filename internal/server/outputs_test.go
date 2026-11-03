package server

// The server halves of tests/test_match_page.py, tests/test_social_clips.py and
// tests/test_voice.py (the pure renderers are tested in their own packages).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/ai"
)

// ── The match page ──

func matchPageSandbox(t *testing.T) *testServer {
	t.Helper()
	s := newTestServer(t, "")
	setState(t, M{"pc_match_id": "mp1"})
	facts := M{"ok": true, "date": "02 Oct 2026", "outcome": "win", "margin": "by 5 wickets",
		"sides": []any{M{"club": "Home CC", "won": false}, M{"club": "Rival CC", "won": true}}}
	db := M{"innings": M{"1": M{"score": 33, "wickets": 1, "overs": "5"}, "2": M{"score": 73, "wickets": 5, "overs": "9.4"}},
		"batters": M{"1": []any{M{"name": "Smith", "runs": 19, "balls": 13, "out": false}}}, "bowlers": M{},
		"fall_of_wickets": []any{M{"batter": "P Smith", "score": "7-1", "innings": "1"}}}
	sf, sdb, sr, sc, sv := GenerateSocialGraphicFacts, pageDBFacts, GenerateMatchReport, pageCard, pageVideoURL
	t.Cleanup(func() {
		GenerateSocialGraphicFacts, pageDBFacts, GenerateMatchReport, pageCard, pageVideoURL = sf, sdb, sr, sc, sv
	})
	GenerateSocialGraphicFacts = func() M { return facts }
	pageDBFacts = func(string) M { return db }
	GenerateMatchReport = func(string) M { return M{"ok": true, "text": "A fine chase."} }
	pageCard = func(M, string, string) (string, error) { return "", errors.New("no") }
	pageVideoURL = func() string { return "https://youtu.be/abc" }
	err := withDB(func(tx *sql.Tx) error {
		for _, r := range [][3]int{{1, 0, 6}, {1, 4, 33}, {2, 0, 4}, {2, 9, 73}, {2, 10, 80}} {
			if _, err := tx.Exec("INSERT INTO balls (match_id, innings, over, ball, cum_runs) VALUES (?,?,?,?,?)", "mp1", r[0], r[1], 1, r[2]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func buildPage(t *testing.T) (string, string) {
	t.Helper()
	ok, msg, path := BuildMatchPage()
	if !ok {
		t.Fatal(msg)
	}
	b, _ := os.ReadFile(path)
	return path, string(b)
}

func TestMatchPageBuiltFromTheMatch(t *testing.T) {
	matchPageSandbox(t)
	path, page := buildPage(t)
	if !strings.HasSuffix(filepath.Base(path), "_home-cc-v-rival-cc.html") {
		t.Fatal(path)
	}
	contains(t, page, "Rival CC won by 5 wickets", "A fine chase.", "https://youtu.be/abc",
		"Fall of wickets: 7-1 (P Smith)", "Runs by over")
}

// The moments of a match, each linked to its place in the video: the match's own video, not
// just today's, and 25s before the scorer entered it, to open on the delivery.
func TestMatchPageLinksMomentsIntoTheVideo(t *testing.T) {
	matchPageSandbox(t)
	start := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	add := func(key, kind, text string, after time.Duration) {
		t.Helper()
		withDBExec(t, "INSERT INTO moments(match_id,key,ts,kind,text) VALUES(?,?,?,?,?)", "mp1", key, ts(start.Add(after)), kind, text)
	}
	add("i0", "innings", "Before the stream", -time.Hour) // not an innings on the page, and no footage
	add("i1", "innings", "Home CC batting", 10*time.Second)
	add("w1-1", "wicket", "Wicket: P Smith 3, 7/1 (2.1 ov)", 45*time.Minute)
	add("m1-Smith-50", "milestone", "Smith 50", time.Hour+30*time.Minute+25*time.Second)
	add("i2", "innings", "Rival CC batting", 2*time.Hour)
	add("w2-9", "wicket", "After the stream stopped", 9*time.Hour)

	saved := pageMatchVideo
	t.Cleanup(func() { pageMatchVideo = saved })
	var asked []Moment
	pageMatchVideo = func(m []Moment) (string, time.Time, time.Time) {
		asked = m
		return "vid1", start, start.Add(4 * time.Hour)
	}
	ok, msg, path := BuildMatchPage()
	if !ok {
		t.Fatal(msg)
	}
	if len(asked) != 6 {
		t.Errorf("the video was found from %d moments, not all 6", len(asked))
	}
	contains(t, msg, "(4 moments link to the video)")
	b, _ := os.ReadFile(path)
	page := string(b)
	contains(t, page,
		`<a class=watch href="https://youtu.be/vid1">`,
		// The start can't go before the video begins.
		`<a href="https://youtu.be/vid1?t=0"><span class=clock>&#9654; 0:00</span> Home CC batting</a>`,
		`<a href="https://youtu.be/vid1?t=2675"><span class=clock>&#9654; 44:35</span> Wicket: P Smith 3, 7/1 (2.1 ov)</a>`,
		`<a href="https://youtu.be/vid1?t=5400"><span class=clock>&#9654; 1:30:00</span> Smith 50</a>`,
		`<a href="https://youtu.be/vid1?t=7175"><span class=clock>&#9654; 1:59:35</span> Rival CC batting</a>`,
		// The scorecard's own wicket goes to the same place.
		`<a href="https://youtu.be/vid1?t=2675">7-1 (P Smith)</a>`)
	for _, bad := range []string{"Before the stream", "After the stream stopped"} {
		if strings.Contains(page, bad) {
			t.Errorf("a moment with no footage is on the page: %q", bad)
		}
	}
	// In their own innings: the first innings' wicket comes before the reply starts.
	if strings.Index(page, "Wicket: P Smith") > strings.Index(page, "Rival CC batting") {
		t.Error("moments are in the wrong innings")
	}
}

// The real thing end to end: frames through the real loggers, the real facts from the DB.
// The moments and the fall of wickets are logged separately, so this is what shows their
// keys still meet: a mismatch would lose the scorecard's links with nothing to say so.
func TestMatchPageLinksFromRealFrames(t *testing.T) {
	matchPageSandbox(t)
	saved, savedNow, savedVideo := pageDBFacts, momentNow, pageMatchVideo
	momentsPrev = momentsPrevT{}
	livePrev = livePrevT{}
	t.Cleanup(func() {
		pageDBFacts, momentNow, pageMatchVideo = saved, savedNow, savedVideo
		momentsPrev = momentsPrevT{}
		livePrev = livePrevT{}
	})
	pageDBFacts = MatchFactsFromDB
	start := time.Date(2026, 6, 6, 13, 0, 0, 0, time.UTC)
	at := start
	momentNow = func() time.Time { return at }
	pageMatchVideo = func([]Moment) (string, time.Time, time.Time) { return "vid1", start, start.Add(6 * time.Hour) }
	step := func(d time.Duration, f M) {
		at = at.Add(d)
		LogLiveFigures(f)
		LogMoments(f)
	}
	step(time.Minute, momentFrame(1, 1, 0, 0.1, "Smith", 1, "Jones", 0, "Patel", 0))
	step(10*time.Minute, momentFrame(1, 40, 0, 6, "Smith", 31, "Jones", 9, "Patel", 0))
	step(time.Minute, momentFrame(1, 41, 1, 6.2, "Smith", 32, "Brown", 0, "Khan", 1)) // Jones out
	step(10*time.Minute, momentFrame(1, 70, 1, 10, "Smith", 52, "Brown", 8, "Khan", 1))
	step(time.Minute, momentFrame(2, 3, 0, 0.3, "Ali", 3, "Cole", 0, "Smith", 0))
	step(time.Minute, momentFrame(2, 4, 1, 0.5, "Ali", 4, "Hart", 0, "Smith", 1)) // Cole out

	ok, msg, path := BuildMatchPage()
	if !ok {
		t.Fatal(msg)
	}
	b, _ := os.ReadFile(path)
	page := string(b)
	// 12 minutes in, less the lead: 11:35. And the reply's, 24 minutes in: 23:35.
	contains(t, page, `<a href="https://youtu.be/vid1?t=695">41-1 (Jones)</a>`,
		`<a href="https://youtu.be/vid1?t=1415">4-1 (Cole)</a>`,
		`<span class=clock>&#9654; 21:35</span> Smith 50`)
	// Each innings has its own wicket 1, and the fall of wickets didn't cross them.
	first, second := strings.Index(page, "41-1 (Jones)"), strings.Index(page, "4-1 (Cole)")
	if first < 0 || second < first {
		t.Errorf("the innings' wickets are out of order: %d %d", first, second)
	}
}

// YouTube being signed out, or down, costs the links and never the page. It falls back to
// the stream link it always had.
func TestMatchPageWithoutTheVideoHasNoLinks(t *testing.T) {
	matchPageSandbox(t)
	withDBExec(t, "INSERT INTO moments(match_id,key,ts,kind,text) VALUES(?,?,?,?,?)", "mp1", "w1-1", 1000.0, "wicket", "Wicket: P Smith")
	saved := pageMatchVideo
	t.Cleanup(func() { pageMatchVideo = saved })
	for name, find := range map[string]func([]Moment) (string, time.Time, time.Time){
		"no video": func([]Moment) (string, time.Time, time.Time) { return "", time.Time{}, time.Time{} },
		"a panic":  func([]Moment) (string, time.Time, time.Time) { panic("x") },
	} {
		pageMatchVideo = find
		ok, msg, path := BuildMatchPage()
		if !ok {
			t.Fatalf("%s: %s", name, msg)
		}
		b, _ := os.ReadFile(path)
		page := string(b)
		contains(t, page, "https://youtu.be/abc", "Fall of wickets: 7-1 (P Smith)")
		if strings.Contains(page, "Watch it back") || strings.Contains(page, "?t=") {
			t.Errorf("%s: links with no video to link to", name)
		}
	}
}

func TestMomentInVideo(t *testing.T) {
	start := time.Date(2026, 6, 6, 13, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for _, c := range []struct {
		name  string
		after time.Duration
		noEnd bool
		at    time.Duration
		ok    bool
	}{
		{name: "before the video", after: -time.Second},
		{name: "after the video", after: time.Hour + time.Second},
		{name: "right at the start", after: 5 * time.Second, at: 0, ok: true},
		{name: "in the middle", after: 30 * time.Minute, at: 30*time.Minute - chapterLead, ok: true},
		{name: "the last moment", after: time.Hour, at: time.Hour - chapterLead, ok: true},
		{name: "a video still going", after: 3 * time.Hour, noEnd: true, at: 3*time.Hour - chapterLead, ok: true},
	} {
		e := end
		if c.noEnd {
			e = time.Time{}
		}
		at, ok := momentInVideo(Moment{TS: ts(start.Add(c.after))}, start, e)
		if ok != c.ok || at != c.at {
			t.Errorf("%s: %v %v, want %v %v", c.name, at, ok, c.at, c.ok)
		}
	}
}

func TestMatchPageChartStopsAtInningsOwnOvers(t *testing.T) {
	matchPageSandbox(t)
	_, page := buildPage(t)
	// innings 2 finished in 9.4 overs: the stray ball logged in over 11 is not drawn
	contains(t, page, ">10</text>")
	if strings.Contains(page, ">11</text>") {
		t.Fatal("drew an over past the innings' end")
	}
}

func TestMatchPageFailedPartNeverStopsThePage(t *testing.T) {
	matchPageSandbox(t)
	GenerateMatchReport = func(string) M { panic("x") }
	_, page := buildPage(t)
	if strings.Contains(page, "Match report") {
		t.Fatal("report section without a report")
	}
	contains(t, page, "Rival CC won")
}

func TestMatchPageNoMatchYet(t *testing.T) {
	matchPageSandbox(t)
	GenerateSocialGraphicFacts = func() M { return M{"ok": false, "error": "No match data yet"} }
	ok, msg, path := BuildMatchPage()
	eqv(t, []any{ok, msg, path}, []any{false, "No match data yet", ""})
}

func TestMatchPageRoutes(t *testing.T) {
	s := matchPageSandbox(t)
	mustEq(t, s.get("/match/page/latest").status, 404)
	buildPage(t)
	r := s.get("/match/page/latest")
	mustEq(t, r.status, 200)
	contains(t, r.header.Get("Content-Type"), "text/html")
	contains(t, string(r.body), "Rival CC won")
	st, d := s.getJSON("/match/page")
	mustEq(t, st, 200)
	if !strings.HasSuffix(gs(d, "latest", ""), ".html") {
		t.Fatal(d)
	}
}

func TestMatchPageSecondBuildWhileOneRunsRefused(t *testing.T) {
	s := matchPageSandbox(t)
	matchPageMu.Lock()
	saved := matchPageStatus["running"]
	matchPageStatus["running"] = true
	matchPageMu.Unlock()
	t.Cleanup(func() {
		matchPageMu.Lock()
		matchPageStatus["running"] = saved
		matchPageMu.Unlock()
	})
	st, d := s.postJSON("/match/page", M{})
	mustEq(t, st, 409)
	mustEq(t, d["ok"], any(false))
}

func TestMatchPageBuiltAutomaticallyWhenStreamEnds(t *testing.T) {
	src := packageSource(t)["streamquality.go"]
	contains(t, src, "time.AfterFunc(60*time.Second, matchPageAfterStreamEnd)")
}

func TestMatchPageNotBuiltIfStreamCameBack(t *testing.T) {
	// The quality ladder's stop/start (or an operator restart) reads as an end for one
	// monitor tick; building then would spend an AI report mid-match.
	builds := 0
	saved := startPageJob
	startPageJob = func() bool { builds++; return true }
	t.Cleanup(func() { startPageJob = saved; setLive(false) })
	setLive(true)
	matchPageAfterStreamEnd()
	mustEq(t, builds, 0)
	setLive(false)
	matchPageAfterStreamEnd()
	mustEq(t, builds, 1)
}

func TestPanelHasTheMatchPageCard(t *testing.T) {
	html := controlSource(t)
	contains(t, html, "buildMatchPage()")
	if strings.Count(html, "loadMatchPage();") < 2 { // both login paths
		t.Fatal("loadMatchPage not called on both login paths")
	}
}

// ── Social clips ──

type clipsFixture struct {
	dir      string
	rendered [][2]string
}

func newClipsFixture(t *testing.T) *clipsFixture {
	t.Helper()
	sandbox(t, "")
	setState(t, M{"home_club_id": ""})
	f := &clipsFixture{dir: t.TempDir()}
	for i, name := range []string{"Replay 1.mp4", "Replay 2.mp4", "Replay 3.mp4"} {
		makeClip(t, f.dir, name, float64(time.Now().Unix()-100+int64(i)))
	}
	tags := map[string]ClipTag{"Replay 1.mp4": {"Six", "SIX · North 31*"}, "Replay 2.mp4": {"Test", "TEST"},
		"Replay 3.mp4": {"Wicket", "WICKET · Smith"}}
	sTags, sGuess, sFF, sAI, sRender := clipTags, guessClipTags, ffmpegFound, aiClipTexts, renderClip
	t.Cleanup(func() {
		clipTags, guessClipTags, ffmpegFound, aiClipTexts, renderClip = sTags, sGuess, sFF, sAI, sRender
	})
	clipTags = func([]string) map[string]ClipTag { return tags }
	guessClipTags = func([]string, float64) map[string]string { return map[string]string{} }
	ffmpegFound = func() bool { return true }
	aiClipTexts = func(string, string, M) (string, string) { return "a line", "a post #c" }
	renderClip = func(src, out, headline, line, work, badge, handle, accent string) (bool, string) {
		f.rendered = append(f.rendered, [2]string{filepath.Base(src), headline})
		os.WriteFile(out, nil, 0o644)
		return true, out
	}
	return f
}

func TestSocialClipsOnePerTaggedMomentTestsSkipped(t *testing.T) {
	f := newClipsFixture(t)
	ok, msg, made := MakeSocialClips(f.dir)
	if !ok {
		t.Fatal(msg)
	}
	eqv(t, f.rendered, [][2]string{{"Replay 1.mp4", "SIX!"}, {"Replay 3.mp4", "WICKET!"}})
	eqv(t, made, []string{"01_six.mp4", "02_wicket.mp4"})
	b, _ := os.ReadFile(filepath.Join(socialClipsDir(f.dir), "01_six.txt"))
	mustEq(t, strings.TrimSpace(string(b)), "a post #c")
}

func TestSocialClipsRerunKeepsExistingClips(t *testing.T) {
	f := newClipsFixture(t)
	MakeSocialClips(f.dir)
	f.rendered = nil
	_, _, made := MakeSocialClips(f.dir)
	mustEq(t, len(f.rendered), 0) // nothing re-rendered
	mustEq(t, len(made), 2)
}

func TestSocialClipsNoFFmpegExplained(t *testing.T) {
	f := newClipsFixture(t)
	ffmpegFound = func() bool { return false }
	ok, msg, _ := MakeSocialClips(f.dir)
	if ok || !strings.Contains(msg, "FFmpeg") {
		t.Fatal(ok, msg)
	}
}

func TestClipTextsNoKeyFallsBackToTheTag(t *testing.T) {
	line, post := aiClipTexts("SIX · North 31*", "Six", M{})
	mustEq(t, line, "SIX · North 31*")
	contains(t, post, "#cricket")
}

func TestClipTextsPromptNeverAssumesASide(t *testing.T) {
	saved := ai.Ask
	t.Cleanup(func() { ai.Ask = saved })
	var prompt string
	ai.Ask = func(_ context.Context, r ai.Request) (string, error) {
		prompt = r.Prompt
		return `{"line": "North hits a six", "post": "North! #cricket"}`, nil
	}
	line, post := aiClipTexts("SIX · North 31*", "Six", M{"anthropic_api_key": "k"})
	eqv(t, []any{line, post}, []any{"North hits a six", "North! #cricket"})
	contains(t, prompt, `never write "we"`)
}

func TestClipTextsBadJSONFromModelFallsBack(t *testing.T) {
	saved := ai.Ask
	t.Cleanup(func() { ai.Ask = saved })
	ai.Ask = func(context.Context, ai.Request) (string, error) { return "nope", nil }
	line, _ := aiClipTexts("SIX · North 31*", "Six", M{"anthropic_api_key": "k"})
	mustEq(t, line, "SIX · North 31*")
}

func TestSocialClipsListDownloadAndTraversal(t *testing.T) {
	s := newTestServer(t, "")
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "01_six.mp4"), []byte("\x00\x00video"), 0o644)
	os.WriteFile(filepath.Join(out, "01_six.txt"), []byte("caption here\n"), 0o644)
	saved := socialClipsDir
	socialClipsDir = func(string) string { return out }
	t.Cleanup(func() { socialClipsDir = saved })
	_, d := s.getJSON("/social/clips")
	eqv(t, d["clips"], []any{map[string]any{"name": "01_six.mp4", "caption": "caption here"}})
	r := s.get("/social/clips/file/01_six.mp4")
	eqv(t, []any{r.status, string(r.body)}, []any{200, "\x00\x00video"})
	for _, bad := range []string{"/social/clips/file/..%2F..%2Fconfig.ini", "/social/clips/file/01_six.txt"} {
		mustEq(t, s.get(bad).status, 404, bad)
	}
}

// ── Spoken commentary ──

func voiceSandbox(t *testing.T) {
	t.Helper()
	sandbox(t, "")
	os.MkdirAll(VoiceDir, 0o755)
	savedSynth, savedKeep := synthesize, voiceKeep
	t.Cleanup(func() { synthesize, voiceKeep = savedSynth, savedKeep })
	synthesize = func(text, out string) (bool, string) {
		os.WriteFile(out, []byte("RIFF"), 0o644)
		return true, out
	}
	saved := getOverCommentary()
	t.Cleanup(func() { setOverCommentary(saved) })
}

func TestVoiceAttachedToMatchingOver(t *testing.T) {
	voiceSandbox(t)
	setOverCommentary(M{"text": "x", "over": 7})
	SpeakOverCommentary("x", 7)
	if !strings.HasPrefix(gs(getOverCommentary(), "voice", ""), "/voice/over_7.wav") {
		t.Fatal(getOverCommentary())
	}
}

func TestVoiceLateNeverLandsOnNewerOver(t *testing.T) {
	voiceSandbox(t)
	setOverCommentary(M{"text": "y", "over": 8}) // over 8 already replaced it
	SpeakOverCommentary("x", 7)
	if _, ok := getOverCommentary()["voice"]; ok {
		t.Fatal("voice attached to a newer over")
	}
}

func TestVoiceOldRecordingsPruned(t *testing.T) {
	voiceSandbox(t)
	voiceKeep = 3
	for n := 0; n < 6; n++ {
		SpeakOverCommentary("x", n)
		mt := time.Unix(int64(n+1), 0)
		os.Chtimes(filepath.Join(VoiceDir, fmt.Sprintf("over_%d.wav", n)), mt, mt)
	}
	SpeakOverCommentary("x", 99)
	entries, _ := os.ReadDir(VoiceDir)
	mustEq(t, len(entries), 3)
}

func TestVoiceServesWavRefusesOthers(t *testing.T) {
	s := newTestServer(t, "")
	os.MkdirAll(VoiceDir, 0o755)
	os.WriteFile(filepath.Join(VoiceDir, "over_3.wav"), []byte("RIFFdata"), 0o644)
	r := s.get("/voice/over_3.wav?t=1")
	eqv(t, []any{r.status, string(r.body)}, []any{200, "RIFFdata"})
	mustEq(t, s.get("/voice/..%2Fconfig.ini").status, 404)
}

func TestVoiceOverlayPlaysOncePerOverOnlyWhenOn(t *testing.T) {
	contains(t, overlaySource(t), "speakCommentary(d);", "!cfg.graphics_voice_commentary", "d.over === _lastSpokenOver")
}

func TestVoiceOBSRoutesOverlayAudioIntoStream(t *testing.T) {
	// One settings map, used for both creating the Overlay and updating an existing one.
	src := obsSetupSource(t)
	contains(t, src, `"reroute_audio": true`,
		`"inputName": "Overlay", "inputSettings": overlay}`,
		`"inputKind": "browser_source", "inputSettings": overlay}`)
}

func TestVoiceOffByDefault(t *testing.T) {
	mustEq(t, DefaultState()["graphics_voice_commentary"], any(false))
}
