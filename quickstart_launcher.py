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


def main():
    here = os.path.dirname(os.path.abspath(sys.executable))
    quickstart_path = os.path.join(here, "quickstart.py")
    if not os.path.exists(quickstart_path):
        print(f"ERROR: quickstart.py not found at {quickstart_path}")
        print("This launcher needs to sit in the same folder as the rest of the project.")
        input("\nPress Enter to exit...")
        sys.exit(1)

    python = find_python()
    if not python:
        print("ERROR: Python was not found on this machine.")
        print("Run CricketStreamSetup.exe first — it installs Python automatically.")
        input("\nPress Enter to exit...")
        sys.exit(1)

    result = subprocess.run([python, quickstart_path] + sys.argv[1:])
    sys.exit(result.returncode)


if __name__ == "__main__":
    main()
