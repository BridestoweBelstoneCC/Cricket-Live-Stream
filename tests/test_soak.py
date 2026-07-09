"""Full-match soak: drive a COMPLETE simulated match through the real HTTP server —
every frame written to a fake PCS folder and polled via /live like the overlay would,
with panel-style /live/view and /state noise mixed in — then reconcile the ball-by-ball
database against the engine's own book, ball for ball, over for over, both innings.

This is the integration-drift catcher: the class of bug where every unit passes but the
pieces disagree (it's exactly the test that would have caught the DB losing the
over-completing delivery of every over)."""
import json
import os
import shutil
import sqlite3
import sys
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import server
import simulate_match as sim
from test_http import HttpTestBase   # noqa: E402


class TestFullMatchSoak(HttpTestBase):
    def setUp(self):
        server._last_good_state = None
        st = dict(server.DEFAULT_STATE)
        self.pcs_dir = os.path.join(self.tmp, "soak_pcs")
        shutil.rmtree(self.pcs_dir, ignore_errors=True)
        os.makedirs(self.pcs_dir, exist_ok=True)
        st["pcs_output_folder"] = self.pcs_dir
        st["use_widget"] = False
        st["demo_mode"] = False
        server.save_state(st)
        server._pcs_last_mtime = 0
        server._pcs_last_state = None
        server._innings_latch = 1
        server._prev_state.update({"score": None, "wickets": None, "overs": None})
        server._event_buffer.clear()
        server._ball_log_prev.update({"mid": None, "innings": None, "over": None,
                                      "score": 0, "wickets": 0, "count": 0})
        server.match_log_reset()
        # start from an empty ball DB so the reconciliation is exact
        with server._db_lock, server._db() as c:
            c.execute("DELETE FROM balls")

    def test_whole_match_reconciles_against_the_engine(self):
        match = sim.MatchSimulator("full", max_overs=5, seed=21)
        path = os.path.join(self.pcs_dir, "nvplay-scoreboard1.xml")
        t = time.time() - 4000
        n = 0
        for label, frame in match.frames():
            n += 1
            with open(path, "w", encoding="utf-8") as f:
                json.dump(frame, f)
            os.utime(path, (t + n, t + n))
            status, body = self.get_json("/live")           # the overlay's poll
            self.assertEqual(status, 200)
            self.assertEqual(body["source"], "pcs")
            if n % 7 == 0:                                   # panel noise, like match day
                self.get_json("/live/view")
                self.get_json("/state")
        self.assertGreater(n, 40, "sanity: a whole match should be many frames")

        inn1, inn2 = match.innings
        self.assertTrue(inn1.complete and inn2.complete)

        with sqlite3.connect(server._db_path()) as c:
            for innings_no, inn in ((1, inn1), (2, inn2)):
                runs, wkts, legal = c.execute(
                    "SELECT COALESCE(SUM(runs),0), COALESCE(SUM(is_wicket),0), "
                    "COALESCE(SUM(legal),0) FROM balls WHERE innings=?",
                    (innings_no,)).fetchone()
                self.assertEqual(runs, inn.total,
                                 f"innings {innings_no}: DB runs != engine total")
                self.assertEqual(wkts, inn.wkts,
                                 f"innings {innings_no}: DB wickets != engine wickets")
                self.assertEqual(legal, inn.legal_balls,
                                 f"innings {innings_no}: DB legal balls != engine")
                # every COMPLETED over must reconcile individually
                for over_no, over_runs in enumerate(inn.over_history):
                    got = c.execute(
                        "SELECT COALESCE(SUM(runs),0) FROM balls WHERE innings=? AND over=?",
                        (innings_no, over_no)).fetchone()[0]
                    self.assertEqual(got, over_runs,
                                     f"innings {innings_no} over {over_no + 1} runs")

        # The match log's fall-of-wickets must have seen every wicket in both innings
        self.assertEqual(len(server._match_log["fall_of_wickets"]),
                         inn1.wkts + inn2.wkts)

        # ...and the innings latch delivered the right innings numbers throughout
        innings_seen = {r[0] for r in sqlite3.connect(server._db_path()).execute(
            "SELECT DISTINCT innings FROM balls")}
        self.assertEqual(innings_seen, {1, 2})


if __name__ == "__main__":
    unittest.main()
