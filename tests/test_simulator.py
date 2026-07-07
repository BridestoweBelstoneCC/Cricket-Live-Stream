"""The match simulator doubles as a regression harness: every frame it emits is fed
through server.parse_pcs_json, and the engine's books must balance like a real scorer's.
If the simulator and the server ever disagree about the feed's grammar or semantics,
these tests catch it before a rehearsal (or a real match) does."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
import simulate_match as sim


def run_scenario(name, seed=7, overs=10):
    s = sim.MatchSimulator(name, max_overs=overs, seed=seed)
    frames = list(s.frames())
    return s, frames


class TestEngineBookkeeping(unittest.TestCase):
    """The scorer's-book invariants, checked over a full simulated match."""

    @classmethod
    def setUpClass(cls):
        cls.sim, cls.frames = run_scenario("full", seed=7, overs=10)

    def test_match_reaches_a_result(self):
        inn1, inn2 = self.sim.innings
        self.assertTrue(inn1.complete and inn2.complete)
        self.assertTrue(inn2.wkts >= 10 or inn2.legal_balls >= inn2.max_overs * 6
                        or inn2.total >= inn2.target)

    def test_totals_balance(self):
        for inn in self.sim.innings:
            bat_runs = sum(b.runs for b in inn.batters)
            self.assertEqual(inn.total, bat_runs + inn.extras,
                             "team total must equal batter runs + extras")
            self.assertEqual(inn.wkts, sum(1 for b in inn.batters if b.how_out))
            self.assertEqual(sum(w.legal for w in inn.attack), inn.legal_balls,
                             "bowlers' legal balls must sum to the innings' legal balls")
            bowler_conceded = sum(w.runs for w in inn.attack)
            self.assertLessEqual(bowler_conceded, inn.total)   # byes/legbyes not conceded

    def test_ticker_grammar_matches_server_parser(self):
        # Every completed over's tokens, summed through the SERVER's classifier, must
        # equal the runs the engine credited that over — i.e. the ball-by-ball DB would
        # log exactly what the simulator scored.
        for inn in self.sim.innings:
            self.assertTrue(inn.token_history)
            for tokens, over_runs in inn.token_history:
                parsed_sum = sum(server._classify_ball(t)["runs"] for t in tokens)
                self.assertEqual(parsed_sum, over_runs, tokens)
                legal = sum(1 for t in tokens if server._classify_ball(t)["legal"])
                self.assertEqual(legal, 6, tokens)


class TestNvPlaySemantics(unittest.TestCase):
    """The exact feed behaviors that have caused real bugs (see CLAUDE.md gotchas)."""

    @classmethod
    def setUpClass(cls):
        cls.sim, cls.frames = run_scenario("full", seed=11, overs=8)

    def test_ticker_clears_on_the_over_completing_write(self):
        # The write that TRANSITIONS overs from X.5 to (X+1).0 must carry an empty
        # ticker. (A later N.0 frame may legitimately show "w" — a wide bowled before
        # the new over's first legal ball doesn't advance the over counter.)
        saw_rollover = False
        prev_overs = None
        for label, f in self.frames:
            if label == "ball":
                if prev_overs is not None and f["overs"] != prev_overs \
                        and f["overs"].endswith(".0") and float(f["overs"]) > 0:
                    saw_rollover = True
                    self.assertEqual(f["last_ball"], "",
                                     "NV Play clears the ticker on the same write that "
                                     "completes the over — the simulator must too")
                prev_overs = f["overs"]
        self.assertTrue(saw_rollover)

    def test_prematch_frames_have_blank_names_not_placeholders(self):
        pre = [f for label, f in self.frames if label == "prematch"]
        self.assertTrue(pre)
        for f in pre:
            self.assertEqual(f["batter1_name"], "")
            self.assertNotIn("{{", f["batter1_name"])
            st = server.parse_pcs_json(f)
            self.assertEqual(st["batter1"]["name"], "—")   # what the overlay's
                                                            # isPreMatchPCS keys off

    def test_all_values_are_strings(self):
        _, f = self.frames[-1]
        for k, v in f.items():
            self.assertIsInstance(v, str, k)

    def test_every_live_frame_parses(self):
        server._innings_latch = 1
        for label, f in self.frames:
            st = server.parse_pcs_json(f)
            self.assertIsNotNone(st, label)
            self.assertGreaterEqual(st["score"], 0)

    def test_innings_latch_transitions_once_and_holds(self):
        server._innings_latch = 1
        seen = []
        for label, f in self.frames:
            st = server.parse_pcs_json(f)
            if not seen or seen[-1] != st["innings"]:
                seen.append(st["innings"])
        server._innings_latch = 1
        # 1 (innings one) → 1→2 at the break → stays 2 through the winning runs
        self.assertEqual(seen, [1, 2])

    def test_winning_runs_do_not_unlatch(self):
        # The final 'end' frames have runs_required == 0 — the latch must hold innings 2
        server._innings_latch = 1
        last_states = [server.parse_pcs_json(f) for label, f in self.frames]
        server._innings_latch = 1
        finals = last_states[-3:]
        for st in finals:
            self.assertEqual(st["innings"], 2)


