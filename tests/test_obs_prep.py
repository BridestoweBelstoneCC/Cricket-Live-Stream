"""obs_prep.py — switching OBS's WebSocket server and replay buffer on before OBS starts.

Everything runs against a temporary copy of OBS's config folder, laid out the way a real
OBS 32 install has it (checked against one): plugin_config/obs-websocket/config.json,
user.ini naming the profile, and basic/profiles/<name>/basic.ini with a UTF-8 BOM and
CamelCase keys. No real OBS install is read or written.
"""
import json
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import obs_prep  # noqa: E402

BASIC_INI = (
    "\ufeff[General]\r\nName=Untitled\r\n\r\n"
    "[SimpleOutput]\r\nVBitrate=6000\r\nFilePath=C:\\\\Users\\\\x\\\\Videos\r\n"
    "RecRB=false\r\nRecRBTime=20\r\n\r\n"
    "[Output]\r\nMode=Simple\r\n\r\n"
    "[AdvOut]\r\nEncoder=obs_x264\r\nRecRBSize=512\r\n"
)


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.cfg = os.path.join(self.tmp, "obs-studio")
        os.makedirs(self.cfg)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def make_profile(self, ini_name="user.ini", profile="Untitled", body=BASIC_INI):
        with open(os.path.join(self.cfg, ini_name), "w", encoding="utf-8") as f:
            f.write(f"\ufeff[General]\nX=1\n\n[Basic]\nProfile={profile}\nProfileDir={profile}\n")
        folder = os.path.join(self.cfg, "basic", "profiles", profile)
        os.makedirs(folder, exist_ok=True)
        path = os.path.join(folder, "basic.ini")
        with open(path, "wb") as f:
            f.write(body.encode("utf-8"))
        return path

    def write_ws(self, conf):
        path = obs_prep.ws_config_path(self.cfg)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            json.dump(conf, f)

    def ini(self, path):
        with open(path, "rb") as f:
            return f.read()


class TestWebSocketConfig(Base):
    def test_fresh_install_gets_an_enabled_password_protected_server(self):
        pw, changed = obs_prep.ensure_websocket(self.cfg)
        self.assertTrue(changed)
        conf = obs_prep.read_ws_config(self.cfg)
        self.assertTrue(conf["server_enabled"])
        self.assertTrue(conf["auth_required"])
        self.assertEqual(conf["server_password"], pw)
        self.assertGreaterEqual(len(pw), 16)
        self.assertEqual(conf["server_port"], 4455)

    def test_first_load_is_false_so_obs_keeps_our_password(self):
        # first_load=true makes obs-websocket generate its own password on start and
        # save over ours, leaving config.ini holding a password OBS no longer accepts.
        obs_prep.ensure_websocket(self.cfg)
        self.assertIs(obs_prep.read_ws_config(self.cfg)["first_load"], False)

    def test_second_run_reuses_the_password(self):
        pw1, _ = obs_prep.ensure_websocket(self.cfg)
        pw2, changed = obs_prep.ensure_websocket(self.cfg)
        self.assertEqual(pw1, pw2)
        self.assertFalse(changed)

    def test_working_setup_without_auth_is_left_alone(self):
        # e.g. this dev machine's own OBS. Rewriting it would break other tools using it.
        conf = {"server_enabled": True, "auth_required": False,
                "server_password": "theirs", "server_port": 4455, "first_load": False}
        self.write_ws(conf)
        pw, changed = obs_prep.ensure_websocket(self.cfg)
        self.assertEqual((pw, changed), ("", False))
        self.assertEqual(obs_prep.read_ws_config(self.cfg), conf)

    def test_existing_password_is_reused_not_replaced(self):
        self.write_ws({"server_enabled": True, "auth_required": True,
                       "server_password": "clubpw", "server_port": 4455})
        self.assertEqual(obs_prep.ensure_websocket(self.cfg), ("clubpw", False))

    def test_disabled_server_is_enabled_keeping_other_settings(self):
        self.write_ws({"server_enabled": False, "auth_required": True,
                       "server_password": "", "server_port": 4460,
                       "alerts_enabled": True, "first_load": True})
        pw, changed = obs_prep.ensure_websocket(self.cfg)
        conf = obs_prep.read_ws_config(self.cfg)
        self.assertTrue(changed)
        self.assertTrue(conf["server_enabled"])
        self.assertEqual(conf["server_port"], 4460)
        self.assertTrue(conf["alerts_enabled"])
        self.assertEqual(conf["server_password"], pw)

    def test_corrupt_file_is_replaced(self):
        path = obs_prep.ws_config_path(self.cfg)
        os.makedirs(os.path.dirname(path))
        with open(path, "w") as f:
            f.write("{not json")
        pw, changed = obs_prep.ensure_websocket(self.cfg)
        self.assertTrue(changed)
        self.assertEqual(obs_prep.read_ws_config(self.cfg)["server_password"], pw)


