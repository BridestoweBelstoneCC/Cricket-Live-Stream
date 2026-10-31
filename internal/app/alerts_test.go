package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/setup"
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

func onAir(on bool) map[string]any { return map[string]any{"streaming": on} }

// The stream stopping is news once it has stayed stopped past a quality step's restart;
// once, and once more when it's back.
func TestAlertStreamStopped(t *testing.T) {
	var s alertState
	t0 := time.Date(2026, 6, 6, 14, 0, 0, 0, time.UTC)
	if n := s.stream(onAir(false), t0); len(n) != 0 {
		t.Fatalf("not on air yet (before Go live): %v", n)
	}
	if n := s.stream(onAir(false), t0.Add(10*time.Minute)); len(n) != 0 {
		t.Fatalf("never went live: %v", n)
	}
	s.stream(onAir(true), t0.Add(11*time.Minute))
	// A quality step: off for a few seconds, back on. Nothing said.
	s.stream(onAir(false), t0.Add(20*time.Minute))
	if n := s.stream(onAir(true), t0.Add(20*time.Minute+10*time.Second)); len(n) != 0 {
		t.Fatalf("a quality step's restart: %v", n)
	}
	off := t0.Add(30 * time.Minute)
	s.stream(onAir(false), off)
	if n := s.stream(onAir(false), off.Add(streamGrace-time.Second)); len(n) != 0 {
		t.Fatalf("inside the grace: %v", n)
	}
	n := s.stream(onAir(false), off.Add(streamGrace))
	if len(n) != 1 || !n[0].Warn || !strings.Contains(n[0].Title, "stopped") || n[0].Local {
		t.Fatalf("stopped: %v", n)
	}
	if n := s.stream(onAir(false), off.Add(10*time.Minute)); len(n) != 0 {
		t.Fatalf("said again: %v", n)
	}
	n = s.stream(onAir(true), off.Add(11*time.Minute))
	if len(n) != 1 || n[0].Warn || !strings.Contains(n[0].Title, "Back on air") {
		t.Fatalf("back: %v", n)
	}
	// No answer from OBS reads as not streaming: the same rule.
	if n := s.stream(map[string]any{"reachable": false}, off.Add(12*time.Minute)); len(n) != 0 {
		t.Fatalf("just gone: %v", n)
	}
}

func TestDiscordMessage(t *testing.T) {
	got := discordMessage("Test Town CC", notice{Title: "The scorer's feed has gone quiet", Text: "No update for 2 minutes.", Warn: true})
	if got != "⚠️ Test Town CC: **The scorer's feed has gone quiet**\nNo update for 2 minutes." {
		t.Fatalf("%q", got)
	}
	if got := discordMessage("", notice{Title: "Back on air", Text: "Live again."}); got != "✅ **Back on air**\nLive again." {
		t.Fatalf("%q", got)
	}
}

// Every alert goes to the club's channel when there's a webhook — but a new version is
// this laptop's news, and with no webhook nothing is sent.
func TestNotifySendsToDiscord(t *testing.T) {
	ta := newTestApp(t)
	sent := make(chan string, 4)
	saved := discordPost
	discordPost = func(hook, msg string) error { sent <- hook + " | " + msg; return nil }
	t.Cleanup(func() { discordPost = saved })

	ta.notify(notice{Title: "OBS", Text: "OBS crashed — reopened.", Warn: false})
	select {
	case m := <-sent:
		t.Fatalf("sent with no webhook: %s", m)
	case <-time.After(300 * time.Millisecond):
	}

	hook := "https://discord.com/api/webhooks/1234567890/abcdefghijklmnopqrstuvwxyz"
	os.WriteFile(setup.ConfigFile(), []byte("[App]\nrole = streaming\n\n[Club]\nname = Test Town CC\n\n[Alerts]\ndiscord_webhook = "+hook+"\n"), 0o600)
	ta.notify(notice{Title: "The stream has stopped", Text: "Nothing has gone to YouTube.", Warn: true})
	select {
	case m := <-sent:
		if !strings.HasPrefix(m, hook+" | ⚠️ Test Town CC: **The stream has stopped**") {
			t.Fatalf("sent %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing sent to Discord")
	}
	ta.notify(s0update())
	select {
	case m := <-sent:
		t.Fatalf("a new version went to the club channel: %s", m)
	case <-time.After(300 * time.Millisecond):
	}
}

func s0update() notice {
	var s alertState
	return s.update("3.1.0")[0]
}
