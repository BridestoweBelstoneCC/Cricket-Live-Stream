"""The live win predictor (win_predictor.py + server.win_prediction + the overlay panel)."""
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
import win_predictor as wp

MODEL = {"rpo": 5.0, "sd_rpo": 1.2, "matches": 40, "default": False}


class ResourceTests(unittest.TestCase):
    def test_matches_the_dls_shape(self):
        self.assertAlmostEqual(wp.resources(300, 0, 300), 1.0)
        # Published DLS reference points, to within a few percent
        for balls, wkts, dls in ((240, 0, 0.893), (150, 0, 0.682), (300, 5, 0.495)):
            self.assertAlmostEqual(wp.resources(balls, wkts, 300), dls, delta=0.03)

    def test_monotonic(self):
        self.assertGreater(wp.resources(200, 2, 300), wp.resources(100, 2, 300))
        self.assertGreater(wp.resources(200, 2, 300), wp.resources(200, 6, 300))
        self.assertEqual(wp.resources(0, 2, 300), 0.0)
        self.assertEqual(wp.resources(100, 10, 300), 0.0)


class PredictTests(unittest.TestCase):
    def test_chase_extremes(self):
        self.assertEqual(wp.predict(2, 200, 3, 30.0, 40, MODEL, target=200)["batting"], 100)
        self.assertEqual(wp.predict(2, 150, 10, 35.0, 40, MODEL, target=200)["batting"], 0)
        self.assertEqual(wp.predict(2, 150, 4, 40.0, 40, MODEL, target=200)["batting"], 0)

    def test_chase_is_never_certain_while_it_can_turn(self):
        easy = wp.predict(2, 190, 0, 30.0, 40, MODEL, target=200)
        self.assertEqual((easy["batting"], easy["bowling"]), (99, 1))
        self.assertEqual(easy["need"], 10)
        self.assertEqual(easy["balls_left"], 60)

    def test_more_wickets_down_means_less_likely(self):
        a = wp.predict(2, 100, 2, 20.0, 40, MODEL, target=200)["batting"]
        b = wp.predict(2, 100, 7, 20.0, 40, MODEL, target=200)["batting"]
        self.assertGreater(a, b)

    def test_first_innings_projection(self):
        par = wp.predict(1, 100, 2, 20.0, 40, MODEL)
        self.assertIn("projected", par)
        self.assertGreater(par["projected"], 100)
        fast = wp.predict(1, 160, 2, 20.0, 40, MODEL)
        self.assertGreater(fast["batting"], par["batting"])

    def test_no_overs_limit_no_prediction(self):
        self.assertIsNone(wp.predict(1, 10, 0, 2.0, None, MODEL))


class LeagueDataTests(unittest.TestCase):
    def test_model_falls_back_with_little_data(self):
        self.assertTrue(wp.league_model([])["default"])
        hist = [{"overs_limit": 40, "i1_runs": 200 + i} for i in range(8)]
        m = wp.league_model(hist)
        self.assertFalse(m["default"])
        self.assertAlmostEqual(m["rpo"], 5.0875, places=3)

    def match(self, result, applied, game_type="Standard", runs=("180", "150"), overs="40"):
        return {"match_details": [{"no_of_overs": overs, "result": result,
                                   "result_applied_to": applied, "game_type": game_type,
                                   "innings": [{"team_batting_id": "1", "runs": runs[0], "wickets": "6"},
                                               {"team_batting_id": "2", "runs": runs[1], "wickets": "10"}]}]}

    def test_innings_summary(self):
        s = wp.innings_summary(self.match("W", "1"))
        self.assertEqual((s["overs_limit"], s["i1_runs"], s["chase_won"]), (40, 180, False))
        self.assertTrue(wp.innings_summary(self.match("W", "2"))["chase_won"])
        self.assertTrue(wp.innings_summary(self.match("L", "1"))["chase_won"])   # 1 lost
        self.assertIsNone(wp.innings_summary(self.match("W", "1", game_type="Pairs")))
        self.assertIsNone(wp.innings_summary(self.match("W", "1", overs="")))

    def test_backtest(self):
        hist = [{"overs_limit": 40, "i1_runs": r, "chase_won": r < 200} for r in (150, 160, 170, 230, 240, 250)]
        bt = wp.backtest(hist)
        self.assertEqual(bt["chases"], 6)
        self.assertEqual(bt["favourite_won_pct"], 100)
        self.assertLess(bt["brier"], 0.25)


class ServerTests(unittest.TestCase):
    def setUp(self):
        saved = dict(server._season_stats)
        self.addCleanup(server._season_stats.update, saved)
        hist = ([{"overs_limit": 40, "i1_runs": 200 + i, "chase_won": True} for i in range(12)]
                + [{"overs_limit": 20, "i1_runs": 150 + i, "chase_won": True} for i in range(12)])
        server._season_stats["innings_history"] = hist

    def state(self, **kw):
        st = {"innings": 2, "score": 100, "wickets": 3, "overs": 20.0, "targetRuns": 201,
              "ballsRemaining": 120, "runsRequired": 101}
        st.update(kw)
        return st

    def test_uses_the_matchs_own_format(self):
        w = server.win_prediction(self.state(), {"max_overs": 50})
        self.assertEqual(w["model_matches"], 12)            # the 40-over games only
        self.assertEqual(w["need"], 101)

    def test_target_from_runs_required_when_not_mapped(self):
        w = server.win_prediction(self.state(targetRuns=0), {})
        self.assertEqual(w["need"], 101)

    def test_history_survives_a_restart_via_the_cache_file(self):
        import json as _json, tempfile
        server._season_stats["innings_history"] = []        # as after a restart
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d, True)
        cache = os.path.join(d, "season_stats_cache.json")
        with open(cache, "w") as f:
            _json.dump({"innings_history": [{"overs_limit": 40, "i1_runs": 210}] * 11}, f)
        with mock.patch.object(server, "SEASON_STATS_CACHE_FILE", cache):
            server._win_history_cache.update(mtime=None, rows=[])
            w = server.win_prediction(self.state(), {})
        self.assertEqual(w["model_matches"], 11)

    def test_switched_off_or_pre_match(self):
        self.assertIsNone(server.win_prediction(self.state(), {"graphics_win_predictor": False}))
        self.assertIsNone(server.win_prediction(self.state(score=0, wickets=0, overs=0), {}))


class OverlayTests(unittest.TestCase):
    def test_panel_is_in_the_over_sequence(self):
        with open(os.path.join(os.path.dirname(os.path.dirname(__file__)), "overlay.html"),
                  encoding="utf-8") as f:
            html = f.read()
        self.assertIn('id="winpred-display"', html)
        self.assertIn("if (fillWinPredictor())                queueGraphic('winpred-display'", html)
        self.assertIn("cfg.graphics_win_predictor === false", html)
        # It runs inside showOverSummary, which has no `state` in scope: reading one threw a
        # ReferenceError there and skipped the rest of the over sequence.
        body = html[html.index("function _fillWinPredictor()"):html.index("// ── Camera auto-cut")]
        self.assertNotRegex(body, r"state")
        self.assertIn("try { return _fillWinPredictor(); }", html)


if __name__ == "__main__":
    unittest.main()
