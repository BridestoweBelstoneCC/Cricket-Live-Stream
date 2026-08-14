"""Passive match-day telemetry logger — writes a CSV you can analyse afterwards.

Samples OBS, the CricketStream server and the scorer's feed on a timer and appends one row
per sample. Everything here is READ-ONLY and cheap: no active bandwidth tests (those would
compete with the live stream and cause the very stalls we are trying to measure), and the
match feed is read from /live/view, never /live — /live is a mutating GET that only the OBS
overlay may call.

    python stream_telemetry.py                       # sample every 10s until Ctrl+C
    python stream_telemetry.py --interval 5
    python stream_telemetry.py --summary diagnostics/telemetry_20260725_1355.csv

Rows land in diagnostics/telemetry_<date>_<time>.csv. Open in Excel, or use --summary for a
quick read-out of the interesting numbers.
"""
import argparse
import base64
import configparser
import csv
import hashlib
import json
import os
import statistics
import sys
import time
import urllib.request

try:
    import websocket
except ImportError:
    sys.exit("websocket-client not installed")

HERE = os.path.dirname(os.path.abspath(__file__))
DIAG = os.path.join(HERE, "diagnostics")
SERVER = "http://127.0.0.1:5000"

FIELDS = [
    "iso_time", "elapsed_s",
    # --- network / encoder ---
    "inst_mbps", "congestion", "delta_skipped", "skipped_total", "frames_total",
    "stream_active", "reconnecting",
    # --- OBS process ---
    "obs_cpu_pct", "obs_fps", "obs_mem_mb", "render_skipped", "render_total",
    # --- adaptive quality ---
    "ladder_step", "ladder_current_kbps", "ladder_baseline_kbps", "ladder_verdict",
    # --- scorer feed ---
    "pcs_fresh", "pcs_age_s",
    # --- match context, so drops can be correlated with what was happening ---
    "batting_team", "score", "wickets", "overs", "last_ball",
    # --- server process ---
    "srv_cpu_pct", "srv_rss_mb", "srv_errors",
    # --- headroom probe (blank on most rows; only populated when a probe ran) ---
    "probe_mbps", "probe_skipped_why",
]

# ── Headroom probe ────────────────────────────────────────────────────────────────
# Passive monitoring cannot measure headroom you are not using: congestion sits at zero
# whenever the stream is comfortably inside the line's capacity, which tells you nothing
# about where the ceiling actually is. A small upload occasionally is the only way to find
# out. Deliberately tiny — 512 KB is under a second of traffic against a 2.5 Mbps stream,
# roughly 0.3% overhead at the default interval — and it refuses to run whenever the stream
# is already struggling, so it can never be the cause of a problem it is meant to detect.
PROBE_BYTES = 512 * 1024
PROBE_URL = "https://speed.cloudflare.com/__up"
PROBE_TIMEOUT = 20


def probe_upload():
    """Measure achievable upload in Mbps. Returns (mbps, None) or (None, reason)."""
    payload = b"\0" * PROBE_BYTES
    req = urllib.request.Request(
        PROBE_URL, data=payload, method="POST",
        # Cloudflare 403s urllib's default User-Agent.
        headers={"Content-Type": "application/octet-stream",
                 "User-Agent": "Mozilla/5.0 (CricketStreamOverlay telemetry-probe)"})
    try:
        ctx = None
        try:                       # match server.py: certifi, or system certs if absent
            import ssl, certifi
            ctx = ssl.create_default_context(cafile=certifi.where())
        except Exception:
            pass
        start = time.time()
        with urllib.request.urlopen(req, timeout=PROBE_TIMEOUT, context=ctx) as r:
            r.read()
        elapsed = max(time.time() - start, 0.001)
        return round((PROBE_BYTES * 8 / elapsed) / 1_000_000, 2), None
    except Exception as e:
        return None, f"{type(e).__name__}"


def get_json(path, timeout=6):
    try:
        with urllib.request.urlopen(f"{SERVER}{path}", timeout=timeout) as r:
            return json.loads(r.read().decode())
    except Exception:
        return None


