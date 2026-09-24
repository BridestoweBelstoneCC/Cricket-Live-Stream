"""
CricketStream Overlay — the one launcher for the streaming laptop
─────────────────────────────────────────────────────────────────
Shipped frozen as CricketStream.exe. Replaces the old two-exe dance
(CricketStreamSetup.exe to configure, then CricketStreamQuickstart.exe on match
day, which had to be put in the right folder by hand). This decides for itself
what still needs doing and does it:

    1. Am I in the project folder?      -> if not, say so clearly and stop
    2. Is Python installed?             -> if not, install it
    3. Are the packages installed?      -> if not, pip install them
    4. Does config.ini exist?           -> if not, run the setup wizard
    5. Start the match                  -> hand over to quickstart.py

Steps 1-4 are skipped silently when they're already satisfied, so on the second
and every later run this is just "double-click, match starts" — which is the
whole point. Nothing here is a new implementation: every step calls the same
function the old wizard/launcher already used, so the actual work is unchanged
and only the decision of WHICH steps to run is new.

Deliberately does NOT freeze server.py/quickstart.py themselves. They stay
ordinary .py files run by a real interpreter, because server.py reads
overlay.html/control.html from disk on every request (that's what makes panel
edits show up on refresh) and resolves config.ini/match_state.json relative to
its own folder. An earlier attempt at freezing them was reverted for exactly
that reason — see the build workflow's comment.
"""
import os
import subprocess
import sys

# setup_wizard owns the shared plumbing: find_python/install_python (which knows winget and
# the python.org pkg), find_project_root, install_packages, the whole configure() interview,
# write_config, and the die()/pause() rules that stop a double-clicked window closing with
# the reason inside it. Importing rather than reimplementing means there is exactly one
# version of each, and setup.bat/setup.sh keep working against the same code.
import setup_wizard as wiz

BANNER = """
====================================================
          CricketStream Overlay
====================================================
"""

# pip package name -> the name you actually import. Needed because they differ for most of
# them, and importing is the only honest test of "is this installed and working".
REQUIRED = {
    "websocket-client":         "websocket",
    "Pillow":                   "PIL",
    "qrcode":                   "qrcode",
    "certifi":                  "certifi",
    "anthropic":                "anthropic",
    "google-api-python-client": "googleapiclient",
    "google-auth-oauthlib":     "google_auth_oauthlib",
}


def missing_packages(python):
    """Which of REQUIRED can't be imported by the interpreter that will run the server.

    Asks that interpreter rather than checking our own frozen one, which has none of them
    and is not what runs server.py. Returns [] if the probe itself fails — a broken probe
    is not evidence of missing packages, and blocking a match day over one would be worse
    than letting the server degrade gracefully, which it already does for all of these.
    """
    # `import importlib` alone does NOT bind importlib.util — it's a submodule, so that
    # spelling raises AttributeError, the probe exits non-zero, and this function reports
    # "nothing missing" for every package forever. Import the submodule explicitly.
    probe = (
        "import importlib.util, json, sys\n"
        f"mods = {list(REQUIRED.values())!r}\n"
        "missing = [m for m in mods if importlib.util.find_spec(m) is None]\n"
        "sys.stdout.write(json.dumps(missing))\n"
    )
    try:
        out = subprocess.run([python, "-c", probe], capture_output=True, text=True, timeout=60)
        if out.returncode != 0:
            return []
        found = __import__("json").loads(out.stdout.strip() or "[]")
    except Exception:
        return []
    back = {v: k for k, v in REQUIRED.items()}
    return [back[m] for m in found if m in back]


def ensure_packages(python):
    missing = missing_packages(python)
    if not missing:
        print("  [OK] Python packages are installed.")
        return
    print(f"  Installing {len(missing)} missing package(s): {', '.join(missing)}\n")
    wiz.install_packages()          # same pip call, same error handling, same die() on failure


def ensure_python():
    python = wiz.find_python()
    if python:
        return python
    python = wiz.install_python()   # offers winget (Windows) / python.org pkg (Mac)
    if not python:
        wiz.die("Python 3 is needed and isn't installed",
                "",
                "The server runs on Python. Install it from:",
                "  https://python.org/downloads",
                "",
                "On Windows, TICK 'Add python.exe to PATH' on the first screen.",
                "Then close this window, open a new one, and run this again.",
                "(PATH changes don't reach windows that are already open.)")
    return python


