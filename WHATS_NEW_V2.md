# What's New in Version 2.0

CricketStream Overlay v2 is a big step up from the first release. The scorebar and
core graphics are still there, but the broadcast now feels far closer to professional
cricket coverage — and there are new tools that help you *after* the final ball, too.

Here's everything that's new.

---

## On-screen during the match

### Player cards with photos and stats
When a new batter walks out, a card slides in showing their photo and season batting
stats (innings, average, high score). At the start of an innings, **both openers** get a
card — left and right of screen, just like broadcast cricket. The card clears itself the
moment the batter faces a ball, so it never lingers.

- Stats are aggregated live from PlayCricket for **both your club and the opposition**,
  so away batters get real numbers too — not just your own players.
- Photos live in a `headshots/` folder and are matched by surname (or by shirt number via
  the squad roster), with several filename patterns accepted so you don't have to be precise.
- If no photo is found, the card shows the player's initials instead — it never breaks.

### Squad roster — brothers and duplicate accounts, solved
NV Play sends surnames only, so two brothers both read as "Ewen", and a player with more
than one PlayCricket account can show up twice. The new **Squad Roster** in the control
panel maps shirt numbers to full names:

```
21 = Patrick Ewen
28 = Peter Ewen
```

The template now sends each batter's shirt number, so the overlay uses the number to pick
the right player — and therefore the right photo and the right stats. Where a player has
two PlayCricket accounts, their stats automatically use the most-played (regular) one.

### Worm chart
The run-rate panel is now a proper **worm** — each innings drawn as a cumulative
runs-by-over line in its own team colours, with red wicket markers and the running total
labelled at the head of each worm. It reads at a glance, exactly like TV.

### Full dismissal detail on the wicket card, spelled out
The fall-of-wicket card shows **how** the batter was out in full words — *Caught Jones
Bowled Smith*, *LBW Bowled Patel*, *Run Out*, *Stumped Wood Bowled Khan* — rather than the
scorer's shorthand. The detail fills in automatically a moment after the wicket, as soon
as the method of dismissal is recorded.

### Kit colours that follow the batting team
The scorebar now recolours so the batting side's kit colour is always on the batting side
of the bar, both innings — no more checking which team is which.

### AI over commentary
At the end of an over, an optional fourth panel shows a single sharp line of analysis
written live by Claude from the real match situation — the kind of observation a
commentator would make. Fast and costs a fraction of a penny per over.

### Drinks-break weather
At an over you choose (default 25), the weather widget appears automatically for the
drinks interval and clears on the next ball. A nice touch that fills the natural break.

### Smarter graphics timing
- The over summary is now automatically **suppressed when a wicket falls on the last
  ball**, so the wicket card and replay get the spotlight without a clashing graphic.
- Over runs are taken from the score itself, so a ball bowled in the same instant the
  over rolls over (a quick single or two to finish the over) is never dropped from the
  end-of-over count.
- The whole overlay is more robust: each part of the update cycle is isolated, so a
  hiccup in one graphic can no longer knock out the others mid-match.

### Automatic club badges, with a manual fallback
Badges are matched to teams by PlayCricket club ID, and the opposition's ID and short
abbreviation are now detected automatically from the day's fixture — name their badge
`<club-id>.png` and it just appears. If anything doesn't match, you can pick either
team's badge from a dropdown in the control panel.

---

## After the match

### AI match report
One click generates a full written match report from the ball-by-ball log of the game —
the result, key partnerships, standout performances and turning points — in seconds.
Edit it in the control panel and it saves to a dated text file ready to publish.

### AI social media posts
Generates ready-to-paste posts summarising the match for your club's social channels,
and can bundle them together with photos from a folder for an easy match-day round-up.

---

## v2.1 — the broadcast intelligence update

- **Standalone result posts for any match** — a "Load results" picker in the social section
  pulls your recent PlayCricket results (home or away, streamed or not), works out the result
  and your top batter and bowler straight from the scorecard, and builds the graphic. Per-team
  photo subfolders are supported: drop photos in `socials/1st`, `socials/2nd`, `socials/3rd`
  and each team's posts use its own (falling back to the main socials folder otherwise).
