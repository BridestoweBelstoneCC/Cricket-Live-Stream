"""
OBS preparation — the part of OBS setup that has to happen while OBS is CLOSED
───────────────────────────────────────────────────────────────────────────────
obs_setup.py configures OBS over its WebSocket, which leaves three things it can't
do, because each needs OBS not to be running yet:

  1. Turn the WebSocket server on in the first place. There's no API for that (the
     API IS the WebSocket), so a new club used to do it by hand in Tools -> WebSocket
     Server Settings and then copy a password between OBS and the setup interview.
  2. Enable the replay buffer so it exists at startup. obs_setup can tick the box over
     the WebSocket, but OBS only creates outputs when it starts, so the first run
     needed an unexpected "restart OBS and run setup again".
  3. Open OBS itself, so match day is one double-click, not "open OBS first".

This module does all three by editing OBS's own config files before launching it:
plugin_config/obs-websocket/config.json for the WebSocket server, and the active
profile's basic.ini for the replay buffer. Both are written only while OBS is not
running, because OBS writes its in-memory copy back over them when it exits.

Stdlib only, because the frozen launcher (cricketstream.py) imports it, and that
exe has none of the project's packages.

Every step is best-effort. A failure here leaves things exactly where they were
before this module existed: obs_setup.py still runs, still reports what it couldn't
do, and the setup guides still describe the manual steps.
"""
import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import time

WS_PORT = 4455
REPLAY_SECONDS = "25"

# --startreplaybuffer: the buffer is enabled in basic.ini below, so start it at launch.
# --disable-shutdown-check: suppresses the crash prompt on OBS releases that still have
# it. It is NOT what stops the prompt on OBS 30+: OBS 32.1.1 contains no trace of the
# option and showed the prompt with it passed. clear_crash_sentinel() is what does that.
LAUNCH_ARGS = ["--startreplaybuffer", "--disable-shutdown-check"]


# ── Where OBS lives ──────────────────────────────────────────────────────────

def obs_config_dir():
    """OBS's per-user settings folder (it may not exist yet on a fresh install)."""
    if sys.platform == "win32":
        base = os.environ.get("APPDATA") or os.path.expanduser(r"~\AppData\Roaming")
        return os.path.join(base, "obs-studio")
    if sys.platform == "darwin":
        return os.path.expanduser("~/Library/Application Support/obs-studio")
    return os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config"),
                        "obs-studio")


def find_obs():
    """Path to the OBS executable (the .app bundle on a Mac), or None."""
    if sys.platform == "win32":
        candidates = []
        try:
            import winreg
            # The installer records its folder here; honours a non-default install drive.
            for hive in (winreg.HKEY_LOCAL_MACHINE, winreg.HKEY_CURRENT_USER):
                try:
                    with winreg.OpenKey(hive, r"SOFTWARE\OBS Studio") as k:
                        folder = winreg.QueryValue(k, None)
                    if folder:
                        candidates.append(os.path.join(folder, "bin", "64bit", "obs64.exe"))
                except OSError:
                    pass
        except ImportError:
            pass
        for env in ("ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"):
            if os.environ.get(env):
                candidates.append(os.path.join(os.environ[env], "obs-studio",
                                               "bin", "64bit", "obs64.exe"))
        for c in candidates:
            if os.path.isfile(c):
                return c
        return None
    if sys.platform == "darwin":
        for c in ("/Applications/OBS.app", os.path.expanduser("~/Applications/OBS.app")):
            if os.path.isdir(c):
                return c
        return None
    return shutil.which("obs")


def obs_running():
    """True if an OBS process is running. Errs towards True when it can't tell, because
    the cost of a wrong False is writing config files OBS will then overwrite on exit."""
    try:
        if sys.platform == "win32":
            out = subprocess.run(["tasklist", "/FI", "IMAGENAME eq obs64.exe", "/NH"],
                                 capture_output=True, text=True, timeout=15)
            return "obs64.exe" in out.stdout.lower()
        out = subprocess.run(["pgrep", "-x", "-i", "obs"],
                             capture_output=True, text=True, timeout=15)
        return out.returncode == 0
    except Exception:
        return True


def obs_pids():
    """PIDs of running OBS processes (Windows only; [] elsewhere or on error)."""
    if sys.platform != "win32":
        return []
    try:
        out = subprocess.run(["tasklist", "/FI", "IMAGENAME eq obs64.exe", "/FO", "CSV", "/NH"],
                             capture_output=True, text=True, timeout=15).stdout
    except Exception:
        return []
    pids = []
    for line in out.splitlines():
        cols = [c.strip('"') for c in line.split('","')]
        if len(cols) > 1 and cols[0].lower().strip('"') == "obs64.exe" and cols[1].isdigit():
            pids.append(int(cols[1]))
    return pids


