#!/usr/bin/env python3
"""Render every scorebar style to a PNG, headlessly, with fixed mock data.

Why this exists: the scorebar styles are pure CSS over a shared DOM, and the failure mode
is always visual, never a syntax error — a team colour that vanishes into the background,
an inline colour from applyColours() winning over the stylesheet, one style's full-width
accent covering another element's. None of that shows up in compile_check_all.py,
check_panel_js.py or the unittest suite. Two real bugs found by rendering during the
2026-09-24 style pass: Minimal's bowler name was white-on-white (applyColours sets
color:#fff inline), and Impact's footer rule painted straight over the striker underline.

It stubs fetch() rather than talking to a server, so every style renders the IDENTICAL
match state — what changes between two shots is the design, not the score. The state is a
deliberately busy one (2nd-innings chase, full ball ticker, target block visible, bowler
figures), because that's the most crowded the bar ever gets.

Usage:
    python scripts/render_scorebar.py                 # all styles -> examples/
    python scripts/render_scorebar.py modern impact   # just these
    python scripts/render_scorebar.py --out-dir /tmp  # somewhere else

Needs Chrome or Edge installed; no pip packages, no node, no running server.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STYLES = ["classic", "modern", "impact", "minimal"]

BROWSERS = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    os.path.expandvars(r"%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe"),
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
]


def find_browser():
    for name in ("google-chrome", "chromium", "chromium-browser", "microsoft-edge"):
        found = shutil.which(name)
        if found:
            return found
    for path in BROWSERS:
        if os.path.exists(path):
            return path
    return None


# Club-agnostic sample data — no real club names here, same rule as DEFAULT_STATE and
# config.example.ini (see CLAUDE.md's "no hardcoded club identity" gotcha).
STATE = {
    "home_team": "Home CC", "away_team": "Rival CC",
    "home_abbrev": "HOME", "away_abbrev": "RIV",
    "home_colour": "#1a3a5c", "away_colour": "#7b2d2d",
    "max_overs": 20, "scorebar_style": "modern",
    "home_club_id": "", "away_club_id": "",
    "graphics_fow": False, "graphics_partnership": False, "graphics_lineup": False,
    "graphics_boundary_flash": False, "graphics_milestones": False,
    "graphics_innings_summary": False, "graphics_commentary": False,
    "graphics_commentary_over": False, "graphics_over_summary": False,
    "graphics_partnership_display": False, "graphics_runrate_trend": False,
    "graphics_league_table": False, "graphics_player_card": False,
    "graphics_camera_auto_cut": False, "replay_enabled": False,
    "sponsor_name": "", "sponsor_id": "", "anthropic_key_set": False,
    "camera2_configured": False, "drinks_over": 0,
}

LIVE = {
    "source": "pcs", "feed": "file", "club_id": 0, "events": [],
    "state": {
        "battingTeamName": "Rival CC", "bowlingTeamName": "Home CC",
        "innings": 2, "score": 137, "wickets": 4, "overs": 15.3,
        "batter1": {"name": "J Hatton", "number": "21", "runs": 48, "balls": 31,
                    "onStrike": True, "sr": 154.8, "fours": 5, "sixes": 2, "howOut": ""},
        "batter2": {"name": "T Gostling", "number": "7", "runs": 23, "balls": 19,
                    "onStrike": False, "sr": 121.0, "fours": 2, "sixes": 0, "howOut": ""},
        "bowler": {"name": "S Ewen", "overs": "3.3", "runs": 28, "wickets": 2, "maidens": 0},
        "statusText": "Needs 31 off 27", "run_rate": 8.84,
        "targetRuns": 168, "runsRequired": 31, "ballsRemaining": 27,
        "partnership_runs": 44, "partnership_balls": 28,
        "lastWicketHowOut": "", "lastWicketBatter": "", "lastWicketBowler": "",
        "lastWicketFielder": "", "lastWicketType": "",
        "last_ball": "1 4 . 6 W 2", "last_over_runs": 13, "over_history": "",
        "pcs_overs": 15.3, "card": {"batters": [], "bowlers": []},
        "home_abbrev": "HOME", "away_abbrev": "RIV",
    },
}

STUB = """
<script>
(function(){
  const STATE = __STATE__, LIVE = __LIVE__;
  const j = (o) => Promise.resolve({ ok: true, status: 200,
      json: () => Promise.resolve(o), text: () => Promise.resolve(JSON.stringify(o)) });
  window.fetch = function(url) {
    url = String(url);
    if (url.includes('/state'))    return j(STATE);
    if (url.includes('/live'))     return j(LIVE);
    if (url.includes('/commands')) return j({commands: []});
    return j({});
  };
  // Hide everything that isn't the scorebar — the rotating panels would otherwise cover it
  // at whatever point in their cycle the screenshot lands.
  window.addEventListener('load', function(){
    setTimeout(function(){
      var sel = '.graphic-panel, #partnership-display, #runrate-display,'
              + '#over-commentary-display, #commentary-lower, #weather-widget, #vs-badge,'
              + '#innings-pill, #target-banner, #replay-banner, #event-flash, #sponsor-strap';
      document.querySelectorAll(sel).forEach(function(e){ e.style.display = 'none'; });
    }, 900);
  });
})();
</script>
"""


def render(style, out_png, browser):
    with open(os.path.join(REPO, "overlay.html"), encoding="utf-8") as f:
        html = f.read()
    stub = (STUB.replace("__STATE__", json.dumps(dict(STATE, scorebar_style=style)))
                .replace("__LIVE__", json.dumps(LIVE)))
    # Inject ahead of the overlay's own first <script> so the stub is installed before any
    # of its code runs and tries a real fetch.
    idx = html.index("<script>")
    html = html[:idx] + stub + html[idx:]
    tmp = os.path.join(tempfile.gettempdir(), f"_scorebar_render_{style}.html")
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(html)
    try:
        subprocess.run([browser, "--headless=new", "--disable-gpu", "--hide-scrollbars",
                        "--force-device-scale-factor=1", "--window-size=1920,1080",
                        "--virtual-time-budget=4000", f"--screenshot={out_png}",
                        "file:///" + tmp.replace("\\", "/")],
                       capture_output=True, timeout=120)
    finally:
        try:
            os.unlink(tmp)
        except OSError:
            pass
    return os.path.exists(out_png)


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("styles", nargs="*", default=None,
                    help=f"styles to render (default: all of {', '.join(STYLES)})")
    ap.add_argument("--out-dir", default=os.path.join(REPO, "examples"))
    ap.add_argument("--full", action="store_true",
                    help="keep the whole 1920x1080 frame instead of cropping to the bar")
    args = ap.parse_args()

    browser = find_browser()
    if not browser:
        print("  ✗  No Chrome/Edge found — install one, or pass its path in BROWSERS.")
        return 1

    styles = args.styles or STYLES
    unknown = [s for s in styles if s not in STYLES]
    if unknown:
        print(f"  ✗  Unknown style(s): {', '.join(unknown)} (known: {', '.join(STYLES)})")
        return 1

    os.makedirs(args.out_dir, exist_ok=True)
    print(f"  Browser: {browser}")
    failed = 0
    for style in styles:
        out = os.path.join(args.out_dir, f"scorebar-{style}.png")
        if not render(style, out, browser):
            print(f"  ✗  {style}: no screenshot produced")
            failed += 1
            continue
        if not args.full:
            # Crop to the bar itself. Pillow is already a requirement (the socials images
            # use it); without it the full frame is still perfectly usable, so don't fail.
            try:
                from PIL import Image
                im = Image.open(out)
                w, h = im.size
                im.crop((0, h - 72, w, h)).save(out)
            except ImportError:
                pass
        print(f"  OK   {style}  ->  {out}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
