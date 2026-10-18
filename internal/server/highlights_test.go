package server

// Auto-tagged highlights (tests/test_highlights.py): captioning, clip tagging,
// mtime-fallback correlation, reel planning and the chapters file — everything short of
// running ffmpeg itself.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var matchState = M{
	"battingTeamName": "Home CC", "score": 84, "wickets": 3, "overs": 18.2,
	"batter1":          M{"name": "SMITH", "runs": 34, "onStrike": true},
	"batter2":          M{"name": "JONES", "runs": 12, "onStrike": false},
	"lastWicketBatter": "WALKER 22", "lastWicketHowOut": "C JONES B HARRISON",
}

func contains(t *testing.T, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Fatalf("%q missing from %q", w, s)
		}
	}
}

func TestClipCaptionWicketUsesDismissalDetail(t *testing.T) {
	contains(t, makeClipCaption("Wicket", matchState), "WICKET", "WALKER 22", "C JONES B HARRISON", "84-3 (18.2 ov)")
}

func TestClipCaptionBoundaryAndSixUseStriker(t *testing.T) {
	contains(t, makeClipCaption("Boundary", matchState), "FOUR", "SMITH 34*")
	contains(t, makeClipCaption("Six", matchState), "SIX")
}

func TestClipCaptionMilestoneReasons(t *testing.T) {
	contains(t, makeClipCaption("Century - SMITH", matchState), "CENTURY", "SMITH")
	contains(t, makeClipCaption("Fifty - JONES", matchState), "FIFTY")
}

func TestClipCaptionTestReasonNeverTagged(t *testing.T) {
	mustEq(t, makeClipCaption("Test", matchState), "")
	mustEq(t, makeClipCaption("", matchState), "")
}

func TestClipCaptionSurvivesMissingState(t *testing.T) {
	contains(t, makeClipCaption("Boundary", nil), "FOUR")
	contains(t, makeClipCaption("Wicket", M{}), "WICKET")
}

func TestReplayCaptionedFromAgentsFrameInTwoLaptopMode(t *testing.T) {
	sandbox(t, "")
	agentMu.Lock()
	agentLastState = matchState
	agentMu.Unlock()
	if mapIdentity(replayFeed(M{"pcs_source": "agent"})) != mapIdentity(matchState) {
		t.Fatal("not captioned from the agent's frame")
	}
}

func clipsDB(t *testing.T) { sandbox(t, "") }

func TestClipTagLogAndReadBack(t *testing.T) {
	clipsDB(t)
	setState(t, M{"pc_match_id": "m1"})
	logReplayClip("/replays/Replay_001.mkv", "Wicket", "WICKET · X")
	logReplayClip("/replays/Replay_002.mkv", "Boundary", "FOUR · Y")
	tags := clipTags([]string{"/replays/Replay_001.mkv", "/replays/Replay_002.mkv", "/replays/untagged.mkv"})
	mustEq(t, tags["Replay_001.mkv"].Reason, "Wicket")
	mustEq(t, tags["Replay_002.mkv"].Caption, "FOUR · Y")
	if _, ok := tags["untagged.mkv"]; ok {
		t.Fatal("untagged clip tagged")
	}
}

func TestClipRetagReplaces(t *testing.T) {
	clipsDB(t)
	setState(t, M{"pc_match_id": "m1"})
	logReplayClip("/r/a.mkv", "Boundary", "FOUR")
	logReplayClip("/r/a.mkv", "Six", "SIX")
	mustEq(t, clipTags([]string{"/r/a.mkv"})["a.mkv"].Reason, "Six")
}

func TestClipEmptyCaptionNotLogged(t *testing.T) {
	clipsDB(t)
	logReplayClip("/r/test.mkv", "Test", "")
	mustEq(t, len(clipTags([]string{"/r/test.mkv"})), 0)
}

func TestClipTaggingNeverRaisesWithBrokenDB(t *testing.T) {
	clipsDB(t)
	closeDB()
	DBFile = filepath.Join(t.TempDir(), "no", "such", "dir", "nope.db")
	logReplayClip("/r/a.mkv", "Wicket", "WICKET") // must not panic
	mustEq(t, len(clipTags([]string{"/r/a.mkv"})), 0)
	mustEq(t, len(guessClipTags([]string{"/r/a.mkv"}, 90)), 0)
}

