# CLAUDE.md — project guide for Claude Code

This file is read automatically at the start of every Claude Code session. It captures the
layout, the commands, and the hard-won gotchas so you don't have to re-explain them.

## What this project is

A free, open-source live-stream graphics system for grassroots cricket, as **one Go
binary**. It reads the scorer's match data and drives an HTML overlay inside OBS, which
streams to YouTube, with a browser **control panel** for the match-day operator. It is a
port of the Python original (<https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream>);
[`PORTING.md`](PORTING.md) has the status, the pinned upstream commit, and the porting
rules. Behaviour, output bytes and tests were carried over; don't redesign while porting.

**Data flow (three sentences):** The scorer's software (NV Play / PCS Pro) writes a JSON
file on every ball. The server reads it, parses it, and serves the live state on `/live`.
`overlay.html` (an OBS browser source) polls `/live` every ~2.5s and renders the graphics.

**One binary, four roles** (`cmd/cricketstream`): double-clicked, it opens its own window
(`internal/app`): on first run "which computer is this?" and the setup page
(`internal/setup`), then the job `config.ini`'s `[App] role` names — streaming laptop
(the match-day screen → `internal/launcher` → the server as a supervised child process,
the control panel shown in the window, logged in), scorer's laptop on the same wifi
(`internal/agent`, with its status), or scorer's machine elsewhere (`internal/bridge`).
Every part is also a subcommand (`cricketstream help`) that works at a console as before.

**The window** is the system web view (WebView2 on Windows, via `go-webview2`, pure Go —
no C compiler) showing pages the app serves on loopback port 5098 (`CRICKETSTREAM_APP_PORT`;
a second double-click finds the first copy there and brings it forward). Where there's no
web view — macOS/Linux for now, or Windows without WebView2 — the same pages open in the
browser. The console's prompts (`console.Pause/Input/Confirm/Die`) become questions in the
window through `console.SetUI`, and everything printed (the server child's output too)
goes to the window's Activity log and `logs/cricketstream-<date>.log`. Release builds on
Windows are `-H windowsgui` (no console window); run from a terminal they attach to it for
subcommands. The launcher stops the server by closing its stdin (`CRICKETSTREAM_STOP_ON_EOF`),
which is a clean stop on Windows too.

**Assets are built in** (`embed.go`: the three pages, the scoreboard template, fonts,
brand, graphics, the example config). `internal/assets` reads a file of the same name
beside the binary first, so editing `control.html` next to the exe and refreshing still
works with no rebuild. Everything the app writes lives beside the binary too
(`assets.Dir()`; `CRICKETSTREAM_HOME` overrides it, which is how tests sandbox it).

## Layout

| Path | What | Python original |
|---|---|---|
| `cmd/cricketstream` | role dispatch, subcommands, Windows resources (`winres/`, go generate) | `cricketstream.py` |
| `internal/server` | every HTTP route, PCS parsing, ball DB, OBS ops, background loops (split by concern; PORTING.md maps files to line ranges) | `server.py` |
| `internal/app` | the window: role choice, match-day screen, agent/bridge status, questions, activity log (`app.html`) | — |
| `internal/launcher` | match-day launch chain, crash-restart loop, after-match airtime + report | `quickstart.py` |
| `internal/setup` | the first-run setup page (`setup.html`) and config.ini writing | `setup_wizard.py` |
| `internal/scoring` | the scorer's-book engine + event-sourced session | `scoring_engine.py` |
| `internal/obs`, `internal/obsws` | OBS prep (closed) + setup (WebSocket); the WebSocket client and a fake OBS | `obs_prep.py`, `obs_setup.py` |
| `internal/agent`, `internal/bridge`, `internal/scorerfile` | scorer's-laptop roles; finding NV Play's output | `scorer_agent.py`, `nvplay_bridge.py` |
| `internal/clubhouse` | the optional clubhouse touch screen: the laptop's link (`link.go`, run by `internal/app`), the Pi's `cricketstream clubhouse` (`screen.go`, `clubhouse.html`), Wake-on-LAN, power-off — see CLUBHOUSE_SCREEN.md | — (Go edition only) |
| `internal/simulate` | match simulator | `simulate_match.py` |
| `internal/tools` | telemetry, camera encoder, refresh-cam, quality-ladder test | the four standalone tools |
| `internal/resultcard`, `matchpage`, `socialclips`, `winpredict`, `voice`, `logobg` | post-match products and extras | same-named `.py` files |
| `internal/pyval`, `pyfmt`, `ini`, `htmlesc` | Python truthiness/str()/round(), configparser INI, html.escape — output must stay byte-identical | — |
| `internal/port` | the server port, read the same way everywhere (`CRICKETSTREAM_PORT`) | — |
| `internal/integration` | opt-in end-to-end test against a PORTABLE OBS | — |
| `scripts/makebrand`, `scripts/renderscorebar` | brand assets; scorebar screenshots | `scripts/*.py` |

## Build / test

Go is at `C:\Users\Boreas\sdk\go\bin` on the maintainer's machine (not on PATH).

```bash
gofmt -l cmd internal scripts      # nothing printed = formatted
go vet ./...
go test ./...                      # ~1 min; the JS tests need node (they skip without it)
go build -o dist/ ./cmd/cricketstream
# release-style Windows build (no console window): add -ldflags "-H windowsgui"
```

- **Differential tests** run the Python original beside the port and compare bytes
  (scoring frames, INI round trips, result-card palette, match page, logo transparency,
  textwrap, the telemetry summary). They skip without the upstream clone in `.upstream/`
  (see PORTING.md) and Python 3 + Pillow; CI's `differential` job runs them and fails if
  any skipped.
- **Visual checks:** `go run ./scripts/renderscorebar` renders every scorebar style to
  `examples/`. There is no automated check for how anything LOOKS — render it.
- **Real OBS:** `internal/integration` drives a PORTABLE OBS end to end (prep, launch,
  scenes, replay to disk, camera add, the tools). Only with `CRICKETSTREAM_OBS_PORTABLE`
  set to a copy holding `obs_portable_mode.txt`; it refuses to run while any OBS is open
  and fails if the machine's own OBS settings change. Never test against the installed
  OBS: overriding `APPDATA` doesn't isolate OBS on Windows (it asks Windows for the folder
  directly), and an "isolated" test once rearranged a maintainer's real scenes.
