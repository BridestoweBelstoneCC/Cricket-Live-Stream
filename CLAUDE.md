# CLAUDE.md — project guide for Claude Code

This file is read automatically at the start of every Claude Code session. It captures the
architecture, the commands, and the hard-won gotchas so you don't have to re-explain them.

## What this project is

A free, open-source live-stream graphics system for grassroots cricket. A Python server reads
the scorer's match data and drives an HTML overlay inside OBS, which streams to YouTube. There
is also a browser **control panel** (served by the server) for the match-day operator.

**Data flow (three sentences):** The scorer's software (NV Play / PCS Pro) writes a JSON file
on every ball. `server.py` reads that file, parses it, and serves the live state on `/live`.
`overlay.html` (an OBS browser source) polls `/live` every ~2.5s and renders the graphics.

## Key files

- **`server.py`** (~5000 lines) — the whole backend. HTTP server on port 5000
  (`ThreadingHTTPServer`). Also serves the control panel as an inline HTML string,
  `CONTROL_HTML`, and builds the social-media images.
- **`overlay.html`** (~2200 lines) — the OBS browser source (1920×1080). Pure HTML/CSS/JS.
- **`quickstart.py`** — auto-setup / launcher; runs a pre-flight self-test.
- **`setup_wizard.py`** — first-time setup wizard; installs packages and writes `config.ini`.
  Also built into a standalone Windows `.exe` / macOS binary by
  `.github/workflows/build-setup-wizard.yml` (see gotchas below).
- **`scoreboard.template`** — the template the scorer's software fills in. Deployed to the
  *scorer's* machine, not the streaming machine.
- **`config.ini`** / **`match_state.json`** — local settings (git-ignored, hold secrets).
  Templates: `config.example.ini`, `match_state.example.json`.
- **`match_data.db`** — SQLite ball-by-ball log, created at runtime (git-ignored).

## Build / test commands

There is no compiler and no test framework yet — verification is lightweight:

```bash
# 1. Server must compile cleanly
python3 -c "import py_compile; py_compile.compile('server.py', doraise=True); print('OK')"

# 2. The control panel JS is embedded in server.py — extract and syntax-check it
python3 -c "import re; s=open('server.py').read(); \
m=re.search(r'CONTROL_HTML\\s*=\\s*\"\"\"(.*?)\"\"\"', s, re.S); \
open('/tmp/panel.js','w').write('\\n'.join(re.findall(r'<script[^>]*>(.*?)</script>', m.group(1), re.S)))"
node --check /tmp/panel.js

# 3. Run it
pip install -r requirements.txt
python3 server.py      # or: python3 quickstart.py
```

Always run steps 1 and 2 after editing `server.py`. Step 2 matters more than it looks (see
gotchas).

## Critical gotchas (these have bitten us before)

- **`CONTROL_HTML` is a plain triple-quoted string, NOT an f-string.** Any backslash in the
  embedded JavaScript must be **doubled** — write `\\n`, `\\t`, `\\d` in regexes, etc. A single
  backslash will break the panel silently. After any panel edit, run the `node --check` step
  above; brace-counting alone is not enough.
- **`overlay.html` JS brace balance baseline is 4** (it isn't zero — there are intentional
  unmatched braces in template strings). Don't "fix" it to zero.
- **Logging must never raise.** `log_ball_data()` and anything in the match-day loop is wrapped
  in try/except and must stay that way — a logging error must never interrupt the stream.
- **State writes must stay atomic.** `save_state()` writes to a temp file then `os.replace()`s,
  with a last-good fallback. Don't replace this with a naive `open().write()`.
- **Route handling checks specific paths before prefixes.** When adding endpoints, put exact
  matches (`path == "/data/status"`) before `startswith` checks so a prefix doesn't swallow a
  more specific route.
- **The current-over DB write is delete-then-reinsert.** That's deliberate — it's how scorer
  edits/deletions within an over stay correct. Don't switch it to plain append.
- **Secrets never reach the browser.** `/state` redacts secret keys; POST `/state` drops
  sentinel values. Keep any new secret field in that redaction list.
- **Never commit `config.ini`, `match_state.json`, or `match_data.db`.** They're git-ignored;
  check `git status` before committing.
- **The server reads the scorer's LOCAL file.** It must run on a machine that can see the
  scorer's output folder. This constrains anything network-related (see `REMOTE_ACCESS_PLAN.md`).
- **Inside a frozen `setup_wizard.py` (PyInstaller), `sys.executable` is the exe itself, not a
  Python interpreter** — passing it to `subprocess` for `pip`/launching another script causes
  infinite self-relaunching. Use `find_python()`, which searches `PATH` instead. Same trap for
  `__file__`: it resolves inside the temp extraction folder, so paths must use
  `os.path.dirname(sys.executable)` when `sys.frozen` is set.
- **No hardcoded club identity in defaults.** `DEFAULT_STATE`, `config.example.ini`, and
  `match_state.example.json` must stay club-agnostic (e.g. `"Home CC"`, blank `ground_filter`/
  `home_club_id`) — this project is used by clubs other than the original maintainer's.

## Conventions

- Match the surrounding style; don't reformat whole files.
- Comments explain *why*, not *what*.
- Prefer small, focused commits with present-tense messages ("Add X", not "added x").
- PlayCricket: BBCC `site_id`/`club_id` = `29434`. The API token is not club-specific.

## Useful diagnostics

- `http://localhost:5000/health` — feed freshness, photos, badges, AI key status.
- `http://localhost:5000/player/stats?name=SURNAME&debug=1` — which season record a name resolves to.
- `http://localhost:5000/data/status` — ball-by-ball DB status.
