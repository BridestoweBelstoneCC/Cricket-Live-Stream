"""
CricketStream Overlay — First-time setup wizard
------------------------------------------------
Run this once to install packages and create your config.ini.
On Windows: double-click setup.bat
On Mac:     run setup.sh
"""
import configparser, glob, os, secrets, shutil, subprocess, sys, tempfile, urllib.request, webbrowser

FROZEN = getattr(sys, "frozen", False)
# When frozen (PyInstaller), __file__ points inside the temp extraction
# folder, which is deleted on exit — use the real exe's location instead.
BASE = os.path.dirname(sys.executable) if FROZEN else os.path.dirname(os.path.abspath(__file__))
CONFIG_FILE = os.path.join(BASE, "config.ini")

# Bump this occasionally. The macos11 tag is a universal2 installer — it
# covers both Intel and Apple Silicon, so no architecture detection needed.
PYTHON_VERSION = "3.12.8"
MAC_PYTHON_PKG_URL = f"https://www.python.org/ftp/python/{PYTHON_VERSION}/python-{PYTHON_VERSION}-macos11.pkg"

BANNER = """
====================================================
      CricketStream Overlay -- First-time Setup
====================================================

This wizard will:
  1. Install required Python packages
  2. Create your config.ini with your club's details
  3. Optionally launch the server straight away

Press Ctrl-C at any time to quit without saving.
"""

def heading(text):
    print(f"\n--- {text} " + "-" * max(0, 48 - len(text)))

def ask(prompt, default="", required=False, secret=False):
    hint = f"  [{default}]" if default else ("  (required)" if required else "  (optional, press Enter to skip)")
    display = prompt + hint + ": "
    while True:
        if secret:
            import getpass
            sys.stdout.flush()
            val = getpass.getpass(display).strip()
        else:
            val = input(display).strip()
        if val:
            return val
        if default:
            return default
        if not required:
            return ""
        print("    This field is required.")

def ask_yn(prompt, default=True):
    hint = "[Y/n]" if default else "[y/N]"
    val = input(f"\n{prompt} {hint}: ").strip().lower()
    if not val:
        return default
    return val.startswith("y")

def abbrev_from_name(name):
    stop = {"cc", "cricket", "club", "and", "&", "the"}
    words = [w for w in name.replace("&", "and").split() if w.lower() not in stop]
    if not words:
        return ""
    return words[0][:6].upper()

def find_python():
    """Return a real Python interpreter, never the frozen exe itself."""
    if not FROZEN:
        return sys.executable
    for candidate in ("python3", "python", "py"):
        path = shutil.which(candidate)
        if path:
            return path
    # winget/the official installer update the registry PATH, but that
    # doesn't propagate into this already-running process — look in the
    # usual per-user and machine-wide install locations too.
    for base in (os.environ.get("LOCALAPPDATA"), os.environ.get("ProgramFiles")):
        if not base:
            continue
        matches = sorted(glob.glob(os.path.join(base, "Programs", "Python", "Python3*", "python.exe")) +
                          glob.glob(os.path.join(base, "Python3*", "python.exe")))
        if matches:
            return matches[-1]
    return None

def install_python_windows():
    if shutil.which("winget"):
        print("  Installing Python 3 via winget (this can take a minute)...\n")
        subprocess.run([
            "winget", "install", "--id", "Python.Python.3.12", "-e", "--silent",
            "--accept-package-agreements", "--accept-source-agreements",
        ])
        python = find_python()
        if python:
            print("  [OK] Python installed.")
            return python
    print("  Couldn't install Python automatically (winget not available).")
    print("  Opening the download page — tick 'Add python.exe to PATH' during setup,")
    print("  then re-run this wizard.")
    webbrowser.open("https://www.python.org/downloads/")
    return None

