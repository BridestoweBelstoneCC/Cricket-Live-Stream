# Running NV Play on Mac via VMware Fusion
## Complete setup guide for streaming with CricketStream Overlay

This guide covers running OBS natively on your Mac (for full performance streaming)
while running NV Play inside a free Windows virtual machine (for ball-by-ball scoring
data). The two communicate via a shared folder — NV Play writes the output file inside
the VM, your Mac reads it directly.

---

## What you need

- Mac (2015 or later recommended)
- VMware Fusion — free download (see Step 1)
- A Windows 11 licence — approximately £20 from Microsoft
- OBS Studio for Mac — https://obsproject.com
- Python 3 — installed automatically by the setup wizard (see `SETUP_GUIDE_MAC.md`), or get it yourself from https://python.org/downloads
- NV Play / PCS Pro — downloaded from your PlayCricket account

---

## Step 1 — Download and install VMware Fusion

VMware Fusion is free for personal use.

1. Go to **https://www.vmware.com/products/fusion.html**
2. Click **Try Fusion** or **Download Free**
3. Create a free Broadcom account if prompted (required since Broadcom acquired VMware)
4. Download **VMware Fusion** for Mac
5. Open the downloaded `.dmg` and drag VMware Fusion to your Applications folder
6. Open VMware Fusion — you may need to go to **System Preferences → Privacy & Security**
   and click **Allow** when prompted about the kernel extension

---

## Step 2 — Install Windows 11 inside VMware Fusion

