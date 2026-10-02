"""The control panel's pre-match check: every credential and connection tried for real,
in parallel, each answering ok / warn / bad with what to do. Built after the YouTube login
on the maintainer's laptop turned out to have expired silently."""
import json
import os
import sys
import time
import unittest
import urllib.error
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server
from tests.test_http import HttpTestBase


def _all(status):
    return lambda st: (status, f"{status} detail", "")


class RunPrecheckTests(unittest.TestCase):
    def run_with(self, checks, timeout=None):
        with mock.patch.object(server, "PRECHECKS", checks), \
                mock.patch.object(server, "load_state", return_value={}), \
                mock.patch.object(server, "PRECHECK_TIMEOUT_SEC", timeout or 20):
            return server.run_precheck()

    def test_all_good(self):
        r = self.run_with((("a", "A", _all("ok")), ("b", "B", _all("warn"))))
        self.assertTrue(r["ok"])                      # a warning isn't a failure
        self.assertEqual([c["status"] for c in r["checks"]], ["ok", "warn"])

    def test_one_failure_fails_overall(self):
        r = self.run_with((("a", "A", _all("ok")), ("b", "B", _all("bad"))))
        self.assertFalse(r["ok"])

    def test_a_crashing_check_is_reported_not_raised(self):
        def boom(st):
            raise RuntimeError("socket exploded")
        r = self.run_with((("a", "A", boom), ("b", "B", _all("ok"))))
        self.assertEqual(r["checks"][0]["status"], "bad")
        self.assertIn("socket exploded", r["checks"][0]["detail"])
        self.assertEqual(r["checks"][1]["status"], "ok")   # the others still answer

    def test_a_hung_check_times_out_and_the_rest_still_answer(self):
        def slow(st):
            time.sleep(3)
            return ("ok", "", "")
        t = time.time()
        r = self.run_with((("a", "A", slow), ("b", "B", _all("ok"))), timeout=0.5)
        self.assertLess(time.time() - t, 2.5)
        self.assertEqual(r["checks"][0]["status"], "bad")
        self.assertIn("No answer", r["checks"][0]["detail"])
        self.assertEqual(r["checks"][1]["status"], "ok")


class CheckTests(unittest.TestCase):
    st = {"home_team": "Home and District CC", "home_club_id": "111",
          "playcricket_api_key": "k", "anthropic_api_key": "a"}

    def test_youtube_expired_login_is_bad_with_the_reason(self):
        with mock.patch.object(server, "YT_CREDS_FILE", __file__), \
                mock.patch.object(server, "_youtube_service",
                                  return_value=(None, "YouTube's login has expired — …")):
            status, detail, fix = server._pc_youtube(self.st)
        self.assertEqual(status, "bad")
        self.assertIn("expired", detail)

    def test_youtube_wrong_channel_is_a_warning(self):
        yt = mock.MagicMock()
        yt.channels.return_value.list.return_value.execute.return_value = {
            "items": [{"snippet": {"title": "Somebody's Holiday Videos"}}]}
        with mock.patch.object(server, "YT_CREDS_FILE", __file__), \
                mock.patch.object(server, "_youtube_service", return_value=(yt, None)):
            status, detail, _ = server._pc_youtube(self.st)
        self.assertEqual(status, "warn")
        self.assertIn("Holiday", detail)

    def test_youtube_club_channel_is_ok(self):
        yt = mock.MagicMock()
        yt.channels.return_value.list.return_value.execute.return_value = {
            "items": [{"snippet": {"title": "Home and District Cricket Club"}}]}
        with mock.patch.object(server, "YT_CREDS_FILE", __file__), \
                mock.patch.object(server, "_youtube_service", return_value=(yt, None)):
            self.assertEqual(server._pc_youtube(self.st)[0], "ok")

    def test_playcricket_rejected_key(self):
        err = urllib.error.HTTPError("u", 401, "Unauthorized", {}, None)
        with mock.patch.object(server, "_pc_get_json", side_effect=err):
            status, detail, _ = server._pc_playcricket(self.st)
        self.assertEqual(status, "bad")
        self.assertIn("rejected", detail)

    def test_playcricket_works(self):
        with mock.patch.object(server, "_pc_get_json", return_value={"matches": [1, 2, 3]}):
            status, detail, _ = server._pc_playcricket(self.st)
        self.assertEqual((status, detail), ("ok", "Key works — 3 fixtures this season."))

    def test_missing_optional_keys_are_warnings_not_failures(self):
        st = {"home_team": "X CC"}
        with mock.patch.dict(os.environ, {"ANTHROPIC_API_KEY": ""}):
            self.assertEqual(server._pc_anthropic(st)[0], "warn")
        self.assertEqual(server._pc_playcricket(st)[0], "warn")

    def test_anthropic_rejected_key(self):
        fake = mock.MagicMock()

        class AuthErr(Exception):
            pass
        fake.AuthenticationError = AuthErr
        fake.PermissionDeniedError = type("PermErr", (Exception,), {})
        fake.Anthropic.return_value.models.list.side_effect = AuthErr()
        with mock.patch.dict(sys.modules, {"anthropic": fake}):
            self.assertEqual(server._pc_anthropic(self.st)[0], "bad")

    def test_obs_closed_is_fine_days_before(self):
        with mock.patch("obs_prep.port_open", return_value=False):
            self.assertEqual(server._pc_obs({"obs_host": "localhost"})[0], "warn")

    def test_obs_open_but_password_wrong(self):
        with mock.patch("obs_prep.port_open", return_value=True), \
                mock.patch.object(server, "_obs_call", return_value=None):
            status, _, fix = server._pc_obs({"obs_host": "localhost"})
        self.assertEqual(status, "bad")
        self.assertIn("password", fix)

    def test_disk_space_thresholds(self):
        for free_gb, want in ((50, "ok"), (5, "warn"), (1, "bad")):
            usage = mock.MagicMock(free=free_gb * 1e9)
            with mock.patch.object(server.shutil, "disk_usage", return_value=usage):
                self.assertEqual(server._pc_disk({"replay_folder": os.path.dirname(__file__)})[0],
                                 want, free_gb)

    def test_disk_check_copes_with_a_folder_that_doesnt_exist_yet(self):
        folder = os.path.join(os.path.dirname(__file__), "no", "such", "replays")
        status, detail, _ = server._pc_disk({"replay_folder": folder})
        self.assertIn("doesn't exist yet", detail)


class EndpointTests(HttpTestBase):
    def test_runs_and_is_rate_limited(self):
        server._rate_limit_ts.clear()
        fake = (("a", "A", _all("ok")),)
        with mock.patch.object(server, "PRECHECKS", fake):
            status, _, data = self.request("POST", "/precheck", body={})
            self.assertEqual(status, 200)
            self.assertEqual(json.loads(data)["checks"][0]["name"], "A")
            self.assertEqual(self.request("POST", "/precheck", body={})[0], 429)

    def test_panel_has_the_button(self):
        with open(os.path.join(os.path.dirname(os.path.dirname(__file__)), "control.html"),
                  encoding="utf-8") as f:
            html = f.read()
        self.assertIn('onclick="runPrecheck()"', html)
        self.assertIn("apiFetch('/precheck'", html)


if __name__ == "__main__":
    unittest.main()
