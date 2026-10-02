"""Spoken commentary (voice.py + server.speak_over_commentary + the overlay player). The
speech engine is faked; the real one (Windows' Microsoft Hazel) was checked by ear."""
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
import voice
from tests.test_http import HttpTestBase

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class SynthesizeTests(unittest.TestCase):
    def test_text_goes_through_a_file_not_the_command_line(self):
        captured = {}

        def fake_run(cmd, **kw):
            captured["cmd"] = cmd
            out = cmd[cmd.index("-OutFile") + 1] if "-OutFile" in cmd else cmd[cmd.index("-o") + 1] \
                if "-o" in cmd else cmd[cmd.index("-w") + 1]
            with open(out, "wb") as f:
                f.write(b"RIFF" + b"\0" * 200)
            return mock.Mock(returncode=0, stderr="")
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        line = 'North hits a "six"; $(rm -rf /) & more'
        with mock.patch.object(voice.subprocess, "run", side_effect=fake_run), \
                mock.patch.object(voice.shutil, "which", return_value="espeak"):
            ok, _ = voice.synthesize(line, os.path.join(tmp, "a.wav"))
        self.assertTrue(ok)
        self.assertFalse(any(line in str(part) for part in captured["cmd"]))

    def test_empty_text_and_failures_never_raise(self):
        self.assertEqual(voice.synthesize("  ", "x.wav")[0], False)
        with mock.patch.object(voice.subprocess, "run", side_effect=OSError("no engine")), \
                mock.patch.object(voice.shutil, "which", return_value="espeak"):
            ok, msg = voice.synthesize("hello", os.path.join(tempfile.gettempdir(), "v.wav"))
        self.assertFalse(ok)


class ServerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        for p in (mock.patch.object(server, "VOICE_DIR", self.tmp),):
            p.start()
            self.addCleanup(p.stop)
        saved = dict(server._over_commentary)
        self.addCleanup(lambda: setattr(server, "_over_commentary", saved))

    def fake_synth(self, text, out):
        with open(out, "wb") as f:
            f.write(b"RIFF")
        return True, out

    def test_voice_attached_to_the_matching_over(self):
        server._over_commentary = {"text": "x", "over": 7}
        with mock.patch.object(voice, "synthesize", side_effect=self.fake_synth):
            server.speak_over_commentary("x", 7)
        self.assertTrue(server._over_commentary["voice"].startswith("/voice/over_7.wav"))

    def test_late_voice_never_lands_on_a_newer_over(self):
        server._over_commentary = {"text": "y", "over": 8}       # over 8 already replaced it
        with mock.patch.object(voice, "synthesize", side_effect=self.fake_synth):
            server.speak_over_commentary("x", 7)
        self.assertNotIn("voice", server._over_commentary)

    def test_old_recordings_are_pruned(self):
        with mock.patch.object(voice, "synthesize", side_effect=self.fake_synth), \
                mock.patch.object(server, "VOICE_KEEP", 3):
            for n in range(6):
                server.speak_over_commentary("x", n)
                os.utime(os.path.join(self.tmp, f"over_{n}.wav"), (n, n))
            server.speak_over_commentary("x", 99)
        self.assertEqual(len(os.listdir(self.tmp)), 3)


class EndpointTests(HttpTestBase):
    def test_serves_wav_and_refuses_others(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d, True)
        with open(os.path.join(d, "over_3.wav"), "wb") as f:
            f.write(b"RIFFdata")
        with mock.patch.object(server, "VOICE_DIR", d):
            status, headers, body = self.request("GET", "/voice/over_3.wav?t=1")
            self.assertEqual((status, body), (200, b"RIFFdata"))
            self.assertEqual(self.request("GET", "/voice/..%2Fconfig.ini")[0], 404)


class WiringTests(unittest.TestCase):
    def read(self, name):
        with open(os.path.join(ROOT, name), encoding="utf-8") as f:
            return f.read()

    def test_overlay_plays_it_once_per_over_and_only_when_on(self):
        html = self.read("overlay.html")
        self.assertIn("speakCommentary(d);", html)
        self.assertIn("!cfg.graphics_voice_commentary", html)
        self.assertIn("d.over === _lastSpokenOver", html)

    def test_obs_routes_the_overlay_audio_into_the_stream(self):
        self.assertEqual(self.read("obs_setup.py").count('"reroute_audio": True'), 2)

    def test_off_by_default(self):
        self.assertFalse(server.DEFAULT_STATE["graphics_voice_commentary"])


if __name__ == "__main__":
    unittest.main()