def obs_window_hung(timeout=5, pids=None):
    """Is OBS frozen ("Not Responding")? True / False, or None when it can't tell.

    Pings OBS's window with a no-op message (WM_NULL) and waits up to timeout seconds
    for it to be handled, which is what a frozen UI thread can't do. Deliberately NOT
    IsHungAppWindow alone, the check behind Windows' own "(Not Responding)": that only
    fires once messages are queued and unhandled, so a frozen OBS that nobody is
    clicking on or moving the mouse over can stay "responding" indefinitely. That was
    found by testing: a suspended OBS read as responding for two minutes straight. The
    ping queues the message itself, so it doesn't depend on someone touching the laptop.

    A modal dialog (an update prompt, say) is NOT a freeze; its message loop answers
    for the window behind it. No equivalent on other platforms, so there it's always
    None and freeze recovery never acts. Blocks for up to `timeout` seconds.
    """
    if sys.platform != "win32":
        return None
    # pids: the caller's cached OBS process ids, so a check every few seconds doesn't
    # spawn `tasklist` every time. None found for them means they're stale: say None.
    pids = set(pids) if pids else set(obs_pids())
    if not pids:
        return None
    try:
        import ctypes
        import ctypes.wintypes as wt
        # use_last_error: ctypes snapshots GetLastError straight after each call; reading
        # it later via kernel32 can see a value overwritten by Python itself in between.
        u32 = ctypes.WinDLL("user32", use_last_error=True)
        found = []

        @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
        def collect(hwnd, _):
            pid = wt.DWORD()
            u32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
            if pid.value in pids and u32.IsWindowVisible(hwnd):
                found.append(hwnd)
            return True

        u32.EnumWindows(collect, 0)
        if not found:
            return None
        # Every OBS window shares one UI thread, so one answer speaks for all of them.
        WM_NULL, SMTO_ABORTIFHUNG, ERROR_TIMEOUT = 0x0000, 0x0002, 1460
        result = ctypes.c_size_t()
        ctypes.set_last_error(0)
        ok = u32.SendMessageTimeoutW(found[0], WM_NULL, 0, 0, SMTO_ABORTIFHUNG,
                                     int(timeout * 1000), ctypes.byref(result))
        if ok:
            return False
        # Zero with ERROR_TIMEOUT (or zero error: SMTO_ABORTIFHUNG on an already-hung
        # window) is a freeze. Anything else — the window closed mid-check — isn't.
        return ctypes.get_last_error() in (ERROR_TIMEOUT, 0)
    except Exception:
        return None


def kill_obs():
    """Force-close OBS. Only for an OBS that's already frozen: a force-close leaves the
    crash marker behind, which is what lets the crash recovery reopen it afterwards."""
    for pid in obs_pids():
        try:
            subprocess.run(["taskkill", "/F", "/PID", str(pid)],
                           capture_output=True, timeout=15)
        except Exception:
            pass


def port_open(port=WS_PORT, host="127.0.0.1", timeout=1.0):
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


# ── WebSocket server: plugin_config/obs-websocket/config.json (OBS 28+) ──────

def ws_config_path(cfg_dir):
    return os.path.join(cfg_dir, "plugin_config", "obs-websocket", "config.json")


def read_ws_config(cfg_dir):
    try:
        with open(ws_config_path(cfg_dir), encoding="utf-8-sig") as f:
            data = json.load(f)
        return data if isinstance(data, dict) else None
    except (OSError, ValueError):
        return None


def ws_usable(conf):
    """Enabled, and either open or with a password we can read."""
    return bool(conf and conf.get("server_enabled")
                and (not conf.get("auth_required") or conf.get("server_password")))


def ensure_websocket(cfg_dir, port=WS_PORT):
    """Make sure OBS's WebSocket server is on. Returns (password, changed).

    An existing working setup is left alone and its password reused, so a club already
    running other tools against OBS doesn't have them broken. Only an off or unusable
    server is rewritten, with a fresh random password. Must only be called while OBS is
    closed. Raises OSError if the file can't be written.
    """
    conf = read_ws_config(cfg_dir)
    if ws_usable(conf):
        return (conf.get("server_password", "") if conf.get("auth_required") else ""), False

    conf = dict(conf or {})
    password = secrets.token_urlsafe(16)
    conf.update({
        # first_load=true makes obs-websocket generate its OWN password on next start
        # and save over ours; false says "already configured, use what's here".
        "first_load":      False,
        "server_enabled":  True,
        "server_port":     int(conf.get("server_port") or port),
        "auth_required":   True,
        "server_password": password,
        "alerts_enabled":  bool(conf.get("alerts_enabled", False)),
    })
    path = ws_config_path(cfg_dir)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    _atomic_write(path, (json.dumps(conf, indent=2) + "\n").encode("utf-8"))
    return password, True


# ── Replay buffer: the active profile's basic.ini ────────────────────────────

def _read_ini_value(path, section, key):
    try:
        with open(path, encoding="utf-8-sig") as f:
            lines = f.read().splitlines()
    except OSError:
        return None
    current = None
    for line in lines:
        s = line.strip()
        if s.startswith("[") and s.endswith("]"):
            current = s[1:-1]
        elif current == section and "=" in s and s.split("=", 1)[0] == key:
            return s.split("=", 1)[1]
    return None


def active_profile_ini(cfg_dir):
    """basic.ini of the profile OBS will open, or None if OBS has never been run."""
    # OBS 31 moved the profile choice from global.ini to user.ini; read the new one first.
    for name in ("user.ini", "global.ini"):
        folder = _read_ini_value(os.path.join(cfg_dir, name), "Basic", "ProfileDir")
        if folder:
            path = os.path.join(cfg_dir, "basic", "profiles", folder, "basic.ini")
            if os.path.isfile(path):
                return path
    return None


