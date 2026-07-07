"""Pure parsing logic: ball tokens, PCS Pro JSON, widget JSON, names, IDs."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server


class TestClassifyBall(unittest.TestCase):
    def c(self, tok):
        return server._classify_ball(tok)

    def test_dots(self):
        for tok in ("", ".", "0", None, "xyz"):
            r = self.c(tok)
            self.assertEqual(r["outcome"], "dot", tok)
            self.assertEqual(r["runs"], 0, tok)
            self.assertTrue(r["legal"], tok)

    def test_runs(self):
        for tok, runs in (("1", 1), ("2", 2), ("3", 3), ("4", 4), ("6", 6)):
            r = self.c(tok)
            self.assertEqual(r["runs"], runs)
            self.assertEqual(r["outcome"], str(runs))
            self.assertTrue(r["legal"])

    def test_wicket_is_capital_w_only(self):
        r = self.c("W")
        self.assertTrue(r["wicket"])
        self.assertTrue(r["legal"])
        self.assertEqual(r["runs"], 0)
        # lowercase w is a wide, never a wicket (NV Play codes are case-sensitive)
        r = self.c("w")
        self.assertFalse(r["wicket"])
        self.assertEqual(r["extra"], "wide")
        self.assertEqual(r["runs"], 1)
        self.assertFalse(r["legal"])

    def test_wide_with_runs(self):
        r = self.c("w+4")
        self.assertEqual(r["extra"], "wide")
        self.assertEqual(r["runs"], 5)     # wide + 4 ran/boundary
        self.assertFalse(r["legal"])

    def test_noball(self):
        self.assertEqual(self.c("1nb")["runs"], 2)       # nb + 1 off the bat... NV codes: prefix is bat runs
        self.assertEqual(self.c("1nb")["extra"], "noball")
        self.assertFalse(self.c("1nb")["legal"])
        self.assertEqual(self.c("2nb+4")["runs"], 7)     # 1 (nb) + 2 (pre) + 4 (post)
        self.assertEqual(self.c("nb")["runs"], 1)

    def test_byes_and_legbyes(self):
        r = self.c("4lb")
        self.assertEqual((r["extra"], r["runs"], r["legal"]), ("legbye", 4, True))
        r = self.c("2b")
        self.assertEqual((r["extra"], r["runs"], r["legal"]), ("bye", 2, True))
        # 'nb' must not be swallowed by the trailing-'b' bye rule
        self.assertEqual(self.c("1nb")["extra"], "noball")


class TestParseTicker(unittest.TestCase):
    def test_split_and_placeholder(self):
        self.assertEqual(server._parse_ticker(""), [])
        self.assertEqual(server._parse_ticker("{{LastBall}}"), [])
        balls = server._parse_ticker("1 4 . W w+2")
        self.assertEqual(len(balls), 5)
        self.assertEqual([b["runs"] for b in balls], [1, 4, 0, 0, 3])
        self.assertTrue(balls[3]["wicket"])

    def test_middle_dot_separator(self):
        balls = server._parse_ticker("1·4·6")
        self.assertEqual([b["runs"] for b in balls], [1, 4, 6])


class TestParsePcsJson(unittest.TestCase):
    def setUp(self):
        self._latch = server._innings_latch
        server._innings_latch = 1

    def tearDown(self):
        server._innings_latch = self._latch

    def base(self, **over):
        d = {"batting_team": "Home CC", "bowling_team": "Opposition CC",
             "runs": "100", "wickets": "2", "overs": "20.0",
             "batter1_name": "SMITH", "batter1_runs": "40", "batter1_balls": "50",
             "batter2_name": "JONES", "batter2_runs": "30", "batter2_balls": "35",
             "bowler_name": "HARRISON", "bowler_overs": "4", "bowler_runs": "20",
             "bowler_wickets": "1", "last_ball": "1 4 ."}
        d.update(over)
        return d

    def test_basic_fields(self):
        st = server.parse_pcs_json(self.base())
        self.assertEqual(st["score"], 100)
        self.assertEqual(st["wickets"], 2)
        self.assertEqual(st["overs"], 20.0)
        self.assertEqual(st["batter1"]["name"], "SMITH")
        self.assertEqual(st["innings"], 1)

    def test_placeholder_and_blank_fields_fall_back(self):
        st = server.parse_pcs_json(self.base(batter1_name="{{Batter1Name}}", batter2_name=""))
        self.assertEqual(st["batter1"]["name"], "—")
        self.assertEqual(st["batter2"]["name"], "—")

    def test_batting_team_fallbacks(self):
        # Blank falls back to the "Batting" placeholder BY DESIGN (g()'s blank fallback —
        # NV Play renders the template with empty fields before the match is configured)...
        st = server.parse_pcs_json(self.base(batting_team=""))
        self.assertEqual(st["battingTeamName"], "Batting")
        # ...and only an explicit em dash means "no state at all"
        self.assertIsNone(server.parse_pcs_json(self.base(batting_team="—")))

    def test_striker_detection(self):
        st = server.parse_pcs_json(self.base(batter1_strike="False", batter2_strike="True"))
        self.assertFalse(st["batter1"]["onStrike"])
        self.assertTrue(st["batter2"]["onStrike"])
        # unknown → batter1 defaults to strike
        st = server.parse_pcs_json(self.base())
        self.assertTrue(st["batter1"]["onStrike"])

    def test_innings_latch_holds_through_winning_runs(self):
        # runs_required positive → 2nd innings, latched
        st = server.parse_pcs_json(self.base(runs_required="50"))
        self.assertEqual(st["innings"], 2)
        # target reached: runs_required drops to 0 but the latch must hold
        st = server.parse_pcs_json(self.base(runs_required="0"))
        self.assertEqual(st["innings"], 2)
        # a genuinely fresh innings (near-empty card) resets the latch
        st = server.parse_pcs_json(self.base(runs="0", wickets="0", overs="0.0",
                                             runs_required="0"))
        self.assertEqual(st["innings"], 1)

    def test_runs_required_falls_back_to_target(self):
        # No runs_required field mapped in the template → compute from target
        st = server.parse_pcs_json(self.base(target="180"))
        self.assertEqual(st["runsRequired"], 80)
        # Template's own field wins when present
        st = server.parse_pcs_json(self.base(target="180", runs_required="79"))
        self.assertEqual(st["runsRequired"], 79)

    def test_full_card_parsing(self):
        st = server.parse_pcs_json(self.base(
            card_b1_name="A Opener", card_b1_runs="12", card_b1_balls="20", card_b1_out="b Jones",
            card_b2_name="{{unset}}",
            card_w1_name="B Bowler", card_w1_o="8.0", card_w1_m="1", card_w1_r="30", card_w1_w="2"))
        self.assertEqual(len(st["card"]["batters"]), 1)
        self.assertEqual(st["card"]["batters"][0]["name"], "A Opener")
        self.assertEqual(len(st["card"]["bowlers"]), 1)
        self.assertEqual(st["card"]["bowlers"][0]["w"], 2)


class TestParseWidgetJson(unittest.TestCase):
    def widget(self, home_score, away_score, batted_first="", home_team_id="77"):
        return {"matches": [{
            "match_id": "123",
            "home_club_name": "Home", "home_team_name": "1st XI",
            "away_club_name": "Away", "away_team_name": "1st XI",
            "home_team_score": home_score, "away_team_score": away_score,
            "batted_first": batted_first, "home_team_id": home_team_id,
        }]}

    def test_first_innings_home_batting(self):
        st, pc_id = server.parse_widget_json_score(
            self.widget("120-4 (25.0)", "Yet to bat"), "Home", "Away")
        self.assertEqual(pc_id, "123")
        self.assertEqual(st["innings"], 1)
        self.assertEqual(st["score"], 120)
        self.assertEqual(st["wickets"], 4)
        self.assertEqual(st["overs"], 25.0)
        self.assertTrue(st["battingTeamName"].startswith("Home"))

    def test_second_innings_after_all_out(self):
        st, _ = server.parse_widget_json_score(
            self.widget("200-10 (45.0)", "50-1 (10.0)",
                        batted_first="77", home_team_id="77"), "Home", "Away")
        self.assertEqual(st["innings"], 2)
        self.assertTrue(st["battingTeamName"].startswith("Away"))
        self.assertEqual(st["score"], 50)

    def test_both_batted_but_first_not_all_out_defaults_innings_1(self):
        st, _ = server.parse_widget_json_score(
            self.widget("200-8 (45.0)", "50-1 (10.0)",
                        batted_first="77", home_team_id="77"), "Home", "Away")
        self.assertEqual(st["innings"], 1)

    def test_no_matches(self):
        st, pc_id = server.parse_widget_json_score({"matches": []}, "H", "A")
        self.assertIsNone(st)


class TestNamesAndKeys(unittest.TestCase):
    def test_norm_name_key(self):
        for raw in ("P SMITH", "p.smith", "P_Smith", "psmith"):
            self.assertEqual(server._norm_name_key(raw), "psmith")

    def test_name_keys_tiers(self):
        k = server._name_keys("K J JONES")
        self.assertEqual(k, {"full": "kjjones", "initsur": "kjones", "surname": "jones"})
        self.assertEqual(server._name_keys("Kevin Jones")["initsur"], "kjones")
        self.assertEqual(server._name_keys("WALKER"),
                         {"full": "walker", "initsur": "walker", "surname": "walker"})
        self.assertEqual(server._name_keys(""), {"full": "", "initsur": "", "surname": ""})

    def test_team_key_youth_and_ordinals(self):
        for name in ("U11", "u-13", "Under 15", "Under-9s Colts", "Junior Girls", "Kwik Cricket"):
            self.assertEqual(server._team_key(name), "youth", name)
        self.assertEqual(server._team_key("1st XI"), "1st")
        self.assertEqual(server._team_key("2nd XI"), "2nd")
        self.assertEqual(server._team_key("Third Team"), "3rd")
        self.assertEqual(server._team_key("Sunday Friendly"), "")

    def test_short_name(self):
        self.assertEqual(server._short_name("Peter Smith"), "P SMITH")
        self.assertEqual(server._short_name("Smith"), "SMITH")
        # youth form: first name + surname initial, only when a full first name exists
        self.assertEqual(server._short_name("Jack Smith", youth=True), "Jack S")
        self.assertEqual(server._short_name("J Smith", youth=True), "J SMITH")

    def test_resolve_player_roster_crosscheck(self):
        from unittest import mock
        roster_state = dict(server.DEFAULT_STATE, roster={"21": "Peter Smith"})
        with mock.patch.object(server, "load_state", return_value=roster_state):
            # number → roster hit, surname agrees → pinned to full name
            self.assertEqual(server.resolve_player("SMITH", "21"), ("Peter Smith", True))
            # surname mismatch (away player wearing 21) → fall back to scorebar name
            self.assertEqual(server.resolve_player("JONES", "21"), ("JONES", False))
            # no number → scorebar name
            self.assertEqual(server.resolve_player("SMITH", ""), ("SMITH", False))


class TestExtractPcMatchId(unittest.TestCase):
    def test_forms(self):
        self.assertEqual(server.extract_pc_match_id("1234567"), 1234567)
        self.assertEqual(server.extract_pc_match_id(
            "https://play-cricket.com/website/results/1234567"), 1234567)
        self.assertEqual(server.extract_pc_match_id(
            "https://x.com/scorecard/1234567?tab=batting"), 1234567)
        self.assertIsNone(server.extract_pc_match_id(""))
        self.assertIsNone(server.extract_pc_match_id("no digits here"))


class TestBitrateRecommendation(unittest.TestCase):
    def test_tiers(self):
        kbps, res, fps, _ = server._recommend_bitrate_and_resolution(1.0)   # 750 safe
        self.assertEqual((res, fps), ("720p", 30))
        self.assertGreaterEqual(kbps, 800)
        _, res, _, _ = server._recommend_bitrate_and_resolution(3.0)        # 2250 safe
        self.assertEqual(res, "720p")
        kbps, res, _, _ = server._recommend_bitrate_and_resolution(5.0)     # 3750 safe
        self.assertEqual(res, "1080p")
        kbps, res, _, _ = server._recommend_bitrate_and_resolution(50.0)
        self.assertEqual(res, "1080p")
        self.assertLessEqual(kbps, 6000)


class TestGroundCoords(unittest.TestCase):
    def test_fallback_prefers_state(self):
        from unittest import mock
        with mock.patch.object(server, "load_state", return_value={}):
            self.assertEqual(server._ground_coords(), (server.GROUND_LAT, server.GROUND_LON))
        with mock.patch.object(server, "load_state",
                               return_value={"weather_lat": "51.5", "weather_lon": "-0.1"}):
            self.assertEqual(server._ground_coords(), (51.5, -0.1))
        with mock.patch.object(server, "load_state",
                               return_value={"weather_lat": "junk", "weather_lon": "-0.1"}):
            self.assertEqual(server._ground_coords(), (server.GROUND_LAT, server.GROUND_LON))


if __name__ == "__main__":
    unittest.main()