func seedBall(t *testing.T, at float64, outcome string, wicket int, batter string) {
	t.Helper()
	runs := 0
	if outcome == "4" {
		runs = 4
	}
	ts := time.Unix(int64(at), 0).Format("2006-01-02T15:04:05")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT OR REPLACE INTO balls VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			"m1", 1, 17, int(at)%6+1, "Home CC", batter, "JONES", "HARRISON", outcome, runs, nil, wicket, 1, 80, 2, ts)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func makeClip(t *testing.T, dir, name string, at float64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte("clip"), 0o644)
	mt := time.Unix(int64(at), 0)
	os.Chtimes(p, mt, mt)
	return p
}

func TestGuessCorrelatesByMtimePreferringWickets(t *testing.T) {
	clipsDB(t)
	base := float64(time.Now().Unix() - 3600)
	seedBall(t, base+10, "4", 0, "SMITH")
	seedBall(t, base+20, "W", 1, "WALKER")
	clip := makeClip(t, t.TempDir(), "manual_save.mkv", base+30)
	tags := guessClipTags([]string{clip}, 90)
	contains(t, tags["manual_save.mkv"], "WICKET", "WALKER")
}

func TestGuessIgnoresClipsOutsideWindow(t *testing.T) {
	clipsDB(t)
	base := float64(time.Now().Unix() - 3600)
	seedBall(t, base, "4", 0, "SMITH")
	clip := makeClip(t, t.TempDir(), "way_later.mkv", base+600)
	mustEq(t, len(guessClipTags([]string{clip}, 90)), 0)
}

func TestGuessDotBallsNeverProduceTags(t *testing.T) {
	clipsDB(t)
	base := float64(time.Now().Unix() - 3600)
	seedBall(t, base, "1", 0, "SMITH") // not a wicket, not a boundary → not notable
	clip := makeClip(t, t.TempDir(), "nothing.mkv", base+5)
	mustEq(t, len(guessClipTags([]string{clip}, 90)), 0)
}

func TestPlanChronologicalOrderAndTestExclusion(t *testing.T) {
	dir := t.TempDir()
	base := float64(time.Now().Unix() - 1000)
	late := makeClip(t, dir, "late.mkv", base+100)
	test := makeClip(t, dir, "test.mkv", base+50)
	early := makeClip(t, dir, "early.mkv", base)
	tags := map[string]ClipTag{"late.mkv": {"Six", "SIX"}, "test.mkv": {"Test", ""}, "early.mkv": {"Wicket", "WICKET"}}
	plan := planHighlights([]string{late, test, early}, tags)
	var names []string
	for _, e := range plan {
		names = append(names, filepath.Base(e.File))
	}
	eqv(t, names, []string{"early.mkv", "late.mkv"})
	mustEq(t, plan[0].Caption, "WICKET")
}

func TestPlanUntaggedClipsKeptWithoutCaption(t *testing.T) {
	plan := planHighlights([]string{makeClip(t, t.TempDir(), "mystery.mkv", float64(time.Now().Unix()))}, nil)
	mustEq(t, len(plan), 1)
	mustEq(t, plan[0].Caption, "")
}

func TestChaptersTextAccumulatesTimestamps(t *testing.T) {
	text := chaptersText([]string{"WICKET · WALKER", "SIX · SMITH", ""}, []float64{18.0, 18.4, 20.0}, "Home v Rival — highlights")
	lines := strings.Split(strings.TrimSpace(text), "\n")
	mustEq(t, lines[0], "Home v Rival — highlights")
	mustEq(t, lines[2], "00:00 WICKET · WALKER")
	mustEq(t, lines[3], "00:18 SIX · SMITH")
	mustEq(t, lines[4], "00:36 Replay") // untagged clips still listed
}

func TestFFFilterPathEscapesWindowsDriveColons(t *testing.T) {
	mustEq(t, ffFilterPath(`C:\Users\x\f.ttf`), `C\:/Users/x/f.ttf`)
	mustEq(t, ffFilterPath("/usr/share/f.ttf"), "/usr/share/f.ttf")
}
