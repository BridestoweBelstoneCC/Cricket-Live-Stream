"""Vertical (9:16) social clips from replay clips — for Shorts, Reels and TikTok.

Each tagged replay (wicket, six, fifty…) becomes a 1080x1920 video: the replay itself in
the middle (it's a recording of the stream, so the scorebar is already in it), a blurred
enlargement of it filling the frame behind, a big headline ("SIX!"), a short line about
the moment, and the club badge and channel handle at the foot.

Pure ffmpeg; stdlib only, so it's testable without a server. Rendering runs at below-
normal priority: it's meant for after the match, but it must never starve OBS if someone
runs it while still streaming.
"""
import os
import subprocess
import textwrap

W, H = 1080, 1920
FONT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fonts")
HEADLINE_FONT = os.path.join(FONT_DIR, "BarlowCondensed-Black.ttf")
TEXT_FONT = os.path.join(FONT_DIR, "BarlowCondensed-SemiBold.ttf")

HEADLINES = {"wicket": "WICKET!", "boundary": "FOUR!", "four": "FOUR!", "six": "SIX!",
             "fifty": "FIFTY!", "century": "CENTURY!", "hat-trick": "HAT-TRICK!",
             "five-for": "FIVE-FOR!"}


def headline_for(reason):
    """'wicket' -> 'WICKET!', 'Fifty - Smith' -> 'FIFTY!'; unknown reasons upper-cased."""
    r = (reason or "").strip().lower()
    for key, text in HEADLINES.items():
        if r == key or r.startswith(key):
            return text
    return (reason or "").strip().upper()[:16] or "HIGHLIGHT"


def wrap_lines(text, width=30, max_lines=3):
    """Pre-wrapped lines for drawtext, which can't wrap or centre a paragraph itself."""
    lines = textwrap.wrap((text or "").strip(), width=width)
    if len(lines) > max_lines:
        lines = lines[:max_lines]
        lines[-1] = lines[-1].rstrip(" .,;") + "…"
    return lines


def _ff(path):
    """A path made safe inside an ffmpeg filter option (Windows drive colons)."""
    return path.replace("\\", "/").replace(":", "\\:")


def build_filter(text_files, accent="0xFFFFFF", has_badge=False):
    """The filter graph. text_files: {"headline": path, "lines": [paths], "handle": path or
    None}. Text goes in via textfile= so no caption can break the filter syntax.
    Returns (filter string, final output label)."""
    f = []
    f.append("[0:v]split=2[a][b]")
    f.append(f"[a]scale={W}:{H}:force_original_aspect_ratio=increase,crop={W}:{H},"
             "gblur=sigma=40,eq=brightness=-0.15[bg]")
    f.append(f"[b]scale={W}:-2[fg]")
    f.append("[bg][fg]overlay=(W-w)/2:(H-h)/2-60[v0]")
    cur, n = "v0", 0

    def text(path, font, size, y, colour, extra=""):
        nonlocal cur, n
        n += 1
        nxt = f"v{n}"
        f.append(f"[{cur}]drawtext=fontfile='{_ff(font)}':textfile='{_ff(path)}'"
                 f":fontsize={size}:fontcolor={colour}:x=(w-text_w)/2:y={y}{extra}[{nxt}]")
        cur = nxt

    text(text_files["headline"], HEADLINE_FONT, 190, 250, accent,
         ":shadowcolor=black@0.55:shadowx=0:shadowy=8")
    for i, p in enumerate(text_files.get("lines") or []):
        text(p, TEXT_FONT, 62, 1290 + i * 78, "white",
             ":box=1:boxcolor=black@0.45:boxborderw=14")
    if has_badge:
        n += 1
        f.append("[1:v]scale=170:-1[badge]")
        f.append(f"[{cur}][badge]overlay=(W-w)/2:1580[v{n}]")
        cur = f"v{n}"
    if text_files.get("handle"):
        text(text_files["handle"], TEXT_FONT, 42, 1790, "white@0.85")
    return ";".join(f), cur


def render(src, out, headline, line, workdir, badge=None, handle="", accent="0xFFFFFF",
           timeout=300):
    """Render one vertical clip. Returns (ok, message). Never raises."""
    try:
        os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
        stem = os.path.splitext(os.path.basename(out))[0]

        def write(name, value):
            p = os.path.join(workdir, f"{stem}_{name}.txt")
            with open(p, "w", encoding="utf-8") as fh:
                fh.write(value)
            return p

        files = {"headline": write("headline", headline),
                 "lines": [write(f"line{i}", l) for i, l in enumerate(wrap_lines(line))],
                 "handle": write("handle", handle) if handle else None}
        has_badge = bool(badge and os.path.exists(badge))
        graph, last = build_filter(files, accent, has_badge)
        cmd = ["ffmpeg", "-y", "-i", src]
        if has_badge:
            cmd += ["-i", badge]
        cmd += ["-filter_complex", graph, "-map", f"[{last}]", "-map", "0:a?",
                "-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p",
                "-r", "30", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", out]
        flags = 0x00004000 if os.name == "nt" else 0       # BELOW_NORMAL_PRIORITY_CLASS
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout,
                           creationflags=flags)
        if r.returncode != 0:
            tail = (r.stderr or "").strip().splitlines()[-1:] or ["ffmpeg failed"]
            return False, tail[0][:200]
        return True, out
    except subprocess.TimeoutExpired:
        return False, "ffmpeg took too long"
    except Exception as e:
        return False, str(e)[:200]
