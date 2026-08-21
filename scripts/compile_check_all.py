"""
Compile-check every top-level Python script in the repo, not just server.py.

server.py gets an explicit check in CI; quickstart.py/scoring_engine.py/simulate_match.py
get incidental coverage because some test module imports them -- but standalone tools like
scorer_agent.py, nvplay_bridge.py, setup_wizard.py, camera_encoder.py, refresh_cam.py,
stream_telemetry.py, stream_quality_test.py, quickstart_launcher.py, and obs_setup.py had
NONE: a syntax error in any of them would sail through CI green and only surface when
someone actually tried to run it, e.g. on the scoring laptop on match day.

    python scripts/compile_check_all.py
"""
import glob
import os
import py_compile
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)


def main():
    failed = []
    scripts = sorted(glob.glob(os.path.join(ROOT, "*.py")))
    for path in scripts:
        name = os.path.basename(path)
        try:
            py_compile.compile(path, doraise=True)
            print(f"  OK   {name}")
        except py_compile.PyCompileError as e:
            failed.append(name)
            print(f"  FAIL {name}\n{e}")

    if failed:
        print(f"\n{len(failed)} file(s) failed to compile: {', '.join(failed)}")
        sys.exit(1)
    print(f"\nAll {len(scripts)} top-level scripts compile cleanly.")


if __name__ == "__main__":
    main()
