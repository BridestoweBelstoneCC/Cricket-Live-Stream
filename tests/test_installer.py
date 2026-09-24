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
        for name in ("setup_wizard.py", "quickstart.py", "cricketstream.py"):
            body = read(name)
            self.assertIn("except Exception:", body, f"{name} has no crash handler")
            self.assertIn("traceback.print_exc(file=sys.stdout)", body,
                          f"{name} must print the traceback to stdout, in order with its "
                          f"own framed message (stderr can interleave out of order)")

    def test_pausing_can_be_switched_off_for_automation(self):
        # Otherwise the wizard's own "launch the server now" subprocess, CI, and these
        # tests would all block forever on an input() nobody is there to answer.
        for name in ("setup_wizard.py", "quickstart.py", "scorer_agent.py"):
            self.assertIn("CRICKETSTREAM_NO_PAUSE", read(name),
                          f"{name} has no way to disable its pauses")
        # cricketstream.py deliberately owns no pause of its own — every one of its pauses
        # is wiz.pause(), which honours the variable. Asserting the delegation rather than
        # the string keeps a second, drifting copy of the rule from appearing.
        launcher = read("cricketstream.py")
        self.assertIn("import setup_wizard as wiz", launcher)
        self.assertNotRegex(launcher, r"^def pause\(",
                            "cricketstream.py should reuse wiz.pause(), not define its own")


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


class TestUnifiedLauncher(unittest.TestCase):
    """cricketstream.py — the one exe for the streaming laptop. What's new here is the
    DECISION of which steps to run; each step itself is setup_wizard's existing code."""

    @staticmethod
    def sandbox(tmp, with_config):
        """A minimal project folder: the markers the launcher looks for, plus a stub
        quickstart.py so the handover can be observed without starting a real server."""
        import shutil
        for f in ("cricketstream.py", "setup_wizard.py", "requirements.txt",
                  "server.py", "overlay.html"):
            shutil.copy(os.path.join(REPO, f), tmp)
        with open(os.path.join(tmp, "quickstart.py"), "w") as fh:
            fh.write("import sys\nprint('STUB-QUICKSTART', sys.argv[1:])\nsys.exit(0)\n")
        if with_config:
            with open(os.path.join(tmp, "config.ini"), "w") as fh:
                fh.write("[Club]\nname = Test CC\n")

    def run_launcher(self, tmp, *args):
        env = dict(os.environ, CRICKETSTREAM_NO_PAUSE="1")
        return subprocess.run([sys.executable, "cricketstream.py", *args], cwd=tmp, env=env,
                              capture_output=True, text=True, timeout=300,
                              stdin=subprocess.DEVNULL)

    def test_with_config_it_skips_setup_and_starts_the_match(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.sandbox(tmp, with_config=True)
            proc = self.run_launcher(tmp, "--passthru")
        out = proc.stdout + proc.stderr
        self.assertIn("[OK] Config:", out)
        self.assertNotIn("First-time setup", out, "ran setup despite config.ini existing")
        # Handed over, and passed the operator's arguments straight through.
        self.assertIn("STUB-QUICKSTART", out)
        self.assertIn("--passthru", out)
        self.assertEqual(proc.returncode, 0)

    def test_without_config_it_runs_setup_first(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.sandbox(tmp, with_config=False)
            proc = self.run_launcher(tmp)
        out = proc.stdout + proc.stderr
        self.assertIn("First-time setup", out)
        # stdin is /dev/null, so the interview hits EOF. That must be reported as "no
        # keyboard input" rather than as a crash — it isn't a bug, and a "something went
        # wrong" box would send people to the issue tracker over a closed stdin.
        self.assertIn("No keyboard input available", out)
        self.assertNotIn("SOMETHING WENT WRONG", out)
        self.assertNotIn("STUB-QUICKSTART", out, "started the match with no config")

    def test_missing_project_files_stop_it_before_anything_else(self):
        with tempfile.TemporaryDirectory() as tmp:
            import shutil
            for f in ("cricketstream.py", "setup_wizard.py"):
                shutil.copy(os.path.join(REPO, f), tmp)
            proc = self.run_launcher(tmp)
        out = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 1)
        self.assertIn("can't find the CricketStream project files", out)
        self.assertNotIn("Traceback", out)


class TestDependencyProbe(unittest.TestCase):
    def setUp(self):
        import cricketstream
        self.cs = cricketstream
        self._saved = cricketstream.REQUIRED

    def tearDown(self):
        self.cs.REQUIRED = self._saved

    def test_reports_a_genuinely_absent_package(self):
        self.cs.REQUIRED = dict(self._saved, **{"not-a-real-pkg": "not_a_real_pkg_xyz"})
        self.assertIn("not-a-real-pkg", self.cs.missing_packages(sys.executable))

    def test_probe_uses_importlib_util_not_bare_importlib(self):
        # `import importlib` does NOT bind importlib.util — with the bare spelling the probe
        # raises AttributeError, exits non-zero, and this function silently reports "nothing
        # missing" for every package forever. Caught by the test above; pinned here because
        # the broken spelling looks completely correct at a glance.
        self.assertIn("import importlib.util", read("cricketstream.py"))

    def test_a_broken_interpreter_is_not_read_as_everything_missing(self):
        # Otherwise a bad python path would trigger a pointless full reinstall on match day.
        self.assertEqual(self.cs.missing_packages(os.path.join(REPO, "no-such-python")), [])


if __name__ == "__main__":
    unittest.main()
