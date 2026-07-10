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

    def test_mid_over_bowler_change_and_maidens(self):
        e = engine()
        e.set_bowler("Bob Beta2")
        for k in ("1", "1", "dot"):                          # Beta2 concedes 2 off 3 balls
            e.apply_outcome(k)
        e.set_bowler("Bob Beta3")                            # injury — Beta3 finishes the over
        for _ in range(3):
            e.apply_outcome("dot")
        # A shared over is nobody's maiden, even though the finisher conceded nothing
        self.assertEqual(e.get_bowler("Bob Beta3").maidens, 0)
        # When the replaced bowler returns and bowls six dots, the stale count from the
        # interrupted over must not suppress the genuine maiden
        e.set_bowler("Bob Beta2")
        for _ in range(6):
            e.apply_outcome("dot")
        self.assertEqual(e.get_bowler("Bob Beta2").maidens, 1)

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


class TestEditAndScorecard(SessionBase):
    def test_edit_a_ball_in_a_completed_over(self):
        s = self.session()
        for k in ("1", "dot", "dot", "dot", "dot", "dot"):   # over 1: 1 run
            s.apply({"event": "ball", "kind": k})
        s.apply({"event": "ball", "kind": "2"})              # over 2, ball 1
        self.assertEqual(s.current.total, 3)
        s.edit_ball(0, {"event": "ball", "kind": "4"})       # that "1" was really a 4
        self.assertEqual(s.current.total, 6)
        self.assertEqual(s.current.overs_str(), "1.1")       # position unchanged

    def test_edit_can_remove_a_wicket(self):
        s = self.session()
        s.apply({"event": "ball", "kind": "W", "wicket_type": "bowled"})
        s.apply({"event": "ball", "kind": "4"})
        self.assertEqual(s.current.wkts, 1)
        s.edit_ball(0, {"event": "ball", "kind": "dot"})     # not out — a dot
        self.assertEqual(s.current.wkts, 0)
        self.assertEqual(s.current.total, 4)

    def test_edit_is_an_exact_replay(self):
        s = self.session()
        for k in ("1", "4", "dot", "2"):
            s.apply({"event": "ball", "kind": k})
        s.edit_ball(1, {"event": "ball", "kind": "6"})
        # a fresh session built from the corrected event list must match frame-for-frame
        ref = self.session()
        for ev in ({"event": "ball", "kind": "1"}, {"event": "ball", "kind": "6"},
                   {"event": "ball", "kind": "dot"}, {"event": "ball", "kind": "2"}):
            ref.apply(dict(ev))
        self.assertEqual(s.current.frame(1), ref.current.frame(1))

    def test_edit_with_downstream_bowler_event_stays_valid(self):
        s = self.session()
        for _ in range(6):
            s.apply({"event": "ball", "kind": "dot"})        # over 1 complete
        s.apply({"event": "bowler", "name": "Bob Beta8"})    # bowler for over 2
        s.apply({"event": "ball", "kind": "4"})
        s.edit_ball(2, {"event": "ball", "kind": "1"})       # correct a dot in over 1
        self.assertEqual(s.current.total, 5)                 # 1 + 4
        self.assertEqual(s.current.bowler_for_over(1).name, "Bob Beta8")

    def test_edit_to_invalid_ball_rolls_back(self):
        s = self.session()
        for k in ("1", "4", "2"):
            s.apply({"event": "ball", "kind": k})
        before = s.current.frame(1)
        with self.assertRaises(ValueError):
            s.edit_ball(1, {"event": "ball", "kind": "banana"})
        self.assertEqual(s.current.frame(1), before)         # restored exactly
        self.assertEqual([e.get("kind") for e in s.events], ["1", "4", "2"])
        # regression: a rejected edit must leave the session fully usable, not a wrecked
        # event log (the _rebuild clobber bug) — scoring must continue normally
        s.apply({"event": "ball", "kind": "6"})
        self.assertEqual(s.current.total, 13)                # 1+4+2+6

    def test_undo_after_edit_that_orphaned_a_batter_pick(self):
        # A wicket, a "someone else came in" pick, then the wicket is edited into runs:
        # the 'batter' event is now lenient-only. Undo must replay leniently too, or it
        # wedges mid-rebuild and leaves the live innings truncated.
        s = self.session()
        s.apply({"event": "ball", "kind": "W", "wicket_type": "bowled"})
        s.apply({"event": "batter", "name": "Alan Alpha7"})
        s.apply({"event": "ball", "kind": "1"})
        s.edit_ball(0, {"event": "ball", "kind": "4"})       # not out after all
        s.apply({"event": "ball", "kind": "2"})
        s.undo()                                             # must not raise
        self.assertEqual(s.current.total, 5)
        self.assertEqual(s.current.wkts, 0)

    def test_restore_after_edit_that_orphaned_a_batter_pick(self):
        # Same log persisted then restored after a "restart": load() must replay
        # leniently or the whole saved match is discarded as unreadable.
        s = self.session()
        s.apply({"event": "ball", "kind": "W", "wicket_type": "bowled"})
        s.apply({"event": "batter", "name": "Alan Alpha7"})
        s.apply({"event": "ball", "kind": "1"})
        s.edit_ball(0, {"event": "ball", "kind": "4"})
        restored = server.ManualScoringSession.load()
        self.assertEqual(restored.current.frame(1), s.current.frame(1))
        self.assertEqual(restored.current.wkts, 0)

    def test_edit_rejects_non_ball_index(self):
        s = self.session()
        s.apply({"event": "ball", "kind": "1"})
        s.apply({"event": "swap_strike"})
        with self.assertRaises(ValueError):
            s.edit_ball(1, {"event": "ball", "kind": "4"})   # index 1 is a swap, not a ball
        with self.assertRaises(ValueError):
            s.edit_ball(9, {"event": "ball", "kind": "4"})   # out of range

    def test_ball_list_positions(self):
        s = self.session()
        for k in ("1", "4", "dot", "2", "6", "dot"):         # over 1 (6 legal)
            s.apply({"event": "ball", "kind": k})
        s.apply({"event": "ball", "kind": "3"})              # over 2, ball 1
        bl = s.ball_list()
        self.assertEqual(len(bl), 7)
        self.assertEqual((bl[0]["index"], bl[0]["over"], bl[0]["ball"], bl[0]["token"]),
                         (0, 1, 1, "1"))
        self.assertEqual((bl[-1]["over"], bl[-1]["ball"], bl[-1]["token"]), (2, 1, "3"))

    def test_scorecard_text_has_both_disciplines_and_the_result(self):
        s = self.session()
        # innings 1: a boundary then a wicket, then declare and play innings 2 to a win
        s.apply({"event": "ball", "kind": "4"})
        s.apply({"event": "ball", "kind": "W", "wicket_type": "bowled"})
        s.apply({"event": "end_innings"})
        s.apply({"event": "start_innings"})
        s.apply({"event": "ball", "kind": "6"})              # 6 > target of 5 → won
        card = s.scorecard_text()
        self.assertIn("Home CC v Rival CC", card)
        self.assertIn("BATTING", card)
        self.assertIn("BOWLING", card)
        self.assertIn("Extras", card)
        self.assertIn("Alan Alpha1", card)                  # home opener who batted
        self.assertIn("RESULT:", card)
        self.assertIn("won by", card)


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

    def test_edit_ball_over_http(self):
        self.setup_match()
        self.post_json("/scoring/ball", {"kind": "1"})
        self.post_json("/scoring/ball", {"kind": "4"})
        status, d = self.get_json("/scoring/balls")
        self.assertEqual(status, 200)
        self.assertEqual(len(d["balls"]), 2)
        # correct the first ball (1 → 6); the drive was really a six
        status, d = self.post_json("/scoring/edit", {"index": 0, "kind": "6"})
        self.assertEqual(status, 200, d)
        self.assertEqual(d["state"]["score"], 10)            # 6 + 4
        # the corrected over flows through to the ball DB via /live too
        status, live = self.get_json("/live")
        import sqlite3
        with sqlite3.connect(server._db_path()) as c:
            rows = c.execute("SELECT outcome FROM balls WHERE over=0 ORDER BY ball").fetchall()
        self.assertEqual([r[0] for r in rows], ["6", "4"])

    def test_edit_bad_index_is_400(self):
        self.setup_match()
        self.post_json("/scoring/ball", {"kind": "1"})
        status, d = self.post_json("/scoring/edit", {"index": 9, "kind": "4"})
        self.assertEqual(status, 400)

    def test_last_over_recap_in_state(self):
        self.setup_match()
        for k in ("1", "dot", "2", "dot", "4", "dot"):       # over 1 = 7 runs
            self.post_json("/scoring/ball", {"kind": k})
        status, st = self.get_json("/scoring/state")
        self.assertTrue(st["awaiting_bowler"])
        self.assertEqual(st["last_over"]["num"], 1)
        self.assertEqual(st["last_over"]["runs"], 7)
        self.assertEqual(st["last_over"]["balls"], ["1", ".", "2", ".", "4", "."])

    def test_scorecard_endpoint(self):
        self.setup_match()
        self.post_json("/scoring/ball", {"kind": "4"})
        status, _, data = self.request("GET", "/scoring/scorecard")
        self.assertEqual(status, 200)
        self.assertIn(b"BATTING", data)
        self.assertIn(b"Home CC", data)

    def test_balls_and_scorecard_empty_before_setup(self):
        status, d = self.get_json("/scoring/balls")
        self.assertEqual(status, 200)
        self.assertFalse(d["ok"])
        self.assertEqual(d["balls"], [])
        status, _, _ = self.request("GET", "/scoring/scorecard")
        self.assertEqual(status, 404)


class TestScoringAuth(HttpTestBase):
    CLUB_PASSWORD = "testpw"

    def test_scoring_posts_are_token_gated(self):
        # No overlay carve-out here: the scoring page has its own login flow
        status, _ = self.post_json("/scoring/ball", {"kind": "4"})
        self.assertEqual(status, 401)
        status, _ = self.post_json("/scoring/setup", {"home": "X"})
        self.assertEqual(status, 401)
        status, _ = self.post_json("/scoring/edit", {"index": 0, "kind": "4"})
        self.assertEqual(status, 401)
        # ...but the page itself and its read-only views stay open (login happens in-page)
        for path in ("/scoring", "/scoring/state", "/scoring/balls", "/scoring/scorecard"):
            status, _, _ = self.request("GET", path)
            self.assertIn(status, (200, 404), path)   # 404 only = no session yet, not auth


if __name__ == "__main__":
    unittest.main()
