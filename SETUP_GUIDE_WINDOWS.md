# Setup Guide — Windows
## CricketStream Overlay

---

## The fast path — up and running in 2 steps

Done this before, or just want to get going? This is the whole job; everything below is the detail.

1. **Once** — download `CricketStream.exe` from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest) and put it in a folder of its own, such as `Documents\CricketStream`.
2. **Every time, including the first** — double-click it.

That's the whole thing. It works out what still needs doing: on the first run it opens a
setup page in your browser to create `config.ini`; on every run after that it goes
straight to starting the match — OBS, today's fixture, season stats, pre-flight check.
Control panel: `http://localhost:5000/control` · Overlay (for OBS): `http://localhost:5000/overlay`

OBS's scenes, the overlay source and the replay buffer are all set up for you. Full detail,
replays, AI features, and troubleshooting follow below.

---

## Before you start

You will need:
- A Windows laptop or PC (Windows 10 or 11; `CricketStream-arm64.exe` for an Arm laptop)
- A camera connected to your laptop (USB webcam or HDMI camera via capture card), or an
  IP camera on the network
- OBS Studio installed — https://obsproject.com
- A YouTube account with Live Streaming enabled
- NV Play installed on the scorer's laptop (or use the [manual scoring page](README.md#no-scoring-software-manual-scoring))

Nothing else to install: CricketStream is one file with everything built in.

---

## Step 1 — Put CricketStream in its own folder

Make a folder somewhere permanent, for example:
```
C:\Users\YourName\Documents\CricketStream\
```
and move `CricketStream.exe` into it. Everything CricketStream saves — `config.ini`, the
match data, club badges, sponsor logos, player photos — goes in this folder beside it, so
copying the folder is a complete backup (or a move to a new laptop).

Avoid Downloads (some clean-up tools empty it) and OneDrive-synced folders (the match
database is written on every ball, and sync conflicts aren't worth the risk mid-match).

---

## Step 2 — First-time setup

Double-click `CricketStream.exe`.

> **"Windows protected your PC"?** The download isn't from the Microsoft Store, so
> SmartScreen asks once. Click **More info → Run anyway**.
>
> **Firewall question?** Tick **Private networks** and click **Allow**. It's needed for
> the control panel on a phone or tablet, and for a scorer's laptop to be found.

A black window opens and your browser shows the setup page. The first question is **What
is this computer for?** — choose **Streaming laptop** (for the scorer's own laptop, see
[TWO_LAPTOP_SETUP.md](TWO_LAPTOP_SETUP.md)). Then your club name, kit colour (there's a
colour picker), PlayCricket ID, and any API keys you have. **Your stream** shows exactly
what viewers will see — your name and colours on each of the four scorebar styles, over a
drawn ground or a photo of your own, with buttons to play the wicket card, boundaries, the
end-of-over summary and more. The **Check** buttons try the PlayCricket key, the
scoreboard folder and the AI key on the spot. Anything you don't have yet can be left
blank and added later. Leave **Put a CricketStream shortcut on the desktop** ticked for an
icon to double-click on match day.

Press **Save and start**. `config.ini` is written next to CricketStream, NV Play's
scoreboard template is installed if NV Play is on this laptop, and the black window goes
straight on into starting the match.

If the page doesn't open by itself, the black window prints its address (usually
`http://127.0.0.1:5000/setup`) — type that into your browser.

---

## Step 3 — Review or edit config.ini (optional)

The setup page wrote `config.ini` for you — you can skip straight to Step 4.

To change the setup answers later: control panel → **Setup** tab → **Open settings**. It's
the same page with your answers filled in, and saved changes reach the overlay straight
away (only "what this computer is for" and phone/tablet access wait for a restart).
Secret fields (API keys, passwords) show blank with "Saved" beside them; leave them blank
to keep what you have, or press **Remove**.

(With CricketStream not running, `CricketStream setup` in a Command Prompt in the
CricketStream folder opens the same page.)

Or open `config.ini` in Notepad (right-click → Open with → Notepad). The main settings are:

```ini
[Club]
name = Your Club CC               ← Your full club name
abbreviation = YCC                ← Up to 6 characters for the scorebar
home_colour = #1a3a5c             ← Your kit colour in hex
playcricket_id = 12345            ← Your club ID from play-cricket.com

[API]
playcricket_key = YOUR_KEY_HERE   ← Your PlayCricket API key
anthropic_key =                   ← Optional: powers commentary, match report, social posts

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
```

