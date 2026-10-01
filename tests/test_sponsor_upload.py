"""Uploading the weekend sponsor's logo from the control panel (POST /sponsor/upload).

Runs the real Handler on an ephemeral port, like tests/test_http.py, with the sponsors
folder and state redirected to a temp dir — the real sponsors/ folder is never touched.
"""
import json
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import server  # noqa: E402

PNG = b"\x89PNG\r\n\x1a\n" + b"\x00" * 64
JPEG = b"\xff\xd8\xff\xe0" + b"\x00" * 64
GIF = b"GIF89a" + b"\x00" * 64
WEBP = b"RIFF\x24\x00\x00\x00WEBPVP8 " + b"\x00" * 64
SVG = b'<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>'


class TestSniff(unittest.TestCase):
    def test_accepted_formats(self):
        self.assertEqual([server.sniff_image(d) for d in (PNG, JPEG, GIF, WEBP)],
                         ["png", "jpg", "gif", "webp"])

    def test_rejected(self):
        # SVG can carry script; a renamed text file is still a text file.
        for data in (SVG, b"hello", b"", b"RIFF\x00\x00\x00\x00WAVE"):
            self.assertIsNone(server.sniff_image(data))


class TestUpload(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.dir = os.path.join(self.tmp, "sponsors")
        os.makedirs(self.dir)
        for n in ("1.png", "2.jpg", "6.png", "README.MD", "club-logo.png"):
            open(os.path.join(self.dir, n), "wb").close()
        self.state = {"sponsor_name": "Acme Builders", "sponsor_id": "2"}
        # No club password unless a test sets one: the server reads the REAL config.ini at
        # import, so on a machine with a password every upload here would be a 401.
        patches = [mock.patch.object(server, "_CLUB_PASSWORD", ""),
                   mock.patch.object(server, "SPONSOR_DIR", self.dir),
                   mock.patch.object(server, "load_state", side_effect=lambda: dict(self.state)),
                   mock.patch.object(server, "save_state", side_effect=self.state.update)]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        self.httpd = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(self.httpd.server_close)
        self.addCleanup(self.httpd.shutdown)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def upload(self, data, origin=None, ctype="image/png"):
        req = urllib.request.Request(self.base + "/sponsor/upload", data=data, method="POST",
                                     headers={"Content-Type": ctype,
                                              "Origin": origin or self.base})
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status, json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            body = e.read().decode() or "{}"
            return e.code, json.loads(body) if body.startswith("{") else {}

    def files(self):
        return sorted(os.listdir(self.dir))

    def test_saved_under_the_next_number_and_used_straight_away(self):
        status, d = self.upload(PNG)
        self.assertEqual((status, d["ok"], d["sponsor_id"]), (200, True, "7"))
        with open(os.path.join(self.dir, "7.png"), "rb") as f:
            self.assertEqual(f.read(), PNG)
        self.assertEqual(self.state["sponsor_id"], "7")

    def test_extension_comes_from_the_contents_not_the_claim(self):
        self.upload(JPEG, ctype="image/png")
        self.assertIn("7.jpg", self.files())

    def test_served_back_to_the_overlay(self):
        self.upload(PNG)
        with urllib.request.urlopen(self.base + "/sponsor/7", timeout=5) as r:
            self.assertEqual((r.headers["Content-Type"], r.read()), ("image/png", PNG))

    def test_not_an_image_is_refused_and_nothing_saved(self):
        before = self.files()
        for data, ctype in ((SVG, "image/svg+xml"), (b"just text", "image/png")):
            status, d = self.upload(data, ctype=ctype)
            self.assertEqual(status, 400)
            self.assertFalse(d["ok"])
        self.assertEqual(self.files(), before)
        self.assertEqual(self.state["sponsor_id"], "2")

    def test_too_big_is_refused(self):
        # Refused from the Content-Length alone, before reading megabytes of it — so the
        # client may see the connection drop mid-send rather than the 413 itself. The panel
        # checks the size first, so a person only ever sees its own message.
        try:
            status, _ = self.upload(PNG + b"\x00" * server.SPONSOR_UPLOAD_MAX_BYTES)
            self.assertEqual(status, 413)
        except (ConnectionError, urllib.error.URLError):
            pass
        self.assertNotIn("7.png", self.files())

    def test_bigger_than_the_usual_body_limit_is_fine(self):
        # Every other POST is capped at 1 MB; a phone-exported logo is often bigger.
        status, _ = self.upload(PNG + b"\x00" * (server.MAX_BODY_BYTES + 1))
        self.assertEqual(status, 200)

    def test_needs_a_login(self):
        with mock.patch.object(server, "_CLUB_PASSWORD", "secret"):
            status, _ = self.upload(PNG)
        self.assertEqual(status, 401)
        self.assertNotIn("7.png", self.files())

    def test_cross_site_refused(self):
        status, _ = self.upload(PNG, origin="https://evil.example")
        self.assertEqual(status, 403)
        self.assertNotIn("7.png", self.files())

    def test_empty_folder_starts_at_one(self):
        shutil.rmtree(self.dir)
        status, d = self.upload(GIF)
        self.assertEqual(d["sponsor_id"], "1")
        self.assertTrue(os.path.exists(os.path.join(self.dir, "1.gif")))


if __name__ == "__main__":
    unittest.main()


class TestConcurrency(unittest.TestCase):
    """Two logo uploads landing together crashed one of them: save_state's shared .tmp
    file collided across threads ("Permission denied" on Windows), and both uploads could
    pick the same "next free" ID."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        state_file = os.path.join(self.tmp, "match_state.json")
        p = mock.patch.object(server, "STATE_FILE", state_file)
        p.start()
        self.addCleanup(p.stop)
        server.save_state({"sponsor_id": ""})

    def hammer(self, fn, threads=12):
        errors = []

        def run(i):
            try:
                fn(i)
            except Exception as e:          # noqa: BLE001 — the point is to catch anything
                errors.append(repr(e))
        ts = [threading.Thread(target=run, args=(i,)) for i in range(threads)]
        for t in ts:
            t.start()
        for t in ts:
            t.join()
        return errors

    def test_simultaneous_state_saves_while_being_read(self):
        stop = threading.Event()

        def reader():
            # Far busier than the real overlay (a poll every ~2.5s): a read every
            # millisecond, so a save regularly lands while the file is open.
            while not stop.is_set():
                try:
                    with open(server.STATE_FILE, encoding="utf-8") as f:
                        f.read()
                except OSError:
                    pass
                time.sleep(0.001)
        r = threading.Thread(target=reader)
        r.start()
        try:
            errors = self.hammer(lambda i: [server.save_state({"n": i, "k": k}) for k in range(5)],
                                 threads=8)
        finally:
            stop.set()
            r.join()
        self.assertEqual(errors, [])
        with open(server.STATE_FILE, encoding="utf-8") as f:
            self.assertIn("n", json.load(f))

    def test_simultaneous_uploads_get_their_own_ids(self):
        d = os.path.join(self.tmp, "sponsors")
        with mock.patch.object(server, "SPONSOR_DIR", d):
            ids = []
            errors = self.hammer(lambda i: ids.append(server.save_sponsor_logo(PNG)[1]), threads=8)
        self.assertEqual(errors, [])
        self.assertEqual(sorted(ids, key=int), [str(n) for n in range(1, 9)])
        self.assertEqual(len(os.listdir(d)), 8)
