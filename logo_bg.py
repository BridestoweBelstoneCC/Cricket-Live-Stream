"""
Making a sponsor logo's background transparent
───────────────────────────────────────────────
Most sponsor logos arrive as a picture on a plain white (or other solid) background,
which shows as a hard box on the stream's dark sponsor strap. This removes that
background — not with any clever image recognition, but the way a person would with a
"magic wand" from the edges:

  1. Work out the background colour from the image's border.
  2. Find every pixel of roughly that colour that's CONNECTED to the border, and make it
     transparent. White inside the logo (the hole in an "o", a badge's white middle)
     stays, because it isn't reachable from the edge without crossing the logo itself.
  3. Soften the cut edge by a pixel and take the old background's tint out of the edge
     pixels, so a white logo background doesn't leave a pale halo on a dark strap.

It refuses, rather than guess, when there's no plain background to find (a photo, a
gradient, a logo that's already transparent) or when it would remove nearly everything.
It never edits the original: the caller saves the result as a new image.

Pillow only (no numpy) — Pillow is already a requirement, numpy isn't.
"""
from PIL import Image, ImageChops, ImageDraw, ImageFilter

MAX_SIDE = 1200        # bigger is pointless (the strap shows logos at most 480x170) and slow
TOLERANCE = 48         # how far from the background colour still counts as background
PLAIN_BORDER = 0.80    # share of the border that must be background-coloured
MAX_REMOVED = 0.97     # refusing beyond this: it's eaten the logo, not just its background


def _border_pixels(im):
    w, h = im.size
    px = im.load()
    pts = ([(x, 0) for x in range(w)] + [(x, h - 1) for x in range(w)] +
           [(0, y) for y in range(1, h - 1)] + [(w - 1, y) for y in range(1, h - 1)])
    return pts, [px[p] for p in pts]


def _dist(a, b):
    return max(abs(a[0] - b[0]), abs(a[1] - b[1]), abs(a[2] - b[2]))


def background_colour(im):
    """(rgb, None) for an RGBA image whose border is a plain colour, else (None, reason)."""
    _, border = _border_pixels(im)
    opaque = [p for p in border if p[3] > 200]
    if len(opaque) < 0.5 * len(border):
        return None, "This logo's background is already transparent."
    # Per-channel median of the border: robust to a few pixels of logo touching the edge.
    bg = tuple(sorted(p[c] for p in opaque)[len(opaque) // 2] for c in range(3))
    near = sum(1 for p in opaque if _dist(p, bg) <= TOLERANCE) / len(opaque)
    if near < PLAIN_BORDER:
        return None, ("There's no plain background around the edge to remove (a photo or a "
                      "gradient needs a proper image editor).")
    return bg, None


MODES = ("outside", "everywhere")


def remove_background(im, mode="outside"):
    """(new RGBA image, share removed, None) or (None, 0, reason).

    mode "outside": only background connected to the image's edge. Keeps white INSIDE the
    logo — right for a badge with a white middle (verified on a real sponsor's round
    badge), wrong for a wordmark, whose letter holes ("o", "B", "A") stay white.
    mode "everywhere": every background-coloured pixel, letter holes included — right
    for a wordmark, wrong for a badge (it would clear the badge's middle too). No single
    rule gets both, so the panel shows both and the operator picks by eye."""
    im = im.convert("RGBA")
    if max(im.size) > MAX_SIDE:
        im.thumbnail((MAX_SIDE, MAX_SIDE), Image.LANCZOS)
    bg, why = background_colour(im)
    if bg is None:
        return None, 0.0, why
    w, h = im.size

    # Distance from the background colour, per pixel: the largest channel difference.
    diff = ImageChops.difference(im.convert("RGB"), Image.new("RGB", im.size, bg))
    r, g, b = diff.split()
    dist = ImageChops.lighter(ImageChops.lighter(r, g), b)

    # Candidates are background-coloured; only those connected to the border are removed.
    cand = dist.point(lambda v: 255 if v <= TOLERANCE else 0)
    if mode == "everywhere":
        region = cand
    else:
        pts, _ = _border_pixels(cand.convert("RGBA"))
        px = cand.load()
        for xy in pts:
            if px[xy] == 255:
                ImageDraw.floodfill(cand, xy, 128)
        region = cand.point(lambda v: 255 if v == 128 else 0)

    removed = region.histogram()[255] / float(w * h)
    if removed > MAX_REMOVED:
        return None, removed, ("That would remove almost the whole image — the logo seems to "
                               "be the same colour as its background.")
    if removed == 0:
        return None, 0.0, "Couldn't find any background to remove."

    # Soft edge. The removed region is fully transparent. In a thin band of logo pixels
    # around it, an anti-aliased pixel is part logo, part background: its transparency is
    # judged from how close its colour is to the background (just past the tolerance:
    # nearly clear; twice the tolerance or more: solid). Not a blur of the cut-out: that
    # also gave the pure background just OUTSIDE the edge some opacity — a faint white halo
    # on the dark strap, caught by tests/test_logo_bg.py. The band is only a couple of
    # pixels wide, so white well inside a logo (a badge's middle) is never touched.
    #
    # "How close to the background" is measured against the logo colour right beside the
    # pixel (the strongest colour within 2px), not a fixed scale: a pixel halfway from
    # white to that local colour is half transparent. A fixed scale counted strong colours
    # (gold on white) as solid too early and left a tint on their edges.
    band = ImageChops.subtract(region.filter(ImageFilter.MaxFilter(5)), region)
    local = dist.filter(ImageFilter.MaxFilter(5))
    keep = ImageChops.invert(region)

    # Un-matte the part-transparent edge as each pixel's opacity is set: take the
    # background's share back out of its colour, so half-gold-half-white becomes
    # half-transparent gold.
    out = im.copy()
    opx, kpx, bpx = out.load(), keep.load(), band.load()
    dpx, lpx = dist.load(), local.load()
    for y in range(h):
        for x in range(w):
            if not bpx[x, y]:
                continue
            reach = lpx[x, y]
            f = min(1.0, dpx[x, y] / reach) if reach > TOLERANCE else 1.0
            kpx[x, y] = round(f * 255)
            if 0 < f < 1:
                c = opx[x, y]
                opx[x, y] = tuple(max(0, min(255, round((c[i] - (1 - f) * bg[i]) / f)))
                                  for i in range(3)) + (c[3],)
    alpha = ImageChops.multiply(im.getchannel("A"), keep)
    out.putalpha(alpha)
    return out, removed, None


def has_plain_background(im):
    """Quick check for the panel's "make it transparent?" suggestion after an upload."""
    try:
        return background_colour(im.convert("RGBA"))[0] is not None
    except Exception:
        return False
