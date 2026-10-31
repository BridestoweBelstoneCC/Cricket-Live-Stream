package server

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func withDBExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := withDB(func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func momentFrame(innings, score, wkts int, overs float64, b1 string, r1 int, b2 string, r2 int, bowler string, bw int) M {
	return M{"innings": innings, "score": score, "wickets": wkts, "overs": overs, "battingTeamName": "Home CC",
		"batter1": M{"name": b1, "runs": r1}, "batter2": M{"name": b2, "runs": r2},
		"bowler": M{"name": bowler, "wickets": bw}}
}

// The moments a match produces, in order, each once — a restart sees them again and
// adds nothing.
func TestLogMoments(t *testing.T) {
	sandbox(t, "")
	momentsPrev = momentsPrevT{}
	t0 := time.Date(2026, 6, 6, 13, 0, 0, 0, time.UTC)
	clockAt := t0
	saved := momentNow
	momentNow = func() time.Time { return clockAt }
	t.Cleanup(func() { momentNow = saved; momentsPrev = momentsPrevT{} })
	step := func(d time.Duration, f M) { clockAt = clockAt.Add(d); LogMoments(f) }

	step(0, momentFrame(1, 0, 0, 0, "Smith", 0, "Jones", 0, "Patel", 0)) // before the first ball
	step(time.Minute, momentFrame(1, 1, 0, 0.1, "Smith", 1, "Jones", 0, "Patel", 0))
	step(10*time.Minute, momentFrame(1, 40, 0, 6, "Smith", 31, "Jones", 9, "Patel", 0))
	step(time.Minute, momentFrame(1, 41, 1, 6.2, "Smith", 32, "", 0, "Khan", 1)) // Jones out
	step(time.Minute, momentFrame(1, 70, 1, 10, "Smith", 52, "Brown", 8, "Khan", 1))
	step(time.Minute, momentFrame(1, 70, 1, 10, "Smith", 53, "Brown", 8, "Khan", 1)) // still 50+: once
	step(time.Minute, momentFrame(1, 90, 5, 15, "Smith", 60, "Gray", 0, "Khan", 5))
	step(time.Minute, momentFrame(2, 3, 0, 0.3, "Ali", 3, "Cole", 0, "Smith", 0))

	got := MatchMoments(CurrentMatchID())
	var texts []string
	for _, m := range got {
		texts = append(texts, m.Text)
	}
	want := []string{"Home CC batting", "Wicket: Jones 9, 41/1 (6.2 ov)", "Smith 50",
		"Wicket: Brown 8, 90/2 (15 ov)", "Wicket: 90/3 (15 ov)", "Wicket: 90/4 (15 ov)", "Wicket: 90/5 (15 ov)",
		"Khan takes 5 wickets", "Home CC batting"}
	if strings.Join(texts, " | ") != strings.Join(want, " | ") {
		t.Fatalf("moments:\n got %q\nwant %q", texts, want)
	}
	if got[0].TS != float64(t0.Add(time.Minute).Unix()) {
		t.Errorf("the innings starts at its first ball: %v", got[0].TS)
	}
	// A restart: the same frames again add nothing.
	momentsPrev = momentsPrevT{}
	step(time.Minute, momentFrame(2, 3, 0, 0.3, "Ali", 3, "Cole", 0, "Smith", 0))
	if n := len(MatchMoments(CurrentMatchID())); n != len(want) {
		t.Fatalf("after a restart: %d moments, want %d", n, len(want))
	}
}

func TestLogMomentsNeverRaises(t *testing.T) {
	sandbox(t, "")
	momentsPrev = momentsPrevT{}
	t.Cleanup(func() { momentsPrev = momentsPrevT{} })
	LogMoments(M{"batter1": "not a map", "overs": "x", "innings": []any{}})
}

func ts(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func TestBuildChapters(t *testing.T) {
	start := time.Date(2026, 6, 6, 12, 50, 0, 0, time.UTC)
	ms := []Moment{
		{TS: ts(start.Add(-5 * time.Minute)), Text: "Before the video"},
		{TS: ts(start.Add(10 * time.Minute)), Text: "Home CC batting"},
		{TS: ts(start.Add(45*time.Minute + 30*time.Second)), Text: "Wicket: Jones 9"},
		{TS: ts(start.Add(45*time.Minute + 33*time.Second)), Text: "Wicket: Brown 0"}, // the next ball
		{TS: ts(start.Add(2*time.Hour + 5*time.Minute)), Text: "Smith 100"},
		{TS: ts(start.Add(5 * time.Hour)), Text: "After the video"},
	}
	got := BuildChapters(ms, start, start.Add(4*time.Hour))
	want := chaptersHeader + "\n0:00 Start\n0:10 Before the video\n9:35 Home CC batting\n45:05 Wicket: Jones 9\n45:15 Wicket: Brown 0\n2:04:35 Smith 100"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if BuildChapters(ms[1:2], start, time.Time{}) != "" {
		t.Error("YouTube needs three chapters: fewer is no block")
	}
}

func TestWithChapters(t *testing.T) {
	block := chaptersHeader + "\n0:00 Start\n1:00 A\n2:00 B"
	d := withChapters("Live from the Rec.", block)
	if d != "Live from the Rec.\n\n"+block {
		t.Fatalf("%q", d)
	}
	newer := chaptersHeader + "\n0:00 Start\n1:00 A\n2:00 B\n3:00 C"
	if d2 := withChapters(d, newer); d2 != "Live from the Rec.\n\n"+newer {
		t.Fatalf("an earlier block must be replaced, not added to: %q", d2)
	}
	long := strings.Repeat("x", 4900)
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "1:00 Wicket: somebody")
	}
	d3 := withChapters(long, chaptersHeader+"\n0:00 Start\n"+strings.Join(lines, "\n"))
	if len(d3) > 5000 || !strings.HasPrefix(d3, long) || !strings.Contains(d3, "0:00 Start") {
		t.Fatalf("over YouTube's 5000: %d bytes", len(d3))
	}
}

