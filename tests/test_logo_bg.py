"""Making sponsor logos' backgrounds transparent (logo_bg.py + the /sponsor/... routes).

Synthetic images stand in for the two kinds of real logo this was built against (a
club's AU Bullion wordmark and Dino Fencing round badge, both on white): a ring whose
middle is a "letter hole", and a badge whose white middle must survive.
"""
import io
import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from unittest import mock

try:
    from PIL import Image, ImageDraw
except ImportError:
    # The suite is stdlib-only by design (CI installs nothing), and logo_bg needs Pillow —
    # a runtime requirement, not a test one. Skip, the way the JS tests skip without an
    # engine; any machine with the project's requirements installed runs these.
    raise unittest.SkipTest("Pillow not installed")

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import logo_bg  # noqa: E402
import server   # noqa: E402

WHITE, GOLD, BLACK = (255, 255, 255), (230, 180, 40), (0, 0, 0)


def ring(size=200):
    """A gold "o" on white: its middle is a letter hole, enclosed by the letter. Drawn
    in proportion, so a bigger canvas is a bigger logo, not a speck in a sea of white."""
    im = Image.new("RGB", (size, size), WHITE)
    s = size / 200.0
    ImageDraw.Draw(im).ellipse((40 * s, 40 * s, 160 * s, 160 * s), outline=GOLD,
                               width=max(1, round(18 * s)))
    return im


def badge(size=200):
    """A white round badge with a black rim and black text on it, on white."""
    im = Image.new("RGB", (size, size), WHITE)
    d = ImageDraw.Draw(im)
    d.ellipse((10, 10, 190, 190), outline=BLACK, width=4)
    d.rectangle((70, 90, 130, 110), fill=BLACK)
    return im


def alpha_at(im, xy):
    return im.getpixel(xy)[3]


class TestModes(unittest.TestCase):
    def test_outside_keeps_white_inside_the_logo(self):
        out, removed, why = logo_bg.remove_background(ring(), "outside")
        self.assertIsNone(why)
        self.assertEqual(alpha_at(out, (5, 5)), 0)           # around it: gone
        self.assertEqual(alpha_at(out, (100, 100)), 255)     # the hole: kept (white)
        self.assertEqual(alpha_at(out, (100, 49)), 255)      # the gold letter: kept

    def test_everywhere_clears_letter_holes(self):
        out, _, _ = logo_bg.remove_background(ring(), "everywhere")
        self.assertEqual(alpha_at(out, (5, 5)), 0)
        self.assertEqual(alpha_at(out, (100, 100)), 0)       # the hole: gone too
        self.assertEqual(alpha_at(out, (100, 49)), 255)

    def test_badge_survives_outside_mode(self):
        out, _, _ = logo_bg.remove_background(badge(), "outside")
        self.assertEqual(alpha_at(out, (3, 3)), 0)           # corner outside the rim
        self.assertEqual(alpha_at(out, (100, 60)), 255)      # badge's white middle kept
        self.assertEqual(out.getpixel((100, 60))[:3], WHITE)

    def test_no_pale_fringe(self):
        # A soft gold edge on white: the half-transparent edge pixels must come out gold,
        # not whitish — that's the halo that would show on the dark strap.
        im = Image.new("RGB", (120, 120), WHITE)
        ImageDraw.Draw(im).ellipse((20, 20, 100, 100), fill=GOLD)
        im = im.resize((60, 60), Image.LANCZOS)              # anti-aliased edges
        out, _, _ = logo_bg.remove_background(im, "outside")
        edges = [p for p in out.getdata() if 0 < p[3] < 255]
        self.assertTrue(edges)
        for r, g, b, _a in edges:
            self.assertLess(b, 150, "edge pixel kept the white background's tint")

    def test_original_alpha_respected(self):
        im = ring().convert("RGBA")
        im.putpixel((100, 49), GOLD + (100,))               # already part-transparent
        out, _, _ = logo_bg.remove_background(im, "outside")
        self.assertLessEqual(alpha_at(out, (100, 49)), 100)

    def test_large_images_are_scaled_down(self):
        out, _, _ = logo_bg.remove_background(ring(2400), "outside")
        self.assertLessEqual(max(out.size), logo_bg.MAX_SIDE)


class TestRefusals(unittest.TestCase):
    def test_already_transparent(self):
        im = Image.new("RGBA", (100, 100), (0, 0, 0, 0))
        ImageDraw.Draw(im).rectangle((30, 30, 70, 70), fill=GOLD + (255,))
        out, _, why = logo_bg.remove_background(im)
        self.assertIsNone(out)
        self.assertIn("already transparent", why)

    def test_photo_or_gradient(self):
        im = Image.linear_gradient("L").convert("RGB")
        out, _, why = logo_bg.remove_background(im)
        self.assertIsNone(out)
        self.assertIn("no plain background", why)

    def test_logo_same_colour_as_its_background(self):
        out, _, why = logo_bg.remove_background(Image.new("RGB", (100, 100), WHITE), "everywhere")
        self.assertIsNone(out)
        self.assertIn("almost the whole image", why)

    def test_has_plain_background(self):
        self.assertTrue(logo_bg.has_plain_background(ring()))
        self.assertFalse(logo_bg.has_plain_background(Image.linear_gradient("L")))


