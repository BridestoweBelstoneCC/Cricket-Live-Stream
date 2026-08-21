"""
CricketStream Overlay — Automated stream-quality-ladder test
──────────────────────────────────────────────────────────────
Automates the tedious/timing-sensitive part of the manual "does the quality ladder
survive a real broadcast" test: watching congestion, triggering a manual quality step,
waiting through the ~5-10s stop-reconfigure-start gap, and confirming the stream actually
survived rather than dying -- then doing the same for the restore step.

Deliberately does NOT start or stop the OBS stream itself -- going live is a real, visible
action you should trigger yourself. Click "Start Streaming" in OBS first (persistent
stream key, not "connect account" -- see BEFORE YOU RUN THIS below), then run this.

BEFORE YOU RUN THIS:
    - OBS must already be streaming, using a PERSISTENT STREAM KEY. "Connect account"
      mode's auto-stop ends the whole broadcast the moment a quality step
      stops/restarts the stream connection -- this script would then report a false
      failure (or worse, silently confirm a broadcast that's actually already dead).
    - Set the YouTube broadcast to Unlisted or Private first -- this is a test, not
      something to publish. This script never touches YouTube's privacy setting.
    - The CricketStream server must already be running (quickstart or the exe).

    python stream_quality_test.py
"""
import base64, configparser, hashlib, json, os, sys, time, urllib.error, urllib.request

try:
    import websocket  # websocket-client, already in requirements.txt
except ImportError:
    sys.exit("websocket-client not installed — run: pip install websocket-client")

HERE   = os.path.dirname(os.path.abspath(__file__))
SERVER = "http://127.0.0.1:5000"
SETTLE_WAIT   = 8    # seconds to let a fresh stream settle before the first check
SHIFT_WAIT    = 12   # seconds to let a down/restore step's stop-reconfigure-start finish
POLL_INTERVAL = 2


def log(msg, status=""):
    icons = {"ok": "  ✓", "warn": "  ⚠", "err": "  ✗", "": "   "}
    print(f"{icons.get(status,'   ')} {msg}")


def load_config():
    cfg = configparser.ConfigParser()
    cfg.read(os.path.join(HERE, "config.ini"), encoding="utf-8")
    return cfg


class OBS:
    """Minimal obs-websocket v5 client -- connect, identify, request, close. Same
    handshake as refresh_cam.py/stream_telemetry.py/server.py's own _obs_call(),
    duplicated rather than imported since this has to run standalone."""

    def __init__(self, password=""):
        self.password = password
        self.ws = None
        self._id = 0

    def connect(self):
        self.ws = websocket.WebSocket()
        self.ws.connect("ws://localhost:4455", timeout=5)
        hello = self._wait(0)
        if not hello:
            raise RuntimeError("no hello from OBS — is OBS running with WebSocket enabled?")
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

    def _send(self, op, d):
        self.ws.send(json.dumps({"op": op, "d": d}))

    def _wait(self, op, timeout=6):
        self.ws.settimeout(timeout)
        end = time.time() + timeout
        while time.time() < end:
            m = json.loads(self.ws.recv())
            if m.get("op") == op:
                return m
        return None

    def request(self, req_type, data=None, timeout=8):
        self._id += 1
        rid = str(self._id)
        payload = {"requestType": req_type, "requestId": rid}
        if data:
            payload["requestData"] = data
        self._send(6, payload)
        self.ws.settimeout(timeout)
        end = time.time() + timeout
        while time.time() < end:
            m = json.loads(self.ws.recv())
            if m.get("op") == 7 and m["d"].get("requestId") == rid:
                return m["d"]
        return None

    def stream_status(self):
        got = self.request("GetStreamStatus")
        if not got or not got.get("requestStatus", {}).get("result"):
            return None
        return got["responseData"]


