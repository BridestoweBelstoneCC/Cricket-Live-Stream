"""quickstart.build_state: the merge that stops match-day runs wiping panel state."""
import configparser
import json
import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import quickstart

CONFIG = """
[Club]
name = Test CC
abbreviation = TEST
home_colour = #1a3a5c
playcricket_id = 111
motto = Up the Test

[API]
playcricket_key = key
anthropic_key =

[Scoring]
pcs_output_folder =

[OBS]
obs_password =
replay_folder =

[Stream]
youtube_title = LIVE: {home} vs {away}
max_overs = 40
"""

FIXTURE = {"away_team": "Fixture CC", "away_abbrev": "FIX", "competition": "League Div 1",
           "umpire1": "U One", "umpire2": "U Two", "scorer1": "S One",
           "match_id": "999", "away_club_id": "222"}


class TestBuildState(unittest.TestCase):
    def setUp(self):
        self.cfg = configparser.ConfigParser()
        self.cfg.read_string(CONFIG)
        fd, self.state_path = tempfile.mkstemp(suffix=".json")
        os.close(fd)

    def tearDown(self):
        os.unlink(self.state_path)

    def write_exist(self, d):
        with open(self.state_path, "w") as f:
            json.dump(d, f)

    def panel_state(self):
        """State as the control panel might have left it after a season of use."""
        return {
            "roster": {"21": "Peter Smith", "7": "James Smith"},
            "sponsor_name": "Acme Builders", "sponsor_id": "3",
            "away_colour": "#123456",
            "away_team": "Manually Entered CC",
            "replay_on_fifty": False, "max_clips": 200, "replay_duration": 25,
            "graphics_player_card": True,
            "network_test_mbps": 18.4, "network_test_at": 1751000000,
            "home_club_id": "29434",
            "weather_api_key": "wk-secret-123",
        }

    def test_panel_state_survives_rerun_without_fixture(self):
        self.write_exist(self.panel_state())
        st = quickstart.build_state(self.cfg, None, state_path=self.state_path)
        # Everything the panel owns must ride through
        self.assertEqual(st["roster"], {"21": "Peter Smith", "7": "James Smith"})
        self.assertEqual(st["sponsor_name"], "Acme Builders")
        self.assertEqual(st["sponsor_id"], "3")
        self.assertEqual(st["away_colour"], "#123456")
        self.assertEqual(st["network_test_mbps"], 18.4)
        self.assertEqual(st["home_club_id"], "111")   # config.ini owns the club's own ID
        # Toggle edits survive too
        self.assertFalse(st["replay_on_fifty"])
        self.assertEqual(st["max_clips"], 200)
        self.assertEqual(st["replay_duration"], 25)
        self.assertTrue(st["graphics_player_card"])
        # No fixture found → the manually entered opposition is kept
        self.assertEqual(st["away_team"], "Manually Entered CC")
        # Panel-set secret survives a re-run (a hard "" used to wipe it every match day)
        self.assertEqual(st["weather_api_key"], "wk-secret-123")

    def test_fixture_data_overrides_manual_entry(self):
        self.write_exist(self.panel_state())
        st = quickstart.build_state(self.cfg, dict(FIXTURE), state_path=self.state_path)
        self.assertEqual(st["away_team"], "Fixture CC")
        self.assertEqual(st["away_abbrev"], "FIX")
        self.assertEqual(st["away_club_id"], "222")
        self.assertEqual(st["competition_name"], "League Div 1")
        self.assertEqual(st["pc_match_id"], "999")
        # ...while panel-only state still survives
        self.assertEqual(st["roster"], {"21": "Peter Smith", "7": "James Smith"})
        self.assertEqual(st["sponsor_name"], "Acme Builders")

    def test_safety_defaults_always_forced(self):
        # However the last session left these, a fresh launch must be stream-safe
        self.write_exist({"demo_mode": True, "use_widget": True, "match_url": "http://x/1"})
        st = quickstart.build_state(self.cfg, None, state_path=self.state_path)
        self.assertFalse(st["demo_mode"])
        self.assertFalse(st["use_widget"])
        self.assertEqual(st["match_url"], "")      # a pinned match is per-day
        self.assertEqual(st["max_overs"], 40)      # config.ini wins for its own fields
        self.assertEqual(st["home_team"], "Test CC")

    def test_missing_or_corrupt_state_file_starts_clean(self):
        os.unlink(self.state_path)
        st = quickstart.build_state(self.cfg, None, state_path=self.state_path)
        self.assertEqual(st["away_team"], "Opposition CC")
        open(self.state_path, "w").close()         # recreate for tearDown
        with open(self.state_path, "w") as f:
            f.write("{corrupt json")
        st = quickstart.build_state(self.cfg, None, state_path=self.state_path)
        self.assertEqual(st["home_team"], "Test CC")
        # a state file holding a non-dict must not blow up either
        with open(self.state_path, "w") as f:
            f.write("[1, 2, 3]")
        st = quickstart.build_state(self.cfg, None, state_path=self.state_path)
        self.assertEqual(st["home_team"], "Test CC")

    def test_result_is_json_serialisable(self):
        self.write_exist(self.panel_state())
        st = quickstart.build_state(self.cfg, dict(FIXTURE), state_path=self.state_path)
        json.dumps(st)   # what main() does with it — must never raise


