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
- **`control.html`** (~2200 lines) — the operator's control panel, served by `/control`
  with the kit-colour presets injected in place of the `/*__KIT_PRESETS__*/[]` placeholder
  (see `control_html()` in server.py). Read from disk per request, so panel edits show on
  refresh without a server restart. It used to live INSIDE server.py as a Python string —
  see the (historical) backslash gotcha below.
- **`overlay.html`** (~2600 lines) — the OBS browser source (1920×1080). Pure HTML/CSS/JS.
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
  latest release; purely informational, silently skipped if there's no git/internet).
- **`setup_wizard.py`** — first-time setup wizard; installs packages and writes `config.ini`.
  Also built into a standalone Windows `.exe` / macOS binary by
  `.github/workflows/build-setup-wizard.yml` (see gotchas below).
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
- **`simulate_match.py`** — match simulator for rehearsing the whole broadcast without a
  scorer: writes NV Play-style frames (faithful to the gotchas: ticker clears on the
  over-completing write, blank pre-match names, runs_required-driven innings 2) to a fake
  PCS folder. Scenarios: full / chase / century / collapse; `--configure` points the running
  server at it; `--chaos` injects mid-write/stall failures. Deterministic per `--seed`; the
  engine is imported by `tests/test_simulator.py` as a parser-consistency harness. Always
  rehearse graphics changes with it before match day.
- **`ARCHITECTURE.md`** — contributor-facing design doc with Mermaid diagrams (data-flow,
  one ball's journey, module map). If you change the architecture, update its diagrams in
  the same commit.
- **`config.ini`** / **`match_state.json`** — local settings (git-ignored, hold secrets).
  Templates: `config.example.ini`, `match_state.example.json`.
- **`match_data.db`** — SQLite ball-by-ball log, created at runtime (git-ignored).
- **`sponsors/`** — weekend-sponsor logos, named by ID (`sponsors/3.png`), served via
  `/sponsor/<id>`. Paired with the control panel's "Weekend sponsor name" / "Weekend sponsor
  image ID" fields. Renders as a persistent strap overlaid on the over-summary/partnership/
  AI-commentary panels only (never the run-rate worm — see `showSponsorStripFor` /
  `.sponsor-space-reserved` in `overlay.html`); off entirely unless a sponsor name is set.

## Build / test commands

There is no compiler — verification is a compile check, an embedded-JS syntax check, and a
stdlib-unittest suite (no test dependencies to install):

```bash
# 1. Server must compile cleanly
python3 -c "import py_compile; py_compile.compile('server.py', doraise=True); print('OK')"

# 2. Syntax-check the JS in control.html and overlay.html.
#    Uses node if present, else falls back to macOS JavaScriptCore, else esprima.
python3 scripts/check_panel_js.py

# 3. Automated tests (~210, a few seconds; stdlib unittest, no pytest). Covers ball/PCS/widget
#    parsing, season-stats aggregation, session tokens, quickstart's state merge, the match
#    simulator's engine invariants, highlight tagging/planning, manual scoring (engine,
#    exact-replay undo, /scoring end-to-end), stream-quality downshift decisions, JS logic
#    executed in a real engine (classifyBall parity, the bowler-milestone chain), and HTTP
#    integration tests that spin up the real Handler on an ephemeral port (auth, redaction,
#    path traversal, origin check, loopback carve-out, /live vs /live/view, event buffer,
#    ball DB).
python3 -m unittest discover -s tests

# 4. Run it
pip install -r requirements.txt
python3 server.py      # or: python3 quickstart.py
```

Always run steps 1–3 after editing `server.py`, `overlay.html`, or `quickstart.py`. Step 2
matters more than it looks (see gotchas). All three are wired into `.github/workflows/ci.yml`.
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
  and any new tooling read `/live/view` (same response, no side effects).
- **`overlay.html` JS brace balance baseline is 4** (it isn't zero — there are intentional
  unmatched braces in template strings). Don't "fix" it to zero.
- **Logging must never raise.** `log_ball_data()` and anything in the match-day loop is wrapped
  in try/except and must stay that way — a logging error must never interrupt the stream.
- **State writes must stay atomic.** `save_state()` writes to a temp file then `os.replace()`s,
  with a last-good fallback. Don't replace this with a naive `open().write()`.
- **Route handling checks specific paths before prefixes.** When adding endpoints, put exact
  matches (`path == "/data/status"`) before `startswith` checks so a prefix doesn't swallow a
  more specific route.
- **The current-over DB write is delete-then-reinsert.** That's deliberate — it's how scorer
  edits/deletions within an over stay correct. Don't switch it to plain append.
- **Secrets never reach the browser.** `/state` redacts secret keys; POST `/state` drops
  sentinel values. Keep any new secret field in that redaction list.
- **Never commit `config.ini`, `match_state.json`, or `match_data.db`.** They're git-ignored;
  check `git status` before committing.
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
  silently see nothing in bridge mode.
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
- **No hardcoded club identity in defaults.** `DEFAULT_STATE`, `config.example.ini`, and
  `match_state.example.json` must stay club-agnostic (e.g. `"Home CC"`, blank `ground_filter`/
  `home_club_id`) — this project is used by clubs other than the original maintainer's.
- **NV Play clears its ball-ticker field (`last_ball`) back to `""` the instant an over
  completes** — it does NOT keep showing the finished over's ticker for one extra poll first.
  Over-transition detection in `overlay.html`'s `processPCSData` (`_oversIncreased`) must run
  *every* poll regardless of whether the ticker string is currently populated, or it fires a
  whole poll late — on the first ball of the NEXT over instead of the instant the over ends.
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

## Conventions

- Match the surrounding style; don't reformat whole files.
- Comments explain *why*, not *what*.
- Prefer small, focused commits with present-tense messages ("Add X", not "added x").
- PlayCricket: BBCC `site_id`/`club_id` = `29434`. The API token is not club-specific.

## Useful diagnostics

- `http://localhost:5000/health` — feed freshness, photos, badges, AI key status, NV Play
  bridge connectivity (`pcs.bridge`), Mac thermal-throttle state (`thermal`).
- `http://localhost:5000/player/stats?name=SURNAME&debug=1` — which season record a name resolves to.
- `http://localhost:5000/data/status` — ball-by-ball DB status.
- `http://localhost:5000/highlights/status` — outcome of the last background highlights
  compile (clips are auto-tagged at replay time via the `clips` DB table; the reel gets
  captions + a chapters description file).
- `http://localhost:5000/obs/stream_check?force=1` (auth-required) — recommended bitrate from a
  real upload-speed test, and an encoder comparison from actual short OBS test recordings
  (never trust hardware specs alone for this — see `obs_stream_health_check()`).
- `http://localhost:5000/stream/monitor` — live congestion/dropped-frame picture while
  streaming, plus the quality-ladder position. Two-tier adaptive quality: OBS's Dynamic
  Bitrate (enabled by obs_setup; seamless) + the sentinel's bitrate ladder
  (stop→reconfigure→start, ~5-10s gap; auto mode is the `stream_auto_downshift` state key,
  off by default, and only ever steps DOWN on its own).