- **Sandboxes can't reach the installed OBS.** With `CRICKETSTREAM_OBS_PORTABLE` set,
  `obs.PIDs/Running/Close/Kill` see only processes inside the portable folder, and
  `obsws.Guard` refuses any connection to OBS's port (4455) while an OBS from anywhere else
  is running. `CRICKETSTREAM_OBS_WS_PORT` moves 4455 for everything (the e2e harness and
  the server's `sandbox` point it at a closed port). Both exist because a manual test of
  the window, run from a sandbox folder with a binary built before the redirect existed,
  reached the maintainer's open OBS (no WebSocket password) and re-ran its setup, pointing
  the Overlay source at the sandbox's port. Always build a fresh binary for a manual run,
  and check `Get-Process obs64` first.
- **Test seams** are package vars (`obsCall`, `obsTarget`, `ServerURL`, `ConfigDir`,
  `FindOBS`, `Running`, `Launch`, wait durations…), swapped in tests and restored with
  `t.Cleanup`. The server's HTTP tests use `harness_test.go` (`sandbox`, `newTestServer`),
  which points state, DB and config at a temp dir and sets the club password explicitly
  — never let a test read the real `config.ini`.
- **Env switches:** `CRICKETSTREAM_NO_PAUSE=1` (no "Press Enter"), `CRICKETSTREAM_NO_BROWSER=1`
  (nothing opens a browser), `CRICKETSTREAM_NO_WINDOW=1` (the app serves its page and opens
  no window — tests drive it over HTTP with the page's key), `CRICKETSTREAM_APP_PORT`
  (0 = any), `CRICKETSTREAM_NO_WEBVIEW=1` (the browser instead of the window — the
  no-WebView2 fallback, by choice), `CRICKETSTREAM_HOME`, `CRICKETSTREAM_PORT`, `CRICKETSTREAM_OBS_PORTABLE`,
  `CRICKETSTREAM_OBS_WS_PORT`, `CRICKETSTREAM_DEVTOOLS=1` (right-click → Inspect in the
  window), `CRICKETSTREAM_CLUBHOUSE_PORT` (the clubhouse screen's link, default 5096). `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9333` lets a
  script drive the real window over CDP.

**No path may exit without pausing first.** A double-clicked exe owns its console window,
so exiting closes it with the reason inside. Fatal paths go through `console.Die()` (framed
problem + what to do + pause) or print and `console.Pause()`.

## Critical gotchas (these have bitten us before)

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
  open mid-match would eat the overlay's events and commands. internal/server/js_test.go
  guards both short-circuits. It takes one `/state` read for the club's real colours and
  nothing else, ever.
- **OBS loads the overlay ONCE, and match day opens OBS before the server.** A browser
  source whose first load finds nothing listening shows an error page and never retries,
  so on a fresh start the scorebar was missing and nothing reached the ball DB, scorecard
  or report (found on a VM end to end, 2026-10-29; the Python edition had it too). A page
  that did load survives a server restart by itself. `overlaywatch.go` reloads the
  `Overlay` source (`refreshnocache`) when OBS is local and reachable but no `/live` poll
  has come for 30s — at most 3 times between polls. `/live/view` doesn't count as the
  overlay; keep it that way.
- **`overlay.html` JS brace balance baseline is 4** (it isn't zero — there are intentional
  unmatched braces in template strings). Don't "fix" it to zero.
- **A match keeps one id when it changes mid-innings.** `CurrentMatchID()` is the
  PlayCricket id once fetched, else date + both team names, so a late "Fetch today's match"
  or an opposition rename mid-match used to split the match's data. `FollowMatchID()`
  (called before the loggers on every /live poll) moves the rows to the new id — but only
  if the innings is under way, the old id logged in the last 10 minutes and the new id is
  empty, so a morning rehearsal is never merged into the real match. A new table keyed by
  `match_id` must be added to `matchTables` or it gets left behind.
- **Post-match facts come from the scorer's own figures, never the `balls` table.** The AI
  report/social post (`buildMatchSummary()`) and the result card
  (`GenerateSocialGraphicFacts()`) both read `mergedMatchFacts()`: the `live_*` tables
  that `LogLiveFigures()` keeps from each /live frame (innings total + overs limit from
  `ballsRemaining`, each batter's runs/balls, each bowler's figures, who fell at what
  score), with the in-memory the in-memory match log only filling gaps. Why: the in-memory match log dies with
  any restart (handed only "Match: X v Y", Haiku wrote a confident made-up result), and the
  `balls` table's `batter` column is whoever was batter1 at the over's last write — not who
  faced each ball. Empty 0-0 innings don't count as data; callers refuse with
  `NoMatchDataError`. Never let the card or report claim WIN/DEFEAT when it can't tell
  which side is ours, or before the chase is finished (PlayCricket's live scorecard has no
  result mid-match). Found in the 2026-10-02 rehearsal and review.
- **Logging must never raise.** `LogBallData()` and anything in the match-day loop is wrapped
  in try/except and must stay that way — a logging error must never interrupt the stream.
- **State writes must stay atomic.** `SaveState()` writes to a temp file then `os.Rename`s,
  with a last-good fallback. Don't replace this with a naive `open().write()`. It's also
  serialised by `stateWriteMu` (every writer shares the one `.tmp` name; two threads at
  once gave "Permission denied" on Windows and a dropped request) and retries `os.Rename`
  for ~2s, because Windows refuses to replace a file another thread has open for reading.
  internal/server/sponsor_test.go reproduces both (and fails with the lock removed). That
  lock does NOT make load-modify-save atomic — for that use `UpdateState(change)`, which
  holds `stateRMWMu` across load→change→save. Every load-then-save holds it now:
  `UpdateState` for a change, `ReplaceState(build)` for the launcher's whole-file write,
  and `SeedStateFromConfig` takes it itself (it saves only when it seeded something).
  `SaveState` on its own is only for writing defaults into a missing file. A new writer
  that loads, changes and calls `SaveState` can lose a concurrent change —
  internal/server/state_rmw_test.go shows both ways it did (and fails with the lock removed).
- **Route handling checks specific paths before prefixes.** When adding endpoints, put exact
  matches (`path == "/data/status"`) before `startswith` checks so a prefix doesn't swallow a
  more specific route.
- **The current-over DB write is delete-then-reinsert.** That's deliberate — it's how scorer
  edits/deletions within an over stay correct. Don't switch it to plain append.
- **Secrets never reach the browser.** `/state` redacts secret keys; POST `/state` drops
  sentinel values. Keep any new secret field in that redaction list.
  The Discord alerts webhook (`[Alerts] discord_webhook`) is a secret too — the token is
  in the URL — and it's never in the state at all: only `internal/discord`, the window's
  `sendDiscord` and the pre-match check read it from config.ini. Go's HTTP errors quote
  the URL, so `discord` strips it (`clean()`); print only errors that came through it.
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
  run on separate hardware from the server via the bridge (`cricketstream bridge`) — this is different from
  the point above, which is about remote *access to the panel*, not where the scorer sits.
- **Every PCS-folder consumer must call `EffectivePCSFolder()`, never read
  `pcs_output_folder` directly** — same shape of bug class as the manual-scoring precedence
  above. When `pcs_bridge_url` is set (NV Play on separate hardware), the folder to actually
  scan is the local mirror (the bridge cache folder), not the configured path.
  `EffectivePCSFolder()` is the one place that branches on it; `/live`, `/health`,
  `/pcs/debug`, the watchdog's freshness check, and AI over-commentary all call it. A new
  endpoint reading `pcs_output_folder` straight from state will work fine in local mode and
  silently see nothing in bridge mode. Two-laptop mode (`pcs_source = "agent"`, see
  the agent (`cricketstream agent`) above) is a THIRD source alongside local/bridge, one level up —
  `ReadScoreSource()` wraps `EffectivePCSFolder()`+`ReadPCSFile()` for local/bridge
  and dispatches to `ReadAgentFile()` for agent mode. Same rule, one door higher: a new
  call site must go through `ReadScoreSource()`, not `ReadPCSFile()` directly, or it
  works in local/bridge mode and silently sees nothing when a club is running agent mode.
- **`/health`'s `thermal` block and the watchdog's throttle warning read `pmset -g therm`,
  not an actual temperature** — macOS doesn't expose real sensor readings without extra
  tooling, but `CPU_Speed_Limit`/`CPU_Scheduler_Limit` dropping below 100 is the signal that
  actually matters: it fires the moment the OS starts throttling for heat, which is well
  before a crash. Built after a streaming Mac overheated running a scorer's VM alongside
  OBS — moving NV Play off that machine (see the bridge (`cricketstream bridge`) above) is the real fix;
  this is early warning, not a cooling system. On **Windows** it's the "% Performance
  Limit" counter (`thermal_windows.go`, read through pdh.dll — never by starting
  PowerShell, /health is polled every 10s), red below 95; it also drops when a laptop is
  off its charger, so the warning names the charger first. A real temperature needs admin
  rights on Windows (the ACPI thermal zone), so it isn't used. `{"available": false}` on
  Linux.
- **No hardcoded club identity in defaults.** `DefaultState()`, `config.example.ini`, and
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
  - **Boundary and hat-trick detection** match each ticker to the over it BELONGS to
    (`tickerOverOf()`: a write sitting on a whole number of overs belongs to the finished
    over), never to `overs` itself. Keyed on `overs`, the occasional write that DOES show
    the final ball read as a brand-new over — every four/six in it replayed again — and
    the next over's first ball was then sliced off as already seen. The hat-trick chain
    also runs on the over-completing write even when the ticker didn't change (the usual
    case): only that write still has the pre-rotation bowler to credit a final-ball wicket
    to; left to the next ball, it went to the incoming bowler. the hat-trick tests in internal/server/js_test.go
    covers all three NV Play behaviours (stale / cleared / final ball shown).
  - **The ball logger** (`LogBallData`) treats a ticker identical to the over it just
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
- **Breaks end at the first ball, counted from the overs — never from the ticker.** The
  drinks card used to hide on "the next poll with balls in the ticker", which (stale
  ticker, above) was the very next poll: drinks never really showed. The overlay's
  `updateBreak()` starts drinks at `drinks_over` complete, the innings break when innings 1
  is all out / out of overs, and ends either at a ball bowled after it (or the reply's
  first ball); the panel starts rain delays / breaks in play the same way. The server's
  `breakshow.go` plays the match's best saved clips through the Replay scene between the
  overlay's break graphics, and cuts back to Main the moment the break ends.
- **On that same over-completing write, `bowler` has ALREADY rotated to the next over's
  bowler and the batter pair has swapped ends.** Anything attributing the over's final
  (never-in-any-ticker) delivery must use the previous poll's snapshot — `_lastPolledBowler`
  in the overlay (over summary, hat-trick/five-for fallback) and the personnel stashed in
  `_ball_log_prev` in the ball logger — never that write's `state.bowler`/`batter1/2`. Three
  separate features trusted the current write and misattributed every over-final wicket.
- **`overlay.html`'s `SERVER` constant must be `location.origin`, never a hardcoded host.**
  The server rejects cross-origin POSTs by design (`originOK()` compares the `Origin` header
  against `Host`, a CSRF defense). If the overlay is loaded via `localhost` but `SERVER` points
  at `127.0.0.1` (or vice versa), every POST it makes (`/replay`, `/weather/show`, ...) silently
  403s — and `curl` testing won't catch this, since curl doesn't send an `Origin` header at all
  (add `-H "Origin: ..."` explicitly, or test from a real browser tab, to catch this class of bug).
- **Endpoints the overlay itself calls need the trusted-loopback carve-out, not just a session
  token.** The overlay has no login flow (it's a loopback OBS browser source), so `/replay`,
  `/weather/show`, `/weather/hide`, and `/commentary/over/generate` check
  `trustedLoopback() or checkToken()` in the POST handler instead of requiring a token
  outright (so do `/break/start` and `/break/end`, the break show). Any *new* endpoint the
  overlay calls needs the same carve-out, or it silently
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
  see the ball. This was the actual root cause behind the RTSP drift `cricketstream refresh-cam` was
  built to paper over (see its file entry above); once the camera source was added to the
  Replay scene too, the drift stopped happening in the first place and periodic reloads became
  mostly unnecessary. Check every scene in OBS's scene list has the camera source, not just
  the one usually shown.
- **Optional second (bowler-end) camera, added 2026-09-20** — `ObsAddCamera()` takes an
  `extra_scenes` param and auto-creates a missing target scene, generalizing the gotcha
  above across more than two scenes (both cameras end up in Main, the bowler-end scene,
  and Replay). Scene creation, cross-presence and stacking were verified 2026-10-01 on a
  real OBS 32.1.1 using two VIDEO FILES as the cameras (screenshots confirmed each scene
  shows its own camera) — still not with two real RTSP cameras, so reconnect/drift
  behaviour with a second live feed is unverified. Manual cut is `/camera/scene`; the
  opt-in automatic cut at the over boundary is `graphics_camera_auto_cut`. Both go through
  `cutToScene()` (the one fast-path call site), and so does the panel's **Test the camera
  cut** (`cuttest.go`, POST/GET `/camera/cut_test`, login-gated, refuses while live): every
  camera in every scene incl. Replay, the cut and the 8s hold and back, both pictures still
  "playing" throughout, and OBS always put back on the scene it was on.
- **The control panel's Match-day checklist** (`ChecklistStatus()` / `ChecklistAction()`,
  `GET /checklist`, `POST /checklist/action`, both token-gated — they start OBS and go live,
  so NEVER the overlay's loopback carve-out). Every item is detected, none ticked by hand
  (the old localStorage ticks are gone). "Start OBS" runs `obs.Prepare` +
  `obs.SetupFromConfig(..., allowRestart=true)` in a background job and copies a
  freshly written WebSocket password from config.ini into state, or every later
  `obsCall` fails auth. `obsCall` returns None per REFUSED request — check `r[0]`,
  not just `r`, or a refusal reads as success (the first version did exactly that).
- **A brand-new OBS needs one restart before the replay buffer can run**, and
  `SetupFromConfig(..., allowRestart=true)` (the launcher, the checklist) now does it: on
  OBS setup's "wouldn't start yet", `obs.Close()` closes OBS the way a person
  would (WM_CLOSE / osascript quit; never a force-kill, so no crash marker) and
  `obs.Prepare` reopens it. Two things had to go for that to work, both modal on a fresh
  OBS: the Auto-Configuration Wizard (`SkipFirstRunWizard()`: `[General]
  FirstRun=true` in user.ini and global.ini) — it blocks the close — and the update box
  (paused, above). With an output running OBS asks "exit anyway?"; `obs.Close` gives up
  and CANCELS that question rather than leave it on screen. The standalone
  OBS setup never restarts OBS (allow_restart=False): whoever runs it by hand is
  managing OBS. Verified end to end on a fresh portable OBS: one Start OBS press, replay
  buffer running ten seconds later, no dialogs left.
- **Never test or switch OBS's encoder over the WebSocket.** OBS only builds encoders
  when it starts (or its settings dialog is applied), so `SetProfileParameter
  StreamEncoder` + a test recording measures whatever encoder was built at launch — the
  old Stream Health Check "hardware vs x264" comparison did exactly that and reported
  NVENC twice (OBS's log showed no x264 encoder ever created). `GetStats`'
  `outputTotalFrames`/`outputSkippedFrames` are pipeline-wide, not per recording, too.
  That check was REMOVED (2026-10-01). Encoder choice is now `obs.EnsureEncoder()`:
  OBS closed, reads the "Available Encoders" list from OBS's own newest log, and moves
  Simple-mode x264 (or unset) onto the best hardware H.264 encoder; never touches a
  hardware choice or Advanced mode; `[Stream] encoder = manual` opts out. Verified by
  OBS's log naming the encoder it actually ran, before and after.
- **A new bitrate only reaches the encoder when NO output is using it — and the replay
  buffer always is.** Simple mode re-reads `VBitrate` when an output starts with nothing
  else active; the replay buffer shares the stream's encoder and runs all match day, so
  the quality ladder's stop→set→start changed nothing (real OBS 33: set 2000 or 4000, it
  sent the 6000 it launched with; with the buffer stopped, 2014 and 4013). A step stops
  the buffer after the stream, and `replayBackNote` starts it AFTER the stream (so the
  stream builds the encoder); a recording in progress makes it refuse; `shifting` stops
  the replay trigger restarting the buffer mid-step. Judge any bitrate change by what OBS
  SENDS (`outputBytes` over time), never by reading the setting back — `quality-test` did
  the latter and passed. `internal/integration/quality_windows_test.go`
  (opt-in, throttled line → ffmpeg) measures it; the stress test
  (`streamquality_stress_test.go`) models it and checks the encoder's rate, not the
  setting. Also measured there: a stop on a congested line takes 30s+ (OBS flushes
  first), so a step waits a stop out rather than abandoning it; and with Dynamic Bitrate
  on, OBS holds a stream to a slow line itself (0 drops, congestion ~0.04), so the ladder
  rightly doesn't move. Don't judge a bitrate on a picture H.264 can't fit in it: the
  test's animated page (`motionPage`) needs 11-13 Mbps even at QP 51, so NVENC sent
  10-15 Mbps whatever it was set to (ffmpeg's NVENC too; x264 held the rate by wrecking
  the picture). Camera/game footage and heavy grain held to ±1% on NVENC, so the ladder
  check asserts on `QStill` and the looped `QClip` (`CRICKETSTREAM_QUALITY_CLIP` = a
  club's own footage), never `QMotion`.
- **The clubhouse screen's link lives in the WINDOW (`internal/app`), not the server.** It
  must answer before match day starts (to start it) and after it ends (to switch the
  laptop off), so it can't be a server route. Its rules, each with a test:
  - Everything but `/clubhouse/hello` and asking to pair needs the pairing key
    (`X-Clubhouse-Key`, constant time, a per-address and a global lockout). Asking
    (`ask.go`: `POST /clubhouse/ask`, `/clubhouse/ask/poll`) is keyless by design, so it
    may only ever put a request on the laptop's card: the LAPTOP picks the code (a
    requester choosing it could copy a real screen's), one request per address and
    `maxAsks` at once, the key handed only to the secret's holder, once, after
    `Link.Answer` — a press on the card. Don't let it return anything else. A request with an `Origin` header is
    refused outright: the screen is a program, so an Origin means a web page.
  - The server is reached only through `/clubhouse/server/...` on `proxyGET`/`proxyPOST`
    in `link.go`. **That allowlist is the security boundary** — the Pi's PIN is a UI lock.
    Never add `/live` (mutating) or `/commands` (pops the overlay's queue), settings,
    `/state` POST, the export, or anything that returns a secret. The proxy adds the
    window's own Bearer token and **`X-Forwarded-For`**, so the server's
    `trustedLoopback()` carve-outs (no login for the overlay's routes; a YouTube sign-in
    window popping up on the laptop) never apply to the clubhouse.
  - **Never stop match day from the screen while it's `starting`**: `App.stop()` in that
    phase QUITS the window (the launcher isn't listening yet). `chHost.StopMatchDay`
    refuses it.
  - Power-off goes through `chHost.ReadyToPowerOff` (match day over, no question waiting,
    OBS closed via `finishOBS`, which refuses while streaming/recording) and only if
    `[Clubhouse] allow_power_off`. Tests swap `clubhouse.runCmd` — **never** let a test
    reach the real `shutdown`/`systemctl poweroff`.
  - `[Clubhouse] key` is per-laptop: it's in `clubpack.configMachine`, so "Move to another
    laptop" never copies it (a laptop that arrives enabled without a key makes its own).
    So is `view_key`.
  - **Score-only screens** hold `[Clubhouse] view_key` (`view_enabled`), a second key the
    link checks in `keyOK`. It reaches only `serveView` (pairing, and a status trimmed to
    `viewStatus` — never the question or the activity log) and the proxy's `viewGET`
    reads; every start/stop/answer/power-off/woke and every POST is refused on the
    LAPTOP. Keep `viewGET` a subset of `proxyGET` with no operator reads in it. The Pi's
    `[Laptop] view_only` only decides what the page shows.
  - **The Pi image** (`scripts/clubhouse/build-pi-image.sh`, the `pi-image` job in
    build-executables.yml) is the official Raspberry Pi OS desktop image, SHA-256 checked,
    plus `scripts/clubhouse/image/`: everything system-wide (`/opt/cricketstream`,
    `/etc/xdg/autostart`), because the user doesn't exist until Raspberry Pi Imager's
    customisation or the first-boot wizard makes it. Never bake in an account, a
    password or a network. The autostart entry shares its file name with install-pi.sh's
    per-user one so the user's copy overrides it, and `clubhouse-start.sh` prefers
    `~/cricketstream/cricketstream` — re-running install-pi.sh is how an image updates.
    Two first-boot traps, both seen inside the trixie image: uid 1000 starts as a
    placeholder `pi` (no login shell) that first boot renames to the Imager's user, and
    with no Imager settings the desktop first signs in as `rpi-first-boot-wizard` to run
    piwiz. `cricketstream-autologin` waits out both (marking itself done only for a real
    account), and `clubhouse-start.sh` never opens over the wizard. Checked by building
    the image and chrooting into it (arm64 emulated), never booted on a Pi by CI.
  - The Pi page must be served by the Pi (the laptop is off when it's first touched); it
    talks only to its own `/api/` with a page key, never to the laptop directly. Its
    corner picture is `/preview.jpg` (cached 1.5s server-side, only fetched while shown)
    — deliberately not a second video encode on the streaming laptop.
- **Scene stacking: move cameras DOWN, never lift graphics UP.** `obs.CoveringPictures()`
  lists the picture sources (cameras, video, screen capture — `obs.PictureKinds`) above a
  graphic; OBS setup moves each, lowest first, to just beneath it, which keeps two cameras'
  order and leaves everything else (sponsor-logo images above the scorebar) where the club
  put it. The first version lifted the graphics to the top of the scene and buried a real
  club's logos under the scorebar.
- **`--startreplaybuffer` only once the scenes exist** (`obs.LaunchArgsFor()` checks the
  scene collection for an `Overlay` source). On an empty scene OBS won't start the buffer:
  it opens a modal "No Sources" question instead, which on a fresh install nobody is there
  to answer. First time through, OBS setup builds the scenes and starts the buffer
  over the WebSocket.
- **OBS's update check is paused for match day** (`PauseUpdateCheck()`: `[General]
  EnableAutoUpdates=false` in `global.ini`, original value kept in a
  `.cricketstream-updates-paused` marker in OBS's config folder). Measured: the "New update
  available" box on 4 of 6 default launches, 0 of 4 with it off. Restored by
  `ResumeUpdateCheck()` when the server's OBS guard sees a NORMAL close, or at
  the launcher's shutdown if OBS is already closed; a crash keeps it paused. A club that
  had it off already gets no marker and is never switched on. Tests that reach
  the launcher's shutdown must stub `_resume_obs_updates` — it reads the REAL OBS folder.
- **OBS puts every new source on TOP of its scene, at native size, top-left.** Before
  `arrangeCameraItem()`, adding a camera covered the Overlay in Main (no graphics) and
  ReplayClip in Replay (replays showed the live camera), and a 720p camera filled a
  quarter of the canvas — all confirmed on real OBS. Rules now: a camera in its HOME scene
  sits directly under the graphics; in any other scene it goes to the bottom (it's only
  there to stay active); new or untouched-default placements are fitted to the canvas
  with `OBS_BOUNDS_SCALE_INNER`, hand-placed ones are left alone. Home camera scenes get
  the Overlay added (the bowler-end scene was created without it, so the auto-cut hid the
  scorebar). OBS setup re-asserts Overlay/ReplayClip on top every run, which repairs
  installs the old code already broke, and fits ReplayClip (replays are recorded at the
  OUTPUT resolution, 720p by default). Any new code that adds a source to a scene must
  think about where OBS just put it.

## Conventions

- Match the surrounding style; don't reformat whole files.
- Comments explain *why*, not *what*. Keep upstream's why-comments when porting.
- Frame/JSON/file output must stay byte-identical to the Python original's — use
  `internal/pyfmt`/`pyval`, and add a differential test when porting something new.
- Prefer small, focused commits with present-tense messages ("Add X", not "added x").
- PlayCricket: BBCC `site_id`/`club_id` = `29434`. The API token is not club-specific.

## Useful diagnostics

- **Control panel → Setup → Run pre-match check** (`POST /precheck`, token-gated, 20s
  cooldown) — tries every credential and connection for real, in parallel with a time limit
  each: YouTube login + which channel, PlayCricket key, Anthropic key (free `models.list`),
  OBS WebSocket, scorer feed, replay disk space, OBS bitrate (from basic.ini when OBS is
  closed; the target is `targetBitrate()` — config.ini's rule, same as OBS setup), upload
  speed, auto quality (suggested on a tight upload; ⚠ if on while OBS is signed in to
  YouTube — `[Auth] Type` in the profile's basic.ini — where a quality step's stop ENDS
  the broadcast, which is why `stream_auto_downshift` stays off by default), cameras
  (TCP to the RTSP port; never echo the URL — it holds the password),
  surname clashes vs the roster, today's fixture, opposition badge, newer release,
  scorer's laptop/bridge, the Discord alerts webhook (asked about with a GET, never a post —
  this runs every six hours), and (with the clubhouse screen on) whether the screen can switch
  the laptop on (`clubhouse.CheckWake`/`WakeAdvice`: cable, wake-armed port, Fast Startup,
  auto sign-in, start at login, and the last wake the screen reported — ⚠ at worst). Read-only. Add new external dependencies to `Prechecks`; a check
  returns (ok|warn|bad|skip, detail, fix) and may raise — that's reported as `bad`, never a
  crash. Keep ✗ for things that are wrong NOW: the server re-runs it every 6 hours and a ✗
  puts a red badge in the panel's pinned bar, so "expected days before" states (cameras
  away from the ground, OBS closed) must be ⚠ or the badge is red all week.
- `http://localhost:5000/health` — feed freshness, photos, badges, AI key status, NV Play
  bridge connectivity (`pcs.bridge`), Mac/Windows CPU throttle state (`thermal`), and a
  pre-flight OBS bitrate sanity check (`obs_bitrate`) that flags a leftover downshift from a
  previous match before the operator goes live — see `obsBitrateSanityCheck()` — and
  OBS crash recovery (`obs_recovery`: watching, recent reopens, last event), and whether
  OBS's overlay is polling (`overlay`: seconds since its last `/live`, reloads).
- `http://localhost:5000/player/stats?name=SURNAME&debug=1` — which season record a name resolves to.
- `http://localhost:5000/h2h` — head to head with today's opposition (`h2h.go`): today's
  two TEAM ids from the fixture, meetings in the last 6 seasons' fixture lists, the last 5
  scorecards. Past seasons' lists and finished scorecards are cached for good in
  `.h2h_cache/`; `{"ready": false}` while it builds; `?force=1` (login) rebuilds.
  An innings' bowling list is the FIELDING side's — the first version would have credited
  their bowlers to us.
- `http://localhost:5000/lineups` — today's two XIs as entered on PlayCricket
  (match_detail `players`, kept 5 min), for the pre-match show. The show itself is the
  overlay's `showPreMatchInfo()` rotation: countdown (`cfg.match_time`, from the fixture),
  head to head (`graphics_h2h`), the XIs (`graphics_lineup`), and the weather card in the
  corner, which `renderScorebar` takes down at the first live frame.
- `http://localhost:5000/league/table` — today's competition's table (home club's row +
  the row above), backend for the overlay's `#league-table-panel` (`graphics_league_table`);
  `"usable": false` means `competition_id` is blank or doesn't resolve to a real league
  table (a cup/friendly fixture, most likely) — see the league-table fetch.
- `http://localhost:5000/data/status` — ball-by-ball DB status.
- `http://localhost:5000/highlights/status` — outcome of the last background highlights
  compile (clips are auto-tagged at replay time via the `clips` DB table; the reel gets
  captions + a chapters description file).
- `http://localhost:5000/obs/stream_check?force=1` (auth-required) — recommended bitrate and
  resolution from a real upload-speed test (the launcher applies them to OBS next run).
- `http://localhost:5000/sponsor/airtime` — today's sponsor airtime (`?date=YYYY-MM-DD`).
- `http://localhost:5000/stream/monitor` — live congestion/dropped-frame picture while
  streaming, plus the quality-ladder position. Two-tier adaptive quality: OBS's Dynamic
  Bitrate (enabled by OBS setup; seamless) + the sentinel's bitrate ladder
  (stop→reconfigure→start, ~5-10s gap; auto mode is the `stream_auto_downshift` state key,
  off by default). Auto steps back UP one rung only after 10 clean minutes that really
  carried the bitrate (`StreamClean`: Dynamic Bitrate can hold it lower silently), at a
  break in play (`playBreak`, fed by the overlay's /live), doubling the wait per failed
  step up. The ladder's place is also saved in the state (`stream_ladder`, cleared at
  step 0) so a server the launcher restarts mid-match picks it up (`resumeLadderLocked`,
  only while OBS is still at that bitrate and within 12h); in memory alone, a restart took
  the stepped-down bitrate as the full one for the rest of the day.
