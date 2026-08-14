"""Periodically reload an OBS media source to stop an RTSP camera drifting out of sync.

Match-day workaround. An RTSP feed in OBS's ffmpeg source can accumulate latency over a long
session, so the picture slowly falls behind the overlay graphics — the score updates before
you see the ball. Re-applying the source's settings tears the ffmpeg input down and rebuilds
it, which is exactly what happens when you open Properties and click OK; this just does it on
a timer instead of by hand.

Each reload costs a visible hitch of roughly a second, so prefer the longest interval that
keeps drift tolerable rather than the shortest one that feels safe.

    python refresh_cam.py                  # reload "Cam" every 60s
    python refresh_cam.py --interval 180   # every 3 minutes
    python refresh_cam.py --source Cam2    # a differently-named source
    python refresh_cam.py --once           # single reload, then exit

Ctrl+C to stop. Never raises on a failed reload — a refresher that dies mid-match is worse
than one that skips a beat, so errors are logged and the loop continues.
"""
import argparse
import base64
import configparser
import hashlib
import json
import os
import sys
import time

try:
    import websocket  # websocket-client, already in requirements.txt
except ImportError:
    sys.exit("websocket-client not installed — run: pip install websocket-client")

HERE = os.path.dirname(os.path.abspath(__file__))


def obs_password():
    cfg = configparser.ConfigParser()
    cfg.read(os.path.join(HERE, "config.ini"))
    return cfg.get("OBS", "obs_password", fallback="")


def log(msg):
    print(f"  {time.strftime('%H:%M:%S')}  {msg}", flush=True)


class OBS:
    """Minimal obs-websocket v5 client — connect, identify, request."""

    def __init__(self, host="localhost", port=4455, password=""):
        self.host, self.port, self.password = host, port, password
        self.ws = None
        self._id = 0

    def connect(self):
        self.ws = websocket.WebSocket()
        self.ws.connect(f"ws://{self.host}:{self.port}", timeout=5)
        hello = self._wait(0)
        if not hello:
            raise RuntimeError("no hello from OBS")
        auth = hello["d"].get("authentication")
        if auth and self.password:
            secret = base64.b64encode(
                hashlib.sha256((self.password + auth["salt"]).encode()).digest()).decode()
            resp = base64.b64encode(
                hashlib.sha256((secret + auth["challenge"]).encode()).digest()).decode()
            self._send(1, {"rpcVersion": 1, "authentication": resp})
        else:
            self._send(1, {"rpcVersion": 1})
        if not self._wait(2):
            raise RuntimeError("OBS authentication failed — check obs_password in config.ini")

    def close(self):
        try:
            if self.ws:
                self.ws.close()
        except Exception:
            pass
        self.ws = None

    def _send(self, op, data):
        self.ws.send(json.dumps({"op": op, "d": data}))

    def _wait(self, op, timeout=6):
        self.ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            msg = json.loads(self.ws.recv())
            if msg.get("op") == op:
                return msg
        return None

    def request(self, req_type, data=None, timeout=10):
        self._id += 1
        rid = str(self._id)
        payload = {"requestType": req_type, "requestId": rid}
        if data:
            payload["requestData"] = data
        self._send(6, payload)
        self.ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            msg = json.loads(self.ws.recv())
            if msg.get("op") == 7 and msg["d"].get("requestId") == rid:
                return msg["d"]
        return None


def reload_source(obs, name):
    """Re-apply the source's own settings, which makes OBS rebuild the ffmpeg input.

    Settings are read back and written unchanged, so this never edits the camera's
    configuration — no risk of clobbering the URL or the hardware-decode flag.
    """
    got = obs.request("GetInputSettings", {"inputName": name})
    if not got or not got.get("requestStatus", {}).get("result"):
        raise RuntimeError((got or {}).get("requestStatus", {}).get("comment")
                           or f"source '{name}' not found")
    settings = got["responseData"]["inputSettings"]
    put = obs.request("SetInputSettings", {"inputName": name,
                                           "inputSettings": settings,
                                           "overlay": True})
    if not put or not put.get("requestStatus", {}).get("result"):
        raise RuntimeError((put or {}).get("requestStatus", {}).get("comment") or "reload failed")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--source", default="Cam", help="media source name (default: Cam)")
    ap.add_argument("--interval", type=float, default=60, help="seconds between reloads")
    ap.add_argument("--once", action="store_true", help="reload once and exit")
    ap.add_argument("--host", default="localhost")
    ap.add_argument("--port", type=int, default=4455)
    args = ap.parse_args()

    obs = OBS(args.host, args.port, obs_password())
    obs.connect()
    log(f"connected to OBS — reloading '{args.source}' every {args.interval:.0f}s")
    log("Ctrl+C to stop")

    if args.once:
        reload_source(obs, args.source)
        log(f"reloaded '{args.source}'")
        obs.close()
        return

    n = 0
    try:
        while True:
            time.sleep(args.interval)
            try:
                reload_source(obs, args.source)
                n += 1
                log(f"reloaded '{args.source}'  (#{n})")
            except Exception as e:
                # Most likely the socket dropped (OBS restarted). Reconnect and carry on
                # rather than dying — this runs unattended through a whole match.
                log(f"reload failed: {type(e).__name__}: {e} — reconnecting")
                obs.close()
                try:
                    obs = OBS(args.host, args.port, obs_password())
                    obs.connect()
                    log("reconnected")
                except Exception as e2:
                    log(f"reconnect failed: {e2} — will retry next cycle")
    except KeyboardInterrupt:
        log(f"stopped after {n} reload(s)")
    finally:
        obs.close()


if __name__ == "__main__":
    main()
