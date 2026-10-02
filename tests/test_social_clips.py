"""Vertical social clips (social_clips.py + server.make_social_clips / the /social/clips
routes). No ffmpeg or API key needed: rendering and the caption call are faked; the real
render was checked by eye on rehearsal replays."""
import json
import os
import shutil
import sys
import tempfile
import time
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
import social_clips
from tests.test_http import HttpTestBase


class RenderingTests(unittest.TestCase):
    def test_headlines(self):
        self.assertEqual(social_clips.headline_for("Wicket"), "WICKET!")
        self.assertEqual(social_clips.headline_for("Century - Smith"), "CENTURY!")
        self.assertEqual(social_clips.headline_for("Boundary"), "FOUR!")
        self.assertEqual(social_clips.headline_for(""), "HIGHLIGHT")

    def test_lines_wrap_and_cap(self):
        lines = social_clips.wrap_lines("word " * 40, width=30, max_lines=3)
        self.assertEqual(len(lines), 3)
        self.assertTrue(lines[-1].endswith("…"))
        self.assertTrue(all(len(l) <= 31 for l in lines))

    def test_filter_uses_textfiles_not_inline_text(self):
        # A caption with quotes/colons can't break the filter graph: text goes via files.
        graph, last = social_clips.build_filter(
            {"headline": "C:\\tmp\\h.txt", "lines": ["C:\\tmp\\l0.txt"], "handle": "C:\\tmp\\x.txt"},
            has_badge=True)
        self.assertIn("textfile='C\\:/tmp/h.txt'", graph)
        self.assertIn("[1:v]scale=170:-1[badge]", graph)
        self.assertTrue(graph.endswith(f"[{last}]"))
        self.assertIn("1080:1920", graph)

    def test_render_reports_failure_instead_of_raising(self):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        with mock.patch.object(social_clips.subprocess, "run",
                               return_value=mock.Mock(returncode=1, stderr="boom\nInvalid data")):
            ok, msg = social_clips.render("in.mp4", os.path.join(tmp, "o.mp4"), "SIX!", "x", tmp)
        self.assertFalse(ok)
        self.assertIn("Invalid data", msg)


class MakeClipsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.files = []
        for i, name in enumerate(("Replay 1.mp4", "Replay 2.mp4", "Replay 3.mp4")):
            p = os.path.join(self.tmp, name)
            open(p, "wb").close()
            os.utime(p, (time.time() - 100 + i, time.time() - 100 + i))
            self.files.append(p)
        self.tags = {"Replay 1.mp4": {"reason": "Six", "caption": "SIX · North 31*"},
                     "Replay 2.mp4": {"reason": "Test", "caption": "TEST"},       # skipped
                     "Replay 3.mp4": {"reason": "Wicket", "caption": "WICKET · Smith"}}
        for p in (mock.patch.object(server, "clip_tags", return_value=self.tags),
                  mock.patch.object(server, "guess_clip_tags", return_value={}),
                  mock.patch.object(server.shutil, "which", return_value="ffmpeg"),
                  mock.patch.object(server, "load_state", return_value={"home_club_id": ""}),
                  mock.patch.object(server, "ai_clip_texts", return_value=("a line", "a post #c"))):
            p.start()
            self.addCleanup(p.stop)
        self.rendered = []

        def fake_render(src, out, headline, line, workdir, **kw):
            self.rendered.append((os.path.basename(src), headline))
            open(out, "wb").close()
            return True, out
        p = mock.patch.object(social_clips, "render", side_effect=fake_render)
        p.start()
        self.addCleanup(p.stop)

    def test_one_clip_per_tagged_moment_tests_skipped(self):
        ok, msg, made = server.make_social_clips(self.tmp)
        self.assertTrue(ok)
        self.assertEqual(self.rendered, [("Replay 1.mp4", "SIX!"), ("Replay 3.mp4", "WICKET!")])
        self.assertEqual(made, ["01_six.mp4", "02_wicket.mp4"])
        out_dir = server.social_clips_dir(self.tmp)
        with open(os.path.join(out_dir, "01_six.txt"), encoding="utf-8") as f:
            self.assertEqual(f.read().strip(), "a post #c")

    def test_rerun_keeps_existing_clips(self):
        server.make_social_clips(self.tmp)
        self.rendered.clear()
        ok, _, made = server.make_social_clips(self.tmp)
        self.assertEqual(self.rendered, [])            # nothing re-rendered
        self.assertEqual(len(made), 2)

    def test_no_ffmpeg_is_explained(self):
        with mock.patch.object(server.shutil, "which", return_value=None):
            ok, msg, _ = server.make_social_clips(self.tmp)
        self.assertFalse(ok)
        self.assertIn("FFmpeg", msg)


class CaptionTests(unittest.TestCase):
    def test_no_key_falls_back_to_the_tag(self):
        line, post = server.ai_clip_texts("SIX · North 31*", "Six", {})
        self.assertEqual(line, "SIX · North 31*")
        self.assertIn("#cricket", post)

    def test_prompt_never_assumes_a_side(self):
        fake = mock.MagicMock()
        fake.Anthropic.return_value.messages.create.return_value.content = [
            mock.Mock(text='{"line": "North hits a six", "post": "North! #cricket"}')]
        with mock.patch.dict(sys.modules, {"anthropic": fake}):
            line, post = server.ai_clip_texts("SIX · North 31*", "Six", {"anthropic_api_key": "k"})
        self.assertEqual((line, post), ("North hits a six", "North! #cricket"))
        prompt = fake.Anthropic.return_value.messages.create.call_args.kwargs["messages"][0]["content"]
        self.assertIn('never write "we"', prompt)

    def test_bad_json_from_the_model_falls_back(self):
        fake = mock.MagicMock()
        fake.Anthropic.return_value.messages.create.return_value.content = [mock.Mock(text="nope")]
        with mock.patch.dict(sys.modules, {"anthropic": fake}):
            line, _ = server.ai_clip_texts("SIX · North 31*", "Six", {"anthropic_api_key": "k"})
        self.assertEqual(line, "SIX · North 31*")


class EndpointTests(HttpTestBase):
    def test_list_and_download_and_traversal(self):
        out = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, out, True)
        with open(os.path.join(out, "01_six.mp4"), "wb") as f:
            f.write(b"\x00\x00video")
        with open(os.path.join(out, "01_six.txt"), "w", encoding="utf-8") as f:
            f.write("caption here\n")
        with mock.patch.object(server, "social_clips_dir", return_value=out):
            status, d = self.get_json("/social/clips")
            self.assertEqual(d["clips"], [{"name": "01_six.mp4", "caption": "caption here"}])
            status, headers, body = self.request("GET", "/social/clips/file/01_six.mp4")
            self.assertEqual((status, body), (200, b"\x00\x00video"))
            for bad in ("/social/clips/file/..%2F..%2Fconfig.ini", "/social/clips/file/01_six.txt"):
                self.assertEqual(self.request("GET", bad)[0], 404, bad)


if __name__ == "__main__":
    unittest.main()
