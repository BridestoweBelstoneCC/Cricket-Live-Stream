# Changelog

All notable changes to CricketStream Overlay are documented here, most recent first.

---

## Unreleased

- **New: AI camera spotter.** Match day → Cameras → **AI camera spotter**. While the stream
  is live, Claude Haiku looks at each camera every few minutes (5 by default) and warns —
  in the Cameras card and with a red camera badge in the panel's top bar — about a fogged,
  wet or dirty lens, a knocked camera, glare, a picture that's too dark or blocked, or a
  black/test picture. A frozen picture is caught without AI. Off by default because it uses
  the club's AI credit: about 10–15p for a 5-hour match. "Check cameras now" tries it any
  time. Tested on fogged and tilted match photos (both caught) and a normal one (no alarm).
- **New: social clips.** After the match, After the match → **Make social clips** turns
  every wicket, six, four and milestone replay into a vertical video for YouTube Shorts,
  Instagram Reels and TikTok: the replay (with the scorebar) in the middle, a big headline
  ("SIX!"), a line about the moment, the club badge and your handle — plus a caption ready
  to paste, written by Claude Haiku (under 1p a match; without an AI key the replay's own
  tag is used). Download each from the panel, phone included. Made after the match on
  purpose: encoding video while streaming would compete with OBS. Needs FFmpeg.

## v2.11 — 2026-10-02

- **New: pre-match check.** Control panel → Setup → **Run pre-match check**, meant for a
  few days before a match. It tries every login and connection for real — YouTube (and
  that it's signed in to the club's channel), the PlayCricket key, the AI key, OBS, the
  scorer feed and the disk space for replays — all at once, and answers ✓ / ⚠ / ✗ with
  what to do. Nothing is changed. Built after a YouTube login on a club laptop turned out
  to have expired with no sign until it was needed. It also checks:
  - **a quality downshift left in OBS** from a previous match (it bit twice last season),
    read from OBS's settings file so it works days before with OBS closed;
  - **the YouTube login's age** — a warning before a Testing-mode login's 7 days run out;
  - **the cameras** answer on the network (⚠, not ✗, away from the ground);
  - **players sharing a surname** whose season stats won't show until the Squad Roster
    has their shirt numbers;
  - **today's fixture** on PlayCricket and **the opposition's badge**;
  - **upload speed** against the stream bitrate, and how old that test is;
  - **a newer version** on GitHub;
  - **the scorer's laptop or NV Play bridge**, for two-laptop setups.
  The server also runs it by itself shortly after starting and every 6 hours; a red
  "✗ N problems" badge appears in the panel's pinned bar when anything needs fixing.
- **The camera buttons are in the panel's pinned bar.** Wide and Bowler-end now sit next to
  Save on every tab (when a bowler-end camera is set up), instead of only in a card below
  the fold on the Match day tab. Cutting cameras is the most frequent thing an operator
  does during play.

## v2.10 — 2026-10-02

- **Fixed: an expired YouTube login now says so.** Google rejects a stored YouTube login
  once it's revoked, or after 7 days if the club's Google Cloud app is still in "Testing"
  mode. That used to crash every YouTube call with an error, so title updates (and now
  viewer-minutes) quietly stopped. It now says the login has expired and how to reconnect:
  press "Update YouTube broadcast now" on the streaming laptop, which opens the Google
  login. A network blip is no longer mistaken for an expired login, so it never pops a
  login up mid-match. To stop the weekly expiry, set the app to "In production" in Google
  Cloud (OAuth consent screen).
- **New: sponsor viewer-minutes.** While the stream is live and YouTube is connected, the
  server reads YouTube's live viewer count once a minute, and the sponsor airtime line adds
  an estimate: "…on screen for 4 min 30 s of the live stream, across 31 appearances — an
  estimated 1,240 viewer-minutes". Seconds on screen × people watching at the time. Only
  the live broadcast is ever counted, a count more than 3 minutes old is never used, and if
  only part of the airtime had a count the line says so rather than scaling it up. Shows in
  the control panel and in the file quickstart saves after the match. Uses about 300 of
  YouTube's 10,000 free daily API units for a long match.
- **Fixed: a sponsor no longer appears twice on the Instagram result card.** Making a
  logo's background transparent keeps the original (so Undo works), and the card showed
  every logo in the folder — original and copy. Copies are now recorded against their
  original, and the card shows one per sponsor: the one in use, else the newest copy.
- **Removed: the per-ball AI commentary lower third.** It had no switch in the control
  panel (the end-of-over AI commentary panel replaced it), but its trigger still ran on
  every update. The panel's commentary preview now shows the latest end-of-over line as it
  goes on air, and "Generate test commentary" waits for the real answer instead of
  sometimes showing "Generating..." for good.

## v2.9 — 2026-10-02

- **New: a much better post-match Instagram graphic.** Rebuilt from scratch in the style
  of county and league result cards: the photo in full colour fading into a club-colour
  panel, a huge WIN / DEFEAT with the margin beside it, both clubs' badges with big scores
  (the winner bright, the other dimmed), and the top batter and bowler as big stat numbers.
  Set in Barlow Condensed (free SIL Open Font License, bundled in `fonts/`), so it looks the
  same on every laptop. Without a photo it uses the club colour and a large crest watermark.
  - Fixed on the old card: the key performers were hidden under the sponsor strip, long
    results ran off the edge, and the footer said "Home CC".
  - The result is PlayCricket's own, so DLS wins ("WIN ON DLS"), conceded matches, ties,
    draws and abandonments are right, and pairs (softball) matches are won by runs.
  - The streamed-match card no longer asks the AI for its numbers — they come from the
    PlayCricket scorecard if it's up, else the ball-by-ball log. Only the caption uses AI.
    If the scorer's team names don't identify which side is ours, the card shows the
    scores without claiming a win or defeat.

- **Rehearsals now behave like the real NV Play feed at the end of an over.** The match
  simulator used to clear the ball ticker when an over completed; real NV Play keeps
  showing it until the next ball. That gap is why the ball logger's missing-final-ball bug
  never showed up in a rehearsal. `--clearing-ticker` keeps the old behaviour available.
- **Fixed: a match no longer splits in two when its id changes mid-innings** — pressing
  "Fetch today's match" after the first ball, or correcting the opposition name during
  play. The data logged so far moves with it (an earlier rehearsal never does), so the
  report, result card and CSV export cover the whole match.
- The ball logger also copes if NV Play ever does show an over's final ball in the ticker:
  it's logged once, in its own over.
- **Fixed (found in a code review):**
  - The ball-by-ball database was missing the last ball of most overs, and at the end of
    each innings kept a copy of the final over under an over that was never bowled. NV
    Play keeps showing a finished over until the next ball, and the logger expected it
    to clear. Ball counts, CSV exports and per-over figures are now complete.
  - The match report and result card now use the scorer's own running figures (saved as
    the match goes, so they survive a restart) for scores, top scorers, bowling figures
    and who was out. The ball-by-ball log put every ball of an over against one batter.
  - The result card no longer declares a result mid-chase when PlayCricket's live
    scorecard has no result yet, never claims a WIN or DEFEAT when it can't tell which
    side is the club's, and ends a chase on the match's real overs limit rather than the
    panel's default of 50.
  - Conceded matches with no innings on PlayCricket, and pairs (softball) innings with 10+
    wickets, show correctly.
  - "Fetch today's match" set the opposition to PlayCricket's team label ("1st XI")
    instead of the club name, which then showed on the overlay and in the report.
- **Fixed (found in a full match-day rehearsal):**
  - The match report, social post and Instagram graphic no longer make up a result after
    the server restarts. They were written from a match log held only in memory, so after
    a restart (including quickstart's automatic one after a crash) the AI was handed just
    the two team names and wrote a confident "victory" anyway. The facts are now rebuilt
    from the ball-by-ball database (innings totals, wickets, top scorers, fifties), and with
    no match data at all the panel says so instead of generating anything.
  - "Show player cards" now shows them even with the automatic "Player card on new batter"
    toggle off. It used to report "showing" while nothing appeared.
  - The Stream Health "Check now" button works straight after opening the panel. The panel's
    own read of the cached result was starting the 5-minute cooldown.
  - The match report and the social post have separate cooldowns, so making one no longer
    blocks the other for 2 minutes.
  - "Reconcile latest" explains a match that isn't linked to a PlayCricket fixture instead of
    showing "HTTP Error 404".

- **New: nothing to switch on in OBS.** Quickstart now gets OBS ready itself, before
  anything else: it switches on OBS's WebSocket server with a random password (and writes
  that into `config.ini`), turns on the replay buffer and points it at the replay folder,
  then opens OBS. This removes three manual steps from first-time setup: switching on the
  WebSocket server and copying its password into setup, restarting OBS once before replays
  would work, and having to open OBS before running quickstart. The setup wizard no
  longer asks for an OBS password, and offers a default replay folder.
  - OBS's settings files can only be changed while OBS is closed (it saves over them on
    exit), so if OBS is open with its WebSocket server off, quickstart asks you to close it
    and carries on. A WebSocket server that's already set up is left alone and its
    password reused.
  - After a crash, a power cut or a force-close, OBS is reopened without stopping on its
    "Run in Safe Mode?" prompt, which nobody is there to answer on a match day and which,
    if Safe Mode is picked, switches off the WebSocket server. OBS marks an unclean exit
    with a file in its `.sentinel` folder, and quickstart clears it before opening OBS.
    An OBS that's already open but not answering (sat on that prompt, or running in Safe
    Mode) is no longer reported as ready; the operator is asked to close it so it can be
    reopened properly.
  - `[OBS] manage_obs = no` in `config.ini` turns all of this off, for OBS on another
    computer or a hand-tuned setup.
  - Verified against a real OBS 32.1.1 (a separate portable copy, so no real install was
    touched): a never-opened OBS came up with the WebSocket server on and accepting the
    password; the replay buffer was running at launch with no restart and saved a clip to
    the replay folder. For Safe Mode, the check is OBS's own log, not whether the WebSocket
    answers, since a person clicking the prompt makes it answer too. Across five
    force-kill-and-relaunch cycles with the marker cleared, OBS logged no crash and no
    prompt; with it left in place, it prompted every time. OBS's own
    `--disable-shutdown-check` option, which looks like the obvious fix, does nothing on
    OBS 32. Not yet tried on a Mac.
