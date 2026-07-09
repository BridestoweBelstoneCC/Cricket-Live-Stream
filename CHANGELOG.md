# Changelog

All notable changes to CricketStream Overlay are documented here, most recent first.

---

## Unreleased — on `dev`

*Manual-scoring improvements and YouTube broadcast controls from live use. Automated
coverage is complete; awaiting a human run-through before merging to `main`.*

- **YouTube broadcast manager** (was "YouTube Title"). Streaming with a stream key
  (recommended) removes OBS's "Manage Broadcast" panel, so the control panel now sets the
  broadcast's **title, description, privacy (public/unlisted/private), and category** over
  the YouTube Data API — most of what that panel did. One button pushes the lot to the
  active (or upcoming) broadcast; each part is a separate call so one failing doesn't sink
  the others, and the result reports exactly what applied. Uses the same one-time Google
  OAuth as the old title updater; the title-only path still works. **"Made for kids" is
  set in YouTube Studio when you create the broadcast** — YouTube's API rejects changing
  it afterwards, so the panel points you there rather than pretending to control it.
  Credential handling hardened for remote use: `yt_credentials.json` git-ignored (was
  not), token written 0600, remote first-run auth refused with a "do it on the streaming
  laptop" message instead of a hung browser, and paths resolved relative to server.py.

- **End-of-over recap on the scoring page.** When an over completes, the scorer sees a
  banner with the over's runs and ball-by-ball tokens, and a choice: confirm and pick the
  next bowler, or step back into the over ("Undo last ball") to fix a mistake before
  moving on.
- **Edit any ball, not just the last one.** An "Edit a ball" button lists recent
  deliveries by over and ball; pick one and the next outcome tap corrects it. Built on the
  event-sourced log — the innings is replayed around the correction, exactly, and any
  follow-on bowler/batter choice invalidated by the change is skipped rather than wedging
  the session. A rejected correction rolls back with the session left fully usable.
- **Scorecard export for Play-Cricket.** A "Scorecard" button (and one at match end)
  produces a full plain-text card — both innings, batting with dismissals, bowling
  figures, extras, and the result — to copy or download. Play-Cricket's API is read-only,
  so results still can't be submitted automatically, but this turns the after-match entry
  into a quick transcription rather than a reconstruction.
- **Fixed:** the session rebuild left the event log clobbered if a replay raised midway
  (e.g. a rejected edit), which could corrupt a live scoring session — the rebuild is now
  exception-safe.

## v2.5 — 2026-07-09

*Manually tested against a live NV Play feed on 2026-07-09 — replays and auto-tagging,
the full graphics run including bowler milestones, and the quality ladder (which found
and fixed the connected-account issue below). The manual scoring page and the match
simulator carry full automated coverage but await their first human outing.*

### New features

- **Manual scoring page (`/scoring`).** Score a match ball-by-ball from a phone or tablet
  with big tap-friendly buttons — no NV Play/PCS Pro needed — and the entire overlay,
  graphics, ball database and highlights pipeline works identically (manual frames render
  through the same parser as the scorer's feed, and outrank the file while a session is
  live). Event-sourced with exact-replay undo (even across the innings break), wicket-type
  / fielder / run-out-end pickers, per-over bowler prompts, next-batter override, and a
  session file that survives server restarts and dead phone batteries. Same club-password
  login as the control panel; selectable as the data source in quickstart. Also the plan B
  if the scorer's feed drops mid-match.
- **YouTube stream key managed by setup.** The wizard now asks for your stream key
  (stored in git-ignored config.ini, redacted like every other secret) and OBS setup
  applies it automatically — making key-based streaming, which survives restarts and
  quality changes, the default path. Never touched while a stream is live.
- **Adaptive stream quality** for grounds with poor internet, two tiers: OBS's built-in
  **Dynamic Bitrate** is now enabled automatically (seamless encoder-level flexing on
  congestion — no disconnects) and its status is verified live in the panel with a
  one-click enable; plus a **stream sentinel** that polls congestion/dropped frames every
  15s while live and can step the bitrate down a 100/70/50/35% ladder — manual panel
  buttons, or an off-by-default auto mode with a 60s evidence window and 150s anti-flap
  cooldown that never raises quality on its own. (A ladder step briefly restarts the
  stream; the YouTube broadcast survives with a few seconds of buffering.)
