#!/usr/bin/env python3
"""Syntax-checks the JavaScript embedded in server.py's CONTROL_HTML and in overlay.html.

Why this imports server.py instead of regexing the raw source: CONTROL_HTML is a plain
Python triple-quoted string, so a single backslash meant for the embedded JS (e.g. an
escaped quote inside a JS string literal) gets silently eaten by Python's own string
escaping before the browser ever sees it. A text-based check over the raw source can't
detect that -- it has to look at what Python actually produces at runtime. Importing the
module gets us the real, evaluated CONTROL_HTML string.

Run with no arguments. Exits non-zero (and prints the JS engine's error) on any syntax error.
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def extract_scripts(html):
    return re.findall(r"<script[^>]*>(.*?)</script>", html, re.S)


def _check_with_node(code):
    with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as f:
        f.write(code)
        path = f.name
    try:
        r = subprocess.run(["node", "--check", path], capture_output=True, text=True)
        return r.returncode == 0, r.stderr.strip()
    finally:
        os.unlink(path)


def _check_with_jsc(code):
    """macOS fallback: run the code through a `new Function(...)` parse in JavaScriptCore
    via JXA, since it's a real, modern engine present on every Mac with no extra install."""
    with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as f:
        f.write(code)
        js_path = f.name
    jxa = """
ObjC.import('Foundation');
function readFile(path) {
  const data = $.NSString.stringWithContentsOfFileEncodingError($(path), $.NSUTF8StringEncoding, null);
  return ObjC.unwrap(data);
}
try {
  new Function(readFile(%r));
  console.log('OK');
} catch (e) {
  console.log('ERROR: ' + e);
}
""" % js_path
    try:
        r = subprocess.run(["osascript", "-l", "JavaScript", "-e", jxa], capture_output=True, text=True)
        # console.log from `osascript -e` lands on stderr, not stdout -- check both.
        out = (r.stdout.strip() + r.stderr.strip())
        if out == "OK":
            return True, ""
        return False, out
    finally:
        os.unlink(js_path)


def _check_with_esprima(code):
    """Last-resort fallback: esprima is ES2017-ish and doesn't know ?? / ?., so those are
    normalized to syntax it can parse first. This can't validate that specific syntax, but
    still catches everything else (missing operators, unbalanced brackets, stray strings)."""
    import esprima
    normalized = code.replace("??", "||")
    normalized = re.sub(r"(?<=[A-Za-z0-9_\)\]])\?\.", ".", normalized)
    try:
        esprima.parseScript(normalized, tolerant=False)
        return True, ""
    except Exception as e:
        return False, str(e)


def check_js(label, code):
    if shutil.which("node"):
        ok, detail = _check_with_node(code)
    elif sys.platform == "darwin":
        ok, detail = _check_with_jsc(code)
    else:
        try:
            ok, detail = _check_with_esprima(code)
            detail = (detail + "\n" if detail else "") + "(checked with esprima -- ?? / ?. syntax not validated)"
        except ImportError:
            print(f"  SKIP {label}: no JS checker available (no node, not on macOS, no esprima installed)")
            return True
    print(f"  {'OK  ' if ok else 'FAIL'} {label}")
    if not ok:
        print(f"        {detail}")
    return ok


def main():
    sys.path.insert(0, REPO_ROOT)
    import server  # noqa: E402 -- import here so REPO_ROOT is on sys.path first

    all_ok = True

    scripts = extract_scripts(server.CONTROL_HTML)
    if not scripts:
        print("  FAIL CONTROL_HTML: found no <script> blocks at all -- extraction is broken")
        all_ok = False
    for i, code in enumerate(scripts):
        if not check_js(f"CONTROL_HTML script #{i+1}", code):
            all_ok = False

    overlay_path = os.path.join(REPO_ROOT, "overlay.html")
    overlay_html = open(overlay_path, encoding="utf-8").read()
    overlay_scripts = extract_scripts(overlay_html)
    if not overlay_scripts:
        print("  FAIL overlay.html: found no <script> blocks at all -- extraction is broken")
        all_ok = False
    for i, code in enumerate(overlay_scripts):
        if not check_js(f"overlay.html script #{i+1}", code):
            all_ok = False

    sys.exit(0 if all_ok else 1)


if __name__ == "__main__":
    main()
