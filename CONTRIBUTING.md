# Contributing to CricketStream Overlay

Thanks for your interest — this project exists so that any grassroots cricket club can
have broadcast-quality graphics for free, and contributions from other clubs are exactly
how it gets better. You don't need to be a professional developer to help.

## Ways to help

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
  HTML file, so it's approachable to edit.

## Making a change

1. **Fork** the repository and create a branch for your change.
2. Keep changes focused — one feature or fix per pull request makes review easier.
3. **Test before you push.** At minimum:
   - `python -c "import py_compile; py_compile.compile('server.py', doraise=True)"` should pass.
   - If you touch the control panel (the HTML/JS inside `server.py`), make sure the page
     still loads and the buttons you changed work.
   - If you touch the overlay, load `overlay.html` in a browser and confirm it renders.
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

- **Remote access:** see `CLAUDE.md`'s gotchas section for the design constraints around
  LAN/Tailscale/Cloudflare Tunnel access — worth reading before touching anything
  network-related.

## Licence

By contributing, you agree that your contributions are licensed under the same
[GPL-3.0 licence](LICENSE) as the rest of the project.
