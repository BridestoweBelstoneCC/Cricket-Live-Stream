"""
CricketStream Overlay — Stream Server
─────────────────────────────────────────────────
Run:  python server.py

Control panel  →  http://localhost:5000/control
OBS overlay    →  http://localhost:5000/overlay

Requirements (pip install each):
    websocket-client          — OBS WebSocket / instant replay
    anthropic                 — AI commentary (optional)
    google-api-python-client  — YouTube title updater (optional)
    google-auth-oauthlib      — YouTube title updater (optional)
    qrcode                    — QR code for remote control panel access (optional)

Also optional (not pip packages — install separately):
    tailscale   — private remote access (recommended)
    cloudflared — public remote access fallback; set cloudflare_tunnel=true in
                  config.ini [Network] once club_password is set

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

import json, os, re, glob, time, hashlib, hmac, secrets, threading, base64, datetime, subprocess, socket, io

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

from scoring_engine import InningsEngine   # shared scorer's book — also drives simulate_match

SERVER_START_TIME = time.time()

# ── Auth ─────────────────────────────────────────────────────
# control_token: signing key for session tokens — never shown to users.
# club_password:  what operators type in the login form.
# Auth is disabled when club_password is empty (safe on localhost).
_CONTROL_TOKEN      = ""
_CLUB_PASSWORD      = ""
_SESSION_HOURS      = 12
_BIND_HOST          = "127.0.0.1"
_CLOUDFLARE_TUNNEL  = False

def _config_ini_path():
    return os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini")

def _persist_control_token(new_token):
    """Writes a freshly-generated control_token into config.ini's [Auth] section, editing
    the line in place (not via configparser's writer, which would strip every comment in
    the file) and atomically (temp file + os.replace, same pattern as save_state()).
    Returns True on success."""
    cfg_path = _config_ini_path()
    try:
        with open(cfg_path, "r", encoding="utf-8") as f:
            lines = f.readlines()
        in_auth, found = False, False
        for i, line in enumerate(lines):
            stripped = line.strip()
            if stripped.startswith("[") and stripped.endswith("]"):
                in_auth = (stripped == "[Auth]")
                continue
            if in_auth and re.match(r"^\s*control_token\s*=", line):
                lines[i] = f"control_token = {new_token}\n"
                found = True
                break
        if not found:
            # Insert directly under the [Auth] header — appending at EOF would land the
            # line inside whichever section happens to be last (e.g. [Network]), where
            # configparser never reads it back, so a new token got appended every restart.
            auth_idx = next((i for i, l in enumerate(lines) if l.strip() == "[Auth]"), None)
            if auth_idx is None:
                lines.append("\n[Auth]\n")
                lines.append(f"control_token = {new_token}\n")
            else:
                lines.insert(auth_idx + 1, f"control_token = {new_token}\n")
        tmp = cfg_path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            f.writelines(lines)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, cfg_path)
        return True
    except Exception as e:
        print(f"  ✗  Could not persist generated control_token to config.ini: {e}")
        return False

def _load_auth_config():
    global _CONTROL_TOKEN, _CLUB_PASSWORD, _BIND_HOST, _CLOUDFLARE_TUNNEL
    import configparser as _cp
    cfg = _cp.ConfigParser()
    cfg_path = _config_ini_path()
    if os.path.exists(cfg_path):
        cfg.read(cfg_path, encoding="utf-8")
        _CONTROL_TOKEN     = cfg.get("Auth",    "control_token", fallback="").strip()
        _CLUB_PASSWORD     = cfg.get("Auth",    "club_password", fallback="").strip()
        _BIND_HOST         = (os.environ.get("BBCC_BIND_HOST","").strip()
                              or cfg.get("Network", "bind_host", fallback="127.0.0.1").strip())
        _CLOUDFLARE_TUNNEL = cfg.getboolean("Network", "cloudflare_tunnel", fallback=False)

    # 1.3 — binding beyond localhost with no password would expose every control-panel
    # action to the network with nothing gating it. Fail closed.
    if _BIND_HOST not in ("127.0.0.1", "localhost") and not _CLUB_PASSWORD:
        print(f"  ✗  bind_host is set to {_BIND_HOST} but club_password is blank — binding "
              f"127.0.0.1 instead. Set [Auth] club_password in config.ini to enable remote "
              f"access.")
        _BIND_HOST = "127.0.0.1"

_load_auth_config()

def _ensure_control_token():
    """1.1 — sessions are HMAC-signed with control_token; a blank one means anyone who's
    read the (public) source can forge a valid session. Generate one the first time a
    password is set, rather than ever signing with "".

    Called from __main__ (server startup), NOT at import time: this can WRITE config.ini,
    and importers (the test suite, tooling) must never trigger writes as a side effect."""
    global _CONTROL_TOKEN, _BIND_HOST
    if _CLUB_PASSWORD and not _CONTROL_TOKEN:
        new_token = secrets.token_hex(32)
        if _persist_control_token(new_token):
            _CONTROL_TOKEN = new_token
            print("  ✓  No control_token was set — generated one and saved it to config.ini")
        else:
            _BIND_HOST = "127.0.0.1"
            print("  ✗  Generated a control_token but could not save it to config.ini — "
                  "refusing to bind beyond 127.0.0.1 until [Auth] control_token is set "
                  "manually")

# Mixed into every session signature. Rotating it (see "log everyone out" / POST
# /auth/logout_all) invalidates every previously issued session instantly, without needing
# to restart the stream — a fresh random value is picked at each server start too, so a
# restart alone already has the same effect.
_SESSION_EPOCH      = secrets.token_hex(8)
_session_epoch_lock = threading.Lock()

def _make_session_token():
    """Issue a signed, expiring session token: '{expiry}:{nonce}:{sig}'."""
    if not _CONTROL_TOKEN:
        raise RuntimeError("refusing to sign a session with an empty control_token")
    expiry = str(int(time.time()) + _SESSION_HOURS * 3600)
    nonce  = secrets.token_hex(8)
    with _session_epoch_lock:
        epoch = _SESSION_EPOCH
    sig = hmac.new(f"{_CONTROL_TOKEN}:{epoch}".encode(), f"{expiry}:{nonce}".encode(),
                   hashlib.sha256).hexdigest()
    return f"{expiry}:{nonce}:{sig}"

def _verify_session_token(token):
    """True if the token has a valid signature (under the current epoch) and hasn't expired."""
    if not _CONTROL_TOKEN:
        return False
    try:
        expiry, nonce, sig = token.split(":", 2)
        if int(expiry) < time.time():
            return False
        with _session_epoch_lock:
            epoch = _SESSION_EPOCH
        expected = hmac.new(f"{_CONTROL_TOKEN}:{epoch}".encode(), f"{expiry}:{nonce}".encode(),
                            hashlib.sha256).hexdigest()
        return hmac.compare_digest(sig, expected)
    except Exception:
        return False

# ── Login brute-force protection (Phase 1.2) ──────────────────
# In-memory per-process is fine here — a village club's server restarts between matches
# anyway, and this only needs to survive the length of one brute-force attempt.
_login_attempts      = {}   # ip -> {"failures": [timestamps], "locked_until": epoch or 0}
_login_attempts_lock = threading.Lock()
LOGIN_MAX_FAILURES   = 5
LOGIN_WINDOW_SEC     = 600    # failures older than this don't count towards the lockout
LOGIN_LOCKOUT_SEC    = 600    # how long an IP is rejected once it trips the limit

# ── Auth event log (Phase 2.7) ────────────────────────────────
# Small ring buffer so an operator can see at a glance (via /health) if something's probing
# the panel during a match — IP + time + event kind only, never the attempted password.
_auth_log      = []   # [{"time","event","ip"}, ...] — most recent last, capped
_auth_log_lock = threading.Lock()
AUTH_LOG_MAX   = 200

def _auth_log_add(event, ip):
    with _auth_log_lock:
        _auth_log.append({"time": time.time(), "event": event, "ip": ip})
        del _auth_log[:-AUTH_LOG_MAX]

# ── Remote access helpers (Tailscale / LAN / QR) ──────────────
def _lan_ip():
    """Best-guess LAN IP for this machine. The UDP 'connect' below sends no packets — it
    just asks the OS which local interface would be used to reach that address."""
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("8.8.8.8", 80))
        return s.getsockname()[0]
    except Exception:
        return None
    finally:
        s.close()

def _tailscale_ip():
    try:
        r = subprocess.run(["tailscale", "ip", "-4"],
                           capture_output=True, text=True, timeout=2)
        if r.returncode == 0:
            return r.stdout.strip()
    except Exception:
        pass
    return None

# Cloudflare Tunnel is a persistent subprocess (unlike the one-shot `tailscale ip` call),
# so its URL is discovered once at startup and cached here rather than looked up per request.
_cf_process       = None
_cf_url           = None
_cf_lock          = threading.Lock()
_cf_restart_count = 0
CF_MAX_RESTARTS   = 5   # give up auto-restarting after this many — likely a real problem

