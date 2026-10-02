"""Post-match result card for Instagram (1080x1350, 4:5) — Pillow only.

Pure rendering: everything it shows arrives in `facts` and `look`, so it can be rendered
and tested without a server (server.build_instagram_image gathers both). Layout, top down:

    photo (full colour, fading into the club-colour panel)  + crest / competition / date
    kicker + hero outcome word ("WIN") with the margin beside it
    two score rows: badge, club, team, big score, overs — the winner bright, the other muted
    two star performers with big stat numbers
    sponsor strip

Type is Barlow Condensed (SIL OFL, bundled in fonts/) so the card looks the same on every
club's laptop instead of whatever Arial-alike the OS has.
"""
import colorsys
import os

from PIL import Image, ImageDraw, ImageEnhance, ImageFilter, ImageFont

W, H = 1080, 1350
PAD = 60
FONT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fonts")
_WEIGHTS = {"black": "Black", "xbold": "ExtraBold", "bold": "Bold",
            "semi": "SemiBold", "medium": "Medium"}
_font_cache = {}

OUTCOME_WORD = {"win": "WIN", "loss": "DEFEAT", "tie": "TIED", "draw": "DRAW",
                "abandoned": "ABANDONED", "cancelled": "CANCELLED",
                "live": "IN PLAY"}


# ── Fonts & colour ──────────────────────────────────────────────────────────

def font(size, weight="bold"):
    key = (size, weight)
    if key not in _font_cache:
        path = os.path.join(FONT_DIR, f"BarlowCondensed-{_WEIGHTS[weight]}.ttf")
        try:
            _font_cache[key] = ImageFont.truetype(path, size)
        except OSError:
            # fonts/ missing (a partial copy of the project): Pillow's own DejaVu still
            # renders every character, just less condensed.
            try:
                _font_cache[key] = ImageFont.truetype("DejaVuSans-Bold.ttf", int(size * 0.8))
            except OSError:
                _font_cache[key] = ImageFont.load_default()
    return _font_cache[key]


def hex_rgb(h, fallback=(26, 58, 92)):
    try:
        h = str(h).strip().lstrip("#")
        return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))
    except (ValueError, IndexError):
        return fallback


def _lum(c):
    def ch(v):
        v /= 255
        return v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4
    r, g, b = (ch(v) for v in c)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def contrast(a, b):
    la, lb = sorted((_lum(a), _lum(b)), reverse=True)
    return (la + 0.05) / (lb + 0.05)


def mix(a, b, t):
    """t=0 -> a, t=1 -> b."""
    return tuple(int(round(a[i] + (b[i] - a[i]) * t)) for i in range(3))


def palette(club_hex):
    """Panel background, highlight and muted text from one club colour. The panel is always
    dark (white type sits on it); the highlight is the club colour lifted until it reads on
    that panel — a near-black navy becomes a clear mid-blue rather than vanishing, the same
    problem the overlay's wormColour() solves for the scorebar."""
    club = hex_rgb(club_hex)
    bg = mix(club, (6, 10, 18), 0.72)
    # Lighten in HSL, keeping the hue and a decent saturation: mixing towards white turned
    # a navy into steel grey, losing the club's colour exactly where it should show.
    h, l, sat = colorsys.rgb_to_hls(*(v / 255 for v in club))
    sat = max(sat, 0.55) if sat > 0.08 else sat        # greys/black/white stay neutral
    hi = club
    while contrast(hi, bg) < 4.5 and l < 0.97:
        l = min(l + 0.03, 0.97)
        hi = tuple(int(round(v * 255)) for v in colorsys.hls_to_rgb(h, l, sat))
    return {"club": club, "bg": bg, "hi": hi, "white": (255, 255, 255),
            "muted": mix((255, 255, 255), bg, 0.38), "faint": mix((255, 255, 255), bg, 0.62)}


# ── Drawing helpers ─────────────────────────────────────────────────────────

def tracked(d, xy, text, f, fill, tracking=0.0, anchor="ls"):
    """Letter-spaced text (Pillow has no tracking). tracking is in ems. Returns width."""
    x, y = xy
    widths = [d.textlength(ch, font=f) for ch in text]
    extra = f.size * tracking
    total = sum(widths) + extra * max(len(text) - 1, 0)
    if anchor[0] == "r":
        x -= total
    elif anchor[0] == "m":
        x -= total / 2
    a = "l" + anchor[1]
    for ch, w in zip(text, widths):
        d.text((x, y), ch, font=f, fill=fill, anchor=a)
        x += w + extra
    return total


