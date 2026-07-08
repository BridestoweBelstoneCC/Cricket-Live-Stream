"""
CricketStream Overlay — Match Simulator
───────────────────────────────────────
Rehearse the entire broadcast without a scorer: simulates a realistic match ball by ball
and writes NV Play-style scoreboard JSON to a folder, exactly as PCS Pro would. Point the
control panel's "PCS Pro output folder" at the simulator's folder (or pass --configure to
do that automatically) and the overlay/graphics/replay pipeline runs as if it were match day.

    python3 simulate_match.py                      # full 20-over match, ~3s per ball
    python3 simulate_match.py --scenario chase     # jump straight to a tense run chase
    python3 simulate_match.py --scenario century   # an opener closing in on a hundred
    python3 simulate_match.py --scenario collapse  # wickets tumbling
    python3 simulate_match.py --list               # describe all scenarios
    python3 simulate_match.py --configure          # also point the running server at it
    python3 simulate_match.py --ball-seconds 1     # faster rehearsal
    python3 simulate_match.py --chaos              # inject mid-write/stall failures too
    python3 simulate_match.py --seed 42            # reproducible match

Faithful to the real feed in the ways that have caused bugs before (see CLAUDE.md):
  • the ball ticker (last_ball) clears to "" on the SAME write that completes an over —
    it never lingers for an extra poll, so the final ball only appears in the score delta
  • batter names are blank pre-match (not {{placeholders}}), exactly as NV Play renders
    the template before the scorer picks the openers
  • innings 2 is signalled by runs_required > 0, and it drops to 0 on the winning runs
    (the case the server's innings latch exists for)
  • all values are strings, matching NV Play's template output

The engine itself is deterministic for a given --seed and importable (tests feed its
frames straight into server.parse_pcs_json), so it doubles as a regression harness.
"""
import argparse
import json
import os
import random
import sys
import time

from scoring_engine import InningsEngine

# ── Squads (deliberately club-agnostic; two Smiths to rehearse the brothers path) ──
HOME_TEAM = "Home CC"
AWAY_TEAM = "Rival CC"
HOME_XI = [  # (shirt number, name) — batting order
    ("21", "Peter Smith"), ("7", "James Smith"), ("14", "Oliver Hart"),
    ("9", "Karl Jones"), ("23", "Ben Walker"), ("4", "Sam Prowse"),
    ("11", "Tom Baker"), ("2", "Harry Cole"), ("17", "Jack Down"),
    ("6", "Lee Friend"), ("30", "Adam Gill"),
]
AWAY_XI = [
    ("5", "Rob North"), ("12", "Dan Vine"), ("3", "Max Reed"),
    ("19", "Joe Marsh"), ("8", "Ed Stone"), ("15", "Guy Bell"),
    ("1", "Ian Frost"), ("24", "Ali Khan"), ("10", "Ray Lamb"),
    ("13", "Cy Woods"), ("27", "Vic Moon"),
]

# Dismissal-type weights for SAMPLED wickets (manual scoring picks the type explicitly)
SIM_WICKET_WEIGHTS = [(45, "caught"), (30, "bowled"), (12, "lbw"), (8, "run out"), (5, "stumped")]


class SimInnings(InningsEngine):
    """The shared scorer's-book engine (scoring_engine.py) plus random outcome sampling —
    all the bookkeeping lives in the engine; only the dice live here. The manual scoring
    page drives the SAME engine with operator button presses instead."""

    def __init__(self, batting_xi, bowling_xi, batting_name, bowling_name,
                 max_overs, rng, target=None, wicket_boost=1.0, run_boost=1.0):
        super().__init__(batting_xi, bowling_xi, batting_name, bowling_name,
                         max_overs, target=target)
        self.rng = rng
        self.wicket_boost, self.run_boost = wicket_boost, run_boost

    def _sample_outcome(self):
        w = self.wicket_boost
        r = self.run_boost
        outcomes = [  # (weight, kind)
            (300, "dot"), (280 * r, "1"), (85 * r, "2"), (12, "3"),
            (95 * r, "4"), (30 * r, "6"), (42 * w, "W"),
            (38, "wide"), (12, "noball"), (8, "bye"), (16, "legbye"),
        ]
        total = sum(wt for wt, _ in outcomes)
        pick = self.rng.uniform(0, total)
        for wt, kind in outcomes:
            pick -= wt
            if pick <= 0:
                return kind
        return "dot"

    def ball(self):
        """Bowl one randomly sampled delivery; returns the ticker token ('' if the
        innings was already complete)."""
        if self.complete:
            return ""
        self.openers_selected = True
        kind = self._sample_outcome()
        if kind == "W":
            pick = self.rng.uniform(0, sum(w for w, _ in SIM_WICKET_WEIGHTS))
            wtype = SIM_WICKET_WEIGHTS[-1][1]
            for w, wtype in SIM_WICKET_WEIGHTS:
                pick -= w
                if pick <= 0:
                    break
            bowler = self.bowler_for_over(self.current_over_no)
            fielder = ""
            if wtype in ("caught", "stumped", "run out"):
                fielder = self.rng.choice([f for f in self.fielders if f != bowler.name])
            return self.apply_outcome("W", wicket_type=wtype, fielder=fielder)
        if kind == "wide":
            return self.apply_outcome("wide", runs=self.rng.choice([0, 0, 0, 1, 4]))
        if kind == "noball":
            return self.apply_outcome("noball", runs=self.rng.choice([0, 0, 1, 2, 4]))
        if kind in ("bye", "legbye"):
            return self.apply_outcome(kind, runs=self.rng.choice([1, 1, 2, 4]))
        return self.apply_outcome(kind)


