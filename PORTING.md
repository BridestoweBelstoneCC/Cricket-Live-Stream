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
| scripts/*.py | ~550 | scripts → Go tests / tools | todo |
| tests/*.py (~670 tests) | 8575 | `_test.go` beside each package | ported per package |
| CI + build-executables workflows | | .github/workflows | todo (go test, go build, one artifact) |

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

`internal/server` compiles, vets and passes its tests (http, auth, parsing, scoring HTTP,
sponsor upload/airtime, checklist, stream quality, OBS guard, precheck, YouTube, season,
league, match report, highlights, spotter, soak, camera layout). Test seams are package
vars (obsCall, guard*, precheck*, checklist*, ytAPI, loginSleep...). Harness:
`harness_test.go` (sandbox/newTestServer/serve/raw requests).

Remaining, in order:
1. Server parts of tests: test_result_card, test_match_page, test_social_clips, test_voice,
   test_pcs_detect (template install + setup question), JS tests (test_js_parity,
   test_bowler_milestones, test_camera_autocut, test_scorebar_styles, test_panel_layout —
   run JS via node if present, skip otherwise).
2. Launcher (cricketstream.py + quickstart.py): default command = role dispatch from
   config.ini; streaming role = obs prep → server (with restart loop) → obs setup →
   telemetry → shutdown report/airtime. Web `/setup` (setup_wizard.py interview) on first run.
3. Tools: stream_telemetry, camera_encoder, refresh_cam, stream_quality_test.
4. scripts/, CI (.github/workflows: go vet, go test, -race on Linux, build matrix), README.
5. Verify against a PORTABLE OBS (CRICKETSTREAM_OBS_PORTABLE) and a real NV Play folder.

## Porting rules

- Behaviour is ported, including every gotcha in upstream `CLAUDE.md`; port the test that
  guards each one alongside the code.
- Frame/JSON strings must be byte-identical to Python's output (see `internal/pyfmt`).
- Keep upstream's comments that explain *why*; drop ones that only describe Python mechanics.