def _start_cloudflare_tunnel():
    """Launches `cloudflared tunnel --url` (a free 'quick tunnel', no Cloudflare account
    needed) and captures the https://*.trycloudflare.com URL it prints to stdout. Runs the
    reader in a daemon thread; safe to call even if cloudflared isn't installed."""
    global _cf_process, _cf_url
    try:
        proc = subprocess.Popen(
            ["cloudflared", "tunnel", "--url", f"http://127.0.0.1:{PORT}"],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
    except FileNotFoundError:
        print("  ✗  Cloudflare Tunnel: 'cloudflared' not installed — see")
        print("     https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")
        return
    except Exception as e:
        print(f"  ✗  Cloudflare Tunnel failed to start: {e}")
        return
    with _cf_lock:
        _cf_process = proc
        _cf_url     = None

    def _read_output():
        for line in proc.stdout:
            m = re.search(r"https://[a-zA-Z0-9\-]+\.trycloudflare\.com", line)
            if m:
                with _cf_lock:
                    globals()["_cf_url"] = m.group(0)
                print(f"  ✓  Cloudflare Tunnel ready → {m.group(0)}/control")
                break
    threading.Thread(target=_read_output, daemon=True).start()

def _stop_cloudflare_tunnel():
    with _cf_lock:
        proc = _cf_process
    if proc and proc.poll() is None:
        proc.terminate()

def _remote_targets():
    """Priority-ordered list of {'via','url'} for reaching the control panel from another
    device — 'tailscale' (private), 'cloudflare' (public, opt-in), 'lan' (same network).
    Empty if the server is only bound to localhost and no tunnel is running."""
    targets = []
    ts_ip = _tailscale_ip() if _BIND_HOST != "127.0.0.1" else None
    if ts_ip:
        targets.append({"via": "tailscale", "url": f"http://{ts_ip}:{PORT}/control"})
    with _cf_lock:
        cf_url = _cf_url
    if cf_url:
        targets.append({"via": "cloudflare", "url": f"{cf_url}/control"})
    if _BIND_HOST != "127.0.0.1":
        lan_ip = _lan_ip()
        if lan_ip:
            targets.append({"via": "lan", "url": f"http://{lan_ip}:{PORT}/control"})
    return targets

def _qr_png_bytes(data):
    """PNG bytes of a QR code for `data`, or None if the optional 'qrcode' package
    isn't installed."""
    try:
        import qrcode
    except ImportError:
        return None
    qr = qrcode.QRCode(border=2, box_size=8)
    qr.add_data(data)
    qr.make(fit=True)
    img = qr.make_image(fill_color="black", back_color="white")
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()

# ── Rate limiting (manual AI endpoints only) ─────────────────
# /commentary/over/generate is NOT here — the overlay fires it automatically at
# end of each over and for opening-pair player cards; capping it would break the
# live experience. Only manual button-triggered endpoints are rate-limited.
_RATE_LIMITS = {
    "/commentary/test":        60,   # test button — 60 s cooldown
    "/report/generate":       120,   # AI match report — 2 min
    "/social/image/generate": 120,   # AI social graphic — 2 min
    "/obs/stream_check":      300,   # runs real test recordings in OBS — 5 min cooldown
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

def match_log_snapshot_copy():
    """Defensive copy of _match_log for the report/social generators. Under the threaded
    server, live ball events mutate the log while a generator reads it — retry if a
    concurrent append trips iteration, so generation can never raise mid-request."""
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
    return snap


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
            CREATE TABLE IF NOT EXISTS clips (
                match_id TEXT, file TEXT, ts TEXT, reason TEXT,
                caption TEXT,
                PRIMARY KEY (match_id, file));
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

# Tracks the last over whose ticker we saw, so the over-completing delivery can be
# recovered. NV Play clears the ticker on the SAME write that completes an over (see
# CLAUDE.md), so the final ball of every over never appears in any ticker — the overlay
# recovers it from the score delta for the over-summary graphic, and this logger must do
# the same or the ball DB (and every CSV export) silently loses ball 6 of every over.
_ball_log_prev = {"mid": None, "innings": None, "over": None,
                  "score": 0, "wickets": 0, "count": 0}


def log_ball_data(state):
    """Capture the current over from the live ticker. Rewrites the whole current over each
    call (delete + reinsert) so scorer edits within the over are reflected; once the over
    rolls on it freezes — after recovering the over-completing ball the cleared ticker can
    never show (see _ball_log_prev). Never raises — logging must not affect the stream."""
    try:
        balls    = _parse_ticker(state.get("last_ball", ""))
        innings  = int(state.get("innings", 1) or 1)
        over_idx = int(float(state.get("overs", 0) or 0))     # completed overs = current over no.
        score    = int(state.get("score", 0) or 0)
        wkts     = int(state.get("wickets", 0) or 0)
        bteam    = state.get("battingTeamName", "")
        b1, b2   = state.get("batter1", {}) or {}, state.get("batter2", {}) or {}
        bowler   = (state.get("bowler", {}) or {}).get("name", "")
        striker, nonstriker = b1.get("name", ""), b2.get("name", "")
        mid      = current_match_id()
        prev     = _ball_log_prev

        # ── Recover the invisible over-completing delivery ──
        # The over we were logging has rolled on (same match+innings, over advanced):
        # whatever score/wickets moved beyond the balls we HAVE seen is the final
        # delivery (or, across a missed poll, final deliveries — logged as one
        # aggregate ball; totals stay exact even when the per-ball split is unknowable).
        if (prev["mid"] == mid and prev["innings"] == innings
                and prev["over"] is not None and over_idx > prev["over"]):
            miss_runs = score - prev["score"] - sum(b["runs"] for b in balls)
            miss_wkts = wkts - prev["wickets"] - sum(1 for b in balls if b["wicket"])
            if miss_runs >= 0 and (miss_runs > 0 or miss_wkts > 0 or prev["count"] > 0):
                synth_outcome = "W" if miss_wkts > 0 else (str(miss_runs) if miss_runs else "dot")
                now_s = datetime.datetime.now().isoformat(timespec="seconds")
                with _db_lock, _db() as c:
                    c.execute(
                        "INSERT OR REPLACE INTO balls(match_id,innings,over,ball,batting_team,"
                        "batter,non_striker,bowler,outcome,runs,extra,is_wicket,legal,"
                        "cum_runs,cum_wkts,ts) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                        (mid, innings, prev["over"], prev["count"] + 1, bteam, striker,
                         nonstriker, bowler, synth_outcome, miss_runs, None,
                         int(miss_wkts > 0), 1, prev["score"] + miss_runs,
                         prev["wickets"] + miss_wkts, now_s))
            prev.update({"over": None, "count": 0, "score": score, "wickets": wkts})

        if not balls:
            # Between overs (or pre-match): remember the baseline so the next over's
            # recovery is computed against the right score
            prev.update({"mid": mid, "innings": innings, "score": score, "wickets": wkts})
            return
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
        prev.update({"mid": mid, "innings": innings, "over": over_idx,
                     "score": score, "wickets": wkts, "count": len(balls)})
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
    # "Coming up" milestones — the anticipation angle, not just reacting after the fact.
    # overlay.html already fires its own graphic once these are actually reached; this just
    # gives the commentator a reason to build tension in the over before that happens.
    try:
        for bk in ('batter1', 'batter2'):
            b = state.get(bk) or {}
            nm, rn = b.get('name', ''), int(b.get('runs', 0) or 0)
            if not nm or nm == '—':
                continue
            target = 50 if rn < 50 else (100 if rn < 100 else None)
            if target and 0 < target - rn <= 15:
                label = 'century' if target == 100 else 'half-century'
                notable.append(f"{nm} is {target - rn} runs from a {label}.")
        bowler_wkts = int(str(figs).split('-')[-1])
        if bowler_wkts in (3, 4):
            need = 5 - bowler_wkts
            notable.append(f"{bowler} is {need} wicket{'s' if need != 1 else ''} from a five-wicket haul.")
    except (ValueError, IndexError, TypeError):
        pass
    try:
        next_hundred = ((sc // 50) + 1) * 50
        if 0 < next_hundred - sc <= 15:
            notable.append(f"{bat} are {next_hundred - sc} runs from {next_hundred}.")
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
# Fallback coordinates only — /match/fetch saves the real ground's weather_lat/weather_lon
# from PlayCricket into state, and fetch_weather_data prefers those. Without that
# preference, every club got THIS ground's weather (and the DLS rain threshold keyed off it).
GROUND_LAT, GROUND_LON = 50.691, -4.093

def _ground_coords():
    cfg = load_state()
    try:
        lat = float(cfg.get("weather_lat") or GROUND_LAT)
        lon = float(cfg.get("weather_lon") or GROUND_LON)
        return lat, lon
    except (TypeError, ValueError):
        return GROUND_LAT, GROUND_LON
WMO_ICONS = {0:"☀",1:"🌤",2:"⛅",3:"☁",45:"🌫",48:"🌫",51:"🌦",53:"🌦",55:"🌧",61:"🌧",63:"🌧",65:"🌧",71:"🌨",73:"🌨",75:"❄",80:"🌦",81:"🌧",82:"⛈",95:"⛈",96:"⛈",99:"⛈"}
WMO_DESC  = {0:"Clear sky",1:"Mainly clear",2:"Partly cloudy",3:"Overcast",45:"Foggy",48:"Freezing fog",51:"Light drizzle",53:"Drizzle",55:"Heavy drizzle",61:"Light rain",63:"Rain",65:"Heavy rain",71:"Light snow",73:"Snow",75:"Heavy snow",80:"Rain showers",81:"Heavy showers",82:"Violent showers",95:"Thunderstorm",96:"Thunderstorm+hail",99:"Heavy thunderstorm"}

def fetch_weather_data():
    try:
        lat, lon = _ground_coords()
        params = urlencode({"latitude":lat,"longitude":lon,
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

# ── YouTube broadcast manager ─────────────────────────────────
# With key-based streaming (recommended — survives restarts/quality changes), OBS's
# "Manage Broadcast" panel disappears, so the broadcast's title, description, privacy,
# "made for kids" flag and category all have to be set through the YouTube Data API
# instead. This does that over the same OAuth the title updater always used.
# Resolve relative to server.py (like STATE_FILE / config.ini / the DB), NOT the current
# working directory — the launch scripts cd elsewhere, so a bare filename could be looked
# up in the wrong place and "put it next to server.py" (what every doc says) would fail.
YT_TOKEN_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "yt_token.json")
YT_CREDS_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "yt_credentials.json")
YT_SCOPES     = ["https://www.googleapis.com/auth/youtube"]
YT_PRIVACY    = ("public", "unlisted", "private")
# The categories worth offering a grassroots cricket stream (id → label). 17 = Sports is
# the sensible default; the full list is large and mostly irrelevant here.
YT_CATEGORIES = [("17", "Sports"), ("24", "Entertainment"), ("22", "People & Blogs"),
                 ("19", "Travel & Events"), ("28", "Science & Technology"),
                 ("25", "News & Politics"), ("20", "Gaming")]


def _yt_broadcast_payload(cur_snippet, cur_status, title=None, description=None,
                          privacy=None, made_for_kids=None):
    """Pure: merge requested changes onto the broadcast's CURRENT snippet/status and return
    (snippet, status) for liveBroadcasts.update part='snippet,status'. Preserving current
    values matters — an update replaces the whole part, so anything omitted is wiped.
    scheduledStartTime is required by the API and always carried through."""
    snippet = {"title": cur_snippet.get("title", "")}
    if cur_snippet.get("scheduledStartTime"):
        snippet["scheduledStartTime"] = cur_snippet["scheduledStartTime"]
    snippet["description"] = cur_snippet.get("description", "")
    if title is not None:
        snippet["title"] = title
    if description is not None:
        snippet["description"] = description
    status = {"privacyStatus": cur_status.get("privacyStatus", "unlisted"),
              "selfDeclaredMadeForKids": bool(cur_status.get("selfDeclaredMadeForKids", False))}
    if privacy is not None:
        if privacy not in YT_PRIVACY:
            raise ValueError(f"privacy must be one of {YT_PRIVACY}")
        status["privacyStatus"] = privacy
    if made_for_kids is not None:
        status["selfDeclaredMadeForKids"] = bool(made_for_kids)
    return snippet, status


def _write_token_private(creds):
    """Persist the OAuth token with owner-only permissions (0600) — it grants channel
    access, so it must not be world-readable on a shared machine."""
    fd = os.open(YT_TOKEN_FILE, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(creds.to_json())
    try:
        os.chmod(YT_TOKEN_FILE, 0o600)   # tighten even if the file pre-existed at 0644
    except OSError:
        pass


def _youtube_service(allow_interactive=True):
    """Authorise and return (service, None) or (None, error_message).

    allow_interactive: the FIRST authorisation opens a browser on THIS machine (the
    streaming laptop) and blocks for the Google login. A remote operator (Tailscale/
    Cloudflare) can't complete that — the browser would open on the wrong computer and
    tie up the request. So the endpoint passes allow_interactive only for genuine
    loopback callers; a remote first-run gets a clear "authorise once locally" message
    instead of a hung browser popup on the host."""
    if not os.path.exists(YT_CREDS_FILE):
        return None, ("yt_credentials.json not found — see YouTube setup instructions in "
                      "the control panel")
    try:
        from google.oauth2.credentials import Credentials
        from google_auth_oauthlib.flow import InstalledAppFlow
        from google.auth.transport.requests import Request
        from googleapiclient.discovery import build
    except ImportError:
        return None, "Run: pip install google-api-python-client google-auth-oauthlib"
    creds = None
    if os.path.exists(YT_TOKEN_FILE):
        creds = Credentials.from_authorized_user_file(YT_TOKEN_FILE, YT_SCOPES)
    if not creds or not creds.valid:
        if creds and creds.expired and creds.refresh_token:
            creds.refresh(Request())
            _write_token_private(creds)
        elif allow_interactive:
            flow = InstalledAppFlow.from_client_secrets_file(YT_CREDS_FILE, YT_SCOPES)
            creds = flow.run_local_server(port=8091, open_browser=True)
            _write_token_private(creds)
        else:
            return None, ("YouTube isn't authorised yet — do it once ON THE STREAMING "
                          "LAPTOP (the control panel there), not remotely: the Google "
                          "login opens a browser on the machine running the server.")
    return build("youtube", "v3", credentials=creds), None


def update_youtube_broadcast(title=None, description=None, privacy=None,
                             made_for_kids=None, category_id=None, allow_interactive=True):
    """Update the active (else upcoming) broadcast's metadata. Any argument left None is
    unchanged. Returns (ok, message). category is on the underlying video, updated
    separately; a category failure is reported but doesn't fail the rest.
    allow_interactive False blocks the first-run browser auth (see _youtube_service)."""
    try:
        if privacy is not None and privacy not in YT_PRIVACY:
            return False, f"privacy must be one of {', '.join(YT_PRIVACY)}"
        yt, err = _youtube_service(allow_interactive=allow_interactive)
        if err:
            return False, err
        resp = yt.liveBroadcasts().list(part="id,snippet,status", broadcastStatus="active",
                                        broadcastType="all").execute()
        items = resp.get("items", [])
        if not items:
            resp = yt.liveBroadcasts().list(part="id,snippet,status", broadcastStatus="upcoming",
                                            broadcastType="all").execute()
            items = resp.get("items", [])
        if not items:
            return False, "No active or upcoming broadcast found on this YouTube account"
        bc  = items[0]
        bid = bc["id"]
        snippet, status = _yt_broadcast_payload(
            bc.get("snippet", {}), bc.get("status", {}),
            title=title, description=description, privacy=privacy, made_for_kids=made_for_kids)
        yt.liveBroadcasts().update(part="snippet,status",
                                   body={"id": bid, "snippet": snippet, "status": status}).execute()
        done = ["metadata"]
        # Category lives on the video resource, not the broadcast — update it separately.
        cat_note = ""
        if category_id:
            try:
                vresp = yt.videos().list(part="snippet", id=bid).execute()
                vitems = vresp.get("items", [])
                if vitems:
                    vsnip = vitems[0]["snippet"]
                    vsnip["categoryId"] = str(category_id)
                    if title is not None:
                        vsnip["title"] = title       # keep in sync with the broadcast update
                    yt.videos().update(part="snippet", body={"id": bid, "snippet": vsnip}).execute()
                    done.append("category")
            except Exception as ce:
                cat_note = f" (category not set: {ce})"
        print(f"  ✓  YouTube broadcast updated: {', '.join(done)}")
        return True, (f"Updated: {', '.join(done)}{cat_note}")
    except Exception as e:
        return False, str(e)


def update_youtube_title(title):
    """Backwards-compatible thin wrapper — title only."""
    ok, msg = update_youtube_broadcast(title=title)
    return ok, (f"Title updated to: {title}" if ok else msg)

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
_event_buffer  = []   # queued wicket events for the overlay (and only the overlay) to consume
_event_buffer_lock = threading.Lock()   # append (live thread) vs pop (overlay poll) race

def buffer_pcs_events(state):
    """Detect boundaries/wickets from PCS state and buffer them for the overlay."""
    global _event_buffer
    score   = state.get("score", 0)
    wickets = state.get("wickets", 0)
    prev_s  = _prev_state["score"]
    prev_w  = _prev_state["wickets"]
    if prev_s is None:
        # First poll: seed the baseline HERE, unconditionally. Seeding used to happen only
        # inside check_commentary_trigger, which is gated on the graphics_commentary toggle
        # (off by default) — so with it off, _prev_state stayed None all match and no wicket
        # ever reached the event buffer or the match log's fall-of-wickets list.
        _prev_state.update({"score": score, "wickets": wickets,
                            "overs": state.get("overs", 0.0)})
        return
    delta = score - prev_s
    dw    = wickets - prev_w
    if dw > 0:
        with _event_buffer_lock:
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
    # Cap buffer size (trim in place — everyone must keep seeing the same list object)
    with _event_buffer_lock:
        del _event_buffer[:-20]

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
MAX_BODY_BYTES = 1024 * 1024   # 1 MB — every POST body is small JSON; cheap guard against junk

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
    "graphics_over_summary":   True,
    "graphics_partnership_display":True,
    "graphics_runrate_trend":  True,
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
    "network_test_mbps":       None,
    "network_test_at":         0,
    "stream_auto_downshift":   False,   # sentinel may auto-reduce bitrate on sustained congestion
    "youtube_title_template":  "LIVE: {home} vs {away}",
    "youtube_description":      "Live grassroots cricket. {home} v {away}.",
    "youtube_privacy":          "unlisted",
    "youtube_made_for_kids":    False,
    "youtube_category":         "17",
    "weather_api_key":         "",
    "logos_folder":           "",
    "headshots_folder":       "",
    "roster":                 {},
    "socials_folder":         "",
    "sponsor_name":           "",
    "sponsor_id":             "",
    "home_club_id":           "",
    "ground_filter":          "",
    "away_club_id":           "",
}

_last_good_state = None   # cached last successful load, used if the file is mid-write/corrupt

# Keys whose values must never be sent to a browser. GET /state replaces a stored value
# with SECRET_SENTINEL; POST /state ignores fields whose value IS the sentinel, so the
# control panel can round-trip its form without wiping stored secrets. "" still clears.
# control_token/club_password can never actually reach match_state.json today (they live only
# in config.ini's [Auth] section and _seed_state_from_config's MAPPING doesn't copy them) —
# they're listed here anyway as a defensive backstop in case that ever changes.
SECRET_KEYS = ("anthropic_api_key", "playcricket_api_key", "api_token",
               "weather_api_key", "obs_password", "camera_rtsp_url",
               "control_token", "club_password", "youtube_stream_key")
SECRET_SENTINEL = "••••••••"

# mtime-keyed cache: load_state() is called several times per overlay poll (the /live
# handler, the ball logger, the match-id lookup, /state itself, the panel's pollers, the
# watchdog, the stream sentinel...). The file only actually changes on a panel save, so
# re-reading + re-parsing it from disk each time is pure waste — it matters on the old
# streaming laptop that's also running the encoder. Keyed on (path, mtime_ns, size):
# save_state()'s os.replace always bumps the key, and tests that repoint STATE_FILE at a
# temp dir miss the cache naturally.
_state_cache = {"key": None, "data": None}
_state_cache_lock = threading.Lock()

def load_state():
    global _last_good_state
    try:
        stt = os.stat(STATE_FILE)
        key = (STATE_FILE, stt.st_mtime_ns, stt.st_size)
    except OSError:
        return DEFAULT_STATE.copy()
    with _state_cache_lock:
        if _state_cache["key"] == key:
            # Shallow copy: callers mutate the top level (s.update(...)) before saving
            return dict(_state_cache["data"])
    try:
        with open(STATE_FILE) as f:
            loaded = json.load(f)
        if not isinstance(loaded, dict):
            raise ValueError(f"state file holds {type(loaded).__name__}, expected object")
        state = {**DEFAULT_STATE, **loaded}
        _last_good_state = state
        with _state_cache_lock:
            _state_cache["key"] = key
            _state_cache["data"] = state
        return dict(state)
    except (json.JSONDecodeError, OSError, ValueError) as e:
        # File was mid-write or corrupt — don't crash /state. Use the last good copy
        # if we have one, otherwise fall back to defaults.
        print(f"  ⚠  state read failed ({e}); using last-good")
        return dict(_last_good_state) if _last_good_state else DEFAULT_STATE.copy()

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
    try:
        stt = os.stat(STATE_FILE)
        with _state_cache_lock:
            _state_cache["key"] = (STATE_FILE, stt.st_mtime_ns, stt.st_size)
            _state_cache["data"] = dict(s)
    except OSError:
        pass


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

    # Build a factual match summary from a defensive snapshot of the log (see helper).
    snap = match_log_snapshot_copy()

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
                 "matches_used": 0, "calls": 0, "error": None, "build_started": None}
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
        _season_stats["building"]      = True
        _season_stats["build_started"] = time.time()
        _season_stats["error"]         = None

    # Everything below runs with 'building' True and no lock held (network calls can take a
    # while). This try/finally is the fix for a real bug: an unexpected exception here used to
    # leave 'building' stuck True forever, permanently wedging the season-stats feature until
    # the server was restarted. Now it always clears, even on a crash — the next request
    # (or the watchdog, as a second line of defense) can retry.
    try:
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
                  "top_scorers": top_scorers, "top_bowlers": top_bowlers, "build_started": None,
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
    except Exception as e:
        with _season_stats_lock:
            _season_stats["building"]      = False
            _season_stats["build_started"] = None
            _season_stats["error"]         = f"build crashed: {e}"
        print(f"  ✗  Season stats: build crashed — {e}")
        return _season_stats


# ── Server self-metrics + error flight recorder ────────────────
# The operator's machine also runs OBS's encoder, so "is the server staying lightweight?"
# should be a number in /health, not a promise. And handler-thread exceptions print to a
# console nobody reads mid-match — a small ring buffer makes "something's throwing"
# visible in /health before it becomes "graphics stopped".
_self_stat = {"t": SERVER_START_TIME, "cpu": 0.0}
_server_errors = []
_server_errors_lock = threading.Lock()


def log_server_error(where, exc):
    with _server_errors_lock:
        _server_errors.append({"time": time.time(), "where": where,
                               "error": str(exc)[:300]})
        del _server_errors[:-30]


def _thread_excepthook(args):
    name = getattr(args.thread, "name", "?")
    log_server_error(f"thread {name}", args.exc_value or args.exc_type)
    print(f"  ✗  Unhandled error in thread {name}: {args.exc_value}")


threading.excepthook = _thread_excepthook


def _recent_server_errors(n=5):
    with _server_errors_lock:
        return list(_server_errors[-n:])


def _self_metrics():
    """CPU% of this process since the last /health call, peak memory, machine load."""
    import resource
    import sys as _sys
    now, cpu = time.time(), time.process_time()
    dt, dcpu = now - _self_stat["t"], cpu - _self_stat["cpu"]
    _self_stat.update({"t": now, "cpu": cpu})
    rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    rss_mb = round(rss / (1e6 if _sys.platform == "darwin" else 1024.0), 1)
    try:
        load1 = round(os.getloadavg()[0], 2)
    except (AttributeError, OSError):
        load1 = None
    return {"cpu_pct": round(100 * dcpu / dt, 1) if dt > 1 else None,
            "max_rss_mb": rss_mb, "machine_load_1m": load1}


# ── Self-healing watchdog ──────────────────────────────────────
# Runs quietly in the background for the life of the process and periodically checks the
# few things known to be able to get stuck during a long match day, fixing what it safely
# can and just logging what it can't (an external process like the scorer's laptop isn't
# something this server can restart for you).
WATCHDOG_INTERVAL_SEC   = 90
SEASON_BUILD_STUCK_SEC  = 300   # defense in depth — build_season_stats' own try/finally
                                # should already prevent this; this is a second line of defense
RATE_LIMIT_TS_MAX_AGE   = 3600  # drop old cooldown timestamps so the dict doesn't grow all day

_watchdog_status = {"last_run": None, "runs": 0, "fixes_applied": [], "pcs_was_fresh": None}
_watchdog_lock   = threading.Lock()

def _watchdog_log_fix(what):
    print(f"  🩹  Watchdog: {what}")
    with _watchdog_lock:
        _watchdog_status["fixes_applied"].append({"time": time.time(), "what": what})
        _watchdog_status["fixes_applied"] = _watchdog_status["fixes_applied"][-20:]

def _watchdog_tick():
    global _cf_restart_count
    now = time.time()

    # 1) Season-stats build stuck 'building' true — reset so the next request can retry.
    started = _season_stats.get("build_started")
    if _season_stats.get("building") and started and (now - started) > SEASON_BUILD_STUCK_SEC:
        with _season_stats_lock:
            _season_stats["building"]      = False
            _season_stats["build_started"] = None
            _season_stats["error"]         = "watchdog: build looked stuck and was reset"
        _watchdog_log_fix("season stats build looked stuck — reset so it can retry")

    # 2) Cloudflare Tunnel died unexpectedly — restart it, capped so a real problem doesn't
    # loop forever.
    if _CLOUDFLARE_TUNNEL and _CLUB_PASSWORD:
        with _cf_lock:
            proc = _cf_process
        if proc is not None and proc.poll() is not None:
            if _cf_restart_count < CF_MAX_RESTARTS:
                _cf_restart_count += 1
                _watchdog_log_fix(f"Cloudflare Tunnel had stopped — restarting "
                                  f"(attempt {_cf_restart_count}/{CF_MAX_RESTARTS})")
                _start_cloudflare_tunnel()
            elif _cf_restart_count == CF_MAX_RESTARTS:
                _cf_restart_count += 1   # bump past the cap so this only prints once
                print("  ✗  Watchdog: Cloudflare Tunnel keeps dying — giving up auto-restart, "
                      "check cloudflared manually")

    # 3) Trim old rate-limit cooldown timestamps (memory hygiene over a long match day).
    with _rate_limit_lock:
        stale = [k for k, ts in _rate_limit_ts.items() if now - ts > RATE_LIMIT_TS_MAX_AGE]
        for k in stale:
            del _rate_limit_ts[k]

    # 4) PCS scorer feed freshness — visibility only. The server can't restart the scorer's
    # laptop, so this just logs the transition instead of silently sitting stale all match.
    try:
        s        = load_state()
        pcs_dir  = os.path.expanduser(s.get("pcs_output_folder","").strip())
        pcs_path = find_pcs_output_file(pcs_dir) if pcs_dir else None
        fresh    = bool(pcs_path) and (now - os.path.getmtime(pcs_path)) < 120
    except Exception:
        fresh = None
    with _watchdog_lock:
        was = _watchdog_status["pcs_was_fresh"]
        if was and fresh is False:
            print("  ⚠  Watchdog: PCS scorer feed has gone stale (2+ min old) — check the scorer's laptop")
        elif was is False and fresh:
            print("  ✓  Watchdog: PCS scorer feed is fresh again")
        _watchdog_status["pcs_was_fresh"] = fresh
        _watchdog_status["last_run"] = now
        _watchdog_status["runs"]    += 1

def _watchdog_loop():
    while True:
        try:
            _watchdog_tick()
        except Exception as e:
            # Must never let the watchdog itself die — that would be the one thing nobody's
            # watching. Log and try again next tick.
            print(f"  ✗  Watchdog tick failed (will retry): {e}")
        time.sleep(WATCHDOG_INTERVAL_SEC)

def start_watchdog():
    threading.Thread(target=_watchdog_loop, daemon=True).start()


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

    # Reuse the same factual summary the report generator builds — from the same defensive
    # snapshot (reading _match_log directly here could raise if a ball event lands mid-read).
    snap = match_log_snapshot_copy()
    st = _pcs_last_state or {}
    home = cfg.get("home_team","") or cfg.get("name","Home")
    away = cfg.get("away_team","Opposition")
    comp = cfg.get("competition","")
    lines = [f"Match: {home} v {away}" + (f" ({comp})" if comp else "")]
    for inn_no in sorted(snap["innings"].keys()):
        r = snap["innings"][inn_no]
        lines.append(f"Innings {inn_no}: {r.get('batting_team','?')} "
                     f"{r.get('score',0)}-{r.get('wickets',0)} ({r.get('overs',0)} overs)")
    if snap["fall_of_wickets"]:
        lines.append("Wickets: " + "; ".join(
            f"{w.get('batter','?')} {w.get('howout','')} at {w.get('score','?')}"
            for w in snap["fall_of_wickets"][:12]))
    if snap["milestones"]:
        lines.append("Milestones: " + "; ".join(
            f"{m.get('batter','?')} {m.get('milestone','')}" for m in snap["milestones"][:8]))
    if snap["events"]:
        lines.append("Key moments: " + " | ".join(e["detail"] for e in snap["events"][-25:]))
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
        "pcs_overs":           overs,
        # Template's own field when mapped; else compute from target (these two keys used to
        # appear TWICE in this literal, silently discarding a computed-from-target value)
        "runsRequired":        gi("runs_required", 0) or (max(0, target - runs) if target else 0),
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


# ── Manual scoring (/scoring) ─────────────────────────────────
# For clubs (or match days) without NV Play/PCS Pro: a phone/tablet page of big buttons
# drives the SAME InningsEngine the simulator uses, and manual_live_state() renders its
# frames through parse_pcs_json — so the overlay, ball-by-ball DB, highlights tagging and
# every graphic work unchanged, exactly as if a scorer's feed were present.
#
# Event-sourced: every button press is an event; undo pops the last event and replays the
# rest through the (deterministic) engine, so the book after an undo is provably identical
# to never having pressed the button. The event log persists to manual_scoring.json after
# every action — a server restart or a dropped phone resumes mid-over.

MANUAL_SCORING_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                   "manual_scoring.json")
_manual_lock = threading.Lock()
_manual = {"session": None, "load_attempted": False}


class ManualScoringSession:
    def __init__(self, config):
        cfg = dict(config or {})
        cfg["home"] = (cfg.get("home") or "Home CC").strip()
        cfg["away"] = (cfg.get("away") or "Opposition CC").strip()
        cfg["max_overs"] = max(1, min(120, int(cfg.get("max_overs") or 40)))
        cfg["batting_first"] = "away" if cfg.get("batting_first") == "away" else "home"
        self.config = cfg
        self.events = []
        self.innings = []
        self._start_innings()

    # ── Teams ────────────────────────────────────────────────
    @staticmethod
    def _xi(names, side_label):
        """Pad/trim a list of names to a full XI; blanks become 'Home CC 4' style
        placeholders so the card and lineup graphics always have eleven rows."""
        clean = [str(n).strip() for n in (names or []) if str(n).strip()]
        while len(clean) < 11:
            clean.append(f"{side_label} {len(clean) + 1}")
        return [("", n) for n in clean[:11]]

    def _sides(self, innings_no):
        cfg = self.config
        home_bats = (cfg["batting_first"] == "home") == (innings_no == 1)
        if home_bats:
            return (self._xi(cfg.get("home_xi"), cfg["home"]),
                    self._xi(cfg.get("away_xi"), cfg["away"]), cfg["home"], cfg["away"])
        return (self._xi(cfg.get("away_xi"), cfg["away"]),
                self._xi(cfg.get("home_xi"), cfg["home"]), cfg["away"], cfg["home"])

    def _start_innings(self):
        n = len(self.innings) + 1
        bat_xi, bowl_xi, bat_nm, bowl_nm = self._sides(n)
        target = self.innings[0].total + 1 if n == 2 else None
        self.innings.append(InningsEngine(
            bat_xi, bowl_xi, bat_nm, bowl_nm, self.config["max_overs"],
            target=target, openers_selected=True))

    @property
    def current(self):
        return self.innings[-1]

    @property
    def innings_no(self):
        return len(self.innings)

    @property
    def match_over(self):
        return self.innings_no == 2 and self.current.complete

    # ── Events ───────────────────────────────────────────────
    def _apply(self, ev, lenient=False):
        """lenient=True is used when REPLAYING after an edit: a corrected ball can make
        later auxiliary events impossible (a next-batter pick for someone who no longer
        arrived, a bowler set for an over that moved) — those are skipped rather than
        wedging the whole rebuild. Ball events themselves are never skipped."""
        kind = ev.get("event")
        inn = self.current
        if lenient and kind in ("batter", "bowler", "swap_strike"):
            try:
                self._apply(ev)
            except ValueError:
                pass
            return
        if lenient and kind == "start_innings":
            if self.innings_no == 1 and not inn.complete:
                inn.end()          # the edit un-completed innings 1 — declare it closed
        if kind == "ball":
            inn.apply_outcome(ev.get("kind", ""), runs=int(ev.get("runs", 0) or 0),
                              wicket_type=ev.get("wicket_type", "bowled"),
                              fielder=(ev.get("fielder") or "").strip(),
                              out_non_striker=bool(ev.get("out_non_striker")))
        elif kind == "bowler":
            name = (ev.get("name") or "").strip()
            if not name:
                raise ValueError("bowler name required")
            inn.set_bowler(name)
        elif kind == "batter":
            inn.choose_next_batter((ev.get("name") or "").strip())
        elif kind == "swap_strike":
            inn.swap_strike()
        elif kind == "end_innings":
            inn.end()
        elif kind == "start_innings":
            if self.innings_no != 1:
                raise ValueError("the second innings has already started")
            if not inn.complete:
                raise ValueError("end the first innings before starting the second")
            self._start_innings()
        else:
            raise ValueError(f"unknown event {kind!r}")

    def apply(self, ev):
        """Validate + apply one operator action, record it, persist."""
        self._apply(ev)                # raises before anything is recorded on bad input
        self.events.append(ev)
        self.save()

    def undo(self):
        if not self.events:
            raise ValueError("nothing to undo")
        dropped = self.events.pop()
        self._rebuild()
        self.save()
        return dropped

    def _rebuild(self, lenient=False):
        """Replay the event log from a fresh book — the engine is deterministic, so this
        is exact. Also how a saved session is restored after a server restart. Exception-
        safe: if a replay raises (a rejected edit), self.events is left INTACT so the
        caller can roll back — never clobbered to the partial rebuild list."""
        events = self.events
        self.events = []
        self.innings = []
        self._start_innings()
        try:
            for ev in events:
                self._apply(ev, lenient=lenient)   # was valid when first applied
        finally:
            self.events = events

    def edit_ball(self, index, new_ev):
        """Replace ball event `index` and replay the innings around it — the whole point
        of event sourcing. Auxiliary events invalidated by the correction are skipped
        (lenient replay); if the corrected BALL itself can't replay, everything is
        restored untouched and the error raised."""
        if not (0 <= index < len(self.events)) or self.events[index].get("event") != "ball":
            raise ValueError("that isn't a ball that can be edited")
        old = self.events[index]
        self.events[index] = new_ev
        try:
            self._rebuild(lenient=True)
        except ValueError:
            self.events[index] = old
            self._rebuild(lenient=True)
            raise
        self.save()

    def ball_list(self, limit=30):
        """The most recent ball events with their positions, for the edit-a-ball picker:
        [{index, innings, over, ball, token}], newest last. Computed by replaying a fresh
        book (cheap, deterministic, no side effects on this session)."""
        temp = ManualScoringSession(dict(self.config))
        out = []
        for i, ev in enumerate(self.events):
            if ev.get("event") == "ball":
                inn = temp.current
                pos = (temp.innings_no, inn.current_over_no, len(inn.over_tokens) + 1)
                temp._apply(ev, lenient=True)
                inn = temp.innings[pos[0] - 1]
                token = (inn.over_tokens[-1] if inn.over_tokens
                         else (inn.token_history[-1][0][-1] if inn.token_history else "?"))
                out.append({"index": i, "innings": pos[0], "over": pos[1] + 1,
                            "ball": pos[2], "token": token})
            else:
                temp._apply(ev, lenient=True)
        return out[-limit:]

    def scorecard_text(self):
        """Plain-text scorecard for BOTH innings — everything Play-Cricket's manual result
        entry asks for (their API is read-only, so transcription is the best possible)."""
        cfg = self.config
        lines = [f"{cfg['home']} v {cfg['away']} — {datetime.date.today().strftime('%d %B %Y')}",
                 f"{cfg['max_overs']} overs per innings", ""]
        for n, inn in enumerate(self.innings, 1):
            lines.append(f"═══ Innings {n}: {inn.batting_name} "
                         f"{inn.total}-{inn.wkts} ({inn.overs_str()} ov) ═══")
            lines.append("")
            lines.append(f"{'BATTING':<24} {'':28} {'R':>4} {'B':>4} {'4s':>3} {'6s':>3}")
            for b in inn.batters:
                if b.balls == 0 and b.runs == 0 and not b.how_out \
                        and b not in (inn.striker, inn.non_striker):
                    continue                       # didn't bat
                how = b.how_out if b.how_out else "not out"
                lines.append(f"{b.name:<24} {how.lower():<28} {b.runs:>4} {b.balls:>4} "
                             f"{b.fours:>3} {b.sixes:>3}")
            lines.append(f"{'Extras':<24} {'':28} {inn.extras:>4}")
            lines.append(f"{'TOTAL':<24} {f'({inn.wkts} wkts, {inn.overs_str()} ov)':<28} "
                         f"{inn.total:>4}")
            lines.append("")
            lines.append(f"{'BOWLING':<24} {'O':>6} {'M':>3} {'R':>4} {'W':>3}")
            for w in inn.all_bowlers():
                if not (w.legal or w.runs):
                    continue
                lines.append(f"{w.name:<24} {w.overs_str:>6} {w.maidens:>3} {w.runs:>4} "
                             f"{w.wkts:>3}")
            lines.append("")
        if self.match_over:
            inn1, inn2 = self.innings
            if inn2.total >= (inn2.target or 0):
                margin = 10 - inn2.wkts
                lines.append(f"RESULT: {inn2.batting_name} won by {margin} wicket"
                             f"{'s' if margin != 1 else ''}")
            elif inn1.total > inn2.total:
                diff = inn1.total - inn2.total
                lines.append(f"RESULT: {inn1.batting_name} won by {diff} run"
                             f"{'s' if diff != 1 else ''}")
            else:
                lines.append("RESULT: Match tied")
        lines.append("")
        lines.append("(Generated by CricketStream Overlay manual scoring — enter into "
                     "Play-Cricket result entry; the API is read-only so this can't be "
                     "submitted automatically.)")
        return "\n".join(lines)

    # ── Persistence ──────────────────────────────────────────
    def save(self):
        try:
            tmp = MANUAL_SCORING_FILE + ".tmp"
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump({"config": self.config, "events": self.events}, f)
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp, MANUAL_SCORING_FILE)
        except OSError as e:
            # Scoring continues in memory — losing restart-resilience beats losing the ball
            print(f"  ⚠  Manual scoring: could not save session ({e})")

    @classmethod
    def load(cls):
        with open(MANUAL_SCORING_FILE, encoding="utf-8") as f:
            data = json.load(f)
        sess = cls(data.get("config") or {})
        sess.events = list(data.get("events") or [])
        sess._rebuild()
        return sess


