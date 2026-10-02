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


class NewCheckTests(unittest.TestCase):
    def test_youtube_login_about_to_expire_warns_with_a_date(self):
        yt = mock.MagicMock()
        yt.channels.return_value.list.return_value.execute.return_value = {
            "items": [{"snippet": {"title": "Home and District Cricket Club"}}]}
        st = {"home_team": "Home and District CC", "youtube_signed_in_at": time.time() - 6 * 86400}
        with mock.patch.object(server, "YT_CREDS_FILE", __file__), \
                mock.patch.object(server, "_youtube_service", return_value=(yt, None)):
            status, detail, fix = server._pc_youtube(st)
        self.assertEqual(status, "warn")
        self.assertIn("6 days ago", detail)
        self.assertIn("Testing mode", fix)
        st["youtube_signed_in_at"] = time.time() - 86400
        with mock.patch.object(server, "YT_CREDS_FILE", __file__), \
                mock.patch.object(server, "_youtube_service", return_value=(yt, None)):
            self.assertEqual(server._pc_youtube(st)[0], "ok")

    def bitrate(self, ini_kbps, st, live=None):
        target = st.pop("bitrate_kbps", None)
        p = mock.patch.object(server, "_target_bitrate",
                              return_value=(target, "config") if target else (None, "manual"))
        p.start()
        self.addCleanup(p.stop)
        saved = dict(server._stream_mon)
        self.addCleanup(server._stream_mon.update, saved)
        server._stream_mon.update(reachable=live is not None, configured_kbps=live)
        values = {"Mode": "Simple", "VBitrate": str(ini_kbps)}
        with mock.patch("obs_prep.active_profile_ini", return_value="basic.ini"), \
                mock.patch("obs_prep._read_ini_value", side_effect=lambda p, sec, k: values.get(k)):
            return server._pc_obs_bitrate(st)

    def test_leftover_downshift_is_caught_with_obs_closed(self):
        status, detail, _ = self.bitrate(875, {"bitrate_kbps": 2500})
        self.assertEqual(status, "bad")
        self.assertIn("875", detail)

    def test_normal_bitrate_is_ok(self):
        self.assertEqual(self.bitrate(2500, {"bitrate_kbps": 2500})[0], "ok")

    def test_bitrate_above_upload_capacity_warns(self):
        self.assertEqual(self.bitrate(6000, {"bitrate_kbps": 2500, "network_test_mbps": 3.7})[0], "warn")

    def test_manual_bitrate_is_left_alone(self):
        self.assertEqual(self.bitrate(875, {})[0], "skip")

    def test_target_comes_from_config_ini(self):
        import tempfile
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d, True)
        ini = os.path.join(d, "config.ini")
        with mock.patch.object(server, "_config_ini_path", return_value=ini):
            for value, want in (("2500", (2500, "config")), ("manual", (None, "manual")),
                                ("", (None, "none"))):
                text = "[Stream]\nbitrate_kbps = " + value + "\n"
                with open(ini, "w") as f:
                    f.write(text)
                self.assertEqual(server._target_bitrate({}), want, text)
            self.assertEqual(server._target_bitrate({"network_test_mbps": 10})[1], "test")

    def test_live_obs_value_wins_over_the_file(self):
        self.assertEqual(self.bitrate(875, {"bitrate_kbps": 2500}, live=2500)[0], "ok")

    def test_cameras(self):
        self.assertEqual(server._pc_cameras({})[0], "skip")
        st = {"camera_rtsp_url": "rtsp://user:secret@192.0.2.10:554/stream"}
        with mock.patch.object(server.socket, "create_connection", side_effect=OSError):
            status, detail, fix = server._pc_cameras(st)
        self.assertEqual(status, "warn")                   # not ✗: days before it's expected
        self.assertNotIn("secret", detail + fix)           # the URL's password never shows
        self.assertNotIn("192.0.2.10", detail)
        with mock.patch.object(server.socket, "create_connection"):
            self.assertEqual(server._pc_cameras(st)[0], "ok")

    def surnames(self, lookup, roster=None):
        saved = dict(server._season_stats)
        self.addCleanup(server._season_stats.update, saved)
        server._season_stats["lookup"] = lookup
        return server._pc_surnames({"roster": roster or {}})

    def test_surname_clash_warns_until_the_roster_resolves_it(self):
        lookup = {"a": {"name": "Peter Smith"}, "b": {"name": "James Smith"},
                  "c": {"name": "Kian Burns"}}
        status, detail, _ = self.surnames(lookup)
        self.assertEqual(status, "warn")
        self.assertIn("James Smith, Peter Smith", detail)
        self.assertEqual(self.surnames(lookup, {"7": "James Smith", "21": "Peter Smith"})[0], "ok")
        self.assertEqual(self.surnames({})[0], "skip")

    def test_badge(self):
        self.assertEqual(server._pc_badge({})[0], "skip")
        self.assertEqual(server._pc_badge({"away_club_id": "999999999", "away_team": "X CC",
                                           "logos_folder": os.path.dirname(__file__)})[0], "warn")
        with mock.patch.object(server.os.path, "exists", return_value=True):
            self.assertEqual(server._pc_badge({"away_club_id": "123", "logos_folder": "x"})[0], "ok")

    def test_fixture(self):
        st = {"playcricket_api_key": "k", "home_club_id": "111"}
        with mock.patch.object(server, "fetch_todays_match",
                               return_value={"error": "No matches found for today"}):
            self.assertEqual(server._pc_fixture(st)[0], "skip")
        with mock.patch.object(server, "fetch_todays_match",
                               return_value={"away_club": "Rivals CC", "competition": "A Division"}):
            self.assertEqual(server._pc_fixture(st), ("ok", "Today: v Rivals CC (A Division).", ""))

    def test_upload(self):
        self.assertEqual(server._pc_upload({})[0], "skip")
        now = time.time()
        ok = {"network_test_mbps": 10, "network_test_at": now, "bitrate_kbps": 2500}
        self.assertEqual(server._pc_upload(ok)[0], "ok")
        self.assertEqual(server._pc_upload(dict(ok, network_test_at=now - 30 * 86400))[0], "warn")
        self.assertEqual(server._pc_upload(dict(ok, network_test_mbps=2))[0], "bad")

    def test_update_check(self):
        def release(tag):
            r = mock.MagicMock()
            r.__enter__.return_value.read.return_value = json.dumps({"tag_name": tag}).encode()
            return r
        with mock.patch.object(server, "_installed_version", return_value="2.10"):
            for tag, want in (("v2.11", "warn"), ("v2.10", "ok"), ("v2.9", "ok")):
                with mock.patch.object(server.urllib.request, "urlopen", return_value=release(tag)):
                    self.assertEqual(server._pc_update({})[0], want, tag)   # 2.10 > 2.9

    def test_installed_version_reads_the_changelog(self):
        self.assertRegex(server._installed_version() or "", r"^\d+\.\d+")

    def test_scorer_link(self):
        self.assertEqual(server._pc_scorer_link({})[0], "skip")
        with mock.patch.object(server, "read_agent_file", return_value=None), \
                mock.patch.object(server, "agent_status", return_value={"connected": False, "address": ""}):
            self.assertEqual(server._pc_scorer_link({"pcs_source": "agent"})[0], "bad")
        with mock.patch.object(server.urllib.request, "urlopen", side_effect=OSError):
            self.assertEqual(server._pc_scorer_link({"pcs_bridge_url": "http://100.1.2.3:5050"})[0], "bad")


class EndpointTests(HttpTestBase):
    def test_last_result_for_the_panel_badge(self):
        server._precheck_last["result"] = None
        status, _, data = self.request("GET", "/precheck/last")
        self.assertEqual((status, json.loads(data)["checks"]), (200, []))
        server._rate_limit_ts.clear()
        with mock.patch.object(server, "PRECHECKS", (("a", "A", _all("bad")),)):
            self.request("POST", "/precheck", body={})
        d = json.loads(self.request("GET", "/precheck/last")[2])
        self.assertFalse(d["ok"])
        self.assertEqual(d["checks"][0]["status"], "bad")

    def test_camera_buttons_in_the_pinned_bar(self):
        with open(os.path.join(os.path.dirname(os.path.dirname(__file__)), "control.html"),
                  encoding="utf-8") as f:
            html = f.read()
        bar = html[html.index('<div id="topbar">'):html.index('<div class="status" id="status">')]
        self.assertIn('data-cam="main"', bar)
        self.assertIn('data-cam="bowler"', bar)
        self.assertIn("querySelectorAll('.cam-btn[data-cam=", html)   # both buttons light up

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
