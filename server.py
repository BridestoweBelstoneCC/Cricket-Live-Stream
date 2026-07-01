"""
CricketStream Overlay — Stream Server
─────────────────────────────────────────────────
Run:  python server.py

Control panel  →  http://localhost:5001/control
OBS overlay    →  http://localhost:5001/overlay

Requirements (pip install each):
    websocket-client          — OBS WebSocket / instant replay
    anthropic                 — AI commentary (optional)
    google-api-python-client  — YouTube title updater (optional)
    google-auth-oauthlib      — YouTube title updater (optional)

Data sources (priority order):
    1. PCS Pro local file  — ball by ball, batter/bowler names, no internet needed
    2. PlayCricket widget  — score only, fallback when PCS not connected
    3. Demo mode           — dummy data for testing overlays

OBS setup:
    1. Tools → WebSocket Server Settings → Enable, note password
    2. Create a scene called exactly:  Main
    3. Create a scene called exactly:  Replay
    4. In Replay scene add a Media Source called: ReplayClip
       - Tick "Local File", tick "Restart playback when source becomes active"
    5. Settings → Output → Recording → Enable Replay Buffer (25 seconds)

PCS Pro setup (scorer's laptop):
    1. Tools → Configuration → Scoreboard → Enable output
    2. Copy scoreboard.template to the PCS Templates folder
    3. Set Template File to scoreboard.template
    4. Paste the output folder path into the control panel → PCS Pro output folder
"""

import json, os, re, glob, time, hashlib, hmac, secrets, threading, base64, datetime, subprocess

# Mac SSL fix — use certifi certificates to avoid CERTIFICATE_VERIFY_FAILED errors
try:
    import ssl, certifi, urllib.request as _ur
    _ssl_ctx   = ssl.create_default_context(cafile=certifi.where())
    _orig_open = _ur.urlopen
    def _patched_urlopen(url, data=None, timeout=10, **kw):
        if 'context' not in kw:
            kw['context'] = _ssl_ctx
        return _orig_open(url, data=data, timeout=timeout, **kw)
    _ur.urlopen = _patched_urlopen
except Exception:
    pass  # certifi not installed or not needed — system certs used instead
import urllib.request, urllib.error, html
from http.server import BaseHTTPRequestHandler, HTTPServer, ThreadingHTTPServer
import sqlite3
from urllib.parse import urlparse, urlencode

SERVER_START_TIME = time.time()

# ── Auth ─────────────────────────────────────────────────────
# control_token: signing key for session tokens — never shown to users.
# club_password:  what operators type in the login form.
# Auth is disabled when club_password is empty (safe on localhost).
_CONTROL_TOKEN = ""
_CLUB_PASSWORD = ""
_SESSION_HOURS = 12
_BIND_HOST     = "127.0.0.1"

def _load_auth_config():
    global _CONTROL_TOKEN, _CLUB_PASSWORD, _BIND_HOST
    import configparser as _cp
    cfg = _cp.ConfigParser()
    cfg_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini")
    if os.path.exists(cfg_path):
        cfg.read(cfg_path, encoding="utf-8")
        _CONTROL_TOKEN = cfg.get("Auth",    "control_token", fallback="").strip()
        _CLUB_PASSWORD = cfg.get("Auth",    "club_password", fallback="").strip()
        _BIND_HOST     = (os.environ.get("BBCC_BIND_HOST","").strip()
                          or cfg.get("Network", "bind_host", fallback="127.0.0.1").strip())

_load_auth_config()

def _make_session_token():
    """Issue a signed, expiring session token: '{expiry}:{nonce}:{sig}'."""
    expiry = str(int(time.time()) + _SESSION_HOURS * 3600)
    nonce  = secrets.token_hex(8)
    sig    = hmac.new(_CONTROL_TOKEN.encode(), f"{expiry}:{nonce}".encode(),
                      hashlib.sha256).hexdigest()
    return f"{expiry}:{nonce}:{sig}"

def _verify_session_token(token):
    """True if the token has a valid signature and has not expired."""
    try:
        expiry, nonce, sig = token.split(":", 2)
        if int(expiry) < time.time():
            return False
        expected = hmac.new(_CONTROL_TOKEN.encode(), f"{expiry}:{nonce}".encode(),
                            hashlib.sha256).hexdigest()
        return hmac.compare_digest(sig, expected)
    except Exception:
        return False

# ── Rate limiting (manual AI endpoints only) ─────────────────
# /commentary/over/generate is NOT here — the overlay fires it automatically at
# end of each over and for opening-pair player cards; capping it would break the
# live experience. Only manual button-triggered endpoints are rate-limited.
_RATE_LIMITS = {
    "/commentary/test":        60,   # test button — 60 s cooldown
    "/report/generate":       120,   # AI match report — 2 min
    "/social/image/generate": 120,   # AI social graphic — 2 min
}
_rate_limit_ts   = {}
_rate_limit_lock = threading.Lock()

# ── Commentary state ──────────────────────────────────────────
# Stores the latest AI-generated commentary line and triggers
# the overlay to display it.
_commentary = {
    "text":      "",
    "pending":   False,   # True when a new line is ready to display
    "last_over": -1,      # Last over we generated commentary for
}

def get_commentary():
    return dict(_commentary)

def set_commentary(text):
    _commentary["text"]    = text
    _commentary["pending"] = True
    print(f"  💬  Commentary: {text}")

def pop_commentary():
    """Called by overlay poll — returns text and clears pending flag."""
    text    = _commentary["text"]
    pending = _commentary["pending"]
    _commentary["pending"] = False
    return {"text": text, "pending": pending}

# ── AI commentary generator ───────────────────────────────────

def generate_commentary(state, innings_history):
    """
    Calls Claude Haiku to generate a single broadcast-style commentary
    line based on the current match state.
    Runs in a background thread — never blocks the overlay.
    """
    cfg = load_state()
    api_key = cfg.get("anthropic_api_key","").strip() \
              or os.environ.get("ANTHROPIC_API_KEY","").strip()

    if not api_key:
        print("  ✗  Commentary: no Anthropic API key set")
        return

    try:
        import anthropic
    except ImportError:
        print("  ✗  Commentary: run 'pip install anthropic'")
        return

    # Build context for Claude
    batting_team = state.get("battingTeamName","Batting team")
    bowling_team = state.get("bowlingTeamName","Bowling team")
    score        = state.get("score", 0)
    wickets      = state.get("wickets", 0)
    overs        = state.get("overs", 0.0)
    rr           = round(score / overs, 2) if overs > 0 else 0
    b1           = state.get("batter1", {})
    b2           = state.get("batter2", {})
    bowler       = state.get("bowler", {})
    max_overs    = cfg.get("max_overs", 50)

    # Build a concise match summary for the prompt
    context = (
        f"Match: {batting_team} vs {bowling_team}\n"
        f"Score: {score}-{wickets} off {overs} overs (max {max_overs})\n"
        f"Run rate: {rr}\n"
        f"Batters: {b1.get('name','?')} {b1.get('runs',0)} ({b1.get('balls',0)}b), "
        f"{b2.get('name','?')} {b2.get('runs',0)} ({b2.get('balls',0)}b)\n"
        f"Bowler: {bowler.get('name','?')} {bowler.get('wickets',0)}-{bowler.get('runs',0)} "
        f"({bowler.get('overs','0')} ov)\n"
    )

    if innings_history:
        context += f"Recent events: {'; '.join(innings_history[-3:])}\n"

    prompt = (
        f"You are a cricket commentator for a club match in Devon, England. "
        f"Based on the following match situation, write exactly ONE short, "
        f"broadcast-style commentary observation (maximum 15 words). "
        f"Be specific to the numbers. No filler. No emojis. "
        f"Sound like Sky Sports, not a school report.\n\n"
        f"{context}\n"
        f"Commentary:"
    )

    try:
        client = anthropic.Anthropic(api_key=api_key)
        message = client.messages.create(
            model="claude-haiku-4-5",
            max_tokens=60,
            messages=[{"role": "user", "content": prompt}]
        )
        text = message.content[0].text.strip().strip('"').strip("'")
        # Trim to sentence if model returns more than asked
        if "." in text:
            text = text.split(".")[0].strip() + "."
        set_commentary(text)
    except Exception as e:
        print(f"  ✗  Commentary API error: {e}")

# ── Innings event history (for commentary context) ────────────
_innings_events = []
_over_commentary  = {'text': '', 'over': -1}

# ── Match log: accumulates key events for the end-of-match report ──
_match_log = {
    "events": [],          # chronological notable events
    "innings": {},         # innings_number -> {team, score, wickets, overs, top_scorer, ...}
    "fall_of_wickets": [], # {batter, score, over, howout}
    "milestones": [],      # {batter, milestone, score}
    "started": None,
}

def match_log_event(kind, detail):
    """Record a notable match event for the report generator."""
    if _match_log["started"] is None:
        _match_log["started"] = datetime.datetime.now().isoformat()
    _match_log["events"].append({"kind": kind, "detail": detail})
    if len(_match_log["events"]) > 400:
        _match_log["events"] = _match_log["events"][-400:]

def match_log_snapshot(state):
    """Update per-innings running totals from the current state."""
    inn = str(state.get("innings", 1))
    rec = _match_log["innings"].setdefault(inn, {})
    rec["batting_team"] = state.get("battingTeamName", "")
    rec["bowling_team"] = state.get("bowlingTeamName", "")
    rec["score"]        = state.get("score", 0)
    rec["wickets"]      = state.get("wickets", 0)
    rec["overs"]        = state.get("overs", 0)

def match_log_reset():
    _match_log["events"].clear()
    _match_log["innings"].clear()
    _match_log["fall_of_wickets"].clear()
    _match_log["milestones"].clear()
    _match_log["started"] = None


# ════════════════════════════════════════════════════════════════════════════
#  Ball-by-ball logger — your own resilient match database (SQLite)
#  Two tiers:
#   • LIVE  : the current over is rewritten from the ticker every poll, so a scorer's
#             within-over edits/insertions/deletions are always captured. Completed overs
#             freeze as last seen.
#   • TRUTH : reconcile_match() pulls PlayCricket's published scorecard afterwards and writes
#             authoritative per-innings batting/bowling aggregates, correcting any live drift.
# ════════════════════════════════════════════════════════════════════════════
_db_lock = threading.Lock()

def _db_path():
    return os.path.join(os.path.dirname(os.path.abspath(__file__)), "match_data.db")

def _db():
    conn = sqlite3.connect(_db_path(), timeout=5)
    try:
        conn.execute("PRAGMA journal_mode=WAL")
    except sqlite3.Error:
        pass
    return conn

def db_init():
    try:
        with _db_lock, _db() as c:
            c.executescript("""
            CREATE TABLE IF NOT EXISTS matches (
                match_id TEXT PRIMARY KEY, date TEXT, home TEXT, away TEXT,
                competition TEXT, team_key TEXT, reconciled INTEGER DEFAULT 0,
                result TEXT, updated TEXT);
            CREATE TABLE IF NOT EXISTS balls (
                match_id TEXT, innings INTEGER, over INTEGER, ball INTEGER,
                batting_team TEXT, batter TEXT, non_striker TEXT, bowler TEXT,
                outcome TEXT, runs INTEGER, extra TEXT, is_wicket INTEGER,
                legal INTEGER, cum_runs INTEGER, cum_wkts INTEGER, ts TEXT,
                PRIMARY KEY (match_id, innings, over, ball));
            CREATE TABLE IF NOT EXISTS innings_totals (
                match_id TEXT, innings INTEGER, batting_team TEXT,
                runs INTEGER, wickets INTEGER, overs TEXT,
                PRIMARY KEY (match_id, innings));
            CREATE TABLE IF NOT EXISTS batting (
                match_id TEXT, innings INTEGER, position INTEGER, name TEXT,
                how_out TEXT, runs INTEGER, balls INTEGER, fours INTEGER, sixes INTEGER,
                PRIMARY KEY (match_id, innings, name));
            CREATE TABLE IF NOT EXISTS bowling (
                match_id TEXT, innings INTEGER, name TEXT, overs TEXT,
                maidens INTEGER, runs INTEGER, wickets INTEGER,
                PRIMARY KEY (match_id, innings, name));
            """)
    except sqlite3.Error as e:
        print(f"  ⚠  match DB init failed: {e}")

def _classify_ball(token):
    """Python port of the overlay's classifyBall → structured outcome for one delivery."""
    t = (token or "").strip()
    if not t or t in (".", "0"):
        return {"outcome": "dot", "runs": 0, "extra": None, "wicket": False, "legal": True}
    if t == "W":
        return {"outcome": "W", "runs": 0, "extra": None, "wicket": True, "legal": True}
    if t == "w":
        return {"outcome": "wide", "runs": 1, "extra": "wide", "wicket": False, "legal": False}
    if len(t) > 2 and t[0] == "w" and t[1] == "+":
        try: r = int(t[2:])
        except ValueError: r = 0
        return {"outcome": "wide", "runs": 1 + r, "extra": "wide", "wicket": False, "legal": False}
    low = t.lower()
    nb = low.find("nb")
    if nb >= 0:
        try: pre = int(t[:nb])
        except ValueError: pre = 0
        after, post = t[nb + 2:], 0
        if len(after) > 1 and after[0] == "+":
            try: post = int(after[1:])
            except ValueError: post = 0
        return {"outcome": "noball", "runs": 1 + pre + post, "extra": "noball", "wicket": False, "legal": False}
    if low.endswith("lb"):
        try: r = int(t[:-2])
        except ValueError: r = 0
        return {"outcome": "legbye", "runs": r, "extra": "legbye", "wicket": False, "legal": True}
    if low.endswith("b") and not low.endswith("nb"):
        try: r = int(t[:-1])
        except ValueError: r = 0
        return {"outcome": "bye", "runs": r, "extra": "bye", "wicket": False, "legal": True}
    try:
        n = int(t)
        return {"outcome": str(n), "runs": n, "extra": None, "wicket": False, "legal": True}
    except ValueError:
        return {"outcome": "dot", "runs": 0, "extra": None, "wicket": False, "legal": True}

def _parse_ticker(s):
    if not s or s.startswith("{{"):
        return []
    import re as _re
    return [_classify_ball(t) for t in _re.split(r"[\s\u00b7]+", s.strip()) if t]

def current_match_id():
    """Stable id for the match being logged: the PlayCricket match id if we have one,
    else a date+teams fallback so a non-pinned match still gets a consistent id all day."""
    cfg = load_state()
    mu = cfg.get("match_url", "").strip()
    mid = extract_pc_match_id(mu) if mu else (str(cfg.get("pc_match_id", "") or "").strip() or None)
    if mid:
        return str(mid)
    home = (cfg.get("home_team") or "home").replace(" ", "")[:14]
    away = (cfg.get("away_team") or "away").replace(" ", "")[:14]
    return f"{datetime.date.today().isoformat()}_{home}_v_{away}"

def log_ball_data(state):
    """Capture the current over from the live ticker. Rewrites the whole current over each
    call (delete + reinsert) so scorer edits within the over are reflected; once the over
    rolls on it freezes. Never raises — logging must not affect the stream."""
    try:
        balls = _parse_ticker(state.get("last_ball", ""))
        if not balls:
            return
        innings  = int(state.get("innings", 1) or 1)
        over_idx = int(float(state.get("overs", 0) or 0))     # completed overs = current over no.
        score    = int(state.get("score", 0) or 0)
        wkts     = int(state.get("wickets", 0) or 0)
        bteam    = state.get("battingTeamName", "")
        b1, b2   = state.get("batter1", {}) or {}, state.get("batter2", {}) or {}
        bowler   = (state.get("bowler", {}) or {}).get("name", "")
        striker, nonstriker = b1.get("name", ""), b2.get("name", "")
        mid      = current_match_id()
        over_runs  = sum(b["runs"] for b in balls)
        over_start = score - over_runs
        cfg = load_state()
        now = datetime.datetime.now().isoformat(timespec="seconds")
        with _db_lock, _db() as c:
            c.execute("INSERT OR IGNORE INTO matches(match_id,date,home,away,competition,team_key,updated) "
                      "VALUES(?,?,?,?,?,?,?)",
                      (mid, datetime.date.today().isoformat(), cfg.get("home_team", ""),
                       cfg.get("away_team", ""), cfg.get("competition", ""), _team_key(bteam), now))
            # New ball data means the published aggregates may now be stale → mark for reconcile
            c.execute("UPDATE matches SET updated=?, reconciled=0 WHERE match_id=?", (now, mid))
            c.execute("DELETE FROM balls WHERE match_id=? AND innings=? AND over=?",
                      (mid, innings, over_idx))
            run = over_start
            for i, b in enumerate(balls, start=1):
                run += b["runs"]
                c.execute(
                    "INSERT OR REPLACE INTO balls(match_id,innings,over,ball,batting_team,batter,"
                    "non_striker,bowler,outcome,runs,extra,is_wicket,legal,cum_runs,cum_wkts,ts) "
                    "VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (mid, innings, over_idx, i, bteam, striker, nonstriker, bowler,
                     b["outcome"], b["runs"], b["extra"], int(b["wicket"]), int(b["legal"]),
                     run, wkts, now))
    except Exception:
        pass

def reconcile_match(match_id):
    """Authoritative pass: write per-innings batting/bowling aggregates from PlayCricket's
    published scorecard. This is the trusted layer for analysis, immune to live-scoring drift."""
    cfg = load_state()
    api_key = (cfg.get("playcricket_api_key") or cfg.get("api_token") or "").strip()
    if not api_key:
        return {"ok": False, "error": "No PlayCricket API key set"}
    try:
        det = _pc_get_json(f"https://play-cricket.com/api/v2/match_detail.json"
                           f"?api_token={api_key}&match_id={match_id}")
    except Exception as e:
        return {"ok": False, "error": f"PlayCricket fetch failed: {e}"}
    md = (det.get("match_details") or [{}])[0]
    innings = md.get("innings", []) or []
    if not innings:
        return {"ok": False, "error": "No published scorecard for that match yet"}
    now = datetime.datetime.now().isoformat(timespec="seconds")
    facts = build_match_facts_from_pc(match_id)
    with _db_lock, _db() as c:
        for idx, inn in enumerate(innings, start=1):
            c.execute("INSERT OR REPLACE INTO innings_totals VALUES(?,?,?,?,?,?)",
                      (match_id, idx, inn.get("team_batting_name", ""),
                       _pc_parse_int(inn.get("runs")) or 0, _pc_parse_int(inn.get("wickets")) or 0,
                       str(inn.get("overs", "") or "")))
            for pos, b in enumerate(inn.get("bat", []) or [], start=1):
                c.execute("INSERT OR REPLACE INTO batting VALUES(?,?,?,?,?,?,?,?,?)",
                          (match_id, idx, pos, b.get("batsman_name", ""), b.get("how_out", ""),
                           _pc_parse_int(b.get("runs")) or 0, _pc_parse_int(b.get("balls")) or 0,
                           _pc_parse_int(b.get("fours")) or 0, _pc_parse_int(b.get("sixes")) or 0))
            for bw in inn.get("bowl", []) or []:
                c.execute("INSERT OR REPLACE INTO bowling VALUES(?,?,?,?,?,?,?)",
                          (match_id, idx, bw.get("bowler_name", ""), str(bw.get("overs", "") or ""),
                           _pc_parse_int(bw.get("maidens")) or 0, _pc_parse_int(bw.get("runs")) or 0,
                           _pc_parse_int(bw.get("wickets")) or 0))
        c.execute("UPDATE matches SET reconciled=1, result=?, updated=? WHERE match_id=?",
                  (facts.get("result", "") if facts.get("ok") else "", now, match_id))
    return {"ok": True, "innings": len(innings), "result": facts.get("result", "")}

def db_status():
    try:
        with _db_lock, _db() as c:
            balls = c.execute("SELECT COUNT(*) FROM balls").fetchone()[0]
            matches = c.execute("SELECT COUNT(*) FROM matches").fetchone()[0]
            recent = c.execute("SELECT match_id,date,home,away,reconciled FROM matches "
                               "ORDER BY updated DESC LIMIT 10").fetchall()
        return {"ok": True, "balls": balls, "matches": matches,
                "recent": [{"match_id": r[0], "date": r[1], "home": r[2],
                            "away": r[3], "reconciled": bool(r[4])} for r in recent]}
    except sqlite3.Error as e:
        return {"ok": False, "error": str(e), "balls": 0, "matches": 0, "recent": []}

def export_match_csv(match_id):
    import io, csv
    buf = io.StringIO()
    w = csv.writer(buf)
    w.writerow(["innings", "over", "ball", "batting_team", "batter", "non_striker",
                "bowler", "outcome", "runs", "extra", "is_wicket", "legal", "cum_runs", "cum_wkts"])
    with _db_lock, _db() as c:
        for r in c.execute("SELECT innings,over,ball,batting_team,batter,non_striker,bowler,"
                           "outcome,runs,extra,is_wicket,legal,cum_runs,cum_wkts FROM balls "
                           "WHERE match_id=? ORDER BY innings,over,ball", (match_id,)):
            w.writerow(r)
    return buf.getvalue()


def generate_over_commentary(over_num, over_runs, bowler, figs, balls_str, state):
    cfg     = load_state()
    api_key = cfg.get('anthropic_api_key','').strip() or os.environ.get('ANTHROPIC_API_KEY','')
    if not api_key: return
    try:
        import anthropic
    except ImportError: return
    sc   = state.get('score', 0)
    wk   = state.get('wickets', 0)
    ov   = float(state.get('overs', over_num) or over_num or 1)
    rr   = round(sc / ov, 2)
    pr   = state.get('partnership_runs', 0)
    pb   = state.get('partnership_balls', 0)
    bat  = state.get('battingTeamName', cfg.get('home_team',''))
    mo   = cfg.get('max_overs', 50)
    wkt  = 'including a wicket' if 'W' in str(balls_str) else 'no wickets'
    pl   = 's' if over_runs != 1 else ''
    # Season-stat notables: give the commentator the storylines (season-best scores,
    # past-average innings) so the AI can mention them like a real broadcast would.
    notable = []
    try:
        for bk in ('batter1', 'batter2'):
            b = state.get(bk) or {}
            nm, rn = b.get('name', ''), int(b.get('runs', 0) or 0)
            if not nm or nm == '—':
                continue
            lk, _ = resolve_player(nm, str(b.get('number', '') or ''))
            rec = lookup_season_stats(lk)
            if not rec:
                continue
            try:    season_hs = int(str(rec.get('hs', '0')).replace('*', '') or 0)
            except ValueError: season_hs = 0
            try:    season_av = float(rec.get('avg', 0) or 0)
            except ValueError: season_av = 0.0
            if season_hs and rn > season_hs:
                notable.append(f"{nm} is now past his season-best of {rec.get('hs')} — new highest score of the season.")
            elif season_av and rn > season_av * 1.5 and rn >= 25:
                notable.append(f"{nm} ({rn}) is well past his season average of {season_av:.0f}.")
    except Exception:
        pass
    notable_txt = (' Notable: ' + ' '.join(notable)) if notable else ''
    prompt = (
        'You are a cricket commentator for a village match in Devon, England. '
        'Write ONE punchy broadcast-style sentence (max 20 words) summarising '
        'the over. Be specific to numbers. Like Sky Sports - direct, vivid. '
        'If a "Notable" fact is given, build the sentence around it.\n\n'
        f'Over {over_num}: {over_runs} run{pl} ({wkt}). '
        f'Bowler: {bowler} {figs}. '
        f'Score: {bat} {sc}-{wk} off {over_num} ov (RR {rr}, max {mo}). '
        f'Partnership: {pr} off {pb} balls. Balls: {balls_str}.{notable_txt}\n'
        'Commentary:'
    )
    def _go():
        global _over_commentary
        try:
            c   = anthropic.Anthropic(api_key=api_key)
            msg = c.messages.create(model='claude-haiku-4-5', max_tokens=80,
                                    messages=[{'role':'user','content':prompt}])
            t = msg.content[0].text.strip().strip('"').strip("'")
            if '.' in t: t = t.split('.')[0].strip() + '.'
            _over_commentary = {'text': t, 'over': over_num}
            print(f'  ✓  Over {over_num} commentary: {t[:55]}')
        except Exception as exc:
            print(f'  ✗  Over commentary: {exc}')
    threading.Thread(target=_go, daemon=True).start()


def record_event(text):
    _innings_events.append(text)
    if len(_innings_events) > 20:
        _innings_events.pop(0)

def clear_events():
    _innings_events.clear()


# ── Pending commands for overlay ─────────────────────────────
_commands = {"show_weather": False, "hide_weather": False, "show_scorecard": False,
             "show_player_cards": False}
def set_command(key, val=True): _commands[key] = val
def pop_commands():
    s = dict(_commands)
    for k in _commands: _commands[k] = False
    return s

# ── Weather (Open-Meteo, no API key needed) ───────────────────
GROUND_LAT, GROUND_LON = 50.691, -4.093
WMO_ICONS = {0:"☀",1:"🌤",2:"⛅",3:"☁",45:"🌫",48:"🌫",51:"🌦",53:"🌦",55:"🌧",61:"🌧",63:"🌧",65:"🌧",71:"🌨",73:"🌨",75:"❄",80:"🌦",81:"🌧",82:"⛈",95:"⛈",96:"⛈",99:"⛈"}
WMO_DESC  = {0:"Clear sky",1:"Mainly clear",2:"Partly cloudy",3:"Overcast",45:"Foggy",48:"Freezing fog",51:"Light drizzle",53:"Drizzle",55:"Heavy drizzle",61:"Light rain",63:"Rain",65:"Heavy rain",71:"Light snow",73:"Snow",75:"Heavy snow",80:"Rain showers",81:"Heavy showers",82:"Violent showers",95:"Thunderstorm",96:"Thunderstorm+hail",99:"Heavy thunderstorm"}

def fetch_weather_data():
    try:
        params = urlencode({"latitude":GROUND_LAT,"longitude":GROUND_LON,
            "current":"temperature_2m,relative_humidity_2m,wind_speed_10m,weather_code",
            "hourly":"precipitation_probability","forecast_hours":4,
            "wind_speed_unit":"mph","timezone":"Europe/London"})
        req = urllib.request.Request(
            f"https://api.open-meteo.com/v1/forecast?{params}",
            headers={"User-Agent":"CricketStreamOverlay/1.0"})
        with urllib.request.urlopen(req, timeout=8) as r:
            data = json.loads(r.read().decode())
        cur  = data["current"]
        code = int(cur.get("weather_code", 0))
        # Rain risk = worst hourly precipitation probability over the next ~3 hours.
        # Drives the automatic DLS par display in the overlay.
        probs = (data.get("hourly", {}) or {}).get("precipitation_probability") or []
        rain_prob = max([p for p in probs[:4] if p is not None], default=0)
        return {"ok":True,"temp":round(cur.get("temperature_2m",0)),
                "humidity":round(cur.get("relative_humidity_2m",0)),
                "wind":f"{round(cur.get('wind_speed_10m',0))} mph",
                "rain_prob": int(rain_prob),
                "icon":WMO_ICONS.get(code,"?"),"description":WMO_DESC.get(code,"Unknown")}
    except Exception as e:
        return {"ok":False,"error":str(e)}

# ── YouTube title updater ─────────────────────────────────────
YT_TOKEN_FILE = "yt_token.json"
YT_CREDS_FILE = "yt_credentials.json"
YT_SCOPES     = ["https://www.googleapis.com/auth/youtube"]

def update_youtube_title(title):
    if not os.path.exists(YT_CREDS_FILE):
        return False, "yt_credentials.json not found — see YouTube setup instructions in control panel"
    try:
        from google.oauth2.credentials import Credentials
        from google_auth_oauthlib.flow import InstalledAppFlow
        from google.auth.transport.requests import Request
        from googleapiclient.discovery import build
    except ImportError:
        return False, "Run: pip install google-api-python-client google-auth-oauthlib"
    creds = None
    if os.path.exists(YT_TOKEN_FILE):
        creds = Credentials.from_authorized_user_file(YT_TOKEN_FILE, YT_SCOPES)
    if not creds or not creds.valid:
        if creds and creds.expired and creds.refresh_token:
            creds.refresh(Request())
        else:
            flow = InstalledAppFlow.from_client_secrets_file(YT_CREDS_FILE, YT_SCOPES)
            creds = flow.run_local_server(port=8091, open_browser=True)
        open(YT_TOKEN_FILE,"w").write(creds.to_json())
    try:
        yt    = build("youtube","v3",credentials=creds)
        resp  = yt.liveBroadcasts().list(part="id,snippet",broadcastStatus="active",broadcastType="all").execute()
        items = resp.get("items",[])
        if not items:
            resp  = yt.liveBroadcasts().list(part="id,snippet",broadcastStatus="upcoming",broadcastType="all").execute()
            items = resp.get("items",[])
        if not items:
            return False, "No active or upcoming broadcast found on this YouTube account"
        bid = items[0]["id"]
        yt.liveBroadcasts().update(part="snippet",body={
            "id":bid,
            "snippet":{"title":title,"scheduledStartTime":items[0]["snippet"].get("scheduledStartTime","")}
        }).execute()
        print(f"  ✓  YouTube title: {title}")
        return True, f"Title updated to: {title}"
    except Exception as e:
        return False, str(e)

# ── ResultsVault live data fetcher ───────────────────────────
# PlayCricket's widget calls api.resultsvault.co.uk for live data.
# We replicate that call directly — no API key needed.
#
# Step 1: Fetch PlayCricket widget HTML → extract PC + RV match IDs
# Step 2: Call ResultsVault matches endpoint for live scorecard data

RV_APIID  = "1003"
_rv_cache = {"rv_id": None, "pc_id": None, "club_id": None}
_widget_last_poll = 0
_widget_last_data = None

def fetch_widget_match_ids(club_id):
    """
    Fetch PlayCricket widget endpoint and extract today's match IDs.
    The endpoint returns JSON with a matches array.
    Returns (pc_match_id, rv_match_id) or (None, None).
    """
    url = (f"https://www.play-cricket.com/embed_widget/live_scorer_widgets"
           f"?club_id={club_id}&days=0")
    try:
        req = urllib.request.Request(url, headers={
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "Accept":     "application/json, text/html, */*",
            "Referer":    "https://www.play-cricket.com/",
        })
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read().decode("utf-8", errors="replace")

        print(f"  Widget HTML: {len(raw)} chars")

        # Try parsing as JSON first (newer API format)
        try:
            data = json.loads(raw)
            matches = data.get("matches", [])
            if matches:
                # Take first match with a match_id
                for m in matches:
                    pc_id = m.get("match_id")
                    if pc_id:
                        pc_id = int(pc_id)
                        rv_id = fetch_rv_mapping(pc_id)
                        if rv_id:
                            print(f"  IDs via JSON: PC={pc_id} RV={rv_id}")
                            return pc_id, rv_id
        except (json.JSONDecodeError, ValueError):
            pass  # Not JSON, fall through to HTML parsing

        # Try data attributes (older widget HTML format)
        pc_ids = re.findall(r'data-match-id=["\']?(\d+)["\']?', raw)
        rv_ids = re.findall(r'data-rv-id=["\']?(\d+)["\']?', raw)
        if pc_ids and rv_ids:
            print(f"  IDs via data attrs: PC={pc_ids[0]} RV={rv_ids[0]}")
            return int(pc_ids[0]), int(rv_ids[0])

        # Try RV mappings URL embedded in JS
        rv_map = re.findall(r'/rv/mappings/4/12/(\d+)/', raw)
        if rv_map:
            pc_id = int(rv_map[0])
            rv_id = fetch_rv_mapping(pc_id)
            if rv_id:
                print(f"  IDs via JS: PC={pc_id} RV={rv_id}")
                return pc_id, rv_id

        # Last resort: any 7-8 digit match_id pattern
        any_ids = re.findall(r'"match_id"\s*:\s*(\d{6,8})', raw)
        if any_ids:
            pc_id = int(any_ids[0])
            rv_id = fetch_rv_mapping(pc_id)
            if rv_id:
                return pc_id, rv_id

        print(f"  Widget preview: {raw[:400]}")
        return None, None
    except Exception as e:
        print(f"  ✗  Widget error: {e}")
        return None, None

def fetch_rv_mapping(pc_match_id):
    """
    Attempt to map a PlayCricket match ID to a ResultsVault match ID.
    Note: api.resultsvault.co.uk requires a browser session cookie to authorise.
    This will return None unless a manual rv_match_id is provided in the control panel.
    Kept for future use if auth can be resolved.
    """
    return None

def fetch_rv_live(rv_match_id):
    """Fetch full live match data from ResultsVault."""
    url = (f"https://api.resultsvault.co.uk/rv/130000/matches/{rv_match_id}/"
           f"?apiid={RV_APIID}&strmflg=3")
    try:
        req = urllib.request.Request(url, headers={
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "Accept":     "application/json, text/plain, */*",
            "Referer":    "https://www.play-cricket.com/",
            "Origin":     "https://www.play-cricket.com",
            "x-ias-api-request": "true",
        })
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except Exception as e:
        print(f"  ✗  RV live error: {e}")
        return None

