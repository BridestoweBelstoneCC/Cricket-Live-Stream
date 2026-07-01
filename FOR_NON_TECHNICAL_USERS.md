# Getting Started — No Technical Experience Needed

This guide is written for club volunteers who are comfortable using a computer but have never written code or used a command line before. If that's you — don't worry. You can do this.

---

## What this software actually does

Imagine you're watching a professional cricket match on TV. At the bottom of the screen there's a bar showing the score, the batters' names, the bowler, and the run rate. When a wicket falls a graphic pops up. When someone hits a six the replay automatically appears.

This software does exactly that — for your club's YouTube live stream. It runs in the background on your laptop, reads the scorer's data, and adds those graphics on top of your camera feed.

---

## What you need before you start

**Hardware:**
- A laptop (Windows or Mac)
- A camera — a basic USB webcam works fine to start. An HDMI camera with a capture card looks more professional but isn't required
- A stable internet connection (broadband, not a mobile hotspot if possible)

**Accounts (all free):**
- A YouTube account — and you need to enable Live Streaming on it
  - Go to youtube.com → Your channel → Go Live → it may ask you to verify your account, which takes 24 hours
- A GitHub account is not needed — you're just downloading the software, not contributing to it

**Software (all free, we'll install it together):**
- Python — a programming language the software is written in. You don't need to install this yourself — the setup wizard in Step 3 installs it for you
- OBS Studio — the software that mixes your camera with the graphics and sends it to YouTube

---

## Step 1 — Download and install OBS Studio

1. Go to **https://obsproject.com**
2. Click the button for your operating system (Windows or macOS)
3. Run the installer and follow the prompts
4. Open OBS — it will ask to run the Auto-Configuration Wizard. Click **Yes**
5. Select **Optimise for streaming** → Next
6. Set Video Resolution to **1280x720** → Next
7. It will test your connection. Click **Apply Settings** when done

---

## Step 2 — Download the cricket stream software

1. Go to **https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream**
2. Click the green **Code** button
3. Click **Download ZIP**
4. Open the ZIP and extract the folder somewhere permanent — your Documents folder is fine
5. **Windows users:** Open the `Windows` folder inside
6. **Mac users:** Open the `Mac` folder inside

---

## Step 3 — First-time setup

Go to the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest) page and download the setup wizard for your computer:

**Windows:** `CricketStreamSetup.exe` — put it in the folder from Step 2 and double-click it.
**Mac:** `CricketStreamSetup-mac.zip` — put it in the folder from Step 2, unzip it, then double-click **`Setup Wizard.command`** (macOS will warn about an unknown developer the first time — click **Open** to proceed).

You don't need to install Python first — if it's missing, the wizard installs it for you (and on Mac, fixes the SSL certificate issue automatically, which used to be a separate manual step). On Mac it will ask for your Mac password partway through — that's the wizard installing Python, not anything suspicious.

A window opens and asks you a few questions:

- **Club name** — your full club name as it will appear on screen
- **Abbreviation** — a short version for the scorebar, max 6 characters (e.g. TAUN, STOW, EXE)
- **Home kit colour** — the hex code for your kit colour. Go to **htmlcolorcodes.com**, pick your colour, copy the code that starts with `#`
- **PlayCricket club ID** — the number in your club's play-cricket.com URL
- **PlayCricket API key** — email PlayCricket support to request one (it's free)
- **NV Play output folder** — where your scorer's NV Play software saves its data (your scorer knows this — it's set in NV Play under Tools → Configuration → Scoreboard)
- **OBS WebSocket password** — you'll set this in Step 4, then come back and update it if needed

For anything you don't have yet, just press Enter to skip it — you can fill it in later via the control panel.

When the wizard finishes, it offers to launch the server straight away.

> Already have Python installed and comfortable with that? You can use `setup.bat` (Windows) / `setup.sh` (Mac) from the folder instead — it's the same wizard, run from source.

> If anything goes wrong during install, see the Troubleshooting section at the bottom.

---

## Step 4 — Set up OBS

### Turn on WebSocket (lets the software talk to OBS)

1. Open OBS
2. Go to **Tools** in the menu bar → **WebSocket Server Settings**
3. Tick **Enable WebSocket server**
4. Make sure Port says **4455**
5. Tick **Enable Authentication**
6. Type a password — anything you'll remember
7. Copy that password into `config.ini` next to **obs_password**
8. Click OK

### Turn on Replay Buffer (saves clips for replays)

1. OBS → **Settings** (bottom right) → **Output**
2. Change Output Mode from Simple to **Advanced**
3. Click the **Recording** tab
4. Scroll down to **Replay Buffer** → tick **Enable**
5. Set Maximum Replay Time to **25**
6. Click OK

---

## Step 5 — Set up your camera in OBS

