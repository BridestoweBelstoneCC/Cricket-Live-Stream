<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo-dark-bg.png">
    <img src="brand/logo-light-bg.png" alt="CricketStream" width="420">
  </picture>
</p>

# CricketStream Overlay

<img width="1889" height="1080" alt="Screenshot_20260622-154222~2" src="https://github.com/user-attachments/assets/9477ebc9-37aa-4f9f-ac28-f6baab3fd130" />

**Professional live stream overlays for grassroots cricket clubs — free, open source, and built for volunteers.**

[![CI](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/actions/workflows/ci.yml/badge.svg)](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/actions/workflows/ci.yml)
[![Licence: GPL v3](https://img.shields.io/badge/Licence-GPLv3-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Built%20with-Go-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform: Windows & Mac](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS-lightgrey?logo=windows&logoColor=white)](README.md)
[![Version](https://img.shields.io/badge/Version-2.12-green)](CHANGELOG.md)
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
- [Get help / chat](#get-help--chat)
- [Code signing policy](#code-signing-policy)
- [Privacy](#privacy)
- [What CricketStream changes on your computer](#what-cricketstream-changes-on-your-computer)

---

## The problem

Professional cricket broadcast graphics cost thousands of pounds a season. Smaller clubs either go without, or settle for a static scoreboard that tells viewers nothing about what's actually happening on the pitch.

CricketStream Overlay changes that. It gives any club — regardless of budget — the same quality of live graphics you see on broadcast cricket, driven by the scoring software your scorer already uses — or, if your club doesn't use scoring software, by a [free manual scoring page](#no-scoring-software-manual-scoring) on any phone.

**All you need is a camera, a laptop and an internet connection.**

---

## What it does

CricketStream Overlay runs alongside OBS Studio (free streaming software) and your scoring laptop, adding a professional broadcast layer to your YouTube live stream.

### Live graphics

- **Scorebar** — always-on bottom bar showing score, overs, both batters with runs and balls faced, bowler with figures, run rate, and a live ball-by-ball ticker for the current over. Club badges appear next to team names automatically. **Four styles** — *Classic* (light grey panels), *Modern* (dark broadcast slab), *Impact* (bold angled cuts, easiest to read on a phone) and *Minimal* (clean white, best under a bright daytime picture) — all using your club colours. Pick one in the control panel under Graphics, which shows a **live preview** of each; changing it takes effect on the overlay's next poll, mid-match if you like.
- **Fall of wicket card** — slides up automatically when a wicket falls, showing the dismissed batter's name, runs, balls faced, and the dismissal **spelled out in full** (e.g. *Caught Jones Bowled Smith*, *LBW Bowled Patel*, *Run Out*, *Stumped Wood Bowled Khan*). The detail fills in automatically as soon as the scorer enters how the batter was out.
- **Player card** *(new in v2)* — when a new batter comes to the crease, a card slides in with their **photo and season batting average** (innings, average, high score). Stats are aggregated live from PlayCricket for **both your club and the opposition**. When two openers start an innings, **both** cards appear — one on the left, one on the right. The card clears automatically when the batter faces their first ball.
- **Boundary flash** — FOUR! or SIX! graphic fires on every boundary.
- **Ball-by-ball colour coding** — wides (amber +), no balls (teal nb), byes (green b), leg byes (olive lb), wickets (red W), fours (blue 4), sixes (purple 6).
- **Over summary** — end-of-over card with runs scored and the bowler's figures. Automatically suppressed when a wicket falls on the last ball, so it never collides with the wicket sequence.
- **Auto-detected moments** *(new in v2.1)* — the over summary spots the storylines by itself: a gold strap fires for season-best scores (*"Smith 67\* — best score of the season!"*) and team milestones (*"100 up in 14 overs"*), and the same facts are fed to the AI commentator so the spoken line builds around them.
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
- **Win predictor** *(new)* — after each over, both teams' chances of winning ("Need 26
  off 24 balls" in a chase, the projected total in the first innings). Built from how this
  season's matches in your league have actually gone (the PlayCricket scorecards the season
  stats already download) and a DLS-style model of the batting left. Backtested on a real
  season of 64 matches: the favourite at the start of the chase won 66% of the time.
- **AI over commentary** *(new in v2)* — an optional Sky Sports-style line of analysis appears as an end-of-over panel (after the over summary, partnership, run rate and win predictor), generated live by Claude AI from the actual match situation.
- **Spoken commentary** *(new, optional)* — the end-of-over line read aloud on the stream in
  the computer's own voice (British English where available). Free and offline; off by
  default, because these voices are serviceable rather than broadcast quality.
- **Drinks-break weather** *(new in v2)* — at a configurable over (default 25), the weather widget automatically appears during the drinks interval, then clears on the next ball.

### After the match *(new in v2)*

- **AI match report** — generates a full written match report in seconds from the scorer's own figures (innings totals, top scorers, who fell when), powered by Claude. The figures are saved as the match goes, so a server restart loses nothing; with no match data it says so rather than writing anything. Editable in the control panel and saved to a dated text file.
- **AI social posts** — generates ready-to-paste social media posts (result, top performers, key moments). Optionally bundles them with match photos from a folder you choose.
- **Social clips** *(new)* — every wicket, six, four and milestone replay becomes a vertical
  video for YouTube Shorts, Instagram Reels and TikTok: the replay in the middle, a big
  headline ("SIX!"), a line about the moment, the club badge and your handle, plus a
  caption ready to paste. Download them from the panel on your phone.
- **Match page** *(new)* — about a minute after the stream ends, a one-page match report is
  built automatically: result, result card, AI report, runs-by-over chart, both scorecards,
  a link to the full stream on YouTube, and your sponsors. One self-contained file, ready
  for the club website or the WhatsApp group.
- **Sponsor airtime** — how long the weekend sponsor was on screen while live, and an
  estimate of viewer-minutes from YouTube's live viewer count, ready to send to the sponsor.

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
- **Instagram result card** *(rebuilt in v2.9)* — a 1080×1350 card in the style of county result graphics: your photo in full colour, a big WIN / DEFEAT with the margin, both clubs' badges and scores, and your top batter and bowler. Built for the match you've just streamed, or for any recent PlayCricket result via the "Load results" picker (home or away, streamed or not). The result is PlayCricket's own, so DLS, conceded, tied, drawn, abandoned and softball matches come out right, and the numbers are never AI-generated. Optional AI caption (template fallback when offline).
- **Per-team and youth photo folders** *(new in v2.1)* — backdrops are pulled from `socials/1st`, `socials/2nd`, `socials/3rd` per team, falling back to the main folder. All age-group sides route to `socials/youth`, which uses club stock photos and a discreet first-name + initial for player names — a safeguarding-conscious default for juniors.
- **Match-day sponsors** *(new in v2.1)* — every logo in `sponsors/` appears on result posts, scaled to share the width, so adding a one-off match-day sponsor is just dropping in a file.

### Automation

- **PlayCricket integration** — fetches today's fixture automatically, filling in opposition name, competition, umpires, and ground.
- **NV Play / PCS Pro integration** — reads the scorer's output file directly, giving ball-by-ball data with batter names, bowler figures, run rate, and dismissal details.
- **OBS auto-setup** — configures scenes, sources, and the replay buffer automatically on first run.
- **One-click camera** *(new in v2.1)* — enter your camera's RTSP URL in the control panel and the overlay adds it to OBS as a media source for you, with auto-reconnect if the feed drops.
- **YouTube broadcast manager** — sets the stream's title, description, privacy and
  category through YouTube's API (needed with stream-key streaming, where OBS's own panel
  disappears).
- **Match-day checklist** — the panel shows what's ready and what isn't (OBS, camera, replay
  buffer, today's match, scorer feed, live) and has a button for each step: Start OBS, Add
  camera, Start replay buffer, Go live. Nothing is ticked by hand; every item is detected.
- **Pre-match check** — one button, days before a match, tries every login and connection
  for real (YouTube and which channel, PlayCricket, the AI key, OBS, the scorer, cameras,
  disk space, a leftover quality downshift, upload speed, today's fixture, a newer version)
  and says ✓ / ⚠ / ✗ with what to do. It also runs by itself every 6 hours.
- **OBS crash recovery** — if OBS crashes or freezes mid-match, the server reopens it past
  the "Safe Mode?" prompt, puts the main scene back, and restarts the stream if it was
  live. Closing OBS normally after the match leaves it closed.
- **AI camera spotter** *(new, optional)* — while live, Claude looks at each camera every
  few minutes and warns about a fogged or wet lens, a knocked camera, glare or a blocked
  view; a frozen picture is caught without AI. About 10–15p for a long match.
- **Weather widget** — current conditions at the ground on demand or at the drinks break.
- **Adaptive stream quality** *(new)* — built for grounds where the internet is the weak
  link. OBS's dynamic bitrate is enabled automatically (the connection is managed
  seamlessly, with no disconnects), and a live congestion monitor in the control panel can
  step the stream quality down a ladder when the line is genuinely struggling — manually,
  or automatically with safeguards against flapping. Stepping down also reduces CPU load
  on older laptops.
- **Practice match** *(new)* — one button in the control panel (Match day → Practice
  match) rehearses the entire broadcast without a scorer: a realistic simulated match (full
  game, tense chase, century chase, or batting collapse) drives every graphic and replay
  exactly as a real feed would, then puts everything back and keeps nothing. Perfect for
  testing your setup the night before a stream. (`CricketStream simulate` does the same
  from the command line.)

---

## How it works

```
Scorer's laptop          Streaming laptop           YouTube
────────────────         ──────────────────         ───────
NV Play / PCS Pro  ───>  CricketStream       ───>  OBS Studio  ───>  Live Stream
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

On match day, CricketStream asks how the game is being scored — choose **Manual
scoring** and it prints the link to open. If your scorer does run NV Play, the file feed
remains the better option (zero extra effort on your side) — but the manual page is also
a ready-made **plan B if the scorer's feed drops out mid-match**.

> PlayCricket-powered extras (auto fixture detection, season stats on player cards, club
> badge matching) are optional either way — manual scoring works with none of them
> configured, which also makes it the quickest way for clubs **outside the ECB/PlayCricket
> ecosystem** to use this project.

---

## NV Play on separate hardware

If NV Play has to run inside a Windows VM (common on a Mac), keeping that VM on the same
machine that's also running OBS and the server can push it past its thermal limit — a VM
crash mid-match takes the scorer's progress with it.

**The bridge** lets NV Play run on a second, cheap machine instead (an old laptop, a
mini PC — anything that can run NV Play). Run CricketStream there and choose *Scorer's
machine, elsewhere* on its setup page — it asks for the NV Play output folder and shows a
connection token — then paste that machine's URL and token into
the control panel's "NV Play on separate hardware" fields instead of the PCS output folder.
Works over the same Tailscale connection used for remote access.

Full setup steps, a security rundown, and troubleshooting: **[`BRIDGE.md`](BRIDGE.md)**.

---

## Running the stream on a second laptop

Keep the scorer's laptop scoring and nothing else — the streaming laptop runs OBS, the
server and the overlay, and can be restarted or fiddled with mid-match without reaching
across to the scorer. The scoring laptop runs the same CricketStream download, set up as
*The scorer's laptop*, and the streaming laptop finds it on the club wifi
automatically (no address to type). This is the option for two laptops already on the
same local network; use the NV Play bridge above instead if they aren't (e.g. reached
over Tailscale).

Full setup steps and troubleshooting: **[`TWO_LAPTOP_SETUP.md`](TWO_LAPTOP_SETUP.md)**.

---

## Requirements

**Hardware:**
- Any laptop or PC running Windows or macOS
- A camera (HDMI camera via capture card, or USB webcam)
- A stable internet connection for streaming

**Software (all free):**
- [OBS Studio](https://obsproject.com) — streaming software
- CricketStream — one file, nothing else to install (see Quick start)
- [NV Play](https://www.play-cricket.com/website/np_downloads) — scoring software (Windows) or PCS Pro
  *(optional — the [manual scoring page](#no-scoring-software-manual-scoring) replaces it entirely)*

**Optional:**
- A PlayCricket API key — for automatic match detection
- An Anthropic API key — for AI over commentary, match reports, and social posts (a few pence per match)
- [FFmpeg](https://ffmpeg.org/download.html) — only for the highlights reel and social clips

---

## Quick start

New to this? Read your platform's quick start first:

- **Windows:** [`SETUP_GUIDE_WINDOWS.md`](SETUP_GUIDE_WINDOWS.md)
- **macOS:** [`SETUP_GUIDE_MAC.md`](SETUP_GUIDE_MAC.md)
- **No coding experience at all?** [`FOR_NON_TECHNICAL_USERS.md`](FOR_NON_TECHNICAL_USERS.md) walks you through every step in plain English.

**One file. You run the same one every time — on every machine.**

1. Download it from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest) — `CricketStream.exe` (Windows) or `CricketStream-mac.zip` (Mac, one download covers both Apple Silicon and Intel) — and put it in a folder of its own (say, Documents → CricketStream).
2. Double-click it. That's the whole job.

It works out for itself what still needs doing:

| | |
|---|---|
| **First run** | CricketStream opens in its own window and asks **which computer is this?** — the streaming laptop, or the scorer's laptop — then the setup questions for that one: your club details, a preview of your stream in your colours with every graphic · writes `config.ini` |
| **Every run after** | Opens on this computer's job — on the streaming laptop the match-day screen (**Start match day**: OBS, today's fixture, season stats, pre-flight check, then the control panel in the same window, logged in); on the scorer's laptop, sharing the score, with a green light when the streaming laptop is collecting it |

Nothing to install first, no files to edit, no console window and no browser tabs, and nothing to remember on a Saturday morning beyond "double-click the thing I double-clicked last time". Questions it needs answering come up in the window, an **Activity** view shows everything it did (and saves it in `logs/` beside the app), and closing the window asks before it stops anything that's running. Everything setup asked can be changed later from **Settings** in the control panel, live. Everything it keeps — settings, match data, badges, sponsor logos — lives in the folder beside it, so one folder is the whole install (and the whole backup).

> **Two laptops?** The scorer's machine runs the very same file; on its first screen click
> *The scorer's laptop* (or *Set this computer up as an NV Play bridge* when the scorer is
> elsewhere). See [`TWO_LAPTOP_SETUP.md`](TWO_LAPTOP_SETUP.md).
>
> **On a Mac** the same screens open in the browser for now, with a Terminal window behind
> them; the control panel is a web page on every platform, so phones and tablets can use it.

If anything goes wrong, it tells you what and waits — these windows never close on an error you haven't read.

<details>
<summary>Every command, for the curious</summary>

Double-clicking runs the job this machine was set up for. From a terminal, each part can
also be run directly:

```
CricketStream setup            the setup page again (change the club's details or this machine's role)
CricketStream start            match day on the streaming laptop (--remote/--local, --manual/--nvplay)
CricketStream server           just the server: overlay, control panel, scoring page
CricketStream agent [folder]   scorer's laptop on the same wifi
CricketStream bridge           scorer's machine elsewhere (token-gated, over Tailscale)
CricketStream simulate         rehearse with a simulated match (--list for scenarios)
CricketStream obs-setup        build OBS's scenes and sources (OBS must be open)
CricketStream telemetry        log bandwidth, drops and match context to diagnostics/
CricketStream camera-encoder   read a Reolink camera's own encoder settings
CricketStream refresh-cam      reload the OBS camera source on a timer (RTSP drift)
CricketStream quality-test     drive the quality ladder down and back while live
```
</details>

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
- Compile a post-match highlights reel, make social clips and build the match page
- Get a recommended bitrate/resolution from a real upload-speed test
- Cut between camera angles from the pinned bar (or press `C`)
- Upload a sponsor logo from your phone and make its background transparent

The panel has four tabs, one per moment of the day — **Match day**, **Graphics & sponsor**,
**After the match** and **Setup** — under a pinned bar with the live status, Save, the
camera buttons and the match-day checklist.

---

## Features at a glance

| Feature | Requires |
|---|---|
| Manual ball-by-ball scoring from a phone | Nothing — just a browser at `/scoring` |
| Scorebar with batter/bowler names | NV Play output file *(or manual scoring)* |
| Ball-by-ball ticker | NV Play output file |
| Fall-of-wicket card with dismissal detail | NV Play output file |
| Player card with photo + season stats | NV Play output file + player photos (Pictures tab) + PlayCricket API key |
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
| YouTube broadcast manager + sponsor viewer-minutes | Google OAuth |
| Win predictor | PlayCricket API key (this season's scorecards) |
| Spoken commentary | Anthropic API key (for the line) — the voice itself is free and offline |
| AI camera spotter | Anthropic API key + OBS WebSocket |
| Social clips | FFmpeg + tagged replays (Anthropic key optional, for captions) |
| Match page | Nothing extra (richer with Anthropic, PlayCricket and YouTube) |
| Pre-match check, checklist, OBS crash recovery | Nothing extra |
| Club badges in scorebar | Badges added in the control panel's Pictures tab |

---

## Player photos (new in v2)

Player cards show a circular headshot next to the batter's name and season stats.

**Setup:** control panel → **Pictures** → Player photos → **Add photos…**, naming each
after the player. With the squad roster entered, the tab lists who still needs a photo,
with an **Add photo…** button beside each. Because NV Play provides surnames only, the
overlay matches names loosely, so you don't have to be exact — any of these names will
match a batter shown as "Smith":

```
Smith.jpg
smith.png
J_Smith.jpg
JOHN_SMITH.png
```

Square images around 400x400 work best. Supported formats: JPG, PNG, WebP. No restart
needed — add a photo and it appears the next time that batter comes in. If no photo is
found, the card simply shows initials instead.

The photos are kept in `headshots/` beside CricketStream; a club that keeps them elsewhere
can point Setup → **Where pictures are kept** at that folder.

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
matched by PlayCricket club ID. Add them in the control panel's **Pictures** tab: ours
once, and the opposition's from the **Today's opposition** button — the club ID and a short
abbreviation come from the day's fixture, so there's nothing to name or look up. If a badge doesn't match, you can also
pick either team's badge from a dropdown in the control panel. See [`CLUB_LOGOS.md`](CLUB_LOGOS.md)
for full instructions, including how to find opposition club IDs.

---

## Platform support

| | Windows | macOS |
|---|---|---|
| Full feature set | yes | yes |
| NV Play native | yes | Via VMware Fusion (free) |
| OBS streaming | yes | yes |
| Download | `CricketStream.exe` (x64; `CricketStream-arm64.exe` for Arm) | `CricketStream-mac.zip` (Apple Silicon + Intel) |

A Linux build (`CricketStream-linux`) is published too, for the scorer agent or bridge on
a spare machine, or for running the server headless.

For Mac users running NV Play: see [`MAC_VM_SETUP.md`](MAC_VM_SETUP.md) for running NV Play
inside a free Windows virtual machine while streaming natively from macOS.

---

## File structure

What you download is one file. Everything below it is created as you use it, beside the app:

```
CricketStream/
├── CricketStream.exe          The whole app (CricketStream + CricketStream.command on a Mac)
├── config.ini                 Your setup answers, API keys, OBS password (written by setup)
├── match_state.json           Control-panel settings (made on first run)
├── match_data.db              Ball-by-ball database
├── logos/                     Club badges (added in the Pictures tab)
├── headshots/                 Player photos (added in the Pictures tab)
├── socials/                   Match photos for social posts (Pictures tab)
│   ├── 1st/ 2nd/ 3rd/         Optional per-team photo folders
│   └── youth/                 Stock photos for youth posts (safeguarding)
├── sponsors/                  Sponsor logos (Pictures tab)
└── diagnostics/               Match-day telemetry CSVs
```

The overlay, control panel, scoring page, fonts and NV Play's scoreboard template are all
built into the app. **Put a file of the same name beside it and that copy is used
instead** — so `overlay.html` or `control.html` can still be edited and picked up with a
browser refresh, no rebuild and no restart.

The source repository:

```
/
├── cmd/cricketstream/         The app's entry point: role dispatch and subcommands
├── internal/
│   ├── server/                The stream server: every HTTP route, PCS parsing, OBS ops, the ball DB
│   ├── launcher/              Match day on the streaming laptop: fixture, OBS, server, pre-flight
│   ├── setup/                 The first-run setup page
│   ├── scoring/               The scorer's-book engine behind /scoring and the simulator
│   ├── simulate/              Match simulator — rehearse the whole broadcast, no scorer needed
│   ├── obs/ obsws/            OBS prep (config while closed), setup (scenes over WebSocket), the client
│   ├── agent/ bridge/         The scorer's-laptop agent and the token-gated NV Play bridge
│   ├── tools/                 Telemetry, camera encoder read-out, camera refresher, quality-ladder test
│   ├── resultcard/ matchpage/ socialclips/ winpredict/ voice/ logobg/   Post-match and extras
│   └── …                      Shared helpers (INI, Python-compatible formatting, assets)
├── overlay.html               OBS browser source overlay (1920x1080), built in
├── control.html               Operator control panel (served at /control), built in
├── scoring.html               Manual ball-by-ball scoring page (served at /scoring), built in
├── scoreboard.template        NV Play output template, built in
├── fonts/ brand/ graphics/    Built-in fonts, brand assets and OBS graphics
├── scripts/                   makebrand (brand assets), renderscorebar (scorebar screenshots)
├── config.example.ini         Every config.ini setting, documented
├── match_state.example.json   Settings template
└── docs/                      GitHub Pages landing page
```

> **`config.ini` and `match_state.json`** are git-ignored — they hold your API keys and
> OBS password once configured. The repo ships `config.example.ini` and
> `match_state.example.json` as safe templates; the setup page writes `config.ini` for you.

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

**v3.0.0 (beta)** — the whole app rewritten in Go as a single signed download: no Python,
no installer, no project folder, and the same file on every machine. It opens in its own
window: which computer is this, setup with a live preview of your stream, then a match-day
screen with the control panel in the same window. Behaviour, the overlay and the control
panel are unchanged; the tests were ported alongside, and parity with the Python original
is checked in CI. Released as betas until it has been through real matches — the proven
Python edition stays on the [`python` branch](../../tree/python) and its v2.12 release.
See [`CHANGELOG.md`](CHANGELOG.md) and [`PORTING.md`](PORTING.md).

**v2.12** — Social clips, an AI camera spotter, a live win predictor, optional spoken
commentary and an automatic match page, plus fixes from a full code review (a final-ball
boundary could replay twice, and a last-ball wicket could be credited to the next bowler).
See [`CHANGELOG.md`](CHANGELOG.md).

**v2.11** — A pre-match check that tries every login and connection for real days before a
match, and re-runs itself every 6 hours; camera buttons in the panel's pinned bar.

**v2.10** — An expired YouTube login now says so; sponsor viewer-minutes from YouTube's live
viewer count; one logo per sponsor on the result card; the old per-ball AI lower third
removed.

**v2.9** — A rebuilt Instagram result card in the style of county result graphics; the
simulator now behaves like real NV Play at the end of an over; the ball database no longer
misses the last ball of most overs; a match keeps its data when its id changes mid-innings.

**v2.8** — Camera cuts about 3.6x quicker over a held-open OBS socket (press `C` to flick),
one launcher (`CricketStream.exe`) for the streaming laptop, and OBS set up end to end
before it opens: WebSocket, replay buffer, video, encoder, Safe Mode prompt cleared.

**v2.7.3** — Fixed the one-click "add camera" OBS setup, found while wiring up a real new
camera: re-running it could silently drop the camera from the Replay scene, or fail outright,
because of a real OBS-websocket quirk around removing a source referenced by more than one
scene. It now updates the camera in place and keeps it in every scene that needs it. Full
detail in [`CHANGELOG.md`](CHANGELOG.md).

**v2.7.2** — Fixed a Windows-console encoding bug that could silently kill an in-flight
request the moment the server printed one of its own checkmarks or arrows. Full detail in
[`CHANGELOG.md`](CHANGELOG.md).

**v2.7.1** — A thin launcher exe for match day, an auto-restart loop when the server crashes
unexpectedly, automated stream-quality-ladder testing, CI compile-checking across all 13
top-level scripts, and `scorer_agent.py` now asks for the scoreboard folder right in its
console window if it can't find it automatically, instead of exiting and asking you to
re-run it from the command line. Full detail in [`CHANGELOG.md`](CHANGELOG.md).

**v2.7** — Two independent ways to run NV Play on a separate machine from the one doing OBS
and the stream, both verified on real two-machine hardware: `nvplay_bridge.py` (Tailscale,
token-gated, for machines that aren't on the same network) and `scorer_agent.py` (UDP
auto-discovery, for two laptops on the same club wifi). A Mac thermal early-warning check,
match-day diagnostic/camera tooling, and a pre-flight OBS bitrate sanity check round out a
run of fixes driven by real match-day failures. Full detail in [`CHANGELOG.md`](CHANGELOG.md).

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

## Get help / chat

Questions, match-day problems, or just want to show off your club's stream? Join the
**[CricketStream Discord](https://discord.gg/2nuBPj9rH7)** — the quickest way to reach the maintainer and other
clubs using it. Bugs that need fixing are still best raised as a
[GitHub issue](../../issues), so they don't get lost in the chat.

---

## Acknowledgements

Built by Bridestowe & Belstone CC, Devon Cricket League.
Scoring data via NV Play and the PlayCricket API (ECB).
AI features powered by Claude (Anthropic).
Weather data via Open-Meteo.
Streaming via OBS Studio.

---

## Code signing policy

Free code signing provided by [SignPath.io](https://about.signpath.io), certificate by
[SignPath Foundation](https://signpath.org).

The Windows downloads (`CricketStream.exe`, `CricketStream-arm64.exe`) are signed, so
Windows shows **SignPath Foundation** as the publisher; the file's own details name
Bridestowe & Belstone CC. They are built only by this repository's
[release workflow](.github/workflows/build-executables.yml), on GitHub's own build machines,
from the source code here — nothing built anywhere else is signed. Every signing request is
approved by hand by an approver before anything is signed.

| Role | Who |
|---|---|
| Committers and reviewers | <!-- SIGNPATH: list GitHub usernames --> *to be filled in before applying* |
| Approvers | <!-- SIGNPATH: list GitHub usernames --> *to be filled in before applying* |

Changes from anyone not listed as a committer are reviewed by a reviewer before they're
merged. Everyone listed uses two-factor sign-in for GitHub and SignPath.

## Privacy

CricketStream doesn't collect anything about you or your club, and sends nothing to its
authors, to SignPath, or to anyone else on its own account: no analytics, no tracking.
Everything it keeps — settings, match data, photos, logs — stays in the folder beside
the app (and its window's cache in `%LOCALAPPDATA%\CricketStream`).

It talks to these services, only to do what you've asked it to:

| Service | When | What it sends |
|---|---|---|
| PlayCricket (play-cricket.com, resultsvault.co.uk) | Fetching today's match, scorecards, season stats | Your club ID and API key |
| YouTube (Google) | Only if you sign in to YouTube from the control panel | The stream title and settings you choose |
| Anthropic (Claude) | Only if you add an AI key and use the AI features | Match facts and figures for commentary and reports; with the camera spotter switched on, camera snapshots |
| Open-Meteo | The weather graphic | Your ground's location |
| Cloudflare | The upload speed test; a Cloudflare Tunnel only if you switch one on | Test data; the control panel's traffic through the tunnel |
| GitHub | Checking for a newer version | Nothing beyond the request itself |

Each service's own privacy policy covers what it does with what it receives.

## What CricketStream changes on your computer

CricketStream is a single file with no installer. Everything it does to the computer is said
in its window (and the activity log in `logs/`) as it happens:

- **OBS** (on the streaming laptop, when it starts match day): switches on OBS's WebSocket
  server with a password, the replay buffer and the video settings for your connection;
  picks a hardware encoder if OBS is using the software one; adds its scenes and sources
  (Main, Replay, the Overlay); and pauses OBS's update check during match day, switching it
  back on afterwards. `[OBS] manage_obs = no` in `config.ini` leaves OBS alone.
- **NV Play**: puts `scoreboard.template` in NV Play's Templates folder (an older copy is
  kept as `scoreboard.template.old`).
- **A desktop shortcut**, if you leave that ticked on the first setup.
- **Windows Firewall**: Windows itself asks whether CricketStream may use the network; you
  choose.

**Removing it:** close CricketStream, then delete its folder, the desktop shortcut and
`%LOCALAPPDATA%\CricketStream`. Its OBS scenes stay in OBS until you delete them there, and
NV Play's template until you delete it from NV Play's Templates folder.

---

## Licence

See the [`LICENSE`](LICENSE) file in the repository. Free to use, modify, and distribute for your club.
