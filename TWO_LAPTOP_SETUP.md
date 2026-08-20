# Running the stream on a second laptop

**What this is for:** keeping the stream off the scorer's machine. The scorer's
laptop does one job — scoring. Your laptop does the stream, OBS, the overlay and
all the fiddling. If a graphic misbehaves at 3pm you fix it on your own screen,
without leaning across the scorebox.

The scoring laptop runs a small program called the **scorer agent**. It reads the
scoreboard file PCS Pro already writes on every ball and makes it available to
your laptop over the club's wifi. It doesn't talk to PCS Pro, doesn't change any
file, and doesn't need internet.

This is one of two ways to split the scoring and streaming machines apart — see
[BRIDGE.md](BRIDGE.md) for the other (`nvplay_bridge.py`), which is aimed at
machines that *aren't* on the same local network (reached over Tailscale, with a
token) rather than two laptops sitting next to each other on the club wifi. If
both laptops are in the same room on the same wifi, the agent below is simpler —
no address to type, no token to copy.

---

## What you need

- Both laptops on the **same wifi** (or both plugged into the same router).
  A phone hotspot works fine if the club has no wifi — connect both to it.
- One file copied onto the scoring laptop, depending on what it already has:
  - **Windows, no Python installed:** download `CricketStreamScorerAgent.exe`
    from the [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest)
    — no separate Python install needed, just double-click it.
  - **Windows or Mac, already running PCS Pro (so Python may already be
    there) or happy to install it:** get **Python 3** from
    [python.org](https://www.python.org/downloads/) (tick **"Add Python to
    PATH"** on Windows) if it's not already there, then copy `scorer_agent.py`
    + `Windows/start_scorer_agent.bat` (Windows) or `Mac/start_scorer_agent.sh`
    (Mac) onto the scoring laptop, in the same folder — anywhere convenient,
    the Desktop is fine.

---

## Setting it up (once)

### On the scoring laptop

1. Make sure PCS Pro is writing the scoreboard file as normal —
   **Tools → Configuration → Scoreboard**, with the output folder set and
   *Enable Scoreboard Output* ticked. This is the same setup you already use;
   nothing changes here.
2. Double-click **`CricketStreamScorerAgent.exe`** (Windows, no Python needed),
   or **`start_scorer_agent.bat`** (Windows, from source) / **`start_scorer_agent.sh`**
   (Mac).
3. A black window opens and shows something like:

   ```
   Watching folder : C:\Users\Scorer\Documents\Cricket Matches\_Scoreboards\Output
   This machine    : 192.168.1.40
   Serving on      : http://192.168.1.40:8788/pcs
   ```

   That's it. **Leave the window open** for the whole match and forget about it.

   The first time, Windows may ask whether to allow it through the firewall
   (as **Python**, or as **CricketStreamScorerAgent** if you're using the exe).
   Say **yes**, and tick **Private networks**. If nobody's looking and it gets
   dismissed, see *When it doesn't work* below.

   If it can't find the scoreboard folder on its own, it will tell you. Copy the
   path out of PCS Pro's Scoreboard settings and start it like this — you only
   need to do it once, it remembers (drag-and-drop the folder onto the exe also
   works, if you're using that):

   ```
   python scorer_agent.py "C:\Users\Scorer\Documents\Cricket Matches\_Scoreboards\Output"
   ```

### On the streaming laptop

1. Start `server.py` as usual and open the control panel.
2. Under **Where the score comes from**, switch on
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

1. Scorer opens PCS Pro, then double-clicks the agent launcher. Leave it open.
2. You start `server.py` and OBS on your laptop.
3. Check the control panel says **Connected** and is reading the file.

If the scorer's laptop lands on a different IP address than last week — most club
routers will do this eventually — you don't need to do anything. Your laptop
finds it again automatically.

---

## When it doesn't work

**"Nothing answered" when you press Find scorer laptop**

Work down this list; it's nearly always one of the first two.

1. **Is the agent actually running?** Check the black window is still open on the
   scoring laptop and hasn't been closed or minimised into oblivion.
2. **Are both laptops on the same wifi?** Not "the club wifi" on one and a phone
   hotspot on the other. Check the network name on both.
3. **Firewall.** On Windows: Settings → Privacy & security → Windows Security →
   Firewall & network protection → Allow an app through firewall → find
   **Python** and tick **Private**. This is the usual culprit if step 1 and 2 are
   fine.
4. **Guest wifi.** Some routers isolate guest devices from each other on purpose,
   so the two laptops can't see each other no matter what. Put both on the main
   network, or use a phone hotspot for the pair of them.
5. **Type the address by hand.** The agent window prints its address
   (`192.168.1.40:8788`). Put that in the *Scorer laptop address* box and save.
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

- **`nvplay_bridge.py`** (see [BRIDGE.md](BRIDGE.md)) if the two machines are
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

The agent is a single stdlib-only Python file. It serves two things on the local
network and nothing else:

| | |
|---|---|
| `GET /ping` | Identifies itself: hostname, which folder it's watching, how old the file is |
| `GET /pcs` | Returns the scoreboard file's contents. Pass `?since=<mtime>` and it replies "unchanged" instead of resending it |
| UDP 8787 | Replies to a `CRICKETSTREAM-DISCOVER` broadcast so the streaming laptop can find it without an IP address |

It only ever reads, never writes, and serves nothing outside the one scoreboard
file. It has no authentication, on the assumption that anyone already on your
club's wifi is welcome to know the score — don't run it on a public network. If
that tradeoff is unwanted, use `nvplay_bridge.py` instead, which is token-gated.

Ports can be changed if 8788 or 8787 clash with something:

```
python scorer_agent.py --port 9788 --discovery-port 9787
```

If you change the HTTP port, type the full `host:port` into the control panel,
since discovery reports whatever port the agent is actually using.