[`config.example.ini`](config.example.ini) in the repository documents every setting.

**Finding your PlayCricket club ID:**
Go to play-cricket.com → find your club page → the number in the URL is your club ID.

---

## Step 4 — Set up OBS

### OBS WebSocket, scenes and replay buffer — done for you

**There's nothing to switch on in OBS.** Every time CricketStream starts the match it:

- switches on OBS's WebSocket server (how CricketStream talks to OBS) with a random
  password, and writes that password into `config.ini` for you;
- turns on the replay buffer (25 seconds) and points it at your replay folder;
- opens OBS, so you don't need to open it first;
- creates the **Main** scene with the **Overlay** browser source and the **Replay** scene
  with the **ReplayClip** media source, if they aren't there yet. The very first time, it
  closes and reopens OBS once so the replay buffer can start — that's expected.

If OBS is already open with the WebSocket server switched off, CricketStream asks you to
close OBS and carries on once you have: OBS saves its settings when it closes, so they can
only be changed while it's shut. If OBS already has a WebSocket server set up, it's left
exactly as it is and its password is reused.

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

**Scenes and sources:** with OBS open, run `CricketStream obs-setup` from a Command Prompt
in the CricketStream folder. It builds the scenes and sources over the WebSocket.

**Replay buffer:** OBS only creates the buffer when it starts. The first time, you'll see
`⚠ Replay buffer enabled but wouldn't start yet — restart OBS once and re-run setup`:
close OBS, open it again, and run `CricketStream obs-setup` again. Or tick it yourself:

1. OBS → **Settings → Output**
2. Set Output Mode to **Advanced**
3. Click the **Recording** tab
4. Scroll down to **Replay Buffer** — tick **Enable**
5. Set Maximum Replay Time to **25 seconds**
6. Click OK

</details>

### Video settings, bitrate and encoder — done for you

CricketStream sets these every time it opens OBS:

- **Canvas (Base Resolution): 1920×1080**, always. The overlay graphics are drawn at that
  size, so any other canvas puts them in the wrong place.
