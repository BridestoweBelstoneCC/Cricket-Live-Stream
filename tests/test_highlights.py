"""Auto-tagged highlights: captioning, clip tagging, mtime-fallback correlation,
reel planning, and the chapters file — everything short of running ffmpeg itself."""
import datetime
import os
import shutil
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server


MATCH_STATE = {
    "battingTeamName": "Home CC", "score": 84, "wickets": 3, "overs": 18.2,
    "batter1": {"name": "SMITH", "runs": 34, "onStrike": True},
    "batter2": {"name": "JONES", "runs": 12, "onStrike": False},
    "lastWicketBatter": "WALKER 22", "lastWicketHowOut": "C JONES B HARRISON",
}


class TestMakeClipCaption(unittest.TestCase):
    def test_wicket_uses_dismissal_detail(self):
        cap = server.make_clip_caption("Wicket", MATCH_STATE)
        self.assertIn("WICKET", cap)
        self.assertIn("WALKER 22", cap)
        self.assertIn("C JONES B HARRISON", cap)
        self.assertIn("84-3 (18.2 ov)", cap)

    def test_boundary_and_six_use_the_striker(self):
        cap = server.make_clip_caption("Boundary", MATCH_STATE)
        self.assertIn("FOUR", cap)
        self.assertIn("SMITH 34*", cap)
        cap = server.make_clip_caption("Six", MATCH_STATE)
        self.assertIn("SIX", cap)

    def test_milestone_reasons(self):
        self.assertIn("CENTURY", server.make_clip_caption("Century - SMITH", MATCH_STATE))
        self.assertIn("SMITH", server.make_clip_caption("Century - SMITH", MATCH_STATE))
        self.assertIn("FIFTY", server.make_clip_caption("Fifty - JONES", MATCH_STATE))

    def test_test_reason_is_never_tagged(self):
        self.assertEqual(server.make_clip_caption("Test", MATCH_STATE), "")
        self.assertEqual(server.make_clip_caption("", MATCH_STATE), "")

    def test_survives_missing_state(self):
        cap = server.make_clip_caption("Boundary", None)
        self.assertIn("FOUR", cap)
        cap = server.make_clip_caption("Wicket", {})
        self.assertIn("WICKET", cap)


class ClipDbBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="clips_test_")
        self._orig_db_path = server._db_path
        server._db_path = lambda: os.path.join(self.tmp, "match_data.db")
        server.db_init()

    def tearDown(self):
        server._db_path = self._orig_db_path
        shutil.rmtree(self.tmp, ignore_errors=True)


class TestClipTagging(ClipDbBase):
    def test_log_and_read_back(self):
        from unittest import mock
        with mock.patch.object(server, "current_match_id", return_value="m1"):
            server.log_replay_clip("/replays/Replay_001.mkv", "Wicket", "WICKET · X")
            server.log_replay_clip("/replays/Replay_002.mkv", "Boundary", "FOUR · Y")
        tags = server.clip_tags(["/replays/Replay_001.mkv", "/replays/Replay_002.mkv",
                                 "/replays/untagged.mkv"])
        self.assertEqual(tags["Replay_001.mkv"]["reason"], "Wicket")
        self.assertEqual(tags["Replay_002.mkv"]["caption"], "FOUR · Y")
        self.assertNotIn("untagged.mkv", tags)

    def test_retag_replaces(self):
        from unittest import mock
        with mock.patch.object(server, "current_match_id", return_value="m1"):
            server.log_replay_clip("/r/a.mkv", "Boundary", "FOUR")
            server.log_replay_clip("/r/a.mkv", "Six", "SIX")
        self.assertEqual(server.clip_tags(["/r/a.mkv"])["a.mkv"]["reason"], "Six")

    def test_empty_caption_not_logged(self):
        server.log_replay_clip("/r/test.mkv", "Test", "")
        self.assertEqual(server.clip_tags(["/r/test.mkv"]), {})

    def test_never_raises_even_with_broken_db(self):
        server._db_path = lambda: "/nonexistent/dir/nope.db"
        server.log_replay_clip("/r/a.mkv", "Wicket", "WICKET")   # must not raise
        self.assertEqual(server.clip_tags(["/r/a.mkv"]), {})
        self.assertEqual(server.guess_clip_tags(["/r/a.mkv"]), {})


