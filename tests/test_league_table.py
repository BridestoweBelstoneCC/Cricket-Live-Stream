"""League-table backend (fetch_league_table / league_table_home_row) — pure logic plus the
network call mocked out, same shape as test_season_stats.py. Covers the defensive checks
called out in server.py's own docstrings: a bad/missing competition_id must never crash or
silently claim a wrong table is usable — see CLAUDE.md's effective_pcs_folder-style "one
door" gotchas for why these checks matter as much as the happy path."""
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server


def table(*rows):
    """rows: list of (position, team, points, played) tuples."""
    return {"date": "2026-09-24", "competition_id": "123", "built": True, "error": None,
            "name": "Division 1", "rows": [
                {"position": p, "team": t, "points": pts, "played": pl}
                for p, t, pts, pl in rows
            ]}


class TestLeagueTableHomeRow(unittest.TestCase):
    def setUp(self):
        self._saved_load_state = server.load_state

    def tearDown(self):
        server.load_state = self._saved_load_state

    def _with_home_team(self, name):
        server.load_state = lambda: {"home_team": name}

    def test_finds_home_row_and_row_above(self):
        self._with_home_team("Bridestowe and Belstone CC")
        tbl = table(
            (4, "Hatherleigh CC", "40", "10"),
            (5, "Bridestowe and Belstone CC", "36", "10"),
            (6, "Rival CC", "30", "10"),
        )
        result = server.league_table_home_row(tbl)
        self.assertIsNotNone(result)
        self.assertEqual(result["index"], 1)
        self.assertEqual(result["row"]["team"], "Bridestowe and Belstone CC")
        self.assertEqual(result["row_above"]["team"], "Hatherleigh CC")

    def test_top_of_table_has_no_row_above(self):
        self._with_home_team("Bridestowe and Belstone CC")
        tbl = table(
            (1, "Bridestowe and Belstone CC", "50", "10"),
            (2, "Rival CC", "40", "10"),
        )
        result = server.league_table_home_row(tbl)
        self.assertEqual(result["index"], 0)
        self.assertIsNone(result["row_above"])

    def test_matches_on_first_significant_word_only(self):
        # PlayCricket's own spelling in the table needn't match state's home_team exactly —
        # only the club's first word has to appear (same spirit as the opposition-abbreviation
        # matching elsewhere in server.py).
        self._with_home_team("Bridestowe CC")
        tbl = table((3, "Bridestowe & District", "20", "8"))
        result = server.league_table_home_row(tbl)
        self.assertIsNotNone(result)

    def test_wrong_table_returns_none_not_a_false_match(self):
        # The defensive case fetch_league_table()'s docstring calls out: a bad competition_id
        # returned an unrelated real league's table rather than erroring. Nothing in it should
        # look like a match for the home club.
        self._with_home_team("Bridestowe and Belstone CC")
        tbl = table((1, "Some Other CC", "50", "10"), (2, "Unrelated CC", "40", "10"))
        self.assertIsNone(server.league_table_home_row(tbl))

    def test_empty_table_and_blank_home_name(self):
        self._with_home_team("")
        self.assertIsNone(server.league_table_home_row(table((1, "Anyone CC", "10", "5"))))
        self._with_home_team("Bridestowe CC")
        self.assertIsNone(server.league_table_home_row(table()))


class TestFetchLeagueTable(unittest.TestCase):
    def setUp(self):
        self._saved_load_state = server.load_state
        self._saved_table = server._league_table

    def tearDown(self):
        server.load_state = self._saved_load_state
        server._league_table = self._saved_table

    def test_no_api_key_or_competition_id_is_a_clean_not_built_error(self):
        # Cup/friendly day, or before /match/fetch has run yet — must not attempt the network
        # call or crash, just report why it isn't usable.
        server.load_state = lambda: {"playcricket_api_key": "", "competition_id": ""}
        result = server.fetch_league_table(force=True)
        self.assertTrue(result["built"])
        self.assertIsNotNone(result["error"])
        self.assertEqual(result["rows"], [])

    def test_picks_columns_by_heading_text_not_fixed_position(self):
        # A non-league table in testing had no points column at all — team/points/played
        # must be found by heading text, never assumed to be a fixed column_N.
        server.load_state = lambda: {"playcricket_api_key": "KEY", "competition_id": "123"}
        api_response = {"league_table": [{
            "name": "Division 1",
            "headings": {"column_1": "Team", "column_5": "Pts", "column_2": "P"},
            "values": [
                {"column_1": "Bridestowe and Belstone CC", "column_5": "36", "column_2": "10"},
            ],
        }]}
        with mock.patch.object(server, "_pc_get_json", return_value=api_response):
            result = server.fetch_league_table(force=True)
        self.assertIsNone(result["error"])
        self.assertEqual(result["rows"][0]["team"], "Bridestowe and Belstone CC")
        self.assertEqual(result["rows"][0]["points"], "36")
        self.assertEqual(result["rows"][0]["played"], "10")

    def test_api_failure_reports_error_not_an_exception(self):
        server.load_state = lambda: {"playcricket_api_key": "KEY", "competition_id": "123"}
        with mock.patch.object(server, "_pc_get_json", side_effect=OSError("boom")):
            result = server.fetch_league_table(force=True)
        self.assertIsNotNone(result["error"])
        self.assertEqual(result["rows"], [])

    def test_cached_same_day_same_competition_skips_the_network(self):
        server.load_state = lambda: {"playcricket_api_key": "KEY", "competition_id": "123"}
        import datetime
        server._league_table = {"date": datetime.date.today().isoformat(),
                                "competition_id": "123", "built": True, "error": None,
                                "name": "Division 1", "rows": [{"position": 1, "team": "X",
                                "points": "1", "played": "1"}]}
        with mock.patch.object(server, "_pc_get_json") as mocked:
            result = server.fetch_league_table(force=False)
        mocked.assert_not_called()
        self.assertEqual(result["rows"][0]["team"], "X")


if __name__ == "__main__":
    unittest.main()