def _manual_session_locked():
    """Current session, lazily restoring a saved one after a restart. Caller holds the lock."""
    if _manual["session"] is None and not _manual["load_attempted"]:
        _manual["load_attempted"] = True
        if os.path.exists(MANUAL_SCORING_FILE):
            try:
                _manual["session"] = ManualScoringSession.load()
                s = _manual["session"]
                print(f"  ✓  Manual scoring: resumed saved session — "
                      f"{s.current.batting_name} {s.current.total}-{s.current.wkts} "
                      f"({s.current.overs_str()} ov), innings {s.innings_no}")
            except Exception as e:
                print(f"  ⚠  Manual scoring: saved session unreadable ({e}) — ignoring it")
    return _manual["session"]


def manual_live_state():
    """Overlay state from the manual scoring session, or None when not active. Rendered
    through parse_pcs_json so it is EXACTLY what the PCS file path would produce."""
    with _manual_lock:
        sess = _manual_session_locked()
        if not sess:
            return None
        frame = sess.current.frame(sess.innings_no)
    return parse_pcs_json(frame)


def manual_ui_state():
    """Everything the /scoring page needs to draw itself."""
    with _manual_lock:
        sess = _manual_session_locked()
        if not sess:
            return {"active": False}
        inn = sess.current
        bowler = inn.bowler_for_over(inn.current_over_no)
        arrived = None      # the auto-arrived new batter who hasn't faced a ball yet
        for b in (inn.striker, inn.non_striker):
            if (not b.how_out and b.balls == 0 and b.runs == 0 and inn.next_bat > 2
                    and inn.batters.index(b) == inn.next_bat - 1):
                arrived = b.name
        return {
            "active": True, "innings": sess.innings_no,
            "batting": inn.batting_name, "bowling": inn.bowling_name,
            "score": inn.total, "wickets": inn.wkts,
            "overs": inn.overs_str(), "max_overs": inn.max_overs,
            "striker": {"name": inn.striker.name, "runs": inn.striker.runs,
                        "balls": inn.striker.balls},
            "non_striker": {"name": inn.non_striker.name, "runs": inn.non_striker.runs,
                            "balls": inn.non_striker.balls},
            "bowler": {"name": bowler.name,
                       "figs": f"{bowler.overs_str}-{bowler.maidens}-{bowler.runs}-{bowler.wkts}"},
            "this_over": list(inn.over_tokens),
            "target": inn.target,
            "need": max(0, inn.target - inn.total) if inn.target else None,
            "innings_complete": inn.complete,
            "match_over": sess.match_over,
            "awaiting_bowler": inn.awaiting_new_over,
            # Recap of the just-completed over, for the scorer's end-of-over banner —
            # confirm and pick the next bowler, or undo back into the over to fix a ball
            "last_over": ({"num": len(inn.over_history),
                           "runs": inn.over_history[-1],
                           "balls": list(inn.token_history[-1][0])}
                          if inn.token_history else None),
            "new_batter": arrived,
            "yet_to_bat": [b.name for b in inn.batters[inn.next_bat:] if not b.how_out],
            "bowling_xi": list(inn.fielders),
            "last_wicket": inn.last_wicket["howout"],
            "events": len(sess.events),
        }


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

    # Auto-abbreviate opposition: first word up to 5 chars, uppercased. Abbreviate the CLUB
    # name (e.g. "Ivybridge CC"), not the team label (e.g. "Under 15" or "1st XI") — PlayCricket
    # gives every age-group/adult team a generic label like that on both sides of the fixture,
    # so abbreviating opp_name instead of opp_club produced meaningless abbreviations like
    # "UNDER" or "1ST" for the opposition. Fall back to opp_name only if opp_club is blank.
    abbrev_source = opp_club or opp_name
    opp_words = abbrev_source.replace(" CC","").replace(" Cricket Club","").strip().split()
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