def set_ini_values(path, updates):
    """Set {section: {key: value}} in an INI file, changing nothing else.

    Hand-rolled rather than configparser because configparser would lowercase every key
    (OBS's are CamelCase and case-sensitive), drop the BOM OBS writes, and reorder the
    file. Returns True if anything changed.
    """
    with open(path, "rb") as f:
        raw = f.read()
    bom = raw.startswith(b"\xef\xbb\xbf")
    text = raw[3:].decode("utf-8") if bom else raw.decode("utf-8")
    nl = "\r\n" if "\r\n" in text else "\n"
    lines = text.splitlines()

    changed = False
    for section, values in updates.items():
        start = next((i for i, l in enumerate(lines) if l.strip() == f"[{section}]"), None)
        if start is None:
            if lines and lines[-1].strip():
                lines.append("")
            lines.append(f"[{section}]")
            lines.extend(f"{k}={v}" for k, v in values.items())
            changed = True
            continue
        end = next((i for i in range(start + 1, len(lines))
                    if lines[i].strip().startswith("[")), len(lines))
        for key, value in values.items():
            hit = next((i for i in range(start + 1, end)
                        if lines[i].split("=", 1)[0] == key), None)
            if hit is not None:
                if lines[hit] != f"{key}={value}":
                    lines[hit] = f"{key}={value}"
                    changed = True
            else:
                # After the section's last real line, not after the blank line before
                # the next section header.
                at = end
                while at > start + 1 and not lines[at - 1].strip():
                    at -= 1
                lines.insert(at, f"{key}={value}")
                end += 1
                changed = True

    if changed:
        out = nl.join(lines) + nl
        _atomic_write(path, (b"\xef\xbb\xbf" if bom else b"") + out.encode("utf-8"))
    return changed


def ensure_replay_buffer(cfg_dir, replay_folder=""):
    """Enable the replay buffer (and point it at replay_folder) in the active profile.

    Same settings obs_setup.py makes over the WebSocket — RecRB in BOTH output-mode
    sections, so switching Simple <-> Advanced later doesn't silently turn replays off —
    but written before OBS starts, so the buffer exists at startup and the first-run
    "restart OBS once" step disappears. The folder matters for the same reason: launched
    with --startreplaybuffer, the buffer starts with whatever path basic.ini holds, before
    obs_setup gets the chance to set it, and the server only looks for clips in
    replay_folder.

    Returns the basic.ini path if it was prepared, None if there's no profile yet (OBS
    never opened; obs_setup's existing fallback covers that run). Keeps one backup of the
    original file the first time it changes anything. Must only be called while OBS is
    closed.
    """
    path = active_profile_ini(cfg_dir)
    if not path:
        return None
    simple = {"RecRB": "true", "RecRBTime": REPLAY_SECONDS}
    adv = {"RecRB": "true", "RecRBTime": REPLAY_SECONDS}
    if replay_folder:
        try:
            os.makedirs(replay_folder, exist_ok=True)
        except OSError:
            pass
        if os.path.isdir(replay_folder):
            # OBS stores paths with forward slashes on every platform.
            simple["FilePath"] = replay_folder.replace("\\", "/")
            adv["RecFilePath"] = replay_folder.replace("\\", "/")
    backup = path + ".cricketstream-backup"
    if not os.path.exists(backup):
        try:
            shutil.copy2(path, backup)
        except OSError:
            pass
    set_ini_values(path, {"SimpleOutput": simple, "AdvOut": adv})
    return path


# ── Safe Mode prompt: the .sentinel folder ───────────────────────────────────

def clear_crash_sentinel(cfg_dir):
    """Remove the marker that makes OBS ask about Safe Mode on its next start.

    OBS 30+ drops an empty .sentinel/run_<uuid> file while it runs and deletes it on a
    clean exit, so one left behind means the last run crashed (or the laptop lost power,
    or something force-closed it). Next start, OBS stops on a modal "Run in Safe Mode?"
    prompt. Nobody is at the laptop to answer it on a match day, and if they pick Safe
    Mode, OBS skips the WebSocket plugin and nothing here can reach it. Clearing the
    marker skips the question and launches normally, which was verified on OBS 32.1.1:
    with it cleared, three force-kill-and-relaunch cycles logged no crash and no prompt.
    With it left, the next launch prompted.

    Must only be called while OBS is closed; a running OBS's marker is its own live one.
    Returns how many markers were removed.
    """
    removed = 0
    folder = os.path.join(cfg_dir, ".sentinel")
    try:
        names = os.listdir(folder)
    except OSError:
        return 0
    for name in names:
        if name.startswith("run_"):
            try:
                os.remove(os.path.join(folder, name))
                removed += 1
            except OSError:
                pass
    return removed


# ── Video: canvas, output resolution, frame rate ─────────────────────────────

