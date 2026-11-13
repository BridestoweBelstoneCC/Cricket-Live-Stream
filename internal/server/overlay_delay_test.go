package server

// The graphics delay (the code review's F1): with it on, every /live frame waits before
// it's drawn, in order; off, nothing waits. The overlay's own functions, run in node with
// a clock the test controls.

import (
	"strings"
	"testing"
)

func delayRun(t *testing.T, script string) map[string]any {
	t.Helper()
	src := overlayFunctions(t, "graphicsDelayMs", "drainHeldFrames") + `
var GRAPHICS_DELAY_MAX_S = 10, _heldFrames = [], _heldTimer = null;
var cfg = {}, clock = 1000000, drawn = [], timer = null;
Date.now = function () { return clock; };
function setTimeout(fn, ms) { timer = { fn: fn, at: clock + ms }; return timer; }
function clearTimeout(x) { if (timer === x) timer = null; }
function applyLiveFrame(f) { drawn.push(f.n + '@' + (clock - 1000000)); }
// What poll() does with a frame.
function arrive(n) {
  var frame = { at: clock, n: n };
  if (graphicsDelayMs() > 0 || _heldFrames.length) { _heldFrames.push(frame); drainHeldFrames(); }
  else applyLiveFrame(frame);
}
// Time passes: timers fire when they're due.
function pass(ms) {
  var end = clock + ms;
  while (timer && timer.at <= end) { clock = timer.at; var f = timer.fn; timer = null; f(); }
  clock = end;
}
var out = {};
` + script + `
process.stdout.write(JSON.stringify(out));`
	var got map[string]any
	runJS(t, src, &got)
	return got
}

func TestGraphicsDelayOffDrawsAtOnce(t *testing.T) {
	got := delayRun(t, `
arrive(1); pass(2500); arrive(2);
cfg = { graphics_delay: false, graphics_delay_seconds: 3 };   // a number set, but switched off
pass(2500); arrive(3);
cfg = { graphics_delay: true, graphics_delay_seconds: 0 };    // on, with nothing to wait
pass(2500); arrive(4);
out.drawn = drawn; out.held = _heldFrames.length; out.timer = timer === null;
`)
	eqv(t, got["drawn"], []string{"1@0", "2@2500", "3@5000", "4@7500"})
	eqv(t, got["held"], 0)
	eqv(t, got["timer"], true)
}

func TestGraphicsDelayHoldsEveryFrameThatLong(t *testing.T) {
	got := delayRun(t, `
cfg = { graphics_delay: true, graphics_delay_seconds: 2.5 };
arrive(1); out.atOnce = drawn.slice();
pass(1000); arrive(2);            // 1.0s
pass(1400); out.before = drawn.slice();   // 2.4s: not yet
pass(200);  out.after = drawn.slice();    // 2.6s
pass(5000); out.all = drawn.slice();
`)
	eqv(t, got["atOnce"], []string{})
	eqv(t, got["before"], []string{})
	eqv(t, got["after"], []string{"1@2500"})
	eqv(t, got["all"], []string{"1@2500", "2@3500"}, "each frame exactly 2.5s after it arrived")
}

// Shortening the delay or switching it off mid-match: what's held comes out oldest first,
// and a new frame never overtakes it.
func TestGraphicsDelayNeverReordersTheScore(t *testing.T) {
	got := delayRun(t, `
cfg = { graphics_delay: true, graphics_delay_seconds: 8 };
arrive(1); pass(2500); arrive(2); pass(2500); arrive(3);
cfg = { graphics_delay: true, graphics_delay_seconds: 1 };   // shortened
pass(100); arrive(4);
out.shortened = drawn.slice();
pass(3000);
out.settled = drawn.slice();
cfg = { graphics_delay: true, graphics_delay_seconds: 5 };
arrive(5); pass(1000); arrive(6);
cfg = { graphics_delay: false };                              // switched off with two held
pass(500); arrive(7);
out.off = drawn.slice(); out.held = _heldFrames.length;
`)
	eqv(t, got["shortened"], []string{"1@5100", "2@5100"}, "what's already old enough, in order; 3 is 100ms old")
	eqv(t, got["settled"], []string{"1@5100", "2@5100", "3@6000", "4@6100"})
	eqv(t, got["off"], []string{"1@5100", "2@5100", "3@6000", "4@6100", "5@9600", "6@9600", "7@9600"})
	eqv(t, got["held"], 0)
}

