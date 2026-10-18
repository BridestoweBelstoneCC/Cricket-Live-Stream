package app

import (
	"strings"
	"testing"
)

func feed(fresh bool) map[string]any {
	return map[string]any{"pcs": map[string]any{"fresh": fresh, "manual_scoring": false}}
}

// A tea break is one notification, and one more when the feed is back — never one a poll.
func TestAlertFeedQuietOnceThenBack(t *testing.T) {
	var s alertState
	if n := s.health(feed(false)); len(n) != 0 {
		t.Fatalf("quiet before it was ever live (morning, no match yet): %v", n)
	}
	if n := s.health(feed(true)); len(n) != 0 {
		t.Fatalf("live: %v", n)
	}
	n := s.health(feed(false))
	if len(n) != 1 || !n[0].Warn || !strings.Contains(n[0].Title, "gone quiet") {
		t.Fatalf("gone quiet: %v", n)
	}
	for i := 0; i < 5; i++ {
		if n := s.health(feed(false)); len(n) != 0 {
			t.Fatalf("said again: %v", n)
		}
	}
	n = s.health(feed(true))
	if len(n) != 1 || n[0].Warn {
		t.Fatalf("back: %v", n)
	}
	if n := s.health(feed(true)); len(n) != 0 {
		t.Fatalf("back said again: %v", n)
	}
}

func TestAlertFeedQuietIgnoredWhenScoringByHand(t *testing.T) {
	var s alertState
	h := map[string]any{"pcs": map[string]any{"fresh": true, "manual_scoring": true}}
	s.health(h)
	h["pcs"].(map[string]any)["fresh"] = false
	if n := s.health(h); len(n) != 0 {
		t.Fatalf("scoring in the panel: %v", n)
	}
}

func obsEvent(t float64, what string) map[string]any {
	ev := map[string]any{}
	if what != "" {
		ev = map[string]any{"time": t, "what": what}
	}
	return map[string]any{"obs_recovery": map[string]any{"last_event": ev}}
}

// An event from before the app looked (an earlier crash) isn't news; later ones are, once.
func TestAlertOBSEvents(t *testing.T) {
	var s alertState
	if n := s.health(obsEvent(100, "OBS crashed — reopened")); len(n) != 0 {
		t.Fatalf("the baseline was reported: %v", n)
	}
	if n := s.health(obsEvent(100, "OBS crashed — reopened")); len(n) != 0 {
		t.Fatalf("the same event again: %v", n)
	}
	n := s.health(obsEvent(200, "OBS crashed — reopened and the stream restarted"))
	if len(n) != 1 || n[0].Warn || n[0].Text != "OBS crashed — reopened and the stream restarted." {
		t.Fatalf("reopened: %v", n)
	}
	n = s.health(obsEvent(300, "reopened, not answering"))
	if len(n) != 1 || !n[0].Warn || n[0].Text != "Reopened, not answering." {
		t.Fatalf("not answering: %v", n)
	}
	if n := s.health(obsEvent(400, "OBS closed normally — left closed")); len(n) != 0 {
		t.Fatalf("a normal close: %v", n)
	}
	n = s.health(obsEvent(500, "OBS keeps crashing — gave up reopening it"))
	if len(n) != 1 || !n[0].Warn {
		t.Fatalf("gave up: %v", n)
	}
}

func TestAlertOBSNoEventYet(t *testing.T) {
	var s alertState
	s.health(obsEvent(0, ""))
	if n := s.health(obsEvent(50, "OBS frozen — force-closed for reopening")); len(n) != 1 || !n[0].Warn {
		t.Fatalf("first event after an empty baseline: %v", n)
	}
}

func TestAlertAgentStopsAndResumes(t *testing.T) {
	var s alertState
	if n := s.agent(map[string]any{}); n != nil {
		t.Fatalf("never collected: %v", n)
	}
	if n := s.agent(map[string]any{"collected_ago": 200}); n != nil {
		t.Fatalf("stale before it was ever collecting: %v", n)
	}
	s.agent(map[string]any{"collected_ago": 2})
	if n := s.agent(map[string]any{"collected_ago": 60}); n != nil {
		t.Fatalf("a minute is fine: %v", n)
	}
	n := s.agent(map[string]any{"collected_ago": 95})
	if len(n) != 1 || !n[0].Warn {
		t.Fatalf("stopped: %v", n)
	}
	if n := s.agent(map[string]any{"collected_ago": 300}); n != nil {
		t.Fatalf("said again: %v", n)
	}
	n = s.agent(map[string]any{"collected_ago": 1})
	if len(n) != 1 || n[0].Warn {
		t.Fatalf("back: %v", n)
	}
}

func TestAlertUpdateOnce(t *testing.T) {
	var s alertState
	if n := s.update(""); n != nil {
		t.Fatal(n)
	}
	if n := s.update("3.0.1"); len(n) != 1 || !strings.Contains(n[0].Title, "v3.0.1") {
		t.Fatalf("%v", n)
	}
	if n := s.update("3.0.1"); n != nil {
		t.Fatalf("again: %v", n)
	}
}

func TestLogLineNotice(t *testing.T) {
	if n := logLineNotice("  ⚠ Server stopped unexpectedly — restarting (attempt 1/5)"); n == nil || !n.Warn {
		t.Fatal("restart not noticed")
	}
	if n := logLineNotice("The server keeps crashing (restarted it 5 times)"); n == nil {
		t.Fatal("give-up not noticed")
	}
	if n := logLineNotice("  ✓ Server started"); n != nil {
		t.Fatalf("%v", n)
	}
}

// The log hands each complete line to its hook, once.
func TestLogOnLine(t *testing.T) {
	var l Log
	var got []string
	l.onLine = func(s string) { got = append(got, s) }
	l.Write([]byte("one\r\ntw"))
	l.Write([]byte("o\nthree"))
	if strings.Join(got, "|") != "one|two" {
		t.Fatalf("%q", got)
	}
}