def get_token(cfg):
    pw = cfg["Auth"].get("club_password", "").strip() if cfg.has_section("Auth") else ""
    if not pw:
        return ""
    try:
        req = urllib.request.Request(f"{SERVER}/login",
            data=json.dumps({"password": pw}).encode(),
            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as r:
            d = json.loads(r.read().decode())
        return d.get("session_token", "") if d.get("ok") else ""
    except Exception:
        return ""


def get_json(path, token="", timeout=6):
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    req = urllib.request.Request(f"{SERVER}{path}", headers=headers)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def post_json(path, body, token="", timeout=15):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(f"{SERVER}{path}", data=json.dumps(body).encode(),
                                  headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return json.loads(e.read().decode())
        except Exception:
            return {"ok": False, "error": f"HTTP {e.code}"}


def watch_stream_survives(obs, seconds, label):
    """Poll GetStreamStatus every POLL_INTERVAL for `seconds`, printing outputActive and
    dropped-frame counts. Returns False the moment the stream is seen NOT active -- that's
    the actual failure mode this whole test exists to catch."""
    log(f"Watching the stream through {label} ({seconds}s)...")
    end = time.time() + seconds
    survived = True
    while time.time() < end:
        st = obs.stream_status()
        if st is None:
            log("Could not reach OBS mid-check — is it still running?", "err")
            return False
        active = st.get("outputActive")
        skipped = st.get("outputSkippedFrames", 0)
        print(f"      active={active}  skipped_frames={skipped}  "
              f"reconnecting={st.get('outputReconnecting')}")
        if not active:
            survived = False
        time.sleep(POLL_INTERVAL)
    return survived


def main():
    cfg = load_config()
    obs_pw = cfg["OBS"].get("obs_password", "") if cfg.has_section("OBS") else ""
    token = get_token(cfg)

    print()
    print("═══════════════════════════════════════════════════════")
    print("  CricketStream — automated stream-quality-ladder test")
    print("═══════════════════════════════════════════════════════")
    print()

    obs = OBS(obs_pw)
    try:
        obs.connect()
    except Exception as e:
        sys.exit(f"  ✗  Could not connect to OBS: {e}")
    log("Connected to OBS", "ok")

    baseline = obs.stream_status()
    if not baseline or not baseline.get("outputActive"):
        obs.close()
        sys.exit("  ✗  OBS is not currently streaming. Start streaming in OBS first "
                 "(persistent stream key, Unlisted/Private broadcast), then re-run this.")
    log(f"Stream is live — {baseline.get('outputBytes',0)} bytes sent so far", "ok")

    mon = get_json("/stream/monitor", token)
    log(f"Server sees it too — step {mon.get('step')}, "
        f"dynamic_bitrate={mon.get('dynamic_bitrate')}, congestion={mon.get('congestion')}",
        "ok" if mon.get("ok") else "warn")

    log(f"Letting it settle for {SETTLE_WAIT}s before the first shift...")
    time.sleep(SETTLE_WAIT)

    # ── Step down ──
    print()
    log("Triggering: Reduce quality now (down one step)")
    r = post_json("/stream/quality", {"action": "down"}, token)
    if not r.get("ok"):
        obs.close()
        sys.exit(f"  ✗  /stream/quality down was rejected: {r.get('error')}")
    down_survived = watch_stream_survives(obs, SHIFT_WAIT, "the down-shift")
    mon_down = get_json("/stream/monitor", token)
    log(f"Now at step {mon_down.get('step')}, {mon_down.get('current_kbps')} kbps "
        f"(baseline {mon_down.get('baseline_kbps')} kbps)",
        "ok" if down_survived else "err")
    if not down_survived:
        log("Stream did NOT survive the down-shift — check OBS/YouTube Studio now", "err")

    # ── Restore ──
    print()
    log("Triggering: Restore full quality")
    r = post_json("/stream/quality", {"action": "restore"}, token)
    if not r.get("ok"):
        obs.close()
        sys.exit(f"  ✗  /stream/quality restore was rejected: {r.get('error')}")
    up_survived = watch_stream_survives(obs, SHIFT_WAIT, "the restore")
    mon_up = get_json("/stream/monitor", token)
    log(f"Back to step {mon_up.get('step')}, {mon_up.get('current_kbps')} kbps",
        "ok" if up_survived else "err")
    if not up_survived:
        log("Stream did NOT survive the restore — check OBS/YouTube Studio now", "err")

    obs.close()

    print()
    print("  ─────────────────────────────────────────────")
    if down_survived and up_survived:
        print("  ✓  ALL SYSTEMS GO — both shifts survived cleanly")
    else:
        print("  ⚠  At least one shift did not survive — do not enable auto quality "
              "scaling until this is understood")
    print("  Stream is still live — stop it manually in OBS when you're done.")
    print("  ─────────────────────────────────────────────")
    print()


if __name__ == "__main__":
    main()