class TestRoutes(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.dir = os.path.join(self.tmp, "sponsors")
        os.makedirs(self.dir)
        buf = io.BytesIO()
        ring().save(buf, "PNG")
        self.original = buf.getvalue()
        with open(os.path.join(self.dir, "1.png"), "wb") as f:
            f.write(self.original)
        self.state = {"sponsor_id": "1"}
        for p in (mock.patch.object(server, "_CLUB_PASSWORD", ""),
                  mock.patch.object(server, "SPONSOR_DIR", self.dir),
                  mock.patch.object(server, "load_state", side_effect=lambda: dict(self.state)),
                  mock.patch.object(server, "save_state", side_effect=self.state.update)):
            p.start()
            self.addCleanup(p.stop)
        self.httpd = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(self.httpd.server_close)
        self.addCleanup(self.httpd.shutdown)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def get(self, path):
        try:
            with urllib.request.urlopen(self.base + path, timeout=10) as r:
                return r.status, r.headers.get("Content-Type"), r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.headers.get("Content-Type"), e.read()

    def post(self, path, body):
        req = urllib.request.Request(self.base + path, data=json.dumps(body).encode(),
                                     method="POST", headers={"Content-Type": "application/json",
                                                             "Origin": self.base})
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status, json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode() or "{}")

    def test_preview_is_a_transparent_png_and_saves_nothing(self):
        status, ctype, data = self.get("/sponsor/bg_preview?id=1&mode=everywhere")
        self.assertEqual((status, ctype), (200, "image/png"))
        self.assertEqual(Image.open(io.BytesIO(data)).getpixel((100, 100))[3], 0)
        self.assertEqual(os.listdir(self.dir), ["1.png"])

    def test_choosing_saves_a_new_image_and_keeps_the_original(self):
        status, d = self.post("/sponsor/remove_background", {"id": "1", "mode": "outside"})
        self.assertEqual(status, 200)
        self.assertEqual((d["sponsor_id"], d["previous_id"]), ("2", "1"))
        self.assertEqual(self.state["sponsor_id"], "2")
        with open(os.path.join(self.dir, "1.png"), "rb") as f:
            self.assertEqual(f.read(), self.original)          # untouched: Undo works
        self.assertEqual(Image.open(os.path.join(self.dir, "2.png")).mode, "RGBA")

    def test_refusal_is_explained(self):
        Image.new("RGBA", (50, 50), (0, 0, 0, 0)).save(os.path.join(self.dir, "5.png"))
        status, d = self.post("/sponsor/remove_background", {"id": "5", "mode": "outside"})
        self.assertEqual(status, 400)
        self.assertIn("already transparent", d["error"])

    def test_bad_mode_and_missing_image(self):
        self.assertEqual(self.post("/sponsor/remove_background", {"id": "1", "mode": "x"})[0], 400)
        self.assertEqual(self.post("/sponsor/remove_background", {"id": "9", "mode": "outside"})[0], 400)

    def test_ids_cant_reach_outside_the_folder(self):
        secret = os.path.join(self.tmp, "secret.png")
        shutil.copy(os.path.join(self.dir, "1.png"), secret)
        for bad in ("../secret", "..\\secret", "/secret", "C:secret"):
            self.assertIsNone(server.find_sponsor_image(bad), bad)

    def test_preview_needs_a_login(self):
        with mock.patch.object(server, "_CLUB_PASSWORD", "secret"):
            self.assertEqual(self.get("/sponsor/bg_preview?id=1&mode=outside")[0], 401)

    def test_upload_says_when_there_is_a_plain_background(self):
        req = urllib.request.Request(self.base + "/sponsor/upload", data=self.original,
                                     method="POST", headers={"Content-Type": "image/png",
                                                             "Origin": self.base})
        with urllib.request.urlopen(req, timeout=10) as r:
            self.assertTrue(json.loads(r.read().decode())["plain_background"])


if __name__ == "__main__":
    unittest.main()


class TestPanel(unittest.TestCase):
    def test_previews_use_data_urls_not_blob_urls(self):
        # The panel's CSP is img-src 'self' data: — a blob: preview is silently refused and
        # the operator gets two empty boxes (found in a real browser).
        with open(os.path.join(REPO, "control.html"), encoding="utf-8") as f:
            src = f.read()
        fn = src[src.index("async function openBgChooser("):src.index("async function pickBg(")]
        self.assertNotIn("URL.createObjectURL(", fn)
        self.assertIn("readAsDataURL", fn)
        csp = open(os.path.join(REPO, "server.py"), encoding="utf-8").read()
        self.assertIn("img-src 'self' data:;", csp)