class OBS:
    def __init__(self):
        cfg = configparser.ConfigParser()
        cfg.read(os.path.join(HERE, "config.ini"))
        self.password = cfg.get("OBS", "obs_password", fallback="")
        self.ws = None
        self._id = 0

    def connect(self):
        self.ws = websocket.WebSocket()
        self.ws.connect("ws://localhost:4455", timeout=5)
        hello = self._wait(0)
        auth = hello["d"].get("authentication")
        if auth and self.password:
            s = base64.b64encode(
                hashlib.sha256((self.password + auth["salt"]).encode()).digest()).decode()
            r = base64.b64encode(
                hashlib.sha256((s + auth["challenge"]).encode()).digest()).decode()
            self._send(1, {"rpcVersion": 1, "authentication": r})
        else:
            self._send(1, {"rpcVersion": 1})
        if not self._wait(2):
            raise RuntimeError("OBS auth failed")

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

    def request(self, rt, d=None, timeout=8):
        self._id += 1
        rid = str(self._id)
        p = {"requestType": rt, "requestId": rid}
        if d:
            p["requestData"] = d
        self._send(6, p)
        self.ws.settimeout(timeout)
        end = time.time() + timeout
        while time.time() < end:
            m = json.loads(self.ws.recv())
            if m.get("op") == 7 and m["d"].get("requestId") == rid:
                return m["d"]
        return None

    def close(self):
        try:
            if self.ws:
                self.ws.close()
        except Exception:
            pass


def sample(obs, prev):
    """One row. prev carries the last (bytes, skipped, time) so rates can be differenced.

    obs may be None: OBS is often not up yet when this starts on match day, and losing the
    whole log to that would defeat the point. The server-side and match-context columns are
    collected regardless; the OBS columns simply stay blank until it connects.
    """
    now = time.time()
    row = {k: "" for k in FIELDS}
    row["iso_time"] = time.strftime("%Y-%m-%d %H:%M:%S")

    if obs is None:
        st = gs = {}
    else:
        st = (obs.request("GetStreamStatus") or {}).get("responseData", {})
        gs = (obs.request("GetStats") or {}).get("responseData", {})

    b, sk = st.get("outputBytes", 0), st.get("outputSkippedFrames", 0)
    if prev:
        dt = max(now - prev["t"], 0.001)
        # A stream restart zeroes OBS's counters, so a raw difference goes hugely negative
        # and poisons every average taken over the file. Blank the sample instead — the
        # restart itself is still visible as a drop in frames_total.
        if b >= prev["bytes"] and sk >= prev["skipped"]:
            row["inst_mbps"] = round((b - prev["bytes"]) * 8 / dt / 1_000_000, 3)
            row["delta_skipped"] = sk - prev["skipped"]
    row["congestion"] = round(st.get("outputCongestion", 0) or 0, 4)
    row["skipped_total"] = sk
    row["frames_total"] = st.get("outputTotalFrames", 0)
    row["stream_active"] = st.get("outputActive")
    row["reconnecting"] = st.get("outputReconnecting")

    row["obs_cpu_pct"] = round(gs.get("cpuUsage", 0) or 0, 2)
    row["obs_fps"] = round(gs.get("activeFps", 0) or 0, 1)
    row["obs_mem_mb"] = round(gs.get("memoryUsage", 0) or 0, 1)
    row["render_skipped"] = gs.get("renderSkippedFrames", 0)
    row["render_total"] = gs.get("renderTotalFrames", 0)

    mon = get_json("/stream/monitor")
    if mon:
        row["ladder_step"] = mon.get("step")
        row["ladder_current_kbps"] = mon.get("current_kbps")
        row["ladder_baseline_kbps"] = mon.get("baseline_kbps")
        row["ladder_verdict"] = mon.get("verdict")

    h = get_json("/health")
    if h:
        pcs = h.get("pcs", {}) or {}
        row["pcs_fresh"] = pcs.get("fresh")
        row["pcs_age_s"] = pcs.get("age_sec")
        srv = h.get("server", {}) or {}
        row["srv_cpu_pct"] = srv.get("cpu_pct")
        row["srv_rss_mb"] = srv.get("max_rss_mb")
        row["srv_errors"] = len(h.get("errors", []) or [])

    live = get_json("/live/view")           # /live/view — never /live
    if live:
        s = live.get("state", {}) or {}
        row["batting_team"] = s.get("battingTeamName", "")
        row["score"] = s.get("score")
        row["wickets"] = s.get("wickets")
        row["overs"] = s.get("overs")
        row["last_ball"] = (s.get("last_ball") or "").strip()

    return row, {"t": now, "bytes": b, "skipped": sk}


