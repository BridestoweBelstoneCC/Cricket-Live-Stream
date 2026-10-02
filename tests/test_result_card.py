"""The post-match result card: facts (server.build_match_facts_from_pc and the live
generate_social_graphic_facts) and the renderer (result_card.py).

The PlayCricket cases are shaped on real 2026 scorecards that the first version got
wrong: a DLS win from the lower score, a conceded match with empty innings, and pairs
(softball) cricket where the side batting second wins by runs.
"""
import os
import sqlite3
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
from tests.test_http import HttpTestBase
from tests.test_match_report import frame, reset_live_tables

try:
    import PIL  # noqa: F401
    HAVE_PIL = True
except ImportError:
    HAVE_PIL = False      # stdlib-only CI: the renderer tests skip, the facts tests run


def _inn(team_id, name, runs, wkts, overs, bat=(), bowl=()):
    return {"team_batting_id": team_id, "team_batting_name": name, "runs": runs,
            "wickets": wkts, "overs": overs,
            "bat": [{"batsman_name": n, "how_out": h, "runs": r, "balls": b} for n, h, r, b in bat],
            "bowl": [{"bowler_name": n, "overs": o, "runs": r, "wickets": w} for n, o, r, w in bowl]}


def _match(innings, result, applied="", desc="", game_type="Standard"):
    return {"match_details": [{
        "home_team_id": "1", "home_club_name": "Home and District CC", "home_team_name": "1st XI",
        "home_club_id": "111", "away_team_id": "2", "away_club_name": "Rivals CC",
        "away_team_name": "1st XI", "away_club_id": "222", "result": result,
        "result_applied_to": applied, "result_description": desc, "game_type": game_type,
        "league_name": "Devon Cricket League", "competition_name": "A Division",
        "match_date": "25/07/2026", "ground_name": "The Ground", "innings": innings}]}


US_BAT = [("Pat Smith", "ct", "78", "96"), ("Al Jones", "not out", "20", "15"),
          ("Ed Dnb", "did not bat", "", "")]
US_BOWL = [("Sam Quick", "10", "25", "3"), ("Tom Slow", "8", "40", "1")]