class MatchSimulator:
    """Yields (label, frame) pairs for a whole scenario. Pure — no sleeping, no I/O —
    so tests can consume it directly; the CLI adds pacing and file writes."""

    SCENARIOS = {
        "full":     "Complete match from pre-match blanks through both innings to the result",
        "chase":    "Jump straight into a tense final-overs run chase (innings 2 live)",
        "century":  "An opener in the 80s closing in on a hundred (milestone + replay rehearsal)",
        "collapse": "Wickets tumbling — FOW cards, new-batter player cards, bowler figures",
    }

    def __init__(self, scenario="full", max_overs=20, seed=None):
        if scenario not in self.SCENARIOS:
            raise ValueError(f"unknown scenario {scenario!r} — one of {sorted(self.SCENARIOS)}")
        self.scenario = scenario
        self.max_overs = max_overs
        self.rng = random.Random(seed)
        self.innings = []

    def _new_innings(self, second=False, target=None, max_overs=None, **tweaks):
        bat_xi, bowl_xi = (AWAY_XI, HOME_XI) if second else (HOME_XI, AWAY_XI)
        bat_nm, bowl_nm = (AWAY_TEAM, HOME_TEAM) if second else (HOME_TEAM, AWAY_TEAM)
        inn = SimInnings(bat_xi, bowl_xi, bat_nm, bowl_nm, max_overs or self.max_overs,
                         self.rng, target=target, **tweaks)
        self.innings.append(inn)
        return inn

    @staticmethod
    def _fast_forward(inn, until):
        """Advance an innings silently (no frames) until `until(inn)` is true."""
        while not inn.complete and not until(inn):
            inn.ball()
        inn.token_history = []      # frames start fresh; history is per-emitted-play only

    def frames(self):
        if self.scenario == "chase":
            inn1 = self._new_innings()
            self._fast_forward(inn1, lambda i: False)          # play innings 1 out silently
            target = inn1.total + 1
            inn2 = self._new_innings(second=True, target=target)
            self._fast_forward(inn2, lambda i: (target - i.total) <= 40
                               or (i.max_overs * 6 - i.legal_balls) <= 30)
            yield from self._play(inn2, innings_no=2)
            return

        if self.scenario == "century":
            # A longer innings and docile bowling so an opener reliably reaches the 80s
            inn = self._new_innings(wicket_boost=0.2, run_boost=1.5,
                                    max_overs=max(self.max_overs, 40))
            self._fast_forward(inn, lambda i: max(b.runs for b in i.batters) >= 85)
            yield from self._play(inn, innings_no=1)
            return

        if self.scenario == "collapse":
            inn = self._new_innings()
            self._fast_forward(inn, lambda i: i.legal_balls >= 8 * 6)
            inn.wicket_boost = 5.0
            yield from self._play(inn, innings_no=1)
            return

        # full match: pre-match blanks → innings 1 → break → innings 2 → result
        inn1 = self._new_innings()
        for _ in range(3):
            yield ("prematch", inn1.frame(1))
        yield from self._play(inn1, innings_no=1)
        for _ in range(3):                                     # innings break holds
            yield ("break", inn1.frame(1))
        inn2 = self._new_innings(second=True, target=inn1.total + 1)
        for _ in range(2):
            yield ("prematch2", inn2.frame(2))                 # openers not yet picked
        yield from self._play(inn2, innings_no=2)

    def _play(self, inn, innings_no):
        while not inn.complete:
            inn.ball()
            yield ("ball", inn.frame(innings_no))
        for _ in range(3):                                     # hold the final state
            yield ("end", inn.frame(innings_no))


# ── CLI ───────────────────────────────────────────────────────
OUTPUT_FILENAME = "nvplay-scoreboard1.xml"   # NV Play writes JSON into a .xml file — kept
                                             # identical so find_pcs_output_file matches it