class TestScenarios(unittest.TestCase):
    def test_chase_starts_live_in_second_innings_with_target(self):
        s, frames = run_scenario("chase", seed=3, overs=10)
        label, first = frames[0]
        st = server.parse_pcs_json(first)
        server._innings_latch = 1
        self.assertEqual(int(first["target"]), s.innings[0].total + 1)
        self.assertGreater(int(first["runs_required"]), 0)
        self.assertLessEqual(int(first["runs_required"]), 41)

    def test_century_scenario_has_a_batter_in_the_eighties(self):
        s, frames = run_scenario("century", seed=5, overs=10)
        _, first = frames[0]
        top = max(int(first["batter1_runs"]), int(first["batter2_runs"]))
        self.assertGreaterEqual(top, 85)

    def test_collapse_scenario_loses_wickets(self):
        s, frames = run_scenario("collapse", seed=9, overs=12)
        start_wkts = int(frames[0][1]["wickets"])
        end_wkts = int(frames[-1][1]["wickets"])
        self.assertGreater(end_wkts, start_wkts)

    def test_deterministic_for_a_seed(self):
        _, a = run_scenario("full", seed=42, overs=6)
        _, b = run_scenario("full", seed=42, overs=6)
        self.assertEqual(a, b)
        _, c = run_scenario("full", seed=43, overs=6)
        self.assertNotEqual(a, c)

    def test_unknown_scenario_rejected(self):
        with self.assertRaises(ValueError):
            sim.MatchSimulator("nope")


class TestFrameFieldContract(unittest.TestCase):
    """The frame must carry exactly the fields scoreboard.template produces, so the
    rehearsal exercises the same parse paths as the real scorer's feed."""

    def test_field_names_match_the_template(self):
        import json
        import re
        tmpl_path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                                 "scoreboard.template")
        template_keys = set(json.load(open(tmpl_path)).keys())
        s, frames = run_scenario("full", seed=1, overs=6)
        frame_keys = set(frames[-1][1].keys())
        self.assertEqual(frame_keys, template_keys)

    def test_wicket_fields_populated_after_a_dismissal(self):
        s, frames = run_scenario("collapse", seed=9, overs=12)
        with_wicket = [f for label, f in frames if f["last_wicket_howout"]]
        self.assertTrue(with_wicket)
        f = with_wicket[-1]
        self.assertTrue(f["last_wicket_batter"])
        self.assertTrue(f["last_wicket_type"])
        # dismissal must reference a real fielding-side bowler (unless run out)
        if f["last_wicket_bowler"]:
            self.assertIn(f["last_wicket_bowler"],
                          [nm for _, nm in sim.HOME_XI + sim.AWAY_XI])

    def test_card_fields_consistent_with_score(self):
        s, frames = run_scenario("full", seed=7, overs=8)
        # take the last frame of innings 1 (just before the break frames)
        inn1_frames = [f for label, f in frames if f["batting_team"] == sim.HOME_TEAM]
        f = inn1_frames[-1]
        card_runs = sum(int(f[f"card_b{i}_runs"] or 0) for i in range(1, 12))
        extras = s.innings[0].extras
        self.assertEqual(card_runs + extras, int(f["runs"]))
        card_wkts = sum(1 for i in range(1, 12) if f[f"card_b{i}_out"])
        self.assertEqual(card_wkts, int(f["wickets"]))


if __name__ == "__main__":
    unittest.main()