class PlayCricketFactsTests(unittest.TestCase):
    def setUp(self):
        self.cfg = dict(server.DEFAULT_STATE, home_team="Home and District CC",
                        home_club_id="111", playcricket_api_key="k")
        p = mock.patch.object(server, "load_state", return_value=self.cfg)
        p.start()
        self.addCleanup(p.stop)

    def facts(self, md):
        with mock.patch.object(server, "_pc_get_json", return_value=md):
            return server.build_match_facts_from_pc("7303595")

    def test_win_by_runs_with_both_stars(self):
        f = self.facts(_match([_inn("1", "x", "231", "7", "40", bat=US_BAT),
                               _inn("2", "y", "185", "10", "36.2", bowl=US_BOWL)], "W", "1"))
        self.assertEqual((f["outcome"], f["margin"]), ("win", "by 46 runs"))
        us, them = f["sides"]
        self.assertTrue(us["won"] and us["ours"] and not them["won"])
        self.assertEqual((us["club"], us["club_id"], us["score"]), ("Home & District CC", "111", "231-7"))
        self.assertEqual((them["score"], them["overs"]), ("185", "All out · 36.2 overs"))
        self.assertEqual([(s["big"], s["small"], s["name"]) for s in f["stars"]],
                         [("78", "(96)", "P SMITH"), ("3-25", "10 ov", "S QUICK")])
        self.assertEqual(f["competition"], "Devon League · A Division")
        self.assertEqual(f["date"], "25 Jul 2026")

    def test_dls_win_from_the_lower_score(self):
        f = self.facts(_match([_inn("2", "y", "200", "5", "40"), _inn("1", "x", "150", "7", "36")],
                              "W", "1", desc="Home and District CC - 1st XI - Win on DLS/Run Rate"))
        self.assertEqual((f["outcome"], f["margin"]), ("win", "on DLS"))
        self.assertTrue(f["sides"][1]["won"])

    def test_loss_by_wickets(self):
        f = self.facts(_match([_inn("1", "x", "150", "10", "35"), _inn("2", "y", "151", "3", "30")],
                              "W", "2"))
        self.assertEqual((f["outcome"], f["margin"]), ("loss", "by 7 wickets"))

    def test_conceded_match_has_no_fake_scores(self):
        f = self.facts(_match([_inn("1", "x", "", "", ""), _inn("2", "y", "", "", "")], "CON",
                              desc="Rivals CC - 1st XI - Conceded"))
        self.assertEqual((f["outcome"], f["margin"]), ("win", "by concession"))
        self.assertEqual([s["score"] for s in f["sides"]], ["", ""])
        self.assertEqual(f["stars"], [])

    def test_pairs_cricket_is_won_by_runs_and_blank_how_out_still_batted(self):
        bat = [("Kim Mac", None, "26", "16")]
        f = self.facts(_match([_inn("2", "y", "66", "4", "16.0"), _inn("1", "x", "108", "5", "16.0", bat=bat)],
                              "W", "1", game_type="Pairs"))
        self.assertEqual((f["outcome"], f["margin"]), ("win", "by 42 runs"))
        self.assertEqual(f["stars"][0]["big"], "26")

    def test_tie(self):
        f = self.facts(_match([_inn("1", "x", "150", "6", "40"), _inn("2", "y", "150", "9", "40")], "T"))
        self.assertEqual(f["outcome"], "tie")
        self.assertFalse(any(s["won"] for s in f["sides"]))

    def test_caption_fields_still_present(self):
        f = self.facts(_match([_inn("1", "x", "231", "7", "40", bat=US_BAT),
                               _inn("2", "y", "185", "10", "36.2", bowl=US_BOWL)], "W", "1"))
        self.assertIn("WIN BY 46 RUNS", f["result"])
        self.assertEqual(f["team2_score"], "185 (All out · 36.2 overs)")
        self.assertEqual(f["performer1"], "P SMITH 78 (96)")


