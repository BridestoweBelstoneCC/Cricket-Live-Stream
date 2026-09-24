"""Camera auto-cut at the over boundary (autoCutCameraForOver / cutCameraScene) — extracted
from overlay.html and executed in a real JS engine, same approach as the bowler-milestone
chain tests. Covers the two gates (opt-in toggle, second camera configured) and, more
importantly, the replay-collision avoidance: an instant replay owns its own Replay->main
scene transition on a server-side timer (see obs_trigger_replay's `duration`), and this
auto-cut must never fight it by cutting away mid-replay or reverting over top of it. This
whole feature is UNTESTED against real two-camera OBS hardware (see TODO.md) -- these tests
only prove the trigger logic itself behaves, not that the resulting stream looks right.
"""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_js_parity import run_js, REPO_ROOT   # noqa: E402


def extract_function(html, name):
    start = html.index("function " + name)
    # cutCameraScene is declared "async function ..." -- the extracted text must keep the
    # "async" keyword or its internal `await fetch(...)` is a syntax error on its own.
    if html[max(0, start - 6):start] == "async ":
        start -= 6
    depth = 0
    for i in range(start, len(html)):
        if html[i] == "{":
            depth += 1
        elif html[i] == "}":
            depth -= 1
            if depth == 0:
                return html[start:i + 1]
    raise AssertionError(f"could not extract {name}")


def overlay_functions(*names):
    html = open(os.path.join(REPO_ROOT, "overlay.html"), encoding="utf-8").read()
    return "\n".join(extract_function(html, n) for n in names)


# Stubs: fake fetch records every call instead of hitting the network; setTimeout/
# clearTimeout are faked (not real timers) so the test controls exactly when the
# revert-to-main step runs, instead of a real 8-second wait.
STUBS = """
var SERVER = 'http://test';
var cfg = {};
var _lastReplayTime = 0;
var fetchCalls = [];
function fetch(url, opts) {
  fetchCalls.push({url: url, body: JSON.parse(opts.body)});
  return Promise.resolve({ok: true, text: function(){return Promise.resolve('');}});
}
var _cameraAutoCutTimer = null;   // declared alongside autoCutCameraForOver in overlay.html
var _pendingTimeout = null;
function setTimeout(fn, ms) { _pendingTimeout = {fn: fn, ms: ms}; return 1; }
function clearTimeout(id) { _pendingTimeout = null; }
function flushTimeout() {
  var t = _pendingTimeout;
  _pendingTimeout = null;
  if (t) t.fn();
}
function scenesRequested() { return fetchCalls.map(function(c){ return c.body.scene; }); }
// cutCameraScene's own console.log/warn calls would otherwise land on stdout too, after
// the result line below (they fire on a microtask once the fetch stub's promise resolves,
// which happens AFTER the synchronous script -- including the final print -- has already
// run), breaking the single-JSON-line contract run_js relies on. Route them to stderr
// instead, same as a real terminal would keep them separate from a program's real output.
var console = { log: function(){ process.stderr.write(Array.prototype.slice.call(arguments).join(' ') + '\\n'); },
               warn: function(){ process.stderr.write(Array.prototype.slice.call(arguments).join(' ') + '\\n'); } };
"""


def run_scenario(body):
    js = (STUBS
          + overlay_functions("autoCutCameraForOver", "cutCameraScene")
          + "\n" + body
          + "\nprocess.stdout.write(JSON.stringify({scenes: scenesRequested()}));")
    result = run_js(js)
    return result


class TestCameraAutoCut(unittest.TestCase):
    def test_cuts_to_bowler_end_then_reverts_to_main(self):
        result = run_scenario("""
cfg = { graphics_camera_auto_cut: true, camera2_configured: true,
        obs_bowler_scene: 'Main-Bowler', obs_main_scene: 'Main', replay_duration: 18 };
autoCutCameraForOver();
flushTimeout();
""")
        if result is None:
            self.skipTest("no JS engine available")
        self.assertEqual(result["scenes"], ["Main-Bowler", "Main"])

    def test_noop_when_toggle_is_off(self):
        result = run_scenario("""
cfg = { graphics_camera_auto_cut: false, camera2_configured: true };
autoCutCameraForOver();
flushTimeout();
""")
        if result is None:
            self.skipTest("no JS engine available")
        self.assertEqual(result["scenes"], [])

    def test_noop_when_no_second_camera_configured(self):
        result = run_scenario("""
cfg = { graphics_camera_auto_cut: true, camera2_configured: false };
autoCutCameraForOver();
flushTimeout();
""")
        if result is None:
            self.skipTest("no JS engine available")
        self.assertEqual(result["scenes"], [])

    def test_skips_entirely_while_a_replay_is_still_running(self):
        # A boundary earlier in the over that just ended triggered a replay that -- given
        # replay_duration -- could still be on screen. The whole cut (both legs) must be
        # skipped rather than race the replay's own scene transition.
        result = run_scenario("""
cfg = { graphics_camera_auto_cut: true, camera2_configured: true,
        obs_bowler_scene: 'Main-Bowler', obs_main_scene: 'Main', replay_duration: 18 };
_lastReplayTime = Date.now() - 5000;   // 5s ago, well inside an 18s replay window
autoCutCameraForOver();
flushTimeout();
""")
        if result is None:
            self.skipTest("no JS engine available")
        self.assertEqual(result["scenes"], [])

    def test_skips_the_revert_if_a_replay_starts_during_the_bowler_end_window(self):
        # Cut-away fires normally, but a boundary happens on the very next ball -- a replay
        # starts while we're still showing the bowler-end camera. Our own revert-to-main
        # must stand down and let the replay's own timer return to main instead.
        result = run_scenario("""
cfg = { graphics_camera_auto_cut: true, camera2_configured: true,
        obs_bowler_scene: 'Main-Bowler', obs_main_scene: 'Main', replay_duration: 18 };
autoCutCameraForOver();
_lastReplayTime = Date.now() + 1000;   // replay triggers after our cut-away
flushTimeout();
""")
        if result is None:
            self.skipTest("no JS engine available")
        self.assertEqual(result["scenes"], ["Main-Bowler"])   # cut away, but no revert

    def test_a_stale_replay_does_not_block_a_later_over(self):
        # An old replay (well outside the window) must not suppress a fresh cut.
        result = run_scenario("""
cfg = { graphics_camera_auto_cut: true, camera2_configured: true,
        obs_bowler_scene: 'Main-Bowler', obs_main_scene: 'Main', replay_duration: 18 };
_lastReplayTime = Date.now() - 60000;   // a minute ago
autoCutCameraForOver();
flushTimeout();
""")
        if result is None:
            self.skipTest("no JS engine available")
        self.assertEqual(result["scenes"], ["Main-Bowler", "Main"])


if __name__ == "__main__":
    unittest.main()
