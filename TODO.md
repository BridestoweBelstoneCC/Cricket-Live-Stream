# TODO — codebase review findings (2026-07-06)

Full review of server.py, overlay.html, quickstart.py, setup_wizard.py, obs_setup.py,
scripts/, and CI. Work on `dev`; merge to `main` only once tested.

## Match-day critical

- [ ] **quickstart.py wipes panel-entered state on every run.** `build_state()` rebuilds
      match_state.json from scratch and only carries over the graphics toggles — `roster`
      (the shirt-number map, "enter once per season"), `sponsor_name`/`sponsor_id`,
      custom `away_colour`, and the cached network test are all lost each run. Merge over
      the existing state instead of replacing it.
- [ ] **/live crashes on a non-numeric `home_club_id`** (server.py `int(s.get("test_club_id")
      or s.get("home_club_id") or 0)`). The manual badge picker sets `home_club_id` to a
      logo *filename stem*, so picking a non-numeric filename 500s every `/live` poll and
      reload-loops the overlay. Guard the cast.
- [ ] **Weather is hardcoded to one ground.** `GROUND_LAT/GROUND_LON` are used
      unconditionally; the `weather_lat`/`weather_lon` that `/match/fetch` saves are never
      read. Any other club gets the wrong weather (and the DLS rain threshold keys off it).
- [ ] **"Export CSV" breaks when club_password is set.** `/data/export` is token-gated but
      the panel opens it via `window.open`, which can't send the Bearer header → 401.
      Fetch with `apiFetch` and download as a blob instead.

## Real bugs, lower impact

- [ ] **Ball-event AI commentary trigger is dead code.** In `/live`, `buffer_pcs_events()`
      advances `_prev_state` *before* `check_commentary_trigger()` reads it, so deltas are
      always zero and `graphics_commentary` / `record_event` context never fire. Reorder.
- [ ] **Prematch "Scorer" never displays** (overlay.html `showPreMatchInfo`):
      `cfg.scorer1_name || u1 && u2 ? '' : ''` — both ternary arms are `''`.
- [ ] **`_persist_control_token` appends into the wrong section** when `[Auth]` exists but
      the `control_token =` line was deleted — the appended line lands at EOF under
      `[Network]`, is never read back, and a new token is appended every restart.
- [ ] **`generate_social_graphic_facts` iterates `_match_log` unprotected** — unlike
      `generate_match_report`, no snapshot/retry, so a concurrent ball event can raise
      RuntimeError out of the handler. Share the report generator's snapshot helper.
- [ ] **Dead/duplicated routes in `do_GET`**: a GET `/commentary/over/generate` handler
      referencing the undefined `body` variable, a GET duplicate of `/commentary/test`,
      and a second unreachable `/pcs/debug` handler. Delete all three.
- [ ] **Duplicate dict keys**: `DEFAULT_STATE` defines `drinks_over` twice;
      `parse_pcs_json`'s state literal defines `runsRequired`/`ballsRemaining` twice
      (the first, computed-from-target values are silently discarded — fold the target
      fallback into the surviving key).
- [ ] **Duplicate DOM id `replay_enabled`** on the disabled "Replay on centuries" toggle
      in CONTROL_HTML — works only because getElementById returns the first match.

## Design warts (needs a decision, not just a patch)

- [ ] **`/live` is a mutating GET consumed by two clients.** The control panel polls it
      every 3s/10s/15s, so it randomly eats buffered wicket events meant for the overlay
      (today only affects player-card timing) and runs the DB logger / event detection on
      every consumer's cadence. There's also a small append-vs-clear race on
      `_event_buffer` under the threaded server. Consider: only pop events for the overlay
      (e.g. `?consume=1`), or move panel polling to a read-only endpoint.
- [ ] **`obs_setup()` accepts `replay_folder` but never applies it to OBS** — quickstart
      passes it in and it's silently ignored. Setting it means touching the Simple-output
      FilePath (shared with recordings), so decide deliberately.
- [ ] **Importing server.py has side effects** — `scripts/check_panel_js.py` imports it to
      read CONTROL_HTML, and `_load_auth_config()` can *write* a generated control_token
      into config.ini during what should be a read-only syntax check. Guard the persist.
- [ ] **CONTROL_HTML is ~2,100 lines of the ~7,000-line server.py.** Consider extracting
      the panel to its own file served from disk (would also simplify the JS check and
      kill the backslash-escaping gotcha class). CLAUDE.md's "~5000 lines" note is stale.

## Done

- [x] Delete `CLAUDE 2.md` (stale Finder duplicate of CLAUDE.md, 3 Jul vintage).