# overlay.html is a fixed 1920x1080 browser source placed at 0,0, so the canvas MUST be
# this size. A fresh OBS sizes its canvas to the laptop's screen instead: on a 1366x768
# laptop most of the overlay sat off the edge, on a 2560x1600 one it filled a corner.
CANVAS = (1920, 1080)
RESOLUTIONS = {"720p": (1280, 720), "1080p": (1920, 1080)}
FPS_CHOICES = ("25", "30")
DEFAULT_VIDEO = ("720p", "30")     # no speed test yet: the tier any ground can carry


def recommend_stream_settings(upload_mbps):
    """(bitrate_kbps, resolution, fps, note) for a measured upload speed.

    Keeps the bitrate comfortably under the measured speed (25% headroom) so real-world
    jitter doesn't cause buffering, then picks the resolution that bitrate can carry well.
    Shared by quickstart (to apply it) and the server's /obs/stream_check (to show it).
    """
    safe_kbps = int(upload_mbps * 1000 * 0.75)
    if safe_kbps < 1500:
        return max(safe_kbps, 800), "720p", 30, "Upload speed is limited — 720p30 keeps quality watchable without buffering."
    if safe_kbps < 2800:
        return safe_kbps, "720p", 30, "Enough headroom for a clean 720p30 stream."
    if safe_kbps < 4500:
        return min(safe_kbps, 4000), "1080p", 30, "Good enough for 1080p30 — the standard for this project."
    return min(safe_kbps, 6000), "1080p", 30, "Plenty of headroom for a strong 1080p30 stream (60fps rarely helps for cricket — the action is slower-moving than most sports)."


def last_upload_mbps(state_file):
    """The server's cached upload-speed test from match_state.json, or None."""
    try:
        with open(state_file, encoding="utf-8") as f:
            mbps = json.load(f).get("network_test_mbps")
        return float(mbps) if mbps else None
    except (OSError, ValueError, TypeError, AttributeError):
        return None


def choose_video(config_file, state_file):
    """(resolution, fps, why) — config.ini's [Stream] output_resolution / fps if set to
    something valid, otherwise the last upload test's recommendation, otherwise 720p30."""
    res_cfg = _read_config_value(config_file, "Stream", "output_resolution").lower()
    fps_cfg = _read_config_value(config_file, "Stream", "fps")
    mbps = last_upload_mbps(state_file)
    if mbps:
        _, res, fps, _ = recommend_stream_settings(mbps)
        res, fps, why = res, str(fps), f"from your last upload test ({mbps:.1f} Mbps)"
    else:
        (res, fps), why = DEFAULT_VIDEO, "no upload test yet"
    if res_cfg in RESOLUTIONS:
        res, why = res_cfg, "set in config.ini"
    if fps_cfg in FPS_CHOICES:
        fps = fps_cfg
    return res, fps, why


def ensure_video_settings(cfg_dir, resolution, fps):
    """Write canvas/output/frame rate into the active profile. Returns True if changed,
    None if there's no profile yet. Must only be called while OBS is closed: OBS refuses
    to change video settings while any output runs, and the replay buffer always does."""
    path = active_profile_ini(cfg_dir)
    if not path:
        return None
    out_w, out_h = RESOLUTIONS[resolution]
    return set_ini_values(path, {"Video": {
        "BaseCX": str(CANVAS[0]), "BaseCY": str(CANVAS[1]),
        "OutputCX": str(out_w), "OutputCY": str(out_h),
        # FPSType 1 = "Integer FPS Value", which reads FPSInt as a plain number. Not the
        # default type 0 ("Common"): that matches FPSCommon against OBS's own menu labels
        # ("25 PAL", not "25") and silently runs at 30 on anything else, verified on OBS
        # 32.1.1, where FPSCommon=25 logged "fps: 30/1".
        "FPSType": "1", "FPSInt": fps,
    }})


# ── OBS's update check, paused for the match ─────────────────────────────────

# OBS checks for a new version at startup and, when there is one, opens a "New update
# available" box over itself — including after the crash recovery reopens it mid-match,
# and it blocks a normal close until someone dismisses it. Paused for match day, put back
# when OBS is closed afterwards. Measured on OBS 32.1.1: with the default, the box appeared
# on 4 of 6 launches; with EnableAutoUpdates=false in global.ini, on 0 of 4.
UPDATES_MARKER = ".cricketstream-updates-paused"


def pause_update_check(cfg_dir):
    """Turn OBS's update check off, remembering what it was. True if it was turned off
    just now. A club that already had it off is left exactly as it was (no marker, so
    resume never turns it on). Must only be called while OBS is closed."""
    path = os.path.join(cfg_dir, "global.ini")
    marker = os.path.join(cfg_dir, UPDATES_MARKER)
    if not os.path.isfile(path):
        os.makedirs(cfg_dir, exist_ok=True)
        open(path, "w", encoding="utf-8").close()
    current = _read_ini_value(path, "General", "EnableAutoUpdates")
    if os.path.exists(marker):
        set_ini_values(path, {"General": {"EnableAutoUpdates": "false"}})
        return False                    # still paused from an earlier run
    if current == "false":
        return False                    # the club's own choice
    with open(marker, "w", encoding="utf-8") as f:
        f.write(current or "")          # "" = never set, OBS's default (on)
    set_ini_values(path, {"General": {"EnableAutoUpdates": "false"}})
    return True


