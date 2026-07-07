"""Parity check: overlay.html's classifyBall (JS) vs server.py's _classify_ball (Python).

The two are hand-written ports of each other — the overlay drives boundary replays and the
ticker display, the server drives the ball-by-ball DB — so a divergence means the graphics
and the logged data silently disagree about the same delivery. This extracts the real JS
function from overlay.html and runs it through a real JS engine (node, or JavaScriptCore
via osascript on macOS — the same fallback chain as scripts/check_panel_js.py), comparing
each token's classification against the Python side.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO_ROOT)
import server

TOKENS = ["", ".", "0", "1", "2", "3", "4", "5", "6",
          "W", "w", "w+1", "w+4", "nb", "1nb", "2nb+4",
          "1b", "2b", "4b", "1lb", "4lb", "xyz", "14"]


def extract_classify_ball():
    """Pull the complete classifyBall function out of overlay.html by brace counting."""
    html = open(os.path.join(REPO_ROOT, "overlay.html"), encoding="utf-8").read()
    start = html.index("function classifyBall")
    depth = 0
    for i in range(start, len(html)):
        if html[i] == "{":
            depth += 1
        elif html[i] == "}":
            depth -= 1
            if depth == 0:
                return html[start:i + 1]
    raise AssertionError("could not extract classifyBall from overlay.html")


def run_js(js_source):
    """Run a JS program that prints one line of JSON; returns the parsed value or None
    if no engine is available on this machine."""
    if shutil.which("node"):
        with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as f:
            f.write(js_source)
            path = f.name
        try:
            r = subprocess.run(["node", path], capture_output=True, text=True, timeout=30)
            if r.returncode != 0:
                raise AssertionError(f"node failed: {r.stderr}")
            return json.loads(r.stdout.strip())
        finally:
            os.unlink(path)
    if sys.platform == "darwin":
        r = subprocess.run(["osascript", "-l", "JavaScript", "-e", js_source],
                           capture_output=True, text=True, timeout=30)
        # osascript prints console.log to stderr and the final expression to stdout
        out = (r.stdout.strip() or r.stderr.strip())
        if r.returncode != 0 or not out:
            raise AssertionError(f"osascript failed: {r.stderr}")
        return json.loads(out)
    return None


def python_cls(token):
    """Map _classify_ball's structured outcome onto the JS side's display class."""
    r = server._classify_ball(token)
    if r["wicket"]:
        return "wicket"
    if r["extra"]:
        return r["extra"]                     # wide / noball / bye / legbye
    if r["outcome"] == "dot":
        return "dot"
    n = int(r["outcome"])
    return "four" if n == 4 else "six" if n == 6 else ""


class TestClassifyBallParity(unittest.TestCase):
    def test_js_and_python_agree_on_every_token(self):
        js = (extract_classify_ball()
              + "\nconst out = " + json.dumps(TOKENS) + ".map(classifyBall);"
              + "\nconsole.log(JSON.stringify(out));")
        results = run_js(js)
        if results is None:
            self.skipTest("no JS engine available (no node, not on macOS)")
        self.assertEqual(len(results), len(TOKENS))
        for token, js_result in zip(TOKENS, results):
            self.assertEqual(js_result["cls"], python_cls(token),
                             f"JS and Python disagree on token {token!r}: "
                             f"js={js_result} py={server._classify_ball(token)}")

    def test_run_totals_agree_with_ticker_sums(self):
        # The overlay's over-summary derives runs from the score delta, but the DB logger
        # sums _classify_ball runs — spot-check the Python run values for the same tokens.
        expected = {"": 0, ".": 0, "0": 0, "1": 1, "4": 4, "6": 6, "W": 0,
                    "w": 1, "w+4": 5, "nb": 1, "1nb": 2, "2nb+4": 7,
                    "2b": 2, "4lb": 4, "xyz": 0, "14": 14}
        for tok, runs in expected.items():
            self.assertEqual(server._classify_ball(tok)["runs"], runs, tok)


if __name__ == "__main__":
    unittest.main()