def obs_trigger_replay(state, reason=""):
    """
    Full OBS WebSocket v5 flow (runs in a background thread).
    Uses a message-ID tracking approach compatible with OBS 28+.
    """
    try:
        import websocket
    except ImportError:
        print("  ✗  websocket-client not installed. Run: pip install websocket-client")
        return

    # Caption is built from the match state NOW, at trigger time — by the time OBS has
    # saved the clip a few seconds from here, the strike may have rotated or a new
    # batter walked in, and the caption would describe the wrong moment. Same source
    # precedence as /live: a live manual-scoring session outranks the PCS file state
    # (which manual mode never updates — using it here captioned clips with stale data).
    clip_caption = make_clip_caption(reason, manual_live_state() or _pcs_last_state)

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
            # Tag the clip with why it was captured — feeds the highlights compiler
            log_replay_clip(clip_path, reason, clip_caption)
            if clip_caption:
                print(f"  🏷  Tagged: {clip_caption}")

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

# ── Replay clip tagging (auto-tagged highlights) ──────────────
# Every replay the overlay triggers already knows WHY it fired (wicket / four / six /
# milestone). Recording that reason against the saved clip file — plus the live match
# context at that moment — is what lets the highlights compiler caption each clip and
# write a chaptered description, with no operator effort.

def make_clip_caption(reason, match_state):
    """One broadcast-style caption line for a clip, from the trigger reason plus the live
    match state at that moment. Returns '' for reasons that shouldn't be tagged (tests)."""
    r = (reason or "").strip()
    if not r or r.lower() == "test":
        return ""
    st = match_state or {}
    score_bit = ""
    if st.get("battingTeamName"):
        score_bit = f"{st.get('score', 0)}-{st.get('wickets', 0)} ({st.get('overs', 0)} ov)"

    def striker():
        b1, b2 = st.get("batter1") or {}, st.get("batter2") or {}
        b = b1 if b1.get("onStrike") else b2
        if not b.get("name") or b.get("name") == "—":
            return ""
        return f"{b['name']} {b.get('runs', 0)}*"

    low = r.lower()
    if low == "wicket":
        parts = ["WICKET", st.get("lastWicketBatter", ""), st.get("lastWicketHowOut", "")]
    elif low == "boundary":
        parts = ["FOUR", striker()]
    elif low == "six":
        parts = ["SIX", striker()]
    elif low.startswith("century"):
        parts = ["CENTURY", r.split("-", 1)[1].strip() if "-" in r else ""]
    elif low.startswith("fifty"):
        parts = ["FIFTY", r.split("-", 1)[1].strip() if "-" in r else ""]
    else:
        parts = [r.upper()]
    parts.append(score_bit)
    return "  ·  ".join(p for p in parts if p)


def log_replay_clip(clip_path, reason, caption):
    """Tag a saved replay clip in the DB. Never raises — same rule as log_ball_data."""
    try:
        if not clip_path or not caption:
            return
        now = datetime.datetime.now().isoformat(timespec="seconds")
        with _db_lock, _db() as c:
            c.execute("INSERT OR REPLACE INTO clips(match_id,file,ts,reason,caption) "
                      "VALUES(?,?,?,?,?)",
                      (current_match_id(), os.path.basename(clip_path), now, reason, caption))
    except Exception:
        pass


def clip_tags(files):
    """Map clip basename → {reason, caption} from the DB, for whichever files exist."""
    try:
        with _db_lock, _db() as c:
            rows = c.execute("SELECT file, reason, caption FROM clips").fetchall()
        wanted = {os.path.basename(f) for f in files}
        return {r[0]: {"reason": r[1], "caption": r[2]} for r in rows if r[0] in wanted}
    except Exception:
        return {}


def guess_clip_tags(files, window_sec=90):
    """Fallback for clips saved manually (OBS hotkey) with no trigger reason on record:
    correlate the file's mtime against notable balls (W/4/6) in the ball-by-ball log and
    caption from the closest one inside the window. Returns basename → caption."""
    out = {}
    try:
        with _db_lock, _db() as c:
            rows = c.execute(
                "SELECT ts, outcome, is_wicket, batter, bowler, cum_runs, cum_wkts, over "
                "FROM balls WHERE is_wicket=1 OR outcome IN ('4','6')").fetchall()
        if not rows:
            return out
        events = []
        for ts, outcome, is_wkt, batter, bowler, runs, wkts, over in rows:
            try:
                t = datetime.datetime.fromisoformat(ts).timestamp()
            except ValueError:
                continue
            events.append((t, outcome, is_wkt, batter, bowler, runs, wkts, over))
        for f in files:
            try:
                mtime = os.path.getmtime(f)
            except OSError:
                continue
            near = [e for e in events if abs(e[0] - mtime) <= window_sec]
            if not near:
                continue
            # prefer wickets, then sixes, then fours; closest in time within each class
            near.sort(key=lambda e: (-(e[2] * 2 + (e[1] == "6")), abs(e[0] - mtime)))
            t, outcome, is_wkt, batter, bowler, runs, wkts, over = near[0]
            label = "WICKET" if is_wkt else ("SIX" if outcome == "6" else "FOUR")
            who = (batter or "").strip()
            out[os.path.basename(f)] = "  ·  ".join(
                p for p in (label, who, f"{runs}-{wkts} (over {over + 1})") if p)
    except Exception:
        pass
    return out


def plan_highlights(files, tags):
    """Pure planning step (testable without ffmpeg): chronological clip order with each
    clip's caption, replay-trigger test clips excluded."""
    plan = []
    for f in sorted(files, key=os.path.getmtime):
        tag = tags.get(os.path.basename(f), {})
        if (tag.get("reason") or "").strip().lower() == "test":
            continue
        plan.append({"file": f, "caption": tag.get("caption", "")})
    return plan


def chapters_text(entries, title="Match highlights"):
    """YouTube-ready description with chapter timestamps.
    entries: [{caption, duration}] in reel order (duration in seconds)."""
    lines = [title, ""]
    t = 0.0
    for e in entries:
        mm, ss = divmod(int(t), 60)
        lines.append(f"{mm:02d}:{ss:02d} {e['caption'] or 'Replay'}")
        t += max(0.0, e.get("duration") or 0.0)
    return "\n".join(lines) + "\n"