- **New: OBS is reopened automatically if it crashes mid-match.** The server checks on OBS
  every 5 seconds. If OBS crashes (or is force-closed), it reopens it without the Safe
  Mode prompt, switches back to the Main scene, and restarts the stream if it was live
  when OBS went down. It never goes on air by itself if the stream wasn't already live.
  - Closing OBS normally (at the end of the match, say) leaves it closed. OBS's own crash
    marker tells the two apart, so this isn't a guess.
  - It only looks after an OBS it has seen running this session, gives up after 3 crashes
    in 30 minutes (a crash loop needs a person), and is off with `manage_obs = no`. Status
    is in `/health` under `obs_recovery`.
  - Verified against a real OBS 32.1.1 (portable copy): force-killed on the Replay scene,
    it was back in about 6 seconds on Main with the replay buffer running, and OBS's log
    showed no crash prompt. Closed normally, it stayed closed. Restarting the stream
    itself was only tested with OBS's responses mocked, since a test stream needs
    somewhere to stream to.
- **New: a frozen OBS is closed and reopened too (Windows).** If OBS stops responding for
  a minute and the stream isn't getting out, it's closed and goes through the same
  recovery as a crash. A frozen OBS *window* over a stream that's still sending is left
  alone, with a warning: OBS encodes separately from its window, so viewers may not
  notice anything wrong, and closing OBS would stop the stream.
  - Verified on a real OBS 32.1.1 frozen by suspending its process: detected, closed and
    reopened, responsive again with no crash prompt. A dialog box sitting open (OBS's
    own crash prompt, in that test) was correctly not treated as a freeze.
  - Windows' own "Not Responding" check turned out to be unreliable for this: it only
    notices once something is waiting on the window, so a frozen OBS that nobody touches
    went unnoticed for two minutes. The check now pings OBS's window directly.
  - Not on Mac or Linux, which have no equivalent check; there, only crashes are handled.
- **New: OBS's video settings and bitrate are set for you.** Every time quickstart opens
  OBS it sets the canvas to 1920×1080 (the size the overlay is drawn at; a fresh OBS
  sizes its canvas to the laptop's screen, which put the graphics in the wrong place on
  anything else), and the output resolution, frame rate and bitrate from your last
  upload-speed test: the same recommendation the Stream Health Check already showed but
  nothing applied. 720p at 30fps until there's been a test. `[Stream] output_resolution`,
  `fps` and `bitrate_kbps` in `config.ini` pin your own values; `manual` leaves OBS alone.
  - **Changed:** a blank `bitrate_kbps` used to mean "leave OBS's bitrate alone". It now
    means "use the recommendation", which also undoes a leftover downshift from the last
    match for clubs that never filled it in. `bitrate_kbps = manual` keeps the old
    behaviour.
  - Verified on a real OBS 32.1.1: 720p before a speed test, 1080p after a good one, 25
    and 30fps both applied, and a 1366×768 canvas corrected to 1920×1080. The first
    attempt at the frame rate wrote a value OBS accepted, saved, and then ignored (it ran
    at 30fps when told 25); it now uses the setting OBS actually honours.
  - The encoder is still chosen by hand (or by OBS's own setup wizard); the Stream Health
    Check says which works best.
- **Fixed: adding a camera hid the graphics and broke replays.** OBS puts a newly added
  source on top of everything in its scene, at its own size in the top-left corner. So the
  control panel's "Add camera to OBS" put the camera over the scorebar in Main and over the
  replay clip in Replay (a replay showed the live camera instead), and a 720p camera filled
  only a quarter of the picture. Unless someone had rearranged OBS by hand, a club
  following the setup steps got no graphics and no replays. Now:
  - cameras fill the frame and sit under the graphics, and quickstart puts the graphics
    back on top every match day, which also repairs a setup the old code already broke;
  - the bowler-end camera's scene gets the scorebar too (it was created with cameras only,
    so cutting to it at the end of an over hid the scorebar);
  - replay clips fill the frame (they're recorded at the stream's resolution, 720p by
    default, so they played in a corner);
  - a camera someone has positioned by hand (picture-in-picture, say) is left where it is.
  - Verified on a real OBS 32.1.1 with test videos standing in for cameras, including
    screenshots of each scene and the two-camera setup, which had never been run against a
    real OBS before. Not yet with two live RTSP cameras.
- **New: NV Play's output folder and template are set up for you.**
  - Setup finds NV Play's output folder itself (including when Documents lives in
    OneDrive, the Windows 11 default on many laptops) and asks you to confirm it, instead
    of asking you to type the path.
  - The scoreboard template no longer has to be copied into NV Play's Templates folder by
    hand. Setup does it when NV Play is on the same laptop, and the scorer agent does it on
    the scoring laptop: `CricketStreamScorerAgent.exe` now carries the template inside it.
    An older copy is kept as `scoreboard.template.old`. Choosing the template inside NV
    Play is still a one-time step for the scorer.
  - Verified by building the agent exe and running it, on its own in an empty folder,
    against a fake scorer's laptop: installed an exact copy, left it alone the second time,
    and replaced an older one keeping the original. The release build now checks the
    template really is inside the exe.
- **Fixed: the Stream Health Check could leave a test clip in your recordings folder**, and
  its second test (the x264 comparison) was usually skipped as "a recording was already
  active". OBS reports a recording stopped before it has finished closing the file; the
  check now waits for it.
- **Removed: the Stream Health Check's encoder comparison.** Testing it against a real OBS
  showed it never compared anything: changing the encoder setting over OBS's remote
  control doesn't switch the encoder OBS uses (OBS only builds encoders when it starts),
  so its "hardware vs x264" result was the same encoder measured twice. The upload-speed
  test, and the bitrate and resolution it recommends, stay.
- **New: quickstart picks OBS's video encoder.** If OBS is on CPU encoding (x264) and the
  laptop has a hardware encoder (NVIDIA, AMD, Intel or Apple), quickstart switches to it,
  using the list of encoders OBS itself reports as working on that laptop. A hardware
  encoder you've chosen is never changed; `[Stream] encoder = manual` turns it off.
  Verified with OBS's own log naming the encoder it actually ran: x264 before, NVENC after.
- **New: no "New update available" box during matches.** Quickstart pauses OBS's update
  check for the match and puts it back when OBS is closed afterwards (a crash keeps it
  paused, so the box can't appear when OBS is reopened mid-match). A club that had it off
  already is left off. Measured on OBS 32.1.1: the box appeared on 4 of 6 launches
  normally, and none of 4 with the check paused.
- **Fixed: a fresh OBS stopped on a "No Sources" question at first launch.** Starting the
  replay buffer from the command line before the scenes exist makes OBS ask whether to
  carry on; nobody's there to answer. The buffer is now started that way only once the
  scenes have been built (first time, the setup script starts it after building them).
- **New: the control panel's checklist does the steps, not just lists them.** The
  Match-day checklist at the top of the panel now ticks itself from what's actually true,
  and every item that isn't ready has a button that does it: **Start OBS** (the same
  preparation quickstart does, then the scenes and overlay), **Add camera to OBS** /
  **Reconnect**, **Start replay buffer**, **Fetch today's match**, **Find scorer laptop**,
  and **Go live** (asks first). The old manual tick-boxes are gone, along with items that
  could never be false ("server.py is running"). Works from a phone too, so OBS can be
  started without being at the laptop.
  - Verified in a real browser against a freshly installed OBS: one press of Start OBS,
    and about ten seconds later OBS was open, set up, and holding replays, with no
    dialogs left on screen.