class TestFetchTodaysMatchParsing(unittest.TestCase):
    def test_no_api_key(self):
        match, err = quickstart.fetch_todays_match("", "111")
        self.assertIsNone(match)
        self.assertIn("API key", err)


class FakeProc:
    """Stand-in for subprocess.Popen. wait() with no args simulates the server process
    having exited (a crash, in the loop's terms) unless configured to raise
    KeyboardInterrupt instead, simulating Ctrl+C. wait(timeout=...) -- the cleanup call --
    always just returns, matching a process that terminates cleanly."""

    def __init__(self, raise_on_wait=None):
        self.raise_on_wait = raise_on_wait
        self.terminated = False
        self.killed = False

    def wait(self, timeout=None):
        if timeout is None and self.raise_on_wait:
            raise self.raise_on_wait
        return 0

    def terminate(self):
        self.terminated = True

    def kill(self):
        self.killed = True


class TestRunServerWithRestarts(unittest.TestCase):
    """quickstart.run_server_with_restarts -- extracted 2026-08-21 specifically so this
    class of test could exist without spawning real subprocesses. Found live the night
    before a match: killing server.py used to take the whole launcher down with it."""

    def setUp(self):
        self.report_patcher = mock.patch("quickstart.offer_match_report")
        self.mock_report = self.report_patcher.start()
        self.addCleanup(self.report_patcher.stop)
        self.sleeps = []

    def fake_sleep(self, secs):
        self.sleeps.append(secs)

    def test_restarts_up_to_the_cap_then_gives_up(self):
        launched = []

        def launch():
            p = FakeProc()
            launched.append(p)
            return p

        first = FakeProc()
        with self.assertRaises(SystemExit) as ctx:
            quickstart.run_server_with_restarts(first, launch, max_restarts=3,
                                                backoff=0, sleep=self.fake_sleep)
        self.assertEqual(ctx.exception.code, 1)
        self.assertEqual(len(launched), 3)             # relaunched exactly up to the cap
        self.assertEqual(len(self.sleeps), 3)           # backed off before each relaunch
        self.mock_report.assert_not_called()            # no Ctrl+C happened here

    def test_ctrl_c_on_the_very_first_wait_shuts_down_cleanly_with_no_restart(self):
        launched = []
        proc = FakeProc(raise_on_wait=KeyboardInterrupt())
        quickstart.run_server_with_restarts(proc, lambda: launched.append(1) or FakeProc(),
                                            token="tok123", max_restarts=3,
                                            backoff=0, sleep=self.fake_sleep)
        self.assertEqual(launched, [])                  # never treated as a crash
        self.assertTrue(proc.terminated)
        self.mock_report.assert_called_once_with("tok123")

    def test_ctrl_c_after_a_restart_terminates_the_new_process_not_the_original(self):
        original = FakeProc()                            # "crashes" once
        restarted = FakeProc(raise_on_wait=KeyboardInterrupt())

        def launch():
            return restarted

        quickstart.run_server_with_restarts(original, launch, max_restarts=3,
                                            backoff=0, sleep=self.fake_sleep)
        self.assertFalse(original.terminated)             # the dead one, nothing to terminate
        self.assertTrue(restarted.terminated)              # the live one gets shut down


if __name__ == "__main__":
    unittest.main()