- **Match simulator (`simulate_match.py`).** Rehearse the whole broadcast without a
  scorer: a deterministic ball-by-ball engine writes NV Play-style frames to a fake PCS
  folder, faithful to the real feed's trickiest behaviours (ticker clearing on the
  over-completing write, blank pre-match names, `runs_required`-driven innings detection).
  Scenarios: full / chase / century / collapse; `--configure` points the running server at
  it; `--chaos` injects mid-write and stall failures.
- **Auto-tagged highlights.** Every replay clip is tagged at capture with why it fired and
  the match context; manually saved clips are tagged by correlating file times against the
  ball log. The highlights compiler burns captions in as lower-thirds, skips test clips,
  and writes a YouTube-ready description with chapter timestamps. The panel now reports
  the compile's real outcome instead of fire-and-forget.
- **Bowler milestone graphics**: five-wicket hauls (re-firing for the 6th/7th) and
  hat-tricks — including cross-over hat-tricks, with run outs breaking (not extending) the
  chain and wides/no-balls neutral.
- **Automated test suite**: 159 tests (stdlib unittest, no dependencies), wired into CI
  (which now also runs on `dev` pushes). Parsing, season stats, auth, quickstart merge,
  simulator invariants, highlights, manual scoring, stream-quality decisions, JS logic
  executed in a real engine, and HTTP integration against a live in-process server.

### Fixed

- **The ball-by-ball database was silently losing the final delivery of every over.**
  NV Play clears the ticker on the same write that completes an over, so ball 6 never
  appears in any ticker — the overlay always compensated via the score delta, but the DB
  logger just skipped it. Every over in `match_data.db` (and every CSV export) had at most
  five balls. The logger now recovers the invisible delivery from the score/wicket delta,
  and a new full-match soak test drives a complete simulated game through the real server
  and reconciles the DB against the engine's book, ball for ball, both innings.
- **The quality ladder could leave the stream down while reporting success** — OBS stops
  outputs asynchronously, so firing StartStream straight after StopStream could be
  rejected unnoticed. Each step is now verified: stop confirmed, bitrate set, restart
  retried, and honest failure messages if OBS misbehaves (concurrent shifts serialized).
  Found live in testing: **OBS's connected-YouTube-account mode ends the broadcast on
  StopStream** — the shift now detects a restart into a dead broadcast and says so, and
  the panel states the plain-stream-key requirement up front.
- **Milestone cards were hidden by their own replay** — the fifty/century replay switched
  OBS to the Replay scene while the gold card was still airing. The replay is now delayed
  (as wicket replays already were) so the card plays first.
- **Clip tagging was invisible** — tags live in the database, not filenames, so the panel
  now shows "N clips saved · M tagged for highlights" live.
- **Replay captions during manual scoring used stale PCS data** — the tagger now uses the
  same source precedence as the live feed (manual session first).
- **A leftover manual-scoring session silently outranked the scorer's feed** — quickstart
  now warns and offers to clear it when NV Play is the chosen source, and the panel shows
  an unmissable amber MANUAL badge whenever the manual session is driving the overlay.
- **State reads cached** — `load_state()` was re-reading and re-parsing the settings file
  from disk several times per overlay poll (thousands of reads per match); it's now
  mtime-cached (16× faster, zero steady-state disk I/O).
- **quickstart no longer wipes panel-entered state** (squad roster, sponsor fields, away
  colour, toggle edits, a manually entered opposition) — it merges over the existing file
  instead of rewriting it.
- **A non-numeric badge pick no longer kills the overlay** — `/live` crashed on every poll
  if `home_club_id` was set to a logo filename.
- **Weather now uses the ground's own coordinates** (saved from PlayCricket) instead of
  hardcoded ones — other clubs were getting the original club's weather, which also drove
  the DLS rain threshold.
- **Wickets were never reaching the event buffer or fall-of-wickets log** unless the AI
  ball-commentary toggle (off by default) was on — the detection baseline was only seeded
  inside that toggled path. Match reports were missing FOW data because of this.
