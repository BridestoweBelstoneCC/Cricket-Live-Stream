# Porting CricketStream from Python to Go

Source of truth for the port: <https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream>
at commit `0813fcf72c8e0554244744d977f2b211b3620a46` (2026-10-17). A reference clone lives in
`.upstream/` (git-ignored). To recreate it in a new session:

    git clone https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream .upstream
    git -C .upstream checkout 0813fcf72c8e0554244744d977f2b211b3620a46

Toolchain: Go 1.27 (portable install at `C:\Users\Boreas\sdk\go\bin`). Python 3.11 is also on
the machine, which is handy for differential tests against the original (`py -3 ...`).

## Product goals (from the user)

1. Rewrite in Go (chosen over C: HTTP/JSON/TLS/WebSocket/SQLite are all stdlib or pure-Go).
2. **One downloadable app with in-app setup.**
   - One binary: `CricketStream.exe` / `CricketStream` (macOS universal). No Python, no pip,
     no setup.bat, no second exe for the scorer's laptop.
   - Every asset is `go:embed`ded (overlay/control/scoring HTML, fonts, brand, scoreboard
     template). A file of the same name beside the exe overrides the embedded copy, which
     keeps upstream's "edit the panel, refresh, no restart" workflow.
   - First run (no `config.ini`) starts the server and opens the browser at `/setup`, a web
     version of `setup_wizard.py`'s interview. Its first question picks the machine's role:
     **streaming laptop** (full server), **scorer's laptop on the same wifi** (scorer agent),
     or **scorer's laptop elsewhere** (NV Play bridge). The role is saved in `config.ini`,
     so later runs go straight into that mode.
   - Data and config live beside the exe (portable install, same as upstream's
     "relative to its own folder").
   - Subcommands remain for automation/CI: `cricketstream server|agent|bridge|simulate|
     obs-setup|telemetry|...`.

## Layout

    cmd/cricketstream/     the single binary (role dispatch + subcommands)
    assets/                go:embed of the HTML/fonts/template (+ disk override)
    internal/pyfmt/        Python-compatible number formatting helpers
    internal/scoring/      scoring_engine.py
    internal/winpredict/   win_predictor.py
    internal/matchpage/    match_page.py
    internal/resultcard/   result_card.py
    internal/logobg/       logo_bg.py
    internal/socialclips/  social_clips.py
    internal/voice/        voice.py
    internal/agent/        scorer_agent.py
    internal/bridge/       nvplay_bridge.py
    internal/simulate/     simulate_match.py
    internal/obs/          obs_prep.py + obs_setup.py + the WebSocket client
    internal/server/       server.py (split into files by concern)
    internal/setup/        setup_wizard.py + quickstart.py + cricketstream.py, web-based
    internal/tools/        stream_telemetry, camera_encoder, refresh_cam, stream_quality_test

## Status

