"""
CricketStream Overlay — innings scoring engine
──────────────────────────────────────────────
The deterministic scorer's-book core shared by two frontends:

  • simulate_match.py — feeds it RANDOMLY SAMPLED outcomes (broadcast rehearsal)
  • the manual scoring page (/scoring in server.py) — feeds it OPERATOR BUTTON PRESSES,
    so a club with no NV Play/PCS Pro can drive the full overlay from a phone

Everything downstream (the overlay, ball-by-ball DB, highlights tagging, graphics) speaks
the NV Play frame dialect, so frame() renders the exact field set scoreboard.template
produces. apply_outcome() is pure bookkeeping — no randomness — which is what makes the
manual page's undo trivially correct: replay the event list minus the last entry and the
book must come out identical (tests assert this).

Outcome kinds and their extra parameters:
    "dot" "1" "2" "3" "4" "6"          — off the bat (no params)
    "W"                                 — wicket; wicket_type, fielder, out_non_striker
    "wide"                              — runs = runs RAN/boundary beyond the wide itself
    "noball"                            — runs = runs off the bat (counts to the batter)
    "bye" / "legbye"                    — runs = byes taken (1/2/4)
"""

WICKET_TYPES = ("bowled", "caught", "lbw", "run out", "stumped")
RUN_KINDS = ("dot", "1", "2", "3", "4", "6")
EXTRA_KINDS = ("wide", "noball", "bye", "legbye")


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


def format_dismissal(wicket_type, bowler_name, fielder=""):
    """PCS-style abbreviated howout string (what LastWicketHowOut carries)."""
    b_sur = surname(bowler_name) if bowler_name else ""
    f_sur = surname(fielder) if fielder else ""
    if wicket_type == "caught":
        return (f"C {f_sur} B {b_sur}" if f_sur else f"C B {b_sur}"), True
    if wicket_type == "bowled":
        return f"B {b_sur}", True
    if wicket_type == "lbw":
        return f"LBW B {b_sur}", True
    if wicket_type == "stumped":
        return (f"ST {f_sur} B {b_sur}" if f_sur else f"ST B {b_sur}"), True
    # run out — never credited to the bowler
    return (f"RUN OUT ({f_sur})" if f_sur else "RUN OUT"), False