1. In OBS, under **Sources** (bottom left), click the **+** button
2. Select **Video Capture Device**
3. Name it Camera → OK
4. Select your camera from the Device dropdown → OK
5. You should see your camera feed in the OBS preview window

---

## Match day — what to do each time

**30 minutes before you go live:**

1. Make sure OBS is open
2. **Windows:** Double-click **`quickstart.bat`**
   **Mac:** Double-click **`quickstart.sh`**

   > ⚠️ **Always use quickstart on match days** — it finds today's fixture, checks OBS is ready, and starts the server. Running `server.py` directly skips those checks.

3. A window appears showing the software starting up. You should see your club name, today's opposition, and the umpires' names appear automatically.
4. Open your browser and go to **http://127.0.0.1:5000/control**
   This is your control panel — you can see the live data from the scorer here
5. In OBS, click **Start Replay Buffer** (in the Controls panel on the right)

**When the scorer starts:**

6. Your scorer opens NV Play and starts the match
7. After the first ball, the scorebar on the overlay fills in with live batter names and bowler figures

**Going live:**

8. In OBS, click **Start Streaming**
9. Your stream is live on YouTube

**After the match:**

10. In the control panel, click **Compile Highlights Reel** — this automatically stitches all the replay clips into one video
11. Click **Stop Streaming** in OBS
12. Close the black command window to stop the server

---

## What the control panel does

When the software is running, open **http://127.0.0.1:5000/control** in your browser. This is your dashboard. From here you can:

- See live data coming in from the scorer (green means connected)
- Turn graphics on and off (fall of wicket, boundaries, replays etc.)
- Show or hide the weather widget
- Update the YouTube stream title
- Manually change the opposition name or kit colour
- Compile highlights after the match

You don't need to use this every match — once config.ini is set up, quickstart.bat/sh handles everything automatically.

---


## Club badges in the scorebar

The scorebar can show a small circular badge next to each team name — like the
ones you see on professional cricket broadcasts. They appear automatically once
you have added the badge file.

**Adding your own club badge:**

1. Find a PNG image of your club badge (from your club website or play-cricket.com)
2. Open `config.ini` and note the number next to `playcricket_id` — that is your club ID
3. Rename the badge image to `{your_id}.png` — for example `12345.png`
4. Create a folder called `logos` inside your CricketStream folder
5. Put the renamed badge file into that `logos` folder
6. Right-click the Overlay source in OBS → **Refresh**

Your badge appears immediately — no restart needed.

**Adding opposition badges:**

1. In the control panel, click **Fetch today's match**
2. The match details card shows the away club information
3. Visit that club's page on play-cricket.com — the number in the URL is their club ID
4. Download their badge, rename it `{their_id}.png`, and put it in your `logos` folder
5. Right-click the Overlay in OBS → **Refresh**

Over a season your badge library builds up automatically — once you have added a club
they will always have their badge shown whenever you play them again.

**Checking badge status:**

The control panel → Kit Colours section has a badge status display showing both teams.
A green tick means the badge file was found. An amber warning means it is missing.
You can see this before you go live so you know exactly what will appear on stream.

For full details including where to find club badge images, see **`CLUB_LOGOS.md`**.

## Troubleshooting

### The black window closes immediately when I run setup.bat or CricketStreamSetup.exe

Right-click it → **Run as administrator**

### The wizard couldn't install Python automatically

It opens the python.org download page instead — download it from there, tick **"Add Python to PATH"** during install (Windows) or run **Install Certificates.command** from the Python folder in Applications afterwards (Mac), then re-run the wizard. This is rare — it only happens without an internet connection, or on very old Windows versions without `winget`.

### It says "Python was not found" (when running setup.bat / setup.sh directly)

You need to install Python from python.org and make sure to tick **"Add Python to PATH"** during installation — or just use `CricketStreamSetup.exe` / `CricketStreamSetup-mac.zip` from Step 3 instead, which installs Python for you.

### The overlay shows on my browser but not in OBS

The overlay will appear white/blank in a browser — that's normal. It only shows correctly inside OBS. In OBS, right-click the Overlay source and click **Refresh**.

### The score isn't updating

- Make sure your scorer has started NV Play and scored at least one ball
- Check the PCS output folder path in config.ini matches what NV Play shows under Tools → Configuration → Scoreboard
- Open http://127.0.0.1:5000/control — if the PCS monitor is grey, the path is wrong

### The stream is choppy

- Close all other programs on the laptop while streaming
- In OBS → Settings → Output → lower the bitrate to 1500
- In OBS → Settings → Video → change Output Resolution to 1280×720

### I can't see the control panel

Make sure the black command window (the server) is still open. If you closed it, run quickstart.bat/sh again.

---

## Getting help

If you're stuck, the full technical setup guides are in the Windows and Mac folders. They cover every setting in detail.

For bugs or issues, raise them on GitHub:
**https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues**

Even if you've never used GitHub before, raising an issue is as simple as clicking New Issue and describing what's happening.

