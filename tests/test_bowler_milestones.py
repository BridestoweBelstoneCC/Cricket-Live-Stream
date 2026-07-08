"""Bowler-milestone logic (hat-trick chain, five-for) — extracted from overlay.html and
executed in a real JS engine with stubbed DOM/config, same approach as the classifyBall
parity test. The chain rules are subtle (cross-over chains, run outs, wides, the
over-completing wicket the ticker never shows), so they get real executions, not reviews."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_js_parity import run_js, REPO_ROOT   # noqa: E402


def extract_function(html, name):
    start = html.index("function " + name)
    depth = 0
    for i in range(start, len(html)):
        if html[i] == "{":
            depth += 1
        elif html[i] == "}":
            depth -= 1
            if depth == 0:
                return html[start:i + 1]
    raise AssertionError(f"could not extract {name}")


def overlay_functions(*names):
    html = open(os.path.join(REPO_ROOT, "overlay.html"), encoding="utf-8").read()
    return "\n".join(extract_function(html, n) for n in names)


STUBS = """
var cfg = { graphics_milestones: true };
var fired = [];
function triggerBowlerMilestone(big, name, sub) { fired.push({big: big, name: name, sub: sub}); }
function fmtSurname(f){ if(!f||f==='\\u2014')return '\\u2014'; var p=f.trim().split(' '); return p[p.length-1]; }
var _hatTrick = { bowler: '', count: 0 };
var _htPrevWickets = 0;
var _bowlerFived = {};
var _lastPCSinnings = 1;
function poll(tokens, bowler, wickets, wicketType) {
  updateHatTrickChain(
    tokens.map(function(c){ return {cls: c}; }),
    {bowler: {name: bowler}, wickets: wickets, lastWicketType: wicketType || ''});
}
"""


class TestHatTrickChain(unittest.TestCase):
    def run_scenario(self, body):
        js = (STUBS
              + overlay_functions("_chainWicket", "_chainBreak",
                                  "updateHatTrickChain", "checkBowlerFiveFor")
              + "\n" + body
              + "\nconsole.log(JSON.stringify(fired));")
        result = run_js(js)
        if result is None:
            self.skipTest("no JS engine available")
        return result

    def test_three_in_three_fires_once(self):
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['wicket'], 'A Nother', 3);
poll(['dot'],    'A Nother', 3);
""")
        self.assertEqual(len(fired), 1)
        self.assertEqual(fired[0]["sub"], "HAT-TRICK!")
        self.assertEqual(fired[0]["name"], "Nother")   # fmtSurname casing, as batter milestones

    def test_a_dot_ball_breaks_the_chain(self):
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['dot'],    'A Nother', 2);
poll(['wicket'], 'A Nother', 3);
""")
        self.assertEqual(fired, [])

    def test_chain_survives_the_other_bowlers_over(self):
        # Wickets on the last two balls of A's over, then B bowls a full over,
        # then A strikes first ball of his next over: a genuine hat-trick.
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['dot'], 'B Side', 2);
poll(['dot','1'], 'B Side', 2);
poll(['wicket'], 'A Nother', 3);
""")
        self.assertEqual(len(fired), 1)
        self.assertEqual(fired[0]["sub"], "HAT-TRICK!")

    def test_wides_and_noballs_are_chain_neutral(self):
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wide'],   'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll(['noball'], 'A Nother', 2);
poll(['wicket'], 'A Nother', 3);
""")
        self.assertEqual(len(fired), 1)

    def test_run_out_breaks_without_extending(self):
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2, 'RUN OUT');
poll(['wicket'], 'A Nother', 3);
poll(['wicket'], 'A Nother', 4);
""")
        # run out at #2 resets: the later two wickets only reach a count of 2
        self.assertEqual(fired, [])

    def test_over_completing_wicket_counts_via_delta_fallback(self):
        # Third wicket falls on the over-completing ball: the ticker clears on that
        # write, so newBalls is empty — the wickets delta must still extend the chain.
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'A Nother', 2);
poll([], 'A Nother', 3);
""")
        self.assertEqual(len(fired), 1)

    def test_two_bowlers_wickets_never_mix(self):
        fired = self.run_scenario("""
poll(['wicket'], 'A Nother', 1);
poll(['wicket'], 'B Side',   2);
poll(['wicket'], 'A Nother', 3);
""")
        self.assertEqual(fired, [])


class TestFiveFor(unittest.TestCase):
    def run_scenario(self, body):
        js = (STUBS
              + overlay_functions("_chainWicket", "_chainBreak",
                                  "updateHatTrickChain", "checkBowlerFiveFor")
              + "\n" + body
              + "\nconsole.log(JSON.stringify(fired));")
        result = run_js(js)
        if result is None:
            self.skipTest("no JS engine available")
        return result

    def test_fires_at_five_once_then_again_at_six(self):
        fired = self.run_scenario("""
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 4, runs: 18, overs: '6.2'}});
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21, overs: '7.1'}});
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21, overs: '7.1'}});
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 6, runs: 23, overs: '7.4'}});
""")
        self.assertEqual([f["big"] for f in fired], ["5-21", "6-23"])
        self.assertIn("5 wicket haul", fired[0]["sub"])

    def test_respects_the_milestones_toggle(self):
        fired = self.run_scenario("""
cfg.graphics_milestones = false;
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21}});
cfg.graphics_milestones = true;
checkBowlerFiveFor({bowler: {name: 'A Nother', wickets: 5, runs: 21}});
""")
        # toggled off: nothing fires AND the fired-flag isn't burned — firing once it's on
        self.assertEqual(len(fired), 1)

    def test_placeholder_bowler_ignored(self):
        fired = self.run_scenario("""
checkBowlerFiveFor({bowler: {name: '\\u2014', wickets: 5, runs: 21}});
checkBowlerFiveFor({});
""")
        self.assertEqual(fired, [])


if __name__ == "__main__":
    unittest.main()