- **New: a brand-new OBS no longer needs restarting by hand.** The replay buffer can't
  run until OBS has restarted once after it's switched on; quickstart and Start OBS now
  close OBS cleanly and reopen it themselves, the first time only. OBS's own first-run
  Auto-Configuration Wizard is skipped (it's another window nobody answers, and it
  stopped OBS closing); everything it sets is set by CricketStream already.
- **Fixed (before release): match-day setup moved sponsor logos under the scorebar.** An
  unreleased version of the "graphics above the camera" repair lifted the scorebar to
  the very top of the scene. It now moves only cameras and other video down beneath the
  graphics, leaving anything placed above the scorebar where it was.
- **New: upload the sponsor's logo from the control panel.** "Upload logo…" in the
  sponsor section sends an image from whatever device the panel is open on (a phone at the
  ground, say) to the streaming laptop, saves it under the next free image number, and
  makes it the sponsor image straight away, with a preview. PNG, JPEG, WebP or GIF up to
  8 MB, checked by what the file actually is rather than its name. No more copying files
  into the sponsors/ folder by hand.
- **New: a refreshed control panel.** Organised by when you need things, in four tabs —
  **Match day** (today's match, cameras, replays, on-screen extras, stream quality, system
  health, the live data feed, YouTube), **Graphics & sponsor**, **After the match** (report,
  Instagram graphic, highlights, match data) and **Setup** (OBS, cameras, scoring source,
  accounts and keys, health check, folders, squad roster) — instead of one 6,800-pixel page.
  The live status and the Save button are pinned to the top of every tab, and the
  checklist stays above the tabs. A new look throughout: readable hint text (much of it was
  too dim), consistent cards and controls, and the accent colour taken from your club's
  home kit colour, adjusted so it always reads on the dark panel (a navy club gets a
  readable club blue; a yellow club gets dark text on its buttons). Works at phone width:
  the pinned bar stays one line and the tabs fit. Your last tab is remembered per device.
  Every control and setting is where the panel's code expects it — checked by a new test
  that every saved field and every button handler survived the move.
- **Fixed: the scorebar style preview was always blank.** The overlay draws the scorebar
  at the bottom of a full 1080-pixel canvas and the preview frame was only 72 pixels tall,
  so it showed the empty top strip. It now shows the real scorebar in the chosen style.
- **New: make a sponsor logo's background transparent.** Most logos arrive on a white box,
  which shows as a hard rectangle on the stream. "Make background transparent…" in the
  sponsor section (offered automatically after an upload, when the logo has a plain
  background) shows two versions over the strap's dark background and you pick the one
  that looks right: **Around the logo** (for badges and round logos: keeps white inside
  them) or **Everywhere** (for lettering: clears the insides of letters too). It saves a new
  image and keeps the original, with an Undo. Works on solid-colour backgrounds; a photo or
  gradient is refused with an explanation, as is a logo that's already transparent.
  Checked on a real club's sponsor logos: a gold wordmark and a round badge, both on white.