def fit(d, text, weight, size, min_size, max_w):
    """Largest font (down to min_size) that fits text in max_w; ellipsis below that."""
    while size > min_size and d.textlength(text, font=font(size, weight)) > max_w:
        size -= 2
    f = font(size, weight)
    if d.textlength(text, font=f) > max_w:
        while text and d.textlength(text + "…", font=f) > max_w:
            text = text[:-1]
        text = text.rstrip() + "…"
    return text, f


def cover(img, w, h, focus_y=0.38):
    """Scale-and-crop to fill w x h. Crops keep a point ~38% down the photo in frame —
    where heads usually are in a cricket shot — rather than the dead centre."""
    s = max(w / img.width, h / img.height)
    img = img.resize((max(w, round(img.width * s)), max(h, round(img.height * s))), Image.LANCZOS)
    left = (img.width - w) // 2
    top = int(min(max(img.height * focus_y - h * 0.38, 0), img.height - h))
    return img.crop((left, top, left + w, top + h))


def vertical_alpha(h, stops):
    """An L-mode 1px-wide ramp through (position 0..1, alpha 0..255) stops, smoothstepped."""
    col = Image.new("L", (1, h))
    px = col.load()
    for y in range(h):
        t = y / max(h - 1, 1)
        for (p0, a0), (p1, a1) in zip(stops, stops[1:]):
            if p0 <= t <= p1:
                u = (t - p0) / max(p1 - p0, 1e-6)
                u = u * u * (3 - 2 * u)
                px[0, y] = int(a0 + (a1 - a0) * u)
                break
    return col


def load_rgba(path, box):
    try:
        im = Image.open(path).convert("RGBA")
        im.thumbnail(box, Image.LANCZOS)
        return im
    except (OSError, ValueError):
        return None


def initials_badge(name, size, colour):
    """Stand-in for a club with no badge in logos/: a disc with its initials."""
    im = Image.new("RGBA", (size * 2, size * 2), (0, 0, 0, 0))   # 2x, then downsampled
    d = ImageDraw.Draw(im)
    d.ellipse((4, 4, size * 2 - 4, size * 2 - 4), fill=colour + (255,),
              outline=(255, 255, 255, 200), width=6)
    skip = {"CC", "AND", "&", "THE", "CRICKET", "CLUB", "OF"}
    words = [w for w in name.replace("&", " ").split() if w.upper() not in skip]
    ini = "".join(w[0] for w in words[:2]).upper() or "?"
    d.text((size, size + 4), ini, font=font(int(size * 0.85), "black"),
           fill=(255, 255, 255), anchor="mm")
    return im.resize((size, size), Image.LANCZOS)


# ── The card ────────────────────────────────────────────────────────────────

