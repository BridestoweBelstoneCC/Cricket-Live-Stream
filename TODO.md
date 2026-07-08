# TODO — codebase review findings (2026-07-06)

Full review of server.py, overlay.html, quickstart.py, setup_wizard.py, obs_setup.py,
scripts/, and CI. Work on `dev`; merge to `main` only once tested.

## Open

- [ ] **`obs_setup()` accepts `replay_folder` but never applies it to OBS** — quickstart
      passes it in and it's silently ignored. Setting it means touching the Simple-output
      FilePath (shared with recordings), so decide deliberately.

## Features added on dev (2026-07-07)

- [x] **Match simulator** (`simulate_match.py`): rehearse the entire broadcast without a
      scorer. Scenarios full/chase/century/collapse, `--configure`, `--chaos` failure
      injection, deterministic per seed; 17 tests feed its frames through the real parser.
- [x] **Auto-tagged highlights**: every replay clip is tagged at capture with why it fired
      plus the match context (a `clips` DB table); manually saved clips get a best-effort
      tag by mtime-correlation against the ball log. The compiler burns captions in as
      lower-thirds, skips replay-test clips, and writes a YouTube-ready
      `highlights_description.txt` with chapter timestamps; the panel now polls
      `/highlights/status` and shows the real outcome (it used to fire-and-forget).
      ⚠ ffmpeg isn't on this Mac — run one compile on the Windows streaming machine
      before Saturday to verify drawtext there.
- [x] **Bowler milestones**: five-wicket hauls (fires again for the 6th/7th) and
      hat-tricks on the milestone panel, in bowling red. The hat-trick chain understands
      cross-over hat-tricks, run outs (break, don't extend), chain-neutral wides/no-balls,
      and the over-completing wicket the cleared ticker never shows (wickets-delta
      fallback). Logic exercised in a real JS engine by tests/test_bowler_milestones.py.

## Features added on dev (2026-07-08)

- [x] **Manual scoring page (`/scoring`)** — the adoption-blocker killer: score ball-by-ball
      from a phone with no NV Play/PCS Pro, driving the full overlay/graphics/DB pipeline
      (manual frames render through the same PCS parser and outrank the file feed in /live).
      Shared `scoring_engine.py` extracted from the simulator; event-sourced with exact-replay
      undo; persists to manual_scoring.json (restart-safe); wicket-type/fielder/run-out-end
      pickers, per-over bowler picker, next-batter override, innings/target flow; same login
      as the panel; selectable as the data source in quickstart. 19 new tests.
      Known MVP limits: run outs don't record completed runs on the ball; changing the bowler
      mid-over doesn't re-attribute earlier balls (undo → set bowler → re-enter instead).
      TODO: a README section pitching this to non-NV Play clubs.

## Design warts fixed on dev (2026-07-08)

- [x] **`/live` split**: the overlay's `/live` poll drives the pipeline (events, ball DB,
      commentary) and consumes the event buffer; the panel's three pollers moved to a
      side-effect-free `GET /live/view`. Event-buffer pop/append now under a lock.
- [x] **Control panel extracted to `control.html`** — a plain file with normal JS escaping
      (backslash-doubling gotcha class eliminated), served with kit presets injected at
      request time; panel edits show on refresh without a server restart. Verified
      byte-identical to the old inline-string output. server.py is back to ~5,100 lines.
- [x] **Importing server.py no longer writes config.ini** — token generation moved to
      `_ensure_control_token()` (called at startup only), and `scripts/check_panel_js.py`
      now reads control.html from disk instead of importing server at all.

## Fixed on dev (2026-07-06) — verify live on Saturday before merging to main

- [x] **quickstart.py wiped panel-entered state on every run** (roster, sponsor fields,
      away colour, network-test cache, replay toggle edits; manual opposition when no
      fixture found). `build_state()` now merges over the existing file; match-day safety
      defaults (demo_mode/use_widget off) still forced.
- [x] **/live crashed on a non-numeric `home_club_id`** (set by the manual badge picker to
      a logo filename stem) — `int()` now guarded, falls back to 0.
- [x] **Weather was hardcoded to one ground** — `fetch_weather_data()` now prefers the
      `weather_lat`/`weather_lon` that `/match/fetch` saves; constants are fallback only.
- [x] **"Export CSV" 401'd when club_password was set** — panel now downloads via
      `apiFetch` + blob instead of `window.open` (which can't carry the Bearer header).
- [x] **Ball-event AI commentary trigger was dead** — `check_commentary_trigger` now runs
      *before* `buffer_pcs_events` advances `_prev_state`.
- [x] **Wicket events never buffered with commentary off** (found while fixing the above):
      `_prev_state` was only ever seeded inside the toggle-gated trigger, so with
      `graphics_commentary` off (the default) it stayed `None` all match — no wicket
      events, no fall-of-wickets in the match log. `buffer_pcs_events` now seeds it.
- [x] **Prematch "Scorer" never displayed** (overlay.html operator-precedence bug).
- [x] **`_persist_control_token` appended into the wrong section** when the
      `control_token =` line was missing — now inserts under `[Auth]`.
- [x] **`generate_social_graphic_facts` read `_match_log` unprotected** — both generators
      now share `match_log_snapshot_copy()`.
- [x] **Dead/duplicated `do_GET` routes removed** (GET `/commentary/over/generate` with its
      undefined `body`, GET `/commentary/test`, second `/pcs/debug`).
- [x] **Duplicate dict keys removed** (`drinks_over` in DEFAULT_STATE;
      `runsRequired`/`ballsRemaining` in `parse_pcs_json`, with the computed-from-target
      value folded in as a fallback).
- [x] **Duplicate DOM id `replay_enabled`** on the display-only centuries toggle removed.
- [x] Deleted `CLAUDE 2.md` (stale Finder duplicate of CLAUDE.md).
- [x] **Added an automated test suite** (`tests/`, stdlib unittest, ~80 tests, wired into
      CI which now also runs on `dev` pushes): parsing (ball tokens, PCS JSON incl. the
      innings latch, widget JSON), season-stats aggregation, session tokens + lockout,
      config token persistence, quickstart's state merge, JS↔Python classifyBall parity,
      and HTTP integration tests against a real in-process server (auth gating, secret
      redaction/sentinel round-trip, origin check, loopback carve-out, path traversal,
      and the /live PCS pipeline with event buffering + the ball-by-ball DB).
      Run: `python3 -m unittest discover -s tests`
