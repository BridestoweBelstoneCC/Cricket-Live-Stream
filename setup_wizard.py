"""
CricketStream Overlay — First-time setup wizard
------------------------------------------------
Run this once to install packages and create your config.ini.
On Windows: double-click setup.bat
On Mac:     run setup.sh
"""
import configparser, os, re, secrets, subprocess, sys

BASE = os.path.dirname(os.path.abspath(__file__))
CONFIG_FILE = os.path.join(BASE, "config.ini")
EXAMPLE_FILE = os.path.join(BASE, "config.example.ini")

BANNER = """
╔══════════════════════════════════════════════════════════╗
║       CricketStream Overlay — First-time Setup           ║
╚══════════════════════════════════════════════════════════╝

This wizard will:
  1. Install required Python packages
  2. Create your config.ini with your club's details
  3. Optionally launch the server straight away

Press Ctrl-C at any time to quit without saving.
"""

def heading(text):
    print(f"\n── {text} " + "─" * max(0, 50 - len(text)))

def ask(prompt, default="", required=False, secret=False):
    hint = f"  [{default}]" if default else ("  (required)" if required else "  (optional, press Enter to skip)")
    display = prompt + hint + ": "
    while True:
        if secret:
            import getpass
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

def install_packages():
    heading("Step 1 — Installing packages")
    req = os.path.join(BASE, "requirements.txt")
    print("  Running: pip install -r requirements.txt\n")
    result = subprocess.run(
        [sys.executable, "-m", "pip", "install", "-r", req, "--quiet"],
        capture_output=False
    )
    if result.returncode != 0:
        print("\n  ✗ Package installation failed.")
        print("    Try running manually: pip install -r requirements.txt")
        sys.exit(1)
    print("\n  ✓ Packages installed.")

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

    motto = ask("Replay motto (e.g. 'Up the Stags')")

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
        yt_title=yt_title, max_overs=max_overs,
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
        "youtube_title": v["yt_title"],
        "max_overs":     v["max_overs"],
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
    print(f"\n  ✓ Saved to {CONFIG_FILE}")

def main():
    print(BANNER)

    if sys.version_info < (3, 8):
        print("✗ Python 3.8 or later is required.")
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

    print("\n  ✓ Setup complete!\n")
    print("  Next steps:")
    print("   • Match day: run quickstart.sh (Mac) or quickstart.bat (Windows)")
    print("   • Control panel: http://localhost:5000/control")
    if values["bind_host"] == "0.0.0.0":
        print("   • From phone/tablet on Wi-Fi: http://<your-laptop-ip>:5000/control")
    print()

    if ask_yn("Launch the server now?", default=True):
        quickstart = os.path.join(BASE, "quickstart.py")
        os.execv(sys.executable, [sys.executable, quickstart])

if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n\n  Cancelled — nothing saved.")
        sys.exit(0)