class TestIniEditing(Base):
    def test_only_the_named_values_change(self):
        path = self.make_profile()
        before = self.ini(path)
        obs_prep.set_ini_values(path, {"SimpleOutput": {"RecRB": "true"}})
        after = self.ini(path)
        self.assertEqual(after, before.replace(b"RecRB=false", b"RecRB=true"))

    def test_bom_crlf_and_key_case_survive(self):
        # configparser would lowercase OBS's CamelCase keys (which OBS reads
        # case-sensitively) and drop the BOM — the reason this is hand-rolled.
        path = self.make_profile()
        obs_prep.set_ini_values(path, {"AdvOut": {"RecRB": "true"}})
        raw = self.ini(path)
        self.assertTrue(raw.startswith(b"\xef\xbb\xbf"))
        self.assertNotIn(b"\n", raw.replace(b"\r\n", b""))
        self.assertIn(b"VBitrate=6000", raw)
        self.assertIn(b"RecRBSize=512", raw)

    def test_missing_key_lands_inside_its_section(self):
        path = self.make_profile()
        obs_prep.set_ini_values(path, {"SimpleOutput": {"RecRBPrefix": "Replay"}})
        text = self.ini(path).decode("utf-8-sig")
        simple = text.split("[SimpleOutput]")[1].split("[Output]")[0]
        self.assertIn("RecRBPrefix=Replay", simple)
        # Inserted before the blank separator line, not after it.
        self.assertTrue(simple.rstrip("\r\n").endswith("RecRBPrefix=Replay"))

    def test_missing_section_is_appended(self):
        path = self.make_profile()
        obs_prep.set_ini_values(path, {"Stream1": {"X": "1"}})
        text = self.ini(path).decode("utf-8-sig")
        self.assertTrue(text.rstrip().endswith("[Stream1]\r\nX=1"))

    def test_unchanged_file_is_not_rewritten(self):
        path = self.make_profile()
        self.assertFalse(obs_prep.set_ini_values(path, {"Output": {"Mode": "Simple"}}))


class TestReplayBuffer(Base):
    def test_enabled_in_both_output_modes(self):
        path = self.make_profile()
        self.assertEqual(obs_prep.ensure_replay_buffer(self.cfg), path)
        for section in ("SimpleOutput", "AdvOut"):
            self.assertEqual(obs_prep._read_ini_value(path, section, "RecRB"), "true")
            self.assertEqual(obs_prep._read_ini_value(path, section, "RecRBTime"), "25")

    def test_replay_folder_set_for_both_modes_and_created(self):
        path = self.make_profile()
        folder = os.path.join(self.tmp, "Replays")
        obs_prep.ensure_replay_buffer(self.cfg, folder)
        self.assertTrue(os.path.isdir(folder))
        want = folder.replace("\\", "/")
        self.assertEqual(obs_prep._read_ini_value(path, "SimpleOutput", "FilePath"), want)
        self.assertEqual(obs_prep._read_ini_value(path, "AdvOut", "RecFilePath"), want)

    def test_one_backup_of_the_original(self):
        path = self.make_profile()
        original = self.ini(path)
        obs_prep.ensure_replay_buffer(self.cfg)
        obs_prep.ensure_replay_buffer(self.cfg, os.path.join(self.tmp, "R"))
        self.assertEqual(self.ini(path + ".cricketstream-backup"), original)

    def test_profile_found_through_pre_31_global_ini(self):
        path = self.make_profile(ini_name="global.ini", profile="Club")
        self.assertEqual(obs_prep.active_profile_ini(self.cfg), path)

    def test_user_ini_wins_over_global_ini(self):
        self.make_profile(ini_name="global.ini", profile="Old")
        path = self.make_profile(ini_name="user.ini", profile="New")
        self.assertEqual(obs_prep.active_profile_ini(self.cfg), path)

    def test_never_opened_obs_has_no_profile(self):
        self.assertIsNone(obs_prep.ensure_replay_buffer(self.cfg))


