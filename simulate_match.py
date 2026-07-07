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

WICKET_TYPES = [  # (weight, type) — howout strings formatted like PCS Pro's
    (45, "caught"), (30, "bowled"), (12, "lbw"), (8, "run out"), (5, "stumped")]


def surname(full):
    return full.split()[-1].upper()


class Batter:
    def __init__(self, number, name):
        self.number, self.name = number, name
        self.runs = self.balls = self.fours = self.sixes = 0
        self.how_out = ""          # "" = not out / yet to bat

    @property
    def sr(self):
        return round(self.runs / self.balls * 100, 1) if self.balls else 0.0


class Bowler:
    def __init__(self, number, name):
        self.number, self.name = number, name
        self.legal = self.runs = self.wkts = self.maidens = 0
        self._over_conceded = 0    # runs off this bowler in the current over

    @property
    def overs_str(self):
        return f"{self.legal // 6}.{self.legal % 6}" if self.legal % 6 else str(self.legal // 6)


class InningsSim:
    """One innings, ball by ball. frame() renders the current state as the exact dict
    NV Play's scoreboard.template produces (all values strings)."""

    def __init__(self, batting_xi, bowling_xi, batting_name, bowling_name,
                 max_overs, rng, target=None, wicket_boost=1.0, run_boost=1.0):
        self.rng = rng
        self.batting_name, self.bowling_name = batting_name, bowling_name
        self.max_overs, self.target = max_overs, target
        self.batters = [Batter(n, nm) for n, nm in batting_xi]
        self.fielders = [nm for _, nm in bowling_xi]
        # Bowling attack: the last five in the order, two ends alternating, 4-over spells
        self.attack = [Bowler(n, nm) for n, nm in bowling_xi[6:]]
        self.striker, self.non_striker = self.batters[0], self.batters[1]
        self.next_bat = 2
        self.total = self.wkts = self.legal_balls = 0
        self.extras = 0
        self.over_tokens = []          # this over's ticker tokens
        self.over_runs = 0             # team runs this over
        self.over_history = []         # runs per completed over
        self.token_history = []        # (tokens, runs) per completed over — for tests
        self.p_runs = self.p_balls = 0
        self.last_wicket = {"howout": "", "batter": "", "bowler": "", "fielder": "", "type": ""}
        self.wicket_boost, self.run_boost = wicket_boost, run_boost
        self.openers_selected = False  # pre-match: names blank until this flips

    # ── Engine ────────────────────────────────────────────────
    def bowler_for_over(self, over_no):
        end = over_no % 2
        cycle = self.attack[end::2] or self.attack
        return cycle[((over_no // 2) // 4) % len(cycle)]

    @property
    def current_over_no(self):
        return self.legal_balls // 6

    @property
    def complete(self):
        return (self.wkts >= 10 or self.legal_balls >= self.max_overs * 6
                or (self.target is not None and self.total >= self.target))

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

    def _dismiss(self, bowler):
        pick = self.rng.uniform(0, sum(w for w, _ in WICKET_TYPES))
        for w, kind in WICKET_TYPES:
            pick -= w
            if pick <= 0:
                break
        out = self.striker
        b_sur = surname(bowler.name)
        fielder = self.rng.choice([f for f in self.fielders if f != bowler.name])
        f_sur = surname(fielder)
        if kind == "caught":
            howout, credit = f"C {f_sur} B {b_sur}", True
        elif kind == "bowled":
            howout, credit, fielder = f"B {b_sur}", True, ""
        elif kind == "lbw":
            howout, credit, fielder = f"LBW B {b_sur}", True, ""
        elif kind == "stumped":
            howout, credit = f"ST {f_sur} B {b_sur}", True
        else:  # run out — not credited to the bowler
            howout, credit = f"RUN OUT ({f_sur})", False
        out.how_out = howout
        self.wkts += 1
        if credit:
            bowler.wkts += 1
        self.last_wicket = {"howout": howout,
                            "batter": f"{surname(out.name)} {out.runs}",
                            "bowler": bowler.name if credit else "",
                            "fielder": fielder, "type": kind.upper()}
        self.p_runs = self.p_balls = 0
        if self.next_bat < 11:
            self.striker = self.batters[self.next_bat]   # new batter takes strike
            self.next_bat += 1

    def ball(self):
        """Bowl one delivery; mutates state. Returns the ticker token (or '' if the
        innings was already complete)."""
        if self.complete:
            return ""
        self.openers_selected = True
        bowler = self.bowler_for_over(self.current_over_no)
        kind = self._sample_outcome()
        token, team_runs, ran = "", 0, 0

        if kind == "W":
            self.striker.balls += 1
            bowler.legal += 1
            self._dismiss(bowler)
            token = "W"
        elif kind == "wide":
            ran = self.rng.choice([0, 0, 0, 1, 4])          # occasional w+1 / w+4
            team_runs = 1 + ran
            self.extras += team_runs
            bowler.runs += team_runs
            bowler._over_conceded += team_runs
            token = f"w+{ran}" if ran else "w"
        elif kind == "noball":
            bat = self.rng.choice([0, 0, 1, 2, 4])
            team_runs = 1 + bat
            self.extras += 1
            self.striker.runs += bat
            self.striker.balls += 1
            if bat == 4:
                self.striker.fours += 1
            bowler.runs += team_runs
            bowler._over_conceded += team_runs
            ran = bat if bat not in (4, 6) else 0
            token = f"{bat}nb" if bat else "nb"
        elif kind in ("bye", "legbye"):
            ran = self.rng.choice([1, 1, 2, 4])
            team_runs = ran
            self.extras += ran
            self.striker.balls += 1
            bowler.legal += 1
            token = f"{ran}{'lb' if kind == 'legbye' else 'b'}"
            if ran == 4:
                ran = 0                                     # boundary byes: no crossing
        elif kind == "dot":
            self.striker.balls += 1
            bowler.legal += 1
            token = "."
        else:                                               # runs off the bat
            n = int(kind)
            team_runs = n
            self.striker.runs += n
            self.striker.balls += 1
            if n == 4:
                self.striker.fours += 1
            if n == 6:
                self.striker.sixes += 1
            bowler.legal += 1
            bowler.runs += n
            bowler._over_conceded += n
            ran = n if n not in (4, 6) else 0
            token = str(n)

        if kind not in ("wide", "noball"):
            self.legal_balls += 1
            if kind != "W":            # _dismiss just reset the NEW pair's partnership
                self.p_balls += 1
        self.total += team_runs
        self.p_runs += team_runs
        self.over_runs += team_runs
        self.over_tokens.append(token)

        if ran % 2 == 1:
            self.striker, self.non_striker = self.non_striker, self.striker

        # Over completed? Roll everything over — the FRAME after this ball must already
        # show the cleared ticker (NV Play semantics; the overlay handles the missing
        # final ball via the score delta).
        if kind not in ("wide", "noball") and self.legal_balls % 6 == 0:
            if bowler._over_conceded == 0:
                bowler.maidens += 1
            bowler._over_conceded = 0
            self.over_history.append(self.over_runs)
            self.token_history.append((list(self.over_tokens), self.over_runs))
            self.over_tokens, self.over_runs = [], 0
            self.striker, self.non_striker = self.non_striker, self.striker
        return token

    # ── Frame rendering ───────────────────────────────────────
    def overs_str(self):
        return f"{self.legal_balls // 6}.{self.legal_balls % 6}"

    def frame(self, innings_no):
        ov_float = self.legal_balls / 6
        rr = round(self.total / ov_float, 2) if ov_float else 0.0
        pre = not self.openers_selected
        b1, b2 = self.striker, self.non_striker
        bowler = self.bowler_for_over(self.current_over_no) if not pre else None
        f = {
            "batting_team": self.batting_name, "bowling_team": self.bowling_name,
            "innings": str(innings_no),
            "runs": str(self.total), "wickets": str(self.wkts), "overs": self.overs_str(),
            "partnership_runs": str(self.p_runs), "partnership_balls": str(self.p_balls),
            "last_ball": " ".join(self.over_tokens),
            "last_over_runs": str(self.over_history[-1] if self.over_history else 0),
            "run_rate": f"{rr:.2f}" if ov_float else "0.00",
            # NB: no "over_history" key — scoreboard.template doesn't map one, so the
            # real feed never carries it; the server tolerates its absence.
            "last_wicket_howout": self.last_wicket["howout"],
            "last_wicket_batter": self.last_wicket["batter"],
            "last_wicket_bowler": self.last_wicket["bowler"],
            "last_wicket_fielder": self.last_wicket["fielder"],
            "last_wicket_type": self.last_wicket["type"],
            "max_overs": str(self.max_overs),
        }
        for tag, b, strike in (("batter1", b1, True), ("batter2", b2, False)):
            f[f"{tag}_name"] = "" if pre else b.name
            f[f"{tag}_number"] = "" if pre else b.number
            f[f"{tag}_runs"] = "0" if pre else str(b.runs)
            f[f"{tag}_balls"] = "0" if pre else str(b.balls)
            f[f"{tag}_strike"] = "" if pre else ("True" if strike else "False")
            f[f"{tag}_fours"] = "0" if pre else str(b.fours)
            f[f"{tag}_sixes"] = "0" if pre else str(b.sixes)
            f[f"{tag}_sr"] = "0" if pre else str(b.sr)
        f["bowler_name"] = "" if pre else bowler.name
        f["bowler_overs"] = "0" if pre else bowler.overs_str
        f["bowler_runs"] = "0" if pre else str(bowler.runs)
        f["bowler_wickets"] = "0" if pre else str(bowler.wkts)
        f["bowler_maidens"] = "0" if pre else str(bowler.maidens)
        if self.target is not None:
            need = max(0, self.target - self.total)
            balls_left = max(0, self.max_overs * 6 - self.legal_balls)
            f["target"] = str(self.target)
            f["runs_required"] = str(need)
            f["balls_remaining"] = str(balls_left)
            f["req_rate"] = f"{(need / balls_left * 6):.2f}" if balls_left else "0.00"
        else:
            f["target"] = f["runs_required"] = f["balls_remaining"] = ""
            f["req_rate"] = ""
        # Full-card fields — every batter who has batted (or is in), bowlers with figures
        batted = [b for b in self.batters[:max(self.next_bat, 2)]]
        for i in range(1, 12):
            if not pre and i <= len(batted):
                b = batted[i - 1]
                at_crease = b in (self.striker, self.non_striker) and not b.how_out
                f[f"card_b{i}_name"] = b.name
                f[f"card_b{i}_runs"] = str(b.runs)
                f[f"card_b{i}_balls"] = str(b.balls)
                f[f"card_b{i}_out"] = "" if at_crease or not b.how_out else b.how_out
            else:
                f[f"card_b{i}_name"] = f[f"card_b{i}_out"] = ""
                f[f"card_b{i}_runs"] = f[f"card_b{i}_balls"] = ""
        bowled = [w for w in self.attack if w.legal or w.runs]
        for i in range(1, 12):
            if not pre and i <= len(bowled):
                w = bowled[i - 1]
                f[f"card_w{i}_name"] = w.name
                f[f"card_w{i}_o"] = w.overs_str
                f[f"card_w{i}_m"] = str(w.maidens)
                f[f"card_w{i}_r"] = str(w.runs)
                f[f"card_w{i}_w"] = str(w.wkts)
            else:
                f[f"card_w{i}_name"] = ""
                f[f"card_w{i}_o"] = f[f"card_w{i}_m"] = f[f"card_w{i}_r"] = f[f"card_w{i}_w"] = ""
        return f


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
        inn = InningsSim(bat_xi, bowl_xi, bat_nm, bowl_nm, max_overs or self.max_overs,
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
