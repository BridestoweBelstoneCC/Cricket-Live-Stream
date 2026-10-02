"""Sponsor airtime: how long the weekend sponsor's strap was on screen while live.

The overlay beacons show/hide; the server times them on its own clock, takes the name
from state, and counts live time separately from time before going live. The HTTP tests
drive the real Handler on an ephemeral port, like tests/test_http.py.
"""
import datetime
import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.request
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import server  # noqa: E402

T0 = 1791000000.0     # a fixed clock, so every interval lands on one known day
DAY = datetime.date.fromtimestamp(T0).isoformat()


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.state = {"sponsor_name": "Acme Builders"}
        for target, kw in [
            (mock.patch.object(server, "SPONSOR_AIRTIME_FILE",
                               os.path.join(self.tmp, "sponsor_airtime.json")), {}),
            (mock.patch.object(server, "load_state"), {}),
        ]:
            target.start()
            self.addCleanup(target.stop)
        server.load_state.side_effect = lambda: dict(self.state)
        saved_open = server._sponsor_air["open"]
        saved_live = server._stream_mon["streaming"]
        self.addCleanup(server._sponsor_air.__setitem__, "open", saved_open)
        self.addCleanup(server._stream_mon.__setitem__, "streaming", saved_live)
        server._sponsor_air["open"] = None
        self.live(True)

    def live(self, on):
        server._stream_mon["streaming"] = on

    def viewers(self, count, at):
        saved = dict(server._viewers)
        self.addCleanup(server._viewers.update, saved)
        server._viewers.update(count=count, at=at)

    def show_for(self, start, secs):
        server.sponsor_airtime_event("show", now=T0 + start)
        server.sponsor_airtime_event("hide", now=T0 + start + secs)

    def today(self):
        s = server.sponsor_airtime_summary(DAY)["sponsors"]
        return s[0] if s else None


class TestTiming(Base):
    def test_live_appearances_add_up(self):
        self.show_for(0, 7)
        self.show_for(60, 5)
        self.show_for(120, 9)
        rec = self.today()
        self.assertEqual((rec["on_air_sec"], rec["on_air_shows"]), (21.0, 3))
        self.assertIn("Acme Builders was on screen for 21 s of the live stream, "
                      "across 3 appearances.", rec["line"])

    def test_minutes_in_the_line(self):
        for i in range(20):
            self.show_for(i * 60, 9)
        self.assertIn("3 min 0 s", self.today()["line"])

    def test_before_going_live_is_counted_separately(self):
        # A rehearsal: on screen, but nobody was watching. Never presented as airtime.
        self.live(False)
        self.show_for(0, 7)
        self.live(True)
        self.show_for(60, 5)
        rec = self.today()
        self.assertEqual((rec["on_air_sec"], rec["on_air_shows"]), (5.0, 1))
        self.assertEqual((rec["off_air_sec"], rec["off_air_shows"]), (7.0, 1))

    def test_going_live_mid_appearance_counts(self):
        self.live(False)
        server.sponsor_airtime_event("show", now=T0)
        self.live(True)
        server.sponsor_airtime_event("hide", now=T0 + 6)
        self.assertEqual(self.today()["on_air_shows"], 1)

    def test_missed_hide_is_capped(self):
        # Overlay reloaded mid-panel: the next "show" closes the old one, at most 60s.
        server.sponsor_airtime_event("show", now=T0)
        server.sponsor_airtime_event("show", now=T0 + 3600)
        server.sponsor_airtime_event("hide", now=T0 + 3607)
        rec = self.today()
        self.assertEqual(rec["on_air_sec"], server.SPONSOR_SHOW_MAX_SEC + 7)
        self.assertEqual(rec["on_air_shows"], 2)

    def test_stray_hide_counts_nothing(self):
        server.sponsor_airtime_event("hide", now=T0)
        self.assertIsNone(self.today())

    def test_no_sponsor_set_counts_nothing(self):
        self.state["sponsor_name"] = ""
        self.show_for(0, 7)
        self.assertIsNone(self.today())

    def test_viewer_minutes_from_the_live_viewer_count(self):
        self.viewers(40, at=T0)
        self.show_for(0, 9)
        self.show_for(60, 6)
        rec = self.today()
        self.assertEqual((rec["viewer_minutes"], rec["viewer_covered_sec"]), (10, 15.0))
        self.assertIn("an estimated 10 viewer-minutes (YouTube's live viewer count × time "
                      "on screen).", rec["line"])

    def test_stale_viewer_count_is_not_used(self):
        self.viewers(40, at=T0 - 1000)
        self.show_for(0, 9)
        rec = self.today()
        self.assertEqual(rec["viewer_minutes"], 0)
        self.assertTrue(rec["line"].endswith("across 1 appearance."))

    def test_partial_coverage_is_said_not_scaled_up(self):
        self.viewers(60, at=T0)
        self.show_for(0, 9)             # count is fresh
        self.show_for(1000, 6)          # count is 16 minutes old by now
        rec = self.today()
        self.assertEqual(rec["viewer_minutes"], 9)
        self.assertIn("an estimated 9 viewer-minutes over the 9 s of it that had a YouTube "
                      "viewer count.", rec["line"])

    def test_viewers_never_counted_before_going_live(self):
        self.live(False)
        self.viewers(40, at=T0)
        self.show_for(0, 9)
        self.assertEqual(self.today()["viewer_minutes"], 0)

    def test_survives_a_server_restart(self):
        # Totals are on disk, not just in memory: quickstart restarts a crashed server.
        self.show_for(0, 7)
        server._sponsor_air["open"] = None
        self.show_for(60, 5)
        self.assertEqual(self.today()["on_air_shows"], 2)

    def test_never_raises(self):
        server.load_state.side_effect = RuntimeError("state unreadable")
        server.sponsor_airtime_event("show")      # must not raise into the handler


