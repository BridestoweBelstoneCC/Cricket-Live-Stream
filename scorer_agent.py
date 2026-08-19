#!/usr/bin/env python3
"""
scorer_agent.py — run this on the SCORING laptop (the one with PCS Pro / NV Play open),
when that machine is NOT the same one running server.py.

Two-laptop mode: the person running the stream gets their own machine, so they can restart
the server, re-scene OBS and fix graphics mid-match without reaching across to the scorer.
See TWO_LAPTOP_SETUP.md.

Unlike nvplay_bridge.py (a manual URL + token, recommended over Tailscale for reaching a
scorer's machine off the club network entirely), this script is built for two laptops that
ARE already on the same club wifi: it answers a UDP "who's out there?" broadcast so the
streaming laptop's control panel can find it with a button press instead of someone typing
an IP address. There is no token — anyone on the same wifi segment can read the live score
(never any secrets) from this machine while it runs. Use nvplay_bridge.py instead if the two
machines are NOT on the same local network, or if that open-LAN-read tradeoff is unwanted.

Read-only: it never touches PCS Pro or the scoreboard file, only serves a copy of it.
Stdlib only — nothing to pip install on the scorer's machine.

Usage:
    python3 scorer_agent.py
    python3 scorer_agent.py "C:/Users/Scorer/Documents/Cricket Matches/_Scoreboards/Output"
    python3 scorer_agent.py --port 8788 --discovery-port 8787

Stop it with Ctrl-C, or just close the window.
"""

import argparse
import glob
import json
import os
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs

AGENT_VERSION = "1.0"
SERVICE_NAME  = "cricketstream-scorer-agent"

DEFAULT_HTTP_PORT      = 8788
DEFAULT_DISCOVERY_PORT = 8787

DISCOVERY_MAGIC = b"CRICKETSTREAM-DISCOVER"

HERE        = os.path.dirname(os.path.abspath(__file__))
CONFIG_FILE = os.path.join(HERE, "scorer_agent.json")

# Same list, same priority order, as server.py's PCS_OUTPUT_FILENAMES. Duplicated rather
# than imported — this script has to run standalone on its own machine, dependency-free,
# so it can't rely on server.py (or its third-party imports) being present there.
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

# Anything bigger than this is not a scoreboard file — refuse to serve it.
MAX_FILE_BYTES = 2 * 1024 * 1024

# Folders PCS Pro / NV Play commonly write to, relative to the user's home.
COMMON_FOLDER_PATTERNS = [
    "Documents/Cricket Matches/_Scoreboards/Output",
    "Documents/Cricket Matches/*/Output",
    "Documents/Cricket Matches/_Scoreboards/*",
    "Documents/NV Play/Scoreboards/Output",
    "Documents/NV Play/*/Output",
    "Documents/PCS Pro/Output",
    "Documents/PCS Pro/Scoreboard*",
    "Documents/Scoreboards/Output",
    "Documents/Scoreboard*",
    "Desktop/Scoreboard*",
    "Desktop/Output",
]


# ── Finding the scoreboard folder ────────────────────────────────────────────

def find_output_file(folder, max_age=600):
    """Return the scoreboard output file path in `folder`, or None.

    Known filenames win. Otherwise fall back to the most recently modified .json/.xml
    in the folder, provided it was touched within `max_age` seconds (max_age=None accepts
    any age — used when hunting for the folder itself, before a match has started).
    """
    if not folder or not os.path.isdir(folder):
        return None

    for fname in PCS_OUTPUT_FILENAMES:
        path = os.path.join(folder, fname)
        if os.path.exists(path):
            return path

    try:
        candidates = (glob.glob(os.path.join(folder, "*.json")) +
                      glob.glob(os.path.join(folder, "*.xml")))
        candidates = [c for c in candidates if os.path.isfile(c)]
    except OSError:
        return None
    if not candidates:
        return None

    newest = max(candidates, key=os.path.getmtime)
    if max_age is None or (time.time() - os.path.getmtime(newest)) < max_age:
        return newest
    return None


def folder_has_scoreboard(folder, max_age=None):
    return find_output_file(folder, max_age=max_age) is not None


def autodetect_folder():
    """Hunt the usual places for the PCS Pro / NV Play output folder.

    Prefers a folder whose file was written recently (a live match), then falls back to
    any folder that has ever held a scoreboard file.
    """
    home = os.path.expanduser("~")
    seen = []
    for pattern in COMMON_FOLDER_PATTERNS:
        try:
            for path in sorted(glob.glob(os.path.join(home, pattern))):
                if os.path.isdir(path) and path not in seen:
                    seen.append(path)
        except OSError:
            continue

    for path in seen:
        if folder_has_scoreboard(path, max_age=600):
            return path, "live"
    for path in seen:
        if folder_has_scoreboard(path, max_age=None):
            return path, "stale"
    return None, None


def load_saved_folder():
    try:
        with open(CONFIG_FILE, encoding="utf-8") as f:
            return (json.load(f).get("folder") or "").strip() or None
    except Exception:
        return None


