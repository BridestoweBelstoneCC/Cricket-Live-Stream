# Security Policy

## Reporting a vulnerability

If you find a security issue — for example a way to read another user's API keys, or a
problem with how the control panel handles input — please **do not open a public issue**.
Instead, report it privately using GitHub's
[private vulnerability reporting](../../security/advisories/new), or contact the club
through the website linked on the repository.

We'll acknowledge your report as soon as we can and work with you on a fix before any public
disclosure.

## How this project handles secrets

- API keys (Anthropic, PlayCricket) and your OBS WebSocket password are stored locally in
  `match_state.json`, which is **git-ignored** so it is never committed.
- The server redacts secrets from all browser-facing responses — they are never readable
  from the control panel or any `/state` endpoint, whether accessed locally or over Wi-Fi.
- The repository ships only placeholder configuration (`config.ini`,
  `match_state.example.json`) — no real credentials.

## Good practice for clubs running this

- **Do not expose port 5000 to the internet.** Local network (Wi-Fi) access for phone
  operators is supported and fine; public internet exposure is not.
- **Plain HTTP on local Wi-Fi** means the session token is visible to anyone monitoring
  the same network. This is an acceptable trade-off for a trusted home or club network;
  use a password on your Wi-Fi and don't run match-day ops from a public hotspot.
- Treat your API keys like passwords. If one is ever committed by accident, revoke and
  regenerate it.
- The ball-by-ball database (`match_data.db`) contains match data only — no credentials —
  but is also git-ignored so your data stays yours.