- **One-click camera setup** — enter your camera's RTSP URL in the OBS section and "Add camera to OBS" creates the media source for you over the WebSocket connection, so non-technical operators don't have to add it by hand. Re-adding replaces the old source, and it auto-reconnects if the stream drops.
- **Ball-by-ball database** — every delivery is logged to a local SQLite file (`match_data.db`) as you stream, building your own season-long dataset. The current over is rewritten live so a scorer's edits are captured, and a "Reconcile" button pulls PlayCricket's final scorecard as the authoritative record. Export any match to CSV from the new Match data panel.
- **Match-day sponsors** — the sponsor strip now shows every logo in the `sponsors/` folder,
  scaled to share the width, so you can add as many as you like.
- **Auto-detected moments** on the end-of-over graphic: season-best scores ("EWEN 67* —
  his best score of the season!"), team milestones ("100 up in 14 overs"), with the same
  storylines fed to the AI commentator so it builds its sentence around them.
- **"At this stage"** — in the second innings, every over summary compares the chase to
  the first innings at the same point ("Heathcoat were 67-2 at this stage").
- **Full innings scorecard** at the innings break — all eleven batters with dismissals
  plus bowling figures, broadcast-card style. Also available on demand from the control
  panel ("Show scorecard"). Requires the v2.1 scoreboard template (see below).
- **Bowler spell tracker** — "This spell: 5-1-18-2" on the over summary once a bowler
  has bowled consecutive overs from the same end.
- **DLS par score** — when rain is forecast (worst-case precipitation probability over
  the next three hours, from the weather service), a small pill above the scorebar shows
  the Duckworth-Lewis par and whether the chasing side is ahead or behind. Standard
  Edition approximation, intended as a guide.
- **Broadcast animation polish** — graphics now enter with a spring, FOUR/SIX banners
  sweep across like a Sky strap, and milestone numbers count up.

**Template upgrade required for the scorecard:** copy the new `bbcc_scoreboard.template`
to the scorer machine, restart PCS Pro, and re-select the template in
Tools → Configuration → Scoreboard. (Same deployment as the shirt-number upgrade — if
you're doing that anyway, this comes free.)

## Under the hood

- Reworked event detection that correctly handles the tricky Saturday cases: boundaries
  and wickets on the last ball of an over, quick wickets, scorer corrections (a score
  going *down* no longer triggers a phantom graphic), retirements, and mid-match overlay
  refreshes.
- Over summaries now reset cleanly at the innings break, so they fire correctly from the
  first over of the second innings.
- A player-stats diagnostic — `…/player/stats?name=SURNAME&debug=1` — lists everyone
  sharing a surname, their innings counts, and which record will be used, so ambiguous
  names and duplicate accounts are easy to spot.
- A live health strip in the control panel (scorer feed freshness, season stats, photos,
  badges, AI key) plus a pre-flight checklist when quickstart launches — so problems
  surface in the warm-up, not at the first ball.
- A faithful test harness now simulates real match sequences so these cases stay fixed.
- Cleaner, more reliable server with all the AI features sharing a single Anthropic key.

---

## Upgrading from v1

1. Replace `server.py`, `overlay.html`, and `quickstart.py` with the v2 versions.
2. Copy the updated `bbcc_scoreboard.template` into NV Play's Templates folder and
   **restart NV Play** so it picks up the new dismissal-detail **and shirt-number** fields.
3. Create three new (optional) folders next to `server.py` if you want the new features:
   - `headshots/` — player photos for the player cards
   - `socials/` — match photos for social posts
   - (`logos/` for club badges is unchanged from v1)
4. Add your Anthropic API key in the control panel to enable commentary, reports, and posts.
5. *(Optional)* Fill in the **Squad Roster** in the control panel for any players who share
   a surname or have duplicate PlayCricket accounts. Make sure those players have shirt
   numbers assigned in the scorer's NV Play / PCS Pro squad.

Your existing `config.ini` and club settings carry over unchanged.

---

## What you need for the AI features

A single Anthropic API key (from console.anthropic.com) powers the over commentary,
match report, and social posts. Costs are tiny — a few pence for a whole match. Everything
else works without it; the AI features simply stay switched off until a key is entered.