def extract_pc_match_id(url_or_id):
    """Extract a PlayCricket match ID from a URL or raw ID string."""
    if not url_or_id:
        return None
    s = str(url_or_id).strip()
    if s.isdigit():
        return int(s)
    ids = re.findall(r'/(\d{6,8})(?:[/?#]|$)', s)
    if ids:
        return int(ids[-1])
    ids = re.findall(r'\b(\d{6,8})\b', s)
    if ids:
        return int(ids[-1])
    return None

def parse_widget_json_score(data, home_name, away_name, pc_match_id=None):
    """
    Parse score directly from widget JSON response.
    Fast — no extra API calls. Gives us score/wickets/overs immediately.
    Batter and bowler names are filled in by RV on a slower cycle.
    If pc_match_id is provided, filters to that specific match.
    """
    matches = data.get("matches", [])
    if not matches:
        return None, None

    # Filter to specific match if we have a match ID
    if pc_match_id:
        filtered = [m for m in matches if str(m.get("match_id","")) == str(pc_match_id)]
        if filtered:
            matches = filtered
        # If not found, the match may not be in this club's widget
        # (e.g. it's an away match listed under the other club)

    m = matches[0]
    pc_id      = m.get("match_id")
    home_team  = f"{m.get('home_club_name','')} {m.get('home_team_name','')}".strip()
    away_team  = f"{m.get('away_club_name','')} {m.get('away_team_name','')}".strip()
    home_score_str = m.get("home_team_score", "") or ""
    away_score_str = m.get("away_team_score", "") or ""
    batted_first   = str(m.get("batted_first", ""))
    home_team_id   = str(m.get("home_team_id", ""))

    home_yet = "yet to bat" in home_score_str.lower()
    away_yet = "yet to bat" in away_score_str.lower()

    if away_yet and not home_yet:
        batting_score_str = home_score_str
        batting_team, bowling_team = home_team, away_team
        innings = 1
    elif home_yet and not away_yet:
        batting_score_str = away_score_str
        batting_team, bowling_team = away_team, home_team
        innings = 1
    else:
        # Both teams have a score — could be 2nd innings, or widget showing
        # the completed 1st innings total alongside the live 1st innings score.
        # Only set innings=2 if the team batting second actually has runs.
        if home_team_id == batted_first:
            batting_score_str = away_score_str
            batting_team, bowling_team = away_team, home_team
        else:
            batting_score_str = home_score_str
            batting_team, bowling_team = home_team, away_team
        # Check if the batting team's score looks live (has overs)
        # If batting_score_str has overs in it, it's genuinely live 2nd innings
        # If it looks like a completed total (e.g. "196-3 (50.0)"), it is 2nd innings
        # If the 1st innings team shows all out, this is 2nd innings
        # Only reliably detect 2nd innings if the 1st innings team is all out
        # or shows 10 wickets. Default to 1 to avoid false positives mid-game.
        first_inn_str = home_score_str if home_team_id == batted_first else away_score_str
        first_all_out = ('all out' in first_inn_str.lower() or
                         bool(re.search(r'[-/]10[^0-9]', first_inn_str)))
        innings = 2 if first_all_out else 1

    score, wickets, overs = 0, 0, 0.0
    sm = re.match(r'(\d+)[-/](\d+)\s*\(?([\.\d]+)?\)?', batting_score_str.strip())
    if sm:
        score   = int(sm.group(1))
        wickets = int(sm.group(2))
        overs   = float(sm.group(3)) if sm.group(3) else 0.0

    rr = round(score / overs, 2) if overs > 0 else 0.0

    # Preserve batter/bowler details from last RV fetch if available
    cached = _rv_cache.get("last_state") or {}

    state = {
        "battingTeamName": batting_team,
        "bowlingTeamName": bowling_team,
        "innings":  innings,
        "score":    score,
        "wickets":  wickets,
        "overs":    overs,
        "batter1":  cached.get("batter1", {"name":"—","runs":0,"balls":0,"onStrike":True}),
        "batter2":  cached.get("batter2", {"name":"—","runs":0,"balls":0,"onStrike":False}),
        "bowler":   cached.get("bowler",  {"name":"—","overs":"0","runs":0,"wickets":0}),
        "statusText":     f"RR: {rr}",
        "targetRuns":     0,
        "runsRequired":   0,
        "ballsRemaining": 0,
    }
    print(f"  Widget: {batting_team} {score}-{wickets} ({overs} ov)")
    return state, pc_id

def get_live_state(club_id, home_name, away_name):
    """
    Main entry: returns overlay state dict or None.
    - If pinned via match_url: polls ResultsVault directly every 20s (full data)
    - Otherwise: polls widget JSON for score, RV every 60s for batter/bowler names
    """
    global _rv_cache

    # Only reset cache if club_id changes AND we are not in pinned mode
    pinned = _rv_cache.get("pinned", False)
    if not pinned and _rv_cache["club_id"] != club_id:
        _rv_cache = {"rv_id": None, "pc_id": None, "club_id": club_id,
                     "last_rv_poll": 0, "last_state": None, "pinned": False}

    # If we have a pinned RV match ID (from match_url), use it directly
    if _rv_cache.get("rv_id") and _rv_cache.get("pinned"):
        now = time.time()
        # Poll RV every 20s when pinned to a specific match
        if (now - _rv_cache.get("last_rv_poll", 0)) > 20:
            data = fetch_rv_live(_rv_cache["rv_id"])
            if data:
                state = parse_rv_to_state(data, home_name, away_name)
                if state:
                    _rv_cache["last_rv_poll"] = now
                    _rv_cache["last_state"]   = state
                    return state
        elif _rv_cache.get("last_state"):
            return _rv_cache["last_state"]
        return None

    # Fetch widget JSON — rate limited to once every 25s
    global _widget_last_poll, _widget_last_data
    now_w = time.time()
    if now_w - _widget_last_poll > 25:
        url = (f"https://www.play-cricket.com/embed_widget/live_scorer_widgets"
               f"?club_id={club_id}&days=0")
        try:
            req = urllib.request.Request(url, headers={
                "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
                "Accept":     "application/json, text/html, */*",
                "Referer":    "https://www.play-cricket.com/",
            })
            with urllib.request.urlopen(req, timeout=10) as resp:
                raw = resp.read().decode("utf-8", errors="replace")
            _widget_last_poll = now_w
            _widget_last_data = raw
        except Exception as e:
            print(f"  ✗  Widget error: {e}")
            if not _widget_last_data:
                return None
            raw = _widget_last_data  # use cached on error
    else:
        if not _widget_last_data:
            return None
        raw = _widget_last_data

    # Parse widget response
    widget_data = None
    try:
        widget_data = json.loads(raw)
    except (json.JSONDecodeError, ValueError):
        pass

    if widget_data:
        pinned_pc_id = _rv_cache.get("pc_id") if _rv_cache.get("pinned") else None
        state, pc_id = parse_widget_json_score(widget_data, home_name, away_name, pinned_pc_id)
        if not state:
            return None

        # Map PC match ID to RV match ID if not cached
        if pc_id and _rv_cache.get("pc_id") != pc_id:
            rv_id = fetch_rv_mapping(pc_id)
            if rv_id:
                _rv_cache["rv_id"]  = rv_id
                _rv_cache["pc_id"]  = pc_id
                _rv_cache["last_rv_poll"] = 0  # force RV refresh

        # Fetch RV details every 60 seconds for batter/bowler names
        now = time.time()
        rv_id = _rv_cache.get("rv_id")
        if rv_id and (now - _rv_cache.get("last_rv_poll", 0)) > 60:
            rv_data = fetch_rv_live(rv_id)
            if rv_data:
                rv_state = parse_rv_to_state(rv_data, home_name, away_name)
                if rv_state:
                    # Overlay score with widget data (more current)
                    # but take batter/bowler names from RV
                    state["batter1"] = rv_state["batter1"]
                    state["batter2"] = rv_state["batter2"]
                    state["bowler"]  = rv_state["bowler"]
                    _rv_cache["last_state"]   = rv_state
                    _rv_cache["last_rv_poll"] = now
        return state
    else:
        # Fallback: try HTML parsing for match IDs
        rv_map = re.findall(r'/rv/mappings/4/12/(\d+)/', raw)
        if rv_map:
            pc_id = int(rv_map[0])
            if _rv_cache.get("pc_id") != pc_id:
                rv_id = fetch_rv_mapping(pc_id)
                if rv_id:
                    _rv_cache.update({"rv_id": rv_id, "pc_id": pc_id, "last_rv_poll": 0})
            rv_id = _rv_cache.get("rv_id")
            if rv_id:
                data = fetch_rv_live(rv_id)
                if data:
                    return parse_rv_to_state(data, home_name, away_name)
        return None

def parse_rv_to_state(data, home_name, away_name):
    """Convert ResultsVault match JSON into overlay state format."""
    teams = data.get("MatchTeams", [])
    if not teams:
        return None
    max_overs = int(data.get("MatchConfig", {}).get("max_overs", 50) or 50)

    batting_team = None
    current_inn  = None
    for team in teams:
        for inn in team.get("Innings", []):
            if inn.get("status") == 0:
                batting_team = team
                current_inn  = inn
    if not batting_team:
        for team in teams:
            inns = team.get("Innings", [])
            if inns:
                batting_team = team
                current_inn  = inns[-1]
    if not batting_team or not current_inn:
        return None

    bowling_team = next((t for t in teams if t is not batting_team), teams[-1])
    runs    = int(current_inn.get("runs",         0) or 0)
    wickets = int(current_inn.get("wickets",      0) or 0)
    overs   = float(current_inn.get("overs_bowled", 0) or 0)
    inn_num = int(current_inn.get("innings_number", 1) or 1)

    perfs   = current_inn.get("PlayerPerfs", [])
    batting = [p for p in perfs if "Batting" in p.get("__type","")]
    bowling = [p for p in perfs if "Bowling" in p.get("__type","")]

    at_crease = sorted(
        [b for b in batting if b.get("dismissal_id") == 1],
        key=lambda b: b.get("number", 99))
    b1 = at_crease[0] if len(at_crease) > 0 else {}
    b2 = at_crease[1] if len(at_crease) > 1 else {}

    # Current bowler — use highest over number, but only if innings in progress
    cb = {}
    if bowling and current_inn.get("status") == 0:
        cb = max(bowling, key=lambda b: b.get("number", 0))

    rr = round(runs / overs, 2) if overs > 0 else 0.0

    # Diagnostic
    b1n = b1.get("player_name","—")
    b2n = b2.get("player_name","—")
    cbn = cb.get("player_name","—")
    inn_status = "live" if current_inn.get("status")==0 else "complete"
    print(f"  RV [{inn_status}] inn{inn_num}: {runs}-{wickets} ({overs}ov) | "
          f"{b1n} {b1.get('runs',0)} / {b2n} {b2.get('runs',0)} | bowl: {cbn}")
    state = {
        "battingTeamName": batting_team.get("team_name", home_name),
        "bowlingTeamName": bowling_team.get("team_name", away_name),
        "innings": inn_num, "score": runs, "wickets": wickets, "overs": overs,
        "batter1": {"name": b1.get("player_name","—"), "runs": int(b1.get("runs",0) or 0),
                    "balls": int(b1.get("balls",0) or 0), "onStrike": True},
        "batter2": {"name": b2.get("player_name","—"), "runs": int(b2.get("runs",0) or 0),
                    "balls": int(b2.get("balls",0) or 0), "onStrike": False},
        "bowler":  {"name": cb.get("player_name","—"), "overs": str(cb.get("overs","0")),
                    "runs": int(cb.get("runs",0) or 0), "wickets": int(cb.get("wickets",0) or 0)},
        "statusText": f"RR: {rr}", "targetRuns": 0, "runsRequired": 0, "ballsRemaining": 0,
    }
    if inn_num == 2:
        for team in teams:
            for inn in team.get("Innings", []):
                if int(inn.get("innings_number",0) or 0) == 1 and inn.get("status") == 1:
                    target = int(inn.get("runs",0) or 0) + 1
                    state["targetRuns"]     = target
                    state["runsRequired"]   = max(0, target - runs)
                    state["ballsRemaining"] = round(max(0, max_overs - overs) * 6)
                    state["statusText"]     = f"Need {state['runsRequired']}"
    print(f"  RV: {state['battingTeamName']} {runs}-{wickets} ({overs} ov) | "
          f"{b1.get('player_name','—')} {b1.get('runs',0)} "
          f"{b2.get('player_name','—')} {b2.get('runs',0)}")
    return state

_prev_state    = {"score": None, "wickets": None, "overs": None}
_event_buffer  = []   # queued boundary/wicket events for overlay to consume

def buffer_pcs_events(state):
    """Detect boundaries/wickets from PCS state and buffer them for the overlay."""
    global _event_buffer
    score   = state.get("score", 0)
    wickets = state.get("wickets", 0)
    prev_s  = _prev_state["score"]
    prev_w  = _prev_state["wickets"]
    if prev_s is None:
        return
    delta = score - prev_s
    dw    = wickets - prev_w
    if dw > 0:
        _event_buffer.append({"type": "wicket", "score": score, "wickets": wickets})
        # Record for match report
        b1 = state.get("batter1",{}); b2 = state.get("batter2",{})
        out_name = b1.get("name","") if b1.get("onStrike") else b2.get("name","")
        match_log_event("wicket", f"Wicket: {score}-{wickets} ({state.get('overs',0)} ov)")
        _match_log["fall_of_wickets"].append({
            "batter": out_name, "score": f"{score}-{wickets}",
            "over": state.get("overs",0), "howout": ""})
    # NOTE: boundaries (4/6) are detected client-side in the overlay from the ball-by-ball
    # ticker, which correctly distinguishes real boundaries from 4 byes / leg-byes / wide+4
    # (all of which also move the score by 4). A score delta alone cannot tell them apart,
    # so we do NOT emit four/six replay events here. We only note a probable six for the
    # match report (low-stakes; the report is AI-written prose).
    if delta == 6 and dw == 0:
        match_log_event("six", f"Six — {score}-{wickets}")
    # Update prev state HERE so it always advances, regardless of commentary toggle
    _prev_state.update({"score": score, "wickets": wickets,
                        "overs": state.get("overs", 0.0)})
    # Cap buffer size
    if len(_event_buffer) > 20:
        _event_buffer = _event_buffer[-20:]

def check_commentary_trigger(state):
    """
    Triggers AI commentary generation after each completed over and on wickets.
    Runs commentary generation in a background thread.
    """
    score   = state.get("score", 0)
    wickets = state.get("wickets", 0)
    overs   = state.get("overs", 0.0)

    prev_score   = _prev_state["score"]
    prev_wickets = _prev_state["wickets"]
    prev_overs   = _prev_state["overs"]

    if prev_score is None:
        _prev_state.update({"score": score, "wickets": wickets, "overs": overs})
        return

    current_over  = int(overs)
    previous_over = int(prev_overs) if prev_overs else 0
    d_score   = score   - prev_score
    d_wickets = wickets - prev_wickets

    if d_wickets > 0:
        record_event(f"Wicket — score {score}-{wickets}")
    if d_score == 4:
        record_event(f"FOUR — {score}-{wickets}")
    if d_score == 6:
        record_event(f"SIX — {score}-{wickets}")

    should_trigger = (d_wickets > 0 or (current_over > previous_over and current_over > 0))
    if should_trigger and _commentary["last_over"] != current_over:
        _commentary["last_over"] = current_over
        cfg = load_state()
        if cfg.get("graphics_commentary") and cfg.get("anthropic_api_key","").strip():
            threading.Thread(
                target=generate_commentary,
                args=(dict(state), list(_innings_events)),
                daemon=True).start()

    # NOTE: _prev_state is now owned/updated by buffer_pcs_events (runs every poll)


# ── State file ────────────────────────────────────────────────
PORT       = 5000
# Always resolve state file relative to server.py regardless of launch directory
STATE_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "match_state.json")
MAX_CLIPS  = 100

DEFAULT_STATE = {
    "home_team":               "Home CC",
    "home_abbrev":             "HOME",
    "away_abbrev":             "",
    "away_team":               "Opposition CC",
    "home_colour":             "#1a3a5c",
    "away_colour":             "#7b2d2d",
    "demo_mode":               True,
    "api_token":               "",
    "match_id":                "",
    "match_url":               "",
    "rv_match_id":            "",
    "site_id":                 "",
    "max_overs":               50,
    "drinks_over":             25,
    "poll_interval":           20,
    "match_notes":             "",
    "replay_motto":            "",
    "graphics_fow":            True,
    "graphics_partnership":    True,
    "graphics_lineup":         True,
    "graphics_boundary_flash": True,
    "graphics_milestones":     True,
    "graphics_innings_summary":True,
    "graphics_commentary":     False,
    "graphics_commentary_over":False,
    "graphics_player_card":     False,
    "replay_enabled":          True,
    "replay_on_fifty":         False,
    "obs_host":                "localhost",
    "obs_port":                4455,
    "obs_password":            "CHANGE_ME",
    "obs_main_scene":          "Main",
    "camera_rtsp_url":         "",
    "obs_camera_name":         "Cricket Camera",
    "obs_replay_scene":        "Replay",
    "replay_folder":           "",
    "replay_duration":         18,
    "max_clips":               500,
    "youtube_title_template":  "LIVE: {home} vs {away}",
    "weather_api_key":         "",
    "logos_folder":           "",
    "headshots_folder":       "",
    "roster":                 {},
    "socials_folder":         "",
    "drinks_over":            25,
    "home_club_id":           "",
    "ground_filter":          "",
    "away_club_id":           "",
}

_last_good_state = None   # cached last successful load, used if the file is mid-write/corrupt

# Keys whose values must never be sent to a browser. GET /state replaces a stored value
# with SECRET_SENTINEL; POST /state ignores fields whose value IS the sentinel, so the
# control panel can round-trip its form without wiping stored secrets. "" still clears.
SECRET_KEYS = ("anthropic_api_key", "playcricket_api_key", "api_token",
               "weather_api_key", "obs_password")
SECRET_SENTINEL = "••••••••"

def load_state():
    global _last_good_state
    if os.path.exists(STATE_FILE):
        try:
            with open(STATE_FILE) as f:
                loaded = json.load(f)
            if not isinstance(loaded, dict):
                raise ValueError(f"state file holds {type(loaded).__name__}, expected object")
            state = {**DEFAULT_STATE, **loaded}
            _last_good_state = state
            return state
        except (json.JSONDecodeError, OSError, ValueError) as e:
            # File was mid-write or corrupt — don't crash /state. Use the last good copy
            # if we have one, otherwise fall back to defaults.
            print(f"  ⚠  state read failed ({e}); using last-good")
            return dict(_last_good_state) if _last_good_state else DEFAULT_STATE.copy()
    return DEFAULT_STATE.copy()

def save_state(s):
    # Atomic write: a reader (overlay polling /state) must never see a half-written file.
    # Write to a temp file on the same directory, then os.replace (atomic on POSIX & Windows).
    global _last_good_state
    tmp = STATE_FILE + ".tmp"
    with open(tmp, "w") as f:
        json.dump(s, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, STATE_FILE)
    _last_good_state = dict(s)


def _seed_state_from_config():
    """Copy config.ini values into match_state.json for fields still at empty/default.

    Runs once at startup so operators who filled in config.ini (following the
    example file) don't have to re-enter everything in the control panel.
    Panel edits take precedence — once a field has been changed from its default,
    config.ini no longer overwrites it.
    """
    import configparser as _cp
    cfg_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini")
    if not os.path.exists(cfg_path):
        return
    cp = _cp.ConfigParser()
    cp.read(cfg_path, encoding="utf-8")

    # (ini_section, ini_key, state_field, type_cast)
    MAPPING = [
        ("Club",    "name",              "home_team",             str),
        ("Club",    "abbreviation",      "home_abbrev",           str),
        ("Club",    "home_colour",       "home_colour",           str),
        ("Club",    "playcricket_id",    "home_club_id",          str),
        ("Club",    "motto",             "replay_motto",          str),
        ("API",     "playcricket_key",   "playcricket_api_key",   str),
        ("API",     "anthropic_key",     "anthropic_api_key",     str),
        ("Scoring", "pcs_output_folder", "pcs_output_folder",     str),
        ("Scoring", "logos_folder",      "logos_folder",          str),
        ("Scoring", "ground_filter",     "ground_filter",         str),
        ("OBS",     "obs_password",      "obs_password",          str),
        ("OBS",     "replay_folder",     "replay_folder",         str),
        ("Stream",  "youtube_title",     "youtube_title_template", str),
        ("Stream",  "max_overs",         "max_overs",             int),
    ]

    state = load_state()
    seeded = []
    for section, ini_key, field, cast in MAPPING:
        try:
            raw = cp.get(section, ini_key).strip()
        except (_cp.NoSectionError, _cp.NoOptionError):
            continue
        if not raw:
            continue
        current = state.get(field)
        default = DEFAULT_STATE.get(field)
        if current == default or current == "" or current is None:
            try:
                state[field] = cast(raw)
                seeded.append(field)
            except (ValueError, TypeError):
                pass

    if seeded:
        save_state(state)
        visible   = [f for f in seeded if f not in SECRET_KEYS]
        n_secrets = len(seeded) - len(visible)
        parts = visible + ([f"{n_secrets} secret key(s)"] if n_secrets else [])
        print(f"  ✔  Seeded from config.ini: {', '.join(parts)}")


# ── PCS Pro local scoreboard reader ──────────────────────────
# PCS Pro can output a JSON file locally on every ball.
# This is far more reliable than any API — instant, no auth needed.
#
# Setup instructions for the scorer:
#   1. Open PCS Pro
#   2. Go to Tools → Configuration → Scoreboard
#   3. Set Output Folder to any convenient path (note it down)
#   4. Set Template File to: scoreboard.template
#      (copy this file from the stream folder into PCS Pro's Templates folder)
#   5. Tick "Enable Scoreboard Output"
#
# The output file is written every ball to:
#   {output_folder}\scoreboard-output.json  (or similar filename)

PCS_OUTPUT_FILENAMES = [
    "nvplay-scoreboard1.xml",
    "nvplay-scoreboard.xml",
    "scoreboard-output.json",
    "scoreboard-output.xml",
    "scoreboard.json",
    "scoreboard.xml",
    "pcs-output.json",
    "output.json",
    "live.json",
]

_pcs_last_mtime = 0
_pcs_last_state = None
_innings_latch  = 1   # latched innings number (see parse_pcs_json); survives the winning runs

def find_pcs_output_file(folder):
    """Find the PCS output file in the configured folder."""
    if not folder or not os.path.isdir(folder):
        return None
    # Try known filenames
    for fname in PCS_OUTPUT_FILENAMES:
        path = os.path.join(folder, fname)
        if os.path.exists(path):
            return path
    # Try any .json or .xml file modified in the last 10 minutes
    import glob as _glob
    candidates = (_glob.glob(os.path.join(folder, "*.json")) +
                  _glob.glob(os.path.join(folder, "*.xml")))
    if candidates:
        newest = max(candidates, key=os.path.getmtime)
        if time.time() - os.path.getmtime(newest) < 600:
            return newest
    return None

def read_pcs_file(folder):
    """
    Read the PCS Pro scoreboard output file and return an overlay state dict.
    Returns None if file not found, unreadable, or stale (>5 min old).
    """
    global _pcs_last_mtime, _pcs_last_state

    path = find_pcs_output_file(folder)
    if not path:
        return None

    try:
        mtime = os.path.getmtime(path)

        # Only re-parse if file has changed
        if mtime == _pcs_last_mtime and _pcs_last_state:
            return _pcs_last_state

        with open(path, encoding="utf-8", errors="replace") as f:
            raw = f.read().strip()

        if not raw:
            # File caught mid-write (empty) — hold the last good frame, don't blank the overlay
            return _pcs_last_state

        # NV Play writes JSON via template even with .xml extension — read as JSON
        if path.endswith(".xml") or path.endswith(".json"):
            try:
                data = json.loads(raw)
            except json.JSONDecodeError:
                # Fallback: try XML if JSON fails
                try:
                    import xml.etree.ElementTree as ET
                    root = ET.fromstring(raw)
                    data = {child.tag.lower(): (child.text or "").strip() for child in root}
                except Exception:
                    return _pcs_last_state
        else:
            data = json.loads(raw)
        state = parse_pcs_json(data)
        if state:
            _pcs_last_mtime = mtime
            _pcs_last_state = state
            print(f"  PCS: {state['battingTeamName']} "
                  f"{state['score']}-{state['wickets']} ({state['overs']} ov) | "
                  f"{state['batter1']['name']} {state['batter1']['runs']} "
                  f"/ {state['batter2']['name']} {state['batter2']['runs']}")
        return state

    except (json.JSONDecodeError, KeyError, OSError) as e:
        print(f"  ✗  PCS file read error: {e}")
        # Transient read error mid-match — hold the last good frame rather than blanking.
        return _pcs_last_state

def generate_match_report(report_type="report"):
    """Generate an AI match report or social post from the match log.
    report_type: 'report' (full written report) | 'social' (short post).
    Returns dict {ok, text, error}.
    """
    cfg = load_state()
    api_key = cfg.get("anthropic_api_key","").strip() or os.environ.get("ANTHROPIC_API_KEY","").strip()
    if not api_key:
        return {"ok": False, "error": "No Anthropic API key set"}
    try:
        import anthropic
    except ImportError:
        return {"ok": False, "error": "anthropic package not installed (pip install anthropic)"}

    # Build a factual match summary from the log + current state.
    # Under the threaded server, live ball events mutate _match_log while we read it here.
    # Take a defensive snapshot (retry if a concurrent append trips iteration) so a report
    # request can never raise — and never blocks the live thread.
    snap = {"innings": {}, "fall_of_wickets": [], "milestones": [], "events": []}
    for _attempt in range(4):
        try:
            snap = {
                "innings": {k: dict(v) for k, v in _match_log.get("innings", {}).items()},
                "fall_of_wickets": list(_match_log.get("fall_of_wickets", [])),
                "milestones": list(_match_log.get("milestones", [])),
                "events": list(_match_log.get("events", [])),
            }
            break
        except RuntimeError:
            time.sleep(0.02)

    st = _pcs_last_state or {}
    home = cfg.get("home_team","") or cfg.get("name","Home")
    away = cfg.get("away_team","Opposition")
    comp = cfg.get("competition","")
    lines = [f"Match: {home} v {away}" + (f" ({comp})" if comp else "")]
    for inn_no in sorted(snap["innings"].keys()):
        r = snap["innings"][inn_no]
        lines.append(
            f"Innings {inn_no}: {r.get('batting_team','?')} "
            f"{r.get('score',0)}-{r.get('wickets',0)} ({r.get('overs',0)} overs)")
    if snap["fall_of_wickets"]:
        lines.append("Wickets:")
        for w in snap["fall_of_wickets"][:12]:
            lines.append(f"  {w.get('batter','?')} {w.get('howout','')} "
                         f"at {w.get('score','?')}")
    if snap["milestones"]:
        lines.append("Milestones: " + "; ".join(
            f"{m.get('batter','?')} {m.get('milestone','')}" for m in snap["milestones"][:8]))
    if snap["events"]:
        recent = [e["detail"] for e in snap["events"][-25:]]
        lines.append("Key moments: " + " | ".join(recent))
    summary = "\n".join(lines)

    if report_type == "social":
        prompt = (
            f"You are running the social media for a village cricket club, {home}. "
            "Write a short, upbeat match-result post for Twitter/Instagram "
            "based on the facts below. Max 60 words. Use the club spirit — warm, proud, a little wit. "
            "You may use up to 3 tasteful hashtags and at most 1 emoji. Do NOT invent facts not present below.\n\n"
            f"{summary}\n\nPost:"
        )
        max_tok = 200
    else:
        prompt = (
            f"You are a cricket correspondent writing a match report for {home}'s club website. "
            "Write an engaging, accurate report of 150-250 words "
            "based ONLY on the facts below. Cover the key passages of play, standout performers, and the result. "
            "Warm local-paper tone, not hyperbole. Do NOT invent statistics or names not present.\n\n"
            f"{summary}\n\nMatch report:"
        )
        max_tok = 600

    try:
        client = anthropic.Anthropic(api_key=api_key)
        msg = client.messages.create(
            model="claude-haiku-4-5", max_tokens=max_tok,
            messages=[{"role":"user","content":prompt}])
        text = msg.content[0].text.strip()
        return {"ok": True, "text": text, "summary": summary}
    except Exception as e:
        return {"ok": False, "error": str(e)}

def _team_key(name):
    """Map a team name to a photo-folder key. Youth/age-group sides (U11, Under-15, Colts,
    Juniors…) ALL map to a single 'youth' key so their posts only ever use the club stock
    photos in socials/youth — never images of the children. Senior sides map to 1st/2nd/3rd."""
    import re as _re
    s = (name or "").lower()
    if (_re.search(r"\bu\s?-?\d{1,2}\b", s) or _re.search(r"\bunder[\s-]?\d{1,2}\b", s)
            or _re.search(r"\b(colts?|juniors?|junior|youth|jnr|girls|boys|kwik)\b", s)):
        return "youth"
    m = _re.search(r"\b([1-9])\s*(?:st|nd|rd|th)\b", s)
    if m:
        n = m.group(1)
        return n + {"1": "st", "2": "nd", "3": "rd"}.get(n, "th")
    for word, key in (("first", "1st"), ("second", "2nd"), ("third", "3rd"),
                      ("fourth", "4th"), ("fifth", "5th")):
        if _re.search(r"\b" + word + r"\b", s):
            return key
    return ""

def _socials_root():
    cfg = load_state()
    folder = cfg.get("socials_folder", "").strip()
    if not folder:
        folder = os.path.join(os.path.dirname(os.path.abspath(__file__)), "socials")
    return os.path.expanduser(folder)

def _socials_dir(team_key=""):
    """Folder to draw backdrop photos from: a per-team subfolder (e.g. socials/2nd) when it
    exists AND has photos, otherwise the root socials folder. Optional — clubs that don't
    make subfolders just keep using the root for every team."""
    root = _socials_root()
    if team_key:
        sub = os.path.join(root, team_key)
        if os.path.isdir(sub):
            try:
                if any(f.lower().endswith((".jpg", ".jpeg", ".png", ".webp"))
                       for f in os.listdir(sub)):
                    return sub
            except OSError:
                pass
    return root

def list_social_photos(team_key=""):
    """Photo filenames available as post backdrops, from the team subfolder if present."""
    folder = _socials_dir(team_key)
    if not os.path.isdir(folder):
        return []
    exts = (".jpg", ".jpeg", ".png", ".webp")
    try:
        return sorted([f for f in os.listdir(folder) if f.lower().endswith(exts)])
    except OSError:
        return []


def _norm_name_key(s):
    """Normalise a player name or filename to a comparison key: lowercase, letters
    and digits only. So 'P SMITH', 'p.smith', 'P_Smith' and 'psmith' all map to 'psmith'.
    This lets headshot/stat lookups tolerate spaces, dots, underscores and case."""
    import re as _re
    return _re.sub(r"[^a-z0-9]", "", (s or "").lower())


def roster_name(number):
    """Resolve a shirt number to a full player name via the squad roster in state.
    This is how brothers (same surname) are told apart: the scorebar may show only 'SMITH'
    for all three Smiths, but each has a distinct shirt number that maps to his full name.
    Returns '' if there's no roster entry."""
    num = str(number or "").strip()
    if not num:
        return ""
    roster = load_state().get("roster") or {}
    return str(roster.get(num, "")).strip()


def resolve_player(name, num):
    """Unified player resolution for BOTH teams — one method, no home/away branching:
        IF the shirt number maps to a roster player whose surname matches the scorebar surname
           → use that full name (pins the exact brother)
        ELSE → fall back to the scorebar name (surname matching against the season pool).
    The surname cross-check is what makes a single 'if number else surname' path safe: an away
    player's number can't resolve to a home squad member, because the surnames won't match.
    Returns (lookup_name, matched_by_number)."""
    cand = roster_name(num)
    if cand:
        cs = _name_keys(cand)["surname"]
        if cs and cs == _name_keys(name)["surname"]:
            return cand, True
    return name, False


def _name_keys(s):
    """Tiered match keys for a player name, tolerant of differing initials/spacing/dots/case:
        full     – every alphanumeric run joined ('K J JONES' -> 'kjjones', 'K Jones' -> 'kjones')
        initsur  – first initial + surname ('K J JONES', 'K Jones' and 'Kevin Jones' -> 'kjones';
                   'P SMITH' and 'p.smith' -> 'psmith')
        surname  – last token only ('jones', 'walker', 'smith')
    Matching tries full, then initsur, then surname (the last only when unambiguous), which
    lets 'WALKER' find 'j.walker', and 'K J JONES' find PlayCricket's 'K Jones'."""
    import re as _re
    toks = _re.sub(r"[^a-z0-9]+", " ", (s or "").lower()).split()
    if not toks:
        return {"full": "", "initsur": "", "surname": ""}
    surname = toks[-1]
    return {
        "full":    "".join(toks),
        "initsur": (toks[0][:1] + surname) if len(toks) >= 2 else surname,
        "surname": surname,
    }


