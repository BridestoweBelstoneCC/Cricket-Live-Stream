"""Installer/launcher robustness — the promises a first-time user depends on.

The reported symptom was "run it in the wrong place and it exits instantly with an error
nobody can read". Two separate causes, both covered here:

  1. The launchers looked for project files in their OWN folder only. Windows/quickstart.bat
     and Windows/install.bat sat in Windows/ and looked for quickstart.py / requirements.txt
     there, which is the repo ROOT — so they failed 100% of the time as shipped, with
     "can't open file" / "Could not open requirements file". Same for the Mac pair, and for
     CricketStreamSetup.exe dropped wherever the setup guide's "this folder" was read as.
  2. Nothing paused before exiting. A double-clicked .exe owns its console window, so every
     sys.exit() closed the window with the reason inside it — including tracebacks, which
     are exactly the text needed to diagnose a problem.

These are text/behaviour assertions rather than a real double-click: the batch files can't
run on CI's Linux runners, and the frozen exe only exists in the release workflow.
"""
import os
import re
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)


def read(*parts):
    with open(os.path.join(REPO, *parts), encoding="utf-8") as f:
        return f.read()


# (launcher, the project file it needs to find)
WINDOWS_LAUNCHERS = [
    ("Windows/quickstart.bat", "quickstart.py"),
    ("Windows/install.bat", "requirements.txt"),
    ("Windows/setup.bat", "setup_wizard.py"),
    ("Windows/start_server.bat", "server.py"),
    ("Windows/start_scorer_agent.bat", "scorer_agent.py"),
]
MAC_LAUNCHERS = [
    ("Mac/quickstart.sh", "quickstart.py"),
    ("Mac/install.sh", "requirements.txt"),
    ("Mac/setup.sh", "setup_wizard.py"),
    ("Mac/start_server.sh", "server.py"),
]


class TestLaunchersFindTheProject(unittest.TestCase):
    def test_the_files_they_look_for_are_in_the_repo_root(self):
        # If one of these ever moves, the launchers' search targets have to move with it.
        for _, target in WINDOWS_LAUNCHERS + MAC_LAUNCHERS:
            self.assertTrue(os.path.exists(os.path.join(REPO, target)),
                            f"{target} is not in the repo root any more")

    def test_windows_launchers_search_the_parent_folder_too(self):
        # They live in Windows/ but everything they run lives one level up. Looking only in
        # their own folder is what made three of the four fail as shipped. Quoting varies
        # (start_scorer_agent.bat predates the others) — the search is what matters.
        for name, target in WINDOWS_LAUNCHERS:
            body = read(*name.split("/"))
            self.assertRegex(body, re.escape(f"..\\{target}").join([r"if exist \"?", r"\"? cd \.\."]),
                             f"{name} never looks in the parent folder for {target}")

    def test_mac_launchers_search_the_parent_folder_too(self):
        for name, target in MAC_LAUNCHERS:
            body = read(*name.split("/"))
            self.assertIn(f"../{target}", body,
                          f"{name} never looks in the parent folder for {target}")

    def test_launchers_pause_so_the_error_can_be_read(self):
        for name, _ in WINDOWS_LAUNCHERS:
            self.assertIn("pause", read(*name.split("/")),
                          f"{name} can exit without pausing — the window would just vanish")
        for name, _ in MAC_LAUNCHERS:
            self.assertIn("read -p", read(*name.split("/")),
                          f"{name} can exit without pausing — the window would just vanish")


class TestNoSilentExits(unittest.TestCase):
    """Every fatal path in the two double-clickable Python entry points has to go through
    die()/pause(), never a bare sys.exit(1) that closes the window with the reason in it."""

    def _fatal_exits_outside_handlers(self, source):
        # Two bare sys.exit(1)s are legitimate and must NOT be flagged: the one inside
        # die() itself (it has already printed and paused — that IS the mechanism), and
        # the ones in the __main__ crash handlers at the bottom (same). Anywhere else is
        # a window that closes with the reason inside it.
        body = source.split('if __name__ == "__main__":')[0]
        body = re.sub(r"\ndef die\(.*?(?=\ndef |\Z)", "\n", body, flags=re.S)
        return re.findall(r"^\s*sys\.exit\(1\)", body, re.M)

    def test_setup_wizard_has_no_bare_fatal_exits(self):
        self.assertEqual(self._fatal_exits_outside_handlers(read("setup_wizard.py")), [],
                         "setup_wizard.py has a sys.exit(1) that isn't die() — use die()")

    def test_quickstart_has_no_bare_fatal_exits(self):
        self.assertEqual(self._fatal_exits_outside_handlers(read("quickstart.py")), [],
                         "quickstart.py has a sys.exit(1) that isn't die() — use die()")

    def test_entry_points_catch_unhandled_exceptions(self):
        # A traceback that flashes past for 40ms is the least useful thing a user can be
        # shown, and the exact text needed to diagnose the problem.
        for name in ("setup_wizard.py", "quickstart.py", "quickstart_launcher.py"):
            body = read(name)
            self.assertIn("except Exception:", body, f"{name} has no crash handler")
            self.assertIn("traceback.print_exc(file=sys.stdout)", body,
                          f"{name} must print the traceback to stdout, in order with its "
                          f"own framed message (stderr can interleave out of order)")

    def test_pausing_can_be_switched_off_for_automation(self):
        # Otherwise the wizard's own "launch the server now" subprocess, CI, and these
        # tests would all block forever on an input() nobody is there to answer.
        for name in ("setup_wizard.py", "quickstart.py", "quickstart_launcher.py"):
            self.assertIn("CRICKETSTREAM_NO_PAUSE", read(name),
                          f"{name} has no way to disable its pauses")


class TestWizardLocatesProject(unittest.TestCase):
    def setUp(self):
        import setup_wizard
        self.wiz = setup_wizard

    def test_recognises_the_real_project_folder(self):
        self.assertTrue(self.wiz.looks_like_project(REPO))

    def test_rejects_a_folder_that_only_looks_right(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertFalse(self.wiz.looks_like_project(tmp))
            # One marker present isn't enough — a stray server.py copy shouldn't con it
            # into writing config.ini somewhere the real server will never read it.
            open(os.path.join(tmp, "server.py"), "w").close()
            self.assertFalse(self.wiz.looks_like_project(tmp))

    def test_wrong_folder_explains_itself_and_does_not_traceback(self):
        # The end-to-end promise: dropped somewhere wrong, it must exit non-zero having
        # said WHERE it looked and WHAT to do — not raise, and not die inside pip.
        with tempfile.TemporaryDirectory() as tmp:
            import shutil
            shutil.copy(os.path.join(REPO, "setup_wizard.py"), tmp)
            env = dict(os.environ, CRICKETSTREAM_NO_PAUSE="1")
            proc = subprocess.run([sys.executable, "setup_wizard.py"], cwd=tmp, env=env,
                                  capture_output=True, text=True, timeout=120)
        self.assertEqual(proc.returncode, 1)
        out = proc.stdout + proc.stderr
        self.assertIn("can't find the CricketStream project files", out)
        self.assertIn("TO FIX", out)
        self.assertNotIn("Traceback", out)


if __name__ == "__main__":
    unittest.main()
