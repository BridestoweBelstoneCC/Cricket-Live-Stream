package server

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/pyval"
)

func breakDBExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := withDB(func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

// clipsOnDisk saves replay clips (files and their DB tags), oldest first.
func clipsOnDisk(t *testing.T, folder string, reasons ...string) {
	t.Helper()
	os.MkdirAll(folder, 0o755)
	t0 := time.Date(2026, 6, 6, 13, 0, 0, 0, time.Local)
	for i, r := range reasons {
		f := fmt.Sprintf("clip%02d.mp4", i)
		os.WriteFile(filepath.Join(folder, f), []byte("x"), 0o644)
		breakDBExec(t, "INSERT INTO clips(match_id,file,ts,reason,caption) VALUES(?,?,?,?,?)",
			CurrentMatchID(), f, t0.Add(time.Duration(i)*time.Minute).Format("2006-01-02T15:04:05"), r, strings.ToUpper(r)+fmt.Sprint(i))
	}
}

func TestBreakClipsPicksTheBestInMatchOrder(t *testing.T) {
	sandbox(t, "")
	folder := filepath.Join(t.TempDir(), "replays")
	clipsOnDisk(t, folder, "Boundary", "Six", "Wicket", "Boundary", "Boundary", "Fifty - Smith", "Boundary",
		"Six", "Boundary", "Wicket", "Boundary", "Boundary")
	os.Remove(filepath.Join(folder, "clip09.mp4")) // a clip deleted since: left out
	got := breakClips(folder)
	var caps []string
	for _, c := range got {
		caps = append(caps, c.caption)
	}
	// 8 of 11 on disk: the wicket, the fifty, both sixes, then the newest fours — in the
	// order they happened.
	want := "SIX1 WICKET2 FIFTY - SMITH5 BOUNDARY6 SIX7 BOUNDARY8 BOUNDARY10 BOUNDARY11"
	if strings.Join(caps, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(caps, " "), want)
	}
}

type obsCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *obsCallLog) add(s string) { l.mu.Lock(); l.calls = append(l.calls, s); l.mu.Unlock() }
func (l *obsCallLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// fakeBreakOBS records the reel's OBS calls; waits take no time, but each counts, and the
// break can be ended after a number of them.
func fakeBreakOBS(t *testing.T, endAfterWaits int, obsUp bool) *obsCallLog {
	t.Helper()
	log := &obsCallLog{}
	savedObs, savedWait, savedLen := breakObs, breakWait, breakClipLength
	t.Cleanup(func() {
		EndBreak()
		waitBackground(t)
		breakObs, breakWait, breakClipLength = savedObs, savedWait, savedLen
	})
	breakObs = func(_ M, clip, scene string) bool {
		log.add(scene + ":" + filepath.Base(clip))
		return obsUp
	}
	breakClipLength = func(M, string) time.Duration { return 7 * time.Second }
	var mu sync.Mutex
	waits := 0
	breakWait = func(stop chan struct{}, d time.Duration) bool {
		mu.Lock()
		waits++
		n := waits
		mu.Unlock()
		log.add(fmt.Sprintf("wait %s", d))
		if endAfterWaits > 0 && n == endAfterWaits {
			go EndBreak()
			<-stop
			return false
		}
		select {
		case <-stop:
			return false
		default:
			return true
		}
	}
	return log
}

func TestBreakReelPlaysClipsBetweenTheGraphics(t *testing.T) {
	sandbox(t, "")
	folder := filepath.Join(t.TempDir(), "replays")
	UpdateState(func(s M) { s["replay_enabled"], s["replay_folder"] = true, folder })
	clipsOnDisk(t, folder, "Wicket", "Six")
	log := fakeBreakOBS(t, 6, true) // lead, clip, between, clip, between, then play restarts mid-clip
	StartBreak("drinks")
	waitBackground(t)
	got := strings.Join(log.get(), " | ")
	want := "wait 20s | Replay:clip00.mp4 | wait 7s | Main:. | wait 25s | Replay:clip01.mp4 | wait 7s | Main:. | wait 25s | " +
		"Replay:clip00.mp4 | wait 7s | Main:."
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if BreakStatus()["active"] != false {
		t.Fatal("still on after play restarted")
	}
	if !pyval.Truthy(popCommands()["break_clip"]) {
		t.Error("the overlay wasn't given the clip's caption")
	}
}

func TestBreakReelNeedsReplaysAndOBS(t *testing.T) {
	sandbox(t, "")
	folder := filepath.Join(t.TempDir(), "replays")
	clipsOnDisk(t, folder, "Wicket")
	UpdateState(func(s M) { s["replay_enabled"], s["replay_folder"] = false, folder })
	log := fakeBreakOBS(t, 0, true)
	StartBreak("rain")
	waitBackground(t)
	if len(log.get()) != 0 {
		t.Fatalf("replays off: %v", log.get())
	}
	if BreakStatus()["active"] != true || BreakStatus()["label"] != "Rain delay" {
		t.Fatal("the break itself (the graphics) carries on without the reel")
	}
	EndBreak()

	UpdateState(func(s M) { s["replay_enabled"] = true })
	log = fakeBreakOBS(t, 0, false) // OBS not answering: one try, then graphics only
	StartBreak("drinks")
	waitBackground(t)
	if got := strings.Join(log.get(), " | "); got != "wait 20s | Replay:clip00.mp4" {
		t.Fatalf("OBS down: %s", got)
	}
}

func TestBreakRoutes(t *testing.T) {
	sandbox(t, "pw")
	ts := serve(t)
	fakeBreakOBS(t, 0, true)
	// The overlay (OBS's browser source, on this laptop, no login) starts drinks...
	code, out := ts.postJSON("/break/start", M{"kind": "drinks"})
	if code != 200 || out["active"] != true || out["kind"] != "drinks" {
		t.Fatalf("from the overlay: %d %v", code, out)
	}
	if popCommands()["break"] != "drinks" {
		t.Error("the overlay must hear of it too")
	}
	// ...a second start renames it, and an unknown kind is a plain break.
	ts.postJSON("/break/start", M{"kind": "rain"})
	if BreakStatus()["kind"] != "rain" {
		t.Fatalf("%v", BreakStatus())
	}
	if code, _ := ts.getJSON("/break"); code != 401 && code != 403 {
		t.Fatalf("the panel's status needs the login: %d", code)
	}
	if code, out := ts.getJSON("/break", ts.bearer("pw")); code != 200 || out["label"] != "Rain delay" {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, _ := ts.postJSON("/break/end", M{}); code != 200 || BreakStatus()["active"] != false {
		t.Fatal("end")
	}
	if !pyval.Truthy(popCommands()["break_end"]) {
		t.Error("the overlay wasn't told the break is over")
	}
	ts.postJSON("/break/start", M{"kind": "nonsense"})
	if BreakStatus()["kind"] != "break" {
		t.Fatalf("unknown kind: %v", BreakStatus())
	}
}