- **Fixed: saving settings from two places at once could fail.** Every save writes
  through one temporary file, and two at the same moment collided on Windows ("Permission
  denied"), dropping one request — found when two logo uploads landed together. Saves are
  now one at a time, and also ride out Windows briefly refusing to replace the settings
  file while it's being read.
- **New: sponsor airtime.** How long the weekend sponsor's strap was actually on screen
  while the stream was live, ready to pass on to the sponsor: shown in the control
  panel's sponsor card, printed and saved by quickstart after the match
  (`sponsor_airtime_<date>.txt`, and below the match report if you make one), and at
  `/sponsor/airtime`. Timed by the server from the overlay's show/hide reports; time on
  screen before going live (a rehearsal) is shown separately and never counted. The
  server-side timing is tested end to end; the overlay's reporting is checked in its
  source but hasn't yet been watched in OBS during a real match.
- **Fixed: the scorer agent could pick an old scoreboard folder over the live one.** Its
  "prefer the folder being written right now" check ignored the file's age for the
  standard NV Play filenames, so a folder holding last season's scoreboard counted as live.

## v2.8 — 2026-09-24

- **Changed: camera cuts are ~3.6x quicker, and the control panel is built for flicking.**
  `/camera/scene` went through `_obs_call`, which opens a connection, waits for Hello,
  authenticates and sends Identify before every single request — four round trips of setup
  per cut. That's the right trade for the health checks and bitrate ladder it was written
  for (seconds or minutes apart), and the wrong one for switching between camera angles
  while following the play. Measured against a live OBS 32.2.2: **31.9 ms median per cut,
  36 ms worst case, against 8.8 ms / 16.6 ms** once one socket is held open.
  - New `_obs_fast_call()` keeps a single OBS socket open, used **only** by camera cuts;
    everything else still uses the per-call connection, which is deliberate.
  - The robustness that connection-per-call was protecting is kept a different way —
    reconnect on any error — and was verified rather than assumed: killing OBS mid-session
    returns cleanly in ~2s with no hang, and once OBS is back the next cut reconnects by
    itself in 63 ms and returns to ~14 ms. An early version retried *connect* failures too,
    which made "OBS not running" take ~14s and timed out an HTTP test; it now retries only
    a socket it was reusing, since a refused connection means OBS simply isn't there.
  - Control panel: the live angle is highlighted and the highlight moves **on click**, not
    on the server's reply, so the panel never feels laggy; **press `C`** to flick between
    angles without aiming at a button (ignored while typing); and rapid presses coalesce
    rather than queue, so mashing the key can't leave OBS working through a backlog of
    stale switches after you've stopped.
- **Docs: OBS Safe Mode disables the WebSocket server.** If OBS didn't shut down cleanly it
  offers Safe Mode on startup, which turns WebSockets off — so replays, scene switching and
  auto-setup all stop working, with "cannot connect to OBS" as the only symptom. Now called
  out in both setup guides' troubleshooting. Found by killing OBS in the test VM.

- **Changed: one download per machine, and the host one does everything.** The streaming
  laptop needed two executables — `CricketStreamSetup.exe` to configure, then
  `CricketStreamQuickstart.exe` on match day, which had to be placed next to `quickstart.py`
  by hand and gave no readable error when it wasn't. Now there is **`CricketStream.exe`**
  (`CricketStream.command` on Mac): one file, run the same way every time, which works out
  for itself what still needs doing —
  *in the project folder? → Python installed? → packages installed? → `config.ini` exists?
  → start the match.* Satisfied steps are skipped silently, so the first run configures and
  every later run goes straight to the match. New `cricketstream.py`; it implements none of
  those steps itself, calling the `setup_wizard.py` function that already did each job, so
  `setup.bat`/`setup.sh` keep working against the same code.
  - Dependency checking is new: it asks the interpreter that will actually run the server
    which packages it can import, and pip-installs only if some are missing. (The first
    version of that probe used `import importlib` and silently reported "nothing missing"
    forever — `importlib.util` is a submodule and needs importing by name. Now pinned by a
    test.)
  - **The scorer's laptop was already a single self-contained exe** —
    `CricketStreamScorerAgent.exe` freezes `scorer_agent.py` whole, which is stdlib-only, so
    that machine needs no Python, no project folder and no config. It gained the same
    never-exit-silently treatment (a taken port used to close the window with the reason in
    it) and the console-codepage fix, so its em-dashes stop arriving as `â€”`.
  - `quickstart_launcher.py` is deleted, superseded by `cricketstream.py`. The build workflow
    is renamed `build-executables.yml` and now smoke-tests each frozen binary in CI — a bad
    freeze fails the build rather than a club's match morning.
  - **Both exes were built and run locally on Windows before this shipped**, which the
    previous attempt at this could never do: verified out-of-project (readable error, exit 1),
    in-project (skips setup, hands over, passes arguments through), and dropped in the
    `Windows\` sub-folder (finds the project one level up).

- **Added: live scorebar-style preview in the control panel, and the setup wizard now asks
  which one you want.** The picker existed but you chose blind from a text dropdown whose
  hint pointed at an `examples/` folder you had to go find in Explorer — and a first-time
  user following the setup guide was never told the styles existed at all. The Graphics card
  now embeds the **real overlay** in an iframe via a new `?preview=1&style=<name>` mode,
  scaled to fit, using your own team names and colours, updating as you change the dropdown
  and before you save. It's the actual CSS rather than a mock-up that could drift.
  Preview mode renders exactly ONE static frame and starts neither timer: `/live` is a
  mutating GET and `GET /commands` pops the queue, so a preview left open mid-match would
  otherwise eat the OBS overlay's events and commands. Verified by queueing a command,
  leaving a preview open for 12s and confirming the command survived.
  `scorebar_style` is now also written to `config.ini` (new `[Graphics]` section, documented
  in `config.example.ini`) and seeded into state like the other settings.
- **Docs:** README, both setup guides, `ARCHITECTURE.md` and `CLAUDE.md` brought up to date
  for this release's work — the four scorebar styles and where to pick them, the render
  script, the preview-mode rule alongside the existing `/live` single-consumer gotcha, the
  installer's never-exit-silently rule, and the test count (216 → ~250).

- **Fixed: the installer closed its own window before you could read the error.** Reported
  as "run it in the wrong place and it auto-exits without an error you can read" — two
  separate causes, both fixed.
  - **Nothing paused before exiting.** A double-clicked `.exe` owns its console window, so
    every `sys.exit()` in `setup_wizard.py` (= `CricketStreamSetup.exe`) closed the window
    with the reason inside it. That included unhandled exceptions — the traceback flashed
    past for a few frames, which is both the least useful thing to show someone and exactly
    the text needed to diagnose it. Every fatal path in `setup_wizard.py`, `quickstart.py`
    and `quickstart_launcher.py` now goes through a `die()` that prints a framed, plain-English
    problem + what to do about it, and waits. Crashes are caught and held on screen with a
    link to report them. `CRICKETSTREAM_NO_PAUSE=1` switches the pauses off for automation;
    `quickstart.py` also only pauses when there's a real console attached, so the wizard's
    own "launch the server now" subprocess and the test suite never block.
  - **Three of the four Windows launchers could never have worked as shipped.**
    `Windows/quickstart.bat`, `install.bat` and `start_server.bat` did `cd /d %~dp0` (the
    `Windows\` folder) and then looked for `quickstart.py` / `requirements.txt` / `server.py`,
    which live in the repo root — so they failed 100% of the time with `can't open file` or
    `Could not open requirements file`. Same bug in `Mac/quickstart.sh`, `install.sh` and
    `start_server.sh`. They all now search their own folder then the parent, the way
    `start_scorer_agent.bat` already did, and say plainly which folders they looked in when
    they still can't find it.
  - **`CricketStreamSetup.exe` dropped in the wrong folder** failed deep inside pip and
    vanished; worse, one that got past that wrote `config.ini` next to itself, where
    `server.py` — which only ever reads `config.ini` from its own folder — would never find
    it. It now locates the project (own folder, then parent, then cwd), reports the folder
    it settled on, and refuses to half-configure an install nobody can use.
  - **Ordering:** `quickstart.py` asked two interactive questions about today's match before
    checking `config.ini` existed, so a first-timer answered both and was then told setup had
    never been run. Checked upfront now.
  - **Mojibake:** reconfiguring Python's streams to UTF-8 only fixes half the problem on
    Windows — the console still renders through its own codepage, so the banner and the
    ✓/⚠/✗ icons arrived as `â€”` on a default cp850/cp1252 console. Both entry points now set
    the console codepage too, which is the only half available to the frozen `.exe` (it has
    no `.bat` wrapper to run `chcp 65001` for it), and the `.bat` files set it as well.
  - New `tests/test_installer.py` guards all of it: every launcher searching the parent
    folder, every launcher pausing, no bare fatal `sys.exit(1)` outside `die()`, a crash
    handler in all three entry points, and an end-to-end run of the wizard from a wrong
    folder asserting it exits non-zero with an explanation and no traceback.

- **Changed: "Modern" scorebar rebuilt, plus two new styles ("Impact", "Minimal").** The
  first Modern was Classic's layout with each segment floated as its own rounded dark pill,
  which read as a row of unrelated buttons with a hole punched through the middle (because
  `#seg-spacer` went transparent). All three alternate styles now follow the rule Classic
  got right: the bar is ONE object, divided by hairlines rather than gaps, with the spacer
  part of the bar. Score is the hero everywhere (40–42px against 19px batter names) instead
  of barely larger than the detail around it.
  - **Modern** — dark broadcast slab, team-colour rule along the top edge, on-strike batter
    highlighted with a team-colour edge and a lift out of the bar.
  - **Impact** — the loud one, T20-broadcast style: angled clip-path cuts, a white score
    plate, heavier type, thick team-colour footer. Reads best on a phone.
  - **Minimal** — clean white sheet, no dividers at all, structure from type weight and
    space. The only light option besides Classic; sits better under a bright daytime picture.
  - `applyColours()` now publishes `--bat`/`--bowl`/`--bat-lift`/`--bowl-lift` on `#scorebar`,
    so styles can carry team identity past the two end blocks without more per-element JS.
    The `-lift` pair runs through the existing luminance-aware `wormColour()` — a club colour
    like a near-black navy is invisible as a 3px rule on a dark bar, which is the same problem
    the run-rate worm already solved rather than a new one.
  - `renderScorebar()` marks the on-strike segment with a `striker` class (not just the 15px
    icon inside it), so a style can highlight the whole block. Done in JS rather than CSS
    `:has()` on purpose — OBS 30 still ships a CEF build without it.
  - Classic is untouched and remains the default.
- **Added: `scripts/render_scorebar.py`** — renders every scorebar style to `examples/` in
  headless Chrome/Edge with fixed mock data, no server or npm packages needed. The scorebar
  failure mode is always visual and never a syntax error, so none of it showed up in the
  existing checks; this pass found two real bugs that way (Minimal's bowler name was
  white-on-white because `applyColours()` sets `color:#fff` inline, and Impact's full-width
  footer rule painted straight over the striker/bowler underlines). New
  `tests/test_scorebar_styles.py` covers the part that *can* run without a browser: the
  style list in overlay.html's `SCOREBAR_STYLES`, its CSS blocks, and the control panel's
  picker all having to agree, plus the shared 72px bar height that the graphic panels line
  up against.

- **Added: automatic camera cut at the over boundary — opt-in, UNTESTED against real
  two-camera hardware.** New `graphics_camera_auto_cut` toggle (control panel, defaults
  off — a camera cut is a real, visible on-air action, same reasoning as
  `stream_auto_downshift`); a no-op unless a second (bowler-end) camera is also configured.
  Keys off the same over-transition signal `overlay.html` already detects for
  over-summary/partnership: cuts to the bowler-end scene, then back to the main scene ~8s
  later, via the existing `/camera/scene` endpoint (now loopback-trusted for the overlay,
  same carve-out `/replay` uses). Deliberately skips itself around an instant replay: never
  cuts while a replay could still be on screen, and stands down its own revert-to-main if a
  replay starts during the bowler-end window, so the two scene transitions never fight each
  other. New `tests/test_camera_autocut.py` (real JS execution, same approach as the
  bowler-milestone tests) covers both gates and all three replay-timing cases. **Not
  independently verified:** no OBS/second camera was reachable from this machine — confirm
  the actual cut behaves correctly on a real two-camera setup before relying on it match
  day, same caveat as the manual-cut backend this builds on. See TODO.md.

- **Added: scorebar style picker — Classic (existing look) or Modern (dark glass, rounded
  segments).** CSS-only skin toggled by `scorebar_style` in the control panel's Graphics
  card, applied instantly on the overlay's next poll (`body.style-modern` in
  `overlay.html`). Deliberately scoped to the scorebar only, not the graphic panels — see
  TODO.md's note on keeping alternate styles restrained so future scorebar features don't
  have to be built twice.
- **Added: bigger new-batter player card.** The photo (72px → 132px) and text (name 28px →
  42px, stats 22px → 30px) are noticeably more legible, scoped to `#player-card`/
  `#player-card-right` specifically so the shared `.pc-headshot`/`.pc-name` classes the
  pregame form-guide panels also use are unaffected.
- **Added: league-table context graphic (`/league/table` + overlay panel).** Given a
  competition_id for today's match, fetches PlayCricket's `league_table.json` (cached
  once/day, same pattern as season stats) and returns the home club's row plus the row
  above it. Verified against BBCC's real Division 1 table. `competition_id` is now
  captured alongside `competition_name` wherever `fetch_todays_match()` runs
  (`quickstart.py` and `server.py`'s `/match/fetch`). The overlay panel (`#league-table-panel`
  in `overlay.html`, new `graphics_league_table` toggle in the control panel's Graphics
  card, defaults on) shows position/points/played and "a win today moves them above
  X" — once pre-match in the dead-time rotation alongside the season-form panels, and once
  more per innings in the live end-of-over rotation (not every over — the table can't
  change mid-match, and showing it every over would crowd out over-summary/partnership).
  Cleanly skips itself when `usable: false` (cup/friendly fixture with no real table) or
  when the daily fetch hasn't completed yet. New pure-logic tests in
  `tests/test_league_table.py` cover `league_table_home_row()`'s name matching and the
  "wrong table returned instead of erroring" defensive case, plus `fetch_league_table()`'s
  no-competition_id/API-failure/cache branches.
- **Added: second-camera (bowler-end) support — backend only, UNTESTED against real
  hardware.** `obs_add_camera()` now takes `extra_scenes` and auto-creates a missing target
  scene, so a second camera's scene (e.g. "Main-Bowler") no longer has to be created by
  hand in OBS first; both cameras end up placed in Main, the bowler-end scene, and Replay,
  matching the existing camera-source gotcha generalized across more scenes. New control
  panel fields (bowler-end camera URL/name/scene) and two buttons: "Add bowler-end camera
  to OBS" and manual cut buttons ("Cut to bowler-end" / "Cut to wide angle") calling a new
  `/camera/scene` endpoint — the same `SetCurrentProgramScene` call `/replay` already uses.
  No automatic cut at the over boundary yet (see TODO.md). This machine has no second
  camera to test against — confirm scene creation and cross-presence on a real two-camera
  setup before relying on it match day.
