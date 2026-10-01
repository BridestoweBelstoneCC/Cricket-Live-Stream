"""The control panel's structure after the 2026-10 refresh: tabs, the pinned top bar, the
scorebar preview, and the club-colour accent.

The refresh moved most of the panel's markup into four tabs. The JS finds everything by
id, so the guards here are about ids and the handlers they call; how it LOOKS was checked
in a real browser (screenshots of every tab, desktop and phone widths).
"""
import os
import re
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_js_parity import run_js, REPO_ROOT   # noqa: E402
from test_camera_autocut import extract_function   # noqa: E402

with open(os.path.join(REPO_ROOT, "control.html"), encoding="utf-8") as f:
    PANEL = f.read()
with open(os.path.join(REPO_ROOT, "server.py"), encoding="utf-8") as f:
    SERVER = f.read()
HTML_IDS = re.findall(r'\bid="([^"]+)"', PANEL)
TABS = ("match", "graphics", "after", "setup")


def js_array(name):
    m = re.search(r"\b" + name + r"\s*=\s*\[([^\]]*)\]", PANEL)
    return re.findall(r"'([^']+)'", m.group(1)) if m else []


class TestStructure(unittest.TestCase):
    def test_ids_are_unique(self):
        dupes = sorted({i for i in HTML_IDS if HTML_IDS.count(i) > 1})
        self.assertEqual(dupes, [])

    def test_every_saved_setting_is_still_on_the_page(self):
        # saveState() collects these by id; a field lost in a restructure would silently
        # stop saving (getElementById returns null and the value is skipped).
        fields = js_array("FIELDS") + js_array("NUM_FIELDS") + js_array("BOOL_FIELDS")
        self.assertGreater(len(fields), 40)
        # Already absent before the refresh, and harmless: set by the server ("Fetch
        # today's match" fills umpires/competition/match id) or retired, and POST /state
        # MERGES, so a key the panel doesn't send is kept, not wiped.
        server_managed = {"match_url", "umpire1_name", "umpire2_name", "competition_name",
                          "pc_match_id", "replay_motto", "poll_interval", "graphics_commentary"}
        missing = [f for f in fields if f not in HTML_IDS and f not in server_managed]
        self.assertEqual(missing, [])

    def test_every_handler_exists(self):
        handlers = set(re.findall(r'on(?:click|change|input|keydown)="(?:if\([^)]*\))?([A-Za-z_]\w*)\(', PANEL))
        missing = [h for h in handlers
                   if not re.search(r"(?:async\s+)?function\s+" + h + r"\s*\(", PANEL)]
        self.assertEqual(missing, [])

    def test_four_tabs_each_with_a_panel(self):
        for t in TABS:
            self.assertIn(f'data-tab="{t}"', PANEL)
            self.assertIn(f'id="tab-{t}"', PANEL)
        # Match day is shown in the markup itself, so nothing flashes before the script.
        self.assertIn('class="tab-panel active" id="tab-match"', PANEL)

    def test_tab_visibility_is_class_only(self):
        # An "#tab-match{display:block}" rule outranked ".tab-panel{display:none}" and
        # pinned Match day open on every tab — found in a browser screenshot.
        self.assertIsNone(re.search(r"#tab-\w+\s*\{[^}]*display", PANEL))

    def test_save_and_status_are_pinned_on_every_tab(self):
        top = PANEL[PANEL.index('<div id="topbar">'):PANEL.index('<div class="status" id="status">')]
        self.assertIn('id="save-btn"', top)
        self.assertIn('id="live-status"', top)
        self.assertIn("position:sticky", PANEL[PANEL.index("#topbar{"):][:200])

    def test_messages_point_at_the_new_places(self):
        for stale in ("Match card", "Camera card", "AI Commentary card", "Stream card"):
            self.assertNotIn(stale, PANEL)
            self.assertNotIn(stale, SERVER)

    def test_no_club_name_in_defaults(self):
        self.assertNotIn("'BRIDES'", PANEL)