def configure_server(folder, port=5000):
    """Best-effort: point the RUNNING server's PCS folder at the sim folder (logging in
    with config.ini's club_password if auth is on). Prints what to restore afterwards."""
    import configparser
    import urllib.request
    base = f"http://127.0.0.1:{port}"
    token = ""
    cfg = configparser.ConfigParser()
    cfg.read(os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini"),
             encoding="utf-8")
    pw = cfg.get("Auth", "club_password", fallback="").strip()
    try:
        if pw:
            req = urllib.request.Request(f"{base}/login",
                                         data=json.dumps({"password": pw}).encode(),
                                         headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=5) as r:
                token = json.loads(r.read().decode()).get("session_token", "")
        with urllib.request.urlopen(f"{base}/state", timeout=5) as r:
            old = json.loads(r.read().decode()).get("pcs_output_folder", "")
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        body = json.dumps({"pcs_output_folder": folder,
                           "use_widget": False, "demo_mode": False}).encode()
        req = urllib.request.Request(f"{base}/state", data=body, headers=headers)
        with urllib.request.urlopen(req, timeout=5) as r:
            ok = json.loads(r.read().decode()).get("ok")
        if ok:
            print(f"  ✓  Server now reading from the simulator folder")
            if old and old != folder:
                print(f"     Restore afterwards: PCS Pro output folder = {old}")
            return True
    except Exception as e:
        print(f"  ✗  Could not configure the server ({e}) — set the PCS Pro output "
              f"folder to the path above manually in the control panel")
    return False


def main():
    ap = argparse.ArgumentParser(description="Rehearse the broadcast with a simulated match")
    ap.add_argument("--scenario", default="full", choices=sorted(MatchSimulator.SCENARIOS))
    ap.add_argument("--folder", default=os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "sim_pcs_output"))
    ap.add_argument("--ball-seconds", type=float, default=3.0,
                    help="pace: seconds between deliveries (default 3)")
    ap.add_argument("--overs", type=int, default=20, help="overs per innings (default 20)")
    ap.add_argument("--seed", type=int, default=None, help="reproducible match")
    ap.add_argument("--configure", action="store_true",
                    help="point the RUNNING server's PCS folder at the sim folder")
    ap.add_argument("--chaos", action="store_true",
                    help="inject realistic failures: mid-write empty files, feed stalls")
    ap.add_argument("--list", action="store_true", help="describe scenarios and exit")
    args = ap.parse_args()

    if args.list:
        for name, desc in MatchSimulator.SCENARIOS.items():
            print(f"  {name:<9} {desc}")
        return

    os.makedirs(args.folder, exist_ok=True)
    path = os.path.join(args.folder, OUTPUT_FILENAME)
    sim = MatchSimulator(args.scenario, max_overs=args.overs, seed=args.seed)

    print(f"\n  Match simulator — scenario: {args.scenario}, {args.overs} overs, "
          f"{args.ball_seconds:g}s/ball" + (f", seed {args.seed}" if args.seed is not None else ""))
    print(f"  Writing: {path}")
    print(f"  Teams:   {HOME_TEAM} v {AWAY_TEAM}")
    print("  Roster lines for the control panel (rehearses the brothers fix):")
    for num, name in HOME_XI[:2]:
        print(f"      {num} = {name}")
    if args.configure:
        configure_server(args.folder)
    else:
        print(f"  Control panel → PCS Pro output folder → {args.folder}")
    print("  Ctrl+C stops the simulation.\n")

    pace = {"prematch": 3.0, "prematch2": 3.0, "break": 5.0, "end": 3.0, "ball": 1.0}
    rng = random.Random(args.seed)
    last_line = ""
    try:
        for label, frame in sim.frames():
            if args.chaos and label == "ball" and rng.random() < 0.04:
                open(path, "w").close()                    # caught mid-write: empty file
                time.sleep(0.6)
            with open(path, "w", encoding="utf-8") as f:
                json.dump(frame, f)
            line = (f"  inn{frame['innings']} {frame['runs']}-{frame['wickets']} "
                    f"({frame['overs']} ov)  {frame['last_ball'] or '—'}")
            if line != last_line:
                print(line)
                last_line = line
            time.sleep(args.ball_seconds * pace.get(label, 1.0))
            if args.chaos and label == "ball" and rng.random() < 0.02:
                print("  …chaos: feed stalls for 8s (watch the health strip go amber)")
                time.sleep(8)
    except KeyboardInterrupt:
        print("\n  Simulation stopped. Remember to point the PCS Pro output folder back "
              "at the real scorer's folder before the next match.")


if __name__ == "__main__":
    main()
