"""
CricketStream Overlay — Quick Start
──────────────────────────────────
Reads config.ini, fetches today's match from the PlayCricket API,
writes match_state.json with everything pre-configured, then starts
the server. Double-click quickstart.bat (Windows) or quickstart.sh (Mac).
"""
import configparser, json, os, sys, subprocess, urllib.request, datetime, platform, time

# Use certifi's certificates to avoid CERTIFICATE_VERIFY_FAILED on the PlayCricket API.
# server.py carries the same patch; without it here the fixture fetch fails outright and
# the day silently starts on the placeholder opposition. Not Mac-only — a fresh Windows
# Python hits it too.
try:
    import ssl, certifi
    _ssl_ctx   = ssl.create_default_context(cafile=certifi.where())
    _orig_open = urllib.request.urlopen
    def _patched_urlopen(url, data=None, timeout=10, **kw):
        if 'context' not in kw:
            kw['context'] = _ssl_ctx
        return _orig_open(url, data=data, timeout=timeout, **kw)
    urllib.request.urlopen = _patched_urlopen
except Exception:
    pass  # certifi not installed or not needed — system certs used instead

# Console output below uses arrows/checkmarks (→, ✓, ⚠, ✗). A Windows console (or any
# stdout that isn't a real UTF-8 terminal) can report a legacy single-byte codepage instead,
# which raises UnicodeEncodeError the first time one of those prints — best-effort fix,
# never allowed to block startup.
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

# Reconfiguring the streams only makes Python EMIT utf-8; a Windows console still renders
# those bytes through its own codepage, so the banner's box-drawing characters and the
# ✓/⚠/✗ icons below arrive as mojibake on a default cp850/cp1252 console. This is the other
# half of that fix, and it's the only half available to CricketStreamQuickstart.exe, which
# has no .bat wrapper to run `chcp 65001` for it.
if sys.platform == "win32":
    try:
        import ctypes
        ctypes.windll.kernel32.SetConsoleOutputCP(65001)
        ctypes.windll.kernel32.SetConsoleCP(65001)
    except Exception:
        pass

BANNER = """
╔══════════════════════════════════════════════════════╗
║         CricketStream Overlay — Quick Start          ║
╚══════════════════════════════════════════════════════╝
"""

def log(msg, status=""):
    icons = {"ok": "  ✓", "warn": "  ⚠", "err": "  ✗", "": "   "}
    print(f"{icons.get(status,'   ')} {msg}")


# ── Never close without being read ────────────────────────────────────────────
# Launched from CricketStreamQuickstart.exe there's no .bat wrapper holding the
# window open, so a bare sys.exit() closes it instantly with the reason inside.
# Only pause when there's actually a person at a console: the setup wizard runs
# this as a subprocess and the tests import it, and neither should ever block.
# CRICKETSTREAM_NO_PAUSE=1 forces it off.
def _interactive():
    if os.environ.get("CRICKETSTREAM_NO_PAUSE", "") == "1":
        return False
    try:
        return sys.stdin is not None and sys.stdin.isatty()
    except Exception:
        return False


def pause(msg="Press Enter to close this window..."):
    if not _interactive():
        return
    try:
        input(f"\n  {msg}")
    except (EOFError, KeyboardInterrupt):
        pass


def die(title, *lines):
    """Readable, framed fatal error + a pause, then quit. Use instead of a bare
    sys.exit() for anything a person needs to act on."""
    print()
    print("  " + "=" * 60)
    print(f"   PROBLEM: {title}")
    print("  " + "=" * 60)
    for line in lines:
        print(f"   {line}" if line else "")
    pause()
    sys.exit(1)

GITHUB_REPO = "BridestoweBelstoneCC/Cricket-Live-Stream"

# server.py's serve_forever() never returns during normal operation, so proc.wait() only
# ever returns here because the server crashed (unhandled exception, OOM, anything) --
# capped so a genuinely broken config doesn't restart-loop forever. Found missing entirely
# by a real crash test against exe-runtime-preview (2026-08-20): killing server.py took the
# whole launcher down with it, no recovery.
SERVER_MAX_RESTARTS = 3