def run_setup_wizard():
    """The same interview the standalone wizard runs, minus its own
    location/packages steps — this launcher has already done those."""
    wiz.heading("First-time setup")
    print("  No config.ini yet, so let's create one. This happens once.\n")
    values = wiz.configure()

    wiz.heading("Summary")
    print(f"  Club:           {values['name']} ({values['abbrev']})")
    print(f"  Colour:         {values['colour']}")
    print(f"  Scorebar:       {values['scorebar_style']}")
    print(f"  PlayCricket ID: {values['pc_id']}")
    print(f"  Network:        bind_host = {values['bind_host']}")
    print(f"  Auth:           {'password set' if values['club_password'] else 'no password (localhost only)'}")

    if not wiz.ask_yn("\nSave this config?", default=True):
        print("\n  Cancelled — nothing saved. Run this again when you're ready.")
        wiz.pause()
        sys.exit(0)
    wiz.write_config(values)


def main():
    print(BANNER)

    if sys.version_info < (3, 8):
        wiz.die("This needs Python 3.8 or later",
                "",
                f"Found Python {sys.version.split()[0]}.",
                "Install a current Python 3 from https://python.org/downloads")

    # 1. In the right place? (die()s with the folder it looked in if not)
    wiz.check_location()
    print(f"  Project folder: {wiz.BASE}")

    # 2/3. Python + packages, so the server has what it needs before we promise a match.
    python = ensure_python()
    ensure_packages(python)

    # 4. Config — the thing that decides "first run" from "match day".
    if not os.path.exists(wiz.CONFIG_FILE):
        run_setup_wizard()
    else:
        print(f"  [OK] Config: {wiz.CONFIG_FILE}")

    # 5. Match day. quickstart.py does the fixture lookup, pre-flight checks and starts the
    # server; it handles its own Ctrl+C, match report and pausing, so the console is its
    # problem from here. Not launched detached: the operator's Ctrl+C should reach it.
    quickstart = os.path.join(wiz.BASE, "quickstart.py")
    if not os.path.exists(quickstart):
        wiz.die("quickstart.py is missing",
                "",
                f"Expected it at:  {quickstart}",
                "",
                "Part of the project folder seems to be missing. Re-download and",
                "unzip it, keeping the folder intact:",
                "  https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream")
    print("\n  Starting the match...\n")
    # quickstart writes straight to the same console. Without this flush our own status
    # lines sit in a buffer and appear AFTER its banner (or not at all), which makes the
    # checks above look like they never ran.
    sys.stdout.flush()
    result = subprocess.run([python, quickstart] + sys.argv[1:])
    # quickstart pauses on the failures it knows about; this catches the ones it can't
    # (Python failing to start, an import error before its own handler is installed).
    if result.returncode != 0:
        print(f"\n  Quickstart exited with an error (code {result.returncode}).")
        print("  The reason is printed above — scroll up to read it.")
        wiz.pause()
    sys.exit(result.returncode)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n\n  Cancelled.")
        wiz.pause()
        sys.exit(0)
    except SystemExit:
        raise
    except EOFError:
        # input() hit end-of-stream: launched with no usable console, or piped from
        # nothing. Not a bug, and not worth a "something went wrong" box — pausing here
        # would just hit the same EOF again.
        print("\n  [!!] No keyboard input available, so setup can't ask its questions.")
        print("       Run it from a terminal instead:")
        print(f"         \"{sys.executable}\"" if wiz.FROZEN
              else f"         python \"{os.path.abspath(__file__)}\"")
        sys.exit(1)
    except Exception:
        import traceback
        print()
        print("  " + "=" * 60)
        print("   SOMETHING WENT WRONG — this is a bug, not something you did")
        print("  " + "=" * 60)
        print()
        traceback.print_exc(file=sys.stdout)
        print()
        print("   Please report this at:")
        print("   https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues")
        print("   Copy the lines above into the issue (they say what broke).")
        wiz.pause()
        sys.exit(1)
