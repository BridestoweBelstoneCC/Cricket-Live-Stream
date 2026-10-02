# CLAUDE.md — project guide for Claude Code

This file is read automatically at the start of every Claude Code session. It captures the
architecture, the commands, and the hard-won gotchas so you don't have to re-explain them.

## What this project is

A free, open-source live-stream graphics system for grassroots cricket. A Python server reads
the scorer's match data and drives an HTML overlay inside OBS, which streams to YouTube. There
is also a browser **control panel** (served by the server) for the match-day operator.

**Data flow (three sentences):** The scorer's software (NV Play / PCS Pro) writes a JSON file
on every ball. `server.py` reads that file, parses it, and serves the live state on `/live`.
`overlay.html` (an OBS browser source) polls `/live` every ~2.5s and renders the graphics.

**NV Play on separate hardware? `nvplay_bridge.py`** lets the scorer's machine be physically
different from the streaming machine — originally built after a streaming Mac overheated and
crashed the VM running NV Play, losing the scorer's progress mid-match. It serves the PCS file
over HTTP from wherever NV Play actually is; `server.py` mirrors it into a local
`.pcs_bridge_cache/` folder every ~2s, at which point it's an ordinary local file to
everything downstream (see the `effective_pcs_folder()` gotcha below).

**No scoring software? `/scoring`** is a phone/tablet page of big buttons that drives the same
pipeline: button presses feed the shared `scoring_engine.InningsEngine`, whose frames are
rendered through the same PCS parser — so the overlay, ball DB, highlights and graphics work
identically. While a manual session is live it OUTRANKS the PCS file in `/live` — and every
OTHER feed consumer must apply the same precedence (`manual_live_state()` for state,
`manual_session_active()` for freshness): over-commentary, `/health`, and the watchdog each
shipped without it and misreported a manual match day until fixed.

## Key files

- **`server.py`** (~5000 lines) — the whole backend. HTTP server on port 5000
  (`ThreadingHTTPServer`). Also builds the social-media images.
- **`control.html`** (~3000 lines) — the operator's control panel, served by `/control`
  with the kit-colour presets injected in place of the `/*__KIT_PRESETS__*/[]` placeholder
  (see `control_html()` in server.py). Read from disk per request, so panel edits show on
  refresh without a server restart. **Layout (2026-10 refresh):** four tabs by moment —
  Match day / Graphics & sponsor / After the match / Setup — under a sticky top bar (live
  status + Save, which every setting except the checklist buttons needs) and the
  checklist. Every colour is a token on `:root` (`--bg`, `--surface`, `--accent`...); add
  new colours as tokens, not hex. `--accent`/`--accent-strong`/`--on-accent` are set at
  runtime from the home kit colour by `applyClubAccent()` (HSL-lightened to 4.5:1 / 3:1
  against the background; black-or-white text on the fill). Tab visibility must stay
  CLASS-only (`.tab-panel.active`): an `#tab-x` rule outranks it and pins a tab open. The
  JS finds controls by id, so moving markup between tabs is safe as long as ids stay
  unique and present — `tests/test_panel_layout.py` checks that every saved field and
  every handler survives. The scorebar preview iframe is the overlay's full 1080px canvas
  shifted up to the bar (measured only while visible — it reads 0 inside a hidden tab). It used to live INSIDE server.py as a Python string —
  see the (historical) backslash gotcha below.
- **`overlay.html`** (~2900 lines) — the OBS browser source (1920×1080). Pure HTML/CSS/JS.
  Four scorebar styles (`classic`/`modern`/`impact`/`minimal`) as `body.style-*` CSS blocks
  over one shared DOM; `classic` is the base stylesheet, so an unknown `scorebar_style`
  renders as classic rather than breaking. The list lives in three places that must agree —
  overlay's `SCOREBAR_STYLES`, its CSS blocks, and the control panel's picker — which
  `tests/test_scorebar_styles.py` enforces. `applyColours()` publishes
  `--bat`/`--bowl`/`--bat-lift`/`--bowl-lift` on `#scorebar` so styles can carry team
  identity beyond the two end blocks; the `-lift` pair goes through the existing
  luminance-aware `wormColour()`, because a club colour like a near-black navy is invisible
  as a thin rule on a dark bar. Every style keeps the bar at **72px** — it's load-bearing
  (graphic panels sit at `bottom:72px`; fow/partnership/milestone panels are themselves
  72px to line up), so hierarchy comes from type scale and colour, never a taller bar.
- **`scripts/render_scorebar.py`** — renders every scorebar style to `examples/` in headless
  Chrome/Edge with fixed mock data; no server, no npm, no node. The only check that catches
  visual regressions (see the build/test section above).
- **`result_card.py`** — the post-match Instagram result card (Pillow only, pure rendering:
  facts + look in, PNG out; `server.build_instagram_image()` gathers both). Fonts are bundled
  in `fonts/` (Barlow Condensed, OFL) — never fall back to system fonts by design. Facts come
  from `build_match_facts_from_pc()` (PlayCricket's own result — never re-derive a DLS/
  conceded/pairs result from totals) or, for the streamed match, `generate_social_graphic_facts()`
  (deterministic from the ball log; the AI only writes the caption). Visual changes: render it
  and look — `tests/test_result_card.py` checks contrast and layout robustness, not looks.
- **`social_clips.py`** — vertical 9:16 clips for Shorts/Reels/TikTok from tagged replays
  (`server.make_social_clips()` picks the clips, gets Claude Haiku's line + post caption
  via `ai_clip_texts()`, falls back to the replay tag offline). Text reaches ffmpeg only via
  `textfile=` (no caption can break the filter graph) and rendering runs at below-normal
  priority. Post-match on purpose — don't trigger it from a replay mid-stream; x264 on the
  streaming laptop competes with OBS. The caption prompt must keep saying the tag doesn't
  know which side a player is on: without it, Haiku wrote "takes us to 60-1" about an
  opposition batter.
- **AI camera spotter** (`spot_cameras()` / `spotter_due()` in server.py, `/camera/spotter`):
  OBS `GetSourceScreenshot` of each camera source -> Claude Haiku with `SPOTTER_PROMPT` ->
  `{ok, problem}`. Live-only and off by default (`camera_spotter`) — it spends the club's AI
  credit; never make it run off-air on a timer. Frozen = two byte-identical JPEGs in a row
  (real cameras have sensor noise), no AI needed. An API failure is reported as "couldn't
  check", never as a camera problem.
