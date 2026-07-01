# Setup Guide — macOS
## CricketStream Overlay — Version 2.1

---

## The fast path — up and running in 3 steps

Done this before, or just want to get going? This is the whole job; everything below is the detail.

1. **Turn off AirPlay Receiver** (it blocks port 5000): System Settings → General → AirDrop & Handoff → AirPlay Receiver **off**. *(Once only.)*
2. **Setup** — download `CricketStreamSetup-mac.zip` from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest), unzip it into this folder, and double-click **`Setup Wizard.command`**. It installs Python and fixes SSL certificates for you if needed (see Step 2 for why that matters), then installs packages and walks you through your club details, creating `config.ini`. *(Once only.)*
3. **Match day** — double-click **`quickstart.sh`**. It finds today's fixture, starts everything, pulls season stats, and runs a pre-flight check. Control panel: `http://localhost:5000/control` · Overlay (for OBS): `http://localhost:5000/overlay`

Add the overlay as a 1920×1080 **Browser source** in OBS and you're live. Full OBS setup, replays, AI features, and troubleshooting follow below.

---

## Before you start

You will need:
- A Mac running macOS 12 (Monterey) or later
- A camera connected to your Mac (USB webcam or HDMI camera via capture card)
- OBS Studio for Mac — https://obsproject.com
- Python 3 — installed automatically by `Setup Wizard.command` (see Step 2), or get it yourself from https://python.org/downloads
- A YouTube account with Live Streaming enabled

**Scoring software on Mac:**
NV Play is a Windows application. Mac users have two options:

1. **Run NV Play inside a free Windows virtual machine (recommended)** — stream natively on Mac while NV Play runs in the background. See `MAC_VM_SETUP.md` for full instructions.
2. **Use PlayCricket widget fallback** — gives live score, wickets and overs but no batter or bowler names. Works without any additional setup.

A 2015 MacBook Pro or later handles both OBS and a Windows VM comfortably.

---

## Step 1 — Extract the files

Unzip `cricketstream_mac.zip` to a permanent location:
```
~/Documents/CricketStream/
```

---

## Step 2 — Install Python

> **Skip this whole step if you're using `CricketStreamSetup-mac.zip`** (see the fast path above) — the setup wizard installs Python and fixes SSL certificates for you automatically, including the "Fix SSL certificates" part below. This section is only for people running `setup.sh` from source.

1. Download from https://python.org/downloads — choose the macOS installer
2. Run the installer and follow the prompts
3. Open **Terminal** (Cmd+Space → type Terminal → Enter)
4. Type `python3 --version` — you should see `Python 3.x.x`

### ⚠️ Fix SSL certificates (required — do this immediately after installing Python)

Mac Python does not trust HTTPS certificates by default. Without this fix the server
will hang indefinitely when trying to contact PlayCricket or any other website, and
the control panel will show "Checking connection..." forever.

**Option A — via Finder (easiest):**
1. Open **Finder → Applications**
2. Open the **Python 3.x** folder
3. Double-click **Install Certificates.command**
4. A terminal window opens and runs automatically — wait for `update complete`
5. Close the window

**Option B — via Terminal:**
```bash
/Applications/Python\ 3.*/Install\ Certificates.command
```

You only need to do this once. It takes about 10 seconds.

**Fix for macOS port 5000 conflict:**
macOS uses port 5000 for AirPlay Receiver, which will block the server.

Go to **System Preferences → General → AirDrop & Handoff** and untick **AirPlay Receiver**.

---

## Step 3 — Install Python packages and configure

Already ran `CricketStreamSetup-mac.zip`'s `Setup Wizard.command`? You've done this step — skip to Step 5.

Otherwise, in Finder, navigate to your CricketStream folder.
Right-click **`setup.sh`** → **Open** → **Open** (macOS will warn about an unknown developer — click Open again to proceed).

The setup wizard installs packages, then asks a few questions — your club name, kit colour, PlayCricket ID, and any API keys you have. It creates `config.ini` for you automatically. You only need to run this once.

If you see a permissions error, open Terminal and run:
```bash
cd ~/Documents/CricketStream
chmod +x setup.sh install.sh quickstart.sh
./setup.sh
```

> **Prefer to configure manually?** Run `install.sh` to install packages, then follow Step 5 to fill in `config.ini` by hand.

---

## Step 4 — Install FFmpeg (for highlights compiler)

Open Terminal and run:
```bash
# Install Homebrew (if not already installed)
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# Install FFmpeg
brew install ffmpeg
```

---

## Step 5 — Review or edit config.ini (optional)

If you ran `setup.sh`, your `config.ini` was created automatically — you can skip straight to Step 6.

To review settings or make changes later, open `config.ini` in TextEdit (right-click → Open With → TextEdit). If TextEdit opens it in rich text mode, go to **Format → Make Plain Text** first.

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
pcs_output_folder =               ← Leave blank (unless using VMware — see MAC_VM_SETUP.md)

