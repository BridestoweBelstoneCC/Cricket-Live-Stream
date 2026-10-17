"""Render the CricketStream brand assets to brand/.

Pillow only, bundled fonts only (same rule as result_card.py), so anyone can
regenerate them after a tweak:  python scripts/make_brand.py

The mark is a cricket ball sending out a broadcast signal: the two things the
project is about. Everything is drawn at 4x and scaled down, because Pillow's
ellipses and arcs aren't anti-aliased.
"""
import math
import os

from PIL import Image, ImageDraw, ImageFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "brand")
FONTS = os.path.join(ROOT, "fonts")

# The project website's greens (docs/index.html) plus a cricket-ball red.
GREEN_DARK = (16, 52, 30)
GREEN = (29, 82, 48)
GREEN_MID = (42, 110, 63)
GREEN_BRIGHT = (126, 214, 150)
BALL = (196, 22, 44)
BALL_SHADE = (138, 10, 28)
SEAM = (250, 244, 236)
SEAM_LINE = (112, 6, 22)
WHITE = (255, 255, 255)
INK = (26, 26, 26)
SS = 4  # supersampling factor


def font(weight, size):
    return ImageFont.truetype(os.path.join(FONTS, f"BarlowCondensed-{weight}.ttf"), size)


def vgradient(w, h, top, bottom):
    g = Image.new("RGB", (1, h))
    for y in range(h):
        t = y / max(h - 1, 1)
        g.putpixel((0, y), tuple(round(a + (b - a) * t) for a, b in zip(top, bottom)))
    return g.resize((w, h))


def draw_mark(size, transparent=False):
    """The square icon: rounded tile, ball, three signal arcs."""
    S = size * SS
    tile = vgradient(S, S, GREEN_MID, GREEN_DARK).convert("RGBA")
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, S - 1, S - 1), radius=int(S * 0.22), fill=255)
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    if not transparent:
        img.paste(tile, (0, 0), mask)
    d = ImageDraw.Draw(img)

    cx, cy, r = S * 0.40, S * 0.60, S * 0.235
    # Flat-style ball, drawn on its own layer and clipped to the circle: a
    # lighter face offset up-left leaves a crescent of shade at the lower right.
    ball = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    bd = ImageDraw.Draw(ball)
    bd.ellipse((cx - r, cy - r, cx + r, cy + r), fill=BALL_SHADE)
    fr, fx, fy = r * 0.89, cx - r * 0.12, cy - r * 0.12
    bd.ellipse((fx - fr, fy - fr, fx + fr, fy + fr), fill=BALL)

    # Seam: one thin darker line, a gentle arc of a much larger circle, with
    # two rows of slanted round-ended stitches either side of it.
    big = r * 2.4
    phi = math.radians(-12)  # direction to the arc's centre: leans with the flight
    scx, scy = cx + math.cos(phi) * (big - r * 0.10), cy + math.sin(phi) * (big - r * 0.10)
    bd.ellipse((scx - big, scy - big, scx + big, scy + big), outline=SEAM_LINE, width=int(r * 0.042))
    dash, dash_w = r * 0.085, r * 0.032
    step = r * 0.125 / big
    for k in range(-12, 13):  # runs off the ball's edge; the clip trims it
        a = phi + math.pi + k * step
        slant = a + math.radians(60)
        for off in (-0.17, -0.075, 0.075, 0.17):
            px = scx + math.cos(a) * (big + r * off)
            py = scy + math.sin(a) * (big + r * off)
            hx, hy = math.cos(slant) * dash / 2, math.sin(slant) * dash / 2
            bd.line((px - hx, py - hy, px + hx, py + hy), fill=SEAM, width=int(dash_w))
            for ex, ey in ((px - hx, py - hy), (px + hx, py + hy)):
                bd.ellipse((ex - dash_w / 2, ey - dash_w / 2, ex + dash_w / 2, ey + dash_w / 2), fill=SEAM)
    ball_mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(ball_mask).ellipse((cx - r, cy - r, cx + r, cy + r), fill=255)
    img.paste(ball, (0, 0), Image.composite(ball.split()[3], Image.new("L", (S, S), 0), ball_mask))

    # Broadcast arcs, round-capped, fanning up and right from the ball.
    width = int(S * 0.058)
    a0, a1 = -62, -8
    for i, rad in enumerate((r * 1.45, r * 1.95, r * 2.45)):
        colour = WHITE if i < 2 else GREEN_BRIGHT
        d.arc((cx - rad, cy - rad, cx + rad, cy + rad), a0, a1, fill=colour, width=width)
        for a in (a0, a1):
            mid = rad - width / 2
            px, py = cx + math.cos(math.radians(a)) * mid, cy + math.sin(math.radians(a)) * mid
            d.ellipse((px - width / 2, py - width / 2, px + width / 2, py + width / 2), fill=colour)
    return img.resize((size, size), Image.LANCZOS)


def wordmark_width(h):
    return font("Black", h).getlength("CRICKET") + font("Black", h).getlength("STREAM")


