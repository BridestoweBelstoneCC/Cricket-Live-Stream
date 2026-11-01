# CricketStream (Go) — Mac test report

**Date:** 2026-11-01
**Machine:** macOS (darwin/amd64), Go 1.27.2
**Repo:** https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream
**Branch/commit built:** `main` @ `b891324` (merge of `second-camera`)
**Note:** `main` is now at `2225199` (adds PCS Pro scoreboard screenshots to
`MAC_VM_SETUP.md`/`SETUP_GUIDE_MAC.md`, docs only — doesn't affect anything below)

This is the **Go port** (`cmd/cricketstream`), not the Python original.

---

## Build

```
go build -o dist/cricketstream ./cmd/cricketstream
go vet ./...
```

Both clean, no errors or warnings.

## Test suite

```
go test ./...
```

All packages pass **except** `cricketstream/internal/server`, which hit the default
10-minute `go test` timeout. Root cause: `TestPreviewDemoGraphicsMakeNoRequests`
(`internal/server/preview_browser_test.go`) drives headless Chrome
(`--headless=new`) to render the overlay preview and hung rather than completing or
skipping, on this Mac's installed Chrome. Every other test in the package, and every
other package, passed. Worth a look on a machine with a known-good headless Chrome
setup — not a regression in scoring/server logic, it's the one test that shells out to
a real browser.

## Runtime smoke test

Ran the actual binary, not just unit tests:

1. **First run / no WebView2 on macOS** — `./dist/cricketstream` (sandboxed
   `CRICKETSTREAM_HOME`) correctly fell back to opening the setup page in the default
   browser (`http://127.0.0.1:5098/`), per the documented macOS/Linux behaviour.
2. **Server** — `cricketstream server` started clean on the real default port **5000**
   (confirmed no AirPlay Receiver conflict on this Mac). `/health`, `/control`,
   `/overlay`, `/pcs/debug`, `/data/status`, `/lineups` all returned 200.
3. **Simulated PCS Pro feed** — `cricketstream simulate -scenario chase -configure
   -ball-seconds 0.4 -overs 20` wrote a live NV Play/PCS Pro-format scoreboard file and
   used `-configure` to point the running server at it over HTTP automatically.
   - `/live` and `/live/view` parsed it correctly ball-by-ball: batter names, strike
     rotation, bowler figures, fall-of-wicket card, run rate, wickets, overs remaining —
     all updated live.
   - `/health` → `pcs.fresh: true`, `pcs.file_found: true`, `demo_mode: false` once
     the simulator took over.
   - Opened the overlay in a real browser tab and confirmed it was actually polling
     `/live` (`overlay.last_poll_sec_ago: 3` in `/health`) — i.e. the real rendering
     pipeline worked end-to-end in a browser, not just the API.
4. **Graceful degradation** — with no PlayCricket API key configured:
   `/league/table` returned `{"ready": false}` rather than erroring, and the server
   logged `⚠ Club milestones: no PlayCricket key or home club id` instead of crashing.
   Both are documented, correct behaviour.
5. **No errors or panics** in the server log through the whole simulated run.
6. **CLI** — `version`, `licences`, `help` all work as expected.

## Known issue to follow up

- `TestPreviewDemoGraphicsMakeNoRequests` hangs against this Mac's Chrome instead of
  completing within its 90s-per-subtest budget (or cleanly skipping when Chrome isn't
  found, which is the documented fallback). Reproduce with:
  ```
  go test ./internal/server -run TestPreviewDemoGraphicsMakeNoRequests -v
  ```
  Worth checking Chrome version/flags compatibility, or whether something about this
  Mac's headless Chrome sandboxing is the cause (the test already works around one
  known headless-Chrome-on-macOS issue — the system keychain prompt — via
  `--use-mock-keychain`/`--password-store=basic`).

## Not covered

- No real OBS, NV Play/PCS Pro, or PlayCricket/Anthropic credentials were available in
  this environment, so the OBS integration suite (`internal/integration`, opt-in via
  `CRICKETSTREAM_OBS_PORTABLE`) and anything needing live PlayCricket/AI keys weren't
  exercised here.
