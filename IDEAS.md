# Ideas

Things worth building that aren't built yet. Nothing here is promised; it's so a good idea
isn't lost between match days.

## Clubhouse scoreboard (Raspberry Pi + touch screen)

*Partly built:* the optional clubhouse screen ([`CLUBHOUSE_SCREEN.md`](CLUBHOUSE_SCREEN.md),
`internal/clubhouse`) runs on exactly this hardware, reads `/live/view`, finds the
streaming laptop or takes a pairing key, and shows the score with the controls behind a
PIN. What's below and not yet done: the scorecard / last few overs / league table views,
and a screen for a club that wants the score *without* the controls.

A screen in the clubhouse (or on the boundary) showing the live score to people who aren't
watching the stream. The club already has the hardware: a Raspberry Pi and a touch screen.

How it would fit:

- The Pi is a display only: Chromium in kiosk mode on a page the streaming laptop's server
  serves, over the club wifi or Tailscale. No scoring logic on the Pi.
- It must read `/live/view`, never `/live` (only the OBS overlay may poll `/live` — see
  CLAUDE.md), and never `GET /commands`.
- A layout for a room, not a broadcast: big total, wickets, overs, target and required
  rate, the two batters and the bowler; readable from across the bar. Club colours from
  `/state`.
- Touch for a few views (scorecard, last few overs, the league table), and nothing that
  changes the match — it's a public screen.
- It needs a read-only way in when the club password is set (the panel's token is too much
  for a screen anyone can touch): a separate, view-only key, shown as a QR code in the
  panel, is one way.
- Setup: a Pi image or a one-line installer that boots straight to the page, finds the
  streaming laptop the way the scorer's laptop does (or takes a pairing code), and shows a
  plain "waiting for the match" screen until there is one.
- The Go binary already builds for Linux on ARM, so the same app could run on the Pi as a
  new role ("clubhouse screen") that opens the kiosk browser itself.