class TestHttp(Base):
    def setUp(self):
        super().setUp()
        self.httpd = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(self.httpd.server_close)
        self.addCleanup(self.httpd.shutdown)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def post(self, body, origin=True):
        headers = {"Content-Type": "application/json"}
        if origin:
            headers["Origin"] = self.base
        req = urllib.request.Request(self.base + "/sponsor/airtime", data=json.dumps(body).encode(),
                                     headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=5) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code

    def test_overlay_beacon_needs_no_login(self):
        # The overlay is a loopback OBS source with no login flow.
        with mock.patch.object(server, "_CLUB_PASSWORD", "secret"):
            self.assertEqual(self.post({"event": "show"}), 200)

    def test_bad_event_rejected(self):
        self.assertEqual(self.post({"event": "credit-me-an-hour"}), 400)

    def test_summary_is_not_swallowed_by_the_logo_route(self):
        self.show_for(0, 7)
        with urllib.request.urlopen(f"{self.base}/sponsor/airtime?date={DAY}", timeout=5) as r:
            data = json.loads(r.read().decode())
        self.assertEqual(data["sponsors"][0]["on_air_shows"], 1)


class TestOverlayBeacon(unittest.TestCase):
    def setUp(self):
        with open(os.path.join(REPO, "overlay.html"), encoding="utf-8") as f:
            self.src = f.read()

    def test_show_and_hide_beacon_only_on_a_real_change(self):
        show = self.src[self.src.index("function showSponsorStripFor("):]
        self.assertIn("if (!s.classList.contains('show')) sponsorAirtime('show');", show[:600])
        fade = self.src[self.src.index("function fadeSponsorStrap("):]
        self.assertIn("sponsorAirtime('hide')", fade[:300])

    def test_never_from_preview_mode(self):
        fn = self.src[self.src.index("function sponsorAirtime("):]
        self.assertIn("if (SPONSOR_PREVIEW) return;", fn[:120])


if __name__ == "__main__":
    unittest.main()


class TestViewerSampler(unittest.TestCase):
    """The once-a-minute YouTube viewer count, against a fake YouTube client."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        creds, token = os.path.join(self.tmp, "c.json"), os.path.join(self.tmp, "t.json")
        for f in (creds, token):
            open(f, "w").close()
        for p in (mock.patch.object(server, "YT_CREDS_FILE", creds),
                  mock.patch.object(server, "YT_TOKEN_FILE", token)):
            p.start()
            self.addCleanup(p.stop)
        saved, saved_live = dict(server._viewers), server._stream_mon["streaming"]
        self.addCleanup(server._viewers.update, saved)
        self.addCleanup(server._stream_mon.__setitem__, "streaming", saved_live)
        self.yt = mock.MagicMock()
        server._viewers.update(count=None, at=0.0, video=None, yt=self.yt, error=None)
        server._stream_mon["streaming"] = True

    def broadcasts(self, items):
        self.yt.liveBroadcasts.return_value.list.return_value.execute.return_value = {"items": items}

    def video(self, details):
        self.yt.videos.return_value.list.return_value.execute.return_value =             {"items": [{"liveStreamingDetails": details}]}

    def test_samples_the_active_broadcast(self):
        self.broadcasts([{"id": "vid1"}])
        self.video({"concurrentViewers": "57"})
        server._viewer_tick(now=T0)
        self.assertEqual(server.live_viewers(now=T0 + 30), 57)
        kw = self.yt.liveBroadcasts.return_value.list.call_args.kwargs
        self.assertEqual(kw["broadcastStatus"], "active")      # never a guessed past stream
        self.yt.videos.return_value.list.assert_called_with(part="liveStreamingDetails", id="vid1")

    def test_no_active_broadcast_no_count(self):
        self.broadcasts([])
        server._viewer_tick(now=T0)
        self.assertIsNone(server.live_viewers(now=T0))
        self.yt.videos.assert_not_called()

    def test_not_live_makes_no_api_calls(self):
        server._stream_mon["streaming"] = False
        server._viewers["video"] = "old"
        server._viewer_tick(now=T0)
        self.yt.liveBroadcasts.assert_not_called()
        self.assertIsNone(server._viewers["video"])

    def test_ended_broadcast_is_forgotten(self):
        server._viewers["video"] = "vid1"
        self.video({"actualEndTime": "2026-10-02T17:00:00Z"})
        server._viewer_tick(now=T0)
        self.assertIsNone(server._viewers["video"])
        self.assertIsNone(server.live_viewers(now=T0))

    def test_youtube_not_set_up_does_nothing(self):
        os.remove(server.YT_TOKEN_FILE)
        server._viewer_tick(now=T0)
        self.yt.liveBroadcasts.assert_not_called()
