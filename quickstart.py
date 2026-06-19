"""
BBCC Stream Overlay — Quick Start
──────────────────────────────────
Reads config.ini, fetches today's match from the PlayCricket API,
writes match_state.json with everything pre-configured, then starts
the server. Double-click quickstart.bat (Windows) or quickstart.sh (Mac).
"""
import configparser, json, os, sys, subprocess, urllib.request, datetime, platform

BANNER = """
╔══════════════════════════════════════════════════════╗
║         BBCC Stream Overlay — Quick Start            ║
╚══════════════════════════════════════════════════════╝
"""

def log(msg, status=""):
    icons = {"ok": "  ✓", "warn": "  ⚠", "err": "  ✗", "": "   "}
    print(f"{icons.get(status,'   ')} {msg}")

def load_config():
    cfg = configparser.ConfigParser()
    config_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini")
    if not os.path.exists(config_path):
        print(f"ERROR: config.ini not found at {config_path}")
        sys.exit(1)
    cfg.read(config_path, encoding="utf-8")
    return cfg

def fetch_todays_match(api_key, club_id):
    if not api_key or api_key == "YOUR_KEY_HERE":
        return None, "No API key configured in config.ini"
    today = datetime.date.today().strftime("%d/%m/%Y")
    year  = datetime.date.today().year
    url   = (f"https://play-cricket.com/api/v2/matches.json"
             f"?api_token={api_key}&site_id={club_id}&season={year}")
    try:
        req = urllib.request.Request(url, headers={"Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=15) as r:
            data = json.loads(r.read().decode())
    except Exception as e:
        return None, str(e)

    matches  = data.get("matches", [])
    todays   = [m for m in matches if m.get("match_date") == today]
    if not todays:
        return None, f"No matches found for today ({today}) — {len(matches)} total in season"

    home_matches = [m for m in todays if str(m.get("home_club_id","")) == str(club_id)]
    match = home_matches[0] if home_matches else todays[0]

    # Use the CLUB name ('Heathcoat CC'), not the team name ('1st XI'), for both the display
    # name and the abbreviation — otherwise the abbreviation comes out as "1ST".
    if str(match.get("home_club_id","")) == str(club_id):
        opp_name    = match.get("away_club_name","") or match.get("away_team_name","")
        opp_club_id = str(match.get("away_club_id","") or "")
    else:
        opp_name    = match.get("home_club_name","") or match.get("home_team_name","")
        opp_club_id = str(match.get("home_club_id","") or "")

    # Auto-abbreviate from the club name: strip CC/Cricket Club, first word, up to 5 chars
    # (e.g. 'Heathcoat CC' -> 'HEATH'). Still overridable in the control panel.
    opp_words   = opp_name.replace(" CC","").replace(" Cricket Club","").strip().split()
    auto_abbrev = opp_words[0][:5].upper() if opp_words else ""

    return {
        "match_id":     str(match.get("id","")),
        "away_team":    opp_name,
        "away_abbrev":  auto_abbrev,
        "away_club_id": opp_club_id,     # opposition's PlayCricket club ID — badge + opp stats
        "competition":  match.get("competition_name",""),
        "ground":       match.get("ground_name",""),
        "ground_lat":   match.get("ground_latitude",""),
        "ground_lon":   match.get("ground_longitude",""),
        "umpire1":      match.get("umpire_1_name",""),
        "umpire2":      match.get("umpire_2_name",""),
        "scorer1":      match.get("scorer_1_name",""),
        "match_date":   match.get("match_date",""),
        "match_time":   match.get("match_time",""),
    }, None

def build_state(cfg, match):
    """Build a complete match_state.json from config + API data."""
    # Load existing state to preserve user-set toggles
    state_path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                              "match_state.json")
    try:
        import json as _j
        with open(state_path) as _f:
            exist = _j.load(_f)
    except Exception:
        exist = {}
    club  = cfg["Club"]
    api   = cfg["API"]
    score = cfg["Scoring"]
    obs   = cfg["OBS"]
    stream= cfg["Stream"]

    away_team   = match["away_team"]   if match else "Opposition CC"
    away_abbrev = match["away_abbrev"] if match else ""
    competition = match["competition"] if match else ""
    umpire1     = match["umpire1"]     if match else ""
    umpire2     = match["umpire2"]     if match else ""
    scorer1     = match["scorer1"]     if match else ""
    pc_match_id = match["match_id"]    if match else ""

    return {
        # Club / match identity
        "home_team":            club.get("name","My Club CC"),
        "home_abbrev":          club.get("abbreviation","CC"),
        "away_team":            away_team,
        "away_abbrev":          away_abbrev,
        "home_colour":          club.get("home_colour","#1a3a5c"),
        "away_colour":          "#7b2d2d",
        "competition_name":     competition,
        "replay_motto":          cfg["Club"].get("motto",""),
        "umpire1_name":         umpire1,
        "umpire2_name":         umpire2,
        "scorer1_name":         scorer1,
        "pc_match_id":          pc_match_id,
        "match_notes":          "",

        # API keys
        "playcricket_api_key":  api.get("playcricket_key",""),
        "anthropic_api_key":    api.get("anthropic_key",""),
        "home_club_id":         club.get("playcricket_id",""),
        "ground_filter":        score.get("ground_filter","Millaton"),
        "away_club_id":         match.get("away_club_id","") if match else "",
        "logos_folder":         score.get("logos_folder",""),

        # Hard-coded operational defaults
        "demo_mode":            False,   # Always off — ready to stream
        "use_widget":           False,   # PCS Pro file only
        "max_overs":            int(stream.get("max_overs",50)),

        # PCS Pro / scoring
        "pcs_output_folder":    os.path.expanduser(score.get("pcs_output_folder","")),
        "logos_folder":          os.path.expanduser(score.get("logos_folder","")),
        "headshots_folder":       os.path.expanduser(score.get("headshots_folder","")),
        "drinks_over":            int(score.get("drinks_over", 25) or 25),

        # Graphics — preserve user settings if state file exists, else sensible defaults
        "graphics_fow":                exist.get("graphics_fow",               True),
        "graphics_partnership":         exist.get("graphics_partnership",        True),
        "graphics_lineup":              exist.get("graphics_lineup",             True),
        "graphics_boundary_flash":      exist.get("graphics_boundary_flash",     True),
        "graphics_milestones":          exist.get("graphics_milestones",         True),
        "graphics_innings_summary":     exist.get("graphics_innings_summary",    True),
        "graphics_over_summary":        exist.get("graphics_over_summary",       True),
        "graphics_partnership_display": exist.get("graphics_partnership_display",True),
        "graphics_runrate_trend":       exist.get("graphics_runrate_trend",      True),
        "graphics_commentary":          exist.get("graphics_commentary",         False),
        "graphics_commentary_over":     exist.get("graphics_commentary_over",    False),
        "graphics_player_card":         exist.get("graphics_player_card",        False),

        # Replay — everything on including 50s
        "replay_enabled":       True,
        "replay_on_fifty":      True,
        "replay_folder":        os.path.expanduser(obs.get("replay_folder","")),
        "max_clips":            500,
        "replay_duration":      18,

        # OBS
        "obs_host":             "localhost",
        "obs_port":             4455,
        "obs_password":         obs.get("obs_password",""),
        "obs_main_scene":       "Main",
        "obs_replay_scene":     "Replay",

        # YouTube
        "youtube_title_template": stream.get("youtube_title","LIVE: {home} vs {away}"),

        # Weather — off
        "weather_api_key":      "",

        # Misc
        "poll_interval":        20,
        "match_url":            "",
    }

def check_requirements():
    """Check Python packages are installed."""
    missing = []
    for pkg, import_name in [
        ("websocket-client", "websocket"),
        ("anthropic",        "anthropic"),
    ]:
        try:
            __import__(import_name)
        except ImportError:
            missing.append(pkg)
    return missing

def main():
    print(BANNER)
    script_dir = os.path.dirname(os.path.abspath(__file__))

    # ── Load config ──
    log("Loading config.ini...")
    cfg = load_config()
    club_name   = cfg["Club"].get("name","My Club CC")
    club_id     = cfg["Club"].get("playcricket_id","")
    api_key     = cfg["API"].get("playcricket_key","")
    log(f"Club: {club_name}", "ok")

    # ── Check requirements ──
    log("Checking Python packages...")
    missing = check_requirements()
    if missing:
        log(f"Installing missing packages: {', '.join(missing)}", "warn")
        subprocess.check_call([sys.executable, "-m", "pip", "install"] + missing,
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        log("Packages installed", "ok")
    else:
        log("All packages present", "ok")

    # ── Fetch today's match ──
    log("Fetching today's match from PlayCricket API...")
    match, error = fetch_todays_match(api_key, club_id)

    if match:
        log(f"Match found: {club_name} vs {match['away_team']}", "ok")
        log(f"Competition: {match['competition']}", "ok")
        if match["umpire1"]:
            log(f"Umpires: {match['umpire1']} / {match['umpire2']}", "ok")
        if match["ground"]:
            log(f"Ground: {match['ground']}", "ok")
    else:
        log(f"API: {error}", "warn")
        log("Continuing without match data — enter opposition manually in control panel", "warn")

    # ── Write match_state.json ──
    state = build_state(cfg, match)
    state_path = os.path.join(script_dir, "match_state.json")
    with open(state_path, "w", encoding="utf-8") as f:
        json.dump(state, f, indent=2)
    log("match_state.json written", "ok")

    # ── Configure OBS ──
    log("Configuring OBS...")
    obs_pw     = cfg["OBS"].get("obs_password","")
    replay_dir = os.path.expanduser(cfg["OBS"].get("replay_folder",""))
    try:
        # Import obs_setup from same directory
        sys.path.insert(0, script_dir)
        from obs_setup import obs_setup
        ok, messages = obs_setup(password=obs_pw, replay_folder=replay_dir, verbose=False)
        if ok:
            log("OBS configured — scenes and sources ready", "ok")
        else:
            last_err = next((m for m in reversed(messages) if "✗" in m), messages[-1] if messages else "")
            log(f"OBS: {last_err.strip()} — configure manually if needed", "warn")
    except Exception as e:
        log(f"OBS setup skipped: {e}", "warn")

    # ── Validate PCS folder ──
    pcs_folder = state["pcs_output_folder"]
    if pcs_folder:
        if os.path.isdir(pcs_folder):
            log(f"PCS folder found: {pcs_folder}", "ok")
        else:
            log(f"PCS folder not found: {pcs_folder}", "warn")
            log("Check pcs_output_folder in config.ini", "warn")
    else:
        log("No PCS folder set — enter it in the control panel", "warn")

    # ── Summary ──
    print()
    print("  ─────────────────────────────────────────────")
    print(f"  Ready: {club_name} vs {match['away_team'] if match else '???'}")
    if match and match.get("competition"):
        print(f"  {match['competition']}")
    print()

    # ── Start server as a subprocess so we regain control on Ctrl+C ──
    server_path = os.path.join(script_dir, "server.py")
    if not os.path.exists(server_path):
        print(f"ERROR: server.py not found at {server_path}")
        sys.exit(1)

    import subprocess
    # Start the server detached from this terminal's signal delivery, so that a Ctrl+C
    # interrupts ONLY this launcher — not the server. That keeps the server alive long
    # enough to generate the match report (the match log lives in the server's memory).
    # Without this, the Ctrl+C reaches the whole process group and the server dies first,
    # giving "Connection refused" when we then try to reach /report/generate.
    popen_kwargs = {}
    if os.name == "nt":
        popen_kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
    else:
        popen_kwargs["start_new_session"] = True   # new session/group on macOS & Linux
    proc = subprocess.Popen([sys.executable, server_path], **popen_kwargs)

    # ── Pre-load season batting stats (once per day, cached locally) ──
    # PlayCricket has no per-player stats endpoint, so the server pulls each completed
    # match's scorecard once, aggregates every player's season figures, and caches them to
    # season_stats_cache.json next to server.py. Re-running the same day reuses that file
    # (no further API calls). Done here so the figures are ready before the first player card.
    if wait_for_server():
        pull_season_stats(api_key)
        self_test()
    else:
        log("Server didn't respond in time — season stats will load on the first player card", "warn")

    print()
    print("  Control panel: http://localhost:5000/control")
    print("  Overlay:       http://localhost:5000/overlay")
    print()
    print("  Keep this window open while streaming.")
    print("  Press Ctrl+C to stop the server.")
    print("  ─────────────────────────────────────────────")
    print()

    try:
        proc.wait()
    except KeyboardInterrupt:
        # Offer the report while the server is STILL RUNNING (match log lives in its memory)
        offer_match_report()
        print("\n  Shutting down server...")
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()
        print("  Server stopped. Bye!")


def self_test():
    """Pre-flight checklist: read /health and print a go/no-go summary.
    Run AFTER the server is up so you find problems in the warm-up, not at the first ball.
    Warnings don't block — plenty are normal (e.g. demo mode before the scorer connects)."""
    import urllib.request
    print()
    print("  ─── Pre-flight check ────────────────────────")
    try:
        with urllib.request.urlopen("http://127.0.0.1:5000/health", timeout=6) as r:
            h = json.loads(r.read().decode())
    except Exception as e:
        log(f"Could not read /health ({e}) — skipping pre-flight", "warn")
        return

    issues = 0
    def ok(msg):
        print(f"   ✓  {msg}")
    def warn(msg):
        nonlocal issues
        issues += 1
        print(f"   ⚠  {msg}")

    # Scorer feed
    pcs = h.get("pcs", {})
    if h.get("demo_mode"):
        warn("Demo mode is ON — turn it off in the control panel before going live")
    if not pcs.get("folder_set"):
        warn("PCS output folder not set (control panel → PCS Pro section)")
    elif not pcs.get("file_found"):
        warn("PCS scoreboard file not found — is PCS Pro running and outputting?")
    elif not pcs.get("fresh"):
        age = pcs.get("age_sec") or 0
        warn(f"PCS file found but stale ({age//60}m old) — scorer not scoring yet?")
    else:
        ok(f"Scorer feed live ({pcs.get('file')}, updated {pcs.get('age_sec')}s ago)")

    # Season stats
    stt = h.get("stats", {})
    if stt.get("built"):
        ok(f"Season stats ready ({stt.get('players',0)} players)")
    elif stt.get("building"):
        ok("Season stats building in the background")
    else:
        warn("Season stats not built — player cards will show photos only")

    # Assets
    assets = h.get("assets", {})
    if assets.get("headshots", 0) > 0:
        ok(f"{assets['headshots']} player photo(s) found")
    else:
        warn("No player photos found (headshots folder)")
    if h.get("match", {}).get("badges"):
        ok("Both club badges set")
    else:
        warn("Club badge(s) missing — set them in the control panel")

    # Keys
    keys = h.get("keys", {})
    if keys.get("anthropic"):
        ok("Anthropic key set — AI features available")
    else:
        warn("No Anthropic key — commentary/reports/social posts disabled")

    print("  ─────────────────────────────────────────────")
    if issues == 0:
        print("   ✓  ALL SYSTEMS GO")
    else:
        print(f"   ⚠  {issues} warning(s) above — fixable in the control panel")
    print()


def wait_for_server(timeout=20):
    """Poll the server's /live endpoint until it responds (or we give up)."""
    import urllib.request, time as _t
    deadline = _t.time() + timeout
    while _t.time() < deadline:
        try:
            with urllib.request.urlopen("http://127.0.0.1:5000/live", timeout=2):
                return True
        except Exception:
            _t.sleep(0.5)
    return False


def pull_season_stats(api_key):
    """Trigger the season-stats build on the running server and report the result.
    Uses the non-forced refresh so a same-day cache is reused (no repeat API calls)."""
    import urllib.request, json
    if not api_key or api_key == "YOUR_KEY_HERE":
        log("Season stats skipped — no PlayCricket API key in config.ini", "warn")
        return
    log("Pulling season batting stats from PlayCricket (both teams, one-off, cached locally)...")
    try:
        with urllib.request.urlopen("http://127.0.0.1:5000/player/stats/refresh", timeout=180) as r:
            d = json.loads(r.read().decode())
    except Exception as e:
        log(f"Season stats: couldn't pull ({e}) — will load on first player card", "warn")
        return
    if d.get("error"):
        log(f"Season stats: {d['error']} (loaded {d.get('players',0)} players)", "warn")
    else:
        src = "from today's saved cache" if d.get("from_cache") else \
              f"{d.get('api_calls',0)} API calls"
        opp = " incl. opposition" if d.get("opposition") else " (home only — no opposition club ID)"
        log(f"Season stats ready: {d.get('players',0)} players "
            f"from {d.get('matches_used',0)} matches ({src}){opp}", "ok")


def offer_match_report():
    """Offer to generate an AI match report from the still-running server."""
    import urllib.request, json, datetime
    print()
    try:
        ans = input("  Generate an AI match report for this game? [y/N]: ").strip().lower()
    except (EOFError, KeyboardInterrupt):
        return
    if ans not in ("y", "yes"):
        print("  No report generated.")
        return
    print("  Generating report (needs your Anthropic API key)...")
    try:
        with urllib.request.urlopen("http://127.0.0.1:5000/report/generate", timeout=60) as r:
            result = json.loads(r.read().decode())
        if result.get("ok"):
            print()
            print("  ─── MATCH REPORT ─────────────────")
            for line in result["text"].split("\n"):
                print("  " + line)
            print("  ────────────────────────────────")
            fn = os.path.join(os.getcwd(),
                 f"match_report_{datetime.date.today().isoformat()}.txt")
            with open(fn, "w", encoding="utf-8") as f:
                f.write(result["text"])
            print(f"\n  Saved to: {fn}")
        else:
            print(f"  Could not generate report: {result.get('error','unknown')}")
    except Exception as e:
        print(f"  Report generation failed: {e}")
        print("  Tip: you can still generate it from the control panel "
              "(Match Report & Social Posts card) while the server is running.")


if __name__ == "__main__":
    main()
