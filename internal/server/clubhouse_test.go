package server

// The clubhouse screen's extra routes: the on-air preview, the live video id, and the
// checklist's stop_stream. OBS and YouTube are faked.

import (
	"encoding/base64"
	"testing"
	"time"

	"cricketstream/internal/obsws"
)

func fakePreviewOBS(t *testing.T, scene string, jpeg []byte) *int {
	t.Helper()
	shots := 0
	saved := obsCall
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		switch reqs[0].Type {
		case "GetCurrentProgramScene":
			if scene == "" {
				return nil
			}
			return []map[string]any{{"currentProgramSceneName": scene}}
		case "GetSourceScreenshot":
			shots++
			if reqs[0].Data.(M)["sourceName"] != scene {
				t.Errorf("screenshot of %v, want the program scene %q", reqs[0].Data.(M)["sourceName"], scene)
			}
			return []map[string]any{{"imageData": "data:image/jpg;base64," + base64.StdEncoding.EncodeToString(jpeg)}}
		}
		return nil
	}
	previewCache.Lock()
	previewCache.jpeg, previewCache.at = nil, time.Time{}
	previewCache.Unlock()
	t.Cleanup(func() {
		obsCall = saved
		previewCache.Lock()
		previewCache.jpeg, previewCache.at = nil, time.Time{}
		previewCache.Unlock()
	})
	return &shots
}

func TestPreviewNeedsLoginAndIsCached(t *testing.T) {
	s := newTestServer(t, "secret")
	shots := fakePreviewOBS(t, "Main", []byte("\xff\xd8JPEGDATA"))
	if r := s.get("/preview.jpg"); r.status != 401 {
		t.Fatalf("no login: %d, want 401", r.status)
	}
	auth := s.bearer("secret")
	r := s.get("/preview.jpg", auth)
	if r.status != 200 || r.header.Get("Content-Type") != "image/jpeg" || string(r.body) != "\xff\xd8JPEGDATA" {
		t.Fatalf("preview: %d %q %q", r.status, r.header.Get("Content-Type"), r.body)
	}
	s.get("/preview.jpg", auth)
	s.get("/preview.jpg", auth)
	if *shots != 1 {
		t.Fatalf("three asks inside the cache window took %d screenshots, want 1", *shots)
	}
}

func TestPreviewWithoutOBS(t *testing.T) {
	s := newTestServer(t, "")
	fakePreviewOBS(t, "", nil)
	if r := s.get("/preview.jpg"); r.status != 503 {
		t.Fatalf("OBS unreachable: %d, want 503", r.status)
	}
}

func TestLiveVideoNeedsLoginAndIsCached(t *testing.T) {
	s := newTestServer(t, "secret")
	calls := 0
	saved := liveVideoLookup
	liveVideoLookup = func() M { calls++; return M{"ok": true, "video_id": "abc123XYZ_-", "status": "active"} }
	liveVideoCache.Lock()
	liveVideoCache.out = nil
	liveVideoCache.Unlock()
	t.Cleanup(func() {
		liveVideoLookup = saved
		liveVideoCache.Lock()
		liveVideoCache.out = nil
		liveVideoCache.Unlock()
	})
	if code, _ := s.getJSON("/youtube/live"); code != 401 {
		t.Fatalf("no login: %d", code)
	}
	auth := s.bearer("secret")
	code, d := s.getJSON("/youtube/live", auth)
	if code != 200 || d["video_id"] != "abc123XYZ_-" {
		t.Fatalf("%d %v", code, d)
	}
	s.getJSON("/youtube/live", auth)
	if calls != 1 {
		t.Fatalf("YouTube asked %d times, want 1 (cached)", calls)
	}
	s.getJSON("/youtube/live?force=1", auth)
	if calls != 2 {
		t.Fatal("force=1 should ask again")
	}
}

func TestChecklistStopStream(t *testing.T) {
	f := newChecklistFake(t)
	f.up(true, true, nil)
	f.obs["StopStream"] = M{}
	status, out := ChecklistAction("stop_stream")
	if status != 200 || out["ok"] != true || !f.sentAny("StopStream") {
		t.Fatalf("live: %d %v sent=%v", status, out, f.sent)
	}
	// Not live: nothing to stop, and nothing sent.
	f.sent = nil
	f.up(true, false, nil)
	if _, out := ChecklistAction("stop_stream"); out["ok"] != true || f.sentAny("StopStream") {
		t.Fatalf("not live: %v sent=%v", out, f.sent)
	}
	// OBS refuses.
	f.up(true, true, nil)
	if _, out := ChecklistAction("stop_stream"); out["ok"] != false {
		t.Fatalf("refused: %v", out)
	}
	// OBS refuses to say whether it's live: never read as "not live" (the screen would
	// carry on and end match day with the stream still up).
	f.obs = map[string]M{}
	f.sent = nil
	if _, out := ChecklistAction("stop_stream"); out["ok"] != false || f.sentAny("StopStream") {
		t.Fatalf("status refused: %v sent=%v", out, f.sent)
	}
	f.obs = nil
	if _, out := ChecklistAction("stop_stream"); out["ok"] != false || out["error"] != "Couldn't reach OBS." {
		t.Fatalf("unreachable: %v", out)
	}
}
