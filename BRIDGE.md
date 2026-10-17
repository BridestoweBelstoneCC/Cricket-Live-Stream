# NV Play Bridge Guide
## CricketStream Overlay

Runs NV Play on a completely different machine from the one doing OBS + the server, and
keeps the live score in sync between them over the network.

---

## When you'd use this

**The scenario that led to this feature:** a club streamed from a MacBook running OBS, the
stream server, *and* a Windows VM with NV Play open inside it, all at once. Mid-match the
Mac overheated, the VM crashed, and the scorer lost their progress — on stream, in front of
people watching live.

Use the bridge if:

- **NV Play only runs on Windows and your streaming machine is a Mac**, so NV Play has to
  live inside a VM — and that VM is competing with OBS's encoder for the same CPU and
  thermal budget.
- **You've had a crash, overheating, or a VM freeze mid-match** and want NV Play off the
  streaming machine entirely rather than just hoping it doesn't happen again.
- **You already have spare hardware** — an old laptop, a mini PC, even a previous-generation
  machine that's too slow for OBS but perfectly fine for NV Play, which is a lightweight
  scoring app.

Don't bother with it if NV Play already runs natively on your streaming machine (no VM) and
it's never struggled — the plain `pcs_output_folder` setup is simpler and has one less thing
to configure. The bridge exists specifically for the split-hardware case.

---

## How it works

The bridge is CricketStream itself — the same single file as on the streaming machine — set
up for the job "scorer's machine, elsewhere". On the machine that has NV Play it watches NV
Play's output folder and serves the latest file over HTTP, gated by a token.

On the streaming machine, the server polls that URL every couple of seconds and mirrors the
file into a local folder. From that point on it's treated as an ordinary local file — the
same parser, the same overlay, the same ball database, the same everything. Nothing
downstream can tell the difference between a local NV Play file and a bridged one.

```
NV Play machine                          Streaming machine
┌─────────────────────┐                  ┌─────────────────────────┐
│ NV Play writes a     │   HTTP, token-   │ the server mirrors it   │
│ JSON file every ball │ ─── gated ────▶  │ locally every ~2s, then │
│ CricketStream        │   (Tailscale)    │ reads it exactly like a │
│ (bridge) serves it   │                  │ local PCS file          │
└─────────────────────┘                  └─────────────────────────┘
```

---

## Setup

### 1. On the machine with NV Play

You only need the one CricketStream file on this machine — nothing else to install.

1. Download `CricketStream.exe` (or `CricketStream-mac.zip`) from the
   [latest release](https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest)
   and put it in a folder of its own on the NV Play machine.
2. Double-click it. The setup page opens in the browser: under **What is this computer
   for?** pick **Scorer's machine, elsewhere**.
3. Give it the NV Play output folder (find it in NV Play under **Tools → Configuration →
   Scoreboard**; it's often filled in for you) and keep the suggested port (5050) unless
   something else uses it. Press **Save and start**.
4. The page shows a **token** — copy it; the streaming machine needs it. It's saved in
   `bridge_config.ini` next to CricketStream, so you won't be asked again: on future match
   days just double-click CricketStream. The black window prints the token every time it
   starts, too. Keep this window open while streaming.

(Running it by hand, or on a machine already set up for something else:
`CricketStream bridge` asks the same two questions in the window instead.)

### 2. Connect the two machines

Both machines need to be able to reach each other. **Tailscale is the recommended way** —
it's free, encrypts the connection, and doesn't require any router configuration:

1. Install [Tailscale](https://tailscale.com/download) on both machines and sign in with the
   same account on each.
2. On the NV Play machine, find its Tailscale IP (Tailscale's system tray/menu bar icon, or
   run `tailscale ip -4`). It looks like `100.x.x.x`.

If both machines are already on the same trusted Wi-Fi (e.g. a home network, not a public
one) you can use the LAN IP instead and skip Tailscale — but Tailscale is worth having
anyway if you're also using remote access for the control panel or scoring page.

### 3. On the streaming machine

1. Open the control panel → the **Match** card.
2. Find **"NV Play on separate hardware"**, just below the PCS output folder field.
3. Leave the PCS output folder field blank, and fill in:
   - **URL**: `http://<the Tailscale IP from step 2>:5050`
   - **Token**: exactly what the bridge printed
4. Save.

### 4. Verify it's working

- `http://localhost:5000/health` → the `pcs.bridge` block should show `"connected": true`.
- The control panel's health strip gets a live "Feed" indicator — it'll show the age of the
  last received ball once NV Play writes one.
- **Rehearse this before match day, not during it** — the first run on Windows will trigger
  a firewall prompt ("allow CricketStream to communicate on private networks") that needs
  clicking through once.

---

## Security

- **Every request needs the token.** `/pcs/latest` (the endpoint that returns match data)
  checks it with a constant-time comparison; a missing or wrong token gets a 401, nothing
  else. `/pcs/ping` (just "is this machine up") doesn't need one, since it returns no match
  data.
- **The bridge refuses to start with a blank token.** If `bridge_config.ini` ever ends up
  with an empty token — hand-edited, corrupted, whatever — the bridge won't listen at all,
  rather than silently accepting any request. Delete the file and rerun to generate a fresh
  one.
- **The token is a random 128-bit value** from the operating system's secure random source — not
  guessable, and there's no meaningful way to brute-force it over a network.
- **Traffic is plain HTTP, not HTTPS** — fine over Tailscale (which is already an encrypted
  tunnel between your two machines) or a trusted home Wi-Fi, but this is **not designed to
  be exposed to the public internet**. Don't port-forward this port on your router. If
  you're not using Tailscale, keep both machines on a network you trust.
- **What's actually exposed if the token leaked:** the live cricket score, a few seconds
  before your stream shows it. Not sensitive data — the design errs on the side of "simple
  and hard to misconfigure" rather than defending a high-value secret.
- **`bridge_config.ini` and the mirrored files never get committed** — both are in
  `.gitignore` (`bridge_config.ini`, `.pcs_bridge_cache/`), same as `config.ini` and
  `match_state.json`.

---

## Troubleshooting

- **"folder not found" warning on startup** — the path you gave doesn't exist on that
  machine. Check it against NV Play's **Tools → Configuration → Scoreboard** setting exactly.
- **`/health` shows `pcs.bridge.connected: false`** — check `last_error` in the same
  response. Usually either the NV Play machine is unreachable (Tailscale not connected, or
  the bridge isn't running) or the token doesn't match what's in the control panel.
- **Windows Firewall blocked it** — you likely clicked "Cancel" instead of "Allow" on the
  first-run prompt. Add an exception manually
  in Windows Defender Firewall for the port you chose.
- **Works, but the score looks a few seconds behind** — normal; the bridge polls every ~2
  seconds, on top of whatever poll interval the overlay already uses. Not something to chase
  down as a bug.

---

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for how this fits into the rest of the codebase if
you're contributing rather than just running it.