def get_current_version():
    """Best-effort local version via `git describe --tags`. Returns '' if this isn't a git
    checkout, git isn't installed, or anything else goes wrong -- purely informational, so a
    missing git binary must never be treated as an error."""
    try:
        script_dir = os.path.dirname(os.path.abspath(__file__))
        out = subprocess.run(["git", "describe", "--tags"], cwd=script_dir,
                              capture_output=True, text=True, timeout=5)
        if out.returncode == 0:
            return out.stdout.strip()
    except Exception:
        pass
    return ""

def check_for_updates():
    """Compares the locally checked-out version against the latest GitHub release. Purely
    informational -- any failure (no internet, no git, API rate limit, private fork) is
    swallowed silently so a flaky network check can never block match-day startup."""
    current = get_current_version()
    if not current:
        return
    try:
        req = urllib.request.Request(
            f"https://api.github.com/repos/{GITHUB_REPO}/releases/latest",
            headers={"User-Agent": "CricketStreamOverlay-quickstart"})
        with urllib.request.urlopen(req, timeout=5) as r:
            latest = json.loads(r.read().decode()).get("tag_name", "")
        if not latest:
            return
        # `git describe` returns exactly the tag when HEAD is on it, or "TAG-N-gHASH" when N
        # commits ahead of it -- either way means "on (or ahead of) the latest release".
        if current == latest or current.startswith(latest + "-"):
            log(f"Up to date ({current})", "ok")
        else:
            log(f"Update available: {latest} (you're on {current})", "warn")
            log("Run 'git pull' in the project folder to update", "warn")
    except Exception:
        pass  # no internet, rate-limited, etc. -- never block startup over this

def load_config():
    cfg = configparser.ConfigParser()
    config_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini")
    if not os.path.exists(config_path):
        die("No config.ini yet — setup hasn't been run",
            "",
            f"Expected it at:  {config_path}",
            "",
            "config.ini holds your club name, colours and API keys. It's",
            "created for you by the setup wizard, which only needs running once.",
            "",
            "TO FIX, run ONE of these first:",
            "  - CricketStreamSetup.exe   (also installs Python for you)",
            "  - Windows:  setup.bat",
            "  - Mac:      setup.sh",
            "",
            "Already ran setup? Then it saved config.ini somewhere else — check",
            "the folder the wizard printed, and move config.ini next to server.py.")
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
        "competition_id": str(match.get("competition_id","")),
        "ground":       match.get("ground_name",""),
        "ground_lat":   match.get("ground_latitude",""),
        "ground_lon":   match.get("ground_longitude",""),
        "umpire1":      match.get("umpire_1_name",""),
        "umpire2":      match.get("umpire_2_name",""),
        "scorer1":      match.get("scorer_1_name",""),
        "match_date":   match.get("match_date",""),
        "match_time":   match.get("match_time",""),
    }, None