def install_python_mac():
    print(f"  Downloading Python {PYTHON_VERSION} from python.org...\n")
    tmp_pkg = os.path.join(tempfile.gettempdir(), f"python-{PYTHON_VERSION}.pkg")
    try:
        urllib.request.urlretrieve(MAC_PYTHON_PKG_URL, tmp_pkg)
    except OSError as e:
        print(f"  [!!] Download failed: {e}")
        webbrowser.open("https://www.python.org/downloads/")
        return None
    print("  Installing — macOS will ask for your Mac password (admin required):\n")
    result = subprocess.run(["sudo", "installer", "-pkg", tmp_pkg, "-target", "/"])
    os.remove(tmp_pkg)
    if result.returncode != 0:
        print("  [!!] Install failed. Please install manually from python.org and re-run.")
        return None
    # Mac Python doesn't trust HTTPS certs until this bundled script runs —
    # skipping it means pip/PlayCricket/Anthropic calls fail with SSL errors.
    cert_script = f"/Applications/Python {PYTHON_VERSION.rsplit('.', 1)[0]}/Install Certificates.command"
    if os.path.exists(cert_script):
        print("  Fixing SSL certificates (required for HTTPS on Mac)...")
        subprocess.run(["bash", cert_script])
    python = shutil.which("python3")
    if python:
        print("  [OK] Python installed.")
        return python
    print("  [!!] Installed, but couldn't find python3 on PATH — try re-running the wizard.")
    return None

def install_python():
    heading("Python 3 required")
    print("  The wizard doesn't need Python, but the server does — it wasn't found.\n")
    if not ask_yn("Install Python 3 now?", default=True):
        return None
    if sys.platform == "win32":
        return install_python_windows()
    if sys.platform == "darwin":
        return install_python_mac()
    print("  Please install Python 3 from https://python.org and re-run this wizard.")
    return None

def install_packages():
    heading("Step 1 — Installing packages")
    req = os.path.join(BASE, "requirements.txt")
    python = find_python() or install_python()
    if not python:
        print("\n  [!!] No Python 3 install found on this machine.")
        print("    The setup wizard doesn't need Python, but the server does.")
        print("    Install Python 3 from python.org, then run:")
        print(f"    pip install -r \"{req}\"")
        sys.exit(1)
    print("  Running: pip install -r requirements.txt\n")
    result = subprocess.run(
        [python, "-m", "pip", "install", "-r", req, "--quiet"],
        capture_output=False
    )
    if result.returncode != 0:
        print("\n  [!!] Package installation failed.")
        print("    Try running manually: pip install -r requirements.txt")
        sys.exit(1)
    print("\n  [OK] Packages installed.")

def configure():
    heading("Step 2 — Club details")
    print("  These appear on the scorebar and graphics.\n")

    name = ask("Club name", required=True)
    default_abbrev = abbrev_from_name(name)
    abbrev = ask("Abbreviation (max 6 chars)", default=default_abbrev, required=True)
    abbrev = abbrev[:6].upper()

    colour = ask("Home kit colour (hex)", default="#1a3a5c")
    if not colour.startswith("#"):
        colour = "#" + colour

    motto = ask("Replay motto (e.g. your club nickname)")

    heading("PlayCricket")
    print("  Your club ID is the number in the URL at play-cricket.com")
    print("  e.g. play-cricket.com/website/results — look for the site number.\n")
    pc_id = ask("PlayCricket club ID", required=True)
    pc_key = ask("PlayCricket API key")

    heading("Scoring software")
    print("  The folder where NV Play / PCS Pro writes its scoreboard output.")
    print("  Leave blank to use PlayCricket widget as fallback (score only).\n")
    pcs_folder = ask("PCS output folder")

    heading("OBS")
    obs_pw = ask("OBS WebSocket password")
    replay_folder = ask("Replay buffer folder")

    heading("Stream")
    yt_title = ask("YouTube title template", default="LIVE: {home} vs {away}")
    max_overs = ask("Overs per innings", default="50")
    print("\n  Your YouTube STREAM KEY (YouTube Studio -> Go Live -> Stream settings).")
    print("  Recommended: key-based streaming survives restarts and quality changes --")
    print("  OBS's 'connect account' mode ends the whole broadcast if the stream stops.")
    print("  The key is applied to OBS automatically on match day. Treat it like a")
    print("  password (anyone with it can stream to your channel).\n")
    yt_key = ask("YouTube stream key", secret=True)

    heading("AI commentary (optional)")
    print("  Powers live over commentary, match reports and social posts.")
    print("  Get a free key at console.anthropic.com\n")
    anthropic_key = ask("Anthropic API key", secret=True) if ask_yn("Do you have an Anthropic API key?", default=False) else ""

    heading("Control panel access")
    print("  The club password is what operators type to unlock the control panel.")
    print("  If you leave it blank, the panel is open to anyone on the network.\n")
    club_password = ask("Club password", secret=True)
    control_token = secrets.token_hex(32)

    heading("Network")
    print("  Use 0.0.0.0 to allow phone/tablet access over Wi-Fi.")
    print("  Use 127.0.0.1 (default) for localhost only.\n")
    bind_host = "0.0.0.0" if ask_yn("Allow access from other devices on Wi-Fi?", default=True) else "127.0.0.1"

    return dict(
        name=name, abbrev=abbrev, colour=colour, motto=motto,
        pc_id=pc_id, pc_key=pc_key,
        pcs_folder=pcs_folder, obs_pw=obs_pw, replay_folder=replay_folder,
        yt_title=yt_title, yt_key=yt_key, max_overs=max_overs,
        anthropic_key=anthropic_key,
        club_password=club_password, control_token=control_token,
        bind_host=bind_host,
    )