[OBS]
obs_password = CHANGE_ME          ← Password from OBS WebSocket settings
replay_folder = ~/Movies/Replays  ← Where OBS saves replay clips

[Stream]
youtube_title = LIVE: {home} vs {away}
max_overs = 50

[AI]
anthropic_key =                   # Optional: powers commentary, match report, social posts

[Scoring]
headshots_folder =                # Optional: folder of player photos (default: headshots/)
socials_folder =                  # Optional: folder of match photos for social posts
drinks_over = 25                  # Over at which the drinks-break weather appears
```

> The `[AI]` key is optional — you can also paste it later in the control panel.
> Leave it blank and the AI features simply stay switched off.

Save and close.

---

## Step 6 — Set up OBS

### Enable OBS WebSocket

1. Open OBS
2. Go to **OBS → Tools → WebSocket Server Settings** (on Mac the menu is at the top of the screen)
3. Tick **Enable WebSocket server**
4. Port: `4455`
5. Tick **Enable Authentication** and set a password
6. Copy that password into `config.ini` → `obs_password`
7. Click OK

### Enable Replay Buffer

1. OBS → **Preferences → Output** (Cmd+,)
2. Set Output Mode to **Advanced**
3. Click the **Recording** tab
4. Scroll to **Replay Buffer** → tick **Enable**
5. Maximum Replay Time: **25 seconds**
6. Click OK

### Output settings (optimised for Mac)

1. OBS → Preferences → Output → **Streaming** tab
2. Encoder: **Apple VT H264 Hardware Encoder** (uses hardware — much faster)
3. Rate Control: **CBR**
4. Bitrate: **2500 Kbps**
5. Click OK

### Video settings

1. OBS → Preferences → Video
2. Base Resolution: **1920×1080**
3. Output Resolution: **1280×720**
4. FPS: **30**
5. Click OK

### Add overlay browser source

1. In OBS, create a scene called **Main** (click + under Scenes)
2. Create another scene called **Replay**
3. Click on **Main**
4. Click **+** under Sources → **Browser**
5. Name it `Overlay`
6. URL: `http://127.0.0.1:5000/overlay`
7. Width: `1920`, Height: `1080`
8. Frame rate: `24`, tick **Use custom frame rate**
9. **Untick "Shutdown source when not visible"**
10. **Untick "Refresh browser when scene becomes active"**
11. Click OK

### Add ReplayClip to Replay scene

1. Click on the **Replay** scene
2. Click **+** under Sources → **Media Source**
3. Name it exactly `ReplayClip`
4. Untick **Local File**
5. Tick **Restart playback when source becomes active**
6. Click OK

---

## Step 7 — First run

> **Tip:** `server.py` now reads `config.ini` on startup and populates the control panel automatically — so your club name, API keys, and other settings appear straight away whether you launch via `quickstart.sh` or `python3 server.py`. Running `quickstart.sh` is still recommended for match days as it also fetches today's fixture and checks OBS.

Right-click `quickstart.sh` → **Open**. Or run from Terminal:
```bash
cd ~/Documents/CricketStream
./quickstart.sh
```

You should see:
```
  ✓ Club: Your Club CC
  ✓ All packages present
  ✓ Match found: Your Club CC vs Example Opposition CC
  ✓ Competition: Your League — Division 1
  ✓ match_state.json written
  ✓ Connected to OBS
  ✓ Scene 'Main' already exists
  ✓ Scene 'Replay' already exists
  ✓ Overlay browser source settings updated
  ✓ Replay buffer started
  ─────────────────────────────────────────
  Ready: Your Club CC vs Example Opposition CC
  Control panel: http://127.0.0.1:5000/control
```

Open `http://127.0.0.1:5000/control` in Safari or Chrome.

---

## Using NV Play on Mac (via VMware Fusion)

For full batter names, bowler figures and ball-by-ball data, you need NV Play running.
See **`MAC_VM_SETUP.md`** for a complete guide to:

- Installing VMware Fusion (free)
- Installing Windows 11 in a virtual machine
- Setting up a shared folder so NV Play's output is readable by the overlay
- Match day workflow with both running simultaneously

A 2015 MacBook Pro 15" handles this comfortably. A 2015 13" works but keep OBS at 720p output.

---

## Player photos (optional, new in v2)

When a new batter comes in, the overlay can show a player card with their photo and
season stats.

1. Create a folder called `headshots` inside your CricketStream folder (next to `server.py`).
2. Add player photos named by **surname** — e.g. `Smith.jpg`. Square images around
   400x400 pixels look best (JPG, PNG, WebP all work).
3. The next time that batter comes in, their photo appears on the card.

Because NV Play gives surnames only, the overlay accepts several filename patterns, so
`Smith.jpg`, `smith.png`, `J_Smith.jpg` and `JOHN_SMITH.png` all match a batter shown as
"Smith". If no photo is found the card shows the player's initials instead. No restart
needed — drop a file in and refresh the Overlay source in OBS.

---

