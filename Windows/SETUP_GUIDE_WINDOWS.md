# Setup Guide — Windows
## CricketStream Overlay — Version 2.1

---

## The fast path — up and running in 3 steps

Done this before, or just want to get going? This is the whole job; everything below is the detail.

1. **Install Python** — get Python 3 from https://python.org/downloads (**tick "Add Python to PATH"**). *(Once only.)*
2. **Setup** — double-click **`setup.bat`**. It installs packages and walks you through your club details, creating `config.ini` for you. *(Once only.)*
3. **Match day** — double-click **`quickstart.bat`**. It finds today's fixture, starts everything, pulls season stats, and runs a pre-flight check. Control panel: `http://localhost:5000/control` · Overlay (for OBS): `http://localhost:5000/overlay`

Add the overlay as a 1920×1080 **Browser source** in OBS and you're live. Full OBS setup, replays, AI features, and troubleshooting follow below.

---

## Before you start

You will need:
- A Windows laptop or PC
- A camera connected to your laptop (USB webcam or HDMI camera via capture card)
- OBS Studio installed — https://obsproject.com
- Python 3 installed — https://python.org/downloads
  - During installation, tick **"Add Python to PATH"**
- A YouTube account with Live Streaming enabled
- NV Play installed on the scorer's laptop

---

## Step 1 — Extract the files

Unzip `bbcc_stream_windows.zip` to a permanent location. Somewhere like:
```
C:\Users\YourName\Documents\BBCC Stream\
```

Do not put it on the Desktop — Windows sometimes blocks scripts running from there.

---

## Step 2 — Install Python packages and configure

Double-click **`setup.bat`**.

The setup wizard installs packages, then asks a few questions — your club name, kit colour, PlayCricket ID, and any API keys you have. It creates `config.ini` for you automatically.

```
  ====================================================
        CricketStream Overlay -- First-time Setup
  ====================================================
  --- Step 1 — Installing packages ------------------
  Running: pip install -r requirements.txt
  [OK] Packages installed.
  --- Step 2 — Club details -------------------------
  Club name  (required):
```

If package installation fails, try right-clicking `setup.bat` and selecting **Run as administrator**.

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
obs_password = CHANGE_ME          ← Password you set in OBS WebSocket settings
replay_folder = C:/Users/You/Videos/Replays
                                  ← Where OBS saves replay clips

[Stream]
youtube_title = LIVE: {home} vs {away} — DCL 2026
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

### Enable OBS WebSocket

1. Open OBS
2. Go to **Tools → WebSocket Server Settings**
3. Tick **Enable WebSocket server**
4. Port: `4455`
5. Tick **Enable Authentication** and set a password
6. Copy that password into `config.ini` under `obs_password`
7. Click OK

### Enable Replay Buffer

1. OBS → **Settings → Output**
2. Set Output Mode to **Advanced**
3. Click the **Recording** tab
4. Scroll down to **Replay Buffer** — tick **Enable**
5. Set Maximum Replay Time to **25 seconds**
6. Click OK

### Output settings (for streaming)

1. OBS → Settings → Output → **Streaming** tab
2. Encoder: **x264** (or NVIDIA/AMD hardware encoder if your PC has one)
3. Rate Control: **CBR**
4. Bitrate: **2500 Kbps** (reduce to 2000 if stream is choppy)
5. Preset: **veryfast**
6. Click OK

### Video settings

1. OBS → Settings → Video
2. Base Resolution: **1920×1080**
3. Output Resolution: **1280×720** (or 1920×1080 if your laptop is powerful)
4. FPS: **30** (or 25 for UK YouTube)
5. Click OK

---

## Step 5 — Set up NV Play (scorer's laptop)

The scorer needs to do this once before the first match.

1. Open NV Play on the scorer's laptop
2. Go to **Tools → Configuration → Scoreboard**
3. Tick **Enable Scoreboard Output**
4. Set the **Output Folder** — note this path exactly
5. Click the **Template File** browse button
6. Navigate to NV Play's Templates folder:
   ```
   C:\Users\[ScorerName]\Documents\Cricket Matches\_Scoreboards\Templates\
   ```
7. Copy `bbcc_scoreboard.template` (from the BBCC Stream folder) into that Templates folder
8. Select `bbcc_scoreboard.template` as the Template File
9. Click OK

Paste the output folder path from step 4 into `config.ini` under `pcs_output_folder`.

**Note:** The template folder and output folder are different locations — the template goes in `\Templates\`, the data comes out of `\Output\`.

---

## Step 6 — First run

Double-click **`quickstart.bat`**.

You will see something like:
```
  ✓ Club: Your Club CC
  ✓ All packages present
  ✓ Match found: Your Club CC vs Okehampton CC
  ✓ Competition: Devon Cricket League — A Division
  ✓ Umpires: M. Davies / G. Allan
  ✓ match_state.json written
  ✓ Connected to OBS
  ✓ Scene 'Main' created
  ✓ Scene 'Replay' created
  ✓ Overlay browser source created in Main
  ✓ ReplayClip media source created in Replay
  ✓ Replay buffer started
  ─────────────────────────────────────────
  Ready: Your Club CC vs Okehampton CC
  Control panel: http://localhost:5000/control
  Overlay:       http://localhost:5000/overlay
```

Open `http://localhost:5000/control` in your browser. You should see the control panel with today's match already filled in.

---

## Step 7 — Player photos (optional, new in v2)

When a new batter comes in, the overlay can show a player card with their photo and
season stats. To enable photos:

1. Create a folder called `headshots` inside your BBCC Stream folder (next to `server.py`).
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

### Before the match (30 minutes before)

1. Double-click `quickstart.bat` — everything configures automatically
2. Open `http://localhost:5000/control` to verify:
   - Opposition name is correct
   - Demo mode is **OFF** (green)
   - PCS Pro output folder path is showing
3. Open OBS — check the preview shows your camera with the overlay
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
12. *(New in v2)* In the control panel → **Match Report & Social Posts** card:
    - Click **Generate Match Report** for a full written report (edit it, then copy)
    - Click **Generate Social Post** for a ready-to-paste social media summary
    - **Important:** generate these **before** stopping the server — the match log lives
      in memory while the server is running
13. Click **Compile Highlights Reel** to create the post-match video
14. Close the command prompt window to stop the server (you'll also be prompted to save
    the match report automatically)

---

## Troubleshooting

### Quickstart says "Cannot connect to OBS"

- Make sure OBS is open before running quickstart
- Check the WebSocket password in `config.ini` matches the one in OBS → Tools → WebSocket Server Settings
- Check the port is 4455 in both places

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
- Reduce bitrate to 1500 Kbps
- Close all other applications during the stream, including the control panel browser tab
- OBS → Settings → Video → reduce Output Resolution to 1280×720

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

1. Create a `logos/` folder inside your BBCC Stream folder (alongside `server.py`)
2. Find your PlayCricket club ID — it's the `playcricket_id` value in `config.ini`
3. Save your club badge as `logos\{your_id}.png` — for example `logos\29434.png`
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
| `bbcc_scoreboard.template` | NV Play template | Copy to NV Play once |
| `headshots/` | Player photos (new in v2) | Add your players |
| `socials/` | Match photos for social posts (new in v2) | Optional |
| `logos/` | Club badges (named by club ID) | Add your badges |
| `SETUP_GUIDE_WINDOWS.md` | This guide | — |
