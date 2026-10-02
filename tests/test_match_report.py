"""Fixes from the 2026-10-02 rehearsal: the match report must survive a server restart
(and never be written from nothing), panel cooldowns must not lock out the wrong button,
and reconcile must explain an unlinked match instead of showing a raw 404."""
import os
import sqlite3
import sys
import time
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
from tests.test_http import HttpTestBase

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def frame(innings=1, score=0, wickets=0, overs=0.0, b1=("Smith", 0, 0), b2=("Jones", 0, 0),
          bowler=("Quick", "0", 0, 0), team="Home CC", remaining=0):
    """An NV Play-shaped /live state, as log_live_figures sees it."""
    return {"innings": innings, "score": score, "wickets": wickets, "overs": overs,
            "battingTeamName": team, "ballsRemaining": remaining,
            "batter1": {"name": b1[0], "runs": b1[1], "balls": b1[2]},
            "batter2": {"name": b2[0], "runs": b2[1], "balls": b2[2]},
            "bowler": {"name": bowler[0], "overs": bowler[1], "runs": bowler[2],
                       "wickets": bowler[3]}}


def reset_live_tables():
    server.match_log_reset()                     # as after a restart: memory log empty
    server._live_prev.clear()
    server._live_prev["key"] = None
    with sqlite3.connect(server._db_path()) as c:
        for t in ("balls", "live_innings", "live_batting", "live_bowling", "live_fow"):
            c.execute(f"DELETE FROM {t}")


class MatchSummaryTests(HttpTestBase):
    def setUp(self):
        reset_live_tables()
        server._rate_limit_ts.clear()

    def test_no_data_anywhere_is_refused(self):
        summary, has_data = server.build_match_summary(server.load_state())
        self.assertFalse(has_data)

    def test_restart_keeps_the_scorers_own_figures(self):
        server.log_live_figures(frame(score=30, overs=5.0, b1=("Smith", 24, 20), b2=("Jones", 4, 10)))
        server.log_live_figures(frame(score=31, wickets=1, overs=5.1,
                                      b1=("Brown", 0, 0), b2=("Jones", 5, 11)))
        server.match_log_reset()                 # the restart
        summary, has_data = server.build_match_summary(server.load_state())
        self.assertTrue(has_data)
        self.assertIn("Innings 1: Home CC 31-1 (5.1 overs)", summary)
        self.assertIn("Smith 24 (20 balls)", summary)        # out: no asterisk
        self.assertIn("Jones 5* (11 balls)", summary)
        self.assertIn("Smith  at 31-1", summary)             # the batter who left the pair

    def test_pre_match_and_blank_frames_are_not_an_innings(self):
        server.log_live_figures(frame())                     # 0-0, 0 overs
        server.match_log_snapshot({"innings": 1, "battingTeamName": "Home CC",
                                   "score": 0, "wickets": 0, "overs": 0.0})
        self.assertFalse(server.build_match_summary(server.load_state())[1])

    def test_other_matches_in_the_db_are_ignored(self):
        with mock.patch.object(server, "current_match_id", return_value="some_other_match"):
            server.log_live_figures(frame(score=4, overs=0.1))
        self.assertFalse(server.build_match_summary(server.load_state())[1])

    def test_memory_fills_an_innings_the_db_lacks(self):
        server.match_log_snapshot({"innings": 1, "battingTeamName": "Home CC",
                                   "score": 50, "wickets": 2, "overs": 8.3})
        summary, _ = server.build_match_summary(server.load_state())
        self.assertIn("Home CC 50-2 (8.3 overs)", summary)

    def test_bowler_rotated_off_gets_the_over_they_finished(self):
        server.log_live_figures(frame(score=10, wickets=1, overs=3.5, bowler=("Quick", "3.5", 10, 1)))
        # Over-completing write: the bowler has already rotated (CLAUDE.md).
        server.log_live_figures(frame(score=14, wickets=2, overs=4.0, bowler=("Slow", "0", 0, 0)))
        bowlers = {b["name"]: b for b in server.match_facts_from_db(server.current_match_id())["bowlers"]["1"]}
        self.assertEqual((bowlers["Quick"]["o"], bowlers["Quick"]["r"], bowlers["Quick"]["w"]),
                         ("4", 14, 2))

    def test_report_endpoint_refuses_without_calling_the_model(self):
        server.update_state(lambda s: s.update(anthropic_api_key="sk-test"))
        fake = mock.MagicMock()
        with mock.patch.dict(sys.modules, {"anthropic": fake}):
            status, d = self.get_json("/report/generate")
        self.assertFalse(d["ok"])
        self.assertEqual(d["error"], server.NO_MATCH_DATA_ERROR)
        fake.Anthropic.assert_not_called()

    def test_report_and_social_post_have_separate_cooldowns(self):
        self.assertEqual(self.get_json("/report/generate")[0], 200)
        self.assertEqual(self.get_json("/report/generate?type=social")[0], 200)
        self.assertEqual(self.get_json("/report/generate")[0], 429)

    def test_reading_the_cached_upload_test_does_not_start_the_cooldown(self):
        server.update_state(lambda s: s.update(network_test_mbps=10.0,
                                               network_test_at=time.time()))
        with mock.patch.object(server, "_measure_upload_mbps", return_value=12.0) as m:
            self.assertEqual(self.get_json("/obs/stream_check")[0], 200)    # panel load
            self.assertEqual(self.get_json("/obs/stream_check")[0], 200)    # and again
            self.assertEqual(self.get_json("/obs/stream_check?force=1")[0], 200)  # Check now
            self.assertEqual(m.call_count, 1)
            self.assertEqual(self.get_json("/obs/stream_check?force=1")[0], 429)

    def test_reconcile_explains_an_unlinked_match(self):
        server.update_state(lambda s: s.update(playcricket_api_key="k"))
        with mock.patch.object(server, "_pc_get_json") as fetch:
            d = server.reconcile_match("2026-10-02_Home_v_Away")
        fetch.assert_not_called()
        self.assertIn("isn't linked to a PlayCricket fixture", d["error"])


class ManualPlayerCardTests(unittest.TestCase):
    def test_panel_button_ignores_the_automatic_card_toggle(self):
        # The toggle is for automatic new-batter cards; the button used to report
        # "showing" while the overlay silently returned.
        with open(os.path.join(ROOT, "overlay.html"), encoding="utf-8") as f:
            src = f.read()
        self.assertIn("showPlayerCardSplit(prev.batter1, prev.batter2, true)", src)
        self.assertIn("showPlayerCard(prev.batter1, true)", src)
        self.assertIn("if (!cfg.graphics_player_card && !manual) return;", src)


if __name__ == "__main__":
    unittest.main()
