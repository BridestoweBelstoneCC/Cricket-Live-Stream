package server

import "testing"

// A four/six's replay comes after its animation, not on top of it: the clip is still asked
// for 700ms in (so it covers the same seconds), with the animation's length as the server's
// hold, and the transition and banner wait for the animation to end. With the animation
// switched off, or for a wicket, nothing changes.
func TestReplayWaitsForTheBoundaryAnimation(t *testing.T) {
	src := overlayFunctions(t, "triggerReplay") + `
var FLASH = { four: { ms: 3100 }, six: { ms: 3900 } };
var SERVER = '', cfg = { replay_enabled: true, graphics_boundary_flash: true }, _lastReplayTime = -1e9;
var clock = 0, timers = [], log = [], print = console.log;
console.log = function () {};
function setTimeout(fn, ms) { timers.push({ at: clock + (ms || 0), fn: fn }); }
async function run() {
  while (timers.length) {
    timers.sort(function (a, b) { return a.at - b.at; });
    var x = timers.shift(); clock = x.at; x.fn();
    for (var i = 0; i < 20; i++) await null;
  }
}
function fetch(url, o) {
  var b = JSON.parse(o.body);
  log.push(clock + ' ask ' + b.reason + (b.hold_ms ? ' hold ' + b.hold_ms : ''));
  return Promise.resolve({ ok: true });
}
function triggerReplayTransition(cb) { log.push(clock + ' transition'); setTimeout(cb, 700); }
function showReplayBanner(r) { log.push(clock + ' banner ' + r); }
(async function () {
  var out = {};
  log = []; clock = 0; _lastReplayTime = -1e9; triggerReplay('Six', 'six'); await run(); out.six = log;
  log = []; clock = 0; _lastReplayTime = -1e9; triggerReplay('Boundary', 'four'); await run(); out.four = log;
  log = []; clock = 0; _lastReplayTime = -1e9; triggerReplay('Wicket'); await run(); out.wicket = log;
  cfg.graphics_boundary_flash = false;
  log = []; clock = 0; _lastReplayTime = -1e9; triggerReplay('Boundary', 'four'); await run(); out.noFlash = log;
  print(JSON.stringify(out));
})();`
	var got map[string][]string
	runJS(t, src, &got)
	eqv(t, got["six"], []string{"700 ask Six hold 3900", "3900 transition", "4600 banner Six"})
	eqv(t, got["four"], []string{"700 ask Boundary hold 3100", "3100 transition", "3800 banner Boundary"})
	unheld := func(r string) []string { return []string{"0 transition", "700 ask " + r, "700 banner " + r} }
	eqv(t, got["wicket"], unheld("Wicket"))
	eqv(t, got["noFlash"], unheld("Boundary"))
}