class LiveFactsTests(HttpTestBase):
    """The streamed match without a published scorecard: built from the scorer's own
    figures (log_live_figures)."""

    def setUp(self):
        reset_live_tables()
        server.update_state(lambda s: s.update(home_team="Home CC", away_team="Rivals CC",
                                               home_club_id="111", away_club_id="222",
                                               max_overs=50, match_url="", pc_match_id=""))

    def innings(self, n, team, score, wkts, overs, remaining=0, batter=("Smith", 0, 0),
                bowler=("Quick", "0", 0, 0)):
        server.log_live_figures(frame(innings=n, team=team, score=score, wickets=wkts,
                                      overs=overs, remaining=remaining, b1=batter,
                                      b2=("Other", 0, 0), bowler=bowler))

    def test_finished_chase_is_a_result_no_ai_involved(self):
        self.innings(1, "RIVALS CC 1ST", 120, 8, 20.0, bowler=("Our Bowler", "4", 20, 3))
        self.innings(2, "HOME CC 1ST", 121, 3, 15.0, batter=("Pat Smith", 64, 40))
        with mock.patch.dict(sys.modules, {"anthropic": mock.MagicMock()}) as mods:
            f = server.generate_social_graphic_facts()
            mods["anthropic"].Anthropic.assert_not_called()
        self.assertEqual((f["outcome"], f["margin"]), ("win", "by 7 wickets"))
        self.assertEqual([s["club_id"] for s in f["sides"]], ["222", "111"])
        self.assertEqual([(s["big"], s["name"]) for s in f["stars"]],
                         [("64*", "P SMITH"), ("3-20", "O BOWLER")])

    def test_defended_total_ends_on_the_scorers_overs_limit_not_the_panels(self):
        # A 40-over game with the panel left on 50: ballsRemaining says 40.
        self.innings(1, "HOME CC 1ST", 200, 6, 40.0)
        self.innings(2, "RIVALS CC 1ST", 100, 3, 20.0, remaining=120)
        self.innings(2, "RIVALS CC 1ST", 180, 7, 40.0, remaining=0)
        f = server.generate_social_graphic_facts()
        self.assertEqual((f["outcome"], f["margin"]), ("win", "by 20 runs"))

    def test_second_innings_still_going_claims_nothing(self):
        self.innings(1, "RIVALS CC 1ST", 120, 8, 20.0)
        self.innings(2, "HOME CC 1ST", 60, 3, 9.0, remaining=66)
        f = server.generate_social_graphic_facts()
        self.assertEqual((f["outcome"], f["margin"]), ("live", ""))

    def test_unrecognised_team_names_never_claim_a_win(self):
        self.innings(1, "SOME XI", 120, 8, 20.0)
        self.innings(2, "OTHER 1ST XI", 121, 3, 15.0)
        f = server.generate_social_graphic_facts()
        self.assertEqual(f["outcome"], "")
        self.assertEqual([s["club"] for s in f["sides"]], ["Some XI", "Other 1st XI"])
        self.assertTrue(f["sides"][1]["won"])

    def test_linked_match_mid_chase_on_playcricket_is_not_a_result(self):
        server.update_state(lambda s: s.update(pc_match_id="7303595", playcricket_api_key="k"))
        md = _match([_inn("2", "y", "220", "7", "40"), _inn("1", "x", "60", "2", "12")], "")
        md["match_details"][0]["no_of_overs"] = "40"
        self.innings(1, "RIVALS CC 1ST", 220, 7, 40.0)
        self.innings(2, "HOME CC 1ST", 61, 2, 12.1, remaining=167)
        with mock.patch.object(server, "_pc_get_json", return_value=md):
            f = server.generate_social_graphic_facts()
        self.assertEqual(f["outcome"], "live")
        self.assertEqual(f["sides"][1]["score"], "61-2")    # the live figures, one ball on

    def test_no_data_is_refused(self):
        self.assertFalse(server.generate_social_graphic_facts()["ok"])

    def test_fetch_todays_match_saves_the_opposition_club_not_the_team_label(self):
        fixture = {"away_team": "1st XI", "away_club": "Rivals CC", "away_club_id": "222",
                   "match_id": "99"}
        with mock.patch.object(server, "fetch_todays_match", return_value=fixture):
            self.get_json("/match/fetch")
        self.assertEqual(server.load_state()["away_team"], "Rivals CC")


class PlayCricketEdgeCaseTests(unittest.TestCase):
    def setUp(self):
        self.cfg = dict(server.DEFAULT_STATE, home_team="Home and District CC",
                        home_club_id="111", playcricket_api_key="k")
        p = mock.patch.object(server, "load_state", return_value=self.cfg)
        p.start()
        self.addCleanup(p.stop)

    def facts(self, md):
        with mock.patch.object(server, "_pc_get_json", return_value=md):
            return server.build_match_facts_from_pc("1")

    def test_no_result_yet_mid_chase_is_an_update(self):
        md = _match([_inn("2", "y", "220", "7", "40"), _inn("1", "x", "60", "2", "12")], "")
        md["match_details"][0]["no_of_overs"] = "40"
        f = self.facts(md)
        self.assertEqual((f["outcome"], f["margin"]), ("live", ""))
        self.assertFalse(any(s["won"] for s in f["sides"]))

    def test_no_result_yet_but_chase_complete(self):
        md = _match([_inn("2", "y", "220", "7", "40"), _inn("1", "x", "221", "2", "35")], "")
        self.assertEqual(self.facts(md)["outcome"], "win")

    def test_cant_tell_which_side_is_ours(self):
        self.cfg.update(home_team="Somewhere Else CC", home_club_id="")
        f = self.facts(_match([_inn("1", "x", "231", "7", "40"), _inn("2", "y", "185", "10", "36")],
                              "W", "1"))
        self.assertEqual((f["outcome"], f["margin"]), ("", ""))
        self.assertTrue(f["sides"][0]["won"])
        self.assertEqual(f["stars"], [])

    def test_conceded_with_no_innings_at_all(self):
        f = self.facts(_match([], "CON", desc="Rivals CC - 1st XI - Conceded"))
        self.assertEqual((f["outcome"], f["margin"]), ("win", "by concession"))
        self.assertEqual(sorted(s["club"] for s in f["sides"]), ["Home & District CC", "Rivals CC"])

    def test_pairs_cricket_twelve_wickets_is_not_all_out(self):
        f = self.facts(_match([_inn("2", "y", "66", "12", "16.0"), _inn("1", "x", "108", "5", "16.0")],
                              "W", "1", game_type="Pairs"))
        self.assertEqual((f["sides"][0]["score"], f["sides"][0]["overs"]), ("66-12", "16.0 overs"))

    def test_away_day_badges_come_from_the_fixture(self):
        f = self.facts(_match([_inn("2", "y", "150", "9", "40"), _inn("1", "x", "151", "4", "30")],
                              "W", "1"))
        self.assertEqual({s["club_id"] for s in f["sides"]}, {"111", "222"})