func TestGraphicsDelayIsCapped(t *testing.T) {
	got := delayRun(t, `
cfg = { graphics_delay: true, graphics_delay_seconds: 600 };
out.capped = graphicsDelayMs();
cfg = { graphics_delay: true, graphics_delay_seconds: "abc" };
out.junk = graphicsDelayMs();
cfg = { graphics_delay: true, graphics_delay_seconds: -4 };
out.negative = graphicsDelayMs();
cfg = { graphics_delay: true, graphics_delay_seconds: "1.5" };
out.text = graphicsDelayMs();
`)
	eqv(t, got["capped"], 10000)
	eqv(t, got["junk"], 0)
	eqv(t, got["negative"], 0)
	eqv(t, got["text"], 1500)
}

// The wiring the tests above stand in for: poll() notes the frame when it ARRIVES (the
// catch-up rule and the "graphics have stopped" watch are about the feed, not the delay),
// queues it, and what the operator presses is never held.
func TestGraphicsDelayWiring(t *testing.T) {
	overlay := page(t, "overlay.html")
	poll := extractFunction(t, overlay, "poll")
	for _, want := range []string{"catchingUp:noteLiveFrame(now)", "_heldFrames.push(frame); drainHeldFrames();", "else applyLiveFrame(frame);"} {
		if !strings.Contains(poll, want) {
			t.Errorf("poll() lacks %q", want)
		}
	}
	if strings.Contains(poll, "renderScorebar(") || strings.Contains(poll, "detectEvents(") {
		t.Error("poll() draws a frame itself: with the delay on it would be drawn twice, once early")
	}
	apply := extractFunction(t, overlay, "applyLiveFrame")
	for _, want := range []string{"_catchingUp = frame.catchingUp", "renderScorebar(", "detectEvents(", "processPCSData("} {
		if !strings.Contains(apply, want) {
			t.Errorf("applyLiveFrame() lacks %q", want)
		}
	}
	if cmds := extractFunction(t, overlay, "pollCommands"); strings.Contains(cmds, "_heldFrames") || strings.Contains(cmds, "graphicsDelayMs") {
		t.Error("the operator's buttons are being held back")
	}
	// Off unless someone turns it on, and the panel can.
	mustEq(t, DefaultState()["graphics_delay"], any(false))
	panel := page(t, "control.html")
	for _, id := range []string{`id="graphics_delay"`, `id="graphics_delay_seconds"`, "'graphics_delay'", "DECIMAL_FIELDS"} {
		if !strings.Contains(panel, id) {
			t.Errorf("control.html lacks %s", id)
		}
	}
	if k, ok := postedStateKind("graphics_delay_seconds"); !ok || k != "number" {
		t.Error("graphics_delay_seconds can't be saved")
	}
}

func TestSyncClockIsAPanelCommand(t *testing.T) {
	s := newTestServer(t, "")
	popCommands()
	st, d := s.postJSON("/sync/show", M{})
	mustEq(t, st, 200)
	mustEq(t, d["ok"], any(true))
	mustEq(t, popCommands()["sync_test"], any(true))
	mustEq(t, popCommands()["sync_test"], any(false)) // once
	if !strings.Contains(extractFunction(t, page(t, "overlay.html"), "pollCommands"), "if (cmds.sync_test) showSyncClock();") {
		t.Error("the overlay doesn't show the clock")
	}
}
