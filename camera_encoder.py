"""Read the Reolink camera's own encoder settings (resolution, bitrate, fps).

Why this matters: OBS re-encodes whatever the camera sends it. If the camera's main stream is
itself encoded at a low bitrate, the picture is already soft before OBS sees it, and raising
the OBS bitrate cannot recover detail that was never captured. Last season's telemetry showed
the stream pinned at 1.21 Mbps against a 2,500 kbps ceiling — the camera is the prime suspect.

Credentials are read from the RTSP URL already stored in the OBS 'Cam' source, so nothing new
needs storing, and they are sent in a POST body rather than a query string.

    python camera_encoder.py                  # read settings from the OBS Cam source
    python camera_encoder.py --host 192.0.2.10 --user admin      # prompts for password
    python camera_encoder.py --source Cam2

Must be run on the club network — the camera is not reachable from anywhere else.
"""
import argparse
import base64
import configparser
import getpass
import hashlib
import json
import os
import socket
import sys
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))


def obs_rtsp_url(source):
    """Pull the RTSP URL out of the named OBS media source."""
    try:
        import websocket
    except ImportError:
        return None
    cfg = configparser.ConfigParser()
    cfg.read(os.path.join(HERE, "config.ini"))
    password = cfg.get("OBS", "obs_password", fallback="")
    try:
        ws = websocket.WebSocket()
        ws.connect("ws://localhost:4455", timeout=5)
    except Exception:
        return None
    try:
        import time
        _id = [0]

        def send(op, d):
            ws.send(json.dumps({"op": op, "d": d}))

        def wait(op, timeout=6):
            ws.settimeout(timeout)
            end = time.time() + timeout
            while time.time() < end:
                m = json.loads(ws.recv())
                if m.get("op") == op:
                    return m
            return None

        hello = wait(0)
        auth = hello["d"].get("authentication")
        if auth and password:
            s = base64.b64encode(
                hashlib.sha256((password + auth["salt"]).encode()).digest()).decode()
            r = base64.b64encode(
                hashlib.sha256((s + auth["challenge"]).encode()).digest()).decode()
            send(1, {"rpcVersion": 1, "authentication": r})
        else:
            send(1, {"rpcVersion": 1})
        if not wait(2):
            return None
        _id[0] += 1
        rid = str(_id[0])
        send(6, {"requestType": "GetInputSettings", "requestId": rid,
                 "requestData": {"inputName": source}})
        ws.settimeout(8)
        end = time.time() + 8
        while time.time() < end:
            m = json.loads(ws.recv())
            if m.get("op") == 7 and m["d"].get("requestId") == rid:
                if not m["d"].get("requestStatus", {}).get("result"):
                    return None
                return m["d"]["responseData"]["inputSettings"].get("input")
        return None
    finally:
        try:
            ws.close()
        except Exception:
            pass


def api(host, params, body, timeout=10):
    """POST to the Reolink CGI API. Credentials go in the body, never the query string."""
    url = f"http://{host}/cgi-bin/api.cgi?{urllib.parse.urlencode(params)}"
    req = urllib.request.Request(
        url, data=json.dumps(body).encode(), method="POST",
        headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def login(host, user, password):
    out = api(host, {"cmd": "Login"},
              [{"cmd": "Login", "param": {"User": {"userName": user,
                                                   "password": password}}}])
    entry = out[0] if isinstance(out, list) and out else {}
    if entry.get("code") != 0:
        detail = (entry.get("error") or {}).get("detail", entry)
        raise RuntimeError(f"login rejected: {detail}")
    return entry["value"]["Token"]["name"]


def describe(stream, label):
    print(f"\n  {label}")
    print(f"    resolution : {stream.get('width')}x{stream.get('height')}"
          if stream.get("width") else f"    size       : {stream.get('size')}")
    print(f"    bitrate    : {stream.get('bitRate')} kbps")
    print(f"    framerate  : {stream.get('frameRate')} fps")
    print(f"    profile    : {stream.get('profile')}")
    if stream.get("gop"):
        print(f"    gop        : {stream.get('gop')}")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--source", default="Cam", help="OBS source to read the RTSP URL from")
    ap.add_argument("--host", help="camera IP (skips OBS lookup)")
    ap.add_argument("--user", help="camera username")
    ap.add_argument("--channel", type=int, default=0)
    args = ap.parse_args()

    host, user, password = args.host, args.user, None
    if not host:
        url = obs_rtsp_url(args.source)
        if not url:
            sys.exit(f"Could not read an RTSP URL from OBS source '{args.source}'. "
                     f"Is OBS running? Otherwise pass --host and --user.")
        p = urllib.parse.urlparse(url)
        host, user, password = p.hostname, p.username, p.password
        print(f"  camera {host} (from OBS source '{args.source}', user '{user}')")
    if not password:
        password = getpass.getpass(f"password for {user}@{host}: ")

    # Fail fast with a clear message rather than a long socket timeout.
    s = socket.socket()
    s.settimeout(4)
    try:
        s.connect((host, 80))
    except OSError as e:
        sys.exit(f"\n  Cannot reach {host}:80 ({e.__class__.__name__}). "
                 f"Are you on the same network as the camera?")
    finally:
        s.close()

    try:
        token = login(host, user, password)
    except (urllib.error.URLError, RuntimeError, KeyError) as e:
        sys.exit(f"  login failed: {e}")

    out = api(host, {"cmd": "GetEnc", "token": token},
              [{"cmd": "GetEnc", "action": 1, "param": {"channel": args.channel}}])
    entry = out[0] if isinstance(out, list) and out else {}
    if entry.get("code") != 0:
        sys.exit(f"  GetEnc failed: {entry}")

    val = entry.get("value", {}).get("Enc", {})
    rng = entry.get("range", {})
    if isinstance(rng, list):
        rng = rng[0] if rng else {}
    rng = rng.get("Enc", {})

    print("\n=== current encoder settings ===")
    if val.get("mainStream"):
        describe(val["mainStream"], "main stream  (h264Preview_01_main — what OBS uses)")
    if val.get("subStream"):
        describe(val["subStream"], "sub stream   (h264Preview_01_sub)")
    print(f"\n  audio enabled: {val.get('audio')}")

    main_rng = (rng.get("mainStream") or {})
    if main_rng:
        print("\n=== what the camera will allow (main stream) ===")
        for key, label in (("size", "resolutions"), ("bitRate", "bitrates (kbps)"),
                           ("frameRate", "framerates")):
            v = main_rng.get(key)
            if v:
                print(f"  {label:18}: {v}")

    cur = (val.get("mainStream") or {}).get("bitRate")
    if cur:
        print("\n=== read-out ===")
        if cur < 4096:
            print(f"  Main stream is {cur} kbps. That is low for this resolution — it is a"
                  f" strong candidate for the soft picture, and raising it costs nothing on"
                  f" a wired LAN. Change it in the camera's web UI: Settings -> Display ->"
                  f" Stream (or Encode).")
        else:
            print(f"  Main stream is {cur} kbps, which is healthy. The bitrate cap is"
                  f" probably NOT the limiter — look at OBS output resolution instead.")


if __name__ == "__main__":
    main()