def resume_update_check(cfg_dir):
    """Put OBS's update check back how the club had it. True if it was restored. Must
    only be called while OBS is closed (OBS writes its settings back over this on exit)."""
    marker = os.path.join(cfg_dir, UPDATES_MARKER)
    try:
        with open(marker, encoding="utf-8") as f:
            original = f.read().strip()
    except OSError:
        return False
    path = os.path.join(cfg_dir, "global.ini")
    try:
        set_ini_values(path, {"General": {"EnableAutoUpdates": original or "true"}})
        os.remove(marker)
    except OSError:
        return False
    return True


# ── OBS's first-run wizard ───────────────────────────────────────────────────

def skip_first_run_wizard(cfg_dir):
    """Mark OBS's first run as done, so a brand-new OBS doesn't open its
    Auto-Configuration Wizard: a modal window nobody is there to answer, which also
    blocks OBS from closing (seen on OBS 32.1.1, where it stopped the first-run restart
    closing OBS). Everything that wizard sets — resolution, frame rate, bitrate, encoder —
    is set by this module instead. OBS keeps the flag in user.ini from OBS 31, global.ini
    before that; set in both. True if anything changed. Must only be called while OBS is
    closed."""
    changed = False
    for name in ("user.ini", "global.ini"):
        path = os.path.join(cfg_dir, name)
        if _read_ini_value(path, "General", "FirstRun") == "true":
            continue
        if not os.path.isfile(path):
            os.makedirs(cfg_dir, exist_ok=True)
            open(path, "w", encoding="utf-8").close()
        changed = set_ini_values(path, {"General": {"FirstRun": "true"}}) or changed
    return changed


# ── Video encoder ────────────────────────────────────────────────────────────

# Hardware H.264 encoders, best first, as (test on the encoder id in OBS's log, the name
# Simple output mode stores in basic.ini). H.264 only: it's what every YouTube ingest takes.
HARDWARE_ENCODERS = [
    (lambda e: "nvenc" in e and "hevc" not in e and "av1" not in e, "nvenc", "NVIDIA NVENC"),
    (lambda e: "videotoolbox" in e and ("ave.avc" in e or "h264.gva" in e), "apple_h264",
     "Apple hardware (VideoToolbox)"),
    (lambda e: "amf" in e and "h264" in e, "amd", "AMD hardware (AMF)"),
    (lambda e: "qsv11" in e and "av1" not in e and "hevc" not in e, "qsv", "Intel QuickSync"),
]
SOFTWARE_ENCODERS = ("x264", "x264_lowcpu", "")


def available_encoders(cfg_dir):
    """Video encoder ids OBS listed as usable in its most recent log, or [] if there's no
    log yet. OBS writes "Available Encoders:" at every start, from the encoders that
    actually loaded on THIS machine (drivers and all), which is a better answer than any
    guess from the hardware."""
    folder = os.path.join(cfg_dir, "logs")
    try:
        logs = sorted(f for f in os.listdir(folder) if f.endswith(".txt"))
    except OSError:
        return []
    for name in reversed(logs):
        try:
            with open(os.path.join(folder, name), encoding="utf-8", errors="replace") as f:
                text = f.read()
        except OSError:
            continue
        if "Video Encoders:" not in text:
            continue        # a log cut short before OBS got that far; try an older one
        block = text.split("Video Encoders:", 1)[1].split("Audio Encoders:", 1)[0]
        found = []
        for line in block.splitlines():
            line = line.split(": ", 1)[-1].strip()       # drop the timestamp
            if line.startswith("- "):
                found.append(line[2:].split(" ", 1)[0])
        return found
    return []


def pick_hardware_encoder(encoder_ids):
    """(simple_mode_name, label) of the best hardware H.264 encoder listed, or None."""
    ids = [e.lower() for e in encoder_ids]
    for test, name, label in HARDWARE_ENCODERS:
        if any(test(e) for e in ids):
            return name, label
    return None


def ensure_encoder(cfg_dir):
    """Move OBS off CPU encoding (x264) onto a hardware encoder when this laptop has one.

    Returns (changed, message) for prepare_obs to print, or (False, None) for nothing to
    say. Simple output mode only (OBS's default): someone in Advanced mode has set their
    encoder up deliberately. A hardware encoder already chosen is never second-guessed.

    Replaces the panel's old "hardware vs x264" test-recording comparison, which switched
    the encoder setting over the WebSocket and recorded — but OBS only builds encoders
    when it starts, so it measured the same encoder twice (verified on OBS 32.1.1).
    Must only be called while OBS is closed.
    """
    path = active_profile_ini(cfg_dir)
    if not path:
        return False, None
    if (_read_ini_value(path, "Output", "Mode") or "Simple") != "Simple":
        return False, None
    stream = (_read_ini_value(path, "SimpleOutput", "StreamEncoder") or "").lower()
    rec = (_read_ini_value(path, "SimpleOutput", "RecEncoder") or "").lower()
    if stream not in SOFTWARE_ENCODERS and rec not in SOFTWARE_ENCODERS:
        return False, None
    pick = pick_hardware_encoder(available_encoders(cfg_dir))
    if not pick:
        return False, None
    name, label = pick
    updates = {}
    if stream in SOFTWARE_ENCODERS:
        updates["StreamEncoder"] = name
    # The recording encoder too: unless recording quality is "Same as stream", the replay
    # buffer runs its own encode with this one, a second full x264 load on the CPU.
    if rec in SOFTWARE_ENCODERS:
        updates["RecEncoder"] = name
    set_ini_values(path, {"SimpleOutput": updates})
    return True, f"Video encoder: {label} (was the CPU, which drops frames under load)."