class TestCrashSentinel(Base):
    """The marker OBS 30+ leaves after a crash, which makes it stop on a Safe Mode prompt."""

    def sentinel(self, name):
        folder = os.path.join(self.cfg, ".sentinel")
        os.makedirs(folder, exist_ok=True)
        open(os.path.join(folder, name), "w").close()
        return os.path.join(folder, name)

    def test_leftover_run_markers_removed(self):
        a = self.sentinel("run_c887aaf3-ecd5-4b72-a795-6f49aaefc6f1")
        b = self.sentinel("run_11111111-2222-3333-4444-555555555555")
        self.assertEqual(obs_prep.clear_crash_sentinel(self.cfg), 2)
        self.assertFalse(os.path.exists(a) or os.path.exists(b))

    def test_other_files_left_alone(self):
        other = self.sentinel("something_else")
        obs_prep.clear_crash_sentinel(self.cfg)
        self.assertTrue(os.path.exists(other))

    def test_no_folder(self):
        self.assertEqual(obs_prep.clear_crash_sentinel(self.cfg), 0)


class TestVideoSettings(Base):
    def files(self, config="", mbps=None):
        cfg = os.path.join(self.tmp, "config.ini")
        with open(cfg, "w", encoding="utf-8") as f:
            f.write(config)
        st = os.path.join(self.tmp, "match_state.json")
        with open(st, "w", encoding="utf-8") as f:
            json.dump({"network_test_mbps": mbps} if mbps else {}, f)
        return cfg, st

    def test_no_speed_test_yet_is_720p30(self):
        self.assertEqual(obs_prep.choose_video(*self.files())[:2], ("720p", "30"))

    def test_speed_test_drives_the_resolution(self):
        self.assertEqual(obs_prep.choose_video(*self.files(mbps=2.0))[:2], ("720p", "30"))
        self.assertEqual(obs_prep.choose_video(*self.files(mbps=8.0))[:2], ("1080p", "30"))

    def test_config_wins_over_the_speed_test(self):
        cfg = "[Stream]\noutput_resolution = 720p\nfps = 25\n"
        self.assertEqual(obs_prep.choose_video(*self.files(cfg, mbps=8.0))[:2], ("720p", "25"))

    def test_nonsense_in_config_is_ignored(self):
        cfg = "[Stream]\noutput_resolution = 4k\nfps = 120\n"
        self.assertEqual(obs_prep.choose_video(*self.files(cfg))[:2], ("720p", "30"))

    def test_canvas_is_always_1920x1080(self):
        # The overlay is a fixed 1920x1080 browser source; a fresh OBS sizes its canvas to
        # the laptop's screen, which put most of the overlay off the edge.
        path = self.make_profile(body=BASIC_INI + "\r\n[Video]\r\nBaseCX=1366\r\nBaseCY=768\r\n")
        obs_prep.ensure_video_settings(self.cfg, "720p", "30")
        get = lambda k: obs_prep._read_ini_value(path, "Video", k)
        self.assertEqual((get("BaseCX"), get("BaseCY")), ("1920", "1080"))
        self.assertEqual((get("OutputCX"), get("OutputCY")), ("1280", "720"))
        self.assertEqual((get("FPSType"), get("FPSInt")), ("1", "30"))

    def test_server_shows_the_same_recommendation_quickstart_applies(self):
        import server
        for mbps in (0.5, 2.0, 4.0, 10.0):
            self.assertEqual(server._recommend_bitrate_and_resolution(mbps),
                             obs_prep.recommend_stream_settings(mbps))


LOG_WITH_ENCODERS = """14:32:37.044: Available Encoders:
14:32:37.044:   Video Encoders:
14:32:37.044: 	- ffmpeg_svt_av1 (SVT-AV1)
14:32:37.044: 	- obs_nvenc_h264_tex (NVIDIA NVENC H.264)
14:32:37.044: 	- obs_nvenc_hevc_tex (NVIDIA NVENC HEVC)
14:32:37.044: 	- obs_x264 (x264)
14:32:37.044:   Audio Encoders:
14:32:37.044: 	- ffmpeg_aac (FFmpeg AAC)
"""