class TestScorebarPreview(unittest.TestCase):
    """The preview box was blank since it was added: the overlay draws the scorebar at the
    bottom of a 1080px canvas, and the frame was 72px tall."""

    def setUp(self):
        self.fn = extract_function(PANEL, "scaleScorebarPreview")

    def test_frame_is_the_full_canvas_shifted_to_the_bar(self):
        self.assertIn("PREVIEW_CANVAS_H + 'px'", self.fn)
        self.assertIn("frame.style.top = (-_previewBarTop * scale)", self.fn)

    def test_bar_measured_only_while_visible(self):
        # In a hidden tab the bar reports top 0, which pinned the preview to the blank top.
        self.assertIn("if (scale > 0)", self.fn)
        self.assertIn("if (top > 0)", self.fn)

    def test_showing_the_tab_rescales_it(self):
        self.assertIn("scaleScorebarPreview()", extract_function(PANEL, "showTab"))


class TestClubAccent(unittest.TestCase):
    """applyClubAccent, run in a real JS engine."""

    def accent(self, hexes):
        fns = "\n".join(extract_function(PANEL, n) for n in
                        ("_hexRgb", "_lum", "_contrast", "_lighter", "_css", "applyClubAccent"))
        js = ("var PANEL_BG = [11, 20, 34];\nvar props = {};\n"
              "var document = {documentElement: {style: {setProperty: function (k, v) { props[k] = v; },"
              " removeProperty: function (k) { delete props[k]; }}}};\n" + fns + "\n"
              "var out = {};\n" + json_list(hexes) + ".forEach(function (h) { props = {};"
              " applyClubAccent(h); out[h] = Object.assign({}, props); });\n"
              "process.stdout.write(JSON.stringify(out));")
        return run_js(js)

    def contrast(self, rgb_css, against=(11, 20, 34)):
        rgb = [int(v) for v in re.findall(r"\d+", rgb_css)]

        def lum(c):
            c = [v / 255 for v in c]
            c = [v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4 for v in c]
            return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]
        a, b = lum(rgb), lum(against)
        return (max(a, b) + 0.05) / (min(a, b) + 0.05)

    def test_every_kit_colour_reads_on_the_dark_panel(self):
        colours = ["#1a3a5c", "#000000", "#7b2d2d", "#f5c842", "#ffffff", "#c62828", "#0a7a3a"]
        out = self.accent(colours)
        if out is None:
            self.skipTest("no JS engine available")
        for c in colours:
            self.assertGreaterEqual(self.contrast(out[c]["--accent"]), 4.4, c)
            self.assertGreaterEqual(self.contrast(out[c]["--accent-strong"]), 2.9, c)

    def test_light_kits_get_dark_button_text(self):
        out = self.accent(["#f5c842", "#ffffff", "#1a3a5c"])
        if out is None:
            self.skipTest("no JS engine available")
        self.assertEqual(out["#f5c842"]["--on-accent"], "#0a0e14")
        self.assertEqual(out["#ffffff"]["--on-accent"], "#0a0e14")
        self.assertEqual(out["#1a3a5c"]["--on-accent"], "#ffffff")

    def test_navy_stays_blue_not_grey(self):
        # Mixing towards white washed navy out to slate; lightening keeps the hue.
        out = self.accent(["#1a3a5c"])
        if out is None:
            self.skipTest("no JS engine available")
        r, g, b = [int(v) for v in re.findall(r"\d+", out["#1a3a5c"]["--accent-strong"])]
        self.assertGreater(b - r, 80)

    def test_not_a_colour_falls_back_to_the_defaults(self):
        out = self.accent(["", "blue", "#12"])
        if out is None:
            self.skipTest("no JS engine available")
        self.assertEqual(out, {"": {}, "blue": {}, "#12": {}})


def json_list(items):
    import json
    return json.dumps(items)


if __name__ == "__main__":
    unittest.main()