def _font_path():
    """A real .ttf/.ttc path for ffmpeg's drawtext, cross-platform (same family list as
    the Instagram builder). None if nothing usable is found — captions are skipped then."""
    candidates = [
        "/System/Library/Fonts/Helvetica.ttc",
        "/System/Library/Fonts/Supplemental/Arial Bold.ttf",
        "C:/Windows/Fonts/arialbd.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
        "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    ]
    try:
        import PIL
        candidates.append(os.path.join(os.path.dirname(PIL.__file__), "fonts",
                                       "DejaVuSans-Bold.ttf"))
    except ImportError:
        pass
    for p in candidates:
        if os.path.exists(p):
            return p
    return None


def _clip_duration(path):
    """Clip length in seconds via ffprobe, or None if unavailable."""
    import shutil
    if not shutil.which("ffprobe"):
        return None
    try:
        r = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                            "-of", "csv=p=0", path],
                           capture_output=True, text=True, timeout=20)
        return float(r.stdout.strip()) if r.returncode == 0 else None
    except Exception:
        return None


# ── Shared one-shot OBS WebSocket call ─────────────────────────
def _obs_call(state, requests_list, timeout=6):
    """Connect, identify, run a list of (request_type, request_data) in order, disconnect.
    Returns a list of responseData dicts (None per request on failure), or None if OBS
    couldn't be reached/authenticated at all. Deliberately connection-per-call: callers
    are all low-frequency (>=15s apart), and never holding a socket means a mid-match OBS
    restart can't wedge anything."""
    try:
        import websocket
    except ImportError:
        return None
    host, port = state.get("obs_host", "localhost"), state.get("obs_port", 4455)
    password = state.get("obs_password", "")
    mid = [0]
    try:
        ws = websocket.create_connection(f"ws://{host}:{port}", timeout=timeout)
    except Exception:
        return None
    try:
        ws.settimeout(timeout)
        deadline = time.time() + timeout
        hello = None
        while time.time() < deadline:
            msg = json.loads(ws.recv() or "{}")
            if msg.get("op") == 0:
                hello = msg
                break
        if not hello:
            return None
        identify = {"rpcVersion": 1}
        auth = hello["d"].get("authentication")
        if auth and password:
            identify["authentication"] = _obs_auth_response(password, auth["salt"],
                                                            auth["challenge"])
        ws.send(json.dumps({"op": 1, "d": identify}))
        deadline = time.time() + timeout
        ok = False
        while time.time() < deadline:
            msg = json.loads(ws.recv() or "{}")
            if msg.get("op") == 2:
                ok = True
                break
        if not ok:
            return None
        results = []
        for rt, rd in requests_list:
            mid[0] += 1
            rid = str(mid[0])
            payload = {"requestType": rt, "requestId": rid}
            if rd is not None:
                payload["requestData"] = rd
            ws.send(json.dumps({"op": 6, "d": payload}))
            resp = None
            deadline = time.time() + timeout
            while time.time() < deadline:
                msg = json.loads(ws.recv() or "{}")
                if msg.get("op") == 7 and msg.get("d", {}).get("requestId") == rid:
                    d = msg["d"]
                    resp = (d.get("responseData") or {}) \
                        if d.get("requestStatus", {}).get("result") else None
                    if resp is None and d.get("requestStatus", {}).get("result"):
                        resp = {}
                    break
            results.append(resp)
        return results
    except Exception:
        return None
    finally:
        try:
            ws.close()
        except Exception:
            pass


# ── Adaptive stream quality (congestion sentinel + bitrate ladder) ─────────────
# Two tiers of defence against a struggling upload:
#   1. OBS's own Dynamic Bitrate (enabled by obs_setup.py) nudges the encoder bitrate up
#      and down seamlessly — no disconnect, no operator action. First line of defence.
#   2. This sentinel watches the stream output's congestion + dropped-frame counters every
#      STREAM_POLL_SEC while live. When trouble is sustained (not a blip), it can step the
#      configured bitrate DOWN a ladder — manually from the panel, or automatically when
#      the operator enables auto mode. A step requires stop → reconfigure → start (~5-10s
#      ingest gap; YouTube keeps the broadcast alive and viewers just see a short buffer),
#      so it is rate-limited by a cooldown and never "shifts up" on its own.
STREAM_POLL_SEC          = 15
STREAM_WINDOW_SEC        = 60     # judge over the last minute, not one bad sample
STREAM_CONGESTION_TRIP   = 0.15   # avg congestion (0..1) that counts as "struggling"
STREAM_DROPPED_PCT_TRIP  = 4.0    # % frames dropped across the window
STREAM_SHIFT_COOLDOWN    = 150    # seconds between automatic downshifts
STREAM_LADDER            = [1.0, 0.7, 0.5, 0.35]   # fractions of the baseline bitrate

_stream_mon = {
    "enabled_thread": False, "streaming": False, "reachable": None,
    "samples": [],            # [{t, congestion, dropped, total}] ring, newest last
    "baseline_kbps": None,    # VBitrate observed at step 0 while live
    "step": 0,                # index into STREAM_LADDER
    "current_kbps": None,
    "last_shift_at": 0.0,
    "shifts": [],             # [{t, kbps_from, kbps_to, reason, mode}] capped
    "last_reason": "",
    "dynamic_bitrate": None,  # OBS's own Dynamic Bitrate setting (None = not yet read)
    "simple_mode": None,      # ladder needs Simple output mode; None = not yet read
}
_stream_mon_lock = threading.Lock()


def evaluate_stream_samples(samples, now, last_shift_at,
                            window_sec=STREAM_WINDOW_SEC,
                            congestion_trip=STREAM_CONGESTION_TRIP,
                            dropped_pct_trip=STREAM_DROPPED_PCT_TRIP,
                            cooldown_sec=STREAM_SHIFT_COOLDOWN):
    """Pure decision: ('downshift'|'hold', reason). Needs sustained evidence across the
    window, and respects the cooldown so a shift's own restart blip can't trigger the next."""
    recent = [s for s in samples if now - s["t"] <= window_sec]
    if len(recent) < 3:
        return "hold", "not enough samples yet"
    if now - last_shift_at < cooldown_sec:
        return "hold", "cooling down after last quality change"
    avg_congestion = sum(s["congestion"] for s in recent) / len(recent)
    d_dropped = recent[-1]["dropped"] - recent[0]["dropped"]
    d_total = recent[-1]["total"] - recent[0]["total"]
    dropped_pct = (100.0 * d_dropped / d_total) if d_total > 0 else 0.0
    if avg_congestion >= congestion_trip:
        return "downshift", f"sustained congestion (avg {avg_congestion:.0%} over {window_sec}s)"
    if dropped_pct >= dropped_pct_trip:
        return "downshift", f"dropping frames ({dropped_pct:.1f}% over {window_sec}s)"
    return "hold", f"healthy (congestion {avg_congestion:.0%}, drops {dropped_pct:.1f}%)"


def _stream_ladder_kbps(baseline, step):
    step = max(0, min(step, len(STREAM_LADDER) - 1))
    return max(500, int(baseline * STREAM_LADDER[step]))


_quality_shift_lock = threading.Lock()


def apply_stream_quality_step(new_step, reason, mode):
    """Stop → wait → set SimpleOutput bitrate → start, verified at each step.
    Returns (ok, message). Serialized: a manual panel press and the auto path must never
    interleave two stop/start cycles."""
    if not _quality_shift_lock.acquire(blocking=False):
        return False, "A quality change is already in progress — wait for it to finish"
    try:
        return _apply_quality_step_locked(new_step, reason, mode)
    finally:
        _quality_shift_lock.release()


def _apply_quality_step_locked(new_step, reason, mode):
    st = load_state()
    with _stream_mon_lock:
        baseline = _stream_mon["baseline_kbps"]
    if not baseline:
        return False, "No baseline bitrate recorded yet — is the stream live?"
    new_step = max(0, min(new_step, len(STREAM_LADDER) - 1))
    target = _stream_ladder_kbps(baseline, new_step)
    status = _obs_call(st, [("GetStreamStatus", None),
                            ("GetProfileParameter", {"parameterCategory": "Output",
                                                     "parameterName": "Mode"})])
    if not status or status[0] is None:
        return False, "Could not reach OBS"
    # The ladder writes SimpleOutput/VBitrate — in Advanced output mode that parameter is
    # ignored, so a shift would restart the stream for NO quality change. Refuse instead.
    # (named output_mode: `mode` is this function's auto/manual parameter)
    output_mode = str((status[1] or {}).get("parameterValue") or "Simple")
    if output_mode != "Simple":
        return False, ("OBS is in Advanced output mode — the quality ladder only supports "
                       "the default Simple mode (OBS's own dynamic bitrate still works). "
                       "Change the bitrate manually in OBS Settings → Output.")
    was_live = bool(status[0].get("outputActive"))
    # OBS stops outputs ASYNCHRONOUSLY: firing StartStream straight after StopStream on
    # one connection gets rejected ("output still stopping") — which would leave the
    # stream DOWN while we report success. So: stop, wait for the output to actually go
    # inactive, reconfigure, then start — verifying each step.
    if was_live:
        r = _obs_call(st, [("StopStream", None)], timeout=8)
        if r is None or r[0] is None:
            return False, "Could not stop the stream — no quality change made"
        stopped = False
        for _ in range(20):                       # up to ~10s for the output to wind down
            time.sleep(0.5)
            s2 = _obs_call(st, [("GetStreamStatus", None)], timeout=5)
            if s2 and s2[0] is not None and not s2[0].get("outputActive") \
                    and not s2[0].get("outputReconnecting"):
                stopped = True
                break
        if not stopped:
            return False, ("Stream did not stop cleanly — no quality change made; "
                           "check OBS")
    r = _obs_call(st, [("SetProfileParameter", {"parameterCategory": "SimpleOutput",
                                                "parameterName": "VBitrate",
                                                "parameterValue": str(target)})], timeout=8)
    if r is None or r[0] is None:
        if was_live:
            _obs_call(st, [("StartStream", None)], timeout=8)   # best effort: get back on air
        return False, "Could not set the new bitrate — stream restarted at the old quality"
    if was_live:
        started = False
        for attempt in range(4):                  # OBS can still be settling — retry briefly
            r = _obs_call(st, [("StartStream", None)], timeout=8)
            if r is not None and r[0] is not None:
                started = True
                break
            time.sleep(1.5)
        if not started:
            # The bitrate DID change — record it so the ladder/restore stay truthful even
            # though the operator has to press Start Streaming themselves
            with _stream_mon_lock:
                _stream_mon["step"] = new_step
                _stream_mon["current_kbps"] = target
                _stream_mon["last_shift_at"] = time.time()
                _stream_mon["samples"] = []
            return False, (f"Bitrate set to {target} kbps but the stream DID NOT restart — "
                           f"press Start Streaming in OBS now")
        # OBS accepting StartStream is not the same as the stream surviving: with the
        # connected-YouTube-account integration + auto-stop, StopStream ENDS the broadcast
        # on YouTube, and the restart goes out into a dead broadcast. Verify it stayed up.
        time.sleep(3)
        s3 = _obs_call(st, [("GetStreamStatus", None)], timeout=5)
        if not (s3 and s3[0] is not None and s3[0].get("outputActive")):
            with _stream_mon_lock:
                _stream_mon["step"] = new_step
                _stream_mon["current_kbps"] = target
                _stream_mon["last_shift_at"] = time.time()
                _stream_mon["samples"] = []
            return False, (f"Bitrate set to {target} kbps and OBS restarted, but the stream "
                           f"did not STAY up. If YouTube shows the broadcast as ENDED, OBS's "
                           f"connected-account auto-stop ended it on StopStream — stream with "
                           f"a persistent stream key instead (YouTube Studio → copy key → OBS "
                           f"Settings → Stream), which survives quality changes.")
    with _stream_mon_lock:
        old = _stream_mon["current_kbps"] or baseline
        _stream_mon["step"] = new_step
        _stream_mon["current_kbps"] = target
        _stream_mon["last_shift_at"] = time.time()
        _stream_mon["samples"] = []          # judge the new setting on fresh evidence
        _stream_mon["shifts"].append({"t": time.time(), "kbps_from": old, "kbps_to": target,
                                      "reason": reason, "mode": mode})
        _stream_mon["shifts"] = _stream_mon["shifts"][-20:]
    pct = int(STREAM_LADDER[new_step] * 100)
    msg = (f"Stream bitrate {'restored' if new_step == 0 else 'reduced'} to {target} kbps "
           f"({pct}% of baseline){' — stream restarted' if was_live else ''}")
    print(f"  📶  {msg} [{mode}: {reason}]")
    return True, msg


def _stream_monitor_tick():
    st = load_state()
    results = _obs_call(st, [("GetStreamStatus", None),
                             ("GetProfileParameter", {"parameterCategory": "SimpleOutput",
                                                      "parameterName": "VBitrate"}),
                             ("GetProfileParameter", {"parameterCategory": "Output",
                                                      "parameterName": "DynamicBitrate"}),
                             ("GetProfileParameter", {"parameterCategory": "Output",
                                                      "parameterName": "Mode"})],
                        timeout=5)
    now = time.time()
    with _stream_mon_lock:
        if results is None or results[0] is None:
            _stream_mon["reachable"] = False
            _stream_mon["streaming"] = False
            _stream_mon["samples"] = []
            return None
        _stream_mon["reachable"] = True
        # Tier-1 defence status: is OBS's own Dynamic Bitrate on? (obs_setup enables it;
        # surfaced in the panel so nobody has to dig through OBS settings to check)
        dyn_raw = str(((results[2] or {}).get("parameterValue")) or "").lower()
        _stream_mon["dynamic_bitrate"] = (dyn_raw == "true")
        # Ladder support: SimpleOutput only (an unset Mode parameter means Simple)
        mode_raw = str(((results[3] or {}).get("parameterValue")) or "Simple")
        _stream_mon["simple_mode"] = (mode_raw == "Simple")
        s0 = results[0]
        live = bool(s0.get("outputActive"))
        _stream_mon["streaming"] = live
        if not live:
            _stream_mon["samples"] = []
            return None
        # Baseline: the configured bitrate the first time we see the stream live at step 0
        try:
            vbitrate = int(str((results[1] or {}).get("parameterValue", "") or 0))
        except ValueError:
            vbitrate = 0
        if _stream_mon["simple_mode"] and _stream_mon["step"] == 0 and vbitrate:
            _stream_mon["baseline_kbps"] = vbitrate
            _stream_mon["current_kbps"] = vbitrate
        _stream_mon["samples"].append({
            "t": now,
            "congestion": float(s0.get("outputCongestion") or 0.0),
            "dropped": int(s0.get("outputSkippedFrames") or 0),
            "total": int(s0.get("outputTotalFrames") or 0),
        })
        _stream_mon["samples"] = _stream_mon["samples"][-40:]
        action, reason = evaluate_stream_samples(
            _stream_mon["samples"], now, _stream_mon["last_shift_at"])
        _stream_mon["last_reason"] = reason
        step = _stream_mon["step"]
        simple = _stream_mon["simple_mode"]
        auto = bool(st.get("stream_auto_downshift"))
    if action == "downshift" and auto and simple and step < len(STREAM_LADDER) - 1:
        return ("down", step + 1, reason)
    return None


