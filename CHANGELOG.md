# Changelog

All notable changes to CricketStream Overlay are documented here, most recent first.

---

## v2.2.2 — 2026-07-01

Docs-only release — no code changes. Consolidated `RELEASE_NOTES_v2.1.md` and
`WHATS_NEW_V2.md` into this single `CHANGELOG.md`, documented Intel Mac support in the
setup guides, and updated the stale version badge.

## v2.2.1 — 2026-07-01

- **Fixed the macOS setup wizard for Intel Macs.** The download was accidentally
  Apple-Silicon-only (`arm64`), which crashed immediately with "bad CPU type in executable"
  on Intel hardware — no Rosetta-equivalent runs `arm64` code the other way round. It's now
  built as a proper `universal2` binary, so one download works on both Apple Silicon and
  Intel Macs.
- **Redacted the camera RTSP URL from `GET /state`.** Most IP cameras embed credentials
  directly in the URL (`rtsp://user:pass@host/stream`); this was being sent in plaintext to
  anything that could reach the control panel, including over plain HTTP Wi-Fi when using
  the phone/tablet control panel. It now redacts the same way the OBS password and API keys
  already did.

## v2.2 — 2026-07-01

- **Standalone setup wizard.** `CricketStreamSetup.exe` (Windows) and
  `CricketStreamSetup-mac.zip` (Mac) let a new club get started without installing Python
  first — the wizard installs Python itself if it's missing (via `winget` on Windows, the
  official installer plus the SSL-certificate fix on Mac), then walks through the same club
  setup as running `setup_wizard.py` from source.
- **Fixed a blank pre-game player card.** NV Play renders the scoreboard template as soon as
  a match starts, so batter names come through as genuinely empty (not missing) until the
  scorer actually selects the openers — the overlay used to treat that as a real new batter
  and show a card with nothing on it.
- **New pre-game "season form" panel** fills that same waiting period with something useful:
  each team's own top run-scorer and leading wicket-taker this season, styled like the
  player card (photo + stat row), cycling alongside the existing competition/umpires panel.
- **Made the project genuinely club-agnostic.** What had been a single club's private tool
  had BBCC's own identity (club name, PlayCricket ID, home ground, AI prompt text, error
  messages, even the control panel's page title) hardcoded as fallback defaults throughout —
  invisible to BBCC's own use since the "wrong" default happened to be their own real data,
  but broken for any other club. Renamed `bbcc_scoreboard.template` to `scoreboard.template`
  and rebranded "BBCC Stream Overlay" to "CricketStream Overlay" throughout.

## v2.1 — Broadcast intelligence & your own data

This release builds on the v2.0 broadcast layer with match intelligence, a dataset of your
own, and easier setup for non-technical operators.

**Highlights**
- **Ball-by-ball database.** Every delivery is logged to a local SQLite file (`match_data.db`)
  as you stream — your own season-long dataset. The current over is rewritten live so scorer
  edits and deletions are captured; completed overs freeze. A **Reconcile** button pulls
  PlayCricket's published scorecard as the authoritative record, and any match can be
  **exported to CSV**.
- **Result posts for any match.** A "Load results" picker pulls your recent PlayCricket
  results — home or away, streamed or not — and builds a polished Instagram result graphic,
  working out the result and your top batter and bowler straight from the scorecard. Per-team
  photo subfolders are supported (`socials/1st`, `socials/2nd`, `socials/3rd`), falling back
  to the main folder; all age-group sides route to `socials/youth`, using club stock photos
  and a discreet first-name + initial for player names — a safeguarding-conscious default
  for juniors.
- **Match-day sponsors.** Every logo in `sponsors/` now appears on result posts, scaled to
  share the width — add a one-off sponsor by dropping in a file.
- **One-click camera.** Enter your camera's RTSP URL in the control panel and "Add camera to
  OBS" creates the media source for you over the WebSocket connection, with auto-reconnect
  if the feed drops.

**Broadcast graphics**
- **Auto-detected moments** — season-best scores and team milestones fire automatically on
  the over summary and are woven into the AI commentary.
- **"At this stage"** — in the second innings, every over summary compares the chase to the
  first innings at the same point.
- **Full innings scorecard** at the break — all eleven batters with dismissals plus bowling
  figures, broadcast-card style. Requires the v2.1 `scoreboard.template`.
- **Bowler spell tracker** — "This spell: 5-1-18-2" once a bowler has bowled consecutive
  overs from the same end.
- **DLS par pill** above the scorebar when rain is forecast (Standard Edition
  approximation, intended as a guide).
- Broadcast animation polish: spring panel entries, sweeping boundary banners, milestone
  count-ups.

**Reliability & security**
- Threaded server so a slow AI or PlayCricket call never freezes the overlay.
- Atomic state writes with a last-good fallback; resilient PCS file reads.
- Secrets are redacted from browser-facing responses; scorer-controlled names are escaped.
- A control-panel health strip and a quickstart pre-flight self-test.
- Reworked event detection for the tricky Saturday cases: boundaries and wickets on the last
  ball of an over, quick wickets, scorer corrections, retirements, mid-match overlay refreshes.
