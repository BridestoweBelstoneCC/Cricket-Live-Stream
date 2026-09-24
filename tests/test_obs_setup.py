"""OBS auto-setup — the replay buffer must be ENABLED, not just started.

obs_setup.py used to call StartReplayBuffer without ever enabling the buffer, which is off
by default in a fresh OBS. The start failed, the operator got a warning mid-setup telling
them to go and tick a box, and if they missed it nothing said so again — the first anyone
knew was a wicket falling and no replay appearing. Replays are half the product.

Verified against a real OBS 32.2.2 on a clean Windows 11 VM (2026-09-24):
  - before: Controls panel had no "Start Replay Buffer" button at all
  - after:  "✓ Replay buffer enabled, 25s", button present following an OBS restart,
            and a second run reported "✓ Replay buffer started"
  - the profile's basic.ini keeps a SEPARATE RecRB under [SimpleOutput] and [AdvOut];
    setting only the active one left the other false, so switching output mode later
    would silently turn replays back off. Both are set now — confirmed as RecRB=true
    twice in the real profile.

These are source assertions: a live OBS can't be part of CI.
"""
import os
import re
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)


def read(name):
    with open(os.path.join(REPO, name), encoding="utf-8") as f:
        return f.read()


class TestReplayBufferEnabled(unittest.TestCase):
    def setUp(self):
        self.src = read("obs_setup.py")

    def test_it_enables_the_buffer_and_does_not_only_start_it(self):
        self.assertIn('"parameterName": "RecRB"', self.src,
                      "obs_setup must ENABLE the replay buffer (RecRB), not just call "
                      "StartReplayBuffer — which fails outright on a fresh OBS")

    def test_both_output_mode_sections_are_set(self):
        # OBS honours only the section matching the current output mode. Setting one
        # leaves a trap for anyone who later switches Simple <-> Advanced.
        block = re.search(r"for category in \((.*?)\)", self.src)
        self.assertIsNotNone(block, "RecRB should be set for both output-mode sections")
        self.assertIn("SimpleOutput", block.group(1))
        self.assertIn("AdvOut", block.group(1))

    def test_the_buffer_length_is_set_too(self):
        self.assertIn('"parameterName": "RecRBTime"', self.src,
                      "a buffer with no configured length isn't much use")

    def test_enable_happens_before_start(self):
        enable = self.src.index('"parameterName": "RecRB"')
        start = self.src.index('request("StartReplayBuffer")')
        self.assertLess(enable, start,
                        "must enable the buffer before trying to start it")

    def test_failure_to_start_after_enabling_says_restart_obs(self):
        # OBS creates outputs at startup, so a buffer enabled in a running instance can't
        # start until OBS is restarted. Repeating "go and enable it" would be wrong by
        # then — it IS enabled. Confirmed on the VM: restart, re-run, "buffer started".
        self.assertIn("restart OBS once", self.src,
                      "the post-enable start failure needs its own message, not the "
                      "generic 'enable it yourself' one")


if __name__ == "__main__":
    unittest.main()
