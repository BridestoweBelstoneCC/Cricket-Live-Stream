"""YouTube broadcast manager: the pure payload builder (fully testable), the no-creds
guardrails, and the endpoint wiring (template fill, state fallback, explicit overrides).
The live Google API path can't run here — no credentials — same as it always has."""
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import server
from test_http import HttpTestBase   # noqa: E402


class TestSnippetPayloadBuilder(unittest.TestCase):
    CUR_SNIP = {"title": "Old title", "description": "old desc",
                "scheduledStartTime": "2026-07-11T13:00:00Z"}

    def test_preserves_current_when_nothing_changes(self):
        snip = server._yt_snippet_payload(self.CUR_SNIP)
        self.assertEqual(snip["title"], "Old title")
        self.assertEqual(snip["description"], "old desc")
        self.assertEqual(snip["scheduledStartTime"], "2026-07-11T13:00:00Z")

    def test_applies_changes_and_carries_scheduled_start(self):
        snip = server._yt_snippet_payload(self.CUR_SNIP, title="New", description="ND")
        self.assertEqual(snip["title"], "New")
        self.assertEqual(snip["description"], "ND")
        self.assertEqual(snip["scheduledStartTime"], "2026-07-11T13:00:00Z")

    def test_never_includes_made_for_kids(self):
        # The whole point of the fix: selfDeclaredMadeForKids must NEVER be in the payload —
        # YouTube 403s any attempt to modify it via update.
        snip = server._yt_snippet_payload(self.CUR_SNIP, title="X")
        self.assertNotIn("selfDeclaredMadeForKids", snip)
        self.assertNotIn("status", snip)

    def test_defaults_when_broadcast_is_bare(self):
        snip = server._yt_snippet_payload({})
        self.assertEqual(snip["title"], "")
        self.assertEqual(snip["description"], "")
        self.assertNotIn("scheduledStartTime", snip)          # nothing to carry

    def test_empty_string_title_and_description_apply(self):
        # "" is a real value (clear the field), distinct from None (leave unchanged)
        snip = server._yt_snippet_payload(self.CUR_SNIP, title="", description="")
        self.assertEqual(snip["title"], "")
        self.assertEqual(snip["description"], "")


class TestNoCredsGuardrails(unittest.TestCase):
    def setUp(self):
        self._orig = server.YT_CREDS_FILE
        server.YT_CREDS_FILE = "/nonexistent/yt_credentials.json"

    def tearDown(self):
        server.YT_CREDS_FILE = self._orig

    def test_missing_credentials_is_a_clean_error(self):
        ok, msg = server.update_youtube_broadcast(title="X")
        self.assertFalse(ok)
        self.assertIn("yt_credentials.json", msg)

    def test_invalid_privacy_short_circuits_before_any_api(self):
        ok, msg = server.update_youtube_broadcast(title="X", privacy="nope")
        self.assertFalse(ok)
        self.assertIn("privacy", msg)

    def test_title_wrapper_still_works(self):
        ok, msg = server.update_youtube_title("Hello")
        self.assertFalse(ok)                                  # no creds → fails cleanly
        self.assertIn("yt_credentials.json", msg)


