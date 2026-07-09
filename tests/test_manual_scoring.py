"""Manual scoring: the shared engine's apply-path, the event-sourced session (undo =
replay must be exact), persistence, and the /scoring HTTP surface end-to-end — including
manual frames flowing through /live into the overlay pipeline and the ball DB."""
import json
import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import server
import scoring_engine
from test_http import HttpTestBase   # noqa: E402

XI_A = [(str(i), f"Alan Alpha{i}") for i in range(1, 12)]
XI_B = [(str(i), f"Bob Beta{i}") for i in range(1, 12)]


def engine(**kw):
    args = dict(batting_xi=XI_A, bowling_xi=XI_B, batting_name="Alpha CC",
                bowling_name="Beta CC", max_overs=20, openers_selected=True)
    args.update(kw)
    return scoring_engine.InningsEngine(**args)


class TestApplyOutcome(unittest.TestCase):
    def test_the_book_balances(self):
        e = engine()
        for kind in ("1", "4", "dot", "6", "2"):
            e.apply_outcome(kind)
        self.assertEqual(e.total, 13)
        self.assertEqual(e.legal_balls, 5)
        self.assertEqual(e.over_tokens, ["1", "4", ".", "6", "2"])
        # striker rotation: 1 swaps, 4 doesn't, dot doesn't, 6 doesn't, 2 doesn't
        self.assertEqual(e.striker.name, "Alan Alpha2")
        self.assertEqual(e.striker.runs, 12)        # 4+6+2 after the single swapped them
        self.assertEqual(e.non_striker.runs, 1)

    def test_over_rollover_clears_ticker_and_swaps(self):
        e = engine()
        for _ in range(6):
            e.apply_outcome("dot")
        self.assertEqual(e.over_tokens, [])          # NV Play semantics
        self.assertEqual(e.overs_str(), "1.0")
        self.assertEqual(e.striker.name, "Alan Alpha2")  # ends swapped
        self.assertEqual(e.bowler_for_over(0).maidens, 1)

    def test_extras_accounting(self):
        e = engine()
        e.apply_outcome("wide", runs=4)              # 5 to the total, no ball faced
        e.apply_outcome("noball", runs=2)            # 3 total: 1 extra + 2 to the batter
        e.apply_outcome("bye", runs=1)
        e.apply_outcome("legbye", runs=4)
        self.assertEqual(e.total, 5 + 3 + 1 + 4)
        self.assertEqual(e.extras, 5 + 1 + 1 + 4)
        self.assertEqual(e.legal_balls, 2)           # wide/nb don't count
        bowler = e.bowler_for_over(0)
        self.assertEqual(bowler.runs, 5 + 3)         # byes/legbyes not conceded
        # all tokens re-sum correctly through the server's own classifier
        run_sum = sum(server._classify_ball(t)["runs"] for t in e.over_tokens)
        self.assertEqual(run_sum, e.total)

    def test_wicket_types_and_credit(self):
        e = engine()
        e.set_bowler("Bob Beta7")
        e.apply_outcome("W", wicket_type="caught", fielder="Bob Beta3")
        self.assertEqual(e.wkts, 1)
        self.assertEqual(e.batters[0].how_out, "C BETA3 B BETA7")
        self.assertEqual(e.get_bowler("Bob Beta7").wkts, 1)
        self.assertEqual(e.last_wicket["type"], "CAUGHT")
        # run out: never credited, non-striker can be the one out
        e.apply_outcome("W", wicket_type="run out", out_non_striker=True)
        self.assertEqual(e.wkts, 2)
        self.assertEqual(e.get_bowler("Bob Beta7").wkts, 1)     # unchanged
        self.assertEqual(e.last_wicket["howout"], "RUN OUT")

    def test_new_batter_arrives_and_can_be_swapped(self):
        e = engine()
        e.apply_outcome("W", wicket_type="bowled")
        self.assertEqual(e.striker.name, "Alan Alpha3")          # auto next-in
        e.choose_next_batter("Alan Alpha7")
        self.assertEqual(e.striker.name, "Alan Alpha7")
        # once they've faced a ball, no swapping
        e.apply_outcome("1")
        with self.assertRaises(ValueError):
            e.choose_next_batter("Alan Alpha3")

    def test_manual_bowler_schedule(self):
        e = engine()
        e.set_bowler("Bob Beta2")                               # not in the default attack
        self.assertEqual(e.bowler_for_over(0).name, "Bob Beta2")
        for _ in range(6):
            e.apply_outcome("dot")
        self.assertTrue(e.awaiting_new_over)                 # over 1 needs a bowler pick
        e.set_bowler("Bob Beta9")
        self.assertFalse(e.awaiting_new_over)
        e.apply_outcome("1")
        self.assertEqual(e.get_bowler("Bob Beta2").legal, 6)
        self.assertEqual(e.get_bowler("Bob Beta9").legal, 1)

    def test_validation(self):
        e = engine()
        with self.assertRaises(ValueError):
            e.apply_outcome("banana")
        with self.assertRaises(ValueError):
            e.apply_outcome("wide", runs=9)
        with self.assertRaises(ValueError):
            e.apply_outcome("W", wicket_type="retired to the bar")
        e.end()
        with self.assertRaises(ValueError):
            e.apply_outcome("1")                             # innings declared over


class SessionBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="manual_test_")
        self._orig_file = server.MANUAL_SCORING_FILE
        server.MANUAL_SCORING_FILE = os.path.join(self.tmp, "manual_scoring.json")
        server._manual["session"] = None
        server._manual["load_attempted"] = True

    def tearDown(self):
        server.MANUAL_SCORING_FILE = self._orig_file
        server._manual["session"] = None
        server._manual["load_attempted"] = False
        shutil.rmtree(self.tmp, ignore_errors=True)

    def session(self):
        return server.ManualScoringSession({
            "home": "Home CC", "away": "Rival CC", "max_overs": 20,
            "home_xi": [n for _, n in XI_A], "away_xi": [n for _, n in XI_B]})


class TestSession(SessionBase):
    BALLS = [{"event": "ball", "kind": k} for k in
             ("1", "4", "dot", "W", "2", "6", "dot", "1")]

    def test_undo_is_an_exact_replay(self):
        a = self.session()
        for ev in self.BALLS:
            a.apply(dict(ev))
        for _ in range(3):
            a.undo()
        b = self.session()
        for ev in self.BALLS[:-3]:
            b.apply(dict(ev))
        self.assertEqual(a.current.frame(1), b.current.frame(1))
        self.assertEqual(a.current.total, b.current.total)

    def test_undo_crosses_the_innings_boundary(self):
        s = self.session()
        s.apply({"event": "ball", "kind": "4"})
        s.apply({"event": "end_innings"})
        s.apply({"event": "start_innings"})
        self.assertEqual(s.innings_no, 2)
        s.undo()                                             # un-start innings 2
        self.assertEqual(s.innings_no, 1)
        s.undo()                                             # un-end innings 1
        self.assertFalse(s.current.complete)
        s.apply({"event": "ball", "kind": "6"})              # play continues
        self.assertEqual(s.current.total, 10)

    def test_persistence_round_trip(self):
        s = self.session()
        for ev in self.BALLS:
            s.apply(dict(ev))
        s.apply({"event": "bowler", "name": "Bob Beta4"})
        restored = server.ManualScoringSession.load()
        self.assertEqual(restored.current.frame(1), s.current.frame(1))
        self.assertEqual(len(restored.events), len(s.events))

    def test_frames_parse_like_a_real_feed(self):
        s = self.session()
        server._innings_latch = 1
        for ev in self.BALLS:
            s.apply(dict(ev))
        st = server.parse_pcs_json(s.current.frame(1))
        self.assertEqual(st["score"], 14)
        self.assertEqual(st["wickets"], 1)
        self.assertEqual(st["battingTeamName"], "Home CC")
        # innings 2 carries the target → the overlay's chase display just works
        s.apply({"event": "end_innings"})
        s.apply({"event": "start_innings"})
        st2 = server.parse_pcs_json(s.current.frame(2))
        server._innings_latch = 1
        self.assertEqual(st2["innings"], 2)
        self.assertEqual(st2["targetRuns"], 15)

    def test_bad_event_is_not_recorded(self):
        s = self.session()
        with self.assertRaises(ValueError):
            s.apply({"event": "ball", "kind": "nope"})
        self.assertEqual(s.events, [])
        with self.assertRaises(ValueError):
            s.apply({"event": "start_innings"})              # innings 1 not ended
        self.assertEqual(s.events, [])