- **Fixed: a bitrate downshift from a previous match's quality ladder silently carried over
  into the next one.** The stream-quality ladder writes `VBitrate` straight into the OBS
  profile on disk when it downshifts for congestion, and that value persists after the
  server exits — only the ladder's own step counter is in-memory and resets to 0 on restart.
  Nothing ever put the bitrate back, so a match that ended on a downshifted step handed the
  *next* match day a silently-crippled stream from the first ball. This has now bitten twice
  for real (`diagnostics/STREAM_FREEZE_2026-08-15.md`, `diagnostics/SEASON_END_2026-09-19.md`
  — both a leftover 875 kbps against a >2,700 kbps recommendation). `/health`'s pre-flight
  sanity check already flagged this, but flagging it wasn't enough — the manual checklist
  item didn't catch it a second time. `obs_setup()` now resets `SimpleOutput/VBitrate` to a
  configured baseline (`bitrate_kbps` in `config.ini`'s `[Stream]` section) every time it
  runs, i.e. every `quickstart.py` start — undoing whatever the last match's ladder left
  behind, regardless of when that happened. Skipped while a stream is actually live, and
  only applies in Simple output mode, same guards as the existing stream-key setup step.
- **Fixed: `certifi` missing from the active Python environment despite being listed in
  `requirements.txt`**, causing real `CERTIFICATE_VERIFY_FAILED` errors on the PlayCricket
  season-stats fetch (also found in `diagnostics/SEASON_END_2026-09-19.md`). `server.py`'s
  own certifi patch silently falls back to system certs when the import fails, which masked
  the gap instead of surfacing it. Re-installed properly against the Python `server.py`
  actually runs under on this machine.

---

## v2.7.3 — 2026-09-19

- **Fixed: the one-click "add camera" setup (`obs_add_camera()`) could silently drop the
  camera source from Replay, and could fail outright on a re-run.** Found while wiring up a
  real new camera: it removed the existing input before recreating it, but a real
  two-machine OBS test showed `RemoveInput` doesn't fully delete a source that's still
  referenced by a scene item in ANOTHER scene — it just detaches one reference, leaving the
  input in a broken state where even a fresh `CreateSceneItem` fails ("Failed to create the
  scene item") until every remaining reference is individually removed. It also only ever
  placed the camera in one scene, contradicting this project's own camera-source gotcha
  (a source left out of even one live scene deactivates and drifts on reconnect). Re-running
  it now updates the existing source's settings in place instead of removing it, and ensures
  it's present in both the main and replay scenes — verified against real OBS, including the
  exact re-run-after-multi-scene-placement case that broke before.

---

## v2.7.2 — 2026-09-19

- **Fixed: a Windows console (or any redirected/piped stdout) could crash the request thread
  that was mid-response, the moment this project's own console output printed a checkmark or
  arrow.** `server.py` and every other top-level script use characters like `✓`/`✗`/`→`
  throughout their console output; a console that isn't a genuine UTF-8 terminal often
  reports a legacy single-byte codepage instead, and `print()` on one of those characters
  raised `UnicodeEncodeError` right there in the thread doing the printing — inside an
  HTTP request handler, that took the in-flight response down with it. Found by 14 automated
  HTTP tests failing with `RemoteDisconnected` on this exact Windows setup. Every top-level
  script with any non-ASCII console output now force-reconfigures `stdout`/`stderr` to UTF-8
  (replacing anything that still can't be represented) right after its imports, best-effort
  and never allowed to block startup.

---

## v2.7.1 — 2026-09-19

*A thin launcher for match day, a crash-recovery gap found by deliberately trying to break
it, a broadened CI compile check after nine of thirteen top-level scripts turned out to have
zero syntax coverage, and a friendlier recovery when `scorer_agent.py` can't find the
scoreboard folder on its own.*

- **`scorer_agent.py` now ships as a standalone Windows exe** (`CricketStreamScorerAgent.exe`),
  same rationale as the setup wizard's own — the scoring laptop is often a club's spare
  machine with no Python installed. No Mac build: NV Play doesn't run natively on Mac, so a
  "Mac scoring laptop" isn't a real scenario.
- **A thin exe for match day too** (`quickstart_launcher.py` → `CricketStreamQuickstart.exe`):
  finds the Python the setup wizard already installed and runs `quickstart.py` exactly as
  `quickstart.bat` does today — a nicer double-click experience with zero change to the
  actual match-day code path. An earlier attempt froze `server.py` and `quickstart.py`
  themselves into standalone exes with no Python needed at all — it worked, but was reverted
  in favour of this simpler, lower-risk version once it turned out the machine was never
  going to be Python-free anyway (the setup wizard installs it regardless).
- **Fixed: a crashed server took the whole launcher down with it, no recovery.** Found by a
  real resilience test against the new launcher: `quickstart.py` had no restart loop at all
  — an unhandled exception, OOM, anything, and the match-day session was simply over.
  `quickstart.py` now retries up to 3 times with a short backoff before giving up, logging
  each attempt. Verified with real kill tests, including on real Windows hardware, and
  (once extracted into its own function for testability) 3 new unit tests covering the
  restart-then-give-up, immediate-Ctrl+C, and Ctrl+C-after-a-restart paths.
- **`stream_quality_test.py`** automates the manual "does a quality shift survive a real
  broadcast" test — watches the stream through the ~5-10s reconfigure gap and confirms it
  actually stayed live, rather than trusting the API call succeeded. Deliberately never
  starts or stops the stream itself; that stays the operator's call.
- **CI's compile check now covers all 13 top-level scripts, not just `server.py`.** Nine of
  them — `scorer_agent.py`, `setup_wizard.py`, and every diagnostic tool among them — had no
  syntax coverage at all, incidental or otherwise; a broken standalone script would go green
  in CI and only surface when someone actually tried to run it. New
  `scripts/compile_check_all.py`, plus two more regression tests pinning behavior found live
  this run: the connection-backlog fix staying above the stdlib default, and that undoing
  right after a bowler pick reverts the pick, not the previous ball (correct by design, easy
  to assume otherwise).
- **The non-technical setup guide now offers a Claude-assisted path.**
  `FOR_NON_TECHNICAL_USERS.md`'s new Option A walks a volunteer through the whole install
  conversationally via Claude Code, alongside the existing written Option B walkthrough.
- **Fixed: `scorer_agent.py` gave up instead of asking, when it couldn't find the
  scoreboard folder.** If auto-detection failed, it printed instructions to re-run from the
  command line with the path as an argument and exited — awkward for the exe build, which
  most scoring laptops are launched by double-click, with no easy way to add an argument.
  It now prompts for the folder path right there in the console window, validates it, and
  remembers it for next time, the same as a folder found any other way.

---

## v2.7 — 2026-08-19

*Two real match-day failures drove most of this: a streaming Mac overheating and crashing
the scorer's VM mid-match (two independent ways to run NV Play off the streaming machine,
plus an early-warning thermal check), and an afternoon broadcast stuck at a third of its
intended bitrate with nothing in the panel showing it (a pre-flight bitrate sanity check).
Camera/telemetry tooling and CI hardening came out of the same run of match days. Both
separate-hardware options were verified tonight on genuine two-machine hardware, not just
simulated on one laptop.*

- **NV Play can now run on separate hardware from the streaming machine — two ways.**
  `nvplay_bridge.py` is a standalone, stdlib-only script for the scorer's machine, for when
  the two machines AREN'T on the same network — it serves NV Play's output file over HTTP,
  gated by a token, reached over Tailscale; the server mirrors it into a local cache every
  ~2s, at which point it's an ordinary local file to `/live`, `/health`, the watchdog, and AI
  commentary, all unchanged. Configure it from the control panel's new "NV Play on separate
  hardware" fields (URL + token) instead of the PCS output folder.
  `scorer_agent.py` is the simpler alternative for two laptops already on the SAME club wifi
  — no address or token to type, found automatically via a UDP broadcast. Verified tonight
  against a real second machine: held the last known score gracefully when the agent was
  deliberately stopped mid-match (simulating the crash that started all this), then
  auto-recovered on restart with no re-pairing needed. One real gap found and worked around:
  Windows Firewall can allow the HTTP port while still silently blocking the UDP discovery
  broadcast — the control panel's manual `host:port` field is the documented fallback.
  See `TWO_LAPTOP_SETUP.md` (agent) and `BRIDGE.md` (bridge) for setup guides.
- **Fixed: the server could reset connections under a burst of simultaneous pollers.**
  Found by load-testing the two-laptop dry run — the stdlib `ThreadingHTTPServer` default
  connection backlog of 5 was too small once the overlay, control panel, `/scoring`, and a
  post-wifi-blip reconnect all landed at once (every request here is a fresh HTTP/1.0
  connection, no keep-alive). Raised to 64; verified clean at 300 requests, concurrency 50.
- **`/health` now reports Mac thermal throttling before it becomes a crash.** A new `thermal`
  block reads `pmset -g therm` — macOS's own throttle signal, and a better one than a raw
  temperature since it fires the moment the OS starts limiting CPU speed for heat, ahead of
  any crash. The watchdog logs a warning on the transition into throttling, and the control
  panel's health strip gets a red "Mac" indicator.
- **Match-day diagnostic and camera tooling**, written while commissioning a Reolink RTSP
  camera over a season: `stream_telemetry.py` passively logs OBS/server/scorer-feed samples
  to a CSV for post-match analysis (now auto-started by `quickstart.py`), plus an opt-in
  headroom probe that measures spare upload capacity passive monitoring can't see;
  `camera_encoder.py` reads the camera's own encoder settings, since OBS re-encoding a
  low-bitrate camera source can't recover detail that was never captured; `refresh_cam.py`
  reloads an OBS media source on a timer to stop long RTSP sessions drifting out of sync.
  None are imported by `server.py`.
- **A leftover downshifted bitrate can no longer hide.** A real 15 August broadcast ran an
  entire match at 875 kbps instead of 2500 — invisible in the panel, because the quality
  ladder's downshift persists in the OBS profile across a server restart while its own step
  counter resets to 0. `/health`'s new `obs_bitrate` check compares OBS's currently
  configured bitrate against the last network test's recommendation and flags a mismatch
  before the operator goes live, surfaced as a new "Bitrate" dot in the health strip.
- **Fixed: the scorebar's over ticker lagged a full over behind.** It stayed on the
  finished over's balls until the first delivery of the next one, disagreeing with the
  end-of-over graphics for the whole gap between overs — the clear was gated on NV Play's
  ticker field going empty, which a full match's captured feed showed doesn't reliably
  happen (801 of 857 over-completions still carried the stale ticker). Now keyed off the
  over boundary itself.
- **CI now runs on Windows as well as Ubuntu.** Every Windows-only bug fixed recently
  (a Unix-only import crashing `/health`, a certifi failure in quickstart, two cp1252
  encoding faults) was invisible to CI before this, despite Windows being the primary
  target platform. Four such failures fixed alongside the matrix change.
- **`.gitignore` hardened against secret leaks via backup files.** Entries like `config.ini`
  only matched the bare filename, so a hand-made backup (`config.ini.bak`,
  `match_data.db.bak-20260801`) fell straight through — closed by globbing every
  secret-holding entry. Also ignores the scorer's live output if the output folder ever
  points at the repo directory, and the `diagnostics/` folder the tools above write to.

---

## v2.6.1 — 2026-07-10

*Ten bugs found by a deep code review of everything shipped between v2.4 and v2.6, all
confirmed against the code and fixed with regression tests (suite now 211 tests). Verified
end-to-end with a full simulator rehearsal against the live server.*

- **Over-final wickets were credited to the wrong bowler.** On the write that completes an
  over, NV Play has already rotated `bowler` to the next over's bowler (and swapped the
  batter pair) — the same timing quirk as the ticker clearing. Three places trusted that
  write's names and paid for it:
  - the overlay's **hat-trick chain** could fire a false "3 IN 3" for the incoming bowler
    and miss a genuine hat-trick for the outgoing one;
  - a **five-wicket haul** taken on an over's final ball never fired its graphic (no later
    poll ever shows the completed figures) — now synthesized from the pre-rotation snapshot;
  - the **ball DB's recovered final delivery** was logged against the new over's bowler and
    a swapped batter pair — wrong clip captions, wrong per-bowler exports, on 1 in 6 rows.