class TestEncoder(Base):
    def log(self, text, name="2026-10-01 14-32-35.txt"):
        folder = os.path.join(self.cfg, "logs")
        os.makedirs(folder, exist_ok=True)
        with open(os.path.join(folder, name), "w", encoding="utf-8") as f:
            f.write(text)

    def profile(self, stream="x264", rec="x264", mode="Simple"):
        body = BASIC_INI.replace("[Output]\r\nMode=Simple", f"[Output]\r\nMode={mode}")
        path = self.make_profile(body=body)
        obs_prep.set_ini_values(path, {"SimpleOutput": {"StreamEncoder": stream, "RecEncoder": rec}})
        return path

    def get(self, path, key):
        return obs_prep._read_ini_value(path, "SimpleOutput", key)

    def test_reads_the_list_from_obs_own_log(self):
        # The real format, from an OBS 32.1.1 log on this project's dev machine.
        self.log(LOG_WITH_ENCODERS)
        self.assertEqual(obs_prep.available_encoders(self.cfg),
                         ["ffmpeg_svt_av1", "obs_nvenc_h264_tex", "obs_nvenc_hevc_tex", "obs_x264"])

    def test_a_cut_short_log_falls_back_to_an_older_one(self):
        self.log(LOG_WITH_ENCODERS, "2026-10-01 10-00-00.txt")
        self.log("14:40:00.000: crashed during startup\n", "2026-10-01 14-40-00.txt")
        self.assertIn("obs_nvenc_h264_tex", obs_prep.available_encoders(self.cfg))

    def test_hardware_choice_per_vendor(self):
        pick = lambda ids: (obs_prep.pick_hardware_encoder(ids) or (None,))[0]
        self.assertEqual(pick(["obs_nvenc_h264_tex", "obs_x264"]), "nvenc")
        self.assertEqual(pick(["jim_nvenc", "obs_x264"]), "nvenc")              # older OBS
        self.assertEqual(pick(["h264_texture_amf", "obs_x264"]), "amd")
        self.assertEqual(pick(["obs_qsv11_v2", "obs_x264"]), "qsv")
        self.assertEqual(pick(["com.apple.videotoolbox.videoencoder.ave.avc"]), "apple_h264")
        self.assertEqual(pick(["obs_nvenc_hevc_tex", "obs_qsv11_av1", "obs_x264"]), None)
        self.assertEqual(pick(["obs_x264"]), None)

    def test_cpu_encoding_moved_to_hardware(self):
        self.log(LOG_WITH_ENCODERS)
        path = self.profile()
        changed, msg = obs_prep.ensure_encoder(self.cfg)
        self.assertTrue(changed)
        self.assertIn("NVENC", msg)
        self.assertEqual((self.get(path, "StreamEncoder"), self.get(path, "RecEncoder")),
                         ("nvenc", "nvenc"))

    def test_unset_encoder_counts_as_cpu(self):
        # A fresh profile has no StreamEncoder line at all.
        self.log(LOG_WITH_ENCODERS)
        path = self.make_profile()
        self.assertTrue(obs_prep.ensure_encoder(self.cfg)[0])
        self.assertEqual(self.get(path, "StreamEncoder"), "nvenc")

    def test_hardware_already_chosen_is_left_alone(self):
        self.log(LOG_WITH_ENCODERS)
        path = self.profile(stream="qsv", rec="qsv")
        self.assertEqual(obs_prep.ensure_encoder(self.cfg), (False, None))
        self.assertEqual(self.get(path, "StreamEncoder"), "qsv")

    def test_advanced_mode_is_left_alone(self):
        self.log(LOG_WITH_ENCODERS)
        path = self.profile(mode="Advanced")
        self.assertEqual(obs_prep.ensure_encoder(self.cfg), (False, None))
        self.assertEqual(self.get(path, "StreamEncoder"), "x264")

    def test_no_hardware_encoder_leaves_x264(self):
        self.log(LOG_WITH_ENCODERS.replace("obs_nvenc", "obs_other"))
        path = self.profile()
        self.assertEqual(obs_prep.ensure_encoder(self.cfg), (False, None))
        self.assertEqual(self.get(path, "StreamEncoder"), "x264")

    def test_no_log_yet_does_nothing(self):
        path = self.profile()
        self.assertEqual(obs_prep.ensure_encoder(self.cfg), (False, None))
        self.assertEqual(self.get(path, "StreamEncoder"), "x264")


class TestReplayFlag(Base):
    def collection(self, sources):
        with open(os.path.join(self.cfg, "user.ini"), "w", encoding="utf-8") as f:
            f.write("[Basic]\nSceneCollectionFile=Untitled.json\n")
        folder = os.path.join(self.cfg, "basic", "scenes")
        os.makedirs(folder, exist_ok=True)
        with open(os.path.join(folder, "Untitled.json"), "w", encoding="utf-8") as f:
            json.dump({"sources": [{"name": n} for n in sources]}, f)

    def test_fresh_obs_launches_without_starting_the_buffer(self):
        # An empty scene makes OBS stop on a modal "No Sources" question instead.
        self.collection([])
        self.assertNotIn("--startreplaybuffer", obs_prep.launch_args(self.cfg))

    def test_set_up_obs_starts_the_buffer_at_launch(self):
        self.collection(["Overlay", "ReplayClip", "Main", "Replay"])
        self.assertIn("--startreplaybuffer", obs_prep.launch_args(self.cfg))

    def test_nothing_readable_means_not_ready(self):
        self.assertFalse(obs_prep.scenes_ready(self.cfg))


