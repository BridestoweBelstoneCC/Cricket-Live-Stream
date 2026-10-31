# The clubhouse screen

An **optional** touch screen in the clubhouse that runs the stream for you:

- **Start the stream** switches the streaming laptop on, opens OBS and gets match day
  ready — nobody has to find the laptop, sign in or click anything on it.
- **Get ready** shows the match-day checklist (OBS, camera, replays, today's match, the
  scorer's feed) with the buttons to fix each one, the pre-match check, and **Go live**.
- **YouTube** sets the stream's title, description and who can watch.
- **Cameras** finds IP cameras on the network and adds one, and cuts between the wide and
  bowler's-end cameras if you have two.
- **The score** — big enough to read across the bar — and a small picture of the stream in
  the corner. Tap it and the stream fills the screen (YouTube's own player, with sound).
  Beside it, **Scorecard** (both innings: batters, bowlers, fall of wickets — the scorer's
  own figures), **Overs** (the last six, ball by ball) and the league **Table** (with the
  club's row picked out; shown when today's match is in a league). Anyone can look; after
  a minute and a half untouched the screen goes back to the score.
- **Finish** ends the stream, ends match day, closes OBS and offers to switch the laptop off.

Anyone can see the score and the stream. Everything that changes anything needs the
screen's PIN.

Want the score on a screen with **no buttons at all** — the bar's TV, the pavilion, the
scorebox? That's a [score-only screen](#score-only-screens): the same Pi and the same
install, paired with a different key.

Nothing about it is specific to one club, and nothing changes for a club that doesn't
use it: it's off until someone switches it on, on the laptop.

## What you need

| | |
|---|---|
| **A Raspberry Pi 4 or 5** (2 GB or more) with **64-bit Raspberry Pi OS (with desktop)** | Any small 64-bit Linux PC works too. |
| **A touch screen** | The [Raspberry Pi Touch Display 2](https://www.raspberrypi.com/products/touch-display-2/) (7"), or any HDMI/USB touch monitor. A Pi 5 can also send the full-screen stream to the bar's TV on its second HDMI port. |
| **A network cable to the streaming laptop's network** | For the laptop, not the Pi: almost no laptop can be switched on over wifi. Plug the laptop into the same switch as the cameras. The Pi itself can be on wifi or a cable, as long as it's on the same network. |
| **CricketStream 3 on the streaming laptop** | The Go edition, with the "Clubhouse screen" card on its match-day screen. |

## 1. The streaming laptop

### Switch it on in CricketStream

Open CricketStream. On the match-day screen, under **Clubhouse screen**:

1. Tick **Let a clubhouse screen run match day on this laptop.** A pairing key appears
   (like `K8Q2-7WMX`) — you'll type it on the screen.
2. Tick **The screen may switch this laptop off when match day is over**, if you want it
   to. It never does while OBS is streaming or recording.
3. Tick **Open CricketStream when Windows starts.** Without this, the laptop wakes up but
   nothing answers the screen.

If the card says *No network cable found*, the screen can still run match day once the
laptop is on — but it can't switch it on. Plug the laptop in by cable.

### Let Windows sign in by itself

A woken laptop sitting at the sign-in screen can't open CricketStream. If the laptop is
the club's own, kept for streaming, set Windows to sign in automatically — Microsoft's
[Autologon](https://learn.microsoft.com/sysinternals/downloads/autologon) tool does it
safely (the password is stored encrypted). Only do this on a laptop used for nothing but
the stream: anyone who switches it on is signed in.

### Wake-on-LAN

This is what lets the screen switch the laptop on. Three places, once:

1. **The BIOS / UEFI** (a key at power-on — F2, F10, F12 or Del, depending on the make):
   enable **Wake on LAN** (sometimes *Power On by PCI-E*, *Remote Wake Up* or *Boot on
   LAN*). Some laptops call the setting *Wake on LAN from S4/S5* or only allow it on mains
   power — keep the charger in.
2. **Windows' Device Manager** → *Network adapters* → the wired adapter (*Ethernet*, not
   *Wi-Fi*) → Properties:
   - **Power Management**: tick *Allow this device to wake the computer* and *Only allow a
     magic packet to wake the computer*.
   - **Advanced**: set *Wake on Magic Packet* to *Enabled* (and *Shutdown Wake-On-Lan*,
     if listed).
3. **Fast Startup off**: Control Panel → Power Options → *Choose what the power buttons
   do* → *Change settings that are currently unavailable* → untick *Turn on fast startup*.
   With it on, "shut down" is really a half-hibernation that many network adapters won't
   wake from.

**The pre-match check checks all of this for you** (control panel → Setup → Run pre-match
check, or the match-day screen's own): the cable, whether Windows lets the network port
wake the laptop, Fast Startup, automatic sign-in, *Open CricketStream when Windows
starts* — and when the screen last switched the laptop on, and how long it took. It
shows ⚠ until the screen has woken the laptop once.

Then test it: shut the laptop down, and press **Start the stream** on the screen. If it
doesn't wake, check each step above; a few laptops (some ultrabooks, most USB network
adapters) can't wake from off at all — those can still be switched on by hand and the
screen takes over from there.

If you keep the laptop closed, set *When I close the lid* to **Do nothing** (Power Options)
on mains power, or it will go to sleep mid-match.

## 2. The Pi

### The easy way: the CricketStream image

Each release has **`CricketStream-clubhouse-pi.img.xz`**: Raspberry Pi OS (64-bit, with
desktop) with the clubhouse screen already in it. Nothing to type on the Pi.

1. Download it from the [releases](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases)
   (about 1.5 GB).
2. Open the [Raspberry Pi Imager](https://www.raspberrypi.com/software/) → *Choose
   Device* (your Pi) → *Choose OS* → **Use custom** → the file you downloaded → *Choose
   Storage* (the SD card).
3. When it asks about OS customisation, **Edit settings**: a username and password, the
   club's wifi (or plug in a cable), and the time zone. Save, and write the card.
4. Put the card in the Pi and switch it on. It starts up (the first start takes a few
   minutes and restarts once) and opens the screen full screen, ready to pair.

The image signs in to the desktop by itself and opens the screen at every start. It has
no password or account of its own — those are the ones you set in the Imager. To update
CricketStream on it later, run the installer below once: it puts the new version in
`~/cricketstream`, which is used from then on.

### Or: install it on Raspberry Pi OS yourself

1. Install **Raspberry Pi OS (64-bit) with desktop** with the
   [Raspberry Pi Imager](https://www.raspberrypi.com/software/). In the Imager's settings,
   set a username and the club's wifi (or plug in a cable).
2. Make it sign in to the desktop by itself: `sudo raspi-config` → *System Options* →
   *Boot / Auto Login* → *Desktop Autologin*.
3. Open a terminal on the Pi and run:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/BridestoweBelstoneCC/Cricket-Live-Stream/main/scripts/clubhouse/install-pi.sh | bash
   ```

   It puts CricketStream in `~/cricketstream`, installs Chromium if needed, and makes the
   screen open full screen whenever the Pi starts. (No internet on the Pi? Download
   `CricketStream-linux-arm64` from the [releases](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases)
   onto a USB stick with the script, and run `bash install-pi.sh` beside it.)
4. Restart the Pi (`sudo reboot`).

The screen opens on **Pair with the streaming laptop** (the laptop must be on, with
CricketStream open and the clubhouse screen ticked on its card):

- **Pair from the laptop** — nothing to type. The screen shows a three-digit code, and
  the laptop's Clubhouse card shows *A screen is asking to pair* with the same code.
  Check they match, and press **Match-day screen** (or **Score-only screen**) on the
  laptop. The screen pairs a moment later. If the codes don't match, press **No**:
  something else on the network is asking.
- Or type the **pairing key** from the laptop's card and press **Pair**.

If the club's network blocks the search — some guest wifi does — press *Laptop not
found?* and type the laptop's address too (it's on the same card); either way of pairing
then uses it.

Then set a **PIN**: four to eight digits. Without one, anyone in the clubhouse can start
and stop the stream.

## Match day

1. On the screen: **Start the stream** → enter the PIN → choose how the match is scored.
   The laptop switches on, signs in, opens CricketStream, and match day starts: OBS, the
   scenes, today's match, the graphics. The screen shows each step.
2. **Get ready** lists what's left. Press the buttons; **Go live** is the last one.
3. Leave it on **Score** for the clubhouse. The stream picture sits in the corner; tap it
   for full screen, and **Close** to come back. After ten minutes untouched the screen
   locks itself — the score carries on.
4. At the end: **Finish** → **End the stream**. The laptop asks whether to write the AI
   match report — answer on the screen (or the laptop). Then **Switch it off**: the laptop
   switches off a minute later unless someone presses **Keep it on**.

The laptop's own window still works throughout: anything done on one shows on the other.

## Score-only screens

A score-only screen shows the score, the Scorecard, Overs and Table views and the stream
picture, and nothing else: no Start, no Get ready, no Finish. It can't switch the laptop on
or off. Use it anywhere people can touch it — a club can have one match-day screen and any
number of score-only ones, or score-only screens alone.

1. On the laptop, on the match-day screen's **Clubhouse screen** card, tick **Let
   score-only screens show the score**. A second key appears, the **score-only key**.
2. Set up the Pi exactly as in [2. The Pi](#2-the-pi). Press **Pair from the laptop** on
   it and **Score-only screen** on the laptop — or type the **score-only key**.

The screen says *No match on* until match day starts on the laptop, then shows the score
by itself. The laptop doesn't need a network cable for it (nothing is woken). A PIN on a
score-only screen guards only its settings — pairing again, the PIN itself.

The laptop is what enforces it: a screen holding the score-only key can read the score,
the scorecard and table, the club's colours and badge, the stream picture and whether
it's on air, and the laptop refuses everything else from it, whatever the screen asks. It
isn't shown the laptop's questions or its activity log either. **New key** beside the
score-only key stops every score-only screen until it's paired again with the new one;
the match-day screen's key is separate and carries on.

## How it works (and what it can't do)

- The Pi serves the screen itself, because the laptop is off when it's first touched.
  It switches the laptop on with a Wake-on-LAN "magic packet" sent on the club network.
- The laptop listens for the screen on **port 5096** (TCP and UDP), only while the
  clubhouse screen is switched on. Every request needs the pairing key; five wrong keys
  from one address and it's refused for a couple of minutes. The one exception is asking
  to pair, which only puts a request on the laptop's own card: the laptop picks the code,
  a few requests at most wait at once, and the key goes only to the screen that asked,
  once, after someone at the laptop said yes.
- The screen can only reach the control panel's match-day buttons and reads — the
  checklist, going live and stopping, the pre-match check, YouTube's title, adding a
  camera, the camera cut, the score, the stream picture. It can't open Settings, see the
  club's passwords or keys, or export anything. It never has the club password: the
  laptop's own login is used.
- Like the control panel, it's plain HTTP on the club network. Pair it on a network you
  trust (the club's own, not open public wifi), and make a **New key** on the laptop if
  you ever think the key has got out — the old one stops working at once.
- The stream picture in the corner is a small screenshot from OBS every two seconds, only
  while someone's looking — not a second video encode on a laptop that has overheated
  before. Full screen is YouTube's own player, so it's the viewers' picture, a few
  seconds behind.
- **Switching on needs the laptop on a cable**, Wake-on-LAN set up, and Windows signing
  in by itself. Without those, switch the laptop on by hand: the screen notices, and
  everything else still works.

## Troubleshooting

| What you see | What to do |
|---|---|
| *Switching the laptop on* never finishes | Wake-on-LAN (above). Check the laptop's on mains power and its cable is in. Try **Start the stream** from the laptop's sleep rather than off, to tell a wake problem from a sign-in one. |
| The laptop wakes but the screen keeps waiting | Windows is at the sign-in screen, or CricketStream didn't open: *Open CricketStream when Windows starts*, and automatic sign-in. |
| *The laptop doesn't accept this screen's pairing key any more* | Someone pressed **New key** on the laptop. On the screen: ⚙ → **Pair again**, and type the new key. |
| A screen shows *No match on* and no buttons | It's paired with the score-only key. For the match-day buttons: ⚙ → **Pair again** with the other key. |
| *No streaming laptop answered* when pairing | CricketStream open on the laptop, the clubhouse screen ticked, both on the same network. Windows Firewall may ask the first time — allow CricketStream on **private** networks. Or type the laptop's address. |
| The stream picture is black | OBS isn't showing anything yet (no camera added), or OBS is closed — see **Get ready**. |
| Full screen shows the small picture, not YouTube | The stream isn't live on YouTube yet, or YouTube isn't connected on the laptop. |

The Pi writes what it's doing to `~/cricketstream/clubhouse.log`; the laptop's side is in
CricketStream's activity log ("Clubhouse screen …").