def render(facts, look, out_path):
    """facts: outcome, margin, kicker, sides[2], stars[], competition, date, ground.
    look: club_colour, crest (path), photo (path), logos_dir, sponsors (paths),
          club_name. Missing pieces are skipped, never fatal. Returns out_path."""
    P = palette(look.get("club_colour", "#1a3a5c"))
    img = Image.new("RGB", (W, H), P["bg"])
    photo_h = 820

    # Backdrop: the photo in full colour, fading into the panel; else the club colour
    # with the crest as a large watermark, so the top never sits empty.
    photo = None
    if look.get("photo"):
        try:
            photo = Image.open(look["photo"]).convert("RGB")
        except (OSError, ValueError):
            photo = None
    if photo:
        photo = cover(photo, W, photo_h)
        photo = ImageEnhance.Contrast(photo).enhance(1.08)
        photo = ImageEnhance.Color(photo).enhance(1.06)
        mask = vertical_alpha(photo_h, [(0, 255), (0.55, 255), (1, 0)]).resize((W, photo_h))
        img.paste(photo, (0, 0), mask)
    else:
        layer = Image.new("RGBA", (W, H), (0, 0, 0, 0))
        dl = ImageDraw.Draw(layer)
        dl.polygon([(W * 0.30, 0), (W, 0), (W, H * 0.48), (W * 0.62, H * 0.62)],
                   fill=P["club"] + (150,))
        for x in range(-H, W, 34):
            dl.line([(x, H * 0.62), (x + H * 0.62, 0)], fill=(255, 255, 255, 9), width=2)
        img = Image.alpha_composite(img.convert("RGBA"), layer)
        wm = load_rgba(look.get("crest") or "", (640, 640))
        if wm:
            # Badges in logos/ are ~180px and thumbnail() never enlarges; soft is fine at 16%.
            # Ends above y=760 so the hero scrim fades it out instead of cutting it off.
            k = 640 / max(wm.size)
            wm = wm.resize((round(wm.width * k), round(wm.height * k)), Image.LANCZOS)
            wm.putalpha(wm.getchannel("A").point(lambda v: int(v * 0.16)))
            img.paste(wm, (W - wm.width + 100, 110), wm)
        img = img.convert("RGB")

    # Header scrim so white type holds over a bright sky.
    scrim = Image.new("RGB", (W, 260), (0, 0, 0))
    img.paste(scrim, (0, 0), vertical_alpha(260, [(0, 150), (1, 0)]).resize((W, 260)))
    # Bottom scrim under the hero word, over the photo's fade.
    hero_mask = vertical_alpha(360, [(0, 0), (1, 170)]).resize((W, 360))
    img.paste(Image.new("RGB", (W, 360), P["bg"]), (0, photo_h - 360), hero_mask)

    d = ImageDraw.Draw(img)

    # ── Header: crest + club + competition | date + ground ──
    x = PAD
    crest = load_rgba(look.get("crest") or "", (104, 104))
    if crest:
        img.paste(crest, (PAD, 44), crest)
        x = PAD + crest.width + 22
    date = (facts.get("date") or "").upper()
    date_w = d.textlength(date, font=font(34, "xbold")) if date else 0
    room = W - PAD - x - (date_w + 40 if date else 0)
    club, f_club = fit(d, (look.get("club_name") or "").upper(), "xbold", 38, 26, room)
    d.text((x, 92), club, font=f_club, fill=P["white"], anchor="ls")
    comp = (facts.get("competition") or "").upper()
    if comp:
        comp, f_comp = fit(d, comp, "semi", 28, 22, room)
        tracked(d, (x, 130), comp, f_comp, P["hi"] if not photo else P["white"], 0.04)
    if date:
        d.text((W - PAD, 92), date, font=font(34, "xbold"), fill=P["white"], anchor="rs")
        ground = (facts.get("ground") or "").upper()
        if ground:
            g, fg = fit(d, ground, "medium", 26, 20, 360)
            d.text((W - PAD, 128), g, font=fg, fill=(235, 238, 245), anchor="rs")

    # ── Hero: kicker, outcome word, margin ──
    word = OUTCOME_WORD.get(facts.get("outcome", ""), "RESULT")
    margin = (facts.get("margin") or "").upper()
    m_lines = _split_margin(margin)
    f_m = font(78, "xbold")
    m_w = max((d.textlength(l, font=f_m) for l in m_lines), default=0)
    hero_size = 270
    while hero_size > 120 and d.textlength(word, font=font(hero_size, "black")) + \
            (m_w + 34 if m_lines else 0) > W - 2 * PAD:
        hero_size -= 6
    f_hero = font(hero_size, "black")
    base = 790                                   # hero baseline
    kicker = (facts.get("kicker") or "MATCH RESULT").upper()
    kx = PAD
    d.rectangle([kx, base - hero_size * 0.74 - 52, kx + 46, base - hero_size * 0.74 - 44],
                fill=P["hi"])
    ky = base - hero_size * 0.74 - 34
    tracked(d, (kx + 62, ky + 2), kicker, font(30, "bold"), (0, 0, 0), 0.14)   # over a photo
    tracked(d, (kx + 60, ky), kicker, font(30, "bold"), P["white"], 0.14)
    _shadowed(img, (PAD - 6, base), word, f_hero, P["white"])
    d = ImageDraw.Draw(img)
    if m_lines:
        mx = PAD - 6 + d.textlength(word, font=f_hero) + 34
        ly = base - (len(m_lines) - 1) * 72
        for line in m_lines:
            d.text((mx, ly), line, font=f_m, fill=P["hi"], anchor="ls")
            ly += 72

    # ── Score rows ──
    y = 834
    sides = (facts.get("sides") or [])[:2]
    row_h, gap = 118, 14
    decided = facts.get("outcome") in ("win", "loss")
    for side in sides:
        _score_row(img, P, side, y, row_h, look, dim=decided and not side.get("won"))
        y += row_h + gap
    d = ImageDraw.Draw(img)

    # ── Stars ──
    stars = [s for s in (facts.get("stars") or []) if s.get("big")][:2]
    sponsors = [p for p in (look.get("sponsors") or []) if p]
    strip_h = 104 if sponsors else 0
    if stars:
        top = y + 12
        col_w = (W - 2 * PAD) / len(stars)
        for i, s in enumerate(stars):
            sx = PAD + i * col_w + (34 if i else 0)
            if i:
                d.line([(PAD + i * col_w, top + 8), (PAD + i * col_w, top + 128)],
                       fill=P["faint"], width=2)
            tracked(d, (sx, top + 26), (s.get("label") or "").upper(), font(25, "bold"),
                    P["hi"], 0.12)
            big = s["big"]
            f_big = font(92, "black")
            d.text((sx - 2, top + 112), big, font=f_big, fill=P["white"], anchor="ls")
            bx = sx + d.textlength(big, font=f_big) + 12
            if s.get("small"):
                d.text((bx, top + 112), s["small"].upper(), font=font(36, "bold"), fill=P["muted"],
                       anchor="ls")
            # Name to the right of the number, detail beneath it:  87  P SMITH
            #                                                          (94)
            name, f_n = fit(d, (s.get("name") or "").upper(), "bold", 36, 24,
                            PAD + (i + 1) * col_w - bx - 24)
            d.text((bx, top + 72), name, font=f_n, fill=P["white"], anchor="ls")

    # ── Sponsor strip ──
    if sponsors:
        sy = H - strip_h
        d.rectangle([0, sy, W, H], fill=(246, 247, 249))
        d.rectangle([0, sy, W, sy + 6], fill=P["club"])
        logos = [l for l in (load_rgba(p, (400, strip_h - 40)) for p in sponsors) if l]
        if logos:
            gap_x = 40
            avail = W - 2 * PAD
            scale = min(1.0, (avail - gap_x * (len(logos) - 1)) / sum(l.width for l in logos))
            if scale < 1:
                logos = [l.resize((max(1, int(l.width * scale)), max(1, int(l.height * scale))),
                                  Image.LANCZOS) for l in logos]
            total = sum(l.width for l in logos) + gap_x * (len(logos) - 1)
            lx = (W - total) // 2
            for l in logos:
                img.paste(l, (lx, sy + 3 + (strip_h - l.height) // 2), l)
                lx += l.width + gap_x

    img.save(out_path, "PNG")
    return out_path


def _split_margin(margin):
    """'BY 46 RUNS' -> ['BY 46', 'RUNS'], stacked beside the hero word like a score bug."""
    words = margin.split()
    if not words:
        return []
    if len(words) <= 2:
        return [margin]
    return [" ".join(words[:-1]), words[-1]]


def _shadowed(img, xy, text, f, fill):
    """Hero type with a soft shadow so it holds over any part of a photo."""
    sh = Image.new("L", img.size, 0)
    ImageDraw.Draw(sh).text((xy[0] + 4, xy[1] + 8), text, font=f, fill=150, anchor="ls")
    sh = sh.filter(ImageFilter.GaussianBlur(14))
    img.paste((0, 0, 0), (0, 0), sh)
    ImageDraw.Draw(img).text(xy, text, font=f, fill=fill, anchor="ls")


def _score_row(img, P, side, y, row_h, look, dim):
    layer = Image.new("RGBA", img.size, (0, 0, 0, 0))
    dl = ImageDraw.Draw(layer)
    dl.rounded_rectangle([PAD - 20, y, W - PAD + 20, y + row_h], radius=14,
                         fill=(255, 255, 255, 14 if dim else 26))
    if side.get("won"):
        dl.rounded_rectangle([PAD - 20, y, PAD - 12, y + row_h], radius=4, fill=P["hi"] + (255,))
    img.paste(Image.alpha_composite(img.convert("RGBA"), layer).convert("RGB"))
    d = ImageDraw.Draw(img)
    text_c = P["muted"] if dim else P["white"]
    sub_c = P["faint"] if dim else P["muted"]

    badge_size = 84
    badge = None
    cid = str(side.get("club_id") or "").strip()
    if cid and look.get("logos_dir"):
        for ext in (".png", ".webp", ".jpg", ".jpeg"):
            p = os.path.join(look["logos_dir"], cid + ext)
            if os.path.exists(p):
                badge = load_rgba(p, (badge_size, badge_size))
                break
    if badge is None:
        badge = initials_badge(side.get("club") or "?", badge_size,
                               P["club"] if side.get("ours") else (90, 98, 112))
    if dim:
        badge.putalpha(badge.getchannel("A").point(lambda v: int(v * 0.6)))
    bx = PAD + 6
    img.paste(badge, (bx + (badge_size - badge.width) // 2, y + (row_h - badge.height) // 2), badge)

    # Score on the right, then the name gets whatever width is left.
    score = side.get("score") or ""
    f_s = font(84, "black")
    sw = d.textlength(score, font=f_s)
    d.text((W - PAD, y + 80), score, font=f_s, fill=text_c, anchor="rs")
    if side.get("overs"):
        d.text((W - PAD, y + 108), side["overs"].upper(), font=font(24, "semi"), fill=sub_c,
               anchor="rs")
    nx = bx + badge_size + 26
    room = W - PAD - sw - 36 - nx
    name, f_n = fit(d, (side.get("club") or "").upper(), "xbold", 46, 30, room)
    has_team = bool(side.get("team"))
    d.text((nx, y + (64 if has_team else 76)), name, font=f_n, fill=text_c, anchor="ls")
    if has_team:
        tracked(d, (nx, y + 98), side["team"].upper(), font(25, "semi"), sub_c, 0.08)
