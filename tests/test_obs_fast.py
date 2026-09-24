"""The held-open OBS socket used for camera cuts (_obs_fast_call).

Cutting between camera angles is something an operator does repeatedly while following the
play, so the per-call connect/Hello/auth/Identify handshake that _obs_call pays is the
wrong trade there. Measured against a live OBS 32.2.2 in the test VM:

    _obs_call     median 31.9 ms   (min 24.0, max 35.9)
    _obs_fast_call median 8.8 ms   (min  5.2, max 16.6)   -> 3.6x

_obs_call's docstring justifies connection-per-call on the grounds that never holding a
socket means a mid-match OBS restart can't wedge anything. The fast path keeps that
property a different way — reconnect on any error — which was verified on the VM rather
than assumed:

    OBS killed mid-session -> None in ~2s (no hang)
    OBS restarted          -> True in 63ms, reconnected by itself, then back to ~14ms

These tests cover what's checkable without an OBS: the unreachable path stays bounded and
returns None so the caller can fall back, and the socket is dropped when it shouldn't be
trusted. The speed itself needs the VM.
"""
import os
import re
import sys
import time
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)
import server  # noqa: E402

# Port chosen to be closed. Connecting here should be refused promptly, not time out.
DEAD = {"obs_host": "127.0.0.1", "obs_port": 45999, "obs_password": ""}


class TestObsFastCall(unittest.TestCase):
    def setUp(self):
        with server._obs_fast_lock:
            server._obs_fast_drop()

    tearDown = setUp

    def test_unreachable_obs_returns_none_so_the_caller_can_fall_back(self):
        self.assertIsNone(server._obs_fast_call(DEAD, "SetCurrentProgramScene",
                                                {"sceneName": "Main"}, timeout=2))

    def test_unreachable_obs_fails_quickly_and_does_not_retry_the_connect(self):
        # Retrying a CONNECT failure just doubles the wait before the caller can fall back
        # or report it — an earlier version did, which made "OBS not running" take ~14s and
        # timed out an HTTP test. A refused connection must stay well inside one timeout.
        t0 = time.perf_counter()
        server._obs_fast_call(DEAD, "SetCurrentProgramScene", {"sceneName": "Main"}, timeout=2)
        elapsed = time.perf_counter() - t0
        self.assertLess(elapsed, 6.0,
                        f"took {elapsed:.1f}s — looks like the connect failure is retried")

    def test_no_socket_is_left_behind_after_a_failure(self):
        server._obs_fast_call(DEAD, "SetCurrentProgramScene", {"sceneName": "Main"}, timeout=2)
        self.assertIsNone(server._obs_fast["ws"],
                          "a failed call must not leave a dead socket cached")

    def test_changing_obs_settings_invalidates_the_held_socket(self):
        # A password or host change mid-match must not keep talking down the old socket.
        server._obs_fast["ws"] = object()          # stand-in for a live connection
        server._obs_fast["key"] = ("otherhost", 4455, "old")
        server._obs_fast["used"] = time.time()
        server._obs_fast_call(DEAD, "SetCurrentProgramScene", {"sceneName": "Main"}, timeout=2)
        self.assertIsNone(server._obs_fast["ws"])

    def test_an_idle_socket_is_not_trusted_indefinitely(self):
        self.assertLessEqual(server.OBS_FAST_IDLE_SEC, 300,
                             "a socket held for very long without use is more likely to be "
                             "half-dead than useful")

    def test_camera_endpoint_prefers_the_fast_path_but_keeps_the_fallback(self):
        src = open(os.path.join(REPO, "server.py"), encoding="utf-8").read()
        endpoint = src.split('elif path == "/camera/scene":')[1][:1600]
        self.assertIn("_obs_fast_call", endpoint, "camera cuts should use the fast path")
        self.assertIn("_obs_call", endpoint,
                      "the connection-per-call fallback must remain for when the fast "
                      "path can't connect at all")

    def test_other_callers_still_use_the_per_call_connection(self):
        # The stream sentinel and health checks are seconds-to-minutes apart; the fast
        # path's held socket buys them nothing and would only add a failure mode.
        src = open(os.path.join(REPO, "server.py"), encoding="utf-8").read()
        fast_uses = len(re.findall(r"_obs_fast_call\(", src))
        self.assertLessEqual(fast_uses, 2,
                             "the fast path is for camera cuts only — if something else "
                             "now needs it, make that a deliberate decision")


if __name__ == "__main__":
    unittest.main()
