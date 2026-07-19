#!/usr/bin/env python3
"""
nvplay_bridge.py — run this on the machine that has NV Play / PCS Pro open, when that
machine is NOT the same one running server.py (e.g. a dedicated Windows box, so a scorer's
VM isn't sharing the streaming machine's CPU/heat budget with OBS + the Python server).

It serves NV Play's scoreboard output file over HTTP so server.py can mirror it in near
real time and feed it through the normal PCS pipeline, unchanged. Stdlib only — nothing
to pip install on the scorer's machine.

Usage:
    python3 nvplay_bridge.py

First run creates bridge_config.ini next to this script and asks for the NV Play output
folder (and picks a random token). Edit that file directly to change settings later, or
delete it to be asked again. Reachable over Tailscale is the recommended way to point the
streaming machine at this one — see CLAUDE.md's remote-access notes.
"""
import configparser, glob, hmac, json, os, secrets, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE        = os.path.dirname(os.path.abspath(__file__))
CONFIG_PATH = os.path.join(HERE, "bridge_config.ini")

# Same filenames/discovery rule as server.py's find_pcs_output_file. Duplicated rather than
# imported — this script has to run standalone, dependency-free, on its own machine, so it
# can't rely on server.py (or its third-party imports) being present there.
PCS_OUTPUT_FILENAMES = [
    "nvplay-scoreboard1.xml", "nvplay-scoreboard.xml",
    "scoreboard-output.json", "scoreboard-output.xml",
    "scoreboard.json", "scoreboard.xml",
    "pcs-output.json", "output.json", "live.json",
]


def find_pcs_output_file(folder):
    if not folder or not os.path.isdir(folder):
        return None
    for fname in PCS_OUTPUT_FILENAMES:
        path = os.path.join(folder, fname)
        if os.path.exists(path):
            return path
    candidates = glob.glob(os.path.join(folder, "*.json")) + glob.glob(os.path.join(folder, "*.xml"))
    if candidates:
        newest = max(candidates, key=os.path.getmtime)
        if time.time() - os.path.getmtime(newest) < 600:
            return newest
    return None


def load_or_create_config():
    cp = configparser.ConfigParser()
    if os.path.exists(CONFIG_PATH):
        cp.read(CONFIG_PATH, encoding="utf-8")
        if cp.has_section("bridge"):
            return cp
    print("No bridge_config.ini found — one-time setup.\n")
    folder = input("Full path to the NV Play / PCS Pro output folder: ").strip().strip('"')
    port   = input("Port to serve on [5050]: ").strip() or "5050"
    token  = secrets.token_hex(16)
    cp["bridge"] = {"folder": folder, "port": port, "token": token}
    with open(CONFIG_PATH, "w", encoding="utf-8") as f:
        cp.write(f)
    print(f"\nSaved {CONFIG_PATH} — edit it by hand to change these later.\n")
    return cp


class BridgeHandler(BaseHTTPRequestHandler):
    folder = None
    token  = None

    def log_message(self, fmt, *args):
        pass  # quiet — the startup banner covers what matters

    def _json(self, obj, status=200):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _authed(self):
        # A blank configured token must never authenticate a blank/missing header — that
        # would turn "token accidentally cleared" into "anyone on the network can read
        # the live score", silently.
        if not self.token:
            return False
        return hmac.compare_digest(self.headers.get("X-Bridge-Token", ""), self.token)

    def do_GET(self):
        if self.path.startswith("/pcs/ping"):
            # No token needed — just "is this machine up", no match data in the response.
            self._json({"ok": True})
            return
        if not self.path.startswith("/pcs/latest"):
            self._json({"ok": False, "error": "not found"}, status=404)
            return
        if not self._authed():
            self._json({"ok": False, "error": "bad token"}, status=401)
            return
        path = find_pcs_output_file(self.folder)
        if not path:
            self._json({"ok": False, "error": "no PCS output file found"})
            return
        try:
            with open(path, encoding="utf-8", errors="replace") as f:
                content = f.read()
            mtime = os.path.getmtime(path)
        except OSError as e:
            self._json({"ok": False, "error": str(e)})
            return
        self._json({"ok": True, "filename": os.path.basename(path),
                    "mtime": mtime, "content": content})


def main():
    cp     = load_or_create_config()
    folder = cp.get("bridge", "folder", fallback="").strip()
    port   = cp.getint("bridge", "port", fallback=5050)
    token  = cp.get("bridge", "token", fallback="")

    if not folder or not os.path.isdir(folder):
        print(f"WARNING: folder not found: {folder!r}")
        print(f"Edit {CONFIG_PATH} and fix the 'folder' path, then rerun.\n")

    if not token:
        print(f"WARNING: no token set in {CONFIG_PATH} — refusing to start.")
        print("A blank token would let anyone on the network read the live score.")
        print("Set one by hand, or delete bridge_config.ini and rerun to generate one.\n")
        return

    BridgeHandler.folder = folder
    BridgeHandler.token  = token
    httpd = ThreadingHTTPServer(("0.0.0.0", port), BridgeHandler)
    print(f"NV Play bridge — serving '{folder}' on port {port}")
    print(f"Token: {token}")
    print("On the streaming machine's control panel (Match Setup), set:")
    print(f"  NV Play Bridge URL:   http://<this machine's Tailscale IP>:{port}")
    print(f"  NV Play Bridge Token: {token}")
    print("\nWindows may prompt to allow network access the first time — allow it.")
    print("Keep this window open while streaming. Ctrl+C to stop.\n")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
