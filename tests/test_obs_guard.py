"""OBS crash recovery (server._obs_guard_tick) — reopening OBS mid-match with nobody there.

The process, launch and WebSocket are mocked; the crash marker is a real file in a temp
folder, because "crashed vs. closed on purpose" is decided by whether OBS left it behind.
"""
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import obs_prep  # noqa: E402
import server    # noqa: E402


class TestObsGuard(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.state = {"obs_host": "localhost", "obs_port": 4455, "obs_main_scene": "Main"}
        self.running = True
        self.hung = False
        self.calls = []

        for target, kwargs in [
            (mock.patch.object(server, "load_state"), {"side_effect": lambda: dict(self.state)}),
            (mock.patch.object(server, "_obs_call"), {"side_effect": self.fake_obs}),
            (mock.patch.object(server.time, "sleep"), {}),
            (mock.patch.object(obs_prep, "obs_config_dir"), {"return_value": self.tmp}),
            (mock.patch.object(obs_prep, "find_obs"), {"return_value": "/fake/obs64.exe"}),
            (mock.patch.object(obs_prep, "port_open"), {"side_effect": lambda port: self.running}),
            (mock.patch.object(obs_prep, "obs_running"), {"side_effect": lambda: self.running}),
            (mock.patch.object(obs_prep, "wait_for_websocket"), {"return_value": True}),
            (mock.patch.object(obs_prep, "obs_window_hung"), {"side_effect": lambda **k: self.hung}),
            (mock.patch.object(obs_prep, "obs_pids"), {"return_value": [4242]}),
            (mock.patch.object(server, "_obs_is_streaming"), {"side_effect": self.is_streaming}),
        ]:
            target.start().configure_mock(**kwargs)
            self.addCleanup(target.stop)
        self.launch = mock.patch.object(obs_prep, "launch_obs").start()
        self.kill = mock.patch.object(obs_prep, "kill_obs").start()
        self.kill.side_effect = self.crash
        self.addCleanup(mock.patch.stopall)

        # Fresh guard and stream-monitor state for every test (module-level dicts).
        self._saved_guard = dict(server._obs_guard)
        self._saved_mon = {k: server._stream_mon[k] for k in ("reachable", "streaming")}
        server._obs_guard.update({"armed": False, "streaming": False, "restarts": [],
                                  "gave_up": False, "last_event": None,
                                  "hung_since": None, "freeze_checked": None, "pids": None})
        self.addCleanup(server._obs_guard.update, self._saved_guard)
        self.addCleanup(server._stream_mon.update, self._saved_mon)
        self.mon(reachable=True, streaming=False)

    def fake_obs(self, st, reqs, timeout=6):
        self.calls.append(reqs)
        return [{}]

    def is_streaming(self, st):
        # The guard asks OBS "are you live?" on every check; answer from the same flag the
        # test sets, and None (no answer) once OBS has gone.
        return None if not self.running else bool(server._stream_mon["streaming"])

    def mon(self, reachable, streaming):
        server._stream_mon["reachable"] = reachable
        server._stream_mon["streaming"] = streaming

    def crash(self):
        """OBS dies leaving its marker behind, as a crash or force-close does."""
        folder = os.path.join(self.tmp, ".sentinel")
        os.makedirs(folder, exist_ok=True)
        open(os.path.join(folder, "run_c887aaf3-ecd5-4b72-a795-6f49aaefc6f1"), "w").close()
        self.running = False
        # The stream monitor notices first and flips to unreachable/not streaming.
        self.mon(reachable=False, streaming=False)

    def markers(self):
        try:
            return os.listdir(os.path.join(self.tmp, ".sentinel"))
        except OSError:
            return []

    def test_crash_reopens_obs_without_the_safe_mode_marker(self):
        self.assertEqual(server._obs_guard_tick(), "armed")
        self.crash()
        self.launch.side_effect = lambda exe, args=None: self.assertEqual(self.markers(), [])
        self.assertEqual(server._obs_guard_tick(), "restarted")
        self.assertEqual(self.launch.call_args[0][0], "/fake/obs64.exe")

    def test_stream_restarted_if_it_was_live(self):
        self.mon(reachable=True, streaming=True)
        server._obs_guard_tick()
        self.crash()      # monitor now says streaming=False — must not erase "it was live"
        server._obs_guard_tick()
        sent = [r[0] for r in self.calls[-1]]
        self.assertEqual(sent, ["SetCurrentProgramScene", "StartStream"])
        self.assertEqual(self.calls[-1][0][1], {"sceneName": "Main"})

    def test_stream_not_started_if_it_wasnt_live(self):
        # A crash before kick-off must not put the club on air.
        server._obs_guard_tick()
        self.crash()
        server._obs_guard_tick()
        self.assertNotIn("StartStream", [r[0] for r in self.calls[-1]])

    def test_normal_close_is_left_closed(self):
        server._obs_guard_tick()
        self.running = False          # clean exit: no marker left
        self.assertEqual(server._obs_guard_tick(), "closed")
        self.assertIsNone(server._obs_guard_tick())    # and stays disarmed
        self.launch.assert_not_called()

    def test_normal_close_puts_the_update_check_back(self):
        obs_prep.pause_update_check(self.tmp)
        server._obs_guard_tick()
        self.running = False
        server._obs_guard_tick()
        self.assertFalse(os.path.exists(os.path.join(self.tmp, obs_prep.UPDATES_MARKER)))
        self.assertEqual(obs_prep._read_ini_value(os.path.join(self.tmp, "global.ini"),
                                                  "General", "EnableAutoUpdates"), "true")

    def test_crash_leaves_the_update_check_paused(self):
        # Reopened mid-match: the pop-up must stay away.
        obs_prep.pause_update_check(self.tmp)
        server._obs_guard_tick()
        self.crash()
        server._obs_guard_tick()
        self.assertTrue(os.path.exists(os.path.join(self.tmp, obs_prep.UPDATES_MARKER)))

    def test_reopened_by_hand_is_watched_again(self):
        server._obs_guard_tick()
        self.running = False
        server._obs_guard_tick()      # closed normally
        self.running = True
        self.assertEqual(server._obs_guard_tick(), "armed")

    def test_obs_never_seen_is_not_started(self):
        # Server up but OBS never opened this session (or it's on another computer).
        self.running = False
        self.crash()
        self.assertIsNone(server._obs_guard_tick())
        self.launch.assert_not_called()

    def test_crash_loop_capped(self):
        server._obs_guard_tick()
        results = []
        for i in range(server.OBS_GUARD_MAX_RESTARTS + 1):
            self.crash()
            results.append(server._obs_guard_tick(now=1000 + i))
            self.running = True
            server._obs_guard_tick(now=1000 + i)
        self.assertEqual(results[:-1], ["restarted"] * server.OBS_GUARD_MAX_RESTARTS)
        self.assertEqual(results[-1], "gave_up")
        self.assertEqual(self.launch.call_count, server.OBS_GUARD_MAX_RESTARTS)
        self.assertTrue(server.obs_guard_status()["gave_up"])

    def test_old_crashes_age_out_of_the_cap(self):
        server._obs_guard_tick()
        for i in range(server.OBS_GUARD_MAX_RESTARTS):
            self.crash()
            server._obs_guard_tick(now=1000 + i)
            self.running = True
            server._obs_guard_tick(now=1000 + i)
        self.crash()
        later = 1000 + server.OBS_GUARD_WINDOW_SEC + 10
        self.assertEqual(server._obs_guard_tick(now=later), "restarted")

    def test_obs_that_doesnt_come_back_is_reported(self):
        server._obs_guard_tick()
        self.crash()
        obs_prep.wait_for_websocket.return_value = False
        self.assertEqual(server._obs_guard_tick(), "restart_failed")
        self.assertEqual(self.calls, [])

    def test_disabled_by_config_or_remote_obs(self):
        with mock.patch.object(server, "_config_ini_path",
                               return_value=os.path.join(self.tmp, "config.ini")):
            self.assertTrue(server._obs_guard_enabled())
            with open(os.path.join(self.tmp, "config.ini"), "w") as f:
                f.write("[OBS]\nmanage_obs = no\n")
            self.assertFalse(server._obs_guard_enabled())
            os.remove(os.path.join(self.tmp, "config.ini"))
            self.state["obs_host"] = "192.168.1.50"
            self.assertFalse(server._obs_guard_enabled())

    # ── Frozen ("Not Responding") ──

    def stream_status(self, *readings):
        """Make GetStreamStatus return these (outputActive, outputBytes) in turn."""
        it = iter(readings)
        def call(st, reqs, timeout=6):
            self.calls.append(reqs)
            if reqs[0][0] == "GetStreamStatus":
                r = next(it, None)
                return None if r is None else [{"outputActive": r[0], "outputBytes": r[1]}]
            return [{}]
        server._obs_call.side_effect = call

    def test_brief_freeze_is_waited_out(self):
        server._obs_guard_tick(now=1000)
        self.hung = True
        self.assertIsNone(server._obs_guard_tick(now=1005))
        self.assertIsNone(server._obs_guard_tick(now=1005 + server.OBS_FREEZE_SEC - 1))
        self.hung = False
        self.assertIsNone(server._obs_guard_tick(now=1100))
        self.hung = True       # a new freeze starts the clock again
        server._obs_guard_tick(now=1105)
        self.assertIsNone(server._obs_guard_tick(now=1105 + server.OBS_FREEZE_SEC - 1))
        self.kill.assert_not_called()

    def test_frozen_and_not_streaming_is_closed_then_reopened(self):
        self.stream_status((False, 0))
        server._obs_guard_tick(now=1000)
        self.hung = True
        server._obs_guard_tick(now=1005)
        self.assertEqual(server._obs_guard_tick(now=1005 + server.OBS_FREEZE_SEC),
                         "frozen_killed")
        self.kill.assert_called_once()
        # The force-close left the crash marker, so the next check reopens it.
        self.hung = False
        self.assertEqual(server._obs_guard_tick(now=1070), "restarted")
        self.launch.assert_called_once()

    def test_frozen_window_over_a_flowing_stream_is_left_alone(self):
        # Encoding runs off the UI thread: viewers may see nothing wrong. Killing OBS
        # would cause the outage this exists to prevent.
        self.stream_status((True, 1000), (True, 5000), (True, 9000), (True, 13000))
        server._obs_guard_tick(now=1000)
        self.hung = True
        server._obs_guard_tick(now=1005)
        t = 1005 + server.OBS_FREEZE_SEC
        self.assertEqual(server._obs_guard_tick(now=t), "frozen_streaming")
        # Not re-measured every tick...
        n = len(self.calls)
        self.assertEqual(server._obs_guard_tick(now=t + 5), "frozen_streaming")
        self.assertEqual(len(self.calls), n)
        # ...but it is periodically, and still left alone while bytes keep moving.
        self.assertEqual(server._obs_guard_tick(now=t + server.OBS_FREEZE_RECHECK_SEC),
                         "frozen_streaming")
        self.kill.assert_not_called()

    def test_frozen_with_a_stalled_stream_is_closed_and_the_stream_restarted(self):
        self.mon(reachable=True, streaming=True)
        server._obs_guard_tick(now=1000)
        self.stream_status((True, 5000), (True, 5000))     # live, but nothing moving
        self.hung = True
        server._obs_guard_tick(now=1005)
        self.assertEqual(server._obs_guard_tick(now=1005 + server.OBS_FREEZE_SEC),
                         "frozen_killed")
        self.hung = False
        server._obs_guard_tick(now=1070)
        self.assertIn("StartStream", [r[0] for r in self.calls[-1]])

    def test_frozen_and_not_answering_is_closed(self):
        self.stream_status(None)        # WebSocket doesn't answer either
        server._obs_guard_tick(now=1000)
        self.hung = True
        server._obs_guard_tick(now=1005)
        self.assertEqual(server._obs_guard_tick(now=1005 + server.OBS_FREEZE_SEC),
                         "frozen_killed")

    def test_cant_tell_is_not_a_freeze(self):
        # Mac/Linux, or no OBS window found: obs_window_hung() is None, never acted on.
        server._obs_guard_tick(now=1000)
        self.hung = None
        for t in range(1005, 1300, 5):
            self.assertIsNone(server._obs_guard_tick(now=t))
        self.kill.assert_not_called()

    # ── From the code review ──

    def test_stands_aside_while_start_obs_runs(self):
        # Start OBS closes and reopens OBS on purpose on a first run; the guard must not
        # read that as "closed normally" (disarming, and switching updates back on).
        server._obs_guard_tick()
        saved = dict(server._checklist_job)
        self.addCleanup(server._checklist_job.update, saved)
        server._checklist_job["running"] = True
        self.running = False
        self.assertIsNone(server._obs_guard_tick())
        self.assertTrue(server._obs_guard["armed"])

    def test_stream_stopped_just_before_a_crash_stays_stopped(self):
        # The monitor's flag lags up to 15s; OBS itself is asked on every check.
        self.mon(reachable=True, streaming=True)
        server._obs_guard_tick()
        with mock.patch.object(server, "_obs_is_streaming", return_value=False):
            server._obs_guard_tick()          # operator pressed Stop Streaming
        self.crash()                          # monitor still says nothing fresher
        server._stream_mon["streaming"] = True
        server._stream_mon["reachable"] = False
        server._obs_guard_tick()
        self.assertNotIn("StartStream", [r[0] for r in self.calls[-1]])

    def test_no_process_check_for_an_obs_never_seen(self):
        self.running = False
        obs_prep.obs_running.reset_mock()
        for _ in range(5):
            server._obs_guard_tick()
        obs_prep.obs_running.assert_not_called()

    def test_freeze_check_reuses_obs_process_ids(self):
        server._obs_guard_tick(now=1000)
        for t in range(1005, 1050, 5):
            server._obs_guard_tick(now=t)
        self.assertEqual(obs_prep.obs_pids.call_count, 1)

    def test_health_reports_it(self):
        self.assertIn("obs_recovery", open(os.path.join(REPO, "server.py"),
                                           encoding="utf-8").read())
        status = server.obs_guard_status()
        for key in ("enabled", "watching", "gave_up", "last_event"):
            self.assertIn(key, status)


if __name__ == "__main__":
    unittest.main()
