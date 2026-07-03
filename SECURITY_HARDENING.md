# SECURITY_HARDENING.md — v3 remote-access security fixes

Task brief for Claude Code. Work through the phases in order, one commit per phase, and show
the diff before each commit. Read `CLAUDE.md` first — especially the `CONTROL_HTML`
backslash-doubling rule and the `node --check` verification step; several fixes below touch
the embedded panel HTML/JS.

**Context:** the control panel can now be exposed beyond localhost (Tailscale / Cloudflare
quick tunnel). A security review found the issues below. The overlay (`overlay.html` in OBS)
polls `/live` locally and must keep working unauthenticated on localhost throughout — do not
break it.

**Ground rules for every change:**
- Never log, print, or send a secret (API keys, `control_token`, `club_password`, session
  tokens) to the browser or to stdout.
- Use `hmac.compare_digest` for every secret/signature comparison — never `==`.
- Use the `secrets` module for anything random — never `random`.
- Config/state writes stay atomic (temp file + `os.replace`), as the existing code does.
- After edits: `python3 -c "import py_compile; py_compile.compile('server.py', doraise=True)"`
  and re-run the panel-JS `node --check` step from CLAUDE.md.

---

## Phase 0 — Scan the repo history for leaked secrets (do this first)

Before changing any code, establish whether a real secret has EVER been committed — a key in
an old commit is public forever even if the file was later removed, and rotation would be more
urgent than any fix below.

1. Install and run **gitleaks** over the full history:
   ```bash
   brew install gitleaks        # macOS; or download from github.com/gitleaks/gitleaks
   gitleaks detect --source . -v
   ```
2. Also run a targeted history check on the files that ever held config:
   ```bash
   git log -p --all -- config.ini match_state.json | \
     grep -inE "key|token|password" | grep -ivE "your_|CHANGE_ME|placeholder|example|= *$"
   ```
3. Interpret results:
   - **No findings** → note "history clean as of <date>" in the commit message and proceed.
   - **Findings** → report each one clearly (file, commit, what kind of secret). Do NOT print
     the secret value itself into the chat or any log. The remedy is: rotate that credential
     immediately (Anthropic console / PlayCricket support / OBS WebSocket settings), then
     optionally scrub history with `git filter-repo` — but rotation is the fix; scrubbing is
     cosmetic once a key has been public.
4. Prevent repeats: add a gitleaks pre-commit hook so a secret can never be committed again:
   ```bash
   # .git/hooks/pre-commit (chmod +x)
   #!/bin/sh
   gitleaks protect --staged -v || { echo "Commit blocked: possible secret detected"; exit 1; }
   ```
   Also add a `.gitleaks.toml` only if false positives appear (e.g. the sentinel `••••••••`);
   keep it minimal.

---

## Phase 1 — Critical fixes

### 1.1 Empty `control_token` must never sign sessions
Session tokens are HMAC-signed with `_CONTROL_TOKEN`, which falls back to `""`. With an empty
key, anyone who has read the public source can forge a valid session.

- At startup, if `club_password` is set (auth enabled) and `control_token` is blank:
  generate one with `secrets.token_hex(32)`, persist it back to `config.ini` atomically, and
  print a one-line notice (not the token itself).
- If writing config fails, refuse to bind to anything other than `127.0.0.1` and say why.
- Belt-and-braces: in `_make_session_token` / verification, raise or fail closed if the
  signing key is empty.

### 1.2 Rate-limit the login endpoint
The login form may sit on a public URL (quick tunnel). There is currently no brute-force
protection.

- Track failed login attempts per client IP (in memory is fine): after 5 failures within 10
  minutes, reject further attempts from that IP for 10 minutes (HTTP 429) and add a 1s delay
  to every failed attempt.
- Use the `X-Forwarded-For` header's first IP when present (tunnels proxy the connection),
  falling back to the socket address.
- Log failures as a count only — never log the attempted password.

### 1.3 Non-localhost bind requires a password
If `bind_host` is anything other than `127.0.0.1`/`localhost` and `club_password` is empty,
the panel would be open to the whole network with auth off.

- At startup, if bind is non-localhost and `club_password` is blank: print a clear warning and
  **fall back to binding 127.0.0.1** (fail closed). Same check before launching any tunnel.