## AI features (optional, new in v2)

A single Anthropic API key unlocks live over commentary, a post-match written report,
and ready-to-paste social posts.

1. Create an API key at **console.anthropic.com** (it starts with `sk-ant-`).
2. Paste it into the control panel → **AI Commentary** card → **Anthropic API key**,
   or into `config.ini` under `[AI] anthropic_key`.
3. Turn on **AI commentary (end of over)** in the Graphics card for live commentary.
   The report and social posts are generated on demand after the match.

Costs are a few pence per match. Without a key, everything else still works.

---

## Match day procedure

### Before the match

1. Run `quickstart.sh` — everything configures automatically
2. If using NV Play: start the Windows VM and open NV Play
3. Open `http://127.0.0.1:5000/control` and verify the opposition name
4. Check OBS preview shows camera + overlay
5. Check **Stop Replay Buffer** is showing in OBS controls

### When the scorer starts

6. Scorer begins scoring in NV Play
7. Within a few seconds, the PCS monitor in the control panel goes green
8. Batter names and bowler appear on the scorebar

### Going live

9. Click **Start Streaming** in OBS

### After the match

10. Click **Stop Streaming**
11. *(New in v2)* Control panel → **Match Report & Social Posts**:
    - **Generate Match Report** for a full written report (edit, then copy)
    - **Generate Social Post** for a social media summary
    - Generate these **before** stopping the server — the match log is held in memory
      while the server runs
12. Control panel → **Compile Highlights Reel** for the post-match video
13. Close the Terminal window to stop the server (you'll be prompted to save the report)

---

## Troubleshooting

### Port 5000 already in use / server won't start

macOS uses port 5000 for AirPlay.
**Fix:** System Preferences → General → AirDrop & Handoff → untick **AirPlay Receiver**

### Overlay goes grey in OBS

- Right-click Overlay source → **Refresh**
- Check the Terminal window (server) is still running
- In browser source Properties, confirm "Shutdown source when not visible" is unticked

### "install.sh cannot be opened because the developer cannot be verified"

Right-click `install.sh` → **Open** → **Open** in the warning dialog.
Or: System Preferences → Privacy & Security → scroll down → **Allow Anyway**.

### Replay not triggering

- Confirm **Stop Replay Buffer** is showing in OBS controls
- Check scene names are exactly `Main` and `Replay` (case sensitive)
- Check media source is named exactly `ReplayClip`
- Check OBS WebSocket password matches `config.ini`

### No batter names (score only)

This is expected on Mac without NV Play running.
Either set up VMware Fusion (see `MAC_VM_SETUP.md`) or accept score-only mode —
wickets, overs, and the score still update correctly via the PlayCricket widget.

### "Match not found" on quickstart

- Check `playcricket_id` in `config.ini` is correct
- Check `playcricket_key` is set
- The PlayCricket API only works from the laptop you registered — if it fails, enter the opposition name manually in the control panel

---


## Club Badges (optional)

Small circular club badge icons appear next to team names in the scorebar.
They are matched automatically by PlayCricket club ID.

**First-time setup:**

1. Create a `logos/` folder inside your CricketStream folder (alongside `server.py`)
2. Find your PlayCricket club ID — it's the `playcricket_id` value in `config.ini`
3. Save your club badge as `logos/{your_id}.png` — for example `logos/12345.png`
4. Restart the server — your badge appears on the left of the scorebar

**Adding opposition badges:**

1. Click **Fetch today's match** in the control panel
2. The match details show the away club information
3. Find that club's PlayCricket ID (number in their play-cricket.com URL)
   — or check `http://127.0.0.1:5000/state` and look for `away_club_id`
4. Save their badge as `logos/{away_club_id}.png`
5. Right-click the Overlay source in OBS → **Refresh** — badge appears immediately

**Badge status in the control panel:**

The Kit Colours card shows a live badge status panel with two slots — home and away.
Each slot shows a preview of the badge (green tick if found, amber warning if missing)
so you can see at a glance what's ready before going live.

**Custom logos folder:**

If your badge files are stored elsewhere, set the path in:
- Control panel → Kit Colours → **Logos folder** field, or
- `config.ini` → `[Scoring]` → `logos_folder = ~/Documents/my-logos`

Supported formats: PNG (recommended, use transparent background), SVG, WebP, JPG.

See **`CLUB_LOGOS.md`** for full instructions and tips on finding club badges.

---
## Quick reference

| URL | Purpose |
|---|---|
| `http://127.0.0.1:5000/control` | Control panel |
| `http://127.0.0.1:5000/overlay` | Overlay (use in OBS only) |
| `http://localhost:5000/pcs/debug` | Diagnose NV Play connection |

| Command | Purpose |
|---|---|
| `./setup.sh` | First-time setup (install packages + create config) |
| `./quickstart.sh` | Start everything (match day) |
| `./install.sh` | Manual package install (alternative to setup.sh) |
| `python3 server.py` | Start server only (advanced) |