- **CSV export worked only without a club password** (the panel used `window.open`, which
  can't send the auth header); **the prematch scorer line never displayed** (operator
  precedence bug); the ball-event commentary trigger compared state against an
  already-updated baseline (never fired); a missing `control_token` line was appended into
  the wrong config.ini section; the highlights concat file mis-declared every clip as
  0.5s long; plus removed dead routes and duplicate dict keys.

### Changed

- **The control panel now lives in `control.html`** (was a 2,100-line Python string inside
  server.py) — normal JS escaping, edits show on refresh without a server restart, and the
  historical backslash-doubling bug class is gone. Importing server.py no longer has side
  effects (token generation moved to startup).
- **`/live` split**: the overlay's poll drives the match pipeline and consumes wicket
  events; the panel polls a side-effect-free `/live/view`, so it can no longer eat the
  overlay's events or triple-run the ball logger.

## v2.4 — 2026-07-06

*(Tagged without a changelog entry at the time — backfilled.)*

- Weekend sponsor strap overlaid on the end-of-over graphics sequence; a long-standing
  over-transition timing bug fixed (end-of-over graphics fired one poll late); startup
  update check against the latest GitHub release.
- Pre-match and crediting fixes: split opening-batter cards no longer collapse to one,
  pre-match graphics persist until play actually starts (not until the match is merely
  configured), over commentary/summary credits the bowler who actually bowled the over,
  and the real cause of replays never firing (a hardcoded origin mismatch in
  overlay.html) resolved.

## v2.3 — 2026-07-02

- **Remote access, phase 3.** A QR code — printed as ASCII art in the terminal at startup
  and shown in the control panel — lets you pair a phone or tablet without typing an IP
  address. The panel shows a small pill ("This machine" / "Same network" / "Tailscale
  remote" / "Cloudflare remote") so whoever's looking at it always knows which kind of
  connection they're on. Added **Cloudflare Tunnel** as a public-URL fallback
  (`cloudflare_tunnel` in `config.ini [Network]`) for operators who can't install
  Tailscale — it refuses to start unless `club_password` is set, since that URL would
  otherwise be reachable with no login.
- **Self-healing watchdog.** A background check every 90 seconds fixes what it safely can
  and just logs what it can't: resets the season-stats build if it ever gets stuck (this
  also fixed the underlying bug — an unexpected error mid-build used to leave it wedged
  until a manual server restart), restarts the Cloudflare Tunnel if it dies (capped
  retries so a real problem doesn't loop forever), trims old rate-limit timestamps, and
  logs when the scorer's feed goes stale or recovers. Status visible at `GET /health`.
- **README rewrite.** Leads with the problem and what the project does, instead of a
  "Version 2.1 highlights" recap that had gone stale relative to this changelog.
- **Landing page polish.** The GitHub Pages site got scroll-in animations, hover lift on
  the feature cards, and a pulsing "LIVE" badge on the hero screenshot — all skipped
  automatically for visitors who've asked their browser for reduced motion.

## v2.2.2 — 2026-07-01

*`v2.2` and `v2.2.1` were superseded by this release and their tags/GitHub releases
removed — everything they contained is included below.*

- **Standalone setup wizard.** `CricketStreamSetup.exe` (Windows) and
  `CricketStreamSetup-mac.zip` (Mac, universal2 — works on both Apple Silicon and Intel) let
  a new club get started without installing Python first — the wizard installs Python
  itself if it's missing (via `winget` on Windows, the official installer plus the
  SSL-certificate fix on Mac), then walks through the same club setup as running
  `setup_wizard.py` from source. (The first build of this was accidentally Apple-Silicon-only
  and crashed with "bad CPU type in executable" on Intel Macs — no Rosetta-equivalent runs
  `arm64` code the other way round — fixed by building a proper `universal2` binary.)
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
- **Redacted the camera RTSP URL from `GET /state`.** Most IP cameras embed credentials
  directly in the URL (`rtsp://user:pass@host/stream`); this was being sent in plaintext to
  anything that could reach the control panel, including over plain HTTP Wi-Fi when using
  the phone/tablet control panel. It now redacts the same way the OBS password and API keys
  already did.
- **Docs:** consolidated `RELEASE_NOTES_v2.1.md` and `WHATS_NEW_V2.md` into this
  `CHANGELOG.md`, documented Intel Mac support in the setup guides, and updated the stale
  version badge.

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
