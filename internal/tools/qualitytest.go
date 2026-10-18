package tools

// Automated stream-quality-ladder test.
//
// Automates the tedious/timing-sensitive part of the manual "does the quality ladder
// survive a real broadcast" test: watching congestion, triggering a manual quality step,
// waiting through the ~5-10s stop-reconfigure-start gap, and confirming the stream
// actually survived rather than dying — then doing the same for the restore step.
//
// Deliberately does NOT start or stop the OBS stream itself — going live is a real,
// visible action you should trigger yourself. Click "Start Streaming" in OBS first.
//
// BEFORE YOU RUN THIS:
//   - OBS must already be streaming, using a PERSISTENT STREAM KEY. "Connect account"
//     mode's auto-stop ends the whole broadcast the moment a quality step stops/restarts
//     the stream connection — this would then report a false failure (or worse, silently
//     confirm a broadcast that's actually already dead).
//   - Set the YouTube broadcast to Unlisted or Private first — this is a test, not
//     something to publish. This never touches YouTube's privacy setting.
//   - The CricketStream server must already be running.
//
//	cricketstream quality-test

import (
	"fmt"
	"strings"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/pyval"
)

// Waits, as package vars so tests can shrink them.
var (
	settleWait   = 8 * time.Second  // let a fresh stream settle before the first check
	shiftWait    = 12 * time.Second // let a down/restore step's stop-reconfigure-start finish
	pollInterval = 2 * time.Second
)

func qlog(msg, status string) {
	icons := map[string]string{"ok": "  ✓", "warn": "  ⚠", "err": "  ✗", "": "   "}
	icon, ok := icons[status]
	if !ok {
		icon = "   "
	}
	fmt.Printf("%s %s\n", icon, msg)
}

func streamStatus(c *obsws.Conn) map[string]any {
	r, err := c.Request("GetStreamStatus", nil)
	if err != nil || !r.OK {
		return nil
	}
	return r.Data
}

func loginToken() string {
	pw := strings.TrimSpace(config().Get("Auth", "club_password", ""))
	if pw == "" {
		return ""
	}
	d := postJSON("/login", map[string]any{"password": pw}, "", 10*time.Second)
	if !pyval.Truthy(d["ok"]) {
		return ""
	}
	s, _ := d["session_token"].(string)
	return s
}

// watchSurvives polls GetStreamStatus every pollInterval for d, printing outputActive and
// dropped-frame counts. False the moment the stream is seen NOT active — that's the actual
// failure mode this whole test exists to catch.
func watchSurvives(c *obsws.Conn, d time.Duration, label string) bool {
	qlog(fmt.Sprintf("Watching the stream through %s (%ds)...", label, int(d.Seconds())), "")
	end := time.Now().Add(d)
	survived := true
	for time.Now().Before(end) {
		st := streamStatus(c)
		if st == nil {
			qlog("Could not reach OBS mid-check — is it still running?", "err")
			return false
		}
		skipped := st["outputSkippedFrames"]
		if skipped == nil {
			skipped = 0
		}
		fmt.Printf("      active=%s  skipped_frames=%s  reconnecting=%s\n",
			pyval.Str(st["outputActive"]), pyval.Str(skipped), pyval.Str(st["outputReconnecting"]))
		if !pyval.Truthy(st["outputActive"]) {
			survived = false
		}
		time.Sleep(pollInterval)
	}
	return survived
}

func okOr(cond bool, bad string) string {
	if cond {
		return "ok"
	}
	return bad
}

// QualityTest is `cricketstream quality-test`. Returns 0 only when both shifts survived.
func QualityTest() int {
	token := loginToken()

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  CricketStream — automated stream-quality-ladder test")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	c, err := obsws.Dial(obsTarget(), 8*time.Second)
	if err != nil {
		return fail("  ✗  Could not connect to OBS: %v", err)
	}
	defer c.Close()
	qlog("Connected to OBS", "ok")

	base := streamStatus(c)
	if base == nil || !pyval.Truthy(base["outputActive"]) {
		return fail("  ✗  OBS is not currently streaming. Start streaming in OBS first " +
			"(persistent stream key, Unlisted/Private broadcast), then re-run this.")
	}
	sent := base["outputBytes"]
	if sent == nil {
		sent = 0
	}
	qlog(fmt.Sprintf("Stream is live — %s bytes sent so far", pyval.Str(sent)), "ok")

	monitor := func() (map[string]any, bool) {
		m, err := getJSON("/stream/monitor", token, 6*time.Second)
		if err != nil {
			fail("  ✗  Could not read /stream/monitor from the server: %v — is it running?", err)
			return nil, false
		}
		return m, true
	}
	mon, ok := monitor()
	if !ok {
		return 1
	}
	qlog(fmt.Sprintf("Server sees it too — step %s, dynamic_bitrate=%s, congestion=%s",
		pyval.Str(mon["step"]), pyval.Str(mon["dynamic_bitrate"]), pyval.Str(mon["congestion"])),
		okOr(pyval.Truthy(mon["ok"]), "warn"))

	qlog(fmt.Sprintf("Letting it settle for %ds before the first shift...", int(settleWait.Seconds())), "")
	time.Sleep(settleWait)

	// ── Step down ──
	fmt.Println()
	qlog("Triggering: Reduce quality now (down one step)", "")
	r := postJSON("/stream/quality", map[string]any{"action": "down"}, token, 15*time.Second)
	if !pyval.Truthy(r["ok"]) {
		return fail("  ✗  /stream/quality down was rejected: %s", pyval.Str(r["error"]))
	}
	downOK := watchSurvives(c, shiftWait, "the down-shift")
	if mon, ok = monitor(); !ok {
		return 1
	}
	qlog(fmt.Sprintf("Now at step %s, %s kbps (baseline %s kbps)",
		pyval.Str(mon["step"]), pyval.Str(mon["current_kbps"]), pyval.Str(mon["baseline_kbps"])), okOr(downOK, "err"))
	if !downOK {
		qlog("Stream did NOT survive the down-shift — check OBS/YouTube Studio now", "err")
	}

	// ── Restore ──
	fmt.Println()
	qlog("Triggering: Restore full quality", "")
	r = postJSON("/stream/quality", map[string]any{"action": "restore"}, token, 15*time.Second)
	if !pyval.Truthy(r["ok"]) {
		return fail("  ✗  /stream/quality restore was rejected: %s", pyval.Str(r["error"]))
	}
	upOK := watchSurvives(c, shiftWait, "the restore")
	if mon, ok = monitor(); !ok {
		return 1
	}
	qlog(fmt.Sprintf("Back to step %s, %s kbps", pyval.Str(mon["step"]), pyval.Str(mon["current_kbps"])), okOr(upOK, "err"))
	if !upOK {
		qlog("Stream did NOT survive the restore — check OBS/YouTube Studio now", "err")
	}

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────────")
	if downOK && upOK {
		fmt.Println("  ✓  ALL SYSTEMS GO — both shifts survived cleanly")
	} else {
		fmt.Println("  ⚠  At least one shift did not survive — do not enable auto quality " +
			"scaling until this is understood")
	}
	fmt.Println("  Stream is still live — stop it manually in OBS when you're done.")
	fmt.Println("  ─────────────────────────────────────────────")
	fmt.Println()
	if downOK && upOK {
		return 0
	}
	return 1
}