def _pc_parse_int(v):
    """Parse PlayCricket numeric strings; return None if not a number."""
    try:
        return int(str(v).strip())
    except (ValueError, TypeError):
        return None


def _aggregate_season_bat(match_details_list, keep_clubs=None):
    """Pure aggregation: given a list of PlayCricket match_detail.json payloads, build a
    season batting-stats lookup. Returned dict maps several name keys to the same record so
    the overlay's short name ('P SMITH') can match PlayCricket's full name ('Peter Smith'):
      - full normalised name        ('petersmith')
      - first-initial + surname      ('psmith')   ← matches NV Play short names
      - surname only                 ('smith')     ← only when that surname is unique
    Each record: {name, inn, avg, hs}. avg counts not-outs correctly; hs marks not-out with *.

    keep_clubs: optional list of lowercased club-name fragments (e.g. ['home','opposition']).
    When set, only innings whose team_batting_name contains one of them are counted. Every
    scorecard lists BOTH teams' batsmen, so without this the pool balloons to hundreds of
    players (every opponent ever faced) and common surnames stop being unique. Falls back to
    counting an innings if it carries no team name, so it can never silently drop data."""
    acc = {}   # normfull -> raw accumulator
    for det in match_details_list:
        try:
            md = (det.get("match_details") or [{}])[0]
            innings = md.get("innings", []) or []
        except AttributeError:
            continue
        for inn in innings:
            if keep_clubs:
                tname = (inn.get("team_batting_name") or "").lower()
                if tname and not any(frag in tname for frag in keep_clubs):
                    continue                                   # other club's innings → ignore
            for bat in inn.get("bat", []) or []:
                nm  = (bat.get("batsman_name") or "").strip()
                if not nm:
                    continue
                how = (bat.get("how_out") or "").strip().lower()
                if how in ("", "did not bat", "dnb", "tdnb", "did_not_bat"):
                    continue                                   # didn't bat → not an innings
                runs = _pc_parse_int(bat.get("runs"))
                if runs is None:
                    continue                                   # no score recorded → skip
                key = _norm_name_key(nm)
                rec = acc.setdefault(key, {"name": nm, "inn": 0, "no": 0, "runs": 0,
                                           "hs": -1, "hs_no": False})
                rec["inn"]  += 1
                rec["runs"] += runs
                not_out = ("not out" in how) or how in ("no",) or ("retired not out" in how) \
                          or ("retired hurt" in how) or ("retired not" in how)
                if not_out:
                    rec["no"] += 1
                # high score: higher runs wins; tie broken in favour of a not-out
                if runs > rec["hs"] or (runs == rec["hs"] and not_out and not rec["hs_no"]):
                    rec["hs"], rec["hs_no"] = runs, not_out

    # Finalise records + build multi-key lookup
    finals = {}
    for key, r in acc.items():
        outs = r["inn"] - r["no"]
        avg  = round(r["runs"] / outs, 2) if outs > 0 else None
        finals[key] = {
            "name": r["name"],
            "inn":  str(r["inn"]),
            "runs": r["runs"],
            "avg":  (f"{avg:.2f}" if avg is not None else "—"),
            "hs":   (f"{r['hs']}*" if r["hs_no"] else str(r["hs"])) if r["hs"] >= 0 else "—",
        }

    # Build a multi-key lookup. Some players have more than one PlayCricket account, so the
    # same person can show up as two records (e.g. "Peter Smith" and "P Smith"). When several
    # records land on the same key, keep the one with the most innings — almost always their
    # main/regular account. Unique full-name keys for distinct people stay separate, so the
    # roster (shirt number → full name) still tells genuine brothers apart.
    surname_count = {}
    meta = {}
    key_cands = {}   # lookup key → candidate records (from full + initial-surname tiers)
    for key, rec in finals.items():
        toks = re.sub(r"[^A-Za-z ]", " ", rec["name"]).split()
        if not toks:
            continue
        full     = _norm_name_key(rec["name"])
        surname  = _norm_name_key(toks[-1])
        init_sur = _norm_name_key(toks[0][:1] + toks[-1]) if len(toks) >= 2 else surname
        meta[key] = (full, init_sur, surname)
        key_cands.setdefault(full, []).append(rec)
        if init_sur != full:
            key_cands.setdefault(init_sur, []).append(rec)
        surname_count[surname] = surname_count.get(surname, 0) + 1

    def _games(r):
        try:
            return int(r.get("inn", 0))
        except (TypeError, ValueError):
            return 0

    lookup = {k: max(recs, key=_games) for k, recs in key_cands.items()}

    # surname-only key only when exactly one player carries that surname (avoids brother mix-ups)
    for key, rec in finals.items():
        _, _, surname = meta.get(key, ("", "", ""))
        if surname and surname_count.get(surname) == 1:
            lookup.setdefault(surname, rec)
    return lookup


def _season_top_bowler(match_details_list, club_frag):
    """Season leading wicket-taker for one club. PlayCricket's per-innings bowling card
    ('bowl') belongs to the FIELDING side, not the innings' own team_batting_name — so a club's
    bowling figures live on the OTHER innings of the same match. For each match, find the
    innings that is this club's own batting innings, then credit the bowlers listed on the
    remaining innings (the side that bowled at them) to this club."""
    if not club_frag:
        return None
    acc = {}
    for det in match_details_list:
        try:
            md = (det.get("match_details") or [{}])[0]
            innings = md.get("innings", []) or []
        except AttributeError:
            continue
        if len(innings) < 2:
            continue
        for i, inn in enumerate(innings):
            tname = (inn.get("team_batting_name") or "").lower()
            if club_frag not in tname:
                continue
            for j, other in enumerate(innings):
                if j == i:
                    continue
                for bw in other.get("bowl", []) or []:
                    nm = (bw.get("bowler_name") or "").strip()
                    if not nm:
                        continue
                    wkts = _pc_parse_int(bw.get("wickets"))
                    runs = _pc_parse_int(bw.get("runs"))
                    if wkts is None or runs is None:
                        continue
                    key = _norm_name_key(nm)
                    rec = acc.setdefault(key, {"name": nm, "wkts": 0, "runs": 0})
                    rec["wkts"] += wkts
                    rec["runs"] += runs
    if not acc:
        return None
    best = max(acc.values(), key=lambda r: (r["wkts"], -r["runs"]))
    if best["wkts"] <= 0:
        return None
    avg = round(best["runs"] / best["wkts"], 1)
    return {"name": best["name"], "wkts": best["wkts"], "runs": best["runs"], "avg": f"{avg}"}


def _season_top_scorer(bat_lookup):
    """Pick the single highest season-runs record from a keep_clubs-filtered batting lookup.
    Lookup values are shared objects (several keys can point at the same record), so dedupe by
    identity first — same trick used elsewhere in this file for player counts."""
    seen = {}
    for rec in bat_lookup.values():
        seen[id(rec)] = rec
    if not seen:
        return None
    best = max(seen.values(), key=lambda r: r.get("runs", 0))
    if best.get("runs", 0) <= 0:
        return None
    return {"name": best["name"], "runs": best["runs"], "inn": best["inn"], "avg": best["avg"]}


# ── Season stats cache (PlayCricket has no per-player endpoint, so we aggregate match
#    scorecards ONCE per day and serve every batsman from the cached build) ──
_season_stats = {"date": None, "lookup": {}, "built": False, "building": False,
                 "matches_used": 0, "calls": 0, "error": None}
_season_stats_lock = threading.Lock()
_season_stats_last_action = None   # "cache" | "fresh" (transient, for status messages)
SEASON_STATS_CACHE_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                       "season_stats_cache.json")


def _pc_get_json(url, timeout=15):
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode())


def _pc_is_past_match(m):
    try:
        d = datetime.datetime.strptime(m.get("match_date", ""), "%d/%m/%Y").date()
        return d < datetime.date.today()
    except Exception:
        return False


def _pc_match_ids(api_key, site_id, season):
    """Return the set of completed match IDs for a club this season. One API call."""
    out = set()
    mlist = _pc_get_json(f"https://play-cricket.com/api/v2/matches.json"
                         f"?api_token={api_key}&site_id={site_id}&season={season}")
    for m in (mlist.get("matches", []) or []):
        if _pc_is_past_match(m) and m.get("id"):
            out.add(str(m["id"]))
    return out


def build_season_stats(force=False):
    """Aggregate season batting stats from PlayCricket scorecards, for BOTH the home club and
    today's opposition. PlayCricket has no per-player stats endpoint, so we gather each club's
    completed-match IDs, DEDUPE (the home-vs-away fixture appears in both clubs' lists, so it
    must only be counted once), fetch each unique scorecard once, and cache the result to disk
    for the day. One build per day, reused on restarts — gentle on the API."""
    global _season_stats
    cfg     = load_state()
    api_key = cfg.get("playcricket_api_key", "").strip()
    site_id = str(cfg.get("home_club_id", "")).strip()
    away_id = str(cfg.get("away_club_id", "") or "").strip()
    away_id = away_id if away_id.isdigit() else ""     # only query the API with a numeric club ID
    season  = str(datetime.date.today().year)
    today   = datetime.date.today().isoformat()

    if not api_key:
        with _season_stats_lock:
            _season_stats.update({"built": True, "building": False,
                                  "error": "No PlayCricket API key set"})
        return _season_stats

    with _season_stats_lock:
        # Reuse the in-memory build only if it's today's AND for the same opposition
        if (not force and _season_stats.get("built")
                and _season_stats.get("date") == today
                and _season_stats.get("away_id", "") == away_id):
            globals()["_season_stats_last_action"] = "cache"
            return _season_stats
        if _season_stats.get("building"):
            return _season_stats                # another thread is already building
        # On-disk cache (one real build per day per opponent, survives restarts)
        if not force:
            try:
                with open(SEASON_STATS_CACHE_FILE) as f:
                    disk = json.load(f)
                if (disk.get("date") == today and disk.get("lookup")
                        and disk.get("away_id", "") == away_id):
                    _season_stats = {**disk, "built": True, "building": False}
                    globals()["_season_stats_last_action"] = "cache"
                    return _season_stats
            except Exception:
                pass
        _season_stats["building"] = True
        _season_stats["error"]    = None

    err = None
    calls = 0
    ids = set()
    # 1) Home club's completed matches
    try:
        ids |= _pc_match_ids(api_key, site_id, season); calls += 1
    except Exception as e:
        err = f"home list failed: {e}"
    # 2) Opposition's completed matches (token isn't club-scoped, so this is allowed)
    away_ok = False
    if away_id:
        try:
            ids |= _pc_match_ids(api_key, away_id, season); calls += 1
            away_ok = True
        except Exception as e:
            err = (err + "; " if err else "") + f"opposition list failed: {e}"

    # 3) Fetch each UNIQUE scorecard once (the shared fixture is in both lists but fetched once)
    details = []
    used = 0
    for mid in sorted(ids)[:160]:                      # overall safety cap
        try:
            det = _pc_get_json(f"https://play-cricket.com/api/v2/match_detail.json"
                               f"?api_token={api_key}&match_id={mid}")
            calls += 1
            details.append(det)
            used += 1
            time.sleep(0.2)                            # be gentle on the API
        except Exception:
            continue

    # Keep only the two clubs we actually display (home + today's opposition), so the pool
    # stays small and surnames remain unique. Match on the first significant word of each name.
    def _first_word(s):
        ws = re.sub(r"[^a-z ]", " ", (s or "").lower()).split()
        return ws[0] if ws and len(ws[0]) > 2 else ""
    home_frag = _first_word(cfg.get("home_team","")) or "home"
    away_frag = _first_word(cfg.get("away_team",""))
    keep = [w for w in (home_frag, away_frag) if w]
    lookup = _aggregate_season_bat(details, keep_clubs=keep or None)

    # Pre-game "season form" panel: each team's own top scorer/wicket-taker, computed
    # separately per club (the combined lookup above mixes both teams' players together).
    top_scorers = {
        "home": _season_top_scorer(_aggregate_season_bat(details, keep_clubs=[home_frag])),
        "away": _season_top_scorer(_aggregate_season_bat(details, keep_clubs=[away_frag]))
                if away_frag else None,
    }
    top_bowlers = {
        "home": _season_top_bowler(details, home_frag),
        "away": _season_top_bowler(details, away_frag) if away_frag else None,
    }

    result = {"date": today, "away_id": away_id, "away_ok": away_ok, "lookup": lookup,
              "top_scorers": top_scorers, "top_bowlers": top_bowlers,
              "built": True, "building": False, "matches_used": used, "calls": calls, "error": err}
    with _season_stats_lock:
        _season_stats = result
    globals()["_season_stats_last_action"] = "fresh"
    try:
        with open(SEASON_STATS_CACHE_FILE, "w") as f:
            json.dump(result, f)
    except Exception:
        pass
    print(f"  📊  Season stats: {len(lookup)} player keys from {used} matches "
          f"({calls} API calls{', incl. opposition' if away_ok else ''})"
          f"{' — ' + err if err else ''}")
    return result


def lookup_season_stats(name):
    """Return {'avg','hs','inn'} for a player name from the cached season build, or None.
    Tries full name, then first-initial+surname, then surname — so the scorebar's 'K J JONES'
    matches PlayCricket's 'K Jones', and 'WALKER' matches 'J Walker' (when unambiguous)."""
    lk = _season_stats.get("lookup") or {}
    if not name or not lk:
        return None
    k = _name_keys(name)
    rec = lk.get(k["full"]) or lk.get(k["initsur"]) or lk.get(k["surname"])
    if rec:
        return {"avg": rec.get("avg", "—"), "hs": rec.get("hs", "—"), "inn": rec.get("inn", "—")}
    return None


def _club_fragment(cfg):
    """First word of our club name, lowercased — used to tell 'us' from the opponent."""
    base = (cfg.get("home_team") or cfg.get("name") or "Home").lower().split()
    return base[0] if base else "home"

def _is_our_team(name, cfg):
    return _club_fragment(cfg) in (name or "").lower()

def _short_name(nm, youth=False):
    """'Peter Smith' -> 'P SMITH'; 'Smith' -> 'SMITH'. Broadcast-style.
    For youth (youth=True) use a more discreet first-name + surname-initial form where a
    full first name is available ('Jack Smith' -> 'Jack S'), aligning with common junior
    safeguarding practice. Falls back to the standard form when only an initial is on record."""
    toks = (nm or "").split()
    if youth and len(toks) >= 2 and len(toks[0]) > 1:
        return f"{toks[0]} {toks[-1][:1]}".title()
    if len(toks) >= 2:
        return f"{toks[0][:1]} {toks[-1]}".upper()
    return (nm or "").upper()

def _pc_recent_matches(api_key, site_id, season, cfg, limit=12):
    """Recent COMPLETED matches for our club this season, newest first, for the picker."""
    mlist = _pc_get_json(f"https://play-cricket.com/api/v2/matches.json"
                         f"?api_token={api_key}&site_id={site_id}&season={season}")
    rows = []
    for m in (mlist.get("matches", []) or []):
        if not _pc_is_past_match(m) or not m.get("id"):
            continue
        home = m.get("home_club_name") or m.get("home_team_name") or ""
        away = m.get("away_club_name") or m.get("away_team_name") or ""
        opp  = away if _is_our_team(home, cfg) else home
        date = m.get("match_date", "")
        try:                       # dd/mm/yyyy → sortable
            d = datetime.datetime.strptime(date, "%d/%m/%Y").date()
        except ValueError:
            d = datetime.date.min
        rows.append({"id": str(m["id"]), "date": date, "sort": d.isoformat(),
                     "opponent": opp, "team": (m.get("home_team_name") if _is_our_team(home, cfg)
                                               else m.get("away_team_name")) or "",
                     "team_key": _team_key(m.get("home_team_name") if _is_our_team(home, cfg)
                                           else m.get("away_team_name")),
                     "label": f"{date} — v {opp}".strip(" —")})
    rows.sort(key=lambda r: r["sort"], reverse=True)
    return rows[:limit]

def build_match_facts_from_pc(match_id, team_key=""):
    """Pull one match's scorecard from PlayCricket and distil it into graphic facts —
    result line, both innings scores, and our top batter + bowler. Works for ANY match,
    streamed or not (away games included). Deterministic; no AI needed.
    team_key='youth' switches performer names to the discreet junior form."""
    youth = (team_key == "youth")
    cfg = load_state()
    api_key = (cfg.get("playcricket_api_key") or cfg.get("api_token") or "").strip()
    if not api_key:
        return {"ok": False, "error": "No PlayCricket API key set"}
    det = _pc_get_json(f"https://play-cricket.com/api/v2/match_detail.json"
                       f"?api_token={api_key}&match_id={match_id}")
    md  = (det.get("match_details") or [{}])[0]
    innings = md.get("innings", []) or []
    if len(innings) < 2:
        return {"ok": False, "error": "That match has no completed scorecard yet"}

    def _score_str(r, w, ov):
        return f"{r} all out ({ov})" if (w is not None and w >= 10) else f"{r}-{w} ({ov})"

    rows = []
    for inn in innings[:2]:
        team = inn.get("team_batting_name", "")
        r = _pc_parse_int(inn.get("runs")) or 0
        w = _pc_parse_int(inn.get("wickets"))
        ov = str(inn.get("overs", "") or "").strip()
        rows.append({"team": team, "r": r, "w": (w if w is not None else 0), "ov": ov, "inn": inn})

    (t1, t2) = rows[0], rows[1]
    # Result (relative to us). Winner by runs (defending) or wickets (chasing).
    if t2["r"] > t1["r"]:
        win_team, margin = t2["team"], f"{max(0, 10 - t2['w'])} WICKET" + ("S" if (10 - t2['w']) != 1 else "")
    elif t1["r"] > t2["r"]:
        diff = t1["r"] - t2["r"]
        win_team, margin = t1["team"], f"{diff} RUN" + ("S" if diff != 1 else "")
    else:
        win_team, margin = None, ""
    abbr = (cfg.get("abbreviation", "") or _club_fragment(cfg)[:5]).upper()
    if win_team is None:
        result = "MATCH TIED"
    elif _is_our_team(win_team, cfg):
        result = f"{abbr} WIN BY {margin}"
    else:
        result = f"{abbr} LOSE BY {margin}"

    # Our top batter (in our batting innings) and top bowler (in our bowling innings).
    top_bat, top_bowl = None, None
    for row in rows:
        inn = row["inn"]
        our_batting = _is_our_team(row["team"], cfg)
        if our_batting:
            for b in inn.get("bat", []) or []:
                runs = _pc_parse_int(b.get("runs"))
                how  = (b.get("how_out") or "").strip().lower()
                if runs is None or how in ("", "did not bat", "dnb", "tdnb", "did_not_bat"):
                    continue
                no = ("not out" in how) or how == "no" or "retired not" in how
                if not top_bat or runs > top_bat["runs"]:
                    top_bat = {"name": b.get("batsman_name", ""), "runs": runs,
                               "balls": _pc_parse_int(b.get("balls")), "no": no}
        else:
            # opponent batting → our bowlers are this innings' bowl[]
            for bw in inn.get("bowl", []) or []:
                wk = _pc_parse_int(bw.get("wickets")) or 0
                rn = _pc_parse_int(bw.get("runs"))
                rn = rn if rn is not None else 999
                if not top_bowl or wk > top_bowl["w"] or (wk == top_bowl["w"] and rn < top_bowl["r"]):
                    top_bowl = {"name": bw.get("bowler_name", ""), "w": wk, "r": rn,
                                "o": str(bw.get("overs", "") or "").strip()}

    perf1 = perf2 = ""
    if top_bat:
        b = f"{_short_name(top_bat['name'], youth)} {top_bat['runs']}{'*' if top_bat['no'] else ''}"
        if top_bat.get("balls"):
            b += f" ({top_bat['balls']})"
        perf1 = b
    if top_bowl and top_bowl["w"] > 0:
        perf2 = f"{_short_name(top_bowl['name'], youth)} {top_bowl['w']}-{top_bowl['r']}"

    return {"ok": True, "result": result,
            "team1_name": t1["team"].upper(), "team1_score": _score_str(t1["r"], t1["w"], t1["ov"]),
            "team2_name": t2["team"].upper(), "team2_score": _score_str(t2["r"], t2["w"], t2["ov"]),
            "performer1": perf1, "performer2": perf2,
            "competition": md.get("competition_name", "") or cfg.get("competition", ""),
            "caption": ""}

def ai_caption_for_facts(facts):
    """Optional: turn computed facts into an upbeat IG caption with Claude. Falls back to a
    simple template if there's no key, so away-game posts still work offline."""
    cfg = load_state()
    api_key = cfg.get("anthropic_api_key", "").strip()
    base = facts.get("result", "").title()
    fallback = (f"{base}! {facts.get('team1_name','').title()} {facts.get('team1_score','')}, "
                f"{facts.get('team2_name','').title()} {facts.get('team2_score','')}. "
                f"#cricket #villagecricket").strip()
    if not api_key:
        return fallback
    try:
        import anthropic
        prompt = ("Write a short upbeat Instagram caption (max 45 words, up to 3 hashtags, "
                  "at most 1 emoji) for this cricket result. Return only the caption.\n\n"
                  f"{facts.get('result','')}\n{facts.get('team1_name','')} {facts.get('team1_score','')}\n"
                  f"{facts.get('team2_name','')} {facts.get('team2_score','')}\n"
                  f"Top batter: {facts.get('performer1','')}\nTop bowler: {facts.get('performer2','')}")
        client = anthropic.Anthropic(api_key=api_key)
        msg = client.messages.create(model="claude-haiku-4-5", max_tokens=200,
                                      messages=[{"role": "user", "content": prompt}])
        return msg.content[0].text.strip() or fallback
    except Exception:
        return fallback


def generate_social_graphic_facts():
    """Use Claude to distil the match log into structured facts for the Instagram graphic.
    Returns a dict with result/score/performer lines, or an error dict."""
    cfg = load_state()
    api_key = cfg.get("anthropic_api_key","").strip()
    if not api_key:
        return {"ok": False, "error": "No Anthropic API key set"}
    try:
        import anthropic
    except ImportError:
        return {"ok": False, "error": "anthropic package not installed"}

    # Reuse the same factual summary the report generator builds
    st = _pcs_last_state or {}
    home = cfg.get("home_team","") or cfg.get("name","Home")
    away = cfg.get("away_team","Opposition")
    comp = cfg.get("competition","")
    lines = [f"Match: {home} v {away}" + (f" ({comp})" if comp else "")]
    for inn_no in sorted(_match_log["innings"].keys()):
        r = _match_log["innings"][inn_no]
        lines.append(f"Innings {inn_no}: {r.get('batting_team','?')} "
                     f"{r.get('score',0)}-{r.get('wickets',0)} ({r.get('overs',0)} overs)")
    if _match_log["fall_of_wickets"]:
        lines.append("Wickets: " + "; ".join(
            f"{w.get('batter','?')} {w.get('howout','')} at {w.get('score','?')}"
            for w in _match_log["fall_of_wickets"][:12]))
    if _match_log["milestones"]:
        lines.append("Milestones: " + "; ".join(
            f"{m.get('batter','?')} {m.get('milestone','')}" for m in _match_log["milestones"][:8]))
    if _match_log["events"]:
        lines.append("Key moments: " + " | ".join(e["detail"] for e in _match_log["events"][-25:]))
    summary = "\n".join(lines)

    prompt = (
        "From the cricket match facts below, produce a JSON object for a result graphic. "
        "Use ONLY facts present; if something is unknown use an empty string. "
        "Return ONLY the JSON, no preamble, no markdown fences. Keys:\n"
        '  "result": short result line, UPPERCASE, max 40 chars (e.g. "HOME WIN BY 5 WICKETS")\n'
        '  "team1_name": first innings batting team, UPPERCASE\n'
        '  "team1_score": e.g. "230 (32.1)"\n'
        '  "team2_name": second innings batting team, UPPERCASE\n'
        '  "team2_score": e.g. "232-6 (38.4)"\n'
        '  "performer1": top performer line, e.g. "J SMITH 64* (42)"\n'
        '  "performer2": second performer, e.g. "A JONES 3-21" (a bowler if possible)\n'
        '  "caption": a short upbeat Instagram caption, max 50 words, up to 3 hashtags, at most 1 emoji\n\n'
        f"{summary}\n\nJSON:"
    )
    try:
        client = anthropic.Anthropic(api_key=api_key)
        msg = client.messages.create(model="claude-haiku-4-5", max_tokens=400,
                                     messages=[{"role":"user","content":prompt}])
        raw = msg.content[0].text.strip()
        raw = raw.replace("```json","").replace("```","").strip()
        facts = json.loads(raw)
        facts["ok"] = True
        return facts
    except Exception as e:
        return {"ok": False, "error": str(e)}


