# Remote access (v3) — design & build plan

**Goal:** let a trusted club volunteer operate the control panel from somewhere other than the
streaming laptop (home, the clubhouse), without exposing the system to the open internet
unsafely.

**Non-goal:** letting the public *watch* — YouTube already does that. This is about *operating*
the graphics remotely, not viewing the stream.

---

## The one constraint that shapes everything

`server.py` reads the **scorer's local match file** (NV Play / PCS Pro writes it to a folder on
the scoring/streaming machine). A server in the cloud cannot see that file. Therefore:

> The brain stays on the streaming laptop. We expose a **remote control layer** to it — we do
> **not** move the server to the cloud.

Every option below keeps the server where it is and changes only *how it's reached*.

---

## Threat model (what we're protecting against)

Once the control panel is reachable beyond `localhost`, anyone who reaches it could:

- trigger replays / change graphics mid-match,
- **burn your Anthropic credit** by spamming AI endpoints,
- change the YouTube stream title,
- read settings (already mitigated: `/state` redacts secrets).

So the two must-haves before any exposure are **authentication** and **rate-limiting on the
AI/expensive endpoints**. Neither exists today.

---

## Build in phases

Each phase is useful on its own. Do them in order; you can stop after any one and still be
better off.

### Phase 1 — Auth token on mutating endpoints  *(do this regardless of remote access)*

The single most valuable change, and safe to ship even while everything stays on `localhost`.

- Add a `control_token` to `config.ini` (git-ignored, so it never leaves the machine). Generate
  a long random value.
- Require it on **all state-changing requests** (every POST, plus expensive GETs like
  `/social/image/generate`, `/report`, `/data/reconcile`). Accept it as an
  `Authorization: Bearer <token>` header or a session cookie.
- Compare with `hmac.compare_digest` (constant-time), never `==`.
- The control panel reads the token from a login form once and stores it for the session.
- Tighten CORS: stop using `*` for mutating routes; the panel is same-origin so it doesn't need
  wildcard CORS.
- Leave read-only `/live` open (the overlay needs it, and it exposes nothing sensitive).

**Test:** a POST without the token gets `401`; with it, `200`. `/live` still works tokenless.

### Phase 2 — A simple login + rate limits

- One **shared club password** is enough to start (multi-user accounts are over-engineering for
  one club). The login exchanges the password for a signed, expiring session token.
- Add a **cooldown** on AI endpoints (e.g. one over-commentary call per N seconds, a daily cap)
  so a leaked token can't run up a bill. This was always on the backlog; remote access makes it
  essential.
- Log auth failures so you can see if something's probing.

### Phase 3 — Expose it (pick one)

#### Option A — Tunnel straight to the laptop  *(recommended first)*

A tunnel gives the laptop an inbound-reachable address **without opening any ports** on the
club's router — the laptop dials *out* to the tunnel provider.

- **Tailscale** *(most private)* — puts the laptop and the volunteer's device on a private
  network. Only devices you invite can reach the panel; nothing is public. Easiest and safest
  if every operator can install Tailscale. Effectively a private VPN.
- **Cloudflare Tunnel** *(public URL + gated)* — gives a `https://something.trycloudflare` style
  URL with automatic TLS, and you put **Cloudflare Access** in front so only approved emails can
  load it. Good when operators can't install anything.

Either way TLS is handled for you — **do not roll your own HTTPS.** Phase 1's auth token still
applies underneath, as defence in depth.

#### Option B — Cloud relay  *(fallback for locked-down networks)*

If the club's network blocks outbound tunnels (some do), stand up a tiny VPS running a
**relay**: the laptop opens a persistent outbound WebSocket to the relay, the volunteer's
browser talks to the relay, and the relay forwards control messages down to the laptop. More
moving parts (a server to maintain, secure, and pay for), and the relay must itself be
authenticated. Only choose this if Option A is genuinely blocked.

**Recommendation:** start with **Tailscale** (private, no public surface, ~zero cost). Keep
Cloudflare Tunnel as the documented alternative for operators who can't install software, and
treat the relay as a last resort.

---

## Where secrets live (unchanged principle)

- API keys and the `control_token` stay in `config.ini` **on the laptop**, never in the repo,
  never sent to the browser.
- The browser only ever holds the short-lived **session token** from login — not the API keys.
- `/state` already redacts secrets; keep every new secret field in that redaction list.

---

## What NOT to do

- Don't move the server to a cloud host — it can't see the scorer's file.
- Don't expose port 5000 directly via router port-forwarding — no TLS, no gating, worst option.
- Don't store API keys in the browser or in any committed file.
- Don't skip Phase 1 and jump to exposing the panel — auth first, exposure second.

---

## Suggested first session with Claude Code

A good opening prompt once this is committed:

> "Read CLAUDE.md and REMOTE_ACCESS_PLAN.md. Let's implement Phase 1 only: add a `control_token`
> to config, require it on all mutating endpoints with a constant-time check, add a login field
> to the control panel that stores the token for the session, and leave `/live` open. Keep
> everything working on localhost. Show me the diff before committing."

Ship Phase 1, run a match on it locally, then move to Phase 2.
