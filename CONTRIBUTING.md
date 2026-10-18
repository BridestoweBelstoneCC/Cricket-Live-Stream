# Contributing to CricketStream Overlay

Thanks for your interest — this project exists so that any grassroots cricket club can
have broadcast-quality graphics for free, and contributions from other clubs are exactly
how it gets better. You don't need to be a professional developer to help.

## Ways to help

- **Chat first if you like.** Not sure whether something's a bug, or want to talk an idea
  through before writing it up? Ask on the [Discord](https://discord.gg/2nuBPj9rH7).
- **Report a bug or rough edge.** Open an [issue](../../issues) describing what happened,
  what you expected, your operating system, and the steps to reproduce it. Screenshots of
  the overlay or the control panel are hugely helpful.
- **Suggest a feature.** Open an issue describing the idea and how it would help on a match
  day. Real-world use cases are worth more than feature lists.
- **Improve the docs.** If a setup step tripped you up, a small clarification to one of the
  guides helps the next volunteer. Doc-only changes are very welcome.
- **Add support for other scoring software.** The overlay currently reads NV Play / PCS Pro
  output. If your club scores with something else, a parser for it would open the project up
  to many more clubs.
- **Improve the overlay design** or add a graphic — the overlay is a single self-contained
  HTML file, so it's approachable to edit. Put your edited `overlay.html` (or
  `control.html`, `scoring.html`) next to CricketStream and refresh: a file beside the app
  overrides the built-in copy, no rebuild needed.

## Making a change

1. **Fork** the repository and create a branch for your change.
2. Keep changes focused — one feature or fix per pull request makes review easier.
3. **Test before you push.** CricketStream is written in Go; install it from
   [go.dev/dl](https://go.dev/dl/) (the version in `go.mod`). These all run in CI too:
   - `go vet ./...` and `gofmt -l .` — nothing printed means all is well.
   - `go test ./...` — the whole suite, about a minute. With [Node.js](https://nodejs.org)
     installed it also runs the overlay's and panel's own JavaScript (a single syntax error
     kills a whole page); without it those tests skip.
   - `go build ./cmd/cricketstream` — builds the app, everything built in.
   - If you touch the control panel (`control.html`) or the overlay, load it in a browser
     and check the parts you changed. For the scorebar, `go run ./scripts/renderscorebar`
     screenshots every style — nothing else catches a visual regression.
   - Touching OBS setup? `internal/integration` drives a real, *portable* OBS end to end
     (see the comment at the top of that test for how to make the portable copy).
4. **Don't commit secrets.** Never commit a populated `match_state.json`, real API keys, or
   the `match_data.db` database — these are git-ignored for a reason. The repo ships
   `match_state.example.json` as a safe template.
5. Open a **pull request** describing what you changed and why. Mention which OS you tested on.

## Style

- Match the surrounding code rather than reformatting whole files.
- Prefer clear names and short comments explaining *why*, not *what*.
- Logging must never crash a live match — wrap anything that touches files, the network, or
  the database so an error is caught and the stream keeps running.

## Safeguarding

This project is used to publish content about cricket matches, including junior cricket.
Please keep youth-related features conservative by default (for example, the youth social
posts use club stock photos and discreet player names). If a change affects how children are
named or shown, call it out clearly in your pull request.

## Architecture notes

- **Start with [ARCHITECTURE.md](ARCHITECTURE.md)** for how the pieces fit, and
  [CLAUDE.md](CLAUDE.md) for the gotchas — the hard-won rules (remote access, OBS, the
  scorer's feed) that look odd until you know why they're there.

## Licence

By contributing, you agree that your contributions are licensed under the same
[GPL-3.0 licence](LICENSE) as the rest of the project.