def _stream_monitor_loop():
    while True:
        try:
            decision = _stream_monitor_tick()
            if decision:
                _, new_step, reason = decision
                apply_stream_quality_step(new_step, reason, mode="auto")
        except Exception as e:
            print(f"  ✗  Stream monitor tick failed (will retry): {e}")
        time.sleep(STREAM_POLL_SEC)


def start_stream_monitor():
    with _stream_mon_lock:
        if _stream_mon["enabled_thread"]:
            return
        _stream_mon["enabled_thread"] = True
    threading.Thread(target=_stream_monitor_loop, daemon=True).start()


def stream_monitor_status():
    with _stream_mon_lock:
        recent = list(_stream_mon["samples"][-4:])
        return {
            "ok": True,
            "reachable": _stream_mon["reachable"],
            "streaming": _stream_mon["streaming"],
            "baseline_kbps": _stream_mon["baseline_kbps"],
            "current_kbps": _stream_mon["current_kbps"],
            "step": _stream_mon["step"],
            "ladder_pct": [int(f * 100) for f in STREAM_LADDER],
            "congestion": recent[-1]["congestion"] if recent else None,
            "verdict": _stream_mon["last_reason"],
            "shifts": list(_stream_mon["shifts"][-5:]),
            "auto": bool(load_state().get("stream_auto_downshift")),
            "dynamic_bitrate": _stream_mon["dynamic_bitrate"],
            "simple_mode": _stream_mon["simple_mode"],
        }


# ── Stream health check ────────────────────────────────────────
# Recommends OBS stream settings (bitrate/resolution/encoder) instead of expecting a
# non-technical volunteer to know what any of that means. Two independent measurements, not
# static specs — a GPU that *should* support hardware encoding doesn't always perform well in
# practice, so the encoder choice is decided by actually recording a short test clip with each
# candidate and comparing dropped-frame rates, not by assuming from hardware alone.

NETWORK_TEST_MAX_AGE_SEC = 7 * 24 * 3600   # re-test at most weekly unless asked to force —
                                            # this uses real upload data, worth respecting a
                                            # club's ground connection/mobile data allowance

def _measure_upload_mbps():
    """Uploads a small payload to Cloudflare's public speed-test endpoint and times it.
    Returns Mbps, or raises on any network failure (caller decides how to report that)."""
    payload = os.urandom(4 * 1024 * 1024)   # 4 MB — enough to smooth out a slow first packet
    req = urllib.request.Request(
        "https://speed.cloudflare.com/__up", data=payload, method="POST",
        # Cloudflare 403s Python's default urllib User-Agent — needs to look like a browser.
        headers={"Content-Type": "application/octet-stream",
                 "User-Agent": "Mozilla/5.0 (CricketStreamOverlay stream-health-check)"})
    start = time.time()
    with urllib.request.urlopen(req, timeout=20) as resp:
        resp.read()
    elapsed = max(time.time() - start, 0.001)
    return (len(payload) * 8 / elapsed) / 1_000_000

def get_upload_mbps(state, force=False):
    """Cached upload-speed test — only re-measures if forced or the cached result is stale."""
    cached_at = state.get("network_test_at", 0)
    if not force and cached_at and (time.time() - cached_at) < NETWORK_TEST_MAX_AGE_SEC:
        return state.get("network_test_mbps"), False
    mbps = _measure_upload_mbps()
    current = load_state()
    current["network_test_mbps"] = round(mbps, 2)
    current["network_test_at"]   = time.time()
    save_state(current)
    return mbps, True

def _recommend_bitrate_and_resolution(upload_mbps):
    """Standard streaming guidance: keep bitrate comfortably under measured upload speed (25%
    headroom) so real-world jitter doesn't cause buffering, then pick a resolution/fps tier
    that bitrate can actually support well."""
    safe_kbps = int(upload_mbps * 1000 * 0.75)
    if safe_kbps < 1500:
        return max(safe_kbps, 800), "720p", 30, "Upload speed is limited — 720p30 keeps quality watchable without buffering."
    if safe_kbps < 2800:
        return safe_kbps, "720p", 30, "Enough headroom for a clean 720p30 stream."
    if safe_kbps < 4500:
        return min(safe_kbps, 4000), "1080p", 30, "Good enough for 1080p30 — the standard for this project."
    return min(safe_kbps, 6000), "1080p", 30, "Plenty of headroom for a strong 1080p30 stream (60fps rarely helps for cricket — the action is slower-moving than most sports)."

def obs_stream_health_check(state, test_seconds=8):
    """Connects to OBS, refuses to touch anything if a real stream/recording is already live,
    then runs one or two short throwaway test recordings to measure actual encoder
    performance (dropped/skipped frames) rather than trusting hardware specs alone. Restores
    whatever encoder was configured before returning, always. Returns a result dict."""
    try:
        import websocket
    except ImportError:
        return {"ok": False, "error": "websocket-client not installed — run: pip install websocket-client"}

    host     = state.get("obs_host", "localhost")
    port     = state.get("obs_port", 4455)
    password = state.get("obs_password", "")
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
                if not raw: continue
                msg = json.loads(raw)
                if msg.get("op") == target:
                    return msg
            except Exception:
                break
        return None
    def send_request(ws, rt, rd=None, timeout=10):
        rid = nid()
        payload = {"requestType": rt, "requestId": rid}
        if rd is not None:
            payload["requestData"] = rd
        send_msg(ws, 6, payload)
        ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw: continue
                msg = json.loads(raw)
                if msg.get("op") == 7 and msg.get("d", {}).get("requestId") == rid:
                    return msg.get("d", {})
            except Exception:
                break
        return None

    def _run_recording_test(ws, label):
        """Starts a recording, waits test_seconds, stops it, and returns dropped-frame stats.
        Deletes the throwaway file it produces.

        GetRecordStatus does NOT carry frame-drop counters (verified against a real OBS
        32.1.2 instance — it only has outputActive/outputBytes/outputDuration/outputTimecode).
        The frame counters live in the general GetStats request instead, and — also verified
        empirically, since this isn't documented anywhere obvious — outputTotalFrames/
        outputSkippedFrames reset to ~0 the moment a new output (recording or stream) starts
        and count up cleanly from there for that session; they're not a lifetime-cumulative
        total. So a single read right before stopping is the right number, no before/after
        diff needed."""
        status = send_request(ws, "GetRecordStatus")
        if (status or {}).get("responseData", {}).get("outputActive"):
            return {"error": "a recording was already active — skipped"}
        start_resp = send_request(ws, "StartRecord")
        if not (start_resp or {}).get("requestStatus", {}).get("result"):
            comment = (start_resp or {}).get("requestStatus", {}).get("comment", "unknown error")
            return {"error": f"could not start test recording: {comment}"}
        time.sleep(test_seconds)
        stats = (send_request(ws, "GetStats") or {}).get("responseData", {})
        stop_resp = send_request(ws, "StopRecord")
        out_path = (stop_resp or {}).get("responseData", {}).get("outputPath")
        if out_path and os.path.exists(out_path):
            try:
                os.remove(out_path)
            except Exception:
                pass   # not worth failing the whole check over a leftover test clip
        total   = stats.get("outputTotalFrames", 0) or 0
        skipped = stats.get("outputSkippedFrames", 0) or 0
        skip_pct = round((skipped / total) * 100, 1) if total else None
        return {"label": label, "total_frames": total, "skipped_frames": skipped,
                "skip_pct": skip_pct}

    result = {"ok": False}
    try:
        ws = websocket.create_connection(ws_url, timeout=10)
        hello = wait_for_op(ws, 0, 5)
        if not hello:
            return {"ok": False, "error": "No response from OBS — is it running with the WebSocket server enabled?"}
        auth = hello["d"].get("authentication")
        identify = {"rpcVersion": 1}
        if auth and password:
            identify["authentication"] = _obs_auth_response(password, auth["salt"], auth["challenge"])
        send_msg(ws, 1, identify)
        if not wait_for_op(ws, 2, 5):
            ws.close()
            return {"ok": False, "error": "OBS authentication failed — check the WebSocket password"}

        # Safety: never touch encoder settings or start a test recording while the real
        # stream (or an unrelated recording) is actually running.
        stream_status = send_request(ws, "GetStreamStatus")
        if (stream_status or {}).get("responseData", {}).get("outputActive"):
            ws.close()
            return {"ok": False, "error": "OBS is currently live streaming — refusing to run "
                                          "the health check while the real stream is active."}

        # The frame-drop counters below are pipeline-wide, not scoped to the test recording —
        # if the Replay Buffer or Virtual Camera is already running (both normal during a real
        # match), their frames land in the same counters and make the test noisier. Doesn't
        # block the check (stopping either would itself be disruptive); just says so.
        caveats = []
        rb = send_request(ws, "GetReplayBufferStatus")
        if (rb or {}).get("responseData", {}).get("outputActive"):
            caveats.append("Replay Buffer is running — its frames count towards the test too, "
                          "so results are less precise than running this before match prep starts.")
        vc = send_request(ws, "GetVirtualCamStatus")
        if (vc or {}).get("responseData", {}).get("outputActive"):
            caveats.append("Virtual Camera is running — same caveat as the Replay Buffer above.")

        mode_resp = send_request(ws, "GetProfileParameter",
                                 {"parameterCategory": "Output", "parameterName": "Mode"})
        mode = (mode_resp or {}).get("responseData", {}).get("parameterValue", "Simple")
        if mode != "Simple":
            ws.close()
            return {"ok": False, "error": "OBS is set to Advanced output mode — the automatic "
                                          "encoder test only supports the default Simple mode "
                                          "for now. Bitrate/resolution recommendations below "
                                          "still apply; set the encoder manually in "
                                          "Settings → Output."}

        enc_resp = send_request(ws, "GetProfileParameter",
                                {"parameterCategory": "SimpleOutput", "parameterName": "StreamEncoder"})
        baseline_encoder = (enc_resp or {}).get("responseData", {}).get("parameterValue", "x264")

        tests = {"baseline": dict(_run_recording_test(ws, baseline_encoder), encoder=baseline_encoder)}

        is_hardware = baseline_encoder != "x264" and "x264" not in baseline_encoder
        if is_hardware:
            # Compare against software (x264) — the one encoder ID that's been stable across
            # OBS versions — since that's the exact comparison the operator needs: "is my
            # hardware encoder actually pulling its weight, or would plain CPU do better?"
            send_request(ws, "SetProfileParameter",
                         {"parameterCategory": "SimpleOutput", "parameterName": "StreamEncoder",
                          "parameterValue": "x264"})
            tests["alternate"] = dict(_run_recording_test(ws, "x264"), encoder="x264")
            # Always restore what was configured before, whether the test above succeeded or not.
            send_request(ws, "SetProfileParameter",
                        {"parameterCategory": "SimpleOutput", "parameterName": "StreamEncoder",
                         "parameterValue": baseline_encoder})

        ws.close()

        # Decide: keep the baseline unless the alternate clearly did better (a couple of
        # points of skipped-frame difference is noise; this needs to be a real gap).
        recommended_encoder = baseline_encoder
        notes = []
        base_pct = tests["baseline"].get("skip_pct")
        if "error" in tests["baseline"]:
            notes.append(f"Could not test the currently configured encoder: {tests['baseline']['error']}")
        elif base_pct and base_pct > 2:
            notes.append(f"Currently configured encoder ({baseline_encoder}) dropped {base_pct}% "
                        f"of frames in an {test_seconds}s test.")
        if "alternate" in tests:
            alt_pct = tests["alternate"].get("skip_pct")
            if "error" in tests["alternate"]:
                notes.append(f"Could not test x264 for comparison: {tests['alternate']['error']}")
            elif base_pct is not None and alt_pct is not None:
                if alt_pct + 2 < base_pct:
                    recommended_encoder = "x264"
                    notes.append(f"Software (x264) dropped fewer frames ({alt_pct}% vs {base_pct}%) — "
                                f"recommending it over {baseline_encoder} on this machine.")
                else:
                    notes.append(f"Hardware encoder ({baseline_encoder}) performed at least as well "
                                f"as software ({alt_pct}% vs {base_pct}% dropped) — keeping it.")

        result = {"ok": True, "tests": tests, "recommended_encoder": recommended_encoder,
                  "notes": notes, "caveats": caveats}
    except Exception as e:
        result = {"ok": False, "error": f"Could not reach OBS: {e}"}
    return result

# ── Control panel HTML ────────────────────────────────────────

# The panel lives in control.html — a PLAIN FILE, so normal JavaScript escaping applies
# (the old inline CONTROL_HTML Python string needed every backslash doubled, a silent-
# breakage class scripts/check_panel_js.py exists for). Read per request: panel edits show
# up on a browser refresh with no server restart. Kit-colour presets are injected at serve
# time in place of the placeholder token below.
CONTROL_HTML_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "control.html")

def control_html():
    with open(CONTROL_HTML_FILE, encoding="utf-8") as f:
        html_text = f.read()
    return html_text.replace("/*__KIT_PRESETS__*/[]", json.dumps(KIT_PRESETS), 1)


# ── HTTP handler ──────────────────────────────────────────────

# ── Post-match highlights compiler ───────────────────────────
# Last/current compile, for the panel: it kicks off in a background thread, so the POST
# response can't carry the outcome — the panel polls GET /highlights/status instead.
_highlights_status = {"running": False, "ok": None, "message": ""}


def _ff_filter_path(path):
    """A path made safe for use INSIDE an ffmpeg filter option value: forward slashes,
    and ':' escaped (the filter parser treats it as an option separator — bites every
    Windows drive-letter path)."""
    return path.replace("\\", "/").replace(":", "\\:")


