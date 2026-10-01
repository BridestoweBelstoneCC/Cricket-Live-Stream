"""Finding NV Play's output folder during setup, instead of asking the club to type it.

The search is scorer_agent.autodetect_folder() (the scorer's-laptop agent hunts the same
folder), plus setup's own fallback to NV Play's standard Output folder before any match
has written to it. Runs against a fake home folder; the real one is never read.
"""
import os
import shutil
import sys
import tempfile
import time
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import scorer_agent      # noqa: E402
import setup_wizard as wiz  # noqa: E402

NVPLAY = os.path.join("Documents", "Cricket Matches", "_Scoreboards", "Output")


class TestDetect(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.home, True)
        real = os.path.expanduser
        p = mock.patch("os.path.expanduser",
                       side_effect=lambda s: self.home + s[1:] if s.startswith("~") else real(s))
        p.start()
        self.addCleanup(p.stop)

    def folder(self, rel, file=None, age=0):
        path = os.path.join(self.home, rel)
        os.makedirs(path, exist_ok=True)
        if file:
            f = os.path.join(path, file)
            open(f, "w").write("{}")
            then = time.time() - age
            os.utime(f, (then, then))
        return path

    def test_live_folder(self):
        path = self.folder(NVPLAY, "nvplay-scoreboard1.xml")
        self.assertEqual(wiz.detect_pcs_folder(), (path, "live"))

    def test_folder_with_an_old_scoreboard(self):
        path = self.folder(NVPLAY, "nvplay-scoreboard1.xml", age=86400 * 30)
        self.assertEqual(wiz.detect_pcs_folder(), (path, "stale"))

    def test_onedrive_documents(self):
        # Windows 11 often redirects Documents into OneDrive.
        path = self.folder(os.path.join("OneDrive", NVPLAY), "nvplay-scoreboard1.xml")
        self.assertEqual(wiz.detect_pcs_folder(), (path, "live"))

    def test_onedrive_work_account(self):
        path = self.folder(os.path.join("OneDrive - Some Club", NVPLAY), "nvplay-scoreboard1.xml")
        self.assertEqual(wiz.detect_pcs_folder(), (path, "live"))

    def test_nvplay_folder_before_any_match(self):
        path = self.folder(NVPLAY)
        self.assertEqual(wiz.detect_pcs_folder(), (path, "empty"))

    def test_live_beats_stale(self):
        self.folder(os.path.join("Documents", "PCS Pro", "Output"), "scoreboard.json", age=86400)
        live = self.folder(os.path.join("OneDrive", NVPLAY), "nvplay-scoreboard1.xml")
        self.assertEqual(wiz.detect_pcs_folder(), (live, "live"))

    def test_nothing_found(self):
        self.assertEqual(wiz.detect_pcs_folder(), (None, None))

    def test_scorer_agent_searches_onedrive_too(self):
        self.assertIn("OneDrive*/Documents/Cricket Matches/_Scoreboards/Output",
                      scorer_agent.COMMON_FOLDER_PATTERNS)


TEMPLATES = os.path.join("Documents", "Cricket Matches", "_Scoreboards", "Templates")