class TestRemoteAuthGuard(unittest.TestCase):
    """A remote (tunnelled) first-run must NOT open a browser on the host — it should
    return a clear 'authorise locally' message instead of hanging."""

    def setUp(self):
        self.tmp = __import__("tempfile").mkdtemp()
        self._creds, self._token = server.YT_CREDS_FILE, server.YT_TOKEN_FILE
        # A creds file that EXISTS (so we get past that check) but no token yet
        server.YT_CREDS_FILE = os.path.join(self.tmp, "yt_credentials.json")
        server.YT_TOKEN_FILE = os.path.join(self.tmp, "yt_token.json")
        with open(server.YT_CREDS_FILE, "w") as f:
            f.write('{"installed": {"client_id": "x", "client_secret": "y"}}')

    def tearDown(self):
        server.YT_CREDS_FILE, server.YT_TOKEN_FILE = self._creds, self._token
        __import__("shutil").rmtree(self.tmp, ignore_errors=True)

    def test_non_interactive_first_run_refuses_cleanly(self):
        # allow_interactive=False and no token → must not attempt the browser flow.
        # Stub the Google modules into sys.modules so the guard path runs deterministically
        # whether or not the libraries are installed (CI runs dependency-free), instead of
        # short-circuiting on ImportError.
        stub = {name: mock.MagicMock() for name in (
            "google", "google.oauth2", "google.oauth2.credentials",
            "google_auth_oauthlib", "google_auth_oauthlib.flow",
            "google.auth", "google.auth.transport", "google.auth.transport.requests",
            "googleapiclient", "googleapiclient.discovery")}
        with mock.patch.dict("sys.modules", stub):
            yt, err = server._youtube_service(allow_interactive=False)
            self.assertIsNone(yt)
            self.assertIn("STREAMING LAPTOP", err)
            # and the same via the public entry point
            ok, msg = server.update_youtube_broadcast(title="X", allow_interactive=False)
            self.assertFalse(ok)
            self.assertIn("authoris", msg.lower())


class _Req:
    def __init__(self, result, fail=None):
        self._result, self._fail = result, fail
    def execute(self):
        if self._fail:
            raise self._fail
        return self._result


class _Resource:
    def __init__(self, parent, kind):
        self.parent, self.kind = parent, kind
    def list(self, **kw):
        self.parent.calls.append((self.kind, "list", kw))
        if self.kind == "liveBroadcasts":
            items = self.parent.broadcast if kw.get("broadcastStatus") == "active" else []
            return _Req({"items": items})
        return _Req({"items": self.parent.video})
    def update(self, **kw):
        self.parent.calls.append((self.kind, "update", kw))
        return _Req({}, fail=self.parent.fail.get((self.kind, kw.get("part"))))


class FakeYT:
    """Minimal stand-in for the googleapiclient youtube service."""
    def __init__(self, broadcast, video, fail=None):
        self.broadcast, self.video, self.fail = broadcast, video, (fail or {})
        self.calls = []
    def liveBroadcasts(self):
        return _Resource(self, "liveBroadcasts")
    def videos(self):
        return _Resource(self, "videos")


class TestUpdateLogicMocked(unittest.TestCase):
    """The real update_youtube_broadcast logic against a fake YouTube service — this is
    what proves the selfDeclaredMadeForKids 403 fix."""

    def make(self, fail=None):
        return FakeYT(
            broadcast=[{"id": "B1", "snippet": {"title": "Old",
                                                "scheduledStartTime": "2026-07-11T13:00:00Z"}}],
            video=[{"snippet": {"title": "Old", "categoryId": "1"}}],
            fail=fail)

    def test_never_sends_made_for_kids_and_splits_calls(self):
        fake = self.make()
        with mock.patch.object(server, "_youtube_service", return_value=(fake, None)):
            ok, msg = server.update_youtube_broadcast(
                title="New", description="D", privacy="unlisted", category_id="17")
        self.assertTrue(ok, msg)
        updates = [c for c in fake.calls if c[1] == "update"]

        # NOTHING sent anywhere may mention selfDeclaredMadeForKids
        self.assertNotIn("selfDeclaredMadeForKids", str(fake.calls))

        # title/description → a snippet-only broadcast update, scheduledStartTime carried
        snip = [c for c in updates if c[0] == "liveBroadcasts" and c[2]["part"] == "snippet"]
        self.assertEqual(len(snip), 1)
        body = snip[0][2]["body"]
        self.assertNotIn("status", body)
        self.assertEqual(body["snippet"]["title"], "New")
        self.assertEqual(body["snippet"]["scheduledStartTime"], "2026-07-11T13:00:00Z")

        # privacy → a status update carrying ONLY privacyStatus
        stat = [c for c in updates if c[0] == "liveBroadcasts" and c[2]["part"] == "status"]
        self.assertEqual(len(stat), 1)
        self.assertEqual(stat[0][2]["body"]["status"], {"privacyStatus": "unlisted"})

        # category → the video resource
        vid = [c for c in updates if c[0] == "videos"]
        self.assertEqual(len(vid), 1)
        self.assertEqual(vid[0][2]["body"]["snippet"]["categoryId"], "17")

    def test_partial_failure_is_reported_not_swallowed(self):
        # privacy fails (as the real 403 would), title/description + category still apply
        fake = self.make(fail={("liveBroadcasts", "status"): Exception("some API error")})
        with mock.patch.object(server, "_youtube_service", return_value=(fake, None)):
            ok, msg = server.update_youtube_broadcast(
                title="New", privacy="private", category_id="17")
        self.assertFalse(ok)                       # a part failed → overall not-ok
        self.assertIn("FAILED", msg)
        self.assertIn("privacy", msg)
        self.assertIn("title/description", msg)    # this one succeeded, still reported

    def test_no_broadcast_found(self):
        fake = FakeYT(broadcast=[], video=[])
        with mock.patch.object(server, "_youtube_service", return_value=(fake, None)):
            ok, msg = server.update_youtube_broadcast(title="X")
        self.assertFalse(ok)
        self.assertIn("No active or upcoming broadcast", msg)


