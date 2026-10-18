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
- CricketStream itself — one file, nothing else to install
- OBS Studio — the software that mixes your camera with the graphics and sends it to YouTube

---

## Option A — Let Claude set it up for you

If you (or someone at your club) has access to Claude, Anthropic's AI assistant, it can walk
you through this entire setup conversationally — installing OBS, running the setup wizard,
and getting your camera working. This
is genuinely the fastest route if you're not confident with computers, and it's how the most
recent setup of this project was actually done.

**What you need:**
- **Claude Code** — a free download from **claude.com/code** that runs in a terminal window
  and can read files, run commands, and see error messages on your computer as you go.
- A Claude account (a free account works; a paid plan gives more room for a long session).

**How to use it:**

1. Download CricketStream (see Step 2 under Option B below) and open a terminal in the
   folder you put it in.
2. Run `claude` to start it.
3. Paste in this prompt to get started:

   > I've just downloaded CricketStream Overlay for my cricket club and I have no coding
   > experience. Please read FOR_NON_TECHNICAL_USERS.md from
   > https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream, then walk me through the
   > entire setup step by step: installing OBS Studio, running CricketStream's first-time
   > setup, and getting my camera source working. Ask me one question at a time, explain what each step does in plain
   > English, and don't assume I know any technical terms.

4. Answer its questions as you go — club name, kit colour, camera type, and so on. It'll tell
   you exactly what to click and where.

**Worth knowing:**
- Claude can read and run things on your computer, but it can't physically click buttons
  inside OBS's window for you — it'll tell you exactly what to click, and you do the clicking.
- If anything looks wrong or you're unsure about a step, just ask — that's what it's there for.
- This doesn't replace Option B below, it's the same setup, just with a guide sitting next to
  you instead of a page you read on your own.

---

## Option B — Do it yourself, step by step

Prefer to follow written instructions at your own pace, or don't have access to Claude?
Here's the full manual walkthrough.

### Step 1 — Download and install OBS Studio

1. Go to **https://obsproject.com**
2. Click the button for your operating system (Windows or macOS)
3. Run the installer and follow the prompts
4. That's it — you don't need to open OBS. CricketStream opens it and sets it up for you
   (resolution, encoder, replays, the lot), including the first time

---

### Step 2 — Download CricketStream

Go to the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest)
page and download the one file for your computer:

- **Windows:** `CricketStream.exe`
- **Mac:** `CricketStream-mac.zip` (works on both Apple Silicon and Intel Macs) — double-click
  it to unzip

Make a folder for it somewhere permanent — **Documents → CricketStream** is a good choice —
and move the file (on a Mac, both unzipped files) into it.

> **Why its own folder?** CricketStream keeps everything it saves — your settings, the match
> data, club badges, sponsor logos — right next to itself. A folder of its own keeps that
> tidy, and means you can back the whole lot up (or move it to a new laptop) by copying
> one folder. Not the Downloads folder, which some computers empty by themselves.

---

### Step 3 — First-time setup

**Windows:** double-click `CricketStream.exe`. **Mac:** double-click **`CricketStream.command`**.

> **The first time on a Mac**, macOS says it can't check the app for malicious software.
> Click **Done**, then open **System Settings → Privacy & Security**, scroll down to the
> message about CricketStream and click **Open Anyway**. It only asks once.
>
> **The first time on Windows**, a blue "Windows protected your PC" box may appear. Click
> **More info → Run anyway**. Windows may also ask whether CricketStream can use the
> network — tick **Private networks** and click **Allow**.

> **This is the only file you ever need to run.** It works out for itself what still
> needs doing: the first time it asks you the setup questions below; every time after that
> it just starts the match. So this same file is both "set it up" and "go live on
> Saturday" — there's nothing else to find.

A black window opens, and then a page in your web browser with the setup questions. The
first one is **what this computer is for** — pick **Streaming laptop** (the one with the
camera and OBS). If your scorer has their own laptop, see
[TWO_LAPTOP_SETUP.md](TWO_LAPTOP_SETUP.md): it's the same file, and on that laptop you pick
one of the other two answers.

Then:

