# Setup Guide — macOS
## CricketStream Overlay

---

## The fast path — up and running in 3 steps

Done this before, or just want to get going? This is the whole job; everything below is the detail.

1. **Turn off AirPlay Receiver** (it holds port 5000): System Settings → General → AirDrop & Handoff → AirPlay Receiver **off**. *(Once only.)*
2. **Once** — download `CricketStream-mac.zip` from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest) (one download works on both Apple Silicon and Intel Macs), unzip it, and put both files in a folder of their own, such as `Documents/CricketStream`.
3. **Every time, including the first** — double-click **`CricketStream.command`**.

That's the whole thing. CricketStream's screens open in your browser (on Windows it has a
window of its own; on a Mac that's still to come), with a Terminal window behind them. On
the first run it asks which computer this is and the setup questions, and writes
`config.ini`; on every run after that it opens on the match-day screen — press **Start
match day** for OBS, today's fixture, season stats and the pre-flight check.
Control panel: `http://localhost:5000/control` · Overlay (for OBS): `http://127.0.0.1:5000/overlay`

OBS's scenes, the overlay source and the replay buffer are all set up for you. Full detail,
replays, AI features, and troubleshooting follow below.

---

## Before you start

You will need:
- A Mac running macOS 12 (Monterey) or later — Apple Silicon or Intel, both supported
- A camera connected to your Mac (USB webcam or HDMI camera via capture card), or an IP
  camera on the network
- OBS Studio for Mac — https://obsproject.com
- A YouTube account with Live Streaming enabled

Nothing else to install: CricketStream is one file with everything built in.

**Scoring software on Mac:**
NV Play is a Windows application. Mac users have three options:

1. **NV Play on a separate Windows laptop (recommended)** — the scorer's laptop runs
   CricketStream too, and your Mac finds it on the club wifi. See
   [TWO_LAPTOP_SETUP.md](TWO_LAPTOP_SETUP.md).
2. **NV Play inside a Windows virtual machine** on the Mac. See
   [MAC_VM_SETUP.md](MAC_VM_SETUP.md) — but a VM competes with OBS for the same processor
   and heat budget; a streaming Mac that overheated mid-match is why the
   [bridge](BRIDGE.md) and the two-laptop setup exist.
3. **No NV Play at all** — score on a phone at `http://localhost:5000/scoring` (see the
   [README](README.md#no-scoring-software-manual-scoring)).

---

## Step 1 — Put CricketStream in its own folder

Double-click `CricketStream-mac.zip` to unzip it. Make a folder somewhere permanent, for
example `Documents/CricketStream`, and move both files (`CricketStream` and
`CricketStream.command`) into it. Everything CricketStream saves — `config.ini`, the match
data, club badges, sponsor logos, player photos — goes in this folder beside it, so
copying the folder is a complete backup (or a move to a new Mac).

---

## Step 2 — First-time setup

Double-click **`CricketStream.command`**. It opens in Terminal.

> **"Apple could not verify CricketStream is free of malware"?** The download isn't from
> the App Store, so macOS asks before the first run. Click **Done**, then open **System
> Settings → Privacy & Security**, scroll down to the message about CricketStream and click
> **Open Anyway**. macOS may ask twice — once for `CricketStream.command` and once for
> `CricketStream` itself.
>
> Or do it in one go: open **Terminal** (Cmd+Space → type Terminal → Enter) and run
> ```bash
> xattr -dr com.apple.quarantine ~/Documents/CricketStream
> ```
> (with your folder's path, if it's somewhere else).
>
> **"Do you want the application to accept incoming network connections?"** Click
> **Allow**. It's needed for the control panel on a phone or tablet, and for a scorer's
> laptop to be found.

Your browser shows CricketStream's first screen: **which computer is this?** — click
**The streaming laptop** (for the scorer's own laptop, see
[TWO_LAPTOP_SETUP.md](TWO_LAPTOP_SETUP.md)). Then your club name, kit colour (there's a
colour picker), PlayCricket ID, and any API keys you have. **Your stream** shows exactly
what viewers will see — your name and colours on each of the four scorebar styles, over a
drawn ground or a photo of your own, with buttons to play the wicket card, boundaries, the
end-of-over summary and more. The **Check** buttons try the PlayCricket key, the
scoreboard folder and the AI key on the spot. Anything you don't have yet can be left
blank and added later.

Press **Save and continue**. `config.ini` is written next to CricketStream and the page
moves on to the match-day screen.

If the page doesn't open by itself, the Terminal window prints its address (`app page
http://127.0.0.1:5098/`) — type that into Safari or Chrome.

---

## Step 2a — Install FFmpeg (for the highlights reel and social clips)

Optional — only the highlights reel and social clips need it. Open Terminal and run:
```bash
# Install Homebrew (if not already installed)
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# Install FFmpeg
brew install ffmpeg
```

---

## Step 3 — Review or edit config.ini (optional)

The setup page wrote `config.ini` for you — you can skip straight to Step 4.

To change the setup answers later: control panel → **Setup** tab → **Open settings**. It's
the same page with your answers filled in, and saved changes reach the overlay straight
away (only "what this computer is for" and phone/tablet access wait for a restart).
Secret fields (API keys, passwords) show blank with "Saved" beside them; leave them blank
to keep what you have, or press **Remove**.

(With CricketStream not running, `./CricketStream setup` in Terminal, in the CricketStream
folder, opens the same page.)

Or open `config.ini` in TextEdit (right-click → Open With → TextEdit). If TextEdit opens it in rich text mode, go to **Format → Make Plain Text** first. The main settings are:

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
pcs_output_folder = /Users/you/Documents/NVPlay Shared
                                  ← Output folder from NV Play (see Step 5)

[OBS]
obs_password =                    ← Leave blank — filled in for you on first run
replay_folder = /Users/you/Movies/Replays
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

**Scenes and sources:** with OBS open, run `./CricketStream obs-setup` in Terminal from
the CricketStream folder. It builds the scenes and sources over the WebSocket.

**Replay buffer:** OBS only creates the buffer when it starts. The first time, you'll see
`⚠ Replay buffer enabled but wouldn't start yet — restart OBS once and re-run setup`:
close OBS, open it again, and run `./CricketStream obs-setup` again. Or tick it yourself:

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
- **Encoder:** if OBS is on its default software encoder (x264) and this Mac has a
  hardware one (Apple VideoToolbox), it's switched to the hardware one,
  which leaves the processor free for everything else. A hardware encoder you picked
  yourself is never changed.

To pin your own choices, set them in `config.ini` under `[Stream]` rather than in OBS
(OBS's own settings get replaced the next time CricketStream starts): `output_resolution =
720p` or `1080p`, `fps = 25` or `30`, `bitrate_kbps = 2500`. Set `output_resolution =
manual`, `bitrate_kbps = manual` or `encoder = manual` to have them left alone entirely.

---

## Step 5 — Set up NV Play

NV Play runs on Windows — on the scorer's laptop, or in a virtual machine (see "Scoring
software on Mac" above). On that Windows machine:

1. Open NV Play
2. Go to **Tools → Configuration → Scoreboard**
3. Tick **Enable Scoreboard Output**
4. Leave the **Output Folder** as NV Play's own unless you have a reason to change it (in
   a VM, point it at the shared folder — see [MAC_VM_SETUP.md](MAC_VM_SETUP.md))
5. Click the **Template File** browse button, open NV Play's `Templates` folder and
   select **`scoreboard.template`**
6. Click OK

On a separate scoring laptop, CricketStream installs `scoreboard.template` for you — see
[TWO_LAPTOP_SETUP.md](TWO_LAPTOP_SETUP.md). In a VM, copy it in by hand once: in Terminal,
`./CricketStream agent --check-template` confirms it's built in, and the template file is
in the [repository](scoreboard.template) to download.

---

## Step 6 — First run

Double-click **`CricketStream.command`**. The match-day screen asks who uses the control
panel today (just this laptop, or phones and tablets too) and how the match is being
scored. Press **Start match day** and the steps tick off as they happen, something like:
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
  Overlay:       http://127.0.0.1:5000/overlay
```

The control panel then opens in a new browser tab, with today's match already filled in.
(Double-clicking CricketStream again while it's open just brings its page back up.)

> **Rehearse first.** Control panel → **Match day** → **Practice match** plays a made-up
> match through the real overlay — scorebar, wicket cards, boundaries, replays, the
> end-of-over graphics — so you can watch everything in OBS without a scorer. Pick a whole
> match, a tense chase, a century or a collapse, and 2, 5 or 10 overs. When it ends (or you
> press Stop) the score source is put back as it was and nothing from the practice is kept.

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
season stats. To add photos: control panel → **Pictures** → Player photos → **Add photos…**,
and give each the player's name ("John Walker"). Square images around 400×400 pixels look
best; JPG, PNG and WebP all work, and it works from a phone too.

If you've entered the squad roster (Setup tab), the Pictures tab lists who still needs a
photo, with an **Add photo…** button beside each name.

Because NV Play only gives surnames, names are matched loosely — "John Walker", "J Walker"
and "Walker" all find a batter shown as "WALKER". If no photo is found the card shows the
player's initials instead — it never breaks the graphic. No restart needed.

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

1. Double-click `CricketStream.command` — everything configures automatically
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
15. Press **Ctrl+C** in the Terminal window to stop (the match-day page's **Stop** does the
    same). It asks about the match report on the page, and saves the sponsors' screen time
    (`sponsor_airtime_<date>.txt`) and the match report in the CricketStream folder.

---

## Troubleshooting

### Port 5000 already in use / the server won't start

macOS uses port 5000 for AirPlay Receiver.
**Fix:** System Settings → General → AirDrop & Handoff → untick **AirPlay Receiver**.

Can't switch it off (a managed Mac, say)? Move CricketStream instead: open
`CricketStream.command` in TextEdit and change its last line to
`CRICKETSTREAM_PORT=5050 ./CricketStream`. Then use `http://localhost:5050/control`; OBS's
overlay source is moved to the new port the next time CricketStream sets OBS up.

### My settings have gone / it's asking the setup questions again

`config.ini` lives next to `CricketStream`. If you downloaded a new copy and ran it from
somewhere else (Downloads, say), it doesn't see your old one. Move the new `CricketStream`
and `CricketStream.command` into your CricketStream folder, replacing the old ones.

### OBS crashed during the match

You shouldn't need to do anything. The server notices within a few seconds, reopens OBS
(without the Safe Mode question), switches back to the Main scene, and restarts the stream
if it was live. The Terminal window shows `⚠ OBS crashed — reopening it`. If OBS
crashes three times inside half an hour, it stops reopening it and says so: something
needs looking at (OBS → Help → Log Files).

Closing OBS yourself is different. It stays closed, so you can shut down normally after
the match.

On a Mac this covers crashes only, not a frozen OBS (there's no reliable way to tell).
If OBS stops responding, Force Quit it (Option+Cmd+Esc); the server treats that as a crash
and reopens it.

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
- Make sure the Terminal window (the server) is still open
- Check the URL in browser source properties is `http://127.0.0.1:5000/overlay`

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
  frames and congestion to `diagnostics/telemetry_<date>.csv` during every match.
  `./CricketStream telemetry --summary diagnostics/telemetry_<date>.csv` prints the
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
`./CricketStream refresh-cam --interval 180` reloads the camera source every three minutes
(each reload is a one-second hitch, so use the longest interval that works).

For a Reolink camera, `./CricketStream camera-encoder` reads the camera's own encoder
settings — a low camera bitrate makes a soft picture that no OBS setting can fix.

---

## Club Badges (optional)

Small circular club badge icons appear next to team names in the scorebar, matched by
PlayCricket club ID — which CricketStream already knows, so there's nothing to look up.

**Adding badges:** control panel → **Pictures** → Club badges:

- **Our badge** → **Upload…** once.
- **Today's opposition** (after **Fetch today's match**) → **Upload…** — on the scorebar
  straight away.
- Any other club: type its PlayCricket ID and **Add badge…** (e.g. your league's badges
  before the season).

Badges you add stay, so over a season most opponents' badges are already there.

**Badge status:** Graphics & sponsor → Team colours & badges shows both teams' badges, with
a green tick or an amber warning, so you can see what's ready before going live. Its
**Home badge** / **Away badge** pickers choose any badge you've added when the fixture
doesn't carry a club ID (cups, friendlies).

PNG with a transparent background looks best; WebP and JPG work too.

See [CLUB_LOGOS.md](CLUB_LOGOS.md) for tips on finding club badges.
See [CLUB_LOGOS.md](CLUB_LOGOS.md) for full instructions and tips on finding club badges.

---

## Quick reference

| URL | Purpose |
|---|---|
| `http://localhost:5000/control` | Control panel |
| `http://127.0.0.1:5000/overlay` | Overlay (add to OBS, don't open in browser) |
| `http://localhost:5000/scoring` | Manual ball-by-ball scoring (phone or tablet) |
| `http://localhost:5000/pcs/debug` | Diagnose NV Play connection |
| `http://localhost:5000/health` | Everything at a glance: feed, OBS, badges, keys |

| Command (in Terminal, in the CricketStream folder) | What it does |
|---|---|
| `./CricketStream` | Same as double-clicking `CricketStream.command` |
| `./CricketStream setup` | The setup page again (when CricketStream isn't running; otherwise use Settings in the control panel) |
| `./CricketStream simulate --configure` | Rehearse with a simulated match |
| `./CricketStream help` | Every command |

---

## Files in the CricketStream folder

| File | Purpose | Edit? |
|---|---|---|
| `CricketStream` | The whole app | Replace to update |
| `CricketStream.command` | Double-click to start | — |
| `config.ini` | Your club settings | ✅ Created by setup; edit any time |
| `match_state.json` | Control-panel settings | Through the control panel |
| `match_data.db` | Ball-by-ball database | Never |
| `headshots/` | Player photos | Through the Pictures tab |
| `socials/` | Match photos for social posts | Through the Pictures tab |
| `logos/` | Club badges (named by club ID) | Through the Pictures tab |
| `sponsors/` | Sponsor logos | Through the control panel |
| `diagnostics/` | Match-day telemetry | Read with `./CricketStream telemetry --summary` |
| `overlay.html`, `control.html` | *Not there by default* — put an edited copy here and it's used instead of the built-in one | For tinkerers |