- **`win_predictor.py`** — live win probability (pure stdlib): DLS-shaped resources curve
  (matches DLS reference points to within ~2%) x this season's first-innings scoring (from
  the season-stats download's `innings_history`, per format when >= `WIN_MODEL_FORMAT_MIN`
  games, else pooled), normal-distribution totals. `server.win_prediction()` adds
  `winPredictor` to every /live state; the overlay's `fillWinPredictor()` reads the copy
  `processPCSData` stores — **there is no global `state` in overlay.html**: reading one
  from a function called by showOverSummary threw and silently skipped the rest of the
  over sequence (AI commentary, league table, sponsor strap). The history also comes from
  the cache file, so a restart doesn't silence it. `backtest()` is the honest number: 66%
  favourite-correct at the start of a chase on 2026's 64 matches.
- **`voice.py`** — spoken commentary (stdlib): Windows System.Speech via a temp .ps1,
  macOS `say`, espeak on Linux. Text always via a file, never argv. `speak_over_commentary()`
  attaches the WAV to `_over_commentary` only if that's still the same over (a slow synth
  must not land on the next one); the overlay's `speakCommentary()` plays it once per over.
  It only reaches viewers because obs_setup sets `reroute_audio` on the Overlay source.
- **`match_page.py`** — the shareable match page: pure `render(d)`, every string
  HTML-escaped, images inlined as base64 so the one file works anywhere. `server.py`'s
  `build_match_page()` gathers the parts, each one optional (a failed AI report or card
  just leaves that section out), and the stream monitor schedules it 60s after a
  live→off transition. Scorecards come from `match_facts_from_db` (rowid order = batting
  order; its top-scorer sort works on a COPY so it can't reorder them). The chart is
  clipped to each innings' own overs, because a re-scored innings can leave stray balls
  logged past the end. `/match/page/latest` is open, like the result card.
- **`scoring_engine.py`** — the deterministic scorer's-book core (`InningsEngine`): striker
  rotation, extras, dismissals, bowler figures, NV Play frame rendering. Two frontends drive
  it: `simulate_match.py` (random sampling) and the manual scoring page. Determinism is
  load-bearing — `/scoring`'s undo replays the event log and must reproduce the book exactly.
  Replays after any edit must be LENIENT (`_rebuild(lenient=True)`): `edit_ball()` leaves
  edit-invalidated auxiliary events in the log by design, so a strict replay in undo/load
  wedges mid-rebuild (truncated live innings; saved match dropped as "unreadable" on restart).
- **`scoring.html`** — the manual ball-by-ball scoring page (`/scoring`), mobile-first, for
  clubs/days without NV Play. Event-sourced via `ManualScoringSession` in server.py; the
  session persists to `manual_scoring.json` (git-ignored) after every ball, so a restart or
  a dropped phone resumes mid-over. Same login/session token as the control panel.
- **`quickstart.py`** — auto-setup / launcher; runs a pre-flight self-test and a best-effort
  GitHub release check (`check_for_updates()` — compares `git describe --tags` against the
  latest release; purely informational, silently skipped if there's no git/internet). Starts
  `server.py` as a detached subprocess (so Ctrl+C stops only the launcher, keeping the server
  alive long enough to generate the match report) and, via `run_server_with_restarts()`,
  relaunches it up to `SERVER_MAX_RESTARTS` times with a short backoff if it exits
  unexpectedly — extracted into its own function specifically so this could be unit-tested
  with a fake Popen-like object rather than real subprocesses.
- **`cricketstream.py`** — **the one launcher for the streaming laptop**, shipped frozen as
  `CricketStream.exe` / `CricketStream.command`. Decides what still needs doing and does it:
  in the project folder? → Python installed? → packages installed? → `config.ini` exists?
  → hand over to `quickstart.py`. Every satisfied step is skipped silently, so the second
  and every later run is just "double-click, match starts". It implements none of those
  steps itself — each one calls the `setup_wizard.py` function that already did that job,
  so there's exactly one version of each and `setup.bat`/`setup.sh` keep working unchanged.
  Replaced the old two-exe dance (`CricketStreamSetup.exe` then `CricketStreamQuickstart.exe`,
  the latter having to be placed next to `quickstart.py` by hand, with no readable error
  when it wasn't). `quickstart_launcher.py` was its predecessor and is deleted — it's in
  git history if ever needed. **Deliberately does NOT freeze `quickstart.py`/`server.py`**:
  `server.py` reads `overlay.html`/`control.html` from disk per request (that's what makes
  panel edits appear on refresh) and resolves `config.ini`/`match_state.json` relative to
  its own folder, so both stay ordinary `.py` files run by a real interpreter and the
  match-day code path is byte-for-byte what runs from source. An earlier freeze attempt
  needed surgery on both and was reverted.
- **`obs_prep.py`** — the half of OBS setup that needs OBS CLOSED, run by `quickstart.py`
  before `load_config()` (it can write `obs_password` into `config.ini`). Edits OBS's own
  files directly, since there's no API for them: `plugin_config/obs-websocket/config.json`
  to switch the WebSocket server on with a random password (`first_load` must be false or
  obs-websocket generates its own password over ours), and the active profile's
  `basic.ini` (found via `user.ini`, OBS 31+, else `global.ini`) for `RecRB` in both
  output-mode sections plus the replay folder. Then clears `.sentinel/run_*` and launches
  OBS with `--startreplaybuffer`. **The sentinel clear is load-bearing**: OBS 30+ leaves
  that marker when it crashes or is force-closed, and the next start stops on a modal
  "Run in Safe Mode?" prompt — nobody's there to click it on match day, and Safe Mode
  skips the WebSocket plugin entirely. `--disable-shutdown-check` is also passed but does
  NOTHING on OBS 32 (the option isn't in its binaries; it prompted with the flag set) —
  don't mistake it for the fix. Verify Safe Mode behaviour from OBS's own log
  (`Crash or unclean shutdown detected`), never from "the WebSocket answered": a person
  clicking the prompt makes the WebSocket answer too, which is exactly how the flag was
  first wrongly "verified". **Never write either file while OBS is running** — OBS saves its in-memory settings over them on exit; an already-working
  WebSocket config is reused, never rewritten. An OBS that's running but not answering on
  its port is treated as stuck (Safe Mode prompt / Safe Mode), not as ready. `basic.ini` is edited line-by-line, not with
  configparser, which would lowercase OBS's case-sensitive keys and drop its BOM.
  Stdlib-only. `[OBS] manage_obs = no` turns it off. `obs_setup.py` (over the WebSocket,
  OBS running) still does scenes/sources/stream key and remains the fallback for a fresh
  OBS with no profile yet. **Mid-match crashes** are `server.py`'s `_obs_guard_tick()`
  (own 5s thread, not the 90s watchdog — every second OBS is down the stream is down):
  reuses obs_prep to reopen OBS, sets the Main scene, and sends `StartStream` only if the
  stream monitor last saw it live while OBS was REACHABLE (the monitor flips to
  not-streaming the moment OBS dies, so a naive read would never restore the stream).
  Crash vs. deliberate close is the sentinel again: a clean exit deletes it, so closing
  OBS after the match leaves it closed. Never starts an OBS it hasn't seen running this
  session; capped at 3 reopens per 30 min. Reported in `/health` → `obs_recovery`.
  Whether it was live is asked of OBS itself on every 5s check (`_obs_is_streaming`), not
  copied from the 15s stream monitor — the stale flag could put a club back on air after
  they'd stopped. It stands aside while the checklist's Start OBS job runs (that job
  closes and reopens OBS on purpose on a first run), only runs the `tasklist` process
  check once armed, and reuses OBS's PIDs for the freeze check.
  **Freezes** (Windows only) go through the same path: `obs_window_hung()` pings OBS's
  window with `SendMessageTimeout(WM_NULL)`; frozen 60s AND the stream not visibly sending
  (`outputBytes` not moving across two reads) → `kill_obs()`, whose force-close leaves the
  sentinel, so the next tick reopens it as a crash. Don't swap the ping for
  `IsHungAppWindow` alone — it only fires when messages are queued, so a frozen OBS nobody
  is touching read as "responding" for two minutes in testing. And never drop the
  stream-flowing check: OBS encodes off the UI thread, so a frozen window can sit over a
  perfectly good stream, and killing it would cause the outage this exists to prevent.
  **Video settings** are written to `basic.ini` `[Video]` here too, OBS closed (OBS
  refuses video changes while any output runs, and the replay buffer always does):
  canvas ALWAYS 1920×1080 because `overlay.html` is a fixed 1920×1080 source at 0,0;
  output/fps from `[Stream] output_resolution`/`fps`, else the cached upload test
  (`network_test_mbps` in match_state.json), else 720p30. FPS goes in as `FPSType=1` /
  `FPSInt` — the default `FPSType=0` matches `FPSCommon` against OBS's menu labels
  ("25 PAL") and silently ran at 30 when given "25". `recommend_stream_settings()` is the
  one recommendation table: the server's `/obs/stream_check` shows it, quickstart applies
  it (including the bitrate, when `bitrate_kbps` is blank; `manual` opts out).
- **`setup_wizard.py`** — the setup interview plus the shared installer plumbing
  (`find_python`/`install_python`, `find_project_root`, `install_packages`, `configure`,
  `write_config`, `die`/`pause`). Still runnable standalone via `setup.bat`/`setup.sh`, and
  imported by `cricketstream.py` rather than duplicated. Built into the exes by
  `.github/workflows/build-executables.yml` (see gotchas below).
- **`scoreboard.template`** — the template the scorer's software fills in. Deployed to the
  *scorer's* machine, not the streaming machine.
- **`nvplay_bridge.py`** — standalone, stdlib-only script for when NV Play runs on hardware
  separate from the streaming machine. Serves the scorer's PCS output file over HTTP
  (`/pcs/latest`, token-gated; `/pcs/ping` open) from a `bridge_config.ini` it creates on
  first run (folder path, port, random token). Deliberately not import-coupled to
  `server.py` — it has to run standalone on a machine that may have nothing else from this
  repo on it, so the small file-finder logic is duplicated by hand rather than shared.
  Configured from the control panel's `pcs_bridge_url` / `pcs_bridge_token` fields (or
  `config.ini`'s `[Scoring]` section) instead of `pcs_output_folder`; a Tailscale IP is the
  recommended way to reach it. `/health`'s `pcs.bridge` block reports connectivity
  separately from file freshness. Operator-facing setup/security/troubleshooting: `BRIDGE.md`.
- **`scorer_agent.py`** — the other separate-hardware option, for two laptops already on
  the SAME club wifi (`nvplay_bridge.py` above is for machines that aren't, reached over
  Tailscale with a token). Standalone, stdlib-only, no import coupling to `server.py` for
  the same reason as the bridge. Serves `/ping` and `/pcs` over HTTP, and answers a UDP
  `CRICKETSTREAM-DISCOVER` broadcast on port 8787 so the streaming laptop's control panel
  can find it with a button press instead of typing an IP — deliberately no token, since
  discovery only works within the same broadcast domain anyway (don't run it on a public
  network). Controlled by state's `pcs_source` ("local" | "agent") and `agent_host`;
  `read_score_source()` in `server.py` is the dispatch point every /live-adjacent call
  site must use — the same one-door pattern as `effective_pcs_folder()`. Operator-facing
  setup/troubleshooting: `TWO_LAPTOP_SETUP.md`. Also installs `scoreboard.template` into NV Play's Templates
  folder on start (`ensure_template()`; bundled into the exe with `--add-data`, guarded in
  CI by `--check-template`), and owns the one list of places NV Play writes to
  (`COMMON_FOLDER_PATTERNS`, OneDrive variants included) — `setup_wizard.py` imports both
  rather than copying them. **The UDP discovery broadcast and the HTTP
  port are independent as far as Windows Firewall is concerned** — a real two-machine test
  found the HTTP port (8788) reachable while the discovery port (8787) silently got nothing,
  because the first-run firewall prompt didn't cover both. The control panel's manual
  `host:port` entry field is the documented fallback for exactly this, not just a
  same-network-but-no-broadcast edge case.
- **`stream_telemetry.py`** / **`camera_encoder.py`** / **`refresh_cam.py`** — standalone
  match-day diagnostic tools, written while commissioning a Reolink RTSP camera over a
  season. None are imported by `server.py`. `stream_telemetry.py` passively samples OBS +
  the server + the scorer's feed to a CSV every few seconds (deliberately no active
  bandwidth test — that would compete with the live stream and cause the stalls it exists to
  measure; reads `/live/view`, never `/live`) plus an opt-in headroom probe (a tiny upload
  every 5 min, only when the stream is already congestion-free, to measure spare capacity
  passive monitoring can't see) — `quickstart.py` now auto-starts it, best-effort.
  `camera_encoder.py` reads a Reolink camera's own encoder settings over its HTTP API, since
  OBS re-encoding a low-bitrate camera source can't recover detail that was never captured.
  `refresh_cam.py` reloads an OBS media source on a timer to stop a long-running RTSP feed
  drifting out of sync with the overlay. `stream_quality_test.py` (added 2026-08-21)
  automates the manual quality-ladder test: requires OBS already streaming (persistent
  stream key — deliberately never starts/stops the stream itself, that's a real, visible
  action left to the operator), then drives `/stream/quality` down and restore, polling
  `GetStreamStatus` through each ~5-10s reconfigure gap to confirm the stream actually
  survived rather than just trusting the API call succeeded.
- **`simulate_match.py`** — match simulator for rehearsing the whole broadcast without a
  scorer: writes NV Play-style frames (faithful to the gotchas: blank pre-match names,
  runs_required-driven innings 2) to a fake PCS folder. Scenarios: full / chase / century /
  collapse; `--configure` points the running server at it; `--chaos` injects mid-write/stall
  failures. Deterministic per `--seed`; the engine is imported by `tests/test_simulator.py`
  as a parser-consistency harness. Always rehearse graphics changes with it before match day.
  **Ticker fidelity (fixed 2026-10-02):** it used to clear the ticker on the
  over-completing write — the opposite of real NV Play (see the ticker gotcha below) —
  which is how the ball logger shipped dropping every over's final ball without a
  rehearsal or `tests/test_soak.py` noticing. It now keeps showing the pre-final-ball
  ticker until the next ball, like the real feed (`MatchSimulator._frame`; the shared
  engine still clears, which is right for `/scoring`). `--clearing-ticker` keeps the old
  path rehearsable. The soak test fails on the old logger with this; keep it that way.
- **`ARCHITECTURE.md`** — contributor-facing design doc with Mermaid diagrams (data-flow,
  one ball's journey, module map). If you change the architecture, update its diagrams in
  the same commit.
- **`config.ini`** / **`match_state.json`** — local settings (git-ignored, hold secrets).
  Templates: `config.example.ini`, `match_state.example.json`.
- **`match_data.db`** — SQLite ball-by-ball log, created at runtime (git-ignored).
- **`sponsors/`** — weekend-sponsor logos, named by ID (`sponsors/3.png`), served via
  `/sponsor/<id>`. Paired with the control panel's "Weekend sponsor name" / "Weekend sponsor
  image ID" fields. **Airtime:** the overlay beacons `POST /sponsor/airtime` (`show`/`hide`,
  loopback carve-out, never from preview mode); the server times it on its own clock, takes
  the name from state, caps one appearance at 12s (a lost hide is credited the cap, so keep it near the longest panel), and keeps live time separate from time
  before going live — only live time is ever presented as airtime. Totals persist to
  `sponsor_airtime.json` (git-ignored); `GET /sponsor/airtime[?date=]` must stay above the
  `/sponsor/<id>` prefix route. Quickstart prints it after the match and saves
  `sponsor_airtime_<date>.txt`. **Viewer-minutes:** while live, and only if YouTube is
  already authorised, `_viewer_tick()` samples the ACTIVE broadcast's concurrentViewers once
  a minute (never the title-updater's newest-broadcast fallback — that could credit a past
  stream's audience; never an interactive login). Each on-air appearance adds seconds ×
  the count if it's ≤3 min old, tracked with the seconds it covered: the line says
  "estimated" and, when coverage is partial, says over how much — never scale it up.
  **Transparent copies** are recorded in `sponsors/.variants.json` ({copy: original}) so
  the result card shows each sponsor once (`sponsor_logos_for_card()`). **Logo upload:** `POST /sponsor/upload` takes the raw image
  as the body (no multipart), handled at the TOP of `do_POST` — before the generic 1 MB
  body read, so it can allow 8 MB and check login/origin before reading a byte. The type
  comes from the file's magic bytes (`sniff_image`: PNG/JPEG/WebP/GIF; never SVG, which
  can carry script), saved as the next free number under `_sponsor_upload_lock` so two
  uploads can't take the same ID. **Transparent backgrounds:** `logo_bg.py` (Pillow only,
  no numpy) — background colour from the border, then two modes the operator picks between
  by eye in the panel, because no one rule handles both kinds of real logo: `outside`
  (edge-connected only: keeps a badge's white middle, leaves letter holes white) and
  `everywhere` (letter holes cleared, a badge's middle too). Edge opacity is judged against
  the logo colour right beside each pixel, not a blur of the cut-out (a blur left a white
  halo). Always saved as a NEW image, so Undo is the old ID. Panel previews must be `data:`
  URLs: the panel's CSP is `img-src 'self' data:`, so `blob:` previews render blank. Renders as a persistent strap overlaid on the over-summary/partnership/
  AI-commentary panels only (never the run-rate worm — see `showSponsorStripFor` /
  `.sponsor-space-reserved` in `overlay.html`); off entirely unless a sponsor name is set.

## Build / test commands

There is no compiler — verification is a compile check, an embedded-JS syntax check, and a
stdlib-unittest suite (no test dependencies to install):

```bash
# 1. Every top-level script must compile cleanly — not just server.py. Nine standalone
#    tools (scorer_agent.py, setup_wizard.py, every diagnostic script) had zero syntax
#    coverage until this existed; a broken one would go green in CI otherwise.
python3 scripts/compile_check_all.py

# 2. Syntax-check the JS in control.html and overlay.html.
#    Uses node if present, else falls back to macOS JavaScriptCore, else esprima.
python3 scripts/check_panel_js.py

# 3. Automated tests (~550, under a minute; stdlib unittest, no pytest). Covers ball/PCS/widget
#    parsing, season-stats aggregation, league-table resolution, session tokens, quickstart's
#    state merge and its crash-restart loop, the match simulator's engine invariants, highlight
#    tagging/planning, manual scoring (engine, exact-replay undo, /scoring end-to-end),
#    stream-quality downshift decisions, installer/launcher robustness, scorebar-style
#    consistency, JS logic executed in a real engine (classifyBall parity, the bowler-milestone
#    chain, the camera auto-cut's replay-collision rules), and HTTP integration tests that spin
#    up the real Handler on an ephemeral port (auth, redaction, path traversal, origin check,
#    loopback carve-out, /live vs /live/view, event buffer, ball DB).
python3 -m unittest discover -s tests

# 4. Run it
pip install -r requirements.txt
python3 server.py      # or: python3 quickstart.py
```

Always run steps 1–3 after editing `server.py`, `overlay.html`, `quickstart.py`, or any other
top-level script. Step 2 matters more than it looks (see gotchas). All three are wired into
`.github/workflows/ci.yml`.

**There is no automated check for how anything LOOKS.** Scorebar styles and graphic panels
are CSS over a shared DOM, and the failure mode is always visual, never a syntax error — a
team colour that vanishes into the background, an inline colour from `applyColours()` beating
the stylesheet, one style's full-width accent painting over another element. Render it:

```bash
# Every scorebar style -> examples/, headless Chrome/Edge, fixed mock data, no server needed.
python3 scripts/render_scorebar.py             # all four
python3 scripts/render_scorebar.py modern      # just one
```

Two real bugs on the day it was added that none of steps 1–3 could have caught: Minimal's
bowler name rendered white-on-white, and Impact's footer rule covered the striker underline.
The control panel's own live preview (below) is the other half of this.
The HTTP tests patch `server.STATE_FILE`/`server._db_path` to a temp dir — real
`match_state.json`/`match_data.db` are never touched.

## Critical gotchas (these have bitten us before)

- **(Mostly historical) the panel used to live inside server.py as `CONTROL_HTML`**, a
  triple-quoted Python string where every JS backslash had to be doubled — a single one
  broke the whole `<script>` block silently (every function "not defined", the status line
  hung on "Checking connection..."). The panel is now `control.html`, a plain file with
  normal JS escaping, which kills that bug class — but two defenses remain and must stay:
  `scripts/check_panel_js.py` (step 2 above; run after every panel edit) and the
  `window.onerror` red-banner handler at the top of control.html's first `<script>` block.
  The extraction wrote the *evaluated* string, so control.html has clean, normal JS
  escaping throughout (`'\n'`, `/\d+/` — no doubling anywhere).
- **The overlay's own poll is `/live`; everything else must use `/live/view`.** `/live` is
  a mutating GET — it advances event detection, logs balls to the DB, and consumes the
  wicket-event buffer, so exactly ONE client (the OBS overlay) may call it. Panel features
  and any new tooling read `/live/view` (same response, no side effects). `GET /commands`
  is the same shape of trap: it POPS the queue, so a second consumer silently steals
  replay/scorecard commands from the real overlay.
  **This is why `overlay.html` has a preview mode** (`/overlay?preview=1&style=<name>`):
  the control panel embeds the real overlay in an iframe to show what each scorebar style
  looks like, and it renders exactly ONE static frame of demo data with both timers never
  started. Don't "fix" that by letting preview mode poll — an operator leaving the panel
  open mid-match would eat the overlay's events and commands. `tests/test_scorebar_styles.py`
  guards both short-circuits. It takes one `/state` read for the club's real colours and
  nothing else, ever.
- **`overlay.html` JS brace balance baseline is 4** (it isn't zero — there are intentional
  unmatched braces in template strings). Don't "fix" it to zero.
- **The HTTP server's `request_queue_size` must stay explicitly raised (currently 64).**
  Every request here is a fresh HTTP/1.0 connection (no keep-alive), so a burst of
  near-simultaneous pollers (overlay + panel + `/scoring` + several devices reconnecting
  together after a wifi blip) queues up real TCP connects, not just requests. The stdlib
  `ThreadingHTTPServer` default backlog of 5 was found by load-testing (`ab -c 10`+) to
  reset connections outright — the server itself never errored, the client just got refused
  at the OS level before Python's handler ever ran. Don't let this silently regress back to
  the default while refactoring the `_Server` class.
- **A match keeps one id when it changes mid-innings.** `current_match_id()` is the
  PlayCricket id once fetched, else date + both team names, so a late "Fetch today's match"
  or an opposition rename mid-match used to split the match's data. `follow_match_id()`
  (called before the loggers on every /live poll) moves the rows to the new id — but only
  if the innings is under way, the old id logged in the last 10 minutes and the new id is
  empty, so a morning rehearsal is never merged into the real match. A new table keyed by
  `match_id` must be added to `_MATCH_TABLES` or it gets left behind.
- **Post-match facts come from the scorer's own figures, never the `balls` table.** The AI
  report/social post (`build_match_summary()`) and the result card
  (`generate_social_graphic_facts()`) both read `merged_match_facts()`: the `live_*` tables
  that `log_live_figures()` keeps from each /live frame (innings total + overs limit from
  `ballsRemaining`, each batter's runs/balls, each bowler's figures, who fell at what
  score), with the in-memory `_match_log` only filling gaps. Why: `_match_log` dies with
  any restart (handed only "Match: X v Y", Haiku wrote a confident made-up result), and the
  `balls` table's `batter` column is whoever was batter1 at the over's last write — not who
  faced each ball. Empty 0-0 innings don't count as data; callers refuse with
  `NO_MATCH_DATA_ERROR`. Never let the card or report claim WIN/DEFEAT when it can't tell
  which side is ours, or before the chase is finished (PlayCricket's live scorecard has no
  result mid-match). Found in the 2026-10-02 rehearsal and review.
- **Logging must never raise.** `log_ball_data()` and anything in the match-day loop is wrapped
  in try/except and must stay that way — a logging error must never interrupt the stream.
- **State writes must stay atomic.** `save_state()` writes to a temp file then `os.replace()`s,
  with a last-good fallback. Don't replace this with a naive `open().write()`. It's also
  serialised by `_state_write_lock` (every writer shares the one `.tmp` name; two threads at
  once gave "Permission denied" on Windows and a dropped request) and retries `os.replace`
  for ~2s, because Windows refuses to replace a file another thread has open for reading.
  `tests/test_sponsor_upload.py` reproduces both (and fails with the lock removed). That
  lock does NOT make load-modify-save atomic — for that use `update_state(change)`, which
  holds `_state_rmw_lock` across load→change→save. POST /state, logo uploads and the
  checklist's password sync use it; older call sites still do bare load/modify/save and
  can lose a concurrent change — convert them as you touch them.
- **HTTP tests must set `_CLUB_PASSWORD` explicitly.** `server.py` reads the REAL
  `config.ini` at import, so a test that assumes "no password" passes in CI (no
  config.ini) and 401s on any machine with a club password — patch it to `""` (or a value)
  in setUp, as the checklist/airtime/upload tests do.
- **Route handling checks specific paths before prefixes.** When adding endpoints, put exact
  matches (`path == "/data/status"`) before `startswith` checks so a prefix doesn't swallow a
  more specific route.
- **The current-over DB write is delete-then-reinsert.** That's deliberate — it's how scorer
  edits/deletions within an over stay correct. Don't switch it to plain append.
- **Secrets never reach the browser.** `/state` redacts secret keys; POST `/state` drops
  sentinel values. Keep any new secret field in that redaction list.
- **Never commit `config.ini`, `match_state.json`, or `match_data.db`.** They're git-ignored;
  check `git status` before committing. The `.gitignore` entries are globbed
  (`config.ini*`, not `config.ini`) so a hand-made backup copy (`config.ini.bak`,
  `match_data.db.bak-20260801`) doesn't fall through and get published by a bare `git add .`
  — extend any new secret-holding filename the same way, not as a bare name.
- **The server reads the scorer's LOCAL file.** It must run on a machine that can see the
  scorer's output folder, so it can never move to a cloud host — remote *operation* (not the
  server itself) is what's exposed. Built: Tailscale (private, recommended first) and a
  Cloudflare Tunnel quick tunnel (public URL, opt-in via `config.ini [Network]
  cloudflare_tunnel`, refuses to start unless `club_password` is set). Don't port-forward the
  raw port directly — no TLS, no gating, worst option. A cloud relay (tiny VPS; the laptop
  opens a persistent outbound WebSocket to it, the relay forwards control messages back) was
  scoped but deliberately not built — only worth it if Tailscale and Cloudflare Tunnel are
  both genuinely blocked on a club's network, which hasn't come up. NV Play itself CAN now
  run on separate hardware from the server via `nvplay_bridge.py` — this is different from
  the point above, which is about remote *access to the panel*, not where the scorer sits.
- **Every PCS-folder consumer must call `effective_pcs_folder()`, never read
  `pcs_output_folder` directly** — same shape of bug class as the manual-scoring precedence
  above. When `pcs_bridge_url` is set (NV Play on separate hardware), the folder to actually
  scan is the local mirror (`PCS_BRIDGE_CACHE_DIR`), not the configured path.
  `effective_pcs_folder()` is the one place that branches on it; `/live`, `/health`,
  `/pcs/debug`, the watchdog's freshness check, and AI over-commentary all call it. A new
  endpoint reading `pcs_output_folder` straight from state will work fine in local mode and
  silently see nothing in bridge mode. Two-laptop mode (`pcs_source = "agent"`, see
  `scorer_agent.py` above) is a THIRD source alongside local/bridge, one level up —
  `read_score_source()` wraps `effective_pcs_folder()`+`read_pcs_file()` for local/bridge
  and dispatches to `read_agent_file()` for agent mode. Same rule, one door higher: a new
  call site must go through `read_score_source()`, not `read_pcs_file()` directly, or it
  works in local/bridge mode and silently sees nothing when a club is running agent mode.
- **`/health`'s `thermal` block and the watchdog's throttle warning read `pmset -g therm`,
  not an actual temperature** — macOS doesn't expose real sensor readings without extra
  tooling, but `CPU_Speed_Limit`/`CPU_Scheduler_Limit` dropping below 100 is the signal that
  actually matters: it fires the moment the OS starts throttling for heat, which is well
  before a crash. Built after a streaming Mac overheated running a scorer's VM alongside
  OBS — moving NV Play off that machine (see `nvplay_bridge.py` above) is the real fix;
  this is early warning, not a cooling system. Returns `{"available": false}` on non-macOS
  by design (Windows/Linux thermal signals weren't scoped).
- **Inside a frozen `setup_wizard.py` (PyInstaller), `sys.executable` is the exe itself, not a
  Python interpreter** — passing it to `subprocess` for `pip`/launching another script causes
  infinite self-relaunching. Use `find_python()`, which searches `PATH` instead. Same trap for
  `__file__`: it resolves inside the temp extraction folder, so paths must use
  `os.path.dirname(sys.executable)` when `sys.frozen` is set.
- **No installer/launcher may exit without pausing first, and none may assume it's in the
  project folder.** A double-clicked `.exe` or `.bat` owns its console window, so a bare
  `sys.exit()` closes it with the reason inside — including tracebacks, which are exactly
  the text needed to diagnose the problem. `setup_wizard.py`, `quickstart.py` and
  `cricketstream.py` all route fatal paths through `die()` (framed problem
  + what to do + pause) and catch unhandled exceptions; `CRICKETSTREAM_NO_PAUSE=1` turns the
  pauses off for automation, and `quickstart.py` additionally only pauses on a real TTY so
  the wizard's own subprocess and the test suite never block. Every `Windows/*.bat` and
  `Mac/*.sh` searches its own folder THEN the parent before giving up, because they live in
  `Windows/`/`Mac/` while everything they run lives in the repo root — three of the four
  Windows launchers were broken as shipped for exactly this reason until 2026-09-24.
  `setup_wizard.py` also locates the project itself (`find_project_root()`) rather than
  trusting its own folder: an exe dropped in `Windows/` used to write `config.ini` beside
  itself, where `server.py` — which only reads `config.ini` from its OWN folder — never
  found it, leaving a half-configured install with no error anywhere.
  `tests/test_installer.py` guards all of it.
- **No hardcoded club identity in defaults.** `DEFAULT_STATE`, `config.example.ini`, and
  `match_state.example.json` must stay club-agnostic (e.g. `"Home CC"`, blank `ground_filter`/
  `home_club_id`) — this project is used by clubs other than the original maintainer's.
- **NV Play does NOT reliably clear its ball-ticker field (`last_ball`) when an over
  completes** — this was long documented here as the opposite ("clears back to `""` the
  instant an over completes") until measured against a full match's captured feed: of 857
  polls where `overs` sat on a whole number, 801 still carried the finished over's stale
  ticker. Two separate things in `overlay.html`'s `processPCSData` depend on this and must
  NOT use the ticker string to detect the boundary:
  - **Over-transition detection** (`_oversIncreased`) keys off the completed-overs counter
    increasing (`currentOver > _lastPCSovers`), deliberately not gated on the ticker being
    populated — gating on it delays the whole end-of-over sequence to the first ball of the
    NEXT over, since `_lastPCSovers` never gets the chance to update on the poll where
    `overs` actually ticks over.
  - **The ball logger** (`log_ball_data`) treats a ticker identical to the over it just
    logged as cleared, on the over-completing write and every write after it until the
    next ball. Before that, the final ball of most overs was never recovered (it read as
    negative runs) and the stale balls were logged again under the next over — which,
    at the end of an innings, stayed in the DB as an over that was never bowled.
  - **The visible ticker itself** used to only clear once the new over's ticker string
    replaced it — which, given the above, usually never happened until the next ball, so
    the scorebar showed the finished over's balls through the whole gap until then,
    disagreeing with the over-summary card. Fixed by keying the clear off the over boundary
    directly (`ballInOver === 0 && currentOver > 0`, guarded so innings-start 0.0 doesn't
    trigger it) instead of the ticker string's content.
- **On that same over-completing write, `bowler` has ALREADY rotated to the next over's
  bowler and the batter pair has swapped ends.** Anything attributing the over's final
  (never-in-any-ticker) delivery must use the previous poll's snapshot — `_lastPolledBowler`
  in the overlay (over summary, hat-trick/five-for fallback) and the personnel stashed in
  `_ball_log_prev` in the ball logger — never that write's `state.bowler`/`batter1/2`. Three
  separate features trusted the current write and misattributed every over-final wicket.
- **`overlay.html`'s `SERVER` constant must be `location.origin`, never a hardcoded host.**
  The server rejects cross-origin POSTs by design (`_origin_ok()` compares the `Origin` header
  against `Host`, a CSRF defense). If the overlay is loaded via `localhost` but `SERVER` points
  at `127.0.0.1` (or vice versa), every POST it makes (`/replay`, `/weather/show`, ...) silently
  403s — and `curl` testing won't catch this, since curl doesn't send an `Origin` header at all
  (add `-H "Origin: ..."` explicitly, or test from a real browser tab, to catch this class of bug).
- **Endpoints the overlay itself calls need the trusted-loopback carve-out, not just a session
  token.** The overlay has no login flow (it's a loopback OBS browser source), so `/replay`,
  `/weather/show`, `/weather/hide`, and `/commentary/over/generate` check
  `_is_trusted_loopback() or _check_token()` in `do_POST` instead of requiring a token
  outright. Any *new* endpoint the overlay calls needs the same carve-out, or it silently
  401/403s the moment `club_password` is set, with nothing but a console warning to show for it.
- **Two players sharing a surname silently suppress season-stat matching, by design.** PCS Pro
  only reports a bare surname, so if it's ambiguous (e.g. brothers), the surname-only stats
  lookup is deliberately withheld rather than risk crediting the wrong player. The fix is data,
  not code: add `shirt_number = Full Name` to the **Squad Roster** card in the control panel.
  `/player/stats?name=SURNAME&debug=1` shows exactly why a name did or didn't resolve.
- **The camera source must be added to EVERY OBS scene that uses it, including Replay** — not
  just Main. A source that isn't present in a scene isn't just hidden when that scene is live,
  it deactivates outright; switching back to Main re-activates it from scratch, which means
  the RTSP feed reconnects and restarts its internal buffering. The picture then drifts out
  of sync with the overlay graphics for the rest of the session — the score updates before you
  see the ball. This was the actual root cause behind the RTSP drift `refresh_cam.py` was
  built to paper over (see its file entry above); once the camera source was added to the
  Replay scene too, the drift stopped happening in the first place and periodic reloads became
  mostly unnecessary. Check every scene in OBS's scene list has the camera source, not just
  the one usually shown.
- **Optional second (bowler-end) camera, added 2026-09-20** — `obs_add_camera()` takes an
  `extra_scenes` param and auto-creates a missing target scene, generalizing the gotcha
  above across more than two scenes (both cameras end up in Main, the bowler-end scene,
  and Replay). Scene creation, cross-presence and stacking were verified 2026-10-01 on a
  real OBS 32.1.1 using two VIDEO FILES as the cameras (screenshots confirmed each scene
  shows its own camera) — still not with two real RTSP cameras, so reconnect/drift
  behaviour with a second live feed is unverified. Manual cut is `/camera/scene`; the
  opt-in automatic cut at the over boundary is `graphics_camera_auto_cut`.
- **Testing against a real OBS: only ever a PORTABLE copy.** Copy `C:\Program Files\
  obs-studio` somewhere scratch and create `obs_portable_mode.txt` in it: OBS then keeps
  every setting inside that folder. Do NOT rely on overriding `APPDATA` — Python honours it
  but OBS on Windows ignores it (it asks Windows for the folder directly), so an "isolated"
  test on 2026-10-01 launched the real install against the maintainer's REAL settings and
  rearranged their scenes. To drive the server against the copy, run a scratch copy of the
  project whose `obs_prep.py` overrides `find_obs()`/`obs_config_dir()` to the portable
  folder and refuses to run without the marker.
- **The control panel's Match-day checklist** (`checklist_status()` / `checklist_action()`,
  `GET /checklist`, `POST /checklist/action`, both token-gated — they start OBS and go live,
  so NEVER the overlay's loopback carve-out). Every item is detected, none ticked by hand
  (the old localStorage ticks are gone). "Start OBS" runs `obs_prep.prepare_obs` +
  `obs_setup.setup_from_config(allow_restart=True)` in a background job and copies a
  freshly written WebSocket password from config.ini into state, or every later
  `_obs_call` fails auth. `_obs_call` returns None per REFUSED request — check `r[0]`,
  not just `r`, or a refusal reads as success (the first version did exactly that).
- **A brand-new OBS needs one restart before the replay buffer can run**, and
  `setup_from_config(allow_restart=True)` (quickstart, the checklist) now does it: on
  obs_setup's "wouldn't start yet", `obs_prep.close_obs()` closes OBS the way a person
  would (WM_CLOSE / osascript quit; never a force-kill, so no crash marker) and
  `prepare_obs` reopens it. Two things had to go for that to work, both modal on a fresh
  OBS: the Auto-Configuration Wizard (`skip_first_run_wizard()`: `[General]
  FirstRun=true` in user.ini and global.ini) — it blocks the close — and the update box
  (paused, above). With an output running OBS asks "exit anyway?"; `close_obs` gives up
  and CANCELS that question rather than leave it on screen. The standalone
  `obs_setup.py` never restarts OBS (allow_restart=False): whoever runs it by hand is
  managing OBS. Verified end to end on a fresh portable OBS: one Start OBS press, replay
  buffer running ten seconds later, no dialogs left.
- **Never test or switch OBS's encoder over the WebSocket.** OBS only builds encoders
  when it starts (or its settings dialog is applied), so `SetProfileParameter
  StreamEncoder` + a test recording measures whatever encoder was built at launch — the
  old Stream Health Check "hardware vs x264" comparison did exactly that and reported
  NVENC twice (OBS's log showed no x264 encoder ever created). `GetStats`'
  `outputTotalFrames`/`outputSkippedFrames` are pipeline-wide, not per recording, too.
  That check was REMOVED (2026-10-01). Encoder choice is now `obs_prep.ensure_encoder()`:
  OBS closed, reads the "Available Encoders" list from OBS's own newest log, and moves
  Simple-mode x264 (or unset) onto the best hardware H.264 encoder; never touches a
  hardware choice or Advanced mode; `[Stream] encoder = manual` opts out. Verified by
  OBS's log naming the encoder it actually ran, before and after.
- **Scene stacking: move cameras DOWN, never lift graphics UP.** `obs_prep.covering_pictures()`
  lists the picture sources (cameras, video, screen capture — `PICTURE_KINDS`) above a
  graphic; obs_setup moves each, lowest first, to just beneath it, which keeps two cameras'
  order and leaves everything else (sponsor-logo images above the scorebar) where the club
  put it. The first version lifted the graphics to the top of the scene and buried a real
  club's logos under the scorebar.
- **`--startreplaybuffer` only once the scenes exist** (`obs_prep.launch_args()` checks the
  scene collection for an `Overlay` source). On an empty scene OBS won't start the buffer:
  it opens a modal "No Sources" question instead, which on a fresh install nobody is there
  to answer. First time through, `obs_setup.py` builds the scenes and starts the buffer
  over the WebSocket.
- **OBS's update check is paused for match day** (`pause_update_check()`: `[General]
  EnableAutoUpdates=false` in `global.ini`, original value kept in a
  `.cricketstream-updates-paused` marker in OBS's config folder). Measured: the "New update
  available" box on 4 of 6 default launches, 0 of 4 with it off. Restored by
  `resume_update_check()` when the server's OBS guard sees a NORMAL close, or at
  quickstart's shutdown if OBS is already closed; a crash keeps it paused. A club that
  had it off already gets no marker and is never switched on. Tests that reach
  quickstart's shutdown must stub `_resume_obs_updates` — it reads the REAL OBS folder.
- **OBS puts every new source on TOP of its scene, at native size, top-left.** Before
  `_arrange_camera_item()`, adding a camera covered the Overlay in Main (no graphics) and
  ReplayClip in Replay (replays showed the live camera), and a 720p camera filled a
  quarter of the canvas — all confirmed on real OBS. Rules now: a camera in its HOME scene
  sits directly under the graphics; in any other scene it goes to the bottom (it's only
  there to stay active); new or untouched-default placements are fitted to the canvas
  with `OBS_BOUNDS_SCALE_INNER`, hand-placed ones are left alone. Home camera scenes get
  the Overlay added (the bowler-end scene was created without it, so the auto-cut hid the
  scorebar). `obs_setup.py` re-asserts Overlay/ReplayClip on top every run, which repairs
  installs the old code already broke, and fits ReplayClip (replays are recorded at the
  OUTPUT resolution, 720p by default). Any new code that adds a source to a scene must
  think about where OBS just put it.

## Conventions

- Match the surrounding style; don't reformat whole files.
- Comments explain *why*, not *what*.
- Prefer small, focused commits with present-tense messages ("Add X", not "added x").
- PlayCricket: BBCC `site_id`/`club_id` = `29434`. The API token is not club-specific.

## Useful diagnostics

- **Control panel → Setup → Run pre-match check** (`POST /precheck`, token-gated, 20s
  cooldown) — tries every credential and connection for real, in parallel with a time limit
  each: YouTube login + which channel, PlayCricket key, Anthropic key (free `models.list`),
  OBS WebSocket, scorer feed, replay disk space, OBS bitrate (from basic.ini when OBS is
  closed; the target is `_target_bitrate()` — config.ini's rule, same as obs_setup), upload
  speed, cameras (TCP to the RTSP port; never echo the URL — it holds the password),
  surname clashes vs the roster, today's fixture, opposition badge, newer release,
  scorer's laptop/bridge. Read-only. Add new external dependencies to `PRECHECKS`; a check
  returns (ok|warn|bad|skip, detail, fix) and may raise — that's reported as `bad`, never a
  crash. Keep ✗ for things that are wrong NOW: the server re-runs it every 6 hours and a ✗
  puts a red badge in the panel's pinned bar, so "expected days before" states (cameras
  away from the ground, OBS closed) must be ⚠ or the badge is red all week.
- `http://localhost:5000/health` — feed freshness, photos, badges, AI key status, NV Play
  bridge connectivity (`pcs.bridge`), Mac thermal-throttle state (`thermal`), and a
  pre-flight OBS bitrate sanity check (`obs_bitrate`) that flags a leftover downshift from a
  previous match before the operator goes live — see `obs_bitrate_sanity_check()` — and
  OBS crash recovery (`obs_recovery`: watching, recent reopens, last event).
- `http://localhost:5000/player/stats?name=SURNAME&debug=1` — which season record a name resolves to.
- `http://localhost:5000/league/table` — today's competition's table (home club's row +
  the row above), backend for the not-yet-built "win today, move up to Nth" graphic;
  `"usable": false` means `competition_id` is blank or doesn't resolve to a real league
  table (a cup/friendly fixture, most likely) — see `fetch_league_table()`.
- `http://localhost:5000/data/status` — ball-by-ball DB status.
- `http://localhost:5000/highlights/status` — outcome of the last background highlights
  compile (clips are auto-tagged at replay time via the `clips` DB table; the reel gets
  captions + a chapters description file).
- `http://localhost:5000/obs/stream_check?force=1` (auth-required) — recommended bitrate and
  resolution from a real upload-speed test (quickstart applies them to OBS next run).
- `http://localhost:5000/sponsor/airtime` — today's sponsor airtime (`?date=YYYY-MM-DD`).
- `http://localhost:5000/stream/monitor` — live congestion/dropped-frame picture while
  streaming, plus the quality-ladder position. Two-tier adaptive quality: OBS's Dynamic
  Bitrate (enabled by obs_setup; seamless) + the sentinel's bitrate ladder
  (stop→reconfigure→start, ~5-10s gap; auto mode is the `stream_auto_downshift` state key,
  off by default, and only ever steps DOWN on its own).