def save_folder(folder):
    try:
        with open(CONFIG_FILE, "w", encoding="utf-8") as f:
            json.dump({"folder": folder}, f, indent=2)
    except Exception:
        pass  # Read-only location, roaming profile, etc. Not worth failing over.


# ── Shared state between the HTTP server, the discovery responder and the console ──

class AgentState:
    def __init__(self, folder, http_port):
        self.folder = folder
        self.http_port = http_port
        self.lock = threading.Lock()
        self.started = time.time()
        self.requests = 0
        self.last_request = 0.0
        self.last_client = ""

    def note_request(self, client):
        with self.lock:
            self.requests += 1
            self.last_request = time.time()
            self.last_client = client

    def snapshot(self):
        with self.lock:
            return {"requests": self.requests, "last_request": self.last_request,
                    "last_client": self.last_client}


STATE = None  # set in main()


def local_ip():
    """Best guess at this machine's LAN address (no traffic is actually sent)."""
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("8.8.8.8", 80))
        return s.getsockname()[0]
    except Exception:
        try:
            return socket.gethostbyname(socket.gethostname())
        except Exception:
            return "127.0.0.1"
    finally:
        s.close()


def describe_self():
    """The payload sent in discovery replies and returned by /ping."""
    path = find_output_file(STATE.folder)
    return {
        "service":    SERVICE_NAME,
        "version":    AGENT_VERSION,
        "hostname":   socket.gethostname(),
        "port":       STATE.http_port,
        "folder":     STATE.folder,
        "file":       os.path.basename(path) if path else None,
        "file_mtime": os.path.getmtime(path) if path else None,
        "file_age":   (time.time() - os.path.getmtime(path)) if path else None,
        "uptime":     time.time() - STATE.started,
    }


# ── HTTP: serve the scoreboard file ──────────────────────────────────────────

class AgentHandler(BaseHTTPRequestHandler):
    server_version = f"CricketStreamScorerAgent/{AGENT_VERSION}"

    def log_message(self, *args):
        pass  # We print our own, much quieter, status line.

    def _json(self, obj, code=200):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        # The streaming laptop is a different origin; let it read us.
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass  # Stream laptop went away mid-response. Harmless.

    def do_GET(self):
        client = self.client_address[0]
        path = self.path.split("?")[0]
        query = {}
        if "?" in self.path:
            query = {k: v[0] for k, v in parse_qs(self.path.split("?", 1)[1]).items()}

        if path in ("/ping", "/"):
            STATE.note_request(client)
            self._json(describe_self())
            return

        if path == "/pcs":
            STATE.note_request(client)
            self._serve_scoreboard(query)
            return

        self._json({"error": "not found", "paths": ["/ping", "/pcs"]}, code=404)

    def _serve_scoreboard(self, query):
        file_path = find_output_file(STATE.folder)
        if not file_path:
            self._json({"ok": False, "error": "no_file",
                        "detail": f"No scoreboard file found in {STATE.folder}",
                        "folder": STATE.folder})
            return

        try:
            mtime = os.path.getmtime(file_path)
            size = os.path.getsize(file_path)
        except OSError as e:
            self._json({"ok": False, "error": "stat_failed", "detail": str(e)})
            return

        if size > MAX_FILE_BYTES:
            self._json({"ok": False, "error": "too_large",
                        "detail": f"{os.path.basename(file_path)} is {size} bytes"})
            return

        # The streaming laptop tells us the mtime it already has, so an unchanged file
        # costs a few bytes instead of the whole scoreboard.
        try:
            since = float(query.get("since", "0") or 0)
        except ValueError:
            since = 0.0
        if since and abs(since - mtime) < 0.001:
            self._json({"ok": True, "unchanged": True, "mtime": mtime})
            return

        # PCS Pro rewrites this file on every ball. If we catch it mid-write we get a
        # truncated read, so retry briefly rather than serving a stub.
        raw = None
        for attempt in range(3):
            try:
                with open(file_path, encoding="utf-8", errors="replace") as f:
                    raw = f.read()
            except OSError as e:
                if attempt == 2:
                    self._json({"ok": False, "error": "read_failed", "detail": str(e)})
                    return
                time.sleep(0.05)
                continue
            if raw.strip():
                break
            time.sleep(0.05)

        if not raw or not raw.strip():
            self._json({"ok": False, "error": "empty", "detail": "File is empty"})
            return

        self._json({"ok": True, "unchanged": False, "name": os.path.basename(file_path),
                    "mtime": mtime, "age": time.time() - mtime, "content": raw})


# ── UDP: answer "where are you?" broadcasts from the streaming laptop ───────