| Python module | Lines | Go package | Status |
|---|---:|---|---|
| scoring_engine.py | 375 | internal/scoring | **done** (engine + session; HTTP tests pending server) |
| win_predictor.py | 149 | internal/winpredict | **done** |
| match_page.py | 133 | internal/matchpage | **done** |
| voice.py | 65 | internal/voice | **done** |
| social_clips.py | 118 | internal/socialclips | **done** |
| logo_bg.py | 144 | internal/logobg | **done** |
| result_card.py | 397 | internal/resultcard | **done** |
| nvplay_bridge.py | 158 | internal/bridge | **done** (`cricketstream bridge`) |
| scorer_agent.py | 712 | internal/agent | **done** (`cricketstream agent`); folder finder shared in internal/scorerfile |
| simulate_match.py | 332 | internal/simulate | **done** (`cricketstream simulate`; parser-consistency tests move to the feed parser) |
| obs_prep.py | 997 | internal/obs | **done** (+ `CRICKETSTREAM_OBS_PORTABLE` safe test mode) |
| obs_setup.py | 502 | internal/obs | **done** (`cricketstream obs-setup`; tested against a fake OBS in internal/obsws/obswstest) |
| server.py | 9347 | internal/server | **done** (`cricketstream server`); all server tests ported (JS tests run in node) |
| setup_wizard.py / quickstart.py / cricketstream.py | 1767 | internal/setup + internal/launcher | **done** (web `/setup`, role dispatch, `start`) |
| stream_telemetry / camera_encoder / refresh_cam / stream_quality_test | 1092 | internal/tools | **done** (`telemetry`, `camera-encoder`, `refresh-cam`, `quality-test`) |
| scripts/*.py | ~550 | scripts/makebrand, scripts/renderscorebar; compile/JS checks are go vet + page tests | **done** |
| tests/*.py (~670 tests) | 8575 | `_test.go` beside each package | **done** — audited test by test (see below) |
| CI + build-executables workflows | | .github/workflows | **done** (3-OS tests, -race, differential job, release builds + smoke tests) |
| docs | | README, guides, ARCHITECTURE, CLAUDE.md, CHANGELOG | **done** for the Go app |

## Shared helpers already built

- `internal/pyval` — Python truthiness/str()/int() on decoded JSON
- `internal/pyfmt` — Python round()/str(float)/f"{:.nf}"
- `internal/htmlesc` — Python html.escape byte-for-byte
- `internal/ini` — configparser-compatible INI (incl. `%%` escaping)
- `internal/proc` — run external programs with timeout / below-normal priority / no window
- `internal/console` — Die()/Pause()/Input(), CRICKETSTREAM_NO_PAUSE, UTF-8 console
- `internal/assets` — embedded files with app-folder override; `assets.Dir()` is the app folder
- `internal/scorerfile` — find the scorer's file, NV Play folder detection, Python-style text read

- `internal/obsws` (+ `obswstest` fake OBS) — OBS WebSocket v5, per-call and held-open
- `internal/ai` — the one Claude client (official Go SDK, `claude-haiku-4-5` as upstream)

## server.py → internal/server (plan; tick as each lands)

Package-level state mirrors the Python module globals (tests patch them the same way).
Upstream line ranges refer to the pinned commit.

| Go file | Python lines | Concern | Status |
|---|---|---|---|
| auth.go | 78–228 | control token, HMAC sessions, login lockout, auth log | done |
| remote.go | 229–346 | LAN/Tailscale IPs, Cloudflare tunnel, QR, rate limits | done |
| commentary.go | 347–476, 811–905 | AI test commentary, match log, over commentary | done |
| db.go | 477–810 | SQLite schema, classify ball, ticker, ball logger, reconcile, CSV | done |
| weather.go, commands.go | 906–957 | overlay command queue, Open-Meteo | done |
| youtube.go | 958–1174 | YouTube Data API (OAuth token file compatible with google-auth) | done |
| resultsvault.go | 1175–1617 | ResultsVault/PlayCricket widget live feed, event buffer | done |
| state.go | 1618–1895 | DEFAULT_STATE, atomic save, redaction, seed from config | done |
| pcs.go | 1896–2070, 3900–4038 | scorer file read + parse_pcs_json (innings latch) | done |
| agentclient.go | 2071–2350 | discover/read scorer agent, read_score_source | done |
| bridgesync.go | 2351–2418 | NV Play bridge mirror loop | done |
| matchfacts.go | 2419–2712 | follow_match_id, live_* figures, match facts, AI report | done |
| roster.go | 2713–2822 | socials folders, roster, name keys | done |
| season.go | 2823–3156 | PlayCricket season aggregation + cache | done |
| league.go | 3158–3256 | league table | done |
| watchdog.go | 3257–3488 | error recorder, self metrics, thermal, watchdog | done |
| cards.go | 3489–3899 | result-card facts (PlayCricket + live) | done |
| manual.go | 4039–4369 | /scoring session glue (engine already in internal/scoring) | done |
| fixture.go | 4370–4477 | today's fixture, kit presets | done |
| obsops.go | 4478–5306 | replay trigger, add camera, clip tagging, highlights plan | done |
| streamquality.go | 5307–5571 | congestion sentinel + bitrate ladder | done |
| airtime.go | 5572–5765 | sponsor airtime + viewer-minutes | done |
| spotter.go, voice glue | 5766–5954 | feed status, camera spotter, spoken commentary | done |
| matchpage.go, winpred.go | 5955–6179 | match page build, win prediction | done |
| precheck.go, checklist.go | 6180–6743 | pre-match check, match-day checklist | done |
| sponsor.go | 6744–6903 | logo upload, transparent copies, variants | done |
| obsguard.go | 6904–7196 | OBS crash/freeze recovery | done |
| streamcheck.go | 7197–7294 | upload test, bitrate sanity | done |
| clips.go | 7295–7545 | control.html serving, social clips, highlights compile | done |
| handler.go, handler_post.go | 7546–9243 | every HTTP route | done |
| run.go | 9244–end | entry point (`cricketstream server`) | done |

## Next session: start here

The port is complete: every module, script and test, CI, docs. Beyond the original:
settings page (/settings), full stream preview with demo graphics on the setup/settings
page, Check buttons, "Your files" folder buttons, desktop shortcut (Windows), practice
match from the control panel (internal/server/rehearsal.go), auto-opening control panel,
second-start detection, panic guard, `cricketstream version`.

The app window (2026-10-18, internal/app): the double-click opens CricketStream's own
window (WebView2 via go-webview2, pure Go; browser fallback elsewhere) with "which computer
is this?", setup in the window, the match-day screen, the control panel shown in the window
logged in, agent and bridge status screens, the console's prompts as window questions,
an activity log + logs/ file, close-asks-first, single instance (port 5098). Windows
release builds are -H windowsgui. A native Mac window (webview_go needs cgo on a macOS
runner) is the obvious next step. The OBS sandbox guard (obs.PIDs/Running/Kill/Close
filtered to the portable copy; obsws.Guard; CRICKETSTREAM_OBS_WS_PORT) came out of a
manual test that reached the maintainer's open OBS — see CLAUDE.md. After it, all three
portable-OBS integration tests passed again (prep/setup/replay/tools, the whole launcher,
crash recovery; 2026-10-18 09:35-09:38) with no file in the machine's own OBS folder
created or modified. Then match day from the window against the portable OBS: OBS opened
and configured from Start match day, the control panel in the window showed OBS and the
replay buffer ready, a practice match drove the scorebar inside OBS (a six, its instant
replay, saved as a real .mp4), and Stop put the score source back — the server's clean
stdin stop, where Windows used to kill it.

Tests beyond the original's: binary end-to-end (cmd/cricketstream/e2e_*: every role,
setup -> match day -> Ctrl+C, agent and bridge feeding a server), portable-OBS
integration (internal/integration: prep/setup/replay/tools, the whole launcher, crash
recovery), headless-Chrome preview test (no mutating requests), Python differentials for
the helper packages. 734 tests pass on Windows; the suite is clean under -race on Linux.

Test audit (2026-10-18): every upstream test was matched to a Go test (or found to be
Python-packaging-only: find_python, .bat/.sh launchers, PyInstaller). Missing ones were
ported; differential tests then found and fixed three output differences (pyfmt str(float)
exponent form, pyval JSON floats, UTF-8 replacement in scorerfile.DecodeText).

Still open:
1. Test on the CS-CleanTest VirtualBox VM (Windows 11, snapshot "02-with-obs" has OBS
   32.2.2, Guest Additions installed): copy dist/CricketStream.exe in with
   `VBoxManage guestcontrol ... copyto`, run first-run setup, match day against the VM's
   OBS. Needs the guest login from the user.
2. A real NV Play folder (agent/bridge against live NV Play output).
3. Code signing (SmartScreen/Gatekeeper prompts) — needs certificates; documented instead.
4. Unexplained: during the first portable-OBS run on 2026-10-17 23:48 the machine's own
   OBS basic.ini mtime changed; two exact re-runs didn't reproduce it, and the
   integration test now fails if the real OBS folder changes. Backup of the file after the
   event is in the session scratchpad (real-obs-backup/).

## Porting rules

- Behaviour is ported, including every gotcha in upstream `CLAUDE.md`; port the test that
  guards each one alongside the code.
- Frame/JSON strings must be byte-identical to Python's output (see `internal/pyfmt`).
- Keep upstream's comments that explain *why*; drop ones that only describe Python mechanics.
