# CricketStream Overlay

<img width="1889" height="1080" alt="Screenshot_20260622-154222~2" src="https://github.com/user-attachments/assets/9477ebc9-37aa-4f9f-ac28-f6baab3fd130" />

**Professional live stream overlays for grassroots cricket clubs — free, open source, and built for volunteers.**

[![CI](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/actions/workflows/ci.yml/badge.svg)](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/actions/workflows/ci.yml)
[![Licence: GPL v3](https://img.shields.io/badge/Licence-GPLv3-blue.svg)](LICENSE)
[![Python 3.8+](https://img.shields.io/badge/Python-3.8%2B-blue?logo=python&logoColor=white)](https://python.org/downloads)
[![Platform: Windows & Mac](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS-lightgrey?logo=windows&logoColor=white)](README.md)
[![Version](https://img.shields.io/badge/Version-2.6.1-green)](CHANGELOG.md)
[![OBS Studio](https://img.shields.io/badge/Works%20with-OBS%20Studio-purple?logo=obs-studio&logoColor=white)](https://obsproject.com)

**[Project website →](https://bridestowebelstonecc.github.io/Cricket-Live-Stream/)**

---

## Contents

- [The problem](#the-problem)
- [What it does](#what-it-does)
- [How it works](#how-it-works)
- [No scoring software? Manual scoring](#no-scoring-software-manual-scoring)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Control panel](#control-panel)
- [Features at a glance](#features-at-a-glance)
- [Player photos](#player-photos-new-in-v2)
- [Squad roster](#squad-roster-new-in-v2)
- [Club badges](#club-badges)
- [Platform support](#platform-support)
- [File structure](#file-structure)
- [Cost comparison](#cost-comparison)
- [Version history](#version-history)
- [Contributing](#contributing)

---

## The problem

Professional cricket broadcast graphics cost thousands of pounds a season. Smaller clubs either go without, or settle for a static scoreboard that tells viewers nothing about what's actually happening on the pitch.

CricketStream Overlay changes that. It gives any club — regardless of budget — the same quality of live graphics you see on broadcast cricket, driven by the scoring software your scorer already uses — or, if your club doesn't use scoring software, by a [free manual scoring page](#no-scoring-software-manual-scoring) on any phone.

**All you need is a camera, a laptop and an internet connection.**

---

## What it does

CricketStream Overlay runs alongside OBS Studio (free streaming software) and your scoring laptop, adding a professional broadcast layer to your YouTube live stream.

### Live graphics

- **Scorebar** — always-on bottom bar showing score, overs, both batters with runs and balls faced, bowler with figures, run rate, and a live ball-by-ball ticker for the current over. Club badges appear next to team names automatically.
- **Fall of wicket card** — slides up automatically when a wicket falls, showing the dismissed batter's name, runs, balls faced, and the dismissal **spelled out in full** (e.g. *Caught Jones Bowled Smith*, *LBW Bowled Patel*, *Run Out*, *Stumped Wood Bowled Khan*). The detail fills in automatically as soon as the scorer enters how the batter was out.
- **Player card** *(new in v2)* — when a new batter comes to the crease, a card slides in with their **photo and season batting average** (innings, average, high score). Stats are aggregated live from PlayCricket for **both your club and the opposition**. When two openers start an innings, **both** cards appear — one on the left, one on the right. The card clears automatically when the batter faces their first ball.
- **Boundary flash** — FOUR! or SIX! graphic fires on every boundary.
- **Ball-by-ball colour coding** — wides (amber +), no balls (teal nb), byes (green b), leg byes (olive lb), wickets (red W), fours (blue 4), sixes (purple 6).
- **Over summary** — end-of-over card with runs scored and the bowler's figures. Automatically suppressed when a wicket falls on the last ball, so it never collides with the wicket sequence.
- **Auto-detected moments** *(new in v2.1)* — the over summary spots the storylines by itself: a gold strap fires for season-best scores (*"Smith 67\* — his best score of the season!"*) and team milestones (*"100 up in 14 overs"*), and the same facts are fed to the AI commentator so the spoken line builds around them.
- **"At this stage"** *(new in v2.1)* — in the second innings, each over summary compares the chase with the first innings at the same point (*"Heathcoat were 67-2 at this stage"*).
- **Bowler spell tracker** *(new in v2.1)* — once a bowler has bowled consecutive overs from the same end, the over card adds *"This spell: 5-1-18-2"*.
- **Full innings scorecard** *(new in v2.1)* — a broadcast-style card at the innings break: all eleven batters with dismissals spelled out, not-out batters highlighted, and bowling figures alongside. Also available on demand from the control panel. Requires the v2.1 scoreboard template.
- **DLS par score** *(new in v2.1)* — when rain is forecast at the ground, a small pill above the scorebar shows the Duckworth-Lewis par score and whether the chasing side is ahead (green) or behind (red). Standard Edition approximation — a guide, not an official calculation.
- **Partnership display** — live partnership runs and balls.
- **Worm chart** — two-innings cumulative run chart ("the worm"), each innings drawn as a climbing line in its team's colours with red wicket markers and the running total labelled at the head of each worm.
- **Player milestones** — automatic graphics for 50s and 100s.
- **Bowler milestones** *(new)* — five-wicket hauls get their own graphic in bowling red
  (updating again for the 6th and 7th), and hat-tricks are detected automatically —
  including across the bowler's consecutive overs, with run outs correctly not counting.
- **Innings summary** — top scorers and bowler figures at the end of each innings.
- **Batting lineup** — starting XI graphic at the beginning of an innings.
- **AI over commentary** *(new in v2)* — an optional Sky Sports-style line of analysis appears as a fourth end-of-over panel (after the over summary, partnership, and run rate), generated live by Claude AI from the actual match situation.
- **Drinks-break weather** *(new in v2)* — at a configurable over (default 25), the weather widget automatically appears during the drinks interval, then clears on the next ball.

### After the match *(new in v2)*

- **AI match report** — generates a full written match report in seconds from the ball-by-ball log of the game, powered by Claude. Editable in the control panel and saved to a dated text file.
- **AI social posts** — generates ready-to-paste social media posts (result, top performers, key moments). Optionally bundles them with match photos from a folder you choose.

### Replay system

- **Instant replay** — automatically saves and replays wickets, boundaries, and milestones via the OBS replay buffer.
- **Replay transition** — full-screen animated transition before each replay.
- **Highlights compiler** — stitches all replay clips into a post-match highlights reel automatically with FFmpeg.
- **Auto-tagged highlights** *(new)* — every replay clip is tagged with why it fired and the
  match situation at that moment ("WICKET · WALKER 22 · C JONES B HARRISON · 84-3"). The
  reel gets those captions burned in as lower-thirds, test clips are skipped, and a
  YouTube-ready description with chapter timestamps is written alongside the video.

### Match data & social posts *(expanded in v2.1)*

- **Ball-by-ball database** *(new in v2.1)* — every delivery is logged to a local SQLite file (`match_data.db`) as you stream: over, ball, batter, bowler, outcome, extras, and running score. The current over is rewritten live so a scorer's edits and deletions are captured, and completed overs freeze. A **Reconcile** button then pulls PlayCricket's published scorecard as the authoritative record, and any match can be **exported to CSV** for analysis. Your own season-long dataset, ready for spreadsheets or a notebook.
- **Result posts for any match** *(new in v2.1)* — a "Load results" picker pulls your recent PlayCricket results (home or away, streamed or not). Pick one and it builds a polished 1080×1350 Instagram graphic, working out the result and your top batter and bowler directly from the scorecard. Optional AI caption (template fallback when offline).
- **Per-team and youth photo folders** *(new in v2.1)* — backdrops are pulled from `socials/1st`, `socials/2nd`, `socials/3rd` per team, falling back to the main folder. All age-group sides route to `socials/youth`, which uses club stock photos and a discreet first-name + initial for player names — a safeguarding-conscious default for juniors.
- **Match-day sponsors** *(new in v2.1)* — every logo in `sponsors/` appears on result posts, scaled to share the width, so adding a one-off match-day sponsor is just dropping in a file.

### Automation

- **PlayCricket integration** — fetches today's fixture automatically, filling in opposition name, competition, umpires, and ground.
- **NV Play / PCS Pro integration** — reads the scorer's output file directly, giving ball-by-ball data with batter names, bowler figures, run rate, and dismissal details.
- **OBS auto-setup** — configures scenes, sources, and the replay buffer automatically on first run.
- **One-click camera** *(new in v2.1)* — enter your camera's RTSP URL in the control panel and the overlay adds it to OBS as a media source for you, with auto-reconnect if the feed drops.
- **YouTube title updater** — updates the stream title automatically when the match starts.
- **Weather widget** — current conditions at the ground on demand or at the drinks break.
- **Adaptive stream quality** *(new)* — built for grounds where the internet is the weak
  link. OBS's dynamic bitrate is enabled automatically (the connection is managed
  seamlessly, with no disconnects), and a live congestion monitor in the control panel can
  step the stream quality down a ladder when the line is genuinely struggling — manually,
  or automatically with safeguards against flapping. Stepping down also reduces CPU load
  on older laptops.
- **Match simulator** *(new)* — `python3 simulate_match.py` rehearses the entire broadcast
  without a scorer: a realistic simulated match (full game, tense chase, century chase, or
  batting collapse) drives every graphic, replay and database exactly as a real feed would.
  Perfect for testing your setup the night before a stream.

---

## How it works

```
Scorer's laptop          Streaming laptop           YouTube
────────────────         ──────────────────         ───────
NV Play / PCS Pro  ───>  server.py (Python)  ───>  OBS Studio  ───>  Live Stream
(scoring software)       reads output file          + Overlay
                         every 2-3 seconds          browser source
```

The scoring software writes a file on every ball. The server reads it and sends the data to an overlay running inside OBS. OBS mixes the overlay with your camera and streams to YouTube.

No scoring software? A phone at `/scoring` takes the place of the scorer's laptop — same overlay, same graphics. See the next section.

Interested in the internals? [`ARCHITECTURE.md`](ARCHITECTURE.md) has the full picture —
data sources, the journey of a single ball, module map and security model — with diagrams
that render right on GitHub.

---

## No scoring software? Manual scoring

Not every club uses NV Play or PCS Pro — and even for clubs that do, the scorer's feed
occasionally isn't available. **You can still stream with the full broadcast overlay.**

Open **`http://localhost:5000/scoring`** on a phone or tablet and score the match ball by
ball with big, tap-friendly buttons. Every press drives exactly the same pipeline the
scoring software would — the scorebar and ticker, wicket cards with dismissals spelled out,
milestones, boundary flashes, instant replays, the worm chart, the ball-by-ball database,
and the highlights compiler all work identically.

<b>Setup takes a minute:</b> enter the two team names, paste each XI (one name per line —
or leave it blank and players are named automatically), set the overs, pick who bats first,
and start scoring.

- **Big buttons** — dot, 1, 2, 3, 4, 6; wides, no-balls, byes and leg byes with the runs
  taken; a wicket flow that asks how out, who caught it, and (for run outs) which end.
- **Undo anything** — every ball is recorded as an event, and undo replays the innings
  without the last entry, so the book is always exactly right — even undoing back across
  the innings break.
- **Bowler and batter prompts** — at the end of each over the page asks who bowls next;
  when a wicket falls it names the next batter in, with a one-tap change if the order
  differs on the day.
- **Restart-safe** — the session saves after every ball. If the laptop restarts or the
  phone battery dies, reopen the page and carry on from the same delivery.
- **Score from the boundary** — with remote access enabled (Tailscale, or
  `bind_host = 0.0.0.0` on club Wi-Fi), a volunteer can score from a deckchair while the
  laptop streams. The page is protected by the same club password as the control panel.

On match day, the quickstart launcher asks how the game is being scored — choose **Manual
scoring** and it prints the link to open. If your scorer does run NV Play, the file feed
remains the better option (zero extra effort on your side) — but the manual page is also
a ready-made **plan B if the scorer's feed drops out mid-match**.

> PlayCricket-powered extras (auto fixture detection, season stats on player cards, club
> badge matching) are optional either way — manual scoring works with none of them
> configured, which also makes it the quickest way for clubs **outside the ECB/PlayCricket
> ecosystem** to use this project.

---

## Requirements

**Hardware:**
- Any laptop or PC running Windows or macOS
- A camera (HDMI camera via capture card, or USB webcam)
- A stable internet connection for streaming

**Software (all free):**
- [OBS Studio](https://obsproject.com) — streaming software
- [Python 3](https://python.org/downloads) — runs the server (the setup wizard below can install this for you)
- [NV Play](https://www.play-cricket.com/website/np_downloads) — scoring software (Windows) or PCS Pro
  *(optional — the [manual scoring page](#no-scoring-software-manual-scoring) replaces it entirely)*

**Optional:**
- A PlayCricket API key — for automatic match detection
- An Anthropic API key — for AI over commentary, match reports, and social posts (a few pence per match)

---

## Quick start

New to this? Read your platform's quick start first:

- **Windows:** [`SETUP_GUIDE_WINDOWS.md`](Windows/SETUP_GUIDE_WINDOWS.md) — starts with a 2-step fast path, full detail below it
- **macOS:** [`SETUP_GUIDE_MAC.md`](Mac/SETUP_GUIDE_MAC.md) — starts with a 3-step fast path, full detail below it
- **No coding experience at all?** [`FOR_NON_TECHNICAL_USERS.md`](FOR_NON_TECHNICAL_USERS.md) walks you through every step in plain English.

**First time? One download does everything — no need to install Python yourself:**

1. Download the setup wizard from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest) — `CricketStreamSetup.exe` (Windows) or `CricketStreamSetup-mac.zip` (Mac, works on both Apple Silicon and Intel) — and put it in the folder you extracted the project to.
2. Run it — Windows: double-click the `.exe`. Mac: unzip it, then double-click `Setup Wizard.command`.
   If Python isn't installed yet, it installs it for you, then installs packages, walks you through your club details, writes `config.ini`, and offers to launch the server immediately.

Already have Python installed and prefer running from source? Skip the download and run `Windows/setup.bat` or `Mac/setup.sh` instead — same wizard.

That's it. The wizard handles everything else interactively — no manual file editing needed.

**Already set up? Match day:**

- Windows: double-click `Windows/quickstart.bat`
- Mac: run `Mac/quickstart.sh`

---

## Control panel

Once running, open the control panel in any browser on the same machine
(`http://localhost:5000/control` on Windows, `http://127.0.0.1:5000/control` on Mac).

**Using it from your phone or tablet:** if you set `bind_host = 0.0.0.0` in `config.ini`
and your phone is on the same Wi-Fi, you can open the panel from any device at
`http://<laptop-ip>:5000/control` (your laptop's local IP is shown in the terminal when
the server starts). The panel is fully responsive and designed to work at phone-screen size.

**API keys and settings load automatically from `config.ini`** — values you entered in
`[API]`, `[OBS]`, `[Scoring]`, and `[Stream]` are seeded into the panel on first run.
You do not need to re-enter them manually.

It lets you:
- Set opposition name and kit colour
- **Pick club badges** for either team from a dropdown (no need to name files perfectly)
- **Edit the squad roster** — map shirt numbers to full player names so cards resolve correctly
- Turn individual graphics on or off
- Test and configure replays
- Enter your Anthropic API key (powers commentary, player-card stats prose, match reports, and social posts)
- Show/hide the weather widget and set the drinks-break over
- Refresh season batting stats from PlayCricket
- Update the YouTube stream title
- Monitor live data from NV Play in real time
- Generate the AI match report and social posts
- Compile a post-match highlights reel
- Get a recommended bitrate/resolution from a real upload-speed test, and find out whether
  your hardware video encoder is actually outperforming plain CPU encoding — measured with a
  short OBS test recording, not guessed from your computer's specs

---

## Features at a glance

| Feature | Requires |
|---|---|
| Manual ball-by-ball scoring from a phone | Nothing — just a browser at `/scoring` |
| Scorebar with batter/bowler names | NV Play output file *(or manual scoring)* |
| Ball-by-ball ticker | NV Play output file |
| Fall-of-wicket card with dismissal detail | NV Play output file |
| Player card with photo + season stats | NV Play output file + `headshots/` folder + PlayCricket API key |
| Opposition player stats | PlayCricket API key |
| Squad roster (resolves brothers / duplicate accounts) | Set in control panel |
| Over summary / partnership / worm panels | NV Play output file |
| Player milestones (50s/100s) | NV Play output file |
| Boundary & six flashes | NV Play output file |
| Drinks-break weather | Open-Meteo (free) |
| Score only (no names) | PlayCricket widget (internet) |
| Instant replay | OBS WebSocket + replay buffer |
| Highlights compiler | FFmpeg (free) |
| AI over commentary | Anthropic API key |
| AI match report & social posts | Anthropic API key |
| Ball-by-ball database + CSV export | NV Play output file (logs as you stream) |
| Result post for any past match | PlayCricket API key |
| Per-team / youth social photo folders | `socials/1st`, `socials/2nd`, `socials/3rd`, `socials/youth` |
| One-click RTSP camera into OBS | OBS WebSocket |
| Bowler milestones (five-fors, hat-tricks) | NV Play output file *(or manual scoring)* |
| Auto-tagged, captioned highlights with chapters | FFmpeg + OBS WebSocket |
| Adaptive stream quality / congestion monitor | OBS WebSocket |
| Match simulator for rehearsals | Nothing — runs offline |
| Auto match detection | PlayCricket API key |
| YouTube title update | Google OAuth |
| Club badges in scorebar | PNG/SVG files in `logos/` folder |

---

## Player photos (new in v2)

Player cards show a circular headshot next to the batter's name and season stats.

**Setup:** create a `headshots/` folder next to `server.py` and add player photos.
Because NV Play provides surnames only, the overlay tries several filename patterns so
you don't have to be exact — any of these will match a batter shown as "Smith":

```
Smith.jpg
smith.png
J_Smith.jpg
JOHN_SMITH.png
```

Square images around 400x400 work best. Supported formats: JPG, PNG, WebP. No restart
needed — drop a file in and it appears the next time that batter comes in. If no photo
is found, the card simply shows initials instead.

A custom folder can be set in the control panel or in `config.ini` under `headshots_folder`.

For players who share a surname (brothers are common at club level), you can also name a
photo by **shirt number** — `21.png`, `28.jpg` — and the squad roster (below) will pin it
to the right player.

---

## Squad roster (new in v2)

NV Play only sends surnames, so two brothers both show as "Smith", and a player with two
PlayCricket accounts can appear twice in the stats. The squad roster solves both.

In the control panel, open **Squad Roster** and enter one player per line, mapping their
**shirt number** to their **full name**:

```
21 = Steve Smith
28 = John Smith
14 = Paul Smith
```

The PCS Pro template sends each batter's shirt number alongside their name, so the overlay
uses the number to look up the correct full name — and from there the correct photo and the
correct season stats. Brothers no longer get mixed up, and where a player has more than one
PlayCricket account the stats use their most-played (regular) account automatically.

You only need to do this once per season, and only for players whose surname is shared or
ambiguous. Everyone else resolves fine by surname alone. For this to work, the scorer's
NV Play / PCS Pro squad must have shirt numbers assigned, and you must deploy the v2
`scoreboard.template` (see the upgrade notes in `CHANGELOG.md`).

**Checking a player's stats match.** If a card shows a photo but no stats (or you suspect
the wrong record), open this in a browser while the server is running:

```
http://localhost:5000/player/stats?name=SURNAME&debug=1
```

It lists every player sharing that surname in the season data, how many innings each has,
and which record the overlay will use — making it obvious when a name is ambiguous (add
those players to the roster) or when someone has a duplicate PlayCricket account.

---

## Club badges

Small circular club badges appear next to team names in the scorebar automatically,
matched by PlayCricket club ID. Create a `logos/` folder next to `server.py` and add
badge images named by club ID (e.g. `12345.png`). The opposition's club ID and a short
abbreviation are detected automatically from the day's fixture, so naming their badge
`<their-club-id>.png` is enough for it to appear. If a badge doesn't match, you can also
pick either team's badge from a dropdown in the control panel. See [`CLUB_LOGOS.md`](CLUB_LOGOS.md)
for full instructions, including how to find opposition club IDs.

---

## Platform support

| | Windows | macOS |
|---|---|---|
| Full feature set | yes | yes |
| NV Play native | yes | Via VMware Fusion (free) |
| OBS streaming | yes | yes |
| Installer / launcher | `install.bat` + `quickstart.bat` | `install.sh` + `quickstart.sh` |

For Mac users running NV Play: see [`MAC_VM_SETUP.md`](Mac/MAC_VM_SETUP.md) for running NV Play
inside a free Windows virtual machine while streaming natively from macOS.

---

## File structure

```
/
├── server.py                  Main server — runs everything
├── overlay.html               OBS browser source overlay (1920x1080)
├── control.html               Operator control panel (served at /control)
├── scoring.html               Manual ball-by-ball scoring page (served at /scoring)
├── scoring_engine.py          The scorer's-book engine behind /scoring and the simulator
├── simulate_match.py          Match simulator — rehearse the whole broadcast, no scorer needed
├── setup_wizard.py            First-time setup wizard (installs packages + creates config.ini)
├── quickstart.py              Match-day launcher script
├── obs_setup.py               OBS auto-configuration
├── tests/                     Automated test suite (python3 -m unittest discover -s tests)
├── requirements.txt           Python package list
├── config.example.ini         Club configuration template — copy to config.ini and fill in
├── match_state.example.json   Settings template — copied to match_state.json on first run
├── scoreboard.template   NV Play output template
├── Windows/
│   ├── SETUP_GUIDE_WINDOWS.md Full Windows setup + troubleshooting
│   ├── setup.bat              First-time setup wizard launcher
│   ├── install.bat            Package installer (advanced)
│   ├── quickstart.bat         Match-day launcher
│   └── start_server.bat       Server-only launcher
├── Mac/
│   ├── SETUP_GUIDE_MAC.md     Full Mac setup + troubleshooting
│   ├── MAC_VM_SETUP.md        Mac + VMware Fusion guide
│   ├── setup.sh               First-time setup wizard launcher
│   ├── install.sh             Package installer (advanced)
│   ├── quickstart.sh          Match-day launcher
│   └── start_server.sh        Server-only launcher
├── docs/                      GitHub Pages landing page
├── README.md                  This file
├── ARCHITECTURE.md            System design with diagrams (for contributors)
├── CHANGELOG.md               Version history and upgrade notes
├── FOR_NON_TECHNICAL_USERS.md Plain-English guide for volunteers
├── CONTRIBUTING.md            How to contribute
├── CLUB_LOGOS.md              Club badge guide
├── SECURITY.md                Security policy
├── logos/                     Club badge images (named by PlayCricket club ID)
├── headshots/                 Player photos (named by surname or shirt number)
├── socials/                   Match photos for social posts
│   ├── 1st/ 2nd/ 3rd/         Optional per-team photo folders
│   └── youth/                 Stock photos for youth posts (safeguarding)
└── sponsors/                  Sponsor logo images
```

> **`config.ini` and `match_state.json`** are git-ignored — they hold your API keys and
> OBS password once configured. The repo ships `config.example.ini` and
> `match_state.example.json` as safe templates. Copy `config.example.ini` to `config.ini`
> and fill in your details; the server creates `match_state.json` from defaults on first run.

---

## Cost comparison

| Provider | Annual cost |
|---|---|
| Professional broadcast graphics | £2,000–£10,000+ |
| **CricketStream Overlay** | **Free** |
| Optional AI features (commentary, reports, posts) | a few pence per match |
| Optional PlayCricket API | Free |

The only costs are what you're likely already paying: a camera, a laptop, and a YouTube account.

---

## Version history

**v2.6.1** — Ten bugs found by a deep code review of everything shipped between v2.4 and
v2.6, all fixed with regression tests (suite now 211): over-final wickets are credited to
the right bowler everywhere (hat-trick chain, five-for graphic, ball database), manual
scoring's edit/undo/restart recovery no longer wedges, and a live manual session now counts
as a healthy feed in the AI commentary, `/health`, and the watchdog. Full detail in
[`CHANGELOG.md`](CHANGELOG.md).

**v2.6** — Manual scoring gains an end-of-over recap, edit-any-ball, and a plain-text
scorecard for Play-Cricket entry. The YouTube "title" updater becomes a full broadcast
manager (title, description, privacy, category) over the API, for stream-key streaming
where OBS's Manage Broadcast panel disappears — with hardened credential handling. Plus
fixes from live testing (see [`CHANGELOG.md`](CHANGELOG.md)).

**v2.5** — Manual scoring page (`/scoring`) for clubs without scoring software; adaptive
stream quality (auto dynamic bitrate + congestion monitor with a quality ladder); match
simulator for full broadcast rehearsals; auto-tagged, captioned highlights with YouTube
chapters; bowler milestone graphics (five-fors, hat-tricks); a 169-test automated suite
wired into CI; and a batch of match-day fixes — including the ball-by-ball database no
longer losing the final delivery of every over. Full detail in
[`CHANGELOG.md`](CHANGELOG.md).

**v2.4** — Weekend sponsor strap, over-transition timing fix, startup update check, and
pre-match/crediting fixes (see changelog).

**v2.1.1** — **Responsive control panel** (stacks to a single column with touch-friendly buttons at phone width, so a Wi-Fi phone operator can run the stream comfortably). **config.ini auto-seeding** — `server.py` now reads all `config.ini` sections on startup and pre-populates any fields still at their defaults, so API keys, club name, kit colour, and folder paths load automatically without needing to re-enter them in the control panel.

**v2.1** — Broadcast intelligence & your own data. A local **ball-by-ball database**
(SQLite) that logs every delivery as you stream, survives scorer edits, reconciles against
PlayCricket's published scorecard, and exports to CSV. **Standalone result posts** for any
match — home or away, streamed or not — with top batter/bowler worked out from the
scorecard, per-team and youth photo folders (safeguarding defaults for juniors), and an
all-sponsors strip. **One-click RTSP camera** setup into OBS. Auto-detected moments
(season-best scores, team milestones) on the over summary and woven into the AI commentary;
second-innings "at this stage" comparison; full innings scorecard at the break (requires the
v2.1 scoreboard template); bowler spell tracker; DLS par score pill when rain is forecast;
broadcast animation polish (spring entries, sweeping boundary banners, milestone count-ups);
a control-panel health strip and a quickstart pre-flight check; threaded server; hardened
security (secrets no longer readable from the browser, atomic state writes, escaped names).

**v2.0** — Broadcast layer release. Player cards with photos and live PlayCricket season
stats for both sides; squad roster (shirt numbers) to resolve brothers, shared surnames,
and duplicate PlayCricket accounts; cumulative worm chart in team colours; full-word
dismissals on the wicket card; AI over commentary, match reports, and social posts (Claude);
automatic club-badge matching by PlayCricket club ID with a manual picker; drinks-break
weather; kit colours that follow the batting team; score-delta over runs and isolated
graphics updates for a more robust match-day loop; and a player-stats diagnostic endpoint.

**v1.0** — Initial release: live scorebar, ball-by-ball ticker, fall-of-wicket card,
boundary flashes, over summaries, partnerships, milestones, innings summaries, instant
replay via OBS, highlights compiler, PlayCricket fixture detection, and YouTube title updates.

---

## Contributing

Contributions are welcome — see [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to get started. If you add support for additional scoring software, fix bugs, or improve the overlay design, please open a pull request. If your club uses this and improves it for your own needs, please share the changes so other clubs can benefit.

---

## Acknowledgements

Built by Bridestowe & Belstone CC, Devon Cricket League.
Scoring data via NV Play and the PlayCricket API (ECB).
AI features powered by Claude (Anthropic).
Weather data via Open-Meteo.
Streaming via OBS Studio.

---

## Licence

See the [`LICENSE`](LICENSE) file in the repository. Free to use, modify, and distribute for your club.