### 1.4 Every mutating or expensive endpoint enforces auth when enabled
Audit the request handlers. When auth is enabled, ALL of the following must return 401 without
a valid session: every POST endpoint, and the expensive GETs (`/report*`,
`/social/image/generate`, `/social/recent`, `/data/export`, `/data/reconcile`, season-stats
rebuilds). Exceptions that must keep working without auth:
- `/live`, `/health`, static overlay assets — read-only, needed by the overlay/OBS locally.
- `/commentary/over/generate` is fired by the overlay: restrict it to loopback clients
  (connection from 127.0.0.1) OR require the session token — either is acceptable, but it must
  not be callable anonymously through a tunnel (it spends Anthropic credit).

Add a small helper (e.g. `_require_auth(self) -> bool`) so the check is one line per endpoint
and impossible to forget on new routes.

---

## Phase 2 — Hardening (recommended additions)

### 2.1 Session cookie hygiene / CSRF
If the session token is stored in a cookie, set `HttpOnly`, `SameSite=Strict`, and `Secure`
when served over HTTPS (tunnels are HTTPS). If it's sent as an `Authorization: Bearer` header
from panel JS, CSRF is largely moot — prefer that. Either way, reject mutating requests whose
`Origin`/`Referer` (when present) doesn't match the request host.

### 2.2 Lock down CORS
Replace any `Access-Control-Allow-Origin: *` on mutating or authenticated routes with
same-origin behaviour (no CORS header needed for the panel itself). `/live` may keep permissive
CORS for local OBS use.

### 2.3 Security response headers
On panel and API responses add: `X-Content-Type-Options: nosniff`,
`X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and a conservative
`Content-Security-Policy` for the panel (self only; allow inline styles/scripts only if the
panel genuinely needs them — it's one inline page, so `'unsafe-inline'` may be required; note
it in a comment).

### 2.4 Redaction list is complete
`/state` (and any endpoint echoing config) must redact: `anthropic_api_key`,
`playcricket_api_key`, `api_token`, `weather_api_key`, `obs_password`, **`control_token`**,
**`club_password`**. POST /state must continue dropping sentinel values for all of these.

### 2.5 Global request size + method guards
Reject request bodies over ~1 MB (413) and unknown methods early. Cheap protection against
junk traffic on a public URL.

### 2.6 Session revocation on demand
Add a "log everyone out" control-panel button (auth-required) that rotates the in-memory
signing epoch (e.g. mix a server-start nonce or counter into the HMAC input) so all existing
sessions die without restarting the stream.

### 2.7 Auth event log
Keep a small in-memory ring buffer (say 200 entries) of auth events: login success/failure
(IP + time only), lockouts, 401s on mutating routes. Show the last few in the panel's health
area so the operator can see if something is probing during a match.

---

## Phase 3 — Repo & docs

- Verify `.gitignore` is committed and covers: `config.ini`, `match_state.json`,
  `match_data.db*`, `__pycache__/`, `*.log`, `.DS_Store`, `season_stats_cache.json`.
- Confirm `config.ini` is untracked (`git ls-files | grep config.ini` → only
  `config.example.ini`). If the live file is still tracked: `git rm --cached config.ini`.
- Docs: README/setup guides say copy `config.example.ini` → `config.ini`; sync the port number
  everywhere (code says 5001, README says 5000); document "restart server or use Log everyone
  out to revoke sessions"; state plainly that Tailscale is the recommended exposure method and
  quick tunnels are the fallback.
- Add a note to `REMOTE_ACCESS_PLAN.md` marking which phases are now implemented.

---

## Acceptance tests (run these before calling it done)

Phase 0:
0. `gitleaks detect --source .` exits clean (or every finding has been reported and the
   credential rotated); a staged file containing a fake key is blocked by the pre-commit hook.

Local, auth enabled (`club_password` set):
1. `curl -X POST localhost:5001/state -d '{}'` → **401**.
2. Login with wrong password 5× → 6th attempt → **429**; correct password still locked out
   until the window passes.
3. Login with correct password → session works; POST /state with token → **200**.
4. `curl localhost:5001/live` (no auth) → **200**; overlay in OBS still updates.
5. `curl localhost:5001/state` (authed) → response contains `••••••••` for every secret,
   including `control_token` and `club_password`.
6. Set `bind_host = 0.0.0.0` with empty `club_password` → server binds 127.0.0.1 and warns.
7. Blank `control_token` + set password → server generates and persists a token; sessions
   issued before a "log everyone out" stop working after it.
8. Through the tunnel: `/report/generate` and `/social/image/generate` anonymously → **401**;
   `/commentary/over/generate` from a non-loopback client → **401** (or authed-only).

Regression: run a demo-mode match end-to-end — scorebar updates, over summaries fire, replay
buttons work from an authenticated phone browser.