class TestUpdateCheck(Base):
    """OBS's "New update available" box, paused for the match and put back after."""

    def setting(self):
        return obs_prep._read_ini_value(os.path.join(self.cfg, "global.ini"),
                                        "General", "EnableAutoUpdates")

    def marker(self):
        return os.path.exists(os.path.join(self.cfg, obs_prep.UPDATES_MARKER))

    def global_ini(self, body):
        with open(os.path.join(self.cfg, "global.ini"), "w", encoding="utf-8") as f:
            f.write(body)

    def test_default_on_is_paused_then_restored(self):
        # The real layout: OBS 32 keeps EnableAutoUpdates under [General] in global.ini.
        self.global_ini("[General]\nLastUpdateCheck=0\nEnableAutoUpdates=true\n")
        self.assertTrue(obs_prep.pause_update_check(self.cfg))
        self.assertEqual(self.setting(), "false")
        self.assertTrue(obs_prep.resume_update_check(self.cfg))
        self.assertEqual(self.setting(), "true")
        self.assertFalse(self.marker())

    def test_never_set_is_restored_to_on(self):
        # A fresh OBS has no global.ini (or an empty one): its default is on.
        obs_prep.pause_update_check(self.cfg)
        self.assertEqual(self.setting(), "false")
        obs_prep.resume_update_check(self.cfg)
        self.assertEqual(self.setting(), "true")

    def test_club_that_turned_it_off_is_never_turned_on(self):
        self.global_ini("[General]\nEnableAutoUpdates=false\n")
        self.assertFalse(obs_prep.pause_update_check(self.cfg))
        self.assertFalse(self.marker())
        self.assertFalse(obs_prep.resume_update_check(self.cfg))
        self.assertEqual(self.setting(), "false")

    def test_second_match_day_keeps_the_original(self):
        # Paused, never resumed (server stopped before OBS closed), paused again: the
        # marker must still say "on", not the "false" we wrote ourselves.
        self.global_ini("[General]\nEnableAutoUpdates=true\n")
        obs_prep.pause_update_check(self.cfg)
        self.assertFalse(obs_prep.pause_update_check(self.cfg))
        obs_prep.resume_update_check(self.cfg)
        self.assertEqual(self.setting(), "true")

    def test_nothing_to_resume(self):
        self.assertFalse(obs_prep.resume_update_check(self.cfg))


class TestFirstRunWizard(Base):
    """OBS's Auto-Configuration Wizard: a modal on a brand-new OBS that nobody answers,
    and which blocked the first-run restart from closing OBS."""

    def flag(self, name):
        return obs_prep._read_ini_value(os.path.join(self.cfg, name), "General", "FirstRun")

    def test_brand_new_obs(self):
        self.assertTrue(obs_prep.skip_first_run_wizard(self.cfg))
        self.assertEqual((self.flag("user.ini"), self.flag("global.ini")), ("true", "true"))

    def test_other_settings_kept(self):
        with open(os.path.join(self.cfg, "user.ini"), "w", encoding="utf-8") as f:
            f.write("[General]\nConfirmOnExit=true\n\n[Basic]\nProfile=Club\n")
        obs_prep.skip_first_run_wizard(self.cfg)
        path = os.path.join(self.cfg, "user.ini")
        self.assertEqual(obs_prep._read_ini_value(path, "General", "ConfirmOnExit"), "true")
        self.assertEqual(obs_prep._read_ini_value(path, "Basic", "Profile"), "Club")

    def test_already_run_is_left_alone(self):
        for name in ("user.ini", "global.ini"):
            with open(os.path.join(self.cfg, name), "w", encoding="utf-8") as f:
                f.write("[General]\nFirstRun=true\n")
        self.assertFalse(obs_prep.skip_first_run_wizard(self.cfg))


