"""The control panel's match-day checklist (server.checklist_status / checklist_action).

Every item is detected, never ticked by hand, and carries the button for its step. OBS,
the process check and the score feed are mocked; the HTTP tests run the real Handler on
an ephemeral port like tests/test_http.py.
"""
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

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import obs_prep  # noqa: E402
import server    # noqa: E402

PLAYING = {"mediaState": "OBS_MEDIA_STATE_PLAYING"}


class Base(unittest.TestCase):
    def setUp(self):
        self.state = {"obs_host": "localhost", "obs_port": 4455, "away_team": "Opposition CC",
                      "camera_rtsp_url": "", "playcricket_api_key": ""}
        self.port_open, self.running = False, False
        self.obs = None                          # what _obs_call returns
        self.sent = []
        self.feed = (False, "No scoreboard file yet.")
        patches = [
            mock.patch.object(server, "load_state", side_effect=lambda: dict(self.state)),
            mock.patch.object(server, "save_state", side_effect=self.state.update),
            mock.patch.object(server, "_obs_call", side_effect=self.fake_obs),
            mock.patch.object(server, "score_feed_status", side_effect=lambda s, now=None: self.feed),
            mock.patch.object(obs_prep, "port_open", side_effect=lambda port: self.port_open),
            mock.patch.object(obs_prep, "obs_running", side_effect=lambda: self.running),
        ]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        saved = dict(server._checklist_job)
        self.addCleanup(server._checklist_job.update, saved)
        server._checklist_job.update({"action": None, "running": False, "ok": None,
                                      "messages": [], "finished": None})

    def fake_obs(self, st, reqs, timeout=6):
        self.sent.append([r[0] for r in reqs])
        if self.obs is None:
            return None
        return [self.obs.get(r[0]) for r in reqs]

    def items(self):
        return {i["id"]: i for i in server.checklist_status()["items"]}

    def obs_up(self, replay=True, live=False, media=PLAYING):
        self.port_open = True
        self.obs = {"GetReplayBufferStatus": {"outputActive": replay},
                    "GetStreamStatus": {"outputActive": live},
                    "GetMediaInputStatus": media}


class TestItems(Base):
    def test_obs_closed_offers_start(self):
        obs = self.items()["obs"]
        self.assertEqual((obs["state"], obs["action"]), ("todo", "start_obs"))

    def test_closed_obs_isnt_even_connected_to(self):
        # Port shut: no WebSocket attempt, so the panel isn't left waiting on a timeout.
        self.items()
        self.assertEqual(self.sent, [])

    def test_obs_open_but_not_answering(self):
        self.running = True
        obs = self.items()["obs"]
        self.assertIn("Safe Mode", obs["detail"])
        self.assertEqual(obs["action"], "start_obs")

    def test_obs_on_another_computer_has_no_start_button(self):
        self.state["obs_host"] = "192.168.1.50"
        obs = self.items()["obs"]
        self.assertEqual(obs["state"], "todo")
        self.assertIsNone(obs["action"])

    def test_obs_starting_shows_progress(self):
        server._checklist_job.update({"action": "start_obs", "running": True,
                                      "messages": ["Switched on OBS's WebSocket server."]})
        obs = self.items()["obs"]
        self.assertEqual(obs["state"], "busy")
        self.assertIn("WebSocket", obs["detail"])

    def test_everything_ready(self):
        self.state.update({"camera_rtsp_url": "rtsp://cam", "away_team": "Hatherleigh CC"})
        self.feed = (True, "Scoreboard updated 4s ago.")
        self.obs_up(replay=True, live=True)
        items = self.items()
        self.assertEqual({k: v["state"] for k, v in items.items()},
                         {"obs": "done", "camera": "done", "replay": "done", "match": "done",
                          "scorer": "done", "live": "done"})
        self.assertEqual(self.sent, [["GetReplayBufferStatus", "GetStreamStatus",
                                      "GetMediaInputStatus"]])   # one connection for all

    def test_no_camera_url_is_not_counted(self):
        # A camera added in OBS by hand is a perfectly good setup.
        self.obs_up()
        self.assertEqual(self.items()["camera"]["state"], "na")

    def test_camera_not_in_obs(self):
        self.state["camera_rtsp_url"] = "rtsp://cam"
        self.obs_up(media=None)
        cam = self.items()["camera"]
        self.assertEqual((cam["state"], cam["action"], cam["action_label"]),
                         ("todo", "add_camera", "Add to OBS"))

    def test_camera_not_playing(self):
        self.state["camera_rtsp_url"] = "rtsp://cam"
        self.obs_up(media={"mediaState": "OBS_MEDIA_STATE_ERROR"})
        cam = self.items()["camera"]
        self.assertEqual(cam["action_label"], "Reconnect")
        self.assertIn("error", cam["detail"])

    def test_replay_buffer_off(self):
        self.obs_up(replay=False)
        self.assertEqual(self.items()["replay"]["action"], "start_replay")

    def test_match_fetch_needs_a_playcricket_key(self):
        self.assertIsNone(self.items()["match"]["action"])
        self.state["playcricket_api_key"] = "k"
        self.assertEqual(self.items()["match"]["action"], "fetch_match")

    def test_two_laptop_mode_offers_find(self):
        self.state["pcs_source"] = "agent"
        self.assertEqual(self.items()["scorer"]["action"], "find_agent")

    def test_go_live_only_once_obs_is_up(self):
        self.assertIsNone(self.items()["live"]["action"])
        self.obs_up()
        self.assertEqual(self.items()["live"]["action"], "go_live")