1. Open VMware Fusion
2. Click **+** → **New Virtual Machine**
3. Select **Install Windows or another OS from a disc or image**
4. Click **Install from Microsoft** — Fusion will download Windows 11 ARM automatically
   *(if this option isn't available, download the Windows 11 ISO from microsoft.com manually)*
5. Follow the Windows setup wizard inside the VM
6. When asked for a Windows licence key — enter yours or click **I don't have a product key**
   to activate later
7. Let Windows finish installing — takes 10-20 minutes

**Recommended VM settings** (VMware Fusion → Virtual Machine → Settings):
- Processors: **2 cores**
- Memory: **4 GB** (enough for NV Play — leave the rest for OBS)
- Hard Disk: **60 GB**

---

## Step 3 — Set up folder sharing between Windows and Mac

This is the key step. You need NV Play's output folder to be accessible from macOS.

### In VMware Fusion:
1. With the Windows VM running, go to **Virtual Machine → Settings**
2. Click **Sharing**
3. Tick **Enable Shared Folders**
4. Click **+** and add a folder from your Mac — create a dedicated folder first:
   - On your Mac, create a folder: `~/Documents/NVPlay Shared/`
5. Name it `NVPlayShared` in the sharing settings
6. Click OK

### In Windows (inside the VM):
The shared folder will appear in Windows Explorer at:
```
\\vmware-host\Shared Folders\NVPlayShared\
```
Or as a mapped network drive — VMware usually maps it automatically as the `Z:` drive.

### Set NV Play to output to the shared folder:
1. Open NV Play inside the VM
2. Go to **Tools → Configuration → Scoreboard**
3. Tick **Enable Scoreboard Output**
4. Set **Output Folder** to the shared folder path:
   ```
   Z:\
   ```
   Or browse to `\\vmware-host\Shared Folders\NVPlayShared\`
5. Set **Template File** to `scoreboard.template`
   (copy this file into the VM first — see Step 5)
6. Click OK

---

## Step 4 — Find the Mac path to the shared folder

Back on your Mac (outside the VM), the shared folder is accessible at:

```
/Users/YourUsername/Documents/NVPlay Shared/
```

You can confirm this by opening **Finder → Documents → NVPlay Shared** — you should
be able to see any files that Windows has written there.

This is the path you will paste into the CricketStream Overlay control panel.

---

## Step 5 — Copy the template file into the VM

NV Play needs the `scoreboard.template` file in its Templates folder.

**Easiest method — via the shared folder:**
1. On your Mac, copy `scoreboard.template` into `~/Documents/NVPlay Shared/`
2. Inside the Windows VM, open Explorer → navigate to the shared folder
3. Copy `scoreboard.template` to:
   ```
   C:\Users\YourWindowsUser\Documents\Cricket Matches\_Scoreboards\Templates\
   ```
4. In NV Play → Tools → Configuration → Scoreboard → browse to that Templates folder
   and select `scoreboard.template`

---

## Step 6 — Install and start the CricketStream Overlay server

On your Mac (not inside the VM):

1. Unzip `cricketstream_mac.zip` to `~/Documents/CricketStream/`
2. Open Terminal and install the Python packages:
   ```bash
   pip3 install websocket-client anthropic google-api-python-client google-auth-oauthlib
   ```
3. Start the server:
   ```bash
   cd ~/Documents/CricketStream
   python3 server.py
   ```
4. Open your browser and go to `http://localhost:5000/control`

---

## Step 7 — Configure the control panel

In the control panel → **Match card**:

| Field | Value |
|---|---|
| Home team name | Your Club CC |
| Home scorebar abbrev. | YOURCC |
| PCS Pro output folder | `/Users/YourUsername/Documents/NVPlay Shared` |
| Use widget as fallback | On (safety net if VM isn't running) |

Click **Save**.

To confirm it's working, go to `http://localhost:5000/pcs/debug` in your browser.
If the shared folder and template are set up correctly you will see the output file
listed under `files` and `folder_exists: true`.

---

## Step 8 — Set up OBS on your Mac

Follow the standard OBS setup from the main Mac setup guide. Key points:

- OBS runs **natively on macOS** — do not install it inside the VM
- Use **Apple VT H264 Hardware Encoder** in OBS settings — much faster than software encoding
- Overlay URL: `http://localhost:5000/overlay`
- The overlay, server, and OBS all run on your Mac; the VM runs only NV Play

---

## Match day workflow

### Before the match

1. **Start the Windows VM** — open VMware Fusion and start the Windows VM
   *(you can leave the VM running minimised in the background)*
2. **Open NV Play** inside the VM and start the match
3. **Start the server** on your Mac: `python3 server.py` in Terminal
4. **Open the control panel** at `http://localhost:5000/control`
   - Set opposition name and abbreviation
   - Make sure demo mode is **OFF**
   - Click Save
5. **Open OBS** on your Mac and click **Start Replay Buffer**

### First ball

6. Score the first ball in NV Play
7. The output file is written to the shared folder instantly
8. Within 2-3 seconds the **PCS Pro Live Data Feed** in the control panel goes green
9. Batter names, bowler, and score appear on the overlay

### Going live

10. Click **Start Streaming** in OBS

### After the match

11. Use **Compile Highlights Reel** in the control panel
12. Click **Stop Streaming** in OBS
13. You can shut down the Windows VM

---

## Performance expectations

On a 2015 MacBook Pro 15":

| Task | Resource usage |
|---|---|
| OBS streaming at 720p | ~40-50% CPU, uses AMD GPU |
| Windows VM with NV Play | ~15-20% CPU, ~3GB RAM |
| CricketStream server | ~2-3% CPU |
| Overlay browser source in OBS | Uses GPU — minimal CPU |
| **Total** | **~60-70% CPU** — comfortable headroom |

On a 2015 MacBook Pro 13":

| Task | Resource usage |
|---|---|
| OBS streaming at 720p | ~50-60% CPU |
| Windows VM with NV Play | ~15-20% CPU, ~3GB RAM |
| **Total** | **~75-80% CPU** — usable but monitor for drops |

**Tips for the 13":**
- Set OBS stream bitrate to 2000 Kbps rather than 2500
- Allocate only 2 cores and 3GB RAM to the VM
- Close all other Mac applications during the stream

---

## Slimming the VM down (scoring-only Windows)

The VM has exactly one job: run NV Play and write the scoreboard file to the shared
folder. Everything else Windows does in there is stealing CPU from OBS on the Mac side.
Stripped down, the VM idles near 0% instead of chattering away at 5-15% doing Microsoft
things nobody asked for.

**Take a VMware snapshot first** (Virtual Machine → Snapshots → Take Snapshot) — every
change below is easily undone by rolling back.

### The big wins, in order

1. **Kill everything in Startup.** Inside the VM: Task Manager → **Startup apps** →
   disable every single entry. NV Play you open yourself on match day.
2. **Uninstall the consumer junk.** Settings → Apps → Installed apps: remove OneDrive,
   Teams, Xbox (all of it), News, Weather, Solitaire, Spotify/Disney promos, Copilot,
   and anything else you didn't install. None of it is needed to score cricket.
3. **Disable the background churn services.** Run `services.msc`, set these to
   **Disabled** (right-click → Properties → Startup type):
   - **Windows Search** — file indexing; you'll never search inside this VM
   - **SysMain** — prefetching that actively hurts VMs
   - **Delivery Optimization** — uploads Windows updates to strangers
   - **Print Spooler** — unless you genuinely print from the VM
   - **Xbox Live** services (all of them, if the app removal left any behind)
4. **Turn off the eye candy.** Search "performance" → *Adjust the appearance and
   performance of Windows* → **Adjust for best performance**. Also Settings →
   Personalisation → turn off transparency and animation effects.
5. **Background apps and tips.** Settings → Privacy → General: everything off.
   Settings → System → Notifications: off, including "tips and suggestions".

### Windows Update: pause it, don't fight it

Don't disable updates permanently (that fight always ends badly). Instead, make a
**pre-match ritual** of it: Settings → Windows Update → **Pause updates for 1 week**
every Friday. That guarantees no surprise "Restarting to finish updates…" at 40-2 off
15 overs. Let it catch up on a weekday.

Leave **Microsoft Defender alone** — it's not worth the risk to remove, and its idle
cost is small. Just check Task Scheduler isn't running a full scan on Saturdays.

### VMware Fusion side (Settings, VM shut down)

- **Disable 3D acceleration** (Display settings) — NV Play doesn't need it, and it
  contends with OBS for the Mac's GPU
- **Remove/disconnect** the sound card, camera, and printer sharing — the VM needs none
  of them (keep the network: NV Play uses it for licensing and Play-Cricket sync, and
  keep the **shared folder** — that's the whole point)
- In Windows, run `powercfg /h off` in an admin prompt — deletes the multi-GB
  hibernation file the VM will never use

### What NOT to remove

- **The network adapter** — NV Play licensing and Play-Cricket publishing need it
- **The shared folder / VMware Tools** — that's how the Mac reads the scoreboard file
- **Defender / the firewall** — not worth it for the pennies of CPU

Nothing from CricketStream Overlay runs inside the VM — no Python, no server, nothing to
install or maintain there. If Python or project files ended up in the VM during earlier
experiments, they can be uninstalled.

*(Community "debloat" scripts like Chris Titus's WinUtil can do steps 1-5 in one pass —
fine if you're comfortable with them, but take the snapshot first and stick to the
standard/recommended tweaks, not the aggressive ones.)*

---

## Troubleshooting

### NV Play output file not appearing in the shared folder

- Check the Output Folder in NV Play is pointing at the shared folder, not a local Windows path
- Make sure folder sharing is enabled in VMware Fusion → Virtual Machine → Settings → Sharing
- Try setting the output folder to `Z:\` (the mapped drive letter) rather than the UNC path
- Score a ball in NV Play — the file is only written after the first delivery

### Shared folder not visible in Windows

- Go to VMware Fusion menu → Virtual Machine → Reinstall VMware Tools
  (VMware Tools enables the shared folder feature inside Windows)
- Restart the Windows VM after reinstalling

### PCS debug shows folder_exists: false

- Check you are using the Mac path (starting with `/Users/`) not the Windows path
- The Mac path should be the folder on your Mac, e.g.:
  `/Users/yourname/Documents/NVPlay Shared`
- Not the Windows path like `Z:\` or `\\vmware-host\...`

### OBS dropping frames

- Right-click the VM window → **Pause** during the stream
  NV Play does not need to run continuously — it only writes when you score a ball
  You can un-pause to score, then pause again
- Reduce VM to 1 core in VMware Fusion settings
- Lower OBS output to 720p if not already

### Port 5000 conflict (AirPlay)

macOS uses port 5000 for AirPlay Receiver which will block the server.
Fix: **System Preferences → General → AirDrop & Handoff → untick AirPlay Receiver**

Or change the server port: edit `server.py` and change `PORT = 5000` to something else (e.g.
`PORT = 5050`), then use `http://localhost:5050/control` — the OBS browser source URL in the
control panel updates itself automatically, no need to change that separately.

### VMware Fusion asks for payment

Make sure you downloaded **VMware Fusion** (for personal use — free), not
**VMware Fusion Pro** (paid). The free version is fully capable for running NV Play.

---

## Alternative: UTM (fully free and open source)

If you prefer not to create a Broadcom account for VMware Fusion, UTM is an alternative:

1. Download from **https://mac.getutm.app** — free on the website (paid on Mac App Store)
2. Create a new VM → select **Virtualize** → **Windows**
3. UTM will guide you through downloading Windows 11 ARM
4. Shared folders work similarly — UTM → VM settings → Sharing → add a Mac folder

UTM is slightly slower than VMware Fusion for Windows but more than adequate for NV Play.

---

## Summary

```
Mac (native)                          Windows VM (background)
─────────────────────────────         ────────────────────────
OBS Studio                            NV Play / PCS Pro
server.py (Python)              ←──── Output file written here
Control panel (browser)               (every ball, instantly)
Overlay (browser source)
```

The VM does one job — run NV Play and write the output file.
Everything else runs natively on your Mac at full speed.