- A player-stats diagnostic (`/player/stats?name=SURNAME&debug=1`) lists everyone sharing a
  surname and which record will be used, so ambiguous names are easy to spot.

**Control panel improvements (post-release patches)**
- **Responsive control panel.** The panel stacks into a single column on narrow screens
  (≤ 768px) with larger, touch-friendly buttons, so a phone operator on Wi-Fi can run the
  stream without pinching and zooming.
- **`config.ini` auto-seeding.** `server.py` reads all sections of `config.ini` on startup
  and pre-populates any `match_state.json` fields still at their defaults — API keys, club
  name, kit colour, folder paths, and YouTube title template all load automatically, no
  need to re-enter them after a fresh install.

**Upgrading from v2.0**
1. Replace `server.py` and `overlay.html`.
2. Deploy the v2.1 `scoreboard.template` to the scorer's NV Play / PCS Pro machine —
   required for the full innings scorecard and shirt-number features. Restart PCS Pro and
   re-select the scoreboard in Tools → Configuration.
3. Install Pillow if you haven't: `pip install Pillow` (used for social-post images).
4. Optional: create `socials/1st`, `socials/2nd`, `socials/3rd`, `socials/youth` and the
   `sponsors/` folder; set your camera's RTSP URL in the control panel.

## v2.0 — Player cards, squad rosters, and AI features

A big step up from v1: the scorebar and core graphics are still there, but the broadcast
feels far closer to professional cricket coverage, plus new tools for after the final ball.

**On-screen during the match**
- **Player cards with photos and stats.** When a new batter walks out, a card slides in
  showing their photo and season batting stats (innings, average, high score). At the start
  of an innings, both openers get a card — left and right of screen. Stats are aggregated
  live from PlayCricket for both your club and the opposition. Photos live in a `headshots/`
  folder, matched by surname or shirt number, with several filename patterns accepted; if no
  photo is found the card shows initials instead — it never breaks.
- **Squad roster.** NV Play sends surnames only, so two brothers both read as "Smith", and a
  player with more than one PlayCricket account can show up twice. The Squad Roster in the
  control panel maps shirt numbers to full names, so the overlay picks the right player —
  and therefore the right photo and stats.
- **Worm chart.** The run-rate panel is a proper worm — each innings drawn as a cumulative
  runs-by-over line in its own team colours, with red wicket markers and the running total
  labelled at the head of each worm.
- **Full dismissal detail on the wicket card**, spelled out in full words — *Caught Jones
  Bowled Smith*, *LBW Bowled Patel*, *Run Out*, *Stumped Wood Bowled Khan* — rather than the
  scorer's shorthand.
- **Kit colours that follow the batting team**, both innings.
- **AI over commentary.** An optional fourth panel shows a single line of analysis written
  live by Claude from the real match situation, at a fraction of a penny per over.
- **Drinks-break weather.** At an over you choose (default 25), the weather widget appears
  automatically for the interval and clears on the next ball.
- **Smarter graphics timing.** The over summary is suppressed when a wicket falls on the
  last ball; over runs come from the score itself so a ball bowled as the over rolls over is
  never dropped; each part of the update cycle is isolated so a hiccup in one graphic can't
  knock out the others mid-match.
- **Automatic club badges**, matched by PlayCricket club ID and detected from the day's
  fixture, with a manual dropdown fallback if anything doesn't match.

**After the match**
- **AI match report** — one click generates a full written report from the ball-by-ball log:
  result, key partnerships, standout performances, turning points — in seconds.
- **AI social media posts** — ready-to-paste posts summarising the match, bundled with
  photos from a folder for an easy match-day round-up.

**Under the hood**
- A faithful test harness simulates real match sequences so tricky edge cases stay fixed.
- Cleaner, more reliable server with all AI features sharing a single Anthropic key.

**Upgrading from v1**
1. Replace `server.py`, `overlay.html`, and `quickstart.py`.
2. Copy the updated `scoreboard.template` into NV Play's Templates folder and restart NV
   Play so it picks up the new dismissal-detail and shirt-number fields.
3. Create `headshots/` (player photos) and `socials/` (match photos) next to `server.py` if
   you want the new features — `logos/` for club badges is unchanged from v1.
4. Add your Anthropic API key in the control panel to enable commentary, reports, and posts.
5. Optional: fill in the Squad Roster for any players who share a surname or have duplicate
   PlayCricket accounts.

Existing `config.ini` and club settings carry over unchanged.

**What you need for the AI features:** a single Anthropic API key (console.anthropic.com)
powers over commentary, match report, and social posts — a few pence for a whole match.
Everything else works without one; AI features simply stay switched off until a key is set.

## v1.0 — Initial release

The first version: live scorebar, fall-of-wicket card, boundary flash, over summary, and
the OBS/NV Play integration that everything since has built on.