def _ig_font(size, bold=True):
    """Load a font at the given size. Falls back through common families so it works
    on any machine; DejaVu ships with Pillow so it is always available."""
    from PIL import ImageFont
    candidates = ([
        "/System/Library/Fonts/Helvetica.ttc",                      # macOS
        "/System/Library/Fonts/Supplemental/Arial Bold.ttf",
        "C:/Windows/Fonts/arialbd.ttf" if bold else "C:/Windows/Fonts/arial.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf" if bold
            else "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
        "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf" if bold
            else "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    ])
    for path in candidates:
        try:
            return ImageFont.truetype(path, size)
        except Exception:
            continue
    # Pillow-bundled DejaVu — guaranteed to exist
    try:
        from PIL import ImageFont as _IF
        import PIL, os as _os
        base = _os.path.join(_os.path.dirname(PIL.__file__), "fonts")
        return _IF.truetype(_os.path.join(base, "DejaVuSans-Bold.ttf" if bold else "DejaVuSans.ttf"), size)
    except Exception:
        from PIL import ImageFont as _IF
        return _IF.load_default()


def build_instagram_image(facts, photo_path=None, out_path=None):
    """Composite an England-Cricket-style result graphic:
    photo backdrop + cinematic gradient + left accent stripe + crest + competition label
    + RESULT pill + hero result line + head-to-head scoreboard + player of the match
    + full-width accent footer bar. Returns the output file path."""
    from PIL import Image, ImageDraw, ImageFilter
    W, H = 1080, 1350                      # Instagram 4:5 portrait
    cfg = load_state()

    # ── Colours ──
    def _hex(h, fallback):
        try:
            h = h.lstrip("#")
            return tuple(int(h[i:i+2], 16) for i in (0, 2, 4))
        except Exception:
            return fallback
    ACCENT = _hex(cfg.get("home_colour", "#1a3a5c"), (26, 58, 92))
    # a brighter tint of the accent for small labels/rules
    ACCENT_LT = tuple(min(255, int(c + (255 - c) * 0.45)) for c in ACCENT)
    ACCENT_DK = tuple(int(c * 0.55) for c in ACCENT)
    WHITE, MUTED, INK = (255, 255, 255), (210, 222, 236), (8, 18, 34)

    # ── Backdrop (cover-scale to fill) ──
    img = Image.new("RGB", (W, H), INK)
    has_photo = False
    if photo_path and os.path.exists(photo_path):
        try:
            photo = Image.open(photo_path).convert("RGB")
            pr, cr = photo.width / photo.height, W / H
            if pr > cr:
                nh = H; nw = int(H * pr)
            else:
                nw = W; nh = int(W / pr)
            photo = photo.resize((nw, nh), Image.LANCZOS)
            img.paste(photo, ((W - nw)//2, (H - nh)//2))
            has_photo = True
        except Exception:
            pass

    if has_photo:
        # ── Duotone the photo into club colours (county-template style): the photo
        # stays readable as texture, but the card reads as a club-colour graphic.
        tint = Image.new("RGB", (W, H), ACCENT_DK)
        img = Image.blend(img, tint, 0.58)

    # ── County-template texture: pinstripes + skewed slashes (photo or flat) ──
    if not has_photo:
        # Vertical accent gradient base (deep club colour fading darker toward the foot)
        for yy in range(H):
            t = yy / H
            col = tuple(int(ACCENT_DK[i] + (ACCENT[i] - ACCENT_DK[i]) * (1 - t) * 0.9) for i in range(3))
            img.paste(Image.new("RGB", (W, 1), col), (0, yy))
    ov = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    do = ImageDraw.Draw(ov)
    # Diagonal pinstripe texture (very faint)
    for x in range(-H, W + H, 56):
        do.line([(x, H), (x + H, 0)], fill=(255, 255, 255, 10), width=3)
    # Two big skewed slashes — the signature county-template shapes
    do.polygon([(W*0.55, 0), (W*1.3, 0), (W*0.95, H*0.62)],
               fill=ACCENT_LT + (38,))
    do.polygon([(W*0.70, 0), (W*1.45, 0), (W*1.05, H*0.70)],
               fill=(255, 255, 255, 16))
    img = Image.alpha_composite(img.convert("RGBA"), ov).convert("RGB")
    if not has_photo:
        # Huge translucent watermark crest on the right (skipped over photos — too busy)
        try:
            cid_w = str(cfg.get("home_club_id", "")).strip()
            logos_w = cfg.get("logos_folder", "").strip()
            logos_w = os.path.expanduser(logos_w) if logos_w else os.path.join(
                      os.path.dirname(os.path.abspath(__file__)), "logos")
            for ext in (".png", ".webp", ".jpg", ".jpeg"):
                lp = os.path.join(logos_w, cid_w + ext)
                if os.path.exists(lp):
                    wm = Image.open(lp).convert("RGBA")
                    wm.thumbnail((860, 860), Image.LANCZOS)
                    a = wm.getchannel("A").point(lambda v: int(v * 0.08))
                    wm.putalpha(a)
                    img_rgba = img.convert("RGBA")
                    img_rgba.paste(wm, (W - wm.width + 150, H//2 - wm.height//2 - 60), wm)
                    img = img_rgba.convert("RGB")
                    break
        except Exception:
            pass

    # ── Cinematic bottom gradient (transparent up top → deep ink over the lower half) ──
    grad = Image.new("L", (1, H), 0)
    for y in range(H):
        t = y / H
        if t < 0.40:
            a = int(38 * (t / 0.40))                       # faint top wash
        else:
            a = int(38 + 217 * ((t - 0.40) / 0.60) ** 1.25)  # ramp to near-solid at the base
        grad.putpixel((0, y), min(255, a))
    grad = grad.resize((W, H))
    ink_layer = Image.new("RGB", (W, H), INK)
    img = Image.composite(ink_layer, img, grad)

    # ── Soft vignette so any photo holds together (skip on the flat-colour backdrop) ──
    if has_photo:
        vig = Image.new("L", (W, H), 0)
        dv = ImageDraw.Draw(vig)
        dv.ellipse([-W*0.25, -H*0.20, W*1.25, H*1.20], fill=255)
        vig = vig.filter(ImageFilter.GaussianBlur(180))
        vig = vig.point(lambda v: 255 - int(v * 0.55))          # darken edges ~55%
        img = Image.composite(Image.new("RGB", (W, H), INK), img, vig)

    d = ImageDraw.Draw(img)

    def shadow_text(x, y, text, font, fill=WHITE, anchor=None, sh=(0,0,0,160)):
        """Text with a soft drop shadow for legibility on any backdrop."""
        d.text((x+2, y+2), text, font=font, fill=(0,0,0), anchor=anchor)
        d.text((x, y), text, font=font, fill=fill, anchor=anchor)

    # ── Left accent stripe (signature element) ──
    d.rectangle([0, 0, 14, H], fill=ACCENT)

    PAD = 80
    # ── Crest top-left (falls back gracefully if missing) ──
    top_y = 70
    crest_h = 0
    try:
        cid = str(cfg.get("home_club_id", "")).strip()
        logos = cfg.get("logos_folder", "").strip()
        logos = os.path.expanduser(logos) if logos else os.path.join(
                os.path.dirname(os.path.abspath(__file__)), "logos")
        for ext in (".png", ".webp", ".jpg", ".jpeg"):
            lp = os.path.join(logos, cid + ext)
            if os.path.exists(lp):
                crest = Image.open(lp).convert("RGBA")
                crest.thumbnail((132, 132), Image.LANCZOS)
                img.paste(crest, (PAD, top_y), crest)
                crest_h = crest.height
                break
    except Exception:
        crest_h = 0

    # ── Competition / match label (top, beside or under the crest) ──
    comp = (cfg.get("competition", "") or "MATCH RESULT").upper()
    f_comp = _ig_font(30, bold=True)
    if crest_h:
        d.text((PAD + 150, top_y + crest_h//2), comp[:34], font=f_comp,
               fill=MUTED, anchor="lm")
    else:
        shadow_text(PAD, top_y + 6, comp[:34], f_comp, fill=MUTED)

    # ===================== LOWER CONTENT BLOCK (left-aligned) =====================
    FOOT_H = 96                      # footer bar height
    # We lay the block out from a start Y and flow downward. Started high enough that
    # the performers section clears the sponsor strip (86px) above the footer.
    y = 620

    # RESULT ribbon — skewed parallelogram with an offset echo stripe (county-template style)
    f_pilllbl = _ig_font(32, bold=True)
    pill_txt = (facts.get("label", "") or "RESULT").upper()[:16]
    pw = d.textlength(pill_txt, font=f_pilllbl)
    rib_w, rib_h, slant = int(pw + 76), 58, 22
    d.polygon([(PAD + slant, y), (PAD + rib_w + slant, y),
               (PAD + rib_w, y + rib_h), (PAD, y + rib_h)], fill=ACCENT)
    # echo stripe just behind it
    d.polygon([(PAD + rib_w + slant + 14, y), (PAD + rib_w + slant + 30, y),
               (PAD + rib_w + 30, y + rib_h), (PAD + rib_w + 14, y + rib_h)], fill=ACCENT_LT)
    d.text((PAD + slant//2 + 30, y + rib_h/2), pill_txt, font=f_pilllbl, fill=WHITE, anchor="lm")
    y += rib_h + 30

    # Hero result line — bigger, tighter, with an accent slash underline
    result = (facts.get("result", "") or "RESULT").upper()
    f_res = _ig_font(92, bold=True)
    if d.textlength(result, font=f_res) > (W - 2*PAD):
        f_res = _ig_font(70, bold=True)
    words, lineA, lineB = result.split(), "", ""
    for wd in words:
        trial = (lineA + " " + wd).strip()
        if d.textlength(trial, font=f_res) <= (W - 2*PAD) and not lineB:
            lineA = trial
        else:
            lineB = (lineB + " " + wd).strip()
    shadow_text(PAD, y, lineA, f_res); y += f_res.size + 2
    if lineB:
        shadow_text(PAD, y, lineB, f_res); y += f_res.size + 2
    # slash underline
    d.polygon([(PAD + 8, y + 16), (PAD + 148, y + 16), (PAD + 132, y + 26), (PAD - 8, y + 26)],
              fill=ACCENT_LT)
    y += 52

    # Head-to-head scoreboard — angled white panel, ink names, accent scores
    rows = []
    if facts.get("team1_name"):
        rows.append((facts["team1_name"].upper(), facts.get("team1_score", "")))
    if facts.get("team2_name"):
        rows.append((facts["team2_name"].upper(), facts.get("team2_score", "")))
    f_score = _ig_font(46, bold=True)
    GAP = 28                                          # min space between name and score
    if rows:
        panel_h = 30 + 74 * len(rows)
        pslant  = 26
        d.polygon([(PAD - 26 + pslant, y), (W, y), (W, y + panel_h),
                   (PAD - 26, y + panel_h)], fill=WHITE)
        # accent edge on the panel's slanted left side
        d.polygon([(PAD - 26 + pslant, y), (PAD - 26 + pslant + 12, y),
                   (PAD - 26 + 12, y + panel_h), (PAD - 26, y + panel_h)], fill=ACCENT)
        y += 22
    for i, (name, score) in enumerate(rows):
        if i > 0:
            d.line([(PAD + 10, y - 8), (W - PAD, y - 8)], fill=(8, 18, 34, 30), width=2)
        cy = y + 24                                   # vertical centre of the row
        sw = d.textlength(score, font=f_score)
        avail = (W - PAD - 40) - PAD - 30 - sw - GAP  # width left for the name
        # Shrink the name font until the full name fits — no ugly mid-word truncation.
        nsize = 46
        f_team = _ig_font(nsize, bold=True)
        while d.textlength(name, font=f_team) > avail and nsize > 26:
            nsize -= 2
            f_team = _ig_font(nsize, bold=True)
        disp = name
        # Ellipsis only as a last resort if still too long at the minimum size.
        if d.textlength(disp, font=f_team) > avail:
            while disp and d.textlength(disp + "…", font=f_team) > avail:
                disp = disp[:-1]
            disp = disp.rstrip() + "…"
        d.text((PAD + 30, cy), disp, font=f_team, fill=INK, anchor="lm")
        d.text((W - PAD - 40 - sw, cy), score, font=f_score, fill=ACCENT, anchor="lm")
        y += 74
    y += 30

    # Player of the match / key performers
    perfs = [p for p in (facts.get("performer1", ""), facts.get("performer2", "")) if p]
    if perfs:
        d.rectangle([PAD, y, PAD + 60, y + 6], fill=ACCENT)
        y += 22
        f_ph = _ig_font(27, bold=True)
        label = "PLAYER OF THE MATCH" if len(perfs) == 1 else "KEY PERFORMERS"
        d.text((PAD, y), label, font=f_ph, fill=ACCENT_LT); y += 40
        f_perf = _ig_font(40, bold=True)
        line = "   •   ".join(perfs)
        if d.textlength(line, font=f_perf) > (W - 2*PAD):
            # stack onto separate lines if too wide
            for p in perfs:
                shadow_text(PAD, y, p, f_perf); y += 52
        else:
            shadow_text(PAD, y, line, f_perf); y += 52

    # ── Sponsor strip (white panel above the footer; shows ALL logos in sponsors/) ──
    # Logos share the available width equally and scale to fit, so 2 sponsors or 8 both
    # lay out tidily in one row. Match-day sponsors: just drop more files into sponsors/.
    try:
        sp_dir = (cfg.get("sponsors_folder", "").strip()
                  or os.path.join(os.path.dirname(os.path.abspath(__file__)), "sponsors"))
        sp_dir = os.path.expanduser(sp_dir)
        sps = sorted([f for f in os.listdir(sp_dir)
                      if f.lower().endswith((".png", ".jpg", ".jpeg", ".webp"))])
        if sps:
            STRIP_H = 88
            sy = H - FOOT_H - STRIP_H
            d.rectangle([0, sy, W, sy + STRIP_H], fill=(244, 246, 249))
            n   = len(sps)
            gap = 30 if n > 1 else 0
            cell = (W - 2*PAD - gap * (n - 1)) / n          # equal share of the row width
            logos_imgs = []
            for f in sps:
                try:
                    s = Image.open(os.path.join(sp_dir, f)).convert("RGBA")
                    s.thumbnail((max(40, int(cell)), STRIP_H - 26), Image.LANCZOS)
                    logos_imgs.append(s)
                except Exception:
                    pass
            if logos_imgs:
                total = sum(s.width for s in logos_imgs) + gap * (len(logos_imgs) - 1)
                x = (W - total) // 2
                for s in logos_imgs:
                    img.paste(s, (x, sy + (STRIP_H - s.height)//2), s)
                    x += s.width + gap
    except Exception:
        pass

    # ── Footer accent bar (club name + date, white on accent) ──
    d.rectangle([0, H - FOOT_H, W, H], fill=ACCENT)
    f_foot = _ig_font(30, bold=True)
    club = (cfg.get("name", "") or "Home CC")
    d.text((PAD, H - FOOT_H/2), club, font=f_foot, fill=WHITE, anchor="lm")
    datestr = datetime.date.today().strftime("%d %b %Y").upper()
    dw = d.textlength(datestr, font=f_foot)
    d.text((W - PAD - dw, H - FOOT_H/2), datestr, font=f_foot, fill=WHITE, anchor="lm")

    # ── Save ──
    if not out_path:
        out_path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                f"instagram_result_{datetime.date.today().isoformat()}.png")
    img.save(out_path, "PNG")
    return out_path


def parse_pcs_json(data):
    """Convert PCS Pro JSON output into overlay state format."""
    def g(key, default=""):
        """Get value, handling PCS's {{field}} placeholders for unset values. Also falls back
        to default on a blank string — NV Play renders the template as soon as a match starts,
        so fields like batter1_name are genuinely empty (not missing, not {{placeholder}}) until
        the scorer actually selects the openers. Without this, pre-game state looks like a real
        (blank-named) player to the overlay instead of "nobody's in yet"."""
        val = data.get(key, default)
        if isinstance(val, str) and (val.startswith("{{") or val.strip() == ""):
            return default
        return val

    def gi(key, default=0):
        try: return int(float(str(g(key, default) or 0)))
        except: return default

    def gf(key, default=0.0):
        try: return float(str(g(key, default) or 0))
        except: return default

    runs    = gi("runs")
    wickets = gi("wickets")
    overs   = gf("overs")

    # Innings detection. The reliable live signal is "runs_required" (the chase target gap),
    # which is positive throughout the 2nd innings — EXCEPT it drops to 0/negative the instant
    # the target is reached, which would otherwise flip us back to "1st innings" on the winning
    # runs. So we LATCH: once we're in the 2nd innings we stay there until a genuinely fresh
    # innings begins (a near-empty scorecard: under an over, 0 for 0).
    global _innings_latch
    runs_req_raw = gi("runs_required", 0)
    fresh_start  = (overs < 1.0 and runs == 0 and wickets == 0)
    if runs_req_raw > 0:
        inn_num = 2
        _innings_latch = 2
    elif _innings_latch == 2 and not fresh_start:
        inn_num = 2                 # hold the 2nd innings through the winning runs
    else:
        inn_num = 1
        _innings_latch = 1

    batting_team = g("batting_team", "Batting")
    bowling_team = g("bowling_team", "Bowling")

    b1_name   = g("batter1_name", "—")
    b1_number = str(g("batter1_number", "")).strip()
    b1_runs   = gi("batter1_runs")
    b1_balls  = gi("batter1_balls")
    # Per-batter how-out isn't in the current-partnership fields (those describe the two
    # batters at the crease, who aren't out). Dismissal detail comes from the LastWicket*
    # fields instead — read below and exposed as state["lastWicket"].
    b1_howout = ""
    # Striker detection — check both fields; NV Play may output True/False/1/0/""
    _b1s = str(g("batter1_strike","")).strip().lower()
    _b2s = str(g("batter2_strike","")).strip().lower()
    TRUTHY = ("true","1","yes","*")
    if _b2s in TRUTHY and _b1s not in TRUTHY:
        b1_strike = False   # batter2 explicitly on strike
    elif _b1s in TRUTHY:
        b1_strike = True    # batter1 explicitly on strike
    else:
        b1_strike = True    # default — batter1 on strike if unknown
    b1_sr     = gf("batter1_sr") or (round(b1_runs/b1_balls*100,1) if b1_balls > 0 else 0.0)

    b2_name   = g("batter2_name", "—")
    b2_number = str(g("batter2_number", "")).strip()
    b2_runs   = gi("batter2_runs")
    b2_balls  = gi("batter2_balls")
    b2_howout = ""
    b2_sr     = gf("batter2_sr") or (round(b2_runs/b2_balls*100,1) if b2_balls > 0 else 0.0)

    bowl_name  = g("bowler_name", "—")
    bowl_overs = str(g("bowler_overs", "0"))
    bowl_runs  = gi("bowler_runs")
    bowl_wkts  = gi("bowler_wickets")

    rr     = gf("run_rate")
    target = gi("target")
    req_rr = gf("req_rate")

    if not batting_team or batting_team == "—":
        return None

    state = {
        "battingTeamName": batting_team,
        "bowlingTeamName": bowling_team,
        "innings":  inn_num,
        "score":    runs,
        "wickets":  wickets,
        "overs":    overs,
        "batter1":  {"name": b1_name, "number": b1_number, "runs": b1_runs, "balls": b1_balls, "onStrike": b1_strike, "sr": b1_sr,
                     "fours": gi("batter1_fours"), "sixes": gi("batter1_sixes"), "howOut": b1_howout},
        "batter2":  {"name": b2_name, "number": b2_number, "runs": b2_runs, "balls": b2_balls, "onStrike": not b1_strike, "sr": b2_sr,
                     "fours": gi("batter2_fours"), "sixes": gi("batter2_sixes"), "howOut": b2_howout},
        "bowler":   {"name": bowl_name, "overs": bowl_overs, "runs": bowl_runs, "wickets": bowl_wkts,
                     "maidens": gi("bowler_maidens")},
        "statusText":     f"RR: {rr:.2f}" if rr else "—",
        "run_rate":       rr,
        "targetRuns":     target,
        "runsRequired":   max(0, target - runs) if target else 0,
        "ballsRemaining": 0,
        # PCS-specific live fields passed through to overlay
        "partnership_runs":    gi("partnership_runs"),
        "partnership_balls":   gi("partnership_balls"),
        # Most-recent dismissal, taken straight from PCS Pro's pre-formatted LastWicket fields.
        # howOut is already formatted by PCS Pro for every dismissal type (e.g. "C JONES B HOGGARD",
        # "LBW B MCGRATH", "RUN OUT (GILCHRIST)", "B HARMISO"), so we pass it through verbatim.
        "lastWicketHowOut":    g("last_wicket_howout", "").strip(),
        "lastWicketBatter":    g("last_wicket_batter", "").strip(),   # e.g. "MARTY 12"
        "lastWicketBowler":    g("last_wicket_bowler", "").strip(),
        "lastWicketFielder":   g("last_wicket_fielder", "").strip(),
        "lastWicketType":      g("last_wicket_type", "").strip(),
        "last_ball":           g("last_ball"),
        "last_over_runs":      gi("last_over_runs"),
        "over_history":        g("over_history"),
        "prev_bowler_name":    g("prev_bowler_name"),
        "prev_bowler_overs":   g("prev_bowler_overs"),
        "prev_bowler_runs":    gi("prev_bowler_runs"),
        "prev_bowler_wickets": gi("prev_bowler_wickets"),
        "prev_bowler_maidens": gi("prev_bowler_maidens", 0),
        "pcs_overs":           overs,
        "runsRequired":        gi("runs_required", 0),
        "ballsRemaining":      gi("balls_remaining", 0),
    }

    # Full scorecard (template v2.1 fields): 11 batters + 11 bowlers for the innings-break card.
    # Older deployed templates simply lack the keys, so the card comes out empty — harmless.
    card_bat, card_bwl = [], []
    for i in range(1, 12):
        nm = str(g(f"card_b{i}_name", "")).strip()
        if nm and not nm.startswith("{{"):
            card_bat.append({"name": nm, "runs": gi(f"card_b{i}_runs"),
                             "balls": gi(f"card_b{i}_balls"),
                             "out":  str(g(f"card_b{i}_out", "")).strip()})
        wn = str(g(f"card_w{i}_name", "")).strip()
        if wn and not wn.startswith("{{"):
            card_bwl.append({"name": wn, "o": str(g(f"card_w{i}_o", "0")).strip(),
                             "m": gi(f"card_w{i}_m"), "r": gi(f"card_w{i}_r"),
                             "w": gi(f"card_w{i}_w")})
    state["card"] = {"batters": card_bat, "bowlers": card_bwl}
    return state


# ── PlayCricket API — auto-detect today's match ──────────────────────────────
def fetch_todays_match(api_key, site_id):
    """
    Fetch today's home fixture from the PlayCricket API.
    Returns a dict with match details or None if not found.
    """
    if not api_key or not api_key.strip():
        return {"error": "No API key set"}
    if not site_id:
        return {"error": "No PlayCricket club ID set"}

    today  = datetime.date.today().strftime("%d/%m/%Y")
    s_cfg  = load_state()
    club_name = s_cfg.get("home_abbrev") or s_cfg.get("home_team") or "Your club"

    url = (f"https://play-cricket.com/api/v2/matches.json"
           f"?api_token={api_key}&site_id={site_id}&season={datetime.date.today().year}")
    try:
        req = urllib.request.Request(url, headers={"Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode())
    except Exception as e:
        return {"error": str(e)}

    matches = data.get("matches", [])
    todays  = [m for m in matches if m.get("match_date") == today]

    if not todays:
        return {"error": f"No matches found for today ({today})", "all_count": len(matches)}

    # Only use home matches — stream is home games only
    home_matches = [m for m in todays if str(m.get("home_club_id","")) == str(site_id)]
    if not home_matches:
        away = [m for m in todays if str(m.get("away_club_id","")) == str(site_id)]
        if away:
            return {"error": f"{club_name} are away today — no home fixture to stream"}
        return {"error": f"No {club_name} matches found for today ({today})"}

    # Filter by ground name if configured
    gnd_filt = s_cfg.get("ground_filter","").strip().lower()
    if gnd_filt and len(home_matches) > 1:
        ground_matches = [m for m in home_matches
                          if gnd_filt in m.get("ground_name","").lower()]
        if ground_matches:
            home_matches = ground_matches

    # If still multiple (e.g. 1st XI and 2nd XI both at home), take first
    match = home_matches[0]

    # Work out opposition
    if str(match.get("home_club_id","")) == str(site_id):
        opp_name  = match.get("away_team_name", "")
        opp_club  = match.get("away_club_name", "")
    else:
        opp_name  = match.get("home_team_name", "")
        opp_club  = match.get("home_club_name", "")

    # Auto-abbreviate opposition: first word up to 5 chars, uppercased
    opp_words = opp_name.replace(" CC","").replace(" Cricket Club","").strip().split()
    auto_abbrev = opp_words[0][:5].upper() if opp_words else ""

    return {
        "match_id":        str(match.get("id","")),
        "match_date":      match.get("match_date",""),
        "match_time":      match.get("match_time",""),
        "competition":     match.get("competition_name",""),
        "competition_type":match.get("competition_type",""),
        "home_team":       match.get("home_team_name",""),
        "away_team":       opp_name,
        "away_club":       opp_club,
        "away_abbrev":     auto_abbrev,
        "ground":          match.get("ground_name",""),
        "ground_lat":      match.get("ground_latitude",""),
        "ground_lon":      match.get("ground_longitude",""),
        "umpire1":         match.get("umpire_1_name",""),
        "umpire2":         match.get("umpire_2_name",""),
        "home_club_id":    str(match.get("home_club_id","")),
        "away_club_id":    str(match.get("away_club_id","")),
        "scorer1":         match.get("scorer_1_name",""),
        "scorer2":         match.get("scorer_2_name",""),
        "all_today":       len(todays),
    }

# ── Kit colour presets ────────────────────────────────────────
KIT_PRESETS = [
    {"name":"Navy",         "hex":"#1a3a5c"},
    {"name":"Royal Blue",   "hex":"#1a4fa8"},
    {"name":"Sky Blue",     "hex":"#0099cc"},
    {"name":"Dark Green",   "hex":"#1a5c2a"},
    {"name":"Bright Green", "hex":"#2e8b2e"},
    {"name":"Maroon",       "hex":"#7b1a1a"},
    {"name":"Red",          "hex":"#c0392b"},
    {"name":"Purple",       "hex":"#6a1a8c"},
    {"name":"Black",        "hex":"#1a1a1a"},
    {"name":"Dark Grey",    "hex":"#4a4a4a"},
    {"name":"Amber",        "hex":"#c87800"},
    {"name":"Orange",       "hex":"#cc5500"},
    {"name":"White",        "hex":"#e8e8e8"},
    {"name":"Teal",         "hex":"#1a6b6b"},
    {"name":"Pink",         "hex":"#b52d6e"},
]

# ── OBS WebSocket helper ──────────────────────────────────────

def _obs_auth_response(password, salt, challenge):
    """Compute OBS WebSocket authentication response string."""
    secret       = base64.b64encode(hashlib.sha256((password + salt).encode()).digest()).decode()
    auth_response = base64.b64encode(hashlib.sha256((secret + challenge).encode()).digest()).decode()
    return auth_response

def obs_trigger_replay(state):
    """
    Full OBS WebSocket v5 flow (runs in a background thread).
    Uses a message-ID tracking approach compatible with OBS 28+.
    """
    try:
        import websocket
    except ImportError:
        print("  ✗  websocket-client not installed. Run: pip install websocket-client")
        return

    host     = state.get("obs_host", "localhost")
    port     = state.get("obs_port", 4455)
    password = state.get("obs_password", "")
    main     = state.get("obs_main_scene", "Main")
    replay_s = state.get("obs_replay_scene", "Replay")
    duration = int(state.get("replay_duration", 18))
    folder   = state.get("replay_folder", "") or _default_replay_folder()

    ws_url = f"ws://{host}:{port}"
    msg_id = [0]

    def nid():
        msg_id[0] += 1
        return str(msg_id[0])

    def send_msg(ws, op, data=None):
        ws.send(json.dumps({"op": op, "d": data or {}}))

    def wait_for_op(ws, target_op, timeout=8):
        """Read messages until we see the target op code, ignore others."""
        ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw:
                    continue
                msg = json.loads(raw)
                if msg.get("op") == target_op:
                    return msg
            except websocket.WebSocketTimeoutException:
                break
            except Exception:
                break
        return None

    def send_request(ws, request_type, request_data=None):
        """Send a request and wait for its response by matching request ID."""
        rid = nid()
        payload = {"requestType": request_type, "requestId": rid}
        if request_data:
            payload["requestData"] = request_data
        send_msg(ws, 6, payload)
        # Wait for op 7 (RequestResponse) matching our request ID
        ws.settimeout(8)
        deadline = time.time() + 8
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw:
                    continue
                msg = json.loads(raw)
                if msg.get("op") == 7 and msg.get("d", {}).get("requestId") == rid:
                    status = msg["d"].get("requestStatus", {})
                    if not status.get("result", True):
                        print(f"  ✗  OBS request {request_type} failed: {status.get('comment','')}")
                    return msg
            except websocket.WebSocketTimeoutException:
                break
            except Exception:
                break
        print(f"  ✗  No response from OBS for {request_type}")
        return None

    clip_path = None
    try:
        ws = websocket.create_connection(ws_url, timeout=10)

        # Step 1: Receive Hello (op 0)
        hello = wait_for_op(ws, 0, timeout=5)
        if not hello:
            print("  ✗  OBS WebSocket: no Hello received — is OBS running?")
            ws.close()
            return

        auth_data = hello["d"].get("authentication")

        # Step 2: Send Identify (op 1)
        identify = {"rpcVersion": 1}
        if auth_data and password:
            identify["authentication"] = _obs_auth_response(
                password,
                auth_data["salt"],
                auth_data["challenge"]
            )
        send_msg(ws, 1, identify)

        # Step 3: Wait for Identified (op 2)
        identified = wait_for_op(ws, 2, timeout=5)
        if not identified:
            print("  ✗  OBS WebSocket auth failed — check password in control panel")
            ws.close()
            return
        print("  ✓  OBS WebSocket authenticated")

        # Step 4: Save replay buffer
        send_request(ws, "SaveReplayBuffer")

        # Step 5: Wait for OBS to write the file
        time.sleep(3)

        # Step 6: Find newest clip and enforce 100-clip limit
        clip_path = _newest_clip(folder)
        if not clip_path:
            print(f"  ✗  No replay clip found in: {folder}")
            print(f"     Make sure OBS Replay Buffer is enabled and saving to: {folder}")
        else:
            _enforce_clip_limit(folder, load_state().get('max_clips', MAX_CLIPS))
            print(f"  ✓  Clip found: {os.path.basename(clip_path)}")

            # Step 7: Point ReplayClip media source at the new clip
            send_request(ws, "SetInputSettings", {
                "inputName": "ReplayClip",
                "inputSettings": {"local_file": clip_path},
            })

        # Step 8: Switch to Replay scene
        send_request(ws, "SetCurrentProgramScene", {"sceneName": replay_s})
        print(f"  ▶  Switched to {replay_s} scene")

        # Step 9: Hold on replay scene for configured duration
        time.sleep(duration)

        # Step 10: Switch back to Main scene
        send_request(ws, "SetCurrentProgramScene", {"sceneName": main})
        print(f"  ◀  Returned to {main} scene")

        ws.close()

    except Exception as e:
        print(f"  ✗  OBS WebSocket error: {e}")

def obs_add_camera(rtsp_url, input_name=None, scene_name=None, state=None):
    """Create an RTSP media source in OBS over WebSocket — one-click camera setup for
    non-technical users. Re-adding with the same name replaces the old source so it stays
    idempotent. Returns (ok, message)."""
    try:
        import websocket
    except ImportError:
        return False, "websocket-client not installed — run: pip install websocket-client"
    state = state or load_state()
    rtsp_url = (rtsp_url or "").strip()
    if not rtsp_url:
        return False, "No camera URL given"
    host     = state.get("obs_host", "localhost")
    port     = state.get("obs_port", 4455)
    password = state.get("obs_password", "")
    name     = (input_name or state.get("obs_camera_name") or "Cricket Camera").strip()
    scene    = (scene_name or state.get("obs_main_scene") or "Main").strip()
    ws_url   = f"ws://{host}:{port}"
    mid = [0]
    def nid():
        mid[0] += 1
        return str(mid[0])
    def send_msg(ws, op, data=None):
        ws.send(json.dumps({"op": op, "d": data or {}}))
    def wait_for_op(ws, target, timeout=6):
        ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw:
                    continue
                msg = json.loads(raw)
                if msg.get("op") == target:
                    return msg
            except Exception:
                break
        return None
    def send_request(ws, rt, rd=None):
        rid = nid()
        payload = {"requestType": rt, "requestId": rid}
        if rd is not None:
            payload["requestData"] = rd
        send_msg(ws, 6, payload)
        ws.settimeout(8)
        deadline = time.time() + 8
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw:
                    continue
                msg = json.loads(raw)
                if msg.get("op") == 7 and msg.get("d", {}).get("requestId") == rid:
                    return msg
            except Exception:
                break
        return None
    def _rdata(msg):
        return (msg or {}).get("d", {}).get("responseData", {}) or {}
    try:
        ws = websocket.create_connection(ws_url, timeout=10)
        hello = wait_for_op(ws, 0, 5)
        if not hello:
            ws.close()
            return False, "No response from OBS — is it running with the WebSocket server enabled?"
        auth = hello["d"].get("authentication")
        identify = {"rpcVersion": 1}
        if auth and password:
            identify["authentication"] = _obs_auth_response(password, auth["salt"], auth["challenge"])
        send_msg(ws, 1, identify)
        if not wait_for_op(ws, 2, 5):
            ws.close()
            return False, "OBS authentication failed — check the WebSocket password"
        # Pick a valid scene: requested one if it exists, else the current program scene.
        scenes = send_request(ws, "GetSceneList")
        names  = [s.get("sceneName") for s in _rdata(scenes).get("scenes", [])]
        if scene not in names:
            scene = _rdata(scenes).get("currentProgramSceneName") or (names[0] if names else scene)
        # Idempotent: if a source with this name exists, remove it before recreating.
        inputs = send_request(ws, "GetInputList")
        if name in [i.get("inputName") for i in _rdata(inputs).get("inputs", [])]:
            send_request(ws, "RemoveInput", {"inputName": name})
        settings = {
            "is_local_file": False,
            "input": rtsp_url,
            "reconnect_delay_sec": 2,    # auto-reconnect if the stream drops
            "restart_on_activate": True,
            "hw_decode": True,           # use GPU decode where available
        }
        resp = send_request(ws, "CreateInput", {
            "sceneName": scene, "inputName": name,
            "inputKind": "ffmpeg_source", "inputSettings": settings,
            "sceneItemEnabled": True})
        ws.close()
        st = (resp or {}).get("d", {}).get("requestStatus", {})
        if st.get("result", False):
            return True, f"Added '{name}' to scene '{scene}'"
        return False, f"OBS rejected the request: {st.get('comment','') or 'unknown error'}"
    except Exception as e:
        return False, f"Could not reach OBS: {e}"


def _default_replay_folder():
    """Best-guess default replay folder for Windows."""
    userprofile = os.environ.get("USERPROFILE", os.path.expanduser("~"))
    candidates = [
        os.path.join(userprofile, "Videos", "Replays"),
        os.path.join(userprofile, "Videos"),
    ]
    for c in candidates:
        if os.path.isdir(c):
            return c
    return os.path.join(userprofile, "Videos", "Replays")

def _newest_clip(folder):
    """Return path to the most recently modified video file in folder."""
    if not folder or not os.path.isdir(folder):
        return None
    patterns = ["*.mkv", "*.mp4", "*.flv", "*.mov"]
    clips = []
    for p in patterns:
        clips.extend(glob.glob(os.path.join(folder, p)))
    if not clips:
        return None
    return max(clips, key=os.path.getmtime)

def _enforce_clip_limit(folder, limit):
    """Delete oldest clips if count exceeds limit (rolling buffer)."""
    if not folder or not os.path.isdir(folder):
        return
    patterns = ["*.mkv", "*.mp4", "*.flv", "*.mov"]
    clips = []
    for p in patterns:
        clips.extend(glob.glob(os.path.join(folder, p)))
    if len(clips) <= limit:
        return
    # Sort oldest first
    clips.sort(key=os.path.getmtime)
    to_delete = clips[:len(clips) - limit]
    for f in to_delete:
        try:
            os.remove(f)
            print(f"  🗑  Deleted old clip: {os.path.basename(f)}")
        except Exception as e:
            print(f"  ✗  Could not delete {f}: {e}")

# ── Control panel HTML ────────────────────────────────────────

CONTROL_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title id="page-title">Stream Control Panel</title>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:Arial,sans-serif;background:#0d1b2e;color:#e8edf2;padding:20px;min-height:100vh}
  header{display:flex;align-items:center;gap:14px;margin-bottom:22px;
         border-bottom:2px solid #1a3a5c;padding-bottom:14px}
  .badge{width:42px;height:42px;background:#1a3a5c;border-radius:50%;border:2px solid #2a5a8c;
         display:flex;align-items:center;justify-content:center;font-size:15px;font-weight:900;color:#fff;flex-shrink:0}
  h1{font-size:17px;font-weight:700;color:#fff}
  h1 span{color:#5b9bd5;font-weight:400;font-size:12px;display:block;margin-top:2px}
  .grid{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-bottom:14px}
  .split-grid{display:grid;grid-template-columns:1fr auto;gap:8px;align-items:end}
  .half-grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}
  .side-grid{display:grid;grid-template-columns:1fr 2fr;gap:14px}
  .btn-row{display:flex;gap:8px;flex-wrap:wrap}
  @media(max-width:768px){
    body{padding:12px}
    .grid,.split-grid,.half-grid,.side-grid{grid-template-columns:1fr}
    .btn{padding:14px;font-size:15px;min-height:48px}
    .btn-test,.btn-commentary{padding:12px;font-size:14px;min-height:44px;margin-top:6px}
    .btn-row{flex-direction:column}
    #live-status-bar{grid-template-columns:1fr !important}
    h1{font-size:15px}
  }
  .card{background:#152237;border:1px solid #1e3550;border-radius:9px;padding:16px}
  .pcs-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:8px;margin-bottom:10px}
  .pcs-cell{background:#0a1628;border:1px solid #1e3550;border-radius:6px;padding:8px 10px}
  .pcs-cell-label{font-size:9px;text-transform:uppercase;letter-spacing:1.5px;color:#3d5a7a;font-weight:600;margin-bottom:3px}
  .pcs-cell-value{font-size:16px;font-weight:700;color:#e8edf2}
  .pcs-cell-sub{font-size:10px;color:#3d5a7a;margin-top:1px}
  .pcs-cell.live .pcs-cell-label{color:#4caf50}
  .pcs-ball-row{display:flex;gap:5px;flex-wrap:wrap;margin-top:8px}
  .pcs-ball{width:28px;height:28px;border-radius:50%;display:flex;align-items:center;justify-content:center;
            font-size:11px;font-weight:800;color:#fff;background:#1e3550}
  .pcs-ball.four{background:#1a5fa8}
  .pcs-ball.six{background:#7c3ab5}
  .pcs-ball.wicket{background:#c0392b}
  .pcs-status-bar{display:flex;align-items:center;gap:8px;margin-bottom:10px;
                  padding:8px 10px;background:#0a1628;border-radius:6px;border:1px solid #1e3550}
  .pcs-dot{width:8px;height:8px;border-radius:50%;background:#4a4a4a;flex-shrink:0}
  .pcs-dot.live{background:#4caf50;animation:livePulse 1.5s infinite}
    .card.highlight{border-color:#7c1a7c;background:#1a1230}
  .card.highlight h2{color:#c060c0}
  .btn-commentary{background:#5c1a7c;margin-top:8px;font-size:12px;padding:8px}
  .btn-commentary:hover{background:#7c2a9c}
  .commentary-preview{
    margin-top:10px;padding:12px 14px;
    background:#0a0818;border:1px solid #3a1a5c;border-left:3px solid #c060c0;
    border-radius:5px;font-size:14px;font-style:italic;color:#e8e0f0;
    min-height:44px;line-height:1.5;
  }
  .commentary-meta{font-size:10px;color:#5b3a7a;margin-top:5px;font-style:normal}
  .card.full{grid-column:1/-1}
  .card h2{font-size:10px;text-transform:uppercase;letter-spacing:1.8px;color:#5b9bd5;margin-bottom:12px;font-weight:600}
  .field{margin-bottom:10px}
  label{display:block;font-size:12px;color:#8aa8c4;margin-bottom:3px;font-weight:500}
  input[type=text],input[type=number],textarea,select{
    width:100%;background:#0d1b2e;border:1px solid #2a4060;border-radius:5px;
    color:#e8edf2;font-size:13px;padding:7px 9px;outline:none;transition:border-color .2s;font-family:inherit}
  input:focus,textarea:focus,select:focus{border-color:#5b9bd5}
  textarea{resize:vertical;min-height:52px}
  .hint{font-size:11px;color:#3d5a7a;margin-top:2px}
  /* Colour picker */
  .colour-row{display:flex;align-items:center;gap:8px;margin-bottom:6px}
  .colour-swatch{width:32px;height:32px;border-radius:4px;border:2px solid rgba(255,255,255,0.2);flex-shrink:0;cursor:pointer}
  .colour-hex{width:86px;background:#0d1b2e;border:1px solid #2a4060;border-radius:4px;
              color:#e8edf2;font-size:12px;padding:6px 7px;outline:none;font-family:monospace}
  .colour-hex:focus{border-color:#5b9bd5}
  .presets{display:flex;flex-wrap:wrap;gap:5px;margin-top:6px}
  .preset-btn{width:22px;height:22px;border-radius:3px;border:2px solid transparent;cursor:pointer;transition:border-color .15s,transform .1s}
  .preset-btn:hover{border-color:#fff;transform:scale(1.15)}
  .preset-btn.active{border-color:#fff}
  .preview-bar{height:32px;border-radius:4px;display:flex;align-items:center;overflow:hidden;margin-top:8px;font-size:11px;font-weight:700}
  .preview-home{padding:0 10px;color:#fff;text-transform:uppercase;letter-spacing:1px;flex-shrink:0}
  .preview-centre{flex:1;background:#d0d4db;display:flex;align-items:center;padding:0 8px;color:#0d1520;font-size:10px}
  .preview-away{padding:0 8px;color:#fff;text-transform:uppercase;flex-shrink:0;font-size:10px}
  .preview-bowler{padding:0 8px;color:#fff;font-size:10px;flex-shrink:0}
  /* Toggle */
  .toggle-row{display:flex;align-items:center;justify-content:space-between;padding:5px 0;border-bottom:1px solid #1a2a40}
  .toggle-row:last-child{border-bottom:none}
  .toggle-label{font-size:13px;color:#e8edf2}
  .toggle-desc{font-size:10px;color:#3d5a7a;margin-top:1px}
  .toggle{position:relative;width:40px;height:22px;flex-shrink:0}
  .toggle input{opacity:0;width:0;height:0;position:absolute}
  .track{position:absolute;inset:0;background:#2a4060;border-radius:11px;cursor:pointer;transition:background .2s}
  .toggle input:checked+.track{background:#1a6ab5}
  .track::after{content:'';position:absolute;width:16px;height:16px;left:3px;top:3px;
               background:#fff;border-radius:50%;transition:transform .2s}
  .toggle input:checked+.track::after{transform:translateX(18px)}
  hr{border:none;border-top:1px solid #1e3550;margin:10px 0}
  /* Buttons */
  .btn{display:block;width:100%;padding:11px;border:none;border-radius:6px;
       font-size:14px;font-weight:700;cursor:pointer;background:#1a5fa8;color:#fff;
       transition:background .15s,transform .1s}
  .btn:hover{background:#2272c0}
  .btn:active{transform:scale(.98)}
  .btn-test{background:#1a4a1a;margin-top:8px;font-size:12px;padding:8px}
  .btn-test:hover{background:#256025}
  .status{margin-top:10px;padding:8px 11px;border-radius:4px;font-size:12px;text-align:center;display:none}
  .ok{background:#0d3a1a;color:#4caf50;border:1px solid #1e6b30;display:block}
  .err{background:#3a0d0d;color:#f44336;border:1px solid #6b1e1e;display:block}
  .links{margin-top:14px;font-size:11px;color:#3d5a7a;display:flex;gap:14px;flex-wrap:wrap}
  .links a{color:#5b9bd5}
  .section-note{font-size:11px;color:#3d5a7a;margin-bottom:10px;line-height:1.5}
</style>
</head>
<body>

<!-- ── Auth overlay — shown on first visit or after token rejection ── -->
<div id="auth-overlay" style="display:none;position:fixed;inset:0;background:rgba(13,27,46,0.97);
     z-index:9999;align-items:center;justify-content:center;">
  <div style="background:#152237;border:1px solid #1e3550;border-radius:12px;padding:32px 28px;
       width:100%;max-width:380px;text-align:center;">
    <div style="font-size:13px;font-weight:700;letter-spacing:2px;color:#5b9bd5;margin-bottom:14px;">
      CONTROL PANEL
    </div>
    <h2 style="font-size:15px;color:#e8edf2;margin-bottom:6px;">Enter club password</h2>
    <p style="font-size:12px;color:#5b9bd5;margin-bottom:20px;">
      Set as <code style="background:#0d1b2e;padding:1px 5px;border-radius:3px;">club_password</code>
      in <code style="background:#0d1b2e;padding:1px 5px;border-radius:3px;">config.ini [Auth]</code>
    </p>
    <input type="password" id="auth-input" placeholder="Club password"
           style="width:100%;background:#0d1b2e;border:1px solid #2a4060;border-radius:6px;
                  color:#e8edf2;font-size:14px;padding:10px 12px;outline:none;
                  margin-bottom:10px;font-family:monospace;"
           onkeydown="if(event.key==='Enter')doLogin()">
    <button onclick="doLogin()"
            style="width:100%;padding:11px;border:none;border-radius:6px;font-size:14px;
                   font-weight:700;cursor:pointer;background:#1a5fa8;color:#fff;margin-bottom:8px;">
      Unlock
    </button>
    <p id="auth-error" style="font-size:12px;color:#f44336;margin-bottom:10px;display:none;"></p>
    <button onclick="doSkip()"
            style="background:none;border:none;font-size:11px;color:#3d5a7a;cursor:pointer;
                   text-decoration:underline;">
      No token configured? Click to continue
    </button>
  </div>
</div>

<header>
  <div class="badge" id="panel-club-badge">CC</div>
  <h1>Stream Control Panel<span id="panel-club-name">Your Club</span></h1>
</header>

<!-- ── Pre-match checklist ── -->
<div id="checklist-bar" style="background:#0a1628;border:1px solid #1e3550;border-radius:9px;padding:16px 18px;margin-bottom:16px;">
  <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px;">
    <span style="font-size:10px;text-transform:uppercase;letter-spacing:1.8px;color:#5b9bd5;font-weight:600;">Pre-match checklist</span>
    <button onclick="resetChecklist()" style="font-size:10px;color:#3d5a7a;background:none;border:none;cursor:pointer;padding:0;">Reset</button>
  </div>
  <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:6px;" id="checklist-items"></div>
</div>

<!-- ── AI Commentary card ── -->
<div class="card highlight" style="margin-bottom:14px;">
  <h2>&#129302; AI Commentary <span style="text-transform:none;letter-spacing:0;font-size:10px;
      color:#5b3a7a;font-weight:400;">(NEW — test feature)</span></h2>
  <p class="section-note" style="color:#8060a0;">
    End-of-over AI commentary and new-batter player cards. Uses Claude Haiku — fast and
    very cheap (fractions of a penny per line). Requires an Anthropic API key below.
  </p>
  <div class="toggle-row">
    <div>
      <div class="toggle-label">AI commentary (end of over)</div>
      <div class="toggle-desc">4th panel after over summary, partnership, run rate</div>
    </div>
    <label class="toggle">
      <input type="checkbox" id="graphics_commentary_over">
      <span class="track"></span>
    </label>
  </div>
  <div class="toggle-row">
    <div>
      <div class="toggle-label">Player card on new batter</div>
      <div class="toggle-desc">Shows name + season stats when a new batter arrives</div>
    </div>
    <label class="toggle">
      <input type="checkbox" id="graphics_player_card">
      <span class="track"></span>
    </label>
  </div>
  <div class="field">
    <label>Anthropic API key</label>
    <input type="text" id="anthropic_api_key"
           placeholder="sk-ant-api03-...">
    <p class="hint">Get a free key at <a href="https://console.anthropic.com"
       target="_blank" style="color:#5b9bd5;">console.anthropic.com</a> —
       Claude Haiku costs ~$0.001 per commentary line</p>
  </div>
  <div class="commentary-preview" id="commentary-preview">
    Commentary will appear here during the match...
  </div>
  <div class="commentary-meta" id="commentary-meta"></div>
  <button class="btn btn-commentary" onclick="testCommentary()">
    &#129302; Generate test commentary
  </button>
</div>

<!-- PlayCricket API card -->
<div class="card" style="margin-bottom:14px;border-color:#2a6496;">
  <h2 style="color:#5b9bd5;">&#9889; PlayCricket API — Auto-fill</h2>
  <p class="section-note">
    Fetches today's fixture automatically — fills in opposition name,
    umpires, competition, and ground coordinates for weather.
    The API only works from your registered laptop.
  </p>
  <div class="split-grid" style="gap:10px;margin-bottom:10px;">
    <div class="field" style="margin-bottom:0">
      <label>PlayCricket API key</label>
      <input type="text" id="playcricket_api_key" placeholder="Paste your API key here">
    </div>
    <div class="field" style="margin-top:10px;">
      <label>Ground name filter</label>
      <input type="text" id="ground_filter" placeholder="e.g. The Green">
      <p class="hint">Only fetch home fixtures at this ground. Partial match, case-insensitive. Leave blank for any home ground.</p>
    </div>
    <button class="btn" onclick="fetchTodaysMatch()"
            style="background:#2a6496;white-space:nowrap;height:38px;">
      &#128203; Fetch today's match
    </button>
  </div>
  <div id="pc-api-result" style="font-size:12px;color:#3d5a7a;padding:8px 10px;
       background:#0a1628;border-radius:5px;border:1px solid #1e3550;min-height:36px;">
    Enter your API key and click Fetch — opposition name, umpires and competition
    will be filled in automatically.
  </div>
  <div id="pc-api-details" style="display:none;margin-top:10px;">
    <div class="pcs-grid" id="pc-api-grid"></div>
  </div>
</div>

<div class="grid">

  <!-- Match -->
  <div class="card">
    <h2>Match</h2>
    <div id="demo-row" class="toggle-row" style="margin-bottom:12px;padding:8px 10px;
         background:#0a1a0a;border:1px solid #1a4a1a;border-radius:6px;">
      <div>
        <div class="toggle-label" id="demo-label" style="color:#4caf50;">Demo mode OFF</div>
        <div class="toggle-desc">Shows dummy data on the overlay — must be OFF when streaming</div>
      </div>
      <label class="toggle">
        <input type="checkbox" id="demo_mode" onchange="updateDemoStyle(this.checked)">
        <span class="track"></span>
      </label>
    </div>
    <div style="display:grid;grid-template-columns:1fr auto;gap:8px;align-items:end;">
      <div class="field" style="margin-bottom:0">
        <label>Home team name</label>
        <input type="text" id="home_team">
      </div>
      <div class="field" style="margin-bottom:0">
        <label>Scorebar abbrev.</label>
        <input type="text" id="home_abbrev" placeholder="e.g. HOME" maxlength="6"
               style="width:70px;text-transform:uppercase;font-weight:700;">
      </div>
    </div>
    <p class="hint" style="margin-top:3px;margin-bottom:10px;">
      Abbreviation overrides the full name on the scorebar (max 6 chars). Leave blank to auto-shorten.
    </p>
    <div style="display:grid;grid-template-columns:1fr auto;gap:8px;align-items:end;">
      <div class="field" style="margin-bottom:0">
        <label>Opposition name</label>
        <input type="text" id="away_team" placeholder="e.g. Opposition CC">
      </div>
      <div class="field" style="margin-bottom:0">
        <label>Scorebar abbrev.</label>
        <input type="text" id="away_abbrev" placeholder="OPP" maxlength="6"
               style="width:70px;text-transform:uppercase;font-weight:700;">
      </div>
    </div>
    <p class="hint" style="margin-top:3px;margin-bottom:10px;">
      e.g. "OPP", "TAUN", "EXTR" — shown in the coloured end blocks of the scorebar.
    </p>
    <div class="field">
      <label>Max overs per innings</label>
      <input type="number" id="max_overs" min="1" max="100" style="width:90px">
    </div>

    <div class="field" style="padding:10px;background:#0a1f0a;border:1px solid #1e4a1e;border-radius:5px;margin-bottom:10px;">
      <label style="color:#4caf50;">&#9679; PCS Pro output folder <span style="color:#3d7a3d;font-size:10px;font-weight:400;">— recommended data source</span></label>
      <input type="text" id="pcs_output_folder" placeholder="e.g. C:/Users/Scorer/Documents/Cricket Matches/_Scoreboards/Output">
      <p class="hint" style="color:#3d7a3d;">Paste the scorer's PCS Pro output folder. Gives batter names, bowler, run rate — updated every ball. No internet needed.</p>
    </div>
    <div class="toggle-row" style="margin-top:8px;">
      <div>
        <div class="toggle-label">Use widget as fallback</div>
        <div class="toggle-desc">Poll PlayCricket widget if PCS file not found. Disable if PCS is always in use.</div>
      </div>
      <label class="toggle"><input type="checkbox" id="use_widget"><span class="track"></span></label>
    </div>
    <div class="field">
      <label>Toss / match notes</label>
      <textarea id="match_notes" placeholder="e.g. Home won toss, elected to bat"></textarea>
      <p class="hint">Shown in the status segment of the scorebar</p>
    </div>
  </div>

  <!-- Kit colours -->
  <div class="card">
    <h2>Kit Colours</h2>
    <div class="field">
      <label>Home kit</label>
      <div class="colour-row">
        <div class="colour-swatch" id="home-swatch"></div>
        <input type="text" class="colour-hex" id="home_colour" maxlength="7">
      </div>
      <div class="presets" id="home-presets"></div>
    </div>
    <div class="field" style="margin-top:12px">
      <label>Away kit</label>
      <div class="colour-row">
        <div class="colour-swatch" id="away-swatch"></div>
        <input type="text" class="colour-hex" id="away_colour" maxlength="7">
      </div>
      <div class="presets" id="away-presets"></div>
    </div>
    <div class="preview-bar" id="colour-preview">
      <div class="preview-home" id="prev-home">HOME</div>
      <div class="preview-centre">147-4 &nbsp; 28.3/50</div>
      <div class="preview-away" id="prev-away">OPP</div>
      <div class="preview-bowler" id="prev-bowler">Harrison 1-34</div>
    </div>

    <!-- Logos folder -->
    <div class="field" style="margin-top:14px;">
      <label>Logos folder <span style="color:#3d5a7a;font-weight:400;">(optional — leave blank to use logos/ next to server.py)</span></label>
      <input type="text" id="logos_folder" placeholder="e.g. /Users/yourname/Documents/CricketStream/logos">
      <p class="hint">Folder containing club badge PNG files named by PlayCricket club ID (e.g. 12345.png)</p>
    </div>

    <div class="field" style="margin-top:10px;">
      <label>Headshots folder <span style="color:#3d5a7a;font-weight:400;">(optional — leave blank to use headshots/ next to server.py)</span></label>
      <input type="text" id="headshots_folder" placeholder="e.g. /Users/yourname/Documents/CricketStream/headshots">
      <p class="hint">Player photo PNGs named by player name. Spaces, dots and case don't matter (e.g. <code>p.smith.jpg</code>, <code>P SMITH.png</code> and <code>psmith.jpg</code> all match the scorebar name "P SMITH").</p>
    </div>

    <div class="field" style="margin-top:10px;">
      <label>Season batting stats <span style="color:#3d5a7a;font-weight:400;">(pulled from PlayCricket, cached for the day)</span></label>
      <button class="btn btn-test" onclick="refreshSeasonStats()" style="width:100%;margin-top:4px;">&#128202; Refresh season stats from PlayCricket</button>
      <span id="season-stats-status" style="font-size:12px;color:#3d5a7a;display:block;margin-top:8px;">Builds player averages/best/innings from this season's scorecards. Click once before the match to pre-load. Uses your PlayCricket API key.</span>
    </div>

    <div class="field" style="margin-top:10px;">
      <label>Socials folder <span style="color:#3d5a7a;font-weight:400;">(optional — leave blank to use socials/ next to server.py)</span></label>
      <input type="text" id="socials_folder" placeholder="e.g. /Users/yourname/Documents/CricketStream/socials">
      <p class="hint">Match photos for social posts (JPG/PNG). The match report tool lists these so you can attach one.</p>
    </div>

    <div class="field" style="margin-top:10px;">
      <label>Drinks break over <span style="color:#3d5a7a;font-weight:400;">(weather card shows at end of this over)</span></label>
      <input type="number" id="drinks_over" placeholder="25" min="1" max="49">
      <p class="hint">Set to 0 to disable. Weather card shows automatically at the end of this over.</p>
    </div>

    <!-- Club Badge Status -->
    <div style="margin-top:18px;border-top:1px solid rgba(255,255,255,0.07);padding-top:16px;">
      <label style="font-size:11px;font-weight:700;color:#3d5a7a;text-transform:uppercase;letter-spacing:1.5px;display:block;margin-bottom:12px;">Club Badges</label>
      <div class="half-grid" style="gap:12px;">

        <!-- Home badge -->
        <div style="display:flex;align-items:center;gap:10px;background:#0d1e35;border-radius:8px;padding:10px 12px;border:1px solid rgba(255,255,255,0.07);">
          <div id="badge-home-circle" style="width:44px;height:44px;border-radius:50%;background:#1a2a3a;border:2px solid #2a3a4a;flex-shrink:0;overflow:hidden;display:flex;align-items:center;justify-content:center;">
            <img id="badge-home-img" src="" style="width:100%;height:100%;object-fit:contain;display:none" onerror="badgeError('home')">
            <span id="badge-home-icon" style="font-size:18px;color:#3d5a7a;">⚪</span>
          </div>
          <div style="min-width:0;">
            <div style="font-size:12px;font-weight:700;color:#e8edf2;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;" id="badge-home-label">Home badge</div>
            <div style="font-size:11px;margin-top:2px;" id="badge-home-status">Checking...</div>
            <div style="font-size:10px;color:#3d5a7a;margin-top:1px;" id="badge-home-id"></div>
          </div>
        </div>

        <!-- Away badge -->
        <div style="display:flex;align-items:center;gap:10px;background:#0d1e35;border-radius:8px;padding:10px 12px;border:1px solid rgba(255,255,255,0.07);">
          <div id="badge-away-circle" style="width:44px;height:44px;border-radius:50%;background:#1a2a3a;border:2px solid #2a3a4a;flex-shrink:0;overflow:hidden;display:flex;align-items:center;justify-content:center;">
            <img id="badge-away-img" src="" style="width:100%;height:100%;object-fit:contain;display:none" onerror="badgeError('away')">
            <span id="badge-away-icon" style="font-size:18px;color:#3d5a7a;">⚪</span>
          </div>
          <div style="min-width:0;">
            <div style="font-size:12px;font-weight:700;color:#e8edf2;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;" id="badge-away-label">Away badge</div>
            <div style="font-size:11px;margin-top:2px;" id="badge-away-status">Checking...</div>
            <div style="font-size:10px;color:#3d5a7a;margin-top:1px;" id="badge-away-id"></div>
          </div>
        </div>

      </div>

      <!-- Manual badge picker -->
      <div class="half-grid" style="margin-top:12px;gap:12px;">
        <div>
          <label style="font-size:11px;color:#3d5a7a;display:block;margin-bottom:4px;">Home badge file</label>
          <select id="home-logo-pick" onchange="applyBadgePick('home')" style="width:100%;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:7px;font-size:12px;box-sizing:border-box;">
            <option value="">— pick a logo —</option>
          </select>
        </div>
        <div>
          <label style="font-size:11px;color:#3d5a7a;display:block;margin-bottom:4px;">Away badge file</label>
          <select id="away-logo-pick" onchange="applyBadgePick('away')" style="width:100%;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:7px;font-size:12px;box-sizing:border-box;">
            <option value="">— pick a logo —</option>
          </select>
        </div>
      </div>
      <p class="hint" style="margin-top:8px;">Manually set each side's badge from your logos folder — handy when PlayCricket didn't supply the opposition's club ID. Picking a file sets that side's badge immediately.</p>
    </div>
  </div>

  <!-- Squad roster (shirt number → player) -->
  <div class="card">
    <label>Squad Roster — tells brothers apart</label>
    <p class="hint" style="margin-bottom:8px;">One player per line as <b>number = Full Name</b> (e.g. <code>21 = Peter Smith</code>). The shirt number is unique per player, so this resolves same-surname brothers to the right stats and photo even when the scorebar shows only the surname. Photos can be named by number (<code>21.png</code>) or by name (<code>p.smith.png</code>). Enter it once per season.</p>
    <textarea id="roster-text" rows="8" placeholder="21 = Peter Smith&#10;7 = James Smith&#10;14 = Michael Smith&#10;9 = Karl Jones&#10;23 = Jamie Jones" style="width:100%;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:10px;font-size:13px;font-family:monospace;box-sizing:border-box;resize:vertical;"></textarea>
    <button class="btn btn-test" onclick="saveRoster()" style="margin-top:8px;width:100%;">Save roster</button>
    <span id="roster-status" style="margin-left:10px;font-size:12px;"></span>
  </div>

  <!-- Graphics toggles -->
  <div class="card">
    <h2>Graphics</h2>
    <div class="toggle-row">
      <div><div class="toggle-label">Fall of wicket card</div>
           <div class="toggle-desc">Slides up when a wicket falls</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_fow"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Partnership milestones</div>
           <div class="toggle-desc">Shows at 50, 100, 150... run partnerships</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_partnership"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Batting lineup card</div>
           <div class="toggle-desc">Appears at start of each innings</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_lineup"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Boundary / six flash</div>
           <div class="toggle-desc">FOUR! and SIX! banner on boundaries</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_boundary_flash"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Player milestones</div>
           <div class="toggle-desc">Gold graphic at 50 and 100 runs</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_milestones"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Innings summary card</div>
           <div class="toggle-desc">Top scorers &amp; bowlers at end of innings</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_innings_summary"><span class="track"></span></label>
    </div>
    <hr>
    <div style="font-size:10px;color:#3d5a7a;text-transform:uppercase;letter-spacing:1.5px;
                font-weight:600;margin:6px 0 4px;">PCS Pro live features</div>

    <div class="toggle-row">
      <div><div class="toggle-label">Over summary card</div>
           <div class="toggle-desc">Slides up at end of each over with runs &amp; bowler figures</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_over_summary"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Partnership display</div>
           <div class="toggle-desc">Current partnership runs &amp; balls — bottom left</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_partnership_display"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Run rate trend</div>
           <div class="toggle-desc">Bar chart of runs per over — top left corner</div></div>
      <label class="toggle"><input type="checkbox" id="graphics_runrate_trend"><span class="track"></span></label>
    </div>
  </div>

  <!-- Instant replay -->
  <div class="card">
    <h2>Instant Replay</h2>
    <p class="section-note">
      Requires OBS WebSocket enabled (Tools → WebSocket Server Settings)
      and a scene named <strong style="color:#e8edf2">Replay</strong> with a
      Media Source named <strong style="color:#e8edf2">ReplayClip</strong>.
      Also enable the Replay Buffer in OBS Settings → Output.
    </p>
    <div class="toggle-row">
      <div><div class="toggle-label">Enable instant replay</div>
           <div class="toggle-desc">Auto-triggers on wickets &amp; boundaries</div></div>
      <label class="toggle"><input type="checkbox" id="replay_enabled"><span class="track"></span></label>
    </div>
    <div class="toggle-row">
      <div><div class="toggle-label">Replay on centuries</div>
           <div class="toggle-desc">Always triggers a replay when a batter reaches 100+</div></div>
      <label class="toggle"><input type="checkbox" id="replay_enabled" disabled checked style="opacity:0.4"></label>
    </div>
    <div class="toggle-row" style="margin-bottom:10px">
      <div><div class="toggle-label">Replay on fifties</div>
           <div class="toggle-desc">Also replay when a batter reaches 50, 150, 200...</div></div>
      <label class="toggle"><input type="checkbox" id="replay_on_fifty"><span class="track"></span></label>
    </div>
    <hr>
    <div class="field">
      <label>OBS WebSocket password</label>
      <input type="text" id="obs_password" placeholder="Set in OBS → Tools → WebSocket Server">
    </div>
    <div class="half-grid">
      <div class="field">
        <label>OBS host</label>
        <input type="text" id="obs_host" placeholder="localhost">
      </div>
      <div class="field">
        <label>Port</label>
        <input type="number" id="obs_port" placeholder="4455" style="width:100%">
      </div>
    </div>
    <div class="half-grid">
      <div class="field">
        <label>Main scene name</label>
        <input type="text" id="obs_main_scene" placeholder="Main">
      </div>
      <div class="field">
        <label>Replay scene name</label>
        <input type="text" id="obs_replay_scene" placeholder="Replay">
      </div>
    </div>
    <hr>
    <div class="field">
      <label>Camera RTSP URL <span style="color:#3d5a7a;font-weight:400;">(adds the match camera to OBS for you)</span></label>
      <input type="text" id="camera_rtsp_url" placeholder="rtsp://user:pass@192.168.1.50:554/stream">
    </div>
    <div class="field">
      <label>Camera source name</label>
      <input type="text" id="obs_camera_name" placeholder="Cricket Camera">
    </div>
    <button class="btn btn-test" onclick="addCameraToObs()" style="width:100%;">&#128247; Add camera to OBS</button>
    <div id="camera-msg" style="margin-top:8px;font-size:12px;min-height:16px;"></div>
    <div class="field">
      <label>Replay folder path (leave blank to auto-detect)</label>
      <input type="text" id="replay_folder" placeholder="C:/Users/You/Videos/Replays">
      <p class="hint">Where OBS saves replay buffer clips. Max 100 clips — oldest deleted automatically.</p>
    </div>
    <div class="field">
      <label>Replay scene duration (seconds)</label>
      <input type="number" id="replay_duration" min="5" max="60" style="width:90px">
      <p class="hint">How long to stay on the Replay scene before returning</p>
    </div>
    <button class="btn btn-test" onclick="testReplay()">&#9654; Test replay now</button>
  </div>

  <!-- YouTube title updater -->
  <div class="card">
    <h2>YouTube Title</h2>
    <p class="section-note">
      Automatically updates your YouTube stream title when you click the button.
      Requires a one-time Google OAuth setup — see instructions below.
    </p>
    <div class="field">
      <label>Title template</label>
      <input type="text" id="youtube_title_template" placeholder="LIVE: {home} vs {away}">
      <p class="hint">{home} and {away} are replaced with the team names automatically</p>
    </div>
    <button class="btn btn-test" onclick="updateYouTubeTitle()" style="margin-top:6px;">&#9654; Update YouTube title now</button>
    <div style="margin-top:10px;padding:10px;background:#0a1628;border-radius:5px;font-size:11px;color:#3d5a7a;line-height:1.6;">
      <strong style="color:#5b9bd5;">One-time setup:</strong><br>
      1. Go to <a href="https://console.cloud.google.com" target="_blank" style="color:#5b9bd5;">console.cloud.google.com</a><br>
      2. Create a project → Enable YouTube Data API v3<br>
      3. Create OAuth2 credentials (Desktop app) → Download JSON<br>
      4. Rename file to <strong style="color:#e8edf2;">yt_credentials.json</strong> and place in the same folder as server.py<br>
      5. Run: <code style="color:#e8edf2;">pip install google-api-python-client google-auth-oauthlib</code><br>
      6. Click the button above — a browser window will open to authorise once
    </div>
  </div>

  <!-- Weather widget -->
  <div class="card">
    <h2>Weather Widget</h2>
    <p class="section-note">Shows current conditions at your ground in the top-left corner of the stream. Use during rain delays.</p>
    <div class="btn-row" style="margin-top:4px;">
      <button class="btn btn-test" onclick="showWeather()" style="flex:1;">&#9728; Show weather</button>
      <button class="btn btn-test" onclick="hideWeather()" style="flex:1;background:#3a1a1a;">&#10005; Hide weather</button>
      <button class="btn btn-test" onclick="showScorecard()" style="flex:1;">&#9783; Show scorecard</button>
      <button class="btn btn-test" onclick="showPlayerCards()" style="flex:1;">&#9786; Show player cards</button>
    </div>
    <div id="weather-preview" style="margin-top:10px;padding:10px;background:#0a1628;border-radius:5px;font-size:12px;color:#5b9bd5;">Click "Show weather" to fetch current conditions</div>
  </div>

  <!-- Match report & social posts -->
  <div class="card" style="grid-column:1/-1;border-color:#2a6496;">
    <h2>&#128221; Match Report &amp; Social Posts</h2>
    <p class="section-note">Generate an AI-written match report or a short social media post from the match so far. Needs an Anthropic API key (set in the AI Commentary card). Best run at the end of the match.</p>
    <div class="btn-row" style="margin-top:4px;">
      <button class="btn btn-test" onclick="generateReport('report')" style="flex:1;">&#128240; Generate match report</button>
      <button class="btn btn-test" onclick="generateReport('social')" style="flex:1;">&#128241; Generate social post</button>
    </div>
    <textarea id="report-output" placeholder="Generated text will appear here — edit freely before copying." style="width:100%;margin-top:12px;min-height:140px;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:10px;font-size:13px;line-height:1.5;resize:vertical;box-sizing:border-box;"></textarea>
    <div style="display:flex;gap:8px;margin-top:8px;align-items:center;">
      <button class="btn btn-test" onclick="copyReport()" style="flex:0 0 auto;">&#128203; Copy</button>
      <span id="report-status" style="font-size:12px;color:#3d5a7a;"></span>
    </div>
    <div id="social-photos" style="margin-top:12px;"></div>

    <!-- Instagram result graphic -->
    <div style="margin-top:18px;padding-top:16px;border-top:1px solid rgba(255,255,255,0.08);">
      <h2 style="font-size:15px;margin:0 0 4px;">&#128247; Instagram Result Graphic</h2>
      <p class="section-note">Builds a ready-to-post 1080&times;1350 image using a photo from your socials folder as the backdrop, with the result and key match facts laid over it. Needs an Anthropic API key.</p>
      <div class="field" style="margin-top:8px;">
        <label>Match <span style="color:#3d5a7a;font-weight:400;">(pick a past result, or use the streamed match)</span></label>
        <div class="split-grid">
          <select id="ig-match" onchange="onMatchPick()" style="flex:1;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:8px;font-size:13px;box-sizing:border-box;">
            <option value="">Streamed match (live data)</option>
          </select>
          <button class="btn btn-test" onclick="loadRecentMatches()" style="white-space:nowrap;">&#8635; Load results</button>
        </div>
      </div>
      <div class="field" style="margin-top:8px;">
        <label>Backdrop photo <span style="color:#3d5a7a;font-weight:400;">(optional — newest photo used if blank)</span></label>
        <select id="ig-photo" style="width:100%;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:8px;font-size:13px;box-sizing:border-box;">
          <option value="">Newest photo in socials folder</option>
        </select>
      </div>
      <button class="btn btn-test" onclick="generateIgGraphic()" style="width:100%;margin-top:10px;">&#127919; Generate Instagram graphic</button>
      <div id="ig-youth-note" style="display:none;margin-top:8px;padding:8px 10px;background:#2a2410;border:1px solid #5c4a18;border-radius:6px;font-size:11px;color:#d2c08a;line-height:1.5;">
        &#9888;&#65039; Youth match selected. Backdrop photos are drawn from <b>socials/youth</b> (use club/ground stock images, not photos of children), and player names use the discreet first-name + initial form. Check your club's safeguarding/consent policy before posting.
      </div>
      <span id="ig-status" style="font-size:12px;color:#3d5a7a;display:block;margin-top:8px;"></span>
      <div id="ig-preview-wrap" style="display:none;margin-top:12px;">
        <img id="ig-preview" style="width:100%;border-radius:8px;border:1px solid rgba(255,255,255,0.1);" alt="Instagram graphic preview">
        <div class="btn-row" style="margin-top:8px;">
          <a id="ig-download" class="btn btn-test" download="bbcc_instagram.png" style="flex:1;text-align:center;text-decoration:none;">&#11015;&#65039; Download image</a>
          <button class="btn btn-test" onclick="copyIgCaption()" style="flex:1;">&#128203; Copy caption</button>
        </div>
        <textarea id="ig-caption" placeholder="Caption will appear here." style="width:100%;margin-top:8px;min-height:70px;background:#0a1628;border:1px solid rgba(255,255,255,0.1);border-radius:6px;color:#e8edf2;padding:10px;font-size:13px;line-height:1.5;resize:vertical;box-sizing:border-box;"></textarea>
      </div>
    </div>

    <div class="card">
      <h2>&#128202; Match data</h2>
      <p class="section-note">Every ball is logged to a local database (<code>match_data.db</code>) as you stream — your own season-long dataset. The current over is rewritten live so scorer edits are captured; "Reconcile" then pulls the final scorecard from PlayCricket as the authoritative record.</p>
      <div id="data-status" style="margin:8px 0;padding:10px;background:#0a1628;border-radius:6px;font-size:13px;color:#9fb3c8;">Loading…</div>
      <div style="display:flex;gap:8px;flex-wrap:wrap;">
        <button class="btn btn-test" onclick="reconcileMatch()" style="flex:1;">&#10003; Reconcile latest</button>
        <button class="btn btn-test" onclick="exportLatestCsv()" style="flex:1;">&#8681; Export CSV</button>
        <button class="btn btn-test" onclick="loadDataStatus()" style="flex:1;">&#8635; Refresh</button>
      </div>
      <div id="data-msg" style="margin-top:8px;font-size:12px;min-height:16px;"></div>
    </div>
  </div>

  <!-- Highlights + PCS monitor side by side (full width row) -->
  <div class="side-grid" style="grid-column:1/-1;">

    <!-- Highlights compiler -->
    <div class="card">
      <h2>Post-match Highlights</h2>
      <p class="section-note">
        Stitches all saved replay clips into a single highlights reel.
        Requires FFmpeg — <a href="https://ffmpeg.org/download.html" target="_blank" style="color:#5b9bd5;">download here</a> and add to PATH.
      </p>
      <div style="display:flex;align-items:center;gap:10px;margin-bottom:10px;padding:8px 10px;
                  background:#0a1628;border-radius:5px;font-size:12px;">
        <span style="color:#5b9bd5;">&#127916;</span>
        <span id="clip-count-display" style="color:#e8edf2;">— clips saved</span>
        <span style="color:#3d5a7a;margin-left:auto;font-size:10px;overflow:hidden;
                     text-overflow:ellipsis;max-width:180px;" id="clip-folder-display"></span>
      </div>
      <div class="field">
        <label>Max clips to keep (rolling buffer)</label>
        <input type="number" id="max_clips" min="10" max="500" style="width:90px">
        <p class="hint">Oldest deleted automatically once limit reached. Default: 100.</p>
      </div>
      <button class="btn btn-test" onclick="compileHighlights()" style="margin-top:4px;">
        &#127916; Compile highlights reel
      </button>
      <div id="highlights-status" style="margin-top:8px;font-size:12px;color:#3d5a7a;"></div>
    </div>

    <!-- PCS Live Data monitor -->
    <div class="card" id="pcs-monitor">
      <h2>&#128200; PCS Pro — Live Data Feed</h2>
      <p class="section-note">Live view of what is being received from the PCS Pro output file and sent to the overlay.</p>
      <div id="pcs-monitor-body">
        <div style="color:#3d5a7a;font-size:12px;padding:10px 0;">
          Waiting for PCS data — set the output folder in the Match card above and start scoring.
        </div>
      </div>
    </div>

  </div>

</div>

<!-- ── Live status bar ── -->
<div id="live-status-bar" style="display:grid;grid-template-columns:1fr auto;gap:10px;margin-bottom:14px;">

  <div id="live-status" style="
    display:flex; align-items:center; gap:12px;
    background:#0a1628; border:1px solid #1e3550; border-radius:8px;
    padding:12px 16px;
  ">
    <div id="status-dot" style="
      width:10px; height:10px; border-radius:50%;
      background:#4a4a4a; flex-shrink:0;
      transition:background .3s;
    "></div>
    <div style="flex:1">
      <div id="status-line1" style="font-size:13px;font-weight:600;color:#e8edf2;">Checking connection...</div>
      <div id="status-line2" style="font-size:11px;color:#3d5a7a;margin-top:2px;"></div>
    </div>
    <div id="status-source" style="
      font-size:10px; font-weight:700; text-transform:uppercase;
      letter-spacing:1.2px; padding:3px 8px; border-radius:3px;
      background:#1e3550; color:#5b9bd5; flex-shrink:0;
    "></div>
  </div>

  <!-- Health strip: one dot per subsystem, refreshed every 10s from /health -->
  <div id="health-strip" style="
    display:flex; flex-wrap:wrap; gap:14px; align-items:center;
    background:#0a1628; border:1px solid #1e3550; border-radius:8px;
    padding:8px 14px; margin:8px 0 0 0; font-size:11px; color:#9fb3c8;
  ">
    <span style="color:#3d5a7a;font-weight:700;text-transform:uppercase;letter-spacing:1px;font-size:9px;">Health</span>
    <span class="hl-item"><span class="hl-dot" id="hl-feed"></span>Feed: <b id="hl-feed-t">—</b></span>
    <span class="hl-item"><span class="hl-dot" id="hl-stats"></span>Stats: <b id="hl-stats-t">—</b></span>
    <span class="hl-item"><span class="hl-dot" id="hl-photos"></span>Photos: <b id="hl-photos-t">—</b></span>
    <span class="hl-item"><span class="hl-dot" id="hl-badges"></span>Badges: <b id="hl-badges-t">—</b></span>
    <span class="hl-item"><span class="hl-dot" id="hl-ai"></span>AI: <b id="hl-ai-t">—</b></span>
  </div>
  <style>
    .hl-item { display:inline-flex; align-items:center; gap:6px; }
    .hl-item b { color:#e8edf2; font-weight:600; }
    .hl-dot { width:8px; height:8px; border-radius:50%; background:#4a4a4a; display:inline-block; }
    .hl-ok   { background:#3fb950 !important; }
    .hl-warn { background:#d29922 !important; }
    .hl-bad  { background:#f85149 !important; }
  </style>

  <div style="
    display:flex; flex-direction:column; align-items:center; justify-content:center;
    background:#0a1628; border:1px solid #1e3550; border-radius:8px;
    padding:10px 16px; min-width:110px; gap:2px;
  ">
    <div style="font-size:10px;text-transform:uppercase;letter-spacing:1.2px;color:#3d5a7a;font-weight:600;">Uptime</div>
    <div id="server-uptime" style="font-size:16px;font-weight:700;color:#4caf50;">—</div>
    <div id="server-clips" style="font-size:10px;color:#3d5a7a;">— clips</div>
  </div>

</div>

<button class="btn" onclick="saveState()">Save &amp; Update Overlay</button>
<div class="status" id="status"></div>

<div class="links">
  <span>OBS browser source:</span>
  <a href="http://localhost:5001/overlay" target="_blank">http://localhost:5001/overlay</a>
</div>

<script>
const PRESETS = """ + json.dumps(KIT_PRESETS) + """;

// ── Auth helpers ─────────────────────────────────────────────
// sessionStorage key 'bbcc_token' holds the control token for this tab session.
// null  = not yet set (show login overlay on load)
// ''    = user skipped (no auth configured on server)
// value = token to send as Authorization: Bearer
function getToken() { return sessionStorage.getItem('bbcc_token'); }

async function apiFetch(url, opts) {
  const t = getToken();
  const baseHeaders = (opts && opts.headers) ? opts.headers : {'Content-Type':'application/json'};
  const h = Object.assign({}, baseHeaders);
  if (t) h['Authorization'] = 'Bearer ' + t;
  const r = await fetch(url, Object.assign({}, opts, {headers: h}));
  if (r.status === 401) showLoginOverlay('Session expired — log in again');
  return r;
}

function showLoginOverlay(msg) {
  const ov  = document.getElementById('auth-overlay');
  const err = document.getElementById('auth-error');
  if (ov)  ov.style.display  = 'flex';
  if (err) { err.textContent = msg || ''; err.style.display = msg ? 'block' : 'none'; }
  setTimeout(function(){ const i = document.getElementById('auth-input'); if (i) i.focus(); }, 50);
}

async function doLogin() {
  const val = (document.getElementById('auth-input').value || '').trim();
  if (!val) {
    const e = document.getElementById('auth-error');
    if (e) { e.textContent = 'Enter the password first'; e.style.display = 'block'; }
    return;
  }
  try {
    const res = await fetch('/login', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({password: val})
    });
    const d = await res.json();
    if (d.ok) {
      sessionStorage.setItem('bbcc_token', d.session_token || '');
      document.getElementById('auth-overlay').style.display = 'none';
    } else {
      const e = document.getElementById('auth-error');
      if (e) { e.textContent = d.error || 'Wrong password'; e.style.display = 'block'; }
    }
  } catch (err) {
    const e = document.getElementById('auth-error');
    if (e) { e.textContent = 'Could not reach server'; e.style.display = 'block'; }
  }
}

async function doSkip() {
  try {
    const res = await fetch('/login', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({password: ''})
    });
    const d = await res.json();
    if (d.ok) {
      sessionStorage.setItem('bbcc_token', d.session_token || '');
      document.getElementById('auth-overlay').style.display = 'none';
    } else {
      const e = document.getElementById('auth-error');
      if (e) { e.textContent = 'Password required — enter it above'; e.style.display = 'block'; }
    }
  } catch (err) {
    sessionStorage.setItem('bbcc_token', '');
    document.getElementById('auth-overlay').style.display = 'none';
  }
}

// Show login overlay on first load if no token is stored for this session.
// Script is at the bottom of <body> so the DOM is ready — no DOMContentLoaded needed.
if (getToken() === null) {
  document.getElementById('auth-overlay').style.display = 'flex';
}

function buildPresets(cId, hexId, swId) {
  const container = document.getElementById(cId);
  const hex       = document.getElementById(hexId);
  const sw        = document.getElementById(swId);
  PRESETS.forEach(p => {
    const btn = document.createElement('div');
    btn.className = 'preset-btn'; btn.title = p.name;
    btn.style.background = p.hex;
    btn.onclick = () => {
      hex.value = p.hex; updateSwatch(sw, p.hex); updatePreview();
      container.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
    };
    container.appendChild(btn);
  });
  hex.addEventListener('input', () => { updateSwatch(sw, hex.value); updatePreview(); });
}

function updateSwatch(sw, hex) {
  if (/^#[0-9a-fA-F]{6}$/.test(hex)) sw.style.background = hex;
}

function shade(hex, amt) {
  const n = parseInt((hex||'#333').replace('#',''),16);
  const r = Math.min(255,Math.max(0,(n>>16)+amt));
  const g = Math.min(255,Math.max(0,((n>>8)&0xff)+amt));
  const b = Math.min(255,Math.max(0,(n&0xff)+amt));
  return '#'+[r,g,b].map(v=>v.toString(16).padStart(2,'0')).join('');
}

function updatePreview() {
  const home = document.getElementById('home_colour').value || '#1a3a5c';
  const away = document.getElementById('away_colour').value || '#7b2d2d';
  document.getElementById('prev-home').style.background   = home;
  document.getElementById('prev-away').style.background   = away;
  document.getElementById('prev-bowler').style.background = shade(away,-30);
  const homeAb = (document.getElementById('home_abbrev')?.value||'').trim().toUpperCase();
  const awayAb = (document.getElementById('away_abbrev')?.value||'').trim().toUpperCase();
  const hn = homeAb || (document.getElementById('home_team').value||'BRIDES').split(' ')[0].toUpperCase().slice(0,6);
  const an = awayAb || (document.getElementById('away_team').value||'OPP').split(' ')[0].toUpperCase().slice(0,6);
  document.getElementById('prev-home').textContent = hn;
  document.getElementById('prev-away').textContent = an;
}

const FIELDS = ['home_team','away_team','home_abbrev','away_abbrev','home_colour','away_colour',
  'pcs_output_folder','match_url','match_notes','replay_folder',
  'obs_host','obs_password','obs_main_scene','obs_replay_scene','camera_rtsp_url','obs_camera_name',
  'youtube_title_template','anthropic_api_key','playcricket_api_key','ground_filter','umpire1_name','umpire2_name','competition_name','pc_match_id','replay_motto','logos_folder','headshots_folder','socials_folder'];
const NUM_FIELDS = ['max_overs','poll_interval','obs_port','replay_duration','max_clips'];
const BOOL_FIELDS = ['demo_mode','graphics_fow','graphics_partnership',
  'graphics_lineup','graphics_boundary_flash','graphics_milestones',
  'graphics_innings_summary','graphics_commentary','graphics_commentary_over','graphics_over_summary','graphics_partnership_display','graphics_runrate_trend','graphics_player_card','replay_enabled','replay_on_fifty','use_widget'];

function badgeError(side) {
  document.getElementById('badge-'+side+'-img').style.display = 'none';
  document.getElementById('badge-'+side+'-icon').textContent = '—';
  document.getElementById('badge-'+side+'-icon').style.color = '#3d5a7a';
  document.getElementById('badge-'+side+'-status').textContent = 'No badge file found';
  document.getElementById('badge-'+side+'-status').style.color = '#c87800';
  document.getElementById('badge-'+side+'-circle').style.borderColor = '#4a3800';
}

function checkBadges(homeId, awayId, homeAbbrev, awayAbbrev) {
  function check(side, id, abbrev) {
    if (!id) {
      document.getElementById('badge-'+side+'-status').textContent = 'No club ID set';
      document.getElementById('badge-'+side+'-status').style.color = '#3d5a7a';
      document.getElementById('badge-'+side+'-icon').textContent = '?';
      return;
    }
    document.getElementById('badge-'+side+'-label').textContent = abbrev || (side === 'home' ? 'Home' : 'Away');
    document.getElementById('badge-'+side+'-id').textContent = 'ID: ' + id;
    document.getElementById('badge-'+side+'-status').textContent = 'Checking...';
    document.getElementById('badge-'+side+'-status').style.color = '#3d5a7a';

    const img = document.getElementById('badge-'+side+'-img');
    img.onload = function() {
      img.style.display = 'block';
      document.getElementById('badge-'+side+'-icon').style.display = 'none';
      document.getElementById('badge-'+side+'-status').textContent = '✓ Badge found';
      document.getElementById('badge-'+side+'-status').style.color = '#4caf50';
      document.getElementById('badge-'+side+'-circle').style.borderColor = '#2a5a2a';
    };
    img.onerror = function() { badgeError(side); };
    img.src = '/logo/' + id + '?t=' + Date.now();
  }
  check('home', homeId, homeAbbrev);
  check('away', awayId, awayAbbrev);
}

function parseRosterText(t) {
  var r = {};
  (t||'').split('\\n').forEach(function(line){
    var m = line.match(/^\\s*(\\d+)\\s*[=,:\\-\\s]\\s*(.+?)\\s*$/);
    if (m) r[m[1]] = m[2].trim();
  });
  return r;
}
function rosterToText(r) {
  return Object.keys(r||{}).sort(function(a,b){return (+a)-(+b);})
    .map(function(k){ return k + ' = ' + r[k]; }).join('\\n');
}
async function loadRoster() {
  try {
    var s = await (await apiFetch('/state')).json();
    var ta = document.getElementById('roster-text');
    if (ta) ta.value = rosterToText(s.roster || {});
  } catch(e){}
}
async function saveRoster() {
  var ta = document.getElementById('roster-text');
  var st = document.getElementById('roster-status');
  if (!ta) return;
  var roster = parseRosterText(ta.value);
  try {
    await apiFetch('/state', {method:'POST', headers:{'Content-Type':'application/json'},
                           body: JSON.stringify({roster: roster})});
    var n = Object.keys(roster).length;
    if (st){ st.textContent = '\u2713 Saved ' + n + ' players'; st.style.color = '#34c759'; }
  } catch(e){ if (st){ st.textContent = 'Error: ' + e.message; st.style.color = '#f44336'; } }
}

async function loadLogoOptions() {  try {
    const d = await (await apiFetch('/logos/list')).json();
    const opts = '<option value="">— pick a logo —</option>'
      + (d.logos||[]).map(function(l){ return '<option value="'+l.id+'">'+l.file+'</option>'; }).join('');
    ['home','away'].forEach(function(side){
      const sel = document.getElementById(side+'-logo-pick');
      if (sel) { const cur = sel.value; sel.innerHTML = opts; sel.value = cur; }
    });
  } catch(e) {}
}

async function applyBadgePick(side) {
  const sel = document.getElementById(side+'-logo-pick');
  if (!sel || !sel.value) return;
  const field = (side === 'home') ? 'home_club_id' : 'away_club_id';
  try {
    // Partial POST is safe now that /state merges. Preserves everything else.
    await apiFetch('/state', {method:'POST', headers:{'Content-Type':'application/json'},
                           body: JSON.stringify({[field]: sel.value})});
    const s = await (await apiFetch('/state')).json();
    checkBadges(s.home_club_id||'', s.away_club_id||'', s.home_abbrev||'Home', s.away_abbrev||'Away');
    showStatus((side==='home'?'Home':'Away')+' badge set to '+sel.value, 'ok');
  } catch(e) { showStatus('Could not set badge: '+e.message, 'err'); }
}

async function loadState() {
  const s = await (await apiFetch('/state')).json();
  FIELDS.forEach(id => { const el=document.getElementById(id); if(el) el.value=s[id]||''; });
  NUM_FIELDS.forEach(id => { const el=document.getElementById(id); if(el) el.value=s[id]||''; });
  BOOL_FIELDS.forEach(id => { const el=document.getElementById(id); if(el) el.checked=!!s[id]; });
  updateSwatch(document.getElementById('home-swatch'), s.home_colour||'#1a3a5c');
  updateSwatch(document.getElementById('away-swatch'), s.away_colour||'#7b2d2d');
  updatePreview();
  updateDemoStyle(!!s.demo_mode);
  checkBadges(s.home_club_id||'', s.away_club_id||'', s.home_abbrev||'Home', s.away_abbrev||'Away');
  const clubName = s.home_team || 'Your Club';
  const clubBadgeEl = document.getElementById('panel-club-badge');
  const clubNameEl  = document.getElementById('panel-club-name');
  if (clubBadgeEl) clubBadgeEl.textContent = (s.home_abbrev || clubName || 'CC').slice(0,2).toUpperCase();
  if (clubNameEl)  clubNameEl.textContent  = clubName;
  document.title = 'Stream Control — ' + clubName;
}

async function saveState() {
  const state = {};
  FIELDS.forEach(id     => { const el=document.getElementById(id); if(el) state[id]=el.value.trim(); });
  NUM_FIELDS.forEach(id => { const el=document.getElementById(id); if(el) state[id]=parseInt(el.value)||0; });
  BOOL_FIELDS.forEach(id=> { const el=document.getElementById(id); if(el) state[id]=el.checked; });
  // Defaults for blank required fields
  if (!state.home_team) state.home_team = 'Home CC';
  if (!state.away_team) state.away_team = 'Opposition CC';
  if (!state.obs_host)  state.obs_host  = 'localhost';
  if (!state.obs_port)  state.obs_port  = 4455;
  if (!state.obs_main_scene)   state.obs_main_scene   = 'Main';
  if (!state.obs_replay_scene) state.obs_replay_scene = 'Replay';
  if (!state.replay_duration)  state.replay_duration  = 18;
  try {
    const res = await apiFetch('/state', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(state)});
    showStatus(res.ok ? 'Saved — overlay updates on its next poll' : 'Error saving', res.ok?'ok':'err');
    if (res.ok) updatePreview();
  } catch(e) { showStatus('Could not reach server: '+e.message,'err'); }
}

async function testReplay() {
  showStatus('Triggering test replay...','ok');
  try {
    const res = await apiFetch('/replay', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:'Test'})});
    const d = await res.json();
    showStatus(d.ok ? 'Replay triggered — check OBS' : ('Error: '+(d.error||'unknown'))  , d.ok?'ok':'err');
  } catch(e) { showStatus('Could not reach server','err'); }
}

function showStatus(msg, cls) {
  const el = document.getElementById('status');
  el.textContent=msg; el.className='status '+cls;
  setTimeout(()=>el.className='status',6000);
}

async function updateYouTubeTitle() {
  const tmpl = document.getElementById('youtube_title_template').value;
  showStatus('Updating YouTube title...','ok');
  try {
    const res = await apiFetch('/youtube/update', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title:tmpl})});
    const d = await res.json();
    showStatus(d.ok ? d.title : 'Error: '+(d.error||'unknown'), d.ok?'ok':'err');
  } catch(e) { showStatus('Could not reach server','err'); }
}

async function showWeather() {
  const prev = document.getElementById('weather-preview');
  prev.textContent = 'Fetching...'; prev.style.color='#5b9bd5';
  try {
    const res  = await apiFetch('/weather');
    const data = await res.json();
    if (data.ok) {
      prev.textContent = `${data.icon} ${data.temp}°C · ${data.description} · Wind ${data.wind} · Humidity ${data.humidity}%`;
      prev.style.color = '#4caf50';
      await apiFetch('/weather/show', {method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    } else {
      prev.textContent = 'Error: ' + (data.error||'unknown');
      prev.style.color = '#f44336';
    }
  } catch(e) { prev.textContent = 'Could not reach server'; prev.style.color='#f44336'; }
}

async function showScorecard() {
  try {
    await apiFetch('/scorecard/show', {method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    showStatus('Scorecard showing on overlay','ok');
  } catch(e) { showStatus('Could not reach server','err'); }
}
async function showPlayerCards() {
  try {
    await apiFetch('/cards/show', {method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    showStatus('Player cards showing on overlay','ok');
  } catch(e) { showStatus('Could not reach server','err'); }
}

// ── OBS camera auto-add ──
async function addCameraToObs() {
  const msg = document.getElementById('camera-msg');
  const url = (document.getElementById('camera_rtsp_url') || {}).value || '';
  if (!url.trim()) { if (msg) { msg.textContent = 'Enter the camera RTSP URL first'; msg.style.color = '#d29922'; } return; }
  if (msg) { msg.textContent = 'Adding camera to OBS…'; msg.style.color = '#5b9bd5'; }
  try {
    await saveState();   // make sure the URL/name are persisted before we call OBS
    const name = (document.getElementById('obs_camera_name') || {}).value || '';
    const d = await (await apiFetch('/obs/add_camera', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:url.trim(), name:name.trim()})})).json();
    if (msg) { msg.textContent = (d.ok ? '\u2713 ' : '\u2717 ') + (d.message || ''); msg.style.color = d.ok ? '#3fb950' : '#f85149'; }
  } catch(e) { if (msg) { msg.textContent = 'Could not reach server'; msg.style.color = '#f85149'; } }
}

// ── Match data (ball-by-ball database) ──
let _latestMatchId = '';
async function loadDataStatus() {
  const box = document.getElementById('data-status');
  if (!box) return;
  try {
    const d = await (await apiFetch('/data/status')).json();
    if (!d.ok) { box.textContent = 'Database error: ' + (d.error || ''); return; }
    _latestMatchId = (d.recent && d.recent[0]) ? d.recent[0].match_id : '';
    let html = '<b>' + (d.balls || 0).toLocaleString() + '</b> balls logged across <b>'
             + (d.matches || 0) + '</b> match(es).';
    if (d.recent && d.recent.length) {
      html += '<div style="margin-top:6px;font-size:12px;line-height:1.6;">'
        + d.recent.slice(0, 5).map(function(m){
            return (m.date || '') + ' &nbsp;' + (m.home || '') + ' v ' + (m.away || '')
              + (m.reconciled ? ' <span style="color:#3fb950;">&#10003; reconciled</span>'
                              : ' <span style="color:#d29922;">live only</span>');
          }).join('<br>')
        + '</div>';
    }
    box.innerHTML = html;
  } catch(e) { box.textContent = 'Could not reach server'; }
}
async function reconcileMatch() {
  const msg = document.getElementById('data-msg');
  if (msg) { msg.textContent = 'Reconciling with PlayCricket…'; msg.style.color = '#5b9bd5'; }
  try {
    const body = JSON.stringify(_latestMatchId ? { match_id: _latestMatchId } : {});
    const d = await (await apiFetch('/data/reconcile', {method:'POST',headers:{'Content-Type':'application/json'},body:body})).json();
    if (d.ok) { if (msg) { msg.textContent = 'Reconciled \u2713 ' + (d.result || '') + ' (' + d.innings + ' innings)'; msg.style.color = '#3fb950'; } loadDataStatus(); }
    else { if (msg) { msg.textContent = d.error || 'Could not reconcile'; msg.style.color = '#f85149'; } }
  } catch(e) { if (msg) { msg.textContent = 'Could not reach server'; msg.style.color = '#f85149'; } }
}
function exportLatestCsv() {
  const msg = document.getElementById('data-msg');
  if (!_latestMatchId) { if (msg) { msg.textContent = 'No match logged yet'; msg.style.color = '#d29922'; } return; }
  window.open('/data/export?match_id=' + encodeURIComponent(_latestMatchId), '_blank');
}
async function hideWeather() {
  await apiFetch('/weather/hide', {method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
  const prev = document.getElementById('weather-preview');
  prev.textContent = 'Weather widget hidden'; prev.style.color='#3d5a7a';
}

async function compileHighlights() {
  const st = document.getElementById('highlights-status');
  st.textContent = 'Compiling... this may take a minute.'; st.style.color='#5b9bd5';
  try {
    const res = await apiFetch('/highlights', {method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    const d = await res.json();
    st.textContent = d.ok ? 'Done — ' + d.output : 'Error: ' + (d.error||'unknown');
    st.style.color = d.ok ? '#4caf50' : '#f44336';
  } catch(e) { st.textContent = 'Could not reach server'; st.style.color='#f44336'; }
}

async function generateReport(type) {
  const out = document.getElementById('report-output');
  const st  = document.getElementById('report-status');
  out.value = 'Generating ' + (type==='social'?'social post':'match report') + '...';
  st.textContent = ''; st.style.color = '#3d5a7a';
  try {
    const url = '/report/generate' + (type==='social' ? '?type=social' : '');
    const d = await (await apiFetch(url)).json();
    if (d.ok) {
      out.value = d.text;
      st.textContent = 'Generated ' + new Date().toLocaleTimeString() + ' · edit before posting';
      st.style.color = '#4caf50';
      if (type==='social') loadSocialPhotos();
    } else {
      out.value = '';
      st.textContent = 'Error: ' + (d.error||'unknown');
      st.style.color = '#f44336';
    }
  } catch(e) {
    out.value = '';
    st.textContent = 'Could not reach server';
    st.style.color = '#f44336';
  }
}

function copyReport() {
  const out = document.getElementById('report-output');
  const st  = document.getElementById('report-status');
  out.select();
  try {
    document.execCommand('copy');
    st.textContent = 'Copied to clipboard';
    st.style.color = '#4caf50';
  } catch(e) {
    if (navigator.clipboard) navigator.clipboard.writeText(out.value);
    st.textContent = 'Copied';
    st.style.color = '#4caf50';
  }
}

async function loadSocialPhotos() {
  const box = document.getElementById('social-photos');
  if (!box) return;
  try {
    const d = await (await apiFetch('/social/photos')).json();
    if (d.photos && d.photos.length) {
      box.innerHTML = '<p class="hint" style="margin-bottom:6px;">Photos in your socials folder ('
        + d.photos.length + ') — attach one when posting:</p>'
        + '<div style="display:flex;flex-wrap:wrap;gap:6px;">'
        + d.photos.map(function(p){ return '<span style="font-size:11px;background:#0d1e35;'
          + 'border:1px solid rgba(255,255,255,0.08);border-radius:4px;padding:3px 8px;color:#5b9bd5;">'
          + p + '</span>'; }).join('')
        + '</div>';
    } else {
      box.innerHTML = '<p class="hint">No photos found in socials folder. Add JPG/PNG files to use with posts.</p>';
    }
  } catch(e) { box.innerHTML = ''; }
  // Also populate the Instagram backdrop dropdown
  try {
    const sel = document.getElementById('ig-photo');
    if (sel) {
      const d2 = await (await apiFetch('/social/photos')).json();
      const cur = sel.value;
      sel.innerHTML = '<option value="">Newest photo in socials folder</option>'
        + (d2.photos || []).map(function(p){
            return '<option value="'+p+'">'+p+'</option>'; }).join('');
      sel.value = cur;
    }
  } catch(e) {}
}

let _matchTeams = {};   // match_id -> team_key (1st/2nd/3rd), for per-team photo folders
async function loadRecentMatches() {
  const sel = document.getElementById('ig-match');
  const status = document.getElementById('ig-status');
  if (status) { status.textContent = 'Loading recent results from PlayCricket…'; status.style.color = '#5b9bd5'; }
  try {
    const d = await (await apiFetch('/social/recent')).json();
    if (!d.ok) { if (status) { status.textContent = d.error || 'Could not load results'; status.style.color = '#f85149'; } return; }
    _matchTeams = {};
    (d.matches || []).forEach(function(m){ _matchTeams[m.id] = m.team_key || ''; });
    sel.innerHTML = '<option value="">Streamed match (live data)</option>'
      + (d.matches || []).map(function(m){
          var tag = m.team_key ? (' [' + m.team_key + ' XI]') : '';
          return '<option value="' + m.id + '">' + (m.label || m.id) + tag + '</option>';
        }).join('');
    if (status) { status.textContent = (d.matches || []).length + ' recent match(es) loaded — pick one above'; status.style.color = '#3fb950'; }
  } catch(e) { if (status) { status.textContent = 'Could not reach server'; status.style.color = '#f85149'; } }
}

// When the chosen match changes, reload the backdrop list from that team's photo folder
async function onMatchPick() {
  const matchEl = document.getElementById('ig-match');
  const team = matchEl ? (_matchTeams[matchEl.value] || '') : '';
  const note = document.getElementById('ig-youth-note');
  if (note) note.style.display = (team === 'youth') ? 'block' : 'none';
  try {
    const sel = document.getElementById('ig-photo');
    if (!sel) return;
    const d = await (await apiFetch('/social/photos?team=' + encodeURIComponent(team))).json();
    const label = team ? ('Newest photo in socials/' + team) : 'Newest photo in socials folder';
    sel.innerHTML = '<option value="">' + label + '</option>'
      + (d.photos || []).map(function(p){ return '<option value="'+p+'">'+p+'</option>'; }).join('');
  } catch(e) {}
}

async function generateIgGraphic() {
  const status  = document.getElementById('ig-status');
  const wrap    = document.getElementById('ig-preview-wrap');
  const photoEl = document.getElementById('ig-photo');
  const photo   = photoEl ? photoEl.value : '';
  const matchEl = document.getElementById('ig-match');
  const matchId = matchEl ? matchEl.value : '';
  const team    = matchEl ? (_matchTeams[matchId] || '') : '';
  if (status) { status.textContent = matchId ? 'Building graphic from PlayCricket result…' : 'Generating graphic (distilling match facts with Claude)...'; status.style.color = '#8060a0'; }
  try {
    const params = [];
    if (photo)   params.push('photo='   + encodeURIComponent(photo));
    if (matchId) params.push('match_id=' + encodeURIComponent(matchId));
    if (team)    params.push('team='     + encodeURIComponent(team));
    const url = '/social/image/generate' + (params.length ? ('?' + params.join('&')) : '');
    const res = await apiFetch(url);   // GET — the endpoint reads ?photo= and ignores any body
    // Parse defensively: an empty or non-JSON body should give a readable error,
    // not a cryptic "Unexpected end of JSON input".
    const text = await res.text();
    let d;
    try {
      d = JSON.parse(text);
    } catch (parseErr) {
      const snippet = (text || '').trim().slice(0, 200);
      throw new Error('Server returned ' + res.status + ' ' + res.statusText
        + (snippet ? (' — ' + snippet) : ' (empty response)'));
    }
    if (d.ok) {
      const bust = '/social/image/latest?t=' + Date.now();   // cache-bust the preview
      const img = document.getElementById('ig-preview');
      const dl  = document.getElementById('ig-download');
      const cap = document.getElementById('ig-caption');
      if (img) img.src = bust;
      if (dl)  dl.href = bust;
      if (cap) cap.value = d.caption || '';
      if (wrap) wrap.style.display = 'block';
      if (status) {
        status.textContent = 'Done' + (d.used_photo ? (' — backdrop: ' + d.used_photo) : ' — plain backdrop (no photo found)');
        status.style.color = '#4caf50';
      }
    } else {
      if (status) { status.textContent = 'Error: ' + (d.error || 'unknown'); status.style.color = '#f44336'; }
    }
  } catch(e) {
    if (status) { status.textContent = 'Request failed: ' + e; status.style.color = '#f44336'; }
  }
}

function copyIgCaption() {
  const cap = document.getElementById('ig-caption');
  if (!cap) return;
  cap.select();
  try { document.execCommand('copy'); } catch(e) {}
  if (navigator.clipboard) { navigator.clipboard.writeText(cap.value).catch(function(){}); }
  const status = document.getElementById('ig-status');
  if (status) { status.textContent = 'Caption copied to clipboard'; status.style.color = '#4caf50'; }
}

async function refreshSeasonStats() {
  const s = document.getElementById('season-stats-status');
  if (s) { s.textContent = 'Pulling this season\u2019s scorecards from PlayCricket (one-off, please wait)\u2026'; s.style.color = '#8060a0'; }
  try {
    const res = await apiFetch('/player/stats/refresh?force=1');   // GET — endpoint lives in do_GET
    const text = await res.text();
    let d;
    try { d = JSON.parse(text); }
    catch (pe) { throw new Error('Server returned ' + res.status + ' ' + res.statusText + (text ? (' — ' + text.slice(0,200)) : ' (empty)')); }
    if (s) {
      if (d.error) {
        s.textContent = 'Done with a warning: ' + d.error + ' (built ' + (d.players||0) + ' players from ' + (d.matches_used||0) + ' matches)';
        s.style.color = '#c47d00';
      } else {
        s.textContent = 'Loaded ' + (d.players||0) + ' players from ' + (d.matches_used||0) + ' matches (' + (d.api_calls||0) + ' API calls). Cached for today.';
        s.style.color = '#4caf50';
      }
    }
  } catch(e) {
    if (s) { s.textContent = 'Request failed: ' + e; s.style.color = '#f44336'; }
  }
}

async function testCommentary() {
  const prev = document.getElementById('commentary-preview');
  const meta = document.getElementById('commentary-meta');
  if (prev) { prev.textContent = 'Generating...'; prev.style.color='#8060a0'; }
  try {
    const res = await apiFetch('/commentary/test', {method:'POST',
      headers:{'Content-Type':'application/json'}, body:'{}'});
    const d = await res.json();
    if (d.ok) {
      if (prev) { prev.textContent = d.text; prev.style.color='#e8e0f0'; }
      if (meta) meta.textContent = `Generated ${new Date().toLocaleTimeString()} · Claude Haiku`;
    } else {
      if (prev) { prev.textContent = 'Error: '+(d.error||'unknown'); prev.style.color='#f44336'; }
    }
  } catch(e) { if(prev) { prev.textContent='Could not reach server'; prev.style.color='#f44336'; } }
}

async function pollCommentary() {
  try {
    const data = await (await apiFetch('/commentary/latest')).json();
    const prev = document.getElementById('commentary-preview');
    const meta = document.getElementById('commentary-meta');
    if (data.text && prev) {
      prev.textContent = data.text;
      prev.style.color = '#e8e0f0';
      if (meta) meta.textContent = `Last generated: ${new Date().toLocaleTimeString()} · Claude Haiku`;
    }
  } catch(e) {}
}

setInterval(pollCommentary, 5000);

// ── PCS Live Data Monitor ──────────────────────────────────
async function updatePCSMonitor() {
  const body = document.getElementById('pcs-monitor-body');
  if (!body) return;
  try {
    const ctrl = new AbortController();
    const tout = setTimeout(() => ctrl.abort(), 8000);
    const res  = await apiFetch('/live', {signal: ctrl.signal});
    clearTimeout(tout);
    const data = await res.json();

    if (data.source !== 'pcs' || !data.state) {
      const source = data.source === 'widget' ? 'Widget (score only — no PCS)' :
                     data.source === 'pcs'    ? 'PCS Pro' : 'None';
      body.innerHTML = `<div class="pcs-status-bar">
        <div class="pcs-dot"></div>
        <span style="font-size:12px;color:#3d5a7a;">
          Source: <strong style="color:#e8edf2;">${source}</strong>
          — Set the PCS output folder and start scoring to see live data here.
        </span></div>`;
      return;
    }

    const s = data.state;
    const b1sr = s.batter1?.balls > 0 ? ((s.batter1.runs / s.batter1.balls)*100).toFixed(0) : '—';
    const b2sr = s.batter2?.balls > 0 ? ((s.batter2.runs / s.batter2.balls)*100).toFixed(0) : '—';

    // Build ball ticker from last_ball if available
    let ballsHtml = '';
    if (s.last_ball && !s.last_ball.startsWith('{{')) {
      const ballClass = s.last_ball.toLowerCase().includes('6') ? 'six'
                      : s.last_ball.toLowerCase().includes('4') && !s.last_ball.includes('14') ? 'four'
                      : s.last_ball.toLowerCase().includes('w') ? 'wicket' : '';
      ballsHtml = `<div class="pcs-ball-row">
        <div style="font-size:10px;color:#3d5a7a;align-self:center;margin-right:4px;">Last ball:</div>
        <div class="pcs-ball ${ballClass}">${s.last_ball.replace(/[^0-9WwNnBb]/g,'').slice(0,2)||'·'}</div>
        <div style="font-size:11px;color:#8aa8c4;align-self:center;margin-left:4px;">${s.last_ball}</div>
      </div>`;
    }

    body.innerHTML = `
      <div class="pcs-status-bar">
        <div class="pcs-dot live"></div>
        <span style="font-size:12px;color:#4caf50;font-weight:600;">PCS Pro connected — live data</span>
        <span style="font-size:10px;color:#3d5a7a;margin-left:auto;">Updated: ${new Date().toLocaleTimeString()}</span>
      </div>

      <div class="pcs-grid">
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Batting</div>
          <div class="pcs-cell-value" style="font-size:13px;">${s.battingTeamName||'—'}</div>
        </div>
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Score</div>
          <div class="pcs-cell-value">${s.score}-${s.wickets}</div>
          <div class="pcs-cell-sub">${s.overs} overs · RR ${parseFloat(s.statusText?.replace('RR:','')||0).toFixed(2)||'—'}</div>
        </div>
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Innings</div>
          <div class="pcs-cell-value">${s.innings||1}</div>
          <div class="pcs-cell-sub">of ${s.max_overs||50} overs</div>
        </div>
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Partnership</div>
          <div class="pcs-cell-value">${s.partnership_runs??'—'}</div>
          <div class="pcs-cell-sub">${s.partnership_balls??'—'} balls</div>
        </div>
      </div>

      <div class="pcs-grid">
        <div class="pcs-cell live">
          <div class="pcs-cell-label">&#9998; ${s.batter1?.onStrike ? 'On strike' : 'Batter 1'}</div>
          <div class="pcs-cell-value">${s.batter1?.name||'—'}</div>
          <div class="pcs-cell-sub">${s.batter1?.runs??0} (${s.batter1?.balls??0}b) · SR ${b1sr}</div>
        </div>
        <div class="pcs-cell live">
          <div class="pcs-cell-label">&#9998; ${!s.batter1?.onStrike ? 'On strike' : 'Batter 2'}</div>
          <div class="pcs-cell-value">${s.batter2?.name||'—'}</div>
          <div class="pcs-cell-sub">${s.batter2?.runs??0} (${s.batter2?.balls??0}b) · SR ${b2sr}</div>
        </div>
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Bowler</div>
          <div class="pcs-cell-value">${s.bowler?.name||'—'}</div>
          <div class="pcs-cell-sub">${s.bowler?.overs||0}-${s.bowler?.maidens||0}-${s.bowler?.runs||0}-${s.bowler?.wickets||0}</div>
        </div>
        <div class="pcs-cell">
          <div class="pcs-cell-label">Bowling</div>
          <div class="pcs-cell-value" style="font-size:13px;">${s.bowlingTeamName||'—'}</div>
        </div>
      </div>

      ${ballsHtml}

      ${s.targetRuns > 0 ? `
      <div class="pcs-grid" style="margin-top:8px;">
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Target</div>
          <div class="pcs-cell-value">${s.targetRuns}</div>
        </div>
        <div class="pcs-cell live">
          <div class="pcs-cell-label">Required</div>
          <div class="pcs-cell-value">${s.runsRequired}</div>
          <div class="pcs-cell-sub">RRR ${s.req_rate||'—'}</div>
        </div>
      </div>` : ''}
    `;
  } catch(e) {
    const body = document.getElementById('pcs-monitor-body');
    if (body) body.innerHTML = '<div style="color:#f44336;font-size:12px;padding:8px 0;">Cannot reach server</div>';
  }
}

updatePCSMonitor();
setInterval(updatePCSMonitor, 3000);

function updateDemoStyle(on) {
  const row   = document.getElementById('demo-row');
  const label = document.getElementById('demo-label');
  if (!row || !label) return;
  if (on) {
    row.style.background  = '#1a0a0a';
    row.style.borderColor = '#6a1a1a';
    label.style.color     = '#f44336';
    label.textContent     = 'Demo mode ON';
  } else {
    row.style.background  = '#0a1a0a';
    row.style.borderColor = '#1a4a1a';
    label.style.color     = '#4caf50';
    label.textContent     = 'Demo mode OFF';
  }
}

async function fetchTodaysMatch() {
  const key = document.getElementById('playcricket_api_key').value.trim();
  if (!key) { alert('Enter your API key first'); return; }

  // Save key first
  await saveState();

  const result_el  = document.getElementById('pc-api-result');
  const details_el = document.getElementById('pc-api-details');
  const grid_el    = document.getElementById('pc-api-grid');

  result_el.textContent  = "Fetching today's fixture...";
  result_el.style.color  = '#5b9bd5';

  try {
    const res  = await apiFetch('/match/fetch');
    const data = await res.json();

    if (!data.ok) {
      result_el.textContent = '✗ ' + (data.result?.error || 'Unknown error');
      result_el.style.color = '#f44336';
      details_el.style.display = 'none';
      return;
    }

    const r = data.result;
    result_el.style.color = '#4caf50';
    result_el.textContent = `✓ Found: ${r.home_team} vs ${r.away_team} — ${r.competition} (${r.match_date} ${r.match_time})`;

    // Show details grid
    const cells = [
      {label:'Opposition',  value: r.away_team},
      {label:'Abbreviation',value: r.away_abbrev},
      {label:'Competition', value: r.competition},
      {label:'Ground',      value: r.ground},
      {label:'Umpire 1',    value: r.umpire1 || '—'},
      {label:'Umpire 2',    value: r.umpire2 || '—'},
      {label:'Scorer',      value: r.scorer1 || '—'},
      {label:'Match ID',    value: r.match_id},
    ];
    grid_el.innerHTML = cells.map(c =>
      `<div class="pcs-cell live">
        <div class="pcs-cell-label">${c.label}</div>
        <div class="pcs-cell-value" style="font-size:13px;">${c.value}</div>
      </div>`
    ).join('');
    details_el.style.display = 'block';

    // Reload state so fields update
    await loadState();
    checkBadges('', '', r.home_team, r.away_team);  // badges re-check after loadState

    if (r.all_today > 1) {
      result_el.textContent += ` (${r.all_today} matches today — loaded first home match)`;
    }

  } catch(e) {
    result_el.textContent = '✗ Could not reach server: ' + e.message;
    result_el.style.color = '#f44336';
  }
}

buildPresets('home-presets','home_colour','home-swatch');
buildPresets('away-presets','away_colour','away-swatch');
document.getElementById('home_team').addEventListener('input',updatePreview);
document.getElementById('away_team').addEventListener('input',updatePreview);
document.getElementById('home_abbrev')?.addEventListener('input',updatePreview);
document.getElementById('away_abbrev')?.addEventListener('input',updatePreview);
loadState();
loadLogoOptions();
loadRoster();
loadDataStatus();

// ── Pre-match checklist ──────────────────────────────────────
const CHECKLIST = [
  { id:'obs',     label:'OBS is open',              hint:'Open OBS Studio' },
  { id:'buffer',  label:'Replay Buffer started',    hint:'Controls panel → Start Replay Buffer' },
  { id:'stream',  label:'Streaming started',        hint:'Controls panel → Start Streaming' },
  { id:'server',  label:'server.py is running',     hint:'Run: python.exe server.py' },
  { id:'opp',     label:'Opposition name set',      hint:'Enter opposition in the Match section' },
  { id:'pcs',     label:'PCS Pro output connected', hint:'Set PCS output folder in Match section' },
  { id:'scorer',  label:'Scorer has started PCS',   hint:'Scorer begins ball-by-ball on first delivery' },
  { id:'camera',  label:'Camera feed visible',      hint:'Check RTSP source is connected in OBS' },
];

function buildChecklist() {
  const saved = JSON.parse(localStorage.getItem('bbcc_checklist') || '{}');
  const grid  = document.getElementById('checklist-items');
  grid.innerHTML = '';
  CHECKLIST.forEach(item => {
    const checked = !!saved[item.id];
    const div = document.createElement('div');
    div.style.cssText = 'display:flex;align-items:center;gap:8px;cursor:pointer;padding:5px 8px;border-radius:5px;transition:background .15s;';
    div.style.background = checked ? 'rgba(76,175,80,0.1)' : 'rgba(255,255,255,0.03)';
    div.innerHTML = `
      <div style="width:18px;height:18px;border-radius:4px;border:2px solid ${checked?'#4caf50':'#2a4060'};
           background:${checked?'#4caf50':'transparent'};display:flex;align-items:center;justify-content:center;
           flex-shrink:0;transition:all .15s;">
        ${checked ? '<svg width=10 height=10 viewBox="0 0 10 10"><polyline points="1.5,5 4,7.5 8.5,2" stroke="#fff" stroke-width="1.8" fill="none" stroke-linecap="round"/></svg>' : ''}
      </div>
      <div>
        <div style="font-size:12px;font-weight:600;color:${checked?'#4caf50':'#e8edf2'};">${item.label}</div>
        <div style="font-size:10px;color:#3d5a7a;">${item.hint}</div>
      </div>`;
    div.onclick = () => toggleCheck(item.id);
    div.onmouseenter = () => { if (!saved[item.id]) div.style.background='rgba(255,255,255,0.06)'; };
    div.onmouseleave = () => { div.style.background = saved[item.id]?'rgba(76,175,80,0.1)':'rgba(255,255,255,0.03)'; };
    grid.appendChild(div);
  });
  // Update header count
  const done  = Object.values(saved).filter(Boolean).length;
  const total = CHECKLIST.length;
  const pct   = Math.round((done/total)*100);
  const bar   = document.querySelector('#checklist-bar span');
  if (bar) bar.textContent = `Pre-match checklist (${done}/${total})`;
  // Flash green border when all done
  const box = document.getElementById('checklist-bar');
  box.style.borderColor = done === total ? '#4caf50' : '#1e3550';
}

function toggleCheck(id) {
  const saved = JSON.parse(localStorage.getItem('bbcc_checklist') || '{}');
  saved[id] = !saved[id];
  localStorage.setItem('bbcc_checklist', JSON.stringify(saved));
  buildChecklist();
}

function resetChecklist() {
  localStorage.removeItem('bbcc_checklist');
  buildChecklist();
}

// Auto-check items we can detect programmatically
async function autoCheck() {
  const saved = JSON.parse(localStorage.getItem('bbcc_checklist') || '{}');
  let changed = false;

  // server.py running — if we're here, it is
  if (!saved['server']) { saved['server'] = true; changed = true; }

  try {
    const s = await (await apiFetch('/state')).json();
    const hasOpp = !!(s.away_team && s.away_team !== 'Opposition CC');
    const hasPCS = !!(s.pcs_output_folder && s.pcs_output_folder.trim());
    if (hasOpp !== !!saved['opp']) { saved['opp'] = hasOpp; changed = true; }
    if (hasPCS !== !!saved['pcs']) { saved['pcs'] = hasPCS; changed = true; }
  } catch(e) {}

  // Live data coming in → scorer has started
  try {
    const live = await (await apiFetch('/live')).json();
    if (live.state && live.state.score > 0 && !saved['scorer']) {
      saved['scorer'] = true; changed = true;
    }
  } catch(e) {}

  if (changed) {
    localStorage.setItem('bbcc_checklist', JSON.stringify(saved));
    buildChecklist();
  }
}

buildChecklist();
setInterval(autoCheck, 10000);
autoCheck();

// ── Live status polling ───────────────────────────────────────
const dot   = document.getElementById('status-dot');
const line1 = document.getElementById('status-line1');
const line2 = document.getElementById('status-line2');
const src   = document.getElementById('status-source');

async function checkLiveStatus() {
  console.log('[CP] checkLiveStatus called at', new Date().toLocaleTimeString());
  const s = await (await apiFetch('/state').catch(()=>null))?.json().catch(()=>null);
  if (!s) { console.error('checkLiveStatus: /state fetch failed'); return; }
  console.log('checkLiveStatus: state ok, fetching /live...');

  try {
    const ctrl = new AbortController();
    const tout = setTimeout(() => ctrl.abort(), 8000);
    const res  = await apiFetch('/live', {signal: ctrl.signal});
    clearTimeout(tout);
    const data = await res.json();
    console.log('checkLiveStatus: /live source =', data.source, 'state =', !!data.state);

    if (data.source === 'pcs') {
      if (!data.state) { line1.textContent='PCS connected — no data yet'; return; }
      dot.style.background='#4caf50';
      line1.textContent=`PCS LIVE — ${data.state.battingTeamName} ${data.state.score}-${data.state.wickets} (${data.state.overs} ov)`;
      line2.textContent=`Ball-by-ball · ${new Date().toLocaleTimeString()}`;
      src.textContent='PCS';src.style.background='#0d3a1a';src.style.color='#4caf50';
    } else if (data.source === 'widget') {
      if (data.state) {
        // Live match found via widget
        const st = data.state;
        dot.style.background = '#4caf50';
        dot.style.animation  = 'livePulse 1.5s infinite';
        line1.textContent    = `LIVE — ${st.battingTeamName || 'Batting'} ${st.score}-${st.wickets} (${st.overs} ov)`;
        line2.textContent    = `Last updated: ${new Date().toLocaleTimeString()}`;
        src.textContent      = 'Widget';
        src.style.background = '#0d3a1a';
        src.style.color      = '#4caf50';
      } else {
        // Widget reachable but no live match today
        dot.style.background = '#c87800';
        dot.style.animation  = 'none';
        line1.textContent    = s.demo_mode ? 'Demo mode active' : 'Connected — no live match today';
        line2.textContent    = `Widget reachable · Club ID ${data.club_id || 'not set'}`;
        src.textContent      = 'Widget';
        src.style.background = '#2a1a00';
        src.style.color      = '#c87800';
      }

    } else if (data.source === 'none' || !data.source) {
      dot.style.background = '#f5c842';
      dot.style.animation  = 'none';
      line1.textContent    = 'Connected — waiting for match to start';
      line2.textContent    = 'Score a ball in NV Play to begin';
      src.textContent      = 'PCS';
      src.style.background = '#2a2000';
      src.style.color      = '#f5c842';
    }
  } catch(e) {
    dot.style.background = '#f44336';
    dot.style.animation  = 'none';
    line1.textContent    = e.name === 'AbortError' ? 'Server slow — retrying' : 'Cannot reach server';
    line2.textContent    = 'Make sure server.py is running';
    src.textContent      = 'Offline';
    src.style.background = '#3a0d0d';
    src.style.color      = '#f44336';
  }
}

// Inject pulse animation
const style = document.createElement('style');
style.textContent = '@keyframes livePulse { 0%,100%{opacity:1} 50%{opacity:0.4} }';
document.head.appendChild(style);

checkLiveStatus();
setInterval(checkLiveStatus, 15000);

// ── Server status / uptime polling ──────────────────────
async function checkServerStatus() {
  try {
    const res  = await apiFetch('/status');
    const data = await res.json();
    document.getElementById('server-uptime').textContent = data.uptime || '—';
    document.getElementById('server-uptime').style.color = '#4caf50';
    document.getElementById('server-clips').textContent  =
      `${data.clip_count} / ${data.max_clips} clips`;
    // Update clip count display in highlights card
    const cd = document.getElementById('clip-count-display');
    const cf = document.getElementById('clip-folder-display');
    if (cd) cd.textContent = `${data.clip_count} clip${data.clip_count!==1?'s':''} saved`;
    if (cf) cf.textContent = data.folder || '';
  } catch(e) {
    document.getElementById('server-uptime').textContent = 'offline';
    document.getElementById('server-uptime').style.color = '#f44336';
  }
}

checkServerStatus();
setInterval(checkServerStatus, 10000);

// ── Health strip ──────────────────────────────────────────
function hlSet(id, cls, text) {
  var d = document.getElementById(id); var t = document.getElementById(id + '-t');
  if (d) d.className = 'hl-dot ' + cls;
  if (t) t.textContent = text;
}
async function pollHealth() {
  try {
    var h = await (await apiFetch('/health')).json();
    // Feed: green = fresh file, amber = found but stale (or demo), red = missing
    if (h.demo_mode) { hlSet('hl-feed','hl-warn','demo mode'); }
    else if (!h.pcs.folder_set) { hlSet('hl-feed','hl-bad','no folder set'); }
    else if (!h.pcs.file_found) { hlSet('hl-feed','hl-bad','file not found'); }
    else if (h.pcs.fresh) { hlSet('hl-feed','hl-ok', h.pcs.age_sec + 's ago'); }
    else {
      var m = Math.floor(h.pcs.age_sec / 60);
      hlSet('hl-feed','hl-warn','stale (' + (m > 99 ? '99+' : m) + 'm)');
    }
    // Season stats
    if (h.stats.building) hlSet('hl-stats','hl-warn','building…');
    else if (h.stats.built) hlSet('hl-stats','hl-ok', h.stats.players + ' players');
    else if (h.stats.error) hlSet('hl-stats','hl-bad','error');
    else hlSet('hl-stats','hl-warn','not built');
    // Assets
    hlSet('hl-photos', h.assets.headshots > 0 ? 'hl-ok' : 'hl-warn', String(h.assets.headshots));
    hlSet('hl-badges', h.match.badges ? 'hl-ok' : 'hl-warn', h.match.badges ? 'both set' : 'incomplete');
    // AI key
    hlSet('hl-ai', h.keys.anthropic ? 'hl-ok' : 'hl-warn', h.keys.anthropic ? 'key set' : 'no key');
  } catch(e) {
    ['hl-feed','hl-stats','hl-photos','hl-badges','hl-ai'].forEach(function(id){ hlSet(id,'','—'); });
  }
}
pollHealth();
setInterval(pollHealth, 10000);
</script>
</body>
</html>
"""


# ── HTTP handler ──────────────────────────────────────────────

# ── Post-match highlights compiler ───────────────────────────
def compile_highlights(folder, output_path, max_clips=100):
    """
    Stitch all replay clips in folder into a highlights reel using FFmpeg.
    Clips are sorted by creation time (chronological match order).
    Adds a 0.5s black gap between clips for a clean broadcast feel.
    """
    import shutil
    if not shutil.which("ffmpeg"):
        return False, "FFmpeg not found — download from https://ffmpeg.org/download.html and add to PATH"

    patterns = ["*.mkv","*.mp4","*.flv","*.mov"]
    clips = []
    for p in patterns:
        clips.extend(glob.glob(os.path.join(folder, p)))

    # Exclude any existing highlights file
    clips = [c for c in clips if "highlights" not in os.path.basename(c).lower()]

    if not clips:
        return False, f"No replay clips found in {folder}"

    clips.sort(key=os.path.getmtime)
    print(f"  📎  Compiling {len(clips)} clips into highlights reel...")

    # Write FFmpeg concat file
    concat_file = os.path.join(folder, "_concat.txt")
    try:
        with open(concat_file, "w") as f:
            for clip in clips:
                # Use forward slashes for FFmpeg on Windows
                safe_path = clip.replace("\\", "/")
                f.write(f"file '{safe_path}'\n")
                f.write("duration 0.5\n")  # small gap between clips

        cmd = [
            "ffmpeg", "-y",
            "-f", "concat", "-safe", "0",
            "-i", concat_file,
            "-c:v", "libx264", "-crf", "23", "-preset", "fast",
            "-c:a", "aac", "-b:a", "128k",
            output_path
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
        if result.returncode != 0:
            return False, f"FFmpeg error: {result.stderr[-200:]}"
        return True, f"Highlights saved to {output_path} ({len(clips)} clips)"
    except subprocess.TimeoutExpired:
        return False, "FFmpeg timed out — too many clips or slow machine"
    except Exception as e:
        return False, str(e)
    finally:
        if os.path.exists(concat_file):
            os.remove(concat_file)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args): pass

    def _json(self, data, status=200):
        try:
            body = json.dumps(data).encode()
            self.send_response(status)
            self.send_header("Content-Type","application/json")
            self.send_header("Content-Length",str(len(body)))
            self.send_header("Access-Control-Allow-Origin","*")
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError): pass

    def _html(self, html):
        try:
            body = html.encode()
            self.send_response(200)
            self.send_header("Content-Type","text/html; charset=utf-8")
            self.send_header("Content-Length",str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError): pass

    def _file(self, path, mime):
        try:
            with open(path,"rb") as f: body=f.read()
            self.send_response(200)
            self.send_header("Content-Type",mime)
            self.send_header("Content-Length",str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except FileNotFoundError:
            self.send_response(404); self.end_headers()
            self.wfile.write(b"Not found")
        except (BrokenPipeError, ConnectionResetError):
            pass  # Client disconnected mid-response (e.g. OBS scene switch) — harmless

    def _check_token(self):
        """Returns True if the request carries a valid session token (or auth is disabled)."""
        if not _CLUB_PASSWORD:
            return True
        auth = self.headers.get("Authorization", "")
        provided = auth[7:] if auth.startswith("Bearer ") else ""
        if not provided:
            for part in self.headers.get("Cookie", "").split(";"):
                k, _, v = part.strip().partition("=")
                if k.strip() == "control_token":
                    provided = v.strip()
                    break
        if provided and _verify_session_token(provided):
            return True
        print(f"  ✗  Auth: 401 {self.path} [{self.address_string()}]")
        self._json({"ok": False, "error": "Unauthorized"}, status=401)
        return False

    def _handle_login(self, body):
        """POST /login — exchange club password for a signed session token."""
        if not _CLUB_PASSWORD:
            # Auth disabled — issue a token so the browser can proceed normally
            tok = _make_session_token() if _CONTROL_TOKEN else ""
            self._json({"ok": True, "session_token": tok})
            return
        try:
            pw = json.loads(body).get("password", "")
        except Exception:
            pw = ""
        if pw and hmac.compare_digest(pw.encode(), _CLUB_PASSWORD.encode()):
            tok = _make_session_token()
            print(f"  ✓  Login: session issued [{self.address_string()}]")
            self._json({"ok": True, "session_token": tok})
        else:
            print(f"  ✗  Login: bad password [{self.address_string()}]")
            self._json({"ok": False, "error": "Wrong password"}, status=401)

    def _check_rate_limit(self, path):
        """Returns True if the call is allowed; sends 429 and returns False if in cooldown."""
        cooldown = _RATE_LIMITS.get(path)
        if not cooldown:
            return True
        with _rate_limit_lock:
            last = _rate_limit_ts.get(path, 0)
            wait = cooldown - (time.time() - last)
            if wait > 0:
                self._json({"ok": False,
                            "error": f"Please wait {int(wait)+1}s before trying again"},
                           status=429)
                return False
            _rate_limit_ts[path] = time.time()
        return True

    def do_OPTIONS(self):
        self.send_response(200)
        self.send_header("Access-Control-Allow-Origin","*")
        self.send_header("Access-Control-Allow-Methods","GET,POST,OPTIONS")
        self.send_header("Access-Control-Allow-Headers","Content-Type,Authorization")
        self.end_headers()

    def do_GET(self):
        path = urlparse(self.path).path
        if path in ("/","/overlay"):
            self._file(os.path.join(os.path.dirname(os.path.abspath(__file__)),"overlay.html"),"text/html; charset=utf-8")
        elif path == "/control":
            self._html(CONTROL_HTML)
        elif path == "/state":
            st = load_state()
            # Add boolean flags so the overlay can gate features without seeing raw secrets
            st["anthropic_key_set"]   = bool(st.get("anthropic_api_key","").strip())
            st["playcricket_key_set"] = bool(st.get("playcricket_api_key","").strip())
            # SECURITY: never send stored secrets over HTTP. Responses carry CORS *
            # (the overlay may be loaded from OBS as a separate origin), which means any
            # web page open on this machine could read this endpoint. The control panel
            # shows the sentinel for a stored key; POSTing the sentinel back leaves the
            # stored value untouched (see POST /state), and posting "" clears it.
            for _k in SECRET_KEYS:
                if str(st.get(_k, "")).strip():
                    st[_k] = SECRET_SENTINEL
            self._json(st)
        elif path == "/weather":
            data = fetch_weather_data()
            self._json(data)

        elif path == "/commands":
            cmds = pop_commands()
            # Also include commentary if pending
            c = pop_commentary()
            cmds["commentary_text"]    = c["text"]
            cmds["commentary_pending"] = c["pending"]
            self._json(cmds)

        elif path == "/commentary/over":
            self._json(_over_commentary)

        elif path == "/commentary/latest":
            self._json({"text": _commentary["text"], "pending": _commentary["pending"]})

        elif path == "/logos/debug":
            s2      = load_state()
            lf      = s2.get("logos_folder","").strip()
            ldir    = (os.path.expanduser(lf) if lf
                       else os.path.join(os.path.dirname(
                            os.path.abspath(__file__)), "logos"))
            files   = []
            exists  = os.path.isdir(ldir)
            if exists:
                files = [f for f in os.listdir(ldir)
                         if not f.startswith(".")]
            self._json({
                "logos_folder_setting": lf,
                "resolved_path": ldir,
                "directory_exists": exists,
                "files": sorted(files),
                "home_club_id": s2.get("home_club_id",""),
                "away_club_id": s2.get("away_club_id",""),
                "headshots_folder": s2.get("headshots_folder",""),
                "drinks_over": s2.get("drinks_over",25),
            })

        elif path == "/pcs/debug":
            s2 = load_state()
            pcs_folder = s2.get("pcs_output_folder","").strip()
            raw = None
            found_path = None
            search_note = None
            if not pcs_folder:
                search_note = "No pcs_output_folder set in the control panel / config.ini."
            elif not os.path.isdir(os.path.expanduser(pcs_folder)):
                search_note = f"Folder does not exist: {pcs_folder}"
            else:
                # Use the SAME finder as the live feed so we locate whatever NV Play actually
                # writes — .json OR .xml OR a known filename (NV Play often writes JSON into a
                # .xml file). The old code only globbed *.json and missed those.
                found_path = find_pcs_output_file(os.path.expanduser(pcs_folder))
                if not found_path:
                    listing = []
                    try:
                        listing = sorted(os.listdir(os.path.expanduser(pcs_folder)))
                    except Exception:
                        pass
                    search_note = ("No PCS output file found (looked for known filenames and any "
                                   ".json/.xml modified in the last 10 minutes). Has the scorer "
                                   "written at least one ball? Files currently in the folder: "
                                   + (", ".join(listing) if listing else "(folder empty)"))
                else:
                    try:
                        with open(found_path, encoding="utf-8-sig") as f:
                            raw = json.load(f)
                    except Exception as e:
                        # Not JSON (e.g. the all-fields dump with no template, or XML).
                        # Return the raw text so we can read the available field names from it.
                        raw_text = None
                        try:
                            with open(found_path, encoding="utf-8-sig", errors="replace") as f:
                                raw_text = f.read()[:8000]   # cap to keep response sane
                        except Exception:
                            pass
                        raw = {"error": f"Found {os.path.basename(found_path)} but could not parse as JSON: {e}",
                               "raw_text": raw_text}
            # Diagnostics: surface the fields most likely to be misconfigured in NV Play.
            # Helps diagnose truncated names and missing dismissal detail without hunting
            # through the raw dump. A {{...}} value means NV Play did not recognise that field.
            diag = None
            if isinstance(raw, dict) and "error" not in raw:
                def _show(key):
                    v = raw.get(key, "(field absent)")
                    if isinstance(v, str) and v.startswith("{{"):
                        return f"NOT RECOGNISED BY NV PLAY ({v})"
                    return v
                diag = {
                    "names": {
                        "batter1_name": _show("batter1_name"),
                        "batter2_name": _show("batter2_name"),
                        "bowler_name":  _show("bowler_name"),
                        "note": "If a name here is shorter than reality (e.g. 'Harmiso' for "
                                "'Harmison'), NV Play itself is truncating it — check the player's "
                                "name in the NV Play team list and any scoreboard name-length limit.",
                    },
                    "wicket_fields": {
                        "batter1_howout (Wicket)":          _show("batter1_howout"),
                        "batter1_wickettype (WicketType)":  _show("batter1_wickettype"),
                        "batter1_wicketfielder (Fielder)":  _show("batter1_wicketfielder"),
                        "batter2_howout (Wicket)":          _show("batter2_howout"),
                        "batter2_wickettype (WicketType)":  _show("batter2_wickettype"),
                        "batter2_wicketfielder (Fielder)":  _show("batter2_wicketfielder"),
                        "note": "After a wicket, at least one set should show real values "
                                "(e.g. type='Caught', fielder='Jones', Wicket='Smith'). If they show "
                                "'NOT RECOGNISED', the field names in scoreboard.template don't "
                                "match this NV Play version — read the real names from the keys list "
                                "below and tell Claude so the template can be corrected.",
                    },
                    "ball_ticker": _show("last_ball"),
                }
            self._json({"diagnostics": diag,
                        "pcs_folder": pcs_folder,
                        "file_found": os.path.basename(found_path) if found_path else None,
                        "search_note": search_note,
                        "raw_pcs": raw,
                        "keys": sorted(raw.keys()) if isinstance(raw, dict) and "error" not in raw else None})

        elif path == "/report/generate":
            if not self._check_token(): return
            if not self._check_rate_limit(path): return
            rtype = "report"
            if "?" in self.path and "type=social" in self.path:
                rtype = "social"
            result = generate_match_report(rtype)
            self._json(result)

        elif path == "/data/status":
            self._json(db_status())

        elif path.startswith("/data/export"):
            from urllib.parse import parse_qs
            mid = parse_qs(urlparse(self.path).query).get("match_id", [""])[0].strip()
            if not mid:
                self._json({"ok": False, "error": "match_id required"}, status=400)
            else:
                csv_text = export_match_csv(mid)
                body = csv_text.encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/csv")
                self.send_header("Content-Disposition",
                                 f'attachment; filename="match_{mid}_balls.csv"')
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        elif path == "/social/photos":
            from urllib.parse import parse_qs
            tk = parse_qs(urlparse(self.path).query).get("team", [""])[0].strip()
            self._json({"photos": list_social_photos(tk), "team": tk})

        elif path == "/social/recent":
            # Recent completed matches for the post picker — works for away games too.
            rcfg = load_state()
            rkey = (rcfg.get("playcricket_api_key") or rcfg.get("api_token") or "").strip()
            rsite = str(rcfg.get("home_club_id") or rcfg.get("site_id") or "").strip()
            if not rkey or not rsite:
                self._json({"ok": False, "error": "Set your PlayCricket API key and club ID first",
                            "matches": []})
            else:
                try:
                    season = str(datetime.date.today().year)
                    self._json({"ok": True,
                                "matches": _pc_recent_matches(rkey, rsite, season, rcfg)})
                except Exception as e:
                    self._json({"ok": False, "error": str(e), "matches": []})

        elif path.startswith("/social/image/generate"):
            if not self._check_token(): return
            if not self._check_rate_limit("/social/image/generate"): return
            # Build an Instagram result graphic: photo backdrop + AI-distilled match facts.
            # Optional ?photo=<filename> selects a backdrop from the socials folder;
            # otherwise the newest photo there is used (or a plain navy backdrop if none).
            from urllib.parse import parse_qs
            qs = parse_qs(urlparse(self.path).query)
            chosen = qs.get("photo", [""])[0].strip()
            match_id = qs.get("match_id", [""])[0].strip()
            team_key = qs.get("team", [""])[0].strip()
            scfg = load_state()
            # Backdrop comes from the team's subfolder if one exists (socials/2nd), else root.
            sfolder = _socials_dir(team_key)
            photo_path = None
            photos = list_social_photos(team_key)
            if chosen and chosen in photos:
                photo_path = os.path.join(sfolder, chosen)
            elif photos:
                # newest by mtime
                try:
                    photo_path = max((os.path.join(sfolder, p) for p in photos),
                                     key=os.path.getmtime)
                except Exception:
                    photo_path = os.path.join(sfolder, photos[0])
            # Facts source: a chosen past match (away games, no stream) or the live/streamed
            # match. The match path is deterministic from PlayCricket + an optional AI caption.
            if match_id:
                facts = build_match_facts_from_pc(match_id, team_key)
                if facts.get("ok"):
                    facts["caption"] = ai_caption_for_facts(facts)
            else:
                facts = generate_social_graphic_facts()
            if not facts.get("ok"):
                self._json({"ok": False, "error": facts.get("error","Could not generate facts")})
            else:
                try:
                    out = build_instagram_image(facts, photo_path)
                    self._json({"ok": True, "file": os.path.basename(out),
                                "caption": facts.get("caption",""),
                                "used_photo": os.path.basename(photo_path) if photo_path else None})
                except Exception as e:
                    hint = (" — Pillow isn't installed on this machine. Run the installer "
                            "again, or: pip3 install Pillow") if "PIL" in str(e) else ""
                    self._json({"ok": False, "error": f"Image build failed: {e}{hint}"})

        elif path == "/social/image/latest":
            # Serve the most recently generated Instagram graphic for preview/download.
            here = os.path.dirname(os.path.abspath(__file__))
            imgs = sorted(glob.glob(os.path.join(here, "instagram_result_*.png")),
                          key=os.path.getmtime, reverse=True)
            if imgs:
                self._file(imgs[0], "image/png")
            else:
                self.send_response(404); self.send_header("Content-Length","0"); self.end_headers()

        elif path == "/report/log":
            # Raw match log (for debugging / manual editing)
            self._json(_match_log)

        elif path.startswith("/headshot/"):
            from urllib.parse import unquote, parse_qs
            raw   = unquote(path[10:].split("?")[0]).strip("/").replace("..","")
            name  = raw.replace("/","").replace("\\","").replace(":","")
            qs_h  = parse_qs(urlparse(self.path).query)
            num   = qs_h.get("num",[""])[0].strip()
            # One method for both sides: a confirmed number wins (file-by-number or roster name),
            # otherwise fall back to surname matching on the scorebar name.
            resolved, by_num = resolve_player(name, num)
            s2    = load_state()
            hdir  = s2.get("headshots_folder","").strip()
            hdir  = (os.path.expanduser(hdir) if hdir
                     else os.path.join(os.path.dirname(
                          os.path.abspath(__file__)), "headshots"))
            mimes = {"png":"image/png","jpg":"image/jpeg",
                     "jpeg":"image/jpeg","webp":"image/webp"}
            # Tiered match so the scorebar name finds the photo even when they differ in form:
            #   full  : 'P SMITH' -> 'p.smith'
            #   initsur: 'K J JONES' -> 'k.jones'  (first initial + surname)
            #   surname: 'WALKER'  -> 'j.walker'   (only when one file has that surname)
            want = _name_keys(resolved)   # resolved = roster full name if confirmed, else scorebar name
            try:
                files = [f for f in os.listdir(hdir)
                         if f.rsplit(".",1)[-1].lower() in mimes]
            except (FileNotFoundError, NotADirectoryError):
                files = []
            fkeys = [(f, _name_keys(f.rpartition(".")[0])) for f in sorted(files)]

            def _serve(f):
                ext = f.rpartition(".")[2].lower()
                self._file(os.path.join(hdir, f), mimes[ext])

            found_h = False
            # 0) a file named by shirt number (e.g. '21.png') — only when the number is a
            #    CONFIRMED roster match (surname agrees), so it can't grab a home file for an away player
            if by_num and num:
                numhits = [f for f, k in fkeys if f.rpartition(".")[0].strip() == num]
                if numhits:
                    _serve(numhits[0]); found_h = True
            # 1) full exact
            if not found_h:
                for f, k in fkeys:
                    if want["full"] and k["full"] == want["full"]:
                        _serve(f); found_h = True; break
            # 2) first-initial + surname
            if not found_h:
                for f, k in fkeys:
                    if want["initsur"] and k["initsur"] == want["initsur"]:
                        _serve(f); found_h = True; break
            # 3) surname only — but just when exactly one file matches (avoids brother mix-ups)
            if not found_h and want["surname"]:
                hits = [f for f, k in fkeys if k["surname"] == want["surname"]]
                if len(hits) == 1:
                    _serve(hits[0]); found_h = True
            if not found_h:
                self.send_response(404)
                self.send_header("Content-Length","0")
                self.end_headers()

        elif path == "/logos/list":
            # List badge files in the logos folder so the control panel can offer a manual picker.
            s_state   = load_state()
            logos_cfg = s_state.get("logos_folder","").strip()
            logo_dir  = (os.path.expanduser(logos_cfg) if logos_cfg
                         else os.path.join(os.path.dirname(os.path.abspath(__file__)), "logos"))
            exts = (".png",".jpg",".jpeg",".svg",".webp",".gif")
            out = []
            try:
                for f in sorted(os.listdir(logo_dir)):
                    if f.lower().endswith(exts):
                        out.append({"file": f, "id": f.rsplit(".",1)[0]})
            except (FileNotFoundError, NotADirectoryError):
                pass
            self._json({"folder": logo_dir, "logos": out})

        elif path.startswith("/logo/"):
            # Serve club badge images from logos/ folder
            raw_name = path[6:].split("?")[0].strip("/")  # strip query string + traversal
            # basename + strip separators: '\' would otherwise survive and on Windows
            # os.path.join treats a leading '\' or 'C:\' as an absolute path.
            name     = os.path.basename(raw_name.replace("..", "").replace("/", "")
                                        .replace("\\", "").replace(":", ""))
            s_state  = load_state()
            logos_cfg = s_state.get("logos_folder","").strip()
            logo_dir = (os.path.expanduser(logos_cfg) if logos_cfg
                        else os.path.join(os.path.dirname(os.path.abspath(__file__)), "logos"))
            mimes    = {"png":"image/png","jpg":"image/jpeg","jpeg":"image/jpeg",
                        "svg":"image/svg+xml","webp":"image/webp","gif":"image/gif"}
            found = False
            for ext, mime in mimes.items():
                logo_path = os.path.join(logo_dir, f"{name}.{ext}")
                if os.path.exists(logo_path):
                    self._file(logo_path, mime)
                    found = True
                    break
            if not found:
                self.send_response(404)
                self.send_header("Content-Length", "0")
                self.end_headers()

        elif path == "/player/stats":
            from urllib.parse import parse_qs
            qs2   = parse_qs(urlparse(self.path).query)
            name  = qs2.get("name",[""])[0].strip()
            num   = qs2.get("num",[""])[0].strip()
            # One method for both sides: number→roster if the surname matches, else surname.
            lookup_by, _byn = resolve_player(name, num)
            # Diagnostic: /player/stats?name=PENBERTHY&debug=1 shows why a name did or didn't match
            if qs2.get("debug",[""])[0]:
                lk = _season_stats.get("lookup") or {}
                k  = _name_keys(lookup_by)
                seen, same = set(), []
                for rec in lk.values():
                    if id(rec) in seen:
                        continue
                    seen.add(id(rec))
                    if _name_keys(rec.get("name",""))["surname"] == k["surname"]:
                        same.append({"name": rec.get("name"), "inn": rec.get("inn"),
                                     "avg": rec.get("avg"), "hs": rec.get("hs")})
                self._json({"query": name, "shirt_num": num, "resolved": lookup_by, "keys": k,
                            "full_hit": k["full"] in lk, "initsur_hit": k["initsur"] in lk,
                            "surname_hit": k["surname"] in lk,
                            "players_with_this_surname": same,
                            "built": _season_stats.get("built"),
                            "note": ("more than one player shares this surname, so the surname-only "
                                     "match is suppressed — add them to the roster with shirt numbers")
                                    if len(same) > 1 else ""})
                return
            today = datetime.date.today().isoformat()
            built = _season_stats.get("built") and _season_stats.get("date") == today
            # Kick off a one-time background build the first time stats are asked for.
            # The API is pulled once (then cached to disk for the day); never blocks the overlay.
            if not built and not _season_stats.get("building"):
                threading.Thread(target=build_season_stats, daemon=True).start()
            if not built:
                # Not ready yet — tell the overlay to retry shortly.
                self._json({"ready": False, "avg":"—", "hs":"—", "inn":"—"})
            else:
                rec = lookup_season_stats(lookup_by)
                if rec:
                    self._json({"ready": True, **rec})
                else:
                    self._json({"ready": True, "avg":"—", "hs":"—", "inn":"—"})

        elif path == "/season/top":
            # Pre-game "season form" panel data: each team's own top scorer/wicket-taker.
            # Same lazy-build-in-background pattern as /player/stats — never blocks the overlay.
            today = datetime.date.today().isoformat()
            built = _season_stats.get("built") and _season_stats.get("date") == today
            if not built and not _season_stats.get("building"):
                threading.Thread(target=build_season_stats, daemon=True).start()
            if not built:
                self._json({"ready": False})
            else:
                self._json({"ready": True,
                            "scorers": _season_stats.get("top_scorers") or {"home": None, "away": None},
                            "bowlers": _season_stats.get("top_bowlers") or {"home": None, "away": None}})

        elif path == "/player/stats/refresh":
            if not self._check_token(): return
            # Build/ensure the season stats cache. ?force=1 forces a fresh pull (control-panel
            # button); without it, a same-day cache is reused so restarts don't re-hit the API.
            from urllib.parse import parse_qs
            q     = parse_qs(urlparse(self.path).query)
            force = q.get("force",["0"])[0].lower() in ("1","true","yes")
            res   = build_season_stats(force=force)
            self._json({"ok": True,
                        "players": len(res.get("lookup",{})),
                        "matches_used": res.get("matches_used",0),
                        "api_calls": res.get("calls",0),
                        "from_cache": (_season_stats_last_action == "cache"),
                        "opposition": bool(res.get("away_ok")),
                        "error": res.get("error")})

        elif path == "/match/fetch":
            if not self._check_token(): return
            # Fetch today's match from PlayCricket API and optionally auto-fill state
            s      = load_state()
            key    = s.get("playcricket_api_key","").strip()
            site   = s.get("home_club_id","").strip()
            result = fetch_todays_match(key, site)
            if "error" not in result:
                # Auto-fill state with fetched data
                updates = {}
                if result.get("away_team"):     updates["away_team"]        = result["away_team"]
                if result.get("home_club_id"): updates["home_club_id"]     = result["home_club_id"]
                if result.get("away_club_id"): updates["away_club_id"]     = result["away_club_id"]
                if result.get("away_abbrev"):   updates["away_abbrev"]      = result["away_abbrev"]
                if result.get("competition"):   updates["competition_name"] = result["competition"]
                if result.get("umpire1"):       updates["umpire1_name"]     = result["umpire1"]
                if result.get("umpire2"):       updates["umpire2_name"]     = result["umpire2"]
                if result.get("match_id"):      updates["pc_match_id"]      = result["match_id"]
                if result.get("ground_lat"):
                    updates["weather_lat"] = result["ground_lat"]
                    updates["weather_lon"] = result["ground_lon"]
                if updates:
                    s.update(updates)
                    save_state(s)
                    print(f"  API: auto-filled {list(updates.keys())}")
            self._json({"ok": "error" not in result, "result": result})

        elif path == "/match/fixtures":
            # Return all fixtures for the season
            s   = load_state()
            key = s.get("playcricket_api_key","").strip()
            site= s.get("home_club_id","").strip()
            url = (f"https://play-cricket.com/api/v2/matches.json"
                   f"?api_token={key}&site_id={site}&season={datetime.date.today().year}")
            try:
                req = urllib.request.Request(url, headers={"Accept":"application/json"})
                with urllib.request.urlopen(req, timeout=15) as resp:
                    data = json.loads(resp.read().decode())
                self._json({"ok": True, "matches": data.get("matches",[])[:50]})
            except Exception as e:
                self._json({"ok": False, "error": str(e)})

        elif path == "/save/test":
            if not self._check_token(): return
            # Quick test: write a test value and read it back
            s = load_state()
            s["_save_test"] = "ok"
            save_state(s)
            s2 = load_state()
            self._json({"saved": s2.get("_save_test") == "ok",
                        "state_file": STATE_FILE,
                        "file_exists": os.path.exists(STATE_FILE)})

        elif path == "/pcs/debug":
            s = load_state()
            folder = s.get("pcs_output_folder", "").strip()
            result = {"folder": folder, "folder_exists": False, "files": [], "found": None, "content_preview": "", "parse_error": ""}
            if folder:
                import os as _os, glob as _glob
                result["folder_exists"] = _os.path.isdir(folder)
                if result["folder_exists"]:
                    all_files = _glob.glob(_os.path.join(folder, "*"))
                    result["files"] = [_os.path.basename(f) for f in all_files]
                    found = find_pcs_output_file(folder)
                    result["found"] = _os.path.basename(found) if found else None
                    if found:
                        try:
                            with open(found, encoding="utf-8", errors="replace") as f:
                                raw = f.read().strip()
                            result["content_preview"] = raw[:1200]
                            import json as _json
                            data = _json.loads(raw)
                            result["json_keys"] = list(data.keys())
                            result["json_sample"] = {k: v for k, v in list(data.items())[:5]}
                            result["ball_fields"] = {k: v for k, v in data.items() if any(x in k.lower() for x in ["ball","over","last"])}
                        except Exception as e:
                            result["parse_error"] = str(e)
            self._json(result)

        elif path == "/status":
            # Server uptime and clip count
            uptime_secs = int(time.time() - SERVER_START_TIME)
            hours, rem  = divmod(uptime_secs, 3600)
            mins, secs  = divmod(rem, 60)
            uptime_str  = f"{hours}h {mins}m {secs}s" if hours else f"{mins}m {secs}s"
            st = load_state()
            folder = st.get("replay_folder","") or _default_replay_folder()
            patterns = ["*.mkv","*.mp4","*.flv","*.mov"]
            clips = [c for p in patterns for c in glob.glob(os.path.join(folder,p))
                     if "highlights" not in os.path.basename(c).lower()]
            self._json({
                "uptime":     uptime_str,
                "uptime_sec": uptime_secs,
                "clip_count": len(clips),
                "max_clips":  int(st.get("max_clips", 100)),
                "folder":     folder,
            })

        elif path == "/health":
            # One-stop health check for the panel strip and quickstart's self-test.
            s_h  = load_state()
            now  = time.time()
            # PCS scorer feed: file present? how stale?
            pcs_dir   = os.path.expanduser(s_h.get("pcs_output_folder","").strip())
            pcs_path  = find_pcs_output_file(pcs_dir) if pcs_dir else None
            pcs_age   = (now - os.path.getmtime(pcs_path)) if pcs_path else None
            # Asset folders
            here      = os.path.dirname(os.path.abspath(__file__))
            hs_dir    = os.path.expanduser(s_h.get("headshots_folder","").strip()) or os.path.join(here,"headshots")
            lg_dir    = os.path.expanduser(s_h.get("logos_folder","").strip()) or os.path.join(here,"logos")
            img_exts  = (".png",".jpg",".jpeg",".webp",".svg",".gif")
            def _count(d):
                try:    return len([f for f in os.listdir(d) if f.lower().endswith(img_exts)])
                except OSError: return 0
            # Season stats
            stats_players = len({id(v) for v in (_season_stats.get("lookup") or {}).values()})
            self._json({
                "ok": True,
                "uptime_sec": int(now - SERVER_START_TIME),
                "demo_mode":  bool(s_h.get("demo_mode")),
                "pcs": {
                    "folder_set": bool(pcs_dir),
                    "file_found": bool(pcs_path),
                    "file":       os.path.basename(pcs_path) if pcs_path else None,
                    "age_sec":    int(pcs_age) if pcs_age is not None else None,
                    "fresh":      (pcs_age is not None and pcs_age < 120),
                },
                "stats": {
                    "built":    bool(_season_stats.get("built")),
                    "building": bool(_season_stats.get("building")),
                    "players":  stats_players,
                    "date":     _season_stats.get("date"),
                    "error":    _season_stats.get("error"),
                },
                "assets": {"headshots": _count(hs_dir), "logos": _count(lg_dir)},
                "keys": {
                    "anthropic":   bool(s_h.get("anthropic_api_key","").strip()),
                    "playcricket": bool((s_h.get("playcricket_api_key","") or s_h.get("api_token","")).strip()),
                    "weather":     bool(s_h.get("weather_api_key","").strip()),
                },
                "match": {
                    "home": s_h.get("home_team",""), "away": s_h.get("away_team",""),
                    "badges": bool(s_h.get("home_club_id")) and bool(s_h.get("away_club_id")),
                },
            })

        elif path == "/live":
            global _rv_cache
            s    = load_state()
            home = s.get("home_team","Home CC")
            away = s.get("away_team","Opposition CC")
            club_id = int(s.get("test_club_id") or s.get("home_club_id") or 0)

            # match_url: pin to a specific match
            match_url   = s.get("match_url","").strip()
            pc_from_url = extract_pc_match_id(match_url) if match_url else None
            if pc_from_url:
                if _rv_cache.get("pc_id") != pc_from_url:
                    _rv_cache.update({"pc_id": pc_from_url, "rv_id": None,
                                      "club_id": club_id, "last_rv_poll": 0,
                                      "last_state": None, "pinned": True})
                # Use manually entered RV ID if available (bypasses mapping API)
                manual_rv = s.get("rv_match_id","").strip()
                if manual_rv and not _rv_cache.get("rv_id"):
                    _rv_cache["rv_id"] = manual_rv
                    print(f"  RV ID set manually: {manual_rv}")
                elif not _rv_cache.get("rv_id"):
                    now = time.time()
                    if (now - _rv_cache.get("last_map_attempt",0)) > 300:
                        _rv_cache["last_map_attempt"] = now
                        rv_id = fetch_rv_mapping(pc_from_url)
                        if rv_id:
                            _rv_cache["rv_id"] = rv_id
                            _rv_cache["last_rv_poll"] = 0
            else:
                if _rv_cache.get("pinned"):
                    _rv_cache = {"rv_id": None, "pc_id": None, "club_id": club_id,
                                 "last_rv_poll": 0, "last_state": None, "pinned": False}

            if s.get("api_token") and s.get("match_id"):
                try:
                    url = (f"https://play-cricket.com/api/v2/match_detail.json"
                           f"?id={s['match_id']}&site_id={s.get('site_id','')}"
                           f"&api_token={s['api_token']}")
                    req = urllib.request.Request(url, headers={"User-Agent":"CricketStreamOverlay/1.0"})
                    with urllib.request.urlopen(req, timeout=10) as r:
                        data = json.loads(r.read().decode())
                    self._json({"source":"api","data":data})
                except Exception as e:
                    self._json({"source":"api","error":str(e),"data":None})
            else:
                # PCS Pro local file takes priority — instant, ball by ball
                pcs_folder = s.get("pcs_output_folder","").strip()
                pcs_state  = read_pcs_file(pcs_folder) if pcs_folder else None
                if pcs_state:
                    # Inject abbreviations so overlay can use them
                    pcs_state["home_abbrev"] = s.get("home_abbrev","").strip().upper()
                    pcs_state["away_abbrev"] = s.get("away_abbrev","").strip().upper()
                    # Buffer boundary/wicket events before updating prev_state
                    buffer_pcs_events(pcs_state)
                    match_log_snapshot(pcs_state)
                    log_ball_data(pcs_state)   # append to our own ball-by-ball database
                    if s.get("graphics_commentary", True):
                        check_commentary_trigger(pcs_state)
                    # Include buffered events in response and clear buffer
                    events = list(_event_buffer)
                    _event_buffer.clear()
                    self._json({"source":"pcs","state":pcs_state,"club_id":club_id,"events":events})
                else:
                    if s.get("use_widget", True):
                        # Run widget fetch in thread with hard 5s timeout
                        # Prevents DNS/connection hangs blocking the HTTP handler
                        import concurrent.futures as _cf
                        state = None
                        try:
                            with _cf.ThreadPoolExecutor(max_workers=1) as ex:
                                fut = ex.submit(get_live_state, club_id, home, away)
                                state = fut.result(timeout=5)
                        except Exception:
                            state = None
                        if state:
                            state["home_abbrev"] = s.get("home_abbrev","").strip().upper()
                            state["away_abbrev"] = s.get("away_abbrev","").strip().upper()
                            self._json({"source":"widget","state":state,"club_id":club_id})
                        else:
                            self._json({"source":"widget","state":None,"club_id":club_id})
                    else:
                        self._json({"source":"widget","state":None,"club_id":club_id})

        elif path == "/commentary/over/generate":
            if not self._check_token(): return
            try:
                d  = json.loads(body)
                st = load_state()
                ps = read_pcs_file(st.get("pcs_output_folder","")) or {}
                generate_over_commentary(
                    d.get("over_num",0), d.get("over_runs",0),
                    d.get("bowler_name",""), d.get("bowler_figs",""),
                    d.get("balls",""), ps)
                self._json({"ok":True})
            except Exception as exc:
                self._json({"ok":False,"error":str(exc)})

        elif path == "/commentary/test":
            if not self._check_token(): return
            if not self._check_rate_limit(path): return
            try:
                st = load_state()
                demo_state = {
                    "battingTeamName": st.get("home_team","Home CC"),
                    "bowlingTeamName": st.get("away_team","Opposition CC"),
                    "score":87,"wickets":3,"overs":18.2,
                    "batter1":{"name":"A. Richards","runs":34,"balls":52},
                    "batter2":{"name":"T. Blake","runs":12,"balls":21},
                    "bowler":{"name":"J. Harrison","wickets":2,"runs":28,"overs":"7"},
                }
                threading.Thread(
                    target=generate_commentary,
                    args=(demo_state, ["FOUR — 87-3","Wicket — 75-3"]),
                    daemon=True).start()
                time.sleep(3)
                c = get_commentary()
                self._json({"ok": bool(c["text"]), "text": c["text"] or "Generating..."})
            except Exception as e:
                self._json({"ok": False, "error": str(e)}, status=500)

        else:
            self.send_response(404); self.end_headers()

    def do_POST(self):
        path   = urlparse(self.path).path
        length = int(self.headers.get("Content-Length",0))
        body   = self.rfile.read(length)

        if path == "/login":
            self._handle_login(body)
            return

        if not self._check_token():
            return

        if path == "/state":
            try:
                incoming = json.loads(body)
                # A non-dict body (array/string/number) must be rejected outright: writing it
                # to the state file would make every later load_state() raise TypeError and
                # permanently break the server until the file is hand-fixed.
                if not isinstance(incoming, dict):
                    self._json({"ok": False, "error": "state must be a JSON object"}, status=400)
                    return
                # Drop sentinel-valued secrets: that's the control panel round-tripping the
                # redacted value from GET /state, meaning "leave the stored key unchanged".
                for _k in SECRET_KEYS:
                    if incoming.get(_k) == SECRET_SENTINEL:
                        incoming.pop(_k)
                # MERGE into existing state rather than replacing the whole file. The control
                # panel form only posts its own fields, so a plain replace would wipe keys that
                # have no form input — notably home_club_id / away_club_id (badges), the season
                # stats config, drinks_over, etc. Merging preserves them across saves.
                current = load_state()
                current.update(incoming)
                save_state(current)
                self._json({"ok":True})
                s = load_state()
                print(f"  ✓  {s.get('away_team','?')}  "
                      f"home={s.get('home_colour')}  "
                      f"away={s.get('away_colour')}  "
                      f"replay={'on' if s.get('replay_enabled') else 'off'}")
            except Exception as e:
                self._json({"ok":False,"error":str(e)},status=400)

        elif path == "/replay":
            state = load_state()
            if not state.get("replay_enabled"):
                self._json({"ok":False,"error":"Replay disabled in control panel"})
                return
            try:
                data   = json.loads(body)
                reason = data.get("reason","Unknown")
                # Fire in background thread — don't block the overlay
                t = threading.Thread(target=obs_trigger_replay, args=(state,), daemon=True)
                t.start()
                self._json({"ok":True,"reason":reason})
                print(f"  ▶  Replay queued — reason: {reason}")
            except Exception as e:
                self._json({"ok":False,"error":str(e)},status=500)

        elif path == "/weather/show":
            set_command("show_weather")
            self._json({"ok": True})

        elif path == "/obs/add_camera":
            try:
                d = json.loads(body or "{}")
            except json.JSONDecodeError:
                d = {}
            cfg = load_state()
            url = (d.get("url") or cfg.get("camera_rtsp_url") or "").strip()
            ok, msg = obs_add_camera(url, d.get("name"), d.get("scene"), cfg)
            self._json({"ok": ok, "message": msg})

        elif path == "/data/reconcile":
            # Reconcile a match's aggregates against PlayCricket's published scorecard.
            try:
                mid = (json.loads(body or "{}").get("match_id") or "").strip()
            except (json.JSONDecodeError, AttributeError):
                mid = ""
            if not mid:
                mid = current_match_id()
            self._json(reconcile_match(mid))

        elif path == "/scorecard/show":
            set_command("show_scorecard")
            self._json({"ok": True})

        elif path == "/cards/show":
            # Manually (re)show player cards for the current batters — handy for testing
            # the pipeline and for match-day "show them again" moments.
            set_command("show_player_cards")
            self._json({"ok": True})

        elif path == "/weather/hide":
            set_command("hide_weather")
            self._json({"ok": True})

        elif path == "/youtube/update":
            try:
                data  = json.loads(body)
                s     = load_state()
                home  = s.get("home_team", "Home CC")
                away  = s.get("away_team", "Opposition")
                tmpl  = data.get("title") or s.get("youtube_title_template", "LIVE: {home} vs {away}")
                title = tmpl.replace("{home}", home).replace("{away}", away)
                ok, msg = update_youtube_title(title)
                print(f"  {'✓' if ok else '✗'}  YouTube: {msg}")
                self._json({"ok": ok, "title": title, "error": msg if not ok else ""})
            except Exception as e:
                self._json({"ok": False, "error": str(e)}, status=500)

        elif path == "/highlights":
            try:
                s      = json.loads(body) if body else {}
                st     = load_state()
                folder = st.get("replay_folder","") or _default_replay_folder()
                output = s.get("output", os.path.join(folder, "highlights.mp4"))
                max_c  = int(st.get("max_clips", 100))
                def run_compile():
                    ok, msg = compile_highlights(folder, output, max_c)
                    print(f"  {'✓' if ok else '✗'}  Highlights: {msg}")
                threading.Thread(target=run_compile, daemon=True).start()
                # Count current clips
                patterns = ["*.mkv","*.mp4","*.flv","*.mov"]
                clips = [c for p in patterns for c in glob.glob(os.path.join(folder,p))
                         if "highlights" not in os.path.basename(c).lower()]
                self._json({"ok": True, "output": output, "clip_count": len(clips)})
            except Exception as e:
                self._json({"ok": False, "error": str(e)}, status=500)

        elif path == "/commentary/over/generate":
            try:
                d  = json.loads(body)
                st = load_state()
                ps = read_pcs_file(st.get("pcs_output_folder","")) or {}
                generate_over_commentary(
                    d.get("over_num",0), d.get("over_runs",0),
                    d.get("bowler_name",""), d.get("bowler_figs",""),
                    d.get("balls",""), ps)
                self._json({"ok":True})
            except Exception as exc:
                self._json({"ok":False,"error":str(exc)})

        elif path == "/commentary/test":
            if not self._check_rate_limit(path): return
            try:
                st = load_state()
                demo_state = {
                    "battingTeamName": st.get("home_team","Home CC"),
                    "bowlingTeamName": st.get("away_team","Opposition CC"),
                    "score":87,"wickets":3,"overs":18.2,
                    "batter1":{"name":"A. Richards","runs":34,"balls":52},
                    "batter2":{"name":"T. Blake","runs":12,"balls":21},
                    "bowler":{"name":"J. Harrison","wickets":2,"runs":28,"overs":"7"},
                }
                threading.Thread(
                    target=generate_commentary,
                    args=(demo_state, ["FOUR — 87-3","Wicket — 75-3"]),
                    daemon=True).start()
                time.sleep(3)
                c = get_commentary()
                self._json({"ok": bool(c["text"]), "text": c["text"] or "Generating..."})
            except Exception as e:
                self._json({"ok": False, "error": str(e)}, status=500)

        else:
            self.send_response(404); self.end_headers()


# ── Entry point ───────────────────────────────────────────────

if __name__ == "__main__":
    # Check for websocket-client
    try:
        import websocket
        ws_status = "websocket-client ✓"
    except ImportError:
        ws_status = "websocket-client NOT installed — run: pip install websocket-client"

    try:
        import anthropic
        anthropic_status = "anthropic ✓  (AI commentary available)"
    except ImportError:
        anthropic_status = "anthropic NOT installed — run: pip install anthropic  (needed for AI commentary)"

    if not os.path.exists(STATE_FILE):
        save_state(DEFAULT_STATE)
        print(f"  Created {STATE_FILE}\n")

    _seed_state_from_config()

    remote_info = ""
    if _BIND_HOST != "127.0.0.1":
        ts_ip = None
        try:
            r = subprocess.run(["tailscale", "ip", "-4"],
                               capture_output=True, text=True, timeout=2)
            if r.returncode == 0:
                ts_ip = r.stdout.strip()
        except Exception:
            pass
        if ts_ip:
            remote_info = f"\n  Tailscale remote → http://{ts_ip}:{PORT}/control"
        else:
            remote_info = f"\n  Listening on {_BIND_HOST}:{PORT} — use your device IP to connect remotely"

    print(f"""
  ╔══════════════════════════════════════════════════════╗
  ║        CricketStream Overlay — Stream Server         ║
  ╠══════════════════════════════════════════════════════╣
  ║   Control panel  →  http://localhost:{PORT}/control     ║
  ║   OBS overlay    →  http://localhost:{PORT}/overlay     ║
  ╚══════════════════════════════════════════════════════╝
{remote_info}
  {ws_status}
  {anthropic_status}
  Replay clips capped at {load_state().get('max_clips', MAX_CLIPS)} — oldest auto-deleted.

  Keep this window open while streaming. Ctrl+C to stop.
    """)

    # Threaded server: a slow request (AI report/social generation, a PlayCricket fetch)
    # must never block the overlay's /live and /state polling. daemon_threads lets the
    # process exit cleanly without waiting on in-flight requests.
    db_init()   # ensure the ball-by-ball database exists
    httpd = ThreadingHTTPServer((_BIND_HOST, PORT), Handler)
    httpd.daemon_threads = True
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n  Server stopped.")