# ── Scene stacking ───────────────────────────────────────────────────────────

# Source kinds that put a whole picture on screen: cameras, video files, screen capture.
# The graphics (Overlay, ReplayClip) must sit ABOVE these or they're hidden. Anything else
# above the graphics — a sponsor logo image, say — is the club's deliberate layout and is
# left exactly where it is. An earlier version lifted the graphics to the very top of the
# scene and moved a club's two sponsor logos underneath its scorebar on a real install.
PICTURE_KINDS = ("ffmpeg_source", "vlc_source", "dshow_input", "v4l2_input",
                 "decklink-input", "ndi_source", "monitor_capture", "window_capture",
                 "game_capture", "screen_capture", "display_capture", "xshm_input",
                 "pipewire-screen-capture-source")


def is_picture_kind(kind):
    kind = (kind or "").lower()
    return kind in PICTURE_KINDS or kind.startswith(("av_capture", "macos-avcapture",
                                                     "coreaudio_video", "syphon"))


def fill_canvas_transform(canvas):
    """Scale to fit the canvas (letterboxed if the aspect differs), anchored top-left."""
    return {"boundsType": "OBS_BOUNDS_SCALE_INNER", "boundsWidth": canvas[0],
            "boundsHeight": canvas[1], "boundsAlignment": 0, "alignment": 5,
            "positionX": 0, "positionY": 0}


def untouched_placement(tr):
    """Still where OBS dropped it: native size, top-left, no bounds. Anything else is
    someone's deliberate layout (a picture-in-picture, say) and isn't ours to undo."""
    return (tr.get("boundsType") in (None, "OBS_BOUNDS_NONE")
            and not tr.get("positionX") and not tr.get("positionY")
            and tr.get("scaleX", 1) == 1 and tr.get("scaleY", 1) == 1)


def covering_pictures(items, source):
    """The picture sources stacked ABOVE `source` (a graphic), LOWEST first. The fix is
    to move each of those DOWN to just beneath the graphic, not to lift the graphic up:
    lifting it past a camera that's been dragged over everything would also lift it past
    the logos in between, and the club put those above the scorebar on purpose (verified
    on OBS 32.1.1). `items` are GetSceneItemList's sceneItems; index 0 is the bottom."""
    me = next((i for i in items if i.get("sourceName") == source), None)
    if not me:
        return []
    mine = me.get("sceneItemIndex", 0)
    above = [i for i in items if i is not me and is_picture_kind(i.get("inputKind"))
             and i.get("sceneItemIndex", 0) > mine]
    # Lowest first: each lands directly beneath the graphic in turn, so two cameras keep
    # their order relative to each other (which one viewers see must not swap).
    return sorted(above, key=lambda i: i.get("sceneItemIndex", 0))


# ── config.ini ───────────────────────────────────────────────────────────────

def sync_config_password(config_file, password):
    """Write the OBS WebSocket password into config.ini's [OBS] section.

    OBS's own file is the source of truth: if they disagree, connecting with config.ini's
    copy would fail anyway, so updating it is a fix rather than a guess. Returns True if
    the file changed.
    """
    if not os.path.isfile(config_file):
        return False
    if _read_config_value(config_file, "OBS", "obs_password") == password:
        return False
    # config.ini is written by configparser, so "key = value" — normalise the key match.
    with open(config_file, encoding="utf-8-sig") as f:
        lines = f.read().splitlines()
    current, done = None, False
    for i, line in enumerate(lines):
        s = line.strip()
        if s.startswith("[") and s.endswith("]"):
            if current == "OBS" and not done:
                lines.insert(i, f"obs_password = {password}")
                done = True
                break
            current = s[1:-1]
        elif current == "OBS" and s.split("=", 1)[0].strip() == "obs_password":
            lines[i] = f"obs_password = {password}"
            done = True
            break
    if not done:
        if current != "OBS":
            lines += ["", "[OBS]"]
        lines.append(f"obs_password = {password}")
    _atomic_write(config_file, ("\n".join(lines) + "\n").encode("utf-8"))
    return True


def manage_obs_enabled(config_file):
    """config.ini [OBS] manage_obs: on unless explicitly no/false/0/off. The one reader —
    it was parsed in four places, and a fifth that missed a spelling would have managed
    OBS on a machine that had opted out."""
    return _read_config_value(config_file, "OBS", "manage_obs").strip().lower() \
        not in ("no", "false", "0", "off")


def _read_config_value(config_file, section, key):
    try:
        with open(config_file, encoding="utf-8-sig") as f:
            lines = f.read().splitlines()
    except OSError:
        return ""
    current = None
    for line in lines:
        s = line.strip()
        if s.startswith("[") and s.endswith("]"):
            current = s[1:-1]
        elif current == section and "=" in s and s.split("=", 1)[0].strip() == key:
            return s.split("=", 1)[1].strip()
    return ""


