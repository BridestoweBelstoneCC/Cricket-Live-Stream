"""Scorebar styles — the three places that define them must agree.

A style name is written down in three independent spots: overlay.html's SCOREBAR_STYLES
(which body.style-* class to apply), overlay.html's CSS (the block that class selects), and
control.html's picker (what the operator can actually choose). Drift between them fails
silently and specifically: the panel offers a style, the operator picks it, the overlay
applies a class no CSS matches, and the bar just stays Classic with nothing logged anywhere.
Same shape of bug as the graphics_* toggles that have to be added to BOOL_FIELDS by hand.

These are text assertions on the files rather than a rendered comparison — the visual side
is covered by scripts/render_scorebar.py, which needs a browser and so can't live in CI.
"""
import os
import re
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)
import server


def read(name):
    with open(os.path.join(REPO, name), encoding="utf-8") as f:
        return f.read()


class TestScorebarStyles(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.overlay = read("overlay.html")
        cls.control = read("control.html")
        m = re.search(r"const SCOREBAR_STYLES = \[(.*?)\];", cls.overlay, re.S)
        assert m, "SCOREBAR_STYLES not found in overlay.html"
        cls.js_styles = re.findall(r"'([a-z]+)'", m.group(1))
        picker = re.search(r'<select id="scorebar_style">(.*?)</select>', cls.control, re.S)
        assert picker, "scorebar_style picker not found in control.html"
        cls.picker_styles = re.findall(r'<option value="([a-z]+)"', picker.group(1))

    def test_every_js_style_has_a_css_block(self):
        # Otherwise the class gets applied and nothing at all changes on screen.
        for style in self.js_styles:
            self.assertIn(f"body.style-{style} #scorebar", self.overlay,
                          f"no CSS block for style '{style}'")

    def test_picker_offers_classic_plus_every_js_style(self):
        self.assertEqual(sorted(self.picker_styles), sorted(["classic"] + self.js_styles))

    def test_classic_is_not_a_style_class(self):
        # Classic is the base stylesheet, not an override block — if it ever gains a
        # body.style-classic class it must also be added to SCOREBAR_STYLES to be applied.
        self.assertNotIn("classic", self.js_styles)
        self.assertNotIn("body.style-classic", self.overlay)

    def test_default_state_value_is_selectable_in_the_panel(self):
        self.assertIn(server.DEFAULT_STATE["scorebar_style"], self.picker_styles)

    def test_dark_styles_override_the_inline_bowler_colour(self):
        # applyColours() sets #bowler-name's colour inline to #fff for the dark team-colour
        # block every style used to have behind it. Any style that puts the bowler on a
        # light background has to win that with !important or the name is invisible --
        # exactly what happened to Minimal, caught only by rendering it.
        self.assertRegex(
            self.overlay,
            r"body\.style-minimal #bowler-name \{[^}]*color:[^;]*!important",
            "Minimal must force #bowler-name's colour over applyColours' inline #fff")

    def test_every_style_keeps_the_bar_at_the_shared_height(self):
        # 72px is load-bearing: the graphic panels sit at bottom:72px and fow/partnership/
        # milestone panels are themselves 72px to line up with the bar. A style that changes
        # #scorebar's height desynchronises all of them.
        for style in self.js_styles:
            block = re.search(r"body\.style-%s #scorebar \{(.*?)\}" % style,
                              self.overlay, re.S)
            self.assertIsNotNone(block, f"no #scorebar block for '{style}'")
            self.assertNotIn("height", block.group(1),
                             f"style '{style}' overrides the shared 72px scorebar height")


if __name__ == "__main__":
    unittest.main()