class TestActions(Base):
    def test_go_live(self):
        self.obs_up(live=False)
        self.assertEqual(server.checklist_action("go_live")[0], 200)
        self.assertIn(["StartStream"], self.sent)

    def test_go_live_when_already_live_does_nothing(self):
        self.obs_up(live=True)
        server.checklist_action("go_live")
        self.assertNotIn(["StartStream"], self.sent)

    def test_refusals_are_reported_not_hidden(self):
        # OBS answering "no" used to come back as ok: true.
        self.obs_up(replay=False, live=False)
        self.obs["StartReplayBuffer"] = None
        self.assertFalse(server.checklist_action("start_replay")[1]["ok"])
        self.obs["StartStream"] = None
        out = server.checklist_action("go_live")[1]
        self.assertFalse(out["ok"])
        self.assertIn("stream key", out["error"])

    def test_start_replay(self):
        self.obs_up(replay=False)
        self.obs["StartReplayBuffer"] = {}
        self.assertTrue(server.checklist_action("start_replay")[1]["ok"])
        self.assertIn(["StartReplayBuffer"], self.sent)

    def test_start_obs_runs_once_at_a_time(self):
        with mock.patch.object(server, "_checklist_run_start_obs"):
            self.assertEqual(server.checklist_action("start_obs")[0], 202)
            self.assertEqual(server.checklist_action("start_obs")[0], 409)

    def test_unknown_action(self):
        self.assertEqual(server.checklist_action("format_the_laptop")[0], 400)

    def test_start_obs_job_adopts_the_new_password(self):
        # prepare_obs can write a fresh WebSocket password into config.ini; without
        # following it, every later OBS call from the server fails to authenticate.
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        cfg = os.path.join(tmp, "config.ini")
        with open(cfg, "w", encoding="utf-8") as f:
            f.write("[OBS]\nobs_password = fresh-pw\n")
        import obs_setup
        with mock.patch.object(server, "_config_ini_path", return_value=cfg), \
             mock.patch.object(obs_prep, "prepare_obs", return_value=True) as prep, \
             mock.patch.object(obs_setup, "setup_from_config", return_value=(True, ["✓ Ready"])):
            server._checklist_job["running"] = True
            server._checklist_run_start_obs()
        prep.assert_called_once()
        self.assertEqual(self.state["obs_password"], "fresh-pw")
        self.assertFalse(server._checklist_job["running"])
        self.assertTrue(server._checklist_job["ok"])


class TestStartObsPassword(Base):
    """From the code review: Start OBS must never replace the panel's working password."""

    def run_job(self, config_text):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        cfg = os.path.join(tmp, "config.ini")
        with open(cfg, "w", encoding="utf-8") as f:
            f.write(config_text)
        import obs_setup
        self.state["obs_password"] = "typed-in-the-panel"
        with mock.patch.object(server, "_config_ini_path", return_value=cfg), \
             mock.patch.object(obs_prep, "prepare_obs", return_value=True), \
             mock.patch.object(obs_setup, "setup_from_config", return_value=(True, [])):
            server._checklist_job["running"] = True
            server._checklist_run_start_obs()
        return self.state["obs_password"]

    def test_blank_config_password_is_not_copied_over_a_working_one(self):
        self.assertEqual(self.run_job("[OBS]\nobs_password =\n"), "typed-in-the-panel")

    def test_unmanaged_obs_keeps_the_panel_password(self):
        self.assertEqual(self.run_job("[OBS]\nobs_password = other\nmanage_obs = no\n"),
                         "typed-in-the-panel")


class TestHttp(Base):
    def setUp(self):
        super().setUp()
        self.httpd = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(self.httpd.server_close)
        self.addCleanup(self.httpd.shutdown)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        pw = mock.patch.object(server, "_CLUB_PASSWORD", "secret")
        pw.start()
        self.addCleanup(pw.stop)

    def status(self, path, body=None):
        req = urllib.request.Request(self.base + path, method="POST" if body else "GET",
                                     data=json.dumps(body).encode() if body else None,
                                     headers={"Content-Type": "application/json",
                                              "Origin": self.base})
        try:
            with urllib.request.urlopen(req, timeout=5) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code

    def test_reading_it_needs_a_login(self):
        self.assertEqual(self.status("/checklist"), 401)

    def test_its_buttons_need_a_login_even_from_this_machine(self):
        # These start OBS and go live. Unlike the overlay's endpoints, no loopback pass.
        self.assertIn(self.status("/checklist/action", {"action": "go_live"}), (401, 403))
        self.assertNotIn(["StartStream"], self.sent)


class TestPanel(unittest.TestCase):
    def setUp(self):
        with open(os.path.join(REPO, "control.html"), encoding="utf-8") as f:
            self.src = f.read()

    def test_no_manual_ticks_left(self):
        for gone in ("bbcc_checklist", "toggleCheck", "resetChecklist", "buildChecklist"):
            self.assertNotIn(gone, self.src)

    def test_going_live_asks_first(self):
        fn = self.src[self.src.index("async function runChecklistAction("):]
        self.assertIn("action === 'go_live' && !confirm(", fn[:500])

    def test_refreshed_after_login(self):
        self.assertEqual(self.src.count("refreshChecklist();        // same: /checklist is token-gated"), 2)


if __name__ == "__main__":
    unittest.main()