def discovery_responder(discovery_port):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
    except OSError:
        pass

    try:
        sock.bind(("", discovery_port))
    except OSError as e:
        print(f"  !  Could not listen for discovery on UDP {discovery_port}: {e}")
        print(f"     The stream laptop can still connect by typing this machine's "
              f"address: {local_ip()}:{STATE.http_port}")
        return

    while True:
        try:
            data, addr = sock.recvfrom(2048)
        except OSError:
            time.sleep(0.5)
            continue
        if not data.startswith(DISCOVERY_MAGIC):
            continue
        try:
            sock.sendto(json.dumps(describe_self()).encode("utf-8"), addr)
        except OSError:
            pass


# ── Console ───────────────────────────────────────────────────────────────────

def status_line():
    path = find_output_file(STATE.folder)
    if not path:
        return "waiting for the scoreboard file — is PCS Pro scoreboard output switched on?"

    age = time.time() - os.path.getmtime(path)
    if age < 30:
        health = "live"
    elif age < 300:
        health = f"last updated {int(age)}s ago"
    else:
        health = f"last updated {int(age // 60)} min ago"

    snap = STATE.snapshot()
    if snap["requests"]:
        since = time.time() - snap["last_request"]
        served = f"stream laptop {snap['last_client']} collected it {int(since)}s ago"
    else:
        served = "stream laptop has not connected yet"

    return f"{os.path.basename(path)} - {health} - {served}"


def console_loop():
    last = ""
    while True:
        try:
            line = status_line()
            if line != last:
                print(f"  [{time.strftime('%H:%M:%S')}] {line}")
                last = line
        except Exception:
            pass
        time.sleep(5)


def banner(folder, how, http_port, discovery_port):
    ip = local_ip()
    print()
    print("  CricketStream -- Scorer Agent")
    print("  ------------------------------")
    print()
    print(f"  Watching folder : {folder}")
    if how == "auto":
        print("                    (found automatically)")
    elif how == "auto-stale":
        print("                    (found automatically -- no recent file, which is")
        print("                     normal before play starts)")
    elif how == "saved":
        print("                    (remembered from last time)")
    print(f"  This machine    : {ip}")
    print(f"  Serving on      : http://{ip}:{http_port}/pcs")
    print(f"  Discovery       : UDP {discovery_port}")
    print()
    print("  On the streaming laptop, open the control panel, set the score source to")
    print("  'Scorer laptop (network)' and press Find scorer laptop.")
    print("  It should appear on its own. If it doesn't, type the address above.")
    print()
    print("  Leave this window open for the whole match. Ctrl-C to stop.")
    print("  ------------------------------------------------------------")
    print()


def resolve_folder(arg_folder):
    """Work out which folder to watch, and how we arrived at it."""
    if arg_folder:
        folder = os.path.abspath(os.path.expanduser(arg_folder))
        if not os.path.isdir(folder):
            print(f"\n  !  That folder does not exist:\n     {folder}\n")
            sys.exit(1)
        save_folder(folder)
        return folder, "argument"

    saved = load_saved_folder()
    if saved and os.path.isdir(os.path.expanduser(saved)):
        return os.path.expanduser(saved), "saved"

    folder, freshness = autodetect_folder()
    if folder:
        save_folder(folder)
        return folder, ("auto" if freshness == "live" else "auto-stale")

    print()
    print("  Could not find the PCS Pro / NV Play scoreboard output folder.")
    print()
    print("  In PCS Pro it's under Tools -> Configuration -> Scoreboard, in the")
    print("  'Output Folder' box. Copy that path and start this again like so:")
    print()
    print('      python3 scorer_agent.py "PASTE THE FOLDER PATH HERE"')
    print()
    print("  You only need to do that once -- it remembers.")
    print()
    sys.exit(1)


def main():
    global STATE

    ap = argparse.ArgumentParser(
        description="Serve the PCS Pro scoreboard file to the streaming laptop.")
    ap.add_argument("folder", nargs="?", default=None,
                    help="PCS Pro / NV Play scoreboard output folder "
                         "(auto-detected if omitted)")
    ap.add_argument("--port", type=int, default=DEFAULT_HTTP_PORT,
                    help=f"HTTP port to serve on (default {DEFAULT_HTTP_PORT})")
    ap.add_argument("--discovery-port", type=int, default=DEFAULT_DISCOVERY_PORT,
                    help=f"UDP port to answer discovery on (default {DEFAULT_DISCOVERY_PORT})")
    args = ap.parse_args()

    folder, how = resolve_folder(args.folder)
    STATE = AgentState(folder, args.port)

    try:
        httpd = ThreadingHTTPServer(("0.0.0.0", args.port), AgentHandler)
    except OSError as e:
        print(f"\n  !  Could not start on port {args.port}: {e}")
        print("     Another copy of the agent may already be running.")
        print(f"     If you need a different port: python3 scorer_agent.py --port {args.port + 1}\n")
        sys.exit(1)

    banner(folder, how, args.port, args.discovery_port)

    threading.Thread(target=discovery_responder, args=(args.discovery_port,), daemon=True).start()
    threading.Thread(target=console_loop, daemon=True).start()

    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n  Agent stopped. The stream will fall back to whatever other source it has configured.\n")
    finally:
        httpd.server_close()


if __name__ == "__main__":
    main()