def summarise(path):
    with open(path, newline="", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    if not rows:
        print("no rows")
        return

    def nums(key):
        out = []
        for r in rows:
            try:
                out.append(float(r[key]))
            except (ValueError, KeyError, TypeError):
                pass
        return out

    def restarts():
        """Stream restarts show up as frames_total going backwards."""
        found, prev = [], None
        for r in rows:
            try:
                fr = float(r["frames_total"])
            except (ValueError, KeyError, TypeError):
                continue
            if prev is not None and fr < prev:
                found.append(r["iso_time"])
            prev = fr
        return found

    print(f"\n=== {os.path.basename(path)} ===")
    print(f"samples      : {len(rows)}")
    print(f"window       : {rows[0]['iso_time']}  ->  {rows[-1]['iso_time']}")

    rs = restarts()
    print(f"stream restarts: {len(rs)}" + (f"  ({', '.join(rs)})" if rs else ""))

    mbps = [m for m in nums("inst_mbps") if 0 <= m < 100]
    if mbps:
        ordered = sorted(mbps)
        print(f"\nbitrate (Mbps, {len(mbps)} valid samples)")
        print(f"  mean {statistics.mean(mbps):.2f} | median {statistics.median(mbps):.2f} "
              f"| min {min(mbps):.2f} | max {max(mbps):.2f} "
              f"| stdev {statistics.pstdev(mbps):.3f}")
        print(f"  p05 {ordered[int(.05*len(ordered))]:.2f} "
              f"| p95 {ordered[int(.95*len(ordered))]:.2f}")

    cong = nums("congestion")
    if cong:
        bad = [c for c in cong if c > 0]
        print(f"\ncongestion")
        print(f"  mean {statistics.mean(cong):.3f} | max {max(cong):.3f} "
              f"| samples above zero: {len(bad)}/{len(cong)}")

    ds = nums("delta_skipped")
    if ds:
        drops = [d for d in ds if d > 0]
        print(f"\ndropped frames")
        print(f"  total {int(sum(ds))} | sample windows with drops: {len(drops)}/{len(ds)}")
        if drops:
            worst = max(range(len(rows)), key=lambda i: float(rows[i].get("delta_skipped") or 0))
            r = rows[worst]
            print(f"  worst window at {r['iso_time']}: {r['delta_skipped']} frames "
                  f"(congestion {r['congestion']}, score {r['score']}-{r['wickets']} "
                  f"{r['overs']} ov)")

    steps = [r.get("ladder_step") for r in rows if r.get("ladder_step") not in ("", None)]
    if steps:
        changes = [(rows[i]["iso_time"], steps[i - 1], steps[i])
                   for i in range(1, len(steps)) if steps[i] != steps[i - 1]]
        print(f"\nquality ladder")
        print(f"  step values seen: {sorted(set(steps))}")
        if changes:
            for t, a, b in changes:
                print(f"  {t}: step {a} -> {b}")
        else:
            print("  no downshifts during this window")

    fresh = [r.get("pcs_fresh") for r in rows]
    stale = [f for f in fresh if str(f).lower() == "false"]
    print(f"\nscorer feed")
    print(f"  stale samples: {len(stale)}/{len(fresh)}")

    # Headroom probes: what the line could actually carry, versus what was being sent.
    probes = [(r["iso_time"], float(r["probe_mbps"]))
              for r in rows if (r.get("probe_mbps") or "").strip()]
    skipped = [r.get("probe_skipped_why") for r in rows
               if (r.get("probe_skipped_why") or "").strip()]
    if probes or skipped:
        print(f"\nheadroom probes")
    if probes:
        vals = [p[1] for p in probes]
        print(f"  {len(probes)} probe(s): mean {statistics.mean(vals):.2f} "
              f"| min {min(vals):.2f} | max {max(vals):.2f} Mbps")
        print(f"  first {probes[0][0]} = {probes[0][1]:.2f}  ->  "
              f"last {probes[-1][0]} = {probes[-1][1]:.2f}")
        if mbps:
            sent = statistics.median(mbps)
            print(f"  median sent {sent:.2f} Mbps vs min measured {min(vals):.2f} Mbps "
                  f"-> headroom ~{max(min(vals) - sent, 0):.2f} Mbps")
        worst = min(probes, key=lambda p: p[1])
        print(f"  worst probe: {worst[1]:.2f} Mbps at {worst[0]}")
    if skipped:
        from collections import Counter
        for why, k in Counter(skipped).most_common():
            print(f"  skipped x{k}: {why}")
    print()


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--interval", type=float, default=10, help="seconds between samples")
    ap.add_argument("--summary", metavar="CSV", help="summarise an existing CSV and exit")
    ap.add_argument("--probe-every", type=float, default=300,
                    help="seconds between headroom probes; 0 disables (default 300)")
    args = ap.parse_args()

    if args.summary:
        summarise(args.summary)
        return

    os.makedirs(DIAG, exist_ok=True)
    path = os.path.join(DIAG, f"telemetry_{time.strftime('%Y%m%d_%H%M')}.csv")

    # Never die because OBS isn't up yet. quickstart launches this automatically and sends
    # its output to DEVNULL, so a crash here would be silent and cost the whole match's log.
    obs = OBS()
    try:
        obs.connect()
    except Exception as e:
        print(f"  OBS not reachable ({type(e).__name__}) — logging server + match data,"
              f" retrying OBS each sample", flush=True)
        obs = None
    print(f"  logging to {path}")
    print(f"  sampling every {args.interval:.0f}s — Ctrl+C to stop\n", flush=True)

    if args.probe_every:
        print(f"  headroom probe: {PROBE_BYTES//1024} KB every {args.probe_every:.0f}s, "
              f"skipped while congested", flush=True)

    start = time.time()
    prev = None
    n = 0
    last_probe = time.time()      # don't probe immediately; let the stream settle first
    with open(path, "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=FIELDS)
        w.writeheader()
        try:
            while True:
                try:
                    if obs is None:      # OBS wasn't up at launch — keep trying quietly
                        try:
                            obs = OBS()
                            obs.connect()
                            print(f"  {time.strftime('%H:%M:%S')}  OBS connected", flush=True)
                        except Exception:
                            obs = None
                    row, prev = sample(obs, prev)
                    row["elapsed_s"] = round(time.time() - start)

                    # Headroom probe. Guarded so it can never worsen a struggling stream:
                    # only while streaming, only with congestion and drops at zero.
                    if args.probe_every and (time.time() - last_probe) >= args.probe_every:
                        last_probe = time.time()
                        cong = row.get("congestion") or 0
                        drops = row.get("delta_skipped") or 0
                        if not row.get("stream_active"):
                            row["probe_skipped_why"] = "not streaming"
                        elif float(cong) > 0:
                            row["probe_skipped_why"] = f"congested ({cong})"
                        elif float(drops) > 0:
                            row["probe_skipped_why"] = f"dropping frames ({drops})"
                        else:
                            mbps, why = probe_upload()
                            row["probe_mbps"] = mbps if mbps is not None else ""
                            row["probe_skipped_why"] = why or ""
                            print(f"  {row['iso_time']}  probe: "
                                  f"{mbps if mbps is not None else 'failed - ' + str(why)} Mbps",
                                  flush=True)

                    w.writerow(row)
                    f.flush()          # flush every row — a crash must not lose the log
                    n += 1
                    if n % 6 == 0:
                        print(f"  {row['iso_time']}  {n} samples  "
                              f"{row['inst_mbps']} Mbps  cong {row['congestion']}  "
                              f"drops {row['delta_skipped']}  "
                              f"{row['score']}-{row['wickets']} ({row['overs']} ov)",
                              flush=True)
                except Exception as e:
                    print(f"  sample failed: {type(e).__name__}: {e} — reconnecting",
                          flush=True)
                    if obs is not None:
                        obs.close()
                    try:
                        obs = OBS()
                        obs.connect()
                    except Exception:
                        pass
                time.sleep(args.interval)
        except KeyboardInterrupt:
            pass
    print(f"\n  stopped — {n} samples written to {path}")
    summarise(path)


if __name__ == "__main__":
    main()