def draw_wordmark(d, x, y, h, dark_bg=True):
    """CRICKET in white (or ink), STREAM in green: one word, two weights of colour."""
    f = font("Black", h)
    first = WHITE if dark_bg else INK
    second = GREEN_BRIGHT if dark_bg else GREEN_MID
    d.text((x, y), "CRICKET", font=f, fill=first, anchor="ls")
    d.text((x + f.getlength("CRICKET"), y), "STREAM", font=f, fill=second, anchor="ls")


def lockup(dark_bg=True):
    """Icon + wordmark on one line, for README headers and the website."""
    H = 240
    icon = draw_mark(H)
    text_h = int(H * 0.62)
    w = H + int(H * 0.18) + int(wordmark_width(text_h)) + 10
    img = Image.new("RGBA", (w, H), (0, 0, 0, 0))
    img.paste(icon, (0, 0), icon)
    d = ImageDraw.Draw(img)
    draw_wordmark(d, H + int(H * 0.18), int(H * 0.5 + text_h * 0.36), text_h, dark_bg)
    return img


def banner(w, h, tagline=True, strip=None):
    """A wide card: gradient, mark, wordmark, tagline, optional scorebar strip."""
    img = vgradient(w, h, GREEN, GREEN_DARK).convert("RGBA")
    # A faint pitch: two crease lines, so the field isn't flat colour. Drawn on
    # their own layer: ImageDraw replaces alpha rather than blending it.
    lines = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    for fx in (0.08, 0.92):
        ImageDraw.Draw(lines).line((w * fx, 0, w * fx, h), fill=(255, 255, 255, 22),
                                   width=max(2, w // 400))
    img = Image.alpha_composite(img, lines)
    d = ImageDraw.Draw(img)
    icon_s = int(h * (0.34 if strip else 0.42))
    text_h = int(icon_s * 0.62)
    # Shrink the whole lockup if it wouldn't fit inside the crease lines.
    while icon_s + int(icon_s * 0.18) + wordmark_width(text_h) > w * 0.76:
        icon_s -= 2
        text_h = int(icon_s * 0.62)
    block_w = icon_s + int(icon_s * 0.18) + int(wordmark_width(text_h))
    top = int(h * (0.16 if strip else 0.22))
    x = (w - block_w) // 2
    icon = draw_mark(icon_s)
    img.paste(icon, (x, top), icon)
    draw_wordmark(d, x + icon_s + int(icon_s * 0.18), int(top + icon_s * 0.5 + text_h * 0.36), text_h)
    if tagline:
        f = font("SemiBold", int(min(h * 0.075, w * 0.032)))
        d.text((w / 2, top + icon_s + h * 0.13), "FREE BROADCAST GRAPHICS FOR GRASSROOTS CRICKET",
               font=f, fill=(225, 240, 230), anchor="ms")
    if strip:
        bar = Image.open(strip).convert("RGBA")
        bw = int(w * 0.92)
        bar = bar.resize((bw, int(bar.height * bw / bar.width)), Image.LANCZOS)
        y = h - bar.height - int(h * 0.09)
        d.rounded_rectangle((((w - bw) // 2) - 6, y - 6, ((w + bw) // 2) + 6, y + bar.height + 6),
                            radius=8, fill=(0, 0, 0, 90))
        img.paste(bar, ((w - bw) // 2, y), bar)
    return img.convert("RGB")


def main():
    os.makedirs(OUT, exist_ok=True)
    draw_mark(1024).save(os.path.join(OUT, "icon-1024.png"))
    draw_mark(512).save(os.path.join(OUT, "icon-512.png"))
    # The browser-tab icon server.py serves at /favicon.ico, and the Windows exe's icon.
    draw_mark(64).save(os.path.join(OUT, "favicon-64.png"))
    draw_mark(256).save(os.path.join(OUT, "icon.ico"), sizes=[(16, 16), (24, 24), (32, 32),
                                                          (48, 48), (64, 64), (128, 128), (256, 256)])
    draw_mark(1024, transparent=True).save(os.path.join(OUT, "mark-transparent.png"))
    lockup(dark_bg=True).save(os.path.join(OUT, "logo-dark-bg.png"))
    lockup(dark_bg=False).save(os.path.join(OUT, "logo-light-bg.png"))
    strip = os.path.join(ROOT, "examples", "scorebar-modern.png")
    banner(1200, 630, strip=strip).save(os.path.join(OUT, "social-card-1200x630.png"))
    banner(960, 540, strip=strip).save(os.path.join(OUT, "discord-banner-960x540.png"))
    banner(2560, 1440, strip=strip).save(os.path.join(OUT, "youtube-banner-2560x1440.png"))
    banner(1500, 500, tagline=True).save(os.path.join(OUT, "header-1500x500.png"))
    # GitHub Pages serves only docs/, so the website needs its own copies.
    docs = os.path.join(ROOT, "docs")
    draw_mark(32).save(os.path.join(docs, "favicon-32.png"))
    draw_mark(180).save(os.path.join(docs, "apple-touch-icon.png"))
    draw_mark(96).save(os.path.join(docs, "icon-96.png"))
    banner(1200, 630, strip=strip).save(os.path.join(docs, "social-card.png"))
    print("Wrote brand assets to", OUT, "and the website's copies to", docs)


if __name__ == "__main__":
    main()