- **Manual scoring: "Edit a ball" could poison undo and restart-recovery.** Correcting a
  wicket into runs leaves follow-on events (a next-batter pick) that only replay leniently;
  `undo` and the restart restore replayed them strictly, so an undo could truncate the live
  innings and a server restart discarded the whole saved match as "unreadable". Both now
  replay leniently, matching the edit path.
- **Manual scoring: a mid-over bowler change corrupted maiden counts.** The replaced
  bowler's conceded-this-over count was never reset, suppressing a genuine maiden when they
  returned — and the finisher of a shared over could be credited a maiden that isn't theirs
  (a shared over is nobody's maiden).
- **A live manual session now counts as a healthy feed everywhere.** AI over-commentary
  built its context from the PCS file even mid-manual-session (narrating a 0-0 non-match or
  a stale previous game), and `/health` + the watchdog reported the feed stale all match on
  a manual scoring day. All three now apply the same manual-outranks-PCS precedence as
  `/live`; `/health` gains `pcs.manual_scoring`.
- **The panel's automatic stream health check ran before login.** With a club password set,
  opening the panel in a fresh tab 401'd the check, replaced the login prompt with a
  misleading "Session expired — log in again", and burned its once-per-session flag so the
  advertised auto-check never ran. It now waits for login and runs right after it.
- **Quickstart wiped a panel-set weather API key on every run** — the one secret its state
  merge didn't preserve. Weather then silently reported "unconfigured" on match day.
- **A panel poll could drop the overlay's pinned-match cache.** `/live/view` is documented
  as side-effect-free, but clearing a pinned match URL took effect on whichever poll saw it
  first — including the panel's — rebinding the cache out from under the overlay
  mid-stream. Cache maintenance now belongs to the overlay's `/live` poll alone.

## v2.6 — 2026-07-09

*Manual-scoring improvements and YouTube broadcast controls, shaped by live testing
against a real stream. 203 automated tests; manually verified before release.*

- **YouTube broadcast manager** (was "YouTube Title"). Streaming with a stream key
  (recommended) removes OBS's "Manage Broadcast" panel, so the control panel now sets the
  broadcast's **title, description, privacy (public/unlisted/private), and category** over
  the YouTube Data API — most of what that panel did. One button pushes the lot to the
  active (or upcoming) broadcast; each part is a separate call so one failing doesn't sink
  the others, and the result reports exactly what applied. Uses the same one-time Google
  OAuth as the old title updater; the title-only path still works. **"Made for kids" is
  set in YouTube Studio when you create the broadcast** — YouTube's API rejects changing
  it afterwards, so the panel points you there rather than pretending to control it.
  Credential handling hardened for remote use: `yt_credentials.json` git-ignored (was
  not), token written 0600, remote first-run auth refused with a "do it on the streaming
  laptop" message instead of a hung browser, and paths resolved relative to server.py.

- **End-of-over recap on the scoring page.** When an over completes, the scorer sees a
  banner with the over's runs and ball-by-ball tokens, and a choice: confirm and pick the
  next bowler, or step back into the over ("Undo last ball") to fix a mistake before
  moving on.
- **Edit any ball, not just the last one.** An "Edit a ball" button lists recent
  deliveries by over and ball; pick one and the next outcome tap corrects it. Built on the
  event-sourced log — the innings is replayed around the correction, exactly, and any
  follow-on bowler/batter choice invalidated by the change is skipped rather than wedging
  the session. A rejected correction rolls back with the session left fully usable.
- **Scorecard export for Play-Cricket.** A "Scorecard" button (and one at match end)
  produces a full plain-text card — both innings, batting with dismissals, bowling
  figures, extras, and the result — to copy or download. Play-Cricket's API is read-only,
  so results still can't be submitted automatically, but this turns the after-match entry
  into a quick transcription rather than a reconstruction.
- **Fixed:** the session rebuild left the event log clobbered if a replay raised midway
  (e.g. a rejected edit), which could corrupt a live scoring session — the rebuild is now
  exception-safe.

### Fixed during live testing

- **YouTube "made for kids" 403.** Sending `selfDeclaredMadeForKids` on an update — even
  its current value — is rejected by YouTube (it's only settable at broadcast creation).
  It's no longer sent; the panel directs you to set it in Studio.
- **YouTube broadcast targeting.** The finder no longer refuses when YouTube's status
  isn't "active" this instant — with a stream key a broadcast can be ready/testing or a
  persistent "Stream now" broadcast can read as "complete" while OBS is happily streaming.
  It now targets the newest real broadcast and names which one it updated.
- **Stale control panel.** The panel, scoring page and overlay were served with no
  `Cache-Control`, so a browser could run old JS against a new server (a toggle looking
  "stuck", etc.). They're now `no-store`. *(One-time: hard-refresh to clear the old cache.)*
- **OBS Dynamic Bitrate** is enabled automatically and verified live in the panel — the
  seamless first line of defence for a poor connection, distinct from the manual quality
  ladder.

## v2.5 — 2026-07-09

*Manually tested against a live NV Play feed on 2026-07-09 — replays and auto-tagging,
the full graphics run including bowler milestones, and the quality ladder (which found
and fixed the connected-account issue below). The manual scoring page and the match
simulator carry full automated coverage but await their first human outing.*

### New features

- **Manual scoring page (`/scoring`).** Score a match ball-by-ball from a phone or tablet
  with big tap-friendly buttons — no NV Play/PCS Pro needed — and the entire overlay,
  graphics, ball database and highlights pipeline works identically (manual frames render
  through the same parser as the scorer's feed, and outrank the file while a session is
  live). Event-sourced with exact-replay undo (even across the innings break), wicket-type
  / fielder / run-out-end pickers, per-over bowler prompts, next-batter override, and a
  session file that survives server restarts and dead phone batteries. Same club-password
  login as the control panel; selectable as the data source in quickstart. Also the plan B
  if the scorer's feed drops mid-match.
- **YouTube stream key managed by setup.** The wizard now asks for your stream key
  (stored in git-ignored config.ini, redacted like every other secret) and OBS setup
  applies it automatically — making key-based streaming, which survives restarts and
  quality changes, the default path. Never touched while a stream is live.
- **Adaptive stream quality** for grounds with poor internet, two tiers: OBS's built-in
  **Dynamic Bitrate** is now enabled automatically (seamless encoder-level flexing on
  congestion — no disconnects) and its status is verified live in the panel with a
  one-click enable; plus a **stream sentinel** that polls congestion/dropped frames every
  15s while live and can step the bitrate down a 100/70/50/35% ladder — manual panel
  buttons, or an off-by-default auto mode with a 60s evidence window and 150s anti-flap
  cooldown that never raises quality on its own. (A ladder step briefly restarts the
  stream; the YouTube broadcast survives with a few seconds of buffering.)
- **Match simulator (`simulate_match.py`).** Rehearse the whole broadcast without a
  scorer: a deterministic ball-by-ball engine writes NV Play-style frames to a fake PCS
  folder, faithful to the real feed's trickiest behaviours (ticker clearing on the
  over-completing write, blank pre-match names, `runs_required`-driven innings detection).
  Scenarios: full / chase / century / collapse; `--configure` points the running server at
  it; `--chaos` injects mid-write and stall failures.
- **Auto-tagged highlights.** Every replay clip is tagged at capture with why it fired and
  the match context; manually saved clips are tagged by correlating file times against the
  ball log. The highlights compiler burns captions in as lower-thirds, skips test clips,
  and writes a YouTube-ready description with chapter timestamps. The panel now reports
  the compile's real outcome instead of fire-and-forget.
- **Bowler milestone graphics**: five-wicket hauls (re-firing for the 6th/7th) and
  hat-tricks — including cross-over hat-tricks, with run outs breaking (not extending) the
  chain and wides/no-balls neutral.
- **Automated test suite**: 159 tests (stdlib unittest, no dependencies), wired into CI
  (which now also runs on `dev` pushes). Parsing, season stats, auth, quickstart merge,
  simulator invariants, highlights, manual scoring, stream-quality decisions, JS logic
  executed in a real engine, and HTTP integration against a live in-process server.

### Fixed

- **The ball-by-ball database was silently losing the final delivery of every over.**
  NV Play clears the ticker on the same write that completes an over, so ball 6 never
  appears in any ticker — the overlay always compensated via the score delta, but the DB
  logger just skipped it. Every over in `match_data.db` (and every CSV export) had at most
  five balls. The logger now recovers the invisible delivery from the score/wicket delta,
  and a new full-match soak test drives a complete simulated game through the real server
  and reconciles the DB against the engine's book, ball for ball, both innings.
- **The quality ladder could leave the stream down while reporting success** — OBS stops
  outputs asynchronously, so firing StartStream straight after StopStream could be
  rejected unnoticed. Each step is now verified: stop confirmed, bitrate set, restart
  retried, and honest failure messages if OBS misbehaves (concurrent shifts serialized).
  Found live in testing: **OBS's connected-YouTube-account mode ends the broadcast on
  StopStream** — the shift now detects a restart into a dead broadcast and says so, and
  the panel states the plain-stream-key requirement up front.
- **Milestone cards were hidden by their own replay** — the fifty/century replay switched
  OBS to the Replay scene while the gold card was still airing. The replay is now delayed
  (as wicket replays already were) so the card plays first.
- **Clip tagging was invisible** — tags live in the database, not filenames, so the panel
  now shows "N clips saved · M tagged for highlights" live.
- **Replay captions during manual scoring used stale PCS data** — the tagger now uses the
  same source precedence as the live feed (manual session first).
- **A leftover manual-scoring session silently outranked the scorer's feed** — quickstart
  now warns and offers to clear it when NV Play is the chosen source, and the panel shows
  an unmissable amber MANUAL badge whenever the manual session is driving the overlay.
- **State reads cached** — `load_state()` was re-reading and re-parsing the settings file
  from disk several times per overlay poll (thousands of reads per match); it's now
  mtime-cached (16× faster, zero steady-state disk I/O).
- **quickstart no longer wipes panel-entered state** (squad roster, sponsor fields, away
  colour, toggle edits, a manually entered opposition) — it merges over the existing file
  instead of rewriting it.
- **A non-numeric badge pick no longer kills the overlay** — `/live` crashed on every poll
  if `home_club_id` was set to a logo filename.
- **Weather now uses the ground's own coordinates** (saved from PlayCricket) instead of
  hardcoded ones — other clubs were getting the original club's weather, which also drove
  the DLS rain threshold.
- **Wickets were never reaching the event buffer or fall-of-wickets log** unless the AI
  ball-commentary toggle (off by default) was on — the detection baseline was only seeded
  inside that toggled path. Match reports were missing FOW data because of this.
- **CSV export worked only without a club password** (the panel used `window.open`, which
  can't send the auth header); **the prematch scorer line never displayed** (operator
  precedence bug); the ball-event commentary trigger compared state against an
  already-updated baseline (never fired); a missing `control_token` line was appended into
  the wrong config.ini section; the highlights concat file mis-declared every clip as
  0.5s long; plus removed dead routes and duplicate dict keys.

### Changed

- **The control panel now lives in `control.html`** (was a 2,100-line Python string inside
  server.py) — normal JS escaping, edits show on refresh without a server restart, and the
  historical backslash-doubling bug class is gone. Importing server.py no longer has side
  effects (token generation moved to startup).
- **`/live` split**: the overlay's poll drives the match pipeline and consumes wicket
  events; the panel polls a side-effect-free `/live/view`, so it can no longer eat the
  overlay's events or triple-run the ball logger.

## v2.4 — 2026-07-06

*(Tagged without a changelog entry at the time — backfilled.)*

- Weekend sponsor strap overlaid on the end-of-over graphics sequence; a long-standing
  over-transition timing bug fixed (end-of-over graphics fired one poll late); startup
  update check against the latest GitHub release.
- Pre-match and crediting fixes: split opening-batter cards no longer collapse to one,
  pre-match graphics persist until play actually starts (not until the match is merely
  configured), over commentary/summary credits the bowler who actually bowled the over,
  and the real cause of replays never firing (a hardcoded origin mismatch in
  overlay.html) resolved.

## v2.3 — 2026-07-02

- **Remote access, phase 3.** A QR code — printed as ASCII art in the terminal at startup
  and shown in the control panel — lets you pair a phone or tablet without typing an IP
  address. The panel shows a small pill ("This machine" / "Same network" / "Tailscale
  remote" / "Cloudflare remote") so whoever's looking at it always knows which kind of
  connection they're on. Added **Cloudflare Tunnel** as a public-URL fallback
  (`cloudflare_tunnel` in `config.ini [Network]`) for operators who can't install
  Tailscale — it refuses to start unless `club_password` is set, since that URL would
  otherwise be reachable with no login.
- **Self-healing watchdog.** A background check every 90 seconds fixes what it safely can
  and just logs what it can't: resets the season-stats build if it ever gets stuck (this
  also fixed the underlying bug — an unexpected error mid-build used to leave it wedged
  until a manual server restart), restarts the Cloudflare Tunnel if it dies (capped
  retries so a real problem doesn't loop forever), trims old rate-limit timestamps, and
  logs when the scorer's feed goes stale or recovers. Status visible at `GET /health`.
- **README rewrite.** Leads with the problem and what the project does, instead of a
  "Version 2.1 highlights" recap that had gone stale relative to this changelog.
- **Landing page polish.** The GitHub Pages site got scroll-in animations, hover lift on
  the feature cards, and a pulsing "LIVE" badge on the hero screenshot — all skipped
  automatically for visitors who've asked their browser for reduced motion.

## v2.2.2 — 2026-07-01

*`v2.2` and `v2.2.1` were superseded by this release and their tags/GitHub releases
removed — everything they contained is included below.*

- **Standalone setup wizard.** `CricketStreamSetup.exe` (Windows) and
  `CricketStreamSetup-mac.zip` (Mac, universal2 — works on both Apple Silicon and Intel) let
  a new club get started without installing Python first — the wizard installs Python
  itself if it's missing (via `winget` on Windows, the official installer plus the
  SSL-certificate fix on Mac), then walks through the same club setup as running
  `setup_wizard.py` from source. (The first build of this was accidentally Apple-Silicon-only
  and crashed with "bad CPU type in executable" on Intel Macs — no Rosetta-equivalent runs
  `arm64` code the other way round — fixed by building a proper `universal2` binary.)
- **Fixed a blank pre-game player card.** NV Play renders the scoreboard template as soon as
  a match starts, so batter names come through as genuinely empty (not missing) until the
  scorer actually selects the openers — the overlay used to treat that as a real new batter
  and show a card with nothing on it.
- **New pre-game "season form" panel** fills that same waiting period with something useful:
  each team's own top run-scorer and leading wicket-taker this season, styled like the
  player card (photo + stat row), cycling alongside the existing competition/umpires panel.
- **Made the project genuinely club-agnostic.** What had been a single club's private tool
  had BBCC's own identity (club name, PlayCricket ID, home ground, AI prompt text, error
  messages, even the control panel's page title) hardcoded as fallback defaults throughout —
  invisible to BBCC's own use since the "wrong" default happened to be their own real data,
  but broken for any other club. Renamed `bbcc_scoreboard.template` to `scoreboard.template`
  and rebranded "BBCC Stream Overlay" to "CricketStream Overlay" throughout.
- **Redacted the camera RTSP URL from `GET /state`.** Most IP cameras embed credentials
  directly in the URL (`rtsp://user:pass@host/stream`); this was being sent in plaintext to
  anything that could reach the control panel, including over plain HTTP Wi-Fi when using
  the phone/tablet control panel. It now redacts the same way the OBS password and API keys
  already did.
- **Docs:** consolidated `RELEASE_NOTES_v2.1.md` and `WHATS_NEW_V2.md` into this
  `CHANGELOG.md`, documented Intel Mac support in the setup guides, and updated the stale
  version badge.

## v2.1 — Broadcast intelligence & your own data

This release builds on the v2.0 broadcast layer with match intelligence, a dataset of your
own, and easier setup for non-technical operators.

**Highlights**
- **Ball-by-ball database.** Every delivery is logged to a local SQLite file (`match_data.db`)
  as you stream — your own season-long dataset. The current over is rewritten live so scorer
  edits and deletions are captured; completed overs freeze. A **Reconcile** button pulls
  PlayCricket's published scorecard as the authoritative record, and any match can be
  **exported to CSV**.
- **Result posts for any match.** A "Load results" picker pulls your recent PlayCricket
  results — home or away, streamed or not — and builds a polished Instagram result graphic,
  working out the result and your top batter and bowler straight from the scorecard. Per-team
  photo subfolders are supported (`socials/1st`, `socials/2nd`, `socials/3rd`), falling back
  to the main folder; all age-group sides route to `socials/youth`, using club stock photos
  and a discreet first-name + initial for player names — a safeguarding-conscious default
  for juniors.
- **Match-day sponsors.** Every logo in `sponsors/` now appears on result posts, scaled to
  share the width — add a one-off sponsor by dropping in a file.
- **One-click camera.** Enter your camera's RTSP URL in the control panel and "Add camera to
  OBS" creates the media source for you over the WebSocket connection, with auto-reconnect
  if the feed drops.

**Broadcast graphics**
- **Auto-detected moments** — season-best scores and team milestones fire automatically on
  the over summary and are woven into the AI commentary.
- **"At this stage"** — in the second innings, every over summary compares the chase to the
  first innings at the same point.
- **Full innings scorecard** at the break — all eleven batters with dismissals plus bowling
  figures, broadcast-card style. Requires the v2.1 `scoreboard.template`.
- **Bowler spell tracker** — "This spell: 5-1-18-2" once a bowler has bowled consecutive
  overs from the same end.
- **DLS par pill** above the scorebar when rain is forecast (Standard Edition
  approximation, intended as a guide).
- Broadcast animation polish: spring panel entries, sweeping boundary banners, milestone
  count-ups.

**Reliability & security**
- Threaded server so a slow AI or PlayCricket call never freezes the overlay.
- Atomic state writes with a last-good fallback; resilient PCS file reads.
- Secrets are redacted from browser-facing responses; scorer-controlled names are escaped.
- A control-panel health strip and a quickstart pre-flight self-test.
- Reworked event detection for the tricky Saturday cases: boundaries and wickets on the last
  ball of an over, quick wickets, scorer corrections, retirements, mid-match overlay refreshes.
- A player-stats diagnostic (`/player/stats?name=SURNAME&debug=1`) lists everyone sharing a
  surname and which record will be used, so ambiguous names are easy to spot.

**Control panel improvements (post-release patches)**
- **Responsive control panel.** The panel stacks into a single column on narrow screens
  (≤ 768px) with larger, touch-friendly buttons, so a phone operator on Wi-Fi can run the
  stream without pinching and zooming.
- **`config.ini` auto-seeding.** `server.py` reads all sections of `config.ini` on startup
  and pre-populates any `match_state.json` fields still at their defaults — API keys, club
  name, kit colour, folder paths, and YouTube title template all load automatically, no
  need to re-enter them after a fresh install.

**Upgrading from v2.0**
1. Replace `server.py` and `overlay.html`.
2. Deploy the v2.1 `scoreboard.template` to the scorer's NV Play / PCS Pro machine —
   required for the full innings scorecard and shirt-number features. Restart PCS Pro and
   re-select the scoreboard in Tools → Configuration.
3. Install Pillow if you haven't: `pip install Pillow` (used for social-post images).
4. Optional: create `socials/1st`, `socials/2nd`, `socials/3rd`, `socials/youth` and the
   `sponsors/` folder; set your camera's RTSP URL in the control panel.

## v2.0 — Player cards, squad rosters, and AI features

A big step up from v1: the scorebar and core graphics are still there, but the broadcast
feels far closer to professional cricket coverage, plus new tools for after the final ball.

**On-screen during the match**
- **Player cards with photos and stats.** When a new batter walks out, a card slides in
  showing their photo and season batting stats (innings, average, high score). At the start
  of an innings, both openers get a card — left and right of screen. Stats are aggregated
  live from PlayCricket for both your club and the opposition. Photos live in a `headshots/`
  folder, matched by surname or shirt number, with several filename patterns accepted; if no
  photo is found the card shows initials instead — it never breaks.
- **Squad roster.** NV Play sends surnames only, so two brothers both read as "Smith", and a
  player with more than one PlayCricket account can show up twice. The Squad Roster in the
  control panel maps shirt numbers to full names, so the overlay picks the right player —
  and therefore the right photo and stats.
- **Worm chart.** The run-rate panel is a proper worm — each innings drawn as a cumulative
  runs-by-over line in its own team colours, with red wicket markers and the running total
  labelled at the head of each worm.
- **Full dismissal detail on the wicket card**, spelled out in full words — *Caught Jones
  Bowled Smith*, *LBW Bowled Patel*, *Run Out*, *Stumped Wood Bowled Khan* — rather than the
  scorer's shorthand.
- **Kit colours that follow the batting team**, both innings.
- **AI over commentary.** An optional fourth panel shows a single line of analysis written
  live by Claude from the real match situation, at a fraction of a penny per over.
- **Drinks-break weather.** At an over you choose (default 25), the weather widget appears
  automatically for the interval and clears on the next ball.
- **Smarter graphics timing.** The over summary is suppressed when a wicket falls on the
  last ball; over runs come from the score itself so a ball bowled as the over rolls over is
  never dropped; each part of the update cycle is isolated so a hiccup in one graphic can't
  knock out the others mid-match.
- **Automatic club badges**, matched by PlayCricket club ID and detected from the day's
  fixture, with a manual dropdown fallback if anything doesn't match.

**After the match**
- **AI match report** — one click generates a full written report from the ball-by-ball log:
  result, key partnerships, standout performances, turning points — in seconds.
- **AI social media posts** — ready-to-paste posts summarising the match, bundled with
  photos from a folder for an easy match-day round-up.

**Under the hood**
- A faithful test harness simulates real match sequences so tricky edge cases stay fixed.
- Cleaner, more reliable server with all AI features sharing a single Anthropic key.

**Upgrading from v1**
1. Replace `server.py`, `overlay.html`, and `quickstart.py`.
2. Copy the updated `scoreboard.template` into NV Play's Templates folder and restart NV
   Play so it picks up the new dismissal-detail and shirt-number fields.
3. Create `headshots/` (player photos) and `socials/` (match photos) next to `server.py` if
   you want the new features — `logos/` for club badges is unchanged from v1.
4. Add your Anthropic API key in the control panel to enable commentary, reports, and posts.
5. Optional: fill in the Squad Roster for any players who share a surname or have duplicate
   PlayCricket accounts.

Existing `config.ini` and club settings carry over unchanged.

**What you need for the AI features:** a single Anthropic API key (console.anthropic.com)
powers over commentary, match report, and social posts — a few pence for a whole match.
Everything else works without one; AI features simply stay switched off until a key is set.

## v1.0 — Initial release

The first version: live scorebar, fall-of-wicket card, boundary flash, over summary, and
the OBS/NV Play integration that everything since has built on.
