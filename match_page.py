"""A shareable match page: one self-contained HTML file per match (images inlined, no
external requests), for the club website or straight into a WhatsApp group.

Pure rendering — render(data) takes everything as plain values (server.build_match_page
gathers them) and every piece of text is HTML-escaped, so a player's name or an AI-written
report can never break or inject into the page.
"""
import html

E = html.escape


def _worm_svg(worm, colours, labels=(), width=760, height=320):
    """Run-rate "worm": cumulative runs by over for each innings, as inline SVG, with a
    legend (labels[i] names the i-th innings) and over numbers along the bottom."""
    series = [(k, pts) for k, pts in sorted(worm.items()) if pts]
    if not series:
        return ""
    max_over = max(p[0] for _, pts in series for p in pts) or 1
    max_runs = max(p[1] for _, pts in series for p in pts) or 1
    pad_l, pad_b, pad_t, pad_r = 48, 34, 16, 16
    w, h = width - pad_l - pad_r, height - pad_t - pad_b

    def xy(over, runs):
        return pad_l + w * over / max_over, pad_t + h - h * runs / max_runs

    out = [f'<svg viewBox="0 0 {width} {height}" role="img" aria-label="Runs by over" '
           f'style="width:100%;height:auto">']
    for i in range(5):
        y = pad_t + h * i / 4
        out.append(f'<line x1="{pad_l}" y1="{y:.0f}" x2="{width - pad_r}" y2="{y:.0f}" '
                   f'stroke="currentColor" stroke-opacity="0.12"/>')
        out.append(f'<text x="{pad_l - 8}" y="{y + 4:.0f}" text-anchor="end" font-size="12" '
                   f'fill="currentColor" fill-opacity="0.6">{round(max_runs * (4 - i) / 4)}</text>')
    step = max(1, -(-max_over // 10))       # at most ~10 over numbers, whatever the format
    for o in range(step, max_over + 1, step):
        x, _ = xy(o, 0)
        out.append(f'<text x="{x:.0f}" y="{pad_t + h + 16:.0f}" text-anchor="middle" '
                   f'font-size="12" fill="currentColor" fill-opacity="0.6">{o}</text>')
    out.append(f'<text x="{pad_l + w / 2:.0f}" y="{height - 4}" text-anchor="middle" '
               f'font-size="12" fill="currentColor" fill-opacity="0.6">Overs</text>')
    for idx, (k, pts) in enumerate(series):
        col = colours[idx % len(colours)]
        if idx < len(labels) and labels[idx]:
            ly = pad_t + 4 + idx * 20
            out.append(f'<rect x="{pad_l + 12}" y="{ly}" width="14" height="4" rx="2" fill="{E(col)}"/>'
                       f'<text x="{pad_l + 32}" y="{ly + 6}" font-size="13" fill="currentColor">'
                       f'{E(labels[idx])}</text>')
        path = " ".join(f"{x:.1f},{y:.1f}" for x, y in (xy(0, 0), *[xy(o, r) for o, r in pts]))
        out.append(f'<polyline points="{path}" fill="none" stroke="{E(col)}" stroke-width="3" '
                   f'stroke-linejoin="round" stroke-linecap="round"/>')
    out.append("</svg>")
    return "".join(out)


def _innings_table(inn):
    rows = "".join(
        f"<tr><td>{E(b['name'])}</td><td class=num>{b['runs']}{'' if b.get('out') else '*'}</td>"
        f"<td class=num>{b['balls']}</td></tr>" for b in inn.get("batters", []))
    bowl = "".join(
        f"<tr><td>{E(b['name'])}</td><td class=num>{E(str(b['o']))}</td>"
        f"<td class=num>{b['r']}</td><td class=num>{b['w']}</td></tr>"
        for b in inn.get("bowlers", []))
    fow = ", ".join(f"{E(f['score'])} ({E(f['batter'])})" for f in inn.get("fow", []))
    return (f'<section class=card><h3>{E(inn["team"])} <span class=total>{E(inn["total"])}</span></h3>'
            f'<table><thead><tr><th>Batting</th><th class=num>R</th><th class=num>B</th></tr></thead>'
            f'<tbody>{rows}</tbody></table>'
            + (f'<table><thead><tr><th>Bowling</th><th class=num>O</th><th class=num>R</th>'
               f'<th class=num>W</th></tr></thead><tbody>{bowl}</tbody></table>' if bowl else "")
            + (f'<p class=muted>Fall of wickets: {fow}</p>' if fow else "")
            + "</section>")


def render(d):
    """d: club, title, date, competition, result, report, innings[], worm{}, colours[],
    card_png_b64, crest_b64, sponsors_b64[], video_url, accent. Missing parts are left out."""
    accent = d.get("accent") or "#2f6db3"
    report = "".join(f"<p>{E(p.strip())}</p>" for p in (d.get("report") or "").split("\n")
                     if p.strip() and not p.strip().startswith("#"))
    crest = (f'<img class=crest src="data:image/png;base64,{d["crest_b64"]}" alt="">'
             if d.get("crest_b64") else "")
    card = (f'<img class=card-img src="data:image/png;base64,{d["card_png_b64"]}" '
            f'alt="Result card">' if d.get("card_png_b64") else "")
    video = (f'<a class=watch href="{E(d["video_url"])}">&#9654; Watch the full stream</a>'
             if d.get("video_url") else "")
    sponsors = "".join(f'<img src="data:image/png;base64,{s}" alt="">'
                       for s in d.get("sponsors_b64") or [])
    worm = _worm_svg(d.get("worm") or {}, d.get("colours") or [accent, "#d9534f"],
                     [i.get("team", "") for i in d.get("innings") or []])
    innings = "".join(_innings_table(i) for i in d.get("innings") or [])
    return f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{E(d.get("title") or "Match report")}</title>
<style>
:root {{ --bg:#f6f7f9; --fg:#14181f; --muted:#5b6472; --card:#fff; --line:#e3e6eb; --accent:{E(accent)}; }}
@media (prefers-color-scheme: dark) {{ :root {{ --bg:#0f141b; --fg:#e8ecf1; --muted:#98a2b3; --card:#171e27; --line:#263241; }} }}
* {{ box-sizing:border-box }}
body {{ margin:0; background:var(--bg); color:var(--fg); font:16px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif }}
main {{ max-width:820px; margin:0 auto; padding:24px 16px 48px }}
header {{ display:flex; gap:16px; align-items:center; margin-bottom:18px }}
.crest {{ width:72px; height:72px; object-fit:contain }}
h1 {{ font-size:26px; margin:0; line-height:1.2 }}
.muted {{ color:var(--muted); font-size:14px }}
.result {{ font-size:20px; font-weight:700; color:var(--accent); margin:6px 0 0 }}
.card {{ background:var(--card); border:1px solid var(--line); border-radius:12px; padding:18px; margin:16px 0 }}
.card-img {{ width:100%; max-width:540px; display:block; margin:16px auto; border-radius:12px }}
h2 {{ font-size:18px; margin:0 0 10px }}
h3 {{ font-size:17px; margin:0 0 10px; display:flex; justify-content:space-between; gap:8px }}
.total {{ color:var(--accent) }}
table {{ width:100%; border-collapse:collapse; margin:6px 0 12px; font-size:15px }}
th, td {{ text-align:left; padding:6px 4px; border-bottom:1px solid var(--line) }}
th {{ font-size:12px; text-transform:uppercase; letter-spacing:.06em; color:var(--muted) }}
.num {{ text-align:right; width:56px; font-variant-numeric:tabular-nums }}
.watch {{ display:inline-block; margin:6px 0 0; padding:10px 16px; border-radius:8px;
         background:var(--accent); color:#fff; text-decoration:none; font-weight:700 }}
.sponsors {{ display:flex; flex-wrap:wrap; gap:18px; justify-content:center; align-items:center;
            background:#fff; border-radius:12px; padding:16px; margin-top:24px }}
.sponsors img {{ max-height:52px; max-width:150px; object-fit:contain }}
footer {{ text-align:center; margin-top:18px }}
</style></head>
<body><main>
<header>{crest}<div><h1>{E(d.get("title") or "")}</h1>
<div class=muted>{E(" · ".join(x for x in (d.get("competition"), d.get("date")) if x))}</div>
<div class=result>{E(d.get("result") or "")}</div>{video}</div></header>
{card}
{f'<section class=card><h2>Match report</h2>{report}</section>' if report else ''}
{f'<section class=card><h2>Runs by over</h2>{worm}</section>' if worm else ''}
{innings}
{f'<div class=sponsors>{sponsors}</div>' if sponsors else ''}
<footer class=muted>{E(d.get("club") or "")} · streamed live with CricketStream</footer>
</main></body></html>
"""
