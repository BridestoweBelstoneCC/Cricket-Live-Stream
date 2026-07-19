# Architecture

How CricketStream Overlay fits together, for contributors and the technically curious.
The diagrams render automatically on GitHub (they're [Mermaid](https://mermaid.js.org/) —
edit them as text, right here in this file).

The design in one sentence: **everything speaks the NV Play "scoreboard frame" dialect** —
the scorer's file, the manual scoring page, and the match simulator all produce the same
frame shape, so the overlay, graphics, ball database, replays and highlights work
identically no matter where the data comes from.

---

## The big picture

```mermaid
flowchart LR
    subgraph sources["Data sources (pick one)"]
        NV["NV Play / PCS Pro<br/>scorer's laptop writes a<br/>JSON frame every ball<br/>(optionally separate hardware,<br/>mirrored by nvplay_bridge.py)"]
        MAN["Manual scoring page<br/>scoring.html on a phone<br/>(no scoring software)"]
        SIM["Match simulator<br/>simulate_match.py<br/>(rehearsals)"]
        WID["PlayCricket widget<br/>(score-only fallback)"]
    end

    subgraph srv["server.py — one process, port 5000"]
        PARSE["parse_pcs_json()<br/>one parser for every source"]
        ENG["scoring_engine.py<br/>the scorer's book<br/>(shared by manual + simulator)"]
        DB[("match_data.db<br/>ball-by-ball SQLite<br/>+ replay clip tags")]
        AI["Claude AI (optional)<br/>commentary · reports · socials"]
        WATCH["watchdog + stream sentinel<br/>self-healing · congestion ladder"]
    end

    subgraph consumers["Browsers"]
        OV["overlay.html<br/>OBS browser source<br/>polls /live every ~2.5s"]
        CP["control.html<br/>operator panel<br/>polls /live/view"]
    end

    OBS["OBS Studio<br/>camera + overlay + encoder"]
    YT["YouTube Live"]

    NV -- "file read" --> PARSE
    MAN --> ENG
    SIM -. "writes the same<br/>file format" .-> PARSE
    ENG -- "frames" --> PARSE
    WID -. "fallback" .-> PARSE
    PARSE --> OV
    PARSE --> CP
    PARSE --> DB
    PARSE --> AI
    OV --> OBS
    WATCH <--> OBS
    OBS --> YT
```

Key decisions baked into that shape:

- **The server must run next to the scorer's output file**, so it can never be a cloud
  service — remote *operation* (Tailscale / Cloudflare Tunnel to the panel and scoring
  page) is what's exposed instead.
- **NV Play can run on hardware separate from the server** via `nvplay_bridge.py`, which
  serves the scorer's file over HTTP; the server mirrors it into a local cache folder every
  ~2s, becoming an ordinary local file before `parse_pcs_json()` — or anything else — ever
  sees it. Built so the streaming machine doesn't have to carry a scorer's VM's CPU/heat
  load alongside OBS.
- **`/live` is the overlay's poll and drives all side effects** (event detection, ball
  logging, commentary triggers, consuming the wicket-event buffer). Every other consumer
  uses the read-only `/live/view` — exactly one client owns the pipeline.
- **Manual scoring and the simulator share `scoring_engine.py`**, a deterministic
  "scorer's book". Determinism is load-bearing: the scoring page's undo replays the event
  log and must reproduce the book exactly (tests assert frame-for-frame equality).

---

## One ball's journey

```mermaid
sequenceDiagram
    participant S as Scorer (NV Play)
    participant F as scoreboard file
    participant SV as server.py
    participant DB as match_data.db
    participant OV as overlay.html (in OBS)
    participant OBS as OBS Studio

    S->>F: writes frame (ball 4 of over 18: a SIX)
    OV->>SV: GET /live (every ~2.5s)
    SV->>F: read + parse_pcs_json()
    SV->>DB: rewrite current over<br/>(delete + reinsert — captures scorer edits)
    SV-->>OV: state + buffered events
    OV->>OV: ticker shows the 6 · SIX! flash fires
    OV->>SV: POST /replay (reason: "Six")
    SV->>OBS: WebSocket: save replay buffer,<br/>switch to Replay scene, back to Main
    SV->>DB: tag the clip<br/>("SIX · SMITH 34* · 88-2 (18.4 ov)")
    Note over SV,DB: post-match, the highlights compiler turns<br/>tagged clips into a captioned, chaptered reel
```

Two subtleties worth knowing (they've caused real bugs):

- **The ticker clears on the over-completing write.** NV Play never shows the finished
  over's ticker for an extra poll, so the final ball of every over is invisible in the
  ticker. The overlay recovers it from the score delta for graphics, and the ball logger
  recovers it the same way for the database.
- **Innings 2 is detected by `runs_required > 0`, latched** — because it drops back to 0
  the instant the winning runs are hit, which would otherwise "end" the innings early.

---

## Module map

| Piece | Role |
|---|---|
| `server.py` | The whole backend: HTTP server, parsing, ball DB, replays/highlights, AI, auth, watchdog, stream sentinel, manual-scoring session |
| `overlay.html` | The broadcast layer (1920×1080 OBS browser source) — scorebar, cards, milestones, worm, replays. Pure client-side JS |
| `control.html` | Operator panel served at `/control` (kit colours, toggles, roster, health, highlights, stream quality) |
| `scoring.html` | Manual ball-by-ball scoring page at `/scoring` — event-sourced, undo-exact, restart-safe |
| `scoring_engine.py` | Deterministic innings engine shared by manual scoring and the simulator |
| `simulate_match.py` | Rehearsal harness: complete simulated matches written as real feed frames (`--chaos` for failure drills) |
| `scoreboard.template` | What NV Play fills in — the contract every source imitates |
| `nvplay_bridge.py` | Standalone stdlib script: serves NV Play's file over HTTP when it's on separate hardware from the server |
| `obs_setup.py` / `quickstart.py` / `setup_wizard.py` | OBS auto-config · match-day launcher · first-run wizard |
| `tests/` | 165 stdlib-unittest tests, including a full-match soak that reconciles the ball DB against the engine's book |

### Security model, briefly

Auth is optional on localhost and mandatory the moment the server binds beyond it: a club
password exchanged for HMAC-signed, expiring session tokens (Bearer-only — no cookies, so
nothing for a cross-site page to ride on), login lockout, an origin check on every POST,
and secret redaction on `/state`. The overlay itself never logs in — endpoints it must
call trust genuine loopback connections only (a proxied `X-Forwarded-For` request doesn't
count).

---

*Diagrams live in this file as Mermaid — if you change the architecture, change the
picture in the same commit.*