class TestTemplateInstall(TestDetect):
    """scoreboard.template into NV Play's Templates folder (scorer_agent + setup)."""

    def test_running_from_source_finds_the_repo_template(self):
        with open(os.path.join(REPO, "scoreboard.template"), encoding="utf-8") as f:
            self.assertEqual(scorer_agent.bundled_template(), f.read())

    def test_installed_as_an_exact_copy(self):
        folder = self.folder(TEMPLATES)
        text = "{\n  \"runs\": \"{{InningsRuns}}\"\n}\n"
        self.assertEqual(scorer_agent.install_template(folder, text), "installed")
        with open(os.path.join(folder, "scoreboard.template"), "rb") as f:
            # Byte-for-byte: Windows text mode would have written \r\n.
            self.assertEqual(f.read(), text.encode("utf-8"))

    def test_identical_copy_left_alone(self):
        folder = self.folder(TEMPLATES)
        scorer_agent.install_template(folder, "same")
        self.assertEqual(scorer_agent.install_template(folder, "same"), "current")
        self.assertFalse(os.path.exists(os.path.join(folder, "scoreboard.template.old")))

    def test_line_endings_alone_dont_count_as_different(self):
        folder = self.folder(TEMPLATES)
        scorer_agent.install_template(folder, "a\r\nb\r\n")
        self.assertEqual(scorer_agent.install_template(folder, "a\nb\n"), "current")

    def test_different_copy_replaced_and_kept_as_old(self):
        folder = self.folder(TEMPLATES)
        with open(os.path.join(folder, "scoreboard.template"), "w") as f:
            f.write("the scorer's old one")
        self.assertEqual(scorer_agent.install_template(folder, "new"), "updated")
        with open(os.path.join(folder, "scoreboard.template.old")) as f:
            self.assertEqual(f.read(), "the scorer's old one")

    def test_templates_folder_beside_the_output_folder(self):
        # A non-standard place, but NV Play keeps Output and Templates side by side.
        out = self.folder(os.path.join("Elsewhere", "Scoreboards", "Output"))
        tpl = self.folder(os.path.join("Elsewhere", "Scoreboards", "Templates"))
        self.assertEqual(scorer_agent.find_templates_folder(out), tpl)

    def test_templates_folder_in_onedrive(self):
        tpl = self.folder(os.path.join("OneDrive", TEMPLATES))
        self.assertEqual(scorer_agent.find_templates_folder(), tpl)

    def test_no_nvplay_means_nothing_written(self):
        self.assertEqual(scorer_agent.ensure_template(), (None, None))
        self.assertIsNone(wiz.install_nvplay_template(""))
        self.assertEqual(os.listdir(self.home), [])

    def test_setup_installs_it_when_nvplay_is_here(self):
        tpl = self.folder(TEMPLATES)
        with mock.patch("builtins.print"):
            self.assertEqual(wiz.install_nvplay_template(""), "installed")
        self.assertTrue(os.path.exists(os.path.join(tpl, "scoreboard.template")))

    def test_both_setup_routes_install_it(self):
        for name in ("setup_wizard.py", "cricketstream.py"):
            with open(os.path.join(REPO, name), encoding="utf-8") as f:
                src = f.read()
            self.assertIn("install_nvplay_template(values", src, name)

    def test_build_bundles_it_and_checks(self):
        with open(os.path.join(REPO, ".github", "workflows", "build-executables.yml"),
                  encoding="utf-8") as f:
            wf = f.read()
        self.assertIn('--add-data "scoreboard.template:."', wf)
        self.assertIn("--check-template", wf)


class TestSetupQuestion(unittest.TestCase):
    """The configure() interview: offered folder, accepted or declined."""

    def run_configure(self, found, use_it, typed="C:/typed/path"):
        answers = {"PCS output folder": typed}
        asked = []

        def fake_ask(prompt, default="", required=False, secret=False):
            asked.append(prompt)
            return answers.get(prompt, default or "x")

        def fake_yn(prompt, default=True):
            return use_it if "Use this folder" in prompt else default

        with mock.patch.object(wiz, "detect_pcs_folder", return_value=found), \
             mock.patch.object(wiz, "ask", side_effect=fake_ask), \
             mock.patch.object(wiz, "ask_yn", side_effect=fake_yn), \
             mock.patch("builtins.print"):
            return wiz.configure()["pcs_folder"], asked

    def test_found_folder_accepted_with_enter(self):
        folder, asked = self.run_configure(("C:/found/Output", "live"), use_it=True)
        self.assertEqual(folder, "C:/found/Output")
        self.assertNotIn("PCS output folder", asked)

    def test_found_folder_declined_asks_as_before(self):
        # Scorer on another laptop: the folder found here isn't the one to use.
        folder, asked = self.run_configure(("C:/found/Output", "stale"), use_it=False)
        self.assertEqual(folder, "C:/typed/path")
        self.assertIn("PCS output folder", asked)

    def test_nothing_found_asks_as_before(self):
        folder, asked = self.run_configure((None, None), use_it=True)
        self.assertEqual(folder, "C:/typed/path")

    def test_no_obs_password_question(self):
        # quickstart switches OBS's WebSocket on and fills the password in itself.
        _, asked = self.run_configure((None, None), use_it=True)
        self.assertFalse([a for a in asked if "WebSocket" in a or "OBS" in a and "password" in a])


if __name__ == "__main__":
    unittest.main()
