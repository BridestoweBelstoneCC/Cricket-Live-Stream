"""Integration tests against a real in-process server instance.

Spins up server.Handler on an ephemeral port with STATE_FILE and the ball-by-ball DB
pointed at a temp dir, then exercises routes, auth, secret redaction, path traversal,
the origin (CSRF) check, the overlay loopback carve-out, and the /live PCS pipeline
(including event buffering and DB logging) — everything short of OBS/PlayCricket/AI,
which need the real external services.
"""
import http.client
import json
import os
import shutil
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from http.server import ThreadingHTTPServer
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server


class HttpTestBase(unittest.TestCase):
    """One server per subclass; module globals patched to a temp sandbox and restored."""
    CLUB_PASSWORD = ""    # subclasses override to test with auth enabled

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix="cricket_test_")
        cls._saved = {
            "STATE_FILE": server.STATE_FILE,
            "_db_path": server._db_path,
            "_CLUB_PASSWORD": server._CLUB_PASSWORD,
            "_CONTROL_TOKEN": server._CONTROL_TOKEN,
            "_last_good_state": server._last_good_state,
        }
        server.STATE_FILE = os.path.join(cls.tmp, "match_state.json")
        server._db_path = lambda: os.path.join(cls.tmp, "match_data.db")
        server._CLUB_PASSWORD = cls.CLUB_PASSWORD
        server._CONTROL_TOKEN = "cd" * 32
        server._last_good_state = None
        server.save_state(dict(server.DEFAULT_STATE))
        server.db_init()
        cls.httpd = ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        cls.port = cls.httpd.server_address[1]
        threading.Thread(target=cls.httpd.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        for k, v in cls._saved.items():
            setattr(server, k, v)
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def request(self, method, path, body=None, headers=None):
        """Raw request via http.client so traversal paths reach the server unnormalized."""
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        try:
            h = dict(headers or {})
            payload = None
            if body is not None:
                payload = body if isinstance(body, (bytes, str)) else json.dumps(body)
                h.setdefault("Content-Type", "application/json")
            try:
                conn.request(method, path, body=payload, headers=h)
            except BrokenPipeError:
                pass   # server rejected early (e.g. 413 before reading a huge body)
            resp = conn.getresponse()
            data = resp.read()
            return resp.status, dict(resp.getheaders()), data
        finally:
            conn.close()

    def get_json(self, path, headers=None):
        status, _, data = self.request("GET", path, headers=headers)
        return status, json.loads(data)

    def post_json(self, path, body, headers=None):
        status, _, data = self.request("POST", path, body=body, headers=headers)
        try:
            return status, json.loads(data)
        except (json.JSONDecodeError, ValueError):
            return status, {}


class TestRoutesOpen(HttpTestBase):
    """Auth disabled (no club_password) — the localhost default."""

    def setUp(self):
        server._last_good_state = None
        server.save_state(dict(server.DEFAULT_STATE))

    def test_core_pages_serve(self):
        for path, expect in (("/", b"<!DOCTYPE html"), ("/overlay", b"<!DOCTYPE html"),
                             ("/control", b"<!DOCTYPE html")):
            status, _, data = self.request("GET", path)
            self.assertEqual(status, 200, path)
            self.assertTrue(data.startswith(expect), path)

    def test_control_panel_served_from_file_with_presets_injected(self):
        status, _, data = self.request("GET", "/control")
        self.assertEqual(status, 200)
        self.assertNotIn(b"__KIT_PRESETS__", data)     # placeholder replaced at serve time
        self.assertIn(b'"Navy"', data)                 # ...with the real kit presets
        self.assertIn(b"apiFetch", data)               # the big script block made it intact

    def test_unknown_routes_404(self):
        for path in ("/nope", "/commentary/test", "/commentary/over/generate"):
            status, _, _ = self.request("GET", path)
            self.assertEqual(status, 404, f"GET {path} should 404 (POST-only or removed)")

    def test_diagnostics_respond(self):
        for path in ("/health", "/status", "/data/status", "/pcs/debug", "/logos/debug",
                     "/highlights/status"):
            status, body = self.get_json(path)
            self.assertEqual(status, 200, path)
        status, body = self.get_json("/health")
        self.assertTrue(body["ok"])
        self.assertIn("pcs", body)
        # self-metrics: the "is the server staying lightweight?" number, plus the
        # error flight recorder (empty on a healthy server)
        self.assertIn("max_rss_mb", body["server"])
        self.assertIsInstance(body["errors"], list)

    def test_options_preflight(self):
        status, headers, _ = self.request("OPTIONS", "/state")
        self.assertEqual(status, 200)
        self.assertIn("Authorization", headers.get("Access-Control-Allow-Headers", ""))

    def test_state_secret_redaction_and_sentinel_roundtrip(self):
        st = server.load_state()
        st["anthropic_api_key"] = "sk-ant-SUPERSECRET"
        st["youtube_stream_key"] = "abcd-STREAMKEY-1234"   # anyone with this can stream
        server.save_state(st)

        status, body = self.get_json("/state")
        self.assertEqual(status, 200)
        self.assertEqual(body["anthropic_api_key"], server.SECRET_SENTINEL)
        self.assertEqual(body["youtube_stream_key"], server.SECRET_SENTINEL)
        self.assertTrue(body["anthropic_key_set"])
        self.assertNotIn("SUPERSECRET", json.dumps(body))
        self.assertNotIn("STREAMKEY", json.dumps(body))

        # Posting the sentinel back must leave the stored key untouched
        status, _ = self.post_json("/state", {"anthropic_api_key": server.SECRET_SENTINEL,
                                              "home_team": "Test CC"})
        self.assertEqual(status, 200)
        on_disk = server.load_state()
        self.assertEqual(on_disk["anthropic_api_key"], "sk-ant-SUPERSECRET")
        self.assertEqual(on_disk["home_team"], "Test CC")

        # Posting empty string clears it
        status, _ = self.post_json("/state", {"anthropic_api_key": ""})
        self.assertEqual(status, 200)
        self.assertEqual(server.load_state()["anthropic_api_key"], "")

    def test_state_post_merges_instead_of_replacing(self):
        status, _ = self.post_json("/state", {"roster": {"21": "Peter Smith"},
                                              "sponsor_name": "Acme"})
        self.assertEqual(status, 200)
        status, _ = self.post_json("/state", {"away_team": "Rival CC"})
        self.assertEqual(status, 200)
        on_disk = server.load_state()
        self.assertEqual(on_disk["roster"], {"21": "Peter Smith"})
        self.assertEqual(on_disk["sponsor_name"], "Acme")
        self.assertEqual(on_disk["away_team"], "Rival CC")

    def test_state_post_rejects_non_dict_and_bad_json(self):
        status, body = self.post_json("/state", [1, 2, 3])
        self.assertEqual(status, 400)
        status, _ = self.post_json("/state", "not json {{{")
        self.assertEqual(status, 400)
        # The state file must be unharmed afterwards
        self.assertEqual(server.load_state()["home_team"], "Home CC")

    def test_origin_check_rejects_cross_site_posts(self):
        status, _ = self.post_json("/state", {"home_team": "evil"},
                                   headers={"Origin": "http://evil.example"})
        self.assertEqual(status, 403)
        status, _ = self.post_json("/state", {"home_team": "evil"},
                                   headers={"Referer": "http://evil.example/page"})
        self.assertEqual(status, 403)
        self.assertNotEqual(server.load_state()["home_team"], "evil")
        # Matching origin passes
        status, _ = self.post_json("/state", {"home_team": "Fine CC"},
                                   headers={"Origin": f"http://127.0.0.1:{self.port}"})
        self.assertEqual(status, 200)

    def test_oversized_body_rejected(self):
        # The server 413s and closes WITHOUT draining the body, so depending on socket
        # timing the client sees either the 413 or a connection reset — both mean rejected.
        # What actually matters: the giant body must never be processed into state.
        big = b'{"home_team": "' + b"x" * server.MAX_BODY_BYTES + b'"}'
        try:
            status, _, _ = self.request("POST", "/state", body=big)
            self.assertEqual(status, 413)
        except (BrokenPipeError, ConnectionResetError):
            pass
        self.assertEqual(server.load_state()["home_team"], "Home CC")
        # ...and the server keeps serving normally afterwards
        status, _ = self.get_json("/health")
        self.assertEqual(status, 200)

    def test_logo_serving_and_path_traversal(self):
        logos = os.path.join(self.tmp, "logos")
        os.makedirs(logos, exist_ok=True)
        with open(os.path.join(logos, "5.png"), "wb") as f:
            f.write(b"\x89PNG fake")
        st = server.load_state()
        st["logos_folder"] = logos
        server.save_state(st)

        status, headers, data = self.request("GET", "/logo/5")
        self.assertEqual(status, 200)
        self.assertEqual(headers.get("Content-Type"), "image/png")
        self.assertEqual(data, b"\x89PNG fake")

        # Traversal attempts must never escape the folder
        for evil in ("/logo/../match_state", "/logo/..%2f..%2fconfig",
                     "/logo/....//....//etc//passwd", "/logo/..\\..\\config"):
            status, _, data = self.request("GET", evil)
            self.assertEqual(status, 404, evil)
            self.assertNotIn(b"match_state", data)

    def test_headshot_tiered_matching(self):
        heads = os.path.join(self.tmp, "headshots")
        os.makedirs(heads, exist_ok=True)
        with open(os.path.join(heads, "p.smith.png"), "wb") as f:
            f.write(b"peter-photo")
        with open(os.path.join(heads, "21.png"), "wb") as f:
            f.write(b"number-photo")
        st = server.load_state()
        st["headshots_folder"] = heads
        st["roster"] = {"21": "Peter Smith"}
        server.save_state(st)

        # initial+surname form matches p.smith.png
        status, _, data = self.request("GET", "/headshot/P%20SMITH")
        self.assertEqual(status, 200)
        # unique bare surname matches too
        status, _, _ = self.request("GET", "/headshot/SMITH")
        self.assertEqual(status, 200)
        # confirmed shirt number (roster surname agrees) prefers the numbered file
        status, _, data = self.request("GET", "/headshot/SMITH?num=21")
        self.assertEqual(status, 200)
        self.assertEqual(data, b"number-photo")
        # unknown player → clean 404
        status, _, _ = self.request("GET", "/headshot/NOBODY")
        self.assertEqual(status, 404)
        # second Smith makes the bare surname ambiguous → suppressed
        with open(os.path.join(heads, "j.smith.png"), "wb") as f:
            f.write(b"james-photo")
        status, _, _ = self.request("GET", "/headshot/SMITH")
        self.assertEqual(status, 404)

    def test_sponsor_unknown_id_404s(self):
        status, _, _ = self.request("GET", "/sponsor/zzz-does-not-exist")
        self.assertEqual(status, 404)


class TestLivePcsPipeline(HttpTestBase):
    """End-to-end /live with a fake NV Play output file: parsing, event buffering,
    fall-of-wicket logging, and the ball-by-ball DB — the match-day critical path."""

    def setUp(self):
        server._last_good_state = None
        st = dict(server.DEFAULT_STATE)
        self.pcs_dir = os.path.join(self.tmp, "pcs")
        shutil.rmtree(self.pcs_dir, ignore_errors=True)   # no leftover file between tests
        os.makedirs(self.pcs_dir, exist_ok=True)
        st["pcs_output_folder"] = self.pcs_dir
        st["use_widget"] = False
        st["demo_mode"] = False
        server.save_state(st)
        # Reset every cache the pipeline keeps between polls
        server._pcs_last_mtime = 0
        server._pcs_last_state = None
        server._innings_latch = 1
        server._prev_state.update({"score": None, "wickets": None, "overs": None})
        server._event_buffer.clear()
        server._ball_log_prev.update({"mid": None, "innings": None, "over": None,
                                      "score": 0, "wickets": 0, "count": 0})
        server.match_log_reset()

    def write_pcs(self, mtime, **fields):
        d = {"batting_team": "Home CC", "bowling_team": "Rival CC",
             "runs": "12", "wickets": "0", "overs": "2.3",
             "batter1_name": "SMITH", "batter1_runs": "8", "batter1_balls": "10",
             "batter1_strike": "True",
             "batter2_name": "JONES", "batter2_runs": "3", "batter2_balls": "5",
             "bowler_name": "HARRISON", "bowler_overs": "1.3", "bowler_runs": "12",
             "bowler_wickets": "0", "last_ball": "1 4 ."}
        d.update(fields)
        path = os.path.join(self.pcs_dir, "scoreboard-output.json")
        with open(path, "w") as f:
            json.dump(d, f)
        os.utime(path, (mtime, mtime))

    def test_pcs_state_parsed_and_no_events_on_first_poll(self):
        self.write_pcs(time.time() - 10)
        status, body = self.get_json("/live")
        self.assertEqual(status, 200)
        self.assertEqual(body["source"], "pcs")
        self.assertEqual(body["state"]["score"], 12)
        self.assertEqual(body["state"]["battingTeamName"], "Home CC")
        self.assertEqual(body["events"], [])          # first poll seeds, never fires

    def test_wicket_reaches_event_buffer_and_match_log(self):
        # Regression test for the seeding bug: with graphics_commentary off (default),
        # _prev_state was never seeded, so wickets never buffered and the match log's
        # fall-of-wickets list stayed empty all match.
        self.write_pcs(time.time() - 10)
        self.get_json("/live")                        # poll 1: seeds baseline
        self.write_pcs(time.time() - 5, runs="16", wickets="1", overs="2.4",
                       last_ball="1 4 . W")
        status, body = self.get_json("/live")         # poll 2: wicket falls
        self.assertEqual(status, 200)
        self.assertEqual([e["type"] for e in body["events"]], ["wicket"])
        self.assertEqual(body["events"][0]["wickets"], 1)
        self.assertEqual(len(server._match_log["fall_of_wickets"]), 1)
        self.assertEqual(server._match_log["fall_of_wickets"][0]["batter"], "SMITH")
        # A third poll with no change must not re-fire
        self.write_pcs(time.time() - 1, runs="16", wickets="1", overs="2.4",
                       last_ball="1 4 . W")
        _, body = self.get_json("/live")
        self.assertEqual(body["events"], [])

    def test_ball_by_ball_db_rewrites_current_over(self):
        self.write_pcs(time.time() - 10)
        self.get_json("/live")
        # Scorer adds a ball (and a wicket) to the same over → over is rewritten, not appended
        self.write_pcs(time.time() - 5, runs="16", wickets="1", overs="2.4",
                       last_ball="1 4 . W")
        self.get_json("/live")
        with sqlite3.connect(server._db_path()) as c:
            rows = c.execute("SELECT ball, outcome, is_wicket FROM balls "
                             "WHERE over=2 ORDER BY ball").fetchall()
        self.assertEqual(len(rows), 4)                 # not 3 + 4 appended
        self.assertEqual(rows[3][1], "W")
        self.assertEqual(rows[3][2], 1)

    def test_over_completing_ball_recovered_into_db(self):
        # NV Play clears the ticker on the SAME write that completes an over, so ball 6
        # never appears in any ticker — without delta recovery, the ball DB (and every
        # CSV export) silently loses the final delivery of every over.
        self.write_pcs(time.time() - 10, runs="12", wickets="0", overs="2.5",
                       last_ball="1 4 . . 2")
        self.get_json("/live")
        # the clearing write: over complete, ticker gone — the final ball was a FOUR
        self.write_pcs(time.time() - 8, runs="16", wickets="0", overs="3.0", last_ball="")
        self.get_json("/live")
        with sqlite3.connect(server._db_path()) as c:
            rows = c.execute("SELECT ball, outcome, runs FROM balls WHERE over=2 "
                             "ORDER BY ball").fetchall()
        self.assertEqual(len(rows), 6)
        self.assertEqual(rows[5], (6, "4", 4))
        # ...and a WICKET on the over-completing ball is recovered too
        self.write_pcs(time.time() - 6, runs="17", wickets="0", overs="3.5",
                       last_ball="1 . . . .")
        self.get_json("/live")
        self.write_pcs(time.time() - 4, runs="17", wickets="1", overs="4.0", last_ball="")
        self.get_json("/live")
        with sqlite3.connect(server._db_path()) as c:
            rows = c.execute("SELECT ball, outcome, is_wicket FROM balls WHERE over=3 "
                             "ORDER BY ball").fetchall()
        self.assertEqual(len(rows), 6)
        self.assertEqual(rows[5], (6, "W", 1))

    def test_live_view_is_read_only_and_never_eats_events(self):
        # The control panel polls /live/view; it must see the same picture as the
        # overlay but with NO side effects — before the split, whichever panel poll
        # landed first ate the overlay's wicket events and ran the ball logger too.
        self.write_pcs(time.time() - 10)
        self.get_json("/live")                        # overlay poll 1: seeds + logs over
        self.write_pcs(time.time() - 5, runs="16", wickets="1", overs="2.4",
                       last_ball="1 4 . W")
        for _ in range(3):                            # panel hammers /live/view first
            status, body = self.get_json("/live/view")
            self.assertEqual(status, 200)
            self.assertEqual(body["source"], "pcs")
            self.assertEqual(body["state"]["score"], 16)
            self.assertEqual(body["events"], [])
        # no side effects ran: baseline not advanced, wicket ball not logged yet
        self.assertEqual(server._prev_state["wickets"], 0)
        with sqlite3.connect(server._db_path()) as c:
            self.assertEqual(c.execute("SELECT COUNT(*) FROM balls WHERE over=2")
                             .fetchone()[0], 3)
        # ...and the overlay's own /live still gets the wicket event afterwards
        _, body = self.get_json("/live")
        self.assertEqual([e["type"] for e in body["events"]], ["wicket"])

    def test_configured_but_empty_folder_still_reports_pcs(self):
        # Pre-match: folder set, no file yet — source must stay 'pcs' (keeps the overlay
        # fast-polling instead of falling back to widget polling)
        status, body = self.get_json("/live")
        self.assertEqual(status, 200)
        self.assertEqual(body["source"], "pcs")
        self.assertIsNone(body["state"])

    def test_mid_write_empty_file_holds_last_good_frame(self):
        self.write_pcs(time.time() - 10)
        self.get_json("/live")
        # NV Play caught mid-write: file momentarily empty → keep the last good frame
        path = os.path.join(self.pcs_dir, "scoreboard-output.json")
        with open(path, "w") as f:
            f.write("")
        os.utime(path, (time.time() - 1, time.time() - 1))
        status, body = self.get_json("/live")
        self.assertEqual(status, 200)
        self.assertEqual(body["state"]["score"], 12)

    def test_non_numeric_club_id_does_not_crash_live(self):
        # Regression test: the manual badge picker can set home_club_id to a filename stem
        st = server.load_state()
        st["home_club_id"] = "brides-logo"
        server.save_state(st)
        self.write_pcs(time.time() - 10)
        status, body = self.get_json("/live")
        self.assertEqual(status, 200)
        self.assertEqual(body["source"], "pcs")
        self.assertEqual(body["club_id"], 0)


class TestAuthEnabled(HttpTestBase):
    """Same server, but with a club password set — the remote-operation config."""
    CLUB_PASSWORD = "testpw"

    def setUp(self):
        server._last_good_state = None
        server.save_state(dict(server.DEFAULT_STATE))
        with server._login_attempts_lock:
            server._login_attempts.clear()

    def login(self, password):
        return self.post_json("/login", {"password": password})

    def bearer(self):
        status, body = self.login("testpw")
        assert status == 200 and body["ok"], body
        return {"Authorization": "Bearer " + body["session_token"]}

    def test_login_and_gated_post(self):
        status, body = self.login("wrong")
        self.assertEqual(status, 401)
        status, body = self.login("testpw")
        self.assertEqual(status, 200)
        self.assertTrue(body["session_token"])

        # POST /state: 401 without a token, 200 with one
        status, _ = self.post_json("/state", {"home_team": "X"})
        self.assertEqual(status, 401)
        status, _ = self.post_json("/state", {"home_team": "X"}, headers=self.bearer())
        self.assertEqual(status, 200)

    def test_gated_gets(self):
        for path in ("/report/log", "/auth/log", "/data/export?match_id=x"):
            status, _, _ = self.request("GET", path)
            self.assertEqual(status, 401, path)
        headers = self.bearer()
        status, _, _ = self.request("GET", "/report/log", headers=headers)
        self.assertEqual(status, 200)
        # /data/export with a token → CSV (this is what the panel's blob download sends)
        status, resp_headers, data = self.request("GET", "/data/export?match_id=x",
                                                  headers=headers)
        self.assertEqual(status, 200)
        self.assertEqual(resp_headers.get("Content-Type"), "text/csv")
        self.assertTrue(data.startswith(b"innings,over,ball"))

    def test_open_endpoints_stay_open(self):
        # The overlay has no login flow: everything it GETs must work tokenless
        for path in ("/state", "/live", "/commands", "/health", "/player/stats?name=X",
                     "/commentary/over", "/commentary/latest"):
            status, _, _ = self.request("GET", path)
            self.assertEqual(status, 200, path)

    def test_overlay_loopback_carveout(self):
        # Disable replay so the endpoint answers without spawning a real OBS connection
        st = server.load_state()
        st["replay_enabled"] = False
        server.save_state(st)
        # Loopback, no token: overlay endpoints must work...
        status, body = self.post_json("/replay", {"reason": "Test"})
        self.assertEqual(status, 200)      # replay off → ok:False in the body, but never 401
        self.assertFalse(body["ok"])
        status, _ = self.post_json("/weather/show", {})
        self.assertEqual(status, 200)
        status, _ = self.post_json("/weather/hide", {})
        self.assertEqual(status, 200)
        # ...but a proxied (X-Forwarded-For) caller is NOT trusted loopback
        status, _ = self.post_json("/replay", {"reason": "Test"},
                                   headers={"X-Forwarded-For": "203.0.113.9"})
        self.assertEqual(status, 401)
        # ...and non-overlay POSTs never get the carve-out even from loopback
        status, _ = self.post_json("/scorecard/show", {})
        self.assertEqual(status, 401)

    def test_lockout_after_repeated_failures(self):
        with mock.patch("time.sleep"):     # skip the deliberate 1s anti-brute-force delay
            for _ in range(server.LOGIN_MAX_FAILURES):
                status, _ = self.login("wrong")
                self.assertEqual(status, 401)
            status, body = self.login("wrong")
            self.assertEqual(status, 429)
            # even the CORRECT password is rejected while locked out
            status, _ = self.login("testpw")
            self.assertEqual(status, 429)

    def test_logout_all_invalidates_existing_sessions(self):
        headers = self.bearer()
        status, _ = self.post_json("/auth/logout_all", {}, headers=headers)
        self.assertEqual(status, 200)
        status, _ = self.post_json("/state", {"home_team": "X"}, headers=headers)
        self.assertEqual(status, 401)
        # fresh login works again
        status, _ = self.post_json("/state", {"home_team": "X"}, headers=self.bearer())
        self.assertEqual(status, 200)

    def test_auth_log_records_events(self):
        self.login("wrong")
        headers = self.bearer()
        status, body = self.get_json("/auth/log", headers=headers)
        self.assertEqual(status, 200)
        events = [e["event"] for e in body["entries"]]
        self.assertIn("login_fail", events)
        self.assertIn("login_ok", events)


if __name__ == "__main__":
    unittest.main()