@unittest.skipUnless(HAVE_PIL, "Pillow not installed")
class RendererTests(unittest.TestCase):
    def setUp(self):
        import result_card
        self.rc = result_card
        self.tmp = tempfile.mkdtemp(prefix="card_test_")

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_highlight_reads_on_the_panel_for_any_club_colour(self):
        for hexc in ("#1a3a5c", "#000000", "#ffffff", "#ffd700", "#b52d6e", "#0b6623"):
            p = self.rc.palette(hexc)
            self.assertGreaterEqual(self.rc.contrast(p["hi"], p["bg"]), 4.5, hexc)
            self.assertGreaterEqual(self.rc.contrast(p["white"], p["bg"]), 7, hexc)

    def test_navy_stays_blue(self):
        r, g, b = self.rc.palette("#1a3a5c")["hi"]
        self.assertGreater(b, r + 60)      # not lifted to steel grey

    def test_renders_with_nothing_optional(self):
        from PIL import Image
        out = os.path.join(self.tmp, "c.png")
        self.rc.render({"outcome": "win", "margin": "by 1 run", "sides": [], "stars": []},
                       {"club_colour": "not a colour", "crest": "/nope.png",
                        "photo": "/nope.jpg", "sponsors": ["/nope.png"]}, out)
        with Image.open(out) as im:
            self.assertEqual(im.size, (1080, 1350))

    def test_long_names_and_unknown_badges(self):
        from PIL import Image
        side = {"club": "The Extraordinarily Long Named Cricket And Social Club Of Somewhere",
                "team": "Sunday Development XI", "score": "999-9", "overs": "All out · 49.5 overs",
                "club_id": "nope", "won": True}
        out = os.path.join(self.tmp, "c.png")
        self.rc.render({"outcome": "loss", "margin": "by 999 wickets", "sides": [side, dict(side, won=False)],
                        "stars": [{"label": "Top score", "big": "150*", "small": "(99)",
                                   "name": "A VERY-LONG-SURNAME-INDEED"}]},
                       {"club_colour": "#ffd700", "logos_dir": self.tmp}, out)
        with Image.open(out) as im:
            self.assertEqual(im.size, (1080, 1350))

    def test_fonts_are_bundled(self):
        for w in ("Black", "ExtraBold", "Bold", "SemiBold", "Medium"):
            self.assertTrue(os.path.exists(os.path.join(self.rc.FONT_DIR, f"BarlowCondensed-{w}.ttf")), w)
        self.assertTrue(os.path.exists(os.path.join(self.rc.FONT_DIR, "OFL.txt")))


if __name__ == "__main__":
    unittest.main()
