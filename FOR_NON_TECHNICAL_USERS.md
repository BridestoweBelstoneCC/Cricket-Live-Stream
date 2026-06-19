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
- Python — a programming language the software is written in. You don't need to know how to use it, just install it
- OBS Studio — the software that mixes your camera with the graphics and sends it to YouTube

---

## Step 1 — Download and install Python

1. Go to **https://python.org/downloads**
2. Click the big yellow button that says **Download Python**
3. Open the downloaded file and run the installer
4. **Windows only:** On the first screen, tick the box that says **"Add Python to PATH"**. It's near the bottom and easy to miss.
5. Click **Install Now**
6. When it finishes, click Close

### Mac only — fix SSL certificates

After installing Python on a Mac you must run one extra step, otherwise the software
will hang when trying to connect to PlayCricket and the control panel will show
"Checking connection..." indefinitely.

1. Open **Finder**
2. Go to **Applications**
3. Open the **Python 3.x** folder (the version you just installed)
4. Double-click **Install Certificates.command**
5. A terminal window opens and runs — wait for it to say **"update complete"**
6. Close the window

You only need to do this once.

---

## Step 2 — Download and install OBS Studio

1. Go to **https://obsproject.com**
2. Click the button for your operating system (Windows or macOS)
3. Run the installer and follow the prompts
4. Open OBS — it will ask to run the Auto-Configuration Wizard. Click **Yes**
5. Select **Optimise for streaming** → Next
6. Set Video Resolution to **1280x720** → Next
7. It will test your connection. Click **Apply Settings** when done

---

## Step 3 — Download the cricket stream software

1. Go to **https://github.com/bridestowebelstone/Open-Source-Cricket-Stream**
2. Click the green **Code** button
3. Click **Download ZIP**
4. Open the ZIP and extract the folder somewhere permanent — your Documents folder is fine
5. **Windows users:** Open the `Windows` folder inside
6. **Mac users:** Open the `Mac` folder inside

---

## Step 4 — Install the packages

This is a one-off step that gives Python everything it needs.

**Windows:** Double-click **`install.bat`**
A black window will appear and show some text. Wait for it to say **"All packages installed successfully"** then press any key.

**Mac:** Right-click **`install.sh`** → Open → Open
A window will appear. Wait for it to say **"All packages installed successfully"** then press Enter.

If anything goes wrong here, see the Troubleshooting section at the bottom.

---

## Step 5 — Fill in your club details

Open the file called **`config.ini`** — this is the only file you ever need to edit.

**Windows:** Right-click → Open with → Notepad
**Mac:** Right-click → Open with → TextEdit (then go Format → Make Plain Text)

You'll see something like this:

```
[Club]
name = Your Club CC
abbreviation = YCC
home_colour = #1a3a5c
playcricket_id = 12345
motto =
```

Change each line to match your club:

**name** — Your club's full name as you want it to appear on screen

**abbreviation** — A short version for the scorebar. Maximum 6 characters. For example: BBCC, TAUN, OKE

**home_colour** — The hex code for your kit colour. To find yours:
- Go to **htmlcolorcodes.com**
- Click your kit colour on the colour wheel
- Copy the code that starts with # (like #1a3a5c for navy)

**playcricket_id** — The number that identifies your club on PlayCricket:
- Go to play-cricket.com and find your club's page
- Look at the URL — there will be a number in it. That's your ID.

**motto** — Optional. Whatever you want to show on the replay screen (e.g. "Up the Stags"). Leave it blank to show nothing.

Further down you'll also need to fill in:

**playcricket_key** — Your PlayCricket API key. Email PlayCricket support to request one. It's free.

**pcs_output_folder** — The folder where your scorer's NV Play software saves its data. Your scorer will know this — it's set inside NV Play under Tools → Configuration → Scoreboard.

**obs_password** — You set this in OBS. See Step 6.

**replay_folder** — Where you want replay clips saved. Create a folder called Replays in your Videos or Movies folder and paste the path here.

Save and close the file when done.

---

## Step 6 — Set up OBS

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

## Step 7 — Set up your camera in OBS

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

   > ⚠️ **Always use quickstart — never run `server.py` directly.**
   > Quickstart reads your `config.ini`, builds the settings file, and then starts
   > the server. If you run `server.py` on its own first, the control panel will be
   > empty and nothing will save correctly. Run quickstart once to fix it.

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
3. Rename the badge image to `{your_id}.png` — for example `29434.png`
4. Create a folder called `logos` inside your BBCC Stream folder
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

### The black window closes immediately when I run install.bat

Right-click **`install.bat`** → **Run as administrator**

### It says "Python was not found"

You need to reinstall Python and make sure to tick **"Add Python to PATH"** during installation.

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
**https://github.com/bridestowebelstone/Open-Source-Cricket-Stream/issues**

Even if you've never used GitHub before, raising an issue is as simple as clicking New Issue and describing what's happening.

