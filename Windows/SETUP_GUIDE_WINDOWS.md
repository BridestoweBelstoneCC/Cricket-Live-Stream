# Setup Guide — Windows
## CricketStream Overlay — Version 2.10

---

## The fast path — up and running in 2 steps

Done this before, or just want to get going? This is the whole job; everything below is the detail.

1. **Once** — download `CricketStream.exe` from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest) and put it **anywhere inside the folder you unzipped** (either here in `Windows\`, or up beside `server.py` — both work).
2. **Every time, including the first** — double-click it.

That's the whole thing. It works out what still needs doing: on the first run it installs
Python if it's missing, installs the packages, and asks you the setup questions to create
`config.ini`; on every run after that it goes straight to starting the match — today's
fixture, season stats, pre-flight check. Control panel: `http://localhost:5000/control` ·
Overlay (for OBS): `http://localhost:5000/overlay`

> Prefer running from source because you already have Python? `setup.bat` once, then
> `quickstart.bat` each match day — same code, same result.

Add the overlay as a 1920×1080 **Browser source** in OBS and you're live. Full OBS setup, replays, AI features, and troubleshooting follow below.

---

## Before you start

You will need:
- A Windows laptop or PC
- A camera connected to your laptop (USB webcam or HDMI camera via capture card)
- OBS Studio installed — https://obsproject.com
- Python 3 — installed automatically by `CricketStream.exe` (see Step 2), or get it yourself from https://python.org/downloads and tick **"Add Python to PATH"**
- A YouTube account with Live Streaming enabled
- NV Play installed on the scorer's laptop

---

## Step 1 — Extract the files

Unzip `cricketstream_windows.zip` to a permanent location. Somewhere like:
```
C:\Users\YourName\Documents\CricketStream\
```

Do not put it on the Desktop — Windows sometimes blocks scripts running from there.

---

## Step 2 — Install Python packages and configure

Don't have Python installed yet? Download `CricketStream.exe` from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest), put it inside the folder you unzipped, and double-click it — it installs Python automatically before continuing. Already have Python? Double-click **`setup.bat`** instead; it's the same wizard, run from source.

> **Where exactly does the .exe go?** Anywhere inside the unzipped project — this `Windows\` folder or the one above it (where `server.py` lives) both work; it looks in both. What it can't do is run from Downloads or the Desktop on its own, because the project files it needs aren't there. If you get that wrong it now tells you so and waits, rather than closing.

The setup wizard installs packages, then asks a few questions — your club name, kit colour, PlayCricket ID, and any API keys you have. It creates `config.ini` for you automatically.

```
  ====================================================
        CricketStream Overlay -- First-time Setup
  ====================================================
  --- Installing packages ---------------------------
  Running: pip install -r requirements.txt
  [OK] Packages installed.
  --- Club details ----------------------------------
  Club name  (required):
```

If package installation fails, try right-clicking `setup.bat` (or the exe) and selecting **Run as administrator**.

You only need to do this once per laptop.

> **Prefer to configure manually?** Run `install.bat` to install packages, then follow Step 3 to fill in `config.ini` by hand.

---

## Step 3 — Review or edit config.ini (optional)

If you ran `setup.bat`, your `config.ini` was created automatically — you can skip straight to Step 4.

To review settings or make changes later, open `config.ini` in Notepad (right-click → Open with → Notepad).

The main settings are:

```ini
[Club]
name = Your Club CC               ← Your full club name
abbreviation = YCC                ← Up to 6 characters for the scorebar
home_colour = #1a3a5c             ← Your kit colour in hex
playcricket_id = 12345            ← Your club ID from play-cricket.com

[API]
playcricket_key = YOUR_KEY_HERE   ← Your PlayCricket API key

[Scoring]
pcs_output_folder = C:/Users/Scorer/Documents/Cricket Matches/_Scoreboards/Output
                                  ← Output folder from NV Play (see Step 5)

[OBS]
obs_password =                    ← Leave blank — filled in for you on first run
replay_folder = C:/Users/You/Videos/Replays
                                  ← Where OBS saves replay clips

[Stream]
youtube_title = LIVE: {home} vs {away}
max_overs = 50

[AI]
anthropic_key =                   ← Optional: powers commentary, match report, social posts

[Scoring]
headshots_folder =                ← Optional: folder of player photos (default: headshots/)
socials_folder =                  ← Optional: folder of match photos for social posts
drinks_over = 25                  ← Over at which the drinks-break weather appears
```

> The `[AI]` key is optional. You can also paste it later in the control panel.
> Leave it blank and the AI features simply stay switched off.

**Finding your PlayCricket club ID:**
Go to play-cricket.com → find your club page → the number in the URL is your club ID.

**Finding your kit colour:**
Go to htmlcolorcodes.com, pick your colour, and copy the hex code (e.g. `#1a3a5c`).

Save and close config.ini when done.

---

## Step 4 — Set up OBS

### OBS WebSocket and replay buffer — done for you

**There's nothing to switch on in OBS any more.** Every time quickstart runs it:

- switches on OBS's WebSocket server (how this software talks to OBS) with a random
  password, and writes that password into `config.ini` for you;
- turns on the replay buffer (25 seconds) and points it at your replay folder;
- opens OBS, so you don't need to open it first.

If OBS is already open with the WebSocket server switched off, quickstart asks you to
close OBS and carries on once you have: OBS saves its settings when it closes,
so they can only be changed while it's shut. If OBS already has a WebSocket server set up,
it's left exactly as it is and its password is reused.

To check it worked: the **Controls** panel in OBS should show **Stop Replay Buffer**.

Running OBS on a different computer, or would rather set it up yourself? Put
`manage_obs = no` under `[OBS]` in `config.ini`, then do the steps below by hand.

<details>
<summary>Doing it manually (only with manage_obs = no, or if the above didn't work)</summary>

**WebSocket server:**

1. Open OBS
2. Go to **Tools → WebSocket Server Settings**
3. Tick **Enable WebSocket server**, port `4455`
4. Tick **Enable Authentication** and set a password
5. Copy that password into `config.ini` under `obs_password`
6. Click OK

**Replay buffer:** quickstart can still switch it on over the WebSocket, but OBS only
creates the buffer when it starts. The first time, you'll see
`⚠ Replay buffer enabled but wouldn't start yet — restart OBS once and re-run setup`:
close OBS, open it again, and run quickstart again. Or tick it yourself:

1. OBS → **Settings → Output**
2. Set Output Mode to **Advanced**
3. Click the **Recording** tab
4. Scroll down to **Replay Buffer** — tick **Enable**
5. Set Maximum Replay Time to **25 seconds**
6. Click OK

</details>

### Video settings and bitrate — done for you

Quickstart sets these every time it opens OBS:

- **Canvas (Base Resolution): 1920×1080**, always. The overlay graphics are drawn at that
  size, so any other canvas puts them in the wrong place.
- **Output resolution and frame rate:** from your last upload-speed test (the control
  panel's **Stream Health Check** runs one the first time it sees OBS). 1080p on a good
  connection, 720p otherwise, at 30fps. Before the first test: 720p at 30fps.
- **Bitrate:** from the same test, comfortably under your measured upload speed.

To pin your own choices, set them in `config.ini` under `[Stream]` rather than in OBS
(OBS's own settings get replaced next time quickstart runs): `output_resolution = 720p`
or `1080p`, `fps = 25` or `30`, `bitrate_kbps = 2500`. Set `output_resolution = manual`
or `bitrate_kbps = manual` to have them left alone entirely.

### Encoder (the one OBS setting left to you)

1. OBS → Settings → Output → **Streaming** tab
2. Video Encoder: a **hardware** encoder (NVIDIA NVENC, AMD, or Intel QuickSync) if
   your PC has one, otherwise **x264**
3. Click OK

Not sure? The control panel's **Stream Health Check** test-records with your encoder and
with x264 and tells you which dropped fewer frames on this laptop.

---

## Step 5 — Set up NV Play (scorer's laptop)

The scorer needs to do this once before the first match.

1. Open NV Play on the scorer's laptop
2. Go to **Tools → Configuration → Scoreboard**
3. Tick **Enable Scoreboard Output**
4. Leave the **Output Folder** as NV Play's own (`...\Cricket Matches\_Scoreboards\Output`)
   unless you have a reason to change it
5. Click the **Template File** browse button, open NV Play's `Templates` folder and
   select **`scoreboard.template`**
6. Click OK

You don't copy the template or the folder path anywhere by hand any more:

- **NV Play on this laptop:** setup puts `scoreboard.template` into NV Play's Templates
  folder for you, and finds the output folder itself (it asks you to confirm it).
- **NV Play on a separate scoring laptop:** `CricketStreamScorerAgent.exe` does both on
  that laptop — see `TWO_LAPTOP_SETUP.md`.

If step 5 shows no `scoreboard.template`, run setup (or the scorer agent) first, then
come back to it. To do it by hand instead, copy `scoreboard.template` from the
CricketStream folder into
`C:\Users\[ScorerName]\Documents\Cricket Matches\_Scoreboards\Templates\`.

**Note:** The template folder and output folder are different locations — the template goes in `\Templates\`, the data comes out of `\Output\`.

---

## Step 6 — First run

Double-click **`quickstart.bat`**.

You will see something like:
```
  ✓ Club: Your Club CC
  ✓ All packages present
  ✓ Match found: Your Club CC vs Example Opposition CC
  ✓ Competition: Your League — Division 1
  ✓ Umpires: M. Davies / G. Allan
  ✓ match_state.json written
  ✓ Connected to OBS
  ✓ Scene 'Main' created
  ✓ Scene 'Replay' created
  ✓ Overlay browser source created in Main
  ✓ ReplayClip media source created in Replay
  ✓ Replay buffer started
  ─────────────────────────────────────────
  Ready: Your Club CC vs Example Opposition CC
  Control panel: http://localhost:5000/control
  Overlay:       http://localhost:5000/overlay
```

Open `http://localhost:5000/control` in your browser. You should see the control panel with today's match already filled in.

---

## Step 6a — Choose your scorebar style (optional)

The score strip along the bottom comes in four looks. All of them use your club colours —
the difference is the styling.

| Style | Looks like | Good for |
|---|---|---|
| **Classic** | Light grey panels — the original | Plain and very legible; the safe default |
| **Modern** | Dark broadcast slab, team-colour rule along the top | A more "TV" feel without shouting |
| **Impact** | Bold angled cuts, white score plate, heavy type | The easiest to read on a phone |
| **Minimal** | Clean white with lots of space | Bright, sunny daytime pictures |

Open the control panel (`http://localhost:5000/control`), find the **Graphics** card, and
use **Scorebar style**. It shows a **live preview** right there using your own team names
and colours, so you can see each one before committing. Click **Save** when you're happy.

You can change it whenever you like — even mid-match. The overlay picks it up on its next
poll, a few seconds later, with no restart.

The setup wizard also asks which one you want, so this is only if you skipped it or changed
your mind.

---

## Step 7 — Player photos (optional, new in v2)

When a new batter comes in, the overlay can show a player card with their photo and
season stats. To enable photos:

1. Create a folder called `headshots` inside your CricketStream folder (next to `server.py`).
2. Add player photos named by **surname** — for example `Smith.jpg`. Square images around
   400x400 pixels look best. JPG, PNG and WebP all work.
3. That's it. The next time that batter comes in, their photo appears on the card.

Because NV Play only gives surnames, the overlay accepts several filename patterns, so
any of these match a batter shown as "Smith": `Smith.jpg`, `smith.png`, `J_Smith.jpg`,
`JOHN_SMITH.png`. If no photo is found the card shows the player's initials instead —
it never breaks the graphic.

No restart is needed — drop a file in and refresh the Overlay source in OBS.

---

## Step 8 — AI features (optional, new in v2)

A single Anthropic API key unlocks three AI features:

- **Over commentary** — a line of analysis at the end of each over
- **Match report** — a full written report generated after the game
- **Social posts** — ready-to-paste posts for your club's channels

To set up:

1. Go to **console.anthropic.com** and create an API key (starts with `sk-ant-`).
2. Paste it into the control panel → **AI Commentary** card → **Anthropic API key**,
   or into `config.ini` under `[AI] anthropic_key`.
3. In the **Graphics** card, turn on **AI commentary (end of over)** if you want live
   commentary. The match report and social posts are generated on demand after the match.

Costs are tiny — a few pence for a whole match. Leave the key blank and everything else
still works; the AI features simply stay off.

---

## Match day procedure

### A few days before

Start the server (`quickstart.bat`), open the control panel → **Setup** → **Run pre-match
check**. It tries every login and connection for real — YouTube, PlayCricket, the AI key,
OBS, the scorer feed, disk space — and says what to fix. ⚠ on OBS and the scorer feed is
normal days before; anything marked ✗ needs fixing before the match. A YouTube login that
has expired shows up here, not on the day.

### Before the match (30 minutes before)

1. Double-click `quickstart.bat` — everything configures automatically
2. Open `http://localhost:5000/control` and work down the **Match-day checklist** at the
   top. It ticks itself as things are ready; anything that isn't has a button that does
   it (Start OBS, Add camera to OBS, Start replay buffer, Fetch today's match, Go live).
   Also check Demo mode is **OFF** (green)
3. In OBS (quickstart opens it for you), check the preview shows your camera with the overlay
4. In OBS Controls, verify **Stop Replay Buffer** is showing (buffer is running)

### When the scorer starts

5. Scorer opens NV Play and starts the match
6. Scorer selects opening batsmen and opening bowler
7. First ball is bowled — the **PCS Pro Live Data Feed** in the control panel goes green
8. Batter names, bowler figures, and score appear on the scorebar

### Going live

9. In OBS, click **Start Streaming**
10. The stream title updates on YouTube automatically

### After the match

11. Click **Stop Streaming** in OBS
12. In the control panel's **After the match** tab → **Match Report & Social Posts**:
    - Click **Generate match report** for a full written report (edit it, then copy)
    - Click **Generate social post** for a ready-to-paste social media summary
    - The match figures are saved as the match goes, so this still works if the server
      was restarted during the match
13. Same tab → **Instagram Result Graphic** → **Generate Instagram graphic** for the
    result card (pick a backdrop photo first if you like), then copy the caption
14. Click **Compile highlights reel** to create the post-match video
15. Close the command prompt window to stop the server (you'll also be prompted to save
    the match report automatically)

---

## Troubleshooting

### The window opens, flashes something, and closes before I can read it

This shouldn't happen any more — every one of these files now stops and waits with
`Press Enter to close this window...` whatever goes wrong, including an outright crash.
If you're seeing it, you're on an older copy: download the current version and try again.

To read the message on an old copy, open Command Prompt (press Start, type `cmd`), then
drag the file you were double-clicking into the window and press Enter. It runs the same
way but the window stays open.

### It says "I can't find the CricketStream project files"

The file needs to live inside the folder you unzipped. It looks in its own folder and the
one above it, so both `Windows\` and the main project folder are fine — but Downloads or
the Desktop on their own aren't, because `server.py` and the rest aren't there.

Move the file next to `server.py` and run it again. The message tells you which folder it
actually looked in, which is usually enough to spot what happened.

### Quickstart says "No config.ini yet — setup hasn't been run"

`config.ini` holds your club details and is created once, by the setup wizard. Run
`CricketStream.exe` or `setup.bat` first.

If you're sure you already ran setup, it saved `config.ini` somewhere else — an older
version wrote it next to itself rather than next to `server.py`. Find it and move it into
the folder with `server.py`.

### OBS crashed during the match

You shouldn't need to do anything. The server notices within a few seconds, reopens OBS
(without the Safe Mode question), switches back to the Main scene, and restarts the stream
if it was live. The black server window shows `⚠ OBS crashed — reopening it`. If OBS
crashes three times inside half an hour, it stops reopening it and says so: something
needs looking at (OBS → Help → Log Files).

Closing OBS yourself is different. It stays closed, so you can shut down normally after
the match.

If OBS **freezes** ("Not Responding") for a minute and the stream has stopped getting out,
it's closed and reopened the same way. A frozen OBS window while the stream is still going
out is left alone, because closing it would stop the stream; the server window says so.

### Quickstart says "Cannot connect to OBS"

- Close OBS and run quickstart again — it opens OBS itself with the WebSocket server on,
  and skips OBS's Safe Mode question after a crash (Safe Mode switches the WebSocket
  server off, which used to be the usual cause of this)
- **Opened OBS yourself and it offered Safe Mode?** If OBS didn't shut down cleanly last
  time, it asks *"Run in Safe Mode (third-party plugins, scripting, and **WebSockets
  disabled**)?"*. Choose **Run in Normal Mode**, or close it and let quickstart open it.
- With `manage_obs = no`: check the WebSocket password in `config.ini` matches OBS →
  Tools → WebSocket Server Settings, and the port is 4455 in both places

### PCS monitor says "Widget" not "PCS"

- Check the output folder path in `config.ini` matches exactly what NV Play shows
- Make sure the scorer has scored at least one ball (NV Play only writes the file after the first delivery)
- Go to `http://localhost:5000/pcs/debug` — it shows exactly what the server can see

### Overlay not showing in OBS

- OBS → right-click the Overlay browser source → **Refresh**
- Make sure the command prompt window (server) is still open
- Check the URL in browser source properties is `http://localhost:5000/overlay`

### Overlay goes grey during the stream

- Right-click Overlay source → **Properties**
- Make sure **"Shutdown source when not visible"** is **unticked**
- Make sure **"Refresh browser when scene becomes active"** is **unticked**
- To recover immediately: right-click → **Refresh**

### Stream is choppy or dropping frames

- OBS → Settings → Output → change preset from **veryfast** to **superfast**
- Close all other applications during the stream, including the control panel browser tab
- In `config.ini` under `[Stream]`, set `output_resolution = 720p` and/or
  `bitrate_kbps = 1500`, then run quickstart again. (Changing these in OBS's own settings
  only lasts until the next quickstart, which sets them again.)

### "Match not found" on quickstart

- The match may not be published on PlayCricket yet — enter the opposition manually in the control panel
- Check your PlayCricket API key is correct in `config.ini`
- The API only works from the laptop you registered with ECB/PlayCricket

### Replay not triggering

- Make sure **Stop Replay Buffer** is showing in OBS controls (buffer must be running)
- Check the OBS password in `config.ini`
- Scene names must be exactly `Main` and `Replay` (capital first letter, case sensitive)
- The media source must be named exactly `ReplayClip`

---


## Club Badges (optional)

Small circular club badge icons appear next to team names in the scorebar.
They are matched automatically by PlayCricket club ID.

**First-time setup:**

1. Create a `logos/` folder inside your CricketStream folder (alongside `server.py`)
2. Find your PlayCricket club ID — it's the `playcricket_id` value in `config.ini`
3. Save your club badge as `logos\{your_id}.png` — for example `logos\12345.png`
4. Restart the server — your badge appears on the left of the scorebar

**Adding opposition badges:**

1. Click **Fetch today's match** in the control panel
2. The match details show the away club information — note the club name
3. Find that club's PlayCricket ID (number in their play-cricket.com URL)
   — or check `http://127.0.0.1:5000/state` and look for `away_club_id`
4. Save their badge as `logos\{away_club_id}.png`
5. Right-click the Overlay source in OBS → **Refresh** — badge appears immediately

**Badge status in the control panel:**

The Kit Colours card shows a live badge status panel with two slots — home and away.
Each slot shows a preview of the badge if found (green tick) or an amber warning
if the file is missing, so you can see at a glance what's ready before going live.

**Custom logos folder:**

If your badge files are stored elsewhere, set the path in:
- Control panel → Kit Colours → **Logos folder** field, or
- `config.ini` → `[Scoring]` → `logos_folder = C:/path/to/your/logos`

Supported formats: PNG (recommended, use transparent background), SVG, WebP, JPG.

See **`CLUB_LOGOS.md`** for full instructions and tips on finding club badges.

---
## Quick reference

| URL | Purpose |
|---|---|
| `http://localhost:5000/control` | Control panel |
| `http://localhost:5000/overlay` | Overlay (add to OBS, don't open in browser) |
| `http://localhost:5000/pcs/debug` | Diagnose NV Play connection |
| `http://localhost:5000/live` | Raw live data feed (JSON) |

---

## Files in this package

| File | Purpose | Edit? |
|---|---|---|
| `config.ini` | Your club settings | ✅ Once (created by setup.bat) |
| `setup.bat` | First-time setup wizard (installs packages + creates config) | Run once |
| `install.bat` | Manual package install (alternative to setup.bat) | Run once |
| `quickstart.bat` | Starts everything | Run each match day |
| `server.py` | Main server | Never |
| `overlay.html` | OBS overlay graphics | Never |
| `obs_setup.py` | OBS auto-configuration | Never |
| `scoreboard.template` | NV Play template | Installed into NV Play for you |
| `headshots/` | Player photos (new in v2) | Add your players |
| `socials/` | Match photos for social posts (new in v2) | Optional |
| `logos/` | Club badges (named by club ID) | Add your badges |
| `SETUP_GUIDE_WINDOWS.md` | This guide | — |