class TestGuessClipTags(ClipDbBase):
    def seed_ball(self, ts_epoch, outcome, is_wicket, batter="SMITH"):
        ts = datetime.datetime.fromtimestamp(ts_epoch).isoformat(timespec="seconds")
        with server._db_lock, server._db() as c:
            c.execute("INSERT OR REPLACE INTO balls VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                      ("m1", 1, 17, int(ts_epoch) % 6 + 1, "Home CC", batter, "JONES",
                       "HARRISON", outcome, 4 if outcome == "4" else 0, None,
                       int(is_wicket), 1, 80, 2, ts))

    def make_clip(self, name, mtime):
        path = os.path.join(self.tmp, name)
        with open(path, "wb") as f:
            f.write(b"clip")
        os.utime(path, (mtime, mtime))
        return path

    def test_correlates_by_mtime_preferring_wickets(self):
        now = time.time() - 3600
        self.seed_ball(now + 10, "4", 0)
        self.seed_ball(now + 20, "W", 1, batter="WALKER")
        clip = self.make_clip("manual_save.mkv", now + 30)
        tags = server.guess_clip_tags([clip])
        self.assertIn("manual_save.mkv", tags)
        self.assertIn("WICKET", tags["manual_save.mkv"])
        self.assertIn("WALKER", tags["manual_save.mkv"])

    def test_ignores_clips_outside_the_window(self):
        now = time.time() - 3600
        self.seed_ball(now, "4", 0)
        clip = self.make_clip("way_later.mkv", now + 600)
        self.assertEqual(server.guess_clip_tags([clip]), {})

    def test_dot_balls_never_produce_tags(self):
        now = time.time() - 3600
        self.seed_ball(now, "1", 0)      # not a wicket, not a boundary → not notable
        clip = self.make_clip("nothing.mkv", now + 5)
        self.assertEqual(server.guess_clip_tags([clip]), {})


class TestPlanAndChapters(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="plan_test_")

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def clip(self, name, mtime):
        p = os.path.join(self.tmp, name)
        open(p, "wb").close()
        os.utime(p, (mtime, mtime))
        return p

    def test_chronological_order_and_test_exclusion(self):
        t = time.time() - 1000
        c_late = self.clip("late.mkv", t + 100)
        c_test = self.clip("test.mkv", t + 50)
        c_early = self.clip("early.mkv", t)
        tags = {"late.mkv": {"reason": "Six", "caption": "SIX"},
                "test.mkv": {"reason": "Test", "caption": ""},
                "early.mkv": {"reason": "Wicket", "caption": "WICKET"}}
        plan = server.plan_highlights([c_late, c_test, c_early], tags)
        self.assertEqual([os.path.basename(e["file"]) for e in plan],
                         ["early.mkv", "late.mkv"])
        self.assertEqual(plan[0]["caption"], "WICKET")

    def test_untagged_clips_are_kept_without_caption(self):
        c = self.clip("mystery.mkv", time.time())
        plan = server.plan_highlights([c], {})
        self.assertEqual(len(plan), 1)
        self.assertEqual(plan[0]["caption"], "")

    def test_chapters_text_accumulates_timestamps(self):
        entries = [{"caption": "WICKET · WALKER", "duration": 18.0},
                   {"caption": "SIX · SMITH", "duration": 18.4},
                   {"caption": "", "duration": 20.0}]
        text = server.chapters_text(entries, title="Home v Rival — highlights")
        lines = text.strip().split("\n")
        self.assertEqual(lines[0], "Home v Rival — highlights")
        self.assertEqual(lines[2], "00:00 WICKET · WALKER")
        self.assertEqual(lines[3], "00:18 SIX · SMITH")
        self.assertEqual(lines[4], "00:36 Replay")          # untagged clips still listed

    def test_ff_filter_path_escapes_windows_drive_colons(self):
        self.assertEqual(server._ff_filter_path("C:\\Users\\x\\f.ttf"),
                         "C\\:/Users/x/f.ttf")
        self.assertEqual(server._ff_filter_path("/usr/share/f.ttf"), "/usr/share/f.ttf")


if __name__ == "__main__":
    unittest.main()