# ── Launching ────────────────────────────────────────────────────────────────

def scenes_ready(cfg_dir):
    """Has obs_setup already built the scenes (an "Overlay" source in the scene
    collection OBS will open)? False on a fresh OBS or if it can't tell."""
    for name in ("user.ini", "global.ini"):
        coll = _read_ini_value(os.path.join(cfg_dir, name), "Basic", "SceneCollectionFile")
        if not coll:
            continue
        if not coll.endswith(".json"):
            coll += ".json"
        try:
            with open(os.path.join(cfg_dir, "basic", "scenes", coll), encoding="utf-8") as f:
                sources = json.load(f).get("sources", [])
            return any(s.get("name") == "Overlay" for s in sources)
        except (OSError, ValueError, AttributeError):
            return False
    return False


def launch_args(cfg_dir):
    """--startreplaybuffer only once the scenes exist. On an empty scene collection OBS
    won't start the buffer: it stops on a modal "No Sources" question instead, which
    nobody is there to answer (seen on OBS 32.1.1). The first time through, obs_setup
    builds the scenes and starts the buffer over the WebSocket itself."""
    if scenes_ready(cfg_dir):
        return list(LAUNCH_ARGS)
    return [a for a in LAUNCH_ARGS if a != "--startreplaybuffer"]


def launch_obs(exe, args=None):
    """Start OBS detached, so closing this console doesn't take OBS with it."""
    args = list(LAUNCH_ARGS if args is None else args)
    if sys.platform == "darwin":
        subprocess.Popen(["open", "-a", exe, "--args"] + args)
        return
    kwargs = {"stdin": subprocess.DEVNULL, "stdout": subprocess.DEVNULL,
              "stderr": subprocess.DEVNULL, "close_fds": True}
    if sys.platform == "win32":
        kwargs["creationflags"] = (subprocess.DETACHED_PROCESS
                                   | subprocess.CREATE_NEW_PROCESS_GROUP)
    else:
        kwargs["start_new_session"] = True
    # cwd matters on Windows: OBS resolves its data folder relative to the working
    # directory and fails to start ("Failed to find locale/en-US.ini") from anywhere else.
    subprocess.Popen([exe] + args, cwd=os.path.dirname(exe), **kwargs)


def close_obs(timeout=30):
    """Close OBS the way a person would, so it saves its scenes and exits cleanly (no
    crash marker, so no Safe Mode prompt next time). True once it's gone. Only for when
    nothing is on air: with an output running, OBS asks before quitting and this gives
    up rather than answer for anyone."""
    try:
        if sys.platform == "win32":
            import ctypes
            import ctypes.wintypes as wt
            u32 = ctypes.windll.user32
            pids = set(obs_pids())
            mains = []

            @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
            def collect(hwnd, _):
                pid = wt.DWORD()
                u32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
                if pid.value in pids and u32.IsWindowVisible(hwnd):
                    n = u32.GetWindowTextLengthW(hwnd)
                    buf = ctypes.create_unicode_buffer(n + 1)
                    u32.GetWindowTextW(hwnd, buf, n + 1)
                    if buf.value.startswith("OBS "):        # the main window
                        mains.append(hwnd)
                return True

            u32.EnumWindows(collect, 0)
            for hwnd in mains:
                u32.PostMessageW(hwnd, 0x0010, 0, 0)        # WM_CLOSE: the window's X
        elif sys.platform == "darwin":
            subprocess.run(["osascript", "-e", 'tell application "OBS" to quit'],
                           capture_output=True, timeout=15)
        else:
            subprocess.run(["pkill", "-TERM", "-x", "obs"], capture_output=True, timeout=15)
    except Exception:
        return False
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not obs_running():
            return True
        time.sleep(1)
    # Still open: almost always OBS's "Active Outputs — exit anyway?" question, which
    # closing it raised. Cancel it, rather than leave an unexplained "exit?" box on the
    # operator's screen (seen on OBS 32.1.1 with the replay buffer running).
    if sys.platform == "win32":
        try:
            pids = set(obs_pids())

            @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
            def cancel(hwnd, _):
                pid = wt.DWORD()
                u32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
                if pid.value in pids and u32.IsWindowVisible(hwnd):
                    n = u32.GetWindowTextLengthW(hwnd)
                    buf = ctypes.create_unicode_buffer(n + 1)
                    u32.GetWindowTextW(hwnd, buf, n + 1)
                    if not buf.value.startswith("OBS "):
                        u32.PostMessageW(hwnd, 0x0010, 0, 0)   # = Cancel / No
                return True

            u32.EnumWindows(cancel, 0)
        except Exception:
            pass
    return False


def wait_for_websocket(port=WS_PORT, timeout=45):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if port_open(port):
            return True
        time.sleep(1)
    return False


def _atomic_write(path, data):
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.replace(tmp, path)


# ── The whole thing ──────────────────────────────────────────────────────────

