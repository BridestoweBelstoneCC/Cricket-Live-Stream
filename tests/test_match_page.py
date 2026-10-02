"""The automatic match page (match_page.render + server.build_match_page and its routes).
The AI report, result card and YouTube lookup are faked; the real build was checked by eye
in a browser against a simulated match."""
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import match_page
import server
from tests.test_http import HttpTestBase

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

INNINGS = [{"team": "Home CC", "total": "33-1 (5 ov)",
            "batters": [{"name": "Smith", "runs": 19, "balls": 13, "out": False}],
            "bowlers": [{"name": "Frost", "o": "3", "r": 22, "w": 0}],
            "fow": [{"score": "7-1", "batter": "P Smith"}]},
           {"team": "Rival CC", "total": "73-5 (10 ov)", "batters": [], "bowlers": []}]


class RenderTests(unittest.TestCase):
    def test_every_piece_of_text_is_escaped(self):
        page = match_page.render({
            "title": "<script>alert(1)</script>", "result": "A & B won",
            "report": "He hit <b>six</b>\n\n<img src=x onerror=alert(1)>",
            "innings": [{**INNINGS[0], "batters": [{"name": "O'Neil <i>", "runs": 1,
                                                    "balls": 2, "out": True}]}],
            "video_url": 'https://youtu.be/x" onclick="evil'})
        self.assertNotIn("<script>alert", page)
        self.assertNotIn("<img src=x", page)
        self.assertNotIn("<i>", page)
        self.assertNotIn('" onclick="', page)
        self.assertIn("A &amp; B won", page)

    def test_missing_parts_are_left_out(self):
        page = match_page.render({"title": "Home v Away"})
        for absent in ("Watch the full stream", "Match report", "Runs by over",
                       'class=sponsors', "Result card"):
            self.assertNotIn(absent, page)

    def test_report_markdown_headings_are_dropped(self):
        page = match_page.render({"report": "# Headline\nFirst para\n\nSecond para"})
        self.assertNotIn("Headline", page)
        self.assertIn("<p>First para</p>", page)

    def test_worm_has_a_legend_and_over_numbers(self):
        svg = match_page._worm_svg({"1": [(1, 5), (2, 12)], "2": [(1, 3), (2, 20)]},
                                   ["#111111", "#222222"], ["Home CC", "Rival <CC>"])
        self.assertEqual(svg.count("<polyline"), 2)
        self.assertIn(">Home CC<", svg)
        self.assertIn("Rival &lt;CC&gt;", svg)
        self.assertIn(">2</text>", svg)
        self.assertEqual(match_page._worm_svg({}, ["#000"]), "")

    def test_over_labels_thin_out_for_long_formats(self):
        svg = match_page._worm_svg({"1": [(o, o * 4) for o in range(1, 51)]}, ["#000"])
        self.assertIn(">50</text>", svg)
        self.assertNotIn(">3</text>", svg)


class BuildTests(HttpTestBase):
    def setUp(self):
        self.out = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.out, True)
        facts = {"ok": True, "date": "02 Oct 2026", "outcome": "win", "margin": "by 5 wickets",
                 "sides": [{"club": "Home CC", "won": False}, {"club": "Rival CC", "won": True}]}
        db = {"innings": {"1": {"score": 33, "wickets": 1, "overs": "5"},
                          "2": {"score": 73, "wickets": 5, "overs": "9.4"}},
              "batters": {"1": INNINGS[0]["batters"]}, "bowlers": {},
              "fall_of_wickets": [{"batter": "P Smith", "score": "7-1", "innings": "1"}]}
        for p in (mock.patch.object(server, "MATCH_PAGES_DIR", self.out),
                  mock.patch.object(server, "current_match_id", return_value="mp1"),
                  mock.patch.object(server, "generate_social_graphic_facts", return_value=facts),
                  mock.patch.object(server, "match_facts_from_db", return_value=db),
                  mock.patch.object(server, "generate_match_report",
                                    return_value={"ok": True, "text": "A fine chase."}),
                  mock.patch.object(server, "build_instagram_image", side_effect=OSError("no")),
                  mock.patch.object(server, "_todays_stream_url", return_value="https://youtu.be/abc")):
            p.start()
            self.addCleanup(p.stop)
        with server._db() as c:
            c.execute("DELETE FROM balls WHERE match_id='mp1'")
            rows = [("mp1", 1, o, 1, r) for o, r in ((0, 6), (4, 33))] + \
                   [("mp1", 2, o, 1, r) for o, r in ((0, 4), (9, 73), (10, 80))]
            c.executemany("INSERT INTO balls (match_id, innings, over, ball, cum_runs) "
                          "VALUES (?,?,?,?,?)", rows)

    def build(self):
        ok, msg, path = server.build_match_page()
        self.assertTrue(ok, msg)
        with open(path, encoding="utf-8") as f:
            return path, f.read()

    def test_builds_a_page_from_the_match(self):
        path, page = self.build()
        self.assertTrue(os.path.basename(path).endswith("_home-cc-v-rival-cc.html"))
        for part in ("Rival CC won by 5 wickets", "A fine chase.", "https://youtu.be/abc",
                     "Fall of wickets: 7-1 (P Smith)", "Runs by over"):
            self.assertIn(part, page)

    def test_chart_stops_at_the_innings_own_overs(self):
        _, page = self.build()
        # innings 2 finished in 9.4 overs: the stray ball logged in over 11 is not drawn
        self.assertIn(">10</text>", page)
        self.assertNotIn(">11</text>", page)

    def test_a_failed_part_never_stops_the_page(self):
        with mock.patch.object(server, "generate_match_report", side_effect=RuntimeError("x")):
            _, page = self.build()
        self.assertNotIn("Match report", page)
        self.assertIn("Rival CC won", page)

    def test_no_match_yet(self):
        with mock.patch.object(server, "generate_social_graphic_facts",
                               return_value={"ok": False, "error": "No match data yet"}):
            self.assertEqual(server.build_match_page(), (False, "No match data yet", None))

    def test_routes(self):
        self.assertEqual(self.request("GET", "/match/page/latest")[0], 404)
        self.build()
        status, headers, body = self.request("GET", "/match/page/latest")
        self.assertEqual(status, 200)
        self.assertIn("text/html", headers.get("Content-Type", ""))
        self.assertIn(b"Rival CC won", body)
        status, d = self.get_json("/match/page")
        self.assertEqual(status, 200)
        self.assertTrue(d["latest"].endswith(".html"))

    def test_a_second_build_while_one_runs_is_refused(self):
        with mock.patch.dict(server._match_page_status, {"running": True}):
            status, d = self.post_json("/match/page", {})
        self.assertEqual(status, 409)
        self.assertFalse(d["ok"])


class WiringTests(unittest.TestCase):
    def test_built_automatically_when_the_stream_ends(self):
        with open(os.path.join(ROOT, "server.py"), encoding="utf-8") as f:
            src = f.read()
        self.assertIn("threading.Timer(60, start_match_page_build)", src)

    def test_panel_has_the_card(self):
        with open(os.path.join(ROOT, "control.html"), encoding="utf-8") as f:
            html = f.read()
        self.assertIn("buildMatchPage()", html)
        self.assertGreaterEqual(html.count("loadMatchPage();"), 2)   # both login paths


if __name__ == "__main__":
    unittest.main()