def build_state(cfg, match, state_path=None):
    """Build match_state.json from config + API data, MERGED over the existing file.

    The merge matters: the control panel stores plenty that this launcher knows nothing
    about — the squad roster (shirt number → name, entered once per season), the weekend
    sponsor fields, the cached network test, badge club IDs... A full rewrite here used to
    silently wipe all of them on every match day. Fields quickstart genuinely owns (today's
    opposition, config.ini values, match-day safety defaults like demo_mode=False) still
    override; everything else in the existing file survives untouched.

    state_path is overridable for the test suite; production callers pass nothing."""
    if state_path is None:
        state_path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                  "match_state.json")
    try:
        import json as _j
        with open(state_path) as _f:
            exist = _j.load(_f)
        if not isinstance(exist, dict):
            exist = {}
    except Exception:
        exist = {}
    club  = cfg["Club"]
    api   = cfg["API"]
    score = cfg["Scoring"]
    obs   = cfg["OBS"]
    stream= cfg["Stream"]

    # Match-derived fields: no fixture found today → keep whatever the operator already
    # entered in the panel rather than stomping it back to placeholders.
    away_team   = match["away_team"]   if match else exist.get("away_team", "Opposition CC")
    away_abbrev = match["away_abbrev"] if match else exist.get("away_abbrev", "")
    competition = match["competition"] if match else exist.get("competition_name", "")
    competition_id = match["competition_id"] if match else exist.get("competition_id", "")
    umpire1     = match["umpire1"]     if match else exist.get("umpire1_name", "")
    umpire2     = match["umpire2"]     if match else exist.get("umpire2_name", "")
    scorer1     = match["scorer1"]     if match else exist.get("scorer1_name", "")
    pc_match_id = match["match_id"]    if match else exist.get("pc_match_id", "")

    fresh = {
        # Club / match identity
        "home_team":            club.get("name","My Club CC"),
        "home_abbrev":          club.get("abbreviation","CC"),
        "away_team":            away_team,
        "away_abbrev":          away_abbrev,
        "home_colour":          club.get("home_colour","#1a3a5c"),
        "away_colour":          exist.get("away_colour", "#7b2d2d"),
        "competition_name":     competition,
        "competition_id":       competition_id,
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
        "ground_filter":        score.get("ground_filter",""),
        "away_club_id":         match.get("away_club_id","") if match else exist.get("away_club_id",""),
        "logos_folder":         score.get("logos_folder",""),

        # Hard-coded operational defaults
        "demo_mode":            False,   # Always off — ready to stream
        "use_widget":           False,   # PCS Pro file only
        "max_overs":            int(stream.get("max_overs",50)),

        # PCS Pro / scoring
        "pcs_source":           (score.get("pcs_source","local") or "local").strip().lower(),
        "agent_host":           (score.get("agent_host","") or "").strip(),
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

        # Replay — on by default (including 50s), but panel edits survive re-runs
        "replay_enabled":       exist.get("replay_enabled",   True),
        "replay_on_fifty":      exist.get("replay_on_fifty",  True),
        "replay_folder":        os.path.expanduser(obs.get("replay_folder","")),
        "max_clips":            exist.get("max_clips",        500),
        "replay_duration":      exist.get("replay_duration",  18),

        # OBS
        "obs_host":             "localhost",
        "obs_port":             4455,
        "obs_password":         obs.get("obs_password",""),
        "obs_main_scene":       "Main",
        "obs_replay_scene":     "Replay",

        # YouTube
        "youtube_title_template": stream.get("youtube_title","LIVE: {home} vs {away}"),

        # Weather — panel-owned secret; a hard "" here silently wiped it every match day
        "weather_api_key":      exist.get("weather_api_key", ""),

        # Misc
        "poll_interval":        exist.get("poll_interval", 20),
        "match_url":            "",   # deliberately cleared — a pinned match is per-day
    }
    # Everything the panel knows that quickstart doesn't (roster, sponsor_name/sponsor_id,
    # network test cache, badge IDs, ...) rides through from the existing file.
    return {**exist, **fresh}

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

    # Check this BEFORE the two interactive prompts below. load_config() happens further
    # down, which meant a first-timer who hadn't run setup answered two questions about
    # today's match and only then got told setup had never been run — the sort of thing
    # that makes a tool feel like it's wasting your time on a match morning.
    if not os.path.exists(os.path.join(script_dir, "config.ini")):
        load_config()          # raises the "setup hasn't been run" box and stops

    # ── Check for updates ──
    check_for_updates()
    print()

    # ── Access mode ──
    print("  How do you want to run today?")
    print()
    print("    [1] Local only    — control panel on this laptop only (recommended)")
    print("    [2] Remote access — allow Tailscale / LAN (share with volunteer operators)")
    print()
    try:
        choice = input("  Enter 1 or 2 [1]: ").strip()
    except (EOFError, KeyboardInterrupt):
        choice = "1"
    remote_mode = (choice == "2")
    if remote_mode:
        log("Remote access mode — server will listen on all network interfaces", "ok")
    else:
        log("Local mode — control panel accessible on this machine only", "ok")
    print()

    # ── Data source ──
    print("  How is today's match being scored?")
    print()
    print("    [1] NV Play / PCS Pro — the scorer's laptop writes the live feed (recommended)")
    print("    [2] Manual scoring — score ball-by-ball from a phone/tablet at /scoring")
    print()
    try:
        choice = input("  Enter 1 or 2 [1]: ").strip()
    except (EOFError, KeyboardInterrupt):
        choice = "1"
    manual_mode = (choice == "2")
    if manual_mode:
        log("Manual scoring — set the match up at /scoring once the server starts", "ok")
        log("Tip: remote access mode + Tailscale lets a volunteer score from the boundary", "")
    else:
        # Foot-gun guard: a saved manual-scoring session OUTRANKS the scorer's feed in
        # /live — a leftover test session would silently replace NV Play on the overlay.
        manual_file = os.path.join(script_dir, "manual_scoring.json")
        if os.path.exists(manual_file):
            log("A saved manual-scoring session exists — it would OVERRIDE the scorer's feed!", "warn")
            try:
                ans = input("  Clear it so NV Play drives the overlay today? [Y/n]: ").strip().lower()
            except (EOFError, KeyboardInterrupt):
                ans = ""
            if ans in ("", "y", "yes"):
                try:
                    os.remove(manual_file)
                    log("Manual session cleared", "ok")
                except OSError as e:
                    log(f"Could not remove it ({e}) — delete manual_scoring.json by hand", "err")
            else:
                log("Keeping it — the overlay will show the MANUAL session, not the scorer", "warn")
    print()

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
        bitrate_kbps = int(cfg["Stream"].get("bitrate_kbps", "").strip() or 0)
    except ValueError:
        bitrate_kbps = 0
    try:
        # Import obs_setup from same directory
        sys.path.insert(0, script_dir)
        from obs_setup import obs_setup
        ok, messages = obs_setup(password=obs_pw, replay_folder=replay_dir, verbose=False,
                                 stream_key=cfg["Stream"].get("youtube_stream_key",
                                                              "").strip(),
                                 bitrate_kbps=bitrate_kbps)
        if ok:
            log("OBS configured — scenes and sources ready", "ok")
        else:
            last_err = next((m for m in reversed(messages) if "✗" in m), messages[-1] if messages else "")
            log(f"OBS: {last_err.strip()} — configure manually if needed", "warn")
    except Exception as e:
        log(f"OBS setup skipped: {e}", "warn")

    # ── Validate the score source ──
    if manual_mode:
        log("PCS folder checks skipped — scoring manually at /scoring today", "ok")
    elif state.get("pcs_source") == "agent":
        # Two-laptop mode: check we can actually see the scoring laptop before the
        # toss, rather than finding out at the start of play.
        agent_host = state.get("agent_host", "")
        try:
            import server as _srv
            if agent_host:
                info, err = _srv.agent_ping(agent_host)
                if info:
                    log(f"Scorer laptop reachable: {info.get('hostname','?')} ({agent_host})", "ok")
                else:
                    log(f"Scorer laptop not reachable at {agent_host}: {err}", "warn")
            else:
                agents = _srv.discover_agents(timeout=2.0)
                if agents:
                    a = agents[0]
                    log(f"Scorer laptop found: {a.get('hostname','?')} ({a['address']})", "ok")
                    if not a.get("file"):
                        log("Connected, but no scoreboard file yet — has the scorer "
                            "started PCS Pro scoreboard output?", "warn")
                else:
                    log("No scorer laptop found on the network — is scorer_agent.py "
                        "running on the scoring laptop? See TWO_LAPTOP_SETUP.md", "warn")
        except Exception as e:
            log(f"Could not check the scorer laptop: {e}", "warn")
    else:
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
        die("server.py is missing",
            "",
            f"Expected it at:  {server_path}",
            "",
            "quickstart.py and server.py have to sit in the same folder — it",
            "looks like only part of the project was copied, or one file got",
            "moved out.",
            "",
            "TO FIX: re-download/unzip the project and keep the folder intact.",
            "  https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream")

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
    server_env = os.environ.copy()
    server_env["BBCC_BIND_HOST"] = "0.0.0.0" if remote_mode else "127.0.0.1"
    proc = subprocess.Popen([sys.executable, server_path], env=server_env, **popen_kwargs)

    # ── Pre-load season batting stats (once per day, cached locally) ──
    # PlayCricket has no per-player stats endpoint, so the server pulls each completed
    # match's scorecard once, aggregates every player's season figures, and caches them to
    # season_stats_cache.json next to server.py. Re-running the same day reuses that file
    # (no further API calls). Done here so the figures are ready before the first player card.
    token = ""
    if wait_for_server():
        token = get_session_token(cfg)
        pull_season_stats(api_key, token)
        self_test()
    else:
        log("Server didn't respond in time — season stats will load on the first player card", "warn")

    start_telemetry(script_dir, popen_kwargs)

    print()
    print("  Control panel: http://localhost:5000/control")
    print("  Overlay:       http://localhost:5000/overlay")
    if manual_mode:
        print("  Manual scoring: http://localhost:5000/scoring  ← set the match up here")
        log("(Any PCS feed warnings in the pre-flight check above are expected today)", "")
    if remote_mode:
        ts_ip = None
        try:
            r = subprocess.run(["tailscale", "ip", "-4"],
                               capture_output=True, text=True, timeout=2)
            if r.returncode == 0:
                ts_ip = r.stdout.strip()
        except Exception:
            pass
        if ts_ip:
            print(f"  Remote panel:  http://{ts_ip}:5000/control  (Tailscale)")
            if manual_mode:
                print(f"  Remote scoring: http://{ts_ip}:5000/scoring  (Tailscale)")
        else:
            print("  Remote panel:  http://<tailscale-ip>:5000/control")
            if manual_mode:
                print("  Remote scoring: http://<tailscale-ip>:5000/scoring")
    print()
    print("  Keep this window open while streaming.")
    print("  Press Ctrl+C to stop the server.")
    print("  ─────────────────────────────────────────────")
    print()

    def launch_server():
        return subprocess.Popen([sys.executable, server_path], env=server_env, **popen_kwargs)

    run_server_with_restarts(proc, launch_server, token)


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
            with urllib.request.urlopen("http://127.0.0.1:5000/health", timeout=2):
                return True
        except Exception:
            _t.sleep(0.5)
    return False


