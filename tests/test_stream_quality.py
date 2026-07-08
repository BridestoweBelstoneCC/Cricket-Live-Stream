"""Adaptive stream quality: the pure downshift decision (hysteresis, cooldown), the
bitrate ladder, and the HTTP surface — everything except talking to a real OBS."""
import os
import sys
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import server
from test_http import HttpTestBase   # noqa: E402


def samples(now, seconds_ago_list, congestion=0.0, dropped_step=0, total_step=375):
    """Build a sample ring: one sample per entry, oldest first."""
    out = []
    dropped = total = 0
    for ago in sorted(seconds_ago_list, reverse=True):
        dropped += dropped_step
        total += total_step
        out.append({"t": now - ago, "congestion": congestion,
                    "dropped": dropped, "total": total})
    return out


class TestDownshiftDecision(unittest.TestCase):
    NOW = 1_000_000.0

    def test_needs_enough_evidence(self):
        action, reason = server.evaluate_stream_samples(
            samples(self.NOW, [30, 15], congestion=0.9), self.NOW, 0)
        self.assertEqual(action, "hold")
        self.assertIn("not enough", reason)

    def test_healthy_stream_holds(self):
        action, reason = server.evaluate_stream_samples(
            samples(self.NOW, [45, 30, 15, 0], congestion=0.02), self.NOW, 0)
        self.assertEqual(action, "hold")
        self.assertIn("healthy", reason)

    def test_sustained_congestion_trips(self):
        action, reason = server.evaluate_stream_samples(
            samples(self.NOW, [45, 30, 15, 0], congestion=0.4), self.NOW, 0)
        self.assertEqual(action, "downshift")
        self.assertIn("congestion", reason)

    def test_dropped_frames_trip(self):
        # ~6.7% of frames dropped across the window, congestion metric quiet
        action, reason = server.evaluate_stream_samples(
            samples(self.NOW, [45, 30, 15, 0], congestion=0.0, dropped_step=25),
            self.NOW, 0)
        self.assertEqual(action, "downshift")
        self.assertIn("frames", reason)

    def test_cooldown_blocks_flapping(self):
        # Same terrible stream, but we shifted 30s ago — the shift's own restart blip
        # must not immediately trigger the next step down
        action, reason = server.evaluate_stream_samples(
            samples(self.NOW, [45, 30, 15, 0], congestion=0.9),
            self.NOW, last_shift_at=self.NOW - 30)
        self.assertEqual(action, "hold")
        self.assertIn("cooling down", reason)

    def test_old_samples_outside_window_ignored(self):
        # Congestion spike 5 minutes ago, clean since → hold
        old = samples(self.NOW, [300, 290], congestion=0.9)
        fresh = samples(self.NOW, [40, 25, 10], congestion=0.01)
        action, _ = server.evaluate_stream_samples(old + fresh, self.NOW, 0)
        self.assertEqual(action, "hold")


class TestLadder(unittest.TestCase):
    def test_ladder_steps(self):
        self.assertEqual(server._stream_ladder_kbps(4000, 0), 4000)
        self.assertEqual(server._stream_ladder_kbps(4000, 1), 2800)
        self.assertEqual(server._stream_ladder_kbps(4000, 2), 2000)
        self.assertEqual(server._stream_ladder_kbps(4000, 3), 1400)

    def test_floor_and_clamping(self):
        self.assertEqual(server._stream_ladder_kbps(1000, 3), 500)     # never below 500
        self.assertEqual(server._stream_ladder_kbps(4000, 99),         # clamps to last step
                         server._stream_ladder_kbps(4000, len(server.STREAM_LADDER) - 1))

    def test_default_state_has_the_toggle(self):
        self.assertIn("stream_auto_downshift", server.DEFAULT_STATE)
        self.assertFalse(server.DEFAULT_STATE["stream_auto_downshift"])


class StreamMonBase(HttpTestBase):
    def setUp(self):
        server._last_good_state = None
        server.save_state(dict(server.DEFAULT_STATE))
        with server._stream_mon_lock:
            server._stream_mon.update({"streaming": False, "reachable": None,
                                       "samples": [], "baseline_kbps": None,
                                       "step": 0, "current_kbps": None,
                                       "last_shift_at": 0.0, "shifts": [],
                                       "last_reason": ""})


class TestStreamHttp(StreamMonBase):
    def test_monitor_endpoint(self):
        status, body = self.get_json("/stream/monitor")
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])
        self.assertFalse(body["streaming"])
        self.assertEqual(body["ladder_pct"], [100, 70, 50, 35])
        self.assertFalse(body["auto"])

    def test_quality_bad_action_rejected(self):
        status, body = self.post_json("/stream/quality", {"action": "sideways"})
        self.assertEqual(status, 400)

    def test_quality_noop_when_already_at_step(self):
        status, body = self.post_json("/stream/quality", {"action": "restore"})
        self.assertEqual(status, 200)
        self.assertFalse(body["ok"])
        self.assertIn("already", body["error"])

    def test_quality_down_without_a_live_baseline_fails_cleanly(self):
        # No stream has been observed → no baseline → clean refusal, no OBS contact
        status, body = self.post_json("/stream/quality", {"action": "down"})
        self.assertEqual(status, 200)
        self.assertFalse(body["ok"])
        self.assertIn("baseline", body["error"].lower())


class TestStreamAuth(StreamMonBase):
    CLUB_PASSWORD = "testpw"

    def test_quality_is_token_gated_but_monitor_is_open(self):
        status, _ = self.post_json("/stream/quality", {"action": "down"})
        self.assertEqual(status, 401)
        status, _, _ = self.request("GET", "/stream/monitor")
        self.assertEqual(status, 200)


if __name__ == "__main__":
    unittest.main()