def compile_highlights(folder, output_path, max_clips=100):
    """
    Stitch replay clips into a captioned highlights reel using FFmpeg.

    Clips run in chronological match order. Each tagged clip (tags are recorded the
    moment the replay fires — see log_replay_clip; untagged clips get a best-effort tag
    by correlating mtime against the ball-by-ball log) has its caption burned in as a
    lower-third; replay-test clips are excluded. A YouTube-ready description with
    chapter timestamps is written next to the reel.
    """
    import shutil
    import tempfile
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

    tags = clip_tags(clips)
    for name, caption in guess_clip_tags(
            [c for c in clips if os.path.basename(c) not in tags]).items():
        tags[name] = {"reason": "auto", "caption": caption}
    plan = plan_highlights(clips, tags)
    if not plan:
        return False, "Only replay-test clips found — nothing worth compiling"
    tagged_n = sum(1 for e in plan if e["caption"])
    print(f"  📎  Compiling {len(plan)} clips ({tagged_n} captioned) into highlights reel...")

    font = _font_path()
    tmpdir = tempfile.mkdtemp(prefix="highlights_")
    concat_file = os.path.join(tmpdir, "_concat.txt")
    try:
        entries = []
        with open(concat_file, "w", encoding="utf-8") as f:
            for i, e in enumerate(plan):
                src = e["file"]
                if e["caption"] and font:
                    # Caption via a textfile (no escaping minefield for the text itself)
                    txt_path = os.path.join(tmpdir, f"cap_{i:03d}.txt")
                    with open(txt_path, "w", encoding="utf-8") as tf:
                        tf.write(e["caption"])
                    vf = ("drawtext=textfile='" + _ff_filter_path(txt_path) + "'"
                          ":fontfile='" + _ff_filter_path(font) + "'"
                          ":fontcolor=white:fontsize=38:x=48:y=h-th-48"
                          ":box=1:boxcolor=black@0.55:boxborderw=16")
                    captioned = os.path.join(tmpdir, f"cap_{i:03d}.mp4")
                    r = subprocess.run(
                        ["ffmpeg", "-y", "-i", src, "-vf", vf,
                         "-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
                         "-c:a", "aac", "-b:a", "160k", captioned],
                        capture_output=True, text=True, timeout=180)
                    if r.returncode == 0:
                        src = captioned      # caption failed? fall back to the raw clip
                f.write(f"file '{src.replace(chr(92), '/')}'\n")
                entries.append({"caption": e["caption"], "duration": _clip_duration(src)})

        cmd = [
            "ffmpeg", "-y",
            "-f", "concat", "-safe", "0",
            "-i", concat_file,
            "-c:v", "libx264", "-crf", "23", "-preset", "fast",
            "-c:a", "aac", "-b:a", "128k",
            output_path
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
        if result.returncode != 0:
            return False, f"FFmpeg error: {result.stderr[-200:]}"

        # Chapters/description file — only when every clip's duration is known (ffprobe)
        desc_note = ""
        if entries and all(e["duration"] for e in entries):
            cfg = load_state()
            title = (f"{cfg.get('home_team','Home')} v {cfg.get('away_team','Opposition')}"
                     f" — highlights, {datetime.date.today().strftime('%d %b %Y')}")
            desc_path = os.path.splitext(output_path)[0] + "_description.txt"
            try:
                with open(desc_path, "w", encoding="utf-8") as df:
                    df.write(chapters_text(entries, title=title))
                desc_note = f" + chapter list ({os.path.basename(desc_path)})"
            except OSError:
                pass
        return True, (f"Highlights saved to {output_path} "
                      f"({len(plan)} clips, {tagged_n} captioned{desc_note})")
    except subprocess.TimeoutExpired:
        return False, "FFmpeg timed out — too many clips or slow machine"
    except Exception as e:
        return False, str(e)
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args): pass

    def _security_headers(self):
        """Baseline hardening headers (Phase 2.3) for every panel/API response."""
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")

    def _json(self, data, status=200):
        try:
            body = json.dumps(data).encode()
            self.send_response(status)
            self.send_header("Content-Type","application/json")
            self.send_header("Content-Length",str(len(body)))
            self._security_headers()
            # 2.2: mutating (POST) responses are same-origin-only — the control panel calls
            # them from its own page on this server, so no CORS header is needed — except
            # the one POST route the overlay itself calls (over-end AI commentary), which
            # keeps it for the same reason /live's GET does.
            if self.command != "POST" or urlparse(self.path).path == "/commentary/over/generate":
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
            self._security_headers()
            # The panel is one self-contained page (inline <style>/<script>, no external
            # resources — verified: its only https:// references are plain <a href> links,
            # never loaded), so 'unsafe-inline' is required but everything else is locked down.
            self.send_header("Content-Security-Policy",
                "default-src 'self'; script-src 'self' 'unsafe-inline'; "
                "style-src 'self' 'unsafe-inline'; img-src 'self' data:; "
                "connect-src 'self'; frame-ancestors 'none'")
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError): pass

    def _bytes(self, body, mime):
        try:
            self.send_response(200)
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
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

    def _client_ip(self):
        """Client IP, preferring X-Forwarded-For's first hop over the raw socket address —
        a tunnel (Tailscale funnel / Cloudflare quick tunnel) proxies the connection, so the
        socket address would otherwise always be the tunnel's own endpoint."""
        xff = self.headers.get("X-Forwarded-For", "")
        if xff:
            return xff.split(",")[0].strip()
        return self.address_string()

    def _is_trusted_loopback(self):
        """True only for a genuine same-machine connection with nothing proxying it — e.g.
        the overlay running in OBS on this box. cloudflared forwards tunnelled requests to
        127.0.0.1 too, so a bare address check would be fooled by it; the giveaway is that a
        proxied request carries X-Forwarded-For and a direct one never does."""
        if self.headers.get("X-Forwarded-For", ""):
            return False
        return self.client_address[0] in ("127.0.0.1", "::1")

    def _check_token(self):
        """Returns True if the request carries a valid session token (or auth is disabled).
        Bearer-only by design (see control.html's apiFetch): no cookie fallback, so there's
        no ambient credential a cross-site page could ride along on CSRF-style."""
        if not _CLUB_PASSWORD:
            return True
        auth = self.headers.get("Authorization", "")
        provided = auth[7:] if auth.startswith("Bearer ") else ""
        if provided and _verify_session_token(provided):
            return True
        ip = self._client_ip()
        print(f"  ✗  Auth: 401 {self.path} [{ip}]")
        _auth_log_add("401", ip)
        self._json({"ok": False, "error": "Unauthorized"}, status=401)
        return False

    def _origin_ok(self):
        """True unless Origin/Referer is present and points at a different host — defense in
        depth against CSRF-style cross-site requests. Absent headers pass (curl, some
        embedded-browser fetches don't send them); only a *mismatched* one is rejected."""
        host = self.headers.get("Host", "")
        for hdr in ("Origin", "Referer"):
            val = self.headers.get(hdr, "")
            if not val:
                continue
            try:
                netloc = urlparse(val).netloc
            except Exception:
                return False
            if netloc and netloc != host:
                return False
        return True

    def _handle_login(self, body):
        """POST /login — exchange club password for a signed session token."""
        if not _CLUB_PASSWORD:
            # Auth disabled — issue a token so the browser can proceed normally
            tok = _make_session_token() if _CONTROL_TOKEN else ""
            self._json({"ok": True, "session_token": tok})
            return

        ip  = self._client_ip()
        now = time.time()
        with _login_attempts_lock:
            rec         = _login_attempts.setdefault(ip, {"failures": [], "locked_until": 0})
            locked_until = rec["locked_until"]
        if locked_until > now:
            wait = int(locked_until - now) + 1
            print(f"  ✗  Login: {ip} is locked out ({wait}s remaining)")
            _auth_log_add("locked_out", ip)
            self._json({"ok": False, "error": f"Too many attempts — try again in {wait}s"},
                       status=429)
            return

        try:
            pw = json.loads(body).get("password", "")
        except Exception:
            pw = ""

        if pw and hmac.compare_digest(pw.encode(), _CLUB_PASSWORD.encode()):
            with _login_attempts_lock:
                _login_attempts.pop(ip, None)
            tok = _make_session_token()
            print(f"  ✓  Login: session issued [{ip}]")
            _auth_log_add("login_ok", ip)
            self._json({"ok": True, "session_token": tok})
        else:
            time.sleep(1)   # slow down brute-force attempts — never log the password itself
            with _login_attempts_lock:
                rec = _login_attempts.setdefault(ip, {"failures": [], "locked_until": 0})
                rec["failures"] = [t for t in rec["failures"] if now - t < LOGIN_WINDOW_SEC]
                rec["failures"].append(now)
                count = len(rec["failures"])
                if count >= LOGIN_MAX_FAILURES:
                    rec["locked_until"] = now + LOGIN_LOCKOUT_SEC
            print(f"  ✗  Login: bad password, {count} failure(s) in window [{ip}]")
            _auth_log_add("login_fail", ip)
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
            try:
                self._html(control_html())
            except OSError as e:
                self._json({"ok": False,
                            "error": f"control.html not found next to server.py ({e}) — "
                                     f"restore it from the repo"}, status=500)
        elif path == "/scoring":
            # Manual ball-by-ball scoring page — phones/tablets, no NV Play needed
            try:
                with open(os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                       "scoring.html"), encoding="utf-8") as f:
                    self._html(f.read())
            except OSError as e:
                self._json({"ok": False,
                            "error": f"scoring.html not found next to server.py ({e})"},
                           status=500)
        elif path == "/scoring/state":
            # Read-only view for the scoring page (no secrets — open, like /live/view)
            self._json(manual_ui_state())
        elif path == "/scoring/balls":
            # Recent balls with positions, for the edit-a-ball picker
            with _manual_lock:
                sess = _manual_session_locked()
                self._json({"ok": bool(sess),
                            "balls": sess.ball_list() if sess else []})
        elif path == "/scoring/scorecard":
            # Full plain-text scorecard for transcribing into Play-Cricket after the game
            with _manual_lock:
                sess = _manual_session_locked()
                text = sess.scorecard_text() if sess else ""
            if text:
                self._bytes(text.encode("utf-8"), "text/plain; charset=utf-8")
            else:
                self._json({"ok": False, "error": "no scoring session"}, status=404)
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
            if not self._check_token(): return
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
            if not self._check_token(): return
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
            if not self._check_token(): return
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

        elif path.startswith("/sponsor/"):
            # Serve the weekend sponsor image from the sponsors/ folder, named by ID
            # (e.g. sponsor_id "3" -> sponsors/3.png) so the same set of images can be
            # reused week to week just by changing which ID is set in the control panel.
            raw_name = path[9:].split("?")[0].strip("/")
            name     = os.path.basename(raw_name.replace("..", "").replace("/", "")
                                        .replace("\\", "").replace(":", ""))
            sponsor_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "sponsors")
            mimes    = {"png":"image/png","jpg":"image/jpeg","jpeg":"image/jpeg",
                        "svg":"image/svg+xml","webp":"image/webp","gif":"image/gif"}
            found = False
            for ext, mime in mimes.items():
                sponsor_path = os.path.join(sponsor_dir, f"{name}.{ext}")
                if os.path.exists(sponsor_path):
                    self._file(sponsor_path, mime)
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

        # (a second, unreachable /pcs/debug handler used to live here — the real one is above)

        elif path == "/highlights/status":
            # Outcome of the last/current background compile (see POST /highlights)
            self._json(dict(_highlights_status))

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
            # Tagged = clips with a real reason on record (replay-test clips excluded) —
            # surfaced in the panel so nobody needs a terminal to confirm tagging works
            tags = clip_tags(clips)
            tagged = sum(1 for t in tags.values()
                         if (t.get("reason") or "").lower() != "test")
            self._json({
                "uptime":     uptime_str,
                "uptime_sec": uptime_secs,
                "clip_count": len(clips),
                "tagged":     tagged,
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
                "watchdog": {
                    "last_run_sec_ago": (int(now - _watchdog_status["last_run"])
                                         if _watchdog_status["last_run"] else None),
                    "runs":          _watchdog_status["runs"],
                    "fixes_applied": _watchdog_status["fixes_applied"][-5:],
                },
                "server": _self_metrics(),
                "errors": _recent_server_errors(),
            })

        elif path == "/auth/log":
            # Auth-gated (unlike /health) — this reveals client IPs, so it shouldn't be
            # readable by anonymous callers even though the rest of /health is deliberately
            # open. Lets an operator see if something's been probing the panel.
            if not self._check_token(): return
            with _auth_log_lock:
                entries = list(_auth_log[-50:])
            entries.reverse()   # most recent first — the panel just lists them top-down
            self._json({"ok": True, "entries": entries})

        elif path == "/obs/stream_check":
            if not self._check_token(): return
            if not self._check_rate_limit(path): return
            from urllib.parse import parse_qs
            q     = parse_qs(urlparse(self.path).query)
            force = q.get("force", ["0"])[0].lower() in ("1", "true", "yes")
            s     = load_state()
            out   = {"ok": True}
            try:
                mbps, fresh = get_upload_mbps(s, force=force)
                bitrate, res, fps, bitrate_note = _recommend_bitrate_and_resolution(mbps)
                out["network"] = {"upload_mbps": round(mbps, 2), "freshly_tested": fresh,
                                  "recommended_kbps": bitrate, "recommended_resolution": res,
                                  "recommended_fps": fps, "note": bitrate_note}
            except Exception as e:
                out["network"] = {"error": f"Could not test upload speed: {e}"}
            out["encoder"] = obs_stream_health_check(s)
            self._json(out)

        elif path == "/stream/monitor":
            # Live congestion/quality picture for the panel (no secrets — open)
            self._json(stream_monitor_status())

        elif path == "/remote/info":
            # Not auth-gated, same reasoning as /health: no secrets beyond a URL, and that
            # URL is only reachable by a device already on the right network/tunnel anyway.
            targets = _remote_targets()
            self._json({"ok": bool(targets), "targets": targets})

        elif path == "/remote/qr.png":
            from urllib.parse import parse_qs
            q      = parse_qs(urlparse(self.path).query)
            via    = q.get("via", [""])[0]
            targets = _remote_targets()
            target  = next((t for t in targets if t["via"] == via), targets[0] if targets else None)
            png = _qr_png_bytes(target["url"]) if target else None
            if not png:
                self.send_response(404); self.end_headers()
                return
            self._bytes(png, "image/png")

        elif path in ("/live", "/live/view"):
            global _rv_cache
            # /live is the OVERLAY's poll and drives the match pipeline: event buffering,
            # ball logging, commentary triggers, and it consumes the event buffer.
            # /live/view is the same picture for any OTHER consumer (the control panel's
            # monitor/status/checklist pollers) with none of the side effects — before the
            # split, whichever panel poll landed first would eat the overlay's wicket
            # events and run the ball logger on its own cadence too.
            mutate = (path == "/live")
            s    = load_state()
            home = s.get("home_team","Home CC")
            away = s.get("away_team","Opposition CC")
            # home_club_id isn't guaranteed numeric: the panel's manual badge picker sets it
            # to a logo FILENAME stem (that picker exists precisely for clubs with no
            # PlayCricket ID). A bare int() here crashed every /live poll — and with it the
            # whole overlay — the moment someone picked a file like "opposition.png".
            _club_raw = str(s.get("test_club_id") or s.get("home_club_id") or "").strip()
            club_id   = int(_club_raw) if _club_raw.isdigit() else 0

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
                # Manual scoring session (if one is live) outranks the PCS file; the PCS
                # file outranks the widget. Manual state is rendered through the same
                # parser, so downstream nothing can tell the difference — both report
                # source "pcs" (the overlay's fast-poll signal), distinguished by "feed".
                pcs_folder   = s.get("pcs_output_folder","").strip()
                manual_state = manual_live_state()
                pcs_state    = manual_state or (read_pcs_file(pcs_folder) if pcs_folder else None)
                if pcs_state:
                    # Inject abbreviations so overlay can use them
                    pcs_state["home_abbrev"] = s.get("home_abbrev","").strip().upper()
                    pcs_state["away_abbrev"] = s.get("away_abbrev","").strip().upper()
                    events = []
                    if mutate:
                        # Commentary trigger MUST run before buffer_pcs_events: both diff
                        # the state against _prev_state, but buffer_pcs_events advances
                        # _prev_state when it's done. Running the trigger after it meant
                        # every delta it saw was zero — commentary could never fire.
                        if s.get("graphics_commentary", True):
                            check_commentary_trigger(pcs_state)
                        # Buffer boundary/wicket events (this advances _prev_state)
                        buffer_pcs_events(pcs_state)
                        match_log_snapshot(pcs_state)
                        log_ball_data(pcs_state)   # append to our own ball-by-ball database
                        # Hand buffered events to the overlay and clear — under the lock,
                        # so an event landing mid-pop can't be silently dropped
                        with _event_buffer_lock:
                            events = list(_event_buffer)
                            _event_buffer.clear()
                    self._json({"source":"pcs","state":pcs_state,"club_id":club_id,
                                "events":events,
                                "feed": "manual" if manual_state else "file"})
                elif pcs_folder:
                    # PCS Pro is configured but hasn't written a match yet (pre-match, or
                    # between innings) -- still report "pcs" so the overlay keeps fast-polling
                    # instead of falling back to slow widget polling and a mislabeled source.
                    self._json({"source":"pcs","state":None,"club_id":club_id,"events":[]})
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

        # NOTE: /commentary/over/generate and /commentary/test are POST-only (see do_POST).
        # GET copies of both used to sit here — dead code, and the first referenced do_POST's
        # `body` variable, which doesn't exist in do_GET.

        else:
            self.send_response(404); self.end_headers()

    def do_POST(self):
        path   = urlparse(self.path).path
        length = int(self.headers.get("Content-Length",0) or 0)
        if length > MAX_BODY_BYTES:
            self.send_response(413)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        body   = self.rfile.read(length)

        if not self._origin_ok():
            self._json({"ok": False, "error": "Cross-origin request rejected"}, status=403)
            return

        if path == "/login":
            self._handle_login(body)
            return

        # These are fired automatically by the overlay itself — a loopback OBS browser
        # source with no login flow of its own. /commentary/over/generate additionally
        # spends Anthropic credit, so all of them trust loopback callers with no forwarding
        # proxy in front of them (see _is_trusted_loopback), or a valid session either way,
        # rather than requiring a session the overlay can never obtain.
        OVERLAY_ENDPOINTS = ("/commentary/over/generate", "/replay", "/weather/show", "/weather/hide")
        if path in OVERLAY_ENDPOINTS:
            if not (self._is_trusted_loopback() or self._check_token()):
                return
        elif not self._check_token():
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
                t = threading.Thread(target=obs_trigger_replay, args=(state, reason), daemon=True)
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
            # If the field still shows the redacted sentinel (the operator loaded the panel
            # without retyping the URL), fall back to the real stored value — otherwise this
            # would try to add "••••••••" itself as the camera source.
            posted_url = (d.get("url") or "").strip()
            url = (cfg.get("camera_rtsp_url") or "").strip() if posted_url == SECRET_SENTINEL \
                  else (posted_url or cfg.get("camera_rtsp_url") or "").strip()
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
                data  = json.loads(body or "{}")
                s     = load_state()
                home  = s.get("home_team", "Home CC")
                away  = s.get("away_team", "Opposition")
                def _fill(t):
                    return (t or "").replace("{home}", home).replace("{away}", away)
                tmpl  = data.get("title") or s.get("youtube_title_template", "LIVE: {home} vs {away}")
                title = _fill(tmpl)
                desc  = _fill(data.get("description")
                              if data.get("description") is not None
                              else s.get("youtube_description", ""))
                privacy  = data.get("privacy")  or s.get("youtube_privacy", "unlisted")
                category = data.get("category") or s.get("youtube_category", "17")
                mfk      = (data.get("made_for_kids") if "made_for_kids" in data
                            else bool(s.get("youtube_made_for_kids", False)))
                ok, msg = update_youtube_broadcast(
                    title=title, description=desc, privacy=privacy,
                    made_for_kids=bool(mfk), category_id=category,
                    # First-run browser auth only for a real same-machine operator — a
                    # remote (tunnelled) request must not pop a browser on the host.
                    allow_interactive=self._is_trusted_loopback())
                print(f"  {'✓' if ok else '✗'}  YouTube: {msg}")
                self._json({"ok": ok, "title": title,
                            "message": msg if ok else "", "error": msg if not ok else ""})
            except Exception as e:
                self._json({"ok": False, "error": str(e)}, status=500)

        elif path == "/highlights":
            try:
                s      = json.loads(body) if body else {}
                st     = load_state()
                folder = st.get("replay_folder","") or _default_replay_folder()
                output = s.get("output", os.path.join(folder, "highlights.mp4"))
                max_c  = int(st.get("max_clips", 100))
                _highlights_status.update({"running": True, "ok": None, "message": ""})
                def run_compile():
                    try:
                        ok, msg = compile_highlights(folder, output, max_c)
                    except Exception as exc:      # belt and braces — status must resolve
                        ok, msg = False, str(exc)
                    _highlights_status.update({"running": False, "ok": ok, "message": msg})
                    print(f"  {'✓' if ok else '✗'}  Highlights: {msg}")
                threading.Thread(target=run_compile, daemon=True).start()
                # Count current clips and how many carry a tag (excluding replay tests)
                patterns = ["*.mkv","*.mp4","*.flv","*.mov"]
                clips = [c for p in patterns for c in glob.glob(os.path.join(folder,p))
                         if "highlights" not in os.path.basename(c).lower()]
                tags  = clip_tags(clips)
                tagged = sum(1 for t in tags.values()
                             if (t.get("reason") or "").lower() != "test")
                self._json({"ok": True, "output": output,
                            "clip_count": len(clips), "tagged": tagged})
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

        elif path == "/auth/logout_all":
            # Rotates the signing epoch mixed into every session's HMAC — every session
            # issued before this instant (including the caller's own) stops verifying,
            # without needing to restart the stream. Already behind the blanket
            # _check_token() gate above, same as every other POST route.
            global _SESSION_EPOCH
            with _session_epoch_lock:
                _SESSION_EPOCH = secrets.token_hex(8)
            ip = self._client_ip()
            print(f"  \u26a0  All sessions revoked by [{ip}]")
            _auth_log_add("logout_all", ip)
            self._json({"ok": True})

        elif path == "/stream/dynamic":
            # One-click enable of OBS's Dynamic Bitrate from the panel — same WebSocket
            # write obs_setup does. OBS reads the setting at stream START, so if you're
            # already live it applies from the next stream.
            st = load_state()
            results = _obs_call(st, [
                ("SetProfileParameter", {"parameterCategory": "Output",
                                         "parameterName": "DynamicBitrate",
                                         "parameterValue": "true"}),
                ("GetStreamStatus", None)])
            if results is None or results[0] is None:
                self._json({"ok": False, "error": "Could not reach OBS — is it running "
                                                  "with the WebSocket server enabled?"})
                return
            live = bool((results[1] or {}).get("outputActive"))
            with _stream_mon_lock:
                _stream_mon["dynamic_bitrate"] = True
            self._json({"ok": True,
                        "message": "Dynamic bitrate enabled"
                                   + (" — takes effect when the stream next starts"
                                      if live else "")})

        elif path == "/stream/quality":
            # Manual quality control from the panel: step down, step back up, or restore.
            # Involves a stop→reconfigure→start (~5-10s ingest gap, broadcast survives).
            try:
                d = json.loads(body or "{}")
            except json.JSONDecodeError:
                d = {}
            act = d.get("action", "")
            with _stream_mon_lock:
                step = _stream_mon["step"]
            if act == "down":
                target_step = min(step + 1, len(STREAM_LADDER) - 1)
            elif act == "up":
                target_step = max(step - 1, 0)
            elif act == "restore":
                target_step = 0
            else:
                self._json({"ok": False, "error": "action must be down/up/restore"}, status=400)
                return
            if target_step == step:
                self._json({"ok": False, "error": "already at that quality step"})
                return
            ok, msg = apply_stream_quality_step(target_step, "operator request", mode="manual")
            self._json({"ok": ok, "message" if ok else "error": msg,
                        "state": stream_monitor_status()})

        elif path == "/scoring/setup":
            # Create (or replace) the manual scoring session. Exact match BEFORE the
            # /scoring/ prefix handler below \u2014 see the route-ordering gotcha.
            try:
                d = json.loads(body or "{}")
                if not isinstance(d, dict):
                    raise ValueError("setup must be a JSON object")
                def _lines(v):
                    return v if isinstance(v, list) else str(v or "").splitlines()
                with _manual_lock:
                    sess = _manual_session_locked()
                    if sess and sess.events and not sess.match_over and not d.get("force"):
                        raise ValueError("a scoring session is already in progress \u2014 "
                                         "reset it (or pass force) first")
                    _manual["session"] = ManualScoringSession({
                        "home": d.get("home"), "away": d.get("away"),
                        "home_xi": _lines(d.get("home_xi")),
                        "away_xi": _lines(d.get("away_xi")),
                        "max_overs": d.get("max_overs"),
                        "batting_first": d.get("batting_first"),
                    })
                    _manual["session"].save()
                    cfg_teams = _manual["session"].config
                # Manual scoring implies: no demo data, no widget fallback, and the
                # overlay's team names/colour mapping should match what's being scored.
                st = load_state()
                st.update({"demo_mode": False, "use_widget": False,
                           "home_team": cfg_teams["home"], "away_team": cfg_teams["away"],
                           "max_overs": cfg_teams["max_overs"]})
                save_state(st)
                print(f"  \u2713  Manual scoring: {cfg_teams['home']} v {cfg_teams['away']}, "
                      f"{cfg_teams['max_overs']} overs")
                self._json({"ok": True, "state": manual_ui_state()})
            except (ValueError, json.JSONDecodeError) as e:
                self._json({"ok": False, "error": str(e)}, status=400)

        elif path.startswith("/scoring/"):
            action = path[len("/scoring/"):]
            try:
                try:
                    d = json.loads(body) if body else {}
                except json.JSONDecodeError:
                    d = {}
                if not isinstance(d, dict):
                    d = {}
                with _manual_lock:
                    sess = _manual_session_locked()
                    if action == "reset":
                        _manual["session"] = None
                        _manual["load_attempted"] = True   # don't resurrect from the file
                        try:
                            os.remove(MANUAL_SCORING_FILE)
                        except OSError:
                            pass
                    elif not sess:
                        raise ValueError("no scoring session \u2014 set the match up first")
                    elif action == "ball":
                        sess.apply({"event": "ball", "kind": d.get("kind", ""),
                                    "runs": d.get("runs", 0),
                                    "wicket_type": d.get("wicket_type", "bowled"),
                                    "fielder": d.get("fielder", ""),
                                    "out_non_striker": bool(d.get("out_non_striker"))})
                    elif action == "undo":
                        sess.undo()
                    elif action == "edit":
                        sess.edit_ball(int(d.get("index", -1)),
                                       {"event": "ball", "kind": d.get("kind", ""),
                                        "runs": d.get("runs", 0),
                                        "wicket_type": d.get("wicket_type", "bowled"),
                                        "fielder": d.get("fielder", ""),
                                        "out_non_striker": bool(d.get("out_non_striker"))})
                    elif action == "bowler":
                        sess.apply({"event": "bowler", "name": d.get("name", "")})
                    elif action == "batter":
                        sess.apply({"event": "batter", "name": d.get("name", "")})
                    elif action == "strike":
                        sess.apply({"event": "swap_strike"})
                    elif action == "innings":
                        sess.apply({"event": "start_innings"
                                    if d.get("action") == "start_next" else "end_innings"})
                    else:
                        self.send_response(404); self.end_headers()
                        return
                self._json({"ok": True, "state": manual_ui_state()})
            except ValueError as e:
                self._json({"ok": False, "error": str(e)}, status=400)

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
    _ensure_control_token()
    start_watchdog()
    start_stream_monitor()

    if _CLOUDFLARE_TUNNEL:
        if not _CLUB_PASSWORD:
            print("  ✗  Cloudflare Tunnel is enabled in config.ini but club_password is blank.")
            print("     Refusing to start it — that would expose the control panel with no")
            print("     login. Set [Auth] club_password first, then restart.")
        else:
            _start_cloudflare_tunnel()

    _LABELS = {"tailscale": "Tailscale remote", "cloudflare": "Cloudflare remote (public)",
               "lan": "Same-network remote"}
    targets = _remote_targets()
    remote_info = ""
    if targets:
        remote_info = "".join(f"\n  {_LABELS[t['via']]} → {t['url']}" for t in targets)
    elif _BIND_HOST != "127.0.0.1":
        remote_info = f"\n  Listening on {_BIND_HOST}:{PORT} — use your device IP to connect remotely"
    if _CLOUDFLARE_TUNNEL and _CLUB_PASSWORD and not any(t["via"] == "cloudflare" for t in targets):
        remote_info += "\n  Cloudflare Tunnel starting up — its URL will appear in the control panel shortly"

    if targets:
        try:
            import qrcode
            top = targets[0]
            qr = qrcode.QRCode(border=1)
            qr.add_data(top["url"])
            qr.make(fit=True)
            print(f"\n  Scan to open the control panel on another device ({top['via']}):\n")
            qr.print_ascii(invert=True)
        except ImportError:
            print("\n  Tip: pip install qrcode to get a scannable QR code here and in the panel.")

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

    class _Server(ThreadingHTTPServer):
        daemon_threads = True

        def handle_error(self, request, client_address):
            # Record handler-thread exceptions in the /health flight recorder as well
            # as the console — mid-match, nobody is reading the terminal.
            import sys as _sys
            log_server_error(f"request from {client_address[0]}",
                             _sys.exc_info()[1] or "unknown")
            super().handle_error(request, client_address)

    httpd = _Server((_BIND_HOST, PORT), Handler)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n  Server stopped.")
    finally:
        _stop_cloudflare_tunnel()