class TestYoutubeEndpoint(HttpTestBase):
    def setUp(self):
        server._last_good_state = None
        server.save_state({**server.DEFAULT_STATE,
                           "home_team": "Alpha CC", "away_team": "Beta CC",
                           "youtube_title_template": "LIVE: {home} vs {away}",
                           "youtube_description": "Watch {home} v {away}.",
                           "youtube_privacy": "public", "youtube_category": "17"})

    def test_fills_templates_and_uses_state_defaults(self):
        captured = {}
        def fake(**kw):
            captured.update(kw)
            return True, "Updated: title/description, privacy=public, category"
        with mock.patch.object(server, "update_youtube_broadcast", side_effect=fake):
            status, d = self.post_json("/youtube/update", {})
        self.assertEqual(status, 200)
        self.assertTrue(d["ok"])
        self.assertEqual(captured["title"], "LIVE: Alpha CC vs Beta CC")
        self.assertEqual(captured["description"], "Watch Alpha CC v Beta CC.")
        self.assertEqual(captured["privacy"], "public")
        self.assertEqual(captured["category_id"], "17")
        self.assertNotIn("made_for_kids", captured)   # never sent — API can't set it

    def test_explicit_payload_overrides_state(self):
        captured = {}
        def fake(**kw):
            captured.update(kw)
            return True, "ok"
        with mock.patch.object(server, "update_youtube_broadcast", side_effect=fake):
            self.post_json("/youtube/update", {
                "title": "Cup Final", "description": "Big one",
                "privacy": "private", "category": "24"})
        self.assertEqual(captured["title"], "Cup Final")
        self.assertEqual(captured["description"], "Big one")
        self.assertEqual(captured["privacy"], "private")
        self.assertEqual(captured["category_id"], "24")

    def test_failure_reported_as_ok_false(self):
        with mock.patch.object(server, "update_youtube_broadcast",
                               return_value=(False, "No active or upcoming broadcast found")):
            status, d = self.post_json("/youtube/update", {})
        self.assertEqual(status, 200)
        self.assertFalse(d["ok"])
        self.assertIn("broadcast", d["error"])


class TestYoutubeAuth(HttpTestBase):
    CLUB_PASSWORD = "testpw"

    def test_update_is_token_gated(self):
        status, _ = self.post_json("/youtube/update", {"title": "x"})
        self.assertEqual(status, 401)


if __name__ == "__main__":
    unittest.main()
