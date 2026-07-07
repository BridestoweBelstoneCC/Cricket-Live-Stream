"""Season-stats aggregation from PlayCricket scorecards — the pure logic, no network."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server


def match(innings):
    return {"match_details": [{"innings": innings}]}


def bat(name, runs, how_out="ct Someone", balls="30"):
    return {"batsman_name": name, "how_out": how_out, "runs": str(runs),
            "balls": balls, "fours": "0", "sixes": "0"}


HOME = "Home CC 1st XI"
AWAY = "Rival CC 1st XI"


class TestAggregateSeasonBat(unittest.TestCase):
    def sample(self):
        return [
            match([
                {"team_batting_name": HOME, "bat": [
                    bat("Peter Smith", 30),
                    bat("Karl Jones", 50, how_out="not out"),
                    bat("A N Other", "", how_out="did not bat"),
                ]},
                {"team_batting_name": AWAY, "bat": [bat("Bob Rival", 10)],
                 "bowl": [{"bowler_name": "Peter Smith", "wickets": "3", "runs": "20",
                           "overs": "8", "maidens": "1"}]},
            ]),
            match([
                {"team_batting_name": HOME, "bat": [
                    bat("Peter Smith", 60, how_out="b Bowler"),
                    bat("Karl Jones", 10),
                ]},
                {"team_batting_name": AWAY, "bat": [bat("Bob Rival", 99)],
                 "bowl": [{"bowler_name": "Peter Smith", "wickets": "1", "runs": "35",
                           "overs": "8", "maidens": "0"}]},
            ]),
        ]

    def test_averages_and_not_outs(self):
        lk = server._aggregate_season_bat(self.sample(), keep_clubs=["home"])
        smith = lk["petersmith"]
        self.assertEqual(smith["inn"], "2")
        self.assertEqual(smith["runs"], 90)
        self.assertEqual(smith["avg"], "45.00")
        self.assertEqual(smith["hs"], "60")
        jones = lk["karljones"]
        # 60 runs, 2 innings, 1 not out → average over 1 dismissal
        self.assertEqual(jones["avg"], "60.00")
        self.assertEqual(jones["hs"], "50*")           # not-out high score keeps the star
        # "did not bat" is not an innings
        self.assertNotIn("another", lk)

    def test_keep_clubs_filters_opposition(self):
        lk = server._aggregate_season_bat(self.sample(), keep_clubs=["home"])
        self.assertNotIn("bobrival", lk)
        lk_all = server._aggregate_season_bat(self.sample())
        self.assertIn("bobrival", lk_all)

    def test_surname_key_only_when_unique(self):
        lk = server._aggregate_season_bat(self.sample(), keep_clubs=["home"])
        self.assertIn("smith", lk)                     # only one Smith → surname key exists
        # Add a second Smith → surname-only key must be suppressed (brother safety)
        data = self.sample()
        data.append(match([{"team_batting_name": HOME,
                            "bat": [bat("James Smith", 5)]}]))
        lk2 = server._aggregate_season_bat(data, keep_clubs=["home"])
        self.assertNotIn("smith", lk2)
        self.assertIn("psmith", lk2)                   # initial+surname tiers stay distinct
        self.assertIn("jsmith", lk2)

    def test_initial_surname_key_matches_scorebar_short_names(self):
        lk = server._aggregate_season_bat(self.sample(), keep_clubs=["home"])
        self.assertIs(lk["psmith"], lk["petersmith"])  # same record, both spellings

    def test_duplicate_account_keeps_most_innings(self):
        # Same person under two PlayCricket accounts: "Peter Smith" (2 inns) and
        # "P Smith" (1 inn) — the shared 'psmith' key must keep the regular account.
        data = self.sample()
        data.append(match([{"team_batting_name": HOME, "bat": [bat("P Smith", 7)]}]))
        lk = server._aggregate_season_bat(data, keep_clubs=["home"])
        self.assertEqual(lk["psmith"]["inn"], "2")
        self.assertEqual(lk["psmith"]["name"], "Peter Smith")


class TestSeasonTopPerformers(unittest.TestCase):
    def test_top_bowler_credited_to_fielding_side(self):
        data = TestAggregateSeasonBat().sample()
        top = server._season_top_bowler(data, "home")
        # Peter Smith's figures live on the AWAY batting innings' bowl card
        self.assertEqual(top["name"], "Peter Smith")
        self.assertEqual(top["wkts"], 4)
        self.assertEqual(top["runs"], 55)

    def test_top_bowler_none_without_club(self):
        self.assertIsNone(server._season_top_bowler([], ""))

    def test_top_scorer_dedupes_shared_records(self):
        lk = server._aggregate_season_bat(TestAggregateSeasonBat().sample(),
                                          keep_clubs=["home"])
        top = server._season_top_scorer(lk)
        self.assertEqual(top["name"], "Peter Smith")
        self.assertEqual(top["runs"], 90)


class TestLookupSeasonStats(unittest.TestCase):
    def setUp(self):
        self._saved = server._season_stats
        lk = server._aggregate_season_bat(TestAggregateSeasonBat().sample(),
                                          keep_clubs=["home"])
        server._season_stats = {"lookup": lk, "built": True}

    def tearDown(self):
        server._season_stats = self._saved

    def test_lookup_tiers(self):
        # Full name, scorebar short form, and unique bare surname all resolve
        for query in ("Peter Smith", "P SMITH", "p.smith", "SMITH"):
            rec = server.lookup_season_stats(query)
            self.assertIsNotNone(rec, query)
            self.assertEqual(rec["avg"], "45.00", query)

    def test_unknown_name_and_empty(self):
        self.assertIsNone(server.lookup_season_stats("ZZZ NOBODY"))
        self.assertIsNone(server.lookup_season_stats(""))


if __name__ == "__main__":
    unittest.main()
