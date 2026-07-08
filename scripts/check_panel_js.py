#!/usr/bin/env python3
"""Syntax-checks the JavaScript embedded in control.html and overlay.html.

History note: the control panel used to live inside server.py as a triple-quoted Python
string (CONTROL_HTML), where Python's own escaping could silently eat a backslash meant
for the JS — so this script had to import server.py and check the *evaluated* string.
The panel now lives in control.html as a plain file (normal JS escaping applies), so both
pages are checked straight from disk, and running this has zero import side effects.

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
    all_ok = True
    for fname in ("control.html", "overlay.html"):
        path = os.path.join(REPO_ROOT, fname)
        if not os.path.exists(path):
            print(f"  FAIL {fname}: file not found at {path}")
            all_ok = False
            continue
        scripts = extract_scripts(open(path, encoding="utf-8").read())
        if not scripts:
            print(f"  FAIL {fname}: found no <script> blocks at all -- extraction is broken")
            all_ok = False
        for i, code in enumerate(scripts):
            if not check_js(f"{fname} script #{i+1}", code):
                all_ok = False
    sys.exit(0 if all_ok else 1)


if __name__ == "__main__":
    main()