def get_session_token(cfg):
    """Log in to the just-started server with the club password from config.ini, so this
    script's own calls to auth-gated endpoints (/player/stats/refresh, /report/generate) carry
    a valid Bearer token instead of 401ing against our own server. Returns "" if no password is
    set (auth disabled — server accepts unauthenticated calls) or if login fails."""
    import urllib.request, json
    pw = cfg["Auth"].get("club_password", "").strip() if cfg.has_section("Auth") else ""
    if not pw:
        return ""
    try:
        req = urllib.request.Request(
            "http://127.0.0.1:5000/login",
            data=json.dumps({"password": pw}).encode(),
            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as r:
            d = json.loads(r.read().decode())
        return d.get("session_token", "") if d.get("ok") else ""
    except Exception:
        return ""


def start_telemetry(script_dir, popen_kwargs):
    """Start the match-day telemetry logger in the background.

    Started here rather than left to the operator because it only has to be forgotten once
    to lose a whole match's data — which is exactly what happened on 2026-08-01, leaving a
    single match's telemetry to reason about the ground's connection from. Entirely
    best-effort: a failure to start must never hold up match day, so anything unexpected is
    logged and ignored.
    """
    import subprocess
    path = os.path.join(script_dir, "stream_telemetry.py")
    if not os.path.exists(path):
        return
    try:
        env = os.environ.copy()
        # The logger prints ✓/✗ and match text; Windows defaults piped stdout to cp1252,
        # which raises UnicodeEncodeError on them and would kill the process on line one.
        env["PYTHONIOENCODING"] = "utf-8"
        subprocess.Popen([sys.executable, path],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                         env=env, **popen_kwargs)
        log("Telemetry logging to diagnostics/ (bandwidth, drops, match context)", "ok")
    except Exception as e:
        log(f"Telemetry didn't start ({e}) — not fatal, run stream_telemetry.py by hand", "warn")


def pull_season_stats(api_key, token=""):
    """Trigger the season-stats build on the running server and report the result.
    Uses the non-forced refresh so a same-day cache is reused (no repeat API calls)."""
    import urllib.request, json
    if not api_key or api_key == "YOUR_KEY_HERE":
        log("Season stats skipped — no PlayCricket API key in config.ini", "warn")
        return
    log("Pulling season batting stats from PlayCricket (both teams, one-off, cached locally)...")
    try:
        req = urllib.request.Request("http://127.0.0.1:5000/player/stats/refresh",
            headers={"Authorization": f"Bearer {token}"} if token else {})
        with urllib.request.urlopen(req, timeout=180) as r:
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


def run_server_with_restarts(proc, launch, token="", max_restarts=SERVER_MAX_RESTARTS,
                              backoff=2, sleep=time.sleep):
    """Waits on the server subprocess, restarting it (capped) if it exits on its own, and
    handles the Ctrl+C shutdown path. `launch` is called with no args to relaunch after an
    unexpected exit -- injected (rather than closing over server_path/server_env/popen_kwargs
    directly) so a test can pass fake Popen-like objects with no real subprocess involved.
    `sleep` is injected the same way so a test doesn't have to actually wait out the backoff."""
    server_restarts = 0
    try:
        while True:
            proc.wait()
            # Reaching here (without US having called terminate/kill below) means the server
            # exited on its own -- a crash, not a shutdown. Ctrl+C is caught by the except
            # clause instead and never reaches this point.
            if server_restarts >= max_restarts:
                die(f"The server keeps crashing (restarted it {max_restarts} times)",
                    "",
                    "Whatever killed it is printed above this box — scroll up; the",
                    "last few lines before each restart are the useful ones.",
                    "",
                    "Common causes:",
                    "  - Port 5000 already in use (another copy still running?)",
                    "  - A setting in config.ini the server can't read",
                    "  - Packages missing or half-installed — re-run setup",
                    "",
                    "If it isn't obvious, please open an issue with those lines:",
                    "  https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues")
            server_restarts += 1
            log(f"Server stopped unexpectedly — restarting "
                f"(attempt {server_restarts}/{max_restarts})", "warn")
            sleep(backoff)   # brief backoff so a genuine crash-loop doesn't hammer the machine
            proc = launch()
    except KeyboardInterrupt:
        # Offer the report while the server is STILL RUNNING (match log lives in its memory)
        offer_match_report(token)
        print("\n  Shutting down server...")
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()
        print("  Server stopped. Bye!")


def offer_match_report(token=""):
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
        req = urllib.request.Request("http://127.0.0.1:5000/report/generate",
            headers={"Authorization": f"Bearer {token}"} if token else {})
        with urllib.request.urlopen(req, timeout=60) as r:
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
    try:
        main()
    except SystemExit:
        raise                    # die()/sys.exit() already said their piece
    except KeyboardInterrupt:
        # main() handles Ctrl+C during the match itself (it offers the report first);
        # this only catches one during startup, before the server is up.
        print("\n\n  Cancelled.")
        sys.exit(0)
    except Exception:
        # Same reasoning as setup_wizard.py: a traceback that flashes past for 40ms is
        # both the least useful thing a person can be shown and exactly the text needed
        # to diagnose it. Hold the window open with it on screen.
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
        pause()
        sys.exit(1)