- **Club name** — your full club name as it will appear on screen
- **Abbreviation** — a short version for the scorebar, max 6 characters (e.g. TAUN, STOW,
  EXE). It fills itself in from the club name; change it if you like
- **Home kit colour** — click the colour box and pick your kit colour, or paste a code that
  starts with `#` from **htmlcolorcodes.com**
- **PlayCricket club ID** — the number in your club's play-cricket.com address
- **PlayCricket API key** — email PlayCricket support to request one (it's free)
- **Scoreboard output folder** — where NV Play saves the live score. If NV Play is on this
  laptop it's usually found for you; otherwise your scorer can see it in NV Play under
  Tools → Configuration → Scoreboard
- **Your stream** — pick one of four scorebar looks and see your stream as viewers will:
  your name and colours over a drawing of a ground (or a photo of yours), with buttons to
  try the wicket card, a four, a six, the end-of-over summary and more
- **Replay folder** — where replay clips are saved; the suggested one is fine
- **YouTube stream key**, **AI commentary key**, **club password** — all optional

For anything you don't have yet, leave it blank — you can fill it in later. The **Check**
buttons next to the PlayCricket key, the scoreboard folder and the AI key try them there
and then, so a typo shows up now rather than on match morning. On Windows, leave **Put a
CricketStream shortcut on the desktop** ticked and you'll have an icon to double-click.

Click **Save and start** and the black window carries straight on into starting the
match (you can close it if today isn't a match day).

> If anything goes wrong, see the Troubleshooting section at the bottom.

---

## Step 4 — Set up OBS

**Nothing to switch on.** The WebSocket server (how the software talks to OBS) and the
Replay Buffer (saves clips for replays) are both turned on for you, and OBS is opened for
you, every time you double-click `CricketStream.exe` / `CricketStream.command`. The scenes
CricketStream needs (**Main** with the overlay, **Replay** for replays) are made for you too.

If OBS is already open when you do, it may ask you to close OBS first. That only happens
when something needs changing, and OBS can only have its settings changed while it's shut.

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

1. Double-click the **same file you used to set it up**:
   **Windows:** `CricketStream.exe` · **Mac:** `CricketStream.command`

   It skips the setup questions this time (it can see you've already answered them) and
   goes straight to starting the match — opening OBS, finding today's fixture, and
   starting the server. It also starts a small logger that records how the stream is
   doing (useful if anyone needs to work out why it stuttered) — you don't need to do
   anything with it.

   > Want to change the setup answers later — a new club password, say, or a different
   > scorebar style? In the control panel, open the **Setup** tab and press **Open
   > settings**: the same page, with your answers filled in, and the changes show on the
   > stream straight away.

2. A window appears showing the software starting up. You should see your club name, today's opposition, and the umpires' names appear automatically.
3. Your web browser opens the control panel by itself (it lives at
   **http://127.0.0.1:5000/control** if you ever need to type it in).
   This is your control panel. The **Match-day checklist** at the top ticks itself as
   things become ready, and anything not ready has a button next to it that does it —
   **Start OBS**, **Start replay buffer**, **Fetch today's match**, and **Go live** when
   you're ready
4. In OBS, the Controls panel on the right should say **Stop Replay Buffer** — that means
   replays are ready (it's started for you)

**When the scorer starts:**

5. Your scorer opens NV Play and starts the match
6. After the first ball, the scorebar on the overlay fills in with live batter names and bowler figures

**Going live:**

7. In OBS, click **Start Streaming**
8. Your stream is live on YouTube

**After the match:**

9. In the control panel, click **Compile highlights reel** — this automatically stitches all the replay clips into one video
10. Click **Stop Streaming** in OBS
11. In the control panel's **After the match** tab you can make a written match report, a social post, and an Instagram result picture — all from the scorer's figures, ready to copy
12. Still in **After the match**: **Make social clips** turns each wicket, four, six and
    milestone into a short upright video for Instagram, TikTok or YouTube Shorts, with a
    caption to copy. Each one has a Download button, which works from your phone too.
13. A **match page** (one web page with the result, the report, both scorecards and a link
    to the stream) is made by itself about a minute after the stream stops. Open it from
    **After the match → Match page** and send it to the club website or WhatsApp group.
14. Press **Ctrl+C** in the black window (or just close it) to stop. Ctrl+C is the better
    way: it first prints the sponsors' screen time for the match and saves the match
    report, both as files in the CricketStream folder.

> **If OBS crashes during the match**, leave it — it reopens by itself within a few
> seconds and the stream carries on. Closing OBS yourself after the match is fine; it
> stays closed.

---

## What the control panel does

When the software is running, open **http://127.0.0.1:5000/control** in your browser. This is your dashboard. From here you can:

- See live data coming in from the scorer (green means connected)
- Turn graphics on and off (fall of wicket, boundaries, replays etc.)
- Show or hide the weather widget
- Update the YouTube stream title
- Manually change the opposition name or kit colour
- After the match: compile highlights, make social clips, open the match page, and make a match report, social post and Instagram result picture
- A few days before a match: **Setup → Run pre-match check** tries every login and
  connection and tells you in plain words what (if anything) needs fixing
- The night before: **Match day → Practice match** plays a made-up match through your
  stream, so you can see every graphic and replay working in OBS without a scorer. It
  puts everything back afterwards
- Optional extras, all off until you switch them on: the **AI camera spotter** (warns you
  if a camera lens fogs up or gets knocked — costs a few pence a match), and **spoken
  commentary** (a computer voice reads out the end-of-over line)

You don't need to use this every match — once you've answered the setup questions, double-clicking CricketStream handles everything automatically.

---


## Club badges in the scorebar

The scorebar can show a small circular badge next to each team name — like the
ones you see on professional cricket broadcasts. They appear automatically once
you have added the badge file.

**Adding your own club badge:**

1. Find a PNG image of your club badge (from your club website or play-cricket.com)
2. Open `config.ini` and note the number next to `playcricket_id` — that is your club ID
3. Rename the badge image to `{your_id}.png` — for example `12345.png`
4. Create a folder called `logos` inside your CricketStream folder (next to
   `CricketStream.exe`)
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

### The black window closes immediately

It shouldn't: whatever goes wrong, CricketStream stops and waits with *"Press Enter to
close this window..."* so you can read why. If the window really does vanish, open Command
Prompt (press Start, type `cmd`), drag `CricketStream.exe` into that window and press Enter
— it runs the same way, but the window stays open afterwards.

### My settings have disappeared

CricketStream keeps `config.ini` (your answers) next to itself. If you've moved the file
to a new folder, or downloaded a new copy somewhere else, it starts fresh. Move the new
`CricketStream.exe` into your old CricketStream folder, replacing the old one — your
settings, logos and match data are all still there.

### The setup page didn't open

The black window prints its address — something like `http://127.0.0.1:5000/setup`.
Type that into your web browser on the same computer.

### The overlay shows on my browser but not in OBS

The overlay will appear white/blank in a browser — that's normal. It only shows correctly inside OBS. In OBS, right-click the Overlay source and click **Refresh**.

### The score isn't updating

- Make sure your scorer has started NV Play and scored at least one ball
- Check the PCS output folder path in config.ini matches what NV Play shows under Tools → Configuration → Scoreboard
- Open http://127.0.0.1:5000/control — if the PCS monitor is grey, the path is wrong

### The stream is choppy

- Close all other programs on the laptop while streaming
- Open `config.ini` in Notepad (TextEdit on a Mac). Under `[Stream]`, set
  `output_resolution = 720p` and `bitrate_kbps = 1500`, save, and start CricketStream again.
  (Don't change these in OBS itself — CricketStream sets OBS's video settings every time it
  starts, so a change made in OBS only lasts until then.)

### I can't see the control panel

Make sure the black window (the server) is still open. If you closed it, double-click CricketStream again.

---

## Getting help

If you're stuck, the full technical setup guides are [SETUP_GUIDE_WINDOWS.md](SETUP_GUIDE_WINDOWS.md) and [SETUP_GUIDE_MAC.md](SETUP_GUIDE_MAC.md). They cover every setting in detail.

For bugs or issues, raise them on GitHub:
**https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues**

Even if you've never used GitHub before, raising an issue is as simple as clicking New Issue and describing what's happening.

For quick questions — or if something's gone wrong on match day and you need a hand — join the Discord:
**https://discord.gg/2nuBPj9rH7**

