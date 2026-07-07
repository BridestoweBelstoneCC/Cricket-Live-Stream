"""Session tokens and config.ini token persistence — no server needed."""
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import server


class TokenTestBase(unittest.TestCase):
    def setUp(self):
        self._token = server._CONTROL_TOKEN
        self._epoch = server._SESSION_EPOCH
        self._hours = server._SESSION_HOURS
        server._CONTROL_TOKEN = "ab" * 32

    def tearDown(self):
        server._CONTROL_TOKEN = self._token
        server._SESSION_EPOCH = self._epoch
        server._SESSION_HOURS = self._hours


class TestSessionTokens(TokenTestBase):
    def test_round_trip(self):
        tok = server._make_session_token()
        self.assertTrue(server._verify_session_token(tok))

    def test_expired_token_rejected(self):
        server._SESSION_HOURS = -1          # issue an already-expired token
        tok = server._make_session_token()
        self.assertFalse(server._verify_session_token(tok))

    def test_tampered_token_rejected(self):
        tok = server._make_session_token()
        bad = tok[:-1] + ("0" if tok[-1] != "0" else "1")
        self.assertFalse(server._verify_session_token(bad))
        # Extending the expiry without re-signing must also fail
        expiry, rest = tok.split(":", 1)
        self.assertFalse(server._verify_session_token(str(int(expiry) + 9999) + ":" + rest))

    def test_epoch_rotation_invalidates_all(self):
        tok = server._make_session_token()
        self.assertTrue(server._verify_session_token(tok))
        server._SESSION_EPOCH = "rotated!"   # what POST /auth/logout_all does
        self.assertFalse(server._verify_session_token(tok))

    def test_garbage_tokens(self):
        for bad in ("", "a:b", "a:b:c", "1:2:3:4", "🏏"):
            self.assertFalse(server._verify_session_token(bad), bad)

    def test_empty_control_token_never_signs_or_verifies(self):
        server._CONTROL_TOKEN = ""
        with self.assertRaises(RuntimeError):
            server._make_session_token()
        self.assertFalse(server._verify_session_token("1:2:3"))


class TestPersistControlToken(unittest.TestCase):
    def setUp(self):
        self._orig_path = server._config_ini_path
        fd, self.path = tempfile.mkstemp(suffix=".ini")
        os.close(fd)
        server._config_ini_path = lambda: self.path

    def tearDown(self):
        server._config_ini_path = self._orig_path
        os.unlink(self.path)

    def write(self, text):
        with open(self.path, "w") as f:
            f.write(text)

    def read_back(self):
        import configparser
        cp = configparser.ConfigParser()
        cp.read(self.path)
        return cp

    def test_replaces_blank_line_in_place(self):
        self.write("[Auth]\ncontrol_token =\nclub_password = pw\n\n[Network]\nbind_host = 127.0.0.1\n")
        self.assertTrue(server._persist_control_token("deadbeef"))
        cp = self.read_back()
        self.assertEqual(cp.get("Auth", "control_token"), "deadbeef")
        self.assertEqual(cp.get("Auth", "club_password"), "pw")     # untouched
        self.assertEqual(cp.get("Network", "bind_host"), "127.0.0.1")

    def test_missing_line_inserted_under_auth_not_at_eof(self):
        # [Auth] exists (password set) but the control_token line was deleted, and
        # [Network] follows — the token must land in [Auth], not the last section.
        self.write("[Auth]\nclub_password = pw\n\n[Network]\nbind_host = 127.0.0.1\n")
        self.assertTrue(server._persist_control_token("deadbeef"))
        cp = self.read_back()
        self.assertEqual(cp.get("Auth", "control_token"), "deadbeef")
        self.assertFalse(cp.has_option("Network", "control_token"))

    def test_no_auth_section_appends_one(self):
        self.write("[Club]\nname = Test CC\n")
        self.assertTrue(server._persist_control_token("deadbeef"))
        cp = self.read_back()
        self.assertEqual(cp.get("Auth", "control_token"), "deadbeef")

    def test_preserves_comments(self):
        self.write("# keep me\n[Auth]\n# and me\ncontrol_token =\n")
        server._persist_control_token("deadbeef")
        text = open(self.path).read()
        self.assertIn("# keep me", text)
        self.assertIn("# and me", text)


if __name__ == "__main__":
    unittest.main()
