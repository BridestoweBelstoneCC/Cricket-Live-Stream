"""The AI camera spotter: Claude looks at each camera while live and flags what a viewer
would notice; a frozen picture is caught without AI. OBS and the API are faked here; the
real thing was checked against test-pattern cameras and fogged/tilted match photos."""
import json
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
from tests.test_http import HttpTestBase

ST = {"obs_camera_name": "Wide Camera", "obs_camera2_name": "Bowler End Camera",
      "camera2_rtsp_url": "rtsp://cam2", "anthropic_api_key": "k"}


class SpotterTests(unittest.TestCase):
    def setUp(self):
        saved = {k: (dict(v) if isinstance(v, dict) else v) for k, v in server._spotter.items()}
        self.addCleanup(server._spotter.update, saved)
        server._spotter.update(cameras={}, last_run=0.0, running=False)

    def run_spot(self, frames, ai=(True, ""), use_ai=True):
        frames = iter(frames)
        with mock.patch.object(server, "_obs_frame", side_effect=lambda st, s: next(frames)), \
                mock.patch.object(server, "_ai_look", return_value=ai) as look:
            cams = server.spot_cameras(ST, use_ai=use_ai)
        return cams, look

    def test_both_cameras_checked(self):
        self.assertEqual([n for _, n in server._spotter_cameras(ST)], ["Wide Camera", "Bowler End Camera"])
        self.assertEqual(len(server._spotter_cameras(dict(ST, camera2_rtsp_url=""))), 1)

    def test_problem_reported(self):
        cams, _ = self.run_spot([b"a", b"b"], ai=(False, "Lens fogged"))
        self.assertEqual(cams["Wide Camera"]["problem"], "Lens fogged")
        self.assertEqual(server.spotter_status()["problems"], 2)

    def test_frozen_caught_without_ai(self):
        self.run_spot([b"same", b"x"])
        cams, look = self.run_spot([b"same", b"y"])
        self.assertIn("frozen", cams["Wide Camera"]["problem"])
        self.assertEqual(cams["Wide Camera"]["how"], "frozen")
        self.assertEqual(look.call_count, 1)          # only the camera that wasn't frozen

    def test_no_picture_from_obs(self):
        cams, look = self.run_spot([None, None])
        self.assertEqual(cams["Wide Camera"]["problem"], "No picture from OBS")
        look.assert_not_called()

    def test_ai_failure_is_not_a_camera_problem(self):
        with mock.patch.object(server, "_obs_frame", return_value=b"f"), \
                mock.patch.object(server, "_ai_look", side_effect=RuntimeError("rate limited")):
            cams = server.spot_cameras(ST)
        self.assertTrue(cams["Wide Camera"]["ok"])
        self.assertIn("Couldn't check", cams["Wide Camera"]["problem"])

    def test_schedule(self):
        on = dict(ST, camera_spotter=True, camera_spotter_minutes=5)
        self.assertTrue(server.spotter_due(on, live=True, now=1000))
        self.assertFalse(server.spotter_due(on, live=False, now=1000))       # never off-air
        self.assertFalse(server.spotter_due(dict(on, camera_spotter=False), True, 1000))
        self.assertFalse(server.spotter_due(dict(on, anthropic_api_key=""), True, 1000))
        server._spotter["last_run"] = 1000
        self.assertFalse(server.spotter_due(on, True, now=1000 + 299))
        self.assertTrue(server.spotter_due(on, True, now=1000 + 300))

    def test_ai_look_sends_the_frame_and_parses_json(self):
        fake = mock.MagicMock()
        fake.Anthropic.return_value.messages.create.return_value.content = [
            mock.Mock(text='```json\n{"ok": false, "problem": "Camera knocked"}\n```')]
        with mock.patch.dict(sys.modules, {"anthropic": fake}):
            self.assertEqual(server._ai_look(ST, b"\xff\xd8jpeg"), (False, "Camera knocked"))
        kwargs = fake.Anthropic.return_value.messages.create.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")
        image = kwargs["messages"][0]["content"][0]
        self.assertEqual((image["type"], image["source"]["media_type"]), ("image", "image/jpeg"))


class EndpointTests(HttpTestBase):
    def test_check_now_and_status(self):
        server._rate_limit_ts.clear()
        server.update_state(lambda s: s.update(obs_camera_name="Cam", camera2_rtsp_url="",
                                               anthropic_api_key=""))
        server._spotter.update(cameras={}, last_run=0.0, running=False)
        with mock.patch.object(server, "_obs_frame", return_value=b"f"), \
                mock.patch.object(server, "_ai_look") as look:
            status, _, data = self.request("POST", "/camera/spotter", body={})
        d = json.loads(data)
        self.assertEqual(status, 200)
        self.assertFalse(d["used_ai"])                 # no key: frozen check only, no AI call
        look.assert_not_called()
        self.assertEqual(d["cameras"][0]["label"], "Wide camera")
        self.assertNotIn("digest", d["cameras"][0])
        status, d2 = self.get_json("/camera/spotter")
        self.assertEqual(status, 200)


if __name__ == "__main__":
    unittest.main()
