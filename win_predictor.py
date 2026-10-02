"""Live win probability for limited-overs club cricket. Pure stdlib.

Two ingredients:
- How much batting is left: a DLS-style "resources" curve — the fraction of a full
  innings' runs a side can still expect, from the balls left and the wickets in hand. Same
  shape as Duckworth-Lewis (Z0·F(w)·(1 - e^(-b·u/F(w)))): late overs are worth more when
  wickets are in hand, and lost wickets cap what's left however many overs remain.
- How this league scores: the average and spread of first-innings totals from this
  season's real scorecards (the season-stats download), as runs per over so different
  formats (40, 45, 50 overs…) pool together.

First innings: P(batting side wins) = P(the chase falls short of the projected total).
Second innings: P(chasing side gets the runs still needed from the resources left).
Both treat innings scores as normally distributed — a simple, honest model; backtest()
reports how it does on the league's own results.
"""
import math

DEFAULT_RPO = 5.0              # used until there's enough league history
DEFAULT_SD_FRACTION = 0.24     # spread of totals as a fraction of the mean
MIN_MATCHES = 6
CURVE = 1.6                    # how strongly scoring accelerates late in an innings
WICKET_POWER = 1.3             # F(w) = (wickets in hand / 10) ** 1.3


def _phi(x):
    return 0.5 * (1 + math.erf(x / math.sqrt(2)))


def resources(balls_left, wickets_lost, total_balls):
    """Fraction (0..1) of a full innings' expected runs still to come."""
    if total_balls <= 0 or balls_left <= 0 or wickets_lost >= 10:
        return 0.0
    x = min(balls_left / total_balls, 1.0)
    f = ((10 - max(wickets_lost, 0)) / 10) ** WICKET_POWER
    return f * (1 - math.exp(-CURVE * x / f)) / (1 - math.exp(-CURVE))


def league_model(history):
    """{rpo, sd_rpo, matches} from [{"overs_limit", "i1_runs", ...}] — first-innings totals
    as runs per over of the limit. Falls back to club-cricket defaults with little data."""
    rates = [m["i1_runs"] / m["overs_limit"] for m in history or []
             if m.get("overs_limit") and m.get("i1_runs") is not None and m["i1_runs"] > 0]
    if len(rates) < MIN_MATCHES:
        return {"rpo": DEFAULT_RPO, "sd_rpo": DEFAULT_RPO * DEFAULT_SD_FRACTION,
                "matches": len(rates), "default": True}
    mean = sum(rates) / len(rates)
    sd = math.sqrt(sum((r - mean) ** 2 for r in rates) / (len(rates) - 1))
    return {"rpo": mean, "sd_rpo": max(sd, mean * 0.08), "matches": len(rates), "default": False}


def _balls(overs):
    """'22.3' or 22.3 -> 135 balls (the digit after the point is balls)."""
    try:
        whole, _, part = f"{float(overs):.1f}".partition(".")
        return int(whole) * 6 + int(part or 0)
    except (TypeError, ValueError):
        return 0


def predict(innings, score, wickets, overs, overs_limit, model, target=0):
    """{"batting": %, "bowling": %, ...} for the side batting now. None if it can't be
    worked out (no overs limit). target = runs needed to win (innings 2 only)."""
    if not overs_limit:
        return None
    total_balls = int(overs_limit) * 6
    bowled = _balls(overs)
    balls_left = max(total_balls - bowled, 0)
    mean = model["rpo"] * overs_limit
    sd = model["sd_rpo"] * overs_limit
    r = resources(balls_left, wickets, total_balls)
    if innings >= 2 and target:
        need = target - score
        if need <= 0:
            p = 1.0
        elif balls_left == 0 or wickets >= 10:
            p = 0.0
        else:
            mu = mean * r
            sigma = sd * math.sqrt(max(r, 1e-6)) + 2.0
            p = 1 - _phi((need - 0.5 - mu) / sigma)
        out = {"need": max(target - score, 0), "balls_left": balls_left}
    else:
        projected = score + mean * r
        sigma1 = sd * math.sqrt(max(r, 0.0))
        p = _phi((projected - mean) / math.sqrt(sd ** 2 + sigma1 ** 2))
        out = {"projected": int(round(projected))}
    pct = round(p * 100)
    if 0 < p < 1:
        pct = min(max(pct, 1), 99)          # never "100%" while it can still turn
    out.update(batting=pct, bowling=100 - pct, model_matches=model["matches"],
               model_default=model.get("default", False))
    return out


def backtest(history, model=None):
    """How the second-innings prediction would have done on the league's own results:
    the chase's win chance at the start of each chase, against what happened. Returns
    {"chases", "favourite_won_pct", "brier"} (Brier: 0 perfect, 0.25 = coin toss)."""
    model = model or league_model(history)
    n = right = 0
    brier = 0.0
    for m in history or []:
        if m.get("chase_won") is None or not m.get("overs_limit") or not m.get("i1_runs"):
            continue
        p = predict(2, 0, 0, 0, m["overs_limit"], model, target=m["i1_runs"] + 1)["batting"] / 100
        won = 1.0 if m["chase_won"] else 0.0
        brier += (p - won) ** 2
        n += 1
        right += 1 if (p >= 0.5) == bool(won) else 0
    if not n:
        return {"chases": 0, "favourite_won_pct": None, "brier": None}
    return {"chases": n, "favourite_won_pct": round(100 * right / n), "brier": round(brier / n, 3)}


def innings_summary(match_detail):
    """One season scorecard (PlayCricket match_detail) -> the facts the model needs, or None
    for anything that isn't a normal completed limited-overs game (pairs/softball, no
    result, missing innings)."""
    md = (match_detail.get("match_details") or [{}])[0]
    if "pairs" in str(md.get("game_type", "")).lower():
        return None
    try:
        limit = int(str(md.get("no_of_overs") or "").strip())
    except ValueError:
        return None
    inns = md.get("innings") or []
    if limit <= 0 or len(inns) < 2:
        return None

    def num(v):
        try:
            return int(str(v).strip())
        except (TypeError, ValueError):
            return None
    i1, i2 = inns[0], inns[1]
    r1, r2 = num(i1.get("runs")), num(i2.get("runs"))
    if r1 is None or r2 is None:
        return None
    res = str(md.get("result") or "").upper()
    applied = str(md.get("result_applied_to") or "")
    chase_won = None
    if res in ("W", "L") and applied:
        chaser = str(i2.get("team_batting_id") or "")
        chase_won = (applied == chaser) == (res == "W")
    elif res == "T":
        chase_won = False
    return {"overs_limit": limit, "i1_runs": r1, "i1_wkts": num(i1.get("wickets")),
            "i2_runs": r2, "i2_wkts": num(i2.get("wickets")), "chase_won": chase_won}
