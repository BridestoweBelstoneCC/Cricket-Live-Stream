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

- API keys (Anthropic, PlayCricket), your OBS WebSocket password and a Discord alerts
  link are stored locally in `config.ini` and `match_state.json`, beside the app. Both are
  **git-ignored** so they are never committed.
- **The club password is stored as a hash** (PBKDF2), never the password itself. A
  forgotten one can't be read back: type a new one into `config.ini` and restart.
- The server redacts secrets from all browser-facing responses — they are never readable
  from the control panel or any `/state` endpoint, whether accessed locally or over Wi-Fi.
  Keys that travel in a web address (PlayCricket's, the Discord link) are taken out of
  error messages and logs.
- The repository ships only placeholder configuration (`config.example.ini`,
  `match_state.example.json`) — no real credentials.

## How the server decides who's asking

- One table (`internal/server/routes.go`) says who may call each route: anyone, the OBS
  overlay, this laptop, or a signed-in operator. A route that isn't in it answers 404.
- The server answers only to its own names (an IP address, localhost, the laptop's name,
  Tailscale, its own Cloudflare tunnel, and `[Network] extra_hosts`), which stops a web
  page elsewhere reaching it by pointing a domain at the laptop.
- Five wrong passwords lock that device out for 10 minutes; thirty from any mix of
  devices stop new sign-ins for 5 minutes, except on the laptop itself.

[`ARCHITECTURE.md`](ARCHITECTURE.md) §9 has the full picture.

## Good practice for clubs running this

- **Do not expose port 5000 to the internet.** Local network (Wi-Fi) access for phone
  operators is supported and fine; public internet exposure is not.
- **The clubhouse screen's port (5096) is for the club network only, too.** It's open only
  when the screen is switched on, needs the pairing key (wrong guesses are locked out), and
  reaches only the match-day buttons, never settings or keys. Make a new key on the
  match-day screen if you think someone else has it.
- **The commentary phones' port (5095) is open only while commentary is switched on.** A
  phone needs the QR code's key, which is new at every switch-on, reaches only its own
  page, and is heard only after the operator says yes.
- **Plain HTTP on local Wi-Fi** means the session token is visible to anyone monitoring
  the same network. This is an acceptable trade-off for a trusted home or club network;
  use a password on your Wi-Fi and don't run match-day ops from a public hotspot.
- Treat your API keys like passwords. If one is ever committed by accident, revoke and
  regenerate it.
- The ball-by-ball database (`match_data.db`) contains match data only — no credentials —
  but is also git-ignored so your data stays yours.