class InningsEngine:
    """One innings, one delivery at a time via apply_outcome(). frame() renders the
    current state as the exact dict NV Play's scoreboard.template produces (all strings)."""

    def __init__(self, batting_xi, bowling_xi, batting_name, bowling_name,
                 max_overs, target=None, openers_selected=False):
        self.batting_name, self.bowling_name = batting_name, bowling_name
        self.max_overs, self.target = max_overs, target
        self.batters = [Batter(n, nm) for n, nm in batting_xi]
        self.fielders = [nm for _, nm in bowling_xi]
        # Default bowling attack: the last five in the order, two ends alternating,
        # 4-over spells. Manual scoring overrides per over via set_bowler().
        self.attack = [Bowler(n, nm) for n, nm in bowling_xi[6:]]
        self.extra_bowlers = []        # bowlers named via set_bowler() outside the default 5
        self.bowler_schedule = {}      # over_no -> Bowler (manual override)
        self.striker, self.non_striker = self.batters[0], self.batters[1]
        self.next_bat = 2
        self.total = self.wkts = self.legal_balls = 0
        self.extras = 0
        self.over_tokens = []          # this over's ticker tokens
        self.over_runs = 0             # team runs this over
        self._over_bowlers = set()     # who has bowled in the current over (maiden rules)
        self.over_history = []         # runs per completed over
        self.token_history = []        # (tokens, runs) per completed over — for tests
        self.p_runs = self.p_balls = 0
        self.last_wicket = {"howout": "", "batter": "", "bowler": "", "fielder": "", "type": ""}
        self.force_complete = False
        # Pre-match: batter names render blank until this flips (the overlay's
        # "hasn't really started" signal). Manual scoring passes True — the operator has
        # already picked the XI, so the scorebar should show at 0-0.
        self.openers_selected = openers_selected

    # ── Bowlers ───────────────────────────────────────────────
    def all_bowlers(self):
        return self.attack + self.extra_bowlers

    def get_bowler(self, name):
        for w in self.all_bowlers():
            if w.name == name:
                return w
        w = Bowler("", name)
        self.extra_bowlers.append(w)
        return w

    def bowler_for_over(self, over_no):
        if over_no in self.bowler_schedule:
            return self.bowler_schedule[over_no]
        end = over_no % 2
        cycle = self.attack[end::2] or self.attack or [self.get_bowler(self.fielders[-1])]
        return cycle[((over_no // 2) // 4) % len(cycle)]

    def set_bowler(self, name, over_no=None):
        """Pin who bowls the given over (default: the upcoming/current one)."""
        self.bowler_schedule[self.current_over_no if over_no is None else over_no] = \
            self.get_bowler(name)

    # ── State queries ─────────────────────────────────────────
    @property
    def current_over_no(self):
        return self.legal_balls // 6

    @property
    def complete(self):
        return (self.force_complete or self.wkts >= 10
                or self.legal_balls >= self.max_overs * 6
                or (self.target is not None and self.total >= self.target))

    @property
    def awaiting_new_over(self):
        """True right after an over completes: the next over hasn't started and the
        manual UI should offer a bowler pick (unless one is already scheduled)."""
        return (self.legal_balls > 0 and self.legal_balls % 6 == 0 and not self.complete
                and self.current_over_no not in self.bowler_schedule
                and not self.over_tokens)

    # ── The book ──────────────────────────────────────────────
    def _dismiss(self, bowler, wicket_type, fielder, out_non_striker):
        if wicket_type not in WICKET_TYPES:
            raise ValueError(f"unknown wicket type {wicket_type!r}")
        out = self.non_striker if out_non_striker else self.striker
        howout, credit = format_dismissal(wicket_type, bowler.name, fielder)
        out.how_out = howout
        self.wkts += 1
        if credit:
            bowler.wkts += 1
        self.last_wicket = {"howout": howout,
                            "batter": f"{surname(out.name)} {out.runs}",
                            "bowler": bowler.name if credit else "",
                            "fielder": fielder, "type": wicket_type.upper()}
        self.p_runs = self.p_balls = 0
        if self.next_bat < len(self.batters):
            replacement = self.batters[self.next_bat]
            self.next_bat += 1
            if out_non_striker:
                self.non_striker = replacement
            else:
                self.striker = replacement   # new batter takes strike

    def choose_next_batter(self, name):
        """Swap the auto-arrived new batter (who must not have faced a ball yet) for a
        different not-yet-batted player — real batting orders change on the day."""
        arrived = None
        for b in (self.striker, self.non_striker):
            if not b.how_out and b.balls == 0 and b.runs == 0 \
                    and self.batters.index(b) == self.next_bat - 1:
                arrived = b
        if arrived is None:
            raise ValueError("no just-arrived batter to swap — they've already faced a ball")
        chosen = next((b for b in self.batters[self.next_bat:]
                       if b.name == name and not b.how_out), None)
        if chosen is None:
            raise ValueError(f"{name!r} is not a yet-to-bat player")
        i, j = self.batters.index(arrived), self.batters.index(chosen)
        self.batters[i], self.batters[j] = self.batters[j], self.batters[i]
        if self.striker is arrived:
            self.striker = chosen
        else:
            self.non_striker = chosen

    def swap_strike(self):
        """Manual correction — the book can't always tell who crossed."""
        self.striker, self.non_striker = self.non_striker, self.striker

    def end(self):
        """Declare the innings over (rain, declaration, operator's call)."""
        self.force_complete = True

    def apply_outcome(self, kind, runs=0, wicket_type="bowled", fielder="",
                      out_non_striker=False):
        """Apply one delivery to the book. Returns the ticker token. Deterministic —
        the manual page's undo/replay and the save-file reload both depend on that."""
        if self.complete:
            raise ValueError("innings is already complete")
        if kind not in RUN_KINDS + EXTRA_KINDS + ("W",):
            raise ValueError(f"unknown outcome kind {kind!r}")
        runs = int(runs or 0)
        if not (0 <= runs <= 6):
            raise ValueError(f"runs out of range: {runs}")
        self.openers_selected = True
        bowler = self.bowler_for_over(self.current_over_no)
        self._over_bowlers.add(bowler)     # a mid-over change (injury) means >1 per over
        token, team_runs, ran = "", 0, 0

        if kind == "W":
            self.striker.balls += 1
            bowler.legal += 1
            self._dismiss(bowler, wicket_type, fielder, out_non_striker)
            token = "W"
        elif kind == "wide":
            ran = runs
            team_runs = 1 + ran
            self.extras += team_runs
            bowler.runs += team_runs
            bowler._over_conceded += team_runs
            token = f"w+{ran}" if ran else "w"
        elif kind == "noball":
            bat = runs
            team_runs = 1 + bat
            self.extras += 1
            self.striker.runs += bat
            self.striker.balls += 1
            if bat == 4:
                self.striker.fours += 1
            if bat == 6:
                self.striker.sixes += 1
            bowler.runs += team_runs
            bowler._over_conceded += team_runs
            ran = bat if bat not in (4, 6) else 0
            token = f"{bat}nb" if bat else "nb"
        elif kind in ("bye", "legbye"):
            ran = runs or 1
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
            # A maiden needs the WHOLE over from one bowler with nothing conceded —
            # an over shared after a mid-over change is nobody's maiden (ACS convention).
            if bowler._over_conceded == 0 and len(self._over_bowlers) == 1:
                bowler.maidens += 1
            # Reset every bowler who bowled in the over, not just the finisher — a
            # replaced bowler's stale count would suppress a genuine maiden later.
            for w in self._over_bowlers:
                w._over_conceded = 0
            self._over_bowlers.clear()
            self.over_history.append(self.over_runs)
            self.token_history.append((list(self.over_tokens), self.over_runs))
            self.over_tokens, self.over_runs = [], 0
            self.striker, self.non_striker = self.non_striker, self.striker
        return token

    # ── Frame rendering (NV Play scoreboard.template dialect) ─
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
        bowled = [w for w in self.all_bowlers() if w.legal or w.runs]
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
