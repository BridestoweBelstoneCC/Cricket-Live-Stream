package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A match streamed to the fake channel: the broadcast MATCH covers its moments, and the
// video is public.
func ytMatch(t *testing.T) (*fakeYouTube, string) {
	t.Helper()
	start := time.Now().Add(-5 * time.Hour).UTC().Truncate(time.Second)
	f := newFakeYouTube(t, &fakeYouTube{mineSet: true,
		mine: []any{M{"id": "MATCH", "snippet": M{"actualStartTime": start.Format(time.RFC3339),
			"actualEndTime": start.Add(4 * time.Hour).Format(time.RFC3339)}}},
		video: []any{M{"snippet": M{"title": "Home CC v Away CC", "categoryId": "17"}, "status": M{"privacyStatus": "public"}}},
	})
	mid := CurrentMatchID()
	withDBExec(t, "INSERT INTO moments(match_id,key,ts,kind,text) VALUES(?,?,?,?,?)",
		mid, "inn1", ts(start.Add(10*time.Minute)), "innings", "Home CC batting")
	saved := GenerateSocialGraphicFacts
	GenerateSocialGraphicFacts = func() M { return M{"ok": false} }
	t.Cleanup(func() { GenerateSocialGraphicFacts = saved })
	return f, mid
}

func (f *fakeYouTube) callsTo(method, kind string) []ytCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ytCall
	for _, c := range f.calls {
		if c.method == method && c.kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func TestSetMatchThumbnail(t *testing.T) {
	sandbox(t, "")
	f, mid := ytMatch(t)
	ok, msg := SetMatchThumbnail(mid)
	if !ok {
		t.Fatal(msg)
	}
	sets := f.callsTo("POST", "thumbnails/set")
	if len(sets) != 1 || !strings.Contains(sets[0].query, "videoId=MATCH") {
		t.Fatalf("thumbnail calls: %+v", sets)
	}
	if raw := sets[0].raw; len(raw) < 1000 || raw[0] != 0xFF || raw[1] != 0xD8 {
		t.Fatalf("not a JPEG (%d bytes)", len(raw))
	}
}

// An unverified channel can't have custom thumbnails: say how to fix it.
func TestThumbnailRefusalSaysVerify(t *testing.T) {
	sandbox(t, "")
	f, mid := ytMatch(t)
	f.fail = map[[2]string]bool{{"thumbnails/set", ""}: true}
	if ok, msg := SetMatchThumbnail(mid); ok || !strings.Contains(msg, "youtube.com/verify") {
		t.Fatalf("%v %q", ok, msg)
	}
}

// replays saves n replay files for this match, tagged as the replay logger does.
func replays(t *testing.T, mid string, reasons ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "replays")
	os.MkdirAll(dir, 0o755)
	UpdateState(func(st M) { st["replay_folder"] = dir })
	for i, r := range reasons {
		name := "Replay " + string(rune('A'+i)) + ".mp4"
		os.WriteFile(filepath.Join(dir, name), []byte("clip"), 0o644)
		withDBExec(t, "INSERT INTO clips(match_id,file,ts,reason,caption) VALUES(?,?,?,?,?)",
			mid, name, time.Now().Add(time.Duration(i)*time.Minute).Format("2006-01-02T15:04:05"), r, "Caption "+r)
	}
	return dir
}

func stubCompile(t *testing.T) *[]string {
	t.Helper()
	var got []string
	saved := compileMatchHighlights
	compileMatchHighlights = func(clips []string, output string) (bool, string) {
		for _, c := range clips {
			got = append(got, filepath.Base(c))
		}
		os.WriteFile(output, []byte("video bytes"), 0o644)
		os.WriteFile(strings.TrimSuffix(output, ".mp4")+"_description.txt", []byte("Highlights\n\n00:00 SIX\n00:12 WICKET <b>\n"), 0o644)
		return true, "built"
	}
	t.Cleanup(func() { compileMatchHighlights = saved })
	return &got
}