class TestFirstRunRestart(unittest.TestCase):
    """obs_setup.setup_from_config: on a brand-new OBS the replay buffer can't start until
    OBS restarts; when we manage OBS, that restart happens by itself (verified on OBS
    32.1.1: one press of Start OBS, buffer running ten seconds later)."""

    NEEDS = (True, ["  ✓ Replay buffer enabled, 25s",
                    "  ⚠ Replay buffer enabled but wouldn't start yet — restart OBS once"])
    FINE = (True, ["  ✓ Replay buffer already running"])

    def run_setup(self, results, manage="", allow=True, closes=True):
        import obs_setup
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        cfg = os.path.join(tmp, "config.ini")
        with open(cfg, "w", encoding="utf-8") as f:
            f.write(f"[OBS]\nobs_password = pw\nmanage_obs = {manage}\n")
        with mock.patch.object(obs_setup, "obs_setup", side_effect=list(results)) as setup, \
             mock.patch.object(obs_prep, "close_obs", return_value=closes) as close, \
             mock.patch.object(obs_prep, "prepare_obs", return_value=True) as prep:
            ok, msgs = obs_setup.setup_from_config(cfg, allow_restart=allow)
        return ok, msgs, setup.call_count, close.call_count, prep.call_count

    def test_restarts_once_and_finishes(self):
        ok, msgs, setups, closes, preps = self.run_setup([self.NEEDS, self.FINE])
        self.assertTrue(ok)
        self.assertEqual((setups, closes, preps), (2, 1, 1))
        self.assertTrue(any("Restarted OBS once" in m for m in msgs))

    def test_no_restart_when_not_needed(self):
        _, _, setups, closes, _ = self.run_setup([self.FINE])
        self.assertEqual((setups, closes), (1, 0))

    def test_standalone_run_never_restarts(self):
        # Someone running obs_setup.py by hand is managing OBS themselves.
        _, msgs, setups, closes, _ = self.run_setup([self.NEEDS], allow=False)
        self.assertEqual((setups, closes), (1, 0))
        self.assertTrue(any("restart OBS once" in m for m in msgs))

    def test_manage_obs_no_never_restarts(self):
        _, _, _, closes, _ = self.run_setup([self.NEEDS], manage="no")
        self.assertEqual(closes, 0)

    def test_obs_that_wont_close_is_left_running(self):
        # e.g. OBS asking "exit with outputs active?": never answered for anyone.
        _, _, setups, closes, preps = self.run_setup([self.NEEDS], closes=False)
        self.assertEqual((setups, closes, preps), (1, 1, 0))


class TestCloseObs(unittest.TestCase):
    def test_gives_up_by_cancelling_its_own_question(self):
        with open(os.path.join(REPO, "obs_prep.py"), encoding="utf-8") as f:
            src = f.read()
        fn = src[src.index("def close_obs("):src.index("def wait_for_websocket(")]
        self.assertIn("0x0010", fn)                     # WM_CLOSE, the window's X
        self.assertIn("= Cancel / No", fn)              # what it does to the leftover question
        body = fn.split('"""', 2)[2]
        for force in ("taskkill", "/F", "SIGKILL", "-KILL", "kill_obs"):   # never a force-kill
            self.assertNotIn(force, body)


class TestConfigPassword(Base):
    def config(self, body):
        path = os.path.join(self.tmp, "config.ini")
        with open(path, "w", encoding="utf-8") as f:
            f.write(body)
        return path

    def read(self, path):
        with open(path, encoding="utf-8") as f:
            return f.read()

    def test_blank_password_filled_in(self):
        path = self.config("[Club]\nname = X\n\n[OBS]\nobs_password = \nreplay_folder = R\n")
        self.assertTrue(obs_prep.sync_config_password(path, "pw1"))
        text = self.read(path)
        self.assertIn("obs_password = pw1\n", text)
        self.assertIn("replay_folder = R", text)
        self.assertIn("name = X", text)

    def test_same_password_is_a_no_op(self):
        path = self.config("[OBS]\nobs_password = pw1\n")
        self.assertFalse(obs_prep.sync_config_password(path, "pw1"))

    def test_missing_key_added_to_the_obs_section(self):
        path = self.config("[OBS]\nreplay_folder = R\n\n[Graphics]\nscorebar_style = classic\n")
        obs_prep.sync_config_password(path, "pw1")
        self.assertEqual(obs_prep._read_config_value(path, "OBS", "obs_password"), "pw1")
        self.assertEqual(obs_prep._read_config_value(path, "Graphics", "scorebar_style"),
                         "classic")

    def test_missing_section_added(self):
        path = self.config("[Club]\nname = X\n")
        obs_prep.sync_config_password(path, "pw1")
        self.assertEqual(obs_prep._read_config_value(path, "OBS", "obs_password"), "pw1")

    def test_no_config_file_is_left_alone(self):
        self.assertFalse(obs_prep.sync_config_password(os.path.join(self.tmp, "nope"), "x"))