def write_config(v):
    cfg = configparser.ConfigParser()
    cfg["Club"] = {
        "name":           v["name"],
        "abbreviation":   v["abbrev"],
        "home_colour":    v["colour"],
        "playcricket_id": v["pc_id"],
        "motto":          v["motto"],
    }
    cfg["API"] = {
        "playcricket_key": v["pc_key"],
        "anthropic_key":   v["anthropic_key"],
    }
    cfg["Scoring"] = {
        "pcs_output_folder": v["pcs_folder"],
        "logos_folder":      "",
        "ground_filter":     "",
    }
    cfg["OBS"] = {
        "obs_password":  v["obs_pw"],
        "replay_folder": v["replay_folder"],
    }
    cfg["Stream"] = {
        "youtube_title":      v["yt_title"],
        "youtube_stream_key": v["yt_key"],
        "max_overs":          v["max_overs"],
    }
    cfg["Auth"] = {
        "control_token": v["control_token"],
        "club_password": v["club_password"],
    }
    cfg["Network"] = {
        "bind_host": v["bind_host"],
    }
    with open(CONFIG_FILE, "w", encoding="utf-8") as f:
        f.write("# CricketStream Overlay — Club Configuration\n")
        f.write("# Generated by setup_wizard.py — safe to edit by hand.\n\n")
        cfg.write(f)
    print(f"\n  [OK] Saved to {CONFIG_FILE}")

def main():
    print(BANNER)

    if sys.version_info < (3, 8):
        print("[!!] Python 3.8 or later is required.")
        sys.exit(1)

    if os.path.exists(CONFIG_FILE):
        print(f"  config.ini already exists at {CONFIG_FILE}")
        if not ask_yn("Re-run setup and overwrite it?", default=False):
            print("\n  Nothing changed. Run quickstart.sh / quickstart.bat to start the server.")
            sys.exit(0)

    install_packages()
    values = configure()

    heading("Summary")
    print(f"  Club:       {values['name']} ({values['abbrev']})")
    print(f"  Colour:     {values['colour']}")
    print(f"  PlayCricket ID: {values['pc_id']}")
    print(f"  Network:    bind_host = {values['bind_host']}")
    print(f"  Auth:       {'password set' if values['club_password'] else 'no password (localhost only)'}")

    if not ask_yn("\nSave this config?", default=True):
        print("  Cancelled — nothing saved.")
        sys.exit(0)

    write_config(values)

    print("\n  [OK] Setup complete!\n")
    print("  Next steps:")
    print("   - Match day: run quickstart.sh (Mac) or quickstart.bat (Windows)")
    print("   - Control panel: http://localhost:5000/control")
    if values["bind_host"] == "0.0.0.0":
        print("   - From phone/tablet on Wi-Fi: http://<your-laptop-ip>:5000/control")
    print()

    if ask_yn("Launch the server now?", default=True):
        quickstart = os.path.join(BASE, "quickstart.py")
        python = find_python()
        if not python or not os.path.exists(quickstart):
            print("\n  Run quickstart.sh (Mac) or quickstart.bat (Windows) to start the server.")
        else:
            subprocess.run([python, quickstart])

if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n\n  Cancelled — nothing saved.")
        sys.exit(0)
