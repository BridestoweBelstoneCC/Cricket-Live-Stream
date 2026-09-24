"""
CricketStream Overlay — Quickstart launcher
────────────────────────────────────────────
A thin double-clickable exe that finds a real Python interpreter (the one
CricketStreamSetup.exe already installed) and runs quickstart.py exactly as
`python quickstart.py` would — so quickstart's own behaviour (starting the
server as a subprocess, telemetry, update checks, installing any packages
still missing) works completely unchanged. This script only exists frozen;
its whole job is finding Python, nothing quickstart.py itself needs to know
or care about.
"""
import glob, os, shutil, subprocess, sys

# Best-effort: this exe's console can report a legacy single-byte codepage instead of
# UTF-8, which raises UnicodeEncodeError on the arrow this file prints below.
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def find_python():
    """Same search as setup_wizard.py's own find_python(), duplicated rather
    than imported — this has to stay a single standalone file, same reasoning
    as scorer_agent.py/nvplay_bridge.py."""
    for candidate in ("python3", "python", "py"):
        path = shutil.which(candidate)
        if path:
            return path
    # winget/the official installer update the registry PATH, but that doesn't
    # propagate into this already-running process if the wizard installed
    # Python only moments ago — check the usual install locations too.
    for base in (os.environ.get("LOCALAPPDATA"), os.environ.get("ProgramFiles")):
        if not base:
            continue
        matches = sorted(glob.glob(os.path.join(base, "Programs", "Python", "Python3*", "python.exe")) +
                          glob.glob(os.path.join(base, "Python3*", "python.exe")))
        if matches:
            return matches[-1]
    return None


def pause(msg="Press Enter to close this window..."):
    if os.environ.get("CRICKETSTREAM_NO_PAUSE", "") == "1":
        return
    try:
        input(f"\n  {msg}")
    except (EOFError, KeyboardInterrupt):
        pass


def find_quickstart():
    """Beside the exe, then one folder up — covers the exe being dropped into the
    Windows/ subfolder next to quickstart.bat, which is what the setup guide's
    'this folder' reads as to most people. Same search the .bat files do."""
    here = os.path.dirname(os.path.abspath(sys.executable))
    for candidate in (here, os.path.dirname(here)):
        path = os.path.join(candidate, "quickstart.py")
        if os.path.exists(path):
            return path, here
    return None, here


def main():
    quickstart_path, here = find_quickstart()
    if not quickstart_path:
        print()
        print("  " + "=" * 60)
        print("   PROBLEM: I can't find the CricketStream project files")
        print("  " + "=" * 60)
        print()
        print(f"   I'm running from:  {here}")
        print("   ...and there's no quickstart.py here or in the folder above.")
        print()
        print("   TO FIX: move this file into the folder you unzipped — the one")
        print("   containing quickstart.py, server.py and overlay.html — then")
        print("   run it again.")
        print()
        print("   https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream")
        pause()
        sys.exit(1)

    python = find_python()
    if not python:
        print()
        print("  " + "=" * 60)
        print("   PROBLEM: Python isn't installed on this machine")
        print("  " + "=" * 60)
        print()
        print("   TO FIX: run CricketStreamSetup.exe first — it installs Python")
        print("   for you, then walks through the rest of the setup.")
        print()
        print("   Installing Python by hand instead? Get it from")
        print("   https://python.org/downloads and TICK 'Add python.exe to PATH'.")
        print("   Then close this window and open a new one — PATH changes don't")
        print("   reach windows that are already open.")
        pause()
        sys.exit(1)

    result = subprocess.run([python, quickstart_path] + sys.argv[1:])
    # quickstart.py pauses on the failures it knows about. This catches the ones it
    # can't — Python itself failing to start, an import error before its own handler
    # is installed — which would otherwise close this window with the reason in it.
    if result.returncode != 0:
        print()
        print(f"   Quickstart exited with an error (code {result.returncode}).")
        print("   The reason is printed above — scroll up to read it.")
        pause()
    sys.exit(result.returncode)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
    except Exception:
        import traceback
        print()
        print("   The launcher itself failed to start:")
        print()
        traceback.print_exc(file=sys.stdout)
        pause()
        sys.exit(1)