class TestPrepareObs(Base):
    """The orchestration, with OBS's process and launch mocked out."""

    def setUp(self):
        super().setUp()
        self.config = os.path.join(self.tmp, "config.ini")
        with open(self.config, "w", encoding="utf-8") as f:
            f.write(f"[OBS]\nobs_password = \nreplay_folder = {self.tmp}/Replays\n")
        self.out = []
        patches = [
            mock.patch.object(obs_prep, "obs_config_dir", return_value=self.cfg),
            mock.patch.object(obs_prep, "find_obs", return_value="/fake/obs64.exe"),
            mock.patch.object(obs_prep, "wait_for_websocket", return_value=True),
            mock.patch.object(obs_prep.time, "sleep"),
        ]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        self.launch = mock.patch.object(obs_prep, "launch_obs").start()
        self.addCleanup(mock.patch.stopall)

    def run_prep(self, running, wait_enter=None):
        with mock.patch.object(obs_prep, "obs_running", side_effect=running):
            return obs_prep.prepare_obs(self.config, wait_enter=wait_enter,
                                        say=self.out.append)

    def test_closed_obs_is_configured_then_opened(self):
        ini = self.make_profile()
        self.assertTrue(self.run_prep([False]))
        conf = obs_prep.read_ws_config(self.cfg)
        self.assertTrue(conf["server_enabled"])
        self.assertEqual(obs_prep._read_config_value(self.config, "OBS", "obs_password"),
                         conf["server_password"])
        self.assertEqual(obs_prep._read_ini_value(ini, "SimpleOutput", "RecRB"), "true")
        self.launch.assert_called_once()
        self.assertEqual(self.launch.call_args[0][0], "/fake/obs64.exe")

    def sentinel(self):
        folder = os.path.join(self.cfg, ".sentinel")
        os.makedirs(folder, exist_ok=True)
        path = os.path.join(folder, "run_c887aaf3-ecd5-4b72-a795-6f49aaefc6f1")
        open(path, "w").close()
        return path

    def test_crash_marker_cleared_before_launch(self):
        # Otherwise OBS stops on "Run in Safe Mode?" with nobody there to answer it.
        marker = self.sentinel()
        self.launch.side_effect = lambda exe, args=None: self.assertFalse(os.path.exists(marker))
        self.assertTrue(self.run_prep([False]))
        self.launch.assert_called_once()
        self.assertIn("Safe Mode", " ".join(self.out))

    def test_open_obs_that_isnt_answering_is_not_trusted(self):
        # The settings file says enabled, but OBS is sat on the Safe Mode prompt (or in
        # Safe Mode, where the WebSocket plugin never loads). Reporting "already open"
        # would hand obs_setup an OBS it can't reach.
        self.write_ws({"server_enabled": True, "auth_required": True,
                       "server_password": "pw", "server_port": 4455})
        marker = self.sentinel()
        obs_prep.wait_for_websocket.return_value = False
        self.assertFalse(self.run_prep([True]))
        self.launch.assert_not_called()
        self.assertTrue(os.path.exists(marker))     # OBS's own live marker — not ours to touch
        self.assertIn("Safe Mode", " ".join(self.out))

    def test_stuck_obs_closed_by_operator_is_reopened_properly(self):
        self.write_ws({"server_enabled": True, "auth_required": True,
                       "server_password": "pw", "server_port": 4455})
        marker = self.sentinel()
        # Not answering while stuck; answering once relaunched.
        obs_prep.wait_for_websocket.side_effect = [False, True]
        prompts = []
        self.assertTrue(self.run_prep([True, True, False], wait_enter=prompts.append))
        self.assertEqual(len(prompts), 1)
        self.assertFalse(os.path.exists(marker))
        self.launch.assert_called_once()

    def test_video_set_while_closed(self):
        ini = self.make_profile()
        self.run_prep([False])
        self.assertEqual(obs_prep._read_ini_value(ini, "Video", "BaseCX"), "1920")
        self.assertIn("720p at 30fps", " ".join(self.out))

    def test_video_left_alone_when_manual(self):
        with open(self.config, "a", encoding="utf-8") as f:
            f.write("\n[Stream]\noutput_resolution = manual\n")
        ini = self.make_profile()
        self.run_prep([False])
        self.assertIsNone(obs_prep._read_ini_value(ini, "Video", "BaseCX"))

    def test_open_obs_with_working_websocket_is_not_touched(self):
        self.write_ws({"server_enabled": True, "auth_required": True,
                       "server_password": "pw", "server_port": 4455})
        ini = self.make_profile()
        before = self.ini(ini)
        self.assertTrue(self.run_prep([True]))
        self.launch.assert_not_called()
        self.assertEqual(self.ini(ini), before)   # OBS would overwrite it on exit anyway
        self.assertEqual(obs_prep._read_config_value(self.config, "OBS", "obs_password"),
                         "pw")

    def test_open_obs_with_websocket_off_writes_nothing_without_a_prompt(self):
        # Non-interactive (tests, the wizard's subprocess): never block, never write
        # files a running OBS will overwrite.
        self.assertFalse(self.run_prep([True]))
        self.assertIsNone(obs_prep.read_ws_config(self.cfg))
        self.launch.assert_not_called()

    def test_operator_can_carry_on_without_closing_obs(self):
        # pause() swallows Ctrl+C, so the wait must give up by itself rather than loop.
        prompts = []
        self.assertFalse(self.run_prep([True] * 20, wait_enter=prompts.append))
        self.assertEqual(len(prompts), 3)
        self.launch.assert_not_called()
        self.assertIn("Carrying on", " ".join(self.out))

    def test_open_obs_with_websocket_off_waits_for_it_to_close(self):
        prompts = []
        self.assertTrue(self.run_prep([True, True, False], wait_enter=prompts.append))
        self.assertEqual(len(prompts), 1)
        self.assertTrue(obs_prep.read_ws_config(self.cfg)["server_enabled"])
        self.launch.assert_called_once()

    def test_obs_not_installed(self):
        with mock.patch.object(obs_prep, "find_obs", return_value=None):
            self.assertFalse(self.run_prep([False]))
        self.assertIn("obsproject.com", " ".join(self.out))
        self.launch.assert_not_called()

    def test_fresh_obs_never_opened_still_gets_websocket(self):
        # No profile yet: the replay buffer falls back to obs_setup's existing path, but
        # the WebSocket server — the step that needed a human — is still switched on.
        self.assertTrue(self.run_prep([False]))
        self.assertTrue(obs_prep.read_ws_config(self.cfg)["server_enabled"])
        self.launch.assert_called_once()


