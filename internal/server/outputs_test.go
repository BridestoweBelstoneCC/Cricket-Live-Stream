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
