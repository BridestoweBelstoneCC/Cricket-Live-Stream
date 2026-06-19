# CricketStream Overlay

**Professional live stream overlays for grassroots cricket clubs — free, open source, and built for volunteers.**

**Version 2.1** · Works on Windows and macOS · [What's new →](WHATS_NEW_V2.md)

---

## Version 2.0 highlights

Version 2.0 turns the original scorebar into a full broadcast layer:

- **Player cards** with photos and **season batting stats** — for your players *and* the opposition, pulled live from PlayCricket.
- **Squad roster** (shirt numbers) so brothers, same-surname players, and players with duplicate PlayCricket accounts always resolve to the right photo and the right stats.
- **Worm chart** — cumulative runs by over for both innings, drawn in each team's colours with wicket markers.
- **Full-word dismissals** on the wicket card (*Caught Jones Bowled Smith*, not *c Jones b Smith*).
- **AI over commentary, match reports, and social posts**, powered by Claude.
- **Automatic club badges** matched by PlayCricket club ID, with a manual picker in the control panel.
- **Drinks-break weather**, smarter over-summary timing, and a much more robust update loop.

See [`WHATS_NEW_V2.md`](WHATS_NEW_V2.md) for the full list and upgrade notes.

---

## The problem

Professional cricket broadcast graphics cost thousands of pounds a season. Smaller clubs either go without, or settle for a static scoreboard that tells viewers nothing about what's actually happening on the pitch.

CricketStream Overlay changes that. It gives any club — regardless of budget — the same quality of live graphics you see on broadcast cricket, driven by the scoring software your scorer already uses.

**All you need is a camera.**

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
- **Auto-detected moments** *(new in v2.1)* — the over summary spots the storylines by itself: a gold strap fires for season-best scores (*"Ewen 67\* — his best score of the season!"*) and team milestones (*"100 up in 14 overs"*), and the same facts are fed to the AI commentator so the spoken line builds around them.
- **"At this stage"** *(new in v2.1)* — in the second innings, each over summary compares the chase with the first innings at the same point (*"Heathcoat were 67-2 at this stage"*).
- **Bowler spell tracker** *(new in v2.1)* — once a bowler has bowled consecutive overs from the same end, the over card adds *"This spell: 5-1-18-2"*.
- **Full innings scorecard** *(new in v2.1)* — a broadcast-style card at the innings break: all eleven batters with dismissals spelled out, not-out batters highlighted, and bowling figures alongside. Also available on demand from the control panel. Requires the v2.1 scoreboard template.
- **DLS par score** *(new in v2.1)* — when rain is forecast at the ground, a small pill above the scorebar shows the Duckworth-Lewis par score and whether the chasing side is ahead (green) or behind (red). Standard Edition approximation — a guide, not an official calculation.
- **Partnership display** — live partnership runs and balls.
- **Worm chart** — two-innings cumulative run chart ("the worm"), each innings drawn as a climbing line in its team's colours with red wicket markers and the running total labelled at the head of each worm.
- **Player milestones** — automatic graphics for 50s and 100s.
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

### Automation

- **PlayCricket integration** — fetches today's fixture automatically, filling in opposition name, competition, umpires, and ground.
- **NV Play / PCS Pro integration** — reads the scorer's output file directly, giving ball-by-ball data with batter names, bowler figures, run rate, and dismissal details.
- **OBS auto-setup** — configures scenes, sources, and the replay buffer automatically on first run.
- **YouTube title updater** — updates the stream title automatically when the match starts.
- **Weather widget** — current conditions at the ground on demand or at the drinks break.

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

---

## Requirements

**Hardware:**
- Any laptop or PC running Windows or macOS
- A camera (HDMI camera via capture card, or USB webcam)
- A stable internet connection for streaming

**Software (all free):**
- [OBS Studio](https://obsproject.com) — streaming software
- [Python 3](https://python.org/downloads) — runs the server
- [NV Play](https://www.play-cricket.com/website/np_downloads) — scoring software (Windows) or PCS Pro

**Optional:**
- A PlayCricket API key — for automatic match detection
- An Anthropic API key — for AI over commentary, match reports, and social posts (a few pence per match)

---

## Quick start

New to this? Read your platform's quick start first:

- **Windows:** [`SETUP_GUIDE_WINDOWS.md`](SETUP_GUIDE_WINDOWS.md) — starts with a 3-step fast path, full detail below it
- **macOS:** [`SETUP_GUIDE_MAC.md`](SETUP_GUIDE_MAC.md) — starts with a 4-step fast path, full detail below it
- **No coding experience at all?** [`FOR_NON_TECHNICAL_USERS.md`](FOR_NON_TECHNICAL_USERS.md) walks you through every step in plain English.

The short version:

1. **Install Python** from [python.org](https://python.org/downloads). On Windows, tick **"Add Python to PATH"**.
2. **Install packages** — Windows: double-click `install.bat`. Mac: run `install.sh`.
3. **Configure** — edit `config.ini` with your club name, colour, PlayCricket ID, and the scorer's output folder.
4. **Run** — Windows: double-click `quickstart.bat`. Mac: run `quickstart.sh`. Then open the control panel in your browser.

---

## Control panel

Once running, open the control panel in any browser on the same machine
(`http://localhost:5000/control` on Windows, `http://127.0.0.1:5000/control` on Mac).

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

---

## Features at a glance

| Feature | Requires |
|---|---|
| Scorebar with batter/bowler names | NV Play output file |
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

NV Play only sends surnames, so two brothers both show as "Ewen", and a player with two
PlayCricket accounts can appear twice in the stats. The squad roster solves both.

In the control panel, open **Squad Roster** and enter one player per line, mapping their
**shirt number** to their **full name**:

```
21 = John  Smith
28 = Steve Smith
14 = Bob Smith
```

The PCS Pro template sends each batter's shirt number alongside their name, so the overlay
uses the number to look up the correct full name — and from there the correct photo and the
correct season stats. Brothers no longer get mixed up, and where a player has more than one
PlayCricket account the stats use their most-played (regular) account automatically.

You only need to do this once per season, and only for players whose surname is shared or
ambiguous. Everyone else resolves fine by surname alone. For this to work, the scorer's
NV Play / PCS Pro squad must have shirt numbers assigned, and you must deploy the v2
`bbcc_scoreboard.template` (see the upgrade notes in `WHATS_NEW_V2.md`).

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
badge images named by club ID (e.g. `29434.png`). The opposition's club ID and a short
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

For Mac users running NV Play: see [`MAC_VM_SETUP.md`](MAC_VM_SETUP.md) for running NV Play
inside a free Windows virtual machine while streaming natively from macOS.

---

## File structure

```
/
├── server.py                  Main server — runs everything
├── overlay.html               OBS browser source overlay (1920x1080)
├── quickstart.py              Auto-setup script
├── quickstart.bat / .sh       Launchers (Windows / Mac)
├── obs_setup.py               OBS auto-configuration
├── install.bat / .sh          Package installers
├── requirements.txt           Python package list
├── config.ini                 Club configuration — edit this
├── match_state.json           Auto-generated match settings
├── bbcc_scoreboard.template   NV Play output template
├── README.md                  This file
├── WHATS_NEW_V2.md            Version 2 release notes
├── SETUP_GUIDE_WINDOWS.md     Full Windows setup + troubleshooting
├── SETUP_GUIDE_MAC.md         Full Mac setup + troubleshooting
├── MAC_VM_SETUP.md            Mac + VMware Fusion guide
├── FOR_NON_TECHNICAL_USERS.md Plain-English guide for volunteers
├── CLUB_LOGOS.md              Club badge guide
├── logos/                     Club badge images (named by PlayCricket club ID)
├── headshots/                 Player photos (named by surname)
├── socials/                   Match photos for social posts
└── sponsors/                  Sponsor logo images
```

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

**v2.1** — Broadcast intelligence update. Auto-detected moments (season-best scores, team
milestones) on the over summary and woven into the AI commentary; second-innings "at this
stage" comparison; full innings scorecard at the break (requires the v2.1 scoreboard
template); bowler spell tracker; DLS par score pill when rain is forecast; broadcast
animation polish (spring entries, sweeping boundary banners, milestone count-ups); a
control-panel health strip and a quickstart pre-flight check; threaded server; hardened
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

Contributions welcome. If you add support for additional scoring software, fix bugs, or
improve the overlay design, please open a pull request. If your club uses this and improves
it for your own needs, please share the changes so other clubs can benefit.

---

## Acknowledgements

Built by Bridestowe & Belstone CC, Devon Cricket League.
Scoring data via NV Play and the PlayCricket API (ECB).
AI features powered by Claude (Anthropic).
Weather data via Open-Meteo.
Streaming via OBS Studio.

---

## Licence

See the `LICENCE` file in the repository. Free to use, modify, and distribute for your club.