class TestScoringHttp(HttpTestBase):
    """The /scoring surface end-to-end against a live server — including manual frames
    flowing through /live into the overlay pipeline and the ball-by-ball DB."""

    def setUp(self):
        server._last_good_state = None
        server.save_state(dict(server.DEFAULT_STATE))
        self._orig_file = server.MANUAL_SCORING_FILE
        server.MANUAL_SCORING_FILE = os.path.join(self.tmp, "manual_scoring.json")
        try:
            os.remove(server.MANUAL_SCORING_FILE)
        except OSError:
            pass
        server._manual["session"] = None
        server._manual["load_attempted"] = True
        server._prev_state.update({"score": None, "wickets": None, "overs": None})
        server._event_buffer.clear()
        server._innings_latch = 1
        server._ball_log_prev.update({"mid": None, "innings": None, "over": None,
                                      "score": 0, "wickets": 0, "count": 0})

    def tearDown(self):
        server.MANUAL_SCORING_FILE = self._orig_file
        server._manual["session"] = None
        server._manual["load_attempted"] = False

    def test_page_serves(self):
        status, _, data = self.request("GET", "/scoring")
        self.assertEqual(status, 200)
        self.assertTrue(data.startswith(b"<!DOCTYPE html"))
        self.assertIn(b"Manual Scoring", data)

    def test_state_inactive_before_setup(self):
        status, body = self.get_json("/scoring/state")
        self.assertEqual(status, 200)
        self.assertFalse(body["active"])

    def setup_match(self):
        status, body = self.post_json("/scoring/setup", {
            "home": "Home CC", "away": "Rival CC", "max_overs": 20,
            "home_xi": "P Smith\nJ Smith\nO Hart", "away_xi": "", "batting_first": "home"})
        self.assertEqual(status, 200, body)
        self.assertTrue(body["ok"])
        return body["state"]

    def test_full_flow_drives_the_overlay_pipeline(self):
        st = self.setup_match()
        self.assertTrue(st["active"])
        self.assertEqual(st["striker"]["name"], "P Smith")
        # scoring implies stream-safe settings
        self.assertFalse(server.load_state()["demo_mode"])
        self.assertFalse(server.load_state()["use_widget"])

        for body in ({"kind": "1"}, {"kind": "4"},
                     {"kind": "W", "wicket_type": "caught", "fielder": "Rival CC 5"}):
            status, d = self.post_json("/scoring/ball", body)
            self.assertEqual(status, 200, d)
        status, d = self.get_json("/scoring/state")
        self.assertEqual(d["score"], 5)
        self.assertEqual(d["wickets"], 1)
        self.assertEqual(d["this_over"], ["1", "4", "W"])
        self.assertEqual(d["new_batter"], "O Hart")

        # /live: manual outranks everything, masquerades as the PCS feed, and the ball
        # logger writes the manual over into the DB exactly like a real feed
        status, live = self.get_json("/live")
        self.assertEqual(live["source"], "pcs")
        self.assertEqual(live["feed"], "manual")
        self.assertEqual(live["state"]["score"], 5)
        import sqlite3
        with sqlite3.connect(server._db_path()) as c:
            rows = c.execute("SELECT outcome FROM balls WHERE over=0 ORDER BY ball").fetchall()
        self.assertEqual([r[0] for r in rows], ["1", "4", "W"])

    def test_undo_and_reset_over_http(self):
        self.setup_match()
        self.post_json("/scoring/ball", {"kind": "6"})
        status, d = self.post_json("/scoring/undo", {})
        self.assertEqual(status, 200)
        self.assertEqual(d["state"]["score"], 0)
        status, d = self.post_json("/scoring/reset", {})
        self.assertEqual(status, 200)
        status, body = self.get_json("/scoring/state")
        self.assertFalse(body["active"])
        self.assertFalse(os.path.exists(server.MANUAL_SCORING_FILE))

    def test_double_setup_needs_reset_or_force(self):
        self.setup_match()
        self.post_json("/scoring/ball", {"kind": "1"})
        status, d = self.post_json("/scoring/setup", {"home": "X", "away": "Y"})
        self.assertEqual(status, 400)
        self.assertIn("in progress", d["error"])
        status, d = self.post_json("/scoring/setup", {"home": "X", "away": "Y", "force": True})
        self.assertEqual(status, 200)

    def test_bad_ball_rejected_cleanly(self):
        self.setup_match()
        status, d = self.post_json("/scoring/ball", {"kind": "banana"})
        self.assertEqual(status, 400)
        status, d = self.get_json("/scoring/state")
        self.assertEqual(d["score"], 0)


class TestScoringAuth(HttpTestBase):
    CLUB_PASSWORD = "testpw"

    def test_scoring_posts_are_token_gated(self):
        # No overlay carve-out here: the scoring page has its own login flow
        status, _ = self.post_json("/scoring/ball", {"kind": "4"})
        self.assertEqual(status, 401)
        status, _ = self.post_json("/scoring/setup", {"home": "X"})
        self.assertEqual(status, 401)
        # ...but the page itself and its read-only state stay open (login happens in-page)
        status, _, _ = self.request("GET", "/scoring")
        self.assertEqual(status, 200)
        status, _, _ = self.request("GET", "/scoring/state")
        self.assertEqual(status, 200)


if __name__ == "__main__":
    unittest.main()
