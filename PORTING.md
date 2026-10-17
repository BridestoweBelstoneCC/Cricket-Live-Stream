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
| simulate_match.py | 332 | internal/simulate | todo |
| obs_prep.py | 997 | internal/obs | todo |
| obs_setup.py | 502 | internal/obs | todo |
| server.py | 9347 | internal/server | todo |
| setup_wizard.py / quickstart.py / cricketstream.py | 1767 | internal/setup | todo (becomes web setup) |
| stream_telemetry / camera_encoder / refresh_cam / stream_quality_test | 1092 | internal/tools | todo |
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

## Porting rules

- Behaviour is ported, including every gotcha in upstream `CLAUDE.md`; port the test that
  guards each one alongside the code.
- Frame/JSON strings must be byte-identical to Python's output (see `internal/pyfmt`).
- Keep upstream's comments that explain *why*; drop ones that only describe Python mechanics.
