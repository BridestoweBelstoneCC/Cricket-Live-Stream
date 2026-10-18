# Running the stream on a second laptop

**What this is for:** keeping the stream off the scorer's machine. The scorer's
laptop does one job — scoring. Your laptop does the stream, OBS, the overlay and
all the fiddling. If a graphic misbehaves at 3pm you fix it on your own screen,
without leaning across the scorebox.

The scoring laptop runs CricketStream too — the same single file — set up as the
**scorer agent**. In that job it reads the scoreboard file PCS Pro already writes on
every ball and makes it available to your laptop over the club's wifi. It doesn't
talk to PCS Pro, doesn't change any file, and doesn't need internet.

This is one of two ways to split the scoring and streaming machines apart — see
[BRIDGE.md](BRIDGE.md) for the other (the **bridge**), which is aimed at
machines that *aren't* on the same local network (reached over Tailscale, with a
token) rather than two laptops sitting next to each other on the club wifi. If
both laptops are in the same room on the same wifi, the agent below is simpler —
no address to type, no token to copy.

---

## What you need

- Both laptops on the **same wifi** (or both plugged into the same router).
  A phone hotspot works fine if the club has no wifi — connect both to it.
- CricketStream on the scoring laptop: the same download as the streaming laptop
  (`CricketStream.exe` on Windows, `CricketStream-mac.zip` on a Mac) from the
  [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest).
  Put it in a folder of its own — a `CricketStream` folder in Documents is fine. Nothing
  else to install.

---

## Setting it up (once)

### On the scoring laptop

1. Make sure PCS Pro is writing the scoreboard file as normal —
   **Tools → Configuration → Scoreboard**, with the output folder set and
   *Enable Scoreboard Output* ticked. This is the same setup you already use;
   nothing changes here.
2. Double-click **`CricketStream.exe`** (or **`CricketStream.command`** on a Mac).
   The first time, the CricketStream window asks **which computer is this?** — click
   **The scorer's laptop**. It only needs one answer: the scoreboard output folder, which
   it usually finds by itself (otherwise press **Browse…**, or paste it from PCS Pro's
   Scoreboard settings). Press **Save and continue**. Every time after that,
   double-clicking goes straight to sharing the score.
3. The window shows a green light and **Sharing the score**, with:
   - **NV Play's scoreboard** — the file, and how long ago NV Play last wrote it
   - **Streaming laptop** — *Connected* once the streaming laptop is collecting it
   - **This laptop's address** — e.g. `192.168.1.40:8788`

   That's it. **Leave the window open** (minimise it if it's in the way) for the whole
   match and forget about it. Closing it asks first, because it stops the score reaching
   the stream.

   The first time, it also puts the CricketStream scoreboard template into NV
   Play's Templates folder and says so. If NV Play isn't using it yet, choose it
   once: **Tools → Configuration → Scoreboard → Template File →
   `scoreboard.template`**. (CricketStream carries the template inside it — there's
   no separate file to copy.)

   The first time, Windows may ask whether to allow it through the firewall
   (as **CricketStream**).
   Say **yes**, and tick **Private networks**. If nobody's looking and it gets
   dismissed, see *When it doesn't work* below.

   If it can't find the scoreboard folder on its own, the window says so and asks for it
   — **Browse…** to it once and it's remembered. (Dragging the folder onto
   `CricketStream.exe` does the same, and **Change…** picks another one later.)

### On the streaming laptop

1. Start CricketStream as usual and open the control panel.
2. In the **Setup** tab, under **Scoring source**, switch on
   **"Run the stream from a second laptop"**.
3. Press **Find scorer laptop**.
4. It should find it by itself and go green:

   > ● Connected to **SCORER-PC** (192.168.1.40:8788)
   > Reading `nvplay-scoreboard1.xml` — updating live

5. Press **Save**. That's the setup done — it's remembered for next week.

Everything else — graphics, replays, OBS, AI commentary, the match report —
works exactly as before. Only where the score comes from has changed.

---

## On match day