- **Output resolution and frame rate:** from your last upload-speed test (the control
  panel's **Stream Health Check** runs one the first time it sees OBS). 1080p on a good
  connection, 720p otherwise, at 30fps. Before the first test: 720p at 30fps.
- **Bitrate:** from the same test, comfortably under your measured upload speed.
- **Encoder:** if OBS is on its default software encoder (x264) and this laptop has a
  hardware one (NVIDIA NVENC, AMD, Intel QuickSync), it's switched to the hardware one,
  which leaves the processor free for everything else. A hardware encoder you picked
  yourself is never changed.

To pin your own choices, set them in `config.ini` under `[Stream]` rather than in OBS
(OBS's own settings get replaced the next time CricketStream starts): `output_resolution =
720p` or `1080p`, `fps = 25` or `30`, `bitrate_kbps = 2500`. Set `output_resolution =
manual`, `bitrate_kbps = manual` or `encoder = manual` to have them left alone entirely.

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

You don't copy the template or the folder path anywhere by hand:

- **NV Play on this laptop:** setup puts `scoreboard.template` into NV Play's Templates
  folder for you, and finds the output folder itself (it asks you to confirm it).
- **NV Play on a separate scoring laptop:** CricketStream on that laptop does both — see
  [TWO_LAPTOP_SETUP.md](TWO_LAPTOP_SETUP.md).

If step 5 shows no `scoreboard.template`, run the setup first, then come back to it.

**Note:** The template folder and output folder are different locations — the template goes in `\Templates\`, the data comes out of `\Output\`.

---

## Step 6 — First run

Double-click **`CricketStream.exe`**. It asks how you want to run today (control panel on
this laptop only, or reachable from phones and tablets too) and how the match is being
scored, then you will see something like:
```
  ✓ Club: Your Club CC
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

The control panel opens in your browser by itself, with today's match already filled in.
(Double-clicking CricketStream again while it's running just brings the control panel
back up.)

> **Rehearse first.** With CricketStream running, open a second Command Prompt in the
> CricketStream folder and run `CricketStream simulate --configure`: it plays a whole
> simulated match into the overlay, so you can watch every graphic and replay without a
> scorer. `CricketStream simulate --list` shows the scenarios. Afterwards, set the score
> source back to NV Play's folder in the control panel.

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

The setup page also asks which one you want, so this is only if you skipped it or changed
your mind.

---

## Step 7 — Player photos (optional)

When a new batter comes in, the overlay can show a player card with their photo and
season stats. To enable photos:

1. Create a folder called `headshots` inside your CricketStream folder (next to `CricketStream.exe`).
2. Add player photos named by **surname** — for example `Smith.jpg`. Square images around
   400x400 pixels look best. JPG, PNG and WebP all work.
3. That's it. The next time that batter comes in, their photo appears on the card.

Because NV Play only gives surnames, the overlay accepts several filename patterns, so
any of these match a batter shown as "Smith": `Smith.jpg`, `smith.png`, `J_Smith.jpg`,
`JOHN_SMITH.png`. If no photo is found the card shows the player's initials instead —
it never breaks the graphic.

No restart is needed — drop a file in and refresh the Overlay source in OBS.

---

## Step 8 — AI features (optional)

A single Anthropic API key unlocks the AI features:

- **Over commentary** — a line of analysis at the end of each over
- **Match report** — a full written report generated after the game
- **Social posts** — ready-to-paste posts for your club's channels
- **Camera spotter** — warns you if a camera lens fogs up or gets knocked (off until you
  switch it on)

To set up:

1. Go to **console.anthropic.com** and create an API key (starts with `sk-ant-`).
2. Paste it into the control panel → **AI Commentary** card → **Anthropic API key**,
   or into `config.ini` under `[API] anthropic_key`, or on the setup page.
3. In the **Graphics** card, turn on **AI commentary (end of over)** if you want live
   commentary. The match report and social posts are generated on demand after the match.

Costs are tiny — a few pence for a whole match. Leave the key blank and everything else
still works; the AI features simply stay off.

---

## Match day procedure

### A few days before

Start CricketStream, open the control panel → **Setup** → **Run pre-match check**. It tries
every login and connection for real — YouTube, PlayCricket, the AI key, OBS, the scorer
feed, disk space — and says what to fix. ⚠ on OBS and the scorer feed is normal days
before; anything marked ✗ needs fixing before the match. A YouTube login that has expired
shows up here, not on the day.

### Before the match (30 minutes before)

1. Double-click `CricketStream.exe` — everything configures automatically
2. Open `http://localhost:5000/control` and work down the **Match-day checklist** at the
   top. It ticks itself as things are ready; anything that isn't has a button that does
   it (Start OBS, Add camera to OBS, Start replay buffer, Fetch today's match, Go live).
   Also check Demo mode is **OFF** (green)
3. In OBS (CricketStream opens it for you), check the preview shows your camera with the overlay
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
14. Click **Compile highlights reel** to create the post-match video (needs
    [FFmpeg](https://ffmpeg.org/download.html) installed)
15. Press **Ctrl+C** in the black window to stop. It prints the sponsors' screen time and
    saves `sponsor_airtime_<date>.txt` and the match report in the CricketStream folder.

---

## Troubleshooting

### The window opens, flashes something, and closes before I can read it

It shouldn't: whatever goes wrong, CricketStream stops and waits with
`Press Enter to close this window...` so you can read why.

If you do see it vanish, open Command Prompt (press Start, type `cmd`), drag
`CricketStream.exe` into the window and press Enter. It runs the same way but the window
stays open.

### My settings have gone / it's asking the setup questions again

`config.ini` lives next to `CricketStream.exe`. If you downloaded a new copy and ran it
from somewhere else (Downloads, say), it doesn't see your old one. Move the new
`CricketStream.exe` into your CricketStream folder, replacing the old file.

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

### It says "Cannot connect to OBS"

- Close OBS and start CricketStream again — it opens OBS itself with the WebSocket server
  on, and skips OBS's Safe Mode question after a crash (Safe Mode switches the WebSocket
  server off, which used to be the usual cause of this)
- **Opened OBS yourself and it offered Safe Mode?** If OBS didn't shut down cleanly last
  time, it asks *"Run in Safe Mode (third-party plugins, scripting, and **WebSockets
  disabled**)?"*. Choose **Run in Normal Mode**, or close it and let CricketStream open it.
- With `manage_obs = no`: check the WebSocket password in `config.ini` matches OBS →
  Tools → WebSocket Server Settings, and the port is 4455 in both places

### PCS monitor says "Widget" not "PCS"

- Check the output folder path in `config.ini` matches exactly what NV Play shows
- Make sure the scorer has scored at least one ball (NV Play only writes the file after the first delivery)
- Go to `http://localhost:5000/pcs/debug` — it shows exactly what the server can see

### Overlay not showing in OBS

- OBS → right-click the Overlay browser source → **Refresh**
- Make sure the black window (the server) is still open
- Check the URL in browser source properties is `http://localhost:5000/overlay`

### Overlay goes grey during the stream

- Right-click Overlay source → **Properties**
- Make sure **"Shutdown source when not visible"** is **unticked**
- Make sure **"Refresh browser when scene becomes active"** is **unticked**
- To recover immediately: right-click → **Refresh**

### Stream is choppy or dropping frames

- Close all other applications during the stream, including the control panel browser tab
- In `config.ini` under `[Stream]`, set `output_resolution = 720p` and/or
  `bitrate_kbps = 1500`, then start CricketStream again. (Changing these in OBS's own
  settings only lasts until the next start, which sets them again.)
- For evidence rather than guesswork: CricketStream logs the stream's bitrate, dropped
  frames and congestion to `diagnostics\telemetry_<date>.csv` during every match.
  `CricketStream telemetry --summary diagnostics\telemetry_<date>.csv` prints the
  interesting numbers — when frames dropped, and what the line could actually carry.

### "Match not found"

- The match may not be published on PlayCricket yet — enter the opposition manually in the control panel
- Check your PlayCricket API key is correct in `config.ini`
- The API only works from the laptop you registered with ECB/PlayCricket

### Replay not triggering

- Make sure **Stop Replay Buffer** is showing in OBS controls (buffer must be running)
- Check the OBS password in `config.ini`
- Scene names must be exactly `Main` and `Replay` (capital first letter, case sensitive)
- The media source must be named exactly `ReplayClip`

### An IP camera's picture drifts behind the score

Make sure the camera source is in **every** scene that uses it — Replay too, not just
Main. A source missing from a scene is switched off while that scene shows, and the RTSP
feed rebuffers when it comes back, falling further behind each time. If it still drifts,
`CricketStream refresh-cam --interval 180` reloads the camera source every three minutes
(each reload is a one-second hitch, so use the longest interval that works).

For a Reolink camera, `CricketStream camera-encoder` reads the camera's own encoder
settings — a low camera bitrate makes a soft picture that no OBS setting can fix.

---

## Club Badges (optional)

Small circular club badge icons appear next to team names in the scorebar.
They are matched automatically by PlayCricket club ID.

**First-time setup:**

1. Create a `logos\` folder inside your CricketStream folder (alongside `CricketStream.exe`)
2. Find your PlayCricket club ID — it's the `playcricket_id` value in `config.ini`
3. Save your club badge as `logos\{your_id}.png` — for example `logos\12345.png`
4. Refresh the Overlay source in OBS — your badge appears on the left of the scorebar

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

See [CLUB_LOGOS.md](CLUB_LOGOS.md) for full instructions and tips on finding club badges.

---

## Quick reference

| URL | Purpose |
|---|---|
| `http://localhost:5000/control` | Control panel |
| `http://localhost:5000/overlay` | Overlay (add to OBS, don't open in browser) |
| `http://localhost:5000/scoring` | Manual ball-by-ball scoring (phone or tablet) |
| `http://localhost:5000/pcs/debug` | Diagnose NV Play connection |
| `http://localhost:5000/health` | Everything at a glance: feed, OBS, badges, keys |

| Command (in the CricketStream folder) | What it does |
|---|---|
| `CricketStream` | Same as double-clicking |
| `CricketStream setup` | The setup page again (when CricketStream isn't running; otherwise use Settings in the control panel) |
| `CricketStream simulate --configure` | Rehearse with a simulated match |
| `CricketStream help` | Every command |

---

## Files in the CricketStream folder

| File | Purpose | Edit? |
|---|---|---|
| `CricketStream.exe` | The whole app | Replace to update |
| `config.ini` | Your club settings | ✅ Created by setup; edit any time |
| `match_state.json` | Control-panel settings | Through the control panel |
| `match_data.db` | Ball-by-ball database | Never |
| `headshots\` | Player photos | Add your players |
| `socials\` | Match photos for social posts | Optional |
| `logos\` | Club badges (named by club ID) | Add your badges |
| `sponsors\` | Sponsor logos | Through the control panel |
| `diagnostics\` | Match-day telemetry | Read with `CricketStream telemetry --summary` |
| `overlay.html`, `control.html` | *Not there by default* — put an edited copy here and it's used instead of the built-in one | For tinkerers |