// End to end against a fake YouTube: the right broadcast's video gets the block, with
// its title, category and the club's own description kept.
func TestAddChaptersToYouTube(t *testing.T) {
	sandbox(t, "")
	start := time.Date(2026, 6, 6, 12, 50, 0, 0, time.UTC)
	f := newFakeYouTube(t, &fakeYouTube{mineSet: true,
		mine: []any{
			M{"id": "OLD", "snippet": M{"actualStartTime": "2026-05-30T12:50:00Z", "actualEndTime": "2026-05-30T19:00:00Z"}},
			M{"id": "MATCH", "snippet": M{"actualStartTime": start.Format(time.RFC3339), "actualEndTime": start.Add(5 * time.Hour).Format(time.RFC3339)}},
			M{"id": "NEXT", "snippet": M{"scheduledStartTime": "2026-06-13T12:50:00Z"}},
		},
		video: []any{M{"snippet": M{"title": "Home CC v Away CC", "categoryId": "17", "description": "Live from the Rec.", "tags": []any{"cricket"}}}},
	})
	mid := CurrentMatchID()
	for i, txt := range []string{"Home CC batting", "Wicket: Jones 9", "Smith 50"} {
		withDBExec(t, "INSERT INTO moments(match_id,key,ts,kind,text) VALUES(?,?,?,?,?)",
			mid, txt, ts(start.Add(time.Duration(10+40*i)*time.Minute)), "wicket", txt)
	}
	ok, msg, block := AddChaptersToYouTube(mid, true)
	if !ok || !strings.Contains(block, "\n49:35 Wicket: Jones 9\n1:29:35 Smith 50") || len(f.updates("videos", "")) != 0 {
		t.Fatalf("preview: %v %q %q", ok, msg, block)
	}
	ok, msg, _ = AddChaptersToYouTube(mid, false)
	if !ok {
		t.Fatal(msg)
	}
	ups := f.updates("videos", "snippet")
	if len(ups) != 1 || ups[0].body["id"] != "MATCH" {
		t.Fatalf("updates: %+v", ups)
	}
	sn := ups[0].body["snippet"].(M)
	if sn["title"] != "Home CC v Away CC" || sn["categoryId"] != "17" || sn["tags"] == nil ||
		!strings.HasPrefix(sn["description"].(string), "Live from the Rec.\n\n"+chaptersHeader) {
		t.Fatalf("snippet: %+v", sn)
	}
}

func TestAddChaptersNeedsMoments(t *testing.T) {
	sandbox(t, "")
	if ok, msg, _ := AddChaptersToYouTube(CurrentMatchID(), true); ok || !strings.Contains(msg, "No wickets") {
		t.Fatalf("%v %q", ok, msg)
	}
}