1. Scorer opens PCS Pro, then double-clicks CricketStream on the scoring laptop.
   Leave it open.
2. You double-click CricketStream on your laptop (it opens OBS for you).
3. Check the control panel says **Connected** and is reading the file.

If the scorer's laptop lands on a different IP address than last week — most club
routers will do this eventually — you don't need to do anything. Your laptop
finds it again automatically.

---

## When it doesn't work

**"Nothing answered" when you press Find scorer laptop**

Work down this list; it's nearly always one of the first two.

1. **Is it actually sharing?** Check the CricketStream window on the scoring laptop is
   still open and says **Sharing the score** (or **Ready — waiting for the next ball**).
2. **Are both laptops on the same wifi?** Not "the club wifi" on one and a phone
   hotspot on the other. Check the network name on both.
3. **Firewall.** On Windows: Settings → Privacy & security → Windows Security →
   Firewall & network protection → Allow an app through firewall → find
   **CricketStream** and tick **Private**. This is the usual culprit if step 1 and 2 are
   fine.
4. **Guest wifi.** Some routers isolate guest devices from each other on purpose,
   so the two laptops can't see each other no matter what. Put both on the main
   network, or use a phone hotspot for the pair of them.
5. **Type the address by hand.** The scoring laptop's window shows its address under
   **This laptop's address** (`192.168.1.40:8788`). Put that in the *Scorer laptop
   address* box and save.
   This skips discovery entirely and is worth doing if your club network is
   awkward — it only breaks when the address changes.

**Connected, but "no scoreboard file yet"**

Your laptop is talking to the scoring laptop fine, so networking is sorted. The
scoreboard output isn't being written. Check *Enable Scoreboard Output* is ticked
in PCS Pro, that the scoreboard template is selected, and that at least one ball
has been scored.

**It was working, then the score froze**

The agent window was probably closed, or the scoring laptop went to sleep. The
overlay keeps showing the last score it received for two minutes, then stops so
you're not broadcasting a stale scorebar without realising. Restart the agent and
it picks up where it left off within a few seconds — nothing is lost.

Worth doing on the scoring laptop before the season: set it to never sleep on
mains power.

**More than one agent answered**

Someone left the agent running on another machine. The control panel will say so
and list them. Type the address you want in the box and save.

---

## Other ways to do this

The agent is the simplest route for two laptops on the same wifi — it needs no
network configuration. Some alternatives, if they suit your club better:

- **The bridge** (see [BRIDGE.md](BRIDGE.md)) if the two machines are
  *not* on the same local network — it's designed to be reached over Tailscale,
  gated by a token, rather than found by broadcast.
- **A shared folder.** If the scorer's laptop already shares its output folder
  over the network, leave the agent switch off and put the network path in the
  folder box above it (`\\SCORER-PC\Output` on Windows, or a mounted share on a
  Mac). No agent needed — but it depends on file sharing, permissions and
  sometimes a password, which is why it isn't the default.
- **One laptop.** Nothing has changed: leave the agent switch off, with a local
  folder path, exactly as before.

---

## For the technically curious

The agent is `cricketstream agent` (what the scorer's-laptop role runs). It serves two
things on the local network and nothing else:

| | |
|---|---|
| `GET /ping` | Identifies itself: hostname, which folder it's watching, how old the file is |
| `GET /pcs` | Returns the scoreboard file's contents. Pass `?since=<mtime>` and it replies "unchanged" instead of resending it |
| UDP 8787 | Replies to a `CRICKETSTREAM-DISCOVER` broadcast so the streaming laptop can find it without an IP address |

It only ever reads, never writes, and serves nothing outside the one scoreboard
file. It has no authentication, on the assumption that anyone already on your
club's wifi is welcome to know the score — don't run it on a public network. If
that tradeoff is unwanted, use the bridge instead, which is token-gated.

Ports can be changed if 8788 or 8787 clash with something:

```
CricketStream agent --port 9788 --discovery-port 9787
```

If you change the HTTP port, type the full `host:port` into the control panel,
since discovery reports whatever port the agent is actually using.
