# v2.1 — Broadcast intelligence & your own data

This release builds on the v2.0 broadcast layer with match intelligence, a dataset of your
own, and easier setup for non-technical operators.

## Highlights

- **Ball-by-ball database.** Every delivery is logged to a local SQLite file (`match_data.db`)
  as you stream — your own season-long dataset. The current over is rewritten live so scorer
  edits and deletions are captured; completed overs freeze. A **Reconcile** button pulls
  PlayCricket's published scorecard as the authoritative record, and any match can be
  **exported to CSV**.
- **Result posts for any match.** A "Load results" picker pulls your recent PlayCricket
  results — home or away, streamed or not — and builds a polished Instagram result graphic,
  working out the result and your top batter and bowler straight from the scorecard.
- **Per-team & youth photo folders.** Backdrops come from `socials/1st`, `socials/2nd`,
  `socials/3rd` per team, falling back to the main folder. All age-group sides route to
  `socials/youth`, using club stock photos and a discreet first-name + initial for player
  names — a safeguarding-conscious default for juniors.
- **Match-day sponsors.** Every logo in `sponsors/` now appears on result posts, scaled to
  share the width — add a one-off sponsor by dropping in a file.
- **One-click camera.** Enter your camera's RTSP URL in the control panel and the overlay
  adds it to OBS as a media source for you, with auto-reconnect if the feed drops.

## Broadcast graphics

- **Auto-detected moments** — season-best scores and team milestones fire automatically on
  the over summary and are woven into the AI commentary.
- **"At this stage"** second-innings comparison against the first innings at the same point.
- **Full innings scorecard** at the break (requires the v2.1 scoreboard template).
- **Bowler spell tracker** on the over card.
- **DLS par pill** above the scorebar when rain is forecast (Standard Edition approximation).
- Animation polish: spring panel entries, sweeping boundary banners, milestone count-ups.

## Reliability & security

- Threaded server so a slow AI or PlayCricket call never freezes the overlay.
- Atomic state writes with a last-good fallback; resilient PCS file reads.
- Secrets are redacted from browser-facing responses; scorer-controlled names are escaped.
- A control-panel health strip and a quickstart pre-flight self-test.

## Control panel improvements (post-release patches)

- **Responsive control panel.** The panel stacks into a single column on narrow screens
  (≤ 768 px) and uses larger, touch-friendly buttons — so a phone operator on Wi-Fi can
  run the stream without pinching and zooming.
- **config.ini auto-seeding.** `server.py` now reads all sections of `config.ini` on
  startup (`[API]`, `[OBS]`, `[Scoring]`, `[Club]`, `[Stream]`) and pre-populates any
  fields in `match_state.json` that are still at their defaults. API keys, club name, kit
  colour, folder paths, and YouTube title template all load automatically — no need to
  re-enter them in the control panel after a fresh install.

## Upgrading from v2.0

1. Replace `server.py` and `overlay.html`.
2. **Deploy the v2.1 `bbcc_scoreboard.template`** to the scorer's NV Play / PCS Pro machine —
   required for the full innings scorecard and shirt-number features. Restart PCS Pro and
   re-select the scoreboard in Tools → Configuration.
3. Install Pillow if you haven't: `pip install Pillow` (used for social-post images).
4. Optional: create `socials/1st`, `socials/2nd`, `socials/3rd`, `socials/youth` and the
   `sponsors/` folder; set your camera's RTSP URL in the control panel.

See [`WHATS_NEW_V2.md`](WHATS_NEW_V2.md) for the full detail.

## Notes

- The ball-by-ball database and your live `match_state.json` are git-ignored — your data and
  API keys stay local.
- AI features (commentary, reports, captions) are optional and need an Anthropic API key;
  result posts work without one, using a template caption.