class TestLaunch(unittest.TestCase):
    def test_launch_args(self):
        self.assertIn("--startreplaybuffer", obs_prep.LAUNCH_ARGS)

    @unittest.skipIf(sys.platform == "darwin", "Mac launches through `open -a`")
    def test_launched_from_its_own_folder(self):
        # OBS on Windows can't find its data files from any other working directory.
        with mock.patch.object(obs_prep.subprocess, "Popen") as popen:
            obs_prep.launch_obs(os.path.join("C:", os.sep, "obs", "bin", "obs64.exe"))
        self.assertEqual(popen.call_args.kwargs["cwd"],
                         os.path.join("C:", os.sep, "obs", "bin"))


class TestQuickstartWiring(unittest.TestCase):
    def test_prepare_runs_before_config_is_loaded(self):
        # It can write the password into config.ini, so loading config first would hand
        # obs_setup the old (blank) password on the very run that fixed it.
        with open(os.path.join(REPO, "quickstart.py"), encoding="utf-8") as f:
            src = f.read()
        main = src[src.index("def main():"):]
        self.assertLess(main.index("obs_prep.prepare_obs("), main.index("cfg = load_config()"))
        self.assertIn("obs_prep.manage_obs_enabled(config_path)", main)

    def test_blank_bitrate_uses_the_recommendation_but_manual_opts_out(self):
        import obs_setup
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        st = os.path.join(tmp, "match_state.json")
        with open(st, "w", encoding="utf-8") as f:
            json.dump({"network_test_mbps": 4.0}, f)
        for raw, want in (("", 3000), ("2500", 2500), ("manual", 0)):
            cfg = os.path.join(tmp, "config.ini")
            with open(cfg, "w", encoding="utf-8") as f:
                f.write(f"[OBS]\nobs_password = pw\n\n[Stream]\nbitrate_kbps = {raw}\n")
            with mock.patch.object(obs_setup, "obs_setup", return_value=(True, [])) as call:
                obs_setup.setup_from_config(cfg, st)
            self.assertEqual(call.call_args.kwargs["bitrate_kbps"], want, raw)
            self.assertEqual(call.call_args.kwargs["password"], "pw")

    def test_quickstart_uses_the_shared_config_reader(self):
        with open(os.path.join(REPO, "quickstart.py"), encoding="utf-8") as f:
            self.assertIn("setup_from_config(", f.read())


if __name__ == "__main__":
    unittest.main()