def prepare_obs(config_file, ask_yn=None, wait_enter=None, say=print):
    """Get OBS ready for match day: WebSocket on, password in config.ini, replay buffer
    enabled, OBS open. Returns True if OBS is running with its WebSocket reachable.

    ask_yn/wait_enter are the launcher's prompts (setup_wizard's ask_yn/pause style);
    without them it never blocks, and simply skips whatever needs OBS closed.
    """
    exe = find_obs()
    if not exe:
        say("  [!!] OBS Studio isn't installed. Get it from https://obsproject.com,")
        say("       then run this again. (Everything else will still start.)")
        return False

    cfg_dir = obs_config_dir()
    port = WS_PORT

    if obs_running():
        conf = read_ws_config(cfg_dir)
        # The settings file saying "enabled" isn't enough: an OBS sitting on its Safe Mode
        # question hasn't loaded the WebSocket plugin yet, and one running IN Safe Mode
        # never will. Only an answering port means OBS is actually reachable.
        if ws_usable(conf) and wait_for_websocket(port, timeout=10):
            password = conf.get("server_password", "") if conf.get("auth_required") else ""
            if sync_config_password(config_file, password):
                say("  [OK] Copied OBS's WebSocket password into config.ini.")
            say("  [OK] OBS is already open.")
            return True
        if ws_usable(conf):
            say("  [!!] OBS is open but not answering. It's probably asking about Safe Mode,")
            say("       or running in it (Safe Mode switches off what this uses to talk to OBS).")
            say("       Close OBS and this will reopen it properly.")
        else:
            # Anything we write now, OBS overwrites when it exits.
            say("  [!!] OBS is open, but its WebSocket server (how this talks to OBS) is off.")
            say("       Close OBS and this will switch it on and reopen it for you.")
        if not wait_enter:
            return False
        # A few asks, not forever: quickstart's pause() swallows Ctrl+C (and returns at once
        # on end-of-input), so an unbounded loop couldn't be escaped — an operator who
        # wanted to carry on with OBS as it is (mid-stream, say) was stuck before the
        # server ever started.
        for _ in range(3):
            if not obs_running():
                break
            wait_enter("Close OBS, then press Enter here (or just press Enter to carry on "
                       "without it)...")
        else:
            if obs_running():
                say("  Carrying on with OBS as it is. Replays and camera cuts won't work "
                    "until OBS's WebSocket is on.")
                return False
        say("")

    try:
        password, changed = ensure_websocket(cfg_dir, port)
        if changed:
            say("  [OK] Switched on OBS's WebSocket server.")
        if sync_config_password(config_file, password):
            say("  [OK] Saved the OBS password in config.ini.")
    except OSError as e:
        say(f"  [!!] Couldn't switch on OBS's WebSocket server ({e}).")
        say("       Do it by hand: OBS -> Tools -> WebSocket Server Settings.")

    try:
        replay_folder = os.path.expanduser(_read_config_value(config_file, "OBS",
                                                              "replay_folder"))
        if ensure_replay_buffer(cfg_dir, replay_folder):
            say("  [OK] Replay buffer on.")
    except (OSError, UnicodeDecodeError) as e:
        say(f"  [!!] Couldn't pre-set the replay buffer ({e}) — OBS setup will try again.")

    try:
        state_file = os.path.join(os.path.dirname(os.path.abspath(config_file)),
                                  "match_state.json")
        manual = _read_config_value(config_file, "Stream",
                                    "output_resolution").lower() in ("manual", "off", "no")
        if not manual:
            res, fps, why = choose_video(config_file, state_file)
            if ensure_video_settings(cfg_dir, res, fps) is not None:
                say(f"  [OK] Video: {res} at {fps}fps ({why}).")
    except (OSError, UnicodeDecodeError) as e:
        say(f"  [!!] Couldn't set OBS's video settings ({e}) — check them in OBS → Settings → Video.")

    try:
        if _read_config_value(config_file, "Stream", "encoder").lower() not in ("manual", "off", "no"):
            changed, message = ensure_encoder(cfg_dir)
            if changed:
                say(f"  [OK] {message}")
    except (OSError, UnicodeDecodeError) as e:
        say(f"  [!!] Couldn't check OBS's video encoder ({e}).")

    try:
        skip_first_run_wizard(cfg_dir)
    except (OSError, UnicodeDecodeError):
        pass    # just means the wizard may appear on a brand-new OBS

    try:
        if pause_update_check(cfg_dir):
            say("  [OK] OBS's update check is paused for the match (back on when OBS is closed).")
    except (OSError, UnicodeDecodeError):
        pass    # just means the pop-up may appear; not worth a message on match day

    if clear_crash_sentinel(cfg_dir):
        say("  [OK] OBS didn't close properly last time; skipping its Safe Mode question.")

    say("  Opening OBS...")
    try:
        launch_obs(exe, launch_args(cfg_dir))
    except OSError as e:
        say(f"  [!!] Couldn't open OBS ({e}). Open it yourself, then carry on.")
        return False
    if wait_for_websocket(port):
        say("  [OK] OBS is open and ready.")
        # Give OBS a moment past "port open" to finish loading the scene collection, so
        # obs_setup's first requests don't race the UI coming up.
        time.sleep(3)
        return True
    say("  [!!] OBS opened but isn't answering yet. If it's showing a first-run")
    say("       wizard, close that; OBS setup will carry on once it can connect.")
    return False