func TestUploadHighlights(t *testing.T) {
	sandbox(t, "")
	f, mid := ytMatch(t)
	dir := replays(t, mid, "six", "test", "wicket", "fifty")
	built := stubCompile(t)
	ok, msg := UploadHighlights(mid, false)
	if !ok {
		t.Fatal(msg)
	}
	// This match's clips, built into a video outside the replay folder (where it would
	// count as a replay).
	eqv(t, *built, []string{"Replay A.mp4", "Replay B.mp4", "Replay C.mp4", "Replay D.mp4"})
	if len(listClips(dir)) != 4 {
		t.Fatalf("the video landed among the replays: %v", listClips(dir))
	}
	starts := f.callsTo("POST", "videos")
	if len(starts) != 1 {
		t.Fatalf("upload starts: %+v", starts)
	}
	sn, stt := starts[0].body["snippet"].(M), starts[0].body["status"].(M)
	desc := gs(sn, "description", "")
	if !strings.Contains(gs(sn, "title", ""), "highlights") || stt["privacyStatus"] != "public" ||
		!strings.Contains(desc, "00:12 WICKET b") || !strings.Contains(desc, "The whole match: https://youtu.be/MATCH") {
		t.Fatalf("metadata: %+v %+v", sn, stt)
	}
	if puts := f.callsTo("PUT", "session"); len(puts) != 1 || string(puts[0].raw) != "video bytes" {
		t.Fatalf("upload: %+v", puts)
	}
	if sets := f.callsTo("POST", "thumbnails/set"); len(sets) != 1 || !strings.Contains(sets[0].query, "videoId=HL") {
		t.Fatalf("highlights thumbnail: %+v", sets)
	}
	if s := YTHighlightsStatus(); s["url"] != "https://youtu.be/HL" {
		t.Fatalf("status: %v", s)
	}
	// Once per match — unless asked again.
	if ok, msg := UploadHighlights(mid, false); !ok || !strings.Contains(msg, "already") || len(f.callsTo("POST", "videos")) != 1 {
		t.Fatalf("second time: %v %q", ok, msg)
	}
	if ok, _ := UploadHighlights(mid, true); !ok || len(f.callsTo("POST", "videos")) != 2 {
		t.Fatal("forced upload didn't upload")
	}
}

func TestHighlightsNeedThreeReplays(t *testing.T) {
	sandbox(t, "")
	f, mid := ytMatch(t)
	replays(t, mid, "six", "test", "four") // the test replay doesn't count
	stubCompile(t)
	if ok, msg := UploadHighlights(mid, false); ok || !strings.Contains(msg, "Only 2 replays") {
		t.Fatalf("%v %q", ok, msg)
	}
	if len(f.callsTo("POST", "videos")) != 0 {
		t.Fatal("uploaded anyway")
	}
}

// After the stream: chapters and thumbnail always; the highlights only once the match
// has a result (a stream that dropped mid-match mustn't upload half the highlights).
func TestAfterStreamHighlightsWaitForTheResult(t *testing.T) {
	sandbox(t, "")
	f, mid := ytMatch(t)
	replays(t, mid, "six", "wicket", "four")
	stubCompile(t)
	saved := matchFinished
	t.Cleanup(func() { matchFinished = saved })
	// The upload runs in the background: wait it out (slow under the race detector)
	// before anything it uses is put back. Registered last, so it runs first.
	idle := func() {
		for i := 0; i < 1200 && gb(YTHighlightsStatus(), "running", false); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	t.Cleanup(idle)

	matchFinished = func() bool { return false }
	afterStreamYouTube()
	if len(f.callsTo("POST", "thumbnails/set")) != 1 || len(f.callsTo("POST", "videos")) != 0 {
		t.Fatalf("mid-match: %+v", f.calls)
	}
	matchFinished = func() bool { return true }
	afterStreamYouTube()
	idle()
	if len(f.callsTo("POST", "videos")) != 1 {
		t.Fatalf("after the result: %v", YTHighlightsStatus())
	}
	UpdateState(func(st M) { st["youtube_highlights"], st["youtube_thumbnail"] = false, false })
	afterStreamYouTube()
	if len(f.callsTo("POST", "videos")) != 1 || len(f.callsTo("POST", "thumbnails/set")) != 3 {
		t.Fatalf("switched off: %+v", f.calls)
	}
}
